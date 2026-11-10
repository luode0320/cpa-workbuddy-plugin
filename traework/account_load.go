// account_load.go tracks how many upstream requests each account is currently
// serving, so the scheduler can spread concurrent load instead of piling every
// parallel sub-request of one conversation onto a single credential.
//
// 背景（2026-11-10 生产实测）：上游 Trae 给每个账号分配的并行槽位默认只有 2
// （付费账号按 entitlement 返回），超出的请求不会报错，而是排在账号侧队列里，
// 表现为首字延迟从 4.6s 涨到 7.8s 而不失败。本插件原先把并发交给宿主调度器，
// 实测 47 个账号里有 42 个的峰值并发超过 2、最高 25，远超槽位额度，这是首字
// 延迟的主要成因之一。这里把「当前在跑多少个」变成调度可见的事实，让选号按
// 真实负载平摊。
package main

import "sync"

var (
	// accountLoadMu guards accountLoad.
	accountLoadMu sync.Mutex
	// accountLoad maps host auth ID to the number of requests it is currently
	// serving. Entries are removed when the count returns to zero so the map
	// never accumulates dead accounts.
	accountLoad = make(map[string]int)
)

// accountLoadAcquire records that one request has been routed to authID.
// [参数] authID：宿主账号标识；空串直接忽略，返回空函数。
// [返回] 幂等的账函数；调用方必须在请求结束时调用它一次。
// 最近修改时间：2026-11-10 22:10:00；改动原因：新增，为调度层提供真实的账号在跑请求数。
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
// [参数] authID：宿主账号标识。
// [返回] 无。
// 最近修改时间：2026-11-10 22:10:00；改动原因：新增，与 accountLoadAcquire 配对使用。
func accountLoadRelease(authID string) {
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
// [参数] authID：宿主账号标识。
// [返回] 在跑请求数；未知账号返回 0。
// 最近修改时间：2026-11-10 22:10:00；改动原因：新增，供调度排序与诊断打点使用。
func accountLoadOf(authID string) int {
	accountLoadMu.Lock()
	defer accountLoadMu.Unlock()
	return accountLoad[authID]
}

// sessionAccountInflightSoftCap 是单个账号允许同时承接的请求软上限。
//
// 取值依据：上游 Trae 按 entitlement 给每个账号分配并行槽位，未知/免费账号默认只有
// 2 个；超过额度的请求不会失败，而是在账号侧排队，表现为首字延迟被推高。这里取 2
// 与上游默认槽位对齐——不是要精确模拟上游配额（插件无法读到真实 entitlement），
// 而是保证「多个账号可用时不会把并发堆到一个号上」。
const sessionAccountInflightSoftCap = 2

// pickLessLoadedThan 在 usable 里找在跑数严格小于 current 的账号，取最闲的一个。
// 要求「严格更闲」而不是「不比它忙」：所有号都满载时改绑没有任何收益，却会打断
// 会话粘性并在两个号之间来回抖，所以那种情况下宁愿保持原绑定。
// [参数] usable：可路由账号列表；exclude：不参与选择的账号标识；current：当前账号的在跑数。
// [返回] 选中账号标识；没有更闲的账号时返回空串。
// 最近修改时间：2026-11-10 22:10:00；改动原因：新增，供会话粘性超限时改绑使用。
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
// [返回] 无。
// 最近修改时间：2026-11-10 22:10:00；改动原因：新增，供单元测试隔离全局状态。
func clearAccountLoad() {
	accountLoadMu.Lock()
	accountLoad = make(map[string]int)
	accountLoadMu.Unlock()
}

// pickLeastLoaded 从 candidates 里挑在跑请求数最少的账号，平级按候选原有顺序取第一个。
// candidates 已由调用方按健康度过滤并排序，所以平级时保留原有优先级，不会把积分高的号
// 排到后面；这里也不再重复做健康判定，避免与调用方的过滤口径分叉。
// [参数] candidates：已筛过的可用账号。
// [返回] 选中的账号标识；无可用候选时返回空串。
// 最近修改时间：2026-11-10 22:10:00；改动原因：新增，替代「会话粘性把并发压到同一个号」的旧行为。
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
