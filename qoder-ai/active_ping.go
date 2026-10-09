// active_ping.go 实现面板「测试」按钮发起的手动活跃推理请求。
//
// 与 workbuddy-provider 不同，qoderwork 不做定时自动探活（watchdog），
// 本文件只承载「点测试按钮 → 弹出模型小窗口 → 点选指定模型 → 发一次真实
// 推理请求」这一条手动链路。
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// defaultActivePingTimeout 限制单次手动测试请求的最长等待时间。
var defaultActivePingTimeout = 20 * time.Second

// sendActivePingQoderFn 允许测试替换真实发送实现。
var sendActivePingQoderFn = sendActivePingQoder

// pickRandomQoderModels 随机选取指定数量的不重复模型列表供手动测试。
// [参数] sa: 账号凭据，用于缓存缺失时拉取可用模型；limit: 期望选取的最大模型数。
// [返回] 随机打乱后的候选模型列表，至少包含一个兜底模型。
// 最近修改时间 2026-10-10（新增测试按钮指定模型选择支持，同步自 workbuddy）
func pickRandomQoderModels(sa *storedAuth, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	// 1. 从缓存或凭据提取候选模型并去重
	models, _ := cachedDynamicModels()
	if len(models) == 0 && sa != nil && strings.TrimSpace(sa.Auth.AccessToken) != "" {
		if dyn, err := callModelsAPI(sa); err == nil && len(dyn) > 0 {
			storeDynamicModels(dyn)
			models = dyn
		}
	}
	seen := make(map[string]struct{})
	var candidates []string
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		candidates = append(candidates, id)
	}

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

	return []string{"auto"}
}

// pickRandomQoderModel 随机选取单个模型。
// [参数] sa: 账号凭据。
// [返回] 选中的模型名称。
// 最近修改时间 2026-10-10（委托给 pickRandomQoderModels）
func pickRandomQoderModel(sa *storedAuth) string {
	res := pickRandomQoderModels(sa, 1)
	if len(res) > 0 {
		return res[0]
	}
	return "auto"
}

// sendActivePingQoder 对指定账号发送一次真实的推理请求用于活跃测试。
// [参数] sa: 账号凭据；chosenModel: CPA 侧模型名（内部会映射为上游 key）。
// [返回] 请求建立成功返回 nil，否则返回错误。
// 最近修改时间 2026-10-10（新增测试按钮指定模型选择支持，同步自 workbuddy）
func sendActivePingQoder(sa *storedAuth, chosenModel string) error {
	req := &openAIRequest{
		Model:    chosenModel,
		Messages: []openAIMessage{{Role: "user", Content: "hi"}},
		Stream:   true,
	}
	upstreamModel := cpaToUpstreamKey(stripProviderPrefix(chosenModel))
	body, err := buildQoderBody(req, upstreamModel, uiUserType(nil))
	if err != nil {
		return fmt.Errorf("build ping body: %w", err)
	}
	encodedBody := qoderEncode(body)

	ctx, cancel := context.WithTimeout(context.Background(), defaultActivePingTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointChat, strings.NewReader(encodedBody))
	if err != nil {
		return fmt.Errorf("create ping request: %w", err)
	}
	if err := applyCosyHeaders(httpReq, sa, encodedBody, endpointChat, upstreamModel, true); err != nil {
		return fmt.Errorf("cosy: %w", err)
	}

	stream, statusCode, _, err := hostHTTPDoStream(httpReq)
	if err != nil {
		return fmt.Errorf("hostHTTPDoStream: %w", err)
	}
	defer stream.Close()

	reader := newHostStreamReader(stream)
	if statusCode >= 400 {
		payload := readAllUpstreamErr(reader)
		return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(payload, 120))
	}
	// 读到首个数据块即视为流已建立；后续内容丢弃。
	buf := make([]byte, 512)
	_, _ = reader.Read(buf)
	return nil
}

// handleTestActive 处理面板「测试」按钮发起的单账号手动活跃推理请求。
// [参数] req: 管理请求，body 含 auth_index 与可选 model。
// [返回] 测试结果 map；缺少 auth_index 或凭据不可用时返回 {error}。
// 最近修改时间 2026-10-10（新增测试按钮指定模型选择支持，同步自 workbuddy）
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
// [参数] sa: 账号凭据；authIndex: 宿主账号索引；model: 指定模型（可空）。
// [返回] 测试结果 map。
// 最近修改时间 2026-10-10（新增测试按钮指定模型选择支持，同步自 workbuddy）
func handleTestActiveWithAuth(sa *storedAuth, authIndex, model string) map[string]any {
	if sa == nil {
		return map[string]any{"error": "账号凭据为空"}
	}
	start := time.Now()
	chosenModel := strings.TrimSpace(model)
	if chosenModel == "" {
		chosenModel = pickRandomQoderModel(sa)
	}
	if err := sendActivePingQoderFn(sa, chosenModel); err != nil {
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
	log.Printf("[qoder-ai] manual active ping test success for %s with model %s (took %v)", key, chosenModel, elapsed)
	return map[string]any{
		"ok":      true,
		"model":   chosenModel,
		"elapsed": elapsed.String(),
		"message": fmt.Sprintf("活跃成功 (模型: %s, 耗时: %v)", chosenModel, elapsed),
	}
}
