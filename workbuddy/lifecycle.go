// lifecycle.go implements credit-based auth lifecycle for workbuddy:
//   - CN exhausted → AUTO-DISABLE: write disabled:true + top-level marker
//     exhausted_disable:true (policy update 2026-09-08, user mandate: 耗尽
//     停用保留，但必须能自动恢复). The 4h check-in reconcile then re-enables
//     the account automatically once refreshed credits are > 0.
//   - Global exhausted → delete auth file (one-shot quota)
//   - Manual disable (manual_disable marker, or a disabled doc without the
//     exhausted_disable marker written by the host UI) is NEVER auto-touched:
//     no automatic path re-enables or re-disables it.
//   - Unknown credits → no-op (never mis-kill)
//   - Hard credit errors from executor → recheck credits then apply policy
//   - Soft rate limits → do not delete Global
package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	lifecycleState   sync.Map // auth_id (auth.ID) -> lifecycleStateEntry
	lifecycleSaveTTL = 30 * time.Second
)

type lifecycleStateEntry struct {
	disabled bool
	note     string
	at       time.Time
}

func lifecycleStateUnchanged(authID string, disabled bool, note string) bool {
	v, ok := lifecycleState.Load(authID)
	if !ok {
		return false
	}
	e := v.(*lifecycleStateEntry)
	if e.disabled != disabled || e.note != note {
		return false
	}
	return time.Since(e.at) < lifecycleSaveTTL
}

func rememberLifecycleState(authID string, disabled bool, note string) {
	lifecycleState.Store(authID, &lifecycleStateEntry{disabled: disabled, note: note, at: time.Now()})
}

// pruneLifecycleState removes entries for auth indices that no longer exist
// or whose TTL has expired. Called from dashboard prune to prevent unbounded growth.
func pruneLifecycleState() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[f.ID] = struct{}{}
	}
	lifecycleState.Range(func(key, value any) bool {
		idx, _ := key.(string)
		if _, ok := live[idx]; !ok {
			lifecycleState.Delete(key)
			return true
		}
		if e, ok := value.(*lifecycleStateEntry); ok && time.Since(e.at) > 10*time.Minute {
			lifecycleState.Delete(key)
		}
		return true
	})
}

