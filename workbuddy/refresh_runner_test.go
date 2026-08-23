// refresh_runner_test.go exercises the throttled refresh queue's state
// machine without touching the host API. Every test uses its own runner with
// a fake fetchFn and a shortened tick interval so the suite stays fast.
package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// newTestRunner builds an isolated runner with a fake fetch and a short tick.
func newTestRunner(fetch func(authIndex, authID string) error) *refreshRunner {
	r := newRefreshRunner()
	r.fetchFn = fetch
	r.tickInterval = 2 * time.Millisecond
	return r
}

// startTestRunner launches the worker and registers a clean shutdown via
// close(stop) (broadcast, non-blocking) so no test can deadlock on a stop send.
func startTestRunner(r *refreshRunner) {
	go r.run()
	// close is idempotent-safe here: each runner is per-test and closed once.
}

// waitForIdle polls Snapshot until the runner is no longer running or the
// deadline passes. Returns the final snapshot.
func waitForIdle(r *refreshRunner, timeout time.Duration) refreshSnapshot {
	deadline := time.Now().Add(timeout)
	for {
		snap := r.Snapshot()
		if !snap.Running {
			return snap
		}
		if time.Now().After(deadline) {
			return snap
		}
		time.Sleep(1 * time.Millisecond)
	}
}

func targets(n int) []refreshTarget {
	out := make([]refreshTarget, n)
	for i := 0; i < n; i++ {
		out[i] = refreshTarget{AuthIndex: fmt.Sprintf("idx-%d", i), AuthID: fmt.Sprintf("id-%d", i)}
	}
	return out
}

// EnqueueAll must return immediately without waiting for any fetch.
func TestRefreshRunner_EnqueueAllReturnsImmediately(t *testing.T) {
	release := make(chan struct{})
	r := newTestRunner(func(authIndex, authID string) error {
		<-release // block the worker so we can prove EnqueueAll doesn't wait
		return nil
	})
	startTestRunner(r)
	defer close(r.stop)

	done := make(chan int, 1)
	go func() { done <- r.EnqueueAll(targets(3), "panel") }()

	select {
	case n := <-done:
		if n != 3 {
			t.Fatalf("expected 3 queued, got %d", n)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("EnqueueAll blocked on fetch")
	}
	close(release)
}

// The worker must fetch one account at a time with at least tickInterval
// between starts — no concurrent burst.
func TestRefreshRunner_ThrottlesOneAccountAtATime(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	r := newTestRunner(func(authIndex, authID string) error {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return nil
	})
	r.tickInterval = 30 * time.Millisecond
	startTestRunner(r)
	defer close(r.stop)

	r.EnqueueAll(targets(4), "watchdog")
	snap := waitForIdle(r, 3*time.Second)

	if snap.Total != 4 || snap.Done != 4 {
		t.Fatalf("expected 4 done, got total=%d done=%d failed=%d", snap.Total, snap.Done, snap.Failed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 4 {
		t.Fatalf("expected 4 fetches, got %d", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		gap := starts[i].Sub(starts[i-1])
		if gap < 20*time.Millisecond {
			t.Fatalf("fetch %d started only %v after %d; want >= 20ms (throttle violated)", i, gap, i-1)
		}
	}
}

// A failed fetch must mark its account failed and still let the next account
// proceed.
func TestRefreshRunner_FailureIsolation(t *testing.T) {
	r := newTestRunner(func(authIndex, authID string) error {
		if authID == "id-1" {
			return fmt.Errorf("boom")
		}
		return nil
	})
	startTestRunner(r)
	defer close(r.stop)

	r.EnqueueAll(targets(3), "panel")
	snap := waitForIdle(r, 3*time.Second)

	if snap.Total != 3 || snap.Done != 2 || snap.Failed != 1 {
		t.Fatalf("expected 2 done/1 failed, got total=%d done=%d failed=%d", snap.Total, snap.Done, snap.Failed)
	}
	var failedID string
	for _, s := range snap.PerAccount {
		if s.Status == rsFailed {
			failedID = s.AuthID
			if s.Error == "" {
				t.Fatal("failed account has empty error")
			}
		}
	}
	if failedID != "id-1" {
		t.Fatalf("expected id-1 to fail, got %q", failedID)
	}
}

// Snapshot counts must stay consistent across the state transitions.
func TestRefreshRunner_SnapshotCounts(t *testing.T) {
	r := newTestRunner(func(authIndex, authID string) error { return nil })
	startTestRunner(r)
	defer close(r.stop)

	r.EnqueueAll(targets(5), "credits")
	// Immediately after enqueue the worker may already have started the first
	// account, so only assert on invariants that hold regardless of timing:
	// total is 5 and the batch is nowhere near fully done.
	early := r.Snapshot()
	if early.Total != 5 {
		t.Fatalf("early snapshot total wrong: %+v", early)
	}
	if early.Done == 5 {
		t.Fatalf("early snapshot already fully done: %+v", early)
	}

	snap := waitForIdle(r, 3*time.Second)
	if snap.Done != 5 || snap.Failed != 0 || snap.Pending != 0 || snap.Running {
		t.Fatalf("final snapshot wrong: %+v", snap)
	}
}

// A second EnqueueAll while a round is in flight must be ignored (idempotent):
// the original round keeps running and no duplicate round is started.
func TestRefreshRunner_EnqueueAllIdempotent(t *testing.T) {
	r := newTestRunner(func(authIndex, authID string) error {
		time.Sleep(5 * time.Millisecond) // keep the first round in flight
		return nil
	})
	startTestRunner(r)
	defer close(r.stop)

	if n := r.EnqueueAll(targets(3), "panel"); n != 3 {
		t.Fatalf("expected 3 queued, got %d", n)
	}
	time.Sleep(8 * time.Millisecond) // let the first round start (and stay in flight)
	if n := r.EnqueueAll(targets(2), "watchdog"); n != 0 {
		t.Fatalf("expected duplicate EnqueueAll to be ignored (0), got %d", n)
	}

	snap := waitForIdle(r, 3*time.Second)

	if snap.Total != 3 || snap.Done != 3 {
		t.Fatalf("expected the original round of 3 done (duplicate ignored), got %+v", snap)
	}
	if len(snap.Sources) != 1 || snap.Sources[0] != "panel" {
		t.Fatalf("expected source panel, got %v", snap.Sources)
	}
}

// EnqueueOne while a round is in flight must be ignored (idempotent).
func TestRefreshRunner_EnqueueOneIdempotent(t *testing.T) {
	r := newTestRunner(func(authIndex, authID string) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	})
	startTestRunner(r)
	defer close(r.stop)

	r.EnqueueAll(targets(3), "panel")
	time.Sleep(8 * time.Millisecond) // let the round start
	if r.EnqueueOne("idx-9", "id-9", "credits") {
		t.Fatal("expected EnqueueOne during a running round to be ignored")
	}

	snap := waitForIdle(r, 3*time.Second)
	if snap.Total != 3 || snap.Done != 3 {
		t.Fatalf("expected original round of 3 done, got %+v", snap)
	}
}
