// permanent_ban_test.go 锁定「账号级永久封禁（11140）不进入探活候选」。
//
// 生产缺陷回归（2026-11-09）：被封禁账号 33d23f97 在 60 分钟内被探活 66 次，
// 全部返回 403 11140（request illegal / account blocked），纯属浪费上游调用。
// 11140 表示凭据已被上游封死，换模型、等冷却、人工启用都不会解除。
package main

import (
	"testing"
)

// TestIsPermanentBanError 验证 11140 的识别边界。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 21:50:00；改动原因：新增，探活需跳过被封禁账号。
func TestIsPermanentBanError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "生产实测原文",
			status: 403,
			body:   `{"code":11140,"msg":"request illegal","requestId":"x","retry_policy":{"retry":false}}`,
			want:   true,
		},
		{name: "400 承载同样识别", status: 400, body: `{"code":11140}`, want: true},
		{name: "11128 渠道拦截不归此类", status: 400, body: `{"code":11128,"msg":"unapproved channel"}`, want: false},
		{name: "普通 403 不归此类", status: 403, body: `forbidden`, want: false},
		{name: "500 不归此类", status: 500, body: `{"code":11140}`, want: false},
		{name: "空正文不归此类", status: 403, body: ``, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermanentBanError(tc.status, tc.body); got != tc.want {
				t.Fatalf("isPermanentBanError(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestParseFailoverAuthDocPermanent 验证 failover_permanent 标记被正确解析。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 21:50:00；改动原因：新增。
func TestParseFailoverAuthDocPermanent(t *testing.T) {
	doc := parseFailoverAuthDoc([]byte(`{"disabled":true,"failover_disable":true,"failover_permanent":true}`))
	if !doc.permanent {
		t.Fatal("failover_permanent=true 应解析为 permanent")
	}
	doc2 := parseFailoverAuthDoc([]byte(`{"disabled":true,"failover_disable":true}`))
	if doc2.permanent {
		t.Fatal("缺少 failover_permanent 时不得判为永久封禁")
	}
}

// TestFailoverProbeTargetsSkipsPermanentBan 锁定永久封禁账号不进探活候选。
//
// 这是端到端断言：只测 isPermanentBanError 无法发现「候选筛选漏了 permanent 判断」。
// 两个账号都直接写盘构造（disabled + failover_disable），只有 banned 带 permanent。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-09 21:55:00；改动原因：新增，锁住探活候选筛选。
func TestFailoverProbeTargetsSkipsPermanentBan(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	// 正常可恢复禁用：应进候选。
	h.addAccount("recoverable", "idx-r", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "deepseek-v4.1-flash",
	})
	// 永久封禁：不应进候选。
	h.addAccount("banned", "idx-b", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "deepseek-v4.1-flash",
		failoverPermanentKey:    true,
	})

	targets := failoverProbeTargets()
	for _, tc := range targets {
		if tc.authIndex == "idx-b" {
			t.Fatalf("永久封禁账号不得进入探活候选: %+v", targets)
		}
	}
	// 可恢复账号必须仍在候选里，证明排除不是「全部跳过」。
	found := false
	for _, tc := range targets {
		if tc.authIndex == "idx-r" {
			found = true
		}
	}
	if !found {
		t.Fatalf("可恢复账号应仍在探活候选: %+v", targets)
	}
}
