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
	activeAuthRR uint64
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
	if id == "" {
		activeAuthRR = 0
	}
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
// 耗尽账号在调度入口已被过滤，这里只承载禁用状态。
type activeAuthCandidate struct {
	ID       string
	Disabled bool
}

// pickActiveAuth chooses which qwenwork auth to use from host candidates.
// The panel selection is sticky: it stays on the current account unless that
// account is no longer in the candidate list (disabled/deleted by host) or is
// no longer routable (cooling down / low residual credits). When switching, it
// picks the first ready candidate — the caller already ordered them
// high-credit-first — and updates activeAuthID so the panel reflects the change
// on next dashboard load. When NO routable candidate exists, it returns "" so
// the scheduler defers to the host's built-in scheduler (cross-provider failover).
func pickActiveAuth(candidates []activeAuthCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	byID := make(map[string]activeAuthCandidate, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}

	cur := getActiveAuthID()
	// Keep current selection if it's still a live candidate AND routable.
	if cur != "" {
		if c, ok := byID[cur]; ok && !c.Disabled && accountRoutable(cur) {
			return cur
		}
	}

	// Selection is gone or unusable — pick the first ready candidate (the
	// slice is pre-ordered high-credit-first), else report no routable account.
	var ready []string
	for _, c := range candidates {
		if !c.Disabled && accountRoutable(c.ID) {
			ready = append(ready, c.ID)
		}
	}
	if len(ready) == 0 {
		return ""
	}
	activeAuthMu.Lock()
	next := ready[int(activeAuthRR%uint64(len(ready)))]
	activeAuthRR++
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
// 耗尽账号不保留选中资格：QwenWork 无免费模型，0 积分只会 429。
//
// [参数] accounts：面板全量账号行（含本轮排除项）。
// [返回] 面板应选中的 auth_id；无可用账号时退化为存活/首个账号。
// 最近修改时间 2026-10-18 17:10:00（耗尽账号不再保留选中资格）
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
// QwenWork has no free models (verified 2026-10-18: 0-credit accounts get
// 429 balance_insufficient on every model), so exhausted accounts are excluded.
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
