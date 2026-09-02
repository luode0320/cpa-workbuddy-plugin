// executor.go implements the chat-completions executor for TraeWork:
// handleExecExecute (non-streaming) and handleExecStream (streaming). Both
// paths normalize the OpenAI request into the Trae llm_utils_chat payload,
// run the per-request account-failover loop on account-level 4xx, and fold
// the upstream SSE back into chat.completion responses.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// executorStreamRequest wraps the host's executor.execute_stream RPC: the
// ExecutorRequest plus the async stream id the host uses to receive chunks.
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// traeSyncStreamDeps 汇总同步流式账号重试使用的既有依赖，便于用脚本化上游锁定同请求恢复行为。
type traeSyncStreamDeps struct {
	CallLLM      func(a *traeAuth, payload map[string]any, authID string) (*hostHTTPResponse, error)
	PickNextAuth func(currentAuthID string) (nextAuthID string, nextSA *traeAuth, ok bool)
}

var defaultTraeSyncStreamDeps = traeSyncStreamDeps{
	CallLLM:      callLLM,
	PickNextAuth: pickNextAuth,
}

// traeSyncStreamContext 保存同步流式重试共享的模型、账号和用量上下文。
type traeSyncStreamContext struct {
	Model         string
	UpstreamModel string
	AuthID        string
	AuthUID       string
	Started       time.Time
	InputChars    int
	Budget        int
}

// traeAsyncUpstream 统一生产宿主流与测试内存流的读取、关闭契约。
type traeAsyncUpstream struct {
	Reader io.Reader
	Close  func()
}

// traeAsyncStreamDeps 汇总异步逻辑请求协调器的账号、上游和客户端流依赖。
type traeAsyncStreamDeps struct {
	Open         func(a *traeAuth, payload map[string]any, authID, hostCallbackID string) (traeAsyncUpstream, int, error)
	PickNextAuth func(currentAuthID string) (nextAuthID string, nextSA *traeAuth, ok bool)
	Emit         func(streamID string, payload []byte) error
	Close        func(streamID string)
}

var defaultTraeAsyncStreamDeps = traeAsyncStreamDeps{
	Open: func(a *traeAuth, payload map[string]any, authID, hostCallbackID string) (traeAsyncUpstream, int, error) {
		stream, statusCode, err := callLLMStream(a, payload, authID, hostCallbackID)
		if err != nil {
			return traeAsyncUpstream{}, statusCode, err
		}
		return traeAsyncUpstream{Reader: newHostStreamReader(stream), Close: stream.Close}, statusCode, nil
	},
	PickNextAuth: pickNextAuth,
	Emit:         streamEmit,
	Close:        streamClose,
}

// traeAsyncStreamContext 保存整个异步逻辑请求跨账号共享的输入与终结上下文。
type traeAsyncStreamContext struct {
	StreamID       string
	HostCallbackID string
	Model          string
	UpstreamModel  string
	Payload        map[string]any
	Started        time.Time
	InputChars     int
	Budget         int
}

// authIDFor returns the account identity used for failover/credit bookkeeping
// under the host's auth-ID namespace: prefer the host-provided ID (scheduler
// key), fall back to the credential's userId.
func authIDFor(a *traeAuth, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	if a != nil && strings.TrimSpace(a.UserID) != "" {
		return strings.TrimSpace(a.UserID)
	}
	return ""
}

// authLogHash 返回账号标识的不可逆短哈希，仅用于跨 attempt 关联脱敏日志。
func authLogHash(authID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(authID)))
	return fmt.Sprintf("%x", sum[:4])
}

