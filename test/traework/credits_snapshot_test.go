package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 积分快照的三条硬约束（2026-10-25）：
//  1. 跨重启存活——热重载后 cachedCredits 仍能命中，面板首屏不必逐个现查上游；
//  2. nil 不写 0——「未知」被写成 0 会让账号被判定为耗尽，方向性错误；
//  3. 与故障 sidecar 分文件——高频积分替换不得冲掉低频禁用状态。

// setupCreditSnapshotDir 把积分 sidecar 指向 t.TempDir() 并清理内存镜像。
func setupCreditSnapshotDir(t *testing.T) string {
	t.Helper()
	resetCreditSnapshotForTest()
	path := filepath.Join(t.TempDir(), creditSnapshotName)
	restore := isolateCreditSnapshotPathForTest(path)
	t.Cleanup(func() {
		restore()
		resetCreditSnapshotForTest()
	})
	return path
}

// clearAccountCacheForTest 清空积分内存缓存（复刻重启后的空内存状态）。
func clearAccountCacheForTest(t *testing.T) {
	t.Helper()
	accountCache.Range(func(k, _ any) bool {
		accountCache.Delete(k)
		return true
	})
	t.Cleanup(func() {
		accountCache.Range(func(k, _ any) bool {
			accountCache.Delete(k)
			return true
		})
	})
}

// TestCreditsSnapshot_SurvivesRestart 锁定跨重启存活，含完整明细。
func TestCreditsSnapshot_SurvivesRestart(t *testing.T) {
	setupCreditSnapshotDir(t)
	clearAccountCacheForTest(t)

	cr := &traeCredits{
		TotalRemain: 1234,
		TotalUsed:   766,
		TotalSize:   2000,
		PackCount:   3,
		FetchedAt:   "2026-10-25T06:00:00Z",
	}
	recordCreditSnapshot("traework-acc-snap", cr)
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// 复刻重启：清内存，磁盘文件保留。
	resetCreditSnapshotForTest()

	entry, ok := creditSnapshotGet("acc-snap")
	if !ok {
		t.Fatalf("重启后必须能从磁盘读到积分快照")
	}
	if entry.Remain != 1234 {
		t.Fatalf("Remain 必须存活，want 1234 got %d", entry.Remain)
	}
	if entry.Credits == nil {
		t.Fatalf("完整明细必须存活（面板进度条依赖它）")
	}
	if entry.Credits.TotalUsed != 766 || entry.Credits.TotalSize != 2000 || entry.Credits.PackCount != 3 {
		t.Fatalf("明细字段必须完整存活，got %+v", *entry.Credits)
	}
	if entry.FetchedAt != "2026-10-25T06:00:00Z" {
		t.Fatalf("抓取时刻必须存活，got %q", entry.FetchedAt)
	}
}

// TestCreditsSnapshot_IgnoresNilCredits 锁定「未知不得写成 0」。
func TestCreditsSnapshot_IgnoresNilCredits(t *testing.T) {
	setupCreditSnapshotDir(t)
	clearAccountCacheForTest(t)

	recordCreditSnapshot("acc-nil", nil)
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	resetCreditSnapshotForTest()

	if entry, ok := creditSnapshotGet("acc-nil"); ok {
		t.Fatalf("nil 积分不得写入快照（会把账号误判为耗尽），got %+v", entry)
	}
	// 生产读入口同样不得把 nil 当成 0。
	if _, ok := cachedCredits("acc-nil"); ok {
		t.Fatalf("nil 积分不得经 cachedCredits 返回 0 值")
	}
}

// TestCreditsSnapshot_ToleratesCorruptFile 锁定损坏容错。
func TestCreditsSnapshot_ToleratesCorruptFile(t *testing.T) {
	path := setupCreditSnapshotDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"accounts":{`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := creditSnapshotGet("acc-corrupt"); ok {
		t.Fatalf("损坏文件必须按空状态处理")
	}
	// 写入必须仍然可用。
	recordCreditSnapshot("acc-corrupt", &traeCredits{TotalRemain: 50})
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist after corrupt: %v", err)
	}
	resetCreditSnapshotForTest()
	if entry, ok := creditSnapshotGet("acc-corrupt"); !ok || entry.Remain != 50 {
		t.Fatalf("损坏文件之后写操作必须能覆盖为正常快照，got %+v ok=%v", entry, ok)
	}
}

// TestCreditsSnapshot_VisibleThroughCacheAfterRestart 锁定生产读入口的回落。
//
// 这条是本任务的核心价值：断言必须走 cachedCredits（面板与调度真正读的函数），
// 而不是 creditSnapshotGet 这个内部函数——否则删掉回落分支测试仍然通过。
func TestCreditsSnapshot_VisibleThroughCacheAfterRestart(t *testing.T) {
	setupCreditSnapshotDir(t)
	clearAccountCacheForTest(t)

	cacheCredits("acc-cache", &traeCredits{TotalRemain: 800})
	recordCreditSnapshot("acc-cache", &traeCredits{TotalRemain: 800})
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// 复刻重启：内存缓存与快照镜像都清空，只留磁盘。
	resetCreditSnapshotForTest()
	accountCache.Range(func(k, _ any) bool {
		accountCache.Delete(k)
		return true
	})

	cr, ok := cachedCredits("acc-cache")
	if !ok || cr == nil {
		t.Fatalf("重启后 cachedCredits 必须从 sidecar 快照回落")
	}
	if cr.TotalRemain != 800 {
		t.Fatalf("回落后积分值必须正确，want 800 got %d", cr.TotalRemain)
	}
	// 回落后排序判据也必须可用（不是 -1 未知）。
	if score, _ := cachedCreditsScore("acc-cache"); score != 800 {
		t.Fatalf("回落后 cachedCreditsScore 必须是真实值，got %d", score)
	}
}

// TestCreditsSnapshot_SeparateFileFromFailoverState 锁定两个 sidecar 分文件。
//
// 合并会让高频积分替换冲掉低频禁用状态，这是 sidecar 模式的通用陷阱。
func TestCreditsSnapshot_SeparateFileFromFailoverState(t *testing.T) {
	setupCreditSnapshotDir(t)
	resetFailoverStateForTest()
	failoverTemp := filepath.Join(t.TempDir(), failoverStateFileName)
	restore := isolateFailoverStatePathForTest(failoverTemp)
	t.Cleanup(func() {
		restore()
		resetFailoverStateForTest()
	})

	if creditSnapshotName == failoverStateFileName {
		t.Fatalf("两个 sidecar 的文件名不得相同")
	}
	if got, want := creditSnapshotPath(), failoverStatePath(); got == want {
		t.Fatalf("积分与故障状态必须分文件，当前都指向 %s", got)
	}
	// 反向守卫：积分写入不得影响故障状态，反之亦然。
	failoverStateMarkDisabled("acc-both", "m1")
	recordCreditSnapshot("acc-both", &traeCredits{TotalRemain: 10})
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist credits: %v", err)
	}
	if !failoverStateIsDisabled("acc-both") {
		t.Fatalf("写积分快照不得冲掉故障禁用状态")
	}
}

// TestCreditsSnapshot_Forget 锁定删除账号时快照一并清除。
func TestCreditsSnapshot_Forget(t *testing.T) {
	setupCreditSnapshotDir(t)
	clearAccountCacheForTest(t)

	recordCreditSnapshot("acc-forget", &traeCredits{TotalRemain: 100})
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	forgetCreditSnapshot("acc-forget")
	resetCreditSnapshotForTest()
	if _, ok := creditSnapshotGet("acc-forget"); ok {
		t.Fatalf("删除账号后快照必须一并清除")
	}
}
