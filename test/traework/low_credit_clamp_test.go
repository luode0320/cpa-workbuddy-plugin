package main

import (
	"testing"
)

// 禁用线与保活线的夹取语义（2026-11-11）。
//
// 背景：0.5.30 把 low_credit_disable_below 默认值从 10 改成 100，而保活线默认
// 仍是 20。creditTierFor 先判禁用线，禁用线高于保活线时保活档在数学上不可达，
// 整个池子被压到只剩「余额 >= 100」的少数账号，并发堆到它们身上，首字延迟尾
// 部同步抬升（0.5.30 实测池子 21 -> 7、只有 2 个号在扛全部流量）。0.5.42 把
// 默认值回滚成 10，但配置项本身仍接受任意整数，运维填 100 会再次触发同一故障。
//
// 修法：生效值不得超过保活线；取消禁用线（<=0）不受约束。

// TestEffectiveLowCreditDisableBelow_ClampsToPreserve 锁定夹取方向与 kill switch。
func TestEffectiveLowCreditDisableBelow_ClampsToPreserve(t *testing.T) {
	cases := []struct {
		name     string
		below    int64
		preserve int64
		want     int64
	}{
		{"禁用线远高于保活线必须夹取", 100, 20, 20},
		{"禁用线略高于保活线必须夹取", 21, 20, 20},
		{"禁用线低于保活线按原值", 10, 20, 10},
		{"禁用线等于保活线按原值", 20, 20, 20},
		{"禁用线关闭不受夹取影响", 0, 20, 0},
		{"禁用线负值按关闭处理", -5, 20, 0},
		{"保活线关闭时不夹取", 100, 0, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restorePreserve := setMinRoutableCredits(tc.preserve)
			defer restorePreserve()
			restoreBelow := setLowCreditDisableBelow(tc.below)
			defer restoreBelow()

			if got := effectiveLowCreditDisableBelow(); got != tc.want {
				t.Fatalf("effectiveLowCreditDisableBelow() = %d, want %d (below=%d preserve=%d)",
					got, tc.want, tc.below, tc.preserve)
			}
		})
	}
}

// TestGradeCredits_StaleDiskBelowIsCleared 锁定跨代阈值错位的自愈。
//
// 场景：旧世代实例按自己那一代的编译期常量（0.5.41 = 100）写盘禁用了一批号，
// 盘上留下 disabled:true + low_credit_disable_below:100；当前生效线已经降到 10。
// 这类号在盘上只有本机制的三键（manual_disable 只由面板 toggle 写入），所以
// ours 成立，必须在下一轮分级时按当前生效线解禁——否则禁用线一回滚，历史被
// 宽松阈值禁掉的号就永远不会自己回来。
func TestGradeCredits_StaleDiskBelowIsCleared(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("tier-stale", "idx-stale", map[string]any{
		"disabled":            true,
		creditDisableKey:      true,
		creditDisableBelowKey: int64(100),
		creditDisableAtKey:    "2026-11-11T01:00:00Z",
	})

	// 当前生效线是 10（默认值），盘上标记却是旧世代的 100。
	restoreBelow := setLowCreditDisableBelow(defaultLowCreditDisableBelow)
	defer restoreBelow()

	tier := gradeCreditsForAuth("idx-stale", "tier-stale", nil, &traeCredits{TotalRemain: 50})

	if tier != creditTierNormal {
		t.Fatalf("余额 50 高于生效线 10，档位应为 normal，got %s", tier)
	}
	doc := h.docOf("idx-stale")
	if doc["disabled"] != false {
		t.Fatalf("被旧阈值禁掉的号在生效线回落后必须解禁，doc=%v", doc)
	}
	for _, k := range []string{creditDisableKey, creditDisableBelowKey, creditDisableAtKey} {
		if _, ok := doc[k]; ok {
			t.Fatalf("解禁后 %s 必须清除，doc=%v", k, doc)
		}
	}
}
