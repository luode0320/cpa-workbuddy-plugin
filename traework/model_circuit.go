// model_circuit.go 处理上游「请求被拦截」（11128）的记账落点。
//
// 历史沿革：本文件最初实现的是独立的模型级熔断状态机——跨 3 个账号命中
// 11128 后在该 provider × 模型上打开 60 秒闸门，闸门期间所有请求直接短路。
// 该设计解决了「整池轮转后才失败」的时间损耗，但留下一个缺口：熔断只是拒绝
// 请求，并不改变路由，被拦截的账号仍会被宿主选中，用户依旧每次都要等一个
// 失败往返。
//
// 现方案（2026-10-20 重构 + 2026-10-22 双写）：不再维护独立的熔断状态，11128 走
// 与其它失败相同的记账落点（classifyUpstreamFailure 返回 failureScopeAuthModel），
// 该落点双写两侧——账号×模型冷却阶梯 + 账号禁用写盘。路由层 isAuthModelCoolingDown
// 会跳过冷却中的账号×模型，禁用语义则让宿主选择器与同请求轮转一起让位；是否恢复
// 统一由 10 分钟禁用探活裁决（冷却不再自行探测）。
//
// 本文件只保留「一次 11128 命中该记到哪」这个判定，状态全部交给 accountFailover。
//
// 最近修改时间：2026-10-22；改动原因：冷却探活移除，注释对齐冷却+禁用双写口径。
package main

import (
	"log"
	"strings"
)

// noteModelBlockedHit 记录一次 11128 命中，双写账号×模型冷却与账号禁用。
//
// 与历史 noteModelCircuitHit 的差异：不再维护跨账号聚合与 blockedTill，
// 而是直接调用与其它失败相同的记账落点 recordFailureScoped。这样：
//  1. 冷却阶梯（60s 起、每次 +60s、上限 10 分钟）对 11128 同样生效；
//  2. 同一时刻把该账号写盘禁用，宿主选择器与同请求轮转一起被挡住——单靠
//     冷却做不到这点，因为宿主的 selector 读不到插件内存；
//  3. 路由与换号通过 isAuthModelCoolingDown / isAccountCoolingDown 自动跳过，
//     不需要额外的全局闸门；恢复由 10 分钟禁用探活回放该模型后统一裁决。
//
// [参数] authID：命中的账号标识；model：已归一化的上游模型名。
// [返回] true 表示已写入冷却并触发禁用。
// 最近修改时间：2026-10-22；改动原因：注释对齐双写与恢复通道变更。
func noteModelBlockedHit(authID, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	return recordFailureScoped(authID, model, failureScopeAuthModel)
}

// noteUpstreamBlockHit 是 11128 的统一记账入口，按正文区分「渠道级」与
// 「账号×模型级」。
//
// 为什么把区分放在这里而不是各调用点：executor.go 共有六处 11128 分支，任何
// 一处漏判都会重新打开「一次请求摘光全池」的口子。让调用点只传原始 status/body，
// 判据集中在本函数，新增调用点无法绕过。
//
// [参数] authID：命中的账号标识；model：冷却维度键；status：上游 HTTP 状态码；
// body：上游响应正文。
// [返回] true 表示已按账号×模型记账；渠道级拦截返回 false 且不产生任何状态。
// 最近修改时间：2026-11-07；改动原因：新增，渠道级拦截不再惩罚账号。
func noteUpstreamBlockHit(authID, model string, status int, body string) bool {
	if isChannelBlockError(status, body) {
		log.Printf("[traework] channel-level block (11128 unapproved channel): auth=%s model=%s",
			authID, strings.TrimSpace(model))
		return false
	}
	return noteModelBlockedHit(authID, model)
}

// resetModelBlockedState 在一次成功请求后清理该模型的残留冷却。
//
// 冷却状态按账号×模型存储。单账号成功由 resetAccountFailover 整账号清空
// （它同时解除盘上禁用）；这里额外清空该模型在全部账号上的记录，是因为一次成功
// 已证明「该模型当前可用」，留着其它账号的旧冷却只会让已恢复的模型继续被跳过。
//
// [参数] model：已归一化的上游模型名。
// 最近修改时间：2026-10-22；改动原因：注释对齐实际调用路径（resetAccountFailover）。
func resetModelBlockedState(model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	clearAuthModelCooldowns("", model)
}
