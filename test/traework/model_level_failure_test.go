package main

import (
	"testing"
)

// 模型级/后端池级故障不得记成账号故障（2026-10-25）。
//
// 生产机理：Trae 上游用 HTTP 200 承载 SSE 错误帧时，executor 会把状态码改写成
// 403（executor.go:793），而 403 必命中 isAccountLevel4xx。于是
// 「provider_error: All backends failed」这类与账号无关的后端池故障被记成账号
// 故障，同一个请求把整池账号逐个禁用。qoderwork 0.11.0 在生产复现过同形事故
// （单请求连禁 9 个号）。
//
// 因此本组用例必须用 **403** 作为输入状态码：用 200 测会得到假绿——历史实现里
// isAccountLevel4xx(200) 本就返回 false，删掉豁免判定测试照样通过。

// resetModelLevelFailoverState 清空 failover 状态并强制打开开关。
func resetModelLevelFailoverState(t *testing.T) {
	t.Helper()
	clearFailoverStates()
	oldEnabled := failoverActive()
	t.Cleanup(func() {
		clearFailoverStates()
		setFailoverEnabled(oldEnabled)
	})
	setFailoverEnabled(true)
}

// TestModelLevelFailure_RecognizesBackendPoolText 表驱动锁定标记集合与反例。
func TestModelLevelFailure_RecognizesBackendPoolText(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"all backends failed", "upstream 200: provider_error: All backends failed", true},
		{"no available backend", "upstream error: no available backend for this model", true},
		{"no backend available", "no backend available", true},
		{"backend pool unavailable", "backend pool unavailable, retry later", true},
		{"model not available", "model not available in current region", true},
		{"model unavailable", "MODEL UNAVAILABLE", true},
		{"empty body", "", false},
		// 反例：额度与凭据类必须不被误判为模型级。
		{"credit exhausted", "402: credits exhausted", false},
		{"forbidden", "403: forbidden", false},
		{"rate limit", "rate limit exceeded", false},
		// 反例：Trae 真实业务错误不得被吞。
		{"request illegal", "400: 11128 request illegal", false},
		{"account banned", "403: 11140 account banned", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isModelLevelFailure(c.body); got != c.want {
				t.Fatalf("isModelLevelFailure(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

// TestRecordAccountFailure_SkipsModelLevelFailures 锁定记账侧豁免。
//
// 用 403（isAccountLevel4xx 必命中）+ 模型级正文，连续 10 次都必须返回 false
// 且不留下任何 failover 状态。末尾用同一状态码 + 账号级正文做对照，证明豁免
// 是因为正文语义，不是因为 403 这段路径整体失效。
func TestRecordAccountFailure_SkipsModelLevelFailures(t *testing.T) {
	resetModelLevelFailoverState(t)
	body := `upstream 200: provider_error: All backends failed`

	for i := 0; i < 10; i++ {
		if recordAccountFailure("acc-model-level", 403, body) {
			t.Fatalf("iteration %d: 模型级故障不得记成账号故障（会造成整池自禁）", i)
		}
	}
	if _, _, ok := failoverStateSnapshot("acc-model-level"); ok {
		t.Fatalf("模型级故障不得留下 failover 状态")
	}

	// 对照组：同为 403，但正文是账号级语义，必须记账。
	if !recordAccountFailure("acc-control", 403, "403: forbidden") {
		t.Fatalf("对照组失败：403 + 账号级正文必须记账，否则豁免逻辑误伤了正常判据")
	}
}

// TestIsAccountFailure_ModelLevelDoesNotTriggerRotation 锁定换号侧豁免。
//
// executor.go 有 6 处直接调 isAccountFailure 决定「是否继续换号」。若只豁免记账
// 而不豁免换号，模型级故障仍会把同一池逐个白打上游——这正是 qoderwork 事故的
// 表现形态。本用例锁定两个入口共用同一判据。
func TestIsAccountFailure_ModelLevelDoesNotTriggerRotation(t *testing.T) {
	modelLevel := "upstream 200: provider_error: All backends failed"
	if isAccountFailure(403, modelLevel) {
		t.Fatalf("403 + 模型级正文不得判为可换号的账号故障")
	}
	if isAccountFailure(503, modelLevel) {
		t.Fatalf("503 + 模型级正文同样不得判为账号故障")
	}
	// 对照：账号级正文仍必须触发换号。
	if !isAccountFailure(403, "403: forbidden") {
		t.Fatalf("403 + 账号级正文必须判为可换号")
	}
	// 对照：硬额度优先于模型级标记，不得被豁免吞掉。
	if !isAccountFailure(402, "model not available; credits exhausted") {
		t.Fatalf("硬额度必须优先于模型级标记，402 不得被豁免")
	}
}

// TestClassifyUpstreamFailure_ModelLevelIsNone 锁定分类唯一落点的口径。
func TestClassifyUpstreamFailure_ModelLevelIsNone(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   failureScope
	}{
		{"403 model level", 403, "provider_error: All backends failed", failureScopeNone},
		{"503 model level", 503, "all backends failed", failureScopeNone},
		{"500 model level", 500, "backend pool unavailable", failureScopeNone},
		// 对照：硬额度仍走账号级。
		{"402 credit", 402, "model not available", failureScopeAccount},
		// 对照：普通 401 仍走账号级。
		{"401 plain", 401, "unauthorized", failureScopeAccount},
		// 对照：429 限流仍走账号×模型。
		{"429 rate limit", 429, "rate limit exceeded", failureScopeAuthModel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyUpstreamFailure(c.status, c.body); got != c.want {
				t.Fatalf("classifyUpstreamFailure(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
			}
		})
	}
}
