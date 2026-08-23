package main

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Pure-function coverage for the panel delete path.
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
//   - isWorkbuddyAuthFileName: the WorkBuddy-ownership assertion used to refuse
//     non-workbuddy files before any physical delete.
//   - clearFailoverStateForAuth: single-key failover cooldown removal.
//   - clearDeletedAccountState: the aggregate cleanup across every in-memory
//     key dimension (lifecycle, cache, active selection, preserve, anomaly,
//     failover, session bindings).
// ---------------------------------------------------------------------------

func TestIsWorkbuddyAuthFileName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"uid form", "workbuddy-abc123.json", true},
		{"legacy canonical", "workbuddy.json", true},
		{"uppercase uid", "WORKBUDDY-X.JSON", true},
		{"surrounding whitespace", "  workbuddy-x.json  ", true},
		{"empty", "", false},
		{"wrong suffix", "workbuddy-x.txt", false},
		{"wrong prefix", "qoderwork-x.json", false},
		{"missing extension", "workbuddy-x", false},
		{"path not bare name", "/etc/workbuddy-x.json", false},
	}
	for _, tc := range cases {
		if got := isWorkbuddyAuthFileName(tc.in); got != tc.want {
			t.Errorf("%s: isWorkbuddyAuthFileName(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestClearFailoverStateForAuth(t *testing.T) {
	resetFailover(t)
	recordAccountFailure("wb-a", 429, "rate limit")
	recordAccountFailure("wb-b", 429, "rate limit")
	if !isAccountCoolingDown("wb-a") || !isAccountCoolingDown("wb-b") {
		t.Fatal("setup: both accounts should be cooling down")
	}

	clearFailoverStateForAuth("wb-a")

	if _, _, ok := failoverStateSnapshot("wb-a"); ok {
		t.Fatal("wb-a failover state should be gone after clear")
	}
	if !isAccountCoolingDown("wb-b") {
		t.Fatal("wb-b must be untouched")
	}

	// Empty key is a no-op and must not panic.
	clearFailoverStateForAuth("")
	clearFailoverStateForAuth("   ")
}

func TestClearDeletedAccountState(t *testing.T) {
	resetFailover(t)

	// Populate every in-memory trace under a primary auth.ID key.
	const id = "wb-a"
	rememberLifecycleState(id, true, "exhausted")
	accountCache.Store(id, &accountCacheEntry{plan: "pro", credits: &creditsSummary{TotalRemain: 1}})
	setActiveAuthID(id)
	preserveSetPut(id)
	anomalySetPut(id)
	recordAccountFailure(id, 429, "rate limit")
	sessionAuthMu.Lock()
	sessionAuthBindings["conv-1"] = sessionAuthBinding{AuthID: id, ExpiresAt: time.Now().Add(time.Hour)}
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
		t.Fatal("preserve flag should be cleared")
	}
	if isAnomaly(id) {
		t.Fatal("anomaly membership should be cleared")
	}
	if _, _, ok := failoverStateSnapshot(id); ok {
		t.Fatal("failover state should be cleared")
	}
	sessionAuthMu.RLock()
	_, stillBound := sessionAuthBindings["conv-1"]
	sessionAuthMu.RUnlock()
	if stillBound {
		t.Fatal("session binding should be evicted")
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
			t.Fatalf("preserve flag for %q should be cleared", k)
		}
		if isAnomaly(k) {
			t.Fatalf("anomaly membership for %q should be cleared", k)
		}
		if _, _, ok := failoverStateSnapshot(k); ok {
			t.Fatalf("failover state for %q should be cleared", k)
		}
	}
}
