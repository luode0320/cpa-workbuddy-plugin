package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Executor_rejects_cross_message_no_progress_before_upstream(t *testing.T) {
	for _, method := range []string{"executor.execute", "executor.execute_stream"} {
		t.Run(method, func(t *testing.T) {
			client := &recordingCursorClient{}
			emitter := &captureEmitter{done: make(chan struct{})}
			handler := NewHandler(Dependencies{Cursor: client, Emitter: emitter})
			raw := executorFixture(t, "session", "account", "auth", "auto", "stream", repeatedToolMessages("todowrite", 3))
			raw = withToolLoopGuard(t, raw, []string{"todowrite"})

			response, ok := handler.CallWithStatus(context.Background(), method, raw)

			require.Empty(t, client.Inputs(), "a blocked loop must not spend another upstream request")
			require.False(t, ok)
			var result envelope
			require.NoError(t, json.Unmarshal(response, &result))
			require.NotNil(t, result.Error)
			require.Equal(t, "cursor_tool_loop_detected", result.Error.Code)
			require.Equal(t, 400, result.Error.HTTPStatus)
			require.False(t, result.Error.Retryable)
			require.Empty(t, result.Result)
		})
	}
}

func repeatedToolMessages(name string, count int) []map[string]any {
	messages := []map[string]any{textMessage("user", "Complete the synthetic task")}
	for index := range count {
		id := fmt.Sprintf("call-%d", index)
		messages = append(messages, map[string]any{
			"role": "assistant", "content": "Updating the task list",
			"tool_calls": []map[string]any{{"id": id, "type": "function", "function": map[string]string{
				"name": name, "arguments": `{"todos":[{"content":"synthetic task","status":"in_progress"}]}`,
			}}},
		}, map[string]any{"role": "tool", "tool_call_id": id, "content": `{"ok":true}`})
	}
	return messages
}

func Test_Executor_allows_progress_and_unambiguous_user_restarts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]map[string]any) []map[string]any
	}{
		{"below threshold", func(messages []map[string]any) []map[string]any { return messages[:5] }},
		{"changed result", func(messages []map[string]any) []map[string]any {
			messages[4]["content"] = `{"ok":true,"progress":1}`
			return messages
		}},
		{"changed arguments", func(messages []map[string]any) []map[string]any {
			setToolCall(messages[3], "call-1", "todowrite", `{"todos":[]}`)
			return messages
		}},
		{"different tool", func(messages []map[string]any) []map[string]any {
			setToolCall(messages[3], "call-1", "read", `{"path":"synthetic.txt"}`)
			return messages
		}},
		{"new user message", func(messages []map[string]any) []map[string]any {
			return append(messages, textMessage("user", "Please retry explicitly"))
		}},
		{"user between calls", func(messages []map[string]any) []map[string]any {
			return append(append(messages[:3:3], textMessage("user", "Continue")), messages[3:]...)
		}},
		{"unlinked result", func(messages []map[string]any) []map[string]any {
			messages[4]["tool_call_id"] = "another-call"
			return messages
		}},
		{"mismatched result name", func(messages []map[string]any) []map[string]any {
			messages[4]["name"] = "different-tool"
			return messages
		}},
		{"duplicated call id", func(messages []map[string]any) []map[string]any {
			setToolCall(messages[3], "call-0", "todowrite", `{"todos":[{"content":"synthetic task","status":"in_progress"}]}`)
			messages[4]["tool_call_id"] = "call-0"
			return messages
		}},
		{"new assistant output", func(messages []map[string]any) []map[string]any {
			return append(messages, textMessage("assistant", "The task has now advanced"))
		}},
		{"distinct large integer arguments", func(messages []map[string]any) []map[string]any {
			for index := range 3 {
				setToolCall(messages[index*2+1], fmt.Sprintf("call-%d", index), "read", `{"id":9007199254740992}`)
			}
			setToolCall(messages[3], "call-1", "read", `{"id":9007199254740993}`)
			return messages
		}},
		{"distinct large integer results", func(messages []map[string]any) []map[string]any {
			for index := range 3 {
				messages[index*2+2]["content"] = `{"id":9007199254740992}`
			}
			messages[4]["content"] = `{"id":9007199254740993}`
			return messages
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingCursorClient{steps: []cursorRunStep{successfulTextStep("progress", "conversation", []byte("checkpoint"))}}
			handler := NewHandler(Dependencies{Cursor: client})
			raw := executorFixture(t, "session", "account", "auth", "auto", "", test.mutate(repeatedToolMessages("todowrite", 3)))
			raw = withToolLoopGuard(t, raw, []string{"todowrite", "read"})

			response, ok := handler.CallWithStatus(context.Background(), "executor.execute", raw)

			require.True(t, ok, "%s", response)
			require.Len(t, client.Inputs(), 1)
		})
	}
}

func Test_Executor_blocks_equivalent_JSON_and_empty_assistant_placeholder(t *testing.T) {
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("progress", "conversation", []byte("checkpoint")),
		successfulTextStep("progress", "other-conversation", []byte("checkpoint")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	messages := repeatedToolMessages("todowrite", 3)
	setToolCall(messages[3], "call-1", "todowrite", ` { "todos": [ { "status": "in_progress", "content": "synthetic task" } ] } `)
	messages[4]["content"] = ` { "ok": true } `
	messages = append(messages, textMessage("assistant", ""))
	raw := executorFixture(t, "session", "account", "auth", "auto", "", messages)
	raw = withToolLoopGuard(t, raw, []string{"todowrite"})

	response, ok := handler.CallWithStatus(context.Background(), "executor.execute", raw)

	require.False(t, ok)
	require.Contains(t, string(response), "cursor_tool_loop_detected")
	require.Empty(t, client.Inputs())
	for _, identity := range []string{"session", "another-session"} {
		fresh := executorFixture(t, identity, "account", "auth", "auto", "", repeatedToolMessages("todowrite", 2))
		fresh = withToolLoopGuard(t, fresh, []string{"todowrite"})
		_, ok := handler.CallWithStatus(context.Background(), "executor.execute", fresh)
		require.True(t, ok, "the guard must not poison subsequent requests")
	}
}

func setToolCall(message map[string]any, id, name, arguments string) {
	message["tool_calls"] = []map[string]any{{"id": id, "type": "function", "function": map[string]string{
		"name": name, "arguments": arguments,
	}}}
}
