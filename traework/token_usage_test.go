package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// realTraeTokenUsage 是 2026-09-04 直连 qwen3.8-max 取证的 event:token_usage
// data 原样：reasoning_tokens 位于顶层（与 CodeBuddy 的 completion_tokens_details
// 子对象形态不同），并带 cache 计数与 cluster 噪声键。
const realTraeTokenUsage = `{"prompt_tokens":69,"completion_tokens":34,"reasoning_tokens":23,"total_tokens":103,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cluster":"normal_context"}`

// tokenUsageSSE 组装一条完整健康流：output + 取证形态 token_usage + done。
func tokenUsageSSE(usageData string) string {
	return "event: output\ndata: {\"response\":\"你好\"}\n\n" +
		"event: token_usage\ndata: " + usageData + "\n\n" +
		"event: done\ndata: {}\n\n"
}

// TestUsageDetailFromTraeMapTopLevel 锁定顶层键形态的解析（Trae 实测取证）：
// prompt/completion/reasoning/total 四类计数全部落位，cache 与未知键（cluster）
// 不干扰解析。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：dashboard Token 列真实 usage——解析函数单测。
func TestUsageDetailFromTraeMapTopLevel(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(realTraeTokenUsage), &m); err != nil {
		t.Fatalf("decode forensic token_usage: %v", err)
	}
	d := usageDetailFromTraeMap(m)
	if d.InputTokens != 69 {
		t.Fatalf("InputTokens = %d; want 69", d.InputTokens)
	}
	if d.OutputTokens != 34 {
		t.Fatalf("OutputTokens = %d; want 34", d.OutputTokens)
	}
	if d.ReasoningTokens != 23 {
		t.Fatalf("ReasoningTokens = %d; want 23", d.ReasoningTokens)
	}
	if d.TotalTokens != 103 {
		t.Fatalf("TotalTokens = %d; want 103", d.TotalTokens)
	}
	if d.CacheReadTokens != 0 || d.CacheCreationTokens != 0 {
		t.Fatalf("cache = %d/%d; want 0/0", d.CacheReadTokens, d.CacheCreationTokens)
	}
}

