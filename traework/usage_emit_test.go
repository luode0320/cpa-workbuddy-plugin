package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// decodeChunkPayload 把一条下行分片 payload 解成通用 map，供 usage 断言使用。
// 走到这里的分片必须是可独立解析的 JSON（sseFrameChunks 的 "data: " 前缀是
// 之后才加的传输包装，不进 payload 本体）。
//
// [参数] t: 当前测试；payload: 分片原始字节。
// [返回] 解析后的 map。
// 最近修改时间：2026-11-08 14:02:00；改动原因：下行 usage 帧缺失的修复验证。
func decodeChunkPayload(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("decode chunk payload %s: %v", payload, err)
	}
	return m
}

// chunkUsageObject 取出分片顶层 usage 对象（缺失返回 nil），只断言形态。
//
// [参数] t: 当前测试；payload: 分片原始字节。
// [返回] usage 对象或 nil。
// 最近修改时间：2026-11-08 14:02:00；改动原因：同 decodeChunkPayload。
func chunkUsageObject(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	m := decodeChunkPayload(t, payload)
	raw, ok := m["usage"]
	if !ok {
		return nil
	}
	u, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("chunk usage is not an object: %T %v", raw, raw)
	}
	return u
}

// TestUsageToOpenAIStandardKeys 锁定 usage 对象必须使用宿主实际读取的 OpenAI
// 蛇形键：宿主 usage_helpers.go 的 hasOpenAIStyleUsageTokenFields 只认
// total_tokens 与 prompt/completion 等标准桶字段，键名写错等于没挂。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：下行 usage 帧缺失的修复验证。
func TestUsageToOpenAIStandardKeys(t *testing.T) {
	got := usageToOpenAI(usage.Detail{InputTokens: 69, OutputTokens: 34, TotalTokens: 103})
	if got == nil {
		t.Fatal("usageToOpenAI returned nil for non-zero detail")
	}
	if v, _ := got["prompt_tokens"].(int64); v != 69 {
		t.Fatalf("prompt_tokens = %v; want 69", got["prompt_tokens"])
	}
	if v, _ := got["completion_tokens"].(int64); v != 34 {
		t.Fatalf("completion_tokens = %v; want 34", got["completion_tokens"])
	}
	if v, _ := got["total_tokens"].(int64); v != 103 {
		t.Fatalf("total_tokens = %v; want 103", got["total_tokens"])
	}
}

// TestUsageToOpenAIDetailNesting 锁定 cached / reasoning 必须放在宿主真正读取
// 的子对象位置：prompt_tokens_details.cached_tokens 与
// completion_tokens_details.reasoning_tokens。顶层的 reasoning_tokens 宿主不读，
// 放错位置等于 reasoning 用量丢失。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：同 TestUsageToOpenAIStandardKeys。
func TestUsageToOpenAIDetailNesting(t *testing.T) {
	got := usageToOpenAI(usage.Detail{
		InputTokens: 69, OutputTokens: 34, TotalTokens: 103,
		ReasoningTokens: 23, CacheReadTokens: 11, CacheCreationTokens: 5,
	})
	ptd, ok := got["prompt_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("prompt_tokens_details missing or wrong type: %v", got["prompt_tokens_details"])
	}
	if v, _ := ptd["cached_tokens"].(int64); v != 11 {
		t.Fatalf("prompt_tokens_details.cached_tokens = %v; want 11", ptd["cached_tokens"])
	}
	if v, _ := ptd["cache_creation_tokens"].(int64); v != 5 {
		t.Fatalf("prompt_tokens_details.cache_creation_tokens = %v; want 5", ptd["cache_creation_tokens"])
	}
	ctd, ok := got["completion_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("completion_tokens_details missing or wrong type: %v", got["completion_tokens_details"])
	}
	if v, _ := ctd["reasoning_tokens"].(int64); v != 23 {
		t.Fatalf("completion_tokens_details.reasoning_tokens = %v; want 23", ctd["reasoning_tokens"])
	}
	if _, has := got["reasoning_tokens"]; has {
		t.Fatal("top-level reasoning_tokens must not be emitted; host never reads it")
	}
}

