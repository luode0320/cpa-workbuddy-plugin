package cursorapi

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_QueryDiagnostic_retains_latest_four_queries_with_relative_timing(t *testing.T) {
	// Given
	var diagnostic queryDiagnostic
	for index := 0; index < 9; index++ {
		diagnostic.record(cursorproto.ServerEvent{Type: "interaction_query", Query: &cursorproto.QueryShape{}}, time.Duration(index)*time.Second)
	}
	// When
	text := diagnostic.summary()
	// Then
	require.Contains(t, text, "at_ms=8000,type=none,fields=[]")
	require.Contains(t, text, "at_ms=5000,type=none,fields=[]")
	require.NotContains(t, text, "at_ms=4000")
	require.Contains(t, text, "dropped=5")
	require.Less(t, len(text), 500)
}

func Test_Client_Run_timeout_includes_query_shape_without_payload(t *testing.T) {
	// Given
	const secret = "DIAGNOSTIC_CANARY_SECRET_QUERY_CONTENT"
	query := appendWireBytes(nil, 9, []byte(secret))
	body := newChunkBody(connectFrame(textServerMessage("partial")), connectFrame(appendWireBytes(nil, 7, query)))
	client := newTestClient(t, staticResponseTransport(body), Config{
		HeartbeatInterval: time.Hour, FirstDataTimeout: time.Second,
		FrameSilenceTimeout: time.Second, ProgressTimeout: 50 * time.Millisecond,
	})
	// When
	result, err := client.Run(context.Background(), validRunInput(), func(cursorproto.ServerEvent) error { return nil })
	// Then
	require.ErrorIs(t, err, ErrProgressTimeout)
	require.True(t, result.OutputExposed)
	require.ErrorContains(t, err, "Cursor query diag v1:")
	require.Regexp(t, `at_ms=\d+,type=none,fields=\[9\]`, err.Error())
	require.ErrorContains(t, err, "interaction_query=1")
	require.NotContains(t, err.Error(), secret)
	t.Log(err)
}

func Test_QueryDiagnostic_leaves_other_errors_unchanged(t *testing.T) {
	// Given
	state := streamState{eventCounts: map[string]int{"interaction_query": 1}}
	state.queries.record(cursorproto.ServerEvent{Type: "interaction_query", Query: &cursorproto.QueryShape{}}, time.Second)
	// When
	err := state.annotateProgressTimeout(context.Canceled)
	// Then
	require.Same(t, context.Canceled, err)
}

func Test_Client_Run_keeps_query_diagnostic_when_timeout_coincides_with_read_error(t *testing.T) {
	// Given
	frame := connectFrame(appendWireBytes(nil, 7, appendWireBytes(nil, 9, nil)))
	client := newTestClient(t, staticResponseTransport(&queryReadErrorBody{frame: frame}), Config{})
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	// When
	_, err := client.Run(ctx, validRunInput(), func(cursorproto.ServerEvent) error {
		cancel(ErrProgressTimeout)
		return nil
	})
	// Then
	require.ErrorIs(t, err, ErrProgressTimeout)
	require.ErrorContains(t, err, "Cursor query diag v1:")
	require.ErrorContains(t, err, "fields=[9]")
}

type queryReadErrorBody struct{ frame []byte }

func (body *queryReadErrorBody) Read(target []byte) (int, error) {
	return copy(target, body.frame), io.ErrUnexpectedEOF
}

func (*queryReadErrorBody) Close() error { return nil }