// TestUsageDetailFromTraeMapNestedReasoningFallback 锁定 OpenAI 惯例形态的兼容：
// 顶层无 reasoning_tokens 时回退 completion_tokens_details.reasoning_tokens
// （CodeBuddy 等平台形态），同时验证 input/output 的蛇形别名键。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：同 usageDetailFromTraeMap。
func TestUsageDetailFromTraeMapNestedReasoningFallback(t *testing.T) {
	var m map[string]any
	raw := `{"prompt_tokens":100,"completion_tokens":40,"total_tokens":140,` +
		`"completion_tokens_details":{"reasoning_tokens":25},"cache_read_input_tokens":7}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := usageDetailFromTraeMap(m)
	if d.InputTokens != 100 || d.OutputTokens != 40 || d.TotalTokens != 140 {
		t.Fatalf("base counts = %d/%d/%d; want 100/40/140", d.InputTokens, d.OutputTokens, d.TotalTokens)
	}
	if d.ReasoningTokens != 25 {
		t.Fatalf("ReasoningTokens = %d; want 25 (completion_tokens_details fallback)", d.ReasoningTokens)
	}
	if d.CacheReadTokens != 7 {
		t.Fatalf("CacheReadTokens = %d; want 7", d.CacheReadTokens)
	}
}

// TestUsageDetailFromTraeMapEmptyAndAliases 锁定空输入返回零 Detail，且顶层
// reasoning_tokens 存在时不被子对象零值覆盖；顺带覆盖 input_tokens/output_tokens
// 别名键形态。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：同 usageDetailFromTraeMap。
func TestUsageDetailFromTraeMapEmptyAndAliases(t *testing.T) {
	if d := usageDetailFromTraeMap(nil); d != (usage.Detail{}) {
		t.Fatalf("nil map detail = %+v; want zero", d)
	}
	if d := usageDetailFromTraeMap(map[string]any{}); d != (usage.Detail{}) {
		t.Fatalf("empty map detail = %+v; want zero", d)
	}
	// 顶层 reasoning 已命中时，子对象同键不得覆盖（Trae 顶层形态优先）。
	m := map[string]any{
		"input_tokens":              int64(5),
		"output_tokens":             int64(6),
		"reasoning_tokens":          int64(2),
		"total_tokens":              int64(11),
		"completion_tokens_details": map[string]any{"reasoning_tokens": int64(99)},
	}
	d := usageDetailFromTraeMap(m)
	if d.ReasoningTokens != 2 {
		t.Fatalf("ReasoningTokens = %d; want 2 (top-level wins over nested zero-overwrite)", d.ReasoningTokens)
	}
	if d.InputTokens != 5 || d.OutputTokens != 6 || d.TotalTokens != 11 {
		t.Fatalf("alias counts = %d/%d/%d; want 5/6/11", d.InputTokens, d.OutputTokens, d.TotalTokens)
	}
}

// TestCollectTraeStreamReportsTokenUsage 锁定同步收集路径把上游 token_usage
// 真实用量带出（dashboard Token 列数据源）：output + token_usage + done 的
// 健康流返回完整 detail 且无错误。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：dashboard Token 列真实 usage——collect 路径回归锁定。
func TestCollectTraeStreamReportsTokenUsage(t *testing.T) {
	chunks, _, _, detail, err := collectTraeStream(strings.NewReader(tokenUsageSSE(realTraeTokenUsage)), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream error = %v; want nil", err)
	}
	if len(chunks) == 0 {
		t.Fatal("chunks empty; want output + stop terminal chunks")
	}
	if detail.InputTokens != 69 || detail.OutputTokens != 34 || detail.ReasoningTokens != 23 || detail.TotalTokens != 103 {
		t.Fatalf("detail = %+v; want 69/34/23/103 from forensic token_usage", detail)
	}
}

// TestCollectTraeStreamWithoutTokenUsageEmptyDetail 锁定旧上游 / 断流早于
// token_usage 事件时返回空 Detail（TotalTokens=0），调用方据此回退估算——
// 这是 usageDetailForAttempt 的兜底前提，防真实值恒零场景误吞。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：同 TestCollectTraeStreamReportsTokenUsage。
func TestCollectTraeStreamWithoutTokenUsageEmptyDetail(t *testing.T) {
	sse := "event: output\ndata: {\"response\":\"你好\"}\n\nevent: done\ndata: {}\n\n"
	_, _, _, detail, err := collectTraeStream(strings.NewReader(sse), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream error = %v; want nil", err)
	}
	if detail.TotalTokens != 0 {
		t.Fatalf("detail = %+v; want empty (no token_usage event observed)", detail)
	}
}

// TestAggregateTraeCompletionReportsTokenUsage 锁定非流式聚合路径同样带出
// 上游真实用量（handleExecExecute 的发布数据源）。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：dashboard Token 列真实 usage——aggregate 路径回归锁定。
func TestAggregateTraeCompletionReportsTokenUsage(t *testing.T) {
	completion, detail, err := aggregateTraeCompletion(strings.NewReader(tokenUsageSSE(realTraeTokenUsage)), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("aggregateTraeCompletion error = %v; want nil", err)
	}
	if len(completion) == 0 {
		t.Fatal("completion empty")
	}
	if detail.InputTokens != 69 || detail.OutputTokens != 34 || detail.ReasoningTokens != 23 || detail.TotalTokens != 103 {
		t.Fatalf("detail = %+v; want 69/34/23/103 from forensic token_usage", detail)
	}
}

// TestUsageDetailForAttemptPrefersReal 锁定流式兜底语义：真实用量 total>0 时
// 原样返回（不掺估算），空 Detail 时回退 content 字符估算（输出非零）。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：同 usageDetailForAttempt。
func TestUsageDetailForAttemptPrefersReal(t *testing.T) {
	real := usage.Detail{InputTokens: 7, OutputTokens: 9, ReasoningTokens: 3, TotalTokens: 16}
	chunks := []pluginapi.ExecutorStreamChunk{{Payload: []byte(`{"choices":[{"delta":{"content":"abcdefgh"}}]}`)}}
	if got := usageDetailForAttempt(real, chunks); got != real {
		t.Fatalf("usageDetailForAttempt(real) = %+v; want real unchanged %+v", got, real)
	}
	got := usageDetailForAttempt(usage.Detail{}, chunks)
	if got.OutputTokens <= 0 {
		t.Fatalf("usageDetailForAttempt(empty) = %+v; want estimate fallback with output > 0", got)
	}
	if got.InputTokens != 0 || got.ReasoningTokens != 0 {
		t.Fatalf("estimate fallback = %+v; want Input/Reasoning zero (chars/4 estimate)", got)
	}
}

// TestUsageDetailForCompletionPrefersReal 锁定非流式兜底语义（与流式同构）：
// 真实 total>0 原样返回；空 Detail 时按 chat.completion 正文 chars/4 估算。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：同 usageDetailForCompletion。
func TestUsageDetailForCompletionPrefersReal(t *testing.T) {
	real := usage.Detail{InputTokens: 11, OutputTokens: 12, ReasoningTokens: 4, TotalTokens: 23}
	completion := []byte(`{"choices":[{"message":{"content":"abcdefgh"}}]}`)
	if got := usageDetailForCompletion(real, completion); got != real {
		t.Fatalf("usageDetailForCompletion(real) = %+v; want real unchanged %+v", got, real)
	}
	got := usageDetailForCompletion(usage.Detail{}, completion)
	if got.OutputTokens <= 0 {
		t.Fatalf("usageDetailForCompletion(empty) = %+v; want estimate fallback with output > 0", got)
	}
	if got.InputTokens != 0 || got.ReasoningTokens != 0 {
		t.Fatalf("estimate fallback = %+v; want Input/Reasoning zero (chars/4 estimate)", got)
	}
}

// TestUsageDetailFromTraeMapHandlesWrappedUsage 锁定 feed 兼容形态：data 外层
// 带 usage 包装键（OpenAI 惯例）时同样解析（collector.feed 先解包再进 map）。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-04；改动原因：同 usageDetailFromTraeMap。
func TestUsageDetailFromTraeMapHandlesWrappedUsage(t *testing.T) {
	var m map[string]any
	raw := `{"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var c traeUsageCollector
	c.feed(strings.TrimSpace(raw))
	d := c.detail()
	if d.InputTokens != 9 || d.OutputTokens != 3 || d.TotalTokens != 12 {
		t.Fatalf("detail = %+v; want 9/3/12 via usage wrapper", d)
	}
}
