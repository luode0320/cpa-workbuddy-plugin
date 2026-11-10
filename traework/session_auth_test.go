package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Test helpers (inlined here; workbuddy keeps them in scheduler_test.go).
func mustMarshal(t *testing.T, v any) []byte {
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

// resetSessionRouting clears session bindings and restores the default TTL so
// each test starts from a clean slate.
func resetSessionRouting(t *testing.T) {
	t.Helper()
	clearSessionBindings()
	oldTTL := sessionStickinessTTL
	t.Cleanup(func() {
		clearSessionBindings()
		sessionStickinessTTL = oldTTL
	})
}

func schedulerRequestWithMeta(meta map[string]any, headers map[string][]string) pluginapi.SchedulerPickRequest {
	return pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Options: pluginapi.SchedulerOptions{
			Headers:  headers,
			Metadata: meta,
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-a", Provider: providerName},
			{ID: "tr-b", Provider: providerName},
			{ID: "tr-c", Provider: providerName},
		},
	}
}

func TestExtractSessionKey_PriorityAndFallbacks(t *testing.T) {
	cases := []struct {
		name   string
		req    pluginapi.SchedulerPickRequest
		expect string
	}{
		{
			name:   "execution session metadata wins",
			req:    schedulerRequestWithMeta(map[string]any{executionSessionIDMetadataKey: "call-1", derivedSessionIDMetadataKey: "ctx:v1:root"}, map[string][]string{"X-Session-ID": {"hdr-1"}}),
			expect: "execution:call-1",
		},
		{
			name:   "client session header before derived identity",
			req:    schedulerRequestWithMeta(map[string]any{derivedSessionIDMetadataKey: "ctx:v1:root"}, map[string][]string{"Session-Id": {"codex-1"}}),
			expect: "codex:codex-1",
		},
		{
			name:   "claude code session header",
			req:    schedulerRequestWithMeta(nil, map[string][]string{"X-Claude-Code-Session-Id": {"claude-s"}}),
			expect: "claude:claude-s",
		},
		{
			name:   "x session id header",
			req:    schedulerRequestWithMeta(nil, map[string][]string{"x-session-id": {"Hdr-2"}}),
			expect: "header:Hdr-2",
		},
		{
			name:   "session affinity header",
			req:    schedulerRequestWithMeta(nil, map[string][]string{"X-Session-Affinity": {"aff-1"}}),
			expect: "affinity:aff-1",
		},
		{
			name:   "client request id header",
			req:    schedulerRequestWithMeta(nil, map[string][]string{"X-Client-Request-Id": {"req-1"}}),
			expect: "clientreq:req-1",
		},
		{
			name:   "derived identity last resort",
			req:    schedulerRequestWithMeta(map[string]any{derivedSessionIDMetadataKey: "ctx:v1:root"}, nil),
			expect: "derived:ctx:v1:root",
		},
		{
			name:   "no session signal",
			req:    schedulerRequestWithMeta(nil, nil),
			expect: "",
		},
		{
			name:   "empty metadata values ignored",
			req:    schedulerRequestWithMeta(map[string]any{executionSessionIDMetadataKey: "  ", derivedSessionIDMetadataKey: ""}, map[string][]string{"X-Session-ID": {"  "}}),
			expect: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractSessionKey(tc.req); got != tc.expect {
				t.Fatalf("extractSessionKey = %q, want %q", got, tc.expect)
			}
		})
	}
}

func TestPickSessionAuth_SameSessionSticky(t *testing.T) {
	resetSessionRouting(t)
	cands := []activeAuthCandidate{
		{ID: "tr-a"}, {ID: "tr-b"}, {ID: "tr-c"},
	}
	first := pickSessionAuth("conv-1", cands)
	if first == "" {
		t.Fatal("first pick returned empty")
	}
	for i := 0; i < 5; i++ {
		if got := pickSessionAuth("conv-1", cands); got != first {
			t.Fatalf("session conv-1 should stick to %q, got %q (iteration %d)", first, got, i)
		}
	}
}

