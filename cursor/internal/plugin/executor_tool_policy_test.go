package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func withToolLoopGuard(t *testing.T, raw []byte, tools []string) []byte {
	t.Helper()
	var request executorRequest
	require.NoError(t, json.Unmarshal(raw, &request))
	var storage map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(request.StorageJSON, &storage))
	encoded, err := json.Marshal(tools)
	require.NoError(t, err)
	storage["tool_loop_guard_tools"] = encoded
	request.StorageJSON, err = json.Marshal(storage)
	require.NoError(t, err)
	updated, err := json.Marshal(request)
	require.NoError(t, err)
	return updated
}

func Test_Executor_allows_legitimate_repetition_without_explicit_tool_policy(t *testing.T) {
	for _, scenario := range []struct{ name, arguments, result, user string }{
		{"append_record", `{"record":"heartbeat"}`, `{"ok":true}`, "Append exactly three heartbeat records, then summarize completion."},
		{"job_status", `{"job_id":"synthetic-job"}`, `{"status":"running"}`, "Poll this job until it finishes, then report the result."},
		{"todowrite", `{"todos":[]}`, `{"ok":true}`, "Update the task list, then finish."},
	} {
		for _, method := range []string{"executor.execute", "executor.execute_stream"} {
			t.Run(scenario.name+"/"+method, func(t *testing.T) {
				// Given
				messages := repeatedToolMessages(scenario.name, 3)
				messages[0] = textMessage("user", scenario.user)
				for index := range 3 {
					setToolCall(messages[index*2+1], fmt.Sprintf("call-%d", index), scenario.name, scenario.arguments)
					messages[index*2+2]["content"] = scenario.result
				}
				client := &recordingCursorClient{steps: []cursorRunStep{successfulTextStep("finished", "conversation", []byte("checkpoint"))}}
				emitter := &captureEmitter{done: make(chan struct{})}
				handler := NewHandler(Dependencies{Cursor: client, Emitter: emitter})
				raw := executorFixture(t, "session", "account", "auth", "auto", "stream", messages)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				// When
				response, ok := handler.CallWithStatus(ctx, method, raw)

				// Then
				require.True(t, ok, "%s", response)
				if method == "executor.execute_stream" {
					select {
					case <-emitter.done:
					case <-ctx.Done():
						t.Fatal("stream did not finish")
					}
					require.NoError(t, emitter.closeError)
					require.Contains(t, string(emitter.payloads[0]), `"content":"finished"`)
					require.Equal(t, "[DONE]", string(emitter.payloads[len(emitter.payloads)-1]))
				} else {
					var envelope envelope
					require.NoError(t, json.Unmarshal(response, &envelope))
					var result executorResponse
					require.NoError(t, json.Unmarshal(envelope.Result, &result))
					require.Contains(t, string(result.Payload), `"content":"finished"`)
				}
				require.Len(t, client.Inputs(), 1)
			})
		}
	}
}

func Test_Executor_applies_exact_tool_policy_per_request(t *testing.T) {
	client := &recordingCursorClient{}
	for range 8 {
		client.steps = append(client.steps, successfulTextStep("finished", "conversation", []byte("checkpoint")))
	}
	handler := NewHandler(Dependencies{Cursor: client})
	for _, scenario := range []struct {
		name    string
		tools   []string
		blocked bool
	}{
		{"explicit match", []string{"todowrite"}, true},
		{"explicit empty", []string{}, false},
		{"null", nil, false},
		{"other tool", []string{"job_status"}, false},
		{"wildcard is not a match", []string{"*"}, false},
		{"prefix is not a match", []string{"todo"}, false},
		{"case differs", []string{"TodoWrite"}, false},
		{"spaces differ", []string{" todowrite "}, false},
		{"match after another name", []string{"job_status", "todowrite"}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// Given
			raw := executorFixture(t, "session", "account", "auth", "auto", "", repeatedToolMessages("todowrite", 3))
			raw = withToolLoopGuard(t, raw, scenario.tools)
			callsBefore := len(client.Inputs())

			// When
			response, ok := handler.CallWithStatus(context.Background(), "executor.execute", raw)

			// Then
			require.Equal(t, !scenario.blocked, ok, "%s", response)
			if scenario.blocked {
				require.Len(t, client.Inputs(), callsBefore)
				require.Contains(t, string(response), `"code":"cursor_tool_loop_detected"`)
			} else {
				require.Len(t, client.Inputs(), callsBefore+1)
			}
		})
	}
}

func Test_Executor_chat_payload_cannot_override_tool_policy(t *testing.T) {
	for _, scenario := range []struct {
		name, payloadPolicy string
		storedPolicy        []string
		calls               int
	}{
		{"cannot enable", `["todowrite"]`, nil, 1},
		{"cannot disable", `[]`, []string{"todowrite"}, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client := &recordingCursorClient{steps: []cursorRunStep{successfulTextStep("finished", "conversation", []byte("checkpoint"))}}
			handler := NewHandler(Dependencies{Cursor: client})
			raw := executorFixture(t, "session", "account", "auth", "auto", "", repeatedToolMessages("todowrite", 3))
			raw = withToolLoopGuard(t, raw, scenario.storedPolicy)
			var request executorRequest
			require.NoError(t, json.Unmarshal(raw, &request))
			var payload map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(request.Payload, &payload))
			payload["tool_loop_guard_tools"] = json.RawMessage(scenario.payloadPolicy)
			var err error
			request.Payload, err = json.Marshal(payload)
			require.NoError(t, err)
			raw, err = json.Marshal(request)
			require.NoError(t, err)

			response, ok := handler.CallWithStatus(context.Background(), "executor.execute", raw)

			require.Equal(t, scenario.calls == 1, ok, "%s", response)
			require.Len(t, client.Inputs(), scenario.calls)
		})
	}
}
