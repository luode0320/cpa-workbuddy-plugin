package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

var accountFailures = struct {
	sync.Mutex
	counts map[string]int
}{counts: make(map[string]int)}

// noteAccountFailure 记录账号连续故障次数。
// [参数] authID: 账号标识。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 账号故障记账入口。
func noteAccountFailure(authID string) {
	if authID == "" {
		return
	}
	accountFailures.Lock()
	defer accountFailures.Unlock()
	accountFailures.counts[authID]++
}

// resetAccountFailure 清除账号故障计数。
// [参数] authID: 账号标识。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 账号成功后复位。
func resetAccountFailure(authID string) {
	if authID == "" {
		return
	}
	accountFailures.Lock()
	defer accountFailures.Unlock()
	delete(accountFailures.counts, authID)
}

// handleExecExecute 处理非流式请求。
// [参数] raw: 宿主传入的执行请求 JSON。
// [返回] 执行响应信封、错误。
// 最近修改时间：2026-11-28 新增, 非流式对话入口。
func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := currentConfig()
	sa := resolveTargetAuth(req.AuthID, req.StorageJSON)
	if sa == nil {
		return nil, fmt.Errorf("no available glm account")
	}

	startTime := time.Now()
	accountLabel := sa.Label
	if accountLabel == "" {
		accountLabel = sa.AuthID
	}

	var completionReq completionRequest
	if err := json.Unmarshal(req.Payload, &completionReq); err != nil {
		return nil, fmt.Errorf("decode completion request: %w", err)
	}
	accessToken, err := acquireToken(sa.RefreshToken, cfg)
	if err != nil {
		noteAccountFailure(sa.AuthID)
		publishUsage(sa.AuthID, accountLabel, req.Model, startTime, usageTokensDetail{}, 0, 500, true, err.Error())
		return nil, fmt.Errorf("acquire token failed: %w", err)
	}
	payload, err := buildStreamPayload(completionReq, cfg, sa)
	if err != nil {
		return nil, err
	}
	httpReq, err := prepareStreamRequest(context.Background(), payload, cfg, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := inferenceHTTPClient.Do(httpReq)
	if err != nil {
		noteAccountFailure(sa.AuthID)
		publishUsage(sa.AuthID, accountLabel, req.Model, startTime, usageTokensDetail{}, time.Since(startTime).Milliseconds(), 0, true, err.Error())
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readLimited(resp.Body)
		if resp.StatusCode == http.StatusUnauthorized {
			invalidateToken(sa.RefreshToken)
		}
		noteAccountFailure(sa.AuthID)
		publishUsage(sa.AuthID, accountLabel, req.Model, startTime, usageTokensDetail{}, time.Since(startTime).Milliseconds(), resp.StatusCode, true, string(body))
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(body))
	}

	rawResp, err := readLimited(resp.Body)
	if err != nil {
		publishUsage(sa.AuthID, accountLabel, req.Model, startTime, usageTokensDetail{}, time.Since(startTime).Milliseconds(), resp.StatusCode, true, err.Error())
		return nil, fmt.Errorf("read upstream response: %w", err)
	}
	converted := convertNonStream(rawResp, completionReq.Model)
	resetAccountFailure(sa.AuthID)
	publishUsage(sa.AuthID, accountLabel, req.Model, startTime, usageTokensDetail{TotalTokens: 2}, time.Since(startTime).Milliseconds(), resp.StatusCode, false, "")
	return okEnvelope(pluginapi.ExecutorResponse{Payload: converted})
}

