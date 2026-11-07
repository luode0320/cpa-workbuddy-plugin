package main

import "testing"

// TestIsRequestShapeFailure 验证「客户端请求形状错误」不被当成账号故障。
//
// 生产缺陷回归（2026-11-07）：发一次 role=wizard 的请求，上游返回
// invalid_parameter_error: wizard is not one of [...]，旧逻辑按账号级失败记账并
// 逐个写盘禁用，撞到第 9 个账号才停下，随后正常请求全部 503 auth_unavailable。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07 22:30:00；改动原因：补请求形状豁免的回归覆盖。
func TestIsRequestShapeFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "非法 role（生产实测原文）", body: "upstream 200: tool call failed: invalid_parameter_error:wizard is not one of ['system', 'assistant', 'user', 'tool', 'function']", want: true},
		{name: "参数类型错误", body: "invalid_parameter: max_tokens must be integer", want: true},
		{name: "缺必填字段", body: "missing required field: messages", want: true},
		{name: "空正文不误判", body: "", want: false},
		{name: "空白正文不误判", body: "   ", want: false},
		{name: "模型级故障不归此类", body: "all backends failed", want: false},
		{name: "限流文案不归此类", body: "too many requests", want: false},
		{name: "额度耗尽不归此类", body: "quota exceeded", want: false},
		{name: "普通 5xx 文本不归此类", body: "the model provider is temporarily unavailable", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRequestShapeErrorBody(tc.body); got != tc.want {
				t.Fatalf("isRequestShapeErrorBody(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestClassifyUpstreamFailure_RequestShapeNotAccount 验证分类层把请求形状错误
// 归到 failureScopeNone（不冷却、不禁用、不换号）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07 22:30:00；改动原因：锁定分类层接线。
func TestClassifyUpstreamFailure_RequestShapeNotAccount(t *testing.T) {
	body := "upstream 200: tool call failed: invalid_parameter_error:wizard is not one of ['system', 'assistant', 'user']"
	if got := classifyUpstreamFailure(200, body); got != failureScopeNone {
		t.Fatalf("classifyUpstreamFailure(200, request-shape) = %v, want failureScopeNone", got)
	}
	// 反证：真正的账号级失败仍要记账，不能被这条豁免误伤。
	if got := classifyUpstreamFailure(401, "unauthorized"); got != failureScopeAccount {
		t.Fatalf("classifyUpstreamFailure(401) = %v, want failureScopeAccount", got)
	}
}

// TestValidateOpenAIMessageRoles 验证请求体 role 前置校验。
//
// 生产缺陷回归（2026-11-07）：上游对非法 role 的报错文案不固定（实测出现过
// "is not one of [...]" 与 "The parameter messages.role ... are not valid"
// 两种写法），靠正文匹配拦不住；必须在本地入口拦掉。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07 23:10:00；改动原因：补前置校验的回归覆盖。
func TestValidateOpenAIMessageRoles(t *testing.T) {
	msg := func(role string) map[string]any { return map[string]any{"role": role} }
	cases := []struct {
		name     string
		messages []map[string]any
		wantErr  bool
	}{
		{name: "正常对话", messages: []map[string]any{msg("system"), msg("user")}},
		{name: "工具对话", messages: []map[string]any{msg("user"), msg("assistant"), msg("tool")}},
		{name: "非法 role wizard（生产实测）", messages: []map[string]any{msg("user"), msg("wizard")}, wantErr: true},
		{name: "空 role", messages: []map[string]any{msg("")}, wantErr: true},
		{name: "role 非字符串", messages: []map[string]any{{"role": 123}}, wantErr: true},
		{name: "空列表放行", messages: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOpenAIMessageRoles(tc.messages)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateOpenAIMessageRoles() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
