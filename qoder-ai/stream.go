// stream.go owns the upstream SSE data plane: emitting cleaned chunks back to
// the host stream (streamEmit/close), pumping the upstream SSE in a goroutine
// (pumpUpstreamStream), collecting it synchronously (collectUpstreamStream),
// and the SSE-frame helpers that re-frame, filter, and aggregate chunks.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// streamEmit pushes one chunk payload to the host stream. Returns an error if
// the host rejected it (e.g. the client already disconnected and the stream
// was closed), which the pump uses to stop reading a dead upstream.
func streamEmit(streamID string, payload []byte) error {
	if streamID == "" {
		return fmt.Errorf("no stream id")
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "payload": payload})
	_, err := hostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

// marshalStreamErrorEnvelope 构造带 "error" 字段的 stream.emit 信封（纯函数，便于回归测试）。
// 排队帧额外带上 http_status（见 envelopeHTTPStatusForError），供宿主识别为可跨
// provider 轮转的容量错误；宿主未读取该字段时会被忽略，行为与旧版一致。
// [参数] streamID：宿主流标识；message：终态错误文本。
// [返回] 序列化后的 emit 信封；序列化失败返回错误。
// 最近修改时间 2026-10-15；改动原因：排队帧补 http_status 供宿主跨 provider 轮转。
func marshalStreamErrorEnvelope(streamID, message string) ([]byte, error) {
	body := map[string]any{"stream_id": streamID, "error": message}
	if status := envelopeHTTPStatusForError(message); status > 0 {
		body["http_status"] = status
	}
	return json.Marshal(body)
}

// emitStreamErrorEnvelope 通过 host.stream.emit 信封的 "error" 字段发送终态错误。
// 宿主把该字段映射为执行器流 chunk 的 Err：核心 conductor 只有收到真正的错误
// chunk 才会判定执行失败，进而轮换下一个凭据——包括其他平台的账号。若把错误当
// payload 数据帧发出，宿主会视为正常流内容，请求以"成功"告终，跨平台失败切换
// 永远不会触发。
func emitStreamErrorEnvelope(streamID, message string) error {
	body, err := marshalStreamErrorEnvelope(streamID, message)
	if err != nil {
		return err
	}
	_, err = hostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

func streamEmitError(streamID, message string) {
	if streamID == "" {
		return
	}
	// A-37: never emit raw upstream bodies that may contain Bearer/JWT.
	_ = emitStreamErrorEnvelope(streamID, redactSecrets(message))
}

var streamCloseOnce sync.Map // streamID -> sync.Once

func streamClose(streamID string) {
	if streamID == "" {
		return
	}
	actual, _ := streamCloseOnce.LoadOrStore(streamID, &sync.Once{})
	once := actual.(*sync.Once)
	once.Do(func() {
		defer streamCloseOnce.Delete(streamID)
		body, _ := json.Marshal(map[string]any{"stream_id": streamID})
		_, _ = hostCall(pluginabi.MethodHostStreamClose, body)
	})
}

func streamHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	return h
}

// parseQoderSSEChunk parses one SSE line from upstream.
// It handles:
// 1. Direct [DONE] lines (with or without "data:" prefix and whitespace).
// 2. Nested Qoder envelope: {"body": "<json>"} or {"body": "[DONE]"}.
// 3. Flat OpenAI SSE chunk: {"id":..., "choices":[...]}.
// 4. Top-level business error frame: {"code":"403","message":"..."}.
// Returns the unwrapped inner OpenAI JSON string and whether this line indicates stream completion.
//
// 第 4 类（顶层 code 帧）此前不属于任何分支而被当作无关行丢弃：上游对排队/限流
// 请求在 HTTP 200 下返回该形态（2026-10-12 实测 qwen3.8 大上下文返回
// code=403 + 内嵌 10605/isQueued），丢弃后插件读不到终止信号，死等 Keep-Alive
// 连接约 60 秒才由对端复位。现按错误帧返回 payload，交 sseErrorFrame 提取语义。
//
// 最近修改时间 2026-10-12；改动原因：识别 HTTP 200 顶层 code 业务错误帧，消除 60s 挂死。
func parseQoderSSEChunk(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return "", false
	}
	if !strings.HasPrefix(trimmed, "data:") {
		return "", false
	}
	payload := stripDataPrefix(trimmed)
	if payload == "" {
		return "", false
	}
	if payload == "[DONE]" {
		return "", true
	}

	var outer map[string]any
	if err := json.Unmarshal([]byte(payload), &outer); err != nil {
		if strings.TrimSpace(payload) == "[DONE]" {
			return "", true
		}
		return "", false
	}

	// 1. Nested Qoder envelope: outer["body"]
	if bVal, ok := outer["body"]; ok && bVal != nil {
		if bodyStr, isStr := bVal.(string); isStr {
			cleanedBody := stripDataPrefix(bodyStr)
			if cleanedBody == "[DONE]" {
				return "", true
			}
			return cleanedBody, false
		}
	}

	// 2. Flat OpenAI SSE chunk (already unnested)
	if _, hasChoices := outer["choices"]; hasChoices {
		return payload, false
	}
	if _, hasID := outer["id"]; hasID {
		return payload, false
	}

	// 3. In-stream error frame in outer
	if _, hasErr := outer["error"]; hasErr {
		return payload, false
	}

	// 4. 顶层业务错误帧：{"code":"403","message":"..."}（上游排队/限流形态）。
	// 仅当存在非空 code 时才认；正常 chunk 由上面 1/2 分支先行返回，不会误伤。
	if code, hasCode := outer["code"].(string); hasCode && strings.TrimSpace(code) != "" {
		return payload, false
	}

	return "", false
}

