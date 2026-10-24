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

// TestPanelSurfacesFailoverDisabled 静态审计面板的「失败禁用」展示接线。
//
// 双写下冷却最长只有 10 分钟，而盘上 disabled 要等 10 分钟探活探通才解除。
// 面板若只显示冷却倒计时，用户会把它误读成「多少秒后自动恢复」。这条接线
// 分三段，任何一段被删都会让该状态从面板消失，因此逐段扫源码守护：
//  1. 服务端从盘上读 failover_disable 标记与回放模型；
//  2. 服务端用内存集合补漏（写盘到 watcher 生效的毫秒级窗口）；
//  3. 前端渲染「失败禁用」状态段。
//
// [返回] 无；断言失败直接 t.Fatal。
// 最近修改时间：2026-10-25；改动原因：新增，对齐国内版面板的失败禁用展示。
func TestPanelSurfacesFailoverDisabled(t *testing.T) {
	reads := func(name string) string {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		return string(raw)
	}

	panelGo := reads("panel.go")
	// 1. 盘上标记读取（含 failoverDisable 与 disabled 双条件，避免手工启用后误标）。
	if !strings.Contains(panelGo, "doc.failoverDisable && doc.disabled") {
		t.Fatal("panel.go 应只在盘上标记与 disabled 同时成立时判为失败禁用")
	}
	if !strings.Contains(panelGo, "acct.FailoverModel = doc.failoverModel") {
		t.Fatal("panel.go 应从盘上带出探活回放模型")
	}
	// 2. 内存补漏（写盘尚未被宿主吸收的窗口）。
	//
	// 这条判据必须逐字匹配整行条件，不能只查子串：写成 `if false && ...` 时
	// 运行时行为已断，但它仍是合法源码、字符串照旧命中，静态审计会静默失效
	// （实测该变异能存活）。
	const memFallback = "if !out[i].FailoverDisabled && isFailoverDisabledLocal(out[i].AuthID) {"
	if !strings.Contains(panelGo, memFallback) {
		t.Fatal("panel.go 应无条件补查内存禁用集合，覆盖写盘到 watcher 生效的窗口")
	}
	if !strings.Contains(panelGo, "failoverProbeModelFor(out[i].AuthID)") {
		t.Fatal("panel.go 补漏路径应带出内存里的回放模型")
	}

	panelHTML := reads("panel.html")
	// 3. 前端状态段与筛选口径。
	if !strings.Contains(panelHTML, "a.failover_disabled") {
		t.Fatal("panel.html 应渲染失败禁用状态段并纳入失败口径")
	}
	if !strings.Contains(panelHTML, "（10 分钟探活中）") {
		t.Fatal("panel.html 应说明该状态由 10 分钟探活解除，避免与冷却倒计时混淆")
	}
}
