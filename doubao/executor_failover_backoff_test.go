package main

import (
	"testing"
	"time"
)

// TestRetryBackoffLadder 校验换号退避按尝试次数翻倍且带抖动。
//
// 不设退避是触发 710022002 的直接放大器：一次失败瞬间变成三次紧挨着的上游
// 请求，上游只看到「同一账号短时间内被反复调用」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定退避阶梯。
func TestRetryBackoffLadder(t *testing.T) {
	cases := []struct {
		attempt int
		base    time.Duration
	}{
		{0, retryBackoffBase},
		{1, retryBackoffBase * 2},
		{2, retryBackoffBase * 4},
	}
	for _, c := range cases {
		got := retryBackoff(c.attempt)
		low := c.base - c.base/10
		high := c.base + c.base/10
		if got < low || got > high {
			t.Errorf("attempt=%d 的退避应落在 %v~%v，实际 %v", c.attempt, low, high, got)
		}
	}
}

// TestRetryBackoffIsJittered 校验同样入参能拿到不同结果。
//
// 没有抖动时多个并发请求会在同一毫秒一起回到上游，自己造出尖峰。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定抖动存在。
func TestRetryBackoffIsJittered(t *testing.T) {
	seen := make(map[time.Duration]struct{}, 8)
	for i := 0; i < 16; i++ {
		seen[retryBackoff(2)] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("退避应带抖动，16 次采样只得到 %d 种取值", len(seen))
	}
}

// TestRetrySleepReturnsImmediatelyForZero 校验零时长不产生等待。
//
// attempt=0 之外的路径可能算出 0，若真去 Sleep(0) 只是空转；更重要的是
// 不能因为 d<=0 就 panic 或死等。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定零值不阻塞。
func TestRetrySleepReturnsImmediatelyForZero(t *testing.T) {
	started := time.Now()
	retrySleep(0)
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("零时长应立即返回，实际耗时 %v", elapsed)
	}
}
