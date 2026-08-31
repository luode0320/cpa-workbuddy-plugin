package main

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestIsPseudoCompletion covers the pseudo-completion detector: a done-terminated
// stream with near-empty visible content must be flagged so the account is
// force-recorded as failed and conversations are evicted to a healthy account.
func TestIsPseudoCompletion(t *testing.T) {
	chunkWith := func(text, reasoning string) pluginapi.ExecutorStreamChunk {
		raw, err := chunkDelta("req", "qwen3.8-max", text, reasoning, "")
		if err != nil {
			t.Fatalf("chunkDelta: %v", err)
		}
		return pluginapi.ExecutorStreamChunk{Payload: raw}
	}

	short := strings.Repeat("x", pseudoCompletionMinChars-1)
	atThreshold := strings.Repeat("x", pseudoCompletionMinChars)
	long := strings.Repeat("x", pseudoCompletionMinChars*3)

	cases := []struct {
		name   string
		chunks []pluginapi.ExecutorStreamChunk
		want   bool
	}{
		{name: "empty stream is not pseudo (no output at all)", chunks: nil, want: false},
		{name: "short content below threshold is pseudo", chunks: []pluginapi.ExecutorStreamChunk{chunkWith(short, "")}, want: true},
		{name: "exactly at threshold is not pseudo", chunks: []pluginapi.ExecutorStreamChunk{chunkWith(atThreshold, "")}, want: false},
		{name: "long content is not pseudo", chunks: []pluginapi.ExecutorStreamChunk{chunkWith(long, "")}, want: false},
		{name: "only reasoning, no content, is not pseudo", chunks: []pluginapi.ExecutorStreamChunk{chunkWith("", "思考过程很长的推理...")}, want: false},
		{
			name: "short content across many chunks is pseudo",
			chunks: []pluginapi.ExecutorStreamChunk{
				chunkWith(strings.Repeat("a", 10), ""),
				chunkWith(strings.Repeat("b", 10), ""),
			},
			want: true,
		},
		{
			name: "malformed chunk payload is ignored",
			chunks: []pluginapi.ExecutorStreamChunk{
				{Payload: []byte("{not json")},
				chunkWith("hi", ""),
			},
			want: true,
		},
	}
	for _, tc := range cases {
		if got := isPseudoCompletion(tc.chunks); got != tc.want {
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
