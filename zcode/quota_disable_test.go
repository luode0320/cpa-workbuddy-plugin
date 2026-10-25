package main

import (
	"testing"
	"time"
)

// quotaTestSnapshot 构造一个「额度已耗尽」的快照，供各用例复用。
// [参数] resetAt：预期重置时刻（Unix 秒）。
// [返回] 额度快照。
// 最近修改时间：2026-10-25；改动原因：新增，避免各用例重复拼装。
func quotaTestSnapshot(resetAt int64) *zcodeQuotaSnapshot {
	return &zcodeQuotaSnapshot{
		PlanName:      "ZCode Start Plan",
		PlanStatus:    "active",
		HasUsage:      true,
		TotalGrant:    3000000,
		TotalUsed:     3000000,
		TotalRemain:   0,
		EarliestReset: resetAt,
		Source:        "balance",
	}
}

// setFailoverNow 把本机制的时钟固定到给定时刻，返回恢复函数。
// [参数] t：当前测试；ts：Unix 秒。
// [返回] 恢复函数由 t.Cleanup 自动登记。
// 最近修改时间：2026-10-25；改动原因：新增，重置时刻判定需要可控时钟。
func setFailoverNow(t *testing.T, ts int64) {
	t.Helper()
	prev := failoverNowFn
	failoverNowFn = func() time.Time { return time.Unix(ts, 0).UTC() }
	t.Cleanup(func() { failoverNowFn = prev })
}

