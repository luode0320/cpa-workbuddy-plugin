// cleanchunk_test.go 锁定 cleanChunkJSON 对上游终止计费帧的保留语义。
// qoderwork 上游会把 usage 挂在 delta 为空、且没有 finish_reason 的 chunk 上，
// 旧逻辑把这类「全空 delta」整帧丢弃，导致下游（new-api 等）拿不到 token 数、
// 全部记 $0。本文件同时守住空壳清理行为不被回退。
// 最近修改时间：2026-11-08 19:20:00；改动原因：修复 usage 终止帧被丢弃致 0 计费。
package main

import (
	"encoding/json"
	"testing"
)

// TestCleanChunkJSONKeepsUsageOnlyFrame 上游真实形态：choices[0].delta 为 {},
// 无 finish_reason，usage 挂在顶层。必须原样保留整帧。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 19:20:00；改动原因：usage 终止帧被丢弃致 0 计费。
func TestCleanChunkJSONKeepsUsageOnlyFrame(t *testing.T) {
	raw := `{"choices":[{"delta":{},"index":0}],"created":1792316744,"id":"chatcmpl-x","model":"MiniMax-M3.1-Flash","object":"chat.completion.chunk","usage":{"completion_tokens":24,"completion_tokens_details":{"reasoning_tokens":20,"text_tokens":24},"prompt_tokens":63,"prompt_tokens_details":{"cached_tokens":0,"text_tokens":63},"total_tokens":87}}`
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
	if obj["model"] != "MiniMax-M3.1-Flash" {
		t.Fatalf("输出丢失 model 字段: %s", got)
	}
}

// TestCleanChunkJSONKeepsUsageWithNoiseFields 上游在 usage 帧里同时带空
// tool_calls / function_call 时，仍必须保留该帧（清空壳但不下沉为 ""）。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 19:20:00；改动原因：同 TestCleanChunkJSONKeepsUsageOnlyFrame。
func TestCleanChunkJSONKeepsUsageWithNoiseFields(t *testing.T) {
	raw := `{"choices":[{"delta":{"function_call":null,"tool_calls":[]},"index":0}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
	got := cleanChunkJSON(raw)
	if got == "" {
		t.Fatal("带空壳噪声的 usage 帧被整帧丢弃")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, got)
	}
	if _, ok := obj["usage"]; !ok {
		t.Fatalf("输出丢失 usage: %s", got)
	}
	choices := obj["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	if _, present := delta["function_call"]; present {
		t.Fatalf("空 function_call 未被清理: %s", got)
	}
	if _, present := delta["tool_calls"]; present {
		t.Fatalf("空 tool_calls 未被清理: %s", got)
	}
}

// TestCleanChunkJSONDropsEmptyNoSignalFrame 无 usage、无 finish_reason 的纯空
// delta 仍必须整帧丢弃，避免给客户端发无意义空帧。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 19:20:00；改动原因：同 TestCleanChunkJSONKeepsUsageOnlyFrame。
func TestCleanChunkJSONDropsEmptyNoSignalFrame(t *testing.T) {
	raw := `{"choices":[{"delta":{"function_call":null},"index":0}],"id":"chatcmpl-y"}`
	if got := cleanChunkJSON(raw); got != "" {
		t.Fatalf("纯空 delta 帧应被丢弃，实际保留: %s", got)
	}
}

// TestCleanChunkJSONKeepsFinishReason 带 finish_reason 的空 delta 帧是正常终止帧，
// 不受 usage 分支影响。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 19:20:00；改动原因：同 TestCleanChunkJSONKeepsUsageOnlyFrame。
func TestCleanChunkJSONKeepsFinishReason(t *testing.T) {
	raw := `{"choices":[{"delta":{},"finish_reason":"stop","index":0}],"id":"chatcmpl-z"}`
	got := cleanChunkJSON(raw)
	if got == "" {
		t.Fatal("带 finish_reason 的终止帧被丢弃")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, got)
	}
	choice := obj["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason 丢失: %s", got)
	}
}
