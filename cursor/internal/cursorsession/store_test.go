package cursorsession

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_Store_uses_checkpoint_when_lineage_matches(t *testing.T) {
	clock := newFakeClock()
	store := NewStore(Options{Now: clock.Now})
	key := mustKey(t, "account-a", "model-a", "session-a")
	token, miss := store.Start(key, Match{SystemDigest: "system", PrefixDigest: "prefix", CoveredMessageCount: 4})
	require.Equal(t, ReasonMissing, miss.Reason)
	committed := store.Commit(token, Confirmed{
		ConversationID: "conversation-a", Checkpoint: []byte("opaque"), CoveredMessageCount: 4,
		SystemDigest: "system", PrefixDigest: "prefix",
	})
	require.Equal(t, ReasonStored, committed.Reason)

	_, hit := store.Start(key, Match{SystemDigest: "system", PrefixDigest: "prefix", CoveredMessageCount: 4})

	require.Equal(t, ReasonHit, hit.Reason)
	require.Equal(t, "conversation-a", hit.Snapshot.ConversationID)
	require.Equal(t, []byte("opaque"), hit.Snapshot.CheckpointBytes())
	checkpoint := hit.Snapshot.CheckpointBytes()
	checkpoint[0] = 'X'
	_, secondHit := store.Start(key, Match{SystemDigest: "system", PrefixDigest: "prefix", CoveredMessageCount: 4})
	require.Equal(t, []byte("opaque"), secondHit.Snapshot.CheckpointBytes())
}

func Test_NewStore_applies_bounded_process_local_defaults(t *testing.T) {
	store := NewStore(Options{})

	require.Equal(t, 15*time.Minute, store.ttl)
	require.Equal(t, 64, store.maxEntries)
	require.Equal(t, 16*1024*1024, store.maxBytes)
}

func Test_Store_reports_expiry_and_invalidation(t *testing.T) {
	clock := newFakeClock()
	store := NewStore(Options{Now: clock.Now, TTL: time.Minute})
	key := mustKey(t, "account", "model", "session")
	commitFixture(t, store, key, "checkpoint")
	clock.Advance(time.Minute)

	_, expired := store.Start(key, fixtureMatch())
	require.Equal(t, ReasonExpired, expired.Reason)

	token, _ := store.Start(key, fixtureMatch())
	require.Equal(t, ReasonStored, store.Commit(token, fixtureConfirmed("new")).Reason)
	invalidated := store.Invalidate(key, ReasonDecodeFailure)
	require.Equal(t, ReasonDecodeFailure, invalidated.Reason)
	_, missing := store.Start(key, fixtureMatch())
	require.Equal(t, ReasonMissing, missing.Reason)
}

func Test_Store_reports_key_and_lineage_mismatches(t *testing.T) {
	tests := []struct {
		name  string
		key   Key
		match Match
		want  Reason
	}{
		{name: "identity", key: newTestKey("account-b", "model-a", "session-a"), match: fixtureMatch(), want: ReasonIdentityMismatch},
		{name: "model", key: newTestKey("account-a", "model-b", "session-a"), match: fixtureMatch(), want: ReasonModelMismatch},
		{name: "session", key: newTestKey("account-a", "model-a", "session-b"), match: fixtureMatch(), want: ReasonSessionMismatch},
		{name: "system", key: newTestKey("account-a", "model-a", "session-a"), match: Match{SystemDigest: "changed", PrefixDigest: "prefix", CoveredMessageCount: 2}, want: ReasonSystemMismatch},
		{name: "prefix", key: newTestKey("account-a", "model-a", "session-a"), match: Match{SystemDigest: "system", PrefixDigest: "changed", CoveredMessageCount: 2}, want: ReasonPrefixMismatch},
		{name: "message count", key: newTestKey("account-a", "model-a", "session-a"), match: Match{SystemDigest: "system", PrefixDigest: "prefix", CoveredMessageCount: 3}, want: ReasonMessageCountMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore(Options{})
			commitFixture(t, store, newTestKey("account-a", "model-a", "session-a"), "checkpoint")
			_, result := store.Start(tt.key, tt.match)
			require.Equal(t, tt.want, result.Reason)
		})
	}
}

func Test_Store_compaction_invalidates_confirmed_checkpoint(t *testing.T) {
	store := NewStore(Options{})
	key := mustKey(t, "account", "model", "session")
	commitFixture(t, store, key, "checkpoint")

	_, compacted := store.Start(key, Match{Compacted: true})
	require.Equal(t, ReasonCompaction, compacted.Reason)
	_, missing := store.Start(key, fixtureMatch())
	require.Equal(t, ReasonMissing, missing.Reason)
}

