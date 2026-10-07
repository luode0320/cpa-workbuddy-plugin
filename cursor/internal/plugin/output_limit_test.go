package plugin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_Executor_honors_output_budget_on_both_response_paths(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, field := range []string{"max_tokens", "max_completion_tokens"} {
			t.Run(field+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				client := &recordingCursorClient{steps: []cursorRunStep{successfulTextStep("abcdef", "conversation", []byte("checkpoint"))}}
				emitter := &captureEmitter{done: make(chan struct{})}
				handler := NewHandler(Dependencies{Cursor: client, Emitter: emitter})
				body := map[string]any{"model": "cursor/auto", "messages": []map[string]any{textMessage("user", "hello")}, field: 4}
				request := contextRequestFixture(t, body, false)

				if !stream {
					result, err := handler.execute(context.Background(), request)
					require.NoError(t, err)
					var response struct {
						Choices []struct {
							Message      struct{ Content string }
							FinishReason string `json:"finish_reason"`
						}
					}
					require.NoError(t, json.Unmarshal(result.(executorResponse).Payload, &response))
					require.Equal(t, "abcd", response.Choices[0].Message.Content)
					require.Equal(t, "length", response.Choices[0].FinishReason)
					return
				}
				_, err := handler.executeStream(context.Background(), request)
				require.NoError(t, err)
				select {
				case <-emitter.done:
				case <-time.After(time.Second):
					t.Fatal("limited stream did not close")
				}
				require.NoError(t, emitter.closeError)
				require.Len(t, emitter.payloads, 3)
				var chunk struct {
					Choices []struct {
						Delta        struct{ Content string }
						FinishReason *string `json:"finish_reason"`
					}
				}
				require.NoError(t, json.Unmarshal(emitter.payloads[0], &chunk))
				require.Equal(t, "abcd", chunk.Choices[0].Delta.Content)
				require.NoError(t, json.Unmarshal(emitter.payloads[1], &chunk))
				require.NotNil(t, chunk.Choices[0].FinishReason)
				require.Equal(t, "length", *chunk.Choices[0].FinishReason)
				require.Equal(t, "[DONE]", string(emitter.payloads[2]))
			})
		}
	}
}

func Test_Executor_output_budget_preserves_utf8_and_never_emits_partial_tools(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   cursorproto.ServerEvent
		content string
	}{
		{"unicode", cursorproto.ServerEvent{Kind: cursorproto.EventText, Text: "中文"}, "中"},
		{"tool", cursorproto.ServerEvent{Kind: cursorproto.EventToolCall, ID: "call", Name: "read_file", Arguments: `{"path":"a.txt"}`}, ""},
		{"thinking", cursorproto.ServerEvent{Kind: cursorproto.EventThinking, Text: "thinking"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &recordingCursorClient{steps: []cursorRunStep{{events: []cursorproto.ServerEvent{tc.event, {Kind: cursorproto.EventDone}}}}}
			handler := NewHandler(Dependencies{Cursor: client})
			request := contextRequestFixture(t, map[string]any{"model": "cursor/auto", "messages": []map[string]any{textMessage("user", "hello")}, "max_tokens": 4}, false)

			result, err := handler.execute(context.Background(), request)

			require.NoError(t, err)
			var response struct {
				Choices []struct {
					Message struct {
						Content   string
						ToolCalls []json.RawMessage `json:"tool_calls"`
					}
					FinishReason string `json:"finish_reason"`
				}
			}
			require.NoError(t, json.Unmarshal(result.(executorResponse).Payload, &response))
			require.Equal(t, tc.content, response.Choices[0].Message.Content)
			require.Empty(t, response.Choices[0].Message.ToolCalls)
			require.Equal(t, "length", response.Choices[0].FinishReason)
		})
	}
}

func Test_Output_budget_cancels_at_exact_thinking_and_tool_boundaries(t *testing.T) {
	for _, event := range []cursorproto.ServerEvent{
		{Kind: cursorproto.EventThinking, Text: "abcd"},
		{Kind: cursorproto.EventToolCall, ID: "i", Name: "f", Arguments: "{}"},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		var emitted []cursorproto.ServerEvent
		budget := outputBudget{remaining: 4, cancel: cancel, emit: func(event cursorproto.ServerEvent) error {
			emitted = append(emitted, event)
			return nil
		}}

		err := budget.emitEvent(event)

		require.ErrorIs(t, err, errOutputLimit)
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.Equal(t, []cursorproto.ServerEvent{event}, emitted)
		cancel()
	}
}
