// anomaly.go manages the "anomaly" status for traework-provider — accounts
// that have failed too many requests in a row and are temporarily quarantined
// so the routing layer stops wasting budget on them.
//
// Anomaly is a *lifetime health gate*: when a traework account has hit
// account-level 4xx (401/403/404/405), 5xx, 402/429-with-marked-body, or a
// transport error (status 0) for `anomaly_pool_threshold` consecutive
// requests (default 10), it is moved into the anomaly set. While in the
// set, the scheduler never picks it. A successful request resets the
// counter immediately so a transient upstream hiccup never latches.
//
// The set is dual-mirrored:
//  1. In-memory: anomalySet guarded by anomalySetMu — read on every
//     scheduler pick.
//  2. On-disk: a top-level `anomaly` boolean on each physical auth JSON
//     file, written via writeAnomalyFileDirect (analogous to workbuddy's
//     writeAuthFileDirect). traework inlines the atomic temp+rename helper
//     here and confines it to safe traework auth paths.
//
// Recovery is operator-driven: the panel exposes a per-account "解除冻结"
// button (calls persistAnomalyToggle(false)) and a global "每日 0 点自动
// 复活" background loop (anomalyRefreshLoop), so a dead account gets
// another shot the next day without manual intervention. If it is truly
// broken it will fail again and re-enter the set — the goal is to surface
// *which* accounts are bad, not to permanently remove them.
//
// Routing contract (scheduler.pick):
//  1. Drop disabled accounts.
//  2. Drop anomaly accounts (this file).
//  3. Drop cooling-down accounts (accountFailover.go).
//  4. Route on the survivors (all "normal" accounts are equal).
//
// Place anomaly BEFORE cooldown so a freshly quarantined account that
// continues to 4xx doesn't double-count (cooldown's filter still applies
// to the survivors). When EVERY qoderwork account is in the anomaly set
// the full list is kept so the picker falls back to the current pin
// (mirrors workbuddy's all-quarantined fallback).
package main

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// anomalyConfig holds the runtime-tunable knobs. Populated from plugin
// config_yaml in configure() (usage_config.go); readers take the RW lock
// so a concurrent reconfigure is safe.
var (
	anomalyThresholdMu  sync.RWMutex
	anomalyThresholdCfg = anomalyThresholdDefault

	anomalyRefreshEnabledMu  sync.RWMutex
	anomalyRefreshEnabledCfg = anomalyRefreshEnabledDefault
)

// anomalyThreshold returns the current consecutive-failure threshold at
// which an account enters the anomaly set. RLock-safe; called from
// recordAccountFailure.
func anomalyThreshold() int32 {
	anomalyThresholdMu.RLock()
	defer anomalyThresholdMu.RUnlock()
	return anomalyThresholdCfg
}

// anomalyRefreshEnabled reports whether the daily 00:00 local-time reset
// loop is active. RLock-safe; toggled via setAnomalyConfig.
func anomalyRefreshEnabled() bool {
	anomalyRefreshEnabledMu.RLock()
	defer anomalyRefreshEnabledMu.RUnlock()
	return anomalyRefreshEnabledCfg
}

// setAnomalyConfig applies the next anomaly-pool configuration under its
// own locks (no nesting). Called from configure() after parsing
// config_yaml. Missing/zero threshold keeps the previous value (kill-switch
// safe, mirrors retry_on_4xx Seen-pattern).
func setAnomalyConfig(threshold int32, refreshEnabled bool) {
	if threshold > 0 {
		anomalyThresholdMu.Lock()
		anomalyThresholdCfg = threshold
		anomalyThresholdMu.Unlock()
	}
	anomalyRefreshEnabledMu.Lock()
	anomalyRefreshEnabledCfg = refreshEnabled
	anomalyRefreshEnabledMu.Unlock()
}

// anomalySet mirrors the top-level `anomaly` flag on the physical auth
// file. Membership means "do not route traffic here, this account has
// failed too many times in a row". The set is rebuilt from disk by
// refreshAnomalySetFromDisk after every persistAnomalyToggle so writes
// are reflected immediately in the next scheduler pick.
var (
	anomalySetMu sync.RWMutex
	anomalySet   = make(map[string]struct{})
)

// isAnomaly reports whether auth.ID is currently in the anomaly set.
// scheduler.pick reads this on every request — keep it cheap.
func isAnomaly(authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	anomalySetMu.RLock()
	_, ok := anomalySet[authID]
	anomalySetMu.RUnlock()
	return ok
}

