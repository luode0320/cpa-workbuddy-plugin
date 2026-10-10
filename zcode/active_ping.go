package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/zcode/internal/mimic"
	"github.com/tidwall/gjson"
)

type testActiveRequest struct {
	AuthIndex string `json:"auth_index"`
	Model     string `json:"model"`
}

type testActiveResponse struct {
	OK        bool   `json:"ok"`
	Status    string `json:"status"`
	Model     string `json:"model,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleModelsQuery 处理 GET /models?auth_index= 路由请求。
func handleModelsQuery(authID string) ([]byte, error) {
	models := authModelsForIndex(authID)
	return json.Marshal(map[string]any{
		"models": models,
	})
}

// handleTestActive 处理 POST /test-active 请求。
func handleTestActive(body []byte) ([]byte, error) {
	authIndex := gjson.GetBytes(body, "auth_index").String()
	if authIndex == "" {
		authIndex = gjson.GetBytes(body, "auth_id").String()
	}
	model := gjson.GetBytes(body, "model").String()

	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return json.Marshal(testActiveResponse{
			OK:     false,
			Status: "failed",
			Error:  "auth_index is required",
		})
	}

	accounts, err := listAllAuthFiles()
	if err != nil || len(accounts) == 0 {
		return json.Marshal(testActiveResponse{
			OK:     false,
			Status: "failed",
			Error:  "no accounts found",
		})
	}

	var target *StoredAuth
	for _, acc := range accounts {
		if acc.AuthID == authIndex || authFileNameFor(acc.APIKey) == authIndex+".json" {
			target = acc
			break
		}
	}

	if target == nil {
		return json.Marshal(testActiveResponse{
			OK:     false,
			Status: "failed",
			Error:  fmt.Sprintf("account %s not found", authIndex),
		})
	}

	res := handleTestActiveWithAuth(target, model)
	return json.Marshal(res)
}

// handleTestActiveWithAuth 向上游发起单次 Anthropic 短消息真实探活。
func handleTestActiveWithAuth(sa *StoredAuth, model string) testActiveResponse {
	if sa == nil || sa.APIKey == "" {
		return testActiveResponse{
			OK:     false,
			Status: "failed",
			Error:  "invalid account or empty API key",
		}
	}

	cfg := currentConfig()
	if strings.TrimSpace(model) == "" {
		models := authModelsForIndex(sa.AuthID)
		if len(models) > 0 {
			model = models[0]
		} else {
			model = "GLM-5.3"
		}
	}
	upstreamModel := mappedModel(model, cfg)

	pingPayload := map[string]any{
		"model":      upstreamModel,
		"max_tokens": 10,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
	}
	payloadBytes, _ := json.Marshal(pingPayload)

	reqURL := cfg.BaseURL + "/v1/messages"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return testActiveResponse{
			OK:     false,
			Status: "failed",
			Model:  model,
			Error:  err.Error(),
		}
	}

	fp := mimic.New(cfg.Mimic, keyID(sa.APIKey))
	fp.ApplyRequest(httpReq.Header, sa.APIKey, mimic.UUID4())

	start := time.Now()
	resp, err := modelHTTPClient.Do(httpReq)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		sa.TestFailed = true
		saveAuthTestStatus(sa)
		noteAccountFailure(sa.AuthID, 0, err.Error())
		return testActiveResponse{
			OK:        false,
			Status:    "failed",
			Model:     model,
			LatencyMS: latency,
			Error:     err.Error(),
		}
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		sa.TestFailed = true
		saveAuthTestStatus(sa)
		noteAccountFailure(sa.AuthID, resp.StatusCode, string(bodyBytes))
		return testActiveResponse{
			OK:        false,
			Status:    "failed",
			Model:     model,
			LatencyMS: latency,
			Error:     fmt.Sprintf("upstream status %d: %s", resp.StatusCode, string(bodyBytes)),
		}
	}

	// 探活成功：清标 test_failed，重置 failover
	sa.TestFailed = false
	saveAuthTestStatus(sa)
	resetAccountFailover(sa.AuthID)

	return testActiveResponse{
		OK:        true,
		Status:    "active",
		Model:     model,
		LatencyMS: latency,
		Message:   "活跃测试成功",
	}
}

// saveAuthTestStatus 保存账号探活测试状态落盘。
func saveAuthTestStatus(sa *StoredAuth) {
	if sa == nil || sa.APIKey == "" {
		return
	}
	var fileName string
	if sa.AuthID != "" {
		authID := strings.TrimSpace(sa.AuthID)
		if !strings.HasPrefix(authID, zcodeAuthFilePrefix) {
			authID = zcodeAuthFilePrefix + authID
		}
		fileName = authID + ".json"
	} else {
		fileName = authFileNameFor(sa.APIKey)
	}
	fullPath := filepath.Join(getAccountsDir(), fileName)
	sa.UpdatedAt = time.Now().Format(time.RFC3339)
	_ = writeAuthFileDirect(fullPath, sa)
}
