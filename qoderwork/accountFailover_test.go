package main

import (
	"strconv"
	"testing"
	"time"
)

// resetFailover clears all failover state and restores the enabled flag so
// each test starts from a clean slate.
func resetFailover(t *testing.T) {
	t.Helper()
	clearFailoverStates()
	oldEnabled := failoverActive()
	t.Cleanup(func() {
		clearFailoverStates()
		setFailoverEnabled(oldEnabled)
	})
	setFailoverEnabled(true)
}

// assertCooldownNear verifies the account's cooldown deadline is roughly the
// given tier away from now (allows small test-run latency).
func assertCooldownNear(t *testing.T, authID string, tier time.Duration) {
	t.Helper()
	count, until, ok := failoverStateSnapshot(authID)
	if !ok {
		t.Fatalf("no failover state for %q", authID)
	}
	if count < 1 {
		t.Fatalf("failover count for %q = %d, want >= 1", authID, count)
	}
	remain := time.Until(until)
	if remain <= tier-5*time.Second || remain > tier+5*time.Second {
		t.Fatalf("cooldown remain for %q = %v, want ~%v (count %d)", authID, remain, tier, count)
	}
}

func TestFailoverCooldownFor_Fixed(t *testing.T) {
	cases := []struct {
		count int
		want  time.Duration
	}{
		{0, 0},
		{1, 15 * time.Second},
		{2, 15 * time.Second},
		{3, 15 * time.Second},
		{4, 15 * time.Second},
		{99, 15 * time.Second},
	}
	for _, tc := range cases {
		if got := failoverCooldownFor(tc.count); got != tc.want {
			t.Fatalf("failoverCooldownFor(%d) = %v, want %v", tc.count, got, tc.want)
		}
	}
}

func TestRecordAccountFailure_FixedCooldown(t *testing.T) {
	resetFailover(t)
	// Every failure — 1st, 2nd, 3rd, 4th — cools for exactly 15 seconds.
	for i := 1; i <= 4; i++ {
		recordAccountFailure("acc-1", 429, "rate limit")
		assertCooldownNear(t, "acc-1", 15*time.Second)
	}
}

func TestRecordAccountFailure_ResetOnSuccess(t *testing.T) {
	resetFailover(t)
	recordAccountFailure("acc-1", 429, "rate limit")
	if !isAccountCoolingDown("acc-1") {
		t.Fatal("account should be cooling down after a failure")
	}
	resetAccountFailover("acc-1")
	count, until, ok := failoverStateSnapshot("acc-1")
	if !ok || count != 0 {
		t.Fatalf("after reset: count = %d, ok = %v; want count 0", count, ok)
	}
	if !until.IsZero() {
		t.Fatalf("after reset: cooldownUntil = %v, want zero", until)
	}
	if isAccountCoolingDown("acc-1") {
		t.Fatal("account should not be cooling down after reset")
	}
}

func TestRecordAccountFailure_Business4xxExcluded(t *testing.T) {
	resetFailover(t)
	// Business 400 does NOT count.
	if recordAccountFailure("acc-1", 400, "bad request: unknown model") {
		t.Fatal("business 400 must not be counted as account failure")
	}
	if recordAccountFailure("acc-1", 400, "bad request: unknown model") {
		t.Fatal("business 400 must not be counted as account failure")
	}
	count, _, _ := failoverStateSnapshot("acc-1")
	if count != 0 {
		t.Fatalf("count = %d after two 400s, want 0", count)
	}
	// 429 counts on top of nothing; another 429 also cools 15s (fixed).
	recordAccountFailure("acc-1", 429, "rate limit")
	assertCooldownNear(t, "acc-1", 15*time.Second)
	recordAccountFailure("acc-1", 429, "rate limit")
	assertCooldownNear(t, "acc-1", 15*time.Second)
}