// anomalySnapshot returns a copy of the current set (auth.ID → true).
// Used by /accounts to surface anomaly_pool_size.
func anomalySnapshot() map[string]bool {
	anomalySetMu.RLock()
	defer anomalySetMu.RUnlock()
	out := make(map[string]bool, len(anomalySet))
	for k := range anomalySet {
		out[k] = true
	}
	return out
}

// anomalySetPut adds an auth to the anomaly set. Idempotent.
func anomalySetPut(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	anomalySetMu.Lock()
	anomalySet[authID] = struct{}{}
	anomalySetMu.Unlock()
}

// anomalySetClear removes an auth from the anomaly set. Idempotent.
func anomalySetClear(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	anomalySetMu.Lock()
	delete(anomalySet, authID)
	anomalySetMu.Unlock()
}

// refreshAnomalySetFromDisk rebuilds the in-memory anomaly set from the
// host's current auth file list. Called from /accounts and after every
// persistAnomalyToggle so the set is always consistent with disk. Returns
// the new size.
//
// Errors are intentionally swallowed: a transient host RPC failure
// shouldn't blank the set when the next /accounts will succeed.
func refreshAnomalySetFromDisk() int {
	files, err := hostAuthList()
	if err != nil {
		return len(anomalySnapshot())
	}
	next := make(map[string]struct{}, len(files))
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[f.ID] = struct{}{}
		phys, err2 := hostAuthGetPhysical(f.AuthIndex)
		if err2 != nil || phys == nil {
			continue
		}
		if parseAnomalyFromAuthJSON(phys.JSON) {
			next[f.ID] = struct{}{}
		}
	}
	anomalySetMu.Lock()
	anomalySet = next
	anomalySetMu.Unlock()
	for id := range anomalySnapshot() {
		if _, ok := live[id]; !ok {
			anomalySetClear(id)
		}
	}
	return len(anomalySnapshot())
}

