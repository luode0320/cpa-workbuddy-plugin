// stream.go owns the upstream SSE data plane: emitting cleaned chunks back to
// the host stream (streamEmit/close), pumping the upstream SSE in a goroutine
// (pumpUpstreamStream), collecting it synchronously (collectUpstreamStream),
// and the SSE-frame helpers that re-frame, filter, and aggregate chunks.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
// 断流额外带上 http_status（见 envelopeHTTPStatusForError），供宿主识别为网关类失败；
// 宿主未读取该字段时会被忽略，行为与旧版一致。
// [参数] streamID：宿主流标识；message：终态错误文本。
// [返回] 序列化后的 emit 信封；序列化失败返回错误。
// 最近修改时间 2026-10-17 21:30:00 断流信封补 http_status。
func marshalStreamErrorEnvelope(streamID, message string) ([]byte, error) {
	body := map[string]any{"stream_id": streamID, "error": message}
	if status := envelopeHTTPStatusForError(message); status > 0 {
		body["http_status"] = status
	}
	return json.Marshal(body)
}

// emitStreamErrorEnvelope 通过 host.stream.emit 信封的 "error" 字段发送终态错误。
// 宿主把该字段映射为执行器流 chunk 的 Err：核心 conductor 只有收到真正的错误
// chunk 才会判定执行失败，进而轮换下一个凭据——包括另一平台（如 traework）的
// 账号。若把错误当 payload 数据帧发出，宿主会视为正常流内容，请求以"成功"告终，
// 跨平台失败切换永远不会触发。
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

func streamClose(streamID string) {
	if streamID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID})
	_, _ = hostCall(pluginabi.MethodHostStreamClose, body)
}

func streamHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	return h
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

	// 前置冷却拦截：如果宿主分配的初始账号当前正在冷却中（如上一请求刚失败 403），
	// 绝对不要对它发起自杀式的上游调用！在 attempt=0 之前立即换号到可用的健康候选。
	if isAuthModelCoolingDown(curAuthID, upstreamModel) || isAuthModelCoolingDown(curAuthUID, upstreamModel) {
		if nextID, nextSA, hasNext := pickNextAuthForModel(curAuthID, upstreamModel); hasNext && nextSA != nil {
			if nextReq, rebErr := rebuildRequestWithSA(curReq, nextSA); rebErr == nil {
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

	// 模型级拦截不再有独立的全局闸门：被拦截的账号×模型已写入冷却，
	// 路由层 isAuthModelCoolingDown 会跳过它们，本请求若仍落到这里说明上游
	// 刚恢复或该组合尚未被标记，按正常流程发起即可。

	for attempt := 0; attempt <= budget; attempt++ {
		stream, statusCode, _, err := hostHTTPDoStream(curReq)
		if err != nil {
			publishUsage(requestedModel, upstreamModel, curAuthUID, started, usage.Detail{}, true, 0, err.Error(), reasoningEffort, 0, curAccountLabel, sessionKey)
			noteAccountFailureForModel(curAuthID, upstreamModel, 0, err.Error())
			streamEmitError(streamID, fmt.Sprintf("http_error: %v", err))
			return
		}
		if statusCode >= 400 {
			// Drain the error body via the same bridge so the message is complete.
			errPayload := readAllUpstreamErr(newHostStreamReader(stream))
			stream.Close()
			publishUsage(requestedModel, upstreamModel, curAuthUID, started, usage.Detail{}, true, statusCode, errPayload, reasoningEffort, 0, curAccountLabel, sessionKey)
			if curAuthUID != "" {
				go reconcileByUIDForModel(curAuthUID, upstreamModel, statusCode, errPayload)
			}
			noteAccountFailureForModel(curAuthID, upstreamModel, statusCode, errPayload)

			// Retry policy (v0.14.2 — 429 included):
			//   account-level 4xx (401/403/404/405) and 429 soft rate limit
			//   benefit from switching accounts on the SAME request,
			//   constrained by retry_on_4xx budget.
			//   5xx/0/402 are still surfaced and recorded, but rotate the
			//   account cross-request via the cooldown tier; same-request
			//   rotation gives no guarantee the next upstream isn't also
			//   5xx, so we don't burn budget on it.
			//   Business 400 is request-shaped and would fail identically
			//   on every account, so we propagate it immediately.
			//
			// 11128「请求被拦截」：双写记账后直接返回。
			// 该状态码是 400，本就不会进入换号分支（isAccountLevel4xx 不含
			// 400），因此这里只需负责记账——noteModelBlockedHit 会同时写入
			// 该模型的冷却（覆盖写盘生效前的窗口）并禁用该账号，路由层用
			// isAccountCoolingDown 跳过它，恢复则由 10 分钟禁用探活裁决。
			if isModelBlockedError(statusCode, errPayload) {
				noteModelBlockedHit(curAuthID, upstreamModel)
				streamEmitError(streamID, fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(errPayload, 200)))
				return
			}
			if !isAccountLevel4xx(statusCode) {
				streamEmitError(streamID, fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(errPayload, 200)))
				return
			}
			if attempt >= budget {
				streamEmitError(streamID, fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(errPayload, 200)))
				return
			}
			// Pick the next account and rebuild the request against
			// its endpoint + auth headers. If nothing else is
			// usable, surface the original error.
			nextID, nextSA, hasNext := pickNextAuthForModel(curAuthID, upstreamModel)
			if !hasNext || nextSA == nil {
				streamEmitError(streamID, fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(errPayload, 200)))
				return
			}
			nextReq, rebErr := rebuildRequestWithSA(curReq, nextSA)
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
		// returning. An in-stream error frame ({"error": ...} on a 200
		// body) is a business failure: with zero chunks emitted it can
		// still rotate accounts on the SAME request; once anything has
		// reached the client the error is surfaced instead.
		collector := &sseUsageCollector{}
		scanner := bufio.NewScanner(newHostStreamReader(stream))
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		var sseErr string
		emitted := false
		contentEmitted := false
		for scanner.Scan() {
			content := stripDataPrefix(scanner.Text())
			if content == "" || content == "[DONE]" {
				continue
			}
			collector.feed(content)
			if msg := sseErrorFrame(content); msg != "" {
				sseErr = fmt.Sprintf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
				break
			}
			cleaned := cleanChunkJSON(content)
			if cleaned == "" {
				continue
			}
			if streamChunkHasContent(content) {
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
			if !contentEmitted && attempt < budget && curReq.GetBody != nil {
				evictSessionBindingsForAuth(curAuthID)
				nextID, nextSA, hasNext := pickNextAuthForModel(curAuthID, upstreamModel)
				if shouldRotateOnStreamReadError(emitted, contentEmitted, attempt, budget, curReq.GetBody != nil, hasNext && nextSA != nil) {
					nextReq, rebErr := rebuildRequestWithSA(curReq, nextSA)
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
			// SSE error frame on HTTP 200 must enter failover cooldown
			// regardless of whether we rotate or surface the error. Remap
			// to 403 so isAccountFailure classifies it as account-level
			// and the account gets its laddered cooldown.
			noteSSEErrorFailure(curAuthID, upstreamModel, sseErr)
			publishUsage(requestedModel, upstreamModel, curAuthUID, started, collector.detail(), true, statusCode, sseErr, reasoningEffort, collector.ttftNS(started), curAccountLabel, sessionKey)
			// 200 内业务错误帧与 HTTP 4xx 同责：零泄漏（emitted=false）且账号级
			// 分类命中且预算允许时换号续试；已泄漏分片时换号会造成输出重复或
			// 拼接错乱，只能透传错误收尾。
			if emitted || !shouldRotateOnUpstreamErr(statusCode, sseErr) || attempt >= budget {
				streamEmitError(streamID, sseErr)
				return
			}
			evictSessionBindingsForAuth(curAuthID)
			nextID, nextSA, hasNext := pickNextAuthForModel(curAuthID, upstreamModel)
			if !hasNext || nextSA == nil {
				streamEmitError(streamID, sseErr)
				return
			}
			nextReq, rebErr := rebuildRequestWithSA(curReq, nextSA)
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
			noteAccountFailureForModel(curAuthID, upstreamModel, statusCode, emptyStreamErr)
			return
		}
		publishUsage(requestedModel, upstreamModel, curAuthUID, started, collector.detail(), false, 0, "", reasoningEffort, collector.ttftNS(started), curAccountLabel, sessionKey)
		invalidateAccountCredits(curAuthID, curAuthUID)
		resetAccountFailover(curAuthID)
		// 异步流式是生产主路径，轮换计时必须在这里推进，否则长流量的账号
		// 永远不会被判定为「连续使用满 N 分钟」。
		noteRotationUsage(curAuthID)
		// 一次真实成功即证明该模型已恢复，清掉该模型上残留的冷却，
		// 避免其它账号的旧记录让已恢复的模型继续被跳过。
		resetModelBlockedState(upstreamModel)
		return
	}
}

// collectUpstreamStream is the synchronous fallback (no async stream id): drain
// the upstream, clean each chunk, return them as a slice. The collector, when
// non-nil, observes raw upstream chunks for usage extraction. statusCode is the
// upstream HTTP status (0 for transport-level failures).
//
// On an account-level 4xx (401/403/404/405) or 429 soft rate limit the
// request body is fine — only the chosen credential is broken for this
// endpoint (or the account's quota is exhausted). We try a configured-budget
// number of alternate accounts before giving up. Failures found by
// isAccountFailure (5xx, 0, 402) are still surfaced as errors but do NOT
// trigger a same-request account rotation; the failover-cooldown layer
// handles those across requests. 429 is included here so the next attempt
// can route around a per-account rate limit without waiting for the laddered
// cooldown to expire; cross-request cooldown continues to apply
// via isAccountFailure / recordAccountFailure on the failing account.
// [参数] body：上游请求体；sa：初始账号凭据；sseFramed：是否需自带 SSE 帧前缀；
// collector：用量采集器；model：已归一化的上游模型名，用于模型级熔断判定。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增 model 参数以支持模型级风控熔断。
func collectUpstreamStream(body []byte, sa *storedAuth, sseFramed bool, collector *sseUsageCollector, model string) ([]pluginapi.ExecutorStreamChunk, int, error) {
	budget := loadedRetryOn4xx()
	curSA := sa
	var lastStatus int
	var lastErr error

	// 前置冷却拦截：如果初始账号处于冷却中，直接换号到健康候选。
	// 轮换冷却与失败冷却共用这条拦截，因为两者都表示「该号当前不应承流」。
	if curSA != nil {
		currentID := strings.TrimSpace(curSA.Auth.AccessToken)
		if currentID == "" {
			currentID = strings.TrimSpace(curSA.Account.UID)
		}
		rotationNow := time.Now()
		if isAuthModelCoolingDown(currentID, model) ||
			(curSA.Account.UID != "" && isAuthModelCoolingDown(curSA.Account.UID, model)) ||
			rotationCoolingDown(currentID, rotationNow) ||
			(curSA.Account.UID != "" && rotationCoolingDown(curSA.Account.UID, rotationNow)) {
			if _, nextSA, hasNext := pickNextAuthForModel(currentID, model); hasNext && nextSA != nil {
				curSA = nextSA
			}
		}
	}

	for attempt := 0; attempt <= budget; attempt++ {
		chunks, statusCode, errOnce := collectUpstreamStreamOnce(body, curSA, sseFramed, collector, model)
		if errOnce == nil {
			// 真实成功即证明模型已恢复，清掉该模型上残留的冷却。
			resetModelBlockedState(model)
			return chunks, statusCode, nil
		}
		lastStatus = statusCode
		lastErr = errOnce

		// 11128：双写记账（模型冷却 + 账号禁用写盘）后直接返回。
		// 禁用探活会把这个模型排在回放序列最前，探通才解除禁用。
		if isModelBlockedError(statusCode, errOnce.Error()) {
			if curSA != nil {
				currentID := strings.TrimSpace(curSA.Auth.AccessToken)
				if currentID == "" {
					currentID = strings.TrimSpace(curSA.Account.UID)
				}
				noteModelBlockedHit(currentID, model)
			}
			return chunks, statusCode, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(errOnce.Error(), 200))
		}

		// 1. 已收集分片即视为已下发；其中是否含正文由 streamChunksHaveContent 判定
		if isStreamReadError(errOnce.Error()) {
			emitted := len(chunks) > 0
			contentEmitted := streamChunksHaveContent(chunks)
			// 2. 正文零泄漏且预算允许时换号重试（仅思考分片允许重放）
			if !contentEmitted && attempt < budget && curSA != nil {
				currentID := strings.TrimSpace(curSA.Auth.AccessToken)
				if currentID == "" {
					currentID = strings.TrimSpace(curSA.Account.UID)
				}
				if currentID != "" {
					evictSessionBindingsForAuth(currentID)
					_, nextSA, hasNext := pickNextAuthForModel(currentID, model)
					if shouldRotateOnStreamReadError(emitted, contentEmitted, attempt, budget, true, hasNext && nextSA != nil) {
						curSA = nextSA
						continue
					}
				}
			}
			// 3. 不满足换号条件时透传错误
			return chunks, statusCode, errOnce
		}

		// Same retry policy as pumpUpstreamStream: account-level 4xx and
		// (new) account-classified 200 in-stream error frames warrant a
		// same-request account rotation; 5xx/0/402 stay cooldown-only.
		if !shouldRotateOnUpstreamErr(statusCode, errOnce.Error()) {
			return chunks, statusCode, errOnce
		}
		if attempt >= budget {
			return chunks, statusCode, errOnce
		}
		// curSA.Account.UID is the host auth id; for pickNextAuth we
		// key on the same scheduler-level ID that noteAccountFailure
		// uses, which is sa.Auth.AccessToken-derived. If we don't
		// have an auth-id-shaped handle, skip the swap (the original
		// error propagates).
		currentID := strings.TrimSpace(curSA.Auth.AccessToken)
		if currentID == "" {
			currentID = strings.TrimSpace(curSA.Account.UID)
		}
		if currentID == "" {
			return chunks, statusCode, errOnce
		}
		_, nextSA, hasNext := pickNextAuthForModel(currentID, model)
		if !hasNext || nextSA == nil {
			return chunks, statusCode, errOnce
		}
		curSA = nextSA
	}
	return nil, lastStatus, lastErr
}

// collectUpstreamStreamOnce performs exactly one synchronous upstream call
// against sa: build the request, route via host.http.do_stream, decide
// success vs 4xx/5xx. Pulled out of collectUpstreamStream so the retry
// loop can rebuild the request against a different account on failure.
func collectUpstreamStreamOnce(body []byte, sa *storedAuth, sseFramed bool, collector *sseUsageCollector, model string) ([]pluginapi.ExecutorStreamChunk, int, error) {
	httpReq, err := http.NewRequest(http.MethodPost, endpointChatFor(sa), bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	backendHeaders(httpReq, sa)
	// Compliance: route via host.http.do_stream so request-log captures the call.
	stream, statusCode, _, err := hostHTTPDoStream(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("http_error: %w", err)
	}
	defer stream.Close()
	reader := newHostStreamReader(stream)
	if statusCode >= 400 {
		errPayload, _ := io.ReadAll(reader)
		if sa != nil && sa.Account.UID != "" {
			go reconcileByUIDForModel(sa.Account.UID, model, statusCode, string(errPayload))
		}
		return nil, statusCode, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(string(errPayload), 200))
	}
	chunks, errAgg := aggregateSSEWithCollector(reader, statusCode, sseFramed, collector)
	if errAgg != nil {
		return chunks, statusCode, errAgg
	}
	return chunks, statusCode, nil
}

// doExecuteOnce performs exactly one synchronous execute against sa: build
// the request, route via host.http.do_stream, fold SSE into a chat.completion
// payload. Pulled out of handleExecExecute so the retry loop can rebuild the
// request against a different account on failure. Returns a non-nil error
// in the canonical "upstream N: ..." or "http_error: ..." shape that
// parseUpstreamStatusFromErr understands.
func doExecuteOnce(body []byte, sa *storedAuth, requestedModel, upstreamModel string) ([]byte, error) {
	httpReq, err := http.NewRequest(http.MethodPost, endpointChatFor(sa), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	backendHeaders(httpReq, sa)
	stream, statusCode, _, err := hostHTTPDoStream(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http_error: %w", err)
	}
	defer stream.Close()
	reader := newHostStreamReader(stream)
	if statusCode >= 400 {
		payload, _ := io.ReadAll(reader)
		if sa != nil && sa.Account.UID != "" {
			go reconcileByUIDForModel(sa.Account.UID, upstreamModel, statusCode, string(payload))
		}
		return nil, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(string(payload), 200))
	}
	var firstByteAt time.Time
	completion, err := aggregateCompletion(reader, requestedModel, statusCode, &firstByteAt)
	if err != nil {
		return nil, err
	}
	return completion, nil
}

// streamChunksHaveContent 判定已收集分片里是否含正文或工具调用。
//
// 同步路径只拿到清理后的分片切片，无法回看原始 JSON，故按同样的 delta 结构判定。
// [参数] chunks：已收集的宿主分片。
// [返回] 任一分片含非空 content / refusal 或 tool_calls 时返回 true。
// 最近修改时间 2026-10-18 03:20:00 新增，用于区分思考泄漏与正文泄漏。
func streamChunksHaveContent(chunks []pluginapi.ExecutorStreamChunk) bool {
	for _, chunk := range chunks {
		if streamChunkHasContent(string(chunk.Payload)) {
			return true
		}
	}
	return false
}

// parseUpstreamStatusFromErr recovers the upstream HTTP status code from a
// "upstream N: ..." sentinel error (or returns 0 for any other shape /
// transport-level errors like http_error / http_error: ...). Used by the
// execute-retry loop in handleExecExecute to decide whether to switch
// accounts.
func parseUpstreamStatusFromErr(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "upstream ") {
		return 0
	}
	rest := strings.TrimPrefix(msg, "upstream ")
	idx := strings.Index(rest, ":")
	if idx <= 0 {
		return 0
	}
	n, perr := strconv.Atoi(strings.TrimSpace(rest[:idx]))
	if perr != nil {
		return 0
	}
	return n
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

// aggregateSSEWithCollector reads an upstream SSE stream and emits one chunk
// per data event. Empty tool-call shells are stripped and the trailing [DONE]
// is dropped (the host appends its own stream terminator). When sseFramed is
// true each payload is emitted as a "data: " line for cross-format
// translators; otherwise the payload is the raw JSON object and the host
// chat-completions writer adds the framing itself. A mid-stream read error
// aborts collection and is returned so the caller records the attempt as
// failed. An in-stream error frame ({"error": ...} on a 200 body) aborts
// collection with the canonical "upstream N:" error so the caller's account
// rotation can classify it; chunks collected before the frame are returned
// alongside the error and discarded by every caller on err != nil.
// The collector, when non-nil, observes raw upstream chunks for usage
// extraction.
// 最近修改时间：2026-09-06 17:00:00；改动原因：同步 traework 的 200 SSE 业务错误换号修复。
func aggregateSSEWithCollector(r io.Reader, statusCode int, sseFramed bool, collector *sseUsageCollector) ([]pluginapi.ExecutorStreamChunk, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var chunks []pluginapi.ExecutorStreamChunk
	for scanner.Scan() {
		content := stripDataPrefix(scanner.Text())
		if content == "" || content == "[DONE]" {
			continue
		}
		if collector != nil {
			collector.feed(content)
		}
		if msg := sseErrorFrame(content); msg != "" {
			return chunks, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		cleaned := cleanChunkJSON(content)
		if cleaned == "" {
			continue
		}
		if sseFramed {
			cleaned = "data: " + cleaned
		}
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: []byte(cleaned)})
	}
	if err := scanner.Err(); err != nil {
		return chunks, fmt.Errorf("upstream stream read error: %w", err)
	}
	return chunks, nil
}

