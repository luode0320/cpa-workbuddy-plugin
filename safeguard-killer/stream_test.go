package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func streamTestFrame(t *testing.T, event map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal SSE event: %v", err)
	}
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event["type"], raw))
}

func streamTestFrames(t *testing.T, events ...map[string]any) []byte {
	t.Helper()
	var body []byte
	for _, event := range events {
		body = append(body, streamTestFrame(t, event)...)
	}
	return body
}

func streamTestStart(index int, block map[string]any) map[string]any {
	return map[string]any{"type": "content_block_start", "index": index, "content_block": block}
}

func streamTestDelta(index int, delta map[string]any) map[string]any {
	return map[string]any{"type": "content_block_delta", "index": index, "delta": delta}
}

func streamTestStop(index int) map[string]any {
	return map[string]any{"type": "content_block_stop", "index": index}
}

func streamTestText(index int, initial, delta string) []map[string]any {
	return []map[string]any{
		streamTestStart(index, map[string]any{"type": "text", "text": initial}),
		streamTestDelta(index, map[string]any{"type": "text_delta", "text": delta}),
		streamTestStop(index),
	}
}

func streamTestThinking(index int, thinking string) []map[string]any {
	return []map[string]any{
		streamTestStart(index, map[string]any{"type": "thinking", "thinking": ""}),
		streamTestDelta(index, map[string]any{"type": "thinking_delta", "thinking": thinking}),
		streamTestStop(index),
	}
}

func streamTestNormalize(t *testing.T, requestID string, body []byte) []byte {
	t.Helper()
	out, err := normalizeClaudeStream(requestID, body)
	if err != nil {
		t.Fatalf("normalizeClaudeStream(%q): %v", requestID, err)
	}
	return out
}

func streamTestParse(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	if normalized != "" && !strings.HasSuffix(normalized, "\n\n") {
		t.Fatalf("incomplete output SSE frame: %q", body)
	}
	var events []map[string]any
	for _, frame := range strings.Split(normalized, "\n\n") {
		if strings.TrimSpace(frame) == "" {
			continue
		}
		var data []string
		var eventName string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "event:") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if len(data) == 0 {
			t.Fatalf("output SSE frame has no data: %q", frame)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &event); err != nil {
			t.Fatalf("decode SSE frame %q: %v", frame, err)
		}
		if eventName != "" && eventName != event["type"] {
			t.Fatalf("SSE event name %q disagrees with type %v", eventName, event["type"])
		}
		events = append(events, event)
	}
	return events
}

type streamTestBlock struct {
	Start    map[string]any
	Deltas   []map[string]any
	Text     string
	Thinking string
}

func streamTestBlocks(t *testing.T, events []map[string]any) []streamTestBlock {
	t.Helper()
	var blocks []streamTestBlock
	open := -1
	for _, event := range events {
		kind := event["type"]
		if kind != "content_block_start" && kind != "content_block_delta" && kind != "content_block_stop" {
			continue
		}
		index, ok := event["index"].(float64)
		if !ok || index != float64(int(index)) {
			t.Fatalf("invalid block index: %v", event)
		}
		i := int(index)
		switch kind {
		case "content_block_start":
			if open != -1 || i != len(blocks) {
				t.Fatalf("noncontiguous or overlapping block: %v; open=%d, blocks=%d", event, open, len(blocks))
			}
			start := event["content_block"].(map[string]any)
			block := streamTestBlock{Start: start}
			switch start["type"] {
			case "text":
				block.Text = start["text"].(string)
			case "thinking":
				block.Thinking = start["thinking"].(string)
			}
			blocks = append(blocks, block)
			open = i
		case "content_block_delta":
			if open != i || open == -1 {
				t.Fatalf("delta outside its block: %v; open=%d", event, open)
			}
			delta := event["delta"].(map[string]any)
			blocks[i].Deltas = append(blocks[i].Deltas, delta)
			switch delta["type"] {
			case "text_delta":
				blocks[i].Text += delta["text"].(string)
			case "thinking_delta":
				blocks[i].Thinking += delta["thinking"].(string)
			}
		case "content_block_stop":
			if open != i || open == -1 {
				t.Fatalf("stop outside its block: %v; open=%d", event, open)
			}
			open = -1
		}
	}
	if open != -1 {
		t.Fatalf("output left block %d open", open)
	}
	return blocks
}

