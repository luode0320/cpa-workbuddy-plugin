package cursorapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_Client_Run_completes_HTTP2_turn_waiting_for_web_search_refusal(t *testing.T) {
	const secret = "PRIVATE_SEARCH_CANARY"
	query := appendWireBytes([]byte{8, 42}, 2, appendWireBytes(nil, 1, appendWireString(nil, 1, secret)))
	replies := make(chan []byte, 1)
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
		frames := append(connectFrame(textServerMessage("before ")), connectFrame(appendWireBytes(nil, 7, query))...)
		if _, err := w.Write(frames); err != nil {
			t.Errorf("query write: %v", err)
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("query flush: %v", err)
			return
		}
		reply, err := readConnectPayloadRaw(r.Body)
		if err != nil {
			replies <- nil
			return
		}
		replies <- reply
		frames = append(connectFrame(textServerMessage("after")), connectFrame(turnEndedServerMessage())...)
		if _, err := w.Write(frames); err != nil {
			t.Errorf("completion write: %v", err)
		}
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
	var text string
	result, err := client.Run(context.Background(), validRunInput(), func(event cursorproto.ServerEvent) error {
		if event.Kind == cursorproto.EventText {
			text += event.Text
		}
		return nil
	})
	require.NoError(t, err, "an unanswered web-search query stalls an otherwise live HTTP2 stream")
	require.Equal(t, "before after", text)
	require.True(t, result.OutputExposed)
	require.True(t, result.InteractionResponded)
	require.False(t, result.ToolExposed)
	select {
	case reply := <-replies:
		require.NotContains(t, string(reply), secret)
		response := requireWireBytes(t, reply, 6)
		require.EqualValues(t, 42, requireWireVarint(t, response, 1))
		web := requireWireBytes(t, response, 2)
		rejected := requireWireBytes(t, web, 2)
		require.Equal(t, appendWireBytes(nil, 2, rejected), web, "rejection must not include approval")
		require.NotEmpty(t, requireWireBytes(t, rejected, 1))
	case <-time.After(time.Second):
		t.Fatal("server did not observe an interaction response")
	}
}
