package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_ExecuteStream_preserves_timeout_scope_and_exposure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result cursorapi.RunResult
		events []cursorproto.ServerEvent
	}{
		{name: "before output"},
		{name: "after text", result: cursorapi.RunResult{OutputExposed: true}, events: []cursorproto.ServerEvent{{Kind: cursorproto.EventText, Text: "partial"}}},
		{name: "after tool", result: cursorapi.RunResult{ToolExposed: true}, events: []cursorproto.ServerEvent{{Kind: cursorproto.EventToolCall, ID: "call1", Name: "bash", Arguments: `{"command":"true"}`}}},
		{name: "after interaction", result: cursorapi.RunResult{InteractionResponded: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a single upstream attempt with a typed watchdog failure.
			client := &recordingCursorClient{steps: []cursorRunStep{{
				result: tc.result, events: tc.events, err: fmt.Errorf("run failed: %w", cursorapi.ErrProgressTimeout),
			}}}
			emitter := &captureEmitter{done: make(chan struct{})}
			handler := NewHandler(Dependencies{Cursor: client, Emitter: emitter})
			raw := executorFixture(t, "session", "account", "auth", "auto", "stream", []map[string]any{textMessage("user", "test")})
			// When: the real stream handler terminates and the C bridge payload is encoded.
			_, err := handler.executeStream(context.Background(), raw)
			require.NoError(t, err)
			<-emitter.done
			wire, err := json.Marshal(NewStreamCloseRequest("stream", emitter.closeError))
			require.NoError(t, err)
			var request struct {
				Error        string `json:"error"`
				ErrorDetails struct {
					Code                 string `json:"code"`
					HTTPStatus           int    `json:"http_status"`
					RequestScoped        bool   `json:"request_scoped"`
					Retryable            bool   `json:"retryable"`
					OutputExposed        bool   `json:"output_exposed"`
					ToolExposed          bool   `json:"tool_exposed"`
					InteractionResponded bool   `json:"interaction_responded"`
				} `json:"error_details"`
			}
			require.NoError(t, json.Unmarshal(wire, &request))
			// Then: timeout is terminal for this request, not an authentication failure.
			require.ErrorIs(t, emitter.closeError, cursorapi.ErrProgressTimeout)
			require.Len(t, client.Inputs(), 1)
			require.Equal(t, emitter.closeError.Error(), request.Error)
			require.Equal(t, "cursor_progress_timeout", request.ErrorDetails.Code)
			require.Equal(t, http.StatusGatewayTimeout, request.ErrorDetails.HTTPStatus)
			require.True(t, request.ErrorDetails.RequestScoped)
			require.False(t, request.ErrorDetails.Retryable)
			require.Equal(t, tc.result.OutputExposed, request.ErrorDetails.OutputExposed)
			require.Equal(t, tc.result.ToolExposed, request.ErrorDetails.ToolExposed)
			require.Equal(t, tc.result.InteractionResponded, request.ErrorDetails.InteractionResponded)
		})
	}
}

func Test_Execute_preserves_timeout_classification_in_RPC_envelope(t *testing.T) {
	// Given
	client := &recordingCursorClient{steps: []cursorRunStep{{err: cursorapi.ErrProgressTimeout}}}
	handler := NewHandler(Dependencies{Cursor: client})
	raw := executorFixture(t, "session", "account", "auth", "auto", "", []map[string]any{textMessage("user", "test")})
	// When
	response, ok := handler.CallWithStatus(context.Background(), "executor.execute", raw)
	var envelope struct {
		Error struct {
			Code          string `json:"code"`
			HTTPStatus    int    `json:"http_status"`
			RequestScoped bool   `json:"request_scoped"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response, &envelope))
	// Then
	require.False(t, ok)
	require.Equal(t, "cursor_progress_timeout", envelope.Error.Code)
	require.Equal(t, http.StatusGatewayTimeout, envelope.Error.HTTPStatus)
	require.True(t, envelope.Error.RequestScoped)
}

func Test_StreamClose_keeps_legacy_error_and_success_contract(t *testing.T) {
	for _, cause := range []error{nil, errors.New("other failure"), context.Canceled, cursorapi.ErrFirstDataTimeout, cursorapi.ErrFrameSilenceTimeout} {
		// Given / When
		request := NewStreamCloseRequest("stream", cause)
		// Then
		require.Equal(t, "stream", request.StreamID)
		if cause == nil {
			require.Empty(t, request.Error)
			require.Nil(t, request.ErrorDetails)
			continue
		}
		require.Equal(t, cause.Error(), request.Error)
		require.Equal(t, cause.Error(), request.ErrorDetails.Message)
	}
}

func Test_ExecutionErrorDetails_keeps_explicit_auth_status_over_request_cause(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		err := &requestCauseStatusError{status: status}
		details := describeExecutionError(err)
		require.Equal(t, status, details.HTTPStatus)
		require.False(t, details.RequestScoped)
		require.True(t, details.Retryable)
	}
}

type requestCauseStatusError struct{ status int }

func (err *requestCauseStatusError) Error() string {
	return "upstream status with invalid_argument cause"
}
func (err *requestCauseStatusError) Unwrap() error   { return cursorapi.ErrInvalidArgument }
func (err *requestCauseStatusError) StatusCode() int { return err.status }
