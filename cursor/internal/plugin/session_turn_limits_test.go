package plugin

import (
	"context"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_Session_total_admission_recovers_after_release(t *testing.T) {
	locks := newSessionTurnLocks()
	var releases []func()
	for index := range 64 {
		release, err := locks.acquire(context.Background(), sessionTurnKey{account: "account", model: "auto", session: strconv.Itoa(index)})
		require.NoError(t, err)
		releases = append(releases, release)
	}

	release, err := locks.acquire(context.Background(), sessionTurnKey{account: "other", model: "auto", session: "overflow"})

	require.ErrorIs(t, err, errSessionBusy)
	require.Nil(t, release)
	releases[0]()
	release, err = locks.acquire(context.Background(), sessionTurnKey{account: "other", model: "auto", session: "overflow"})
	require.NoError(t, err)
	release()
	for _, done := range releases[1:] {
		done()
	}
	require.Empty(t, locks.locks)
	require.Zero(t, locks.total)
}

func Test_Session_background_wait_has_a_deadline_and_reclaims_its_slot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := newSessionTurnLocks()
		key := sessionTurnKey{account: "account", model: "auto", session: "same"}
		release, err := locks.acquire(context.Background(), key)
		require.NoError(t, err)
		started := time.Now()

		waiter, err := locks.acquire(context.Background(), key)

		require.ErrorIs(t, err, errSessionBusy)
		require.Nil(t, waiter)
		require.Equal(t, 30*time.Second, time.Since(started))
		require.Equal(t, 1, locks.total)
		release()
		release, err = locks.acquire(context.Background(), key)
		require.NoError(t, err)
		release()
		require.Empty(t, locks.locks)
		require.Zero(t, locks.total)
	})
}
