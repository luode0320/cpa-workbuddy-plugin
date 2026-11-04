package main

import "testing"

// TestUnifiedOutputCap 锁定统一输出上限常量与流式缺省值的对齐关系。
//
// streamDefaultMaxTokens 是 traework 唯一直接引用非推理统一上限的请求体路径，
// 客户端不传 max_tokens 时由它决定上游上限；两处若漂移会导致流式长任务被提前截断。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-04；改动原因：非推理上限由 64000 抬升至 128000，补锁定用例。
func TestUnifiedOutputCap(t *testing.T) {
	if defaultMaxOutputTokens != 128000 {
		t.Fatalf("defaultMaxOutputTokens = %d, want 128000", defaultMaxOutputTokens)
	}
	if defaultReasoningMaxTokens != 128000 {
		t.Fatalf("defaultReasoningMaxTokens = %d, want 128000", defaultReasoningMaxTokens)
	}
	if streamDefaultMaxTokens != defaultMaxOutputTokens {
		t.Fatalf("streamDefaultMaxTokens = %d, want %d", streamDefaultMaxTokens, defaultMaxOutputTokens)
	}
	if minMaxTokensFloor != 32768 {
		t.Fatalf("minMaxTokensFloor = %d, want 32768", minMaxTokensFloor)
	}
}
