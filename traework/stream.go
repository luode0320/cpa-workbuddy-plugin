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
	"log"
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
func completionAggregate(requestID, model, text, reasoning, finishReason string) ([]byte, error) {
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
				"finish_reason": finishReason,
			},
		},
	}
	return json.Marshal(out)
}

// traeSSETerminal 记录本次 SSE 是否以可验证的业务事件结束。
type traeSSETerminal struct {
	hasOutput bool
	hasDone   bool
}

// recordOutput 解析输出事件，并只在可转换为客户端内容时将其标记为有效输出。
// [参数] data: output 事件的原始 JSON 数据。
// [返回] text: 正文片段；reasoning: 推理片段；ok: 是否为有效输出。
// 最近修改时间：2026-08-30 21:01:25；改动原因：统一三条响应路径的有效输出判定，阻止异常正文被当作空成功。
func (t *traeSSETerminal) recordOutput(data string) (text, reasoning string, ok bool) {
	text, reasoning, _ = normalizeOutput(data)
	if text == "" && reasoning == "" {
		return "", "", false
	}
	t.hasOutput = true
	return text, reasoning, true
}

// recordDone 标记上游已明确发送完成事件。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-08-30 21:01:25；改动原因：统一三条响应路径的有效终止判定。
func (t *traeSSETerminal) recordDone() {
	t.hasDone = true
}

// hasPayload 报告本次扫描是否已收到可交付的业务事件（正文输出或明确完成）。
// 供 scanSSE 在读取错误时判定：已有可交付内容的上游断流应按截断收尾，
// 而非把已生成内容连同错误一起丢弃。
// [参数] 无。
// [返回] 是否已收到正文输出或完成事件。
// 最近修改时间：2026-08-31 15:20:00；改动原因：读错误型断流（RST/unexpected EOF）需与干净 EOF 同款兜底。
func (t *traeSSETerminal) hasPayload() bool {
	return t.hasOutput || t.hasDone
}

// traeStreamTermination 描述一次 SSE 扫描的终结类别，供三条响应路径统一收尾。
// 区分「业务完整」「上游中途断流但有部分输出」「空响应」三类，避免把可兜底的部分输出误判为致命错误。
type traeStreamTermination int

const (
	terminationDone      traeStreamTermination = iota // 收到明确 done，业务完整。
	terminationOutputEOF                              // 有部分输出但 EOF 无 done，上游中途断流，可兜底收尾。
	terminationInvalid                                // 既无 output 也无 done，空响应。
)

// classify 依据扫描累积的 output/done 判定终结类别。
// [参数] statusCode: 上游 HTTP 状态码。
// [返回] traeStreamTermination: 终结类别；error: 空响应时的协议错误，其余类别为 nil。
// 最近修改时间：2026-08-31 02:10:00；改动原因：部分 output 后 EOF 是上游断流而非空成功，应兜底收尾而非报错中断。
func (t traeSSETerminal) classify(statusCode int) (traeStreamTermination, error) {
	if t.hasDone {
		return terminationDone, nil
	}
	if t.hasOutput {
		return terminationOutputEOF, nil
	}
	return terminationInvalid, fmt.Errorf("upstream %d: invalid SSE response: missing output and done event", statusCode)
}

// terminationLabel 返回终结类别的稳定短标签，供流路径日志区分业务完整 / 断流 / 空响应。
func terminationLabel(t traeStreamTermination) string {
	switch t {
	case terminationDone:
		return "done"
	case terminationOutputEOF:
		return "output_eof"
	case terminationInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

// collectTraeStream reads the upstream SSE stream, converts every output
// event into a chat.completion.chunk, and returns the chunks. On an
// event:error it surfaces the upstream message via fmt.Errorf with the
// canonical "upstream N:" prefix when status is known.
// [参数] r: 上游 SSE 响应；model: 客户端模型；statusCode: 上游 HTTP 状态码。
// [返回] chunks: 转换后的分片；error: 上游错误、缺少 done 或传输截断时的协议错误。
// 最近修改时间：2026-08-30 23:40:18；改动原因：业务成功必须收到 done，部分 output 后 EOF 不得补成正常 stop。
func collectTraeStream(r io.Reader, model string, statusCode int) ([]pluginapi.ExecutorStreamChunk, error) {
	requestID := randomUUID()
	started := time.Now()
	var chunks []pluginapi.ExecutorStreamChunk
	var terminal traeSSETerminal
	err := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			text, reasoning, ok := terminal.recordOutput(ev.Data)
			if !ok {
				return nil
			}
			if raw, err := chunkDelta(requestID, model, text, reasoning, ""); err == nil && raw != nil {
				chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
			}
		case "done":
			terminal.recordDone()
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		return nil
	}, terminal.hasPayload)
	if err != nil {
		log.Printf("[traework] stream collect error: request_id=%s model=%s status=%d err=%s elapsed_ms=%d", requestID, model, statusCode, truncateRedacted(err.Error(), 200), time.Since(started).Milliseconds())
		return nil, err
	}
	termination, err := terminal.classify(statusCode)
	if err != nil {
		log.Printf("[traework] stream collect invalid: request_id=%s model=%s status=%d err=%s elapsed_ms=%d", requestID, model, statusCode, truncateRedacted(err.Error(), 200), time.Since(started).Milliseconds())
		return nil, err
	}
	// 收到 done 正常收尾；部分 output 后 EOF（上游中途断流）补 length 收尾，
	// 让客户端保留已生成内容，而不是把可兜底的中断误判为致命错误。仅空响应才真正报错。
	finish := "stop"
	if termination == terminationOutputEOF {
		finish = "length"
	}
	raw, _ := chunkDelta(requestID, model, "", "", finish)
	if raw != nil {
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
	}
	log.Printf("[traework] stream collect done: request_id=%s model=%s status=%d termination=%s chunks=%d finish=%s elapsed_ms=%d",
		requestID, model, statusCode, terminationLabel(termination), len(chunks), finish, time.Since(started).Milliseconds())
	return chunks, nil
}

