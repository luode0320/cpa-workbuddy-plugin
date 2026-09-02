package main

// toolchain_test.go — P0 完整工具调用链路回归（2026-09-03 取证后锁定）。
// 覆盖：①上行 tools 规整（parameters object→string）；②tool_choice 规范化；
// ③历史消息 function→function_call 键 + role=tool 保留；④下行快照式
// tool_calls → OpenAI function 键（流式分片 / 非流式聚合 / finish=tool_calls）。

import (
	"encoding/json"
	"strings"
	"testing"
)

// upstreamToolCallEvent 是 2026-09-03 直连取证的真实 output 事件载荷
// （qwen3.8-max 双工具调用，单事件快照承载 index 0/1 两个完整调用）。
const upstreamToolCallEvent = `{"response":"","reasoning_content":null,"tool_calls":[{"function_call":{"arguments":"{}","name":"get_current_time","namespace":null,"partial_arguments":null},"id":"call_4851a3b8846a458a9e1c6247","index":0,"type":"function"},{"function_call":{"arguments":"{\"min\":1}","name":"get_random_number","namespace":null,"partial_arguments":null},"id":"call_33b3bab10eaf414a95c3cfce","index":1,"type":"function"}],"multimodal_contents":null,"phase":null}`

func TestTraeToolsFromOpenAI_StringifiesParameters(t *testing.T) {
	origParams := map[string]any{"type": "object", "properties": map[string]any{}}
	inputs := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":       "get_current_time",
				"parameters": origParams,
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":       "get_random_number",
				"parameters": `{"type":"object","properties":{}}`, // 已是字符串原样保留
			},
		},
		{"type": "web_search"},         // 非 function 类型剔除
		{"function": map[string]any{}}, // 缺 name 剔除
	}
	tools := traeToolsFromOpenAI(inputs)
	if len(tools) != 2 {
		t.Fatalf("traeToolsFromOpenAI = %d tools, want 2", len(tools))
	}
	for _, td := range tools {
		fn, _ := td["function"].(map[string]any)
		p, ok := fn["parameters"].(string)
		if !ok {
			t.Fatalf("parameters = %T, want string (upstream strong type)", fn["parameters"])
		}
		if !strings.Contains(p, `"type":"object"`) {
			t.Fatalf("parameters = %s, want object schema", p)
		}
	}
	// 输入 map 不得被改写（深拷贝语义）。
	if orig, ok := inputs[0]["function"].(map[string]any); ok {
		if _, isStr := orig["parameters"].(string); isStr {
			t.Fatalf("traeToolsFromOpenAI mutated the caller's input map")
		}
	}
}

func TestNormalizeTraeToolChoice(t *testing.T) {
	cases := []struct {
		name          string
		in            any
		wantChoice    string
		wantSuppress  bool
	}{
		{name: "nil no-op", in: nil, wantChoice: "", wantSuppress: false},
		{name: "auto string", in: "auto", wantChoice: "auto", wantSuppress: false},
		{name: "required string", in: "required", wantChoice: "required", wantSuppress: false},
		{name: "function name string", in: "get_current_time", wantChoice: "get_current_time", wantSuppress: false},
		{name: "none string suppresses", in: "none", wantChoice: "", wantSuppress: true},
		{name: "auto object", in: map[string]any{"type": "auto"}, wantChoice: "auto", wantSuppress: false},
		{name: "function object with name", in: map[string]any{"type": "function", "function": map[string]any{"name": "get_random_number"}}, wantChoice: "get_random_number", wantSuppress: false},
		{name: "function object missing name falls auto", in: map[string]any{"type": "function"}, wantChoice: "auto", wantSuppress: false},
		{name: "none object suppresses", in: map[string]any{"type": "none"}, wantChoice: "", wantSuppress: true},
		{name: "unknown object suppresses", in: map[string]any{"type": "hack"}, wantChoice: "", wantSuppress: true},
		{name: "garbage value suppresses", in: []int{1}, wantChoice: "", wantSuppress: true},
	}
	for _, tc := range cases {
		gotChoice, gotSuppress := normalizeTraeToolChoice(tc.in)
		if gotChoice != tc.wantChoice || gotSuppress != tc.wantSuppress {
			t.Errorf("%s: normalizeTraeToolChoice = (%q, %v), want (%q, %v)", tc.name, gotChoice, gotSuppress, tc.wantChoice, tc.wantSuppress)
		}
	}
}

