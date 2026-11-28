package main

import (
	"testing"
	"time"
)

// resetSessionState clears group session bindings and the prune throttle so cases start clean.
func resetSessionState(t *testing.T) {
	t.Helper()
	stateMu.Lock()
	groupSessions = make(map[string]*sessionBinding)
	lastSessionPrune = time.Time{}
	stateMu.Unlock()
	t.Cleanup(func() {
		stateMu.Lock()
		groupSessions = make(map[string]*sessionBinding)
		lastSessionPrune = time.Time{}
		stateMu.Unlock()
	})
}

// TestPruneSessionsThrottledDropsStaleBindings checks stale bindings are pruned and that
// pruning is throttled to one full sweep per interval.
func TestPruneSessionsThrottledDropsStaleBindings(t *testing.T) {
	resetSessionState(t)
	now := time.Now()

	stateMu.Lock()
	groupSessions["stale"] = &sessionBinding{Session: "stale", LastSeen: now.Add(-sessionRetention - time.Minute)}
	groupSessions["fresh"] = &sessionBinding{Session: "fresh", LastSeen: now}
	pruneSessionsThrottled(now)
	_, staleKept := groupSessions["stale"]
	_, freshKept := groupSessions["fresh"]
	stateMu.Unlock()

	if staleKept {
		t.Fatal("超过 sessionRetention 的绑定应被清理")
	}
	if !freshKept {
		t.Fatal("保留期内的绑定不应被清理")
	}

	// Inside the throttle window nothing is pruned: the newly stale binding stays.
	stateMu.Lock()
	groupSessions["stale2"] = &sessionBinding{Session: "stale2", LastSeen: now.Add(-sessionRetention - time.Minute)}
	pruneSessionsThrottled(now.Add(time.Minute))
	_, throttledKept := groupSessions["stale2"]
	stateMu.Unlock()
	if !throttledKept {
		t.Fatal("节流窗口内不应再次全表清理")
	}

	// Past the throttle interval pruning runs again.
	stateMu.Lock()
	pruneSessionsThrottled(now.Add(sessionPruneInterval + time.Second))
	_, stale2Kept := groupSessions["stale2"]
	stateMu.Unlock()
	if stale2Kept {
		t.Fatal("节流窗口结束后应清理过期绑定")
	}
}

// TestBindGroupSessionPrunesExpiredBindings checks that writing a binding also drops bindings
// older than the retention window, so the session map cannot grow forever.
func TestBindGroupSessionPrunesExpiredBindings(t *testing.T) {
	resetSessionState(t)
	now := time.Now()

	stateMu.Lock()
	groupSessions["session:stale\x00g"] = &sessionBinding{Session: "session:stale", LastSeen: now.Add(-sessionRetention - time.Hour)}
	stateMu.Unlock()

	bindGroupSession(defaultPluginConfig(), "session:live", "g", "m1", "", now)

	stateMu.Lock()
	_, staleKept := groupSessions["session:stale\x00g"]
	live, liveKept := groupSessions["session:live\x00g"]
	stateMu.Unlock()

	if staleKept {
		t.Fatal("绑定写入时应清掉超过保留期的旧绑定")
	}
	if !liveKept || live.AuthID != "m1" {
		t.Fatalf("新绑定未写入: kept=%v binding=%+v", liveKept, live)
	}
}

// TestBuildUIStateSkipsExpiredSessions checks the panel state only reports bindings whose
// stickiness is still valid.
func TestBuildUIStateSkipsExpiredSessions(t *testing.T) {
	resetSessionState(t)
	now := time.Now()

	stateMu.Lock()
	groupSessions["expired"] = &sessionBinding{
		Session: "expired", LastSeen: now.Add(-time.Minute), IdleExpiresAt: now.Add(-time.Minute),
	}
	groupSessions["active"] = &sessionBinding{
		Session: "active", LastSeen: now, IdleExpiresAt: now.Add(time.Minute),
	}
	stateMu.Unlock()

	state := buildUIState()
	if len(state.Sessions) != 1 {
		t.Fatalf("sessions = %d 条，期望只回 1 条未过期绑定", len(state.Sessions))
	}
	if state.Sessions[0].Session != "active" {
		t.Fatalf("sessions[0] = %q，期望 active", state.Sessions[0].Session)
	}
}
