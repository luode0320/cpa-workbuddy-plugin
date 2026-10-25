// policy.go owns the failure-classification and check-in verdict helpers.
// The status/body → failure mapping mirrors the workbuddy-provider plugin
// (accountFailover.go) with markers adapted to the Trae SOLO upstream
// (4011 rate-limit, quota markers, device-dedupe wording).
package main

import (
	"strings"
	"sync"
)

const httpStatusPaymentRequired = 402

// defaultMinRoutableCredits 是安全缓冲额度红线默认值（20 点）。
// 剩余额度低于该阈值的账号不再承载新请求，防止推理中途断开。
const defaultMinRoutableCredits int64 = 20

// defaultLowCreditDisableBelow 是积分硬禁用线的默认值：0 表示关闭。
//
// 刻意与 workbuddy 的默认 100 不同：traework 现有生产账号的 remain 落在 20–99
// 区间的不在少数，直接照搬会在首轮扫描时把它们全部写盘禁用，可用池骤减。
// 0 = kill switch，行为与 0.3.0 完全一致；需要硬禁用时显式配置该键。
const defaultLowCreditDisableBelow int64 = 0

var (
	minRoutableCredits   int64 = defaultMinRoutableCredits
	minRoutableCreditsMu sync.RWMutex

	lowCreditDisableBelow   int64 = defaultLowCreditDisableBelow
	lowCreditDisableBelowMu sync.RWMutex
)

// loadedMinRoutableCredits 返回当前生效的安全缓冲额度红线。
func loadedMinRoutableCredits() int64 {
	minRoutableCreditsMu.RLock()
	defer minRoutableCreditsMu.RUnlock()
	return minRoutableCredits
}

// loadedLowCreditDisableBelow 返回当前生效的积分硬禁用线；0 表示关闭。
// [参数] 无。
// [返回] 禁用线阈值。
// 最近修改时间：2026-10-25；改动原因：新增，积分三档分级的配置入口。
func loadedLowCreditDisableBelow() int64 {
	lowCreditDisableBelowMu.RLock()
	defer lowCreditDisableBelowMu.RUnlock()
	return lowCreditDisableBelow
}

// setLowCreditDisableBelow 设置积分硬禁用线并返回清理函数。
// [参数] n：阈值，负值按 0（关闭）处理。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-10-25；改动原因：新增，供 configure 与单测使用。
func setLowCreditDisableBelow(n int64) func() {
	if n < 0 {
		n = 0
	}
	lowCreditDisableBelowMu.Lock()
	old := lowCreditDisableBelow
	lowCreditDisableBelow = n
	lowCreditDisableBelowMu.Unlock()
	return func() {
		lowCreditDisableBelowMu.Lock()
		lowCreditDisableBelow = old
		lowCreditDisableBelowMu.Unlock()
	}
}

// creditTier 是积分三档分级的档位标识。
type creditTier string

const (
	// creditTierDisabled 低于硬禁用线：写盘 disabled，宿主级停用。
	creditTierDisabled creditTier = "disabled"
	// creditTierPreserve 低于保活线但高于禁用线：可路由，但排在健康号之后。
	creditTierPreserve creditTier = "preserve"
	// creditTierNormal 高于保活线：正常参与路由。
	creditTierNormal creditTier = "normal"
)

// String 返回档位的字符串形式，便于日志与 JSON 直接使用。
// [参数] 无。
// [返回] 档位名。
// 最近修改时间：2026-10-25；改动原因：新增。
func (t creditTier) String() string { return string(t) }

// creditTierFor 判定积分余量所属档位。
//
// 判定顺序必须先判禁用线：禁用一个号远比把它排到后面严重，阈值配反
// （禁用线 >= 保活线）时也应当优先保证「不会把有余额的号误禁」。
// [参数] remain：积分余量，负数表示未知；disableBelow：硬禁用线；preserveBelow：保活线。
// [返回] 该余量所属档位；未知余量返回 creditTierNormal（不参与任何降级）。
// 最近修改时间：2026-10-25；改动原因：新增，三档分级的唯一判定点。
func creditTierFor(remain, disableBelow, preserveBelow int64) creditTier {
	// 1. 未知积分不降级：没有数据不等于没钱，误判会把健康号踢出路由。
	if remain < 0 {
		return creditTierNormal
	}
	// 2. 低于硬禁用线：宿主级停用。必须与 isCreditsLowOrExhausted 同侧边界，
	// 否则分级判停用、路由判可用，落盘结果与实际行为互相打架。
	if disableBelow > 0 && remain < disableBelow {
		return creditTierDisabled
	}
	// 3. 低于保活线：保留可路由，仅降级排序。
	if preserveBelow > 0 && remain < preserveBelow {
		return creditTierPreserve
	}
	// 4. 其余正常。
	return creditTierNormal
}

// setMinRoutableCredits 设置安全缓冲额度红线并返回清理函数，供单测或动态配置使用。
func setMinRoutableCredits(n int64) func() {
	minRoutableCreditsMu.Lock()
	old := minRoutableCredits
	minRoutableCredits = n
	minRoutableCreditsMu.Unlock()
	return func() {
		minRoutableCreditsMu.Lock()
		minRoutableCredits = old
		minRoutableCreditsMu.Unlock()
	}
}

