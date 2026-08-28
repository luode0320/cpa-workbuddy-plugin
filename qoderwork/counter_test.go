package main

import (
	"encoding/json"
	"testing"
)

// TestRecordOutcome_InMemoryTotal covers the in-memory increment path: an
// empty UID is a no-op, and successes/failures accumulate in the total.
func TestRecordOutcome_InMemoryTotal(t *testing.T) {
	clearCountersForTest()

	recordOutcome("", true) // empty uid → no-op
	recordOutcome("uid-a", true)
	recordOutcome("uid-a", true)
	recordOutcome("uid-a", false)
	recordOutcome("uid-b", false)

	s, f := counterSnapshot("uid-a")
	if s != 2 || f != 1 {
		t.Fatalf("uid-a total = (%d,%d), want (2,1)", s, f)
	}
	s, f = counterSnapshot("uid-b")
	if s != 0 || f != 1 {
		t.Fatalf("uid-b total = (%d,%d), want (0,1)", s, f)
	}
	s, f = counterSnapshot("")
	if s != 0 || f != 0 {
		t.Fatalf("empty uid total = (%d,%d), want (0,0)", s, f)
	}
}

// TestEnsureCounterLoaded_SeedsAndMerges covers the startup seeding path: the
// persisted json initializes the total, and any in-process increment that
// raced ahead of the load (a request completed before the first watchdog
// tick) is preserved on top of the persisted value. A second load is a no-op
// (idempotent) and must not double-count.
func TestEnsureCounterLoaded_SeedsAndMerges(t *testing.T) {
	clearCountersForTest()

	// recordOutcome fires before the first load.
	recordOutcome("uid-a", true)
	recordOutcome("uid-a", false)

	// json holds 100 success / 5 failed from the previous process.
	ensureCounterLoaded("uid-a", []byte(`{"success_count":100,"failed_count":5}`))

	s, f := counterSnapshot("uid-a")
	if s != 101 || f != 6 {
		t.Fatalf("uid-a after load = (%d,%d), want (101,6)", s, f)
	}

	// Second load is a no-op — must not double-count.
	ensureCounterLoaded("uid-a", []byte(`{"success_count":100,"failed_count":5}`))
	s, f = counterSnapshot("uid-a")
	if s != 101 || f != 6 {
		t.Fatalf("uid-a after 2nd load = (%d,%d), want (101,6)", s, f)
	}
}

// TestEnsureCounterLoaded_EmptyUIDNoop verifies an empty UID is ignored.
func TestEnsureCounterLoaded_EmptyUIDNoop(t *testing.T) {
	clearCountersForTest()
	ensureCounterLoaded("", []byte(`{"success_count":1}`))
	s, f := counterSnapshot("")
	if s != 0 || f != 0 {
		t.Fatalf("empty uid = (%d,%d), want (0,0)", s, f)
	}
}

// TestCounterPendingDelta_AfterLoadAndIncrement verifies the flusher's input:
// the not-yet-persisted delta is total - persisted, and the persisted value
// stays at the last-loaded json value until a flush succeeds. This is the
// exact computation flushCounters performs, asserted without host RPC (the
// flusher itself needs host.auth.get and is covered by cgo-shim integration).
func TestCounterPendingDelta_AfterLoadAndIncrement(t *testing.T) {
	clearCountersForTest()
	ensureCounterLoaded("uid-a", []byte(`{"success_count":100,"failed_count":5}`))
	recordOutcome("uid-a", true)
	recordOutcome("uid-a", false)

	counterMu.Lock()
	e := counterEntries["uid-a"]
	counterMu.Unlock()

	dS := e.success - e.persistedSuccess
	dF := e.failed - e.persistedFailed
	if dS != 1 || dF != 1 {
		t.Fatalf("pending delta = (%d,%d), want (1,1)", dS, dF)
	}
	if e.persistedSuccess != 100 || e.persistedFailed != 5 {
		t.Fatalf("persisted = (%d,%d), want (100,5)", e.persistedSuccess, e.persistedFailed)
	}
}

// TestParseCountersFromAuthJSON covers the tolerant reader: populated fields
// parse, missing fields read zero, malformed/empty input reads zero.
func TestParseCountersFromAuthJSON(t *testing.T) {
	s, f := parseCountersFromAuthJSON([]byte(`{"success_count":7,"failed_count":3}`))
	if s != 7 || f != 3 {
		t.Fatalf("populated = (%d,%d), want (7,3)", s, f)
	}
	s, f = parseCountersFromAuthJSON([]byte(`{"disabled":true,"note":"x"}`))
	if s != 0 || f != 0 {
		t.Fatalf("missing fields = (%d,%d), want (0,0)", s, f)
	}
	s, f = parseCountersFromAuthJSON([]byte(`not json`))
	if s != 0 || f != 0 {
		t.Fatalf("malformed = (%d,%d), want (0,0)", s, f)
	}
	s, f = parseCountersFromAuthJSON(nil)
	if s != 0 || f != 0 {
		t.Fatalf("nil = (%d,%d), want (0,0)", s, f)
	}
}

// TestFoldCounterIntoDoc is the pure fold unit: it increments success_count /
// failed_count on an existing top-level JSON doc while preserving every other
// key — the exact logic persistCounterDelta runs after reading the physical
// file.
func TestFoldCounterIntoDoc(t *testing.T) {
	base := []byte(`{"type":"qoderwork-provider","disabled":true,"note":"x","success_count":4,"failed_count":1,"auth":{"accessToken":"tok"},"account":{"uid":"u1"}}`)
	out := foldCounterIntoDoc(base, 3, 2)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("result not JSON: %v", err)
	}
	if m["success_count"] != float64(7) {
		t.Fatalf("success_count = %v, want 7", m["success_count"])
	}
	if m["failed_count"] != float64(3) {
		t.Fatalf("failed_count = %v, want 3", m["failed_count"])
	}
	// Preserved keys.
	if m["disabled"] != true {
		t.Fatalf("disabled lost: %v", m)
	}
	if m["note"] != "x" {
		t.Fatalf("note lost: %v", m)
	}
	if _, ok := m["auth"]; !ok {
		t.Fatalf("auth lost: %v", m)
	}
	if _, ok := m["account"]; !ok {
		t.Fatalf("account lost: %v", m)
	}

	// Malformed base folds into a fresh doc with just the counters.
	out = foldCounterIntoDoc([]byte(`not json`), 1, 1)
	m = map[string]any{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("malformed fold: %v", err)
	}
	if m["success_count"] != float64(1) || m["failed_count"] != float64(1) {
		t.Fatalf("malformed fold counters = %v", m)
	}
}

// TestFlushCounters_NoopWhenEmpty verifies the flusher returns cleanly with an
// empty counter map (no RPC, no panic).
func TestFlushCounters_NoopWhenEmpty(t *testing.T) {
	clearCountersForTest()
	flushCounters() // must not panic
}

// clearCountersForTest wipes the in-memory counter maps between tests. Mirrors
// clearFailoverStates in accountFailover.go.
func clearCountersForTest() {
	counterMu.Lock()
	counterEntries = make(map[string]counterEntry)
	counterLoaded = make(map[string]bool)
	counterMu.Unlock()
}
