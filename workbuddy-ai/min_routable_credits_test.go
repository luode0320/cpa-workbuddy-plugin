package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestCreditsDoNotGateRouting 锁定「积分不参与可用性判定」契约。
//
// 产品口径（2026-10-24 用户裁定）：WorkBuddy AI 无积分也能正常使用，因此
// 额度耗尽既不打「耗尽」标签，也不排除路由、不触发自动禁用。本用例覆盖三处
// 曾经的硬排除点：scheduler.pick 候选过滤、accountRoutable 判据、
// ensureDefaultActiveAuth 的面板选中。
func TestCreditsDoNotGateRouting(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)

	// 1. 已下线的配置键不再影响任何行为（存在即被忽略）。
	cfgBytes, _ := json.Marshal(map[string]any{
		"config_yaml": []byte("min_routable_credits: 50\nscheduler_mode: credits\n"),
	})
	configure(cfgBytes)
	if got := loadedSchedulerMode(); got != schedulerModeCredits {
		t.Fatalf("scheduler_mode = %q, want %q", got, schedulerModeCredits)
	}

	// 2. 零积分账号必须仍是可路由状态。
	accountCache.Store("acc-zero", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 0, TotalUsed: 100, TotalSize: 100},
	})
	accountCache.Store("acc-ok", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 100, TotalUsed: 0, TotalSize: 100},
	})
	defer func() {
		accountCache.Delete("acc-zero")
		accountCache.Delete("acc-ok")
	}()
	if !accountRoutable("acc-zero") {
		t.Fatal("零积分账号必须可路由：额度耗尽不等于不可用")
	}

	// 3. 面板选中零积分账号时，调度不得因额度把它换掉。
	setActiveAuthID("acc-zero")
	raw, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-zero", Provider: providerName},
			{ID: "acc-ok", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("pick err: %v", err)
	}
	if resp := parsePickResponse(t, raw); !resp.Handled || resp.AuthID != "acc-zero" {
		t.Fatalf("零积分选中项必须保留，got %+v", resp)
	}

	// 4. 池内只剩零积分账号时仍由插件处理（不 defer）。
	rawOnly, err := handleSchedulerPick(mustMarshalBytes(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "acc-zero", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("pick only-zero err: %v", err)
	}
	if resp := parsePickResponse(t, rawOnly); !resp.Handled || resp.AuthID != "acc-zero" {
		t.Fatalf("仅剩零积分账号时仍应正常路由，got %+v", resp)
	}

	// 5. 面板选中规则同样不得因额度跳过零积分账号。
	dashID := ensureDefaultActiveAuth([]wbAccount{
		{AuthIndex: "idx-zero", AuthID: "acc-zero"},
	})
	if dashID != "acc-zero" {
		t.Fatalf("面板选中应为零积分账号 acc-zero，got %q", dashID)
	}

	// 6. 14018 / exceeded the quota 仍必须触发换号（额度语义与上游失败分类无关）。
	if !shouldRotateOnUpstreamErr(200, `upstream 200: 14018: Your requests have exceeded the quota.`) {
		t.Fatal("expected 14018 / exceeded the quota to trigger account rotation")
	}
}
