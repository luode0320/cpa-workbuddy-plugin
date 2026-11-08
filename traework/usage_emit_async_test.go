package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// asyncUsageSSE 组装一条带真实 token_usage 的健康异步流，用于锁定异步协调器
// 终止帧的 usage 承载（正文一帧 + token_usage + done）。
const asyncUsageSSE = "event: output\ndata: {\"response\":\"ok\"}\n\n" +
	"event: token_usage\ndata: " + realTraeTokenUsage + "\n\n" +
	"event: done\ndata: {}\n\n"

// runAsyncCapturingEmit 跑一次异步协调器，收集所有下发分片与关闭次数。
// Emit 注入内存收集器而非真实宿主流，避免依赖宿主回调。
//
// [参数] t: 当前测试；sse: 上游 SSE 文本。
// [返回] emitted: 逐帧 payload；closes: Close 调用次数。
// 最近修改时间：2026-11-08 14:10:00；改动原因：下行 usage 帧缺失的修复验证。
func runAsyncCapturingEmit(t *testing.T, sse string) (emitted []string, closes int) {
	t.Helper()
	deps := traeAsyncStreamDeps{
		Open: func(a *traeAuth, payload map[string]any, authID, hostCallbackID string) (traeAsyncUpstream, int, error) {
			return traeAsyncUpstream{Reader: strings.NewReader(sse), Close: func() {}}, 200, nil
		},
		PickNextAuth: func(currentAuthID string) (string, *traeAuth, bool) {
			return "", nil, false
		},
		Emit: func(streamID string, payload []byte) error {
			emitted = append(emitted, string(payload))
			return nil
		},
		EmitError: func(_ string, message string) error {
			emitted = append(emitted, "chunk.Err:"+message)
			return nil
		},
		Close: func(streamID string) { closes++ },
	}
	runTraeAsyncStream(&traeAuth{UserID: "uid-a"}, "auth-a", traeAsyncStreamContext{
		StreamID:       "stream-usage-test",
		HostCallbackID: "cb-test",
		Model:          "qwen-max-latest",
		UpstreamModel:  "qwen3.8-max",
		Payload:        map[string]any{"prompt": "hi"},
		Started:        time.Now().Add(-time.Second),
		InputChars:     10,
		Budget:         1,
		SessionKey:     "sess-test",
	}, deps)
	return emitted, closes
}

// TestAsyncStreamTerminalChunkCarriesUsage 锁定异步协调器下发的最终终止帧必须
// 携带 usage，且中间正文帧一律不带。异步是生产最常用的流式路径（long-running
// stream），这条断言直接对应 2026-11-08 new-api Tokens 列显示 "-" 的现场。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:10:00；改动原因：下行 usage 帧缺失的修复验证。
func TestAsyncStreamTerminalChunkCarriesUsage(t *testing.T) {
	emitted, closes := runAsyncCapturingEmit(t, asyncUsageSSE)
	if closes != 1 {
		t.Fatalf("Close calls = %d; want exactly 1", closes)
	}
	var usageSeen map[string]any
	for i, payload := range emitted {
		if strings.HasPrefix(payload, "chunk.Err:") {
			t.Fatalf("unexpected error frame at %d: %s", i, payload)
		}
		u := chunkUsageObject(t, []byte(payload))
		if i == len(emitted)-1 {
			usageSeen = u
			continue
		}
		if u != nil {
			t.Fatalf("frame[%d] must not carry usage: %v", i, u)
		}
	}
	if usageSeen == nil {
		t.Fatal("async terminal frame carries no usage; new-api will log Tokens: -")
	}
	if v, _ := usageSeen["prompt_tokens"].(float64); v != 69 {
		t.Fatalf("terminal prompt_tokens = %v; want 69", usageSeen["prompt_tokens"])
	}
	if v, _ := usageSeen["completion_tokens"].(float64); v != 34 {
		t.Fatalf("terminal completion_tokens = %v; want 34", usageSeen["completion_tokens"])
	}
	if v, _ := usageSeen["total_tokens"].(float64); v != 103 {
		t.Fatalf("terminal total_tokens = %v; want 103", usageSeen["total_tokens"])
	}
	var tail map[string]any
	if err := json.Unmarshal([]byte(emitted[len(emitted)-1]), &tail); err != nil {
		t.Fatalf("decode terminal frame: %v", err)
	}
	choices, _ := tail["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("terminal choices = %v; want exactly 1", tail["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("terminal finish_reason = %v; want stop", choice["finish_reason"])
	}
}

// TestAsyncStreamTerminalChunkUsageEstimatesWhenAbsent 锁定异步路径在上游未发
// token_usage 事件时仍必须给终止帧挂估算 usage，否则该路径流量继续零计费。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-08 14:10:00；改动原因：同 TestAsyncStreamTerminalChunkCarriesUsage。
func TestAsyncStreamTerminalChunkUsageEstimatesWhenAbsent(t *testing.T) {
	emitted, _ := runAsyncCapturingEmit(t, testHealthySSE)
	if len(emitted) == 0 {
		t.Fatal("no frames emitted")
	}
	u := chunkUsageObject(t, []byte(emitted[len(emitted)-1]))
	if u == nil {
		t.Fatal("async terminal frame carries no estimated usage")
	}
	if v, _ := u["total_tokens"].(float64); v <= 0 {
		t.Fatalf("terminal total_tokens = %v; want > 0", u["total_tokens"])
	}
}
