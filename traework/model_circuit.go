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
	"sync"
	"time"
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

// modelCircuitCrossAuthThreshold 是触发模型级全局熔断的跨账号命中门限。
//
// 取值 3 与 2026-10-22 前的历史实现一致：单账号失败可能是个例，跨 3 个不同
// 账号在同一模型上命中同一类确定性失败，即证明该模型此刻整体不可用。
const modelCircuitCrossAuthThreshold = 3

// modelCircuitOpenWindow 是模型级全局熔断的开启窗口。
//
// 取 60 秒而不是指数阶梯：熔断的作用是止血（让用户在 60 秒内立刻拿到明确
// 失败，而不是逐个账号各等 15~30 秒），恢复仍由账号×模型冷却到期后重新探测。
// 窗口过长会让上游已经恢复时用户继续被拒。
const modelCircuitOpenWindow = 60 * time.Second

// modelCircuitMaxWindow 是退避上限。
//
// 取 30 分钟：上游模型级故障（通道下线、套餐失效、风控策略变更）通常以小时计，
// 但过长的窗口会让上游已恢复时用户继续拿不到该模型；30 分钟是与
// failoverCooldownMax 同量级的保守上界，且一次真实成功会立即清零。
const modelCircuitMaxWindow = 30 * time.Minute

// modelCircuitMu guards modelCircuitHits / modelCircuitAuths / modelCircuitUntil.
var (
	modelCircuitMu    sync.Mutex
	modelCircuitHits  = map[string]int{}
	modelCircuitAuths = map[string]map[string]struct{}{}
	modelCircuitUntil = map[string]time.Time{}
	// modelCircuitStreak 记录连续开闸次数，驱动窗口指数退避。
	//
	// 只有固定窗口是不够的（2026-11-11 生产实证）：glm-5.3-flashx 持续 100% 失败时，
	// 60s 窗口一到期流量就回来撞一轮（实测开闸 8 次、间隔 60~90s、cross_auth_hits
	// 从 3 涨到 10、15 分钟内 done=0 / attempt error=136）。窗口必须随连续失败次数
	// 增长，让已确认不可用的模型真正退到后台，而不是每分钟骚扰一次。
	modelCircuitStreak = map[string]int{}
)

// noteModelCircuitHit 记录一次确定性模型级失败，并在跨账号命中达门限时开闸。
//
// 为什么需要这一层（2026-11-11 生产实证）：content moderation（sensitive
// content）、11128 等确定性失败与账号健康无关，同一模型在所有账号上必然重演。
// 只写「账号×模型」冷却时，路由只能跳过被标记的那个组合，请求仍会逐个换号，
// 每个账号各付一次 15~30 秒的上游思考成本再失败：
// glm-5.3-flashx 部署后实测单请求 conductor duration p50=29.8s、p90=62.8s、
// max=153.0s，一个请求轮询 10 个不同账号。跨账号熔断让这类失败在命中第 3 个
// 账号后直接短路，把 153 秒压到一次失败往返。
//
// 与历史实现（2026-10-22 前）的差异：历史熔断只拒绝请求、不改变路由，被拦截
// 的账号仍会被宿主选中；现在账号×模型冷却已由 classifyUpstreamFailure 正确
// 写入（本版本修复），两层叠加才有意义——本函数负责止血，冷却负责让具体组合
// 退出路由。
//
// [参数] authID：命中账号标识；model：已归一化的上游模型名。
// [返回] true 表示该次命中已计入（跨账号去重后仍计数）。
// 最近修改时间：2026-11-11；改动原因：新增，堵住确定性失败逐个账号扇出。
func noteModelCircuitHit(authID, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	authID = strings.TrimSpace(authID)
	now := time.Now()
	modelCircuitMu.Lock()
	defer modelCircuitMu.Unlock()
	// 已开闸期间不重复计数：窗口内的后续失败不再延长熔断，
	// 否则持续流量会让熔断永不释放。
	if until, ok := modelCircuitUntil[model]; ok && now.Before(until) {
		return false
	}
	auths := modelCircuitAuths[model]
	if auths == nil {
		auths = map[string]struct{}{}
		modelCircuitAuths[model] = auths
	}
	if _, seen := auths[authID]; seen {
		return false
	}
	auths[authID] = struct{}{}
	modelCircuitHits[model]++
	if modelCircuitHits[model] >= modelCircuitCrossAuthThreshold {
		window := modelCircuitWindowFor(modelCircuitStreak[model])
		modelCircuitStreak[model]++
		modelCircuitUntil[model] = now.Add(window)
		log.Printf("[traework] model circuit opened: model=%s cross_auth_hits=%d streak=%d window=%s",
			model, modelCircuitHits[model], modelCircuitStreak[model], window)
	}
	return true
}

// modelCircuitWindowFor 按连续开闸次数给出指数退避窗口。
//
// 第 1 次开闸用基数 60s（抖动仍可能是个例，需要尽快恢复）；此后每多一次连续
// 失败就把窗口翻倍，上限 modelCircuitMaxWindow。一次成功即由 resetModelCircuit
// 把 streak 清零，所以短暂抖动不会累积惩罚。
//
// [参数] streak：本次开闸前的连续开闸次数（0 表示首次）。
// [返回] 该次熔断的窗口时长。
// 最近修改时间：2026-11-11；改动原因：新增，固定窗口导致持续失败模型每分钟重试。
func modelCircuitWindowFor(streak int) time.Duration {
	if streak <= 0 {
		return modelCircuitOpenWindow
	}
	window := modelCircuitOpenWindow
	for i := 0; i < streak && window < modelCircuitMaxWindow; i++ {
		window *= 2
	}
	if window > modelCircuitMaxWindow {
		window = modelCircuitMaxWindow
	}
	return window
}

// isModelCircuitOpen 报告该模型是否处于全局熔断窗口内。
//
// [参数] model：已归一化的上游模型名。
// [返回] true 表示应直接短路，不再换号。
// 最近修改时间：2026-11-11；改动原因：新增，供调度与换号判定使用。
func isModelCircuitOpen(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	modelCircuitMu.Lock()
	defer modelCircuitMu.Unlock()
	until, ok := modelCircuitUntil[model]
	if !ok {
		return false
	}
	if time.Now().Before(until) {
		return true
	}
	// 窗口到期：把跨账号额度降到门限以下，让该模型重新获得探测机会，
	// 但**保留 modelCircuitStreak**——连续开闸次数是退避窗口的依据，
	// 清零会让持续故障的模型永远停留在 60s 窗口（2026-11-11 生产实证：
	// glm-5.3-flashx 连续 15 分钟 100% 失败，开闸 8 次、每次间隔仅 60~90s）。
	// streak 只在一次真实成功（resetModelCircuit）时才清零。
	modelCircuitHits[model] = modelCircuitCrossAuthThreshold - 1
	modelCircuitAuths[model] = map[string]struct{}{}
	return false
}

// resetModelCircuit 在一次成功请求后清空该模型的熔断记账。
//
// 一次真实成功即证明该模型已恢复，残留的跨账号命中只会让下一次抖动立即再次开闸。
//
// [参数] model：已归一化的上游模型名。
// 最近修改时间：2026-11-11；改动原因：新增，成功即清账。
func resetModelCircuit(model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	modelCircuitMu.Lock()
	delete(modelCircuitUntil, model)
	delete(modelCircuitHits, model)
	delete(modelCircuitAuths, model)
	// streak 一并清零：真实成功是「该模型已恢复」的唯一证据，退避惩罚必须解除。
	delete(modelCircuitStreak, model)
	modelCircuitMu.Unlock()
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
