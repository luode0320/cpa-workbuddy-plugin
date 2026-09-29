package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// resetTestFailed wipes the in-memory 测试 mirror so each test starts clean.
func resetTestFailed(t *testing.T) {
	t.Helper()
	testFailedSetMu.Lock()
	testFailedSet = make(map[string]struct{})
	testFailedSetMu.Unlock()
	t.Cleanup(func() {
		testFailedSetMu.Lock()
		testFailedSet = make(map[string]struct{})
		testFailedSetMu.Unlock()
	})
}

// storeCredits seeds accountCache with a credits snapshot for one auth ID and
// auto-clears it on test end. The routing tests below are its only callers.
func storeCredits(t *testing.T, id string, remain, used, total int64) {
	t.Helper()
	accountCache.Store(id, &accountCacheEntry{credits: &creditsSummary{
		TotalRemain: remain,
		TotalUsed:   used,
		TotalSize:   total,
	}})
	t.Cleanup(func() { accountCache.Delete(id) })
}

// TestSchedulerPick_TestFailedFiltered: a 「测试」account must never carry
// traffic — not even when it is the panel-selected card.
func TestSchedulerPick_TestFailedFiltered(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	storeCredits(t, "wb-tagged", 30, 0, 30)
	storeCredits(t, "wb-clean", 40, 0, 40)
	testFailedSetPut("wb-tagged")
	setActiveAuthID("wb-tagged")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-tagged", Provider: providerName},
			{ID: "wb-clean", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-clean" {
		t.Fatalf("want 测试 account skipped → wb-clean, got %+v", resp)
	}
}

// TestSchedulerPick_AllTestFailed_Defers: 测试 is a hard exclusion, so an
// all-tagged fleet must defer (cross-provider failover) rather than re-admit a
// known-broken account.
func TestSchedulerPick_AllTestFailed_Defers(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	storeCredits(t, "wb-a", 30, 0, 30)
	storeCredits(t, "wb-b", 40, 0, 40)
	testFailedSetPut("wb-a")
	testFailedSetPut("wb-b")
	setActiveAuthID("wb-b")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-a", Provider: providerName},
			{ID: "wb-b", Provider: providerName},
			{ID: "tr-a", Provider: "traework-provider"},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if resp.Handled {
		t.Fatalf("all-测试 fleet must defer (cross-provider failover), got %+v", resp)
	}
}

// TestSchedulerPick_LowCreditFirstAndUnknownLast pins the ordering contract:
// the lowest-credit account wins, and an account with no cached snapshot (-1)
// never jumps ahead of a measured one.
func TestSchedulerPick_LowCreditFirstAndUnknownLast(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	storeCredits(t, "wb-mid", 300, 0, 300)
	storeCredits(t, "wb-low", 12, 0, 300)
	accountCache.Store("wb-unknown", &accountCacheEntry{})
	defer accountCache.Delete("wb-unknown")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-unknown", Provider: providerName},
			{ID: "wb-mid", Provider: providerName},
			{ID: "wb-low", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-low" {
		t.Fatalf("want lowest-credit wb-low routed first, got %+v", resp)
	}
}

// TestSchedulerPick_UnknownCreditsRankLast: with only a measured account and an
// unmeasured one left, the measured account must win.
func TestSchedulerPick_UnknownCreditsRankLast(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	storeCredits(t, "wb-known", 900, 0, 900)
	accountCache.Store("wb-unknown", &accountCacheEntry{})
	defer accountCache.Delete("wb-unknown")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-unknown", Provider: providerName},
			{ID: "wb-known", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-known" {
		t.Fatalf("want measured wb-known ahead of unknown-credits account, got %+v", resp)
	}
}

// TestSchedulerPick_RecoversAfterTagCleared: clearing the 测试 flag (which the
// scheduled ping does on success) must put the account back into rotation.
func TestSchedulerPick_RecoversAfterTagCleared(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	storeCredits(t, "wb-recovered", 20, 0, 20)
	storeCredits(t, "wb-other", 90, 0, 90)
	testFailedSetPut("wb-recovered")
	setActiveAuthID("wb-recovered")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-recovered", Provider: providerName},
			{ID: "wb-other", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp := parsePickResponse(t, raw); !resp.Handled || resp.AuthID != "wb-other" {
		t.Fatalf("tagged account must be skipped, got %+v", resp)
	}

	// Ping succeeded → flag cleared → the low-credit account is routable again
	// and (being lowest) becomes the pick.
	testFailedSetClear("wb-recovered")
	setActiveAuthID("wb-recovered")
	raw2, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-recovered", Provider: providerName},
			{ID: "wb-other", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp := parsePickResponse(t, raw2); !resp.Handled || resp.AuthID != "wb-recovered" {
		t.Fatalf("cleared account must be routable again, got %+v", resp)
	}
}

// TestTestFailedSetBasic covers the mirror helpers the routing filter reads.
func TestTestFailedSetBasic(t *testing.T) {
	resetTestFailed(t)
	if isTestFailed("wb-1") {
		t.Fatal("unmarked account must not be test-failed")
	}
	if isTestFailed("") {
		t.Fatal("empty authID must never be test-failed")
	}
	testFailedSetPut("wb-1")
	if !isTestFailed("wb-1") {
		t.Fatal("testFailedSetPut should mark the account")
	}
	testFailedSetPut("wb-1") // idempotent
	if len(testFailedSnapshot()) != 1 {
		t.Fatalf("snapshot size = %d, want 1", len(testFailedSnapshot()))
	}
	testFailedSetClear("wb-1")
	if isTestFailed("wb-1") {
		t.Fatal("testFailedSetClear should unmark the account")
	}
	testFailedSetClear("wb-missing") // no-op; must not panic
}

// TestEnsureDefaultActiveAuth_SkipsTestFailed keeps the panel selection on the
// same availability contract as routing.
func TestEnsureDefaultActiveAuth_SkipsTestFailed(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	storeCredits(t, "a1", 10, 0, 300)
	storeCredits(t, "a2", 500, 0, 500)
	storeCredits(t, "a3", 300, 0, 300)
	testFailedSetPut("a1")
	testFailedSetPut("a2")
	setActiveAuthID("a1")
	id := ensureDefaultActiveAuth([]wbAccount{
		{AuthIndex: "a1", AuthID: "a1"},
		{AuthIndex: "a2", AuthID: "a2"},
		{AuthIndex: "a3", AuthID: "a3"},
	})
	if id != "a3" {
		t.Fatalf("want only available a3, got %q", id)
	}
}
