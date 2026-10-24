// model_level_failure_test.go 锁死「模型级故障不得记在账号头上」这条红线。
//
// 背景（2026-10-25 生产实证）：单个请求 20ebfacb 请求 Kimi-K3.1-Pro，依次
// 尝试 9 个账号全部返回 `provider_error: All backends failed`，旧逻辑把这 9 个
// 账号逐个写盘禁用；12 秒后另一个请求又打到这批已禁用账号上。一个模型级抖动
// 直接造成整池自我禁用。
//
// 本测试覆盖三件事：
//  1. 模型级文案被 isModelLevelFailure 正确识别；
//  2. 模型级失败在 recordAccountFailure 入口被拦下，不产生冷却、不产生禁用；
//  3. 真正的账号级失败（401/403 凭据问题、110 日限额）仍然正常记账。
package main

import (
	"testing"
	"time"
)

// TestIsModelLevelFailure_RecognizesBackendPoolFailures 覆盖生产实际出现的响应文案。
func TestIsModelLevelFailure_RecognizesBackendPoolFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "生产实证：All backends failed",
			body: "upstream 200: provider_error: All backends failed",
			want: true,
		},
		{
			name: "排队帧容量不足",
			body: `403: 10605: {"isQueued":true,"serviceAvailable":false,"subCode":"RESOURCE_LIMIT"}`,
			want: true,
		},
		{
			name: "模型不可用",
			body: "model not available for this plan",
			want: true,
		},
		{
			name: "空正文不误判",
			body: "",
			want: false,
		},
		{
			name: "账号凭据失效不是模型级",
			body: "401: invalid access token",
			want: false,
		},
		{
			name: "110 日限额是账号级",
			body: "110: Billing daily count exceeded",
			want: false,
		},
		{
			name: "402 欠费是账号级",
			body: "402: payment required",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isModelLevelFailure(tc.body); got != tc.want {
				t.Fatalf("isModelLevelFailure(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestRecordAccountFailure_SkipsModelLevelFailures 是本次修复的核心断言：
// 模型级失败进入记账入口后必须原样返回 false，且账号不得进入冷却。
//
// 必须用 status=403 复现真实路径：生产上 stream.go / main.go 会把 HTTP 200 的
// SSE 错误帧 remap 成 403 再记账。若这里用 200 测试，isAccountFailure 本就会
// 返回 false，测试会在缺少 isModelLevelFailure 判定时依然通过（假绿）。
func TestRecordAccountFailure_SkipsModelLevelFailures(t *testing.T) {
	resetFailover(t)

	const authID = "qoderwork-model-level-fixture"
	body := "upstream 200: provider_error: All backends failed"

	// 连续 10 次模型级失败：若分类错误，阶梯冷却会累积到上限。
	for i := 0; i < 10; i++ {
		if recordAccountFailure(authID, 403, body) {
			t.Fatalf("第 %d 次模型级失败被计入账号冷却，应为 false", i+1)
		}
	}
	if count, until, ok := failoverStateSnapshot(authID); ok && (count != 0 || until.After(time.Now())) {
		t.Fatalf("模型级失败产生了冷却状态: count=%d until=%v", count, until)
	}
	if isAccountCoolingDown(authID) {
		t.Fatal("模型级失败后账号被判为冷却中，应保持可路由")
	}
	// 前置条件断言：同样文案换成账号级语义（去掉模型级标记）时，403 必须记账。
	// 这一条守住「测试确实走在 isModelLevelFailure 这条分支上」。
	if !recordAccountFailure("qoderwork-403-control", 403, "403: forbidden") {
		t.Fatal("403 账号级失败未记账，说明 403 路径本身不通，本用例失去区分力")
	}
}

// TestRecordAccountFailure_StillCountsAccountLevelFailures 反证：
// 收紧分类后，真正的账号级失败必须照旧记账，否则等于把冷却机制整体关掉。
func TestRecordAccountFailure_StillCountsAccountLevelFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "403 权限不足", status: 403, body: "403: forbidden"},
		{name: "401 凭据失效", status: 401, body: "401: invalid access token"},
		{name: "402 欠费", status: 402, body: "402: payment required"},
		{name: "500 服务端错误", status: 500, body: "500: internal error"},
		{name: "110 日限额", status: 403, body: "110: Billing daily count exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFailover(t)

			authID := "qoderwork-account-level-fixture"
			if !recordAccountFailure(authID, tc.status, tc.body) {
				t.Fatalf("账号级失败 %s 未被记账，冷却机制已失效", tc.name)
			}
			if count, _, ok := failoverStateSnapshot(authID); !ok || count == 0 {
				t.Fatalf("账号级失败未产生失败计数: count=%d ok=%v", count, ok)
			}
		})
	}
}
