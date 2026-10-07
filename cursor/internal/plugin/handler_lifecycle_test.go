package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Handler_RequestLifecycle_ignores_non_cursor_auth(t *testing.T) {
	handler := NewHandler(Dependencies{})

	observeRequestAuth(t, handler, "request-codex", "codex-auth-id")
	completeCursorRequest(t, handler, "request-codex", "succeeded")

	usage := handler.usage.snapshot("codex-auth-id")
	require.Zero(t, usage.Requests)
	require.Zero(t, usage.Succeeded)
	require.Zero(t, usage.Failed)
}

func Test_Handler_RequestLifecycle_counts_prepare_failure_from_completion_metadata(t *testing.T) {
	handler := NewHandler(Dependencies{})

	completeCursorRequestForAuth(t, handler, "request-prepare", "cursor-auth-id", "failed")

	usage := handler.usage.snapshot("cursor-auth-id")
	require.EqualValues(t, 1, usage.Requests)
	require.Zero(t, usage.Succeeded)
	require.EqualValues(t, 1, usage.Failed)
}

func Test_Handler_RequestLifecycle_completion_metadata_overrides_stale_after_auth(t *testing.T) {
	handler := NewHandler(Dependencies{})
	observeRequestAuth(t, handler, "request-retry", "cursor-auth-old")

	completeCursorRequestForAuth(t, handler, "request-retry", "cursor-auth-new", "failed")

	require.Zero(t, handler.usage.snapshot("cursor-auth-old").Requests)
	usage := handler.usage.snapshot("cursor-auth-new")
	require.EqualValues(t, 1, usage.Requests)
	require.EqualValues(t, 1, usage.Failed)
}

func Test_Handler_RequestLifecycle_non_cursor_completion_drops_stale_cursor_auth(t *testing.T) {
	handler := NewHandler(Dependencies{})
	observeRequestAuth(t, handler, "request-rerouted", "cursor-auth-old")

	completeCursorRequestForAuth(t, handler, "request-rerouted", "codex-auth-new", "failed")

	require.Zero(t, handler.usage.snapshot("cursor-auth-old").Requests)
}
