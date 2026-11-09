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

// disableLowCreditLine 关闭积分硬禁用线：本文件的用例只测「测试」标签与排序，
// 账号 remain 是象征值（12~90）。0.5.30 起默认禁用线为 100，若不关闭，这些
// 账号会先被判停用，用例测到的就不是标签语义了。
// [参数] t：测试上下文。
// [返回] 无；用例结束时自动恢复原值。
// 最近修改时间：2026-11-09；改动原因：默认禁用线改为 100，隔离本文件的测点。
func disableLowCreditLine(t *testing.T) {
	t.Helper()
	restore := setLowCreditDisableBelow(0)
	t.Cleanup(restore)
}

// TestSchedulerPick_TestFailedFiltered: a 「测试」account must never carry
// traffic — not even when it is the panel-selected card.
func TestSchedulerPick_TestFailedFiltered(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	disableLowCreditLine(t)
	storeCredits(t, "tr-tagged", 30)
	storeCredits(t, "tr-clean", 40)
	testFailedSetPut("tr-tagged")
	setActiveAuthID("tr-tagged")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-tagged", Provider: providerName},
			{ID: "tr-clean", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "tr-clean" {
		t.Fatalf("want 测试 account skipped → tr-clean, got %+v", resp)
	}
}

// TestSchedulerPick_AllTestFailed_Defers: 测试 is a hard exclusion, so an
// all-tagged fleet must defer (cross-provider failover) rather than re-admit a
// known-broken account.
func TestSchedulerPick_AllTestFailed_Defers(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	disableLowCreditLine(t)
	storeCredits(t, "tr-a", 30)
	storeCredits(t, "tr-b", 40)
	testFailedSetPut("tr-a")
	testFailedSetPut("tr-b")
	setActiveAuthID("tr-b")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-a", Provider: providerName},
			{ID: "tr-b", Provider: providerName},
			{ID: "wb-a", Provider: "workbuddy-provider"},
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

// TestSchedulerPick_LowCreditFirst: a below-floor account is hard-excluded,
// so the remaining healthy account is routed. The pick request carries no
// session signal, so the picker falls back to the candidate order built by
// handleSchedulerPick. (The test name predates the low-credit-first reversal;
// current routing is HIGH-CREDIT-FIRST.)
func TestSchedulerPick_LowCreditFirst(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	disableLowCreditLine(t)
	storeCredits(t, "tr-mid", 300)
	storeCredits(t, "tr-low", 12)
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-mid", Provider: providerName},
			{ID: "tr-low", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	// tr-low (12 < 20) 低于安全红线被硬排除，tr-mid (300) 被选中
	if !resp.Handled || resp.AuthID != "tr-mid" {
		t.Fatalf("want healthy tr-mid routed (tr-low < 20 excluded), got %+v", resp)
	}
}

// TestSchedulerPick_UnknownCreditsRankLast: with a measured account and an
// unmeasured one left, the measured account must win.
func TestSchedulerPick_UnknownCreditsRankLast(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	storeCredits(t, "tr-known", 900)
	accountCache.Store("tr-unknown", &accountCacheEntry{})
	defer accountCache.Delete("tr-unknown")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-unknown", Provider: providerName},
			{ID: "tr-known", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "tr-known" {
		t.Fatalf("want measured tr-known ahead of unknown-credits account, got %+v", resp)
	}
}

// TestSchedulerPick_RecoversAfterTagCleared: clearing the 测试 flag (which the
// scheduled ping does on success) must put the account back into rotation.
func TestSchedulerPick_RecoversAfterTagCleared(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	disableLowCreditLine(t)
	storeCredits(t, "tr-recovered", 20)
	storeCredits(t, "tr-other", 90)
	testFailedSetPut("tr-recovered")
	setActiveAuthID("tr-recovered")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-recovered", Provider: providerName},
			{ID: "tr-other", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp := parsePickResponse(t, raw); !resp.Handled || resp.AuthID != "tr-other" {
		t.Fatalf("tagged account must be skipped, got %+v", resp)
	}

	testFailedSetClear("tr-recovered")
	resetSessionRouting(t)
	setActiveAuthID("tr-recovered")
	raw2, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-recovered", Provider: providerName},
			{ID: "tr-other", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp := parsePickResponse(t, raw2); !resp.Handled || resp.AuthID != "tr-recovered" {
		t.Fatalf("cleared account must be routable again, got %+v", resp)
	}
}

// TestTestFailedSetBasic covers the mirror helpers the routing filter reads.
func TestTestFailedSetBasic(t *testing.T) {
	resetTestFailed(t)
	if isTestFailed("tr-1") {
		t.Fatal("unmarked account must not be test-failed")
	}
	if isTestFailed("") {
		t.Fatal("empty authID must never be test-failed")
	}
	testFailedSetPut("tr-1")
	if !isTestFailed("tr-1") {
		t.Fatal("testFailedSetPut should mark the account")
	}
	testFailedSetPut("tr-1") // idempotent
	if len(testFailedSnapshot()) != 1 {
		t.Fatalf("snapshot size = %d, want 1", len(testFailedSnapshot()))
	}
	testFailedSetClear("tr-1")
	if isTestFailed("tr-1") {
		t.Fatal("testFailedSetClear should unmark the account")
	}
	testFailedSetClear("tr-missing") // no-op; must not panic
}

// TestEnsureDefaultActiveAuth_SkipsTestFailed keeps the panel selection on the
// same availability contract as routing.
func TestEnsureDefaultActiveAuth_SkipsTestFailed(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	storeCredits(t, "a1", 10)
	storeCredits(t, "a2", 500)
	storeCredits(t, "a3", 300)
	testFailedSetPut("a1")
	testFailedSetPut("a2")
	setActiveAuthID("a1")
	id := ensureDefaultActiveAuth([]traeAccountView{
		{AuthIndex: "a1", AuthID: "a1", Remain: 10},
		{AuthIndex: "a2", AuthID: "a2", Remain: 500},
		{AuthIndex: "a3", AuthID: "a3", Remain: 300},
	})
	if id != "a3" {
		t.Fatalf("want only available a3, got %q", id)
	}
}
