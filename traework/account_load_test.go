// account_load_test.go 覆盖账号在跑请求计数的生命周期与选号倾向。
//
// 背景（2026-11-10 生产实测）：上游 Trae 每账号并行槽位默认只有 2，插件原先按
// 会话粘性把同一会话的并发子请求全压在一个号上，账号并发中位数 16、峰值 25，
// 首字延迟随账号并发单调上升。本测试守住三件事：计数成对增减、Close 归还、
// 选号优先低负载。
package main

import (
	"sync"
	"testing"
)

// TestAccountLoadAcquireReleaseBalanced 验证计数成对增减并归零删条目。
func TestAccountLoadAcquireReleaseBalanced(t *testing.T) {
	clearAccountLoad()
	const authID = "auth-load-basic"

	release1 := accountLoadAcquire(authID)
	release2 := accountLoadAcquire(authID)
	if got := accountLoadOf(authID); got != 2 {
		t.Fatalf("在跑计数应为 2，实际 %d", got)
	}
	release1()
	if got := accountLoadOf(authID); got != 1 {
		t.Fatalf("释放一次后应为 1，实际 %d", got)
	}
	release1() // 幂等：重复调用不得把计数打成负数
	if got := accountLoadOf(authID); got != 1 {
		t.Fatalf("重复释放后仍应为 1，实际 %d", got)
	}
	release2()
	if got := accountLoadOf(authID); got != 0 {
		t.Fatalf("全部释放后应为 0，实际 %d", got)
	}
	accountLoadMu.Lock()
	_, exists := accountLoad[authID]
	accountLoadMu.Unlock()
	if exists {
		t.Fatalf("计数归零后条目应被删除，实际仍存在")
	}
}

// TestAccountLoadEmptyIDIgnored 验证空账号不参与记账，避免把未知归属算到空键上。
func TestAccountLoadEmptyIDIgnored(t *testing.T) {
	clearAccountLoad()
	release := accountLoadAcquire("")
	if got := accountLoadOf(""); got != 0 {
		t.Fatalf("空账号不应记账，实际 %d", got)
	}
	release()
	if got := accountLoadOf(""); got != 0 {
		t.Fatalf("空账号释放后仍应为 0，实际 %d", got)
	}
}

// TestAccountLoadConcurrent 验证并发增减下计数可回到零，回归数据竞争。
func TestAccountLoadConcurrent(t *testing.T) {
	clearAccountLoad()
	const authID = "auth-load-concurrent"
	const workers, perWorker = 16, 32

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				release := accountLoadAcquire(authID)
				_ = accountLoadOf(authID)
				release()
			}
		}()
	}
	wg.Wait()
	if got := accountLoadOf(authID); got != 0 {
		t.Fatalf("并发结束后计数应归零，实际 %d", got)
	}
}

// TestPickLeastLoadedPrefersIdle 验证选号优先在跑数为零的账号，而不是候选顺序首位。
func TestPickLeastLoadedPrefersIdle(t *testing.T) {
	clearAccountLoad()

	busy := accountLoadAcquire("auth-busy")
	defer busy()
	accountLoadAcquire("auth-busy-more")

	cands := []activeAuthCandidate{
		{ID: "auth-busy"},
		{ID: "auth-busy-more"},
		{ID: "auth-idle"},
	}
	if got := pickLeastLoaded(cands); got != "auth-idle" {
		t.Fatalf("应选在跑数最少的 auth-idle，实际 %q", got)
	}
}

// TestPickLeastLoadedTieKeepsOrder 验证平级时保留候选原有顺序（高积分优先不被破坏）。
func TestPickLeastLoadedTieKeepsOrder(t *testing.T) {
	clearAccountLoad()
	cands := []activeAuthCandidate{{ID: "auth-first"}, {ID: "auth-second"}}
	if got := pickLeastLoaded(cands); got != "auth-first" {
		t.Fatalf("平级应保留原顺序取 auth-first，实际 %q", got)
	}
}

// TestPickLeastLoadedSkipsDisabledAndEmpty 验证禁用、耗尽、空 ID 不参与选号。
func TestPickLeastLoadedSkipsDisabledAndEmpty(t *testing.T) {
	clearAccountLoad()
	cands := []activeAuthCandidate{
		{ID: ""},
		{ID: "auth-disabled", Disabled: true},
		{ID: "auth-exhausted", Exhausted: true},
	}
	if got := pickLeastLoaded(cands); got != "" {
		t.Fatalf("无可用候选应返回空串，实际 %q", got)
	}
}