// disableAuth applies the exhausted-disable decision for one account.
//
// Dual-path semantics (policy update 2026-09-08):
//   - Manual toggle path (extra contains manual_disable:true): writes
//     disabled:true and carries the manual_disable intent marker. The
//     reconcile never auto-re-enables a manually disabled account, even
//     when credits recover.
//   - Auto exhaustion path (extra=nil, the only remaining automatic caller
//     set — applyExhaustedPolicy / reconcileOneAccount lifecycleDisable):
//     writes disabled:true + exhausted_disable:true when the account is
//     currently enabled (耗尽自动停用). The 4h check-in reconcile clears the
//     pair once refreshed credits are > 0 (自动恢复). If the file is already
//     disabled, the existing flag is preserved untouched: manual_disable
//     carries forward, exhausted_disable carries forward, and a marker-less
//     disabled doc (host-UI toggle or unknown writer) gains NO marker — it
//     stays exactly as the user left it.
//
// An existing manual_disable marker is ALWAYS carried forward, so an auto
// note refresh of an already-manually-disabled account never erases the
// user's choice. extra merges additional top-level keys into the auth file.
func disableAuth(authIndex, authID string, sa *storedAuth, cr *creditsSummary, reason string, extra map[string]any) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	note := displayNote(sa, cr, true)
	if reason != "" && !strings.Contains(note, reason) {
		// keep note short; reason only if room
		if len(note)+len(reason) < 75 {
			note = note + " · " + reason
		}
	}
	// Prefer live physical file to preserve any extra fields if present.
	phys, err := hostAuthGetPhysical(authIndex)
	// Merge caller extras with any marker already on disk.
	merged := map[string]any{}
	for k, v := range extra {
		merged[k] = v
	}
	if err == nil && manualDisableFromAuthJSON(phys.JSON) {
		merged["manual_disable"] = true
	}
	// Determine whether this is a manual toggle or an auto-lifecycle call.
	// Manual toggle (extra contains manual_disable:true) writes disabled:true
	// with the intent marker. The auto exhaustion path (policy update
	// 2026-09-08) writes disabled:true + exhausted_disable:true when the
	// account is enabled; already-disabled files keep their existing flag and
	// markers untouched (manual_disable carries forward via merged above).
	_, isManualToggle := extra["manual_disable"]
	if !isManualToggle {
		// Auto-lifecycle: exhaustion disable with auto-recovery marker.
		finalDisabled := false
		if err == nil && phys != nil {
			finalDisabled = parseDisabledFromAuthJSON(phys.JSON)
			switch {
			case manualDisableFromAuthJSON(phys.JSON):
				// Manually disabled: user intent wins — the manual_disable
				// marker is already carried in merged; never add
				// exhausted_disable (must not become auto-recoverable).
			case exhaustedDisableFromAuthJSON(phys.JSON):
				// Already auto-disabled by this lifecycle: marker carries
				// forward so recovery stays armed.
				merged["exhausted_disable"] = true
			case finalDisabled:
				// Disabled by an unknown/manual writer without markers
				// (e.g. host-UI toggle): leave the flag exactly as-is.
			default:
				// Enabled → exhausted auto-disable (可自动恢复).
				finalDisabled = true
				merged["exhausted_disable"] = true
			}
		}
		note = displayNote(sa, cr, finalDisabled)
		if reason != "" && !strings.Contains(note, reason) {
			// keep note short; reason only if room
			if len(note)+len(reason) < 75 {
				note = note + " · " + reason
			}
		}
		// Skip the write when state+note are unchanged.
		if lifecycleStateUnchanged(authID, finalDisabled, note) {
			return nil
		}
		name := authFileNameFor(sa)
		path := ""
		legacyPath := ""
		if phys != nil {
			name, path, legacyPath = resolveAuthFileTarget(sa, phys)
		}
		raw, err := buildAuthFileJSON(sa, finalDisabled, note, merged)
		if err != nil {
			return err
		}
		if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
			return err
		}
		rememberLifecycleState(authID, finalDisabled, note)
		accountCache.Delete(authID)
		return nil
	}
	// Manual toggle path: write disabled:true.
	// Skip the write only when state+note are unchanged AND nothing new to
	// merge — a manual toggle must persist even inside the lifecycle TTL.
	if lifecycleStateUnchanged(authID, true, note) {
		// already disabled; still refresh note if needed
		if lifecycleStateUnchanged(authID, true, note) {
			return nil
		}
	}
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	if phys != nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
	}
	raw, err := buildAuthFileJSON(sa, true, note, merged)
	if err != nil {
		return err
	}
	// Direct physical write (NOT host.auth.save): the save channel rebuilds
	// the record as Active and would silently re-enable the account. The file
	// watcher applies top-level disabled to the scheduler instead.
	if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
		return err
	}
	rememberLifecycleState(authID, true, note)
	accountCache.Delete(authID)
	return nil
}

// reenableAuth writes disabled:false when CN has credits again.
func reenableAuth(authIndex, authID string, sa *storedAuth, cr *creditsSummary) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	if !shouldReenableCN(true, cr) {
		return nil
	}
	note := displayNote(sa, cr, false)
	if lifecycleStateUnchanged(authID, false, note) {
		return nil
	}
	phys, err := hostAuthGetPhysical(authIndex)
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	if err == nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
	}
	raw, err := buildAuthFileJSON(sa, false, note, nil)
	if err != nil {
		return err
	}
	// Direct physical write — host.auth.save would force the record Active
	// (fine here) but would ALSO reset the scheduler state via the persist
	// hook; the watcher path keeps every top-level field intact.
	if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
		return err
	}
	rememberLifecycleState(authID, false, note)
	accountCache.Delete(authID)
	return nil
}

