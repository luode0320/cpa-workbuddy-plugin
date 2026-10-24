// body.go constructs the Qoder AI agent_chat_generation request body from
// OpenAI-style chat completion inputs.
//
// The base template lives in baseprompt.json (embedded). Per-request we
// overwrite request/session ids, timestamps, model key, and the user prompt.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

//go:embed baseprompt.json
var basepromptJSON []byte

// cpaToUpstreamKey maps CPA-facing model names to upstream keys.
// Unknown names pass through unchanged (server silently routes to auto).
//
// 查找顺序：运行时映射（动态发现登记的上游官方名）优先，其次是下面的静态
// 别名表。运行时映射必须优先——上游改名后内部代号不变，静态表只会给出旧名，
// 新名未命中则透传，最终由上游静默落到 auto，参数与计费全错且无报错。
// [参数] cpaModel: 客户端请求的模型名（可带 provider/ 前缀由调用方剥离）
// [返回] 上游内部代号；全部未命中时原样返回入参
// 最近修改时间 2026-10-25（接入上游官方名运行时映射，承接上游模型改名）
func cpaToUpstreamKey(cpaModel string) string {
	trimmed := strings.TrimSpace(cpaModel)
	if key, ok := lookupUpstreamKey(trimmed); ok {
		return key
	}
	switch strings.ToLower(trimmed) {
	case "qoder-auto", "auto":
		return "auto"
	// Cantus
	case "cantus", "cmodel":
		return "cmodel"
	// 国际版套餐档位代号
	case "ultimate":
		return "ultimate"
	case "performance":
		return "performance"
	case "efficient":
		return "efficient"
	case "fast":
		return "fast"
	case "sonus", "smodel":
		return "smodel"
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
// ToolCalls / ToolCallID / Name 承载工具调用与工具结果，供重建上游 messages 时保持配对。
type openAIMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCalls  []any  `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
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
// 最近修改时间 2026-10-15；改动原因：同步解析 tool_calls / tool_call_id / name，避免重建 messages 时丢失工具配对结构
func (m *openAIMessage) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []any           `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
		Name       string          `json:"name"`
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
	m.ToolCalls = wire.ToolCalls
	m.ToolCallID = wire.ToolCallID
	m.Name = wire.Name
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

// alignToolsWithClient 按客户端真实声明对齐上游 body 的 tools 字段。
// 模板 baseprompt.json 硬编码了 Qoder CLI 的 14 个 agent 工具（Bash/Glob/Read…）
// 与 agent 会话上下文，而客户端普通问答并不具备这些工具的执行回路；若不剥离，
// 上游会把每次问答都当 agent 任务作答：先输出一句编排语，再发出工具调用并以
// finish_reason=tool_calls 结束本轮，下游表现为「推理一小段就停、等用户继续」。
// 因此：客户端未声明工具时剥离模板工具集，声明了则用客户端工具覆盖。
// tool_choice 由 normalizeToolChoice 归一后写入：上游只认字符串形态，客户端
// 常见的对象形（{"type":"function",...}）与 "none" 会被静默忽略——实测
// tool_choice:"none" 时上游照常发出工具调用，与「禁用工具」意图相反。
// [参数] base: 模板解析后的 body 根对象（就地修改）；req: 客户端请求。
// [返回] 无。
// 最近修改时间 2026-10-14；改动原因：补齐 tool_choice 形态规整，修复 "none" 被上游忽略致工具抑制失效
func alignToolsWithClient(base map[string]any, req *openAIRequest) {
	if req == nil || len(req.Tools) == 0 {
		delete(base, "tools")
		delete(base, "tool_choice")
		return
	}
	base["tools"] = req.Tools
	choice, suppress := normalizeToolChoice(req.ToolChoice)
	if suppress {
		// tool_choice=none：上游不认该值，唯一可靠抑制是整组删除 tools。
		delete(base, "tools")
		delete(base, "tool_choice")
		return
	}
	if choice != "" {
		base["tool_choice"] = choice
	} else {
		delete(base, "tool_choice")
	}
}

// normalizeToolChoice 把 OpenAI 各形态的 tool_choice 归一为上游可识别的字符串。
// 覆盖四种形态：字符串（auto/none/required）、对象形
// {"type":"auto|none|required|function", ...}、函数指定形（提取 name）、
// 以及未知/非法值（视为缺省）。语义与 workbuddy 的 normalizeToolsInPlace 一致。
// [参数] tc: 客户端 tool_choice 原值（any，可能为 nil/字符串/对象）。
// [返回] choice: 归一后的字符串（"auto"/"required"/函数名；空表示无显式选择）；
// suppress: 是否要求禁用工具（tool_choice=none）。
// 最近修改时间 2026-10-14；改动原因：新增 tool_choice 形态规整（对齐 workbuddy）
func normalizeToolChoice(tc any) (choice string, suppress bool) {
	switch v := tc.(type) {
	case nil:
		return "", false
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		switch s {
		case "none":
			return "", true
		case "auto", "required":
			return s, false
		case "":
			return "", false
		default:
			// 非标准字符串（含具体函数名）原样透传，上游按函数名处理。
			return strings.TrimSpace(v), false
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		typ = strings.ToLower(strings.TrimSpace(typ))
		switch typ {
		case "none":
			return "", true
		case "auto", "required":
			return typ, false
		case "function":
			name := ""
			if fn, ok := v["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				name, _ = v["name"].(string)
			}
			name = strings.TrimSpace(name)
			if name != "" {
				return name, false
			}
			return "auto", false
		default:
			return "", false
		}
	default:
		return "", false
	}
}

// openAIRequest is the CPA-facing chat completion request.
type openAIRequest struct {
	Model           string          `json:"model"`
	Messages        []openAIMessage `json:"messages"`
	Stream          bool            `json:"stream"`
	MaxTokens       int64           `json:"max_tokens"`
	ReasoningEffort string          `json:"reasoning_effort"`
	// Tools / ToolChoice 承载客户端的工具声明。模板 baseprompt.json 硬编码了
	// Qoder CLI 的 14 个 agent 工具，必须按客户端真实声明对齐，否则普通问答
	// 会被上游当作 agent 任务作答并以 finish_reason=tool_calls 提前结束。
	Tools      []any `json:"tools"`
	ToolChoice any   `json:"tool_choice"`
}

// extractLatestUserPrompt returns the content of the last user message,
// with safe fallback to the latest non-empty message or "continue" if none found.
func extractLatestUserPrompt(messages []openAIMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && strings.TrimSpace(messages[i].Content) != "" {
			return messages[i].Content
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Content) != "" {
			return messages[i].Content
		}
	}
	return "continue"
}

// buildQoderBody renders the upstream agent_chat_generation body for one request.
// modelKey is the upstream key (already mapped via cpaToUpstreamKey).
// [参数] req: 客户端 OpenAI 请求；modelKey: 上游模型键；userType: 阿里云用户类型。
// [返回] []byte: 渲染后的上游请求体；error: 模板解析或渲染异常。
// 最近修改时间 2026-10-15；改动原因：重建 messages 时保留 tool_calls / tool_call_id / name 配对结构
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

	// parameters: 按模型官方规格自适应计算放宽 max_tokens，避免长思维链被截断
	if params, ok := base["parameters"].(map[string]any); ok {
		mLower := strings.ToLower(modelKey)
		isReasoning := strings.Contains(mLower, "reasoner") || strings.Contains(mLower, "thinking") || (req != nil && req.ReasoningEffort != "" && req.ReasoningEffort != "none")
		val := int64(0)
		if req != nil && req.MaxTokens > 0 {
			val = int64(req.MaxTokens)
		}
		params["max_tokens"] = computeAdaptiveMaxTokens(modelKey, val, isReasoning)
	}

	// tools: 把工具声明权归还请求方，消除模板硬编码工具集导致的 agent 劫持。
	alignToolsWithClient(base, req)

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
		msg := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			msg["tool_calls"] = m.ToolCalls
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		if m.Name != "" {
			msg["name"] = m.Name
		}
		systemMsgs = append(systemMsgs, msg)
	}
	base["messages"] = systemMsgs

	// business
	if biz, ok := base["business"].(map[string]any); ok {
		biz["id"] = uuid.NewString()
		biz["begin_at"] = time.Now().UnixMilli()
		runes := []rune(prompt)
		if len(runes) > 30 {
			biz["name"] = string(runes[:30])
		} else {
			biz["name"] = prompt
		}
	}

	return json.Marshal(base)
}
