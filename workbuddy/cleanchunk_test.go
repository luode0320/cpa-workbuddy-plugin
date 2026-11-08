package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanChunkStripsLegacyShellInTerminalChunk(t *testing.T) {
	in := `{"choices":[{"index":0,"delta":{"role":"assistant","function_call":{"arguments":"","name":""}},"finish_reason":"tool_calls"}]}`
	got := cleanChunkJSON(in)
	if got == "" {
		t.Fatal("terminal chunk with finish_reason dropped")
	}
	if strings.Contains(got, "function_call") {
		t.Fatalf("legacy function_call shell not stripped: %s", got)
	}
	if !strings.Contains(got, `"finish_reason":"tool_calls"`) || !strings.Contains(got, `"role":"assistant"`) {
		t.Fatalf("meaningful fields lost: %s", got)
	}
}

func TestCleanChunkKeepsRealToolCallDeltas(t *testing.T) {
	// mid-stream fragment: real arguments flowing — must pass through untouched
	in := `{"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"\"","name":""},"index":0}]}}]}`
	got := cleanChunkJSON(in)
	if got != in {
		t.Fatalf("real tool_call fragment altered:\n in: %s\ngot: %s", in, got)
	}
	// non-empty function_call (legacy real call) must survive
	in2 := `{"choices":[{"index":0,"delta":{"function_call":{"arguments":"{}","name":"get_time"}}]}`
	if got := cleanChunkJSON(in2); got != in2 {
		t.Fatalf("real legacy function_call altered: %s", got)
	}
}

func TestIsEmptyValueRecursion(t *testing.T) {
	if !isEmptyValue(map[string]any{"name": "", "arguments": ""}) {
		t.Fatal("all-empty map should be empty")
	}
	if isEmptyValue(map[string]any{"name": "get_time", "arguments": ""}) {
		t.Fatal("map with non-empty value must not be empty")
	}
}

func TestCleanChunkStripsNoiseFields(t *testing.T) {
	in := `{"choices":[{"index":0,"delta":{"content":"hi","extra_fields":null,"refusal":"","reasoning_content":""}}]}`
	got := cleanChunkJSON(in)
	if got == "" {
		t.Fatal("chunk dropped")
	}
	for _, noise := range []string{"extra_fields", "refusal", "reasoning_content"} {
		if strings.Contains(got, noise) {
			t.Fatalf("noise field %s not stripped: %s", noise, got)
		}
	}
	if !strings.Contains(got, `"content":"hi"`) {
		t.Fatalf("content lost: %s", got)
	}
}

func TestCleanChunk_EmptyOrInvalid(t *testing.T) {
	if cleanChunkJSON("") != "" {
		t.Fatal("empty should stay empty")
	}
	if cleanChunkJSON("not-json") != "not-json" && cleanChunkJSON("not-json") != "" {
		// implementation may pass-through or drop; both acceptable if no panic
	}
	// purely empty delta object
	in := `{"choices":[{"index":0,"delta":{}}]}`
	_ = cleanChunkJSON(in) // must not panic
}

func TestStripDataPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"data: hello", "hello"},
		{"data:hello", "hello"},
		{"data:  {\"a\":1}", "{\"a\":1}"},
		{"hello", "hello"},
		{"", ""},
		{"DATA: x", "DATA: x"}, // case-sensitive: only lowercase data:
	}
	for _, tc := range cases {
		if got := stripDataPrefix(tc.in); got != tc.want {
			t.Fatalf("stripDataPrefix(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeTools_NoToolsPassThrough(t *testing.T) {
	in := []byte(`{"model":"x","messages":[]}`)
	got := normalizeToolsForUpstream(in)
	if string(got) != string(in) {
		t.Fatalf("unexpected rewrite: %s", got)
	}
}

// TestCleanChunkJSONKeepsUsageOnlyFrame 上游真实形态：choices[0].delta 为 {},
// 无 finish_reason，usage 挂在顶层。必须原样保留整帧——旧逻辑把这类「全空
// delta」整帧丢弃，下游（new-api）拿不到 token 数只能记 $0。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 19:20:00；改动原因：usage 终止帧被丢弃致下游零计费。
func TestCleanChunkJSONKeepsUsageOnlyFrame(t *testing.T) {
	raw := `{"choices":[{"delta":{},"index":0}],"created":1792316744,"id":"chatcmpl-x","model":"deepseek-v4.1-flash","object":"chat.completion.chunk","usage":{"completion_tokens":24,"completion_tokens_details":{"reasoning_tokens":20,"text_tokens":24},"prompt_tokens":63,"prompt_tokens_details":{"cached_tokens":0,"text_tokens":63},"total_tokens":87}}`
	got := cleanChunkJSON(raw)
	if got == "" {
		t.Fatal("usage 终止计费帧被丢弃：下游将拿不到 token 数")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, got)
	}
	usage, ok := obj["usage"].(map[string]any)
	if !ok {
		t.Fatalf("输出丢失 usage: %s", got)
	}
	if total, _ := usage["total_tokens"].(float64); total != 87 {
		t.Fatalf("usage.total_tokens = %v, want 87", usage["total_tokens"])
	}
}

// TestCleanChunkJSONDropsEmptyNoSignalFrameWithoutUsage 无 usage、无 finish_reason
// 的纯空 delta 仍必须整帧丢弃，避免给客户端发无意义空帧。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 19:20:00；改动原因：同 TestCleanChunkJSONKeepsUsageOnlyFrame。
func TestCleanChunkJSONDropsEmptyNoSignalFrameWithoutUsage(t *testing.T) {
	raw := `{"choices":[{"delta":{"function_call":null},"index":0}],"id":"chatcmpl-y"}`
	if got := cleanChunkJSON(raw); got != "" {
		t.Fatalf("纯空 delta 帧应被丢弃，实际保留: %s", got)
	}
}
