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
	// pseudoRetryBudget 是同一账号在同一逻辑请求内被判伪完成后的同号重试次数。
	// 伪完成常是上游对该账号的窗口性限流（2351 实证：同号约 30s 后恢复 18696
	// tokens），立即 noteForcedAccountFailure（打入 1/3/10 分钟冷却）+ 换号在池中
	// 只剩该健康账号时会直接 pool exhausted。同号重试一次让窗口性限流有机会自愈，
	// 仍伪才核算 + 换号；不消耗跨账号 Budget，仅受本预算约束。
	pseudoRetryBudget = 1
)

// isPseudoCompletion reports whether a done-terminated stream carried far less
// output than a long-input task warrants — the signature of an account silently
// throttled/flagged by the upstream. inputChars is the estimated prompt length;
// a short prompt with a short answer is NOT treated as a pseudo completion.
// Reasoning counts as healthy output: a thinking model that reasons at length
// then answers tersely (long reasoning + short content) is healthy, and a
// reasoning-only stream is a legitimate thinking trace, not a throttle. Only a
// stream with near-nothing on BOTH axes (short content AND short/no reasoning)
// is flagged.
func isPseudoCompletion(chunks []pluginapi.ExecutorStreamChunk, inputChars int) bool {
	contentChars, reasoningChars := 0, 0
	for _, c := range chunks {
		var ch struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(c.Payload, &ch); err != nil {
			continue
		}
		for _, c2 := range ch.Choices {
			contentChars += len(c2.Delta.Content)
			reasoningChars += len(c2.Delta.Reasoning)
		}
	}
	// Healthy axes: content ≥ threshold, or reasoning ≥ threshold (thinking model
	// that reasoned fully), or pure reasoning trace (existing terminal contract:
	// reasoning-only is never pseudo, regardless of length).
	if contentChars >= pseudoCompletionMaxChars || reasoningChars >= pseudoCompletionMaxChars {
		return false
	}
	if contentChars <= 0 && reasoningChars > 0 {
		return false
	}
	if contentChars+reasoningChars <= 0 {
		return false
	}
	return inputChars >= pseudoCompletionMinInputChars
}

// traeStreamAttemptResult 保存单次上游流尝试的转换结果；逻辑请求协调器据此决定换号或唯一收尾。
type traeStreamAttemptResult struct {
	RequestID   string
	Chunks      []pluginapi.ExecutorStreamChunk
	Termination traeStreamTermination
	Pseudo      bool
	Emitted     bool
	Err         error
}

// traeStreamEmitter 下发一个已转换的客户端分片。
type traeStreamEmitter func(payload []byte) error

