package cursorapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func Test_Client_Run_query_diagnostics_preserve_HTTP2_outcomes_and_replies(t *testing.T) {
	cases := []struct {
		name  string
		field protowire.Number
		ended bool
	}{
		{"unknown_timeout", 9, false}, {"unknown_completion", 9, true},
		{"web_timeout", 2, false}, {"web_completion", 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			const secret = "DIAGNOSTIC_CANARY_SECRET_QUERY_CONTENT"
			query := protowire.AppendTag(nil, 1, protowire.VarintType)
			query = protowire.AppendVarint(query, 987654321)
			query = appendWireBytes(query, tc.field, appendWireBytes(nil, 100, []byte(secret)))
			frames := append(connectFrame(textServerMessage("partial")), connectFrame(appendWireBytes(nil, 7, query))...)
			if tc.ended && tc.field != 2 {
				frames = append(frames, connectFrame(turnEndedServerMessage())...)
			}
			replies := make(chan int, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 || r.URL.Path != "/agent.v1.AgentService/Run" {
					t.Errorf("unexpected transport: %s %s", r.Proto, r.URL.Path)
					return
				}
				if _, err := readConnectPayloadRaw(r.Body); err != nil {
					t.Errorf("read initial frame: %v", err)
					return
				}
				w.Header().Set("Content-Type", "application/connect+proto")
				if _, err := w.Write(frames); err != nil {
					t.Errorf("write response: %v", err)
					return
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("flush response: %v", err)
					return
				}
				count := 0
				if tc.field == 2 {
					if _, err := readConnectPayloadRaw(r.Body); err != nil {
						replies <- count
						return
					}
					count++
					if tc.ended {
						if _, err := w.Write(connectFrame(turnEndedServerMessage())); err != nil {
							t.Errorf("write turn end: %v", err)
							return
						}
						if err := http.NewResponseController(w).Flush(); err != nil {
							t.Errorf("flush turn end: %v", err)
							return
						}
					}
				}
				for {
					if _, err := readConnectPayloadRaw(r.Body); err != nil {
						replies <- count
						return
					}
					count++
				}
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			client, err := NewClient(Config{
				BaseURL: server.URL, HTTPClient: server.Client(), HeartbeatInterval: time.Hour,
				FirstDataTimeout: time.Second, FrameSilenceTimeout: time.Second,
				ProgressTimeout: 150 * time.Millisecond, OverallTimeout: 3 * time.Second,
				TrailingDrainTimeout: time.Millisecond,
			})
			require.NoError(t, err)
			var text string
			// When
			result, err := client.Run(context.Background(), validRunInput(), func(event cursorproto.ServerEvent) error {
				if event.Kind == cursorproto.EventText {
					text += event.Text
				}
				return nil
			})
			// Then
			require.Equal(t, "partial", text)
			require.True(t, result.OutputExposed)
			require.False(t, result.ToolExposed)
			if tc.ended {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrProgressTimeout)
				require.ErrorContains(t, err, "Cursor query diag v1:")
				require.NotContains(t, err.Error(), secret)
				require.NotContains(t, err.Error(), "987654321")
				t.Log(err)
			}
			select {
			case count := <-replies:
				if tc.field == 2 {
					require.Equal(t, 1, count, "known web query gets exactly one refusal")
				} else {
					require.Zero(t, count, "diagnostics must not answer unknown queries")
				}
			case <-time.After(time.Second):
				t.Fatal("HTTP/2 stream was not closed")
			}
		})
	}
}