// TestUsageToOpenAIZeroDetailNil 锁定全零明细不挂 usage：挂一个 0/0/0 的
// usage 会被下游当成真实用量采信，new-api 会把该请求记成 0 token，
// 比「未上报」更糟。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：同 TestUsageToOpenAIStandardKeys。
func TestUsageToOpenAIZeroDetailNil(t *testing.T) {
	if got := usageToOpenAI(usage.Detail{}); got != nil {
		t.Fatalf("usageToOpenAI(zero) = %v; want nil", got)
	}
}

// TestUsageToOpenAITotalFallback 锁定上游只给 prompt/completion 而不给
// total_tokens 时用两者之和补齐——total 缺省会让下游统计少算总量。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：同 TestUsageToOpenAIStandardKeys。
func TestUsageToOpenAITotalFallback(t *testing.T) {
	got := usageToOpenAI(usage.Detail{InputTokens: 7, OutputTokens: 5})
	if v, _ := got["total_tokens"].(int64); v != 12 {
		t.Fatalf("total_tokens = %v; want 12 (input+output fallback)", got["total_tokens"])
	}
}

// TestCollectTraeStreamTerminalChunkCarriesUsage 锁定流式终止帧必须携带 usage：
// 中间正文帧一律不得带（OpenAI 约定流式用量只在最后一帧，中间帧带 usage 会被
// 客户端按重复计费累加），最后一帧必须带且数值等于上游 token_usage 真实值。
// 这条断言就是 2026-11-08 new-api Tokens 列显示 "-" 的回归守卫——一旦终止帧退回
// 不挂 usage 的构造路径，本用例立即失败。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：下行 usage 帧缺失的修复验证。
func TestCollectTraeStreamTerminalChunkCarriesUsage(t *testing.T) {
	chunks, _, _, _, err := collectTraeStream(strings.NewReader(tokenUsageSSE(realTraeTokenUsage)), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d; want >= 2 (body + terminal)", len(chunks))
	}
	for i, c := range chunks[:len(chunks)-1] {
		if u := chunkUsageObject(t, c.Payload); u != nil {
			t.Fatalf("chunk[%d] must not carry usage: %v", i, u)
		}
	}
	terminal := chunks[len(chunks)-1]
	u := chunkUsageObject(t, terminal.Payload)
	if u == nil {
		t.Fatal("terminal chunk carries no usage; host and new-api will see Tokens: -")
	}
	if v, _ := u["prompt_tokens"].(float64); v != 69 {
		t.Fatalf("terminal prompt_tokens = %v; want 69", u["prompt_tokens"])
	}
	if v, _ := u["completion_tokens"].(float64); v != 34 {
		t.Fatalf("terminal completion_tokens = %v; want 34", u["completion_tokens"])
	}
	if v, _ := u["total_tokens"].(float64); v != 103 {
		t.Fatalf("terminal total_tokens = %v; want 103", u["total_tokens"])
	}
	m := decodeChunkPayload(t, terminal.Payload)
	choices, _ := m["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("terminal choices = %v; want exactly 1", m["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("terminal finish_reason = %v; want stop", choice["finish_reason"])
	}
}

// TestCollectTraeStreamTerminalChunkUsageEstimatesWhenAbsent 锁定上游未发
// token_usage 事件时的兜底：终止帧仍必须带 usage（按正文字符估算），否则这条
// 路径的流量依旧零计费。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：同 TestCollectTraeStreamTerminalChunkCarriesUsage。
func TestCollectTraeStreamTerminalChunkUsageEstimatesWhenAbsent(t *testing.T) {
	sse := "event: output\ndata: {\"response\":\"你好\"}\n\nevent: done\ndata: {}\n\n"
	chunks, _, _, _, err := collectTraeStream(strings.NewReader(sse), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream: %v", err)
	}
	u := chunkUsageObject(t, chunks[len(chunks)-1].Payload)
	if u == nil {
		t.Fatal("terminal chunk carries no usage without token_usage event; estimated usage must still be emitted")
	}
	if v, _ := u["total_tokens"].(float64); v <= 0 {
		t.Fatalf("terminal total_tokens = %v; want > 0 (estimated)", u["total_tokens"])
	}
}

