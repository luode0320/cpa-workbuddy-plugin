// counter.go owns the plugin-side cumulative success/failure counters.
//
// Why this exists: the panel's "成功 / 失败" columns were historically
// sourced from the host's recent-request window (HostAuthFileEntry.Success /
// Failed). Those fields are `json:"-"` in the CPA auth type — they are
// memory-only rolling counters (10min × 20 buckets ≈ last 200 minutes), never
// serialized to the auth file, so a container restart zeroes them. This
// module replaces that with counters the plugin itself owns and persists into
// the physical auth file's top-level JSON so they survive restarts.
// （同步自 workbuddy 0.14.10 / 0.14.11；qoderwork 无 preserve watchdog，
// 落盘节奏挂载在 anomalyRefreshLoop（每日 00:00），见 anomaly.go。）
//
// Persistence model (memory-first, JSON as best-effort backup):
//   - recordOutcome increments an in-memory cumulative counter keyed by the
//     account UID (the executor's stable account identity, same key the
//     scheduler / failover / preserve / anomaly layers already use).
//   - On startup, loadCountersFromDisk seeds the in-memory counters from the
//     persisted success_count / failed_count, so a restart recovers the last
//     flushed value. After that the in-memory counter is the source of truth —
//     the panel reads it directly and does NOT re-read json on every render.
//   - flushCounters folds each account's not-yet-persisted delta into the
//     auth file's top-level success_count / failed_count. It runs on the
//     anomaly refresh loop's tick cadence (daily 00:00), NOT a dedicated fast
//     timer, because the counters are pure observability: a crash loses at
//     most one tick's worth of deltas, which is acceptable for a best-effort
//     backup.
//
// Field naming is deliberately success_count / failed_count (NOT success /
// failed): the host's HostAuthFileEntry already exposes `success` / `failed`
// for its own recent window, and reusing those names on the physical file
// would create an ambiguous dual source of truth. The distinct names make it
// obvious this is the plugin's own persisted cumulative counter.
//
// UID-less accounts (legacy single-file qoderwork.json) are skipped: without
// a UID there is no stable key to map a counter back to a physical file on
// flush, so we silently no-op rather than persist a counter under a key that
// can never be attributed. Those accounts simply fall back to the host's
// recent-window numbers in the panel (unchanged behavior).
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Top-level auth-file keys carrying the plugin-owned cumulative counters.
const (
	counterSuccessKey = "success_count"
	counterFailedKey  = "failed_count"
)

// counterEntry holds one account's cumulative counters. success / failed are
// the in-memory source of truth (what the panel reads). persistedSuccess /
// persistedFailed are the last value folded into the physical auth file, so
// the flusher computes the pending delta as (success - persistedSuccess)
// without re-reading the file.
type counterEntry struct {
	success          int64
	failed           int64
	persistedSuccess int64
	persistedFailed  int64
}

var (
	counterMu sync.Mutex
	// counterEntries maps account UID → cumulative counters. Keyed by UID so
	// the flusher can resolve the physical file via resolveAuthIndexAndID.
	counterEntries = make(map[string]counterEntry)
	// counterLoaded marks UIDs already seeded from the persisted json, so
	// ensureCounterLoaded is idempotent and does not double-count.
	counterLoaded = make(map[string]bool)
)

// recordOutcome records one completed executor request against the account.
// uid is the account UID (empty → no-op, see package doc). success=true for a
// served request, false for an upstream/transport failure that surfaced to
// the caller. Pure in-memory increment: it never touches disk; the flusher
// persists on its own tick cadence.
func recordOutcome(uid string, success bool) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	counterMu.Lock()
	e := counterEntries[uid]
	if success {
		e.success++
	} else {
		e.failed++
	}
	counterEntries[uid] = e
	counterMu.Unlock()
}

// ensureCounterLoaded seeds the in-memory cumulative counter for uid from the
// physical auth JSON exactly once. If recordOutcome already incremented before
// this ran (e.g. a request completed ahead of the first flusher tick), the
// in-flight delta is preserved on top of the persisted value so nothing is
// lost. Idempotent via counterLoaded.
func ensureCounterLoaded(uid string, physJSON []byte) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	counterMu.Lock()
	defer counterMu.Unlock()
	if counterLoaded[uid] {
		return
	}
	s, f := parseCountersFromAuthJSON(physJSON)
	e := counterEntries[uid]
	// e.success / e.failed already hold any in-process increment that raced
	// ahead of this load; layer the persisted value underneath them.
	e.success += s
	e.failed += f
	e.persistedSuccess = s
	e.persistedFailed = f
	counterEntries[uid] = e
	counterLoaded[uid] = true
}

