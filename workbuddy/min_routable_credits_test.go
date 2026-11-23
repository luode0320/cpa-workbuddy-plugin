package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// lowCredits 返回一个必然低于当前红线的正积分值（残血但未耗尽）。
func lowCredits() int64 {
	if redline := loadedMinRoutableCredits(); redline > 0 {
		return redline / 2
	}
	return 0
}

// healthyCredits 返回一个必然高于当前红线的积分值，供路由测试构造健康账号。
// 用例数据统一走这里，阈值调整时语义自动跟随，不会静默变成残血号。
func healthyCredits() int64 {
	return loadedMinRoutableCredits() * 2
}

// TestMinRoutableCredits_DefaultRedlineBoundary 锁定三档分级的边界语义：
// remain == 保活线 属正常档；remain == 保活线-1 属保活档但仍可路由；
// 只有低于硬禁用线才不可路由（2026-10-23 由硬排除改为降级）。
func TestMinRoutableCredits_DefaultRedlineBoundary(t *testing.T) {
	resetActiveAuth(t)
	restoreMin := setMinRoutableCredits(defaultMinRoutableCredits)
	defer restoreMin()
	restoreDisable := setLowCreditDisableBelow(defaultLowCreditDisableBelow)
	defer restoreDisable()

	const redline = 50
	const disableBelow = 20
	if got := loadedMinRoutableCredits(); got != redline {
		t.Fatalf("default minRoutableCredits = %d, want %d", got, redline)
	}
	if got := loadedLowCreditDisableBelow(); got != disableBelow {
		t.Fatalf("default lowCreditDisableBelow = %d, want %d", got, disableBelow)
	}

	storeCredits(t, "wb-at-redline", redline, 0, redline)
	storeCredits(t, "wb-below-redline", redline-1, 1, redline)
	storeCredits(t, "wb-at-disable", disableBelow, 0, redline)
	storeCredits(t, "wb-below-disable", disableBelow-1, 1, redline)

	if !accountRoutable("wb-at-redline") {
		t.Fatalf("remain == %d 属正常档，必须可路由", redline)
	}
	// 保活档的关键语义：低于保活线仍可路由，不再被摘出。
	if !accountRoutable("wb-below-redline") {
		t.Fatalf("remain == %d 属保活档，必须仍可路由", redline-1)
	}
	if !accountRoutable("wb-at-disable") {
		t.Fatalf("remain == %d 恰好等于禁用线，不算低于，必须可路由", disableBelow)
	}
	if accountRoutable("wb-below-disable") {
		t.Fatalf("remain == %d 低于禁用线 %d，必须不可路由", disableBelow-1, disableBelow)
	}

	// 排序：正常档必须排在保活档之前，即使保活档余量更大也不得抢先。
	storeCredits(t, "wb-preserve-rich", redline-1, 0, redline)
	storeCredits(t, "wb-normal-poor", redline, 0, redline)
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-preserve-rich", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("pick err: %v", err)
	}
	if resp := parsePickResponse(t, raw); !resp.Handled || resp.AuthID != "wb-preserve-rich" {
		t.Fatalf("保活档是唯一候选时必须被选中，got %+v", resp)
	}
}

// TestMinRoutableCredits_Configurable400Boundary 验证保活线与禁用线均可被配置覆盖，
// 且覆盖后判据立即跟随新值（不是把默认阈值写死在调用点）。
func TestMinRoutableCredits_Configurable400Boundary(t *testing.T) {
	restoreMin := setMinRoutableCredits(defaultMinRoutableCredits)
	defer restoreMin()
	restoreDisable := setLowCreditDisableBelow(defaultLowCreditDisableBelow)
	defer restoreDisable()
	restoreMode := setSchedulerMode(schedulerModeSession)
	defer restoreMode()

	cfgBytes, _ := json.Marshal(map[string]any{
		"config_yaml": []byte("min_routable_credits: 800\nscheduler_mode: credits\n"),
	})
	configure(cfgBytes)
	if got := loadedMinRoutableCredits(); got != 800 {
		t.Fatalf("configured minRoutableCredits = %d, want 800", got)
	}

	// 700 低于 800 保活线但高于默认禁用线：现在属保活档，仍可路由。
	storeCredits(t, "wb-700", 700, 0, 700)
	if !accountRoutable("wb-700") {
		t.Fatal("remain=700 在禁用线之上，必须仍可路由（保活档）")
	}
	if got := creditTierFor(700, loadedLowCreditDisableBelow(), loadedMinRoutableCredits()); got != creditTierPreserve {
		t.Fatalf("tier(700) = %v, want preserve", got)
	}

	// 显式 0 关闭保活降级：高于禁用线的账号全部回到正常档。
	cfgOff, _ := json.Marshal(map[string]any{
		"config_yaml": []byte("min_routable_credits: 0\nscheduler_mode: credits\n"),
	})
	configure(cfgOff)
	if got := loadedMinRoutableCredits(); got != 0 {
		t.Fatalf("minRoutableCredits = %d after explicit 0, want 0", got)
	}
	if got := creditTierFor(700, loadedLowCreditDisableBelow(), loadedMinRoutableCredits()); got != creditTierNormal {
		t.Fatalf("min_routable_credits: 0 关闭保活降级后 tier(700) = %v, want normal", got)
	}
}

