// 本文件覆盖 2026-11-10 生产取证后落地的三处调度修复：
//   1. pickActiveAuth 不再让面板粘性盖过按负载平摊；
//   2. extractSessionKeyFromSources 剔除低熵会话标识；
//   3. pickLeastLoaded 的平级语义（保留候选原有优先级）。
package main

import (
	"testing"
)

// TestPickActiveAuth_SpreadsToLessLoadedAccount 锁定面板选择只作平级优先。
//
// 原实现在面板号可用时直接 return，下面的按负载平摊永远走不到：只要面板指着
// 某个号，并发就一直堆在它身上。上游 Trae 每号并行槽位默认 2，堆积部分在账号侧
// 排队，实测把首字延迟从 4596ms 推到 7795ms。
func TestPickActiveAuth_SpreadsToLessLoadedAccount(t *testing.T) {
	resetActiveAuth(t)
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	const busy, idle = "acc-busy", "acc-idle"
	setActiveAuthID(busy)

	// 面板号背 5 个在跑请求，另一个号空闲。
	for i := 0; i < 5; i++ {
		accountLoadAcquire(busy)
	}

	got := pickActiveAuth([]activeAuthCandidate{{ID: busy}, {ID: idle}})
	if got != idle {
		t.Fatalf("面板号已背 5 个在跑请求时必须让位给空闲号，got %q", got)
	}
}

// TestPickActiveAuth_KeepsPanelChoiceWhenIdle 锁定面板选择仍然生效。
//
// 平摊只是「负载更低时让位」，不是放弃面板选择：两边一样闲时必须选面板号。
func TestPickActiveAuth_KeepsPanelChoiceWhenIdle(t *testing.T) {
	resetActiveAuth(t)
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	const panel, other = "acc-panel", "acc-other"
	setActiveAuthID(panel)

	got := pickActiveAuth([]activeAuthCandidate{{ID: other}, {ID: panel}})
	if got != panel {
		t.Fatalf("两边同等空闲时必须保留面板选择，got %q want %q", got, panel)
	}
}

// TestPickActiveAuth_SkipsUnhealthyCandidates 锁定候选健康度过滤。
func TestPickActiveAuth_SkipsUnhealthyCandidates(t *testing.T) {
	resetActiveAuth(t)
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)
	setActiveAuthID("")

	got := pickActiveAuth([]activeAuthCandidate{
		{ID: "acc-disabled", Disabled: true},
		{ID: "acc-exhausted", Exhausted: true},
		{ID: "acc-ok"},
	})
	if got != "acc-ok" {
		t.Fatalf("必须跳过 disabled / exhausted 候选，got %q", got)
	}
}

// TestLowEntropySessionID 锁定弱会话标识判定。
//
// 生产全量请求都带 X-Session-ID: 1，若照收就会把所有客户端折叠成同一个会话，
// 1 小时粘性窗口内选号退化成钉死单一账号。短值与被同一字符填满的值都不可信；
// 长度足够的纯数字合法标识必须放行，误杀会把正常粘性一起关掉。
func TestLowEntropySessionID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"1", true},
		{"0", true},
		{"s1", true},
		{"abc", true},
		{"aaaa", true},
		{"0000", true},
		{"1234", false},
		{"123456", false},
		{"sess-9f2a", false},
		{"abcd", false},
	}
	for _, tc := range cases {
		if got := lowEntropySessionID(tc.id); got != tc.want {
			t.Fatalf("lowEntropySessionID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// TestExtractSessionKey_SkipsLowEntropyHeader 锁定弱会话头被剔除后回退。
//
// X-Session-ID: 1 必须被丢弃；没有其它强信号时返回空串，由调度层按负载平摊，
// 而不是把全量流量折成一个会话。
func TestExtractSessionKey_SkipsLowEntropyHeader(t *testing.T) {
	headers := map[string][]string{"X-Session-Id": {"1"}}
	if got := extractSessionKeyFromSources(headers, nil); got != "" {
		t.Fatalf("弱会话标识必须剔除并返回空串，got %q", got)
	}
}

// TestExtractSessionKey_KeepsStrongHeader 锁定强会话头正常生效。
func TestExtractSessionKey_KeepsStrongHeader(t *testing.T) {
	headers := map[string][]string{"X-Session-Id": {"conv-9f2a1b"}}
	if got := extractSessionKeyFromSources(headers, nil); got != "header:conv-9f2a1b" {
		t.Fatalf("强会话标识必须保留，got %q", got)
	}
}

// TestExtractSessionKey_FallsBackPastWeakHeader 锁定弱头不影响后续强信号。
//
// 弱标识必须「跳过继续找」，不是「命中即返回空」：X-Session-ID 是弱值，但同一
// 请求带了合法的 X-Client-Request-Id 时仍然要拿到会话键。
func TestExtractSessionKey_FallsBackPastWeakHeader(t *testing.T) {
	headers := map[string][]string{
		"X-Session-Id":        {"1"},
		"X-Client-Request-Id": {"req-7d3c5e"},
	}
	if got := extractSessionKeyFromSources(headers, nil); got != "clientreq:req-7d3c5e" {
		t.Fatalf("弱头之后必须继续沿优先级链匹配，got %q", got)
	}
}

// TestPickLeastLoaded_PrefersIdleAndKeepsOrder 锁定平级保留候选顺序。
func TestPickLeastLoaded_PrefersIdleAndKeepsOrder(t *testing.T) {
	clearAccountLoad()
	t.Cleanup(clearAccountLoad)

	accountLoadAcquire("acc-a")
	accountLoadAcquire("acc-a")

	// b 更闲：即使 a 排在前面也必须选 b。
	got := pickLeastLoaded([]activeAuthCandidate{{ID: "acc-a"}, {ID: "acc-b"}})
	if got != "acc-b" {
		t.Fatalf("必须选最闲的账号，got %q", got)
	}

	// 全部空闲：保留候选原有顺序（第一个）。
	clearAccountLoad()
	got = pickLeastLoaded([]activeAuthCandidate{{ID: "acc-a"}, {ID: "acc-b"}})
	if got != "acc-a" {
		t.Fatalf("平级时必须保留候选顺序，got %q", got)
	}
}
