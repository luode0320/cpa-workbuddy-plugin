// lifecycle.go implements credit-based auth lifecycle for traework:
//
//   - credits exhausted (remain <= 0, cached snapshot) → disable the auth file
//     (disabled:true) so routing stops wasting requests on an account with no
//     balance left.
//   - Unknown credits → no-op (never mis-kill an account we couldn't read).
//   - No auto re-enable: a manually-disabled account must never be silently
//     re-enabled, and an exhausted account comes back only via the panel's
//     启用 button or a fresh import. (workbuddy re-enables CN accounts after
//     check-in; traework keeps operator control — the panel already surfaces
//     remain per account, so "enable when recharged" is one click.)
//
// Trigger: the dashboard calls reconcileAllAccounts(force=true) after a
// forced credits refresh (panel 刷新 button), so exhaust → disable is
// immediate instead of waiting for the next preserve-watchdog tick.
package main

import (
	"encoding/json"
	"sync"
)

var (
	lifecycleAuto   = true
	lifecycleAutoMu sync.RWMutex
)

// lifecycleEnabled reports whether auto-disable of exhausted accounts is
// active. Configurable via config_yaml `lifecycle_auto: false`.
func lifecycleEnabled() bool {
	lifecycleAutoMu.RLock()
	defer lifecycleAutoMu.RUnlock()
	return lifecycleAuto
}

// setLifecycleEnabled toggles the auto-disable mechanism (config / tests).
func setLifecycleEnabled(on bool) {
	lifecycleAutoMu.Lock()
	lifecycleAuto = on
	lifecycleAutoMu.Unlock()
}

// lifecycleReconcileRow is one account actioned by reconcileAllAccounts.
type lifecycleReconcileRow struct {
	AuthIndex string `json:"auth_index"`
	AuthID    string `json:"auth_id"`
	UID       string `json:"uid,omitempty"`
	Nickname  string `json:"nickname,omitempty"`
	Action    string `json:"action"` // disabled | already_disabled | skipped
	Remain    int64  `json:"remain,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// reconcileAllAccounts walks every traework auth and disables accounts whose
// cached credits are exhausted. force=false skips the pass entirely (the
// dashboard uses force=true after a refresh; nothing else calls it).
// Returns the per-account rows for the panel's lifecycle section.
func reconcileAllAccounts(force bool) []map[string]any {
	if !force || !lifecycleEnabled() {
		return nil
	}
	files, err := hostAuthList()
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		row := lifecycleReconcileRow{AuthIndex: f.AuthIndex, AuthID: f.ID}
		sa, phys, loadErr := hostAuthGetBundle(f.AuthIndex)
		if loadErr != nil || sa == nil || phys == nil {
			row.Action = "skipped"
			row.Reason = "load auth failed"
			out = append(out, rowMap(row))
			continue
		}
		row.UID = sa.UserID
		row.Nickname = sa.Nickname
		cr, ok := cachedCredits(f.ID)
		if !ok || cr == nil {
			// Unknown credits: never auto-kill.
			row.Action = "skipped"
			row.Reason = "no credits snapshot"
			out = append(out, rowMap(row))
			continue
		}
		row.Remain = cr.TotalRemain
		if !isCreditsExhausted(cr) {
			row.Action = "skipped"
			row.Reason = "credits available"
			out = append(out, rowMap(row))
			continue
		}
		if phys.Disabled {
			row.Action = "already_disabled"
			out = append(out, rowMap(row))
			continue
		}
		if err := persistDisabledToggle(f.AuthIndex, f.ID, true); err != nil {
			row.Action = "skipped"
			row.Reason = "disable failed: " + err.Error()
			out = append(out, rowMap(row))
			continue
		}
		row.Action = "disabled"
		// Exhausted account can no longer carry the panel pin or sticky
		// conversations — release both so routing moves on.
		clearActiveAuthIfMatch(f.ID)
		evictSessionBindingsForAuth(f.ID)
		out = append(out, rowMap(row))
	}
	return out
}

func rowMap(r lifecycleReconcileRow) map[string]any {
	b, _ := json.Marshal(r)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

// handleLifecycleStatus returns the lifecycle toggle state for the panel.
func handleLifecycleStatus() map[string]any {
	return map[string]any{
		"enabled": lifecycleEnabled(),
	}
}
