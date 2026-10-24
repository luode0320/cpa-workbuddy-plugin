package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

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
			{ID: "wbai-a", Provider: providerName},
			{ID: "wbai-b", Provider: providerName},
			{ID: "wbai-c", Provider: providerName},
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
		{ID: "wbai-a"}, {ID: "wbai-b"}, {ID: "wbai-c"},
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
		{ID: "wbai-a"}, {ID: "wbai-b"}, {ID: "wbai-c"},
	}
	seen := make(map[string]string)
	for i := 0; i < 3; i++ {
		key := "conv-" + string(rune('a'+i))
		seen[key] = pickSessionAuth(key, cands)
	}
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
		{ID: "wbai-a"}, {ID: "wbai-b"},
	}
	pickSessionAuth("c1", cands)
	pickSessionAuth("c2", cands)
	if got := pickSessionAuth("c3", cands); got == "" {
		t.Fatal("third conversation got no account")
	}
}

func TestPickSessionAuth_TTLExpiryReassigns(t *testing.T) {
	resetSessionRouting(t)
	sessionStickinessTTL = time.Millisecond
	cands := []activeAuthCandidate{
		{ID: "wbai-a"}, {ID: "wbai-b"},
	}
	first := pickSessionAuth("conv-1", cands)
	other := pickSessionAuth("conv-2", cands)
	if first == "" || other == "" {
		t.Fatalf("both conversations should get accounts, got %q / %q", first, other)
	}
	if first == other {
		t.Fatalf("two conversations should spread, both got %q", first)
	}

	time.Sleep(2 * time.Millisecond)
	pruneSessionBindings()
	if got := pickSessionAuth("conv-3", cands); got != first {
		t.Fatalf("after conv-1 expiry, conv-3 should reuse released account %q, got %q", first, got)
	}

	if got := pickSessionAuth("conv-1", cands); got == "" {
		t.Fatal("expired conversation should still be assigned an account")
	}
}

// TestPickSessionAuth_TestFailedAccountReassigns 断言会话绑定只在账号被打上
// 「测试」标签时重新分配；额度归零不触发换号（零积分账号可正常推理）。
func TestPickSessionAuth_TestFailedAccountReassigns(t *testing.T) {
	resetSessionRouting(t)
	resetTestFailed(t)
	cands := []activeAuthCandidate{
		{ID: "wbai-a"}, {ID: "wbai-b"},
	}
	first := pickSessionAuth("conv-1", cands)
	// 零积分不影响粘性：同一会话应继续留在原账号上。
	accountCache.Store(first, &accountCacheEntry{credits: &creditsSummary{TotalRemain: 0, TotalUsed: 100, TotalSize: 100}})
	defer accountCache.Delete(first)
	if got := pickSessionAuth("conv-1", cands); got != first {
		t.Fatalf("zero-credit binding must stay sticky at %q, got %q", first, got)
	}
	// 打上「测试」标签后才重新分配。
	testFailedSetPut(first)
	if got := pickSessionAuth("conv-1", cands); got == first {
		t.Fatalf("test-failed pinned account should be re-assigned, stuck at %q", got)
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
		{ID: "wbai-a"}, {ID: "wbai-b"},
	}
	setActiveAuthID("wbai-b")
	if got := pickSessionAuth("", cands); got != "wbai-b" {
		t.Fatalf("no-session pick should fall back to panel selection wbai-b, got %q", got)
	}
}

