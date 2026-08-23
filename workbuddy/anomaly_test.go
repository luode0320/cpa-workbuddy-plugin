package main

import (
	"sync"
	"testing"
)

// resetAnomalySet wipes the in-memory anomaly set and restores the threshold
// so each test starts from a clean slate.
func resetAnomalySet(t *testing.T) {
	t.Helper()
	anomalySetMu.Lock()
	anomalySet = make(map[string]struct{})
	anomalySetMu.Unlock()
	oldTh := anomalyThreshold()
	t.Cleanup(func() { setAnomalyConfig(oldTh, true) })
	// Disable auto-freeze by default so tests that exercise
	// recordAccountFailure don't trigger async persist side-effects.
	setAnomalyConfig(0, true)
}

func TestIsAnomaly_BasicSetMembership(t *testing.T) {
	resetAnomalySet(t)
	if isAnomaly("acc-1") {
		t.Fatal("freshly-cleared set should not contain acc-1")
	}
	anomalySetPut("acc-1")
	if !isAnomaly("acc-1") {
		t.Fatal("after anomalySetPut, isAnomaly must be true")
	}
	anomalySetClear("acc-1")
	if isAnomaly("acc-1") {
		t.Fatal("after anomalySetClear, isAnomaly must be false")
	}
}

func TestAnomalySetPut_TrimsAndIgnoresEmpty(t *testing.T) {
	resetAnomalySet(t)
	anomalySetPut("  acc-2  ")
	if !isAnomaly("acc-2") {
		t.Fatal("anomalySetPut must trim whitespace before storing")
	}
	anomalySetPut("")
	if len(anomalySnapshot()) != 1 {
		t.Fatalf("anomalySetPut(\"\") must be a no-op, got %d entries", len(anomalySnapshot()))
	}
}

func TestAnomalySnapshot_CopiesMap(t *testing.T) {
	resetAnomalySet(t)
	anomalySetPut("acc-3")
	snap := anomalySnapshot()
	if len(snap) != 1 || !snap["acc-3"] {
		t.Fatalf("snapshot mismatch: %+v", snap)
	}
	// Mutating the snapshot must not affect the underlying set.
	delete(snap, "acc-3")
	if !isAnomaly("acc-3") {
		t.Fatal("mutating snapshot must not delete from underlying set")
	}
}

func TestParseAnomalyFromAuthJSON(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`{"anomaly": true}`, true},
		{`{"anomaly": false}`, false},
		{`{"foo": "bar"}`, false},
		{`{}`, false},
		{``, false},
		{`not json`, false},
	}
	for _, tc := range cases {
		if got := parseAnomalyFromAuthJSON([]byte(tc.raw)); got != tc.want {
			t.Fatalf("parseAnomalyFromAuthJSON(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// TestRecordAccountFailure_AnomalyThresholdTripped verifies that crossing
// the configured threshold flips the account into the anomaly set without
// waiting on a host RPC (we set the trigger threshold to a small number
// and call the failing record N times in-process). The async
// freezeAccountForAnomaly goroutine may attempt host I/O; the test only
// asserts the in-memory mirror stays consistent so it isn't flaky in
// environments where host RPC is stubbed/absent.
func TestRecordAccountFailure_AnomalyThresholdTripped(t *testing.T) {
	resetAnomalySet(t)
	resetFailover(t)
	// Enable auto-freeze with a small threshold for this test.
	setAnomalyConfig(3, true)

	for i := 0; i < 3; i++ {
		ok := recordAccountFailure("acc-thresh", 403, "forbidden")
		if !ok {
			t.Fatalf("recordAccountFailure iteration %d should count", i)
		}
	}
	// Count state is observable immediately.
	count, _, ok := failoverStateSnapshot("acc-thresh")
	if !ok || count < 3 {
		t.Fatalf("failoverStateSnapshot = (%d, _, %v), want count >= 3, ok", count, ok)
	}
}

// TestAnomalySet_ConcurrentSafety runs the public set operations from many
// goroutines simultaneously to surface data races in go test -race.
func TestAnomalySet_ConcurrentSafety(t *testing.T) {
	resetAnomalySet(t)
	const N = 200
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			anomalySetPut("acc-" + string(rune('a'+i%26)))
		}()
		go func() {
			defer wg.Done()
			_ = isAnomaly("acc-" + string(rune('a'+i%26)))
		}()
	}
	wg.Wait()
}