// cleanChunkJSON strips only the known-problematic empty tool-call shells
// from choice deltas: a null/empty function_call and an empty tool_calls array
// (CodeBuddy emits these on the terminal chunk, and strict clients interpret
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

// aggregateCompletion folds an upstream SSE stream into one chat.completion
// payload. statusCode is the upstream HTTP status, used only to label an
// in-stream error frame ({"error": ...} on a 200 body) with the canonical
// "upstream N:" prefix so executor rotation can classify it.
// 最近修改时间：2026-09-06 17:00:00；改动原因：同步 traework 的 200 SSE 业务错误换号修复。
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

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		data := stripDataPrefix(scanner.Text())
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if msg := sseErrorFrame(data); msg != "" {
			return nil, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
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
		"id":      firstNonEmpty(respID, "chatcmpl-workbuddy"),
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

// sseErrorFrame extracts the message from an OpenAI-convention in-stream
// error frame: data: {"error": {...}} or data: {"error": "..."}. The HTTP
// status of such a stream is 200 — the business failure rides inside the
// SSE body (production evidence: the Trae upstream carries quota/rate-limit
// failures this way; see traework stream 389/391). Returns "" for any
// non-error chunk so the hot path stays a single map lookup.
// 最近修改时间：2026-09-06 17:00:00；改动原因：同步 traework 的 200 SSE 业务错误换号修复。
func sseErrorFrame(content string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(content), &obj) != nil {
		return ""
	}
	v, present := obj["error"]
	if !present || v == nil {
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
