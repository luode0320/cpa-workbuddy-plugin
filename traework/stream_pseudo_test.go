package main

import (
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