func streamTestWantBlocks(t *testing.T, blocks []streamTestBlock, types []string) {
	t.Helper()
	var got []string
	for _, block := range blocks {
		got = append(got, block.Start["type"].(string))
	}
	if !reflect.DeepEqual(got, types) {
		t.Fatalf("block types = %v, want %v", got, types)
	}
}

func TestNormalizeClaudeStreamReordersLateThinkingBeforeCombinedTextAndTool(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	initial := streamTestFrames(t, streamTestThinking(0, "Let me check")...)
	out := streamTestNormalize(t, id, initial)
	first := streamTestBlocks(t, streamTestParse(t, out))
	streamTestWantBlocks(t, first, []string{"thinking"})
	if first[0].Thinking != "Let me check" {
		t.Fatalf("initial thinking = %q", first[0].Thinking)
	}

	var buffered []map[string]any
	buffered = append(buffered, streamTestText(1, "", "You")...)
	buffered = append(buffered, streamTestThinking(2, ".")...)
	buffered = append(buffered, streamTestText(3, "", " already have the answer.")...)
	if got := streamTestNormalize(t, id, streamTestFrames(t, buffered...)); len(got) != 0 {
		t.Fatalf("text/thinking emitted before tool boundary: %s", got)
	}

	tool := map[string]any{"type": "tool_use", "id": "toolu_exact", "name": "lookup", "input": map[string]any{"seed": "kept"}}
	toolStart := streamTestNormalize(t, id, streamTestFrames(t, streamTestStart(4, tool)))
	if len(toolStart) == 0 {
		t.Fatal("tool start did not flush buffered segment")
	}
	out = append(out, toolStart...)
	args := []string{`{"query":`, `"answer","limit":2}`}
	var tail []map[string]any
	for _, arg := range args {
		tail = append(tail, streamTestDelta(4, map[string]any{"type": "input_json_delta", "partial_json": arg}))
	}
	tail = append(tail, streamTestStop(4), map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 42}}, map[string]any{"type": "message_stop"})
	out = append(out, streamTestNormalize(t, id, streamTestFrames(t, tail...))...)
	events := streamTestParse(t, out)
	blocks := streamTestBlocks(t, events)
	streamTestWantBlocks(t, blocks, []string{"thinking", "thinking", "text", "tool_use"})
	if blocks[1].Thinking != "." || blocks[2].Text != "You already have the answer." {
		t.Fatalf("reordered content = %+v", blocks)
	}
	if !reflect.DeepEqual(blocks[3].Start, tool) {
		t.Fatalf("tool fields = %v, want %v", blocks[3].Start, tool)
	}
	if len(blocks[3].Deltas) != len(args) {
		t.Fatalf("tool deltas = %v", blocks[3].Deltas)
	}
	for i, delta := range blocks[3].Deltas {
		want := map[string]any{"type": "input_json_delta", "partial_json": args[i]}
		if !reflect.DeepEqual(delta, want) {
			t.Fatalf("tool delta %d = %v, want %v", i, delta, want)
		}
	}
	last := events[len(events)-2]
	if last["type"] != "message_delta" || last["delta"].(map[string]any)["stop_reason"] != "tool_use" || last["usage"].(map[string]any)["output_tokens"] != float64(42) {
		t.Fatalf("message delta changed: %v", last)
	}
}