func TestToTraeMessages_KeepsToolSemantics(t *testing.T) {
	// 形态 C 取证消息：assistant 历史（function 键标准 OpenAI）+ role=tool 结果。
	msgs := []map[string]any{
		{"role": "user", "content": "现在几点？"},
		{
			"role":    "assistant",
			"content": nil, // OpenAI SDK 工具调用消息 content 为 null
			"tool_calls": []any{
				map[string]any{
					"id":   "call_mt_a1",
					"type": "function",
					"function": map[string]any{
						"name":      "get_current_time",
						"arguments": "{}",
					},
				},
			},
		},
		{"role": "tool", "tool_call_id": "call_mt_a1", "content": "现在是00:55"},
	}
	out := toTraeMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("toTraeMessages = %d messages, want 3 (tool messages must survive)", len(out))
	}
	// assistant：function → function_call 键。
	asm := out[1]
	tcs, ok := asm["tool_calls"].([]map[string]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("assistant tool_calls = %#v, want 1 call", asm["tool_calls"])
	}
	if _, hasFn := tcs[0]["function"]; hasFn {
		t.Fatalf("assistant tool_calls still uses OpenAI function key: %#v", tcs[0])
	}
	fc, ok := tcs[0]["function_call"].(map[string]any)
	if !ok || fc["name"] != "get_current_time" || fc["arguments"] != "{}" {
		t.Fatalf("function_call = %#v, want name+arguments intact", tcs[0]["function_call"])
	}
	// content null → 空 parts 数组（上游 LLMRawMessage 兼容）。
	content, ok := asm["content"].([]map[string]any)
	if !ok || len(content) != 0 {
		t.Fatalf("assistant content = %#v, want empty parts array", asm["content"])
	}
	// role=tool：role/tool_call_id/content 全保留。
	toolMsg := out[2]
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_mt_a1" {
		t.Fatalf("tool message = %#v, want role=tool with tool_call_id", toolMsg)
	}
}

func TestTraeToolCallsToOpenAI_SnapshotToDelta(t *testing.T) {
	calls := traeToolCallsToOpenAI(json.RawMessage(`[{"index":0,"id":"call_1","type":"function","function_call":{"name":"a","arguments":"{}","partial_arguments":null}},{"index":1,"id":"call_2","type":"function","function_call":{"name":"b","arguments":"","partial_arguments":"{\"x\":1}"}}]`))
	if len(calls) != 2 {
		t.Fatalf("traeToolCallsToOpenAI = %d calls, want 2", len(calls))
	}
	// 键必须已转 function。
	if _, hasFC := calls[0]["function_call"]; hasFC {
		t.Fatalf("delta still carries function_call key: %#v", calls[0])
	}
	fn0, _ := calls[0]["function"].(map[string]any)
	if fn0["name"] != "a" || fn0["arguments"] != "{}" {
		t.Fatalf("call[0].function = %#v, want name=a arguments={}", fn0)
	}
	// arguments 缺省时回退 partial_arguments。
	fn1, _ := calls[1]["function"].(map[string]any)
	if fn1["arguments"] != `{"x":1}` {
		t.Fatalf("call[1].function.arguments = %#v, want fallback to partial_arguments", fn1["arguments"])
	}
	if got := traeToolCallsToOpenAI(json.RawMessage(`null`)); got != nil {
		t.Fatalf("null input = %#v, want nil", got)
	}
}