// handleExecExecute performs one non-streaming chat completion.
func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	a, err := parseTraeAuth(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	upstreamModel := stripProviderPrefix(req.Model)
	started := time.Now()
	authUID := strings.TrimSpace(a.UserID)

	oa := &openAIRequest{}
	if err := json.Unmarshal(req.Payload, oa); err != nil && len(req.Payload) > 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "payload parse: "+err.Error())
		return nil, fmt.Errorf("payload parse: %w", err)
	}
	messages := toTraeMessages(oa.Messages)
	if len(messages) == 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "empty messages")
		return nil, fmt.Errorf("empty messages after normalization")
	}
	payload := buildTraePayload(messages, upstreamModel, false, oa.MaxTokens, oa.Temperature, oa.TopP)

	budget := loadedRetryOn4xx()
	curSA := a
	usedAuthID := authIDFor(curSA, req.AuthID)
	for attempt := 0; attempt <= budget; attempt++ {
		resp, callErr := callLLM(curSA, payload, usedAuthID)
		if callErr == nil {
			completion, aggErr := aggregateTraeCompletion(bytes.NewReader(resp.Body), req.Model, resp.StatusCode)
			if aggErr == nil {
				resetAccountFailover(usedAuthID)
				publishUsage(req.Model, upstreamModel, authUID, started, estimateUsageFromCompletion(completion), false, 0, "")
				return okEnvelope(pluginapi.ExecutorResponse{Payload: completion})
			}
			// SSE-layer business errors: the Trae llm_utils_chat upstream returns
			// HTTP 200 + event:error (4011 rate-limit, 14018 quota exhausted,
			// ...), so callLLM succeeded and the account-level failure only
			// surfaces here. When the error classifies as account-level, rotate
			// to the next candidate on the SAME request (mirrors the HTTP 4xx
			// branch below); otherwise surface it immediately.
			if !isAccountFailure(resp.StatusCode, aggErr.Error()) || attempt >= budget || curSA == nil {
				reconcileAfterExecutorError(usedAuthID, resp.StatusCode, aggErr.Error())
				publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, resp.StatusCode, aggErr.Error())
				return nil, aggErr
			}
			reconcileAfterExecutorError(usedAuthID, resp.StatusCode, aggErr.Error())
			nextAuthID, nextSA, hasNext := pickNextAuth(usedAuthID)
			if !hasNext || nextSA == nil {
				return nil, aggErr
			}
			curSA = nextSA
			usedAuthID = nextAuthID
			continue
		}

		statusCode := parseUpstreamStatusFromErr(callErr)
		// Retry only on account-level 4xx (401/403/404/405/429); business 400,
		// 5xx and transport errors surface immediately (cooldown handles them).
		if !isAccountLevel4xx(statusCode) || attempt >= budget || curSA == nil {
			reconcileAfterExecutorError(usedAuthID, statusCode, callErr.Error())
			publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, callErr.Error())
			return nil, callErr
		}
		nextAuthID, nextSA, hasNext := pickNextAuth(usedAuthID)
		if !hasNext || nextSA == nil {
			break
		}
		curSA = nextSA
		usedAuthID = nextAuthID
	}
	errFinal := fmt.Errorf("upstream account pool exhausted after %d attempt(s)", budget+1)
	reconcileAfterExecutorError(usedAuthID, 0, errFinal.Error())
	publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, errFinal.Error())
	return nil, errFinal
}

// handleExecStream 执行流式聊天补全；有 StreamID 时异步推送分片，否则同步收集后返回。
// [参数] raw: 宿主传入的流式执行请求 JSON。
// [返回] 响应 envelope JSON；请求解析或上游调用失败时返回错误。
// 最近修改时间：2026-08-30 23:40:18；改动原因：异步路径改用宿主流桥实时读取并透传 callback context，避免长回答被全量缓冲。
func handleExecStream(raw []byte) ([]byte, error) {
	// 1. 解析请求、账号与模型，构造 Trae 流式上游负载。
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	a, err := parseTraeAuth(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	upstreamModel := stripProviderPrefix(req.Model)
	started := time.Now()
	authUID := strings.TrimSpace(a.UserID)

	bodyRaw := req.Payload
	if len(bodyRaw) == 0 {
		bodyRaw = req.OriginalRequest
	}
	oa := &openAIRequest{}
	if err := json.Unmarshal(bodyRaw, oa); err != nil && len(bodyRaw) > 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "payload parse: "+err.Error())
		return nil, fmt.Errorf("payload parse: %w", err)
	}
	messages := toTraeMessages(oa.Messages)
	if len(messages) == 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "empty messages")
		return nil, fmt.Errorf("empty messages after normalization")
	}
	inputChars := estimateInputChars(messages)
	payload := buildTraePayload(messages, upstreamModel, true, oa.MaxTokens, oa.Temperature, oa.TopP)
	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)

	usedAuthID := authIDFor(a, req.AuthID)

	// 2. 无异步流标识时同步收集分片；账号级失败与伪完成都在同一请求内按预算换号。
	if req.StreamID == "" {
		chunks, streamErr := runTraeSyncStream(a, payload, traeSyncStreamContext{
			Model:         req.Model,
			UpstreamModel: upstreamModel,
			AuthID:        usedAuthID,
			AuthUID:       authUID,
			Started:       started,
			InputChars:    inputChars,
			Budget:        loadedRetryOn4xx(),
		}, defaultTraeSyncStreamDeps)
		if streamErr != nil {
			return nil, streamErr
		}
		if sseFramed {
			chunks = sseFrameChunks(chunks)
		}
		return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
	}

	// 3. 有异步流标识时立即返回响应头；后台协调器在同一 StreamID 内完成上游打开、伪完成换号和唯一收尾。
	go func() {
		deps := defaultTraeAsyncStreamDeps
		closeStream := deps.Close
		var closeOnce sync.Once
		deps.Close = func(streamID string) {
			closeOnce.Do(func() { closeStream(streamID) })
		}
		runTraeAsyncStream(a, usedAuthID, traeAsyncStreamContext{
			StreamID:       req.StreamID,
			HostCallbackID: req.HostCallbackID,
			Model:          req.Model,
			UpstreamModel:  upstreamModel,
			Payload:        payload,
			Started:        started,
			InputChars:     inputChars,
			Budget:         loadedRetryOn4xx(),
		}, deps)
	}()
	log.Printf("[traework] exec stream async scheduled: model=%s auth_hash=%s stream_id=%s", req.Model, authLogHash(usedAuthID), req.StreamID)
	return okEnvelope(streamResponse{Headers: headers})
}

