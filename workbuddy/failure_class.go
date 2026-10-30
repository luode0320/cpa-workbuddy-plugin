// failure_class.go 把上游失败分类固化为纯函数，作为冷却维度与探活策略的单一判据。
//
// 背景：账号级冷却的既有实现（isAccountFailure / isAccountLevel4xx /
// isSoftRateLimit / isHardCreditError / isTransientThrottle）分散在
// accountFailover.go 与 policy.go 中，判据真实但缺少统一表达。后续要按
// 「账号维度 / 账号×模型维度 / 不冷却」三种落点记账，若各处自行组合判据，
// 迟早出现同一失败在不同路径被记到不同维度。
//
// 本模块只做分类，不做 I/O、不写状态。分类结果必须与历史记账门槛完全等价：
// 历史门槛是 isAccountFailure(status, body) || isTransientThrottle(status, body)，
// 等价性由 TestClassifyUpstreamFailureMatchesLegacyCountGate 全枚举锁定。
//
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增失败分类纯函数，固化实施总览第 3 节边界表。
package main

import (
	"net/http"
	"strings"
)

// failureScope 表示一次上游失败应记入的冷却维度。
// 零值 failureScopeNone 表示不进入任何冷却（请求形状问题或客户端主动断开）。
type failureScope int

const (
	// failureScopeNone 不冷却：换账号也会同样失败，或根本不是账号故障。
	failureScopeNone failureScope = iota
	// failureScopeAccount 账号级冷却：凭据、端点、额度或传输层问题，与模型无关。
	failureScopeAccount
	// failureScopeAuthModel 账号×模型级冷却（写盘禁用）：限流文案、模型拦截等
	// 需要摘掉该账号该模型的场景。
	failureScopeAuthModel
	// failureScopeModelRateLimit 模型级限流（只冷却模型、不禁用账号）。
	// 与 failureScopeAuthModel 的差别只在禁用侧：6004 表明账号整体健康，
	// 只是该模型当下限流，摘掉整个账号会把可用池无谓缩小。
	failureScopeModelRateLimit
	// failureScopeUpstreamBusy 上游拥堵：渠道/集群容量问题，不是账号故障。
	// 该维度既不写冷却也不写盘禁用——罚号只会缩小可用池并放大拥堵。
	failureScopeUpstreamBusy
)

// 上游拥堵相关的业务码。
const (
	// modelRateLimitCode 明确指向「模型级 429 限流」的业务 code：账号健康、
	// 只是这个模型此刻被限，切模型即可用。
	modelRateLimitCode = "6004"
	// queueFullCode 明确指向「渠道级容量满载」的业务 code：上游推理集群排队
	// 队列打满，同一时刻换任何账号都会撞同一堵墙。
	queueFullCode = "6021"
)

// String 返回分类的可读名，供日志与测试断言使用。
func (s failureScope) String() string {
	switch s {
	case failureScopeAccount:
		return "account"
	case failureScopeAuthModel:
		return "account_model"
	case failureScopeModelRateLimit:
		return "model_rate_limit"
	case failureScopeUpstreamBusy:
		return "upstream_busy"
	default:
		return "none"
	}
}

// recordsCooldown 报告该分类是否需要写入冷却状态。
// 上游拥堵刻意不写：它会在数秒内把整池健康号逐个冷却。
// [返回] 需要记账时返回 true。
// 最近修改时间：2026-10-31；改动原因：新增，表达拥堵维度不记账。
func (s failureScope) recordsCooldown() bool {
	return s != failureScopeNone && s != failureScopeUpstreamBusy
}

// disablesAccount 报告该分类是否需要把账号写盘禁用。
// 只有模型级限流例外：6004 是账号健康的证据，禁用会让健康号退出路由。
// [返回] 需要写盘禁用时返回 true。
// 最近修改时间：2026-10-31；改动原因：新增，堵住模型级限流误禁用健康号。
func (s failureScope) disablesAccount() bool {
	return s == failureScopeAccount || s == failureScopeAuthModel
}

