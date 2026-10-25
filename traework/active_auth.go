// active_auth.go tracks the panel-selected TraeWork account used for routing.
// The selection is sticky; when the active account becomes exhausted /
// disabled / cooling down / 测试失败 / missing, routing switches to the next
// healthy candidate and remembers the choice.
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
type activeAuthCandidate struct {
	ID        string
	Disabled  bool
	Exhausted bool
}

// pickActiveAuth chooses which traework auth to use from host candidates.
// The panel selection is sticky; it switches to the next healthy candidate
// when the current one is exhausted / disabled / cooling down / 测试失败 /
// missing. When NO healthy candidate exists, it returns "" so the scheduler
// defers to the host's built-in scheduler (cross-provider failover).
func pickActiveAuth(candidates []activeAuthCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	byID := make(map[string]activeAuthCandidate, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}

	cur := getActiveAuthID()
	if cur != "" {
		if c, ok := byID[cur]; ok && !c.Disabled && !c.Exhausted && accountRoutable(cur) {
			return cur
		}
	}

	var ready []string
	for _, c := range candidates {
		if !c.Disabled && !c.Exhausted && accountRoutable(c.ID) {
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

// traeAccountView is the dashboard's per-account card model.
type traeAccountView struct {
	AuthID        string       `json:"auth_id"`
	AuthIndex     string       `json:"auth_index"`
	Nickname      string       `json:"nickname"`
	Label         string       `json:"label,omitempty"`
	Name          string       `json:"name,omitempty"`
	UID           string       `json:"uid"`
	Remain        int64        `json:"remain"`
	Credits       *traeCredits `json:"credits,omitempty"`
	Exhausted     bool         `json:"exhausted"`
	LowCredits    bool         `json:"low_credits"`
	Disabled      bool         `json:"disabled"`
	TestFailed    bool         `json:"test_failed"` // scheduled active-ping failed while credits remained
	CheckinToday  bool         `json:"checkin_today"`
	SuccessCount  int64        `json:"success_count,omitempty"`
	FailedCount   int64        `json:"failed_count,omitempty"`
	CoolingDown   bool         `json:"cooling_down"`
	FailCount     int          `json:"fail_count"`
	CooldownUntil int64        `json:"cool_until,omitempty"`
	// 双写下的禁用侧：盘上的 failover_disable 标记才是恢复的唯一通道，冷却到期
	// 不会让账号回来，所以面板必须把它和冷却分开显示。
	FailoverDisabled bool   `json:"failover_disabled,omitempty"`
	FailoverModel    string `json:"failover_model,omitempty"` // 探活回放的模型（唯一目标）
	// 轮换侧：与失败冷却完全独立的状态（resetAccountFailover 不清它），
	// 面板必须分开显示，否则用户看到「没有冷却却被跳过」会无法理解。
	RotationCooling bool  `json:"rotation_cooling,omitempty"`
	RotationUntil   int64 `json:"rotation_until,omitempty"`
	RotationServedM int   `json:"rotation_served_m,omitempty"`
	// SlowCoolingModels 列出该账号当前因「显著慢于同池同模型水平」而冷却的模型。
	// 慢速冷却按「账号×模型」生效，不是账号整体停用，因此这里必须是清单而非布尔值。
	SlowCoolingModels []string `json:"slow_cooling_models,omitempty"`
	// 积分档位：disabled 表示低于禁用线（已被本机制停用），preserve 表示
	// 仍在路由但排序靠后。空串表示未知或不适用。
	CreditTier       string `json:"credit_tier,omitempty"`
	LowCreditDisable bool   `json:"low_credit_disable,omitempty"`
	Active           bool   `json:"active"`
	Error            string `json:"error,omitempty"`
}

// ensureDefaultActiveAuth keeps the panel selection consistent with routing
// (same rules as pickActiveAuth).
//
// [参数] accounts：面板全量账号行（含本轮排除项）。
// [返回] 面板应选中的 auth_id；无可用账号时退化为存活/首个账号。
// 最近修改时间：2026-09-29；改动原因：面板选中项与「硬排除三类标签 + 低积分优先」路由口径对齐。
func ensureDefaultActiveAuth(accounts []traeAccountView) string {
	cur := getActiveAuthID()
	live := make(map[string]traeAccountView, len(accounts))
	for _, a := range accounts {
		live[a.AuthID] = a
	}
	if cur != "" {
		if a, ok := live[cur]; ok && !a.Disabled && !a.Exhausted && accountRoutable(cur) {
			return cur
		}
	}
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
		if a.Exhausted || !accountRoutable(a.AuthID) {
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
	next := firstReady
	if next == "" {
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
// cooling down (failover/rotation), not test-failed (「测试」标签). Shared by
// the panel-selection rules and documented as the same predicate family
// scheduler.pick applies before ordering candidates.
//
// 保活线不在这里硬排除：它只影响排序（见 scheduler.go 的 preserveRank）。
// 保留在硬判据里会让 remain 在 20–99 的账号被面板判为「不可用」，与调度层
// 实际会选它们的行为互相矛盾。
func accountRoutable(authID string) bool {
	if isAccountCoolingDown(authID) || isTestFailed(authID) {
		return false
	}
	if rotationCoolingDown(authID, time.Now()) {
		return false
	}
	_, lowOrExhausted := cachedCreditsScore(authID)
	return !lowOrExhausted
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
