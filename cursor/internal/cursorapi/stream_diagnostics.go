package cursorapi

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
)

func (state *streamState) recordEvent(event cursorproto.ServerEvent) {
	eventType := event.Type
	if eventType == "" {
		eventType = string(event.Kind)
	}
	if state.eventCounts == nil {
		state.eventCounts = make(map[string]int)
	}
	state.eventCounts[eventType]++
	state.trace.record(eventTraceEntry{elapsed: time.Since(state.started), typeName: eventType, progress: eventMakesProgress(event.Kind)})
}

func (state *streamState) annotateProgressTimeout(err error) error {
	if !errors.Is(err, ErrProgressTimeout) || len(state.eventCounts) == 0 {
		return err
	}
	if diagnostic := state.queries.summary(); diagnostic != "" {
		err = fmt.Errorf("%w (Cursor query diag v1: %s)", err, diagnostic)
	}
	if trace := state.trace.summary(); trace != "" {
		err = fmt.Errorf("%w (Cursor event trace v1: %s)", err, trace)
	}
	types := make([]string, 0, len(state.eventCounts))
	for eventType := range state.eventCounts {
		types = append(types, eventType)
	}
	sort.Strings(types)
	counts := make([]string, 0, len(types))
	for _, eventType := range types {
		counts = append(counts, fmt.Sprintf("%s=%d", eventType, state.eventCounts[eventType]))
	}
	return fmt.Errorf("%w (Cursor event counts: %s)", err, strings.Join(counts, ", "))
}

func eventMakesProgress(kind cursorproto.EventKind) bool {
	switch kind {
	case cursorproto.EventText, cursorproto.EventThinking, cursorproto.EventTokens, cursorproto.EventToolCall, cursorproto.EventImage, cursorproto.EventDone:
		return true
	case cursorproto.EventIgnored, cursorproto.EventCheckpoint:
		return false
	default:
		return false
	}
}

func eventExposesOutput(kind cursorproto.EventKind) bool {
	switch kind {
	case cursorproto.EventText, cursorproto.EventThinking, cursorproto.EventTokens, cursorproto.EventImage, cursorproto.EventDone:
		return true
	case cursorproto.EventIgnored, cursorproto.EventToolCall, cursorproto.EventCheckpoint:
		return false
	default:
		return false
	}
}
