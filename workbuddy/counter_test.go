package main

import (
	"encoding/json"
	"testing"
)

// TestRecordOutcome_PendingDelta covers the in-memory increment path: an
// empty UID is a no-op, and successes/failures accumulate under the UID key.
func TestRecordOutcome_PendingDelta(t *testing.T) {
	clearCountersForTest()

	recordOutcome("", true) // empty uid → no-op
	recordOutcome("uid-a", true)
	recordOutcome("uid-a", true)
	recordOutcome("uid-a", false)
	recordOutcome("uid-b", false)

	s, f := counterPendingDelta("uid-a")
	if s != 2 || f != 1 {
		t.Fatalf("uid-a delta = (%d,%d), want (2,1)", s, f)
	}
	s, f = counterPendingDelta("uid-b")
	if s != 0 || f != 1 {
		t.Fatalf("uid-b delta = (%d,%d), want (0,1)", s, f)
	}
	s, f = counterPendingDelta("")
	if s != 0 || f != 0 {
		t.Fatalf("empty uid delta = (%d,%d), want (0,0)", s, f)
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
	base := []byte(`{"type":"workbuddy-provider","disabled":true,"note":"x","success_count":4,"failed_count":1,"auth":{"accessToken":"tok"},"account":{"uid":"u1"}}`)
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
// empty delta map (no RPC, no panic).
func TestFlushCounters_NoopWhenEmpty(t *testing.T) {
	clearCountersForTest()
	flushCounters() // must not panic
}

// clearCountersForTest wipes the pending delta map between tests. Mirrors
// clearFailoverStates in accountFailover.go.
func clearCountersForTest() {
	counterMu.Lock()
	counterDeltas = make(map[string]*counterDelta)
	counterMu.Unlock()
}