func Test_Store_evicts_lru_by_entry_and_byte_limits_and_skips_oversize(t *testing.T) {
	store := NewStore(Options{MaxEntries: 2, MaxBytes: 6})
	keyA := newTestKey("a", "model", "a")
	keyB := newTestKey("b", "model", "b")
	keyC := newTestKey("c", "model", "c")
	commitFixture(t, store, keyA, "aa")
	commitFixture(t, store, keyB, "bb")
	_, _ = store.Start(keyA, fixtureMatch())
	token, _ := store.Start(keyC, fixtureMatch())
	result := store.Commit(token, fixtureConfirmed("cccc"))
	require.Equal(t, 1, result.Evicted)
	require.Equal(t, ReasonEvicted, result.EvictionReason)
	_, keyBMiss := store.Start(keyB, fixtureMatch())
	require.Equal(t, ReasonMissing, keyBMiss.Reason)
	_, keyAHit := store.Start(keyA, fixtureMatch())
	require.Equal(t, ReasonHit, keyAHit.Reason)

	oversizeToken, _ := store.Start(keyA, fixtureMatch())
	oversize := store.Commit(oversizeToken, fixtureConfirmed("1234567"))
	require.Equal(t, ReasonOversized, oversize.Reason)
	_, retained := store.Start(keyA, fixtureMatch())
	require.Equal(t, ReasonHit, retained.Reason)
	require.Equal(t, []byte("aa"), retained.Snapshot.CheckpointBytes())
}

func Test_Store_stale_same_key_commit_cannot_overwrite_newer_generation(t *testing.T) {
	store := NewStore(Options{})
	key := mustKey(t, "account", "model", "session")
	stale, _ := store.Start(key, fixtureMatch())
	newer, _ := store.Start(key, fixtureMatch())
	require.Equal(t, ReasonStored, store.Commit(newer, fixtureConfirmed("newer")).Reason)

	result := store.Commit(stale, fixtureConfirmed("stale"))

	require.Equal(t, ReasonStaleCommit, result.Reason)
	_, hit := store.Start(key, fixtureMatch())
	require.Equal(t, []byte("newer"), hit.Snapshot.CheckpointBytes())
}

func Test_Store_generation_bookkeeping_remains_bounded_under_distinct_keys(t *testing.T) {
	// Given
	const maxEntries = 4
	store := NewStore(Options{MaxEntries: maxEntries})
	oldKey := mustKey(t, "old-account", "model", "old-session")
	oldToken, _ := store.Start(oldKey, fixtureMatch())

	// When
	for index := range 1_000 {
		key := newTestKey(fmt.Sprintf("account-%d", index), "model", fmt.Sprintf("session-%d", index))
		if index%2 == 0 {
			_, _ = store.Start(key, fixtureMatch())
			continue
		}
		store.Invalidate(key, ReasonInvalidated)
	}

	// Then
	require.LessOrEqual(t, len(store.generations), maxEntries)
	require.Equal(t, len(store.generations), store.generationLRU.Len())
	require.Equal(t, ReasonStaleCommit, store.Commit(oldToken, fixtureConfirmed("stale")).Reason)
}

func commitFixture(t *testing.T, store *Store, key Key, checkpoint string) {
	t.Helper()
	token, _ := store.Start(key, fixtureMatch())
	require.Equal(t, ReasonStored, store.Commit(token, fixtureConfirmed(checkpoint)).Reason)
}

func fixtureMatch() Match {
	return Match{SystemDigest: "system", PrefixDigest: "prefix", CoveredMessageCount: 2}
}

func fixtureConfirmed(checkpoint string) Confirmed {
	return Confirmed{
		ConversationID: "conversation", Checkpoint: []byte(checkpoint), CoveredMessageCount: 2,
		SystemDigest: "system", PrefixDigest: "prefix",
	}
}

func mustKey(t *testing.T, account, model, session string) Key {
	t.Helper()
	key, err := NewKey(account, model, session)
	require.NoError(t, err)
	return key
}

func newTestKey(account, model, session string) Key {
	key, err := NewKey(account, model, session)
	if err != nil {
		panic(fmt.Sprintf("test key: %v", err))
	}
	return key
}

type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.now = c.now.Add(duration)
}
