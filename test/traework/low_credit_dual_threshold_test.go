package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 积分双阈值分级（2026-10-25）。
//
// 两条线职责必须分清：
//   - 保活线 min_routable_credits（默认 20）：只影响排序，不再硬排除；
//   - 禁用线 low_credit_disable_below（默认 0 = 关闭）：低于它才写盘禁用。
//
// 红线：未配置禁用线时，remain=50 这类常见生产账号必须仍然可路由。默认值一旦
// 被改成 100（照搬 workbuddy），现网一大批号会在首轮扫描里被直接写盘禁用。

// TestCreditTierFor_Boundaries 锁定档位边界六点。
func TestCreditTierFor_Boundaries(t *testing.T) {
	cases := []struct {
		name          string
		remain        int64
		disableBelow  int64
		preserveBelow int64
		want          creditTier
	}{
		{"未知积分不降级", -1, 100, 20, creditTierNormal},
		{"耗尽且禁用线关闭", 0, 0, 20, creditTierPreserve},
		{"禁用线边缘下侧", 99, 100, 20, creditTierDisabled},
		{"恰在禁用线上", 100, 100, 20, creditTierNormal},
		{"保活档", 399, 0, 400, creditTierPreserve},
		{"正常档", 400, 0, 400, creditTierNormal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := creditTierFor(tc.remain, tc.disableBelow, tc.preserveBelow); got != tc.want {
				t.Fatalf("creditTierFor(%d, %d, %d) = %s, want %s",
					tc.remain, tc.disableBelow, tc.preserveBelow, got, tc.want)
			}
		})
	}
}

// TestLowCreditDisableBelowZeroKeepsLegacyBehavior 是本次改动的红线用例。
//
// 未配置禁用线（默认 0）时，remain=50 的账号必须仍然可路由、可被调度选中，
// 且不得被写盘禁用——行为与 0.3.0 完全一致。
func TestLowCreditDisableBelowZeroKeepsLegacyBehavior(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	clearAccountCacheForTest(t)
	restoreMin := setMinRoutableCredits(defaultMinRoutableCredits)
	defer restoreMin()
	restoreBelow := setLowCreditDisableBelow(defaultLowCreditDisableBelow)
	defer restoreBelow()
	oldMode := setSchedulerMode(schedulerModeCredits)
	defer oldMode()

	if got := loadedLowCreditDisableBelow(); got != 0 {
		t.Fatalf("禁用线默认值必须为 0（关闭），got %d", got)
	}

	const authID = "tier-legacy-50"
	cacheCredits(authID, &traeCredits{TotalRemain: 50})

	// 1. 不得被判为「低或耗尽」。
	if _, lowOrExhausted := cachedCreditsScore(authID); lowOrExhausted {
		t.Fatal("禁用线关闭时 remain=50 不得被判为低或耗尽")
	}
	// 2. 面板可路由判据必须为真。
	if !accountRoutable(authID) {
		t.Fatal("禁用线关闭时 remain=50 必须仍可路由")
	}
	// 3. 调度层必须真的能选中它（唯一候选）。
	setActiveAuthID("")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider:   providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: authID, Provider: providerName}},
	}))
	if err != nil {
		t.Fatalf("handleSchedulerPick: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != authID {
		t.Fatalf("禁用线关闭时 remain=50 的账号必须被选中，got %+v", resp)
	}
}

// TestGradeCredits_YieldsToManualAndFailoverDisable 锁定归属边界。
//
// 别人的禁用本机制一律不碰：manual / failover 的号有自己的恢复路径，
// 被积分机制抢先解禁会绕过各自的校验。
func TestGradeCredits_YieldsToManualAndFailoverDisable(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]any
	}{
		{"manual", map[string]any{"manual_disable": true}},
		{"failover", map[string]any{failoverDisableKey: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFailover(t)
			h := newFailoverFakeHost(t)
			extra := map[string]any{"disabled": true}
			for k, v := range tc.extra {
				extra[k] = v
			}
			h.addAccount("tier-yield", "idx-yield", extra)
			restoreBelow := setLowCreditDisableBelow(100)
			defer restoreBelow()

			// 积分已回升到禁用线以上，但这号不归本机制管。
			gradeCreditsForAuth("idx-yield", "tier-yield", nil, &traeCredits{TotalRemain: 500})

			if got := h.writeCount(); got != 0 {
				t.Fatalf("%s 禁用不得被积分机制改写，写盘次数 = %d", tc.name, got)
			}
			if h.docOf("idx-yield")["disabled"] != true {
				t.Fatalf("%s 禁用必须保持", tc.name)
			}
		})
	}
}

// TestGradeCredits_ReenablesOnlyOwnMarker 锁定只解除自己的标记。
func TestGradeCredits_ReenablesOnlyOwnMarker(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("tier-own", "idx-own", map[string]any{
		"disabled":            true,
		creditDisableKey:      true,
		creditDisableBelowKey: int64(100),
	})
	restoreBelow := setLowCreditDisableBelow(100)
	defer restoreBelow()

	// 积分回升到禁用线以上：本机制禁的号必须解禁并清掉三个自有键。
	gradeCreditsForAuth("idx-own", "tier-own", nil, &traeCredits{TotalRemain: 500})

	doc := h.docOf("idx-own")
	if doc["disabled"] != false {
		t.Fatalf("本机制禁用的号在积分回升后必须解禁，doc=%v", doc)
	}
	for _, k := range []string{creditDisableKey, creditDisableBelowKey, creditDisableAtKey} {
		if _, ok := doc[k]; ok {
			t.Fatalf("解禁后 %s 必须清除，doc=%v", k, doc)
		}
	}
}

// TestGradeCredits_DisablesBelowLine 锁定低于禁用线时的写盘方向与标记完整性。
func TestGradeCredits_DisablesBelowLine(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("tier-low", "idx-low", nil)
	restoreBelow := setLowCreditDisableBelow(100)
	defer restoreBelow()

	gradeCreditsForAuth("idx-low", "tier-low", nil, &traeCredits{TotalRemain: 50})

	doc := h.docOf("idx-low")
	if doc["disabled"] != true {
		t.Fatalf("低于禁用线必须写盘禁用，doc=%v", doc)
	}
	if doc[creditDisableKey] != true {
		t.Fatalf("%s 标记必须写入（否则永远找不到这个号，无法自动恢复）", creditDisableKey)
	}
	if v, _ := doc[creditDisableBelowKey].(float64); int64(v) != 100 {
		t.Fatalf("%s 必须记录当时的禁用线，got %v", creditDisableBelowKey, doc[creditDisableBelowKey])
	}
	if at, _ := doc[creditDisableAtKey].(string); at == "" {
		t.Fatalf("%s 必须写入", creditDisableAtKey)
	}
}

// TestGradeCredits_NilCreditsDoesNothing 锁定「积分未知不动作」。
func TestGradeCredits_NilCreditsDoesNothing(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("tier-nil", "idx-nil", nil)
	restoreBelow := setLowCreditDisableBelow(100)
	defer restoreBelow()

	tier := gradeCreditsForAuth("idx-nil", "tier-nil", nil, nil)

	if tier != creditTierNormal {
		t.Fatalf("未知积分应返回 normal 档，got %s", tier)
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("未知积分不得写盘，写盘次数 = %d", got)
	}
}