// pumpTraeStreamAttempt 读取单次 Trae SSE；长输入达到健康门槛前缓存全部分片，伪完成时不向客户端泄漏。
// [参数] r: 单次上游 SSE；ctx: 流上下文；requestID: 逻辑请求固定 ID；emit: 分片下发函数。
// [返回] 单次尝试的终结类别、分片、伪完成和下发状态；本函数不发送 finish，也不关闭宿主流。
// 最近修改时间：2026-09-01 23:30:00；改动原因：伪完成必须在首次下发前识别，才能在同一请求内无痕换号。
func pumpTraeStreamAttempt(r io.Reader, ctx traeStreamPumpContext, requestID string, emit traeStreamEmitter) traeStreamAttemptResult {
	result := traeStreamAttemptResult{RequestID: requestID}
	gateOpen := ctx.InputChars < pseudoCompletionMinInputChars
	contentChars := 0
	pending := make([][]byte, 0)
	var terminal traeSSETerminal

	// 1. 转换每个 output；长输入在正文达到 600 字节前只缓存，不向客户端承诺当前账号。
	scanErr := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			text, reasoning, ok := terminal.recordOutput(ev.Data)
			if !ok {
				return nil
			}
			raw, err := chunkDelta(requestID, ctx.Model, text, reasoning, "")
			if err != nil || raw == nil {
				return err
			}
			result.Chunks = append(result.Chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
			contentChars += len(text)
			if !gateOpen {
				pending = append(pending, raw)
				if contentChars < pseudoCompletionMaxChars {
					return nil
				}
				gateOpen = true
				for _, buffered := range pending {
					if err := emit(buffered); err != nil {
						return err
					}
					result.Emitted = true
				}
				pending = nil
				return nil
			}
			if err := emit(raw); err != nil {
				return err
			}
			result.Emitted = true
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
	if scanErr != nil {
		result.Err = scanErr
		return result
	}

	// 2. 先分类并识别伪完成；命中时丢弃 pending，禁止下发正文、reasoning 和终止分片。
	result.Termination, result.Err = terminal.classify(ctx.StatusCode)
	if result.Err != nil {
		return result
	}
	if result.Termination == terminationDone && isPseudoCompletion(result.Chunks, ctx.InputChars) {
		result.Pseudo = true
		return result
	}

	// 3. reasoning-only 或 output_eof 不属于伪完成，按既有兼容语义释放尚未承诺的缓冲。
	for _, buffered := range pending {
		if err := emit(buffered); err != nil {
			result.Err = err
			return result
		}
		result.Emitted = true
	}
	return result
}

// pumpTraeStream 保留单次异步流入口；账号级协调与唯一终结由 executor 统一接管。
func pumpTraeStream(r io.Reader, ctx traeStreamPumpContext) {
	requestID := randomUUID()
	started := time.Now()
	result := pumpTraeStreamAttempt(r, ctx, requestID, func(payload []byte) error {
		return streamEmit(ctx.StreamID, payload)
	})
	if result.Err != nil {
		log.Printf("[traework] stream pump error: request_id=%s stream_id=%s model=%s status=%d err=%s elapsed_ms=%d",
			requestID, ctx.StreamID, ctx.Model, ctx.StatusCode, truncateRedacted(result.Err.Error(), 200), time.Since(started).Milliseconds())
		reconcileAfterExecutorError(ctx.AuthID, ctx.StatusCode, result.Err.Error())
		streamEmitError(ctx.StreamID, result.Err.Error())
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), true, ctx.StatusCode, result.Err.Error())
		return
	}
	if result.Pseudo {
		reason := "pseudo completion: upstream returned done with near-empty output"
		log.Printf("[traework] stream pump pseudo-done: request_id=%s stream_id=%s model=%s status=%d chunks=%d elapsed_ms=%d",
			requestID, ctx.StreamID, ctx.Model, ctx.StatusCode, len(result.Chunks), time.Since(started).Milliseconds())
		noteForcedAccountFailure(ctx.AuthID, reason)
		evictSessionBindingsForAuth(ctx.AuthID)
		streamEmitError(ctx.StreamID, reason)
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), true, ctx.StatusCode, reason)
		return
	}

	finish := "stop"
	failed := false
	failureReason := ""
	if result.Termination == terminationOutputEOF {
		finish = "length"
		failed = true
		failureReason = "truncated: upstream stream ended without done"
	}
	raw, err := chunkDelta(requestID, ctx.Model, "", "", finish)
	if err == nil && raw != nil {
		err = streamEmit(ctx.StreamID, raw)
		if err == nil {
			result.Chunks = append(result.Chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
		}
	}
	if err != nil {
		reconcileAfterExecutorError(ctx.AuthID, ctx.StatusCode, err.Error())
		streamEmitError(ctx.StreamID, err.Error())
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), true, ctx.StatusCode, err.Error())
		return
	}
	streamClose(ctx.StreamID)
	if failed {
		publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), true, ctx.StatusCode, failureReason)
		return
	}
	resetAccountFailover(ctx.AuthID)
	publishUsage(ctx.Model, ctx.UpstreamModel, ctx.AuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), false, 0, "")
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
