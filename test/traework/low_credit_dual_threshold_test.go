package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 积分双阈值分级（2026-10-25；2026-11-09 禁用线默认值改为 100）。
//
// 两条线职责必须分清：
//   - 保活线 min_routable_credits（默认 20）：只影响排序，不硬排除；
//   - 禁用线 low_credit_disable_below（默认 100）：低于它才写盘禁用。
//
// 默认值由用户裁定从 0（关闭）改为 100：低于 100 的号不再承载请求，余额回升后
// 由下一轮积分刷新自动解禁。填 0 仍是 kill switch——下面保留该语义的回归用例，
// 防止「改成默认 100」时把显式配置的关闭态一起吃掉。

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

// TestLowCreditDisableBelowZeroKeepsLegacyBehavior 锁定 kill switch：
//
// 显式配置 0 时，remain=50 的账号必须仍然可路由、可被调度选中，且不得被写盘
// 禁用——行为与 0.3.0 完全一致。默认值是 10（开启），所以这里必须显式设为 0，
// 不能再用 defaultLowCreditDisableBelow。
func TestLowCreditDisableBelowZeroKeepsLegacyBehavior(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	clearAccountCacheForTest(t)
	restoreMin := setMinRoutableCredits(defaultMinRoutableCredits)
	defer restoreMin()
	restoreBelow := setLowCreditDisableBelow(0)
	defer restoreBelow()
	oldMode := setSchedulerMode(schedulerModeCredits)
	defer oldMode()

	if got := loadedLowCreditDisableBelow(); got != 0 {
		t.Fatalf("显式关闭后禁用线 = %d, want 0", got)
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

// TestLowCreditDisableDefaultIs10 锁定 2026-11-10 的默认值裁定：
// 9 停用、10 转保活降级、未知余量不降级，且默认配置下真实走完
// 「写盘停用 → 余额回升解禁」闭环。
//
// 默认值从 100 降回 10 的依据是生产取证：Trae 单账号总积分 2800 点，正常消耗后
// 余量长期落在 20-99 区间，100 点的线把健康账号逐个写盘停用，41 个账号的池子被
// 压到 1 个，剩下的还是 remain=0 的耗尽号。禁用线必须低于保活线（20），三档分级
// 才有「正常 / 保活降级 / 停用」的完整语义。
func TestLowCreditDisableDefaultIs10(t *testing.T) {
	if defaultLowCreditDisableBelow != 10 {
		t.Fatalf("禁用线默认值 = %d, want 10", defaultLowCreditDisableBelow)
	}
	if defaultLowCreditDisableBelow >= defaultMinRoutableCredits {
		t.Fatalf("禁用线 %d 必须低于保活线 %d，否则保活档不会出现",
			defaultLowCreditDisableBelow, defaultMinRoutableCredits)
	}
	// 边界：9 判停用、10 落保活档、30 正常、未知余量不降级。
	if got := creditTierFor(9, defaultLowCreditDisableBelow, defaultMinRoutableCredits); got != creditTierDisabled {
		t.Fatalf("remain=9 应判停用，got %s", got)
	}
	if got := creditTierFor(10, defaultLowCreditDisableBelow, defaultMinRoutableCredits); got != creditTierPreserve {
		t.Fatalf("remain=10 应判保活降级，got %s", got)
	}
	if got := creditTierFor(30, defaultLowCreditDisableBelow, defaultMinRoutableCredits); got != creditTierNormal {
		t.Fatalf("remain=30 应判正常，got %s", got)
	}
	if got := creditTierFor(-1, defaultLowCreditDisableBelow, defaultMinRoutableCredits); got != creditTierNormal {
		t.Fatalf("未知余量不得降级，got %s", got)
	}

	// 真实链路：默认配置下 9 必须写盘停用，回升到 10 必须自动解禁。
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("tier-default", "idx-default", nil)
	restoreBelow := setLowCreditDisableBelow(defaultLowCreditDisableBelow)
	defer restoreBelow()

	gradeCreditsForAuth("idx-default", "tier-default", nil, &traeCredits{TotalRemain: 9})
	doc := h.docOf("idx-default")
	if doc["disabled"] != true || doc[creditDisableKey] != true {
		t.Fatalf("remain=9 必须写盘停用并带本机制标记: %v", doc)
	}

	gradeCreditsForAuth("idx-default", "tier-default", nil, &traeCredits{TotalRemain: 10})
	doc = h.docOf("idx-default")
	if doc["disabled"] == true {
		t.Fatalf("remain=10 必须自动解禁: %v", doc)
	}
	if _, ok := doc[creditDisableKey]; ok {
		t.Fatalf("解禁后不得残留本机制标记: %v", doc)
	}
}

// TestKillSwitchReleasesOwnMarkers 锁定 kill switch 是双向开关，不是单向阀。
//
// 关闭本机制（low_credit_disable_below <= 0）只应停止「新增禁用」，本机制历史上
// 禁掉的号必须照常解禁。原实现在 gradeCreditsForAuth 里对 disableBelow <= 0 提前
// return，解禁分支永远走不到：运维把开关关掉想救回被误禁的号，结果那些号仍然
// 永久留在池外，池子只能越来越小。
func TestKillSwitchReleasesOwnMarkers(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("tier-kill", "idx-kill", map[string]any{
		"disabled":         true,
		creditDisableKey:   true,
		creditDisableAtKey: "2026-11-10T00:00:00Z",
	})
	// 关闭开关，同时给一个远高于任何禁用线的余量。
	restoreBelow := setLowCreditDisableBelow(0)
	defer restoreBelow()

	gradeCreditsForAuth("idx-kill", "tier-kill", nil, &traeCredits{TotalRemain: 500})

	doc := h.docOf("idx-kill")
	if doc["disabled"] == true {
		t.Fatalf("关闭开关后本机制禁的号必须解禁: %v", doc)
	}
	if _, ok := doc[creditDisableKey]; ok {
		t.Fatalf("解禁后不得残留本机制标记: %v", doc)
	}
}

// TestKillSwitchDoesNotAddNewDisables 锁定关闭开关时不新增任何禁用写盘。
func TestKillSwitchDoesNotAddNewDisables(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("tier-kill2", "idx-kill2", nil)
	restoreBelow := setLowCreditDisableBelow(0)
	defer restoreBelow()

	gradeCreditsForAuth("idx-kill2", "tier-kill2", nil, &traeCredits{TotalRemain: 1})

	if got := h.writeCount(); got != 0 {
		t.Fatalf("关闭开关时不得新增禁用写盘，写盘次数 = %d", got)
	}
	if h.docOf("idx-kill2")["disabled"] == true {
		t.Fatal("关闭开关时 remain=1 的号不得被停用")
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
	// 保活线必须同步抬到 100：禁用线高于保活线时会被夹取到保活线（见
	// effectiveLowCreditDisableBelow），不抬保活线就测不到「禁用线 100 生效」。
	restorePreserve := setMinRoutableCredits(100)
	defer restorePreserve()
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
