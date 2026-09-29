package main

import (
	"testing"
	"time"
)

// TestWatchdogIntervalDefault pins the fixed cadence: 保号 (preserve) 池已于
// 2026-09-29 移除，watchdog 不再接受 interval/enabled 配置，只剩「刷新积分 +
// 定时活跃测试」两项职责。
func TestWatchdogIntervalDefault(t *testing.T) {
	if watchdogIntervalDefault != 10*time.Minute {
		t.Fatalf("watchdogIntervalDefault = %v, want 10m", watchdogIntervalDefault)
	}
}

// TestWaitHostReadyForWatchdog covers the startup-readiness probe used to
// close the init-race blind window. Four cases:
//   - always-ready returns true immediately, no sleep;
//   - becomes-ready-on-second-call still returns true and ends in <maxWait;
//   - never-ready with maxWait=0 returns ready() right away (no goroutine);
//   - never-ready with a tight maxWait returns false (proves the deadline
//     is honored and we don't loop forever).
func TestWaitHostReadyForWatchdog(t *testing.T) {
	// Case 1: always-ready, short maxWait — instant true, zero sleeps.
	calls := 0
	ready := func() bool { calls++; return true }
	if !waitHostReadyForWatchdog(50*time.Millisecond, ready) {
		t.Fatal("always-ready should return true")
	}
	if calls != 1 {
		t.Fatalf("expected one ready() call, got %d", calls)
	}

	// Case 2: becomes-ready on the second call. Probes at least twice
	// and returns true in <maxWait.
	calls = 0
	ready = func() bool { calls++; return calls >= 2 }
	if !waitHostReadyForWatchdog(2*time.Second, ready) {
		t.Fatal("eventually-ready should return true")
	}
	if calls < 2 {
		t.Fatalf("expected at least 2 probes, got %d", calls)
	}

	// Case 3: never-ready, maxWait=0 — returns whatever ready() says,
	// without sleeping. One call, no goroutine.
	calls = 0
	ready = func() bool { calls++; return false }
	if waitHostReadyForWatchdog(0, ready) {
		t.Fatal("maxWait=0 + never-ready must return false")
	}
	if calls != 1 {
		t.Fatalf("maxWait=0 must probe exactly once, got %d", calls)
	}

	// Case 4: never-ready with a tight maxWait — returns false once the
	// deadline passes. Without the deadline guard this would loop forever.
	calls = 0
	ready = func() bool { calls++; return false }
	start := time.Now()
	if waitHostReadyForWatchdog(20*time.Millisecond, ready) {
		t.Fatal("never-ready under a finite maxWait must return false")
	}
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("wait overshot the deadline: %v", elapsed)
	}
	if calls < 1 {
		t.Fatalf("never-ready should be probed at least once, got %d", calls)
	}
}

// TestRequestWatchdogTickCoalesces proves the trigger channel collapses
// concurrent requesters onto a single queued tick — protects the upstream
// billing/points API from reconfigure/panel storms (v0.12.1 contract).
func TestRequestWatchdogTickCoalesces(t *testing.T) {
	// Drain anything queued by a previous test (the chan is package-global).
	for {
		select {
		case <-watchdogTickCh:
		default:
			goto drained
		}
	}
drained:
	// Three requests in rapid succession — buffered cap 1 means only one
	// value may queue; the other two drop on default.
	requestWatchdogTick()
	requestWatchdogTick()
	requestWatchdogTick()
	select {
	case <-watchdogTickCh:
		// OK — exactly one queued.
	default:
		t.Fatal("expected at least one tick queued after coalesced requests")
	}
	// Re-check: no second value should be sitting in the channel.
	select {
	case <-watchdogTickCh:
		t.Fatal("requestWatchdogTick must coalesce; got a second queued value")
	default:
	}
}
