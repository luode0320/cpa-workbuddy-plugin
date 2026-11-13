package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHasTruncatedOrReasoningOutput 验证截断与推理内容识别覆盖非流式、流式两类响应体。
// 判据防的是思考模型把输出配额烧在推理链上、正文为空时被误判成拒绝。
// 无副作用，纯函数判定。
func TestHasTruncatedOrReasoningOutput(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "finish_reason=length 视为截断",
			body: `{"choices":[{"finish_reason":"length","message":{"content":""}}]}`,
			want: true,
		},
		{
			name: "非流式仅推理内容",
			body: `{"choices":[{"finish_reason":"stop","message":{"content":"","reasoning_content":"先分析需求，再给结论"}}]}`,
			want: true,
		},
		{
			name: "非流式 reasoning 字段同样识别",
			body: `{"choices":[{"finish_reason":"stop","message":{"content":"","reasoning":"thinking..."}}]}`,
			want: true,
		},
		{
			name: "流式分片 delta 携带推理内容",
			body: `{"choices":[{"delta":{"reasoning_content":"正在思考"}}]}`,
			want: true,
		},
		{
			name: "真空白响应不命中",
			body: `{"choices":[{"finish_reason":"stop","message":{"content":""}}]}`,
			want: false,
		},
		{
			name: "有正常正文不算截断",
			body: `{"choices":[{"finish_reason":"stop","message":{"content":"幂等性指同一操作重复执行结果一致"}}]}`,
			want: false,
		},
		{
			name: "空推理内容不命中",
			body: `{"choices":[{"finish_reason":"stop","message":{"content":"","reasoning_content":"   "}}]}`,
			want: false,
		},
		{
			name: "非 JSON 输入不命中",
			body: `not-json`,
			want: false,
		},
		{
			name: "responses 推理摘要增量视为推理内容",
			body: `{"type":"response.reasoning_summary_text.delta","sequence_number":5,"item_id":"rs_x_0","output_index":0,"summary_index":0,"delta":"我们需要"}`,
			want: true,
		},
		{
			name: "responses 推理正文增量视为推理内容",
			body: `{"type":"response.reasoning_text.delta","sequence_number":5,"item_id":"rs_x_0","output_index":0,"delta":"先读题"}`,
			want: true,
		},
		{
			name: "responses 推理摘要完成帧携带文本",
			body: `{"type":"response.reasoning_summary_text.done","sequence_number":29,"item_id":"rs_x_0","output_index":0,"summary_index":0,"text":"我们需要回答中文"}`,
			want: true,
		},
		{
			name: "responses 推理摘要空增量不命中",
			body: `{"type":"response.reasoning_summary_text.delta","sequence_number":5,"item_id":"rs_x_0","delta":"   "}`,
			want: false,
		},
		{
			name: "responses 推理 item 完成帧带 summary",
			body: `{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_x_0","type":"reasoning","summary":[{"type":"summary_text","text":"我们需要回答中文"}]}}`,
			want: true,
		},
		{
			name: "responses 推理 item 起始帧 summary 为空不命中",
			body: `{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_x_0","type":"reasoning","status":"in_progress","summary":[]}}`,
			want: false,
		},
		{
			name: "responses 顶层 max_output_tokens 截断",
			body: `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"model":"deepseek-v4-pro"}}`,
			want: true,
		},
		{
			name: "responses content_filter 不算截断",
			body: `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`,
			want: false,
		},
		{
			name: "responses 非流式 output 含推理项",
			body: `{"object":"response","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"思考过程"}]}]}`,
			want: true,
		},
		{
			name: "responses 非流式空输出不命中",
			body: `{"object":"response","status":"completed","output":[]}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasTruncatedOrReasoningOutput([]byte(tc.body)); got != tc.want {
				t.Fatalf("hasTruncatedOrReasoningOutput() = %v, want %v; body=%s", got, tc.want, tc.body)
			}
		})
	}
}

// TestCheckNonStreamResponse_TruncatedAndReasoningPass 验证截断与仅推理内容的响应被放行。
// 判据防的是思考模型正文为空时触发换模型，把破甲降级成模型切换。
// 无副作用。
func TestCheckNonStreamResponse_TruncatedAndReasoningPass(t *testing.T) {
	pass := []struct {
		name string
		body string
	}{
		{
			name: "配额截断放行",
			body: `{"choices":[{"finish_reason":"length","message":{"content":""}}]}`,
		},
		{
			name: "仅推理内容放行",
			body: `{"choices":[{"finish_reason":"stop","message":{"content":"","reasoning_content":"长思考链"}}]}`,
		},
		{
			name: "推理内容加截断放行",
			body: `{"choices":[{"finish_reason":"length","message":{"content":"","reasoning_content":"长思考链"}}]}`,
		},
		{
			name: "responses 仅推理摘要放行",
			body: `{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_x_0","type":"reasoning","summary":[{"type":"summary_text","text":"我们需要回答中文"}]}}`,
		},
		{
			name: "responses 配额截断放行",
			body: `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`,
		},
	}
	for _, tc := range pass {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkNonStreamResponse([]byte(tc.body)); err != nil {
				t.Fatalf("checkNonStreamResponse() 应放行, got error: %v; body=%s", err, tc.body)
			}
		})
	}

	fail := []struct {
		name string
		body string
		want string
	}{
		{
			name: "真空白仍按空响应拒绝",
			body: `{"choices":[{"finish_reason":"stop","message":{"content":""}}]}`,
			want: "empty response (no text or tool calls)",
		},
		{
			name: "content_filter 仍判拒绝",
			body: `{"choices":[{"finish_reason":"content_filter","message":{"content":"","reasoning_content":"思考"}}]}`,
			want: "refusal (content_filter)",
		},
		{
			name: "responses 真空白仍按空响应拒绝",
			body: `{"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[],"model":"deepseek-v4-pro"}}`,
			want: "empty response (no text or tool calls)",
		},
		{
			name: "responses content_filter 仍判拒绝",
			body: `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`,
			want: "refusal (content_filter)",
		},
	}
	for _, tc := range fail {
		t.Run(tc.name, func(t *testing.T) {
			err := checkNonStreamResponse([]byte(tc.body))
			if err == nil {
				t.Fatalf("checkNonStreamResponse() 应拒绝, got nil; body=%s", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkNonStreamResponse() error = %q, want 包含 %q", err.Error(), tc.want)
			}
		})
	}
}

