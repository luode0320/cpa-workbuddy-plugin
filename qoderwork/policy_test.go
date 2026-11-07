package main

import "testing"

// TestIsRequestShapeFailure 验证「客户端请求形状错误」不被当成账号故障。
//
// 生产缺陷回归（2026-11-07）：发一次 role=wizard 的请求，上游返回
// provider_error，旧逻辑按账号级失败记账并逐个写盘禁用，一次请求打穿整池，
// 随后 5/5 次正常请求全部 503 auth_unavailable。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07 22:30:00；改动原因：补请求形状豁免的回归覆盖。
func TestIsRequestShapeFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "非法 role（生产实测原文）", body: "upstream 200: provider_error: wizard is not one of ['system', 'assistant', 'user']", want: true},
		{name: "参数类型错误", body: "invalid_parameter: max_tokens must be integer", want: true},
		{name: "缺必填字段", body: "missing required field: messages", want: true},
		{name: "tool call failed 包装", body: "upstream 200: tool call failed: invalid request", want: true},
		{name: "空正文不误判", body: "", want: false},
		{name: "空白正文不误判", body: "   ", want: false},
		{name: "模型级故障不归此类", body: "all backends failed", want: false},
		{name: "限流文案不归此类", body: "too many requests, retry_after 30", want: false},
		{name: "额度耗尽不归此类", body: "quota exceeded", want: false},
		{name: "普通 5xx 文本不归此类", body: "the model provider is temporarily unavailable", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRequestShapeFailure(tc.body); got != tc.want {
				t.Fatalf("isRequestShapeFailure(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestValidateOpenAIMessages 验证请求体 role 前置校验。
//
// 生产缺陷回归（2026-11-07）：上游对非法 role 的报错文案不固定（qoderwork 只回
// 无信息量的 provider_error），靠正文匹配拦不住；必须在本地入口拦掉，
// 否则一次畸形请求会把池内账号逐个写盘禁用。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07 23:10:00；改动原因：补前置校验的回归覆盖。
func TestValidateOpenAIMessages(t *testing.T) {
	cases := []struct {
		name     string
		messages []openAIMessage
		wantErr  bool
	}{
		{name: "正常对话", messages: []openAIMessage{{Role: "system"}, {Role: "user"}}},
		{name: "工具对话", messages: []openAIMessage{{Role: "user"}, {Role: "assistant"}, {Role: "tool"}}},
		{name: "developer 角色", messages: []openAIMessage{{Role: "developer"}, {Role: "user"}}},
		{name: "非法 role wizard（生产实测）", messages: []openAIMessage{{Role: "user"}, {Role: "wizard"}}, wantErr: true},
		{name: "空 role", messages: []openAIMessage{{Role: ""}}, wantErr: true},
		{name: "空列表放行", messages: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOpenAIMessages(tc.messages)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateOpenAIMessages() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
