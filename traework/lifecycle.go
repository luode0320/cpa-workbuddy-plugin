// lifecycle.go implements credit-based auth lifecycle for traework:
//
//   - credits exhausted (remain <= 0, live or cached snapshot) → AUTO-DISABLE:
//     disabled:true + top-level marker exhausted_disable:true (policy update
//     2026-09-08, user mandate: 耗尽停用保留，但必须能自动恢复). Exhausted
//     accounts also release the panel pin and sticky conversations.
//   - RECOVERY: every path that refreshes the credits cache (the 4-hour
//     check-in loop, the panel refresh runner, manual check-in, /credits
//     query) re-runs reconcile afterwards; when an account carries
//     exhausted_disable:true (and NOT manual_disable) and its refreshed
//     credits are > 0, the flag pair is cleared and the account is
//     re-enabled — 积分恢复>0 自动开启.
//   - manual_disable (panel toggle, persistDisabledToggle) is NEVER touched
//     by any automatic path — a manually disabled account stays disabled
//     until the user re-enables it. Files disabled without any marker
//     (host-side toggles) are equally left alone.
//   - Unknown credits → no-op (never mis-kill an account we couldn't read).
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
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
	Action    string `json:"action"` // disabled | reenabled | skipped | error | off
	Remain    int64  `json:"remain,omitempty"`
}