// TestQuotaExhaustedGuards 覆盖耗尽判定的三类输入。
// 核心红线：HasUsage=false（只有 current 通道）时快照的 TotalRemain 是「授予量回退」，
// 绝不能当耗尽；重置时刻未知时也不能禁用——无法验证恢复的禁用等于永久禁用。
func TestQuotaExhaustedGuards(t *testing.T) {
	cases := []struct {
		name string
		snap *zcodeQuotaSnapshot
		want bool
	}{
		{"nil 快照", nil, false},
		{"无用量通道（current 回退）", &zcodeQuotaSnapshot{HasUsage: false, TotalRemain: 0, EarliestReset: 100}, false},
		{"有用量但重置时刻未知", &zcodeQuotaSnapshot{HasUsage: true, TotalRemain: 0, EarliestReset: 0}, false},
		{"有用量且仍有剩余", &zcodeQuotaSnapshot{HasUsage: true, TotalRemain: 1, EarliestReset: 100}, false},
		{"真实耗尽", &zcodeQuotaSnapshot{HasUsage: true, TotalRemain: 0, EarliestReset: 100}, true},
	}
	for _, tc := range cases {
		if got := quotaExhausted(tc.snap); got != tc.want {
			t.Fatalf("%s: quotaExhausted = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestDisableAccountForQuotaWritesMarkers 断言额度耗尽写入 disabled 与三个额度标记，
// 并保留盘上既有顶层键。
func TestDisableAccountForQuotaWritesMarkers(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-quota01", "idx-q1", map[string]any{"custom_key": "keep-me"})
	setFailoverNow(t, 1000)

	if !disableAccountForQuota("idx-q1", "zcode-quota01", 2000) {
		t.Fatalf("expected quota disable to succeed")
	}
	doc := h.docOf("idx-q1")
	if disabled, _ := doc["disabled"].(bool); !disabled {
		t.Fatalf("disabled 未写入: %+v", doc)
	}
	if marked, _ := doc[quotaDisableKey].(bool); !marked {
		t.Fatalf("quota_disable 标记缺失: %+v", doc)
	}
	if got, _ := doc[quotaDisableResetKey].(int64); got != 2000 {
		t.Fatalf("quota_disable_reset = %v, want 2000", doc[quotaDisableResetKey])
	}
	if at, _ := doc[quotaDisableAtKey].(string); at == "" {
		t.Fatalf("quota_disable_at 缺失: %+v", doc)
	}
	if doc["custom_key"] != "keep-me" {
		t.Fatalf("既有顶层键被破坏: %+v", doc)
	}
}

// TestDisableAccountForQuotaSkipsOthersClaim 断言盘上已被别的机制禁用时不接管。
// 抢别人的号会让原机制失去恢复职责，是最需要防的回归。
func TestDisableAccountForQuotaSkipsOthersClaim(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-quota02", "idx-q2", map[string]any{"disabled": true, failoverDisableKey: true})
	setFailoverNow(t, 1000)

	if disableAccountForQuota("idx-q2", "zcode-quota02", 2000) {
		t.Fatalf("失败禁用的号不应被额度机制认领")
	}
	doc := h.docOf("idx-q2")
	if marked, _ := doc[quotaDisableKey].(bool); marked {
		t.Fatalf("不应写入额度标记: %+v", doc)
	}
}

// TestDisableAccountForQuotaIsIdempotent 断言同一账号重复耗尽只写一次盘。
// 每写一次都触发宿主 watcher 重载，重复写会放大上游压力。
func TestDisableAccountForQuotaIsIdempotent(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-quota03", "idx-q3", nil)
	setFailoverNow(t, 1000)

	disableAccountForQuota("idx-q3", "zcode-quota03", 2000)
	first := h.writes
	disableAccountForQuota("idx-q3", "zcode-quota03", 2000)
	if h.writes != first {
		t.Fatalf("重复禁用又写盘了：writes %d → %d", first, h.writes)
	}
}

// TestReenableForQuotaResetRequiresAllThree 覆盖恢复三条件的组合。
// 三个条件：盘上有额度标记、已越过重置时刻、探活成功。任一缺失都必须保持禁用。
func TestReenableForQuotaResetRequiresAllThree(t *testing.T) {
	cases := []struct {
		name     string
		setup    bool  // 是否先写额度禁用
		now      int64 // 当前时刻
		resetAt  int64 // 预期重置时刻
		pingOK   bool
		wantBack bool
	}{
		{"未禁用时不动", false, 3000, 2000, true, false},
		{"未到重置时刻", true, 1500, 2000, true, false},
		{"到点但探活失败", true, 3000, 2000, false, false},
		{"到点且探活成功", true, 3000, 2000, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newFailoverFakeHost(t)
			h.addAccount("zcode-quota04", "idx-q4", nil)
			if tc.setup {
				setFailoverNow(t, 1000)
				disableAccountForQuota("idx-q4", "zcode-quota04", tc.resetAt)
			}
			setFailoverNow(t, tc.now)

			prevPing := failoverPingFn
			failoverPingFn = func(_ *StoredAuth, _ string) error {
				if tc.pingOK {
					return nil
				}
				return errFakePingFailed
			}
			t.Cleanup(func() { failoverPingFn = prevPing })

			got := reenableForQuotaReset("idx-q4", "zcode-quota04", &StoredAuth{AuthID: "idx-q4", APIKey: "sk-test"})
			if got != tc.wantBack {
				t.Fatalf("reenable = %v, want %v", got, tc.wantBack)
			}
			doc := h.docOf("idx-q4")
			disabled, _ := doc["disabled"].(bool)
			if tc.wantBack && disabled {
				t.Fatalf("应恢复但盘上仍 disabled: %+v", doc)
			}
			if !tc.wantBack && tc.setup && !disabled {
				t.Fatalf("不应恢复但盘上已 enabled: %+v", doc)
			}
			if marked, _ := doc[quotaDisableKey].(bool); tc.wantBack && marked {
				t.Fatalf("恢复后额度标记应清除: %+v", doc)
			}
		})
	}
}

// errFakePingFailed 是假探活的失败信号。
var errFakePingFailed = &fakePingError{}

type fakePingError struct{}

func (e *fakePingError) Error() string { return "fake ping failed" }

// TestReenableForQuotaResetKeepsFailoverDisabled 断言额度恢复不替失败禁用解禁。
// 两个标记同时存在时，`disabled` 必须保持 true，交给失败探活决定。
func TestReenableForQuotaResetKeepsFailoverDisabled(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-quota05", "idx-q5", map[string]any{
		"disabled":           true,
		failoverDisableKey:   true,
		failoverDisableAtKey: "2026-10-25T00:00:00Z",
		quotaDisableKey:      true,
		quotaDisableAtKey:    "2026-10-25T00:00:00Z",
		quotaDisableResetKey: int64(2000),
	})
	setFailoverNow(t, 3000)
	prevPing := failoverPingFn
	failoverPingFn = func(_ *StoredAuth, _ string) error { return nil }
	t.Cleanup(func() { failoverPingFn = prevPing })

	if !reenableForQuotaReset("idx-q5", "zcode-quota05", &StoredAuth{AuthID: "idx-q5", APIKey: "sk-test"}) {
		t.Fatalf("额度标记应被清除")
	}
	doc := h.docOf("idx-q5")
	if disabled, _ := doc["disabled"].(bool); !disabled {
		t.Fatalf("失败禁用仍在，disabled 必须保持 true: %+v", doc)
	}
	if marked, _ := doc[failoverDisableKey].(bool); !marked {
		t.Fatalf("失败禁用标记不得被额度恢复清除: %+v", doc)
	}
}

// TestQuotaDisableSummaryRequiresBoth 断言摘要只对「带标记且盘上 disabled」成立。
// 只有标记没有 disabled（例如别人已恢复）时不能报告为额度禁用。
func TestQuotaDisableSummaryRequiresBoth(t *testing.T) {
	cases := []struct {
		name    string
		doc     map[string]any
		want    bool
		wantRes int64
	}{
		{"标记齐全", map[string]any{"disabled": true, quotaDisableKey: true, quotaDisableResetKey: float64(123)}, true, 123},
		{"有标记但未禁用", map[string]any{"disabled": false, quotaDisableKey: true, quotaDisableResetKey: int64(123)}, false, 0},
		{"已禁用但无本机制标记", map[string]any{"disabled": true}, false, 0},
		{"nil 文档", nil, false, 0},
	}
	for _, tc := range cases {
		got, gotRes := quotaDisableSummary(tc.doc)
		if got != tc.want || gotRes != tc.wantRes {
			t.Fatalf("%s: got (%v, %d), want (%v, %d)", tc.name, got, gotRes, tc.want, tc.wantRes)
		}
	}
}

// TestQuotaResetText 断言重置时刻按北京时间渲染，非法时刻返回空串。
// 上游 period_end 是当日 23:59:59 CST，展示口径必须与之一致，否则用户会以为当晚还能用。
func TestQuotaResetText(t *testing.T) {
	// 1791734399 = 2026-10-11 23:59:59 CST
	if got := quotaResetText(1791734399); got != "10-11 23:59（北京时间）" {
		t.Fatalf("quotaResetText = %q", got)
	}
	if got := quotaResetText(0); got != "" {
		t.Fatalf("零时刻应返回空串，got %q", got)
	}
}

// TestGradeQuotaForAuthDisablesWhenExhausted 断言分级入口在耗尽时写盘禁用。
func TestGradeQuotaForAuthDisablesWhenExhausted(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-quota06", "idx-q6", nil)
	setFailoverNow(t, 1000)

	gradeQuotaForAuth("idx-q6", "zcode-quota06", &StoredAuth{AuthID: "idx-q6", APIKey: "sk-test"}, quotaTestSnapshot(2000))

	doc := h.docOf("idx-q6")
	if disabled, _ := doc["disabled"].(bool); !disabled {
		t.Fatalf("耗尽账号应被写盘禁用: %+v", doc)
	}
	if marked, _ := doc[quotaDisableKey].(bool); !marked {
		t.Fatalf("额度标记缺失: %+v", doc)
	}
}

// TestGradeQuotaForAuthIgnoresNoUsage 断言只有 current 通道（无真实用量）时不动手。
// 此时 TotalRemain 是「授予量回退」，若据此判耗尽会把全部账号误禁。
func TestGradeQuotaForAuthIgnoresNoUsage(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-quota07", "idx-q7", nil)
	setFailoverNow(t, 1000)

	snap := &zcodeQuotaSnapshot{HasUsage: false, TotalGrant: 0, TotalRemain: 0, EarliestReset: 2000, Source: "current"}
	gradeQuotaForAuth("idx-q7", "zcode-quota07", &StoredAuth{AuthID: "idx-q7", APIKey: "sk-test"}, snap)

	doc := h.docOf("idx-q7")
	if disabled, _ := doc["disabled"].(bool); disabled {
		t.Fatalf("无用量通道不得触发禁用: %+v", doc)
	}
}
