// counter.go owns the plugin-side cumulative success/failure counters.
//
// Why this exists: the panel's "成功 / 失败" columns were historically
// sourced from the host's recent-request window (HostAuthFileEntry.Success /
// Failed). Those fields are `json:"-"` in the CPA auth type — they are
// memory-only rolling counters (10min × 20 buckets ≈ last 200 minutes), never
// serialized to the auth file, so a container restart zeroes them. This
// module replaces that with counters the plugin itself owns, persisted into
// the physical auth file's top-level JSON so they survive restarts.
//
// Persistence model:
//   - recordOutcome increments an in-memory delta map keyed by account UID
//     (the executor's stable account identity, same key the scheduler /
//     failover / preserve / anomaly layers already use).
//   - A background flusher (counterFlushInterval) folds each pending delta
//     into the auth file's top-level `success_count` / `failed_count` fields
//     and clears the in-memory delta.
//   - The panel reads success_count/failed_count straight from the physical
//     auth JSON (already fetched via hostAuthGetBundle) plus any not-yet-
//     flushed in-memory delta, so the number is live without an extra RPC.
//
// Field naming is deliberately success_count / failed_count (NOT success /
// failed): the host's HostAuthFileEntry already exposes `success` / `failed`
// for its own recent window, and reusing those names on the physical file
// would create an ambiguous dual source of truth. The distinct names make it
// obvious this is the plugin's own persisted cumulative counter.
//
// UID-less accounts (legacy single-file workbuddy.json) are skipped: without
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
	"time"
)

// counterFlushInterval bounds how often pending success/failure deltas are
// folded into the physical auth files. 10s keeps the panel's number within
// ~10s of live while bounding write amplification under bursty traffic (each
// flush is one direct write per dirty account, not one per request).
const counterFlushInterval = 10 * time.Second

// Top-level auth-file keys carrying the plugin-owned cumulative counters.
const (
	counterSuccessKey = "success_count"
	counterFailedKey  = "failed_count"
)

// counterDelta is the not-yet-persisted increment for one account.
type counterDelta struct {
	success int64
	failed  int64
}

var (
	counterMu sync.Mutex
	// counterDeltas maps account UID → pending delta. Keyed by UID so the
	// flusher can resolve the physical file via resolveAuthIndexAndID.
	counterDeltas = make(map[string]*counterDelta)
)

// recordOutcome records one completed executor request against the account.
// uid is the account UID (empty → no-op, see package doc). success=true for a
// served request, false for an upstream/transport failure that surfaced to
// the caller. Increment-only and non-blocking: it never touches disk.
func recordOutcome(uid string, success bool) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	counterMu.Lock()
	d := counterDeltas[uid]
	if d == nil {
		d = &counterDelta{}
		counterDeltas[uid] = d
	}
	if success {
		d.success++
	} else {
		d.failed++
	}
	counterMu.Unlock()
}

// counterPendingDelta returns the in-memory (not yet flushed) delta for uid.
// Used by the panel to add the live portion on top of the persisted value.
func counterPendingDelta(uid string) (success, failed int64) {
	uid = strings.TrimSpace(uid)
	counterMu.Lock()
	defer counterMu.Unlock()
	if d := counterDeltas[uid]; d != nil {
		return d.success, d.failed
	}
	return 0, 0
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

// startCounterFlusher launches the background fold loop. Started from init()
// so it runs for the lifetime of the plugin process. The loop reads the
// interval from a constant (not config) — counters are pure observability and
// must never be tunable to a value that disables persistence by accident.
func startCounterFlusher() {
	go func() {
		ticker := time.NewTicker(counterFlushInterval)
		defer ticker.Stop()
		for range ticker.C {
			flushCounters()
		}
	}()
}

// flushCounters drains the pending delta map and folds each entry into its
// account's physical auth file. It is idempotent and failure-tolerant: a
// failed fold keeps the delta in memory so the next tick retries. Called by
// the background flusher and exposed for tests.
func flushCounters() {
	counterMu.Lock()
	if len(counterDeltas) == 0 {
		counterMu.Unlock()
		return
	}
	// Swap the map out so the RPC+disk work below runs without holding the
	// lock (new increments land in a fresh map, no lost updates).
	toFlush := counterDeltas
	counterDeltas = make(map[string]*counterDelta)
	counterMu.Unlock()

	for uid, d := range toFlush {
		if d == nil || (d.success == 0 && d.failed == 0) {
			continue
		}
		if err := persistCounterDelta(uid, d.success, d.failed); err != nil {
			// Fold back so a later tick retries; no increment is lost.
			counterMu.Lock()
			cur := counterDeltas[uid]
			if cur == nil {
				counterDeltas[uid] = d
			} else {
				cur.success += d.success
				cur.failed += d.failed
			}
			counterMu.Unlock()
		}
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
		name = "workbuddy-" + uid + ".json"
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
