package plugin

import (
	"context"
	"errors"
	"net/http"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/openai"
)

type executionError struct {
	cause  error
	result cursorapi.RunResult
}

func (err *executionError) Error() string { return err.cause.Error() }
func (err *executionError) Unwrap() error { return err.cause }

func withExecutionResult(err error, result cursorapi.RunResult) error {
	if err == nil {
		return nil
	}
	return &executionError{cause: err, result: result}
}

type StreamCloseRequest struct {
	StreamID     string         `json:"stream_id"`
	Error        string         `json:"error,omitempty"`
	ErrorDetails *envelopeError `json:"error_details,omitempty"`
}

func NewStreamCloseRequest(streamID string, err error) StreamCloseRequest {
	request := StreamCloseRequest{StreamID: streamID}
	if err != nil {
		request.Error = err.Error()
		request.ErrorDetails = describeExecutionError(err)
	}
	return request
}

func describeExecutionError(err error) *envelopeError {
	details := &envelopeError{Code: "cursor_plugin_error", Message: err.Error()}
	var execution *executionError
	if errors.As(err, &execution) {
		details.OutputExposed = execution.result.OutputExposed
		details.ToolExposed = execution.result.ToolExposed
		details.InteractionResponded = execution.result.InteractionResponded
	}
	var status interface{ StatusCode() int }
	if errors.As(err, &status) {
		details.HTTPStatus = status.StatusCode()
	}
	switch {
	case errors.Is(err, errSessionBusy):
		details.Code = "cursor_session_busy"
		details.HTTPStatus = http.StatusConflict
		details.RequestScoped = true
	case errors.Is(err, cursorapi.ErrProgressTimeout):
		details.Code = "cursor_progress_timeout"
		details.HTTPStatus = http.StatusGatewayTimeout
		details.RequestScoped = true
	case errors.Is(err, cursorapi.ErrFirstDataTimeout), errors.Is(err, cursorapi.ErrFrameSilenceTimeout):
		details.Code = "cursor_transport_timeout"
		details.HTTPStatus = http.StatusGatewayTimeout
		details.RequestScoped = true
	case errors.Is(err, context.Canceled):
		details.Code = "request_canceled"
		details.HTTPStatus = 499
		details.RequestScoped = true
	case errors.Is(err, context.DeadlineExceeded):
		details.Code = "cursor_request_timeout"
		details.HTTPStatus = http.StatusGatewayTimeout
		details.RequestScoped = true
	case errors.Is(err, openai.ErrInvalidRequest):
		details.HTTPStatus = http.StatusBadRequest
		details.RequestScoped = true
	case errors.Is(err, cursorapi.ErrInvalidArgument) && (details.HTTPStatus == 0 || details.HTTPStatus == http.StatusBadRequest):
		details.HTTPStatus = http.StatusBadRequest
		details.RequestScoped = true
	}
	var loop *openai.ToolLoopError
	if errors.As(err, &loop) {
		details.Code = "cursor_tool_loop_detected"
	}
	var limit *contextLimitError
	if errors.As(err, &limit) {
		details.Code = "context_length_exceeded"
	}
	details.Retryable = !details.RequestScoped && !details.OutputExposed && !details.ToolExposed && !details.InteractionResponded
	return details
}
