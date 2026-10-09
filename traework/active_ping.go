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
var defaultActivePingTimeout = 20 * time.Second
var defaultActivePingMaxBudget = 30 * time.Second
var defaultActivePingMaxModels = 5

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

// pickRandomTraeModels 随机选取指定数量的不重复 Trae 模型列表供探活轮测。
// [参数] sa: 账号凭据，用于缓存缺失时拉取可用模型；limit: 期望选取的最大模型数。
// [返回] 随机打乱后的候选模型列表，至少包含一个兜底模型。
// 最近修改时间：2026-10-09 23:45:00 支持多模型随机轮测
func pickRandomTraeModels(sa *traeAuth, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	// 1. 从缓存或凭据提取候选模型并去重
	models := dynamicTraeModelsFromCacheOrAuth()
	seen := make(map[string]struct{})
	var candidates []string
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id != "" {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				candidates = append(candidates, id)
			}
		}
	}

	// 2. 缓存缺失时尝试调用上游模型接口补齐
	if len(candidates) == 0 && sa != nil {
		if dyn, err := fetchTraeModels(sa); err == nil && len(dyn) > 0 {
			storeTraeDynamicModels(dyn)
			for _, m := range dyn {
				id := strings.TrimSpace(m.ID)
				if id != "" {
					if _, ok := seen[id]; !ok {
						seen[id] = struct{}{}
						candidates = append(candidates, id)
					}
				}
			}
		}
	}

	// 3. 随机打乱并截取前 limit 个模型
	if len(candidates) > 0 {
		shuffled := append([]string(nil), candidates...)
		for i := len(shuffled) - 1; i > 0; i-- {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
			if err == nil {
				j := int(n.Int64())
				shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
			}
		}
		if len(shuffled) > limit {
			shuffled = shuffled[:limit]
		}
		return shuffled
	}

	return []string{"claude-3-5-sonnet"}
}

// pickRandomTraeModel 随机选取单个模型。
// [参数] sa: 账号凭据。
// [返回] 选中的模型名称。
// 最近修改时间：2026-10-09 23:45:00 委托给 pickRandomTraeModels
func pickRandomTraeModel(sa *traeAuth) string {
	res := pickRandomTraeModels(sa, 1)
	if len(res) > 0 {
		return res[0]
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

	ctx, cancel := context.WithTimeout(context.Background(), defaultActivePingTimeout)
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

// doActivePing 自动定时探活主逻辑，支持多模型依次轮测与熔断。
// [参数] authIndex: 宿主 RPC 索引; authID: 账号唯一标识; sa: Trae 账号凭据对象。
// [返回] 探活失败时的错误，成功返回 nil。
// 最近修改时间：2026-10-09 23:45:00 支持最多 5 个模型轮测与 30s 熔断
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

	// 1. 检查 30 分钟节流限制
	if !shouldActivePing(key) {
		return nil
	}

	// 2. 选取最多 5 个模型并设定整轮熔断截止时间
	models := pickRandomTraeModels(sa, defaultActivePingMaxModels)
	deadline := time.Now().Add(defaultActivePingMaxBudget)
	var lastErr error
	var successModel string
	success := false

	// 3. 依次测试候选模型，任意一个成功即刻退出
	for _, m := range models {
		if time.Now().After(deadline) {
			break
		}
		if err := sendActivePingTraeFn(sa, m); err == nil {
			success = true
			successModel = m
			break
		} else {
			lastErr = err
		}
	}

	// 4. 处理探活结果
	if !success {
		// 定时活跃测试全部失败：仍有积分的账号打 test_failed 标签
		if idx, idxErr := hostAuthIndexForPhys(key); idxErr == nil {
			if cr, ok := cachedCredits(authID); ok && cr != nil && cr.TotalRemain > 0 {
				if tagErr := persistTestFailedToggle(idx, authID, true); tagErr != nil {
					log.Printf("[traework] test_failed tag %s failed: %v", authID, tagErr)
				}
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("active ping timeout after %v", defaultActivePingMaxBudget)
		}
		return lastErr
	}

	recordActivePing(key)
	// 定时活跃测试成功：清除既有 test_failed 标签
	if idx, idxErr := hostAuthIndexForPhys(key); idxErr == nil {
		if err := persistTestFailedToggle(idx, authID, false); err != nil {
			log.Printf("[traework] test_failed clear %s failed: %v", authID, err)
		}
	}
	log.Printf("[traework] active ping success for %s with model %s", key, successModel)
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
		Model     string `json:"model"`
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
	return handleTestActiveWithAuth(sa, authIndex, strings.TrimSpace(body.Model))
}

// handleTestActiveWithAuth 对指定账号发起一次手动活跃推理。
// model 非空时使用调用方指定的模型；为空时回退到随机模型，兼容旧面板。
func handleTestActiveWithAuth(sa *traeAuth, authIndex, model string) map[string]any {
	if sa == nil {
		return map[string]any{"error": "账号凭据为空"}
	}
	start := time.Now()
	chosenModel := strings.TrimSpace(model)
	if chosenModel == "" {
		chosenModel = pickRandomTraeModel(sa)
	}
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
