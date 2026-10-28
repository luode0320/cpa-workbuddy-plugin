// credit_disable_test.go 覆盖低额禁用经宿主管理 API 同步的路径与写盘兜底。
package main

import "testing"

// TestDisableForLowCredits_UsesCPAAPIWhenConfigured 锁定低额禁用也走宿主 API。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-28；改动原因：新增，覆盖三类禁用的 API 通道。
func TestDisableForLowCredits_UsesCPAAPIWhenConfigured(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	calls, restore := withCPADisableSync(t, true)
	defer restore()
	resetCreditProbeForTest()

	h.addAccount("auth-low", "idx-low", nil)
	sa := &storedAuth{Account: storedAccount{UID: "auth-low"}}
	cr := &creditsSummary{TotalRemain: 10}

	if err := disableForLowCredits("idx-low", "auth-low", sa, cr, 100); err != nil {
		t.Fatalf("低额禁用失败: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("API 调用次数 = %d; want 1", len(*calls))
	}
	if got := (*calls)[0]; !got.Disabled || got.AuthIndex != "idx-low" {
		t.Fatalf("请求体 = %+v", got)
	}
	if h.writeCount() != 0 {
		t.Fatalf("走 API 时不应写盘，实际 %d", h.writeCount())
	}
}

// TestDisableForLowCredits_FallsBackToDiskWhenNotConfigured 锁定未配密钥的零回归。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-28；改动原因：新增。
func TestDisableForLowCredits_FallsBackToDiskWhenNotConfigured(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	_, restore := withCPADisableSync(t, false)
	defer restore()
	resetCreditProbeForTest()

	h.addAccount("auth-low2", "idx-low2", nil)
	sa := &storedAuth{Account: storedAccount{UID: "auth-low2"}}
	cr := &creditsSummary{TotalRemain: 10}

	if err := disableForLowCredits("idx-low2", "auth-low2", sa, cr, 100); err != nil {
		t.Fatalf("低额禁用失败: %v", err)
	}
	if h.writeCount() != 1 {
		t.Fatalf("未配密钥应写盘一次，实际 %d", h.writeCount())
	}
	if h.docOf("idx-low2")[creditDisableKey] != true {
		t.Fatal("盘上应带低额禁用标记")
	}
}
