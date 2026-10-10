package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/zcode/internal/mimic"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// handleExecExecute 处理非流式请求。
func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	cfg := currentConfig()
	upstreamModel := mappedModel(req.Model, cfg)
	startTime := time.Now()

	sa := resolveTargetAuth(req.AuthID, req.StorageJSON)
	curPayload := mutateModel(req.Payload, upstreamModel)

	var lastErr error
	var finalRespBytes []byte
	var finalStatusCode int

	// 最多 3 次换号重试
	for attempt := 0; attempt < 3; attempt++ {
		if sa == nil || sa.APIKey == "" {
			lastErr = fmt.Errorf("no available zcode account")
			break
		}

		respBytes, status, err := executeOnce(curPayload, sa, cfg)
		finalStatusCode = status
		if err == nil {
			finalRespBytes = respBytes
			lastErr = nil
			break
		}

		lastErr = err
		noteAccountFailure(sa.AuthID, status, err.Error())

		if !shouldRotateOnUpstreamErr(status, err.Error()) {
			break
		}

		// 尝试换下一个可用账号
		nextSA, hasNext := pickNextAuth(sa.AuthID)
		if !hasNext || nextSA == nil {
			break
		}
		sa = nextSA
	}

	durationMS := time.Since(startTime).Milliseconds()
	authID := ""
	if sa != nil {
		authID = sa.AuthID
	}

	if lastErr != nil {
		publishUsage(authID, req.Model, upstreamModel, 0, 0, durationMS, finalStatusCode, true, lastErr.Error())
		return nil, lastErr
	}

	// 提取用量
	inTokens := gjson.GetBytes(finalRespBytes, "usage.input_tokens").Int()
	outTokens := gjson.GetBytes(finalRespBytes, "usage.output_tokens").Int()
	publishUsage(authID, req.Model, upstreamModel, inTokens, outTokens, durationMS, finalStatusCode, false, "")
	resetAccountFailover(authID)

	return okEnvelope(pluginapi.ExecutorResponse{Payload: finalRespBytes})
}

// handleExecStream 处理流式请求。
func handleExecStream(raw []byte) ([]byte, error) {
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	cfg := currentConfig()
	upstreamModel := mappedModel(req.Model, cfg)
	startTime := time.Now()

	sa := resolveTargetAuth(req.AuthID, req.StorageJSON)
	curPayload := mutateModel(req.Payload, upstreamModel)

	if sa == nil || sa.APIKey == "" {
		streamEmitError(req.StreamID, "no available zcode account")
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: streamHeaders()})
	}

	ctx, cancel := context.WithCancel(context.Background())
	httpReq, err := prepareUpstreamRequest(ctx, curPayload, sa, cfg)
	if err != nil {
		cancel()
		streamEmitError(req.StreamID, err.Error())
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: streamHeaders()})
	}

	resp, err := modelHTTPClient.Do(httpReq)
	if err != nil {
		cancel()
		streamEmitError(req.StreamID, err.Error())
		streamClose(req.StreamID)
		noteAccountFailure(sa.AuthID, 0, err.Error())
		return okEnvelope(streamResponse{Headers: streamHeaders()})
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		cancel()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		errMsg := fmt.Sprintf("upstream status %d: %s", resp.StatusCode, string(body))
		streamEmitError(req.StreamID, errMsg)
		streamClose(req.StreamID)
		noteAccountFailure(sa.AuthID, resp.StatusCode, errMsg)
		return okEnvelope(streamResponse{Headers: streamHeaders()})
	}

	// 异步读取并逐块推送
	go pumpUpstreamStream(ctx, cancel, req.StreamID, resp, sa.AuthID, req.Model, upstreamModel, startTime)

	return okEnvelope(streamResponse{Headers: streamHeaders()})
}

func resolveTargetAuth(reqAuthID string, storageJSON []byte) *StoredAuth {
	if len(storageJSON) > 0 {
		var sa StoredAuth
		if err := json.Unmarshal(storageJSON, &sa); err == nil && strings.TrimSpace(sa.APIKey) != "" {
			if sa.AuthID == "" {
				sa.AuthID = authIDFor(sa.APIKey)
			}
			if accountRoutable(&sa) {
				return &sa
			}
		}
	}

	reqAuthID = strings.TrimSpace(reqAuthID)
	if reqAuthID != "" {
		accounts, err := listAllAuthFiles()
		if err == nil {
			for _, acc := range accounts {
				if acc.AuthID == reqAuthID || authFileNameFor(acc.APIKey) == reqAuthID+".json" {
					if accountRoutable(acc) {
						return acc
					}
				}
			}
		}
	}

	// 从调度器选取
	if sa := pickActiveAuth(); sa != nil {
		return sa
	}

	// 兜底：若配置文件里写死了 api_key，构造单账号
	cfg := currentConfig()
	if cfg.APIKey != "" {
		return &StoredAuth{
			AuthID:   "zcode-config-static",
			APIKey:   cfg.APIKey,
			Label:    "Config API Key",
			Disabled: false,
		}
	}

	return nil
}

func prepareUpstreamRequest(ctx context.Context, payload []byte, sa *StoredAuth, cfg Config) (*http.Request, error) {
	reqURL := cfg.BaseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	fp := mimic.New(cfg.Mimic, keyID(sa.APIKey))
	fp.ApplyRequest(httpReq.Header, sa.APIKey, mimic.UUID4())
	return httpReq, nil
}

func executeOnce(payload []byte, sa *StoredAuth, cfg Config) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()

	httpReq, err := prepareUpstreamRequest(ctx, payload, sa, cfg)
	if err != nil {
		return nil, 0, err
	}

	resp, err := modelHTTPClient.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if errRead != nil {
		return nil, resp.StatusCode, errRead
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body))
	}

	return body, resp.StatusCode, nil
}

func mutateModel(raw []byte, model string) []byte {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return append([]byte(nil), raw...)
	}
	if model != "" {
		body["model"] = model
	}
	out, err := json.Marshal(body)
	if err != nil {
		return append([]byte(nil), raw...)
	}
	return out
}
