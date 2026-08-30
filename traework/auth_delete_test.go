package main

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Pure-function coverage for the panel delete path.
//
// Coverage boundary: handleDeleteAuth itself is NOT unit-tested here. Its
// first step is hostAuthList() -> hostCall(MethodHostAuthList) -> hostAPI (a
// cgo function-pointer captured at init). In the cgo-shim test environment
// hostAPI is nil, so hostCall returns "host API unavailable" before any of
// the validation branches become reachable. The codebase has no injectable
// host seam (hostCall is a plain function), so the full request chain is
// verified via the cgo-shim build/vet plus the real panel interaction
// instead (mirrors workbuddy/auth_delete_test.go).
//
// What IS covered here:
//   - isTraeworkAuthFileName: the TraeWork-ownership assertion used to refuse
//     non-traework files before any physical delete.
//   - clearFailoverStateForAuth: single-key failover cooldown removal.
//   - clearDeletedAccountState: the aggregate cleanup across every in-memory
//     key dimension (cache, active selection, preserve, anomaly, failover,
//     session bindings).
// ---------------------------------------------------------------------------

func TestIsTraeworkAuthFileName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"uid form", "traework-abc123.json", true},
		{"legacy canonical", "traework.json", true},
		{"uppercase uid", "TRAEWORK-X.JSON", true},
		{"surrounding whitespace", "  traework-x.json  ", true},
		{"empty", "", false},
		{"wrong suffix", "traework-x.txt", false},
		{"wrong prefix", "workbuddy-x.json", false},
		{"missing extension", "traework-x", false},
		{"path not bare name", "/etc/traework-x.json", false},
	}
	for _, tc := range cases {
		if got := isTraeworkAuthFileName(tc.in); got != tc.want {
			t.Errorf("%s: isTraeworkAuthFileName(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestClearFailoverStateForAuth(t *testing.T) {
	resetFailover(t)
	recordAccountFailure("tr-a", 429, "rate limit")
	recordAccountFailure("tr-b", 429, "rate limit")
	if !isAccountCoolingDown("tr-a") || !isAccountCoolingDown("tr-b") {
		t.Fatal("setup: both accounts should be cooling down")
	}

	clearFailoverStateForAuth("tr-a")

	if _, _, ok := failoverStateSnapshot("tr-a"); ok {
		t.Fatal("tr-a failover state should be gone after clear")
	}
	if !isAccountCoolingDown("tr-b") {
		t.Fatal("tr-b must be untouched")
	}

	// Empty key is a no-op and must not panic.
	clearFailoverStateForAuth("")
	clearFailoverStateForAuth("   ")
}

func TestClearDeletedAccountState(t *testing.T) {
	resetFailover(t)

	// Populate every in-memory trace under a primary auth.ID key.
	const id = "tr-a"
	accountCache.Store(id, &accountCacheEntry{credits: &traeCredits{TotalRemain: 1}})
	setActiveAuthID(id)
	preserveSetPut(id)
	anomalySetPut(id)
	recordAccountFailure(id, 429, "rate limit")
	sessionAuthMu.Lock()
	sessionAuthBindings["conv-1"] = sessionAuthBinding{AuthID: id, ExpiresAt: time.Now().Add(time.Hour)}
	sessionAuthMu.Unlock()

	clearDeletedAccountState(id, "", "  ", id) // duplicate/blank keys must be harmless

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
		accountCache.Store(k, &accountCacheEntry{})
		preserveSetPut(k)
		anomalySetPut(k)
		recordAccountFailure(k, 429, "rate limit")
	}

	clearDeletedAccountState("f-id", "auth-index-1", "uid-xyz")

	for _, k := range []string{"f-id", "auth-index-1", "uid-xyz"} {
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
