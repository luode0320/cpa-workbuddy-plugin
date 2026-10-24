// active_auth.go tracks the panel-selected WorkBuddy account used for routing.
//
// Region is taken from that account's stored domain field — no per-request JWT
// iss decode. Default: first available candidate. When the active account is
// disabled/missing, switch to another routable candidate and remember the choice.
// 额度不参与可用性判定：零积分账号仍可正常推理。
package main

import (
	"strings"
	"sync"
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
type activeAuthCandidate struct {
	ID       string
	Disabled bool
}

// pickActiveAuth chooses which workbuddy auth to use from host candidates.
// The panel selection is sticky: it stays on the current account unless that
// account is no longer in the candidate list (disabled/deleted by host) or is
// no longer routable (cooling down / test-failed). When switching, it picks
// the first ready candidate — the caller already ordered them — and updates
// activeAuthID so the panel reflects the change on next dashboard load. When
// NO healthy candidate exists, it returns "" so the scheduler defers to the
// host's built-in scheduler (cross-provider failover).
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

	// Selection is gone or unusable — pick the first ready candidate, else
	// report no healthy account.
	var ready []string
	for _, c := range candidates {
		if !c.Disabled && accountRoutable(c.ID) {
			ready = append(ready, c.ID)
		}
	}
	if len(ready) == 0 {
		// All candidates disabled/unavailable: no healthy account exists.
		// Return "" WITHOUT changing the panel selection so
		// handleSchedulerPick defers (Handled: false) to the host's built-in
		// scheduler, which can fail over to other providers' accounts (e.g.
		// traework). The selection is restored automatically once an account
		// recovers.
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
//  1. If current selection is live AND available (not disabled / cooling /
//     test-failed) → keep it.
//  2. If current selection is not available → switch to the available account
//     with the HIGHEST remaining credits (unknown credits rank last).
//  3. If current selection is gone (disabled/deleted) → same as rule 2.
//  4. If none available → keep current if alive, else first.
//
// 积分为 0 不影响「可用」：额度不再参与路由判定。
// This ensures the panel's selected card always matches what scheduler.pick
// actually routes to. No silent drift.
//
// [参数] accounts：面板全量账号行（含本轮排除项）。
// [返回] 面板应选中的 auth_id；无可用账号时退化为存活/首个账号。
// 最近修改时间：2026-10-24；改动原因：额度耗尽不再是不可用状态。
func ensureDefaultActiveAuth(accounts []wbAccount) string {
	cur := getActiveAuthID()
	live := make(map[string]wbAccount, len(accounts))
	for _, a := range accounts {
		live[a.AuthID] = a
	}

	// Rule 1: current selection is live AND available → keep.
	if cur != "" {
		if a, ok := live[cur]; ok && !a.Disabled && accountRoutable(cur) {
			return cur
		}
	}

	// Rule 2 & 3: selection is unavailable or gone → pick the HIGHEST-credit
	// available account. Unknown credits (-1) rank last and only win when
	// nothing else is measurable.
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

	// Prefer first non-disabled routable account.
	next := firstReady
	if next == "" {
		// Rule 4: none available — keep current if still alive (not disabled).
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
// cooling down (failover), not test-failed (「测试」标签). Shared by the
// panel-selection rules and documented as the same predicate family
// scheduler.pick applies before ordering candidates.
func accountRoutable(authID string) bool {
	return !isAccountCoolingDown(authID) && !isTestFailed(authID)
}

// accountHigherCredits 比较两个可用账号的充裕度，积分更多者排在前面；未测 (-1) 排最后。
// 仅用于排序，不影响账号是否可用。
func accountHigherCredits(left, right string) bool {
	leftScore, _ := cachedCreditsScore(left)
	rightScore, _ := cachedCreditsScore(right)
	if (leftScore < 0) != (rightScore < 0) {
		return leftScore >= 0
	}
	return leftScore > rightScore
}
