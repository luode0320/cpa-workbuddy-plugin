// account_load_test.go 锁定「按账号在跑请求数平摊并发」这条契约。
//
// 为什么必须有它：上游按账号分配并行槽位，超出额度的请求不报错而是在账号侧排队，
// 表现为首字延迟升高而错误率健康。本插件原先的选号只看「轮询轮到谁」与「会话绑定
// 在谁身上」，同一会话的并发子请求因此全压到一个号上（生产实测同账号同会话
// 99.8% 前缀缓存命中，首字仍在 5.9s~23.2s 之间波动）。
package main

import (
	"testing"
	"time"
)

// TestAccountLoadAcquireRelease 锁定配对自增自减与幂等双释放。
func TestAccountLoadAcquireRelease(t *testing.T) {
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	rel1 := accountLoadAcquire("acc-a")
	rel2 := accountLoadAcquire("acc-a")
	if got := accountLoadOf("acc-a"); got != 2 {
		t.Fatalf("在跑数 = %d, want 2", got)
	}
	rel1()
	rel1() // 幂等：重复归还不应把计数压到负数
	if got := accountLoadOf("acc-a"); got != 1 {
		t.Fatalf("重复归还后在跑数 = %d, want 1", got)
	}
	rel2()
	if got := accountLoadOf("acc-a"); got != 0 {
		t.Fatalf("全部归还后在跑数 = %d, want 0", got)
	}
}

// TestAccountLoadOf_UnknownAccount 锁定未知账号返回 0。
func TestAccountLoadOf_UnknownAccount(t *testing.T) {
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)
	if got := accountLoadOf("never-seen"); got != 0 {
		t.Fatalf("未知账号在跑数 = %d, want 0", got)
	}
}

// TestPickLeastLoaded_PrefersIdleKeepsOrder 锁定空闲优先、平级保留原顺序。
func TestPickLeastLoaded_PrefersIdleKeepsOrder(t *testing.T) {
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	rel := accountLoadAcquire("acc-busy")
	t.Cleanup(rel)

	cands := []activeAuthCandidate{{ID: "acc-busy"}, {ID: "acc-idle"}}
	if got := pickLeastLoaded(cands); got != "acc-idle" {
		t.Fatalf("必须选在跑数更少的账号，got %q", got)
	}

	// 平级：保留候选原有顺序（不把高积分的号排到后面）。
	flat := []activeAuthCandidate{{ID: "acc-1"}, {ID: "acc-2"}}
	if got := pickLeastLoaded(flat); got != "acc-1" {
		t.Fatalf("平级时必须保留候选原顺序，got %q", got)
	}
}

// TestPickLeastLoaded_SkipsUnusable 锁定停用/耗尽候选被跳过。
func TestPickLeastLoaded_SkipsUnusable(t *testing.T) {
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	cands := []activeAuthCandidate{{ID: "acc-off", Disabled: true}, {ID: "acc-drained", Exhausted: true}}
	if got := pickLeastLoaded(cands); got != "" {
		t.Fatalf("停用/耗尽候选不得被选中，got %q", got)
	}
}

// TestPickLessLoadedThan_RequiresStrictlyLess 锁定「严格更闲」语义。
func TestPickLessLoadedThan_RequiresStrictlyLess(t *testing.T) {
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	relA := accountLoadAcquire("acc-a")
	t.Cleanup(relA)
	relB := accountLoadAcquire("acc-b")
	t.Cleanup(relB)

	// acc-a 与 acc-b 同样忙（各 1 个在跑）⇒ 没有「严格更闲」的候选。
	if got := pickLessLoadedThan([]string{"acc-a", "acc-b"}, "acc-a", 1); got != "" {
		t.Fatalf("没有严格更闲的账号时必须返回空串，got %q", got)
	}

	// acc-c 完全空闲 ⇒ 应被选中。
	if got := pickLessLoadedThan([]string{"acc-a", "acc-c"}, "acc-a", 1); got != "acc-c" {
		t.Fatalf("必须选严格更闲的账号，got %q", got)
	}
}

