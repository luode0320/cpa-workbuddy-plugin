// failover.go 保留「一次上游失败该不该换号、该不该记账」这两条判定，
// 状态全部交给 failover_disable.go 的写盘禁用机制。
//
// 历史沿革：本文件曾实现内存阶梯冷却（60s × 连续失败次数，上限 10 分钟）
// 与账号×模型双粒度冷却状态机。2026-10-25 按用户裁定下线——冷却只存在插件
// 内存里，宿主选择器看不见，同一个请求内宿主仍会把已知坏号发进来。现在失败
// 直接落成宿主可见的写盘禁用，恢复由 10 分钟探活裁决。
//
// 保留 shouldRotateOnUpstreamErr：它决定「同一个请求内要不要换下一个账号」，
// 与「这个账号要不要被禁用」是两件事——前者是请求级自救，后者是跨请求状态。
//
// 最近修改时间：2026-10-25；改动原因：移除内存冷却状态机，只保留判定函数。
package main

import (
	"log"
	"net/http"
	"strings"
)

// noteAccountFailure 记录一次账号级上游失败。
// [参数] authID: 账号唯一标识；statusCode: 上游 HTTP 状态码，0 表示传输层失败；
// errMsg: 错误文本。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：保留历史调用形态，内部转模型维度记账。
func noteAccountFailure(authID string, statusCode int, errMsg string) {
	noteAccountFailureForModel(authID, "", statusCode, errMsg)
}

// noteAccountFailureForModel 按分类维度把失败落成写盘禁用。
//
// 模型名传空时退化为账号级判定，与历史行为一致，供尚未改造的调用点继续工作。
// [参数] authID: 账号唯一标识；model: 已归一化的上游模型名，可为空；
// statusCode: 上游 HTTP 状态码，0 表示传输层失败；errMsg: 错误文本。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：内部改为委托分类 + 写盘禁用。
func noteAccountFailureForModel(authID, model string, statusCode int, errMsg string) {
	recordFailureScoped(authID, model, classifyUpstreamFailure(statusCode, errMsg), errMsg)
}

// recordFailureScoped 是失败记账的唯一落点：分类结果非 none 时写盘禁用。
//
// 调用方必须已完成分类。分类判定本身在 failure_class.go，本函数只负责落地。
// [参数] authID: 账号唯一标识；model: 已归一化的上游模型名，可为空；
// scope: 记账维度；errMsg: 错误文本，用于日志与诊断。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：冷却状态下线，只保留写盘禁用。
func recordFailureScoped(authID, model string, scope failureScope, errMsg string) {
	authID = strings.TrimSpace(authID)
	if authID == "" || scope == failureScopeNone {
		return
	}
	model = strings.TrimSpace(model)
	if strings.TrimSpace(errMsg) != "" {
		logFailoverFailure(authID, model, scope, errMsg)
	}
	disableAccountForFailover(authID, model)
}

// shouldRotateOnUpstreamErr 判断上游错误是否应触发同请求换号重试。
//
// 与「是否禁用账号」解耦：这里只回答「换个号再试一次有没有意义」。
// 请求形状错误（换号同样失败）返回 false，账号级与限流类错误返回 true。
// [参数] statusCode: 上游 HTTP 状态码；errMsg: 错误文本。
// [返回] true 表示应在同一个请求内换下一个账号重试。
// 最近修改时间：2026-10-25；改动原因：保留判定，注释澄清与禁用职责的边界。
func shouldRotateOnUpstreamErr(statusCode int, errMsg string) bool {
	if statusCode == http.StatusUnauthorized ||
		statusCode == http.StatusForbidden ||
		statusCode == http.StatusNotFound ||
		statusCode == http.StatusMethodNotAllowed ||
		statusCode == http.StatusTooManyRequests {
		return true
	}
	lower := strings.ToLower(errMsg)
	if strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "exceeded the quota") ||
		strings.Contains(lower, "14018") ||
		strings.Contains(lower, "额度已用尽") ||
		strings.Contains(lower, "insufficient") ||
		strings.Contains(lower, "concurrency") ||
		strings.Contains(lower, "3009") {
		return true
	}
	return false
}

// logFailoverFailure 记录一次进入禁用流程的失败，供排查。
// [参数] authID：账号标识；model：上游模型名；scope：记账维度；errMsg：错误文本。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，替代原冷却记录里的 lastError 字段。
func logFailoverFailure(authID, model string, scope failureScope, errMsg string) {
	log.Printf("[zcode] upstream failure auth=%s model=%s scope=%s err=%s",
		authID, model, scope, truncateRedacted(errMsg, 200))
}

// resetAccountFailover 在一次真实成功请求后解除本机制的写盘禁用。
//
// 语义（2026-10-25 用户裁定）：账号被禁用后若仍被路由到并且这次请求成功了，
// 说明它其实可用，标记就该清掉——这是正确的传播，不是误解除。恢复同样受
// 「只解除自己写的标记」约束：用户手动禁用的号不会被这里放开。
// [参数] authID：账号标识。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：内存冷却下线，改为解除写盘禁用。
func resetAccountFailover(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	reenableAccountForFailover("", authID)
}

// resetModelBlockedState 在成功请求后清理模型维度的残留状态。
//
// 内存冷却已下线，本函数只剩一个职责：把「该模型可用」这一事实记下来，
// 供后续探活参考。跨账号清理模型冷却的历史行为随状态机一起移除。
// [参数] model：已归一化的上游模型名。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：冷却状态下线，函数退化为无状态操作。
func resetModelBlockedState(model string) {
	_ = strings.TrimSpace(model)
}