// pumpUpstreamStream reads the upstream SSE response in the background and
// emits each cleaned chunk to the host stream. It closes the stream when done.
// An emit failure (client disconnected → host closed the stream) aborts the
// pump so we stop reading a dead upstream. cancel is invoked on every exit so
// the underlying http request context is released promptly.
//
// v0.7.0: requests now route via host.http.do_stream so request-log captures
// the outbound call and host transport policy applies. The host bridge emits
// arbitrary 32KB chunks, so we adapt to io.Reader and keep the bufio.Scanner
// SSE line framing unchanged.
func pumpUpstreamStream(httpReq *http.Request, cancel context.CancelFunc, streamID string, sseFramed bool, requestedModel, upstreamModel, authUID string, started time.Time, authID, reasoningEffort, accountLabel, sessionKey string) {
	// Always close the host stream exactly once on every exit path.
	closed := false
	closeOnce := func() {
		if closed {
			return
		}
		closed = true
		streamClose(streamID)
	}
	defer closeOnce()
	if cancel != nil {
		defer cancel()
	}

	// Per-request account-failover budget. 0 means no retry (kill switch
	// via `retry_on_4xx: 0` in plugin.yaml). Hot path is unchanged when
	// the budget is 0 — we still go through one attempt and propagate.
	budget := loadedRetryOn4xx()
	curReq := httpReq
	curAuthID := authID
	curAuthUID := authUID
	curAccountLabel := accountLabel

	// Snapshot the encoded body once: it is account-independent and every
	// retry re-signs the SAME body via applyCosyHeaders for the new
	// account. The stream path builds httpReq with strings.NewReader, so
	// GetBody is populated automatically. If we cannot recover the body
	// for some reason, account-switch retries are disabled (encodedBody
	// == "") but the first attempt still runs normally.
	encodedBody := ""
	if httpReq.GetBody != nil {
		if rc, berr := httpReq.GetBody(); berr == nil {
			if b, rerr := io.ReadAll(rc); rerr == nil {
				encodedBody = string(b)
			}
		}
	}

	// 前置冷却拦截：若初始账号在冷却中，直接切换到健康账号，不发送自杀式请求
	if encodedBody != "" && (isAccountCoolingDown(curAuthID) || isAccountCoolingDown(curAuthUID)) {
		if nextID, nextSA, hasNext := pickNextAuth(curAuthID); hasNext && nextSA != nil {
			if nextReq, rebErr := rebuildRequestWithQoderAuth(nextSA, encodedBody, upstreamModel, curReq.Context()); rebErr == nil {
				curReq = nextReq
				curAuthID = nextID
				curAuthUID = nextSA.Account.UID
				curAccountLabel = strings.TrimSpace(nextSA.Account.Nickname)
				if curAccountLabel == "" {
					curAccountLabel = curAuthUID
				}
			}
		}
	}

	for attempt := 0; attempt <= budget; attempt++ {
		stream, statusCode, _, err := hostHTTPDoStream(curReq)
		if err != nil {
			publishUsage(requestedModel, upstreamModel, curAuthUID, started, usage.Detail{}, true, 0, err.Error(), reasoningEffort, 0, curAccountLabel, sessionKey)
			noteAccountFailure(curAuthID, 0, err.Error())
			streamEmitError(streamID, fmt.Sprintf("http_error: %v", err))
			return
		}
		if statusCode >= 400 {
			// Drain the error body via the same bridge so the message is complete.
			errPayload := readAllUpstreamErr(newHostStreamReader(stream))
			stream.Close()
			publishUsage(requestedModel, upstreamModel, curAuthUID, started, usage.Detail{}, true, statusCode, errPayload, reasoningEffort, 0, curAccountLabel, sessionKey)
			if isDailyCountExceeded(statusCode, errPayload) {
				_ = triggerDailyCooldown("", curAuthID)
			}
			if curAuthUID != "" {
				go reconcileByUID(curAuthUID, statusCode, errPayload)
			}
			noteAccountFailure(curAuthID, statusCode, errPayload)
			// Retry policy (v0.9.1 — 429 included): account-level 4xx
			// (401/403/404/405) and 429 soft rate limit benefit from
			// switching accounts. 5xx/0/402 are already surfaced as
			// failures and recorded; rotating the account on 5xx inside
			// a single request gives no guarantee the next upstream
			// isn't also 5xx, so we don't burn budget on it (the
			// cooldown mechanism handles long-term 5xx). Business 400 is
			// request-shaped and would fail identically on every account,
			// so we propagate it immediately.
			if !isAccountLevel4xx(statusCode) || attempt >= budget || encodedBody == "" {
				streamEmitError(streamID, fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(errPayload, 200)))
				return
			}
			// Pick the next account and rebuild the request against its
			// COSY signature. If nothing else is usable, surface the
			// original error.
			evictSessionBindingsForAuth(curAuthID)
			nextID, nextSA, hasNext := pickNextAuth(curAuthID)
			if !hasNext || nextSA == nil {
				streamEmitError(streamID, fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(errPayload, 200)))
				return
			}
			nextReq, rebErr := rebuildRequestWithQoderAuth(nextSA, encodedBody, upstreamModel, curReq.Context())
			if rebErr != nil {
				streamEmitError(streamID, fmt.Sprintf("upstream %d: %s (retry rebuild failed: %v)", statusCode, truncateRedacted(errPayload, 200), rebErr))
				return
			}
			curReq = nextReq
			curAuthID = nextID
			curAuthUID = nextSA.Account.UID
			curAccountLabel = strings.TrimSpace(nextSA.Account.Nickname)
			if curAccountLabel == "" {
				curAccountLabel = curAuthUID
			}
			continue
		}
		// Success — pump chunks to the host stream. From here on this
		// attempt owns the response stream and must close it before
		// returning. An in-stream error frame ({"error": ...} inside the
		// unwrapped inner body on a 200 response) is a business failure:
		// with zero chunks emitted it can still rotate accounts on the
		// SAME request; once anything has reached the client the error is
		// surfaced instead. （同步自 workbuddy 200 错误帧换号）
		collector := &sseUsageCollector{}
		// TTFT 计时基准取本次尝试的发出时刻，而不是请求级 started：
		// 后者跨重试复用，会把上一个账号的失败耗时算到当前账号头上。
		collector.markAttemptStart(time.Now())
		var sseErr string
		emitted := false
		contentEmitted := false
		scanner := bufio.NewScanner(newHostStreamReader(stream))
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			chunkJSON, isDone := parseQoderSSEChunk(line)
			if isDone {
				break
			}
			if chunkJSON == "" {
				continue
			}
			collector.feed(chunkJSON)
			if msg := sseErrorFrame(chunkJSON); msg != "" {
				sseErr = fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
				break
			}
			cleaned := cleanChunkJSON(chunkJSON)
			if cleaned == "" {
				continue
			}
			if streamChunkHasContent(chunkJSON) {
				contentEmitted = true
			}
			if sseFramed {
				cleaned = "data: " + cleaned
			}
			if err := streamEmit(streamID, []byte(cleaned)); err != nil {
				// Client disconnected / host closed stream — abort; do not report success.
				stream.Close()
				publishUsage(requestedModel, upstreamModel, curAuthUID, started, collector.detail(), true, 0, "stream_emit: "+err.Error(), reasoningEffort, collector.ttftNS(started), curAccountLabel, sessionKey)
				return
			}
			emitted = true
		}
		// 断流失败：正文零泄漏时换号续试，已出正文时透传错误。
		stream.Close()
		if err := scanner.Err(); err != nil {
			readErrMsg := fmt.Sprintf("upstream stream read error: %v", err)
			// 已泄漏正文分片时打标记，抑制换号与信封状态码；仅思考分片单独标记，仍可换号
			if contentEmitted {
				readErrMsg = fmt.Sprintf("upstream stream read error: %v (%s)", err, streamReadErrorLeakedMarker)
			} else if emitted {
				readErrMsg = fmt.Sprintf("upstream stream read error: %v (%s)", err, streamReadErrorReasoningOnlyMarker)
			}
			// 1. 记录本次尝试失败（用量统计），但不对账号记账：
			// 断流是上游网络/服务抖动（connection reset by peer），与账号凭据健康度无关；
			// 记账会让健康账号被阶梯冷却，故障时段过后仍无法承载流量（2026-10-18 实证：
			// 48h 内 2 次断流均来自同一上游 IP，账号本身全程正常）。
			publishUsage(requestedModel, upstreamModel, curAuthUID, started, collector.detail(), true, 0, err.Error(), reasoningEffort, collector.ttftNS(started), curAccountLabel, sessionKey)
			// 2. 正文零泄漏且预算允许时换号重建请求
			if !contentEmitted && attempt < budget && encodedBody != "" {
				evictSessionBindingsForAuth(curAuthID)
				nextID, nextSA, hasNext := pickNextAuth(curAuthID)
				if shouldRotateOnStreamReadError(emitted, contentEmitted, attempt, budget, true, hasNext && nextSA != nil) {
					nextReq, rebErr := rebuildRequestWithQoderAuth(nextSA, encodedBody, upstreamModel, curReq.Context())
					if rebErr != nil {
						streamEmitError(streamID, fmt.Sprintf("%s (retry rebuild failed: %v)", readErrMsg, rebErr))
						return
					}
					curReq = nextReq
					curAuthID = nextID
					curAuthUID = nextSA.Account.UID
					curAccountLabel = strings.TrimSpace(nextSA.Account.Nickname)
					if curAccountLabel == "" {
						curAccountLabel = curAuthUID
					}
					continue
				}
			}
			// 3. 不满足换号条件时收尾：正文已泄漏时合成 length 终止分片，
			// 让下游转译出 response.incomplete（客户端保留已生成内容，new-api
			// 可凭终态事件与已收正文计费）；零泄漏/无候选仍透传错误。
			if contentEmitted {
				if termRaw, termErr := buildStreamTerminalChunk(requestedModel, streamTruncatedFinishReason); termErr == nil {
					if sseFramed {
						termRaw = append([]byte("data: "), termRaw...)
					}
					if emitErr := streamEmit(streamID, termRaw); emitErr == nil {
						return
					}
				}
			}
			streamEmitError(streamID, readErrMsg)
			return
		}
		if sseErr != "" {
			// SSE error frame on HTTP 200: the current account must enter
			// failover cooldown. Remap to 403 so isAccountFailure classifies
			// it as account-level and the account gets its laddered cooldown.
			if isDailyCountExceeded(statusCode, sseErr) {
				_ = triggerDailyCooldown("", curAuthID)
			}
			noteAccountFailure(curAuthID, http.StatusForbidden, sseErr)
			publishUsage(requestedModel, upstreamModel, curAuthUID, started, collector.detail(), true, statusCode, sseErr, reasoningEffort, collector.ttftNS(started), curAccountLabel, sessionKey)
			// 200 内业务错误帧与 HTTP 4xx 同责：零泄漏（emitted=false）且账号级
			// 分类命中且预算允许时换号续试；已泄漏分片时换号会造成输出重复或
			// 拼接错乱，只能透传错误收尾。
			if emitted || !shouldRotateOnUpstreamErr(statusCode, sseErr) || attempt >= budget || encodedBody == "" {
				streamEmitError(streamID, sseErr)
				return
			}
			evictSessionBindingsForAuth(curAuthID)
			nextID, nextSA, hasNext := pickNextAuth(curAuthID)
			if !hasNext || nextSA == nil {
				streamEmitError(streamID, sseErr)
				return
			}
			nextReq, rebErr := rebuildRequestWithQoderAuth(nextSA, encodedBody, upstreamModel, curReq.Context())
			if rebErr != nil {
				streamEmitError(streamID, fmt.Sprintf("%s (retry rebuild failed: %v)", sseErr, rebErr))
				return
			}
			curReq = nextReq
			curAuthID = nextID
			curAuthUID = nextSA.Account.UID
			curAccountLabel = strings.TrimSpace(nextSA.Account.Nickname)
			if curAccountLabel == "" {
				curAccountLabel = curAuthUID
			}
			continue
		}
		if !emitted {
			// Upstream zero-byte stream: the gateway accepted the connection
			// and closed it before the first payload (the host judges this
			// empty_stream / Retryable and retries across accounts). Keep the
			// stream close behavior unchanged — only fix the accounting: the
			// attempt is a TRANSIENT-THROTTLE failure that goes through
			// noteAccountFailure onto the shared laddered cooldown (soft
			// failures count toward the ladder too; no freeze), not a success.
			const emptyStreamErr = "upstream stream closed before first payload"
			publishUsage(requestedModel, upstreamModel, curAuthUID, started, collector.detail(), true, statusCode, emptyStreamErr, reasoningEffort, collector.ttftNS(started), curAccountLabel, sessionKey)
			noteAccountFailure(curAuthID, statusCode, emptyStreamErr)
			return
		}
		successTTFTNS := collector.ttftNS(started)
		publishUsage(requestedModel, upstreamModel, curAuthUID, started, collector.detail(), false, 0, "", reasoningEffort, successTTFTNS, curAccountLabel, sessionKey)
		invalidateAccountCredits(curAuthID, curAuthUID)
		resetAccountFailover(curAuthID)
		// 慢速打点：真实成功才记样本，失败换号不计（错误耗时不是首字延迟）。
		noteSlowObservation(curAuthID, upstreamModel, successTTFTNS)
		return
	}
}

