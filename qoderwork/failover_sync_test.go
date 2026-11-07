// failover_sync_test.go 锁定「失败禁用 ↔ 手动启用」的联动契约（qoderwork）。
//
// 与 workbuddy 0.21.13 同批整改。本插件的探活候选以 sidecar 为主，因此重点在：
//  1. 恢复走宿主 API 成功后必须同时清掉盘上归属标记（否则「兼容旧数据」那一路
//     会把已启用的号当禁用号反复探活，Qoder 探活与真实推理共享日配额）；
//  2. 对账判据必须是「盘上确实 enabled」，不能只看宿主列表（写盘未吸收时会把
//     刚摘掉的坏号放回路由）；
//  3. 对账清标记与计数只针对确曾归属本机制的号，健康号计数不得被无差别清零。
package main

import (
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// isolateFailoverSidecar 把 sidecar 指到临时文件，避免用例之间互相污染。
func isolateFailoverSidecar(t *testing.T) {
	t.Helper()
	restore := isolateFailoverStatePathForTest(filepath.Join(t.TempDir(), failoverStateFileName))
	t.Cleanup(restore)
}

// TestReenableClearsDiskMarkersOnAPIPath 锁定恢复必须同时清盘上标记。
func TestReenableClearsDiskMarkersOnAPIPath(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	calls, restore := withCPADisableSync(t, true)
	defer restore()

	h.addAccount("qdw-api", "idx-api", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "dfmodel",
		failoverDisableAtKey:    "2026-11-07T11:58:07Z",
		"note":                  "keep",
	})
	if !reenableAccountForFailover("idx-api", "qdw-api") {
		t.Fatal("恢复应成功")
	}
	if len(*calls) != 1 || (*calls)[0].Disabled {
		t.Fatalf("应走 API 置回 enabled, calls=%+v", *calls)
	}
	doc := h.docOf("idx-api")
	if doc["disabled"] != false {
		t.Fatalf("disabled = %v, want false", doc["disabled"])
	}
	for _, k := range []string{failoverDisableKey, failoverDisableModelKey, failoverDisableAtKey} {
		if _, ok := doc[k]; ok {
			t.Fatalf("API 恢复后盘上仍残留 %s: %v", k, doc)
		}
	}
	if doc["note"] != "keep" {
		t.Fatalf("清理标记不得动其它顶层键: %v", doc)
	}
}

// TestReconcileClearsStaleDiskMarkersAndCounters 锁定手动启用后的完整对账。
func TestReconcileClearsStaleDiskMarkersAndCounters(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("qdw-manual", "idx-manual", map[string]any{
		"disabled":              false,
		failoverDisableKey:      true,
		failoverDisableModelKey: "dfmodel",
		failoverDisableAtKey:    "2026-11-07T11:49:40Z",
	})
	h.addAccount("qdw-spare", "idx-spare", nil)

	markFailoverDisabledLocal("qdw-manual", "idx-manual")
	failoverStateMarkDisabled("qdw-manual", "dfmodel")
	recordCooldownState("qdw-manual")
	recordCooldownState("qdw-manual")

	if cleared := reconcileFailoverLocalSet(); cleared == 0 {
		t.Fatal("手动启用的号必须被对账")
	}
	if isAccountCoolingDown("qdw-manual") {
		t.Fatal("对账后不得继续排除该号")
	}
	if failoverStateIsDisabled("qdw-manual") {
		t.Fatal("对账后 sidecar 必须解除")
	}
	if _, ok := h.docOf("idx-manual")[failoverDisableKey]; ok {
		t.Fatalf("对账必须清掉盘上陈旧标记: %v", h.docOf("idx-manual"))
	}
	if count, _, ok := failoverStateSnapshot("qdw-manual"); ok && count != 0 {
		t.Fatalf("对账后失败计数残留 %d", count)
	}
	if got := h.docOf("idx-manual")["disabled"]; got != false {
		t.Fatalf("对账不得改动 disabled, got %v", got)
	}
}

// TestReconcileKeepsCounterForHealthyAccount 锁定对账不得无差别清零。
func TestReconcileKeepsCounterForHealthyAccount(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("qdw-healthy", "idx-healthy", nil)
	h.addAccount("qdw-spare", "idx-spare", nil)

	recordCooldownState("qdw-healthy")
	recordCooldownState("qdw-healthy")

	reconcileFailoverLocalSet()

	count, _, ok := failoverStateSnapshot("qdw-healthy")
	if !ok || count != 2 {
		t.Fatalf("健康号的失败计数被误清: ok=%v count=%d", ok, count)
	}
}

// TestReconcileKeepsSidecarWhenDiskStillDisabled 锁定判据收紧：
// 宿主列表报 enabled 但盘上仍 disabled（写盘尚未吸收）时不得清排除。
func TestReconcileKeepsSidecarWhenDiskStillDisabled(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("qdw-pending", "idx-pending", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "dfmodel",
	})
	failoverStateMarkDisabled("qdw-pending", "dfmodel")
	markFailoverDisabledLocal("qdw-pending", "idx-pending")

	// 宿主列表报 enabled（写盘尚未被吸收），但盘上仍 disabled。
	origList := failoverListFn
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{
			{ID: "qdw-pending", Name: "qoderwork-qdw-pending.json", AuthIndex: "idx-pending", Disabled: false},
		}, nil
	}
	defer func() { failoverListFn = origList }()

	if cleared := reconcileFailoverLocalSet(); cleared != 0 {
		t.Fatalf("盘上仍 disabled 时不得清排除, cleared=%d", cleared)
	}
	if !failoverStateIsDisabled("qdw-pending") {
		t.Fatal("盘上仍 disabled 时应保留 sidecar 禁用")
	}
}

// TestReconcileSkipsUnknownAccounts 锁定对账保守方向。
func TestReconcileSkipsUnknownAccounts(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	newFailoverFakeHost(t)
	markFailoverDisabledLocal("ghost", "idx-ghost")
	failoverStateMarkDisabled("ghost", "dfmodel")

	if cleared := reconcileFailoverLocalSet(); cleared != 0 {
		t.Fatalf("列表里没有的号不得被清除, cleared=%d", cleared)
	}
	if !failoverStateIsDisabled("ghost") {
		t.Fatal("查不到的号必须保留 sidecar 禁用")
	}
}
