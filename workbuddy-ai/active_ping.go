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

var defaultActivePingInterval = 4 * time.Hour
var defaultActivePingTimeout = 20 * time.Second

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

// pickRandomWorkbuddyModels 随机选取指定数量的不重复模型列表供面板单模型测试。
// [参数] sa: 账号凭据，用于缓存缺失时拉取可用模型；limit: 期望选取的最大模型数。
// [返回] 随机打乱后的候选模型列表，至少包含一个兜底模型。
// 最近修改时间：2026-10-09 23:40:00 支持多模型随机轮测
func pickRandomWorkbuddyModels(sa *storedAuth, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	// 1. 从缓存或凭据提取候选模型并去重
	candidates := workbuddyCandidateModels(sa)

	// 2. 随机打乱并截取前 limit 个模型
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

	return []string{"deepseek-v4.1-flash"}
}

// orderedAllWorkbuddyModels 返回全部候选模型，按稳定发现顺序排列供定时探活依次轮测。
// [参数] sa: 账号凭据，用于缓存缺失时拉取可用模型。
// [返回] 按发现顺序去重后的完整候选模型列表，至少包含一个兜底模型。
// 最近修改时间：2026-10-13 23:00:00 定时探活改为全量模型按序测试
func orderedAllWorkbuddyModels(sa *storedAuth) []string {
	if candidates := workbuddyCandidateModels(sa); len(candidates) > 0 {
		return candidates
	}
	return []string{"deepseek-v4.1-flash"}
}

// workbuddyCandidateModels 收集去重后的候选模型，缓存缺失时回源上游补齐。
// [参数] sa: 账号凭据。
// [返回] 按发现顺序去重后的候选模型列表；无候选时返回空切片。
// 最近修改时间：2026-10-13 23:00:00 从 pickRandomWorkbuddyModels 抽出候选收集逻辑复用
func workbuddyCandidateModels(sa *storedAuth) []string {
	seen := make(map[string]struct{})
	var candidates []string
	appendModel := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		candidates = append(candidates, id)
	}

	// 1. 优先使用缓存/凭据中的模型
	for _, m := range dynamicModelsFromCacheOrAuth() {
		appendModel(m.ID)
	}

	// 2. 缓存缺失时尝试调用上游模型接口补齐
	if len(candidates) == 0 && sa != nil && strings.TrimSpace(sa.Auth.AccessToken) != "" {
		if dyn, err := callModelsAPI(sa.Auth.AccessToken, sa.Account.EnterpriseID); err == nil && len(dyn) > 0 {
			storeDynamicModels(dyn)
			for _, m := range dyn {
				appendModel(m.ID)
			}
		}
	}
	return candidates
}

// pickRandomWorkbuddyModel 随机选取单个模型。
// [参数] sa: 账号凭据。
// [返回] 选中的模型名称。
// 最近修改时间：2026-10-09 23:40:00 委托给 pickRandomWorkbuddyModels
func pickRandomWorkbuddyModel(sa *storedAuth) string {
	res := pickRandomWorkbuddyModels(sa, 1)
	if len(res) > 0 {
		return res[0]
	}
	return "deepseek-v4.1-flash"
}

func sendActivePingWorkbuddy(sa *storedAuth, chosenModel string) error {
	reqMap := map[string]any{
		"model": chosenModel,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
		"stream": true,
		// 探活只需首帧即可断开；取 GPT 系下限 16 是为了同时兼容 gpt- 系列
		// （<16 会被上游 11133 拒绝）。旧实现写 5 再经统一地板抬到 32768，
		// 属自研控制副作用，2026-11-07 随地板一并删除。
		"max_tokens": int(gptMinMaxTokens),
	}
	raw, err := json.Marshal(reqMap)
	if err != nil {
		return fmt.Errorf("marshal ping req: %w", err)
	}

	body := prepareUpstreamBody(raw, nil, sa, chosenModel)

	ctx, cancel := context.WithTimeout(context.Background(), defaultActivePingTimeout)
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

// doActivePing 自动定时探活主逻辑：全部候选模型按稳定顺序依次轮测。
// [参数] authIndex: 宿主 RPC 索引; authID: 账号唯一标识; sa: 账号凭据对象。
// [返回] 探活失败时的错误，成功返回 nil。
// 最近修改时间：2026-10-14 02:50:00 节流间隔改为 4 小时，且无论成败都刷新节流时间戳
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

	// 1. 检查 4 小时节流限制
	if !shouldActivePing(key) {
		return nil
	}
	// 无论本轮成败都刷新节流时间戳：失败账号若不计时，会在每个 watchdog
	// tick（10 分钟）被反复全量重测，反而比健康账号更频繁地消耗账号配额。
	recordActivePing(key)

	// 2. 取全部候选模型按稳定顺序依次轮测
	models := orderedAllWorkbuddyModels(sa)
	var lastErr error
	var successModel string
	success := false

	// 3. 依次测试候选模型，任意一个成功即刻退出
	for _, m := range models {
		if err := sendActivePingWorkbuddyFn(sa, m); err == nil {
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
			if cr := cachedCredits(authID); cr != nil && cr.TotalRemain > 0 {
				if tagErr := persistTestFailedToggle(idx, authID, true); tagErr != nil {
					log.Printf("[workbuddy] test_failed tag %s failed: %v", authID, tagErr)
				}
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("active ping failed for all %d models", len(models))
		}
		return lastErr
	}

	// 定时活跃测试成功：清除既有 test_failed 标签
	if idx, idxErr := hostAuthIndexForPhys(key); idxErr == nil {
		if err := persistTestFailedToggle(idx, authID, false); err != nil {
			log.Printf("[workbuddy] test_failed clear %s failed: %v", authID, err)
		}
	}
	log.Printf("[workbuddy] active ping success for %s with model %s", key, successModel)
	return nil
}

func triggerActivePing(authIndex, authID string, sa *storedAuth) error {
	return triggerActivePingFn(authIndex, authID, sa)
}

// handleTestActive 处理面板“测试”按钮发起的单账号手动活跃推理请求。
// 手动测试绕过 4 小时节流限制，直接发起真实请求并返回测试结果。
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
func handleTestActiveWithAuth(sa *storedAuth, authIndex, model string) map[string]any {
	if sa == nil {
		return map[string]any{"error": "账号凭据为空"}
	}
	start := time.Now()
	chosenModel := strings.TrimSpace(model)
	if chosenModel == "" {
		chosenModel = pickRandomWorkbuddyModel(sa)
	}
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