// collectUpstreamStreamQoder is the Qoder AI-flavoured synchronous fallback
// (no async stream id): drain the upstream nested SSE, unwrap the inner
// OpenAI chunks, return them as a slice. The collector, when non-nil,
// observes the unwrapped inner chunks for usage extraction.
func collectUpstreamStreamQoder(encodedBody string, sa *storedAuth, modelKey string, sseFramed bool, collector *sseUsageCollector) ([]pluginapi.ExecutorStreamChunk, int, error) {
	// Per-request account-failover budget. 0 means no retry (kill switch
	// via `retry_on_4xx: 0` in plugin.yaml). The encoded body is
	// account-independent; each retry re-signs it with COSY for the next
	// candidate account.
	budget := loadedRetryOn4xx()
	curSA := sa
	lastStatus := 0

	// 前置冷却拦截：若初始账号在冷却中，直接换号到健康候选
	if curSA != nil {
		currentID := strings.TrimSpace(curSA.Auth.AccessToken)
		if currentID == "" {
			currentID = strings.TrimSpace(curSA.Account.UID)
		}
		if isAccountCoolingDown(currentID) || (curSA.Account.UID != "" && isAccountCoolingDown(curSA.Account.UID)) {
			if _, nextSA, hasNext := pickNextAuth(currentID); hasNext && nextSA != nil {
				curSA = nextSA
			}
		}
	}

	for attempt := 0; attempt <= budget; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointChat, strings.NewReader(encodedBody))
		if err != nil {
			return nil, 0, err
		}
		if err := applyCosyHeaders(httpReq, curSA, encodedBody, endpointChat, modelKey, true); err != nil {
			return nil, 0, fmt.Errorf("cosy: %w", err)
		}
		stream, statusCode, _, err := hostHTTPDoStream(httpReq)
		if err != nil {
			return nil, 0, fmt.Errorf("http_error: %w", err)
		}
		if statusCode >= 400 {
			payload := readAllUpstreamErr(newHostStreamReader(stream))
			stream.Close()
			lastStatus = statusCode
			errResp := fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(payload, 200))
			// Retry policy: only account-level 4xx (401/403/404/405)
			// benefit from switching accounts. 5xx/0/429/402 and business
			// 400 are propagated immediately (the cooldown mechanism
			// handles long-term failures).
			if !isAccountLevel4xx(statusCode) || attempt >= budget || curSA == nil {
				return nil, statusCode, errResp
			}
			// Failover bookkeeping keys on the same heuristic as the
			// executor: access token first, UID fallback.
			currentID := strings.TrimSpace(curSA.Auth.AccessToken)
			if currentID == "" {
				currentID = strings.TrimSpace(curSA.Account.UID)
			}
			if isDailyCountExceeded(statusCode, errResp.Error()) {
				_ = triggerDailyCooldown("", currentID)
			}
			evictSessionBindingsForAuth(currentID)
			_, nextSA, hasNext := pickNextAuth(currentID)
			if !hasNext || nextSA == nil {
				return nil, statusCode, errResp
			}
			curSA = nextSA
			continue
		}
		chunks := make([]pluginapi.ExecutorStreamChunk, 0, 64)
		var sseErrMsg string
		contentEmitted := false
		scanner := bufio.NewScanner(newHostStreamReader(stream))
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			chunkJSON, isDone := parseQoderSSEChunk(line)
			if isDone {
				break
			}
			if chunkJSON == "" {
				continue
			}
			if collector != nil {
				collector.feed(chunkJSON)
			}
			if msg := sseErrorFrame(chunkJSON); msg != "" {
				// 200 内业务错误帧（同步路径）：与 4xx 同责处理，跳出后
				// 走统一换号判定。（同步自 workbuddy 200 错误帧换号）
				sseErrMsg = msg
				break
			}
			cleaned := cleanChunkJSON(chunkJSON)
			if cleaned == "" {
				continue
			}
			if streamChunkHasContent(chunkJSON) {
				contentEmitted = true
			}
			if sseFramed {
				cleaned = "data: " + cleaned
			}
			chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: json.RawMessage(cleaned)})
		}
		stream.Close()
		if sseErrMsg != "" {
			// Surface the in-stream failure with the canonical "upstream N:"
			// shape so parseUpstreamStatusFromErr (caller side) sees 200.
			// Zero-leak rule: chunks already collected means partial output
			// was produced — propagate instead of rotating.
			lastStatus = http.StatusOK
			errResp := fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(sseErrMsg, 200))
			if len(chunks) > 0 || !shouldRotateOnUpstreamErr(http.StatusOK, sseErrMsg) || attempt >= budget || curSA == nil {
				return chunks, http.StatusOK, errResp
			}
			currentID := strings.TrimSpace(curSA.Auth.AccessToken)
			if currentID == "" {
				currentID = strings.TrimSpace(curSA.Account.UID)
			}
			evictSessionBindingsForAuth(currentID)
			_, nextSA, hasNext := pickNextAuth(currentID)
			if !hasNext || nextSA == nil {
				return chunks, http.StatusOK, errResp
			}
			curSA = nextSA
			continue
		}
		if err := scanner.Err(); err != nil {
			// 1. 已收集分片即视为已下发；其中是否含正文由 contentEmitted 判定
			emitted := len(chunks) > 0
			// 2. 正文零泄漏且预算允许时换号重试（仅思考分片允许重放）
			if !contentEmitted && attempt < budget && curSA != nil {
				currentID := strings.TrimSpace(curSA.Auth.AccessToken)
				if currentID == "" {
					currentID = strings.TrimSpace(curSA.Account.UID)
				}
				evictSessionBindingsForAuth(currentID)
				_, nextSA, hasNext := pickNextAuth(currentID)
				if shouldRotateOnStreamReadError(emitted, contentEmitted, attempt, budget, encodedBody != "", hasNext && nextSA != nil) {
					curSA = nextSA
					continue
				}
			}
			// 3. 不满足换号条件时透传错误
			return chunks, 0, fmt.Errorf("upstream stream read error: %w", err)
		}
		return chunks, 0, nil
	}
	return nil, lastStatus, fmt.Errorf("upstream: retry budget exhausted")
}