// TestPickSessionAuth_RebindsWhenOverSoftCap 锁定粘性命中超软上限即改绑。
//
// 这是本次修复的核心行为：粘性不能变成「把同一会话的并发子请求全塞给同一个号」。
func TestPickSessionAuth_RebindsWhenOverSoftCap(t *testing.T) {
	resetSessionRouting(t)
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	cands := []activeAuthCandidate{{ID: "acc-a"}, {ID: "acc-b"}}

	// 先建立绑定：acc-a。
	first := pickSessionAuth("sess-x", cands)
	if first != "acc-a" {
		t.Fatalf("首次绑定 = %q, want acc-a", first)
	}

	// acc-a 已达软上限（2 个在跑），acc-b 空闲 ⇒ 改绑到 acc-b。
	rel1 := accountLoadAcquire("acc-a")
	t.Cleanup(rel1)
	rel2 := accountLoadAcquire("acc-a")
	t.Cleanup(rel2)

	got := pickSessionAuth("sess-x", cands)
	if got != "acc-b" {
		t.Fatalf("超软上限后必须改绑到更闲的账号，got %q", got)
	}
	// 改绑必须写回绑定表，否则下一次 pick 又会回到旧号。
	bound := sessionAuthBindings["sess-x"].AuthID
	if bound != "acc-b" {
		t.Fatalf("改绑必须写回绑定表，bound = %q, want acc-b", bound)
	}
}

// TestPickSessionAuth_KeepsBindingWhenAllBusy 锁定全池都忙时保持原绑定。
//
// 改绑没有收益时保持粘性，避免会话在两个号之间来回抖。
func TestPickSessionAuth_KeepsBindingWhenAllBusy(t *testing.T) {
	resetSessionRouting(t)
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	cands := []activeAuthCandidate{{ID: "acc-a"}, {ID: "acc-b"}}
	if first := pickSessionAuth("sess-y", cands); first != "acc-a" {
		t.Fatalf("首次绑定 = %q, want acc-a", first)
	}

	relA1 := accountLoadAcquire("acc-a")
	t.Cleanup(relA1)
	relA2 := accountLoadAcquire("acc-a")
	t.Cleanup(relA2)
	relB1 := accountLoadAcquire("acc-b")
	t.Cleanup(relB1)
	relB2 := accountLoadAcquire("acc-b")
	t.Cleanup(relB2)

	if got := pickSessionAuth("sess-y", cands); got != "acc-a" {
		t.Fatalf("全池同样忙时必须保持原绑定，got %q", got)
	}
}

// TestPickSessionAuth_StickyHitUnderSoftCap 锁定未超上限时保持粘性。
func TestPickSessionAuth_StickyHitUnderSoftCap(t *testing.T) {
	resetSessionRouting(t)
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	cands := []activeAuthCandidate{{ID: "acc-a"}, {ID: "acc-b"}}
	if first := pickSessionAuth("sess-z", cands); first != "acc-a" {
		t.Fatalf("首次绑定 = %q, want acc-a", first)
	}
	rel := accountLoadAcquire("acc-a")
	t.Cleanup(rel)

	if got := pickSessionAuth("sess-z", cands); got != "acc-a" {
		t.Fatalf("未超软上限时必须保持粘性，got %q", got)
	}
}

// TestSessionAccountInflightSoftCap 锁定软上限取值与上游默认槽位对齐。
func TestSessionAccountInflightSoftCap(t *testing.T) {
	if sessionAccountInflightSoftCap != 2 {
		t.Fatalf("软上限必须与上游默认并行槽位对齐（2），当前 %d", sessionAccountInflightSoftCap)
	}
	if sessionStickinessTTL <= 0 || sessionStickinessTTL > time.Hour {
		t.Fatalf("会话粘性 TTL 异常：%v", sessionStickinessTTL)
	}
}