// runTraeAsyncStream 协调一个宿主 StreamID 下的多账号上游尝试，并只在最终结果上收尾一次。
// [参数] initialAuth: 初始账号；initialAuthID: 初始账号标识；ctx: 逻辑请求上下文；deps: 上游与宿主流依赖。
// [返回] 无；最终正文、错误和关闭通过 deps 发送，所有 attempt 的用量分别按实际账号记录。
// 最近修改时间：2026-09-01 23:50:00；改动原因：异步伪完成必须保持原 StreamID 打开并在当前请求内切到健康账号。
func runTraeAsyncStream(initialAuth *traeAuth, initialAuthID string, ctx traeAsyncStreamContext, deps traeAsyncStreamDeps) {
	curSA := initialAuth
	curAuthID := initialAuthID
	curAuthUID := strings.TrimSpace(initialAuth.UserID)
	requestID := randomUUID()
	triedAuthIDs := map[string]struct{}{curAuthID: {}}
	attemptsMade := 0
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr := fmt.Errorf("Trae stream coordinator panic: %v", recovered)
			emitTraeAsyncError(ctx.StreamID, panicErr.Error(), deps)
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, 0, panicErr.Error())
		}
	}()
	for attempt := 0; attempt <= ctx.Budget; attempt++ {
		attemptsMade = attempt + 1
		// 1. 每次账号尝试复用同一 payload 和 HostCallbackID，但拥有独立、及时关闭的上游句柄。
		//    伪完成是上游对账号的窗口性限流信号；先核算 + 换号，仅当池中已无其它可用
		//    候选（单号池或他号全冷却）时才对当前账号同号退避重试——生产实证 2351：
		//    同号约 60s 后恢复完整长输出，直接判 pool exhausted 会让单号池的请求必败。
		pseudoTries := 0
	retrySameAuth:
		upstream, statusCode, openErr := deps.Open(curSA, ctx.Payload, curAuthID, ctx.HostCallbackID)
		if openErr != nil {
			if statusCode == 0 {
				statusCode = parseUpstreamStatusFromErr(openErr)
			}
			log.Printf("[traework] exec stream async open error: request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d status=%d err=%s",
				requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, statusCode, truncateRedacted(openErr.Error(), 200))
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, statusCode, openErr.Error())
			if !isAccountLevel4xx(statusCode) || attempt >= ctx.Budget {
				if isAccountLevel4xx(statusCode) {
					reconcileAfterExecutorError(curAuthID, statusCode, openErr.Error())
					evictSessionBindingsForAuth(curAuthID)
				}
				emitTraeAsyncError(ctx.StreamID, openErr.Error(), deps)
				return
			}
			// 账号级 4xx 必须核算冷却并驱逐绑定，否则下个请求的会话亲和仍会选回该死号
			//（生产实证：225774 反复 401，host 每次 cache-miss 重绑死号 → pool exhausted）。
			reconcileAfterExecutorError(curAuthID, statusCode, openErr.Error())
			evictSessionBindingsForAuth(curAuthID)
			nextAuthID, nextSA, hasNext := deps.PickNextAuth(curAuthID)
			if !hasNext || nextSA == nil {
				break
			}
			if _, seen := triedAuthIDs[nextAuthID]; seen {
				break
			}
			triedAuthIDs[nextAuthID] = struct{}{}
			curSA = nextSA
			curAuthID = nextAuthID
			curAuthUID = strings.TrimSpace(nextSA.UserID)
			continue
		}

		result := func() traeStreamAttemptResult {
			if upstream.Close != nil {
				defer upstream.Close()
			}
			return pumpTraeStreamAttempt(upstream.Reader, traeStreamPumpContext{
				StreamID:      ctx.StreamID,
				Model:         ctx.Model,
				UpstreamModel: ctx.UpstreamModel,
				StatusCode:    statusCode,
				AuthID:        curAuthID,
				AuthUID:       curAuthUID,
				Started:       ctx.Started,
				InputChars:    ctx.InputChars,
			}, requestID, func(payload []byte) error {
				return deps.Emit(ctx.StreamID, payload)
			})
		}()

		// 2. 门槛前伪完成不产生任何客户端输出；核算失败后换号，仅当无候选才同号退避重试。
		if result.Pseudo {
			reason := "pseudo completion: upstream returned done with near-empty output"
			log.Printf("[traework] exec stream async pseudo retry: request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d chunks=%d",
				requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, len(result.Chunks))
			noteForcedAccountFailure(curAuthID, reason)
			evictSessionBindingsForAuth(curAuthID)
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), true, statusCode, reason)
			if attempt >= ctx.Budget {
				break
			}
			nextAuthID, nextSA, hasNext := deps.PickNextAuth(curAuthID)
			if !hasNext || nextSA == nil {
				// 无其它候选：不立即判池耗尽，先同号退避重试一次（窗口性限流自愈），
				// 仍伪才由外层 attempt 预算收口为 pool exhausted。
				if pseudoTries < pseudoRetryBudget {
					pseudoTries++
					log.Printf("[traework] exec stream async pseudo same-auth retry (pool exhausted candidates): request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d chunks=%d",
						requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, len(result.Chunks))
					goto retrySameAuth
				}
				break
			}
			if _, seen := triedAuthIDs[nextAuthID]; seen {
				break
			}
			triedAuthIDs[nextAuthID] = struct{}{}
			curSA = nextSA
			curAuthID = nextAuthID
			curAuthUID = strings.TrimSpace(nextSA.UserID)
			continue
		}
		if result.Err != nil {
			log.Printf("[traework] exec stream async attempt error: request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d status=%d emitted=%v err=%s",
				requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, statusCode, result.Emitted, truncateRedacted(result.Err.Error(), 200))
			reconcileAfterExecutorError(curAuthID, statusCode, result.Err.Error())
			emitTraeAsyncError(ctx.StreamID, result.Err.Error(), deps)
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), true, statusCode, result.Err.Error())
			return
		}

		// 3. 只有最终可交付 attempt 能发送 finish；随后恰好关闭一次宿主 StreamID。
		finish := "stop"
		failed := false
		failureReason := ""
		if result.Termination == terminationOutputEOF {
			finish = "length"
			failed = true
			failureReason = "truncated: upstream stream ended without done"
		}
		finishRaw, finishErr := chunkDelta(requestID, ctx.Model, "", "", finish)
		if finishErr == nil && finishRaw != nil {
			finishErr = deps.Emit(ctx.StreamID, finishRaw)
			if finishErr == nil {
				result.Chunks = append(result.Chunks, pluginapi.ExecutorStreamChunk{Payload: finishRaw})
			}
		}
		if finishErr != nil {
			reconcileAfterExecutorError(curAuthID, statusCode, finishErr.Error())
			emitTraeAsyncError(ctx.StreamID, finishErr.Error(), deps)
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), true, statusCode, finishErr.Error())
			return
		}
		deps.Close(ctx.StreamID)
		if failed {
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), true, statusCode, failureReason)
			return
		}
		resetAccountFailover(curAuthID)
		publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, estimateUsageFromChunks(result.Chunks), false, 0, "")
		log.Printf("[traework] exec stream async done: request_id=%s stream_id=%s model=%s auth_hash=%s attempt=%d chunks=%d",
			requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), attempt+1, len(result.Chunks))
		return
	}

	errFinal := fmt.Errorf("upstream account pool exhausted after %d attempt(s)", attemptsMade)
	log.Printf("[traework] exec stream async pool exhausted: request_id=%s stream_id=%s model=%s auth_hash=%s err=%s",
		requestID, ctx.StreamID, ctx.Model, authLogHash(curAuthID), truncateRedacted(errFinal.Error(), 200))
	emitTraeAsyncError(ctx.StreamID, errFinal.Error(), deps)
}