// clientNeedsSSEFrame reports whether chunk payloads must carry their own
// "data: " SSE framing. CPA's chat-completions passthrough adds the prefix
// itself, but every cross-format response translator (claude/gemini/codex/...)
// only consumes payloads already framed as "data: " lines. The host hands the
// plugin the inbound request path in Metadata, so we frame chunks ourselves for
// any entry path other than the native OpenAI chat-completions one.
func clientNeedsSSEFrame(metadata map[string]any) bool {
	path, _ := metadata["request_path"].(string)
	switch strings.ToLower(strings.TrimSpace(path)) {
	case "/v1/chat/completions", "/v1/completions":
		return false
	default:
		return true
	}
}

// cleanChunkJSON strips only the known-problematic empty tool-call shells
// from choice deltas: a null/empty function_call and an empty tool_calls array
// (Qoder AI emits these on the terminal chunk, and strict clients interpret
// them as a truncated tool call). Other empty-but-legal values are preserved:
// content:"" is a valid delta (pure tool-call chunk) and the role-only first
// chunk must survive so clients can establish the message role.
func cleanChunkJSON(s string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(s), &obj) != nil {
		return s
	}
	changed := false
	if choices, ok := obj["choices"].([]any); ok {
		for _, c := range choices {
			choice, ok := c.(map[string]any)
			if !ok {
				continue
			}
			delta, ok := choice["delta"].(map[string]any)
			if !ok {
				continue
			}
			if v, present := delta["function_call"]; present && isEmptyValue(v) {
				delete(delta, "function_call")
				changed = true
			}
			if v, present := delta["tool_calls"]; present {
				if arr, isArr := v.([]any); isArr && len(arr) == 0 {
					delete(delta, "tool_calls")
					changed = true
				}
			}
			// Upstream often pads terminal/noop deltas with empty noise fields
			// that clients ignore but pollute wire size / some parsers.
			for _, noise := range []string{"extra_fields", "refusal", "reasoning_content"} {
				if v, present := delta[noise]; present && isEmptyValue(v) {
					delete(delta, noise)
					changed = true
				}
			}
			// Drop a fully-empty delta ONLY when the choice carries no other
			// signal (no finish_reason): e.g. {"delta":{"function_call":null}}
			// reduced to {}. A delta with role/content:"" is meaningful and
			// never reaches this branch (those fields are preserved above).
			if len(delta) == 0 {
				if fr, _ := choice["finish_reason"].(string); fr == "" {
					return ""
				}
			}
		}
	}
	if !changed {
		return s
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return s
	}
	return string(out)
}