func TestNormalizeClaudeStreamInitialThinkingStreamsBeforeBlockStop(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	out := streamTestNormalize(t, id, streamTestFrames(t,
		streamTestStart(0, map[string]any{"type": "thinking", "thinking": ""}),
	))
	chunk := streamTestNormalize(t, id, streamTestFrames(t,
		streamTestDelta(0, map[string]any{"type": "thinking_delta", "thinking": "still thinking"}),
	))
	found := false
	for _, event := range streamTestParse(t, chunk) {
		if event["type"] == "content_block_delta" {
			delta := event["delta"].(map[string]any)
			if delta["type"] == "thinking_delta" && delta["thinking"] == "still thinking" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("initial thinking delta was buffered until stop: %s", chunk)
	}
	out = append(out, chunk...)
	out = append(out, streamTestNormalize(t, id, streamTestFrames(t,
		streamTestStop(0), map[string]any{"type": "message_stop"},
	))...)
	blocks := streamTestBlocks(t, streamTestParse(t, out))
	streamTestWantBlocks(t, blocks, []string{"thinking"})
	if blocks[0].Thinking != "still thinking" {
		t.Fatalf("initial thinking = %q", blocks[0].Thinking)
	}
}

func TestNormalizeClaudeStreamFlushesAllTextAtBoundaries(t *testing.T) {
	for _, boundary := range []map[string]any{
		{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 7}},
		{"type": "message_stop"},
		{"type": "error", "error": map[string]any{"type": "overloaded_error", "message": "busy"}},
	} {
		t.Run(boundary["type"].(string), func(t *testing.T) {
			id := t.Name()
			defer cleanupClaudeStream(id)
			var input []map[string]any
			input = append(input, streamTestText(0, "start ", "one")...)
			input = append(input, streamTestThinking(1, "late")...)
			input = append(input, streamTestText(2, " + ", "two 🧪")...)
			if got := streamTestNormalize(t, id, streamTestFrames(t, input...)); len(got) != 0 {
				t.Fatalf("segment emitted before boundary: %s", got)
			}
			out := streamTestNormalize(t, id, streamTestFrames(t, boundary))
			events := streamTestParse(t, out)
			blocks := streamTestBlocks(t, events)
			streamTestWantBlocks(t, blocks, []string{"thinking", "text"})
			if blocks[0].Thinking != "late" || blocks[1].Text != "start one + two 🧪" {
				t.Fatalf("content lost at boundary: %+v", blocks)
			}
			wantRaw, err := json.Marshal(boundary)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal(wantRaw, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(events[len(events)-1], want) {
				t.Fatalf("boundary changed: %v, want %v", events[len(events)-1], want)
			}
		})
	}
}

func TestNormalizeClaudeStreamInterleavedRequests(t *testing.T) {
	a, b := t.Name()+"/a", t.Name()+"/b"
	defer cleanupClaudeStream(a)
	defer cleanupClaudeStream(b)
	for _, item := range []struct{ id, text string }{{a, "alpha"}, {b, "beta"}} {
		if got := streamTestNormalize(t, item.id, streamTestFrames(t, streamTestText(0, "", item.text)...)); len(got) != 0 {
			t.Fatalf("%s emitted buffered text: %s", item.id, got)
		}
	}
	for _, item := range []struct{ id, text string }{{b, "beta"}, {a, "alpha"}} {
		out := streamTestNormalize(t, item.id, streamTestFrames(t, map[string]any{"type": "message_stop"}))
		blocks := streamTestBlocks(t, streamTestParse(t, out))
		streamTestWantBlocks(t, blocks, []string{"text"})
		if blocks[0].Text != item.text {
			t.Fatalf("%s text = %q, want %q", item.id, blocks[0].Text, item.text)
		}
	}
}

func TestNormalizeClaudeStreamSplitFrames(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("newline_%q", newline), func(t *testing.T) {
			id := t.Name()
			defer cleanupClaudeStream(id)
			var input []map[string]any
			input = append(input, streamTestThinking(0, "initial")...)
			input = append(input, streamTestText(1, "You", " already 🧪")...)
			input = append(input, streamTestThinking(2, ".")...)
			input = append(input, map[string]any{"type": "message_stop"})
			body := bytes.ReplaceAll(streamTestFrames(t, input...), []byte("\n"), []byte(newline))
			var out []byte
			// One-byte chunks split field names, JSON, UTF-8 and the CRLF delimiters.
			for i := range body {
				out = append(out, streamTestNormalize(t, id, body[i:i+1])...)
			}
			blocks := streamTestBlocks(t, streamTestParse(t, out))
			streamTestWantBlocks(t, blocks, []string{"thinking", "thinking", "text"})
			if blocks[0].Thinking != "initial" || blocks[1].Thinking != "." || blocks[2].Text != "You already 🧪" {
				t.Fatalf("split frame content = %+v", blocks)
			}
		})
	}
}

