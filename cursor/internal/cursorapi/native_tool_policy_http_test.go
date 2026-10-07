package cursorapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_Client_Run_hands_off_bash_after_HTTP2_native_shell_refusal(t *testing.T) {
	for _, catalogSize := range []int{1, 84} {
		t.Run(fmt.Sprintf("catalog_%d_tools", catalogSize), func(t *testing.T) {
			// Given
			input := validRunInput()
			for index := 1; index < catalogSize; index++ {
				input.Tools = append(input.Tools, cursorproto.ToolDefinition{
					Name: fmt.Sprintf("unrelated_%02d", index), Parameters: []byte(`{"type":"object"}`),
				})
			}
			input.Tools = append(input.Tools, cursorproto.ToolDefinition{
				Name: "bash", Parameters: []byte(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
			})
			replies := make(chan [][]byte, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 {
					t.Errorf("expected HTTP2, got %s", r.Proto)
					return
				}
				if _, err := readConnectPayloadRaw(r.Body); err != nil {
					t.Errorf("initial Run frame: %v", err)
					return
				}
				w.Header().Set("Content-Type", "application/connect+proto")
				if _, err := w.Write(connectFrame(nativeShellExecMessage(51, "native-shell", 14, "pwd", "/downstream/workspace"))); err != nil {
					t.Errorf("native shell write: %v", err)
					return
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("native shell flush: %v", err)
					return
				}
				var received [][]byte
				for range 5 {
					reply, err := readConnectPayloadRaw(r.Body)
					if err != nil {
						t.Errorf("native shell reply: %v", err)
						return
					}
					received = append(received, reply)
				}
				replies <- received
				if !bytes.Contains(received[3], []byte("`bash`")) {
					return
				}
				if _, err := w.Write(connectFrame(mcpExecServerMessage(52, "client-bash", "bash", "command", "pwd"))); err != nil {
					t.Errorf("client tool write: %v", err)
				}
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			t.Cleanup(server.Close)
			client, err := NewClient(Config{
				BaseURL: server.URL, HTTPClient: server.Client(), HeartbeatInterval: time.Hour,
				FirstDataTimeout: time.Second, FrameSilenceTimeout: time.Second,
				ProgressTimeout: time.Second, OverallTimeout: 3 * time.Second,
			})
			require.NoError(t, err)
			var calls []cursorproto.ServerEvent

			// When
			result, runErr := client.Run(context.Background(), input, func(event cursorproto.ServerEvent) error {
				if event.Kind == cursorproto.EventToolCall {
					calls = append(calls, event)
				}
				return nil
			})

			// Then
			select {
			case received := <-replies:
				requireNativeShellStreamReplies(t, received, 51, "native-shell")
				exec := requireWireBytes(t, received[3], 2)
				failure := requireWireBytes(t, requireWireBytes(t, exec, 2), 2)
				policy := string(requireWireBytes(t, failure, 6))
				require.Equal(t, []string{"`bash`"}, regexp.MustCompile("`[^`]+`").FindAllString(policy, -1))
			default:
				t.Fatalf("missing native shell replies: %v", runErr)
			}
			require.NoError(t, runErr)
			require.True(t, result.ToolExposed)
			require.Len(t, calls, 1)
			require.Equal(t, "client-bash", calls[0].ID)
			require.Equal(t, "bash", calls[0].Name)
			require.JSONEq(t, `{"command":"pwd"}`, calls[0].Arguments)
		})
	}
}

func Test_Client_Run_marks_native_refusal_before_checkpoint_timeout(t *testing.T) {
	for _, test := range []struct {
		name    string
		request []byte
		replies int
	}{
		{"read", nativeReadOnlyExecMessage(41, "native-read", 7, "/downstream/workspace/probe"), 1},
		{"grep", nativeReadOnlyExecMessage(42, "native-grep", 5, "/downstream/workspace"), 1},
		{"shell", nativeShellExecMessage(43, "native-shell", 2, "pwd", "/downstream/workspace"), 1},
		{"shell_stream", nativeShellExecMessage(44, "native-stream", 14, "pwd", "/downstream/workspace"), 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			replied := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 {
					t.Errorf("expected HTTP2, got %s", r.Proto)
					return
				}
				if _, err := readConnectPayloadRaw(r.Body); err != nil {
					t.Errorf("initial Run frame: %v", err)
					return
				}
				w.Header().Set("Content-Type", "application/connect+proto")
				if _, err := w.Write(connectFrame(test.request)); err != nil {
					t.Errorf("native request write: %v", err)
					return
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("native request flush: %v", err)
					return
				}
				for range test.replies {
					if _, err := readConnectPayloadRaw(r.Body); err != nil {
						t.Errorf("native reply: %v", err)
						return
					}
				}
				close(replied)
				<-r.Context().Done()
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			t.Cleanup(server.Close)
			client, err := NewClient(Config{
				BaseURL: server.URL, HTTPClient: server.Client(), HeartbeatInterval: time.Hour,
				FirstDataTimeout: time.Second, FrameSilenceTimeout: time.Second,
				ProgressTimeout: 200 * time.Millisecond, OverallTimeout: 3 * time.Second,
			})
			require.NoError(t, err)
			input := validRunInput()
			input.Mode = cursorproto.CheckpointSuffix
			input.Checkpoint = []byte{0x22, 0x01, 0x78}

			// When
			result, err := client.Run(context.Background(), input, func(cursorproto.ServerEvent) error { return nil })

			// Then
			require.ErrorIs(t, err, ErrProgressTimeout)
			select {
			case <-replied:
			default:
				t.Fatal("native refusal did not reach the HTTP2 peer")
			}
			require.False(t, result.OutputExposed)
			require.False(t, result.ToolExposed)
			require.True(t, result.InteractionResponded)
		})
	}
}
