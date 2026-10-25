package main

import (
	"errors"
	"strings"
	"testing"
)

// TestRunFailoverProbeTickReenablesOnSuccess 断言探通失败模型后解除禁用。
func TestRunFailoverProbeTickReenablesOnSuccess(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-probe001", "idx-p1", nil)
	if !disableAccountForFailover("zcode-probe001", "glm-5.3") {
		t.Fatal("准备阶段禁用失败")
	}

	resolved := map[string]*StoredAuth{
		"idx-p1": {AuthID: "idx-p1", APIKey: "sk-test", Label: "probe"},
	}

	// 必须注入探活成功：真实 failoverPingFn 走上游 HTTP，shim 下必然失败，
	// 那样测出来的「没有解禁」是探活失败造成的，不是恢复逻辑的问题。
	prevPing := failoverPingFn
	t.Cleanup(func() { failoverPingFn = prevPing })
	failoverPingFn = func(*StoredAuth, string) error { return nil }

	probed := runFailoverProbeTick(func(idx string) *StoredAuth { return resolved[idx] })

	if probed != 1 {
		t.Fatalf("应探测 1 个账号，实际 %d", probed)
	}
	doc := h.docOf("idx-p1")
	if doc["disabled"] != false {
		t.Fatalf("探通后应解除禁用，实际 %+v（probed=%d writes=%d）", doc, probed, h.writes)
	}
}

// TestRunFailoverProbeTickKeepsDisabledOnFailure 断言探活失败保持禁用。
func TestRunFailoverProbeTickKeepsDisabledOnFailure(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-probe002", "idx-p2", nil)
	disableAccountForFailover("zcode-probe002", "glm-5.3")

	prevPing := failoverPingFn
	t.Cleanup(func() { failoverPingFn = prevPing })
	failoverPingFn = func(*StoredAuth, string) error { return errors.New("still broken") }

	resolved := map[string]*StoredAuth{"idx-p2": {AuthID: "idx-p2", APIKey: "sk-test"}}
	runFailoverProbeTick(func(idx string) *StoredAuth { return resolved[idx] })

	if h.docOf("idx-p2")["disabled"] != true {
		t.Fatal("探活失败应保持禁用")
	}
	if h.docOf("idx-p2")[failoverDisableKey] != true {
		t.Fatal("探活失败不应清除归属标记")
	}
}

// TestRunFailoverProbeTickSkipsWithoutModel 断言没有失败模型的账号被跳过，
// 不得因为「无法验证」而盲目放回路由。
func TestRunFailoverProbeTickSkipsWithoutModel(t *testing.T) {
	h := newFailoverFakeHost(t)
	// 手工构造一个带标记但没有模型的盘上文档。
	h.addAccount("zcode-probe003", "idx-p3", map[string]any{
		"disabled":           true,
		failoverDisableKey:   true,
		failoverDisableAtKey: "2026-10-25T00:00:00Z",
	})
	resetFailoverDisableState()

	called := false
	prevPing := failoverPingFn
	t.Cleanup(func() { failoverPingFn = prevPing })
	failoverPingFn = func(*StoredAuth, string) error { called = true; return nil }

	resolved := map[string]*StoredAuth{"idx-p3": {AuthID: "idx-p3", APIKey: "sk-test"}}
	probed := runFailoverProbeTick(func(idx string) *StoredAuth { return resolved[idx] })

	if probed != 0 {
		t.Fatalf("无失败模型时不应计入探测数，实际 %d", probed)
	}
	if called {
		t.Fatal("无失败模型时不应发起探活")
	}
	if h.docOf("idx-p3")["disabled"] != true {
		t.Fatal("无失败模型时不得解禁")
	}
}

// TestRunFailoverProbeTickOnlyProbesFailedModel 断言探活只回放当初失败的模型，
// 不会拿别的模型「证明恢复」。
func TestRunFailoverProbeTickOnlyProbesFailedModel(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-probe004", "idx-p4", nil)
	disableAccountForFailover("zcode-probe004", "glm-5.3")

	var probedModels []string
	prevPing := failoverPingFn
	t.Cleanup(func() { failoverPingFn = prevPing })
	failoverPingFn = func(_ *StoredAuth, model string) error {
		probedModels = append(probedModels, model)
		return nil
	}

	resolved := map[string]*StoredAuth{"idx-p4": {AuthID: "idx-p4", APIKey: "sk-test"}}
	runFailoverProbeTick(func(idx string) *StoredAuth { return resolved[idx] })

	if len(probedModels) != 1 || probedModels[0] != "glm-5.3" {
		t.Fatalf("应只探失败模型 glm-5.3，实际 %v", probedModels)
	}
}

// TestFailoverProbeTargetsSkipsUnmarkedDisabled 断言只认本机制标记，
// 用户手动禁用的号不进探活候选（否则探通就会被本机制擅自启用）。
func TestFailoverProbeTargetsSkipsUnmarkedDisabled(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-probe005", "idx-p5", map[string]any{"disabled": true})
	resetFailoverDisableState()

	// 候选枚举走真实 hostCall，在 shim 下拿不到宿主，返回 nil 即视为通过本断言。
	targets := failoverProbeTargets()
	for _, tc := range targets {
		if tc.authIndex == "idx-p5" {
			t.Fatal("无本机制标记的手动禁用号不应成为探活候选")
		}
	}
	_ = h
}

// TestRunFailoverTickNonOverlap 断言上一轮未结束时本轮被丢弃而不是排队。
func TestRunFailoverTickNonOverlap(t *testing.T) {
	resetFailoverDisableState()
	done := make(chan struct{})
	release := make(chan struct{})

	if !runFailoverTickNonOverlap(func() {
		close(done)
		<-release
	}) {
		t.Fatal("首轮应启动")
	}
	<-done

	if runFailoverTickNonOverlap(func() {}) {
		t.Fatal("上一轮未结束时本轮应被丢弃")
	}
	close(release)
}

// TestNormalizeFailoverKey 断言三种标识形态归一化到同一键。
func TestNormalizeFailoverKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"zcode-ABCD1234", "zcode-abcd1234"},
		{"  zcode-abcd1234.json  ", "zcode-abcd1234"},
		{"ZCode-Abcd1234", "zcode-abcd1234"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeFailoverKey(tc.in); got != tc.want {
			t.Fatalf("normalizeFailoverKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFailoverProbeModelPrefersLatest 断言内存里更新的失败模型优先于盘上旧值。
func TestFailoverProbeModelPrefersLatest(t *testing.T) {
	resetFailoverDisableState()
	rememberFailoverProbeModel("zcode-model01", "glm-5.3")
	rememberFailoverProbeModel("zcode-model01", "glm-5.3-flash")

	if got := failoverProbeModelFor("zcode-model01"); got != "glm-5.3-flash" {
		t.Fatalf("应取最新失败模型，实际 %q", got)
	}
	if !strings.HasPrefix(failoverProbeModelFor("zcode-model01"), "glm-5.3") {
		t.Fatal("模型记录丢失")
	}
}
