package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// resetPreserve wipes the in-memory preserve set so each test starts clean
// and never leaks marks into later tests.
func resetPreserve(t *testing.T) {
	t.Helper()
	preserveSetMu.Lock()
	preserveSet = make(map[string]struct{})
	preserveSetMu.Unlock()
	t.Cleanup(func() {
		preserveSetMu.Lock()
		preserveSet = make(map[string]struct{})
		preserveSetMu.Unlock()
	})
}

// storeCredits seeds accountCache with a credits entry for one auth ID and
// auto-clears it on test end. traework's credits snapshot is traeCredits.
func storeCredits(t *testing.T, id string, remain int64) {
	t.Helper()
	accountCache.Store(id, &accountCacheEntry{credits: &traeCredits{
		TotalRemain: remain,
		FetchedAt:   time.Now().Format(time.RFC3339),
	}})
	t.Cleanup(func() { accountCache.Delete(id) })
}

// resetActiveAuth resets the panel selection and pins scheduler mode for the
// duration of a routing test (traework default mode is off).
func resetActiveAuth(t *testing.T) {
	t.Helper()
	setActiveAuthID("")
	restoreMode := setSchedulerMode(schedulerModeCredits)
	t.Cleanup(func() {
		setActiveAuthID("")
		restoreMode()
	})
}

// TestPreserveShouldFlip is the watchdog's decision table: an account is
// parked in the preserve set when remain < threshold (strictly below) and
// released when credits recover to >= threshold. No-op states must report
// changed=false so the tick skips disk writes.
func TestPreserveShouldFlip(t *testing.T) {
	cases := []struct {
		name        string
		remain      int64
		threshold   int64
		preserved   bool
		wantShould  bool
		wantChanged bool
	}{
		{"below threshold, free", 49, 50, false, true, true},
		{"below threshold, already preserved", 10, 50, true, true, false},
		{"exactly at threshold, free", 50, 50, false, false, false},
		{"above threshold, free", 500, 50, false, false, false},
		{"recovered to threshold, preserved", 50, 50, true, false, true},
		{"recovered above threshold, preserved", 120, 50, true, false, true},
		{"zero threshold, zero remain", 0, 0, false, false, false},
		{"zero threshold, negative remain", -1, 0, false, true, true},
	}
	for _, c := range cases {
		should, changed := preserveShouldFlip(c.remain, c.threshold, c.preserved)
		if should != c.wantShould || changed != c.wantChanged {
			t.Errorf("%s: preserveShouldFlip(%d,%d,%v) = (%v,%v), want (%v,%v)",
				c.name, c.remain, c.threshold, c.preserved, should, changed, c.wantShould, c.wantChanged)
		}
	}
}

// TestPreserveConfigDefaultsAndSet covers the runtime-tunable knobs: defaults
// match the documented contract (50 credits / 10m / enabled) and
// setPreserveConfig applies values under their own locks.
func TestPreserveConfigDefaultsAndSet(t *testing.T) {
	if got := preserveThreshold(); got != preserveThresholdDefault {
		t.Fatalf("default threshold = %d, want %d", got, preserveThresholdDefault)
	}
	if got := preserveWatchdogInterval(); got != preserveWatchdogIntervalDefault {
		t.Fatalf("default interval = %v, want %v", got, preserveWatchdogIntervalDefault)
	}
	if got := preserveWatchdogEnabled(); !got {
		t.Fatal("default watchdog should be enabled")
	}
	// Invalid values are ignored: negative threshold and zero interval must
	// not clobber the current configuration.
	setPreserveConfig(-5, 0, false)
	if got := preserveThreshold(); got != preserveThresholdDefault {
		t.Fatalf("negative threshold must be ignored, got %d", got)
	}
	if got := preserveWatchdogInterval(); got != preserveWatchdogIntervalDefault {
		t.Fatalf("zero interval must be ignored, got %v", got)
	}
	if preserveWatchdogEnabled() {
		t.Fatal("enabled=false should apply")
	}
	// Valid values apply.
	setPreserveConfig(30, 5*time.Minute, true)
	if got := preserveThreshold(); got != 30 {
		t.Fatalf("threshold = %d, want 30", got)
	}
	if got := preserveWatchdogInterval(); got != 5*time.Minute {
		t.Fatalf("interval = %v, want 5m", got)
	}
	if !preserveWatchdogEnabled() {
		t.Fatal("enabled=true should apply")
	}
	// Restore defaults for later tests.
	setPreserveConfig(preserveThresholdDefault, preserveWatchdogIntervalDefault, preserveWatchdogEnabledDefault)
}

