// plan_restricted_test.go 锁定「套餐限制（1005）」与信封状态码。
//
// 生产背景（2026-11-08）：kimi-k3.1-pro 对全部 25 个样本返回
// upstream 200: {"code":1005,"message":"","extra":"{\"plan\":5}"}，
// 旧逻辑无判据，宿主逐个凭据重试，单请求换遍 12 个账号。
package main

import (
	"encoding/json"
	"testing"
)

// TestIsPlanRestrictedFailure 验证 1005 套餐限制的识别边界。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 21:05:00；改动原因：新增，堵住 1005 跨账号扇出。
func TestIsPlanRestrictedFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "生产实测原文",
			body: `upstream 200: {"code":1005,"message":"","extra":"{\"plan\":5}"}`,
			want: true,
		},
		{
			name: "带空格写法",
			body: `upstream 200: {"code": 1005, "message": "", "plan": 5}`,
			want: true,
		},
		{name: "空正文不误判", body: "", want: false},
		{name: "无 plan 字段不归此类", body: `upstream 200: {"code":1005,"message":"x"}`, want: false},
		{name: "其它业务码不归此类", body: `upstream 200: {"code":1006,"plan":5}`, want: false},
		{name: "裸数字不误判", body: "the answer is 1005 tokens with plan", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPlanRestrictedFailure(tc.body); got != tc.want {
				t.Fatalf("isPlanRestrictedFailure(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestPlanRestrictedIsModelLevel 验证 1005 归入模型级失败（不记账号、不换号）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 21:05:00；改动原因：新增。
func TestPlanRestrictedIsModelLevel(t *testing.T) {
	body := `upstream 200: {"code":1005,"message":"","extra":"{\"plan\":5}"}`
	if !isModelLevelFailure(body) {
		t.Fatal("1005 套餐限制应归入模型级失败，不记在账号头上")
	}
	if got := classifyUpstreamFailure(200, body); got != failureScopeNone {
		t.Fatalf("classifyUpstreamFailure(200, 1005) = %v, want failureScopeNone", got)
	}
}

// TestAccountPoolExhaustedIsRequestLevel 验证「换号预算耗尽」被宿主判为请求级。
//
// 生产缺陷回归（2026-11-08）：插件换号预算耗尽后的终局错误经 stream.emit 发出时
// 不带状态码，宿主判定为可重试并继续换凭据，与插件自身的换号叠加成双重放大 ——
// 实测单请求换遍 8 个账号后仍失败。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 23:35:00；改动原因：新增，堵住双重换号放大。
func TestAccountPoolExhaustedIsRequestLevel(t *testing.T) {
	const exhausted = "upstream account pool exhausted after 8 attempt(s)"
	if !isAccountPoolExhausted(exhausted) {
		t.Fatal("换号预算耗尽必须被识别")
	}
	if got := envelopeHTTPStatusForError(exhausted); got != httpStatusBadRequest {
		t.Fatalf("http_status = %d, want %d", got, httpStatusBadRequest)
	}
	// 普通错误不携带状态码，避免改变既有分类。
	if got := envelopeHTTPStatusForError("upstream 500: internal error"); got != 0 {
		t.Fatalf("普通错误 http_status = %d, want 0", got)
	}
}

// TestErrorEnvelopeCarriesRequestLevelStatus 锁定失败信封的 http_status 通道。
//
// 宿主 statusCodeFromError 只认数字状态码；缺该字段时任何失败都会被当成可重试，
// 于是逐个凭据轮转（生产实测单请求换遍 12 个账号）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 21:05:00；改动原因：新增。
func TestErrorEnvelopeCarriesRequestLevelStatus(t *testing.T) {
	type probeEnv struct {
		OK    bool `json:"ok"`
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			HTTPStatus int    `json:"http_status"`
		} `json:"error"`
	}

	cases := []struct {
		name    string
		message string
		want    int
	}{
		{
			name:    "套餐限制 1005",
			message: `upstream 200: {"code":1005,"message":"","extra":"{\"plan\":5}"}`,
			want:    httpStatusBadRequest,
		},
		{
			name:    "模型名写法不匹配",
			message: "upstream 200: We're sorry, the param is invalid. Please try with a valid param.",
			want:    httpStatusBadRequest,
		},
		{name: "普通 5xx", message: "upstream 500: internal error", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := errorEnvelope("plugin_error", tc.message)
			var p probeEnv
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatalf("unmarshal: %v (raw=%s)", err, raw)
			}
			if p.Error.HTTPStatus != tc.want {
				t.Fatalf("http_status = %d, want %d", p.Error.HTTPStatus, tc.want)
			}
		})
	}
}
