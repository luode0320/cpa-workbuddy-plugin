// active_ping.go 实现账号刷新时的自动活跃推理请求。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
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

// isInternalNonChatTraeModel 判断模型 ID 是否为上游 get_detail_param 返回的内部子代理或占位配置（直接调用 llm_utils_chat 会报 2001）。
// [参数] id: 模型配置名称。
// [返回] bool: 属于内部非对话配置返回 true。
// 最近修改时间：2026-10-11 21:55:00
func isInternalNonChatTraeModel(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "custom_model_placeholder", "browser_use_subagent", "computer_use_subagent", "file_search_agent", "explore_sub_agent_v2", "summary":
		return true
	default:
		return false
	}
}

// pickRandomTraeModels 随机选取指定数量的不重复 Trae 模型列表供面板单模型测试。
// [参数] sa: 账号凭据，用于缓存缺失时拉取可用模型；limit: 期望选取的最大模型数。
// [返回] 随机打乱后的候选模型列表，至少包含一个兜底模型。
// 最近修改时间：2026-10-11 21:55:00 过滤内部非对话子代理占位项
func pickRandomTraeModels(sa *traeAuth, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	// 1. 从缓存或凭据提取候选模型并去重（跳过内部非对话子代理占位项）
	candidates := traeCandidateModels(sa)

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

	return []string{"claude-3-5-sonnet"}
}

// orderedAllTraeModels 返回全部候选模型，按稳定发现顺序排列供定时探活依次轮测。
// [参数] sa: 账号凭据，用于缓存缺失时拉取可用模型。
// [返回] 按发现顺序去重后的完整候选模型列表，至少包含一个兜底模型。
// 最近修改时间：2026-10-13 23:00:00 定时探活改为全量模型按序测试
func orderedAllTraeModels(sa *traeAuth) []string {
	if candidates := traeCandidateModels(sa); len(candidates) > 0 {
		return candidates
	}
	return []string{"claude-3-5-sonnet"}
}

// traeCandidateModels 收集去重后的候选模型，缓存缺失时回源上游补齐，并跳过内部非对话占位项。
// [参数] sa: 账号凭据。
// [返回] 按发现顺序去重后的候选模型列表；无候选时返回空切片。
// 最近修改时间：2026-10-13 23:00:00 从 pickRandomTraeModels 抽出候选收集逻辑复用
func traeCandidateModels(sa *traeAuth) []string {
	seen := make(map[string]struct{})
	var candidates []string
	appendModel := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || isInternalNonChatTraeModel(id) {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		candidates = append(candidates, id)
	}

	// 1. 优先使用缓存/凭据中的模型
	for _, m := range dynamicTraeModelsFromCacheOrAuth() {
		appendModel(m.ID)
	}

	// 2. 缓存缺失时尝试调用上游模型接口补齐
	if len(candidates) == 0 && sa != nil {
		if dyn, err := fetchTraeModels(sa); err == nil && len(dyn) > 0 {
			storeTraeDynamicModels(dyn)
			for _, m := range dyn {
				appendModel(m.ID)
			}
		}
	}
	return candidates
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

// sendActivePingTrae 对指定账号发起一次真实的流式活跃探测请求。
// [参数] sa: Trae 账号凭据；chosenModel: 待测试的模型名称。
// [返回] error: 传输失败、HTTP 非 2xx 或 SSE 返回 event:error 时返回错误；收到首个有效 output/done 事件即刻成功返回 nil。
// 最近修改时间：2026-10-11 21:50:00 改用流式直连并在收到首个有效 output 时立即中止关闭流，消除思考模型生成整段思维链导致的 5~8 秒耗时
func sendActivePingTrae(sa *traeAuth, chosenModel string) error {
	// 1. 构造最小流式探活负载（stream=true，max_tokens 取缺省上限）
	// 2026-11-07：原传 1 依赖 buildTraePayload 内部的 applyMaxTokensFloor 抬值；
	// 该抬升已移除（会抵消客户端显式上限），探活改为显式传缺省上限，保持既有语义。
	msgs := toTraeMessages([]map[string]any{
		{"role": "user", "content": "hi"},
	})
	payload := buildTraePayload(msgs, chosenModel, true, int(streamDefaultMaxTokens), nil, nil, nil, "")

	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal ping payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultActivePingTimeout)

	// 2. 通过流式通道发起请求，避免 hostHTTPDo 全量读取等待数百个思考 token 生成完毕
	url := apiHostFor(sa) + llmUtilsChatPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		cancel()
		return fmt.Errorf("create ping request: %w", err)
	}
	req.Header = buildTraeAuthHeaders(sa)

	stream, statusCode, _, err := hostStreamDirectFn(req, raw)
	if err != nil {
		cancel()
		return fmt.Errorf("llm_utils_chat transport: %w", err)
	}
	host := apiHostFor(sa)
	defer func() {
		// 异步关闭底层流并预热下一条 Keep-Alive 连接，确保首包验证通过后零阻塞返回且下次点击免握手
		go func() {
			stream.Close()
			cancel()
			prewarmStreamHTTPClient(host)
		}()
	}()

	reader := newHostStreamReader(stream)
	if statusCode < 200 || statusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(reader, maxBodyBytes))
		return fmt.Errorf("upstream HTTP %d: %s", statusCode, truncateRedacted(string(body), 120))
	}

	// 3. 扫描 SSE 首包：收到首个有效 output 或 done 即刻返回，遇到 event:error 如实报错
	return verifyActivePingTraeStream(reader, statusCode)
}

