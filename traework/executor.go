// executor.go implements the chat-completions executor for TraeWork:
// handleExecExecute (non-streaming) and handleExecStream (streaming). Both
// paths normalize the OpenAI request into the Trae llm_utils_chat payload,
// run the per-request account-failover loop on account-level 4xx, and fold
// the upstream SSE back into chat.completion responses.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strings"
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
	payload := buildTraePayload(messages, upstreamModel, true, oa.MaxTokens, oa.Temperature, oa.TopP)
	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)

	usedAuthID := authIDFor(a, req.AuthID)

	// 2. 无异步流标识时同步收集分片；账号级失败在同一请求内按预算换号重试。
	if req.StreamID == "" {
		budget := loadedRetryOn4xx()
		curSA := a
		curAuthID := usedAuthID
		for attempt := 0; attempt <= budget; attempt++ {
			resp, callErr := callLLM(curSA, payload, curAuthID)
			if callErr != nil {
				statusCode := parseUpstreamStatusFromErr(callErr)
				if !isAccountLevel4xx(statusCode) || attempt >= budget || curSA == nil {
					log.Printf("[traework] exec stream upstream error: model=%s auth=%s status=%d err=%s", req.Model, curAuthID, statusCode, truncateRedacted(callErr.Error(), 200))
					reconcileAfterExecutorError(curAuthID, statusCode, callErr.Error())
					publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, callErr.Error())
					return nil, callErr
				}
				nextAuthID, nextSA, hasNext := pickNextAuth(curAuthID)
				if !hasNext || nextSA == nil {
					break
				}
				curSA = nextSA
				curAuthID = nextAuthID
				continue
			}
			chunks, collectErr := collectTraeStream(bytes.NewReader(resp.Body), req.Model, resp.StatusCode)
			if collectErr == nil {
				if isPseudoCompletion(chunks) {
					// 伪完成：上游账号被静默限流/标记。计一次账号失败并驱逐会话
					// 绑定，让下一次请求切到健康账号；已生成内容照常返回客户端。
					log.Printf("[traework] exec stream collect pseudo-done: model=%s auth=%s attempt=%d chunks=%d", req.Model, curAuthID, attempt+1, len(chunks))
					noteForcedAccountFailure(curAuthID, "pseudo completion: upstream returned done with near-empty output")
					evictSessionBindingsForAuth(curAuthID)
					publishUsage(req.Model, upstreamModel, authUID, started, estimateUsageFromChunks(chunks), false, resp.StatusCode, "pseudo completion: upstream returned done with near-empty output")
					if sseFramed {
						return okEnvelope(streamResponse{Headers: headers, Chunks: sseFrameChunks(chunks)})
					}
					return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
				}
				resetAccountFailover(curAuthID)
				log.Printf("[traework] exec stream collect ok: model=%s auth=%s attempt=%d chunks=%d", req.Model, curAuthID, attempt+1, len(chunks))
				publishUsage(req.Model, upstreamModel, authUID, started, estimateUsageFromChunks(chunks), false, 0, "")
				if sseFramed {
					return okEnvelope(streamResponse{Headers: headers, Chunks: sseFrameChunks(chunks)})
				}
				return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
			}
			// SSE-layer business error on a 200 response: rotate accounts
			// when it classifies as account-level (4011/14018/...).
			if !isAccountFailure(resp.StatusCode, collectErr.Error()) || attempt >= budget || curSA == nil {
				reconcileAfterExecutorError(curAuthID, resp.StatusCode, collectErr.Error())
				publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, resp.StatusCode, collectErr.Error())
				return nil, collectErr
			}
			reconcileAfterExecutorError(curAuthID, resp.StatusCode, collectErr.Error())
			nextAuthID, nextSA, hasNext := pickNextAuth(curAuthID)
			if !hasNext || nextSA == nil {
				return nil, collectErr
			}
			curSA = nextSA
			curAuthID = nextAuthID
		}
		errFinal := fmt.Errorf("upstream account pool exhausted after %d attempt(s)", budget+1)
		log.Printf("[traework] exec stream auth pool exhausted: model=%s auth=%s err=%s", req.Model, curAuthID, truncateRedacted(errFinal.Error(), 200))
		reconcileAfterExecutorError(curAuthID, 0, errFinal.Error())
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, errFinal.Error())
		return nil, errFinal
	}

	// 3. 有异步流标识时打开宿主流桥并立即返回响应头，由流泵实时拉取和下发上游分片。
	upstreamStream, statusCode, callErr := callLLMStream(a, payload, usedAuthID, req.HostCallbackID)
	if callErr != nil {
		if statusCode == 0 {
			statusCode = parseUpstreamStatusFromErr(callErr)
		}
		log.Printf("[traework] exec stream async start error: model=%s auth=%s status=%d err=%s stream_id=%s", req.Model, usedAuthID, statusCode, truncateRedacted(callErr.Error(), 200), req.StreamID)
		reconcileAfterExecutorError(usedAuthID, statusCode, callErr.Error())
		streamEmitError(req.StreamID, callErr.Error())
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, callErr.Error())
		return okEnvelope(streamResponse{Headers: headers})
	}
	log.Printf("[traework] exec stream async pump: model=%s auth=%s status=%d stream_id=%s", req.Model, usedAuthID, statusCode, req.StreamID)
	go func() {
		// 3.1 后台流泵统一关闭上游句柄，并把非预期 panic 转成可见失败，避免穿透插件运行时。
		defer upstreamStream.Close()
		defer func() {
			if recovered := recover(); recovered != nil {
				panicErr := fmt.Errorf("Trae stream pump panic: %v", recovered)
				reconcileAfterExecutorError(usedAuthID, statusCode, panicErr.Error())
				streamEmitError(req.StreamID, panicErr.Error())
				publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, panicErr.Error())
			}
		}()

		// 3.2 宿主流桥按需拉取任意大小分片，SSE 扫描器负责跨分片重组事件行。
		pumpTraeStream(newHostStreamReader(upstreamStream), traeStreamPumpContext{
			StreamID:      req.StreamID,  // 绑定当前宿主异步流。
			Model:         req.Model,     // 保留客户端请求模型作为统计别名。
			UpstreamModel: upstreamModel, // 记录 Trae 实际请求模型。
			StatusCode:    statusCode,    // 供 SSE 错误核算使用。
			AuthID:        usedAuthID,    // 供故障退避与恢复使用。
			AuthUID:       authUID,       // 供账号用量维度使用。
			Started:       started,       // 计算请求总延迟。
		})
	}()
	return okEnvelope(streamResponse{Headers: headers})
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
