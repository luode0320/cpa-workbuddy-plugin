package cursorapi

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_RunStatusError_classifies_only_exact_invalid_argument_code(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "exact code", status: 400, body: `{"code":"invalid_argument","message":"checkpoint rejected"}`, want: true},
		{name: "mixed-case code", status: 400, body: `{"code":"INVALID_ARGUMENT"}`, want: true},
		{name: "negated code", status: 400, body: `{"code":"not_invalid_argument"}`, want: false},
		{name: "superstring code", status: 400, body: `{"code":"invalid_argument_retry"}`, want: false},
		{name: "HTTP 500 message only", status: 500, body: `{"code":"internal","message":"invalid_argument"}`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			body := []byte(test.body)

			// When
			err := runStatusError(test.status, body)

			// Then
			require.Equal(t, test.want, IsInvalidArgument(err))
		})
	}
}

func Test_IsInvalidArgument_recognizes_only_wrapped_typed_error(t *testing.T) {
	// Given
	wrapped := fmt.Errorf("start Cursor Run: %w", ErrInvalidArgument)
	messageOnly := errors.New("invalid_argument: rejected checkpoint")

	// When
	wrappedMatches := IsInvalidArgument(wrapped)
	messageOnlyMatches := IsInvalidArgument(messageOnly)

	// Then
	require.True(t, wrappedMatches)
	require.False(t, messageOnlyMatches)
}
