package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestIsPseudoCompletion covers the pseudo-completion detector: a done-terminated
// stream with far less output than a long-input task warrants must be flagged so
// the account is force-recorded as failed and conversations are evicted to a
// healthy account. Short prompts with short answers are NOT flagged.
func TestIsPseudoCompletion(t *testing.T) {
	chunkWith := func(text, reasoning string) pluginapi.ExecutorStreamChunk {
		raw, err := chunkDelta("req", "qwen3.8-max", text, reasoning, "")
		if err != nil {
			t.Fatalf("chunkDelta: %v", err)
		}
		return pluginapi.ExecutorStreamChunk{Payload: raw}
	}

	// Output char thresholds: pseudo = 59..479 chars (≈14..119 tokens, below 600);
	// healthy long = 860 chars (≈215 tokens, above 600). inputChars: long tasks
	// feed a few thousand chars, a greeting is a few chars.
	const (
		shortInput = 10
		longInput  = 3000
	)
	pseudoShort := strings.Repeat("x", 15*4-1) // ≈14 tokens (59 chars), below 600
	pseudoMid := strings.Repeat("x", 120*4-1)  // ≈119 tokens (479 chars), below 600 — the 01:49 gap
	healthy := strings.Repeat("x", 215*4)      // ≈215 tokens (860 chars), above 600
	greeting := strings.Repeat("x", 5*4-1)     // ≈4 tokens (19 chars), short answer

	cases := []struct {
		name       string
		chunks     []pluginapi.ExecutorStreamChunk
		inputChars int
		want       bool
	}{
		{name: "empty stream is not pseudo (no output at all)", chunks: nil, inputChars: longInput, want: false},
		{name: "short output with long input is pseudo", chunks: []pluginapi.ExecutorStreamChunk{chunkWith(pseudoShort, "")}, inputChars: longInput, want: true},
		{name: "mid output (~119 tok) with long input is pseudo — 01:49 gap", chunks: []pluginapi.ExecutorStreamChunk{chunkWith(pseudoMid, "")}, inputChars: longInput, want: true},
		{name: "short output with short input is NOT pseudo (greeting)", chunks: []pluginapi.ExecutorStreamChunk{chunkWith(greeting, "")}, inputChars: shortInput, want: false},
		{name: "healthy long output is not pseudo", chunks: []pluginapi.ExecutorStreamChunk{chunkWith(healthy, "")}, inputChars: longInput, want: false},
		{name: "only reasoning, no content, is not pseudo", chunks: []pluginapi.ExecutorStreamChunk{chunkWith("", "思考过程很长的推理...")}, inputChars: longInput, want: false},
		{
			name: "short content across many chunks with long input is pseudo",
			chunks: []pluginapi.ExecutorStreamChunk{
				chunkWith(strings.Repeat("a", 10), ""),
				chunkWith(strings.Repeat("b", 10), ""),
			},
			inputChars: longInput,
			want:       true,
		},
		{
			name: "malformed chunk payload is ignored (short output, long input)",
			chunks: []pluginapi.ExecutorStreamChunk{
				{Payload: []byte("{not json")},
				chunkWith("hi", ""),
			},
			inputChars: longInput,
			want:       true,
		},
	}
	for _, tc := range cases {
		if got := isPseudoCompletion(tc.chunks, tc.inputChars); got != tc.want {
			t.Errorf("%s: isPseudoCompletion = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPumpTraeStreamAttemptHealthGate 验证长输入在健康门槛前零下发，达标后按序释放。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-01 23:30:00；改动原因：锁定伪完成同请求恢复所需的首次下发门槛。
func TestPumpTraeStreamAttemptHealthGate(t *testing.T) {
	makeSSE := func(parts ...string) string {
		var out strings.Builder
		for _, part := range parts {
			out.WriteString("event: output\ndata: {\"response\":\"")
			out.WriteString(part)
			out.WriteString("\"}\n\n")
		}
		out.WriteString("event: done\ndata: {}\n\n")
		return out.String()
	}
	ctx := traeStreamPumpContext{Model: "qwen-max-latest", StatusCode: 200, InputChars: 3000}

	// 1. 599 字节正文后 done 必须判伪完成，失败账号的任何分片都不能下发。
	var emitted [][]byte
	pseudo := pumpTraeStreamAttempt(strings.NewReader(makeSSE(strings.Repeat("a", 599))), ctx, "gate-pseudo", func(payload []byte) error {
		emitted = append(emitted, bytes.Clone(payload))
		return nil
	})
	if !pseudo.Pseudo || pseudo.Err != nil || pseudo.Emitted || len(emitted) != 0 {
		t.Fatalf("599-byte result = pseudo:%v err:%v emitted:%v calls:%d", pseudo.Pseudo, pseudo.Err, pseudo.Emitted, len(emitted))
	}

	// 2. 分片累计到 600 字节时必须按原顺序释放；后续分片继续实时下发。
	emitted = nil
	healthy := pumpTraeStreamAttempt(strings.NewReader(makeSSE(strings.Repeat("b", 300), strings.Repeat("c", 300), "tail")), ctx, "gate-healthy", func(payload []byte) error {
		emitted = append(emitted, bytes.Clone(payload))
		return nil
	})
	if healthy.Pseudo || healthy.Err != nil || !healthy.Emitted || len(emitted) != 3 {
		t.Fatalf("600-byte result = pseudo:%v err:%v emitted:%v calls:%d", healthy.Pseudo, healthy.Err, healthy.Emitted, len(emitted))
	}
	if !bytes.Contains(emitted[0], []byte(strings.Repeat("b", 300))) || !bytes.Contains(emitted[1], []byte(strings.Repeat("c", 300))) || !bytes.Contains(emitted[2], []byte("tail")) {
		t.Fatalf("emission order/content mismatch: %q", emitted)
	}
}

// TestPumpTraeStreamAttemptKeepsExistingTerminalSemantics 验证短输入、纯推理和断流仍沿用原终结契约。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-01 23:30:00；改动原因：门槛缓冲不能误伤正常短答或已有断流兜底。
func TestPumpTraeStreamAttemptKeepsExistingTerminalSemantics(t *testing.T) {
	cases := []struct {
		name        string
		sse         string
		inputChars  int
		termination traeStreamTermination
		wantEmit    int
		wantPseudo  bool
	}{
		{name: "short input emits immediately", sse: "event: output\ndata: {\"response\":\"hi\"}\n\nevent: done\ndata: {}\n\n", inputChars: 2, termination: terminationDone, wantEmit: 1},
		{name: "reasoning only is not pseudo", sse: "event: output\ndata: {\"reasoning\":\"long reasoning\"}\n\nevent: done\ndata: {}\n\n", inputChars: 3000, termination: terminationDone, wantEmit: 1},
		{name: "output eof flushes buffered content", sse: "event: output\ndata: {\"response\":\"partial\"}\n\n", inputChars: 3000, termination: terminationOutputEOF, wantEmit: 1},
		{name: "done without output stays valid", sse: "event: done\ndata: {}\n\n", inputChars: 3000, termination: terminationDone, wantEmit: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emitCount := 0
			result := pumpTraeStreamAttempt(strings.NewReader(tc.sse), traeStreamPumpContext{Model: "qwen-max-latest", StatusCode: 200, InputChars: tc.inputChars}, "terminal", func([]byte) error {
				emitCount++
				return nil
			})
			if result.Err != nil || result.Pseudo != tc.wantPseudo || result.Termination != tc.termination || emitCount != tc.wantEmit {
				t.Fatalf("result = termination:%s pseudo:%v err:%v emits:%d", terminationLabel(result.Termination), result.Pseudo, result.Err, emitCount)
			}
		})
	}
}

// TestNoteForcedAccountFailureForPseudo is a behavior sentinel for the failover
// accounting path used by pseudo-completion detection: it must bypass the
// isAccountFailure status gate (HTTP 200 has no failure marker) yet still bump
// the account's failover state so the next request routes away.
func TestNoteForcedAccountFailureForPseudo(t *testing.T) {
	resetFailover(t)
	if !failoverActive() {
		t.Skip("failover inactive in this test binary")
	}
	auth := "tr-force-test"
	// A normal noteAccountFailure with status 200 + body must NOT count
	// (isAccountFailure rejects it) — this is the gap that let pseudo
	// completions slip through and reset the account as healthy.
	if noteAccountFailure(auth, 200, "upstream returned done with near-empty output") {
		t.Fatal("noteAccountFailure must not count HTTP 200 pseudo completion")
	}
	if count, _, ok := failoverStateSnapshot(auth); ok && count != 0 {
		t.Fatalf("failover count after noteAccountFailure = %d, want 0", count)
	}
	// The forced path MUST count it regardless of status.
	if !noteForcedAccountFailure(auth, "pseudo completion: upstream returned done with near-empty output") {
		t.Fatal("noteForcedAccountFailure should count a pseudo completion")
	}
	if count, _, _ := failoverStateSnapshot(auth); count != 1 {
		t.Fatalf("failover count after noteForcedAccountFailure = %d, want 1", count)
	}
}

// TestEstimateInputChars covers the pseudo-completion input-length signal: it
// must sum text across the normalized message shape (content = text-parts array),
// so a long GC-style task feeds enough chars to satisfy the input gate while a
// greeting does not.
func TestEstimateInputChars(t *testing.T) {
	msgs := toTraeMessages([]map[string]any{
		{"role": "user", "content": "你好"},
		{"role": "user", "content": strings.Repeat("分析", 2000)},
	})
	got := estimateInputChars(msgs)
	// 你好 = 6 bytes (UTF-8); 2000 * 分析 (6 bytes each) = 12000 bytes.
	want := 6 + 2000*6
	if got != want {
		t.Fatalf("estimateInputChars = %d, want %d", got, want)
	}
	// Multi-part content must also be counted.
	multi := toTraeMessages([]map[string]any{
		{"role": "user", "content": []any{map[string]any{"type": "text", "text": "abc"}, map[string]any{"type": "text", "text": "def"}}},
	})
	if got := estimateInputChars(multi); got != 6 {
		t.Fatalf("estimateInputChars(multi-part) = %d, want 6", got)
	}
}
