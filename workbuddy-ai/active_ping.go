// active_ping.go 实现账号刷新时的自动活跃推理请求。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var defaultActivePingInterval = 30 * time.Minute

var (
	activePingMu        sync.Mutex
	lastActivePingTimes = make(map[string]time.Time)
)

var (
	triggerActivePingFn       = doActivePing
	sendActivePingWorkbuddyFn = sendActivePingWorkbuddy
)

func shouldActivePing(authKey string) bool {
	activePingMu.Lock()
	defer activePingMu.Unlock()
	last, ok := lastActivePingTimes[authKey]
	if !ok || time.Since(last) >= defaultActivePingInterval {
		return true
	}
	return false
}

func recordActivePing(authKey string) {
	activePingMu.Lock()
	defer activePingMu.Unlock()
	lastActivePingTimes[authKey] = time.Now()
}

func resetActivePingTimes() {
	activePingMu.Lock()
	defer activePingMu.Unlock()
	lastActivePingTimes = make(map[string]time.Time)
}

func pickRandomWorkbuddyModel(sa *storedAuth) string {
	models := dynamicModelsFromCacheOrAuth()
	var candidates []string
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id != "" {
			candidates = append(candidates, id)
		}
	}

	if len(candidates) == 0 && sa != nil && strings.TrimSpace(sa.Auth.AccessToken) != "" {
		if dyn, err := callModelsAPI(sa.Auth.AccessToken); err == nil && len(dyn) > 0 {
			storeDynamicModels(dyn)
			for _, m := range dyn {
				id := strings.TrimSpace(m.ID)
				if id != "" {
					candidates = append(candidates, id)
				}
			}
		}
	}

	if len(candidates) > 0 {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
		if err == nil {
			return candidates[n.Int64()]
		}
		return candidates[0]
	}

	return "deepseek-v4.1-flash"
}

func sendActivePingWorkbuddy(sa *storedAuth, chosenModel string) error {
	reqMap := map[string]any{
		"model": chosenModel,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
		"stream":     true,
		"max_tokens": 5,
	}
	raw, err := json.Marshal(reqMap)
	if err != nil {
		return fmt.Errorf("marshal ping req: %w", err)
	}

	body := prepareUpstreamBody(raw, nil, sa, chosenModel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointChatFor(sa), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create ping request: %w", err)
	}
	backendHeaders(httpReq, sa)

	stream, statusCode, _, err := hostHTTPDoStream(httpReq)
	if err != nil {
		return fmt.Errorf("hostHTTPDoStream: %w", err)
	}
	defer stream.Close()

	if statusCode >= 400 {
		reader := newHostStreamReader(stream)
		errPayload := readAllUpstreamErr(reader)
		return fmt.Errorf("upstream HTTP %d: %s", statusCode, truncateRedacted(errPayload, 120))
	}

	reader := newHostStreamReader(stream)
	buf := make([]byte, 512)
	_, _ = reader.Read(buf)
	return nil
}

func doActivePing(authIndex, authID string, sa *storedAuth) error {
	if sa == nil {
		return fmt.Errorf("nil storedAuth")
	}

	key := strings.TrimSpace(authID)
	if key == "" {
		key = strings.TrimSpace(sa.Account.UID)
	}
	if key == "" {
		key = strings.TrimSpace(authIndex)
	}
	if key == "" {
		return fmt.Errorf("empty auth identifier")
	}

	if !shouldActivePing(key) {
		return nil
	}

	chosenModel := pickRandomWorkbuddyModel(sa)
	if err := sendActivePingWorkbuddyFn(sa, chosenModel); err != nil {
		// 定时活跃测试失败：仍有积分的账号打 test_failed 标签（面板可过滤
		// 并人工清理），积分未知或已耗尽的账号不打。落盘失败只留告警，
		// 不影响刷新主链路。
		if idx, idxErr := hostAuthIndexForPhys(key); idxErr == nil {
			if cr := cachedCredits(authID); cr != nil && cr.TotalRemain > 0 {
				if tagErr := persistTestFailedToggle(idx, authID, true); tagErr != nil {
					log.Printf("[workbuddy] test_failed tag %s failed: %v", authID, tagErr)
				}
			}
		}
		return err
	}

	recordActivePing(key)
	// 定时活跃测试成功：清除既有 test_failed 标签（幂等，字段不存在时
	// 不落盘）。落盘失败只留告警，不影响刷新主链路。
	if idx, idxErr := hostAuthIndexForPhys(key); idxErr == nil {
		if err := persistTestFailedToggle(idx, authID, false); err != nil {
			log.Printf("[workbuddy] test_failed clear %s failed: %v", authID, err)
		}
	}
	log.Printf("[workbuddy] active ping success for %s with model %s", key, chosenModel)
	return nil
}

func triggerActivePing(authIndex, authID string, sa *storedAuth) error {
	return triggerActivePingFn(authIndex, authID, sa)
}

// handleTestActive 处理面板“测试”按钮发起的单账号手动活跃推理请求。
// 手动测试绕过 30 分钟节流限制，直接发起真实请求并返回测试结果。
func handleTestActive(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required"}
	}
	sa, err := hostAuthGet(authIndex)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("获取凭据失败: %v", err)}
	}
	return handleTestActiveWithAuth(sa, authIndex)
}

func handleTestActiveWithAuth(sa *storedAuth, authIndex string) map[string]any {
	if sa == nil {
		return map[string]any{"error": "账号凭据为空"}
	}
	start := time.Now()
	chosenModel := pickRandomWorkbuddyModel(sa)
	if err := sendActivePingWorkbuddyFn(sa, chosenModel); err != nil {
		return map[string]any{
			"error": fmt.Sprintf("活跃测试失败 (模型: %s): %v", chosenModel, err),
			"model": chosenModel,
		}
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	key := strings.TrimSpace(sa.Account.UID)
	if key == "" {
		key = authIndex
	}
	recordActivePing(key)
	log.Printf("[workbuddy] manual active ping test success for %s with model %s (took %v)", key, chosenModel, elapsed)
	return map[string]any{
		"ok":      true,
		"model":   chosenModel,
		"elapsed": elapsed.String(),
		"message": fmt.Sprintf("活跃成功 (模型: %s, 耗时: %v)", chosenModel, elapsed),
	}
}