// classifyUpstreamFailure 把一次上游失败归类到冷却维度。
//
// 判定顺序刻意与历史实现对齐，而不是按「状态码表」直觉排序：
// 历史门槛会先看正文标记再看状态码，因此 400 携带限流或额度文案时同样记账。
//
// 11128「请求被拦截」按账号×模型记账：该错误在单个账号上是请求被拦，
// 但同一模型跨账号等量出现时就是上游对该模型的拦截，换号同样失败。
// 两种成因的处置完全一致——冷却只给命中的那个模型记账，禁用则摘掉整个账号
// （双写，见 accountFailover.recordFailureScoped），恢复统一由 10 分钟禁用探活
// 裁决。因此这里不需要区分「账号级」还是「模型级」成因。
//
// [参数] status：上游 HTTP 状态码，0 表示传输层失败；body：上游响应正文或错误文本。
// [返回] 该失败应记入的冷却维度。
// 最近修改时间：2026-10-21；改动原因：冷却探活移除，改为冷却+禁用双写口径。
func classifyUpstreamFailure(status int, body string) failureScope {
	// 1. 硬额度与凭据/端点问题属账号级：换模型也不可用。
	//    硬额度优先，避免 429 + 额度标记被误判成限流。
	if isHardCreditError(status, body) || isAccountLevel4xxExcludingRateLimit(status) {
		return failureScopeAccount
	}
	// 2. 渠道级容量满载必须在通用限流兜底之前判定：它是比「限流」更具体的
	//    信号（同 14018 的具体优先哲学）。落进限流分支会按账号逐个冷却，
	//    高峰期可在数秒内把整池健康号摘光。
	if isQueueFullBody(body) {
		return failureScopeUpstreamBusy
	}
	// 3. 模型级限流（6004）：账号健康，只隔离该账号×模型，换模型即可用。
	//    必须同时先于通用限流与 5xx —— 6004 常以 429 或 5xx 承载。
	if isModelRateLimitBody(body) {
		return failureScopeModelRateLimit
	}
	// 4. 传输错误与 5xx 属账号级。
	if status == 0 || status >= 500 {
		return failureScopeAccount
	}
	// 5. 请求被拦截（11128）按账号×模型冷却：模型维度只记这一个模型，
	//    禁用探活会把它排在回放序列最前，验证该账号在这模型上是否真的恢复。
	//    先于限流文案判定，因为 11128 的 displayMsg 里可能带「请重新发送」
	//    这类无关文案。
	if isModelBlockedError(status, body) {
		return failureScopeAuthModel
	}
	// 6. 限流文案与零字节断流按账号×模型处理：这类信号通常随模型变化。
	//    顺序与历史一致——先判文案再判状态，故 400 携带限流文案时仍会记账。
	if isSoftRateLimit(status, body) || isEmptyStreamBody(body) {
		return failureScopeAuthModel
	}
	// 7. 其余不记账：400/422 等请求形状问题换账号同样失败。
	return failureScopeNone
}

// isAccountLevel4xxExcludingRateLimit 判定是否为不含 429 的账号级 4xx。
// 429 单独归入账号×模型维度，因此这里必须排除它，避免同一次失败被记两次口径。
// [参数] status：上游 HTTP 状态码。
// [返回] true 表示 401/403/404/405。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增。
func isAccountLevel4xxExcludingRateLimit(status int) bool {
	return isAccountLevel4xx(status) && status != http.StatusTooManyRequests
}

// isRequestShapeFailure 判定失败是否由请求本身引起，供调用方判断「不值得换号」。
// 与 classifyUpstreamFailure 的分工：本函数只看状态码语义，不参与记账门槛。
// [参数] status：上游 HTTP 状态码。
// [返回] true 表示 400/422 这类换账号也会同样失败的请求形状问题。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增。
func isRequestShapeFailure(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
}

// businessCodeMarkers 返回一个上游业务码在 JSON 里的全部常见书写形态。
// 上游同一 code 可能写成数字、带空格的数字或字符串包裹，逐个比对避免漏判。
// [参数] code：纯数字业务码文本，如 "6004"。
// [返回] 待匹配的形态列表。
// 最近修改时间：2026-10-31；改动原因：新增，供拥堵码识别复用。
func businessCodeMarkers(code string) []string {
	return []string{
		`"code":` + code,
		`"code": ` + code,
		`"code":"` + code + `"`,
		`"code": "` + code + `"`,
	}
}

