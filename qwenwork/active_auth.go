// active_auth.go tracks the panel-selected QwenWork account used for routing.
//
// Region is always CN for QwenWork —
// no per-request JWT iss decode. Default: first available candidate. When the
// active account is exhausted/disabled/missing, randomly switch to another
// non-exhausted candidate and remember the choice.
package main

import (
	"strings"
	"sync"
	"time"
)

var (
	activeAuthID string
	activeAuthMu sync.RWMutex
)

func getActiveAuthID() string {
	activeAuthMu.RLock()
	defer activeAuthMu.RUnlock()
	return strings.TrimSpace(activeAuthID)
}

func setActiveAuthID(id string) {
	id = strings.TrimSpace(id)
	activeAuthMu.Lock()
	activeAuthID = id
	activeAuthMu.Unlock()
}

// clearActiveAuthIfMatch clears the selection when the given auth is removed.
func clearActiveAuthIfMatch(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	activeAuthMu.Lock()
	if activeAuthID == id {
		activeAuthID = ""
	}
	activeAuthMu.Unlock()
}

// activeAuthCandidate is a thin view used by pickActiveAuth.
// Exhausted 由调度入口按请求模型预先判定（免费模型下 0 积分账号不计耗尽），
// picker 只消费该标记，不再重复查积分，避免二次判定把免费模型账号挡掉。
type activeAuthCandidate struct {
	ID        string
	Disabled  bool
	Exhausted bool
}

// pickActiveAuth chooses which qwenwork auth to use from host candidates.
//
// 面板选择只作为「平级优先」，不再是无条件直通。原实现在这里直接 return cur，
// 下面按在跑请求数平摊的 pickLeastLoaded 永远走不到，等于面板粘性盖过了负载
// 均衡：只要面板指着某个号，并发就一直堆在它身上。上游按账号分配并行槽位，
// 堆积部分在账号侧排队（不报错、只抬首字延迟）。改成把 cur 排在候选首位：负载与
// 其它可用号持平时照样选它（面板选择仍然生效），一旦它堆得更多就让位给更空闲的号。
func pickActiveAuth(candidates []activeAuthCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	byID := make(map[string]activeAuthCandidate, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}

	cur := getActiveAuthID()
	var ready []string
	if cur != "" {
		if c, ok := byID[cur]; ok && !c.Disabled && !c.Exhausted && !isAccountCoolingDown(cur) {
			ready = append(ready, cur)
		}
	}
	for _, c := range candidates {
		if c.ID == cur {
			continue
		}
		if !c.Disabled && !c.Exhausted && !isAccountCoolingDown(c.ID) {
			ready = append(ready, c.ID)
		}
	}
	if len(ready) == 0 {
		return ""
	}
	// 从 ready 里挑在跑请求数最少的账号，而不是轮到谁就谁。
	//
	// 轮询只保证「请求数均摊」，不保证「同时在跑的数量均摊」：长请求会堆积在同一
	// 个刚被轮到的账号上，短请求早已结束。ready 已按高积分优先排序，pickLeastLoaded
	// 平级时保留该顺序，因此这里不会把残血号提到健康号前面。
	cands := make([]activeAuthCandidate, 0, len(ready))
	for _, id := range ready {
		cands = append(cands, activeAuthCandidate{ID: id})
	}
	next := pickLeastLoaded(cands)
	if next == "" {
		next = ready[0]
	}
	activeAuthMu.Lock()
	if next != cur {
		activeAuthID = next
	}
	activeAuthMu.Unlock()
	return next
}

// ensureDefaultActiveAuth sets the panel-selected account.
// Called from buildDashboardEx on every /accounts and /refresh request.
//
// Rules (single source of truth, same as pickActiveAuth):
//  1. If current selection is live AND routable (not disabled / cooling /
//     exhausted / low residual credits) → keep it.
//  2. If current selection is not routable → switch to the routable account
//     with the HIGHEST remaining credits (unknown credits rank last).
//  3. If current selection is gone (disabled/deleted) → same as rule 2.
//  4. If none is routable → keep current if alive, else first.
//
// [参数] accounts：面板全量账号行（含本轮排除项）。
// [返回] 面板应选中的 auth_id；无可用账号时退化为存活/首个账号。
// 最近修改时间 2026-11-27（免费模型放行 0 积分账号，面板选中仍按账号级判定）
func ensureDefaultActiveAuth(accounts []wbAccount) string {
	cur := getActiveAuthID()
	live := make(map[string]wbAccount, len(accounts))
	for _, a := range accounts {
		live[a.AuthID] = a
	}

	// Rule 1: current selection is live AND routable → keep.
	if cur != "" {
		if a, ok := live[cur]; ok && !a.Disabled && accountRoutable(cur) {
			return cur
		}
	}

	// Rule 2 & 3: selection is unavailable or gone → pick the HIGHEST-credit
	// routable account. Unknown credits (-1) rank last and only win when nothing
	// else is measurable.
	var firstAny, firstOK, firstReady string
	for _, a := range accounts {
		if firstAny == "" {
			firstAny = a.AuthID
		}
		if a.Disabled {
			continue
		}
		if firstOK == "" {
			firstOK = a.AuthID
		}
		if !accountRoutable(a.AuthID) {
			continue
		}
		if firstReady == "" {
			firstReady = a.AuthID
			continue
		}
		if accountHigherCredits(a.AuthID, firstReady) {
			firstReady = a.AuthID
		}
	}

	// Prefer the first routable non-disabled account.
	next := firstReady
	if next == "" {
		// Rule 4: none routable — keep current if still alive (not disabled).
		if cur != "" {
			if a, ok := live[cur]; ok && !a.Disabled {
				return cur
			}
		}
		next = firstOK
	}
	if next == "" {
		next = firstAny
	}
	if next != "" && next != cur {
		setActiveAuthID(next)
	}
	return next
}

// accountRoutable reports whether an auth ID may carry traffic at all: not
// cooling down (failover) and not exhausted or below the residual-credit floor.
// 面板选中等无具体请求模型的场景仍用账号级判定；带请求模型的调度入口
// （scheduler.pick）与换号（pickNextAuth）改用 accountUsableForModel 放行免费模型。
func accountRoutable(authID string, model ...string) bool {
	if isAccountCoolingDown(authID) {
		return false
	}
	_, lowOrExhausted := cachedCreditsScore(authID)
	if lowOrExhausted {
		return false
	}
	// 慢速冷却：该号在这个模型上显著慢于同池水平时让位。只影响当前模型，
	// 不能因为一个模型慢就把整个账号摘掉（账号在别的模型上可能完全正常）。
	if slowModel := firstSlowModel(model); slowModel != "" && slowCoolingDown(authID, slowModel, time.Now()) {
		return false
	}
	return true
}

// firstSlowModel 取出变参里的模型名，未传时返回空串（调用方未带模型上下文）。
// [参数] model：可选模型名。
// [返回] 模型名或空串。
// 最近修改时间：2026-10-26；改动原因：新增，避免每个调用点都做长度判断。
func firstSlowModel(model []string) string {
	if len(model) == 0 {
		return ""
	}
	return strings.TrimSpace(model[0])
}

// accountHigherCredits 比较两个健康账号的充裕度，积分更多者排在前面；未测 (-1) 排最后。
func accountHigherCredits(left, right string) bool {
	leftScore, _ := cachedCreditsScore(left)
	rightScore, _ := cachedCreditsScore(right)
	if (leftScore < 0) != (rightScore < 0) {
		return leftScore >= 0
	}
	return leftScore > rightScore
}
