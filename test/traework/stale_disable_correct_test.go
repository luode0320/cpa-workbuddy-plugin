package main

import (
	"testing"
)

// 旧世代禁用纠正（2026-11-11 生产实证）。
//
// 背景：宿主热重载不调用 plugin_shutdown，进程内同时 mmap 着 9 代 .so。
// v0.5.41/42/44 的 watchdog 没有 retireIfSuperseded 闸门（strings 实测这三代
// 没有 "watchdog loop" 文案），每 10 分钟用自己编译期的禁用线 100 重写
// 「disabled:true + low_credit_disable_below:100」，把 remain=12~92 的健康号
// 逐个禁掉，可用池被反复削到 2/51。
//
// 本文件锁定「按阈值指纹纠正」的四条边界：该放的放、不该放的一律不放。

// TestStaleDisableBelowRatio 锁定阈值指纹的判定方向。
func TestStaleDisableBelowRatio(t *testing.T) {
	cases := []struct {
		name    string
		stored  int64
		current int64
		want    bool
	}{
		{"旧代 100 高于当前 20 必须识别", 100, 20, true},
		{"旧代 100 高于当前 10 必须识别", 100, 10, true},
		{"与本代相同不算旧代", 20, 20, false},
		{"低于当前线不算旧代", 10, 20, false},
		{"当前线关闭时不判旧代", 100, 0, false},
		{"盘上无阈值不判旧代", 0, 20, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := staleDisableBelowRatio(tc.stored, tc.current); got != tc.want {
				t.Fatalf("staleDisableBelowRatio(%d, %d) = %v, want %v",
					tc.stored, tc.current, got, tc.want)
			}
		})
	}
}

// TestCorrectStaleDisableReleasesOldGeneration 锁定「旧代按 100 禁的号必须被释放」。
func TestCorrectStaleDisableReleasesOldGeneration(t *testing.T) {
	resetFailover(t)
	restoreBelow := setLowCreditDisableBelow(10)
	defer restoreBelow()
	restoreMin := setMinRoutableCredits(20)
	defer restoreMin()

	const authID = "acc-stale"
	state := fakeHostAuth(t, "idx-stale", authID, map[string]any{
		"disabled":            true,
		creditDisableKey:      true,
		creditDisableBelowKey: float64(100), // 旧代写的阈值
		creditDisableAtKey:    "2026-11-11T04:46:19Z",
		"note":                "积分不足停用 · 余37 < 禁用线100（积分回升后自动启用）",
		"uid":                 authID,
	})
	// 当前余额 37，远高于生效线（10 被 clamp 到 20），不该被禁。
	cacheCredits(authID, &traeCredits{TotalRemain: 37})

	if !correctStaleDisable("idx-stale", authID) {
		t.Fatalf("旧代按禁用线 100 禁的号必须被纠正；state=%v", *state)
	}
	if disabled, _ := (*state)["disabled"].(bool); disabled {
		t.Fatalf("纠正后必须 disabled=false，实际 %v", *state)
	}
}

// TestCorrectStaleDisableKeepsOwnDisable 反向守卫：
// 本代自己按当前阈值禁的号不得被纠正循环误放。
func TestCorrectStaleDisableKeepsOwnDisable(t *testing.T) {
	resetFailover(t)
	restoreBelow := setLowCreditDisableBelow(10)
	defer restoreBelow()
	restoreMin := setMinRoutableCredits(20)
	defer restoreMin()

	const authID = "acc-own"
	state := fakeHostAuth(t, "idx-own", authID, map[string]any{
		"disabled":            true,
		creditDisableKey:      true,
		creditDisableBelowKey: float64(10), // 与本代生效线 min(10,20)=10 一致
		"uid":                 authID,
	})
	// 余额 5 低于生效线 10，本来就该被禁。
	cacheCredits(authID, &traeCredits{TotalRemain: 5})

	if correctStaleDisable("idx-own", authID) {
		t.Fatalf("本代按当前阈值禁的号不得被纠正；state=%v", *state)
	}
	if disabled, _ := (*state)["disabled"].(bool); !disabled {
		t.Fatal("必须保持禁用")
	}
}

