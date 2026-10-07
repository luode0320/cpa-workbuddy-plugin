package cursorapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_ConnectEndStreamError_preserves_cancellation_and_deadline_causes(t *testing.T) {
	for _, tc := range []struct {
		code  string
		cause error
	}{
		{code: "canceled", cause: context.Canceled},
		{code: "deadline_exceeded", cause: context.DeadlineExceeded},
	} {
		t.Run(tc.code, func(t *testing.T) {
			err := connectEndStreamError([]byte(`{"error":{"code":"` + tc.code + `"}}`))
			require.ErrorIs(t, err, tc.cause)
			require.False(t, IsReplayableCheckpointError(err))
		})
	}
}

func Test_CheckpointReplay_rejects_HTTP_auth_and_quota_errors_with_invalid_argument_body(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		err := runStatusError(status, []byte(`{"code":"invalid_argument"}`))
		require.False(t, IsReplayableCheckpointError(err), "must not replay credential failure HTTP %d", status)
	}
	require.True(t, IsReplayableCheckpointError(runStatusError(http.StatusBadRequest, []byte(`{"code":"invalid_argument"}`))))
}

func Test_RunStatusError_preserves_auth_and_quota_HTTP_status(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		// Given / When
		err := runStatusError(status, []byte(`{"message":"SECRET_NOT_FOR_LOGS"}`))
		// Then
		var classified interface{ StatusCode() int }
		require.True(t, errors.As(err, &classified), "HTTP %d lost across transport error", status)
		require.Equal(t, status, classified.StatusCode())
		require.NotContains(t, err.Error(), "SECRET_NOT_FOR_LOGS")
	}
}

func Test_ConnectEndStreamError_preserves_auth_and_quota_status(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{code: "unauthenticated", status: http.StatusUnauthorized},
		{code: "permission_denied", status: http.StatusForbidden},
		{code: "resource_exhausted", status: http.StatusTooManyRequests},
	} {
		// Given / When
		err := connectEndStreamError([]byte(`{"error":{"code":"` + tc.code + `"}}`))
		// Then
		var classified interface{ StatusCode() int }
		require.True(t, errors.As(err, &classified), "Connect code %s lost", tc.code)
		require.Equal(t, tc.status, classified.StatusCode())
	}
}
