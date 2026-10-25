package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 三层判据与标记补回（2026-10-25）。
//
// 背景：宿主 persist 会按内存 Status + 白名单重建 auth 文档，把本机制的三个顶层键
// （failover_disable / _model / _at）丢掉，只留 disabled。标记没了探活就枚举不到
// 候选，账号永久卡在「盘上禁用、插件不知情」的死角。四个改动必须同批生效：
// 判据接 sidecar、补回标记、周期落盘触发补回、外部启用对账。

// setupMarkerRestoreFixture 准备一个隔离的 sidecar + 本地集合 + 假宿主。
func setupMarkerRestoreFixture(t *testing.T) {
	t.Helper()
	setupFailoverStateDir(t)
	clearFailoverStates()
	setFailoverEnabled(true)
	t.Cleanup(func() {
		clearFailoverStates()
		setFailoverEnabled(true)
		clearFailoverDisabledLocalAll()
	})
}

// TestRestoreMarkersIntoDoc_OnlyWhenLocalAndDisabled 锁定 4 种组合的补写边界。
//
// 这是本任务的核心判据：只有「本地记了 + 盘上仍 disabled + 盘上缺标记」三条件
// 同时成立才补写。任何一个放宽都会让补回越界：
//   - 去掉本地判据 → 别人的禁用被我们认领；
//   - 去掉 disabled 判据 → 已被启用的号被重新打上禁用标记；
//   - 去掉「缺标记」判据 → 覆盖已有标记（可能是旧版本写的别的模型）。
func TestRestoreMarkersIntoDoc_OnlyWhenLocalAndDisabled(t *testing.T) {
	cases := []struct {
		name       string
		localDisc  bool
		diskDisc   bool
		diskMarked bool
		want       bool
	}{
		{"local + disk enabled + unmarked", true, false, false, false},
		{"local + disk disabled + unmarked", true, true, false, true},
		{"local + disk disabled + marked", true, true, true, false},
		{"not local + disk disabled + unmarked", false, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setupMarkerRestoreFixture(t)

			const authID = "acc-restore"
			if c.localDisc {
				markFailoverDisabledLocal(authID, "")
				rememberFailoverProbeModel(authID, "qwen3.8-max")
			}
			doc := map[string]any{"disabled": c.diskDisc}
			if c.diskMarked {
				doc[failoverDisableKey] = true
				doc[failoverDisableModelKey] = "old-model"
			}

			got := restoreFailoverMarkersIntoDoc(doc, authID, "qwen3.8-max")
			if got != c.want {
				t.Fatalf("restore = %v, want %v", got, c.want)
			}
			if !c.want {
				if marked, _ := doc[failoverDisableKey].(bool); marked && !c.diskMarked {
					t.Fatalf("不该补写时不得写入 failover_disable")
				}
				return
			}
			if marked, _ := doc[failoverDisableKey].(bool); !marked {
				t.Fatalf("应补写 failover_disable")
			}
			if got, _ := doc[failoverDisableModelKey].(string); got != "qwen3.8-max" {
				t.Fatalf("补写时必须带上失败模型，got %q", got)
			}
			if at, _ := doc[failoverDisableAtKey].(string); at == "" {
				t.Fatalf("补写时必须带上禁用时刻")
			}
		})
	}
}

// TestPersistCounterDeltaRestoresMarkers 锁定周期落盘这条整合链路。
//
// 模拟宿主 persist 抹键后的形态（只剩 disabled + 计数），跑 foldCounterIntoDoc +
// restoreFailoverMarkersIntoJSON，断言三键回来且计数增量正确。
func TestPersistCounterDeltaRestoresMarkers(t *testing.T) {
	setupMarkerRestoreFixture(t)

	const authID = "acc-persist"
	markFailoverDisabledLocal(authID, "")
	rememberFailoverProbeModel(authID, "qwen3.8-max")

	// 宿主 persist 之后盘上的样子：只剩 disabled 与旧计数，三键全无。
	disk := map[string]any{
		"type":          providerName,
		"provider":      providerName,
		"disabled":      true,
		"success_count": 5,
		"failed_count":  2,
	}
	base, _ := json.Marshal(disk)

	raw := foldCounterIntoDoc(base, 1, 1)
	raw = restoreFailoverMarkersIntoJSON(raw, authID, failoverLocalDisabledModel(authID))

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if marked, _ := doc[failoverDisableKey].(bool); !marked {
		t.Fatalf("周期落盘必须把 %s 补回", failoverDisableKey)
	}
	if got, _ := doc[failoverDisableModelKey].(string); got != "qwen3.8-max" {
		t.Fatalf("%s 必须是失败模型，got %q", failoverDisableModelKey, got)
	}
	if at, _ := doc[failoverDisableAtKey].(string); at == "" {
		t.Fatalf("%s 必须补回", failoverDisableAtKey)
	}
	// 计数器增量必须正确（补回不能破坏既有折叠逻辑）。
	if v, _ := doc["success_count"].(float64); int64(v) != 6 {
		t.Fatalf("success_count 应为 6，got %v", doc["success_count"])
	}
	if v, _ := doc["failed_count"].(float64); int64(v) != 3 {
		t.Fatalf("failed_count 应为 3，got %v", doc["failed_count"])
	}
}

