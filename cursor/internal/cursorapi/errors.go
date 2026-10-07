package cursorapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var (
	ErrInvalidArgument    = errors.New("Cursor invalid argument")
	ErrEmptyCompletion    = errors.New("Cursor stream completed without output")
	ErrInternalStream     = errors.New("Cursor internal stream failure")
	ErrFailedPrecondition = errors.New("Cursor failed precondition")
)

type connectStreamError struct {
	code  string
	cause error
}

func (streamError connectStreamError) Error() string {
	return "Cursor stream failed: " + streamError.code
}

func (streamError connectStreamError) Unwrap() error {
	return streamError.cause
}

func (streamError connectStreamError) StatusCode() int {
	switch streamError.code {
	case "unauthenticated":
		return http.StatusUnauthorized
	case "permission_denied":
		return http.StatusForbidden
	case "resource_exhausted":
		return http.StatusTooManyRequests
	default:
		return 0
	}
}

type connectEndStreamResponse struct {
	Error *connectWireError `json:"error"`
}

type connectWireError struct {
	Code string `json:"code"`
}

func IsInvalidArgument(err error) bool {
	return errors.Is(err, ErrInvalidArgument)
}

func IsReplayableCheckpointError(err error) bool {
	var status interface{ StatusCode() int }
	if errors.As(err, &status) && status.StatusCode() != 0 && status.StatusCode() != http.StatusBadRequest {
		return false
	}
	return IsInvalidArgument(err) || errors.Is(err, ErrEmptyCompletion) || errors.Is(err, ErrInternalStream) ||
		errors.Is(err, ErrFailedPrecondition) || errors.Is(err, ErrProgressTimeout)
}

func runStatusError(status int, body []byte) error {
	var response struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &response) == nil && strings.EqualFold(response.Code, "invalid_argument") {
		return &runHTTPError{status: status, cause: ErrInvalidArgument}
	}
	return &runHTTPError{status: status}
}

type runHTTPError struct {
	status int
	cause  error
}

func (err *runHTTPError) Error() string {
	if err.cause != nil {
		return fmt.Sprintf("%s: Cursor Run returned HTTP %d", err.cause, err.status)
	}
	return fmt.Sprintf("Cursor Run returned HTTP %d", err.status)
}

func (err *runHTTPError) Unwrap() error   { return err.cause }
func (err *runHTTPError) StatusCode() int { return err.status }

func connectEndStreamError(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	var response connectEndStreamResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return fmt.Errorf("decode Cursor Connect end stream: %w", err)
	}
	if response.Error == nil {
		return nil
	}
	code := strings.ToLower(strings.TrimSpace(response.Error.Code))
	switch code {
	case "invalid_argument":
		return fmt.Errorf("%w: Cursor rejected the request", ErrInvalidArgument)
	case "internal":
		return connectStreamError{code: code, cause: ErrInternalStream}
	case "failed_precondition":
		return connectStreamError{code: code, cause: ErrFailedPrecondition}
	case "canceled":
		return connectStreamError{code: code, cause: context.Canceled}
	case "deadline_exceeded":
		return connectStreamError{code: code, cause: context.DeadlineExceeded}
	case "unknown", "not_found", "already_exists", "permission_denied",
		"resource_exhausted", "aborted", "out_of_range", "unimplemented",
		"unavailable", "data_loss", "unauthenticated":
		return connectStreamError{code: code}
	default:
		return errors.New("Cursor stream failed")
	}
}