// TestParsePreserveFromAuthJSON covers the disk-format contract: the top-level
// preserve boolean on the physical auth file.
func TestParsePreserveFromAuthJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty doc", `{}`, false},
		{"preserve true", `{"preserve":true}`, true},
		{"preserve false", `{"preserve":false}`, false},
		{"other fields only", `{"disabled":true,"note":"x"}`, false},
		{"malformed json", `not-json{`, false},
	}
	for _, c := range cases {
		if got := parsePreserveFromAuthJSON([]byte(c.raw)); got != c.want {
			t.Errorf("%s: parsePreserveFromAuthJSON(%s) = %v, want %v", c.name, c.raw, got, c.want)
		}
	}
}

// TestPreserveSetBasic covers the in-memory set helpers used by the watchdog
// and scheduler: put / clear / is / snapshot.
func TestPreserveSetBasic(t *testing.T) {
	resetPreserve(t)
	if isPreserve("tr-1") {
		t.Fatal("unmarked account should not be preserved")
	}
	if isPreserve("") {
		t.Fatal("empty authID must never be preserved")
	}
	preserveSetPut("tr-1")
	if !isPreserve("tr-1") {
		t.Fatal("preserveSetPut should mark the account")
	}
	preserveSetPut("tr-1") // idempotent
	if len(preserveSnapshot()) != 1 {
		t.Fatalf("snapshot size = %d, want 1", len(preserveSnapshot()))
	}
	preserveSetClear("tr-1")
	if isPreserve("tr-1") {
		t.Fatal("preserveSetClear should unmark the account")
	}
	preserveSetClear("tr-missing") // no-op; must not panic
}