// convertNonStream 把 GLM 非流式 SSE 聚合结果转成 OpenAI chat.completion。
// 上游每一帧都携带 parts 的「全量快照」而非增量，因此不能逐帧直接拼接，
// 否则同一段回答会按帧数重复（实测 3 帧即得到三份）。这里复用流式路径的
// 快照增量算法：只在快照变长时追加新增部分。
// [参数] raw: 上游响应体；model: 模型名。
// [返回] OpenAI 兼容响应字节。
// 最近修改时间：2026-11-29 修正, 按快照增量去重，修复回答重复三遍。
func convertNonStream(raw []byte, model string) []byte {
	var text, reasoning strings.Builder
	convID := ""
	sentText := 0
	sentReasoning := 0
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
		if line == "" || line == "[DONE]" {
			continue
		}
		var frame glmStreamFrame
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			continue
		}
		if frame.ConversationID != "" {
			convID = frame.ConversationID
		}
		textDelta, reasoningDelta, newText, newReasoning := parseGLMFrame(frame, sentText, sentReasoning)
		sentText = newText
		sentReasoning = newReasoning
		text.WriteString(textDelta)
		reasoning.WriteString(reasoningDelta)
	}
	message := map[string]any{"role": "assistant", "content": text.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	result := map[string]any{
		"id":      convID,
		"model":   model,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
	out, _ := json.Marshal(result)
	return out
}

// handleExecStream 处理流式请求。
// [参数] raw: 宿主传入的流式执行请求 JSON。
// [返回] 流响应信封、错误。
// 最近修改时间：2026-11-28 新增, 流式对话入口。
func handleExecStream(raw []byte) ([]byte, error) {
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := currentConfig()
	sa := resolveTargetAuth(req.AuthID, req.StorageJSON)
	if sa == nil {
		streamEmitError(req.StreamID, "no available glm account")
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: streamHeaders()})
	}

	var completionReq completionRequest
	if err := json.Unmarshal(req.Payload, &completionReq); err != nil {
		return nil, fmt.Errorf("decode completion request: %w", err)
	}
	streamStart := time.Now()
	streamLabel := sa.Label
	if streamLabel == "" {
		streamLabel = sa.AuthID
	}
	accessToken, err := acquireToken(sa.RefreshToken, cfg)
	if err != nil {
		noteAccountFailure(sa.AuthID)
		publishUsage(sa.AuthID, streamLabel, req.Model, streamStart, usageTokensDetail{}, time.Since(streamStart).Milliseconds(), 500, true, err.Error())
		streamEmitError(req.StreamID, err.Error())
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: streamHeaders()})
	}
	payload, err := buildStreamPayload(completionReq, cfg, sa)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	httpReq, err := prepareStreamRequest(ctx, payload, cfg, accessToken)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := inferenceHTTPClient.Do(httpReq)
	if err != nil {
		cancel()
		noteAccountFailure(sa.AuthID)
		publishUsage(sa.AuthID, streamLabel, req.Model, streamStart, usageTokensDetail{}, time.Since(streamStart).Milliseconds(), 0, true, err.Error())
		streamEmitError(req.StreamID, err.Error())
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: streamHeaders()})
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readLimited(resp.Body)
		_ = resp.Body.Close()
		cancel()
		noteAccountFailure(sa.AuthID)
		if resp.StatusCode == http.StatusUnauthorized {
			invalidateToken(sa.RefreshToken)
		}
		publishUsage(sa.AuthID, streamLabel, req.Model, streamStart, usageTokensDetail{}, time.Since(streamStart).Milliseconds(), resp.StatusCode, true, string(body))
		streamEmitError(req.StreamID, fmt.Sprintf("upstream status %d: %s", resp.StatusCode, string(body)))
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: streamHeaders()})
	}

	go pumpGLMStream(ctx, cancel, req.StreamID, resp, completionReq.Model, sa.AuthID, streamLabel, sa.RefreshToken, streamStart)
	return okEnvelope(streamResponse{Headers: streamHeaders()})
}

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

// readLimited 读取有限长度的响应体。
// [参数] r: 输入流。
// [返回] 字节与错误。
// 最近修改时间：2026-11-28 新增, 上游响应读取上限。
func readLimited(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 32*1024)
	for len(out) < 1<<20 {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			return out, nil
		}
	}
	return out, nil
}