// counterSnapshot returns the in-memory cumulative counters for uid. Callers
// (the panel) must ensureCounterLoaded first so the persisted history is
// included; otherwise an account not yet seeded reads as zero.
func counterSnapshot(uid string) (success, failed int64) {
	uid = strings.TrimSpace(uid)
	counterMu.Lock()
	defer counterMu.Unlock()
	e := counterEntries[uid]
	return e.success, e.failed
}

// loadCountersFromDisk walks every qoderwork auth file and seeds the in-memory
// counters from the persisted success_count / failed_count. Called once at
// the anomaly refresh loop startup so the panel reads restart-recovered values
// without re-reading json on every render.
func loadCountersFromDisk() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	for _, f := range files {
		sa, phys, err := hostAuthGetBundle(f.AuthIndex)
		if err != nil || sa == nil || phys == nil {
			continue
		}
		uid := strings.TrimSpace(sa.Account.UID)
		if uid == "" {
			continue
		}
		ensureCounterLoaded(uid, phys.JSON)
	}
}

// parseCountersFromAuthJSON reads the persisted success_count / failed_count
// from a physical auth file's top-level JSON. Missing/malformed fields read
// as zero (tolerant, like parsePreserveFromAuthJSON).
func parseCountersFromAuthJSON(raw []byte) (success, failed int64) {
	if len(raw) == 0 {
		return 0, 0
	}
	var m struct {
		Success int64 `json:"success_count"`
		Failed  int64 `json:"failed_count"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Success, m.Failed
}

// flushCounters folds each account's pending delta (success - persistedSuccess)
// into its physical auth file. It is idempotent and failure-tolerant: a failed
// fold leaves persisted* unchanged so the delta is retried on the next tick.
// Called by the anomaly refresh loop on its tick cadence (daily 00:00), not a
// dedicated fast timer.
func flushCounters() {
	type item struct {
		uid      string
		dSuccess int64
		dFailed  int64
	}
	counterMu.Lock()
	items := make([]item, 0, len(counterEntries))
	for uid, e := range counterEntries {
		dS := e.success - e.persistedSuccess
		dF := e.failed - e.persistedFailed
		if dS != 0 || dF != 0 {
			items = append(items, item{uid: uid, dSuccess: dS, dFailed: dF})
		}
	}
	counterMu.Unlock()

	for _, it := range items {
		if err := persistCounterDelta(it.uid, it.dSuccess, it.dFailed); err != nil {
			// Leave persisted* unchanged so the next tick retries; no
			// increment is lost.
			continue
		}
		counterMu.Lock()
		e := counterEntries[it.uid]
		e.persistedSuccess += it.dSuccess
		e.persistedFailed += it.dFailed
		counterEntries[it.uid] = e
		counterMu.Unlock()
	}
}

// persistCounterDelta adds addSuccess / addFailed to the account's physical
// auth file top-level success_count / failed_count. The write goes through
// persistAuthDirect (NOT host.auth.save) so the host's file watcher re-syncs
// the record without rebuilding it — the same rule as preserve / anomaly /
// manual_disable, because host.auth.save drops top-level fields it doesn't
// recognize.
func persistCounterDelta(uid string, addSuccess, addFailed int64) error {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return fmt.Errorf("counter: empty uid")
	}
	authIndex, _ := resolveAuthIndexAndID(uid)
	if authIndex == "" {
		return fmt.Errorf("counter: no auth_index for uid %s", uid)
	}
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	raw := foldCounterIntoDoc(phys.JSON, addSuccess, addFailed)
	name := phys.Name
	if name == "" {
		name = "qoderwork-" + uid + ".json"
	}
	return persistAuthDirect(name, phys.Path, "", raw)
}

// foldCounterIntoDoc increments success_count / failed_count on a top-level
// auth JSON doc, preserving every other key. Extracted from persistCounterDelta
// so the fold is unit-testable without host RPC (the cgo shim can't drive
// host.auth.get).
func foldCounterIntoDoc(base []byte, addSuccess, addFailed int64) []byte {
	var doc map[string]any
	if json.Unmarshal(base, &doc) != nil || doc == nil {
		// Tolerant of malformed JSON: fold into a fresh doc, consistent with
		// persistPreserveToggle / persistAnomalyToggle.
		doc = map[string]any{}
	}
	prevSuccess, prevFailed := parseCountersFromAuthJSON(base)
	doc[counterSuccessKey] = prevSuccess + addSuccess
	doc[counterFailedKey] = prevFailed + addFailed
	raw, err := json.Marshal(doc)
	if err != nil {
		return base
	}
	return raw
}
