package cursorapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
	"github.com/stretchr/testify/require"
)

func Test_Client_Run_hands_off_tool_after_HTTP2_catalog_query(t *testing.T) {
	// Given
	query := appendWireBytes([]byte{8, 42}, 36, appendWireString(nil, 1, "opencodex-responses"))
	query = appendWireString(query, 15, "catalog-exec")
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
		if _, err := w.Write(connectFrame(appendWireBytes(nil, 2, query))); err != nil {
			t.Errorf("query write: %v", err)
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("query flush: %v", err)
			return
		}
		reply, err := readConnectPayloadRaw(r.Body)
		if err != nil {
			return
		}
		replies <- reply
		if !bytes.Contains(reply, []byte("cpa_smoke_echo")) {
			<-r.Context().Done()
			return
		}
		if _, err := w.Write(connectFrame(mcpExecServerMessage(43, "call-echo", "cpa_smoke_echo", "value", "OK"))); err != nil {
			t.Errorf("tool write: %v", err)
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
	input := validRunInput()
	input.Tools = []cursorproto.ToolDefinition{{Name: "cpa_smoke_echo", Parameters: []byte(`{"type":"object","properties":{"value":{"type":"string"}}}`)}}
	var calls []cursorproto.ServerEvent
	// When
	result, err := client.Run(context.Background(), input, func(event cursorproto.ServerEvent) error {
		if event.Kind == cursorproto.EventToolCall {
			calls = append(calls, event)
		}
		return nil
	})
	// Then
	require.NoError(t, err, "schema lookup must complete before the upstream can hand off the tool")
	require.True(t, result.ToolExposed)
	require.Len(t, calls, 1)
	require.Equal(t, "cpa_smoke_echo", calls[0].Name)
	require.JSONEq(t, `{"value":"OK"}`, calls[0].Arguments)
	select {
	case reply := <-replies:
		exec := requireWireBytes(t, reply, 2)
		require.EqualValues(t, 42, requireWireVarint(t, exec, 1))
		require.Equal(t, "catalog-exec", string(requireWireBytes(t, exec, 15)))
		state := requireWireBytes(t, exec, 36)
		success := requireWireBytes(t, state, 1)
		catalog := requireWireBytes(t, success, 1)
		require.Equal(t, "opencodex-responses", string(requireWireBytes(t, catalog, 2)))
		tool := requireWireBytes(t, catalog, 5)
		require.Equal(t, "cpa_smoke_echo", string(requireWireBytes(t, tool, 5)))
		require.NotEmpty(t, requireWireBytes(t, tool, 3))
	default:
		t.Fatal("missing correlated catalog reply")
	}
}