// TestRecordAccountFailure_AccountLevel4xxCounted locks in the v0.12
// behavior change: 401/403/404/405 now count as account-level failures
// so the same-request failover loop in stream.go can switch accounts.
// 400 is intentionally excluded (request-shaped error, not account-shaped).
func TestRecordAccountFailure_AccountLevel4xxCounted(t *testing.T) {
	resetFailover(t)
	for _, status := range []int{401, 403, 404, 405} {
		if !recordAccountFailure("acc-"+strconv.Itoa(status), status, "upstream rejected") {
			t.Fatalf("account-level %d must count as account failure", status)
		}
	}
	if recordAccountFailure("acc-400", 400, "bad request shape") {
		t.Fatal("business 400 must still NOT count")
	}
}

func TestIsAccountLevel4xx_Classification(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{200, false},
		{400, false}, // business — excluded by design
		{401, true},
		{403, true},
		{404, true},
		{405, true},
		{406, false},
		{409, false},
		{410, false},
		{418, false},
		{422, false},
		{429, true}, // v0.9.1: same-request rotation, was false before
		{500, false}, // covered by isAccountFailure's 5xx branch
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			if got := isAccountLevel4xx(tc.status); got != tc.want {
				t.Fatalf("isAccountLevel4xx(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestRecordAccountFailure_TransportAnd5xxCounted(t *testing.T) {
	resetFailover(t)
	if !recordAccountFailure("acc-1", 0, "connection refused") {
		t.Fatal("transport-level failure (status 0) must be counted")
	}
	if !recordAccountFailure("acc-2", 502, "upstream gateway error") {
		t.Fatal("5xx must be counted")
	}
	if recordAccountFailure("acc-3", 200, "ok") {
		t.Fatal("success (200) must not be counted")
	}
	if recordAccountFailure("acc-3", 204, "") {
		t.Fatal("success (204) must not be counted")
	}
}

func TestIsAccountFailure_Classification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"transport failure", 0, "dial tcp: connection refused", true},
		{"429 rate limit", 429, "rate limit exceeded", true},
		{"429 empty body", 429, "", true},
		{"402 payment required", 402, "payment required", true},
		{"402 credit marker in body", 200, `{"error":"insufficient credit"}`, true},
		{"5xx", 503, "service unavailable", true},
		{"5xx empty body", 500, "", true},
		{"account-level 401", 401, "token expired", true},
		{"account-level 403", 403, "no permission", true},
		{"account-level 404", 404, "endpoint not found", true},
		{"account-level 405", 405, "method not allowed", true},
		{"business 400", 400, "bad request", false},
		{"business 422", 422, "validation failed", false},
		{"success", 200, "ok", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAccountFailure(tc.status, tc.body); got != tc.want {
				t.Fatalf("isAccountFailure(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestFailoverCooldownExpiry(t *testing.T) {
	resetFailover(t)
	recordAccountFailure("acc-1", 429, "rate limit")
	if !isAccountCoolingDown("acc-1") {
		t.Fatal("account should be cooling down right after failure")
	}
	// Simulate the cooldown window elapsing: the counter persists but the
	// account becomes routable again.
	setFailoverCooldownUntil("acc-1", time.Now().Add(-time.Second))
	if isAccountCoolingDown("acc-1") {
		t.Fatal("account should be routable after cooldown expires")
	}
	// A new failure re-enters cooldown at the fixed 15s window.
	recordAccountFailure("acc-1", 429, "rate limit")
	assertCooldownNear(t, "acc-1", 15*time.Second)
}

func TestFailoverDisabled(t *testing.T) {
	resetFailover(t)
	setFailoverEnabled(false)
	if recordAccountFailure("acc-1", 429, "rate limit") {
		t.Fatal("recordAccountFailure must be a no-op when disabled")
	}
	if isAccountCoolingDown("acc-1") {
		t.Fatal("no cooldown when disabled")
	}
	resetAccountFailover("acc-1") // must not panic
}
