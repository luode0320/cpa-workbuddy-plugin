package main

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Pure-function coverage for the panel delete path（同步自 workbuddy 0.14.7）。
//
// Coverage boundary: handleDeleteAuth itself is NOT unit-tested here. Its first
// step is hostAuthList() -> hostCall(MethodHostAuthList) -> hostAPI (a cgo
// function-pointer captured at init). In the cgo-shim test environment hostAPI
// is nil, so hostCall returns "host API unavailable" before any of the
// validation branches become reachable. The codebase has no injectable host
// seam (hostCall is a plain function), so the full request chain is verified
// via the cgo-shim build/vet plus the real panel interaction instead.
//
// What IS covered here:
//   - isQoderworkAuthFileName: the QoderWork-ownership assertion used to refuse
//     non-qoderwork files before any physical delete.
//   - clearFailoverStateForAuth: single-key failover cooldown removal.
//   - clearDeletedAccountState: the aggregate cleanup across every in-memory
//     key dimension (lifecycle, cache, active selection, anomaly, failover).
//     （preserve 与 session 绑定清理随对应功能同步后在此补充断言。）
// ---------------------------------------------------------------------------

func TestIsQoderworkAuthFileName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"uid form", "qoderwork-abc123.json", true},
		{"legacy canonical", "qoderwork.json", true},
		{"uppercase uid", "QODERWORK-X.JSON", true},
		{"surrounding whitespace", "  qoderwork-x.json  ", true},
		{"empty", "", false},
		{"wrong suffix", "qoderwork-x.txt", false},
		{"wrong prefix", "workbuddy-x.json", false},
		{"missing extension", "qoderwork-x", false},
		{"path not bare name", "/etc/qoderwork-x.json", false},
	}
	for _, tc := range cases {
		if got := isQoderworkAuthFileName(tc.in); got != tc.want {
			t.Errorf("%s: isQoderworkAuthFileName(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestClearFailoverStateForAuth(t *testing.T) {
	resetFailover(t)
	recordAccountFailure("qo-a", 429, "rate limit")
	recordAccountFailure("qo-b", 429, "rate limit")
	if !isAccountCoolingDown("qo-a") || !isAccountCoolingDown("qo-b") {
		t.Fatal("setup: both accounts should be cooling down")
	}

	clearFailoverStateForAuth("qo-a")

	if _, _, ok := failoverStateSnapshot("qo-a"); ok {
		t.Fatal("qo-a failover state should be gone after clear")
	}
	if !isAccountCoolingDown("qo-b") {
		t.Fatal("qo-b must be untouched")
	}

	// Empty key is a no-op and must not panic.
	clearFailoverStateForAuth("")
	clearFailoverStateForAuth("   ")
}

func TestClearDeletedAccountState(t *testing.T) {
	resetFailover(t)

	// Populate every in-memory trace under a primary auth.ID key.
	const id = "qo-a"
	rememberLifecycleState(id, true, "exhausted")
	accountCache.Store(id, &accountCacheEntry{plan: "pro", credits: &creditsSummary{TotalRemain: 1}})
	setActiveAuthID(id)
	preserveSetPut(id)
	anomalySetPut(id)
	recordAccountFailure(id, 429, "rate limit")
	sessionAuthMu.Lock()
	sessionAuthBindings["conv-qo"] = sessionAuthBinding{AuthID: id, ExpiresAt: time.Now().Add(time.Hour)}
	sessionAuthMu.Unlock()

	clearDeletedAccountState(id, "", "  ", id) // duplicate/blank keys must be harmless

	if _, ok := lifecycleState.Load(id); ok {
		t.Fatal("lifecycle state should be cleared")
	}
	if _, ok := accountCache.Load(id); ok {
		t.Fatal("account cache should be cleared")
	}
	if got := getActiveAuthID(); got != "" {
		t.Fatalf("active selection should be cleared, got %q", got)
	}
	if isPreserve(id) {
		t.Fatal("preserve membership should be cleared")
	}
	if isAnomaly(id) {
		t.Fatal("anomaly membership should be cleared")
	}
	if _, _, ok := failoverStateSnapshot(id); ok {
		t.Fatal("failover state should be cleared")
	}
	sessionAuthMu.RLock()
	_, stillBound := sessionAuthBindings["conv-qo"]
	sessionAuthMu.RUnlock()
	if stillBound {
		t.Fatal("session bindings pinned to the deleted account should be evicted")
	}
}

func TestClearDeletedAccountState_MultiKeyDimension(t *testing.T) {
	resetFailover(t)

	// auth.ID, auth_index and UID may each be distinct strings; each was used
	// as a key by a different code path. All three must be swept.
	for _, k := range []string{"f-id", "auth-index-1", "uid-xyz"} {
		rememberLifecycleState(k, true, "x")
		accountCache.Store(k, &accountCacheEntry{})
		preserveSetPut(k)
		anomalySetPut(k)
		recordAccountFailure(k, 429, "rate limit")
	}

	clearDeletedAccountState("f-id", "auth-index-1", "uid-xyz")

	for _, k := range []string{"f-id", "auth-index-1", "uid-xyz"} {
		if _, ok := lifecycleState.Load(k); ok {
			t.Fatalf("lifecycle state for %q should be cleared", k)
		}
		if _, ok := accountCache.Load(k); ok {
			t.Fatalf("account cache for %q should be cleared", k)
		}
		if isPreserve(k) {
			t.Fatalf("preserve membership for %q should be cleared", k)
		}
		if isAnomaly(k) {
			t.Fatalf("anomaly membership for %q should be cleared", k)
		}
		if _, _, ok := failoverStateSnapshot(k); ok {
			t.Fatalf("failover state for %q should be cleared", k)
		}
	}
}