// aggregateCompletion folds a flat inner SSE stream (already unwrapped from
// the Qoder AI nested envelope by aggregateQoderSSE) into a single OpenAI
// chat.completion object. statusCode is the upstream HTTP status (200 on the
// aggregate path) and is echoed into the canonical "upstream N:" error when
// an in-stream OpenAI error frame is detected — a business failure inside a
// 200 body must fail fast instead of folding a bogus completion.
// （同步自 workbuddy 200 错误帧 fail-fast）
func aggregateCompletion(r io.Reader, model string, statusCode int, firstByteAt *time.Time) ([]byte, error) {
	var content, reasoning, role, respModel, respID, finish string
	var created int64
	var usage map[string]any
	// tool_calls arrive as streaming deltas: each chunk carries an index plus a
	// partial call (id/type/function.name on the first delta, argument text
	// fragments afterwards). Merge by index instead of appending raw fragments
	// so the folded completion holds whole calls.
	toolCalls := map[int]map[string]any{}
	var toolOrder []int
	var scanErr error
	var sseErr string

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		data := stripDataPrefix(scanner.Text())
		if data == "" || data == "[DONE]" {
			continue
		}
		if msg := sseErrorFrame(data); msg != "" {
			// Business failure inside an HTTP 200 body: fail fast with the
			// canonical "upstream N:" shape; the frame and everything after
			// it must not be folded into the completion.
			sseErr = fmt.Sprintf("upstream %d: %s", statusCode, msg)
			break
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		// 首个可解析分片即视为「上游开始回数据」，作为非流式路径的 TTFT 观测点。
		if firstByteAt != nil && firstByteAt.IsZero() {
			*firstByteAt = time.Now()
		}
		if v, ok := chunk["id"].(string); ok && v != "" {
			respID = v
		}
		if v, ok := chunk["model"].(string); ok && v != "" {
			respModel = v
		}
		if v, ok := chunk["created"].(float64); ok {
			created = int64(v)
		}
		if v, ok := chunk["usage"].(map[string]any); ok {
			usage = v
		}
		choices, _ := chunk["choices"].([]any)
		for _, c := range choices {
			choice, _ := c.(map[string]any)
			if delta, ok := choice["delta"].(map[string]any); ok {
				if v, ok := delta["role"].(string); ok && v != "" {
					role = v
				}
				if v, ok := delta["content"].(string); ok {
					content += v
				}
				if v, ok := delta["reasoning_content"].(string); ok {
					reasoning += v
				}
				if tcs, ok := delta["tool_calls"].([]any); ok {
					for _, tc := range tcs {
						call, ok := tc.(map[string]any)
						if !ok {
							continue
						}
						idx := 0
						if v, ok := call["index"].(float64); ok {
							idx = int(v)
						}
						merged, seen := toolCalls[idx]
						if !seen {
							merged = map[string]any{"index": idx}
							toolCalls[idx] = merged
							toolOrder = append(toolOrder, idx)
						}
						mergeToolCallDelta(merged, call)
					}
				}
			}
			if v, ok := choice["finish_reason"].(string); ok && v != "" {
				finish = v
			}
		}
	}
	if err := scanner.Err(); err != nil {
		scanErr = err
	}
	// An in-stream business error frame (200 body) outranks a truncated read:
	// the failure is definitive and must surface with the canonical prefix.
	if sseErr != "" {
		return nil, fmt.Errorf("%s", sseErr)
	}
	// A mid-stream read failure means the folded completion is truncated. The
	// host discards the payload entirely when the plugin returns an error
	// (sdk/api/handlers executeWithPluginExecutor), so fail fast here instead
	// of assembling a partial completion nobody can safely consume.
	if scanErr != nil {
		return nil, fmt.Errorf("upstream stream read error: %w", scanErr)
	}

	message := map[string]any{"role": firstNonEmpty(role, "assistant"), "content": content}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	if len(toolOrder) > 0 {
		sort.Ints(toolOrder)
		calls := make([]map[string]any, 0, len(toolOrder))
		for _, idx := range toolOrder {
			calls = append(calls, toolCalls[idx])
		}
		message["tool_calls"] = calls
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	result := map[string]any{
		"id":      firstNonEmpty(respID, "chatcmpl-qoderwork"),
		"object":  "chat.completion",
		"created": created,
		"model":   firstNonEmpty(respModel, model),
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": firstNonEmpty(finish, "stop"),
		}},
	}
	if usage != nil {
		result["usage"] = usage
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// aggregateQoderSSE folds Qoder AI's nested SSE stream into a single
// OpenAI chat.completion object. The gateway emits frames shaped:
//
//	data:{"headers":{...},"body":"<json-string>","statusCodeValue":200,...}
//
// where `body` is a JSON-encoded string that itself contains an OpenAI-style
// chunk: {"choices":[{"delta":{...}}],...}. We unwrap the outer envelope,
// then aggregate the inner chunks using the same logic as aggregateCompletion
// (including the in-stream error-frame fail-fast; statusCode is echoed into
// the canonical "upstream N:" error).
//
// Terminal frames: data:{"body":"[DONE]"} followed by an event:finish line
// with timing metadata (ignored).
func aggregateQoderSSE(r io.Reader, model string, statusCode int, firstByteAt *time.Time) ([]byte, error) {
	// Unwrap the nested SSE into an inner plain-text stream of OpenAI chunks,
	// then delegate to aggregateCompletion. We materialise the inner stream
	// into memory because the gateway's SSE is short-lived (one chat call)
	// and the inner stream is at most a few hundred KB.
	var inner strings.Builder
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		chunkJSON, isDone := parseQoderSSEChunk(line)
		if isDone {
			break
		}
		if chunkJSON == "" {
			continue
		}
		// 错误帧（含顶层 code 排队帧）必须在读取循环内立即收尾：上游在
		// HTTP 200 下吐出错误帧后仍保持 Keep-Alive 心跳约 60 秒，若继续读到
		// EOF 再判定，非流式路径会与流式路径一样挂死。这里提前返回 canonical
		// "upstream N:" 错误，交调用方按既有 failover 语义处理。
		if msg := sseErrorFrame(chunkJSON); msg != "" {
			return nil, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		// Re-emit the inner JSON as a standard "data:<json>\n" SSE frame so
		// aggregateCompletion's parser can consume it unchanged.
		inner.WriteString("data:")
		inner.WriteString(chunkJSON)
		inner.WriteString("\n\n")
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("qoder SSE read: %w", err)
	}
	return aggregateCompletion(strings.NewReader(inner.String()), model, statusCode, firstByteAt)
}

// sseErrorFrame extracts the message from an OpenAI-convention in-stream
// error frame — `{"error":{"message":...}}` or `{"error":"..."}` — and
// returns "" for any normal content chunk. Upstreams occasionally deliver
// quota/rate-limit failures as HTTP 200 + an error frame instead of a 4xx
// status; without this check those frames would be forwarded to the client
// as if they were regular content. （同步自 workbuddy，2026-09-06）
//
// 另识别 Qoder 上游的顶层业务错误帧 `{"code":"403","message":"..."}`
// （排队/限流形态，message 为内嵌 JSON 字符串时二次解析提取语义）。
// 最近修改时间 2026-10-12；改动原因：识别顶层 code 排队帧，消除 60s 挂死。
func sseErrorFrame(content string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(content), &obj) != nil {
		return ""
	}
	v, present := obj["error"]
	if !present || v == nil {
		// 顶层 code 帧：{"code":"403","message":"..."}。message 可能是内嵌
		// JSON 字符串（如 {"code":"10605","message":"{\"isQueued\":true,...}"}），
		// 逐层解析取出可读文本。
		if code, hasCode := obj["code"].(string); hasCode && strings.TrimSpace(code) != "" {
			if msg, _ := obj["message"].(string); strings.TrimSpace(msg) != "" {
				return code + ": " + unwrapNestedMessage(msg)
			}
			return code
		}
		return ""
	}
	switch t := v.(type) {
	case map[string]any:
		msg, _ := t["message"].(string)
		if msg == "" {
			return ""
		}
		if code, _ := t["code"].(string); code != "" {
			return code + ": " + msg
		}
		return msg
	case string:
		return t
	}
	return ""
}

