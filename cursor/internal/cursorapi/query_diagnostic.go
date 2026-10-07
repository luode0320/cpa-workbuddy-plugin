package cursorapi

import (
	"fmt"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
)

const queryTraceLimit = 4

type queryDiagnostic struct {
	recent  []string
	dropped uint64
}

func (diagnostic *queryDiagnostic) record(event cursorproto.ServerEvent, elapsed time.Duration) {
	if event.Query == nil {
		return
	}
	queryType := strings.TrimPrefix(event.Type, "interaction_query.")
	if event.Type == "interaction_query" {
		queryType = "none"
	}
	entry := fmt.Sprintf("at_ms=%d,type=%s,fields=%v", elapsed.Milliseconds(), queryType, event.Query.Fields)
	if event.Query.Truncated {
		entry += ",fields_truncated=true"
	}
	if len(diagnostic.recent) == queryTraceLimit {
		copy(diagnostic.recent, diagnostic.recent[1:])
		diagnostic.recent = diagnostic.recent[:queryTraceLimit-1]
		diagnostic.dropped++
	}
	diagnostic.recent = append(diagnostic.recent, entry)
}

func (diagnostic *queryDiagnostic) summary() string {
	if len(diagnostic.recent) == 0 {
		return ""
	}
	entries := make([]string, 0, len(diagnostic.recent))
	for index := len(diagnostic.recent) - 1; index >= 0; index-- {
		entries = append(entries, diagnostic.recent[index])
	}
	return fmt.Sprintf("%s; dropped=%d", strings.Join(entries, "; "), diagnostic.dropped)
}