// aggregateTraeCompletion reads the upstream SSE stream and folds all output
// events into one chat.completion aggregate (non-streaming path).
// [参数] r: 上游 SSE 响应；model: 客户端模型；statusCode: 上游 HTTP 状态码。
// [返回] []byte: OpenAI 完成响应；error: 上游错误、缺少 done 或传输截断时的协议错误。
// 最近修改时间：2026-08-30 23:40:18；改动原因：聚合响应必须收到 done，避免把部分输出后的 EOF 误判为完整完成。
func aggregateTraeCompletion(r io.Reader, model string, statusCode int) ([]byte, error) {
	requestID := randomUUID()
	started := time.Now()
	var text, reasoning strings.Builder
	var terminal traeSSETerminal
	err := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			t, rz, ok := terminal.recordOutput(ev.Data)
			if ok {
				text.WriteString(t)
				reasoning.WriteString(rz)
			}
		case "done":
			terminal.recordDone()
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		return nil
	}, terminal.hasPayload)
	if err != nil {
		log.Printf("[traework] stream aggregate error: request_id=%s model=%s status=%d err=%s elapsed_ms=%d", requestID, model, statusCode, truncateRedacted(err.Error(), 200), time.Since(started).Milliseconds())
		return nil, err
	}
	termination, err := terminal.classify(statusCode)
	if err != nil {
		log.Printf("[traework] stream aggregate invalid: request_id=%s model=%s status=%d err=%s elapsed_ms=%d", requestID, model, statusCode, truncateRedacted(err.Error(), 200), time.Since(started).Milliseconds())
		return nil, err
	}
	finish := "stop"
	if termination == terminationOutputEOF {
		finish = "length"
	}
	log.Printf("[traework] stream aggregate done: request_id=%s model=%s status=%d termination=%s finish=%s chars=%d elapsed_ms=%d",
		requestID, model, statusCode, terminationLabel(termination), finish, text.Len()+reasoning.Len(), time.Since(started).Milliseconds())
	return completionAggregate(requestID, model, text.String(), reasoning.String(), finish)
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
	InputChars    int       // 请求输入估算字符数，用于伪完成检测的输入长度判据。
}

// pumpTraeStream 读取 Trae 上游 SSE，向宿主推送分片，并在流结束时发布一次用量结果。
// [参数] r: 上游 SSE；ctx: 异步流下发、账号核算与用量发布上下文。
// [返回] 无；分片与错误通过宿主流通道发送，用量通过共享发布路径异步落地。
// 最近修改时间：2026-08-30 23:40:18；改动原因：补齐 done 终止校验与最终 stop 下发失败核算，客户端未完整收尾时禁止记成功。

// pseudoCompletionMaxChars 是伪完成检测的最大输出字符数（等价于 150 token 估算，
// 即 150*4=600 字符）。pseudoCompletionMinInputChars 是判伪完成所需的最小输入
// 长度。双重判据：输出字符低于阈值 且 输入是长任务时，才判伪完成——覆盖生产
// 观察到的伪完成（15~129 token，包括用户长任务被掐断的 120 token），同时通过
// 输入长度保护正常短答（如「你好」≈5 token）不被误判。健康长输出 215+ token
// （860+ 字符）不受影响。旧实现用 120 字符阈值，漏掉 ~120 token（≈480 字符）
// 的伪完成。用字符数而非估算 token，避免短输出 chars/4 取整归零误判。
const (
	pseudoCompletionMaxChars      = 600
	pseudoCompletionMinInputChars = 200
)

// isPseudoCompletion reports whether a done-terminated stream carried far less
// output than a long-input task warrants — the signature of an account silently
// throttled/flagged by the upstream. inputChars is the estimated prompt length;
// a short prompt with a short answer is NOT treated as a pseudo completion.
// It sums only content (not reasoning) deltas; a reasoning-heavy model that
// answers tersely is not flagged either.
func isPseudoCompletion(chunks []pluginapi.ExecutorStreamChunk, inputChars int) bool {
	var chars int
	for _, c := range chunks {
		var ch struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(c.Payload, &ch); err != nil {
			continue
		}
		for _, c2 := range ch.Choices {
			chars += len(c2.Delta.Content)
		}
	}
	if chars <= 0 || chars >= pseudoCompletionMaxChars {
		return false
	}
	return inputChars >= pseudoCompletionMinInputChars
}

