// body.go constructs the QoderWork agent_chat_generation request body from
// OpenAI-style chat completion inputs.
//
// The base template lives in baseprompt.json (embedded). Per-request we
// overwrite request/session ids, timestamps, model key, and the user prompt.
package main

import (
	"strings"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

//go:embed baseprompt.json
var basepromptJSON []byte

// cpaToUpstreamKey maps CPA-facing model names to upstream keys.
// Unknown names pass through unchanged (server silently routes to auto).
func cpaToUpstreamKey(cpaModel string) string {
	switch strings.ToLower(strings.TrimSpace(cpaModel)) {
	case "qoder-auto", "auto":
		return "auto"
	// Cantus
	case "cantus", "cmodel":
		return "cmodel"
	// Qwen
	case "qwen3.8-max", "qwen3.8-max-preview", "qmodel_38max", "qmodel_preview":
		return "qmodel_38max"
	case "qwen3.8-flash", "qwen3.6-flash", "qfmodel", "q36fmodel":
		return "qfmodel"
	case "qwen3.7-max", "qmodel_latest":
		return "qmodel_latest"
	case "qwen3.7-plus", "qmodel":
		return "qmodel"
	// GLM
	case "glm-5.3", "gmodel", "glm-5.2", "gm51model":
		return "gmodel"
	case "glm-5.3-flash", "gfmodel":
		return "gfmodel"
	// Kimi
	case "kimi-k3", "kmodel_latest":
		return "kmodel_latest"
	case "kimi-k2.8-preview", "kimi-k2.7-code", "kmodel":
		return "kmodel"
	// DeepSeek
	case "deepseek-v4-pro", "dmodel":
		return "dmodel"
	case "deepseek-flash", "deepseek-v4-flash", "dfmodel":
		return "dfmodel"
	// MiniMax
	case "minimax-m3", "mmodel", "minimax-m2.7":
		return "mmodel"
	}
	return cpaModel
}

// openAIMessage is one message in the OpenAI chat completion format.
// Content 归一为字符串：纯文本请求原样接收，多模态部件数组由
// UnmarshalJSON 提取文本后拼接，使下游 buildQoderBody 无需感知差异。
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIContentPart 是 OpenAI 多模态 content 数组中的一个部件。
type openAIContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// UnmarshalJSON 兼容 OpenAI 的两种 content 形态：纯字符串与部件数组。
// 上游 agent_chat_generation 的 messages 只接受文本 content，因此数组形态
// 仅提取 text / input_text 部件的文本（图片等非文本部件无对应位置，按空处理）。
// [参数] raw: 单条消息的原始 JSON。
// [返回] 解析成功返回 nil；content 为非法形态时返回错误。
// 最近修改时间 2026-10-10；改动原因：修复多模态 content 数组导致 payload parse 503
func (m *openAIMessage) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	text, err := decodeOpenAIContent(wire.Content)
	if err != nil {
		return err
	}
	m.Role = wire.Role
	m.Content = text
	return nil
}

// decodeOpenAIContent 把 OpenAI content 字段归一为文本。
// [参数] raw: content 原始 JSON（字符串、部件数组，或 null / 缺省）。
// [返回] 归一后的文本；既非字符串也非部件数组时返回错误。
func decodeOpenAIContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	// 1. 纯字符串形态（旧客户端与探活构造）优先
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	// 2. 多模态部件数组形态：只保留文本部件
	var parts []openAIContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("content must be a string or content parts array")
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" || part.Type == "input_text" {
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
	}
	return strings.Join(texts, "\n"), nil
}

// openAIRequest is the CPA-facing chat completion request.
type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

// extractLatestUserPrompt returns the content of the last user message.
func extractLatestUserPrompt(messages []openAIMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

// buildQoderBody renders the upstream agent_chat_generation body for one request.
// modelKey is the upstream key (already mapped via cpaToUpstreamKey).
func buildQoderBody(req *openAIRequest, modelKey, userType string) ([]byte, error) {
	var base map[string]any
	if err := json.Unmarshal(basepromptJSON, &base); err != nil {
		return nil, fmt.Errorf("baseprompt decode: %w", err)
	}

	prompt := extractLatestUserPrompt(req.Messages)
	if prompt == "" {
		return nil, fmt.Errorf("no user message in request")
	}

	nid := uuid.NewString()
	base["request_id"] = nid
	base["chat_record_id"] = nid
	base["request_set_id"] = uuid.NewString()
	base["session_id"] = uuid.NewString()
	base["stream"] = true
	base["aliyun_user_type"] = userType
	base["agent_id"] = "agent_common"

	// model_config
	if mc, ok := base["model_config"].(map[string]any); ok {
		mc["key"] = modelKey
	}

	// chat_context.text.text + chat_context.extra.originalContent.text
	if cc, ok := base["chat_context"].(map[string]any); ok {
		if txt, ok := cc["text"].(map[string]any); ok {
			txt["text"] = prompt
		}
		if extra, ok := cc["extra"].(map[string]any); ok {
			if oc, ok := extra["originalContent"].(map[string]any); ok {
				oc["text"] = prompt
			}
			if mc, ok := extra["modelConfig"].(map[string]any); ok {
				mc["key"] = modelKey
			}
		}
	}

	// messages: keep system prompt from baseprompt (template has it), replace user/assistant
	var systemMsgs []any
	if msgs, ok := base["messages"].([]any); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				if role, _ := mm["role"].(string); role == "system" {
					systemMsgs = append(systemMsgs, m)
				}
			}
		}
	}
	// Append the actual conversation
	for _, m := range req.Messages {
		systemMsgs = append(systemMsgs, map[string]any{
			"role":    m.Role,
			"content": m.Content,
		})
	}
	base["messages"] = systemMsgs

	// business
	if biz, ok := base["business"].(map[string]any); ok {
		biz["id"] = uuid.NewString()
		biz["begin_at"] = time.Now().UnixMilli()
		if len(prompt) > 30 {
			biz["name"] = prompt[:30]
		} else {
			biz["name"] = prompt
		}
	}

	return json.Marshal(base)
}
