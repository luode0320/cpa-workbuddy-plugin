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
	// failureScopeAuthModel 账号×模型级冷却：限流或业务错误帧通常按账号+模型生效。
	failureScopeAuthModel
)

// String 返回分类的可读名，供日志与测试断言使用。
func (s failureScope) String() string {
	switch s {
	case failureScopeAccount:
		return "account"
	case failureScopeAuthModel:
		return "account_model"
	default:
		return "none"
	}
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
	// 2. 传输错误与 5xx 属账号级。
	if status == 0 || status >= 500 {
		return failureScopeAccount
	}
	// 3. 请求被拦截（11128）按账号×模型冷却：模型维度只记这一个模型，
	//    禁用探活会把它排在回放序列最前，验证该账号在这模型上是否真的恢复。
	//    先于限流文案判定，因为 11128 的 displayMsg 里可能带「请重新发送」
	//    这类无关文案。
	if isModelBlockedError(status, body) {
		return failureScopeAuthModel
	}
	// 4. 限流文案与零字节断流按账号×模型处理：这类信号通常随模型变化。
	//    顺序与历史一致——先判文案再判状态，故 400 携带限流文案时仍会记账。
	if isSoftRateLimit(status, body) || isEmptyStreamBody(body) {
		return failureScopeAuthModel
	}
	// 5. 其余不记账：400/422 等请求形状问题换账号同样失败。
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