// pumpTraeStream reads the upstream SSE stream, pushes every output chunk to the
// host stream, and reconciles failover/usage on the terminal event.
func pumpTraeStream(r io.Reader, ctx traeStreamPumpContext) {
	// 1. 转换并推送上游分片，同时保留已成功生成的标准分片用于估算输出 token。
	requestID := randomUUID()
	started := time.Now()
	var chunks []pluginapi.ExecutorStreamChunk
	var terminal traeSSETerminal
	scanErr := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			text, reasoning, ok := terminal.recordOutput(ev.Data)
			if !ok {
				return nil
			}
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
			terminal.recordDone()
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", ctx.StatusCode, truncateRedacted(msg, 200))
		}
		return nil
	}, terminal.hasPayload)
	// scanErr: SSE 事件错误 / 分片下发失败；空响应（invalid）也归入致命失败。
	var termination traeStreamTermination
	if scanErr == nil {
		termination, scanErr = terminal.classify(ctx.StatusCode)
	}
	if scanErr != nil {
		// 致命失败：上游显式 event:error、下发失败或空响应。关闭流并发布失败用量，已输出分片仍纳入统计。
		log.Printf("[traework] stream pump error: request_id=%s stream_id=%s model=%s status=%d err=%s elapsed_ms=%d",
			requestID, ctx.StreamID, ctx.Model, ctx.StatusCode, truncateRedacted(scanErr.Error(), 200), time.Since(started).Milliseconds())
		reconcileAfterExecutorError(ctx.AuthID, ctx.StatusCode, scanErr.Error())
		streamEmitError(ctx.StreamID, scanErr.Error())
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(chunks), true, ctx.StatusCode, scanErr.Error())
		return
	}

	// 上游中途断流（部分 output 但 EOF 无 done）时补 length 收尾，保留已生成内容；
	// 不把可兜底的中断误判为账号失败，也不复位账号（未走正常 done 结束）。
	finish := "stop"
	incomplete := false
	if termination == terminationOutputEOF {
		finish = "length"
		incomplete = true
	}
	raw, _ := chunkDelta(requestID, ctx.Model, "", "", finish)
	if raw != nil {
		if emitErr := streamEmit(ctx.StreamID, raw); emitErr != nil {
			// 终止分片下发失败表示客户端没有收到完整终止信号，不能继续复位账号或记成功。
			log.Printf("[traework] stream pump finish emit error: request_id=%s stream_id=%s model=%s err=%s", requestID, ctx.StreamID, ctx.Model, truncateRedacted(emitErr.Error(), 200))
			reconcileAfterExecutorError(ctx.AuthID, ctx.StatusCode, emitErr.Error())
			streamEmitError(ctx.StreamID, emitErr.Error())
			publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(chunks), true, ctx.StatusCode, emitErr.Error())
			return
		}
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
	}
	streamClose(ctx.StreamID)
	if incomplete {
		// 断流收尾：不清零账号故障、不记成功用量；用「不完整」标记落一次用量供面板识别。
		log.Printf("[traework] stream pump truncated: request_id=%s stream_id=%s model=%s status=%d chunks=%d elapsed_ms=%d",
			requestID, ctx.StreamID, ctx.Model, ctx.StatusCode, len(chunks), time.Since(started).Milliseconds())
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(chunks), false, ctx.StatusCode, "truncated: upstream stream ended without done")
		return
	}
	log.Printf("[traework] stream pump done: request_id=%s stream_id=%s model=%s status=%d termination=%s chunks=%d elapsed_ms=%d",
		requestID, ctx.StreamID, ctx.Model, ctx.StatusCode, terminationLabel(termination), len(chunks), time.Since(started).Milliseconds())
	if isPseudoCompletion(chunks, ctx.InputChars) {
		// 伪完成：上游账号被静默限流/标记时返回「done + 极少正文」。不当作成功
		// 清零（否则账号永不换号），计一次账号失败并驱逐会话绑定，让下一次请求
		// 切到健康账号。已生成的少量内容仍正常下发给客户端。
		log.Printf("[traework] stream pump pseudo-done: request_id=%s stream_id=%s model=%s status=%d chunks=%d elapsed_ms=%d",
			requestID, ctx.StreamID, ctx.Model, ctx.StatusCode, len(chunks), time.Since(started).Milliseconds())
		noteForcedAccountFailure(ctx.AuthID, "pseudo completion: upstream returned done with near-empty output")
		evictSessionBindingsForAuth(ctx.AuthID)
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(chunks), false, ctx.StatusCode, "pseudo completion: upstream returned done with near-empty output")
		return
	}
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