// TestSchedulerPick_PreserveFiltered is the core routing contract: a preserve
// account is excluded even when it is the panel-selected active account — the
// picker must route to a healthy non-preserved account instead.
func TestSchedulerPick_PreserveFiltered(t *testing.T) {
	resetActiveAuth(t)
	resetPreserve(t)
	resetFailover(t)
	storeCredits(t, "tr-pres", 30)
	storeCredits(t, "tr-norm", 500)
	preserveSetPut("tr-pres")
	setActiveAuthID("tr-pres") // panel selects the preserved account
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-pres", Provider: providerName},
			{ID: "tr-norm", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "tr-norm" {
		t.Fatalf("want preserved account skipped → tr-norm, got %+v", resp)
	}
}

// TestSchedulerPick_AllPreserved_KeepsFullList: when EVERY traework account
// is preserved the preserve filter keeps the full list so routing falls back
// to the current pin (mirrors the all-cooldown fallback) instead of deferring
// to the built-in scheduler.
func TestSchedulerPick_AllPreserved_KeepsFullList(t *testing.T) {
	resetActiveAuth(t)
	resetPreserve(t)
	resetFailover(t)
	storeCredits(t, "tr-a", 10)
	storeCredits(t, "tr-b", 20)
	preserveSetPut("tr-a")
	preserveSetPut("tr-b")
	setActiveAuthID("tr-b")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-a", Provider: providerName},
			{ID: "tr-b", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled {
		t.Fatalf("all-preserved fleet should still fall back to the pin, got %+v", resp)
	}
	if resp.AuthID != "tr-b" {
		t.Fatalf("want fallback to panel pin tr-b, got %+v", resp)
	}
}

// TestWaitHostReadyForWatchdog covers the startup-readiness probe used to
// close the init-race blind window.
func TestWaitHostReadyForWatchdog(t *testing.T) {
	// Case 1: always-ready, short maxWait — instant true, zero sleeps.
	calls := 0
	ready := func() bool { calls++; return true }
	if !waitHostReadyForWatchdog(50*time.Millisecond, ready) {
		t.Fatal("always-ready should return true")
	}
	if calls != 1 {
		t.Fatalf("expected one ready() call, got %d", calls)
	}

	// Case 2: becomes-ready on the second call.
	calls = 0
	ready = func() bool { calls++; return calls >= 2 }
	if !waitHostReadyForWatchdog(2*time.Second, ready) {
		t.Fatal("eventually-ready should return true")
	}
	if calls < 2 {
		t.Fatalf("expected at least 2 probes, got %d", calls)
	}

	// Case 3: never-ready, maxWait=0 — returns whatever ready() says.
	calls = 0
	ready = func() bool { calls++; return false }
	if waitHostReadyForWatchdog(0, ready) {
		t.Fatal("maxWait=0 + never-ready must return false")
	}
	if calls != 1 {
		t.Fatalf("maxWait=0 must probe exactly once, got %d", calls)
	}

	// Case 4: never-ready with a tight maxWait — returns false once the
	// deadline passes.
	calls = 0
	ready = func() bool { calls++; return false }
	start := time.Now()
	if waitHostReadyForWatchdog(20*time.Millisecond, ready) {
		t.Fatal("never-ready under a finite maxWait must return false")
	}
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("wait overshot the deadline: %v", elapsed)
	}
	if calls < 1 {
		t.Fatalf("never-ready should be probed at least once, got %d", calls)
	}
}

// TestRequestPreserveTickCoalesces proves the trigger channel collapses
// concurrent requesters onto a single queued tick.
func TestRequestPreserveTickCoalesces(t *testing.T) {
	// Drain anything queued by a previous test (the chan is package-global).
	for {
		select {
		case <-preserveTickCh:
		default:
			goto drained
		}
	}
drained:
	requestPreserveTick()
	requestPreserveTick()
	requestPreserveTick()
	select {
	case <-preserveTickCh:
		// OK — exactly one queued.
	default:
		t.Fatal("expected at least one tick queued after coalesced requests")
	}
	select {
	case <-preserveTickCh:
		t.Fatal("requestPreserveTick must coalesce; got a second queued value")
	default:
	}
}

// TestPreserveFlipsNeeded is the decision table for the dashboard-side
// reconcile path. Same threshold contract as the watchdog (remain<threshold
// → preserve) and must skip no-ops so we don't churn disk writes on every
// panel refresh.
func TestPreserveFlipsNeeded(t *testing.T) {
	threshold := int64(50)
	cases := []struct {
		name      string
		authIndex string
		authID    string
		remain    int64
		currently bool // set preserveSetPut before calling
		want      *preserveFlipDecision
	}{
		{"below threshold, currently free → enter preserve", "tr-low-idx", "tr-low", 27, false, &preserveFlipDecision{AuthIndex: "tr-low-idx", AuthID: "tr-low", Preserve: true}},
		{"below threshold, currently preserved → no flip", "tr-already-idx", "tr-already", 10, true, nil},
		{"recovered above threshold, currently preserved → exit preserve", "tr-recov-idx", "tr-recov", 120, true, &preserveFlipDecision{AuthIndex: "tr-recov-idx", AuthID: "tr-recov", Preserve: false}},
		{"above threshold, currently free → no flip", "tr-healthy-idx", "tr-healthy", 200, false, nil},
		{"no credits snapshot (remain=-1) → skip", "tr-unk-idx", "tr-unk", -1, false, nil},
	}
	for _, c := range cases {
		resetPreserve(t)
		if c.currently {
			preserveSetPut(c.authID)
		}
		acct := traeAccountView{AuthIndex: c.authIndex, AuthID: c.authID, Remain: c.remain}
		flips := preserveFlipsNeeded([]traeAccountView{acct}, threshold)
		switch {
		case c.want == nil && len(flips) != 0:
			t.Errorf("%s: expected no flips, got %+v", c.name, flips)
		case c.want != nil && len(flips) != 1:
			t.Errorf("%s: expected one flip, got %+v", c.name, flips)
		case c.want != nil && (flips[0].AuthIndex != c.want.AuthIndex || flips[0].AuthID != c.want.AuthID || flips[0].Preserve != c.want.Preserve):
			t.Errorf("%s: flip=%+v want=%+v", c.name, flips[0], *c.want)
		}
	}
}
