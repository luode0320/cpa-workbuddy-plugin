package adapter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/claude"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/gemini"
	"github.com/gin-gonic/gin"
)

func TestHistoryKeysNilTurnsProducesValidPrefix(t *testing.T) {
	keysNil := historyKeys("sys", ToolBridge{}, nil, historyTurn{Role: "assistant", Text: "ok"})
	keysEmpty := historyKeys("sys", ToolBridge{}, []historyTurn{}, historyTurn{Role: "assistant", Text: "ok"})
	if len(keysNil) != 1 || len(keysEmpty) != 1 || keysNil[0] != keysEmpty[0] {
		t.Fatalf("expected nil turns and empty turns to hash identically, got %v vs %v", keysNil, keysEmpty)
	}
}

func TestContinuedHistoryAndClaudePrefillPreservation(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	prevContinue := continueSession.Swap(true)
	defer continueSession.Store(prevContinue)
	state := gemini.NewConversationStateFrom(gemini.ConversationSnapshot{
		CID:  "c_123",
		RID:  "r_123",
		RCID: "rc_123",
	})
	turnsBefore := []historyTurn{{Role: "user", Text: "hello"}}
	reply := historyTurn{Role: "assistant", Text: "world"}
	key := historyKeys("be concise", ToolBridge{}, turnsBefore, reply)[0]
	conversations.put(state, "acc1", 10, key)

	// Trailing assistant prefill must still match the completed prior assistant turn.
	turnsWithPrefill := []historyTurn{
		{Role: "user", Text: "hello"},
		{Role: "assistant", Text: "world"},
		{Role: "user", Text: "output JSON"},
		{Role: "assistant", Text: "{"},
	}
	if got := continuedHistoryKey("be concise", ToolBridge{}, turnsWithPrefill); got != key {
		t.Fatalf("expected continuedHistoryKey to match %q despite trailing assistant prefill, got %q", key, got)
	}

	// buildClaudePrompt with continuation=true must keep both the new user message and the assistant prefill.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	req := claude.ClaudeRequest{
		Model: "gemini-4-carbon",
		Messages: []claude.Message{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
			{Role: "assistant", Content: json.RawMessage(`"world"`)},
			{Role: "user", Content: json.RawMessage(`"output JSON"`)},
			{Role: "assistant", Content: json.RawMessage(`"{"`)},
		},
	}
	prompt, _, err := buildClaudePrompt(c, nil, req, true, ToolBridge{})
	if err != nil {
		t.Fatalf("buildClaudePrompt failed: %v", err)
	}
	if !strings.Contains(prompt, "output JSON") || !strings.Contains(prompt, "{") {
		t.Fatalf("expected continuation prompt to retain new user turn and assistant prefill, got %q", prompt)
	}
	if strings.Contains(prompt, "world") {
		t.Fatalf("expected continuation prompt to omit already-sent historical assistant reply, got %q", prompt)
	}
}

func TestStopCutterMultiByteUTF8AcrossChunks(t *testing.T) {
	cutter := newStopCutter([]string{"结束标记"})
	if cutter == nil {
		t.Fatal("expected non-nil stopCutter")
	}
	// Snapshot 1 ends with first rune "结" of "结束标记": must hold "结" and only emit "正文内容".
	delta1, matched1 := cutter.cut("正文内容结")
	if matched1 || delta1 != "正文内容" {
		t.Fatalf("chunk 1: got (%q, %v), want (%q, false)", delta1, matched1, "正文内容")
	}
	// Snapshot 2 turns out to be "结局" (not the stop sequence): must release "结局".
	delta2, matched2 := cutter.cut("正文内容结局继续")
	if matched2 || delta2 != "结局继续" {
		t.Fatalf("chunk 2: got (%q, %v), want (%q, false)", delta2, matched2, "结局继续")
	}
	// Snapshot 3 hits full stop sequence "结束标记": must cut cleanly before it.
	delta3, matched3 := cutter.cut("正文内容结局继续完美结束标记后面丢弃")
	if !matched3 || delta3 != "完美" {
		t.Fatalf("chunk 3: got (%q, %v), want (%q, true)", delta3, matched3, "完美")
	}
}

func TestFakeStreamProjectionBuffersThoughtAndText(t *testing.T) {
	prev := fakeStream.Swap(true)
	defer fakeStream.Store(prev)

	proj := newStreamProjection(ToolBridge{}, true)
	var emitted []gemini.Event
	emit := func(ev gemini.Event) error {
		emitted = append(emitted, ev)
		return nil
	}
	if err := proj.project(gemini.Event{Kind: gemini.EventThought, Delta: "thinking...", Operation: gemini.SnapshotAppend}, emit); err != nil {
		t.Fatalf("project thought failed: %v", err)
	}
	if err := proj.project(gemini.Event{Kind: gemini.EventText, Delta: "hello", Operation: gemini.SnapshotAppend}, emit); err != nil {
		t.Fatalf("project text failed: %v", err)
	}
	if len(emitted) != 0 {
		t.Fatalf("expected fake_stream=true to buffer live thought/text deltas, got %+v", emitted)
	}
}
