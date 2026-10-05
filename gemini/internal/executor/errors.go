package executor

import (
	"encoding/json"
	"fmt"
	"strings"
)

type statusError struct {
	statusCode int
	body       []byte
}

func (e statusError) Error() string {
	message := upstreamErrorMessage(e.body)
	if message != "" {
		return message
	}
	return fmt.Sprintf("status %d", e.statusCode)
}

func (e statusError) StatusCode() int {
	return e.statusCode
}

func upstreamErrorMessage(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var decoded struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if errUnmarshal := json.Unmarshal([]byte(trimmed), &decoded); errUnmarshal == nil {
		if len(decoded.Error) > 0 {
			var errorObject struct {
				Message string `json:"message"`
			}
			if errObject := json.Unmarshal(decoded.Error, &errorObject); errObject == nil {
				if message := strings.TrimSpace(errorObject.Message); message != "" {
					return message
				}
			}
			var errorString string
			if errString := json.Unmarshal(decoded.Error, &errorString); errString == nil {
				if message := strings.TrimSpace(errorString); message != "" {
					return message
				}
			}
		}
		if message := strings.TrimSpace(decoded.Message); message != "" {
			return message
		}
	}
	return trimmed
}
