package main

import (
	"sync"
	"testing"
	"time"
)

// TestClaimActiveTestRejectsConcurrent 校验同账号并发测试只放行一次。
//
// 面板连点是 710022002 最常见的触发方式：每点一次就是一个真实上游请求，
// 用户看到「没反应」会继续点，等于自己把账号打成限流态。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定防重入。
func TestClaimActiveTestRejectsConcurrent(t *testing.T) {
	resetActiveTestStates()
	t.Cleanup(resetActiveTestStates)

	const authID = "doubao-activeclaim"
	if _, ok := claimActiveTest(authID); !ok {
		t.Fatal("第一次占位应成功")
	}
	if _, ok := claimActiveTest(authID); ok {
		t.Fatal("占位未释放时第二次不应成功")
	}

	releaseActiveTest(authID)
	if _, ok := claimActiveTest(authID); ok {
		t.Fatal("冷却窗口内不应立刻放行")
	}
}

// TestClaimActiveTestConcurrentOnlyOneWins 用并发压测确认只有一次抢到名额。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，并发下防重入会失效。
func TestClaimActiveTestConcurrentOnlyOneWins(t *testing.T) {
	resetActiveTestStates()
	t.Cleanup(resetActiveTestStates)

	const authID = "doubao-activeclaim"
	var (
		wg      sync.WaitGroup
		wins    atomicInt
		workers = 20
	)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if _, ok := claimActiveTest(authID); ok {
				wins.add(1)
			}
		}()
	}
	wg.Wait()

	if got := wins.load(); got != 1 {
		t.Fatalf("并发下应只有一次成功占位，实际 %d", got)
	}
}

// TestReleaseActiveTestAllowsAfterCooldown 校验冷却结束后可再次测试。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定时限语义。
func TestReleaseActiveTestAllowsAfterCooldown(t *testing.T) {
	resetActiveTestStates()
	t.Cleanup(resetActiveTestStates)

	const authID = "doubao-activeclaim"
	if _, ok := claimActiveTest(authID); !ok {
		t.Fatal("第一次占位应成功")
	}
	releaseActiveTest(authID)

	// 1. 直接把上次完成时间推远，模拟冷却已过。
	activeTestMu.Lock()
	if state := activeTestStates[authID]; state != nil {
		state.lastAt = time.Now().Add(-2 * activeTestCooldown)
	}
	activeTestMu.Unlock()

	if _, ok := claimActiveTest(authID); !ok {
		t.Fatal("冷却结束后应能再次占位")
	}
}

// atomicInt 是只在本文件用到的极小计数器，避免为两个用例引入额外依赖。
type atomicInt struct {
	mu sync.Mutex
	n  int
}

// add 累加计数。
// [参数] delta: 增量。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，并发计数。
func (a *atomicInt) add(delta int) {
	a.mu.Lock()
	a.n += delta
	a.mu.Unlock()
}

// load 读取当前计数。
// [参数] 无。
// [返回] 当前值。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，并发计数。
func (a *atomicInt) load() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}