// TestPickSessionAuth_AllTestFailedDefers 断言全池打满「测试」标签时 defer；
// 零积分不构成 defer 理由。
func TestPickSessionAuth_AllTestFailedDefers(t *testing.T) {
	resetSessionRouting(t)
	resetTestFailed(t)
	allFailed := []activeAuthCandidate{
		{ID: "wbai-a"},
		{ID: "wbai-b"},
	}
	testFailedSetPut("wbai-a")
	testFailedSetPut("wbai-b")
	if got := pickSessionAuth("conv-1", allFailed); got != "" {
		t.Fatalf("all-test-failed should defer (empty pick), got %q", got)
	}
	testFailedSetClear("wbai-a")
	if got := pickSessionAuth("conv-1", allFailed); got == "" {
		t.Fatal("cleared pool should assign an account again")
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

func TestSchedulerPick_DefaultModeIsSession(t *testing.T) {
	resetSessionRouting(t)
	restoreMode := setSchedulerMode(schedulerModeSession)
	t.Cleanup(func() {
		setActiveAuthID("")
		restoreMode()
	})

	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Options: pluginapi.SchedulerOptions{
			Metadata: map[string]any{derivedSessionIDMetadataKey: "ctx:v1:conv-root"},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-a", Provider: providerName},
			{ID: "wbai-b", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID == "" {
		t.Fatalf("default mode should handle session routing, got %+v", resp)
	}
}

func TestSchedulerPick_SessionMode_RoutesByConversation(t *testing.T) {
	resetSessionRouting(t)
	restoreMode := setSchedulerMode(schedulerModeSession)
	t.Cleanup(func() {
		setActiveAuthID("")
		restoreMode()
	})

	req := pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Options: pluginapi.SchedulerOptions{
			Metadata: map[string]any{derivedSessionIDMetadataKey: "ctx:v1:conv-root"},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wbai-a", Provider: providerName},
			{ID: "wbai-b", Provider: providerName},
		},
	}
	raw, err := handleSchedulerPick(mustMarshalBytes(t, req))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID == "" {
		t.Fatalf("session mode should handle pick, got %+v", resp)
	}
	raw2, err2 := handleSchedulerPick(mustMarshalBytes(t, req))
	if err2 != nil {
		t.Fatalf("err: %v", err2)
	}
	resp2 := parsePickResponse(t, raw2)
	if resp2.AuthID != resp.AuthID {
		t.Fatalf("same conversation should stick, got %q then %q", resp.AuthID, resp2.AuthID)
	}
	req2 := req
	req2.Options.Metadata = map[string]any{derivedSessionIDMetadataKey: "ctx:v1:other-root"}
	raw3, err3 := handleSchedulerPick(mustMarshalBytes(t, req2))
	if err3 != nil {
		t.Fatalf("err: %v", err3)
	}
	resp3 := parsePickResponse(t, raw3)
	if resp3.AuthID == resp.AuthID {
		t.Fatalf("different conversations should spread, both got %q", resp.AuthID)
	}
}

func TestSchedulerPick_SessionMode_NoSessionFallsBackToPanel(t *testing.T) {
	resetSessionRouting(t)
	setActiveAuthID("")
	restoreMode := setSchedulerMode(schedulerModeSession)
	t.Cleanup(func() {
		setActiveAuthID("")
		restoreMode()
	})
	setActiveAuthID("wbai-b")

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
	if !resp.Handled || resp.AuthID != "wbai-b" {
		t.Fatalf("no-session request should fall back to panel account wbai-b, got %+v", resp)
	}
}

func TestPickSessionAuth_CoolingDownAccountReassigns(t *testing.T) {
	resetSessionRouting(t)
	resetFailover(t)
	cands := []activeAuthCandidate{
		{ID: "wbai-a"}, {ID: "wbai-b"},
	}
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
		{ID: "wbai-a"}, {ID: "wbai-b"},
	}
	pickSessionAuth("c1", cands)
	pickSessionAuth("c2", cands)
	pickSessionAuth("c3", cands)
	sessionAuthMu.RLock()
	pinned := 0
	for _, b := range sessionAuthBindings {
		if b.AuthID == "wbai-a" {
			pinned++
		}
	}
	sessionAuthMu.RUnlock()
	if pinned == 0 {
		t.Fatal("expected at least one binding pinned to wbai-a")
	}
	evicted := evictSessionBindingsForAuth("wbai-a")
	if evicted != pinned {
		t.Fatalf("evicted %d bindings, want %d", evicted, pinned)
	}
	sessionAuthMu.RLock()
	for key, b := range sessionAuthBindings {
		if b.AuthID == "wbai-a" {
			t.Fatalf("binding %q still pinned to wbai-a after eviction", key)
		}
	}
	sessionAuthMu.RUnlock()
}
