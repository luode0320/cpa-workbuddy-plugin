package plugin

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
)

func Test_Session_waiter_cancellation_returns_without_waiting_for_active_turn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		step := successfulTextStep("ok", "conversation", nil)
		step.entered, step.release = entered, release
		client := &recordingCursorClient{steps: []cursorRunStep{step}}
		handler := NewHandler(Dependencies{Cursor: client})
		request := executorFixture(t, "same", "account", "auth", "auto", "", []map[string]any{textMessage("user", "hello")})
		firstDone := make(chan error, 1)
		go func() { _, err := handler.execute(context.Background(), request); firstDone <- err }()
		<-entered
		ctx, cancel := context.WithCancel(context.Background())
		waiterDone := make(chan error, 1)
		go func() { _, err := handler.execute(ctx, request); waiterDone <- err }()
		synctest.Wait()

		cancel()
		synctest.Wait()

		select {
		case err := <-waiterDone:
			require.ErrorIs(t, err, context.Canceled)
		default:
			t.Error("cancelled waiter still retains the request behind the active turn")
		}
		close(release)
		require.NoError(t, <-firstDone)
		synctest.Wait()
		require.Len(t, client.Inputs(), 1)
	})
}

func Test_Session_rejects_excess_waiters_without_upstream_or_account_penalty(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		step := successfulTextStep("ok", "conversation", nil)
		step.entered, step.release = entered, release
		client := &recordingCursorClient{steps: []cursorRunStep{step}}
		handler := NewHandler(Dependencies{Cursor: client})
		request := executorFixture(t, "same", "account", "auth", "auto", "", []map[string]any{textMessage("user", "hello")})
		firstDone := make(chan error, 1)
		go func() { _, err := handler.execute(context.Background(), request); firstDone <- err }()
		<-entered
		ctx, cancel := context.WithCancel(context.Background())
		results := make(chan error, 17)
		for range 17 {
			go func() { _, err := handler.execute(ctx, request); results <- err }()
		}
		synctest.Wait()

		immediate := len(results)

		require.Equal(t, 9, immediate, "one active turn permits at most eight queued followers")
		for range immediate {
			details := describeExecutionError(<-results)
			require.Equal(t, 409, details.HTTPStatus)
			require.True(t, details.RequestScoped)
			require.False(t, details.Retryable)
		}
		cancel()
		close(release)
		require.NoError(t, <-firstDone)
		for range 17 - immediate {
			require.True(t, errors.Is(<-results, context.Canceled))
		}
		require.Len(t, client.Inputs(), 1)
	})
}
