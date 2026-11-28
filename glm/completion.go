package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// completionMessage 是对话消息。
type completionMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// completionRequest 是 OpenAI 兼容请求体。
type completionRequest struct {
	Model       string              `json:"model"`
	Messages    []completionMessage `json:"messages"`
	Stream      bool                `json:"stream"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Temperature *float64            `json:"temperature,omitempty"`
}

// streamContent 是 GLM SSE 段落里的内容单元。
type streamContent struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Think string `json:"think"`
}

// streamPart 是 GLM SSE 帧里的段落。
type streamPart struct {
	LogicID string          `json:"logic_id"`
	Status  string          `json:"status"`
	Content []streamContent `json:"content"`
}

// glmStreamFrame 是 GLM SSE 单帧。
type glmStreamFrame struct {
	ConversationID string       `json:"conversation_id"`
	Status         string       `json:"status"`
	Parts          []streamPart `json:"parts"`
}

// openAIChunk 是发给宿主的 OpenAI 兼容流式块。
type openAIChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Object  string `json:"object"`
	Choices []struct {
		Index  int    `json:"index"`
		Delta  any    `json:"delta"`
		Finish string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Created int64 `json:"created"`
}

// buildStreamPayload 构造 GLM 上游流式请求体。
// [参数] req: OpenAI 兼容请求；cfg: 插件配置；sa: 账号凭据。
// [返回] 上游请求体字节、错误。
// 最近修改时间：2026-11-28 新增, 对话端点请求构造。
func buildStreamPayload(req completionRequest, cfg Config, sa *StoredAuth) ([]byte, error) {
	assistantID, chatMode, plusModel := resolveUpstreamTarget(req.Model)
	content := mergeMessages(req.Messages)
	meta := map[string]any{
		"channel":             "",
		"chat_mode":           chatMode,
		"draft_id":            "",
		"if_plus_model":       plusModel,
		"input_question_type": "xxxx",
		"is_networking":       false,
		"is_test":             false,
		"platform":            "pc",
		"quote_log_id":        "",
	}
	payload := map[string]any{
		"assistant_id":    assistantID,
		"conversation_id": "",
		"project_id":      "",
		"chat_type":       "user_chat",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": content}},
			},
		},
		"meta_data": meta,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal stream payload: %w", err)
	}
	return raw, nil
}

// mergeMessages 把多轮消息合并成单条文本，角色以尖括号标注。
// [参数] messages: 对话消息列表。
// [返回] 合并后的文本。
// 最近修改时间：2026-11-28 新增, 多轮对话合并策略。
func mergeMessages(messages []completionMessage) string {
	var b strings.Builder
	for _, m := range messages {
		text := messageText(m.Content)
		if text == "" {
			continue
		}
		role := strings.ToLower(m.Role)
		if role == "system" {
			role = "sytstem"
		}
		if role == "assistant" || role == "user" {
			b.WriteString("<|" + role + "|>\n")
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	b.WriteString("<|assistant|>\n")
	return b.String()
}

// messageText 提取消息 content 的文本部分。
// [参数] content: 消息内容（字符串或数组）。
// [返回] 文本内容。
// 最近修改时间：2026-11-28 新增, 多模态消息文本提取。
func messageText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if obj["type"] == "text" {
				if text, ok := obj["text"].(string); ok {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	}
	return ""
}

// prepareStreamRequest 构造带签名与伪装头的上游 HTTP 请求。
// X-Device-Id 必须取自 access_token 载荷，随机值会被上游判 40004。
// [参数] ctx: 上下文；raw: 请求体；cfg: 配置；accessToken: 调用令牌。
// [返回] HTTP 请求、错误。
// 最近修改时间：2026-11-28 修正 X-Device-Id 来源，对话端点同样校验设备一致性。
func prepareStreamRequest(ctx context.Context, raw []byte, cfg Config, accessToken string) (*http.Request, error) {
	deviceID := deviceIDFromToken(accessToken)
	if deviceID == "" {
		return nil, fmt.Errorf("access_token 缺少 device_id，请重新授权")
	}
	sign := generateSign(cfg.SignSecret, time.Now().UnixMilli(), newNonce())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.StreamEndpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("build stream request: %w", err)
	}
	h := refreshHeaders("", sign, deviceID)
	h.Set("Authorization", "Bearer "+accessToken)
	h.Set("Accept", "text/event-stream")
	req.Header = h
	return req, nil
}

// streamDelta 是一次增量输出。
type streamDelta struct {
	Content   string
	Reasoning string
}

// parseGLMFrame 从 GLM SSE 帧提取增量文本与思考，按全量 parts 快照重建。
// [参数] frame: GLM 帧；sentText: 已发送文本长度；sentReasoning: 已发送思考长度。
// [返回] 文本增量、思考增量、新文本长度、新思考长度。
// 最近修改时间：2026-11-28 新增, 全量 parts 快照增量去重。
func parseGLMFrame(frame glmStreamFrame, sentText, sentReasoning int) (string, string, int, int) {
	var fullText, fullReasoning strings.Builder
	for _, part := range frame.Parts {
		for _, c := range part.Content {
			switch c.Type {
			case "text":
				fullText.WriteString(c.Text)
			case "think":
				fullReasoning.WriteString(c.Think)
			}
		}
	}
	text := fullText.String()
	reasoning := fullReasoning.String()
	textDelta := ""
	reasoningDelta := ""
	if len(text) > sentText {
		textDelta = text[sentText:]
	}
	if len(reasoning) > sentReasoning {
		reasoningDelta = reasoning[sentReasoning:]
	}
	return textDelta, reasoningDelta, len(text), len(reasoning)
}

// chunkBytes 生成一个 OpenAI 兼容 chunk 的 SSE 行。
// [参数] id: 会话 ID；model: 模型名；delta: 增量内容；finish: 结束原因；created: 时间戳。
// [返回] SSE 行字节。
// 最近修改时间：2026-11-28 新增, OpenAI 流格式输出。
func chunkBytes(id, model string, delta any, finish string, created int64) []byte {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	chunk := map[string]any{
		"id":      id,
		"model":   model,
		"object":  "chat.completion.chunk",
		"choices": []any{choice},
		"created": created,
	}
	raw, _ := json.Marshal(chunk)
	return []byte("data: " + string(raw) + "\n\n")
}

// pumpGLMStream 后台读取 GLM SSE 并转换成 OpenAI chunk 推给宿主。
// [参数] ctx: 上下文；cancel: 上下文取消函数；streamID: 宿主流 ID；resp: 上游响应；model: 模型名；authID: 账号标识；accountLabel: 账号展示名；refreshToken: 账号刷新令牌；startTime: 起始时间。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, GLM SSE 到 OpenAI 流的转换。
func pumpGLMStream(ctx context.Context, cancel context.CancelFunc, streamID string, resp *http.Response, model, authID, accountLabel, refreshToken string, startTime time.Time) {
	defer func() {
		cancel()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		streamCloseFn(streamID)
	}()

	reader := bufio.NewReader(resp.Body)
	sentText := 0
	sentReasoning := 0
	created := time.Now().Unix()
	convID := ""
	failed := false
	failMsg := ""
	defer func() {
		detail := usageTokensDetail{TotalTokens: 2}
		publishUsage(authID, accountLabel, model, startTime, detail, time.Since(startTime).Milliseconds(), resp.StatusCode, failed, failMsg)
	}()
	emitOK := func(payload []byte) bool {
		return streamEmitFn(streamID, payload) == nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		lineStr := strings.TrimSpace(string(line))
		if isStreamDoneLine(lineStr) || lineStr == "event: finish" {
			break
		}
		if strings.HasPrefix(lineStr, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(lineStr, "data:"))
			if msg := sseErrorFrame(payload); msg != "" {
				failed = true
				failMsg = msg
				streamEmitErrorFn(streamID, truncateRedacted(msg, 200))
				return
			}
			var frame glmStreamFrame
			if errJSON := json.Unmarshal([]byte(payload), &frame); errJSON == nil {
				if frame.ConversationID != "" {
					convID = frame.ConversationID
				}
				textDelta, reasoningDelta, newText, newReasoning := parseGLMFrame(frame, sentText, sentReasoning)
				sentText = newText
				sentReasoning = newReasoning
				if reasoningDelta != "" && !emitOK(chunkBytes(convID, model, map[string]any{"reasoning_content": reasoningDelta}, "", created)) {
					return
				}
				if textDelta != "" && !emitOK(chunkBytes(convID, model, map[string]any{"content": textDelta}, "", created)) {
					return
				}
				if frame.Status == "finish" {
					if !emitOK(chunkBytes(convID, model, map[string]any{}, "stop", created)) {
						return
					}
					_ = emitOK([]byte("data: [DONE]\n\n"))
					if convID != "" {
						go removeConversationAsync(convID, model, refreshToken, currentConfig())
					}
					return
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				failed = true
				failMsg = err.Error()
				streamEmitErrorFn(streamID, err.Error())
			}
			return
		}
	}
	_ = emitOK([]byte("data: [DONE]\n\n"))
}

// removeConversationAsync 流结束后删除上游会话记录。
// [参数] convID: 会话 ID；model: 模型名；refreshToken: 账号刷新令牌；cfg: 配置。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 清理上游会话痕迹。
func removeConversationAsync(convID, model, refreshToken string, cfg Config) {
	assistantID, _ := resolveAssistantID(model)
	accessToken, err := acquireToken(refreshToken, cfg)
	if err != nil || accessToken == "" {
		return
	}
	raw, _ := json.Marshal(map[string]any{"assistant_id": assistantID, "conversation_id": convID})
	req, err := http.NewRequest(http.MethodPost, conversationDeleteEndpoint, bytes.NewReader(raw))
	if err != nil {
		return
	}
	sign := generateSign(cfg.SignSecret, time.Now().UnixMilli(), newNonce())
	req.Header = refreshHeaders("", sign, newNonce())
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := tokenHTTPClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// streamHeaders 返回流式响应头。
// [参数] 无。
// [返回] HTTP 头。
// 最近修改时间：2026-11-28 新增, 流式响应头。
func streamHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	return h
}