// prewarmStreamHTTPClient 在后台向指定上游主机发送一次轻量 HEAD 请求以预热 TLS Keep-Alive 连接池。
// 仅在生产宿主环境（hostBridgeAvailable 为 true）触发，单测环境下直接跳过。
// [参数] apiHost: 上游 API 根地址。
// [返回] 无。
// 最近修改时间：2026-10-11 21:58:00
func prewarmStreamHTTPClient(apiHost string) {
	if !hostBridgeAvailable() || strings.TrimSpace(apiHost) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimRight(apiHost, "/")+"/", nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "node")
	req.Header.Set("Connection", "keep-alive")
	resp, err := streamHTTPClient().Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// verifyActivePingTraeStream 扫描探活 SSE 流，收到首个有效输出即刻退出，遇到错误事件返回错误。
// [参数] r: 上游 SSE 字节流；statusCode: 上游 HTTP 状态码。
// [返回] error: 遇到 event:error 或空流时返回错误，收到首个有效 output/done 返回 nil。
// 最近修改时间：2026-10-11 21:50:00 首包即停，避免等待完整思维链并拦截 HTTP 200 下的业务错误
func verifyActivePingTraeStream(r io.Reader, statusCode int) error {
	// 1. 初始化终态记录器并逐帧扫描 SSE
	var terminal traeSSETerminal
	err := scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			// 2. 收到首个有效正文或思考 token 即证明模型可用，立即终止扫描
			if _, _, _, ok := terminal.recordOutput(ev.Data); ok {
				return errTraeStreamDone
			}
		case "done":
			terminal.recordDone()
			return errTraeStreamDone
		case "error":
			// 3. 拦截 HTTP 200 下的业务错误（如 4008 额度耗尽、2001 模型无效、4011 限流）
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(msg, 200))
		}
		return nil
	}, terminal.hasPayload)
	if err != nil {
		return err
	}
	// 4. 校验是否收到至少一个有效输出或完成事件
	if !terminal.hasPayload() {
		return fmt.Errorf("upstream %d: invalid SSE response: missing output and done event", statusCode)
	}
	return nil
}

// doActivePing 自动定时探活主逻辑，支持多模型依次轮测与熔断。
// [参数] authIndex: 宿主 RPC 索引; authID: 账号唯一标识; sa: Trae 账号凭据对象。
// [返回] 探活失败时的错误，成功返回 nil。
// 最近修改时间：2026-10-14 02:50:00 节流间隔改为 4 小时，且无论成败都刷新节流时间戳
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

	// 1. 检查 4 小时节流限制
	if !shouldActivePing(key) {
		return nil
	}
	// 无论本轮成败都刷新节流时间戳：失败账号若不计时，会在每个 watchdog
	// tick（10 分钟）被反复全量重测，反而比健康账号更频繁地消耗账号配额。
	recordActivePing(key)

	// 2. 取全部候选模型按稳定顺序依次轮测
	models := orderedAllTraeModels(sa)
	var lastErr error
	var successModel string
	success := false

	// 3. 依次测试候选模型，任意一个成功即刻退出
	for _, m := range models {
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
			lastErr = fmt.Errorf("active ping failed for all %d models", len(models))
		}
		return lastErr
	}

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