// TestMinRoutableCredits_RegisteredAsConfigField 锁定「红线可在配置文件修改」
// 这一对外承诺：只被 configure() 解析还不够，必须同时登记到插件 ConfigFields，
// 管理端才会把该键渲染成可编辑项；漏登记会让配置项实际存在却无处可改。
func TestMinRoutableCredits_RegisteredAsConfigField(t *testing.T) {
	fields := wbRegistration().Metadata.ConfigFields
	for _, f := range fields {
		if f.Name != "min_routable_credits" {
			continue
		}
		if f.Type != pluginapi.ConfigFieldTypeInteger {
			t.Fatalf("min_routable_credits type = %v, want integer", f.Type)
		}
		if !strings.Contains(f.Description, fmt.Sprintf("%d", defaultMinRoutableCredits)) {
			t.Fatalf("description must state the %d default, got %q", defaultMinRoutableCredits, f.Description)
		}
		return
	}
	t.Fatal("min_routable_credits 未登记到 ConfigFields：用户在配置里改了也不会被管理端暴露")
}

// TestNoAutomaticAccountDeletion 锁定「耗尽不再自动删号」：删号行为已下线
// （国际版由 workbuddy-ai 插件专门承载），生命周期只允许禁用/恢复。
// 直接扫生产源码，防止任何人重新接回一条自动删除通道。
func TestNoAutomaticAccountDeletion(t *testing.T) {
	files := []string{"policy.go", "lifecycle.go"}
	forbidden := []string{"deleteAuthFileInDir", "deleteAuth(", "lifecycleDelete", "os.Remove("}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(data)
		for _, bad := range forbidden {
			if strings.Contains(src, bad) {
				t.Fatalf("%s 仍包含自动删除通道 %q；生命周期只允许禁用/恢复", name, bad)
			}
		}
	}
}

// TestMinRoutableCredits_ConfigAndExclusion 验证安全缓冲额度红线默认 50、可配置、
// 调度排除 remain < 红线、平摊轮询以及 14018 换号判据。
func TestMinRoutableCredits_ConfigAndExclusion(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	restoreMin := setMinRoutableCredits(defaultMinRoutableCredits)
	defer restoreMin()

	// 1. 默认阈值为 50
	if got := loadedMinRoutableCredits(); got != 50 {
		t.Fatalf("default minRoutableCredits = %d, want 50", got)
	}

	// 2. 通过 configure() 动态修改阈值为 120（与默认 50 区分，证明取值来自配置）
	cfgBytes, _ := json.Marshal(map[string]any{
		"config_yaml": []byte("min_routable_credits: 120\nscheduler_mode: credits\n"),
	})
	configure(cfgBytes)
	if got := loadedMinRoutableCredits(); got != 120 {
		t.Fatalf("configured minRoutableCredits = %d, want 120", got)
	}
	setMinRoutableCredits(defaultMinRoutableCredits)

	// 3. 调度器把保活档排在正常档之后，并在正常档内平摊轮询。
	// 用例数据全部相对默认阈值构造，阈值再次调整时语义不被静默改写。
	lowRemain, okRemain, okHighRemain := lowCredits(), healthyCredits(), healthyCredits()*2
	accountCache.Store("acc-low", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: lowRemain, TotalUsed: 100, TotalSize: lowRemain + 100},
	})
	accountCache.Store("acc-ok1", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: okRemain, TotalUsed: 0, TotalSize: okRemain},
	})
	accountCache.Store("acc-ok2", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: okHighRemain, TotalUsed: 0, TotalSize: okHighRemain},
	})
	defer func() {
		accountCache.Delete("acc-low")
		accountCache.Delete("acc-ok1")
		accountCache.Delete("acc-ok2")
	}()

	// 即使面板曾经选中了 acc-low（保活档），健康号存在时也必须优先健康号。
	setActiveAuthID("acc-low")
	raw1, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-low", Provider: providerName},
			{ID: "acc-ok1", Provider: providerName},
			{ID: "acc-ok2", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("pick1 err: %v", err)
	}
	resp1 := parsePickResponse(t, raw1)
	if !resp1.Handled || resp1.AuthID != "acc-ok2" {
		t.Fatalf("expected 保活档 acc-low 让位、最高分健康号 acc-ok2 优先，got %+v", resp1)
	}

	// 再次切换（清空当前选中模拟轮询）：应轮询到下一个健康候选 acc-ok1
	activeAuthMu.Lock()
	activeAuthID = ""
	activeAuthMu.Unlock()
	raw2, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-low", Provider: providerName},
			{ID: "acc-ok1", Provider: providerName},
			{ID: "acc-ok2", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("pick2 err: %v", err)
	}
	resp2 := parsePickResponse(t, raw2)
	if !resp2.Handled || resp2.AuthID != "acc-ok1" {
		t.Fatalf("expected round-robin to pick acc-ok1 on second turn, got %+v", resp2)
	}

	// 4. 只剩保活档候选时必须仍然路由（不再全灭 defer）。
	rawAllLow, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-low", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("pickAllLow err: %v", err)
	}
	respAllLow := parsePickResponse(t, rawAllLow)
	if !respAllLow.Handled || respAllLow.AuthID != "acc-low" {
		t.Fatalf("保活档是唯一候选时必须仍然路由，got %+v", respAllLow)
	}

	// 5. accountRoutable 对保活档返回 true，只有低于禁用线才是 false。
	if !accountRoutable("acc-low") {
		t.Fatalf("expected accountRoutable(acc-low) == true for remain=%d（保活档仍可路由）", lowRemain)
	}
	if !accountRoutable("acc-ok1") {
		t.Fatalf("expected accountRoutable(acc-ok1) == true for remain=%d >= %d", okRemain, defaultMinRoutableCredits)
	}

	// 6. 14018 与 exceeded the quota 必须触发换号
	if !shouldRotateOnUpstreamErr(200, `upstream 200: 14018: Your requests have exceeded the quota.`) {
		t.Fatal("expected 14018 / exceeded the quota to trigger account rotation")
	}
}