// TestCorrectStaleDisableKeepsOwnDisableWithHighRemain 是阈值判据的精确哨兵。
//
// 上一个用例的余额 5 低于生效线，去掉阈值判据后仍会被「余额不足」挡住，
// 无法证明阈值判据真的在工作。本用例让余额充足（80 > 20），此时**唯一**
// 阻止释放的理由就是「盘上阈值 20 等于本代生效线 20」——阈值判据一旦被
// 放宽或删掉，这个号就会被错误释放。
func TestCorrectStaleDisableKeepsOwnDisableWithHighRemain(t *testing.T) {
	resetFailover(t)
	restoreBelow := setLowCreditDisableBelow(10)
	defer restoreBelow()
	restoreMin := setMinRoutableCredits(20)
	defer restoreMin()

	const authID = "acc-own-high"
	state := fakeHostAuth(t, "idx-own-high", authID, map[string]any{
		"disabled":       true,
		creditDisableKey: true,
		// 本代生效线是 effectiveLowCreditDisableBelow() = min(below=10, preserve=20) = 10。
		// 盘上写 10 与之一致，说明不是旧代写的。
		creditDisableBelowKey: float64(10),
		"uid":                 authID,
	})
	cacheCredits(authID, &traeCredits{TotalRemain: 80})

	if correctStaleDisable("idx-own-high", authID) {
		t.Fatalf("阈值与本代一致时不得释放（即使余额充足）；state=%v", *state)
	}
	if disabled, _ := (*state)["disabled"].(bool); !disabled {
		t.Fatal("必须保持禁用")
	}
}

// TestCorrectStaleDisableFallsBackToSidecar 锁定积分读不到内存缓存时的 sidecar 回落。
//
// 为什么必须有这条回落：cache.go 的 accountCache 是每个 .so 实例独立的内存副本，
// 旧代写入的禁用标记在新代的缓存里可能没有对应条目。只读内存会让纠正循环对
// 「刚被旧代禁掉、新代还没刷新过」的账号直接放弃——而那恰恰是最需要纠正的时刻。
// sidecar（traework-credits.json 的内存镜像）是跨实例共享的来源。
func TestCorrectStaleDisableFallsBackToSidecar(t *testing.T) {
	resetFailover(t)
	restoreBelow := setLowCreditDisableBelow(10)
	defer restoreBelow()
	restoreMin := setMinRoutableCredits(20)
	defer restoreMin()

	const authID = "acc-sidecar"
	state := fakeHostAuth(t, "idx-sidecar", authID, map[string]any{
		"disabled":            true,
		creditDisableKey:      true,
		creditDisableBelowKey: float64(100), // 旧代写的阈值
		"uid":                 authID,
	})
	// 只写 sidecar 快照，模拟「新代进程内 accountCache 尚无该账号」的形态。
	recordCreditSnapshot(authID, &traeCredits{TotalRemain: 55})
	if _, ok := creditSnapshotGet(authID); !ok {
		t.Fatal("前置条件不成立：sidecar 必须有快照")
	}

	if !correctStaleDisable("idx-sidecar", authID) {
		t.Fatalf("必须通过 sidecar 回落读到余额并释放；state=%v", *state)
	}
	if disabled, _ := (*state)["disabled"].(bool); disabled {
		t.Fatal("纠正后必须 disabled=false")
	}
}

// TestCorrectStaleDisableRespectsOtherOwners 反向守卫：
// manual / failover / exhausted 三类归属一律不能被纠正循环抢走。
func TestCorrectStaleDisableRespectsOtherOwners(t *testing.T) {
	cases := []struct {
		name string
		doc  map[string]any
	}{
		{"手动停用", map[string]any{"disabled": true, creditDisableKey: true, creditDisableBelowKey: float64(100), "manual_disable": true}},
		{"故障停用", map[string]any{"disabled": true, creditDisableKey: true, creditDisableBelowKey: float64(100), "failover_disable": true}},
		{"额度耗尽", map[string]any{"disabled": true, creditDisableKey: true, creditDisableBelowKey: float64(100), "exhausted_disable": true}},
		{"无本机制标记", map[string]any{"disabled": true, creditDisableBelowKey: float64(100)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFailover(t)
			restoreBelow := setLowCreditDisableBelow(10)
			defer restoreBelow()
			restoreMin := setMinRoutableCredits(20)
			defer restoreMin()

			authID := "acc-" + tc.name
			state := fakeHostAuth(t, "idx-"+authID, authID, tc.doc)
			cacheCredits(authID, &traeCredits{TotalRemain: 80})

			if correctStaleDisable("idx-"+authID, authID) {
				t.Fatalf("%s：不得被纠正循环释放；state=%v", tc.name, *state)
			}
			if disabled, _ := (*state)["disabled"].(bool); !disabled {
				t.Fatalf("%s：必须保持 disabled=true", tc.name)
			}
		})
	}
}
