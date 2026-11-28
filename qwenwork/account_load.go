// account_load.go 统计每个账号「此刻正在服务多少个请求」，让调度层按真实负载
// 平摊并发，而不是把同一会话的并发子请求全压到一个账号上。
//
// 背景（2026-11-28 生产取证，QwenWork）：上游按账号分配并行槽位，**超出额度的
// 请求不会报错，而是在账号侧静默排队**，表现为首字延迟升高而错误率健康。
// 本插件原先的选号只看「轮询轮到谁」与「会话绑定在谁身上」：
//  1. pickActiveAuth：activeAuthRR 单调轮询，与真实负载完全解耦；
//  2. pickSessionAuth：粘性命中后**无条件**复用绑定号，同一会话的并发子请求因此
//     全部压到同一个号上；
//  3. 没有任何地方记录「某个账号正在服务多少个请求」，调度层天然看不到负载。
//
// 生产观测（0.2.24 生效后，同账号 + 同一会话 + 99.8% 前缀缓存命中）：首字仍在
// **5.9s~23.2s** 之间大幅波动，而上下文规模、账号、会话、缓存命中四个维度与首字
// 均无相关性 —— 与「少量会话的并发子请求被压到同一个号、超过上游槽位后排队」
// 的形态完全一致（同仓 traework 插件 2026-11-10 已就同一机制落地 0.5.42 修复，
// 见 doc/4-bugs/2026-11-08_210000_traework首字延迟与生成耗时偏高根因分析.md 第十九~二十一节）。
//
// 这里把「当前在跑多少个」变成调度可见的事实，让选号按真实负载平摊。
package main

import "sync"

var (
	// accountLoadMu guards accountLoad.
	accountLoadMu sync.Mutex
	// accountLoad 把宿主 auth ID 映射到它当前正在服务的请求数；计数归零即删除条目，
	// 避免 map 无限积累死账号。
	accountLoad = make(map[string]int)
)

// accountLoadAcquire 记录「有一个请求被路由到 authID」。
//
// [参数] authID：宿主账号标识；空串直接忽略，返回空函数。
// [返回] 幂等的账函数；调用方必须在请求结束时调用它一次。
// 最近修改时间：2026-11-28；改动原因：新增，为调度层提供真实的账号在跑请求数。
func accountLoadAcquire(authID string) func() {
	if authID == "" {
		return func() {}
	}
	accountLoadMu.Lock()
	accountLoad[authID]++
	accountLoadMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { accountLoadRelease(authID) })
	}
}

// accountLoadRelease 把 authID 的在跑请求数减一，计数归零时删除条目。
//
// [参数] authID：宿主账号标识。
// [返回] 无。
// 最近修改时间：2026-11-28；改动原因：新增，与 accountLoadAcquire 配对使用。
func accountLoadRelease(authID string) {
	if authID == "" {
		return
	}
	accountLoadMu.Lock()
	defer accountLoadMu.Unlock()
	n := accountLoad[authID] - 1
	if n <= 0 {
		delete(accountLoad, authID)
		return
	}
	accountLoad[authID] = n
}

// accountLoadOf 读取 authID 当前在跑的请求数。
//
// [参数] authID：宿主账号标识。
// [返回] 在跑请求数；未知账号返回 0。
// 最近修改时间：2026-11-28；改动原因：新增，供调度排序与诊断打点使用。
func accountLoadOf(authID string) int {
	accountLoadMu.Lock()
	defer accountLoadMu.Unlock()
	return accountLoad[authID]
}

// sessionAccountInflightSoftCap 是单个账号允许同时承接的请求软上限。
//
// 取值依据：上游按账号分配并行槽位，超出额度的请求不报错而是在账号侧排队，
// 表现为首字延迟被推高。这里取 2 与上游默认槽位对齐——不是要精确模拟上游配额
// （插件读不到真实 entitlement），而是保证「多个账号可用时不会把并发堆到一个号上」。
const sessionAccountInflightSoftCap = 2

// pickLessLoadedThan 在 usable 里找在跑数严格小于 current 的账号，取最闲的一个。
//
// 要求「严格更闲」而不是「不比它忙」：所有号都满载时改绑没有收益，却会打断会话
// 粘性并在两个号之间来回抖，所以那种情况下宁愿保持原绑定。
//
// [参数] usable：可路由账号列表；exclude：不参与选择的账号标识；current：当前账号的在跑数。
// [返回] 选中账号标识；没有更闲的账号时返回空串。
// 最近修改时间：2026-11-28；改动原因：新增，供会话粘性超限时改绑使用。
func pickLessLoadedThan(usable []string, exclude string, current int) string {
	bestID := ""
	bestLoad := 0
	for _, id := range usable {
		if id == "" || id == exclude {
			continue
		}
		n := accountLoadOf(id)
		if n >= current {
			continue
		}
		if bestID == "" || n < bestLoad {
			bestID = id
			bestLoad = n
		}
	}
	return bestID
}

// clearAccountLoad 清空全部在跑计数。测试辅助；生产路径不调用。
//
// [返回] 无。
// 最近修改时间：2026-11-28；改动原因：新增，供单元测试隔离全局状态。
func clearAccountLoad() {
	accountLoadMu.Lock()
	accountLoad = make(map[string]int)
	accountLoadMu.Unlock()
}

// pickLeastLoaded 从 candidates 里挑在跑请求数最少的账号，平级按候选原有顺序取第一个。
//
// candidates 已由调用方按健康度过滤并排序，所以平级时保留原有优先级，不会把积分高的号
// 排到后面；这里也不再重复做健康判定，避免与调用方的过滤口径分叉。
//
// [参数] candidates：已筛过的可用账号。
// [返回] 选中的账号标识；无可用候选时返回空串。
// 最近修改时间：2026-11-28；改动原因：新增，替代「会话粘性把并发压到同一个号」的旧行为。
func pickLeastLoaded(candidates []activeAuthCandidate) string {
	bestID := ""
	bestLoad := 0
	for _, c := range candidates {
		if c.ID == "" || c.Disabled || c.Exhausted {
			continue
		}
		n := accountLoadOf(c.ID)
		if bestID == "" || n < bestLoad {
			bestID = c.ID
			bestLoad = n
		}
	}
	return bestID
}
