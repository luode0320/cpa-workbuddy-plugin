// active_ping.go 实现面板「测试」按钮发起的手动活跃推理请求。
//
// 自动定时探活已于 2026-10-12 移除：上游对同账号突发调用限流（返回 110
// Billing daily count exceeded），每轮 5 模型轮测会稳定触发，且真实推理与
// 探活共享账号调用配额，故不再由插件主动发起任何推理请求——只保留用户在
// 面板主动点击「测试」时的单次调用。
package main

import (
	"bufio"
	"bytes"
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

// defaultActivePingTimeout 限制单次手动活跃测试请求的最长等待时间。
var defaultActivePingTimeout = 20 * time.Second

var sendActivePingQoderFn = sendActivePingQoder

// pickRandomQoderModels 随机选取指定数量的不重复模型列表供探活与测试。
// [参数] sa: 账号凭据，用于缓存缺失时拉取可用模型；limit: 期望选取的最大模型数。
// [返回] 随机打乱后的候选模型列表，至少包含一个兜底模型。
// 最近修改时间 2026-10-10（新增测试按钮指定模型选择与自动探活轮测支持）
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
		if len(models) == 0 {
			models = resolveModels(nil, getConfiguredModels(), wbModels())
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
// [参数] sa: 账号凭据；chosenModel: CPA 侧模型名（透传给上游）。
// [返回] 请求建立成功返回 nil，否则返回错误。
// 最近修改时间 2026-10-18（改为 OpenAI 兼容最小请求体，透传路径不再走模板）
func sendActivePingQoder(sa *storedAuth, chosenModel string) error {
	body, err := json.Marshal(map[string]any{
		"model":    cpaToUpstreamKey(chosenModel),
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
		"stream":   true,
	})
	if err != nil {
		return fmt.Errorf("build ping body: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultActivePingTimeout)
	defer cancel()

	sess, errSess := ensureMaasSession(sa, "")
	if errSess != nil {
		return fmt.Errorf("maas: %w", errSess)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, maasInferenceURL(sess, "/v1/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create ping request: %w", err)
	}
	applyMaasHeaders(httpReq, sess)

	// 面板探活是控制面探测，不绑定任何客户端请求的 callback context。
	stream, statusCode, _, err := hostHTTPDoStream(httpReq, "")
	if err != nil {
		return fmt.Errorf("hostHTTPDoStream: %w", err)
	}
	defer stream.Close()

	reader := newHostStreamReader(stream)
	if statusCode >= 400 {
		payload := readAllUpstreamErr(reader)
		return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(payload, 120))
	}
	// 读到首个有效业务帧才算流建立成功；错误帧/顶层 code 排队帧一律如实返回
	// 失败，严禁凭首包有字节流出盲判成功（HTTP 200 假成功盲区）。
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		chunkJSON, isDone := parseQoderSSEChunk(scanner.Text())
		if isDone {
			return nil
		}
		if chunkJSON == "" {
			continue
		}
		if msg := sseErrorFrame(chunkJSON); msg != "" {
			return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("upstream stream read error: %w", err)
	}
	return fmt.Errorf("upstream stream closed before first payload")
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
	log.Printf("[qwenwork] manual active ping test success for %s with model %s (took %v)", key, chosenModel, elapsed)
	return map[string]any{
		"ok":      true,
		"model":   chosenModel,
		"elapsed": elapsed.String(),
		"message": fmt.Sprintf("活跃成功 (模型: %s, 耗时: %v)", chosenModel, elapsed),
	}
}
