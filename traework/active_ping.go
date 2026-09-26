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
	triggerActivePingFn  = doActivePing
	sendActivePingTraeFn = sendActivePingTrae
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

func pickRandomTraeModel(sa *traeAuth) string {
	models := dynamicTraeModelsFromCacheOrAuth()
	var candidates []string
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id != "" {
			candidates = append(candidates, id)
		}
	}

	if len(candidates) == 0 && sa != nil {
		if dyn, err := fetchTraeModels(sa); err == nil && len(dyn) > 0 {
			storeTraeDynamicModels(dyn)
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

	return "claude-3-5-sonnet"
}

func sendActivePingTrae(sa *traeAuth, chosenModel string) error {
	msgs := toTraeMessages([]map[string]any{
		{"role": "user", "content": "hi"},
	})
	payload := buildTraePayload(msgs, chosenModel, false, 5, nil, nil, nil, "")

	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal ping payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := apiHostFor(sa) + llmUtilsChatPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("create ping request: %w", err)
	}
	req.Header = buildTraeAuthHeaders(sa)

	resp, err := hostHTTPDo(req)
	if err != nil {
		return fmt.Errorf("llm_utils_chat transport: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, truncateRedacted(string(resp.Body), 120))
	}
	return nil
}

func doActivePing(authIndex, authID string, sa *traeAuth) error {
	if sa == nil {
		return fmt.Errorf("nil traeAuth")
	}

	key := strings.TrimSpace(authID)
	if key == "" {
		key = strings.TrimSpace(sa.UserID)
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

	chosenModel := pickRandomTraeModel(sa)
	if err := sendActivePingTraeFn(sa, chosenModel); err != nil {
		return err
	}

	recordActivePing(key)
	log.Printf("[traework] active ping success for %s with model %s", key, chosenModel)
	return nil
}

func triggerActivePing(authIndex, authID string, sa *traeAuth) error {
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

func handleTestActiveWithAuth(sa *traeAuth, authIndex string) map[string]any {
	if sa == nil {
		return map[string]any{"error": "账号凭据为空"}
	}
	start := time.Now()
	chosenModel := pickRandomTraeModel(sa)
	if err := sendActivePingTraeFn(sa, chosenModel); err != nil {
		return map[string]any{
			"error": fmt.Sprintf("活跃测试失败 (模型: %s): %v", chosenModel, err),
			"model": chosenModel,
		}
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	key := strings.TrimSpace(sa.UserID)
	if key == "" {
		key = authIndex
	}
	recordActivePing(key)
	log.Printf("[traework] manual active ping test success for %s with model %s (took %v)", key, chosenModel, elapsed)
	return map[string]any{
		"ok":      true,
		"model":   chosenModel,
		"elapsed": elapsed.String(),
		"message": fmt.Sprintf("活跃成功 (模型: %s, 耗时: %v)", chosenModel, elapsed),
	}
}
