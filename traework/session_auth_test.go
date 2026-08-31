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

func TestPickSessionAuth_AllExhaustedKeepsPin(t *testing.T) {
	resetSessionRouting(t)
	allExhausted := []activeAuthCandidate{
		{ID: "tr-a", Exhausted: true},
		{ID: "tr-b", Exhausted: true},
	}
	first := pickSessionAuth("conv-1", allExhausted)
	if first == "" {
		t.Fatal("pick with all-exhausted candidates returned empty")
	}
	// All exhausted: the pin must be kept as long as the account still exists.
	if got := pickSessionAuth("conv-1", allExhausted); got != first {
		t.Fatalf("all-exhausted should keep pin %q, got %q", first, got)
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