// TestAggregateTraeCompletionCarriesUsage 锁定非流式响应体顶层必须带 usage：
// 宿主 ParseOpenAIUsage 与非流式计费都只读这里，缺失时整条请求记 0 用量。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：下行 usage 帧缺失的修复验证。
func TestAggregateTraeCompletionCarriesUsage(t *testing.T) {
	payload, detail, err := aggregateTraeCompletion(strings.NewReader(tokenUsageSSE(realTraeTokenUsage)), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("aggregateTraeCompletion: %v", err)
	}
	if detail.TotalTokens != 103 {
		t.Fatalf("returned detail total = %d; want 103", detail.TotalTokens)
	}
	u := chunkUsageObject(t, payload)
	if u == nil {
		t.Fatal("non-streaming completion carries no usage; host will record 0 tokens")
	}
	if v, _ := u["prompt_tokens"].(float64); v != 69 {
		t.Fatalf("usage.prompt_tokens = %v; want 69", u["prompt_tokens"])
	}
	if v, _ := u["total_tokens"].(float64); v != 103 {
		t.Fatalf("usage.total_tokens = %v; want 103", u["total_tokens"])
	}
}

// TestNormalizeTraeDetailPartialBuckets 锁定真实用量判定放宽：上游只给
// prompt/completion 而不给 total_tokens 时必须判为「已上报」并用两者之和补齐，
// 否则会退回字符估算，导致下行 usage 与上游实际计数不一致。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：同 TestUsageToOpenAIStandardKeys。
func TestNormalizeTraeDetailPartialBuckets(t *testing.T) {
	got, ok := normalizeTraeDetail(usage.Detail{InputTokens: 7, OutputTokens: 5})
	if !ok {
		t.Fatal("normalizeTraeDetail(partial) reported not-reported; want ok")
	}
	if got.TotalTokens != 12 {
		t.Fatalf("TotalTokens = %d; want 12 (input+output)", got.TotalTokens)
	}
	if _, ok := normalizeTraeDetail(usage.Detail{}); ok {
		t.Fatal("normalizeTraeDetail(zero) reported ok; want not-reported")
	}
}

// TestChunkFinishWithUsageZeroDetailStillTerminal 锁定终止帧形态不因 usage 为空
// 而退化：即使明细全零，也必须是带 finish_reason 的单 choice 终止帧。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：同 TestUsageToOpenAIStandardKeys。
func TestChunkFinishWithUsageZeroDetailStillTerminal(t *testing.T) {
	raw, err := chunkFinishWithUsage("req-1", "qwen3.8-max", "stop", usage.Detail{})
	if err != nil {
		t.Fatalf("chunkFinishWithUsage: %v", err)
	}
	m := decodeChunkPayload(t, raw)
	if _, has := m["usage"]; has {
		t.Fatal("zero detail must not emit usage object")
	}
	choices, _ := m["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %v; want exactly 1", m["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v; want stop", choice["finish_reason"])
	}
}

// TestSSEFramedChunksKeepUsage 锁定 SSE 包装不破坏 usage：sseFrameChunks 给每个
// payload 加 "data: " 前缀与空行后，逐帧剥壳仍必须能解出终止帧的 usage。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:02:00；改动原因：确认 usage 在 sse_framed 客户端
// 路径下同样可达。
func TestSSEFramedChunksKeepUsage(t *testing.T) {
	chunks, _, _, _, err := collectTraeStream(strings.NewReader(tokenUsageSSE(realTraeTokenUsage)), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream: %v", err)
	}
	framed := sseFrameChunks(chunks)
	last := framed[len(framed)-1].Payload
	body := strings.TrimSuffix(strings.TrimPrefix(string(last), "data: "), "\n\n")
	u := chunkUsageObject(t, []byte(body))
	if u == nil {
		t.Fatal("usage lost after sse framing")
	}
	if v, _ := u["total_tokens"].(float64); v != 103 {
		t.Fatalf("framed total_tokens = %v; want 103", u["total_tokens"])
	}
}

// 编译期占位：确保 pluginapi 与 usage 在本文件被真实引用（断言零值形态）。
var _ = pluginapi.ExecutorStreamChunk{}