// hardCreditMarkers match upstream bodies that indicate the account's credit
// quota is exhausted (as opposed to a transient/rate-limit failure).
var hardCreditMarkers = []string{
	"credit", "quota", "额度", "积分不足", "余额不足", "已用完", "exhausted",
}

// isHardCreditError reports whether the upstream response indicates the
// account ran out of credits (402 or a marker in the body).
func isHardCreditError(status int, body string) bool {
	if status == httpStatusPaymentRequired {
		return true
	}
	lower := strings.ToLower(body)
	for _, m := range hardCreditMarkers {
		if strings.Contains(lower, strings.ToLower(m)) || strings.Contains(body, m) {
			return true
		}
	}
	return false
}

// isSoftRateLimit reports whether the response is a soft per-account rate
// limit (429 or a marker), distinct from a hard credit error.
func isSoftRateLimit(status int, body string) bool {
	if isHardCreditError(status, body) {
		return false
	}
	if status == 429 {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "throttl") ||
		strings.Contains(lower, "4011") // Trae WAF rate-limit event code
}

// isEmptyStreamBody reports whether the failure text describes an upstream
// zero-byte stream: the gateway accepted the connection and then closed it
// before delivering any payload (host-side empty_stream / the collect error
// for a 200 SSE body carrying neither output nor done). Pumps and collectors
// record such attempts with these canonical texts.
func isEmptyStreamBody(body string) bool {
	return strings.Contains(body, "closed before first payload") ||
		strings.Contains(body, "empty stream") ||
		strings.Contains(body, "invalid SSE response: missing output and done event")
}

// isTransientThrottle 判定失败是否属于上游瞬时过载信号：429 无额度标记、
// 软限流文案或上游零字节断流。2026-10-16 起这类失败与硬失败共用
// recordAccountFailure 的计数与阶梯冷却路径，不再单独豁免计数；本函数仅用于
// 把「HTTP 200 承载的零字节断流文案」等不命中 isAccountFailure 状态码判据的
// 失败纳入可记账范围。硬额度错误优先排除：429 + 额度标记属账号耗尽。
// 历史沿革：2026-09-06 曾因网关抖动连续零字节失败误隔离 4 个健康账号而把
// 这类失败改为「只冷却不计数」；异常池于 2026-09-08 移除后计数不再触发
// 冻结，2026-10-16 起软失败重新并入阶梯计数，代价是抖动期间账号会沿阶梯
// 冷却更久，由「一次成功即清零」兜底回落。
func isTransientThrottle(status int, body string) bool {
	if isHardCreditError(status, body) {
		return false
	}
	return isSoftRateLimit(status, body) || isEmptyStreamBody(body)
}

// DeviceBlocked reports whether the check-in body indicates a device-level
// block (Trae dedupes check-in per device per day).
func DeviceBlocked(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(body, "设备") ||
		strings.Contains(lower, "device") ||
		strings.Contains(lower, "machine")
}

// modelLevelFailureMarkers 匹配「上游模型/后端池整体不可用」的正文。
//
// 这类失败的成因在服务端模型侧，与具体账号无关：换号只会把同一个错误在池里
// 逐个重放一遍，最后既白烧额度又把整池账号禁用。workbuddy/qoderwork 的
// 「All backends failed」生产事故就是这个形态（单请求连禁 9 个号）。
//
// 刻意不含 Qoder 专属的 RESOURCE_LIMIT / 10605：Trae 上游没有排队帧概念，
// 误加会把真实的账号级资源受限一并吞掉。
var modelLevelFailureMarkers = []string{
	"all backends failed",
	"no available backend",
	"no backend available",
	"backend pool unavailable",
	"model not available",
	"model unavailable",
}

// isModelLevelFailure 判定失败是否属于模型级/后端池级故障。
//
// 调用方必须把它放在账号级判据之前：硬额度（402 / 额度标记）与凭据类 4xx
// 优先由 isHardCreditError / isAccountLevel4xx 认领，本函数只看正文语义。
// [参数] body：上游响应正文或错误文本。
// [返回] true 表示该失败与账号无关，不应计入账号故障，也不应换号重试。
// 最近修改时间：2026-10-25；改动原因：新增，堵住模型级故障被记成账号级、整池自禁。
func isModelLevelFailure(body string) bool {
	if body == "" {
		return false
	}
	lower := strings.ToLower(body)
	for _, m := range modelLevelFailureMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// AlreadyCheckedIn reports whether the body indicates the account already
// checked in today (as opposed to a device-level block).
func AlreadyCheckedIn(body string) bool {
	if DeviceBlocked(body) {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(body, "已签到") ||
		strings.Contains(body, "已经签到") ||
		strings.Contains(body, "明日再来") ||
		strings.Contains(body, "今日已完成") ||
		strings.Contains(body, "已领取") ||
		strings.Contains(lower, "already") ||
		strings.Contains(lower, "checked") ||
		strings.Contains(lower, "claimed")
}