// deleteAuth removes Global exhausted credentials from disk.
func deleteAuth(authIndex, authID string, sa *storedAuth) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(phys.Path)
	if path == "" {
		// Try to reconstruct path from peer workbuddy files' directory + canonical name.
		name := authFileNameFor(sa)
		if phys.Name != "" && !isLegacyWorkbuddyAuthName(phys.Name) {
			name = phys.Name
		} else if strings.TrimSpace(phys.Name) != "" && isLegacyWorkbuddyAuthName(phys.Name) {
			name = authFileNameFor(sa)
		}
		if dir := peerAuthDir(); dir != "" && name != "" {
			candidate := filepath.Join(dir, name)
			if isSafeWorkbuddyAuthPath(candidate) {
				path = candidate
			}
		}
	}
	if path == "" {
		// Last resort: preserve the existing disabled flag (never auto-disable).
		existingDisabled := parseDisabledFromAuthJSON(phys.JSON)
		note := displayNote(sa, nil, existingDisabled)
		extra := map[string]any{}
		if manualDisableFromAuthJSON(phys.JSON) {
			extra["manual_disable"] = true
		}
		if exhaustedDisableFromAuthJSON(phys.JSON) {
			extra["exhausted_disable"] = true
		}
		raw, berr := buildAuthFileJSON(sa, existingDisabled, note, extra)
		if berr != nil {
			return fmt.Errorf("no path and build failed: %w", berr)
		}
		name := authFileNameFor(sa)
		if phys.Name != "" && !isLegacyWorkbuddyAuthName(phys.Name) {
			name = phys.Name
		}
		if err := hostAuthPersistMigrate(name, "", "", raw); err != nil {
			return err
		}
		rememberLifecycleState(authID, true, note)
		accountCache.Delete(authID)
		clearActiveAuthIfMatch(authID)
		preserveSetClear(authID)
		return nil
	}
	if err := deleteAuthFileInDir(path, filepath.Dir(path)); err != nil {
		return err
	}
	// Also remove legacy workbuddy.json if this UID was dual-named historically.
	if sa != nil && strings.TrimSpace(sa.Account.UID) != "" {
		if dir := filepath.Dir(path); dir != "" {
			legacy := filepath.Join(dir, authFileName)
			// A-36: same path safety as primary deleteAuthFileInDir.
			if isLegacyWorkbuddyAuthName(filepath.Base(legacy)) {
				_ = deleteAuthFileInDir(legacy, dir)
			}
		}
	}
	clearDeletedAccountState(authID, authIndex, sa.Account.UID)
	return nil
}

// clearDeletedAccountState removes every in-memory trace of a deleted account
// for each provided key (auth.ID, auth_index, and account UID may each have
// been used as a key by different code paths). Covers lifecycle state, cached
// credits/plan/checkin, active selection, preserve flag,
// failover cooldown/counter, and session bindings pinned to the account.
// Idempotent — safe to call when maps are empty or keys already absent.
func clearDeletedAccountState(keys ...string) {
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		lifecycleState.Delete(k)
		accountCache.Delete(k)
		clearActiveAuthIfMatch(k)
		preserveSetClear(k)
		clearFailoverStateForAuth(k)
		evictSessionBindingsForAuth(k)
	}
}

// peerAuthDir returns the directory of any workbuddy auth file known to the host.
// Uses HostAuthFileEntry.Path from the list response (A-38: was N+1 — list + getPhysical per file).
func peerAuthDir() string {
	files, err := hostAuthList()
	if err != nil {
		return ""
	}
	for _, f := range files {
		p := strings.TrimSpace(f.Path)
		if p != "" {
			return filepath.Dir(p)
		}
	}
	return ""
}

// applyExhaustedPolicy applies disable (CN) or delete (Global).
func applyExhaustedPolicy(authIndex, authID string, sa *storedAuth, cr *creditsSummary, reason string) error {
	if !lifecycleEnabled() {
		return nil
	}
	action := lifecycleActionFor(accountRegion(sa), cr)
	switch action {
	case lifecycleDelete:
		return deleteAuth(authIndex, authID, sa)
	case lifecycleDisable:
		return disableAuth(authIndex, authID, sa, cr, reason, nil)
	default:
		return nil
	}
}

