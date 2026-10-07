package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Executor_context_limit_blocks_all_entrypoints_before_upstream(t *testing.T) {
	large := strings.Repeat("x", 1_000_001)
	unicode := strings.Repeat("中", 340_000)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"text", map[string]any{"messages": []map[string]any{textMessage("user", large)}}},
		{"unicode", map[string]any{"messages": []map[string]any{textMessage("user", unicode)}}},
		{"history", map[string]any{"messages": []map[string]any{textMessage("user", large[:500_001]), textMessage("assistant", large[:500_001]), textMessage("user", "continue")}}},
		{"tools", map[string]any{"tools": []map[string]any{{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"description": large}}}}}},
		{"file", map[string]any{"messages": []map[string]any{{"role": "user", "content": []map[string]any{{"type": "file", "file": map[string]any{"filename": "test.txt", "file_data": base64.StdEncoding.EncodeToString([]byte(large))}}}}}}},
		{"output", map[string]any{"max_tokens": 1_000_001}},
		{"completion output", map[string]any{"max_completion_tokens": 1_000_001}},
	}
	for _, tc := range cases {
		for _, method := range []string{"executor.execute", "executor.execute_stream", "executor.count_tokens"} {
			for _, originalOnly := range []bool{false, true} {
				t.Run(tc.name+"/"+method+"/"+map[bool]string{false: "payload", true: "original"}[originalOnly], func(t *testing.T) {
					client := &recordingCursorClient{}
					emitter := &captureEmitter{done: make(chan struct{})}
					handler := NewHandler(Dependencies{Cursor: client, Emitter: emitter})
					body := map[string]any{"model": "cursor/auto", "messages": []map[string]any{textMessage("user", "hello")}}
					for key, value := range tc.body {
						body[key] = value
					}
					raw := contextRequestFixture(t, body, originalOnly)
					response, ok := handler.CallWithStatus(context.Background(), method, raw)
					require.Empty(t, client.Inputs())
					require.False(t, ok, string(response))
					var result envelope
					require.NoError(t, json.Unmarshal(response, &result))
					require.NotNil(t, result.Error)
					require.Equal(t, "context_length_exceeded", result.Error.Code)
					require.Equal(t, 400, result.Error.HTTPStatus)
					require.True(t, result.Error.RequestScoped)
					require.False(t, result.Error.Retryable)
				})
			}
		}
	}
}

func Test_Executor_context_limit_accepts_small_requests_and_labels_count_estimate(t *testing.T) {
	client := &recordingCursorClient{steps: []cursorRunStep{successfulTextStep("ok", "conversation", nil)}}
	handler := NewHandler(Dependencies{Cursor: client})
	raw := contextRequestFixture(t, map[string]any{"model": "cursor/auto", "messages": []map[string]any{textMessage("user", "hello 中")}}, false)
	response, ok := handler.CallWithStatus(context.Background(), "executor.execute", raw)
	require.True(t, ok, string(response))
	require.Len(t, client.Inputs(), 1)
	response, ok = handler.CallWithStatus(context.Background(), "executor.count_tokens", raw)
	require.True(t, ok, string(response))
	var result struct{ Result executorResponse }
	require.NoError(t, json.Unmarshal(response, &result))
	var count struct {
		Estimated bool   `json:"estimated"`
		Method    string `json:"count_method"`
		Limit     int64  `json:"client_context_limit"`
	}
	require.NoError(t, json.Unmarshal(result.Result.Payload, &count))
	require.True(t, count.Estimated)
	require.Equal(t, "conservative_utf8_bytes_v1", count.Method)
	require.EqualValues(t, 1_000_000, count.Limit)
}

func contextRequestFixture(t *testing.T, body map[string]any, originalOnly bool) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	request := executorRequest{Payload: payload, StreamID: "context-limit-test", StorageJSON: []byte(`{"type":"cursor-provider","access_token":"synthetic","refresh_token":"synthetic"}`)}
	if originalOnly {
		request.OriginalRequest, request.Payload = request.Payload, nil
	}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	return raw
}
