package main

import "testing"

// TestProductionShapesContentFilterEndToEnd 用生产真实分片形态验证「守卫 + 记账」链路。
//
// 这些样本取自 2026-11-12 会话 session-3304f06f 的观测形态：宿主把上游
// content_filter 转成 finish_reason 值下发，分片结构为
// `delta:{} + finish_reason:"content_filter" + usage`。
//
// 关键前提（必须与生产一致）：生产的三处记账调用点都由守卫包裹——
// stream.go:290 的 `if isContentModerationFailure(content)`、
// stream.go:361 的 `if sseErr != ""`、main.go 的完成错误分支。
// 因此本用例必须走「先判据、后记账」的同一顺序；直接调用记账函数会绕过守卫，
// 测出来的失败是测试不忠实而非实现缺陷。
//
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-12；改动原因：新增，用生产形态验证端到端记账链路。
func TestProductionShapesContentFilterEndToEnd(t *testing.T) {
	resetFailover(t)
	// 生产真实分片：宿主 finish_reason 审核终止。
	chunk := `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	// 1. 判据必须命中（这是生产记账前的守卫条件）。
	if !isContentModerationFailure(chunk) {
		t.Fatal("生产形态的审核终止分片必须被识别")
	}
	// 2. 分类必须落模型级（禁号会整池清零）。
	if got := classifyUpstreamFailure(200, chunk); got != failureScopeModelRateLimit {
		t.Fatalf("生产形态分类 = %v, want model_rate_limit", got)
	}
	// 3. 按生产的「守卫 + 记账」顺序走，冷却必须真的写入。
	if isContentModerationFailure(chunk) {
		if !noteSSEErrorFailure("acc-prod", "deepseek-v4-pro", chunk) {
			t.Fatal("生产形态必须记入冷却，否则模型无法退出路由")
		}
	}
	if _, _, ok := failoverStateSnapshot("acc-prod"); !ok {
		t.Fatal("冷却状态必须落盘")
	}

	// 4. 反例：守卫必须拦住，不得进入记账。
	for _, body := range []string{
		`{"choices":[{"index":0,"delta":{"content":"他写下 content error 这两个词"}}]}`,
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	} {
		if isContentModerationFailure(body) {
			t.Fatalf("守卫误命中反例（会导致健康号被误记冷却）: %s", body)
		}
	}
	if _, _, ok := failoverStateSnapshot("acc-fp"); ok {
		t.Fatal("反例不得写入冷却状态")
	}
}
