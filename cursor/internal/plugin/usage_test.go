package plugin

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUsageStore_BoundsAbandonedRequestAuthUnderConcurrentTraffic(t *testing.T) {
	store := newUsageStore()
	const requests = 5000

	var workers sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		workers.Add(1)
		go func(offset int) {
			defer workers.Done()
			for index := offset; index < requests; index += 32 {
				store.selectRequestAuth(fmt.Sprintf("request-%d", index), "cursor-account.json")
			}
		}(worker)
	}
	workers.Wait()

	store.mu.RLock()
	pending := len(store.requestAuth)
	ordered := store.requestAuthOrder.Len()
	store.mu.RUnlock()
	require.Equal(t, requestAuthCapacity, pending)
	require.Equal(t, pending, ordered)
}

func TestUsageStore_ExpiresAbandonedRequestAuth(t *testing.T) {
	store := newUsageStore()
	now := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	store.selectRequestAuth("abandoned", "cursor-old.json")
	now = now.Add(requestAuthTTL + time.Second)
	store.selectRequestAuth("current", "cursor-current.json")
	store.completeRequest("abandoned", "", false, "succeeded")
	store.completeRequest("current", "", false, "succeeded")

	store.mu.RLock()
	pending := len(store.requestAuth)
	ordered := store.requestAuthOrder.Len()
	store.mu.RUnlock()
	require.Zero(t, pending)
	require.Zero(t, ordered)
	require.Zero(t, store.snapshot("cursor-old.json").Requests)
	require.EqualValues(t, 1, store.snapshot("cursor-current.json").Requests)
}

func TestUsageStore_DeduplicatesMetadataBearingCompletion(t *testing.T) {
	store := newUsageStore()
	store.selectRequestAuth("request", "cursor-account.json")

	store.completeRequest("request", "cursor-account.json", true, "succeeded")
	store.completeRequest("request", "cursor-account.json", true, "failed")

	usage := store.snapshot("cursor-account.json")
	require.EqualValues(t, 1, usage.Requests)
	require.EqualValues(t, 1, usage.Succeeded)
	require.Zero(t, usage.Failed)
}

func TestUsageStore_IgnoresLateSelectionAfterCompletion(t *testing.T) {
	store := newUsageStore()
	store.completeRequest("request", "cursor-account.json", true, "succeeded")

	store.selectRequestAuth("request", "cursor-account.json")
	store.completeRequest("request", "cursor-account.json", true, "failed")

	usage := store.snapshot("cursor-account.json")
	require.EqualValues(t, 1, usage.Requests)
	require.EqualValues(t, 1, usage.Succeeded)
	require.Zero(t, usage.Failed)
}

func TestUsageStore_BoundsCompletedRequestDeduplicationUnderConcurrentTraffic(t *testing.T) {
	store := newUsageStore()
	const requests = 5000

	var workers sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		workers.Add(1)
		go func(offset int) {
			defer workers.Done()
			for index := offset; index < requests; index += 32 {
				store.completeRequest(fmt.Sprintf("request-%d", index), "cursor-account.json", true, "succeeded")
			}
		}(worker)
	}
	workers.Wait()

	require.Len(t, store.requestCompletions.current, requestCompletionWords)
	require.Len(t, store.requestCompletions.previous, requestCompletionWords)
	require.EqualValues(t, requests, store.snapshot("cursor-account.json").Requests)
}

func TestUsageStore_ExpiresCompletedRequestDeduplication(t *testing.T) {
	store := newUsageStore()
	now := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	store.completeRequest("expired", "cursor-account.json", true, "succeeded")
	now = now.Add(requestCompletionWindow + time.Second)
	store.completeRequest("current", "cursor-account.json", true, "succeeded")

	require.True(t, store.requestCompletions.contains(requestAuthKey("expired"), now))
	now = now.Add(requestCompletionWindow + time.Second)
	require.False(t, store.requestCompletions.contains(requestAuthKey("expired"), now))
}

func TestUsageStore_FirstCompletionWithoutAuthRemainsTerminal(t *testing.T) {
	store := newUsageStore()
	store.completeRequest("request", "", false, "failed")

	store.completeRequest("request", "cursor-account.json", true, "succeeded")

	require.Zero(t, store.snapshot("cursor-account.json").Requests)
}

func TestUsageStore_DeduplicatesCompletionAfterCapacityPressure(t *testing.T) {
	store := newUsageStore()
	store.completeRequest("oldest", "cursor-account.json", true, "succeeded")
	for index := 0; index < 10_000; index++ {
		store.completeRequest(fmt.Sprintf("newer-%d", index), "cursor-account.json", true, "succeeded")
	}
	expected := store.snapshot("cursor-account.json").Requests

	store.completeRequest("oldest", "cursor-account.json", true, "succeeded")

	require.Equal(t, expected, store.snapshot("cursor-account.json").Requests)
}

func TestUsageStore_DeduplicatesConcurrentCompletionCallbacks(t *testing.T) {
	store := newUsageStore()
	var workers sync.WaitGroup
	for worker := 0; worker < 100; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			store.completeRequest("request", "cursor-account.json", true, "succeeded")
		}()
	}
	workers.Wait()

	require.EqualValues(t, 1, store.snapshot("cursor-account.json").Requests)
}