func TestToolCallDeltaChunk(t *testing.T) {
	raw, err := toolCallDeltaChunk("req-p0", "qwen3.8-max", []map[string]any{
		{"index": 0, "function": map[string]any{"name": "get_current_time", "arguments": "{}"}},
	})
	if err != nil || raw == nil {
		t.Fatalf("toolCallDeltaChunk = %v, %v; want a chunk", raw, err)
	}
	var ch struct {
		Object  string `json:"object"`
		Choices []struct {
			Delta struct {
				ToolCalls []map[string]any `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &ch); err != nil {
		t.Fatalf("decode chunk: %v", err)
	}
	if ch.Object != "chat.completion.chunk" || len(ch.Choices) != 1 || len(ch.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("chunk shape = %s", raw)
	}
}

// toolFlowSSE 组装取证形态的完整工具流：推理短流 + 快照 tool_calls + done。
func toolFlowSSE(toolEvent string) string {
	return "event: output\ndata: {\"response\":\"\",\"reasoning_content\":\"I need to call the tool.\",\"tool_calls\":null}\n\n" +
		"event: output\ndata: " + toolEvent + "\n\n" +
		"event: done\ndata: {}\n\n"
}

func TestCollectTraeStream_EmitsToolCallDeltaAndFinish(t *testing.T) {
	sse := toolFlowSSE(upstreamToolCallEvent)
	chunks, hasToolCalls, err := collectTraeStream(strings.NewReader(sse), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream error = %v", err)
	}
	if !hasToolCalls {
		t.Fatalf("hasToolCalls = false, want true")
	}
	var sawToolCallDelta, sawToolFinish bool
	var toolDeltaCount int
	for _, c := range chunks {
		var ch struct {
			Choices []struct {
				Delta struct {
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(c.Payload, &ch); err != nil {
			t.Fatalf("chunk decode: %v", err)
		}
		for _, c2 := range ch.Choices {
			if len(c2.Delta.ToolCalls) > 0 {
				sawToolCallDelta = true
				toolDeltaCount += len(c2.Delta.ToolCalls)
			}
			if c2.FinishReason == "tool_calls" {
				sawToolFinish = true
			}
		}
	}
	if !sawToolCallDelta {
		t.Fatalf("no tool_calls delta emitted in stream")
	}
	if toolDeltaCount != 2 {
		t.Fatalf("tool_calls delta count = %d, want 2 (snapshot carries two calls)", toolDeltaCount)
	}
	if !sawToolFinish {
		t.Fatalf("finish_reason = stop/length, want tool_calls")
	}
}

func TestAggregateTraeCompletion_FoldsToolCalls(t *testing.T) {
	sse := toolFlowSSE(upstreamToolCallEvent)
	completion, err := aggregateTraeCompletion(strings.NewReader(sse), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("aggregateTraeCompletion error = %v", err)
	}
	var out struct {
		Choices []struct {
			Message struct {
				ToolCalls []map[string]any `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(completion, &out); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(out.Choices))
	}
	m := out.Choices[0].Message
	if len(m.ToolCalls) != 2 {
		t.Fatalf("message.tool_calls = %d, want 2", len(m.ToolCalls))
	}
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", out.Choices[0].FinishReason)
	}
	// 按 index 排序后校验（上游事件 index 0/1 顺序）。
	first, _ := m.ToolCalls[0]["function"].(map[string]any)
	second, _ := m.ToolCalls[1]["function"].(map[string]any)
	if first["name"] != "get_current_time" || second["name"] != "get_random_number" {
		t.Fatalf("tool order = %v / %v, want get_current_time then get_random_number", first["name"], second["name"])
	}
}

func TestPumpTraeStreamAttempt_ReportsHasToolCalls(t *testing.T) {
	var emitted [][]byte
	result := pumpTraeStreamAttempt(strings.NewReader(toolFlowSSE(upstreamToolCallEvent)), traeStreamPumpContext{
		Model: "qwen3.8-max", StatusCode: 200, InputChars: 3000,
	}, "p0-pump", func(payload []byte) error {
		emitted = append(emitted, payload)
		return nil
	})
	if result.Err != nil {
		t.Fatalf("pump error = %v", result.Err)
	}
	if !result.HasToolCalls {
		t.Fatalf("HasToolCalls = false, want true")
	}
	if result.Pseudo {
		t.Fatalf("tool-call stream flagged pseudo — P1 exemption broken")
	}
	// 工具流短正文不达 600 门槛：gate 必须被 hasToolCalls 提前打开并放行。
	var toolCallsSeen bool
	for _, p := range emitted {
		if strings.Contains(string(p), `"tool_calls"`) {
			toolCallsSeen = true
		}
	}
	if !result.Emitted || !toolCallsSeen {
		t.Fatalf("tool-call stream not emitted (emitted=%v, tool_calls seen=%v, chunks=%d)", result.Emitted, toolCallsSeen, len(emitted))
	}
}

// TestCollectTraeStream_PlainTextStreamUnchanged 锁定无工具流不受 P0 改动影响：
// 仍以 stop 收尾、无 tool_calls 分片。
func TestCollectTraeStream_PlainTextStreamUnchanged(t *testing.T) {
	sse := "event: output\ndata: {\"response\":\"你好\"}\n\nevent: output\ndata: {\"response\":\"世界\"}\n\nevent: done\ndata: {}\n\n"
	chunks, hasToolCalls, err := collectTraeStream(strings.NewReader(sse), "qwen3.8-max", 200)
	if err != nil {
		t.Fatalf("collectTraeStream error = %v", err)
	}
	if hasToolCalls {
		t.Fatalf("hasToolCalls = true, want false for plain stream")
	}
	var finish string
	for _, c := range chunks {
		var ch struct {
			Choices []struct {
				Delta struct {
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(c.Payload, &ch); err == nil {
			for _, c2 := range ch.Choices {
				if len(c2.Delta.ToolCalls) > 0 {
					t.Fatalf("plain stream contains tool_calls delta: %s", c.Payload)
				}
				if c2.FinishReason != "" {
					finish = c2.FinishReason
				}
			}
		}
	}
	if finish != "stop" {
		t.Fatalf("plain stream finish = %q, want stop", finish)
	}
}