func TestPickSessionAuth_DifferentSessionsSpreadUnboundFirst(t *testing.T) {
	resetSessionRouting(t)
	cands := []activeAuthCandidate{
		{ID: "tr-a"}, {ID: "tr-b"}, {ID: "tr-c"},
	}
	seen := make(map[string]string)
	for i := 0; i < 3; i++ {
		key := "conv-" + string(rune('a'+i))
		seen[key] = pickSessionAuth(key, cands)
	}
	// Three fresh conversations should be spread across three accounts.
	byAuth := make(map[string]string)
	for key, auth := range seen {
		if other, dup := byAuth[auth]; dup {
			t.Fatalf("conversations %q and %q both got %q, want unique spread", other, key, auth)
		}
		byAuth[auth] = key
	}
}

func TestPickSessionAuth_AllBoundRoundRobin(t *testing.T) {
	resetSessionRouting(t)
	cands := []activeAuthCandidate{
		{ID: "tr-a"}, {ID: "tr-b"},
	}
	// Bind two conversations to consume both accounts.
	pickSessionAuth("c1", cands)
	pickSessionAuth("c2", cands)
	// Third conversation must still get an account (round-robin).
	if got := pickSessionAuth("c3", cands); got == "" {
		t.Fatal("third conversation got no account")
	}
}

func TestPickSessionAuth_TTLExpiryReassigns(t *testing.T) {
	resetSessionRouting(t)
	sessionStickinessTTL = time.Millisecond
	cands := []activeAuthCandidate{
		{ID: "tr-a"}, {ID: "tr-b"},
	}
	// Two conversations consume both accounts.
	first := pickSessionAuth("conv-1", cands)
	other := pickSessionAuth("conv-2", cands)
	if first == "" || other == "" {
		t.Fatalf("both conversations should get accounts, got %q / %q", first, other)
	}
	if first == other {
		t.Fatalf("two conversations should spread, both got %q", first)
	}

	// Once conv-1's binding expires and is pruned, a new conversation must be
	// able to reuse the released account (this is the whole point of the TTL).
	time.Sleep(2 * time.Millisecond)
	pruneSessionBindings()
	if got := pickSessionAuth("conv-3", cands); got != first {
		t.Fatalf("after conv-1 expiry, conv-3 should reuse released account %q, got %q", first, got)
	}

	// The expired conversation itself still gets an account on its next pick.
	if got := pickSessionAuth("conv-1", cands); got == "" {
		t.Fatal("expired conversation should still be assigned an account")
	}
}

func TestPickSessionAuth_ExhaustedAccountReassigns(t *testing.T) {
	resetSessionRouting(t)
	cands := []activeAuthCandidate{
		{ID: "tr-a"}, {ID: "tr-b"},
	}
	first := pickSessionAuth("conv-1", cands)
	// Mark the pinned account exhausted; the next pick must switch.
	exhausted := make([]activeAuthCandidate, len(cands))
	for i, c := range cands {
		exhausted[i] = c
		if c.ID == first {
			exhausted[i].Exhausted = true
		}
	}
	if got := pickSessionAuth("conv-1", exhausted); got == first {
		t.Fatalf("exhausted pinned account should be re-assigned, stuck at %q", got)
	}
}

func TestPickSessionAuth_NoSessionFallsBackToPanel(t *testing.T) {
	resetSessionRouting(t)
	setActiveAuthID("")
	restoreMode := setSchedulerMode(schedulerModeCredits)
	t.Cleanup(func() {
		setActiveAuthID("")
		restoreMode()
	})
	cands := []activeAuthCandidate{
		{ID: "tr-a"}, {ID: "tr-b"},
	}
	setActiveAuthID("tr-b")
	// Empty session key must behave exactly like credits mode: panel selection.
	if got := pickSessionAuth("", cands); got != "tr-b" {
		t.Fatalf("no-session pick should fall back to panel selection tr-b, got %q", got)
	}
}

