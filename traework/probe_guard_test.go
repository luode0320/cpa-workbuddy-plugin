// probe_guard_test.go 锁定「探活逐账号检查实例所有权」。
//
// 生产背景（2026-11-08）：旧实例若只等到 10 分钟 tick 边界才自查实例所有权，
// 这期间仍会整轮探测。实测修复前 45 分钟内每账号被探活 59 次（9 个实例并行）。
package main

import (
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// seedProbeCandidate 把账号写入 sidecar 禁用集合与失败模型表，构造探活候选。
// [参数] t：测试上下文；authID：账号标识；model：回放模型。
// [返回] 无
// 最近修改时间：2026-11-08 22:25:00；改动原因：新增，测试辅助。
func seedProbeCandidate(t *testing.T, authID, model string) {
	t.Helper()
	failoverStateMu.Lock()
	failoverStateAccounts[normalizeFailoverStateKey(authID)] = failoverStateEntry{Disabled: true, Model: model}
	failoverStateMu.Unlock()
	// 探活回放模型取自失败模型表；表为空时退化为固定兜底模型，
	// 两者都必须非空，否则该账号会被判为「无模型可探」而跳过。
	failoverProbeModelMu.Lock()
	failoverProbeModels[normalizeFailoverStateKey(authID)] = model
	failoverProbeModelMu.Unlock()
}

// TestRunFailoverProbeTickStopsWhenSuperseded 验证旧实例在探活中途即停止。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 22:15:00；改动原因：新增，锁住逐账号守卫。
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

	// 1. 本实例登记所有权。
	claimPluginInstance()

	// 2. 构造两个候选账号（探活目标来自宿主列表 + 本机制禁用标记）。
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
	// 物理文档读不到：此时只有 sidecar 记着禁用才算候选。
	failoverGetPhysicalFn = func(string) (*hostAuthPhysical, error) { return nil, os.ErrNotExist }
	seedProbeCandidate(t, "acc-a", "deepseek-v4.1-flash")
	seedProbeCandidate(t, "acc-b", "deepseek-v4.1-flash")

	pinged := 0
	failoverPingFn = func(sa *traeAuth, model string) error {
		pinged++
		return nil
	}

	// 3. 先确认候选确实存在（否则后续断言无意义）。
	if got := len(failoverProbeTargets()); got != 2 {
		t.Fatalf("候选账号数 = %d, want 2（测试构造不成立）", got)
	}

	// 4. 所有权被新实例覆盖后，本轮探活必须一个账号都不探。
	if err := os.WriteFile(path, []byte("newer-instance"), 0o644); err != nil {
		t.Fatalf("写入新实例标记失败: %v", err)
	}

	probed := runFailoverProbeTick(func(string) *traeAuth {
		return &traeAuth{UserID: "probe-user"}
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
// 最近修改时间：2026-11-08 22:20:00；改动原因：新增，反证守卫没有误停活跃实例。
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
	seedProbeCandidate(t, "acc-c", "deepseek-v4.1-flash")

	pinged := 0
	failoverPingFn = func(sa *traeAuth, model string) error {
		pinged++
		return nil
	}

	probed := runFailoverProbeTick(func(string) *traeAuth {
		return &traeAuth{UserID: "probe-user"}
	})
	if probed != 1 || pinged != 1 {
		t.Fatalf("活跃实例应正常探活：probed=%d pinged=%d, want 1/1", probed, pinged)
	}
}
