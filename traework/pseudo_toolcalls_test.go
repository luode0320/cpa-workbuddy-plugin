// pseudo_toolcalls_test.go — P1 止血回归：含结构化工具调用的短流永不判伪完成。
// 上游取证（2026-09-03 直连）：正常工具调用流 = 少量 reasoning + tool_calls 数组
// （function_call 结构）+ done，全程 ~2s、completion ~43 tokens。此前插件丢弃
// tool_calls 信号后此类流被 isPseudoCompletion 误判为伪完成 → 丢弃重试 → 池耗尽
// （生产 stream 3206 实证），工具型任务全部失败。
package main

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// toolCallSSE 是取证所得的真实上游流形态：短 reasoning + 结构化 tool_calls + done。
// tool_calls 元素使用上游原始 schema（function_call 字段），与 normalizeOutput 直接交互。
const toolCallSSE = `event: output
data: {"response":"","reasoning_content":"I need to call the tool to answer.","tool_calls":null}

event: output
data: {"response":"","reasoning_content":null,"tool_calls":[{"index":0,"id":"call_0f9d54575cc0484bb26165e9","type":"function","function_call":{"name":"get_current_time","arguments":"{}","partial_arguments":null,"namespace":null}}]}

event: done
data: {}

`

// TestCollectTraeStreamReportsToolCalls 锁定 collect 路径把 hasToolCalls 信号带出：
// 含结构化 tool_calls 的流返回 hasToolCalls=true，且不因正文为空被当作无效响应。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-03；改动原因：P1 止血——同步路径伪完成豁免依赖该信号。
func TestCollectTraeStreamReportsToolCalls(t *testing.T) {
	chunks, hasToolCalls, _, err := collectTraeStream(strings.NewReader(toolCallSSE), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream error = %v; want nil (tool-call stream is valid)", err)
	}
	if !hasToolCalls {
		t.Fatalf("hasToolCalls = false; want true (stream carried a real tool_calls payload)")
	}
	// 至少要有 done 收尾分片。
	if len(chunks) == 0 {
		t.Fatalf("chunks empty; want a terminal chunk after done")
	}
}

// TestCollectTraeStreamNullToolCallsNotSignal 锁定 tool_calls:null（上游常态填充）
// 不产生误信号——hasToolCalls 只对真实载荷为 true。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-03；改动原因：normalizeOutput 过滤 null/[] 后 recordOutput 才能正确置位。
func TestCollectTraeStreamNullToolCallsNotSignal(t *testing.T) {
	sse := `event: output
data: {"response":"正常正文","reasoning_content":"","tool_calls":null}

event: done
data: {}

`
	_, hasToolCalls, _, err := collectTraeStream(strings.NewReader(sse), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream error = %v; want nil", err)
	}
	if hasToolCalls {
		t.Fatalf("hasToolCalls = true; want false (tool_calls:null is upstream padding)")
	}
}

// shortContentChunk 构造一个极短正文分片（工具型短流的正文特征）。
func shortContentChunk(t *testing.T, content string) pluginapi.ExecutorStreamChunk {
	t.Helper()
	return pluginapi.ExecutorStreamChunk{
		Payload: []byte(`{"choices":[{"delta":{"content":"` + content + `"}}]}`),
	}
}

// TestIsPseudoCompletionToolCallsExempt 锁定豁免语义：同样的短输出 + 长输入，
// hasToolCalls=true 永不判伪（工具调用是正常终止），false 时维持原判伪行为。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-03；改动原因：P1 止血——工具型短停不是账号限流信号。
func TestIsPseudoCompletionToolCallsExempt(t *testing.T) {
	short := []pluginapi.ExecutorStreamChunk{
		shortContentChunk(t, "我先读取一下项目文件"),
	}
	if isPseudoCompletion(short, 5000, true) {
		t.Fatalf("isPseudoCompletion(hasToolCalls=true) = true; want false (tool-call stream exempt)")
	}
	if !isPseudoCompletion(short, 5000, false) {
		t.Fatalf("isPseudoCompletion(hasToolCalls=false) = false; want true (original pseudo detection preserved)")
	}
	// 短输入 + 短答不受影响（原语义）。
	if isPseudoCompletion(short, 10, false) {
		t.Fatalf("isPseudoCompletion(input=10) = true; want false (short prompt short answer is healthy)")
	}
}

// TestPumpTraeStreamAttemptToolCallStreamNotPseudo 锁定异步泵路径端到端行为：
// 长输入 + 短正文 + tool_calls + done 的流，判伪被豁免 → 内容照常下发（Emitted），
// 而非被吞成 Pseudo 让协调器换号重试。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-09-03；改动原因：P1 止血——生产 stream 3206 型误杀闭环。
func TestPumpTraeStreamAttemptToolCallStreamNotPseudo(t *testing.T) {
	emitted := 0
	ctx := traeStreamPumpContext{
		Model:      "qwen3.8-max",
		StatusCode: 200,
		InputChars: 5000, // 长输入：无豁免时必判伪
	}
	result := pumpTraeStreamAttempt(strings.NewReader(toolCallSSE), ctx, "req-tool", func(payload []byte) error {
		emitted++
		return nil
	})
	if result.Err != nil {
		t.Fatalf("pump error = %v; want nil", result.Err)
	}
	if result.Pseudo {
		t.Fatalf("Pseudo = true; want false (tool-call stream must not be treated as pseudo completion)")
	}
	if emitted == 0 {
		t.Fatalf("emitted = 0; want > 0 (tool-call stream content must reach the client)")
	}
}