// parseAnomalyFromAuthJSON reads the top-level anomaly flag. The flag is
// the single source of truth — lives on the physical auth JSON, never on
// the host's auth record, because some host rebuilders drop top-level
// fields the host doesn't recognize (same root cause as workbuddy's
// preserve / manual_disable persistence).
func parseAnomalyFromAuthJSON(raw []byte) bool {
	var m struct {
		Anomaly bool `json:"anomaly"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Anomaly
}

// writeAnomalyFileDirect is kept as a thin alias over the shared
// writeAuthFileDirect (authfile.go) so existing anomaly call sites stay
// untouched. Same safety contract: isSafeAuthPath + absolute + atomic
// temp-then-rename.
func writeAnomalyFileDirect(path string, raw []byte) error {
	return writeAuthFileDirect(path, raw)
}

// persistAnomalyToggle writes the anomaly flag to the physical auth file.
// Uses writeAnomalyFileDirect (NOT host.auth.save) so unrecognized
// top-level fields persist across save round-trips. Mirrors workbuddy's
// preserve / manual_disable persistence pattern.
//
// on=true sets anomaly: true; on=false drops the key entirely so the
// file stays clean and parseAnomalyFromAuthJSON returns false on the
// next read.
func persistAnomalyToggle(authIndex, authID string, on bool) error {
	authIndex = strings.TrimSpace(authIndex)
	authID = strings.TrimSpace(authID)
	if authIndex == "" {
		return errAuthIndexRequired()
	}
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	var doc map[string]any
	if uerr := json.Unmarshal(phys.JSON, &doc); uerr != nil {
		// Treat malformed JSON as a fresh doc — losing the existing
		// top-level flags is better than refusing the write (consistent
		// with workbuddy's direct-write toggles).
		doc = map[string]any{}
	}
	if on {
		doc["anomaly"] = true
	} else {
		delete(doc, "anomaly")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := writeAnomalyFileDirect(phys.Path, raw); err != nil {
		return err
	}
	if on {
		anomalySetPut(authID)
	} else {
		anomalySetClear(authID)
	}
	return nil
}

// authFileErr / errAuthIndexRequired / errAuthMissing mirror workbuddy's
// helpers so persistAnomalyToggle returns stable error values without
// allocating fmt.Errorf strings.
type authFileErr struct{ msg string }

func (e *authFileErr) Error() string { return e.msg }

func errAuthIndexRequired() error { return &authFileErr{msg: "auth_index is required"} }
func errAuthMissing() error       { return &authFileErr{msg: "auth file missing or empty"} }

// freezeAccountForAnomaly is the threshold-trip side effect. Called from
// recordAccountFailure when count >= anomalyThreshold; flips the anomaly
// flag on disk (via persistAnomalyToggle) and updates the in-memory
// mirror. Always run off the request goroutine (host.auth.list can be
// slow under contention). Also evicts any sticky session binding pinned to
// this account (session_auth.go) so conversations are re-assigned on the
// next pick instead of continuing to fail.
func freezeAccountForAnomaly(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" || isAnomaly(authID) {
		return
	}
	idx, id := resolveAuthIndexAndID(authID)
	if idx == "" || id == "" {
		// Can't write back to disk without an auth_index; still update the
		// in-memory mirror so the current process stops routing here.
		anomalySetPut(authID)
		evictSessionBindingsForAuth(authID)
		return
	}
	if err := persistAnomalyToggle(idx, id, true); err != nil {
		log.Printf("[anomaly] freeze %s: persist failed: %v", id, err)
		return
	}
	evictSessionBindingsForAuth(id)
}

// clearAllAnomalies iterates the current anomaly set, removes each entry
// from disk, and rebuilds the in-memory mirror. Used by the daily 00:00
// refresh loop. Returns the number of accounts cleared.
func clearAllAnomalies() (int, error) {
	snap := anomalySnapshot()
	cleared := 0
	for id := range snap {
		files, err := hostAuthList()
		if err != nil {
			return cleared, err
		}
		var authIndex string
		for _, f := range files {
			if f.ID == id {
				authIndex = f.AuthIndex
				break
			}
		}
		if authIndex == "" {
			anomalySetClear(id)
			continue
		}
		if err := persistAnomalyToggle(authIndex, id, false); err != nil {
			log.Printf("[anomaly] daily refresh: clear %s failed: %v", id, err)
			continue
		}
		cleared++
	}
	refreshAnomalySetFromDisk()
	return cleared, nil
}

// handleUnfreezeAuth is the traework management endpoint backing the
// panel's "解除冻结" buttons. With auth_index present it clears one
// account; with an empty body it clears every account in the anomaly
// set. Idempotent.
func handleUnfreezeAuth(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		cleared, err := clearAllAnomalies()
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"ok": true, "cleared": cleared}
	}
	files, lerr := hostAuthList()
	if lerr != nil {
		return map[string]any{"error": lerr.Error(), "auth_index": authIndex}
	}
	var id, realIndex string
	for _, f := range files {
		if f.AuthIndex == authIndex || f.ID == authIndex {
			id = f.ID
			realIndex = f.AuthIndex
			break
		}
	}
	if id == "" {
		return map[string]any{"error": "account not found", "auth_index": authIndex}
	}
	if err := persistAnomalyToggle(realIndex, id, false); err != nil {
		return map[string]any{"error": err.Error(), "auth_index": realIndex, "id": id}
	}
	sa, _ := hostAuthGet(realIndex)
	out := map[string]any{"ok": true, "auth_index": realIndex, "id": id, "cleared": 1}
	if sa != nil {
		out["nickname"] = sa.Nickname
		out["uid"] = sa.UserID
	}
	return out
}

// anomalyRefreshTickInterval bounds how often the daily refresh loop
// checks the wall-clock; 1 minute is the resolution we need to detect
// the local 00:00 boundary.
const anomalyRefreshTickInterval = 1 * time.Minute

// anomalyRefreshLoop wakes every minute, and when the local clock
// crosses 00:00 it calls clearAllAnomalies so every quarantined account
// gets another shot. The lastDay guard ensures we run exactly once per
// calendar day.
func anomalyRefreshLoop() {
	ticker := time.NewTicker(anomalyRefreshTickInterval)
	defer ticker.Stop()
	lastDay := -1
	for range ticker.C {
		if !anomalyRefreshEnabled() {
			continue
		}
		now := time.Now().Local()
		if now.Hour() == 0 && now.Minute() == 0 && now.Day() != lastDay {
			lastDay = now.Day()
			if cleared, err := clearAllAnomalies(); err != nil {
				log.Printf("[anomaly] daily refresh error: %v", err)
			} else if cleared > 0 {
				log.Printf("[anomaly] daily refresh cleared %d accounts", cleared)
			}
		}
	}
}

func init() {
	go anomalyRefreshLoop()
}
