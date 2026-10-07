package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Context_limit_rejects_inherited_tool_names_before_expansion(t *testing.T) {
	for _, original := range []bool{false, true} {
		for _, method := range []string{"executor.execute", "executor.execute_stream", "executor.count_tokens"} {
			messages := toolNameExpansionMessages(strings.Repeat("n", 4096), 256)
			body := map[string]any{"model": "cursor/auto", "messages": messages}
			client := &recordingCursorClient{}
			handler := NewHandler(Dependencies{Cursor: client})

			raw, ok := handler.CallWithStatus(context.Background(), method, contextRequestFixture(t, body, original))

			require.False(t, ok)
			var result envelope
			require.NoError(t, json.Unmarshal(raw, &result))
			require.Equal(t, "context_length_exceeded", result.Error.Code)
			require.Contains(t, result.Error.Message, "inherited tool names")
			require.Empty(t, client.Inputs())
		}
	}
}

func Test_Context_limit_preserves_long_tool_names_and_explicit_result_names(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		messages := toolNameExpansionMessages(strings.Repeat("n", 4096), 256)
		if explicit {
			for _, message := range messages[2:] {
				message["name"] = "read"
			}
		} else {
			messages = messages[:3]
		}
		body := map[string]any{"model": "cursor/auto", "messages": messages}
		handler := NewHandler(Dependencies{Cursor: &recordingCursorClient{}})

		raw, ok := handler.CallWithStatus(context.Background(), "executor.count_tokens", contextRequestFixture(t, body, false))

		require.True(t, ok, string(raw))
	}
}

func Test_Context_expansion_tracks_latest_names_and_output_reserve(t *testing.T) {
	for _, name := range []string{"name", "中文"} {
		messages := toolNameExpansionMessages(name, 2)
		wire := make([]json.RawMessage, len(messages))
		for i, message := range messages {
			encoded, err := json.Marshal(message)
			require.NoError(t, err)
			wire[i] = encoded
		}
		limit := int64(100 + 2*len(name))
		require.NoError(t, validateContextExpansion(wire, 100, limit))
		require.Error(t, validateContextExpansion(wire, 100, limit-1))
		wire = append(wire[:2], append([]json.RawMessage{json.RawMessage(`{"role":"assistant","tool_calls":[{"id":"call","function":{"name":"n"}}]}`)}, wire[2:]...)...)
		require.NoError(t, validateContextExpansion(wire, 100, 102))
	}
}

func toolNameExpansionMessages(name string, results int) []map[string]any {
	messages := []map[string]any{textMessage("user", "hello"), {
		"role": "assistant", "tool_calls": []map[string]any{{
			"id": "call", "type": "function", "function": map[string]any{"name": name, "arguments": "{}"},
		}},
	}}
	for range results {
		messages = append(messages, map[string]any{"role": "tool", "tool_call_id": "call", "name": " \t", "content": "ok"})
	}
	return messages
}
