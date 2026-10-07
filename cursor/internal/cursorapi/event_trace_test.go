package cursorapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"

	"github.com/stretchr/testify/require"
)

func Test_ProgressTimeout_keeps_bounded_ordered_event_types_without_content(t *testing.T) {
	// Given
	state := streamState{started: time.Now()}
	for i := 0; i < 40; i++ {
		state.recordEvent(cursorproto.ServerEvent{
			Kind: cursorproto.EventIgnored, Type: fmt.Sprintf("wire_event_%02d", i),
			Text: "SECRET_TEXT", Arguments: "SECRET_ARGUMENTS", ID: "SECRET_ID",
		})
	}
	state.recordEvent(cursorproto.ServerEvent{Kind: cursorproto.EventIgnored, Type: "interaction_update.summary_started"})
	state.recordEvent(cursorproto.ServerEvent{Kind: cursorproto.EventIgnored, Type: "interaction_update.heartbeat"})
	// When
	err := state.annotateProgressTimeout(ErrProgressTimeout)
	// Then
	require.ErrorIs(t, err, ErrProgressTimeout)
	parts := strings.SplitN(err.Error(), "Cursor event trace v1: ", 2)
	require.Len(t, parts, 2)
	trace := strings.SplitN(parts[1], ")", 2)[0]
	require.Equal(t, 16, strings.Count(trace, "at_ms="))
	require.NotContains(t, trace, "wire_event_00")
	require.Contains(t, trace, "dropped=26")
	require.Less(t, strings.Index(trace, "summary_started"), strings.Index(trace, "heartbeat"))
	require.NotContains(t, err.Error(), "SECRET_")
}

func Test_ProgressTimeout_trace_distinguishes_output_from_heartbeat(t *testing.T) {
	// Given
	state := streamState{started: time.Now()}
	state.recordEvent(cursorproto.ServerEvent{Kind: cursorproto.EventText, Type: "TextDeltaUpdate", Text: "PRIVATE"})
	state.recordEvent(cursorproto.ServerEvent{Kind: cursorproto.EventIgnored, Type: "interaction_update.heartbeat"})
	// When
	err := state.annotateProgressTimeout(ErrProgressTimeout)
	// Then
	require.ErrorContains(t, err, "type=TextDeltaUpdate,progress=true")
	require.ErrorContains(t, err, "type=interaction_update.heartbeat,progress=false")
	require.NotContains(t, err.Error(), "PRIVATE")
}