// authDocFlags reads the three top-level lifecycle flags from a physical auth
// JSON doc in one parse: disabled, manual_disable (panel toggle — never
// auto-touched) and exhausted_disable (set by persistExhaustedDisable,
// cleared by persistExhaustedReenable and manual re-enable). Unparsable or
// empty input yields all-false — callers treat that as "no flags".
func authDocFlags(raw []byte) (disabled, manual, exhausted bool) {
	if len(raw) == 0 {
		return false, false, false
	}
	var m struct {
		Disabled         bool `json:"disabled"`
		ManualDisable    bool `json:"manual_disable"`
		ExhaustedDisable bool `json:"exhausted_disable"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false, false, false
	}
	return m.Disabled, m.ManualDisable, m.ExhaustedDisable
}

// reconcileOneAccount applies the exhausted lifecycle to one auth using the
// cached credits snapshot (force=true queries the upstream credits first and
// refreshes the cache). Returns the action string for logs/panel rows.
func reconcileOneAccount(authIndex, authID string, force bool) string {
	if !lifecycleEnabled() {
		return "off"
	}
	sa, phys, err := hostAuthGetBundle(authIndex)
	if err != nil || sa == nil || phys == nil {
		return "skipped"
	}
	var cr *traeCredits
	if force {
		if live, cerr := accountCredits(sa); cerr == nil && live != nil {
			cr = live
			cacheCredits(authID, live)
		}
	}
	if cr == nil {
		if cached, ok := cachedCredits(authID); ok {
			cr = cached
		}
	}
	if cr == nil {
		return "skipped" // unknown credits — never mis-kill
	}
	disabled, manual, exhausted := authDocFlags(phys.JSON)
	switch {
	case !disabled && isCreditsExhausted(cr):
		if perr := persistExhaustedDisable(authIndex, sa, cr); perr != nil {
			log.Printf("[lifecycle] exhausted-disable %s failed: %v", authID, perr)
			return "error"
		}
		// Exhausted account should not carry the panel pin or sticky
		// conversations — release both so routing moves on.
		clearActiveAuthIfMatch(authID)
		evictSessionBindingsForAuth(authID)
		return "disabled"
	case disabled && exhausted && !manual && cr.TotalRemain > 0:
		if perr := persistExhaustedReenable(authIndex, sa, cr); perr != nil {
			log.Printf("[lifecycle] exhausted-reenable %s failed: %v", authID, perr)
			return "error"
		}
		return "reenabled"
	default:
		return "skipped"
	}
}

// reconcileAfterCreditsRefresh is the hook every credits-refresh path calls
// (4h check-in loop, panel refresh runner, manual check-in, /credits query).
// It re-runs the lifecycle decision with the freshly cached snapshot so an
// exhausted account is disabled promptly and a recovered account is
// re-enabled within the same 4-hour cycle. Never panics into the caller —
// errors are logged inside reconcileOneAccount.
func reconcileAfterCreditsRefresh(authIndex, authID string) {
	_ = reconcileOneAccount(authIndex, authID, false)
}

// persistExhaustedDisable writes disabled:true + exhausted_disable:true and a
// 耗尽 note onto the physical auth file. Files already disabled (manual
// toggle or unknown origin) are left untouched — the auto path only owns the
// enabled→disabled transition. Goes through persistAuthDirect, NOT
// host.auth.save: the save channel rebuilds the record and drops unknown
// top-level fields, which would silently lose the exhausted_disable marker.
func persistExhaustedDisable(authIndex string, sa *traeAuth, cr *traeCredits) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	if disabled, _, _ := authDocFlags(phys.JSON); disabled {
		return nil // already disabled — nothing this function owns
	}
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil {
		return err
	}
	var remain, used int64
	if cr != nil {
		remain, used = cr.TotalRemain, cr.TotalUsed
	}
	doc["disabled"] = true
	doc["exhausted_disable"] = true
	doc["note"] = fmt.Sprintf("耗尽停用 · 余%d 已用%d（签到恢复积分后自动启用）", remain, used)
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(phys.Name)
	if name == "" {
		name = authFileNameFor(sa)
	}
	if err := persistAuthDirect(name, phys.Path, "", raw); err != nil {
		return err
	}
	log.Printf("[lifecycle] exhausted-disable %s: credits exhausted (remain=%d used=%d), disabled + exhausted_disable set", authIndex, remain, used)
	return nil
}

// persistExhaustedReenable clears the exhausted-disable pair when the
// account's refreshed credits are back above zero. It re-checks the markers
// on the CURRENT file content so a concurrent manual toggle can never be
// overridden: manual_disable wins, and a file that lost its exhausted_disable
// marker is left alone. The note records the recovery for the panel.
func persistExhaustedReenable(authIndex string, sa *traeAuth, cr *traeCredits) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	disabled, manual, exhausted := authDocFlags(phys.JSON)
	if !disabled || manual || !exhausted {
		return nil // nothing this function owns — never blind-write
	}
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil {
		return err
	}
	var remain, used int64
	if cr != nil {
		remain, used = cr.TotalRemain, cr.TotalUsed
	}
	doc["disabled"] = false
	delete(doc, "exhausted_disable")
	doc["note"] = fmt.Sprintf("恢复启用 · 签到积分到账 · 余%d 已用%d", remain, used)
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(phys.Name)
	if name == "" {
		name = authFileNameFor(sa)
	}
	if err := persistAuthDirect(name, phys.Path, "", raw); err != nil {
		return err
	}
	log.Printf("[lifecycle] exhausted-reenable %s: credits recovered (remain=%d), disabled flag cleared", authIndex, remain)
	return nil
}

// reconcileAllAccounts walks every traework auth and applies the exhausted
// lifecycle (disable on exhaustion, re-enable on recovery). force=true
// queries fresh credits per account instead of trusting the cache.
// Returns the per-account rows for the panel's lifecycle section.
func reconcileAllAccounts(force bool) []map[string]any {
	if !lifecycleEnabled() {
		return nil
	}
	files, err := hostAuthList()
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		if strings.TrimSpace(f.AuthIndex) == "" {
			continue
		}
		row := lifecycleReconcileRow{AuthIndex: f.AuthIndex, AuthID: f.ID}
		row.Action = reconcileOneAccount(f.AuthIndex, f.ID, force)
		if cr, ok := cachedCredits(f.ID); ok && cr != nil {
			row.Remain = cr.TotalRemain
		}
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
