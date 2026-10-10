// checkin_schedule_test.go 守护自动签到调度器的槽位计算：必须锚定到下一个
// 签到槽位的绝对时间，而不能依赖 ticker 相位 + "当前分钟==0" 的脆弱匹配
// （该缺陷导致生产自动签到从未触发，2026-10-10）。
package main

import (
	"testing"
	"time"
)

// TestNextAutoCheckinTime_AlignsToNextSlot 验证槽位计算返回严格晚于 now 的
// 最近一个整点槽位，且跨日顺延正确。
func TestNextAutoCheckinTime_AlignsToNextSlot(t *testing.T) {
	loc := time.Local
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		// 00:00:00 之后第一秒 → 下一个槽位 04:00（0 点已过）
		{time.Date(2026, 10, 10, 0, 0, 1, 0, loc), time.Date(2026, 10, 10, 4, 0, 0, 0, loc)},
		// 03:59:59 → 04:00
		{time.Date(2026, 10, 10, 3, 59, 59, 0, loc), time.Date(2026, 10, 10, 4, 0, 0, 0, loc)},
		// 13:30 → 16:00
		{time.Date(2026, 10, 10, 13, 30, 0, 0, loc), time.Date(2026, 10, 10, 16, 0, 0, 0, loc)},
		// 20:00:00 之后 → 次日 00:00
		{time.Date(2026, 10, 10, 20, 0, 1, 0, loc), time.Date(2026, 10, 11, 0, 0, 0, 0, loc)},
		// 23:59:59 → 次日 00:00
		{time.Date(2026, 10, 10, 23, 59, 59, 0, loc), time.Date(2026, 10, 11, 0, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		got := nextAutoCheckinTime(c.now)
		if !got.Equal(c.want) {
			t.Errorf("nextAutoCheckinTime(%v) = %v, want %v", c.now, got, c.want)
		}
		if !got.After(c.now) {
			t.Errorf("nextAutoCheckinTime(%v) = %v, must be strictly after now", c.now, got)
		}
	}
}

// TestNextAutoCheckinTime_HitsEverySlot 验证每个配置槽位都能被精确命中，
// 即调度覆盖全部 autoCheckinTimes（旧 ticker 相位错配下会漏掉几乎所有槽位）。
func TestNextAutoCheckinTime_HitsEverySlot(t *testing.T) {
	loc := time.Local
	for i, h := range autoCheckinTimes {
		// 上一槽位结束时刻（更早的整点+1秒）→ 应命中本槽位 h:00
		prevHour := h - 4
		baseDay := 10
		if i == 0 {
			prevHour = 20 // 00:00 槽位的前一个是前一天 20:00
			baseDay = 9
		}
		before := time.Date(2026, 10, baseDay, prevHour, 0, 1, 0, loc)
		want := time.Date(2026, 10, 10, h, 0, 0, 0, loc)
		got := nextAutoCheckinTime(before)
		if !got.Equal(want) {
			t.Errorf("slot hour=%d: nextAutoCheckinTime(%v) = %v, want %v", h, before, got, want)
		}
	}
}

// TestNextAutoCheckinTime_NeverEqualsNowAtSlot 验证槽位整点本身不会返回自身
// （避免同一槽位重复触发的边界）。
func TestNextAutoCheckinTime_NeverEqualsNowAtSlot(t *testing.T) {
	loc := time.Local
	at := time.Date(2026, 10, 10, 4, 0, 0, 0, loc)
	got := nextAutoCheckinTime(at)
	if got.Equal(at) {
		t.Errorf("槽位整点应返回下一个槽位而非自身，got=%v", got)
	}
	want := time.Date(2026, 10, 10, 8, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("nextAutoCheckinTime(%v) = %v, want %v", at, got, want)
	}
}
