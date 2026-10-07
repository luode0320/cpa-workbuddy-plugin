package cursorsession

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Store_Concurrent_32_independent_sessions_remain_isolated(t *testing.T) {
	store := NewStore(Options{})
	const sessions = 32
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(sessions)
	done.Add(sessions)
	errors := make(chan error, sessions)
	for index := range sessions {
		go func() {
			defer done.Done()
			key, err := NewKey(fmt.Sprintf("account-%d", index), "model", fmt.Sprintf("session-%d", index))
			if err != nil {
				errors <- err
				return
			}
			ready.Done()
			<-start
			token, _ := store.Start(key, fixtureMatch())
			checkpoint := fmt.Sprintf("checkpoint-%d", index)
			if result := store.Commit(token, fixtureConfirmed(checkpoint)); result.Reason != ReasonStored {
				errors <- fmt.Errorf("commit %d: %s", index, result.Reason)
				return
			}
			_, hit := store.Start(key, fixtureMatch())
			if hit.Reason != ReasonHit || string(hit.Snapshot.CheckpointBytes()) != checkpoint {
				errors <- fmt.Errorf("lookup %d: reason=%s checkpoint=%q", index, hit.Reason, hit.Snapshot.CheckpointBytes())
			}
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
}