// unwrapNestedMessage 递归展开内嵌 JSON 字符串，直到取得可读文本。
// 上游排队帧的 message 形如 {"code":"10605","message":"{\"isQueued\":true,...}"}，
// 需逐层解析才能取到最内层的业务描述。展开过程中保留首层业务码（如 10605），
// 最终拼成 "code: 描述"，避免排障时丢失关键错误码。最多展开 4 层，防异常自嵌套死循环。
// [参数] msg：可能为内嵌 JSON 的 message 文本。
// [返回] 展开后的可读文本；非 JSON 时原样返回。
// 最近修改时间 2026-10-12；改动原因：随顶层 code 排队帧识别新增。
func unwrapNestedMessage(msg string) string {
	cur := strings.TrimSpace(msg)
	code := ""
	for i := 0; i < 4; i++ {
		if !strings.HasPrefix(cur, "{") {
			break
		}
		var inner map[string]any
		if json.Unmarshal([]byte(cur), &inner) != nil {
			break
		}
		if c, _ := inner["code"].(string); strings.TrimSpace(c) != "" && code == "" {
			code = strings.TrimSpace(c)
		}
		next, _ := inner["message"].(string)
		if strings.TrimSpace(next) == "" {
			break
		}
		cur = strings.TrimSpace(next)
	}
	if code != "" {
		return code + ": " + cur
	}
	return cur
}