func TestNormalizeClaudeStreamSignedThinkingKeepsOriginalOrder(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	var input []map[string]any
	input = append(input, streamTestText(0, "", "before")...)
	input = append(input,
		streamTestStart(1, map[string]any{"type": "thinking", "thinking": "signed "}),
		streamTestDelta(1, map[string]any{"type": "thinking_delta", "thinking": "reasoning"}),
		streamTestDelta(1, map[string]any{"type": "signature_delta", "signature": "opaque-signature"}),
		streamTestStop(1),
	)
	input = append(input, streamTestText(2, "", "after")...)
	input = append(input, map[string]any{"type": "message_stop"})
	out := streamTestNormalize(t, id, streamTestFrames(t, input...))
	blocks := streamTestBlocks(t, streamTestParse(t, out))
	streamTestWantBlocks(t, blocks, []string{"text", "thinking", "text"})
	if blocks[0].Text != "before" || blocks[1].Thinking != "signed reasoning" || blocks[2].Text != "after" {
		t.Fatalf("signed segment changed order/content: %+v", blocks)
	}
	var signatures []string
	for _, delta := range blocks[1].Deltas {
		if delta["type"] == "signature_delta" {
			signatures = append(signatures, delta["signature"].(string))
		}
	}
	if !reflect.DeepEqual(signatures, []string{"opaque-signature"}) {
		t.Fatalf("signatures = %v", signatures)
	}
}

func TestNormalizeClaudeStreamPreservesUnknownBlock(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	unknown := map[string]any{"type": "future_block", "opaque": map[string]any{"value": "unchanged"}}
	delta := map[string]any{"type": "future_delta", "payload": "opaque"}
	input := streamTestText(0, "", "before")
	input = append(input, streamTestStart(1, unknown), streamTestDelta(1, delta), streamTestStop(1))
	input = append(input, streamTestText(2, "", "after")...)
	input = append(input, map[string]any{"type": "message_stop"})
	blocks := streamTestBlocks(t, streamTestParse(t, streamTestNormalize(t, id, streamTestFrames(t, input...))))
	streamTestWantBlocks(t, blocks, []string{"text", "future_block", "text"})
	if blocks[0].Text != "before" || blocks[2].Text != "after" || !reflect.DeepEqual(blocks[1].Start, unknown) || !reflect.DeepEqual(blocks[1].Deltas, []map[string]any{delta}) {
		t.Fatalf("unknown block or surrounding content changed: %+v", blocks)
	}
}

func TestCleanupClaudeStreamDiscardsBufferedState(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	pending := streamTestFrames(t, streamTestText(0, "", "discard me")...)
	pending = append(pending, []byte("event: content_block_start\ndata: {\"type\":")...)
	streamTestNormalize(t, id, pending)
	cleanupClaudeStream(id)
	input := streamTestText(0, "", "fresh")
	input = append(input, map[string]any{"type": "message_stop"})
	blocks := streamTestBlocks(t, streamTestParse(t, streamTestNormalize(t, id, streamTestFrames(t, input...))))
	streamTestWantBlocks(t, blocks, []string{"text"})
	if blocks[0].Text != "fresh" {
		t.Fatalf("cleanup retained old content: %q", blocks[0].Text)
	}
}

