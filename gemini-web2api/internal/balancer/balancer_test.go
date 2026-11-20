package balancer

import (
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/gemini"
)

func TestNextForPinnedPreservesSessionBinding(t *testing.T) {
	pool := NewAccountPool(time.Minute, 10*time.Minute)
	clientA := &gemini.Client{}
	clientB := &gemini.Client{}
	pool.entries = []*accountEntry{
		{AccountID: "accA", Client: clientA, State: StateReady},
		{AccountID: "accB", Client: clientB, State: StateReady},
	}

	gotClient, gotID := pool.nextForPinned("accA", "s1", "", nil)
	if gotClient != clientA || gotID != "accA" {
		t.Fatalf("first turn: got (%p, %q), want (%p, accA)", gotClient, gotID, clientA)
	}

	// CPA core round-robins to accB on the next turn; existing session s1 must stay on accA.
	gotClient, gotID = pool.nextForPinned("accB", "s1", "", nil)
	if gotClient != clientA || gotID != "accA" {
		t.Fatalf("second turn with different pinnedID: got (%p, %q), want (%p, accA)", gotClient, gotID, clientA)
	}

	// If accA is excluded, nextForPinned returns (nil, "accA") so the caller can rebuild on another account or report 503.
	gotClient, gotID = pool.nextForPinned("accB", "s1", "", map[string]struct{}{"accA": {}})
	if gotClient != nil || gotID != "accA" {
		t.Fatalf("excluded bound account: got (%p, %q), want (nil, accA)", gotClient, gotID)
	}

	// Rebuilt / new session (empty sessionKey) uses the new pinnedID accB.
	gotClient, gotID = pool.nextForPinned("accB", "", "", map[string]struct{}{"accA": {}})
	if gotClient != clientB || gotID != "accB" {
		t.Fatalf("rebuilt session: got (%p, %q), want (%p, accB)", gotClient, gotID, clientB)
	}
}

func TestLookupAccountAndLeaveQueueBoundsSafety(t *testing.T) {
	pool := NewAccountPool(time.Minute, 10*time.Minute)
	pool.Register("User@Example.com", AccountInfo{}, false)

	id, state, enabled, ok := pool.LookupAccount("user@example.com")
	if !ok || id != "User@Example.com" || state != StateInitializing || !enabled {
		t.Fatalf("unexpected LookupAccount result: id=%q state=%q enabled=%v ok=%v", id, state, enabled, ok)
	}

	// Re-registering as disabled must notify waiters and report enabled=false.
	prevChanged := pool.changed
	pool.Register("User@Example.com", AccountInfo{}, true)
	select {
	case <-prevChanged:
	default:
		t.Fatalf("expected Register state update to close changed notification channel")
	}
	_, state, enabled, ok = pool.LookupAccount("user@example.com")
	if !ok || state != StateDisabled || enabled {
		t.Fatalf("expected disabled state after re-register, got state=%q enabled=%v ok=%v", state, enabled, ok)
	}

	// Calling leaveQueue for an already-removed wake channel must not panic.
	key, wake := pool.joinQueue("", "", "gemini-4-carbon", nil)
	pool.leaveQueue(key, wake)
	pool.leaveQueue(key, wake)
}
