// ttft_attempt_test.go 锁定 per-attempt TTFT 计时基准。
//
// TTFT 必须是「本次账号尝试」的首字延迟。重试发生后请求级 started 与
// 本次尝试相差整段前序耗时，用它会把上一个账号的失败耗时算到当前健康
// 账号头上，慢速冷却判据吃这个样本就会误禁健康号。
package main

import (
	"testing"
	"time"
)

// TestTTFTNS_UsesAttemptBase 守住优先使用 attempt 级基准。
func TestTTFTNS_UsesAttemptBase(t *testing.T) {
	t0 := time.Now()
	c := &sseUsageCollector{}
	c.markAttemptStart(t0.Add(10 * time.Second))
	c.firstByteAt = t0.Add(12 * time.Second)

	got := c.ttftNS(t0)
	if want := uint64(2 * time.Second); got != want {
		t.Fatalf("TTFT 必须按 attempt 级基准计算：want %d, got %d（若为 12s 说明退回了请求级基准）", want, got)
	}
}

// TestTTFTNS_FallsBackToRequestStart 守住历史调用点的兼容回退。
func TestTTFTNS_FallsBackToRequestStart(t *testing.T) {
	t0 := time.Now()
	c := &sseUsageCollector{}
	c.firstByteAt = t0.Add(3 * time.Second)

	if got, want := c.ttftNS(t0), uint64(3*time.Second); got != want {
		t.Fatalf("未注入 attempt 基准时必须回退到请求起点：want %d, got %d", want, got)
	}
}

// TestTTFTNS_UnobservableReturnsZero 守住不可观测时返回 0。
func TestTTFTNS_UnobservableReturnsZero(t *testing.T) {
	t0 := time.Now()
	c := &sseUsageCollector{}
	if got := c.ttftNS(t0); got != 0 {
		t.Fatalf("首帧缺失时必须返回 0，实际 %d", got)
	}
	c.markAttemptStart(t0.Add(5 * time.Second))
	c.firstByteAt = t0
	if got := c.ttftNS(t0); got != 0 {
		t.Fatalf("负差值必须返回 0，实际 %d", got)
	}
}
