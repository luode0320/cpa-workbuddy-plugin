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
	"sync"
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

// defaultActivePingInterval 是定时探活对同一账号的最小间隔。
// defaultActivePingTimeout 限制单次探活请求的最长等待时间。
var defaultActivePingInterval = 4 * time.Hour
var defaultActivePingTimeout = 20 * time.Second

var (
	activePingMu        sync.Mutex
	lastActivePingTimes = make(map[string]time.Time)
)

// pingUpstreamOnceFn 是 pingUpstreamOnce 的可注入引用，供单元测试替换传输内核。
var pingUpstreamOnceFn = pingUpstreamOnce

// authModelsForIndexFn 是 authModelsForIndex 的可注入引用，供单元测试替换候选模型来源。
var authModelsForIndexFn = authModelsForIndex

// shouldActivePing 判断指定账号是否已超过定时探活间隔。
// [参数] authID: 账号标识。
// [返回] 允许探活返回 true。
func shouldActivePing(authID string) bool {
	activePingMu.Lock()
	defer activePingMu.Unlock()
	last, ok := lastActivePingTimes[authID]
	if !ok || time.Since(last) >= defaultActivePingInterval {
		return true
	}
	return false
}

// recordActivePing 记录指定账号的本次探活时间。
// [参数] authID: 账号标识。
// [返回] 无。
func recordActivePing(authID string) {
	activePingMu.Lock()
	defer activePingMu.Unlock()
	lastActivePingTimes[authID] = time.Now()
}

// resetActivePingTimes 清空探活节流记录。
// [参数] 无。
// [返回] 无。
func resetActivePingTimes() {
	activePingMu.Lock()
	defer activePingMu.Unlock()
	lastActivePingTimes = make(map[string]time.Time)
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

// pingUpstreamOnce 向上游发起一次流式首包即停探活，仅返回纯传输结果，不写入任何账号状态。
// [参数] sa: ZCode 账号凭据；model: 待测试模型名称（展示名）。
// [返回] ok 表示收到有效首包；statusCode 为 HTTP 状态码；errMsg 为失败原因；latencyMS 为耗时毫秒；ttftNS 为首包时延纳秒。
// 最近修改时间：2026-10-14 02:30:00 上游请求改用无整体超时的推理客户端
func pingUpstreamOnce(sa *StoredAuth, model string) (ok bool, statusCode int, errMsg string, latencyMS int64, ttftNS uint64) {
	cfg := currentConfig()

	// 1. 按账号类型选择上游端点与凭证（start-plan 走 zcode-plan + JWT，付费走 api.z.ai + 业务令牌）
	targets := resolveUpstreamTargets(sa, cfg)
	if len(targets) == 0 {
		return false, 0, "unable to resolve upstream credential", 0, 0
	}
	target := targets[0]
	upstreamModel := mappedModel(model, cfg)

	// 2. 构造流式最小探活负载（stream=true，max_tokens 抬升至全局输出底线）
	pingPayload := map[string]any{
		"model":      upstreamModel,
		"max_tokens": int(applyMaxTokensFloor(1)),
		"stream":     true,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
	}
	payloadBytes, _ := json.Marshal(pingPayload)

	reqURL := target.URL + "/v1/messages"
	ctx, cancel := context.WithTimeout(context.Background(), defaultActivePingTimeout)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		cancel()
		return false, 0, err.Error(), 0, 0
	}

	fp := mimic.New(cfg.Mimic, keyID(target.Credential))
	fp.ApplyRequest(httpReq.Header, target.Credential, mimic.UUID4())
	applyTargetAuth(httpReq.Header, target)

	// 3. 用推理客户端发起请求，收到首个数据块后立即异步关闭流，避免等待整段思考链完成
	start := time.Now()
	resp, err := inferenceHTTPClient.Do(httpReq)
	if err != nil {
		cancel()
		return false, 0, err.Error(), time.Since(start).Milliseconds(), 0
	}
	defer func() {
		go func() {
			_ = resp.Body.Close()
			cancel()
		}()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		latency := time.Since(start).Milliseconds()
		return false, resp.StatusCode, fmt.Sprintf("upstream status %d: %s", resp.StatusCode, string(bodyBytes)), latency, 0
	}

	// 4. 读取首个分片（512 字节）即视为流已建立，拦截流内显式错误帧
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	latency := time.Since(start).Milliseconds()
	firstChunk := string(buf[:n])
	ttft := uint64(latency * 1000000)
	if strings.Contains(firstChunk, `"type":"error"`) || strings.Contains(firstChunk, `"type": "error"`) {
		return false, http.StatusForbidden, fmt.Sprintf("upstream stream error: %s", firstChunk), latency, ttft
	}
	return true, resp.StatusCode, "", latency, ttft
}