// syncAuthNote writes note without changing disabled state.
func syncAuthNote(authIndex, authID string, sa *storedAuth, cr *creditsSummary, disabled bool) error {
	if sa == nil {
		return nil
	}
	note := displayNote(sa, cr, disabled)
	if lifecycleStateUnchanged(authID, disabled, note) {
		return nil
	}
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()
	phys, err := hostAuthGetPhysical(authIndex)
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	extra := map[string]any{}
	if err == nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
		// re-read disabled from disk as source of truth
		disabled = parseDisabledFromAuthJSON(phys.JSON)
		note = displayNote(sa, cr, disabled)
		// carry the manual-disable marker forward (note refresh must not
		// clear it), same for the exhausted-disable marker (note refresh
		// must not disarm auto-recovery)
		if manualDisableFromAuthJSON(phys.JSON) {
			extra["manual_disable"] = true
		}
		if exhaustedDisableFromAuthJSON(phys.JSON) {
			extra["exhausted_disable"] = true
		}
	}
	if lifecycleStateUnchanged(authID, disabled, note) {
		return nil
	}
	raw, err := buildAuthFileJSON(sa, disabled, note, extra)
	if err != nil {
		return err
	}
	// Direct physical write keeps the disabled state and the manual_disable
	// marker intact (host.auth.save would rebuild the record as Active).
	if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
		return err
	}
	rememberLifecycleState(authID, disabled, note)
	return nil
}

// reconcileOneAccount refreshes credits and applies lifecycle for one auth.
// authIndex is used for host RPC (host.auth.get), authID (auth.ID) is used
// for cache keys (accountCache/lifecycleState) so it matches the scheduler's
// SchedulerAuthCandidate.ID.
// force ignores short-circuit only for credit fetch (uses force on cache via caller).
func reconcileOneAccount(authIndex, authID string, force bool) (action lifecycleAction, err error) {
	if !lifecycleEnabled() {
		return lifecycleNone, nil
	}
	// Single host.auth.get (A-19): previous hostAuthGet + hostAuthGetPhysical
	// doubled RPC on every reconcile tick (21 accounts × 2).
	sa, phys, err := hostAuthGetBundle(authIndex)
	if err != nil {
		return lifecycleNone, err
	}
	disabled := false
	if phys != nil {
		disabled = phys.Disabled
	}

	// Credits: use force path via fetchUserResource always when force,
	// else try cache first.
	var cr *creditsSummary
	if !force {
		if v, ok := accountCache.Load(authID); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 && e.credits != nil && time.Since(e.fetched) < accountCacheTTL {
				cr = e.credits
			}
		}
	}
	if cr == nil {
		// Route credits fetch through cachedAccountDetails so singleflight
		// serializes concurrent writers for the same authID (P0-2 fix: the
		// previous Load→Store sequence here had a check-then-act window
		// where a concurrent dashboard cachedAccountDetails write could
		// overwrite our merge with newer plan/checkin values).
		_, _, cr2, _ := cachedAccountDetails(authID, sa, true)
		cr = cr2
		if cr == nil {
			return lifecycleNone, nil
		}
	}

	region := accountRegion(sa)
	if region == "cn" && disabled {
		// Manual disable (manual_disable marker) must stick: never
		// auto-re-enable an account the user explicitly disabled, even when
		// credits recover.
		if phys != nil && manualDisableFromAuthJSON(phys.JSON) {
			_ = syncAuthNote(authIndex, authID, sa, cr, true)
			return lifecycleNone, nil
		}
		// Marker-gated recovery (policy update 2026-09-08): only docs this
		// lifecycle auto-disabled (exhausted_disable:true) re-enable
		// automatically once credits recover. A marker-less disabled doc is
		// host-UI manual intent or an unknown writer — sticky by design.
		if phys == nil || !exhaustedDisableFromAuthJSON(phys.JSON) {
			_ = syncAuthNote(authIndex, authID, sa, cr, true)
			return lifecycleNone, nil
		}
		if shouldReenableCN(true, cr) {
			if err := reenableAuth(authIndex, authID, sa, cr); err != nil {
				return lifecycleReenable, err
			}
			return lifecycleReenable, nil
		}
		// still disabled: refresh note
		_ = syncAuthNote(authIndex, authID, sa, cr, true)
		return lifecycleNone, nil
	}

	act := lifecycleActionFor(region, cr)
	switch act {
	case lifecycleDelete:
		// P1-4: confirm before deleting a Global account — a transient 402
		// from the upstream billing API could otherwise cause an irreversible
		// delete. Re-fetch credits once more; only proceed if still exhausted.
		cr2, err2 := fetchUserResource(sa)
		if err2 != nil || !isCreditsExhausted(cr2) {
			// Credits may have recovered (or fetch failed) — don't delete.
			return lifecycleNone, nil
		}
		return lifecycleDelete, deleteAuth(authIndex, authID, sa)
	case lifecycleDisable:
		return lifecycleDisable, disableAuth(authIndex, authID, sa, cr, "耗尽", nil)
	default:
		// healthy: keep note fresh (throttled)
		_ = syncAuthNote(authIndex, authID, sa, cr, false)
		return lifecycleNone, nil
	}
}

