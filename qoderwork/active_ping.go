// active_ping.go 实现面板「测试」按钮发起的手动活跃推理请求，以及禁用探活
// 复用的单账号探测原语 sendActivePingQoder。
//
// 自动定时探活（4 小时轮测健康账号）已于 2026-10-12 移除：上游对同账号突发调用
// 限流（返回 110 Billing daily count exceeded），每轮 5 模型轮测会稳定触发，且真实
// 推理与探活共享账号调用配额。
//
// 2026-10-22 起新增的是**只针对已被禁用账号**的 10 分钟探活（failover_disable.go）：
// 它的目的是把恢复的号放回池子，只探已知坏号、不碰健康号，且单账号当日失败达到
// 10 次即停到本地 0 点，因此不会重现当年「轮测健康账号把自己限流」的问题。
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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

// queueFrameError 表示本次活跃测试被上游排进容量队列，而非账号或凭据故障。
type queueFrameError struct {
	status int
	info   queueFrameInfo
	raw    string
}

// Error 返回上游错误文本。
// [返回] 形如 "upstream 200: 403: 10605: {...}" 的错误文本。
// 最近修改时间 2026-11-12 21:40:00；改动原因：排队帧单独成型后保持原错误文本，供日志对照。
func (e *queueFrameError) Error() string {
	return fmt.Sprintf("upstream %d: %s", e.status, e.raw)
}

// queueFrameNotice 把排队帧转成一句面板可读的中文提示。
// [参数] model：本次测试使用的模型名；info：解析出的结构化排队信息。
// [返回] 单行中文提示，明确「账号与凭据正常、稍后重试」，不含原始 JSON。
// 最近修改时间 2026-11-12 21:35:00；改动原因：排队帧此前只呈现一坨截断 JSON，被误读成凭据损坏。
func queueFrameNotice(model string, info queueFrameInfo) string {
	// 1. 组装队列分档与前方排队数，任一缺失则跳过
	detail := ""
	if info.QueueType != "" {
		detail = "队列 " + info.QueueType
	}
	if info.QueueCount > 0 {
		if detail != "" {
			detail += "，"
		}
		detail += fmt.Sprintf("前方 %d 个请求", info.QueueCount)
	}
	// 2. 拼出模型、排队明细与建议等待秒数
	notice := fmt.Sprintf("上游排队中 (模型: %s", model)
	if detail != "" {
		notice += "，" + detail
	}
	notice += ")"
	if wait := info.waitSeconds(); wait > 0 {
		notice += fmt.Sprintf("，约 %d 秒后可重试", wait)
	}
	return notice + "；账号与凭据正常，属上游容量排队而非测试失败"
}

// pickRandomQoderModels 随机选取指定数量的不重复模型列表供探活与测试。
// [参数] sa: 账号凭据，用于缓存缺失时拉取可用模型；limit: 期望选取的最大模型数。
// [返回] 随机打乱后的候选模型列表，至少包含一个兜底模型。
// 最近修改时间 2026-10-10（新增测试按钮指定模型选择与自动探活轮测支持）
func pickRandomQoderModels(sa *storedAuth, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	// 1. 从缓存或凭据提取候选模型并去重。缓存按账号分片：探活针对的是
	// sa 这个具体账号，只能读它自己的清单，否则会拿别的账号的模型去测。
	var accountKey string
	if sa != nil {
		accountKey = sa.Account.UID
	}
	models, _ := cachedDynamicModels(accountKey)
	if len(models) == 0 && sa != nil && strings.TrimSpace(sa.Auth.AccessToken) != "" {
		if dyn, err := callModelsAPI(sa); err == nil && len(dyn) > 0 {
			storeDynamicModels(accountKey, dyn)
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
// [参数] sa: 账号凭据；chosenModel: CPA 侧模型名（内部会映射为上游 key）。
// [返回] 请求建立成功返回 nil；排队帧返回 *queueFrameError，其余失败返回普通 error。
// 最近修改时间 2026-11-12 21:40:00（排队帧单独成型，供面板区分「上游拥堵」与「账号故障」）
func sendActivePingQoder(sa *storedAuth, chosenModel string) error {
	// 1. 构造流式探活请求体并完成 COSY 签名
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

	// 2. 非 2xx：整段读取错误体，排队帧同样成型后返回
	reader := newHostStreamReader(stream)
	if statusCode >= 400 {
		payload := readAllUpstreamErr(reader)
		// 排队帧可能由网关直接挂在非 2xx 响应体上，这里同样成型后再返回。
		if info, ok := parseQueueFrameInfo(payload); ok {
			return &queueFrameError{status: statusCode, info: info, raw: truncateRedacted(payload, 120)}
		}
		return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(payload, 120))
	}
	// 3. 2xx：读到首个有效业务帧才算成功，错误帧一律如实返回，禁止凭首包有字节盲判成功
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
			// 排队帧单独成型：调用方要能区分「上游此刻拥堵」与「这个号坏了」。
			if info, ok := parseQueueFrameInfo(msg); ok {
				return &queueFrameError{status: statusCode, info: info, raw: truncateRedacted(msg, 200)}
			}
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
// [返回] 测试结果 map；失败时由 buildActivePingFailure 折叠，排队帧带 upstream_queued。
// 最近修改时间 2026-11-12 21:40:00（失败结果改走 buildActivePingFailure，排队帧不再等同于测试失败）
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
		return buildActivePingFailure(chosenModel, err)
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	key := strings.TrimSpace(sa.Account.UID)
	if key == "" {
		key = authIndex
	}
	log.Printf("[qoderwork] manual active ping test success for %s with model %s (took %v)", key, chosenModel, elapsed)
	return map[string]any{
		"ok":      true,
		"model":   chosenModel,
		"elapsed": elapsed.String(),
		"message": fmt.Sprintf("活跃成功 (模型: %s, 耗时: %v)", chosenModel, elapsed),
	}
}

// buildActivePingFailure 把一次活跃测试失败折叠成面板可渲染的结果 map，排队帧带 upstream_queued。
// [参数] model：本次测试使用的模型名；err：sendActivePingQoder 返回的失败。
// [返回] 失败结果 map；命中排队帧时额外带 upstream_queued 与排队明细。
// 最近修改时间 2026-11-12 21:40:00；改动原因：排队帧被渲染成「测试失败」，误读为账号故障。
func buildActivePingFailure(model string, err error) map[string]any {
	// 1. 排队帧：给出可读中文提示与结构化排队明细，键名 upstream_queued 与运行时的「刷新排队数」区分
	var queueErr *queueFrameError
	if errors.As(err, &queueErr) {
		log.Printf("[qoderwork] manual active ping test queued for model %s: %v", model, err)
		out := map[string]any{
			"error":           queueFrameNotice(model, queueErr.info),
			"model":           model,
			"upstream_queued": true,
			"detail":          err.Error(),
		}
		if wait := queueErr.info.waitSeconds(); wait > 0 {
			out["retry_after_seconds"] = wait
		}
		if queueErr.info.QueueType != "" {
			out["queue_type"] = queueErr.info.QueueType
		}
		if queueErr.info.QueueCount > 0 {
			out["queue_count"] = queueErr.info.QueueCount
		}
		if queueErr.info.SubCode != "" {
			out["sub_code"] = queueErr.info.SubCode
		}
		if queueErr.info.ModelKey != "" {
			out["upstream_model"] = queueErr.info.ModelKey
		}
		return out
	}
	// 2. 其余失败维持改造前的文案与字段，不做任何语义变更
	return map[string]any{
		"error": fmt.Sprintf("活跃测试失败 (模型: %s): %v", model, err),
		"model": model,
	}
}
