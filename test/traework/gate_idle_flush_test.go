package main

import (
	"strings"
	"testing"
	"time"
)

// 闸门空闲放行（2026-11-11 生产实证）。
//
// 背景：pumpTraeStreamAttempt 的「持有超时」判据 heldTooLong 原本只在 output
// 事件回调里计算。上游若在首帧之后长时间不发新事件（只送心跳、注释或半个帧），
// 回调根本不会被调用，判据就永远算不到——已缓冲的首字被一直压到下一个事件到达。
//
// 生产实测（2026-11-11，minimax-m3.1-flash）：
//   gate_ms=19850 ttft_ms=10001   ← 首字早已到达，客户端多等 19.8 秒
//   gate_ms=11902 chunks=14       ← 仅 14 个分片却积压 11.9 秒
//   gate_ms=25780（全局 max）
// 该模型 gate p50=546ms 但 p99=16003ms，长尾完全由这个缺口造成。

// TestScanSSECallsOnIdlePerBatch 锁定 scanSSE 在每个读批次结束后都调用 onIdle。
func TestScanSSECallsOnIdlePerBatch(t *testing.T) {
	calls := 0
	src := "event: output\ndata: {}\n\nevent: output\ndata: {}\n\n"
	err := scanSSE(strings.NewReader(src), func(ev sseEvent) error {
		return nil
	}, func() bool { return true }, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("scanSSE 不应报错: %v", err)
	}
	if calls == 0 {
		t.Fatal("onIdle 必须被调用：否则上游空闲期间闸门永远无法放行")
	}
}

// TestScanSSEOnIdleOptional 锁定「不传 onIdle 时行为与历史一致」。
func TestScanSSEOnIdleOptional(t *testing.T) {
	n := 0
	err := scanSSE(strings.NewReader("event: output\ndata: {}\n\n"), func(ev sseEvent) error {
		n++
		return nil
	}, func() bool { return true })
	if err != nil {
		t.Fatalf("scanSSE 不应报错: %v", err)
	}
	if n == 0 {
		t.Fatal("既有调用点（不传 onIdle）必须仍能收到 data 回调")
	}
}

// TestScanSSEOnIdleStopsOnDone 锁定 onIdle 返回 errTraeStreamDone 时正常终止。
func TestScanSSEOnIdleStopsOnDone(t *testing.T) {
	err := scanSSE(strings.NewReader("event: output\ndata: {}\n\n"), func(ev sseEvent) error {
		return nil
	}, func() bool { return true }, func() error {
		return errTraeStreamDone
	})
	if err != nil {
		t.Fatalf("errTraeStreamDone 应按正常终止处理，实际 %v", err)
	}
}

// TestPseudoGateMaxHoldIsShort 锁定闸门上限不得被调大。
//
// 该常量直接决定「上游空闲时用户最多多等多久」。调大会让 2026-11-11 的
// gate_ms=19850 长尾复现。
func TestPseudoGateMaxHoldIsShort(t *testing.T) {
	if pseudoGateMaxHold > 2*time.Second {
		t.Fatalf("pseudoGateMaxHold=%v 必须 <= 2s，否则上游空闲时首字会被长压", pseudoGateMaxHold)
	}
	if pseudoGateMaxHold <= 0 {
		t.Fatalf("pseudoGateMaxHold 必须为正，实际 %v", pseudoGateMaxHold)
	}
}
