// ttft_attempt_test.go 锁定 per-attempt TTFT 计时基准。
//
// 关键语义：TTFT 是「本次账号尝试」的首字延迟，不是「整个请求」的。
// 重试发生后两者相差整段前序失败耗时——用请求级起点会把上一个账号的
// 失败耗时算到当前健康账号头上，慢速冷却判据直接吃这个样本就会误禁健康号。
package main

import (
	"testing"
	"time"
)

// TestAttemptTTFTNS_UsesAttemptBase 守住优先使用 attempt 级基准。
//
// 场景：请求起点 T0，账号 A 失败耗时 10s，换到账号 B 后 T0+10s 发出，
// B 首帧在 T0+12s 到达。正确 TTFT 是 2s，而不是 12s。
func TestAttemptTTFTNS_UsesAttemptBase(t *testing.T) {
	t0 := time.Now()
	attemptStarted := t0.Add(10 * time.Second)
	firstOutputAt := t0.Add(12 * time.Second)

	got := attemptTTFTNS(attemptStarted, t0, firstOutputAt)
	want := uint64(2 * time.Second)
	if got != want {
		t.Fatalf("TTFT 必须按 attempt 级基准计算：want %d ns, got %d ns（若为 12s 说明退回了请求级基准）", want, got)
	}
}

// TestAttemptTTFTNS_FallsBackToRequestStart 守住历史调用点的兼容回退。
//
// AttemptStarted 未注入时（如单次尝试的老路径）必须回退到请求起点，
// 不能返回 0——否则该路径的 TTFT 会静默消失。
func TestAttemptTTFTNS_FallsBackToRequestStart(t *testing.T) {
	t0 := time.Now()
	firstOutputAt := t0.Add(3 * time.Second)

	got := attemptTTFTNS(time.Time{}, t0, firstOutputAt)
	want := uint64(3 * time.Second)
	if got != want {
		t.Fatalf("AttemptStarted 为空时必须回退到请求起点：want %d ns, got %d ns", want, got)
	}
}

// TestAttemptTTFTNS_UnobservableReturnsZero 守住不可观测时返回 0。
//
// 未收到任何输出（开启即失败）不应产生样本，返回 0 让上层跳过打点。
func TestAttemptTTFTNS_UnobservableReturnsZero(t *testing.T) {
	t0 := time.Now()
	if got := attemptTTFTNS(t0, t0, time.Time{}); got != 0 {
		t.Fatalf("首帧时间缺失时必须返回 0，实际 %d", got)
	}
	// 时钟抖动导致负差值同样返回 0，不能出现巨大无符号数。
	if got := attemptTTFTNS(t0.Add(5*time.Second), t0, t0); got != 0 {
		t.Fatalf("负差值必须返回 0，实际 %d", got)
	}
}

// TestAttemptTTFTNS_IsolatesPreviousAttemptCost 是多账号重试场景的整体回归。
//
// 三次尝试：A 耗时 10s 失败、B 耗时 8s 失败、C 首帧 2s 成功。
// 每次的 TTFT 都必须只反映各自耗时，C 的样本绝不能被累加成 20s。
func TestAttemptTTFTNS_IsolatesPreviousAttemptCost(t *testing.T) {
	t0 := time.Now()
	cases := []struct {
		name           string
		attemptStarted time.Time
		firstOutput    time.Time
		want           time.Duration
	}{
		{"attempt-A", t0, t0.Add(10 * time.Second), 10 * time.Second},
		{"attempt-B", t0.Add(10 * time.Second), t0.Add(18 * time.Second), 8 * time.Second},
		{"attempt-C", t0.Add(18 * time.Second), t0.Add(20 * time.Second), 2 * time.Second},
	}
	for _, c := range cases {
		got := attemptTTFTNS(c.attemptStarted, t0, c.firstOutput)
		if got != uint64(c.want) {
			t.Fatalf("%s: want %v, got %v（累加了前序耗时）", c.name, c.want, time.Duration(got))
		}
	}
}
