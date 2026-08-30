// stream.go implements the Trae SSE → OpenAI chat-completion conversion.
// The upstream llm_utils_chat emits event:output / event:done / event:error
// SSE frames; these are folded into chat.completion.chunk deltas (streaming)
// or a chat.completion aggregate (non-streaming), mirroring the verified
// prototype's renderOpenAI* paths.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// streamEmit pushes one chunk to the host's stream channel for streamID.
func streamEmit(streamID string, payload []byte) error {
	body, _ := json.Marshal(map[string]any{
		"stream_id": streamID,
		"payload":   payload,
	})
	_, err := hostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

// streamEmitError emits a final error chunk and closes the stream.
func streamEmitError(streamID, message string) {
	payload, _ := json.Marshal(map[string]any{
		"error": message,
	})
	_ = streamEmit(streamID, payload)
	streamClose(streamID)
}

// streamClose terminates a host stream channel.
func streamClose(streamID string) {
	body, _ := json.Marshal(map[string]string{"stream_id": streamID})
	_, _ = hostCall(pluginabi.MethodHostStreamClose, body)
}

// streamHeaders is the client-facing response header set for streaming.
func streamHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	return h
}

// chunkDelta renders one chat.completion.chunk for an output fragment.
func chunkDelta(requestID, model string, text, reasoning string, finishReason string) ([]byte, error) {
	delta := map[string]any{}
	if text != "" {
		delta["content"] = text
	}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}
	if len(delta) == 0 && finishReason == "" {
		return nil, nil
	}
	chunk := map[string]any{
		"id":      "chatcmpl-" + requestID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": delta,
			},
		},
	}
	if finishReason != "" {
		chunk["choices"] = []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": finishReason,
			},
		}
	}
	return json.Marshal(chunk)
}

// completionAggregate renders the final chat.completion for non-streaming
// requests from the accumulated text/reasoning.
func completionAggregate(requestID, model, text, reasoning string) ([]byte, error) {
	msg := map[string]any{"role": "assistant", "content": text}
	if reasoning != "" {
		msg["reasoning_content"] = reasoning
	}
	out := map[string]any{
		"id":      "chatcmpl-" + requestID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": "stop",
			},
		},
	}
	return json.Marshal(out)
}

// collectTraeStream reads the upstream SSE stream, converts every output
// event into a chat.completion.chunk, and returns the chunks. On an
// event:error it surfaces the upstream message via fmt.Errorf with the
// canonical "upstream N:" prefix when status is known.
func collectTraeStream(r io.Reader, model string, statusCode int) ([]pluginapi.ExecutorStreamChunk, error) {
	requestID := randomUUID()
	var chunks []pluginapi.ExecutorStreamChunk
	err := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			text, reasoning, _ := normalizeOutput(ev.Data)
			if raw, err := chunkDelta(requestID, model, text, reasoning, ""); err == nil && raw != nil {
				chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
			}
		case "done":
			raw, _ := chunkDelta(requestID, model, "", "", "stop")
			if raw != nil {
				chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
			}
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	raw, _ := chunkDelta(requestID, model, "", "", "stop")
	if raw != nil {
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
	}
	return chunks, nil
}

// aggregateTraeCompletion reads the upstream SSE stream and folds all output
// events into one chat.completion aggregate (non-streaming path).
func aggregateTraeCompletion(r io.Reader, model string, statusCode int) ([]byte, error) {
	requestID := randomUUID()
	var text, reasoning strings.Builder
	err := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			t, rz, _ := normalizeOutput(ev.Data)
			text.WriteString(t)
			reasoning.WriteString(rz)
		case "done":
			return nil
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return completionAggregate(requestID, model, text.String(), reasoning.String())
}

// traeStreamPumpContext 保存异步流下发与用量发布共享的请求上下文。
type traeStreamPumpContext struct {
	StreamID      string    // 宿主流标识。
	Model         string    // 客户端请求模型。
	UpstreamModel string    // Trae 上游实际模型。
	StatusCode    int       // 上游 HTTP 状态码。
	AuthID        string    // 调度与故障核算使用的账号标识。
	AuthUID       string    // 用量维度使用的 Trae 账号 UID。
	Started       time.Time // 请求开始时间。
}

