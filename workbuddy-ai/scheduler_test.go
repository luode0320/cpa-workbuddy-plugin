package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func mustMarshalBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func parsePickResponse(t *testing.T, raw []byte) pluginapi.SchedulerPickResponse {
	t.Helper()
	var env struct {
		OK     bool                            `json:"ok"`
		Result pluginapi.SchedulerPickResponse `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatal("envelope not ok")
	}
	return env.Result
}

func resetActiveAuth(t *testing.T) {
	t.Helper()
	setActiveAuthID("")
	restoreMode := setSchedulerMode(schedulerModeCredits)
	t.Cleanup(func() {
		setActiveAuthID("")
		restoreMode()
	})
}

func TestSchedulerPick_NonWorkbuddy_Defers(t *testing.T) {
	resetActiveAuth(t)
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: "other",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "x", Provider: "other"},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if resp.Handled {
		t.Fatal("non-workbuddy candidates should defer")
	}
}

func TestSchedulerPick_OffMode_Defers(t *testing.T) {
	setActiveAuthID("")
	restoreMode := setSchedulerMode(schedulerModeOff)
	t.Cleanup(func() {
		setActiveAuthID("")
		restoreMode()
	})
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-only", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if resp.Handled {
		t.Fatal("scheduler_mode=off should defer to built-in scheduler")
	}
}

func TestSchedulerPick_SingleCandidate_PicksIt(t *testing.T) {
	resetActiveAuth(t)
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-only", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wbai-only" {
		t.Fatalf("want wbai-only handled, got %+v", resp)
	}
	if getActiveAuthID() != "wbai-only" {
		t.Fatalf("active auth should stick to wbai-only, got %q", getActiveAuthID())
	}
}

// TestSchedulerPick_RoutesZeroCreditAccount 断言零积分账号仍可承载流量。
//
// 产品口径（2026-10-24 用户裁定）：额度耗尽不代表账号不可用——无积分也能
// 正常推理，因此调度不得按积分排除候选。本用例锁死该契约：面板选中的零积分
// 账号必须被保留（而不是被切换到高积分账号）。
func TestSchedulerPick_RoutesZeroCreditAccount(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	accountCache.Store("wbai-zero", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 0, TotalUsed: 100, TotalSize: 100}})
	accountCache.Store("wbai-rich", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 500, TotalSize: 500}})
	defer func() {
		accountCache.Delete("wbai-zero")
		accountCache.Delete("wbai-rich")
	}()
	setActiveAuthID("wbai-zero")
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-zero", Provider: providerName},
			{ID: "wbai-rich", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wbai-zero" {
		t.Fatalf("want zero-credit panel selection kept, got %+v", resp)
	}
}

// TestSchedulerPick_AllZeroCreditStillHandled 断言全池零积分时仍由插件处理，
// 而不是 defer 给宿主（defer 会让请求落到别的 provider，掩盖额度与可用性无关的事实）。
func TestSchedulerPick_AllZeroCreditStillHandled(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	accountCache.Store("wbai-zero-a", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 0, TotalUsed: 100, TotalSize: 100}})
	accountCache.Store("wbai-zero-b", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 0, TotalUsed: 200, TotalSize: 200}})
	defer func() {
		accountCache.Delete("wbai-zero-a")
		accountCache.Delete("wbai-zero-b")
	}()
	setActiveAuthID("wbai-zero-a")
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-zero-a", Provider: providerName},
			{ID: "wbai-zero-b", Provider: providerName},
			{ID: "tr-a", Provider: "traework-provider"},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled {
		t.Fatalf("all-zero-credit fleet must still be handled by the plugin, got %+v", resp)
	}
	if resp.AuthID != "wbai-zero-a" && resp.AuthID != "wbai-zero-b" {
		t.Fatalf("pick must stay inside the workbuddy pool, got %+v", resp)
	}
}

func TestSchedulerPick_KeepsHealthyPanelSelection(t *testing.T) {
	resetActiveAuth(t)
	accountCache.Store("wbai-a", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 300, TotalSize: 300}})
	accountCache.Store("wbai-b", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 500, TotalSize: 500}})
	defer func() {
		accountCache.Delete("wbai-a")
		accountCache.Delete("wbai-b")
	}()
	setActiveAuthID("wbai-a")
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-a", Provider: providerName},
			{ID: "wbai-b", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wbai-a" {
		t.Fatalf("want healthy panel selection wbai-a kept, got %+v", resp)
	}
}

// TestSchedulerPick_ZeroCreditSelectionSwitchesToTestFailed 断言切换账号的唯一
// 理由是「测试」标签，而不是积分归零：零积分的选中项被保留时不会切走。
func TestSchedulerPick_ZeroCreditSelectionSwitchesToTestFailed(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	accountCache.Store("wbai-zero", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 0, TotalUsed: 500, TotalSize: 500},
	})
	accountCache.Store("wbai-ok", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 300, TotalUsed: 0, TotalSize: 300},
	})
	defer func() {
		accountCache.Delete("wbai-zero")
		accountCache.Delete("wbai-ok")
	}()
	setActiveAuthID("wbai-zero")
	// 1. 零积分但无「测试」标签 → 保留选中项
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-zero", Provider: providerName},
			{ID: "wbai-ok", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp := parsePickResponse(t, raw); !resp.Handled || resp.AuthID != "wbai-zero" {
		t.Fatalf("zero-credit selection must be kept, got %+v", resp)
	}

	// 2. 打上「测试」标签 → 切换到另一个账号
	testFailedSetPut("wbai-zero")
	raw2, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-zero", Provider: providerName},
			{ID: "wbai-ok", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp2 := parsePickResponse(t, raw2)
	if !resp2.Handled || resp2.AuthID != "wbai-ok" {
		t.Fatalf("test-failed account must be skipped, got %+v", resp2)
	}
	if getActiveAuthID() != "wbai-ok" {
		t.Fatalf("active should update to wbai-ok, got %q", getActiveAuthID())
	}
}

// TestSchedulerPick_AllTestFailed_DefersInsteadOfZeroCredit 断言全池只有「测试」
// 标签才会 defer；零积分不构成 defer 理由（见 AllZeroCreditStillHandled）。
func TestSchedulerPick_AllTestFailed_DefersInsteadOfZeroCredit(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	accountCache.Store("wbai-a", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 0, TotalUsed: 100, TotalSize: 100},
	})
	accountCache.Store("wbai-b", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 0, TotalUsed: 200, TotalSize: 200},
	})
	defer func() {
		accountCache.Delete("wbai-a")
		accountCache.Delete("wbai-b")
	}()
	testFailedSetPut("wbai-a")
	testFailedSetPut("wbai-b")
	setActiveAuthID("wbai-a")
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-a", Provider: providerName},
			{ID: "wbai-b", Provider: providerName},
			{ID: "tr-a", Provider: "traework-provider"},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if resp.Handled {
		t.Fatalf("all test-failed should defer to host (cross-provider failover), got %+v", resp)
	}
}

func TestSchedulerPick_SwitchesOnlyWhenSelectionGone(t *testing.T) {
	resetActiveAuth(t)
	accountCache.Store("wbai-ok", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 300, TotalUsed: 0, TotalSize: 300},
	})
	defer accountCache.Delete("wbai-ok")
	setActiveAuthID("wbai-gone")
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-ok", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wbai-ok" {
		t.Fatalf("want switch to wbai-ok, got %+v", resp)
	}
	if getActiveAuthID() != "wbai-ok" {
		t.Fatalf("active should update to wbai-ok, got %q", getActiveAuthID())
	}
}

func TestSchedulerPick_SkipsDisabledCandidates(t *testing.T) {
	resetActiveAuth(t)
	accountCache.Store("wbai-live", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 50, TotalSize: 50},
	})
	defer accountCache.Delete("wbai-live")
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-off", Provider: providerName, Status: "disabled"},
			{ID: "wbai-live", Provider: providerName, Status: "active"},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wbai-live" {
		t.Fatalf("want wbai-live, got %+v", resp)
	}
	raw2, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-off", Provider: providerName, Status: "disabled", Metadata: map[string]any{"disabled": true}},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp2 := parsePickResponse(t, raw2)
	if resp2.Handled {
		t.Fatalf("all disabled should defer, got %+v", resp2)
	}
}

func TestCandidateDisabled(t *testing.T) {
	if !candidateDisabled(pluginapi.SchedulerAuthCandidate{Status: "disabled"}) {
		t.Fatal("status disabled")
	}
	if !candidateDisabled(pluginapi.SchedulerAuthCandidate{Metadata: map[string]any{"disabled": true}}) {
		t.Fatal("meta disabled")
	}
	if candidateDisabled(pluginapi.SchedulerAuthCandidate{Status: "active"}) {
		t.Fatal("active should not be disabled")
	}
}

func TestEnsureDefaultActiveAuth(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	id := ensureDefaultActiveAuth([]wbAccount{
		{AuthIndex: "a1", AuthID: "a1", Disabled: true},
		{AuthIndex: "a2", AuthID: "a2"},
		{AuthIndex: "a3", AuthID: "a3"},
	})
	if id != "a2" {
		t.Fatalf("want first enabled a2, got %q", id)
	}
}
