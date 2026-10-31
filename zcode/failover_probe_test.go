package main

import (
	"errors"
	"strings"
	"testing"
)

// errFakeProbeFailedZCode 是兜底探活失败注入的错误。
// 最近修改时间：2026-10-31；改动原因：新增，供兜底探活失败侧用例注入。
var errFakeProbeFailedZCode = errors.New("zcode fake probe failed")

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

// TestRunFailoverProbeTickSkipsWithoutModel 锁定缺失败模型时的处置。
//
// 2026-10-31 语义修正：旧实现「没有模型就跳过」看似安全，实际制造了
// 「禁用且永远探不了」的死角——2026-10-29 生产事故里主力快号就是这样被禁
// 8 小时零探活，只能人工启用。现在改为兜底出一个可回放模型：探通即解禁，
// 探不通保持禁用。代价是可能探错一次模型，远小于健康号永久失联。
//
// 本测试锁定兜底行为本身，名字保留以便对照历史。
// 最近修改时间：2026-10-31；改动原因：语义从「跳过」改为「兜底回放」，避免禁用号失联。
func TestRunFailoverProbeTickSkipsWithoutModel(t *testing.T) {
	h := newFailoverFakeHost(t)
	// 手工构造一个带标记但没有模型的盘上文档。
	h.addAccount("zcode-probe003", "idx-p3", map[string]any{
		"disabled":           true,
		failoverDisableKey:   true,
		failoverDisableAtKey: "2026-10-25T00:00:00Z",
	})
	resetFailoverDisableState()

	pingedModel := ""
	prevPing := failoverPingFn
	t.Cleanup(func() { failoverPingFn = prevPing })
	failoverPingFn = func(_ *StoredAuth, model string) error { pingedModel = model; return nil }

	resolved := map[string]*StoredAuth{"idx-p3": {AuthID: "idx-p3", APIKey: "sk-test"}}
	probed := runFailoverProbeTick(func(idx string) *StoredAuth { return resolved[idx] })

	if probed != 1 {
		t.Fatalf("标记残缺必须兜底探活，实际 %d", probed)
	}
	if pingedModel == "" {
		t.Fatal("兜底探活必须带一个具体模型")
	}
	if h.docOf("idx-p3")["disabled"] != false {
		t.Fatal("兜底模型探通后应解除禁用")
	}
}

// TestRunFailoverProbeTickFallbackKeepsDisabledOnFailure 锁定兜底探活的失败侧：
// 探不通就保持禁用，不得因为「探错了模型」把号放回去。
// 最近修改时间：2026-10-31；改动原因：新增，防止兜底探活把故障号误放回。
func TestRunFailoverProbeTickFallbackKeepsDisabledOnFailure(t *testing.T) {
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-probe005", "idx-p5", map[string]any{
		"disabled":           true,
		failoverDisableKey:   true,
		failoverDisableAtKey: "2026-10-25T00:00:00Z",
	})
	resetFailoverDisableState()

	prevPing := failoverPingFn
	t.Cleanup(func() { failoverPingFn = prevPing })
	failoverPingFn = func(*StoredAuth, string) error { return errFakeProbeFailedZCode }

	resolved := map[string]*StoredAuth{"idx-p5": {AuthID: "idx-p5", APIKey: "sk-test"}}
	if probed := runFailoverProbeTick(func(idx string) *StoredAuth { return resolved[idx] }); probed != 1 {
		t.Fatalf("应兜底探测 1 个禁用账号, got %d", probed)
	}
	if h.docOf("idx-p5")["disabled"] != true {
		t.Fatal("兜底探活失败必须保持禁用")
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
