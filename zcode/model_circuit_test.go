// model_circuit_test.go 验证 11128「请求被拦截」的记账落点。
//
// 历史：本文件曾覆盖独立熔断状态机（跨账号聚合、60 秒闸门、窗口清理）。
// 2026-10-20 起重构为「11128 直接写账号×模型冷却」，状态全部由
// accountFailover 持有，因此这里的用例聚焦三件事：
//  1. 判据边界（11128 / 11140 / 6004 不能混淆）；
//  2. 命中后确实写入模型级冷却，路由会跳过该组合；
//  3. 冷却状态按账号×模型维度持有（恢复通道见 failover_disable.go）。
package main

import (
	"strings"
	"testing"
)

// TestIsModelBlockedErrorClassifiesUpstreamRiskControl 验证风控错误分类边界：
// 11128 是被拦截、11140 是账号级封禁、6004 是频率限制，三者必须区分开。
func TestIsModelBlockedErrorClassifiesUpstreamRiskControl(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "11128 请求被拦截",
			status: 400,
			body:   `{"code":11128,"msg":"request illegal","displayMsg":{"zh":"请求被拦截，请重新发送"}}`,
			want:   true,
		},
		{
			name:   "11128 以 403 返回同样命中",
			status: 403,
			body:   `{"code":11128,"msg":"request illegal"}`,
			want:   true,
		},
		{
			name:   "11140 账号级封禁必须排除",
			status: 403,
			body:   `{"code":11140,"msg":"request illegal","displayMsg":{"en":"Your account has been blocked"}}`,
			want:   false,
		},
		{
			name:   "11140 与 11128 混排时以账号级为准",
			status: 403,
			body:   `{"code":11140,"msg":"request illegal","ref":"11128"}`,
			want:   false,
		},
		{
			name:   "6004 频率限制不属拦截",
			status: 400,
			body:   `{"code":6004,"msg":"usage exceeds frequency limit"}`,
			want:   false,
		},
		{
			name:   "429 不在判定范围",
			status: 429,
			body:   `{"code":11128}`,
			want:   false,
		},
		{
			name:   "空正文不算",
			status: 400,
			body:   "",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isModelBlockedError(tc.status, tc.body); got != tc.want {
				t.Fatalf("isModelBlockedError(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestClassifyUpstreamFailureBlocks11128AtModelScope 验证 11128 被归到账号×模型
// 冷却，而不是「不记账」。这是整个方案的根因修复点：旧实现把它判成
// failureScopeNone，导致被拦截的组合永远不会冷却、也不会被探活验证。
func TestClassifyUpstreamFailureBlocks11128AtModelScope(t *testing.T) {
	body := `{"code":11128,"msg":"request illegal"}`
	if got := classifyUpstreamFailure(400, body); got != failureScopeAuthModel {
		t.Fatalf("11128 应归入账号×模型冷却, got %v", got)
	}
	// 11140 仍是账号级：封号换模型也不可用。
	if got := classifyUpstreamFailure(403, `{"code":11140}`); got != failureScopeAccount {
		t.Fatalf("11140 应归入账号级冷却, got %v", got)
	}
	// 普通业务 400 不记账：换账号同样失败。
	if got := classifyUpstreamFailure(400, `{"code":1234,"msg":"bad request"}`); got != failureScopeNone {
		t.Fatalf("普通 400 不应记账, got %v", got)
	}
}

// TestNoteModelBlockedHitWritesDisable 验证一次 11128 命中就把该账号写盘禁用，
// 且失败的模型被记为探活回放目标。
//
// 粒度说明（2026-10-25 设计变更）：宿主 disabled 只有账号级布尔值，没有模型维度。
// 因此 11128 会禁用整个账号——这是有意的取舍，代价由「恢复时只探失败模型」承担：
// 若该模型确实恢复，一次探通即整体解禁；若只是被别的模型牵连，探活会失败并保持禁用。
func TestNoteModelBlockedHitWritesDisable(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-11128a", "idx-28a", nil)
	h.addAccount("zcode-11128b", "idx-28b", nil)

	const model = "glm-5.3"
	noteModelBlockedHit("zcode-11128a", model)

	if h.docOf("idx-28a")["disabled"] != true {
		t.Fatal("命中 11128 后该账号应被写盘禁用")
	}
	if got := failoverProbeModelFor("zcode-11128a"); got != model {
		t.Fatalf("应记录失败模型供探活回放，实际 %q", got)
	}
	// 只影响命中的账号：其它账号不受牵连。
	if h.docOf("idx-28b")["disabled"] == true {
		t.Fatal("11128 只应禁用命中账号，不应牵连其它账号")
	}
}
func TestBlockedErrorBodyMarkersCoverProductionShape(t *testing.T) {
	// 生产原样报文（2026-10-18，1,173 次）
	production := `{"code":11128,"msg":"request illegal","requestId":"e1a6043f-92ab-4eb8-b5c0-a1791dea9a24","displayMsg":{"en":"Request blocked. Please send it again","zh":"请求被拦截，请重新发送"}}`
	if !isModelBlockedError(400, production) {
		t.Fatal("生产 11128 报文必须被识别为请求被拦截")
	}
	if !strings.Contains(production, "11128") {
		t.Fatal("回归样本本身应包含 11128")
	}
}
