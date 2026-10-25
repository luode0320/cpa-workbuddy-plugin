package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestMinRoutableCredits_TraeWork 验证 TraeWork 保活线（min_routable_credits）
// 默认 20、可配置，并且在 0.4.0 里**只降级排序、不再硬排除**。
//
// 语义变更说明（2026-10-25，积分双阈值分级）：保活线原本是硬排除，remain 低于
// 它的账号直接被踢出路由。这与「放宽而非收紧」的 0.4.0 目标冲突——现有生产账号
// 有不少落在 20–99 区间，全部踢出会让可用池骤减。现在它是**排序降级**：
// 保活档排在健康档之后，但仍然可以承接请求。硬禁用改由独立的
// low_credit_disable_below 承担（默认 0 = 关闭）。
func TestMinRoutableCredits_TraeWork(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	restoreMin := setMinRoutableCredits(defaultMinRoutableCredits)
	defer restoreMin()
	// 禁用线必须是关闭状态，否则下面的低积分号会被硬禁用，测不到排序语义。
	restoreBelow := setLowCreditDisableBelow(defaultLowCreditDisableBelow)
	defer restoreBelow()

	// 1. 默认阈值为 20
	if got := loadedMinRoutableCredits(); got != 20 {
		t.Fatalf("default minRoutableCredits = %d, want 20", got)
	}

	// 2. 通过 configure() 动态修改阈值为 50
	cfgBytes, _ := json.Marshal(map[string]any{
		"config_yaml": []byte("min_routable_credits: 50\nscheduler_mode: credits\n"),
	})
	configure(cfgBytes)
	if got := loadedMinRoutableCredits(); got != 50 {
		t.Fatalf("configured minRoutableCredits = %d, want 50", got)
	}
	setMinRoutableCredits(20)
	// configure 会按默认值重写禁用线；这里显式确认它仍是关闭态。
	if got := loadedLowCreditDisableBelow(); got != 0 {
		t.Fatalf("low_credit_disable_below 默认应为 0（关闭），got %d", got)
	}

	// 3. 保活档不再硬排除：remain=15 仍可被选中，只是排在健康号之后。
	accountCache.Store("tr-low", &accountCacheEntry{
		credits: &traeCredits{TotalRemain: 15},
	})
	accountCache.Store("tr-ok1", &traeCredits{TotalRemain: 100})
	accountCache.Store("tr-ok2", &traeCredits{TotalRemain: 200})
	defer func() {
		accountCache.Delete("tr-low")
		accountCache.Delete("tr-ok1")
		accountCache.Delete("tr-ok2")
	}()

	setActiveAuthID("")
	raw1, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-low", Provider: providerName},
			{ID: "tr-ok1", Provider: providerName},
			{ID: "tr-ok2", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("pick1 err: %v", err)
	}
	resp1 := parsePickResponse(t, raw1)
	if !resp1.Handled {
		t.Fatal("有可用候选时必须 handled")
	}
	if resp1.AuthID == "tr-low" {
		t.Fatalf("健康号存在时保活档应排在后面，不应被优先选中，got %+v", resp1)
	}
	if resp1.AuthID != "tr-ok1" && resp1.AuthID != "tr-ok2" {
		t.Fatalf("expected a healthy candidate (tr-ok1 or tr-ok2), got %+v", resp1)
	}

	// 4. 全部候选都落在保活档时，必须仍能选中（这就是「放宽」的核心断言）。
	rawAllLow, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "tr-low", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("pickAllLow err: %v", err)
	}
	respAllLow := parsePickResponse(t, rawAllLow)
	if !respAllLow.Handled || respAllLow.AuthID != "tr-low" {
		t.Fatalf("保活档不再硬排除，唯一候选必须被选中，got %+v", respAllLow)
	}

	// 5. accountRoutable 同样不再因保活线判 false（面板与调度必须同口径）。
	if !accountRoutable("tr-low") {
		t.Fatal("remain=15 属于保活档，仍应可路由（硬排除已降级为排序降级）")
	}
	if !accountRoutable("tr-ok1") {
		t.Fatal("expected accountRoutable(tr-ok1) == true for remain=100")
	}
}
