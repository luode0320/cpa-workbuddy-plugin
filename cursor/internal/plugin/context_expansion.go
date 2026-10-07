package plugin

import (
	"encoding/json"
	"strings"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/openai"
)

type contextExpansionMessage struct {
	Role       string `json:"role"`
	Name       string `json:"name"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// Tool-result names can be inherited repeatedly from one assistant tool call.
func validateContextExpansion(messages []json.RawMessage, inputBytes, limit int64) error {
	nameBytes := make(map[string]int64)
	for _, raw := range messages {
		var message contextExpansionMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			return &openai.InvalidRequestError{Message: "decode context expansion: " + err.Error()}
		}
		switch message.Role {
		case "assistant":
			for _, call := range message.ToolCalls {
				nameBytes[call.ID] = int64(len(call.Function.Name))
			}
		case "tool":
			if strings.TrimSpace(message.Name) == "" {
				inputBytes += nameBytes[message.ToolCallID]
			}
		}
		if inputBytes > limit {
			return contextExceeded("input JSON and inherited tool names exceed the conservative byte budget; reduce or compact the request")
		}
	}
	return nil
}