// containsBusinessCode 报告 body 是否携带指定上游业务码。
// [参数] body：上游响应正文或错误帧文本；code：纯数字业务码。
// [返回] 命中任一书写形态时返回 true。
// 最近修改时间：2026-10-31；改动原因：新增，统一业务码匹配口径。
func containsBusinessCode(body, code string) bool {
	if body == "" {
		return false
	}
	for _, marker := range businessCodeMarkers(code) {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

// isModelRateLimitBody 报告 body 是否明确指向「模型级限流」。
//
// 上游用 code 6004 表达「该模型当下被限流」，与账号整体健康无关：同一个账号
// 换到别的模型即可正常使用。处置口径因此是「只隔离该账号×模型」，不得据此
// 摘掉整个账号（2026-10-31 起）。
//
// [参数] body：上游响应正文或错误帧文本。
// [返回] 命中 6004 语义时返回 true。
// 最近修改时间：2026-10-31；改动原因：新增，区分模型级限流与账号级故障。
func isModelRateLimitBody(body string) bool {
	return containsBusinessCode(body, modelRateLimitCode)
}

// isQueueFullBody 报告 body 是否指向「渠道级容量满载」。
//
// 上游用 code 6021 与固定文案 queue.full 表达推理集群排队队列打满。这不是
// 账号问题：同一时刻换任何账号都会撞同一堵墙，换号只会放大请求量。处置口径
// 是「不罚号、不换号、直接透传可读提示」（参考实现记录 2026-10-15 实案：
// 按普通限流处置会在 2 秒内把整池健康号逐个冷却）。
//
// 只匹配固定文案 queue.full / queue full，不做宽泛的 queue 子串匹配——
// 后者会把「queued for processing」这类正常排队语义也吞进来。
//
// [参数] body：上游响应正文或错误帧文本。
// [返回] 命中 6021 或 queue.full 文案时返回 true。
// 最近修改时间：2026-10-31；改动原因：新增，识别渠道级容量满载。
func isQueueFullBody(body string) bool {
	if body == "" {
		return false
	}
	if containsBusinessCode(body, queueFullCode) {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "queue.full") || strings.Contains(lower, "queue full")
}

// isTransientUpstreamFailure 报告一次失败是否属于「可能自愈的瞬时故障」。
//
// 判据只影响写盘禁用是否受连续失败门限约束，不影响分类与冷却：
//   - 瞬时（受门限约束）：传输错误、5xx、限流文案、零字节断流。这些信号在
//     高峰期成片出现，同一个号上可能只是恰好撞上上游抖动；
//   - 确定性（不受门限）：401/403/404/405 凭据或权限问题、402 额度耗尽、
//     11128 模型拦截。它们在同一个号上必然重演，等两次只会让用户多失败两次。
//
// [参数] status：上游 HTTP 状态码，0 表示传输层失败；body：上游响应正文。
// [返回] true 表示该失败可能在短时间内自愈。
// 最近修改时间：2026-10-31；改动原因：新增，给写盘禁用门限划出适用范围。
func isTransientUpstreamFailure(status int, body string) bool {
	// 确定性故障优先判定：它们即使带 5xx 或限流文案也不该等。
	if isModelBlockedError(status, body) || isHardCreditError(status, body) {
		return false
	}
	if isAccountLevel4xxExcludingRateLimit(status) {
		return false
	}
	return status == 0 || status >= 500 || isSoftRateLimit(status, body) || isEmptyStreamBody(body)
}

// isModelBlockedError 判定一次失败是否属于「上游拦截了这次请求」。
//
// 只认 11128（request illegal / 请求被拦截）。11140 是账号级封禁、6004 是
// 频率限制，两者分别由账号冷却与限流逻辑处理，不能混进这里。
//
// 该判据同时用于两个落点：失败分类（写入账号×模型冷却）与同请求换号决策
// （一次命中就够，不必把预算耗在其余账号上）。
//
// [参数] status：上游 HTTP 状态码；body：上游响应正文。
// [返回] 命中 11128 语义时返回 true。
// 最近修改时间：2026-10-20；改动原因：从 model_circuit.go 迁入，成为唯一判定点。
func isModelBlockedError(status int, body string) bool {
	if status != http.StatusBadRequest && status != http.StatusForbidden {
		return false
	}
	// 11140 = 账号级封禁，必须排除，否则会把封号误判成模型拦截。
	if strings.Contains(body, "11140") {
		return false
	}
	return strings.Contains(body, "11128")
}
