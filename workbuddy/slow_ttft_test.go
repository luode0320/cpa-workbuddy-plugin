// slow_ttft_test.go 锁定首字延迟的计时口径。
//
// 关键语义：TTFT 必须只反映「当前这次尝试」的耗时。请求级 started 是换号链
// 的起点，用它算会把上一个账号的失败耗时累加到最终承接的账号头上——
// 慢速判据一旦吃到被污染的样本，就会把健康账号判成慢号。
package main

import (
	"testing"
	"time"
)

func TestTTFT_AttemptStartOverridesRequestStart(t *testing.T) {
	// AC-008：换号后 TTFT 只统计当前 attempt 的耗时，不叠加前一次失败耗时。
	requestStarted := time.Now()
	// 模拟：attempt 0 失败耗时 10s，attempt 1 在 10s 后发出，首帧在其后 2s。
	attempt1Started := requestStarted.Add(10 * time.Second)
	firstByte := requestStarted.Add(12 * time.Second)

	c := &sseUsageCollector{}
	c.markAttemptStart(attempt1Started)
	c.firstByteAt = firstByte

	got := c.ttftNS(requestStarted)
	want := uint64(2 * time.Second)
	if got != want {
		t.Fatalf("TTFT 必须只算当前 attempt：期望 %d ns，实际 %d ns（说明叠加了前一账号的失败耗时）", want, got)
	}
}

func TestTTFT_FallsBackToRequestStartWhenUnmarked(t *testing.T) {
	// 未标记 attempt 时回退到请求起点，保持非重试场景的历史行为。
	started := time.Now()
	firstByte := started.Add(1500 * time.Millisecond)

	c := &sseUsageCollector{firstByteAt: firstByte}
	got := c.ttftNS(started)
	want := uint64(1500 * time.Millisecond)
	if got != want {
		t.Fatalf("未标记 attempt 时应回退请求起点：期望 %d ns，实际 %d ns", want, got)
	}
}

func TestTTFT_MarkAttemptStartResetsFirstByte(t *testing.T) {
	// 重置基准必须同时清掉上一次的首帧时刻，否则新一轮尝试会沿用旧首帧。
	base := time.Now()
	c := &sseUsageCollector{}
	c.markAttemptStart(base)
	c.firstByteAt = base.Add(time.Second)

	c.markAttemptStart(base.Add(5 * time.Second))
	if !c.firstByteAt.IsZero() {
		t.Fatal("markAttemptStart 必须清空 firstByteAt")
	}
	if got := c.ttftNS(base); got != 0 {
		t.Fatalf("首帧未到达时应返回 0，实际 %d", got)
	}
}

func TestTTFT_ZeroWhenNoFirstByte(t *testing.T) {
	// 无首帧（零字节断流 / 连接失败）时 TTFT 必须为 0，不得拿请求耗时冒充。
	c := &sseUsageCollector{}
	if got := c.ttftNS(time.Now()); got != 0 {
		t.Fatalf("无首帧时 TTFT 必须为 0，实际 %d", got)
	}
}

func TestTTFTSince_HandlesUnsetAndNegative(t *testing.T) {
	// ttftSince 是非流式路径的口径入口：未设置或时钟倒退都必须返回 0。
	now := time.Now()
	if got := ttftSince(time.Time{}, now); got != 0 {
		t.Fatalf("started 为零值时必须返回 0，实际 %d", got)
	}
	if got := ttftSince(now, time.Time{}); got != 0 {
		t.Fatalf("firstByteAt 为零值时必须返回 0，实际 %d", got)
	}
	if got := ttftSince(now, now.Add(-time.Second)); got != 0 {
		t.Fatalf("首帧早于起点（时钟异常）时必须返回 0，实际 %d", got)
	}
	if got := ttftSince(now, now.Add(800*time.Millisecond)); got != uint64(800*time.Millisecond) {
		t.Fatalf("正常场景应返回真实差值，实际 %d", got)
	}
}