// mergeToolCallDelta folds one streaming tool_call fragment into the merged
// call: scalar fields (id/type) are taken when first seen, function.name is
// concatenated (upstream may split it), and function.arguments text fragments
// are appended in arrival order.
func mergeToolCallDelta(merged, delta map[string]any) {
	for _, k := range []string{"id", "type"} {
		if _, present := merged[k]; !present {
			if v, ok := delta[k].(string); ok && v != "" {
				merged[k] = v
			}
		}
	}
	dfn, _ := delta["function"].(map[string]any)
	if dfn == nil {
		return
	}
	mfn, _ := merged["function"].(map[string]any)
	if mfn == nil {
		mfn = map[string]any{}
		merged["function"] = mfn
	}
	if v, ok := dfn["name"].(string); ok && v != "" {
		cur, _ := mfn["name"].(string)
		mfn["name"] = cur + v
	}
	if v, ok := dfn["arguments"].(string); ok && v != "" {
		cur, _ := mfn["arguments"].(string)
		mfn["arguments"] = cur + v
	}
}

func stripDataPrefix(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "data:") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "data:"))
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// isEmptyValue reports whether v is a "zero" SSE field that should be
// stripped from outgoing chunks. Used by cleanChunkJSON to drop the legacy
// function_call shell ({"name":"","arguments":""}) some upstreams emit on
// the terminal chunk — clients that strictly validate the delta will reject
// the shell as an invalid tool call.
func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		if len(x) == 0 {
			return true
		}
		for _, val := range x {
			if !isEmptyValue(val) {
				return false
			}
		}
		return true
	}
	return false
}

// ttftNSBetween 计算两个时刻之间的纳秒差，任一为零或差值非正时返回 0。
// [参数] started：计时起点；firstOutputAt：首个有效输出到达时刻。
// [返回] 纳秒差值。
// 最近修改时间：2026-10-26；改动原因：新增，非流式路径复用统一的首字延迟口径。
func ttftNSBetween(started, firstOutputAt time.Time) uint64 {
	if started.IsZero() || firstOutputAt.IsZero() {
		return 0
	}
	d := firstOutputAt.Sub(started)
	if d <= 0 {
		return 0
	}
	return uint64(d)
}