// handleTestActiveWithAuth 向上游发起单次 Anthropic 流式首包即停真实探活。
// [参数] sa: ZCode 账号凭据；model: 待测试模型名称。
// [返回] testActiveResponse: 探活状态、耗时与结果消息。
// 最近修改时间：2026-10-13 23:00:00 复用 pingUpstreamOnce 纯传输内核
func handleTestActiveWithAuth(sa *StoredAuth, model string) testActiveResponse {
	if sa == nil || (sa.APIKey == "" && sa.Token == "" && sa.AccessToken == "") {
		return testActiveResponse{
			OK:     false,
			Status: "failed",
			Error:  "invalid account or empty api key",
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

	// 1. 发起纯传输探活
	start := time.Now()
	ok, statusCode, errMsg, latency, ttftNS := pingUpstreamOnceFn(sa, model)
	if !ok {
		sa.TestFailed = true
		saveAuthTestStatus(sa)
		noteAccountFailureForModel(sa.AuthID, upstreamModel, statusCode, errMsg)
		publishUsage(sa.AuthID, sa.Label, model, upstreamModel, start, usageTokensDetail{InputTokens: 1, OutputTokens: 0, TotalTokens: 1}, latency, statusCode, true, errMsg, ttftNS)
		return testActiveResponse{
			OK:        false,
			Status:    "failed",
			Model:     model,
			LatencyMS: latency,
			Error:     errMsg,
		}
	}

	// 2. 探活成功：清标 test_failed，重置 failover
	sa.TestFailed = false
	saveAuthTestStatus(sa)
	resetAccountFailover(sa.AuthID)
	publishUsage(sa.AuthID, sa.Label, model, upstreamModel, start, usageTokensDetail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, latency, statusCode, false, "", ttftNS)

	return testActiveResponse{
		OK:        true,
		Status:    "active",
		Model:     model,
		LatencyMS: latency,
		Message:   "活跃测试成功",
	}
}

// doActivePing 定时活跃探活：全部候选模型按稳定顺序依次轮测，任一成功即成功。
// [参数] sa: ZCode 账号凭据。
// [返回] 探活失败时的错误，成功返回 nil。
// 最近修改时间：2026-10-14 02:50:00 节流间隔改为 4 小时，且无论成败都刷新节流时间戳
func doActivePing(sa *StoredAuth) error {
	if sa == nil {
		return fmt.Errorf("nil storedAuth")
	}
	key := strings.TrimSpace(sa.AuthID)
	if key == "" {
		return fmt.Errorf("empty auth identifier")
	}

	// 1. 检查 4 小时节流限制
	if !shouldActivePing(key) {
		return nil
	}
	// 无论本轮成败都刷新节流时间戳：失败账号若不计时，会在每个 watchdog
	// tick（10 分钟）被反复全量重测，反而比健康账号更频繁地消耗账号配额。
	recordActivePing(key)

	// 2. 取全部候选模型按稳定顺序依次轮测
	models := authModelsForIndexFn(key)
	if len(models) == 0 {
		models = []string{"GLM-5.3"}
	}
	var lastErr error
	success := false
	lastModel := ""
	lastStatus := 0

	// 3. 依次测试候选模型，任意一个成功即刻退出
	for _, m := range models {
		if ok, statusCode, errMsg, latency, ttftNS := pingUpstreamOnceFn(sa, m); ok {
			success = true
			sa.TestFailed = false
			saveAuthTestStatus(sa)
			resetAccountFailover(sa.AuthID)
			upstreamModel := mappedModel(m, currentConfig())
			publishUsage(sa.AuthID, sa.Label, m, upstreamModel, time.Now(), usageTokensDetail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, latency, statusCode, false, "", ttftNS)
			break
		} else {
			lastErr = fmt.Errorf("%s: %s", m, errMsg)
			lastModel, lastStatus = m, statusCode
		}
	}

	// 4. 处理探活结果
	if !success {
		// 全部模型失败：打 test_failed 标签并计入账号级故障
		sa.TestFailed = true
		saveAuthTestStatus(sa)
		if lastErr == nil {
			lastErr = fmt.Errorf("active ping failed for all %d models", len(models))
		}
		// 必须带上真实模型记账：传空模型会把 failover_disable_model 写成空串，
		// 探活候选因此再也取不到可回放的模型，账号永久卡在禁用（2026-10-29
		// 事故同形）。lastModel 为空时退回候选列表首个模型，保证标记非空。
		if strings.TrimSpace(lastModel) == "" && len(models) > 0 {
			lastModel = models[0]
		}
		noteAccountFailureForModel(sa.AuthID, mappedModel(lastModel, currentConfig()), lastStatus, lastErr.Error())
		return lastErr
	}

	return nil
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
