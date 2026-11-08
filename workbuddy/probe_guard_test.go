// probe_guard_test.go 锁定「探活逐账号检查实例所有权」。
//
// 生产背景（2026-11-08）：旧实例若只等到 10 分钟 tick 边界才自查实例所有权，
// 这期间仍会整轮探测。实测单账号 60 分钟内被探活 54 次（多个实例并行）。
package main

import (
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// seedWorkbuddyProbeCandidate 把账号写入 sidecar 禁用集合与失败模型表，构造探活候选。
// [参数] t：测试上下文；authID：账号标识；model：回放模型。
// [返回] 无
// 最近修改时间：2026-11-08 22:45:00；改动原因：新增，测试辅助。
func seedWorkbuddyProbeCandidate(t *testing.T, authID, model string) {
	t.Helper()
	key := normalizeFailoverStateKey(authID)
	failoverStateMu.Lock()
	failoverStateAccounts[key] = failoverStateEntry{Disabled: true, Model: model}
	failoverStateMu.Unlock()
	failoverProbeModelMu.Lock()
	failoverProbeModels[key] = model
	failoverProbeModelMu.Unlock()
}

// TestRunFailoverProbeTickStopsWhenSuperseded 验证旧实例在探活中途即停止。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 22:45:00；改动原因：新增，锁住逐账号守卫。
func TestRunFailoverProbeTickStopsWhenSuperseded(t *testing.T) {
	path := pluginInstancePath()
	backup, backupErr := os.ReadFile(path)
	t.Cleanup(func() {
		if backupErr == nil {
			_ = os.WriteFile(path, backup, 0o644)
		} else {
			_ = os.Remove(path)
		}
	})

	claimPluginInstance()

	origList := failoverListFn
	origPhys := failoverGetPhysicalFn
	origPing := failoverPingFn
	t.Cleanup(func() {
		failoverListFn = origList
		failoverGetPhysicalFn = origPhys
		failoverPingFn = origPing
	})

	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{
			{ID: "acc-a", AuthIndex: "idx-a"},
			{ID: "acc-b", AuthIndex: "idx-b"},
		}, nil
	}
	failoverGetPhysicalFn = func(string) (*hostAuthPhysical, error) { return nil, os.ErrNotExist }
	seedWorkbuddyProbeCandidate(t, "acc-a", "deepseek-v4.1-flash")
	seedWorkbuddyProbeCandidate(t, "acc-b", "deepseek-v4.1-flash")

	pinged := 0
	failoverPingFn = func(sa *storedAuth, model string) error {
		pinged++
		return nil
	}

	if got := len(failoverProbeTargets()); got != 2 {
		t.Fatalf("候选账号数 = %d, want 2（测试构造不成立）", got)
	}

	if err := os.WriteFile(path, []byte("newer-instance"), 0o644); err != nil {
		t.Fatalf("写入新实例标记失败: %v", err)
	}

	probed := runFailoverProbeTick(func(string) *storedAuth {
		return &storedAuth{}
	})
	if probed != 0 {
		t.Fatalf("被取代的实例不得继续探活，实际探测 %d 个账号", probed)
	}
	if pinged != 0 {
		t.Fatalf("被取代的实例不得发出探活请求，实际 %d 次", pinged)
	}
}

// TestRunFailoverProbeTickProbesWhenStillOwner 验证所有权未被覆盖时探活照常进行。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 22:45:00；改动原因：新增，反证守卫没有误停活跃实例。
func TestRunFailoverProbeTickProbesWhenStillOwner(t *testing.T) {
	origList := failoverListFn
	origPhys := failoverGetPhysicalFn
	origPing := failoverPingFn
	t.Cleanup(func() {
		failoverListFn = origList
		failoverGetPhysicalFn = origPhys
		failoverPingFn = origPing
	})

	claimPluginInstance()
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{
			{ID: "acc-c", AuthIndex: "idx-c"},
		}, nil
	}
	failoverGetPhysicalFn = func(string) (*hostAuthPhysical, error) { return nil, os.ErrNotExist }
	seedWorkbuddyProbeCandidate(t, "acc-c", "deepseek-v4.1-flash")

	pinged := 0
	failoverPingFn = func(sa *storedAuth, model string) error {
		pinged++
		return nil
	}

	probed := runFailoverProbeTick(func(string) *storedAuth {
		return &storedAuth{}
	})
	if probed != 1 || pinged != 1 {
		t.Fatalf("活跃实例应正常探活：probed=%d pinged=%d, want 1/1", probed, pinged)
	}
}
