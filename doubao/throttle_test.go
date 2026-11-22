package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newThrottleTestAccount 造一个只用于闸门用例的账号标识。
// [参数] t: 测试上下文。
// [返回] 账号标识。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，闸门用例夹具。
func newThrottleTestAccount(t *testing.T) string {
	t.Helper()
	resetUpstreamGates()
	t.Cleanup(resetUpstreamGates)
	return "doubao-throttletest"
}

// testThrottleSetting 是闸门用例的短间隔设置，避免每个用例都等满 2 秒。
// [参数] maxInflight: 在途上限；minInterval: 最小间隔；backoff: 软退避时长。
// [返回] 闸门设置。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，压缩用例等待。
func testThrottleSetting(maxInflight int, minInterval, backoff time.Duration) throttleSetting {
	return throttleSetting{maxInflight: maxInflight, minInterval: minInterval, backoff: backoff}
}

// TestAcquireUpstreamRespectsInflightLimit 校验同一账号在途数不超过上限。
//
// 这是 710022002 的正面防线：并发全由宿主决定，插件不给上限就等于把限流权
// 交给上游随机拒绝。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定在途上限。
func TestAcquireUpstreamRespectsInflightLimit(t *testing.T) {
	authID := newThrottleTestAccount(t)
	restore := swapThrottleSetting(testThrottleSetting(1, 0, time.Millisecond))
	defer restore()

	first, errFirst := acquireUpstream(context.Background(), authID, upstreamKindChat)
	if errFirst != nil {
		t.Fatalf("第一次取许可应成功，实际 %v", errFirst)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, errSecond := acquireUpstream(ctx, authID, upstreamKindChat); errSecond == nil {
		t.Fatal("在途已满时第二次取许可应等待到 ctx 超时")
	}

	releaseUpstream(first, nil)
	second, errSecond := acquireUpstream(context.Background(), authID, upstreamKindChat)
	if errSecond != nil {
		t.Fatalf("归还后应能再次取到许可，实际 %v", errSecond)
	}
	releaseUpstream(second, nil)
}

// TestAcquireUpstreamEnforcesMinInterval 校验两次发起之间保持最小间隔。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定速率闸门。
func TestAcquireUpstreamEnforcesMinInterval(t *testing.T) {
	authID := newThrottleTestAccount(t)
	const minInterval = 40 * time.Millisecond
	restore := swapThrottleSetting(testThrottleSetting(1, minInterval, time.Millisecond))
	defer restore()

	started := time.Now()
	first, errFirst := acquireUpstream(context.Background(), authID, upstreamKindChat)
	if errFirst != nil {
		t.Fatalf("第一次取许可应成功，实际 %v", errFirst)
	}
	releaseUpstream(first, nil)

	second, errSecond := acquireUpstream(context.Background(), authID, upstreamKindChat)
	if errSecond != nil {
		t.Fatalf("第二次取许可应成功，实际 %v", errSecond)
	}
	releaseUpstream(second, nil)

	if elapsed := time.Since(started); elapsed < minInterval {
		t.Errorf("两次发起间隔应不小于 %v，实际 %v", minInterval, elapsed)
	}
}

// TestReleaseUpstreamAppliesSoftBackoff 校验访问频繁会写入软退避窗口。
//
// 只认 710022002：其它业务码不能触发退避，否则一次普通失败会让账号白停一分钟。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定软退避触发条件。
func TestReleaseUpstreamAppliesSoftBackoff(t *testing.T) {
	authID := newThrottleTestAccount(t)
	const backoff = 80 * time.Millisecond
	restore := swapThrottleSetting(testThrottleSetting(1, 0, backoff))
	defer restore()

	ticket, errTicket := acquireUpstream(context.Background(), authID, upstreamKindChat)
	if errTicket != nil {
		t.Fatalf("取许可应成功，实际 %v", errTicket)
	}
	releaseUpstream(ticket, &upstreamError{Code: rateLimitFrequentCode})

	if remaining := softBackoffRemaining(authID); remaining <= 0 {
		t.Fatal("访问频繁后应处于软退避窗口内")
	}

	// 1. 窗口内取许可必须排队：ctx 先超时即为正确表现。
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, errWait := acquireUpstream(ctx, authID, upstreamKindChat); errWait == nil {
		t.Fatal("软退避窗口内不应立刻拿到许可")
	}

	// 2. 窗口过去后应恢复。
	time.Sleep(backoff + 30*time.Millisecond)
	after, errAfter := acquireUpstream(context.Background(), authID, upstreamKindChat)
	if errAfter != nil {
		t.Fatalf("软退避结束后应能取到许可，实际 %v", errAfter)
	}
	releaseUpstream(after, nil)
}

// TestReleaseUpstreamIgnoresOtherCodes 校验其它错误不写入软退避。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，防止普通失败触发退避。
func TestReleaseUpstreamIgnoresOtherCodes(t *testing.T) {
	authID := newThrottleTestAccount(t)
	restore := swapThrottleSetting(testThrottleSetting(1, 0, time.Second))
	defer restore()

	ticket, errTicket := acquireUpstream(context.Background(), authID, upstreamKindChat)
	if errTicket != nil {
		t.Fatalf("取许可应成功，实际 %v", errTicket)
	}
	releaseUpstream(ticket, &upstreamError{Code: 710012001})

	if remaining := softBackoffRemaining(authID); remaining != 0 {
		t.Fatalf("非访问频繁错误不应触发软退避，实际剩余 %v", remaining)
	}
}

// TestAcquireUpstreamAllowsEmptyAuthID 校验账号标识为空时直接放行。
//
// 登录桥接页等场景拿不到 auth_id，此时拦下来只会让请求永远排队。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定空账号放行。
func TestAcquireUpstreamAllowsEmptyAuthID(t *testing.T) {
	ticket, errTicket := acquireUpstream(context.Background(), "   ", upstreamKindProbe)
	if errTicket != nil {
		t.Fatalf("空账号应直接放行，实际 %v", errTicket)
	}
	releaseUpstream(ticket, nil)
}

// TestAcquireUpstreamRespectsContextCancel 校验上下文取消能立刻打断排队。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定可取消性。
func TestAcquireUpstreamRespectsContextCancel(t *testing.T) {
	authID := newThrottleTestAccount(t)
	restore := swapThrottleSetting(testThrottleSetting(1, 0, time.Millisecond))
	defer restore()

	held, errHeld := acquireUpstream(context.Background(), authID, upstreamKindChat)
	if errHeld != nil {
		t.Fatalf("先占住在途应成功，实际 %v", errHeld)
	}
	defer releaseUpstream(held, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, errWait := acquireUpstream(ctx, authID, upstreamKindChat); !errors.Is(errWait, context.Canceled) {
		t.Fatalf("已取消的上下文应返回 context.Canceled，实际 %v", errWait)
	}
}

// TestConcurrentAcquireKeepsSingleInflight 用并发压测确认在途峰值恒为上限。
//
// 这是 AC-1 的机器化判据：只靠单线程用例看不出竞态，必须并发跑。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，并发压测锁定峰值。
func TestConcurrentAcquireKeepsSingleInflight(t *testing.T) {
	authID := newThrottleTestAccount(t)
	restore := swapThrottleSetting(testThrottleSetting(1, 0, time.Millisecond))
	defer restore()

	const workers = 100
	var (
		wg      sync.WaitGroup
		current atomic.Int32
		peak    atomic.Int32
	)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			ticket, errAcquire := acquireUpstream(context.Background(), authID, upstreamKindChat)
			if errAcquire != nil {
				return
			}
			n := current.Add(1)
			for {
				observed := peak.Load()
				if n <= observed || peak.CompareAndSwap(observed, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			current.Add(-1)
			releaseUpstream(ticket, nil)
		}()
	}
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Fatalf("在途峰值应恒为 1，实际 %d", got)
	}
}

// TestThrottleSettingFallbackOnInvalidValues 校验非法配置回落默认值。
//
// maxInflight 为 0 会让请求永久排队，必须挡住。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定非法值回落。
func TestThrottleSettingFallbackOnInvalidValues(t *testing.T) {
	restore := swapThrottleSetting(throttleSetting{maxInflight: 0, minInterval: -time.Second, backoff: 0})
	defer restore()

	setting := effectiveThrottleSetting()
	if setting.maxInflight != defaultAccountMaxInflight {
		t.Errorf("maxInflight 非法值应回落 %d，实际 %d", defaultAccountMaxInflight, setting.maxInflight)
	}
	if setting.minInterval != 0 {
		t.Errorf("负间隔应回落 0，实际 %v", setting.minInterval)
	}
	if setting.backoff != defaultRateLimitBackoffSeconds*time.Second {
		t.Errorf("backoff 非法值应回落 %v，实际 %v", defaultRateLimitBackoffSeconds*time.Second, setting.backoff)
	}
}