// pumpTraeStream 读取 Trae 上游 SSE，向宿主推送分片，并在流结束时发布一次用量结果。
// [参数] r: 上游 SSE；ctx: 异步流下发、账号核算与用量发布上下文。
// [返回] 无；分片与错误通过宿主流通道发送，用量通过共享发布路径异步落地。
// 最近修改时间：2026-08-30 17:37:24；改动原因：补齐异步流成功与失败终点的用量发布，避免 WorkBuddy 流式对话漏记。
func pumpTraeStream(r io.Reader, ctx traeStreamPumpContext) {
	// 1. 转换并推送上游分片，同时保留已成功生成的标准分片用于估算输出 token。
	requestID := randomUUID()
	var chunks []pluginapi.ExecutorStreamChunk
	scanErr := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			text, reasoning, _ := normalizeOutput(ev.Data)
			raw, err := chunkDelta(requestID, ctx.Model, text, reasoning, "")
			if err != nil {
				return err
			}
			if raw != nil {
				if emitErr := streamEmit(ctx.StreamID, raw); emitErr != nil {
					return emitErr
				}
				chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
			}
		case "done":
			return nil
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", ctx.StatusCode, truncateRedacted(msg, 200))
		}
		return nil
	})
	// 2. 扫描或下发失败时关闭流并发布失败用量；已输出分片仍纳入统计。
	if scanErr != nil {
		reconcileAfterExecutorError(ctx.AuthID, ctx.StatusCode, scanErr.Error())
		streamEmitError(ctx.StreamID, scanErr.Error())
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(chunks), true, ctx.StatusCode, scanErr.Error())
		return
	}

	// 3. 正常结束时下发 stop 分片、关闭宿主流，并发布成功用量。
	raw, _ := chunkDelta(requestID, ctx.Model, "", "", "stop")
	if raw != nil {
		_ = streamEmit(ctx.StreamID, raw)
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
	}
	streamClose(ctx.StreamID)
	resetAccountFailover(ctx.AuthID)
	publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(chunks), false, 0, "")
}

// clientNeedsSSEFrame reports whether the client expects raw SSE framing in
// the chunks (older clients) vs plain JSON payloads (current convention).
func clientNeedsSSEFrame(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	if v, ok := metadata["sse_framed"].(bool); ok {
		return v
	}
	return false
}

// stripProviderPrefix removes a leading "<provider>/" segment from a
// CPA-facing model name, leaving the bare alias/key.
func stripProviderPrefix(model string) string {
	if i := strings.Index(model, "/"); i > 0 {
		return model[i+1:]
	}
	return model
}

// openAIRequest mirrors the chat-completions request body the host forwards.
type openAIRequest struct {
	Model       string           `json:"model"`
	Messages    []map[string]any `json:"messages"`
	Stream      bool             `json:"stream"`
	MaxTokens   int              `json:"max_tokens"`
	Temperature *float64         `json:"temperature"`
	TopP        *float64         `json:"top_p"`
}

// toTraeMessages normalizes OpenAI messages into the Trae messages shape.
// The upstream LLMRawMessage expects messages[].content as a content-parts
// array ([{"type":"text","text":...}]) — a plain string fails with 4001
// "cannot unmarshal string into ... []*LLMRawMessageContent". Multi-part
// content arrays are mapped 1:1 onto text parts.
func toTraeMessages(msgs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		role, _ := m["role"].(string)
		switch c := m["content"].(type) {
		case string:
			out = append(out, map[string]any{"role": role, "content": textParts(c)})
		case []any:
			var parts []map[string]any
			for _, item := range c {
				if part, ok := item.(map[string]any); ok {
					if txt, ok := part["text"].(string); ok && txt != "" {
						parts = append(parts, map[string]any{"type": "text", "text": txt})
					}
				}
			}
			if len(parts) > 0 {
				out = append(out, map[string]any{"role": role, "content": parts})
			}
		default:
			// Skip malformed messages rather than failing the whole request.
		}
	}
	return out
}

// textParts wraps plain text into the single-part content array the upstream
// LLMRawMessage contract requires.
func textParts(s string) []map[string]any {
	return []map[string]any{{"type": "text", "text": s}}
}