func TestStreamChunkHandlerNonClaudePassthrough(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"unchanged\"}}]}\n\n")
	request, err := json.Marshal(map[string]any{"RequestID": id, "SourceFormat": "openai", "ChunkIndex": 0, "Body": body})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod("response.intercept_stream_chunk", request)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Body      []byte
		DropChunk bool
	}
	decodeOK(t, raw, &result)
	if result.DropChunk || !bytes.Equal(result.Body, body) {
		t.Fatalf("non-claude chunk changed: body=%q drop=%v", result.Body, result.DropChunk)
	}
}

func TestStreamChunkHandlerHeaderInitDoesNotConsumeBody(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	request, err := json.Marshal(map[string]any{"RequestID": id, "SourceFormat": "claude", "ChunkIndex": -1, "Body": []byte("not an SSE frame")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod("response.intercept_stream_chunk", request)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		DropChunk bool
	}
	decodeOK(t, raw, &result)
	if result.DropChunk {
		t.Fatal("header initialization dropped the chunk")
	}
	input := streamTestText(0, "", "fresh")
	input = append(input, map[string]any{"type": "message_stop"})
	blocks := streamTestBlocks(t, streamTestParse(t, streamTestNormalize(t, id, streamTestFrames(t, input...))))
	streamTestWantBlocks(t, blocks, []string{"text"})
	if blocks[0].Text != "fresh" {
		t.Fatalf("header initialization contaminated stream: %+v", blocks)
	}
}

func TestStreamNormalizerCapabilities(t *testing.T) {
	caps := pluginRegistration().Capabilities
	if !caps.StreamInterceptor || !caps.RequestLifecycle {
		t.Fatal("stream interception and lifecycle cleanup must both be registered")
	}
}

func TestNormalizeClaudeStreamSignedInitialThinkingKeepsTextStreaming(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	initial := streamTestThinking(0, "initial")
	initial = append(initial[:2], streamTestDelta(0, map[string]any{"type": "signature_delta", "signature": "opaque"}), streamTestStop(0))
	out := streamTestNormalize(t, id, streamTestFrames(t, initial...))
	text := streamTestNormalize(t, id, streamTestFrames(t, streamTestText(1, "", "immediate")...))
	if len(text) == 0 {
		t.Fatal("text after signed thinking must not be buffered")
	}
	blocks := streamTestBlocks(t, streamTestParse(t, append(out, text...)))
	streamTestWantBlocks(t, blocks, []string{"thinking", "text"})
	if blocks[1].Text != "immediate" {
		t.Fatalf("text = %q", blocks[1].Text)
	}
}

func TestNormalizeClaudeStreamRejectsMismatchedBlockIndex(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	streamTestNormalize(t, id, streamTestFrames(t, streamTestStart(7, map[string]any{"type": "thinking", "thinking": ""})))
	_, err := normalizeClaudeStream(id, streamTestFrames(t, streamTestStop(8)))
	if err == nil {
		t.Fatal("mismatched block index must fail")
	}
}

func TestRequestCompleteCleansClaudeStream(t *testing.T) {
	id := t.Name()
	defer cleanupClaudeStream(id)
	streamTestNormalize(t, id, streamTestFrames(t, streamTestText(0, "", "discard me")...))
	request, err := json.Marshal(map[string]any{"RequestID": id})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod("request.complete", request)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	decodeOK(t, raw, &result)
	input := streamTestText(0, "", "fresh")
	input = append(input, map[string]any{"type": "message_stop"})
	blocks := streamTestBlocks(t, streamTestParse(t, streamTestNormalize(t, id, streamTestFrames(t, input...))))
	streamTestWantBlocks(t, blocks, []string{"text"})
	if blocks[0].Text != "fresh" {
		t.Fatalf("request.complete retained old content: %q", blocks[0].Text)
	}
}
