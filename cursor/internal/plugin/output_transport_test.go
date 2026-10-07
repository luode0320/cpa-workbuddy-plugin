package plugin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func Test_Output_budget_closes_real_upstream_HTTP2_stream(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
	}{
		{"overrun", []string{"abcdef"}},
		{"exact", []string{"abcd"}},
		{"split_exact", []string{"ab", "cd"}},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				testOutputTransportCancellation(t, tc.chunks, stream)
			})
		}
	}
}

func testOutputTransportCancellation(t *testing.T, chunks []string, stream bool) {
	t.Helper()
	cancelled := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		header := make([]byte, 5)
		_, err := io.ReadFull(request.Body, header)
		require.NoError(t, err)
		_, err = io.CopyN(io.Discard, request.Body, int64(binary.BigEndian.Uint32(header[1:])))
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/connect+proto")
		for _, chunk := range chunks {
			text := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), chunk)
			update := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), text)
			event := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), update)
			_, err = w.Write(cursorproto.EncodeConnectFrame(event))
			require.NoError(t, err)
			w.(http.Flusher).Flush()
		}
		<-request.Context().Done()
		close(cancelled)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client, err := cursorapi.NewClient(cursorapi.Config{BaseURL: server.URL, HTTPClient: server.Client()})
	require.NoError(t, err)
	emitter := &captureEmitter{done: make(chan struct{})}
	handler := NewHandler(Dependencies{Cursor: client, Emitter: emitter})
	request := contextRequestFixture(t, map[string]any{"model": "cursor/auto", "messages": []map[string]any{textMessage("user", "hello")}, "max_tokens": 4}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	method := "executor.execute"
	if stream {
		method = "executor.execute_stream"
	}
	raw, ok := handler.CallWithStatus(ctx, method, request)

	require.True(t, ok, string(raw))
	if stream {
		select {
		case <-emitter.done:
		case <-ctx.Done():
			t.Fatal("limited SSE stream did not close")
		}
		require.NoError(t, emitter.closeError)
		var content strings.Builder
		for _, payload := range emitter.payloads[:len(emitter.payloads)-1] {
			var chunk struct {
				Choices []struct {
					Delta        struct{ Content string }
					FinishReason *string `json:"finish_reason"`
				}
			}
			require.NoError(t, json.Unmarshal(payload, &chunk))
			content.WriteString(chunk.Choices[0].Delta.Content)
			if chunk.Choices[0].FinishReason != nil {
				require.Equal(t, "length", *chunk.Choices[0].FinishReason)
			}
		}
		require.Equal(t, "abcd", content.String())
		require.Contains(t, string(emitter.payloads[len(emitter.payloads)-2]), `"finish_reason":"length"`)
		require.Equal(t, "[DONE]", string(emitter.payloads[len(emitter.payloads)-1]))
	} else {
		var result struct{ Result executorResponse }
		require.NoError(t, json.Unmarshal(raw, &result))
		var response struct {
			Choices []struct {
				Message      struct{ Content string }
				FinishReason string `json:"finish_reason"`
			}
		}
		require.NoError(t, json.Unmarshal(result.Result.Payload, &response))
		require.Equal(t, "abcd", response.Choices[0].Message.Content)
		require.Equal(t, "length", response.Choices[0].FinishReason)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("upstream stream was not cancelled after output limit")
	}
}

func Test_Output_truncation_does_not_commit_a_divergent_checkpoint(t *testing.T) {
	client := &recordingCursorClient{steps: []cursorRunStep{
		successfulTextStep("seed", "conversation", []byte("original-checkpoint")),
		successfulTextStep("abcdef", "conversation", []byte("truncated-checkpoint")),
		successfulTextStep("next", "conversation", []byte("next-checkpoint")),
	}}
	handler := NewHandler(Dependencies{Cursor: client})
	seed := []map[string]any{textMessage("user", "hello")}
	_, err := handler.execute(context.Background(), executorFixture(t, "same", "account", "auth", "auto", "", seed))
	require.NoError(t, err)
	branch := append(seed, textMessage("assistant", "seed"), textMessage("user", "branch"))
	var request executorRequest
	require.NoError(t, json.Unmarshal(executorFixture(t, "same", "account", "auth", "auto", "", branch), &request))
	var body map[string]any
	require.NoError(t, json.Unmarshal(request.Payload, &body))
	body["max_tokens"] = 4
	request.Payload, err = json.Marshal(body)
	require.NoError(t, err)
	raw, err := json.Marshal(request)
	require.NoError(t, err)

	_, err = handler.execute(context.Background(), raw)

	require.NoError(t, err)
	_, err = handler.execute(context.Background(), executorFixture(t, "same", "account", "auth", "auto", "", branch))
	require.NoError(t, err)
	inputs := client.Inputs()
	require.Len(t, inputs, 3)
	require.Equal(t, cursorproto.CheckpointSuffix, inputs[2].Mode)
	require.Equal(t, "original-checkpoint", string(inputs[2].Checkpoint))
}