// reconcileAllAccounts walks workbuddy auths and applies lifecycle.
func reconcileAllAccounts(force bool) []map[string]any {
	if !lifecycleEnabled() {
		return nil
	}
	files, err := hostAuthList()
	if err != nil {
		return []map[string]any{{"error": err.Error()}}
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		act, err := reconcileOneAccount(f.AuthIndex, f.ID, force)
		row := map[string]any{"auth_index": f.AuthIndex, "action": act.String()}
		if err != nil {
			row["error"] = err.Error()
		}
		if act != lifecycleNone || err != nil {
			out = append(out, row)
		}
	}
	return out
}

// noteAccountFailure records an upstream failure against the account and,
// when it enters failover cooldown, evicts every session binding pinned to it
// so new requests route elsewhere. The executor AuthID is normally the same
// auth.ID the scheduler keys on, but legacy hosts may pass the account UID
// instead; a background resolve backfills the canonical key so pick still
// skips the account. Returns true when the failure was counted.
func noteAccountFailure(authID string, status int, body string) bool {
	if !failoverActive() || strings.TrimSpace(authID) == "" {
		return false
	}
	if !recordAccountFailure(authID, status, body) {
		return false
	}
	evictSessionBindingsForAuth(authID)
	go func() {
		_, id := resolveAuthIndexAndID(authID)
		if id == "" || id == authID {
			return
		}
		if recordAccountFailure(id, status, body) {
			evictSessionBindingsForAuth(id)
		}
	}()
	return true
}

// reconcileAfterExecutorError triggers lifecycle when upstream reports hard credit failure.
// AuthID from the executor may be the credential ID (UID) rather than runtime auth_index;
// we resolve via host.auth.list when direct get fails.
func reconcileAfterExecutorError(authID string, status int, body string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	// Failover first — independent of lifecycle_auto: any upstream failure
	// (429/402/5xx/transport) counts toward cooldown and evicts bindings.
	noteAccountFailure(authID, status, body)
	// Hard credit errors additionally trigger the disable/delete lifecycle.
	if !lifecycleEnabled() || !isHardCreditError(status, body) {
		return
	}
	go func() {
		idx, id := resolveAuthIndexAndID(authID)
		if idx == "" {
			return
		}
		_, _ = reconcileOneAccount(idx, id, true)
	}()
}