func TestPickSessionAuth_FreshAssignmentPrefersActiveID(t *testing.T) {
	resetSessionRouting(t)
	setActiveAuthID("")
	t.Cleanup(func() { setActiveAuthID("") })
	cands := []activeAuthCandidate{
		{ID: "tr-a"}, {ID: "tr-b"},
	}
	// Panel-selected account is usable: a fresh conversation must land on it,
	// even though tr-a sorts first and would otherwise win round-robin.
	setActiveAuthID("tr-b")
	if got := pickSessionAuth("conv-1", cands); got != "tr-b" {
		t.Fatalf("fresh assignment should prefer panel active account tr-b, got %q", got)
	}
	// Panel-selected account is NOT in candidates (e.g. disabled upstream): fall
	// back to the unbound-first spread and never pick a missing account.
	setActiveAuthID("ghost")
	if got := pickSessionAuth("conv-2", cands); got == "" || got == "ghost" {
		t.Fatalf("unusable panel selection should fall back to a real candidate, got %q", got)
	}
	// Explicit panel selection is cleared: unbound-first spread wins again.
	setActiveAuthID("")
	first := pickSessionAuth("spread-a", cands)
	second := pickSessionAuth("spread-b", cands)
	if first == "" || second == "" {
		t.Fatalf("fresh assignments with no panel selection should return accounts, got %q / %q", first, second)
	}
	if first == second {
		t.Fatalf("two fresh conversations with no panel selection should spread, both got %q", first)
	}
}

func TestPickSessionAuth_AllExhaustedDefers(t *testing.T) {
	resetSessionRouting(t)
	allExhausted := []activeAuthCandidate{
		{ID: "tr-a", Exhausted: true},
		{ID: "tr-b", Exhausted: true},
	}
	// All exhausted: no healthy account exists, so the picker must return ""
	// (defer) instead of pinning the conversation to a dead account. The
	// host's built-in scheduler then fails over to other providers.
	if got := pickSessionAuth("conv-1", allExhausted); got != "" {
		t.Fatalf("all-exhausted should defer (empty pick), got %q", got)
	}
	// The binding is kept for sticky recovery: once an account recovers, the
	// session re-binds normally.
	recovered := []activeAuthCandidate{
		{ID: "tr-a", Exhausted: false},
		{ID: "tr-b", Exhausted: true},
	}
	if got := pickSessionAuth("conv-1", recovered); got == "" {
		t.Fatal("recovered pool should assign an account again")
	}
}

func TestPickSessionAuth_EmptyCandidates(t *testing.T) {
	resetSessionRouting(t)
	if got := pickSessionAuth("conv-1", nil); got != "" {
		t.Fatalf("empty candidates should return empty, got %q", got)
	}
	if got := pickSessionAuth("", nil); got != "" {
		t.Fatalf("empty candidates (no session) should return empty, got %q", got)
	}
}

func TestPickSessionAuth_CoolingDownAccountReassigns(t *testing.T) {
	resetSessionRouting(t)
	resetFailover(t)
	cands := []activeAuthCandidate{
		{ID: "tr-a"}, {ID: "tr-b"},
	}
	// conv-1 pins tr-a, then tr-a enters failover cooldown.
	first := pickSessionAuth("conv-1", cands)
	if first == "" {
		t.Fatal("first pick returned empty")
	}
	recordAccountFailure(first, 429, "rate limit exceeded")
	if got := pickSessionAuth("conv-1", cands); got == first {
		t.Fatalf("cooling-down pinned account should be re-assigned, stuck at %q", got)
	}
}