// TestRestoreMarkers_DoesNotTouchOtherAccounts 反向守卫：补回不得越界。
func TestRestoreMarkers_DoesNotTouchOtherAccounts(t *testing.T) {
	setupMarkerRestoreFixture(t)

	markFailoverDisabledLocal("acc-ours", "")
	doc := map[string]any{"disabled": true}
	// 用别人的 authID 调用，不得补写。
	if restoreFailoverMarkersIntoDoc(doc, "acc-theirs", "m") {
		t.Fatalf("不得为未被本机制禁用的账号补写标记")
	}
}

// TestReconcileDropsSidecarWhenExternallyEnabled 锁定 sidecar 对账。
//
// 外部（CPA 面板 / 宿主 UI）把号启用后，盘上 disabled=false，但 sidecar 仍记着
// 禁用。若不对账，scheduler.pick 会把这个号永久排除——而宿主已经会把流量路由
// 过来，形成「号回来了却接不到流量」的半死状态。
func TestReconcileDropsSidecarWhenExternallyEnabled(t *testing.T) {
	setupMarkerRestoreFixture(t)

	const authID = "acc-external"
	failoverStateMarkDisabled(authID, "m1")
	if !failoverStateIsDisabled(authID) {
		t.Fatalf("precondition: sidecar 应记为禁用")
	}

	// 假宿主：该账号盘上已 enabled。
	oldList := failoverListFn
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{
			{ID: "traework-" + authID + ".json", Name: "traework-" + authID + ".json", AuthIndex: "idx-" + authID, Disabled: false},
		}, nil
	}
	t.Cleanup(func() { failoverListFn = oldList })

	if cleared := reconcileFailoverLocalSet(); cleared == 0 {
		t.Fatalf("外部启用后对账必须至少清掉 1 个条目")
	}
	if failoverStateIsDisabled(authID) {
		t.Fatalf("外部启用后 sidecar 禁用必须被清掉，否则该号永久接不到流量")
	}
}

// TestReconcileKeepsSidecarWhenStillDisabled 反向守卫：盘上仍禁用时不得清。
func TestReconcileKeepsSidecarWhenStillDisabled(t *testing.T) {
	setupMarkerRestoreFixture(t)

	const authID = "acc-still"
	failoverStateMarkDisabled(authID, "m1")

	oldList := failoverListFn
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{
			{ID: "traework-" + authID + ".json", Name: "traework-" + authID + ".json", AuthIndex: "idx-" + authID, Disabled: true},
		}, nil
	}
	t.Cleanup(func() { failoverListFn = oldList })

	reconcileFailoverLocalSet()
	if !failoverStateIsDisabled(authID) {
		t.Fatalf("盘上仍 disabled 时不得清掉 sidecar 禁用")
	}
}

// TestSchedulerSkipsSidecarDisabledAccount 锁定调度层的排除链路。
//
// 必须调 handleSchedulerPick（生产入口）而不是只测纯函数：qoderwork 0.11.0 的
// 教训是「过滤行没接上时纯函数用例照样全绿」，变异存活。
func TestSchedulerSkipsSidecarDisabledAccount(t *testing.T) {
	setupMarkerRestoreFixture(t)
	clearAccountCacheForTest(t)

	const badID = "acc-sidecar-bad"
	const goodID = "acc-sidecar-good"
	// 只让 sidecar 记禁用：内存冷却为空、盘上无标记。
	failoverStateMarkDisabled(badID, "m1")

	oldMode := setSchedulerMode(schedulerModeCredits)
	t.Cleanup(oldMode)
	// credits 模式会读面板 pin；清掉 pin 让它回落到候选列表轮询。
	oldActive := getActiveAuthID()
	setActiveAuthID("")
	t.Cleanup(func() { setActiveAuthID(oldActive) })

	req := pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Model:    "qwen3.8-max",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: badID, Provider: providerName},
			{ID: goodID, Provider: providerName},
		},
	}
	raw, err := handleSchedulerPick(mustMarshal(t, req))
	if err != nil {
		t.Fatalf("handleSchedulerPick: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled {
		t.Fatalf("应至少选中健康账号，got 未处理")
	}
	if resp.AuthID == badID {
		t.Fatalf("sidecar 记禁用的账号不得被调度选中（调度层排除链断开会漏到这里）")
	}
	if resp.AuthID != goodID {
		t.Fatalf("应选中健康账号 %s，got %q", goodID, resp.AuthID)
	}
}