// resolveAuthIndexAndID maps executor AuthID (index, file id, or account UID)
// to host auth_index AND auth.ID. Returns ("", "") if not found.
func resolveAuthIndexAndID(authID string) (string, string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return "", ""
	}
	// Fast path: already an auth_index the host understands.
	if _, err := hostAuthGet(authID); err == nil {
		// Find the matching file entry to get auth.ID.
		if files, err := hostAuthList(); err == nil {
			for _, f := range files {
				if f.AuthIndex == authID {
					return authID, f.ID
				}
			}
		}
		return authID, ""
	}
	files, err := hostAuthList()
	if err != nil {
		return "", ""
	}
	// Prefer O(list) name/id match before per-account host.auth.get (A-22).
	// Multi-account files are workbuddy-<uid>.json; list Name/ID usually carry that.
	wantName := "workbuddy-" + authID + ".json"
	for _, f := range files {
		if f.AuthIndex == authID || f.ID == authID || f.Name == authID {
			return f.AuthIndex, f.ID
		}
		if listEntryMatchesUID(f, authID, wantName) {
			return f.AuthIndex, f.ID
		}
	}
	// Slow path: only when list metadata lacks uid (rare legacy shapes).
	for _, f := range files {
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			continue
		}
		if strings.TrimSpace(sa.Account.UID) == authID {
			return f.AuthIndex, f.ID
		}
	}
	return "", ""
}

// reconcileByUID finds workbuddy auth by account UID and applies executor-error lifecycle.
func reconcileByUID(uid string, status int, body string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	// Failover first — independent of lifecycle_auto. reconcileByUID receives
	// the account UID; noteAccountFailure resolves it to the auth.ID key the
	// scheduler uses and evicts bindings when cooldown triggers.
	noteAccountFailure(uid, status, body)
	if !lifecycleEnabled() || !isHardCreditError(status, body) {
		return
	}
	idx, id := resolveAuthIndexAndID(uid)
	if idx == "" {
		return
	}
	_, _ = reconcileOneAccount(idx, id, true)
}

// invalidateAccountCredits drops cached credits so the next panel/reconcile
// fetch hits upstream. Call after a successful chat completion — otherwise a
// short TTL cache makes "used" look frozen while the user is burning credits.
func invalidateAccountCredits(authID, authUID string) {
	// Invalidate credits only — keep plan/checkin in cache.
	invalidateCredits := func(id string) {
		if v, ok := accountCache.Load(id); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 {
				fresh := *e
				fresh.credits = nil
				fresh.fetched = time.Now()
				accountCache.Store(id, &fresh)
			}
		}
	}
	if authID != "" {
		invalidateCredits(authID)
	}
	if authUID == "" || authUID == authID {
		return
	}
	// Also drop any cache keyed by auth_index that maps to this UID.
	files, err := hostAuthList()
	if err != nil {
		return
	}
	wantName := "workbuddy-" + authUID + ".json"
	matchedByName := false
	for _, f := range files {
		if f.AuthIndex == authID || f.ID == authID || f.Name == authID {
			invalidateCredits(f.ID)
			continue
		}
		if listEntryMatchesUID(f, authUID, wantName) {
			invalidateCredits(f.ID)
			matchedByName = true
		}
	}
	if matchedByName {
		return
	}
	// Slow path: legacy names without uid in list metadata.
	for _, f := range files {
		if f.AuthIndex == authID {
			continue
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			continue
		}
		if strings.TrimSpace(sa.Account.UID) == authUID {
			invalidateCredits(f.ID)
		}
	}
}

// listEntryMatchesUID reports whether host list metadata already encodes the UID
// (workbuddy-<uid>.json naming). Pure helper for O(list) cache invalidation.
func listEntryMatchesUID(f pluginapi.HostAuthFileEntry, uid, wantName string) bool {
	if uid == "" {
		return false
	}
	if strings.EqualFold(f.Name, wantName) || strings.EqualFold(f.ID, wantName) {
		return true
	}
	base := strings.TrimSuffix(f.Name, ".json")
	return strings.EqualFold(base, "workbuddy-"+uid)
}

// enrichAuthMetadata builds Metadata map for AuthData (type/logo/note/disabled).
func enrichAuthMetadata(sa *storedAuth, cr *creditsSummary, disabled bool) map[string]any {
	note := displayNote(sa, cr, disabled)
	return map[string]any{
		"type":     providerName,
		"provider": providerName,
		"logo":     pluginLogoURL,
		"note":     note,
		"disabled": disabled,
	}
}