func TestEvictSessionBindingsForAuth_RemovesAllBindings(t *testing.T) {
	resetSessionRouting(t)
	cands := []activeAuthCandidate{
		{ID: "tr-a"}, {ID: "tr-b"},
	}
	pickSessionAuth("c1", cands)
	pickSessionAuth("c2", cands)
	pickSessionAuth("c3", cands)
	// Count bindings pinned to tr-a before eviction.
	sessionAuthMu.RLock()
	pinned := 0
	for _, b := range sessionAuthBindings {
		if b.AuthID == "tr-a" {
			pinned++
		}
	}
	sessionAuthMu.RUnlock()
	if pinned == 0 {
		t.Fatal("expected at least one binding pinned to tr-a")
	}
	evicted := evictSessionBindingsForAuth("tr-a")
	if evicted != pinned {
		t.Fatalf("evicted %d bindings, want %d", evicted, pinned)
	}
	sessionAuthMu.RLock()
	for key, b := range sessionAuthBindings {
		if b.AuthID == "tr-a" {
			t.Fatalf("binding %q still pinned to tr-a after eviction", key)
		}
	}
	sessionAuthMu.RUnlock()
}

// TestSchedulerPick_AllCoolingDown_DefersForCrossProviderFailover is the
// regression test for the cross-provider failover bug: a session first gets a
// traework account, every traework account then fails (failover cooldown), and
// the scheduler must defer (Handled: false) so the host's built-in scheduler
// can retry on the other provider's healthy accounts (e.g. workbuddy) instead
// of answering with a doomed traework candidate.
func TestSchedulerPick_AllCoolingDown_DefersForCrossProviderFailover(t *testing.T) {
	resetSessionRouting(t)
	resetFailover(t)
	restoreMode := setSchedulerMode(schedulerModeSession)
	t.Cleanup(restoreMode)

	req := schedulerRequestWithMeta(map[string]any{derivedSessionIDMetadataKey: "ctx:v1:root"}, nil)
	req.Candidates = append(req.Candidates,
		pluginapi.SchedulerAuthCandidate{ID: "wb-a", Provider: "workbuddy-provider"},
	)

	first := parsePickResponse(t, mustSchedulerPick(t, req))
	if !first.Handled || first.AuthID == "" {
		t.Fatalf("healthy pool should bind a traework account, got %+v", first)
	}

	// Every traework account fails and enters failover cooldown.
	for _, id := range []string{"tr-a", "tr-b", "tr-c"} {
		recordAccountFailure(id, 429, "rate limit exceeded")
	}

	again := parsePickResponse(t, mustSchedulerPick(t, req))
	if again.Handled {
		t.Fatalf("all traework accounts cooling down must defer to host for cross-provider failover, got %+v", again)
	}
}

// TestSchedulerPick_AllExhausted_DefersForCrossProviderFailover covers the
// credits-mode variant: every traework candidate is credit-exhausted, so the
// pick must defer instead of returning a dead account.
func TestSchedulerPick_AllExhausted_DefersForCrossProviderFailover(t *testing.T) {
	resetSessionRouting(t)
	resetFailover(t)
	restoreMode := setSchedulerMode(schedulerModeCredits)
	t.Cleanup(restoreMode)
	setActiveAuthID("")
	t.Cleanup(func() { setActiveAuthID("") })
	for _, id := range []string{"tr-a", "tr-b", "tr-c"} {
		accountCache.Store(id, &accountCacheEntry{credits: &traeCredits{TotalRemain: 0}, updated: time.Now()})
	}
	t.Cleanup(func() {
		for _, id := range []string{"tr-a", "tr-b", "tr-c"} {
			accountCache.Delete(id)
		}
	})

	req := pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-a", Provider: providerName},
			{ID: "tr-b", Provider: providerName},
			{ID: "tr-c", Provider: providerName},
			{ID: "wb-a", Provider: "workbuddy-provider"},
		},
	}
	resp := parsePickResponse(t, mustSchedulerPick(t, req))
	if resp.Handled {
		t.Fatalf("all traework accounts exhausted must defer to host for cross-provider failover, got %+v", resp)
	}
}

// mustSchedulerPick runs handleSchedulerPick and fails the test on transport
// errors (envelope errors are asserted by the callers via Handled).
func mustSchedulerPick(t *testing.T, req pluginapi.SchedulerPickRequest) []byte {
	t.Helper()
	raw, err := handleSchedulerPick(mustMarshal(t, req))
	if err != nil {
		t.Fatalf("handleSchedulerPick: %v", err)
	}
	return raw
}