// emitTraeAsyncError 发送单个错误分片并关闭宿主流；调用方发送后必须立即结束逻辑请求。
func emitTraeAsyncError(streamID, message string, deps traeAsyncStreamDeps) {
	payload, err := json.Marshal(map[string]any{"error": message})
	if err == nil {
		_ = deps.Emit(streamID, payload)
	}
	deps.Close(streamID)
}

// runTraeSyncStream 在同步流式请求内执行账号重试；伪完成分片在切号前直接丢弃。
// [参数] initialAuth: 初始账号；payload: 上游负载；ctx: 重试与用量上下文；deps: 上游和候选选择依赖。
// [返回] 最终健康账号分片；账号池耗尽或上游失败时返回错误且不返回伪完成分片。
// 最近修改时间：2026-09-01 23:40:00；改动原因：HTTP 200 伪完成必须恢复当前请求，而不是把短结果返回后只影响下一请求。
func runTraeSyncStream(initialAuth *traeAuth, payload map[string]any, ctx traeSyncStreamContext, deps traeSyncStreamDeps) ([]pluginapi.ExecutorStreamChunk, error) {
	curSA := initialAuth
	curAuthID := ctx.AuthID
	curAuthUID := ctx.AuthUID
	triedAuthIDs := map[string]struct{}{curAuthID: {}}
	lastFailurePublished := false
	attemptsMade := 0
	for attempt := 0; attempt <= ctx.Budget; attempt++ {
		attemptsMade = attempt + 1
		lastFailurePublished = false
		// 1. 每次尝试重新调用上游；普通账号级错误沿用既有同请求换号契约。
		//    伪完成仅在池中已无其它候选时同号退避重试（见下方伪完成分支）。
		pseudoTries := 0
	retrySameAuth:
		resp, callErr := deps.CallLLM(curSA, payload, curAuthID)
		if callErr != nil {
			statusCode := parseUpstreamStatusFromErr(callErr)
			if !isAccountLevel4xx(statusCode) || attempt >= ctx.Budget || curSA == nil {
				log.Printf("[traework] exec stream upstream error: model=%s auth_hash=%s status=%d err=%s", ctx.Model, authLogHash(curAuthID), statusCode, truncateRedacted(callErr.Error(), 200))
				reconcileAfterExecutorError(curAuthID, statusCode, callErr.Error())
				publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, statusCode, callErr.Error())
				return nil, callErr
			}
			nextAuthID, nextSA, hasNext := deps.PickNextAuth(curAuthID)
			if !hasNext || nextSA == nil {
				break
			}
			if _, seen := triedAuthIDs[nextAuthID]; seen {
				break
			}
			triedAuthIDs[nextAuthID] = struct{}{}
			curSA = nextSA
			curAuthID = nextAuthID
			curAuthUID = strings.TrimSpace(nextSA.UserID)
			continue
		}

		chunks, collectErr := collectTraeStream(bytes.NewReader(resp.Body), ctx.Model, resp.StatusCode)
		if collectErr == nil {
			if isPseudoCompletion(chunks, ctx.InputChars) {
				// 2. 伪完成按失败 attempt 核算并驱逐绑定；候选允许时丢弃当前 chunks 后继续。
				reason := "pseudo completion: upstream returned done with near-empty output"
				log.Printf("[traework] exec stream collect pseudo-done: model=%s auth_hash=%s attempt=%d chunks=%d", ctx.Model, authLogHash(curAuthID), attempt+1, len(chunks))
				noteForcedAccountFailure(curAuthID, reason)
				evictSessionBindingsForAuth(curAuthID)
				publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, estimateUsageFromChunks(chunks), true, resp.StatusCode, reason)
				lastFailurePublished = true
				if attempt >= ctx.Budget {
					break
				}
				nextAuthID, nextSA, hasNext := deps.PickNextAuth(curAuthID)
				if !hasNext || nextSA == nil {
					// 无其它候选：不立即判池耗尽，先同号退避重试一次（窗口性限流自愈），
					// 仍伪才由外层 attempt 预算收口为 pool exhausted。
					if pseudoTries < pseudoRetryBudget {
						pseudoTries++
						log.Printf("[traework] exec stream collect pseudo same-auth retry (pool exhausted candidates): model=%s auth_hash=%s attempt=%d chunks=%d", ctx.Model, authLogHash(curAuthID), attempt+1, len(chunks))
						goto retrySameAuth
					}
					break
				}
				if _, seen := triedAuthIDs[nextAuthID]; seen {
					break
				}
				triedAuthIDs[nextAuthID] = struct{}{}
				curSA = nextSA
				curAuthID = nextAuthID
				curAuthUID = strings.TrimSpace(nextSA.UserID)
				continue
			}
			resetAccountFailover(curAuthID)
			log.Printf("[traework] exec stream collect ok: model=%s auth_hash=%s attempt=%d chunks=%d", ctx.Model, authLogHash(curAuthID), attempt+1, len(chunks))
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, estimateUsageFromChunks(chunks), false, 0, "")
			return chunks, nil
		}

		// 3. HTTP 200 内的账号级 SSE 错误可继续换号，其余错误立即返回。
		if !isAccountFailure(resp.StatusCode, collectErr.Error()) || attempt >= ctx.Budget || curSA == nil {
			reconcileAfterExecutorError(curAuthID, resp.StatusCode, collectErr.Error())
			publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, resp.StatusCode, collectErr.Error())
			return nil, collectErr
		}
		reconcileAfterExecutorError(curAuthID, resp.StatusCode, collectErr.Error())
		nextAuthID, nextSA, hasNext := deps.PickNextAuth(curAuthID)
		if !hasNext || nextSA == nil {
			return nil, collectErr
		}
		if _, seen := triedAuthIDs[nextAuthID]; seen {
			break
		}
		triedAuthIDs[nextAuthID] = struct{}{}
		curSA = nextSA
		curAuthID = nextAuthID
		curAuthUID = strings.TrimSpace(nextSA.UserID)
	}

	errFinal := fmt.Errorf("upstream account pool exhausted after %d attempt(s)", attemptsMade)
	log.Printf("[traework] exec stream auth pool exhausted: model=%s auth_hash=%s err=%s", ctx.Model, authLogHash(curAuthID), truncateRedacted(errFinal.Error(), 200))
	if !lastFailurePublished {
		publishUsage(ctx.Model, ctx.UpstreamModel, curAuthUID, ctx.Started, usage.Detail{}, true, 0, errFinal.Error())
	}
	return nil, errFinal
}

