package main

import (
	"sync"
	"testing"
	"time"
)

// 本文件锁定巡检链路的并发安全。
//
// 巡检是多 worker 并发执行，而快照镜像被面板读取、签到落盘会触发整表写盘。
// 三者共用一把 RWMutex：一旦有人在持锁期间再次获取同一把锁（Go 的
// RWMutex 不可重入），整轮巡检会永久挂死——表现为「面板一直显示巡检中」
// 且再也不更新积分。这类缺陷在单线程单测里完全看不出来。

// TestCreditSnapshotConcurrentWriters 验证并发写入不丢数据也不死锁。
//
// 每个 goroutine 写自己的账号键，结束后必须全部可见：
// 少一条就说明写路径存在覆盖或竞态。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，并发写覆盖防护。
func TestCreditSnapshotConcurrentWriters(t *testing.T) {
	t.Cleanup(resetCreditSnapshotForTest)
	resetCreditSnapshotForTest()

	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			authID := "coze-conc-" + string(rune('a'+idx%26)) + string(rune('0'+idx/26))
			recordCreditSnapshot(authID, &creditSummary{Total: 1500, Remain: int64(idx)}, "并发账号")
		}(i)
	}
	wg.Wait()

	all := creditSnapshotAll()
	if len(all) != n {
		t.Fatalf("并发写入后应有 %d 条快照，实际 %d", n, len(all))
	}
}

// TestRecordDailyClaimedDoesNotDeadlock 验证签到落盘不会自锁。
//
// recordDailyClaimedSnapshot 在锁内改内存、释放后再整表落盘；
// 若把 persistCreditSnapshot 挪进锁内，就会在同一个 goroutine 里
// 二次获取同一把 RWMutex，整轮巡检永久挂死。
// 用例用超时兜底：死锁时测试会超时失败，而不是静默通过。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，锁重入死锁防护。
func TestRecordDailyClaimedDoesNotDeadlock(t *testing.T) {
	t.Cleanup(resetCreditSnapshotForTest)
	resetCreditSnapshotForTest()

	done := make(chan struct{})
	go func() {
		defer close(done)
		recordDailyClaimedSnapshot("coze-deadlock", 1500)
	}()

	select {
	case <-done:
	case <-testTimeoutChan():
		t.Fatal("签到落盘发生死锁：锁内二次获取了同一把 RWMutex")
	}

	entry, ok := creditSnapshotGet("coze-deadlock")
	if !ok || entry.DailyClaimedAmount != 1500 {
		t.Fatalf("签到态应写入快照，实际 %+v ok=%v", entry, ok)
	}
}

// testTimeoutChan 返回一个用于死锁检测的超时通道。
// [参数] 无。
// [返回] 5 秒后关闭的通道。
// 最近修改时间：2026-10-25；改动原因：新增，避免测试永久挂起。
func testTimeoutChan() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		// 死锁检测不需要精确计时，5 秒足以覆盖正常写盘。
		time.Sleep(5 * time.Second)
		close(ch)
	}()
	return ch
}

// TestCreditSnapshotPersistIsAtomic 验证落盘走临时文件 + 重命名。
//
// 直接覆盖写会让并发读者看到半截 JSON，进而把整份快照解析失败、
// 当成「没有数据」，所有账号退回未知。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，原子写验证。
func TestCreditSnapshotPersistIsAtomic(t *testing.T) {
	t.Cleanup(resetCreditSnapshotForTest)
	resetCreditSnapshotForTest()

	recordCreditSnapshot("coze-atomic", &creditSummary{Total: 1500, Remain: 500}, "原子账号")
	// 连写两次：第二次写盘时磁盘上已有完整文件，
	// 若实现改成原地覆盖，中途异常会留下截断内容。
	if errPersist := persistCreditSnapshot(); errPersist != nil {
		t.Fatalf("首次落盘失败：%v", errPersist)
	}
	recordCreditSnapshot("coze-atomic", &creditSummary{Total: 1500, Remain: 600}, "原子账号")
	if errPersist := persistCreditSnapshot(); errPersist != nil {
		t.Fatalf("二次落盘失败：%v", errPersist)
	}

	resetCreditSnapshotForTest()
	entry, ok := creditSnapshotGet("coze-atomic")
	if !ok || entry.Remain != 600 {
		t.Fatalf("二次落盘后应读到最新值 600，实际 %+v ok=%v", entry, ok)
	}
}
