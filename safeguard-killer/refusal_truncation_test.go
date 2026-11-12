package main

import (
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
