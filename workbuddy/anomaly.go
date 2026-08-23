// anomaly.go manages the "anomaly" status — accounts that have failed too
// often in a row and are temporarily quarantined so the routing layer stops
// wasting budget on them.
//
// Anomaly is a *lifetime health gate*: when a workbuddy account has hit
// account-level 4xx (401/403/404/405), 5xx, 402/429-with-marked-body, or a
// transport error (status 0) for `anomaly_pool_threshold` consecutive
// requests (default 10), it is moved into the anomaly set. While in the set,
// the scheduler never picks it (analogous to the preserve filter, see
// preserve.go). A successful request resets the counter immediately so a
// transient upstream hiccup never latches.
//
// The set is dual-mirrored:
//  1. In-memory: anomalySet guarded by anomalySetMu — read on every
//     scheduler pick.
//  2. On-disk: a top-level `anomaly` boolean on each physical auth JSON
//     file, written via writeAuthFileDirect (NOT host.auth.save, which
//     rebuilds the auth record and drops top-level fields the host does
//     not recognize — same root cause as preserve / manual_disable).
//
// refreshAnomalySetFromDisk rebuilds the in-memory set by scanning the host
// auth files; called from /accounts and after every persistAnomalyToggle so
// the set is always consistent with disk.
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
//  2. Drop preserve accounts (preserve.go).
//  3. Drop anomaly accounts (this file).
//  4. Drop cooling-down accounts (accountFailover.go).
//  5. Route on the survivors (all "normal" accounts are equal).
//
// Place anomaly BEFORE cooldown so a freshly quarantined account that
// continues to 4xx doesn't double-count (cooldown's filter still applies
// to the survivors). When EVERY workbuddy account is in the anomaly set
// the full list is kept so the pickers fall back to the current pin
// (mirrors preserve's all-quarantined fallback) instead of locking
// routing.
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
//
// Defaults are exported (anomalyThresholdDefault / anomalyRefreshEnabledDefault)
// and mirrored by the accessors below.
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
// refreshAnomalySetFromDisk after every persistAnomalyToggle so writes are
// reflected immediately in the next scheduler pick.
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

// anomalySetPut adds an auth to the anomaly set. Callers MUST persist the
// change via persistAnomalyToggle before returning, otherwise the next
// /accounts reload from disk will revert. Idempotent.
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
	// Prune any in-memory entry whose auth is no longer live on disk.
	for id := range anomalySnapshot() {
		if _, ok := live[id]; !ok {
			anomalySetClear(id)
		}
	}
	return len(anomalySnapshot())
}

// parseAnomalyFromAuthJSON reads the top-level anomaly flag. The flag is
// the single source of truth — lives on the physical auth JSON, never on
// the host's auth record, because host.auth.save rebuilds the record and
// drops top-level fields the host doesn't recognize (same root cause as
// preserve / manual_disable).
func parseAnomalyFromAuthJSON(raw []byte) bool {
	var m struct {
		Anomaly bool `json:"anomaly"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Anomaly
}

// persistAnomalyToggle writes the anomaly flag to the physical auth file.
// Uses persistAuthDirect (NOT host.auth.save) because the host silently
// drops unrecognized top-level fields on save. Direct write lets the
// host's file watcher re-synthesize the auth record with the new
// top-level field preserved alongside disabled / preserve / etc.
//
// on=true sets anomaly: true; on=false drops the key entirely so the
// file stays clean and parseAnomalyFromAuthJSON returns false on the next
// read.
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
	if err := json.Unmarshal(phys.JSON, &doc); err != nil {
		// Treat malformed JSON as a fresh doc — losing the existing
		// top-level flags is better than refusing the write (consistent
		// with the other direct-write toggles such as manual_disable).
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
	if err := persistAuthDirect(phys.Name, phys.Path, "", raw); err != nil {
		return err
	}
	if on {
		anomalySetPut(authID)
	} else {
		anomalySetClear(authID)
	}
	return nil
}

// freezeAccountForAnomaly is the threshold-trip side effect. Called from
// recordAccountFailure when count >= anomalyThreshold; flips the anomaly
// flag on disk (via persistAnomalyToggle), updates the in-memory mirror,
// and evicts session bindings pinned to the account. Always run off the
// request goroutine (host.auth.list can be slow under contention).
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
		return
	}
	if err := persistAnomalyToggle(idx, id, true); err != nil {
		log.Printf("[anomaly] freeze %s: persist failed: %v", id, err)
		// Don't update mirror on write failure: next /accounts will retry
		// via the failure counter and we don't want a false-positive.
		return
	}
	// Mirror update is performed inside persistAnomalyToggle; evict any
	// session bindings so conversations don't keep stacking on the bad
	// account.
	evictSessionBindingsForAuth(id)
}

// clearAllAnomalies iterates the current anomaly set, removes each entry
// from disk, and rebuilds the in-memory mirror. Used by the daily 00:00
// refresh loop. Returns the number of accounts cleared.
//
// Errors during individual clears are logged but do not abort the batch
// — the goal is best-effort reset, so a single broken file should not
// block the rest from being reset.
func clearAllAnomalies() (int, error) {
	snap := anomalySnapshot()
	cleared := 0
	for id := range snap {
		// We need the auth_index for persistAnomalyToggle. The fast path is
		// to scan hostAuthList and match by ID; UID-style keys are common
		// enough that a direct lookup may fail (see resolveAuthIndexAndID
		// for the same reason).
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
			// Fall back to clearing only the in-memory mirror so we don't
			// keep a stale set entry pointing at a deleted account.
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

// handleUnfreezeAuth is the management endpoint backing the panel's
// "解除冻结" buttons. With auth_index present it clears one account; with
// an empty body it clears every account in the anomaly set (the daily
// refresh loop's manual equivalent). Idempotent: clearing an account
// that isn't flagged returns ok with cleared=0.
func handleUnfreezeAuth(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		// Bulk clear: delegate to the same helper the daily loop uses,
		// then rebuild the dashboard payload so the panel can refresh.
		cleared, err := clearAllAnomalies()
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"ok": true, "cleared": cleared}
	}
	// Single-account clear: resolve the auth.ID via hostAuthList so we
	// update the right entry. Look up by index first, then by ID, so the
	// panel can pass either field (its data carries both).
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
		out["nickname"] = sa.Account.Nickname
		out["uid"] = sa.Account.UID
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
// calendar day even if the tick fires twice during the rollover window.
//
// Started from package init(); disable via setAnomalyConfig(0, false)
// (config anomaly_refresh_enabled: false).
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
