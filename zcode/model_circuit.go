// model_circuit.go 处理上游「请求被拦截」（11128）的记账落点。
//
// 历史沿革：本文件最初实现独立的模型级熔断状态机（跨 3 个账号命中 11128 后
// 在该 provider × 模型上打开 60 秒闸门）；2026-10-20 改为复用账号×模型冷却；
// 2026-10-25 冷却状态下线后，11128 与其它失败一样直接落成写盘禁用。
//
// 为什么 11128 仍然值得单独一个函数：它的判据（isModelBlockedError）与
// 「命中即停止换号」的决策绑定在一起，单独成函数让这条语义有明确归属。
//
// 最近修改时间：2026-10-25；改动原因：冷却状态下线，改为直接写盘禁用。
package main

import (
	"strings"
)

// noteModelBlockedHit 记录一次 11128 命中，把它落成写盘禁用。
//
// 与其它失败走同一个落点（recordFailureScoped），区别只在分类固定为
// failureScopeAuthModel：上游拦截的是「这个账号 × 这个模型」这个组合，
// 探活时应当回放同一个模型来验证是否恢复。
// [参数] authID: 命中的账号标识；model: 已归一化的上游模型名。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：改为直接写盘禁用。
func noteModelBlockedHit(authID, model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	recordFailureScoped(authID, model, failureScopeAuthModel, "upstream 11128 request illegal")
}
