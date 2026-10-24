// panel_cache_test.go 守护「面板默认渲染不碰上游、也不提前丢缓存」这两条约束。
//
// 背景：面板移除自动刷新后，打开面板成为唯一的高频入口且不会再补数据。
// 历史实现有两处依赖「每次打开都会全量刷新」的假设，移除后都会静默退化：
//  1. 渲染路径对无 plan 账号直接查上游注册态（每个账号一次 HTTP）；
//  2. 缓存按 4×TTL（3 分钟）过期删除，而能补数据的只剩 10 分钟巡检。
package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestCachedRegisteredStateNeverCallsUpstream 断言默认渲染口径不触网。
//
// resolveRegisteredState 在无 plan 且未探测过时会调用 fetchRegistrationStatus；
// 未注入 host 桩时该调用必然失败——因此「返回 false 且不 panic」正好证明渲染路径
// 走的是纯缓存分支。若有人把 cachedRegisteredState 换回 resolveRegisteredState，
// 该用例会在缺少 host 桥的环境下暴露。
func TestCachedRegisteredStateNeverCallsUpstream(t *testing.T) {
	const id = "wbai-reg-cache"
	accountCache.Delete(id)
	t.Cleanup(func() { accountCache.Delete(id) })

	if got := cachedRegisteredState(id, "free"); !got {
		t.Fatalf("有 plan 即视为已注册, got %v", got)
	}
	// 无 plan 且从未探测：按未注册处理，且不得发请求。
	if got := cachedRegisteredState(id, ""); got {
		t.Fatalf("未探测账号应为 false, got %v", got)
	}
	// 已缓存的探测结果必须被采用。
	truth := true
	accountCache.Store(id, &accountCacheEntry{registered: &truth})
	if got := cachedRegisteredState(id, ""); !got {
		t.Fatalf("应复用缓存中的探测结果, got %v", got)
	}
}

// TestDashboardRenderPathUsesCachedRegistration 断言渲染路径真的调用了缓存版。
//
// cachedRegisteredState 自己的单测通过，并不能证明 buildDashboardEx 用的是它——
// 这正是「纯函数单测通过 ≠ 调用点被覆盖」的形态（实测把调用换回触网版后全绿）。
// 因此在源码层机械断言：默认渲染分支必须出现 cachedRegisteredState。
func TestDashboardRenderPathUsesCachedRegistration(t *testing.T) {
	data, err := os.ReadFile("panel.go")
	if err != nil {
		t.Fatalf("read panel.go: %v", err)
	}
	var hits int
	for _, ln := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "func cachedRegisteredState") {
			continue
		}
		if strings.Contains(ln, "cachedRegisteredState(") {
			hits++
		}
	}
	if hits == 0 {
		t.Fatal("buildDashboardEx 的默认渲染分支必须用 cachedRegisteredState，否则打开面板会对每个无 plan 账号打一次上游")
	}
}

// TestPruneAccountCacheKeepsLiveStaleEntry 断言清理只按账号存活与否，
// 不按 fetched 年龄——自动刷新移除后，刷新间隔就是 watchdog 的 10 分钟。
func TestPruneAccountCacheKeepsLiveStaleEntry(t *testing.T) {
	const id = "wbai-stale-entry"
	const goneID = "wbai-gone-entry"
	for _, k := range []string{id, goneID} {
		accountCache.Delete(k)
		k := k
		t.Cleanup(func() { accountCache.Delete(k) })
	}

	accountCache.Store(id, &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 42},
		plan:    "free",
		fetched: time.Now().Add(-3 * 4 * accountCacheTTL), // 远超 4×TTL
	})
	accountCache.Store(goneID, &accountCacheEntry{fetched: time.Now()})

	pruneAccountCacheForLive(map[string]struct{}{id: {}})

	if _, ok := accountCache.Load(id); !ok {
		t.Fatal("在位账号的缓存不得因过期被删：巡检之间面板会显示空白")
	}
	if _, ok := accountCache.Load(goneID); ok {
		t.Fatal("已消失账号的缓存必须被回收")
	}
}
