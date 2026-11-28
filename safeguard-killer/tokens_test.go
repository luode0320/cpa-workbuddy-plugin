package main

import (
	"encoding/json"
	"testing"
)

func TestCountGroupTokens(t *testing.T) {
	body := `{"model":"uncensored-models","system":"You are terse.","messages":[{"role":"user","content":[{"type":"text","text":"Hello there, how are you?"}]}]}`
	raw, errCount := countGroupTokens([]byte(`{"Model":"uncensored-models","SourceFormat":"claude","Payload":"`+b64(body)+`"}`), groupConfig())
	if errCount != nil {
		t.Fatalf("countGroupTokens() error = %v", errCount)
	}
	var resp executorResponse
	decodeOK(t, raw, &resp)
	var count countTokensResponse
	if errUnmarshal := json.Unmarshal(resp.Payload, &count); errUnmarshal != nil {
		t.Fatalf("decode payload %s: %v", resp.Payload, errUnmarshal)
	}
	// "You are terse." + "Hello there, how are you?" is 11 o200k tokens; the model name is not prompt.
	if count.InputTokens != 11 {
		t.Fatalf("input_tokens = %d, want 11", count.InputTokens)
	}
}

func TestCountGroupTokensUnknownGroup(t *testing.T) {
	if _, errCount := countGroupTokens([]byte(`{"Model":"nope","Payload":"`+b64(`{}`)+`"}`), groupConfig()); errCount == nil {
		t.Fatal("countGroupTokens(unknown group) error = nil")
	}
}

// TestCountGroupTokensAcceptsMemberModelName covers a request made with a group member model name
// instead of the group codename. The host routes count_tokens through model.route just like a chat
// request, so both paths must accept the same model names.
func TestCountGroupTokensAcceptsMemberModelName(t *testing.T) {
	body := `{"model":"deepseek-v4.1-flash-uncensored","messages":[]}`
	raw, errCount := countGroupTokens([]byte(`{"Model":"deepseek-v4.1-flash-uncensored","Payload":"`+b64(body)+`"}`), groupConfig())
	if errCount != nil {
		t.Fatalf("countGroupTokens(member model) error = %v", errCount)
	}
	var resp executorResponse
	decodeOK(t, raw, &resp)
	var count countTokensResponse
	if errUnmarshal := json.Unmarshal(resp.Payload, &count); errUnmarshal != nil {
		t.Fatalf("decode payload %s: %v", resp.Payload, errUnmarshal)
	}
	if count.InputTokens != 0 {
		t.Fatalf("input_tokens = %d, want 0（空 messages 不含提示词）", count.InputTokens)
	}
}