// TestPrefetchStream_ResponsesReasoningOnlyPasses 验证 responses 推理通道下正文为空的流不再被判成空响应。
// 判据防的是思考模型把 max_output_tokens 全烧在推理摘要上、一个 output_text 都不发时，
// 预取阶段误判成「模型没干活」从而触发换模型或兜底提示。
// 桩只模拟宿主流式读取，不触碰网络与磁盘。
func TestPrefetchStream_ResponsesReasoningOnlyPasses(t *testing.T) {
	chunks := []string{
		`data: {"type":"response.created","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"in_progress","output":[],"model":"deepseek-v4-pro"}}`,
		`data: {"type":"response.in_progress","sequence_number":2,"response":{"id":"resp_1","status":"in_progress","output":[],"model":"deepseek-v4-pro"}}`,
		`data: {"type":"response.output_item.added","sequence_number":3,"output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"in_progress","summary":[]}}`,
		`data: {"type":"response.reasoning_summary_text.delta","sequence_number":4,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"我们需要"}`,
		`data: {"type":"response.reasoning_summary_text.delta","sequence_number":5,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"回答中文"}`,
		`data: {"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"rs_1","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":"我们需要回答中文"}]}}`,
		`data: {"type":"response.incomplete","sequence_number":7,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"model":"deepseek-v4-pro"}}`,
	}
	index := 0
	stubHost(t, func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostModelStreamRead:
			if index >= len(chunks) {
				return json.Marshal(hostStreamReadResponse{Done: true})
			}
			payloadText := chunks[index] + "\n\n"
			index++
			return json.Marshal(hostStreamReadResponse{Payload: []byte(payloadText), Done: index >= len(chunks)})
		case methodHostModelStreamClose:
			return json.RawMessage(`{}`), nil
		}
		t.Fatalf("unexpected host call %s", method)
		return nil, nil
	})

	prefetched, done, errPrefetch := prefetchStream("src", "cb", true, 128)
	if errPrefetch != nil {
		t.Fatalf("prefetchStream() 应放行推理内容, got error: %v", errPrefetch)
	}
	if len(prefetched) == 0 {
		t.Fatalf("prefetchStream() 未返回任何分片")
	}
	// 推理内容到达后应提前放行，不必读到末帧
	if done {
		t.Fatalf("prefetchStream() 应在推理内容到达后提前放行, 实际读到末帧 done=%v", done)
	}
	if index >= len(chunks) {
		t.Fatalf("prefetchStream() 应提前停止读取, index=%d total=%d", index, len(chunks))
	}
}

// TestPrefetchStream_ResponsesTrulyEmptyStillFails 验证 responses 通道真正没有任何产出的流仍判空响应。
// 判据防的是为了让推理内容放行而把空响应判据整体删掉，导致真失败也被当成成功。
func TestPrefetchStream_ResponsesTrulyEmptyStillFails(t *testing.T) {
	chunks := []string{
		`data: {"type":"response.created","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"in_progress","output":[],"model":"deepseek-v4-pro"}}`,
	}
	index := 0
	stubHost(t, func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostModelStreamRead:
			if index >= len(chunks) {
				return json.Marshal(hostStreamReadResponse{Done: true})
			}
			payloadText := chunks[index] + "\n\n"
			index++
			return json.Marshal(hostStreamReadResponse{Payload: []byte(payloadText), Done: index >= len(chunks)})
		case methodHostModelStreamClose:
			return json.RawMessage(`{}`), nil
		}
		t.Fatalf("unexpected host call %s", method)
		return nil, nil
	})

	if _, _, errPrefetch := prefetchStream("src", "cb", true, 128); errPrefetch == nil {
		t.Fatalf("prefetchStream() 应对真空白响应返回错误, got nil")
	} else if !strings.Contains(errPrefetch.Error(), "empty response") {
		t.Fatalf("prefetchStream() error = %q, want 包含 empty response", errPrefetch.Error())
	}
}
