package cursorapi

import (
	"fmt"
	"strings"
	"time"
)

const eventTraceLimit = 16

type eventTraceEntry struct {
	elapsed  time.Duration
	typeName string
	progress bool
}

type eventTrace struct {
	recent  []eventTraceEntry
	dropped uint64
}

func (trace *eventTrace) record(entry eventTraceEntry) {
	if len(trace.recent) == eventTraceLimit {
		copy(trace.recent, trace.recent[1:])
		trace.recent = trace.recent[:eventTraceLimit-1]
		trace.dropped++
	}
	trace.recent = append(trace.recent, entry)
}

func (trace *eventTrace) summary() string {
	if len(trace.recent) == 0 {
		return ""
	}
	entries := make([]string, 0, len(trace.recent))
	for _, entry := range trace.recent {
		entries = append(entries, fmt.Sprintf("at_ms=%d,type=%s,progress=%t", entry.elapsed.Milliseconds(), entry.typeName, entry.progress))
	}
	return fmt.Sprintf("%s; dropped=%d", strings.Join(entries, "; "), trace.dropped)
}