// reconcileAfterExecutorError records the failure (failover cooldown +
// anomaly trip) for an executor error. Kept as a small indirection so the
// executor never has to know the failover/anomaly mechanics.
func reconcileAfterExecutorError(authID string, status int, body string) {
	noteAccountFailure(authID, status, body)
}

// sseFrameChunks wraps raw JSON payloads into SSE-framed text for legacy
// clients that expect "data: {...}\n\n" chunks.
func sseFrameChunks(chunks []pluginapi.ExecutorStreamChunk) []pluginapi.ExecutorStreamChunk {
	out := make([]pluginapi.ExecutorStreamChunk, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, pluginapi.ExecutorStreamChunk{
			Payload: append([]byte("data: "), append(c.Payload, '\n', '\n')...),
		})
	}
	return out
}

// estimateUsageFromCompletion estimates token usage from a chat.completion
// aggregate payload (best-effort: content length / 4).
func estimateUsageFromCompletion(completion []byte) usage.Detail {
	var c struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(completion, &c); err != nil {
		return usage.Detail{}
	}
	var chars int
	for _, ch := range c.Choices {
		chars += len(ch.Message.Content)
	}
	toks := int64(chars / 4)
	return usage.Detail{InputTokens: 0, OutputTokens: toks, TotalTokens: toks}
}

// estimateUsageFromChunks estimates output tokens from collected stream chunks.
func estimateUsageFromChunks(chunks []pluginapi.ExecutorStreamChunk) usage.Detail {
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
	toks := int64(chars / 4)
	return usage.Detail{InputTokens: 0, OutputTokens: toks, TotalTokens: toks}
}

// estimateInputChars sums the text characters across normalized request
// messages (toTraeMessages output: content is a text-parts array). Used as the
// pseudo-completion input-length signal: a long prompt that yields far less
// output than warranted is flagged, while a short prompt with a short answer is
// not.
func estimateInputChars(messages []map[string]any) int {
	var chars int
	for _, m := range messages {
		parts, _ := m["content"].([]map[string]any)
		for _, p := range parts {
			if txt, ok := p["text"].(string); ok {
				chars += len(txt)
			}
		}
	}
	return chars
}
