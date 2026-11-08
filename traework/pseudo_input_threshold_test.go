// pseudo_input_threshold_test.go 锁定伪完成判定的输入阈值边界。
//
// 生产缺陷回归（2026-11-09）：旧阈值 200 字符过低，把「长任务但模型正常短答」
// 误判为伪完成 —— 实测单请求连续 pseudo retry 11 次换遍 11 个账号全部判伪，
// 最终 pool exhausted；632 条伪完成样本的 input 中位仅 188 字符。
package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// pseudoChunk 构造一个只含指定正文的 OpenAI chunk。
// [参数] content：正文文本。
// [返回] 分片。
// 最近修改时间：2026-11-09 00:45:00；改动原因：新增。
func pseudoChunk(content string) pluginapi.ExecutorStreamChunk {
	payload, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": content}}},
	})
	return pluginapi.ExecutorStreamChunk{Payload: payload}
}

// TestPseudoCompletionInputThresholdBoundary 验证阈值边界两侧的行为。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 00:45:00；改动原因：新增，锁住 20000 字符阈值。
func TestPseudoCompletionInputThresholdBoundary(t *testing.T) {
	short := []pluginapi.ExecutorStreamChunk{pseudoChunk("好的")}

	// 1. 短输入（低于阈值）：正常短答，不得判伪。
	if isPseudoCompletion(short, pseudoCompletionMinInputChars-1, false) {
		t.Fatalf("input=%d 低于阈值，不得判伪完成", pseudoCompletionMinInputChars-1)
	}

	// 2. 长输入（达到阈值）：短答判伪 —— 这是上游掐断长任务的真实签名。
	if !isPseudoCompletion(short, pseudoCompletionMinInputChars, false) {
		t.Fatalf("input=%d 达到阈值，应判伪完成", pseudoCompletionMinInputChars)
	}

	// 3. 阈值本身必须足够高，避免把生产短请求误判。
	//    生产伪完成样本 input 中位 188 字符，远低于阈值。
	if pseudoCompletionMinInputChars < 10000 {
		t.Fatalf("阈值 %d 过低：生产短请求会被误判为伪完成并触发换号", pseudoCompletionMinInputChars)
	}
}
