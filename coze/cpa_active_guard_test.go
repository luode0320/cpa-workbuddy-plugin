package main

import (
	"testing"
)

// 本文件锁定「退役实例不得写盘」这条防线。
//
// 事故背景（2026-11-08 生产实证）：宿主热重载时只把旧实例标记为 retired，
// **不调用它的 cliproxyPluginShutdown**，于是旧实例的巡检 goroutine 继续存活，
// 用编译期的旧红线 200 把新实例刚解禁的 23 个 coze 账号整批重新禁用，
// 直接引发连续 4 个 503。
//
// 这些用例必须自带变异反证：把 allowWrite 判定去掉后必须 FAIL。

// TestActiveInstanceIsCurrentDetectsRetiredInstance 验证退役实例能被识别。
//
// 判据是「宿主报告的 path 里的版本号 == 自己编译期的版本号」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-08；改动原因：新增，退役实例识别。
func TestActiveInstanceIsCurrentDetectsRetiredInstance(t *testing.T) {
	original := cpaActiveGuardFetch
	t.Cleanup(func() { cpaActiveGuardFetch = original })

	// 宿主报告的是新版本（0.7.5），本实例是旧版本（0.7.4）：已退役。
	cpaActiveGuardFetch = func() string {
		return "plugins/linux/amd64/coze-provider-v0.7.9.so"
	}
	if activeInstanceIsCurrent() {
		t.Fatal("宿主报告的版本与本实例不一致时必须判定为已退役")
	}

	// 宿主报告的就是本实例版本：活跃。
	cpaActiveGuardFetch = func() string {
		return "plugins/linux/amd64/coze-provider-v" + version + ".so"
	}
	if !activeInstanceIsCurrent() {
		t.Fatal("宿主报告本实例版本时必须判定为活跃")
	}
}

// TestActiveInstanceIsCurrentTreatsUnknownAsAlive 验证宿主查不到时按活跃处理。
//
// 宿主接口异常不等于「我已退役」：若把未知判成退役，宿主一次抖动就会让
// 整池账号的禁用与解禁全部停摆，比误判更严重。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-08；改动原因：新增，未知语义保护。
func TestActiveInstanceIsCurrentTreatsUnknownAsAlive(t *testing.T) {
	original := cpaActiveGuardFetch
	t.Cleanup(func() { cpaActiveGuardFetch = original })

	cpaActiveGuardFetch = func() string { return "" }
	if !activeInstanceIsCurrent() {
		t.Fatal("宿主查不到自身路径时必须按活跃处理，不得停摆整池写盘")
	}
}

// TestEnsureActiveInstanceBeforeWriteBlocksRetiredInstance 验证退役实例被拦下。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-08；改动原因：新增，写盘许可判定。
func TestEnsureActiveInstanceBeforeWriteBlocksRetiredInstance(t *testing.T) {
	original := cpaActiveGuardFetch
	t.Cleanup(func() { cpaActiveGuardFetch = original })

	logged := 0
	logf := func(string, string, map[string]any) { logged++ }

	cpaActiveGuardFetch = func() string {
		return "plugins/linux/amd64/coze-provider-v0.0.1-retired.so"
	}
	if ensureActiveInstanceBeforeWrite(logf) {
		t.Fatal("已退役实例必须被拦下写盘")
	}
	if logged == 0 {
		t.Fatal("拦截时必须留痕，否则生产上无从判断为何某轮没有写盘")
	}

	cpaActiveGuardFetch = func() string {
		return "plugins/linux/amd64/coze-provider-v" + version + ".so"
	}
	if !ensureActiveInstanceBeforeWrite(logf) {
		t.Fatal("活跃实例必须放行")
	}
}