// TestSchedulerMode_DefaultIsSession 验证 traework 默认调度模式为 session，与 workbuddy 基线对齐。
// 最近修改时间：2026-10-11 批次 A 默认会话路由对齐
func TestSchedulerMode_DefaultIsSession(t *testing.T) {
	if mode := loadedSchedulerMode(); mode != schedulerModeSession {
		t.Fatalf("loadedSchedulerMode() = %q, want %q", mode, schedulerModeSession)
	}
	cfg := defaultTraeConfig()
	if cfg.SchedulerMode != schedulerModeSession {
		t.Fatalf("defaultTraeConfig().SchedulerMode = %q, want %q", cfg.SchedulerMode, schedulerModeSession)
	}
}

// TestPickSessionAuth_StickyReassignsWhenBoundAccountSaturated 验证粘性命中后若绑定
// 账号已在跑数达到软上限、且池中有更闲的可用账号，会把会话改绑到更闲的账号。
//
// 这是 2026-11-10 生产问题的直接回归：旧逻辑无条件信任绑定，导致一个会话的并发
// 子请求全压在一个账号上，账号并发中位数 16、峰值 25，远超上游默认 2 个并行槽位，
// 首字延迟从 4596ms 涨到 7795ms。
func TestPickSessionAuth_StickyReassignsWhenBoundAccountSaturated(t *testing.T) {
	resetSessionRouting(t)
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	cands := []activeAuthCandidate{{ID: "tr-a"}, {ID: "tr-b"}}
	pinned := pickSessionAuth("conv-saturated", cands)
	if pinned == "" {
		t.Fatal("首次选号不应为空")
	}
	// 让绑定账号堆到软上限，模拟同一会话的并发子请求占满上游槽位。
	releases := make([]func(), 0, sessionAccountInflightSoftCap)
	for i := 0; i < sessionAccountInflightSoftCap; i++ {
		releases = append(releases, accountLoadAcquire(pinned))
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()

	got := pickSessionAuth("conv-saturated", cands)
	if got == pinned {
		t.Fatalf("绑定账号已在跑 %d 个（软上限 %d），应改绑到更闲账号，实际仍为 %q",
			sessionAccountInflightSoftCap, sessionAccountInflightSoftCap, got)
	}
	// 改绑必须写回绑定表，否则下一次请求还会命中旧账号。
	sessionAuthMu.RLock()
	b := sessionAuthBindings["conv-saturated"]
	sessionAuthMu.RUnlock()
	if b.AuthID != got {
		t.Fatalf("改绑后绑定表应写入 %q，实际 %q", got, b.AuthID)
	}
}

// TestPickSessionAuth_StickyKeepsBindingWhenAllSaturated 验证全池都超过软上限时
// 保持原绑定：所有号一样忙时改绑没有收益，反而会让会话在两个号之间来回抖。
func TestPickSessionAuth_StickyKeepsBindingWhenAllSaturated(t *testing.T) {
	resetSessionRouting(t)
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	cands := []activeAuthCandidate{{ID: "tr-a"}, {ID: "tr-b"}}
	pinned := pickSessionAuth("conv-all-busy", cands)
	if pinned == "" {
		t.Fatal("首次选号不应为空")
	}
	var releases []func()
	defer func() {
		for _, r := range releases {
			r()
		}
	}()
	// 两个账号都堆到同一在跑数：没有「更闲」的候选。
	for _, id := range []string{"tr-a", "tr-b"} {
		for i := 0; i < sessionAccountInflightSoftCap+1; i++ {
			releases = append(releases, accountLoadAcquire(id))
		}
	}

	if got := pickSessionAuth("conv-all-busy", cands); got != pinned {
		t.Fatalf("全池同样繁忙时应保持绑定 %q，实际 %q", pinned, got)
	}
}
