package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// failoverFakeHost 是宿主 auth 读写的内存替身。禁用/恢复的正确性全押在「盘上
// 顶层键」，所以断言必须直接查这份假盘，而不是只看函数返回值。
type failoverFakeHost struct {
	mu     sync.Mutex
	docs   map[string]map[string]any
	index  map[string]string
	ids    map[string]string
	names  map[string]string
	paths  map[string]string
	writes int
}

func newFailoverFakeHost(t *testing.T) *failoverFakeHost {
	t.Helper()
	h := &failoverFakeHost{
		docs:  make(map[string]map[string]any),
		index: make(map[string]string),
		ids:   make(map[string]string),
		names: make(map[string]string),
		paths: make(map[string]string),
	}

	origList := failoverListFn
	origGetPhysical := failoverGetPhysicalFn
	origGetAuth := failoverGetAuthFn
	origResolve := failoverResolveIndexFn
	origPersist := failoverPersistFn
	origWait := failoverWaitConfirmed
	origPing := failoverPingFn
	origNow := failoverNowFn
	t.Cleanup(func() {
		failoverListFn = origList
		failoverGetPhysicalFn = origGetPhysical
		failoverGetAuthFn = origGetAuth
		failoverResolveIndexFn = origResolve
		failoverPersistFn = origPersist
		failoverWaitConfirmed = origWait
		failoverPingFn = origPing
		failoverNowFn = origNow
		clearFailoverProbeModels()
		clearFailoverDisabledLocalAll()
	})

	failoverNowFn = time.Now
	failoverWaitConfirmed = func(string, bool, time.Duration) bool { return true }
	failoverGetAuthFn = func(string) (*storedAuth, error) { return &storedAuth{}, nil }

	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		out := make([]pluginapi.HostAuthFileEntry, 0, len(h.docs))
		for idx, doc := range h.docs {
			out = append(out, pluginapi.HostAuthFileEntry{
				ID:        h.ids[idx],
				AuthIndex: idx,
				Name:      h.names[idx],
				Disabled:  doc["disabled"] == true,
			})
		}
		return out, nil
	}

	failoverGetPhysicalFn = func(authIndex string) (*hostAuthPhysical, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		doc, ok := h.docs[authIndex]
		if !ok {
			return nil, errors.New("fake host: no such auth")
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		return &hostAuthPhysical{
			AuthIndex: authIndex,
			Name:      h.names[authIndex],
			Path:      h.paths[authIndex],
			JSON:      raw,
			Disabled:  doc["disabled"] == true,
		}, nil
	}

	failoverResolveIndexFn = func(authID string) (string, string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.index[authID], authID
	}

	failoverPersistFn = func(_, path string, _ string, raw []byte) error {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return err
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		for idx, p := range h.paths {
			if p == path {
				h.docs[idx] = doc
				break
			}
		}
		h.writes++
		return nil
	}
	return h
}

// addAccount 在假盘上放一个账号，extra 是要预置的顶层键（如别人的禁用标记）。
func (h *failoverFakeHost) addAccount(authID, authIndex string, extra map[string]any) {
	doc := map[string]any{
		"disabled": false,
		"account":  map[string]any{"uid": authID},
	}
	for k, v := range extra {
		doc[k] = v
	}
	name := "workbuddy-" + authID + ".json"
	h.mu.Lock()
	defer h.mu.Unlock()
	h.docs[authIndex] = doc
	h.index[authID] = authIndex
	h.ids[authIndex] = authID
	h.names[authIndex] = name
	h.paths[authIndex] = "/tmp/host/" + name
}

// docOf 读取假盘上某账号当前的顶层键。
func (h *failoverFakeHost) docOf(authIndex string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.docs[authIndex]
}

// writeCount 返回累计写盘次数。
func (h *failoverFakeHost) writeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.writes
}

// clearFailoverProbeModels / clearFailoverDisabledLocalAll 是本机制两张内存表的
// 测试清场入口。它们刻意放在测试侧而不是生产文件里：生产代码没有任何「清空全部」
// 的合理时机（禁用状态的真相源在盘上，进程重启后本就该从空集合开始）。
// 最近修改时间：2026-10-21；改动原因：新增，把测试辅助从生产代码移出来。
func clearFailoverProbeModels() {
	failoverProbeModelMu.Lock()
	failoverProbeModels = make(map[string]string)
	failoverProbeModelMu.Unlock()
}

func clearFailoverDisabledLocalAll() {
	failoverProbeModelMu.Lock()
	failoverDisabledLocal = make(map[string]struct{})
	failoverDisableAtLocal = make(map[string]string)
	failoverProbeModelMu.Unlock()
}

func TestDisableAccountForFailoverWritesOwnMarkers(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-1", "idx-1", map[string]any{"success_count": 9, "note": "ok"})

	if !disableAccountForFailover("wb-1", "glm-4.6") {
		t.Fatal("enabled 账号失败应被禁用")
	}
	if got := h.writeCount(); got != 1 {
		t.Fatalf("写盘次数 = %d, want 1", got)
	}
	doc := h.docOf("idx-1")
	if doc["disabled"] != true {
		t.Fatalf("disabled = %v, want true", doc["disabled"])
	}
	if doc[failoverDisableKey] != true {
		t.Fatalf("%s = %v, want true", failoverDisableKey, doc[failoverDisableKey])
	}
	if doc[failoverDisableModelKey] != "glm-4.6" {
		t.Fatalf("%s = %v, want glm-4.6", failoverDisableModelKey, doc[failoverDisableModelKey])
	}
	// 既有顶层键必须原样保留：本机制逐键改文档，不重建。
	if doc["success_count"] != float64(9) || doc["note"] != "ok" {
		t.Fatalf("既有顶层键被重建丢失: %v", doc)
	}
}

func TestDisableAccountForFailoverIsIdempotent(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-2", "idx-2", nil)

	if !disableAccountForFailover("wb-2", "glm-4.6") {
		t.Fatal("首次禁用应成功")
	}
	// 宿主同请求轮转与并发请求会对同一账号反复记账，第二次起不得再写盘，
	// 否则 fsnotify 事件风暴会让宿主反复 reload。
	if !disableAccountForFailover("wb-2", "deepseek-v4.1-flash") {
		t.Fatal("已在本机制管辖下的账号应返回 true")
	}
	if got := h.writeCount(); got != 1 {
		t.Fatalf("重复禁用后的写盘次数 = %d, want 1", got)
	}
	// 不写盘也要更新内存失败模型，探活才能回放最新故障。
	if got := failoverProbeModelFor("wb-2"); got != "deepseek-v4.1-flash" {
		t.Fatalf("内存失败模型 = %q, want deepseek-v4.1-flash", got)
	}
}

func TestDisableAccountForFailoverNeverClaimsOthersDisabled(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]any
	}{
		{"manual", map[string]any{"manual_disable": true}},
		{"exhausted", map[string]any{"exhausted_disable": true}},
		{"markerless", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFailover(t)
			h := newFailoverFakeHost(t)
			extra := map[string]any{"disabled": true}
			for k, v := range tc.extra {
				extra[k] = v
			}
			h.addAccount("wb-3", "idx-3", extra)

			if disableAccountForFailover("wb-3", "glm-4.6") {
				t.Fatalf("%s 已禁用账号不应被本机制认领", tc.name)
			}
			if got := h.writeCount(); got != 0 {
				t.Fatalf("%s 已禁用账号写盘次数 = %d, want 0", tc.name, got)
			}
		})
	}
}

func TestReenableAccountForFailoverYieldsToManualDisable(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-4", "idx-4", nil)

	disableAccountForFailover("wb-4", "glm-4.6")
	// 禁用期间用户又表达了手动禁用意图：本机制必须让位，绝不解除。
	h.docOf("idx-4")["manual_disable"] = true
	before := h.writeCount()

	if reenableAccountForFailover("idx-4", "wb-4") {
		t.Fatal("带 manual_disable 的账号不得被本机制解除禁用")
	}
	if got := h.writeCount(); got != before {
		t.Fatalf("写盘次数 = %d, want 不变(%d)", got, before)
	}
}

func TestReenableAccountForFailoverClearsKeysAndKeepsOthers(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-5", "idx-5", map[string]any{"success_count": 3, "note": "n"})

	disableAccountForFailover("wb-5", "glm-4.6")
	if !reenableAccountForFailover("idx-5", "wb-5") {
		t.Fatal("本机制禁用的账号应能解除")
	}
	doc := h.docOf("idx-5")
	if doc["disabled"] != false {
		t.Fatalf("disabled = %v, want false", doc["disabled"])
	}
	for _, k := range []string{failoverDisableKey, failoverDisableModelKey, failoverDisableAtKey} {
		if _, ok := doc[k]; ok {
			t.Fatalf("恢复后 %s 未清除: %v", k, doc)
		}
	}
	if doc["success_count"] != float64(3) || doc["note"] != "n" {
		t.Fatalf("恢复丢顶层键: %v", doc)
	}
	if got := failoverProbeModelFor("wb-5"); got != "" {
		t.Fatalf("恢复后内存失败模型未清理: %q", got)
	}
	// 标记已不在，再解一次不得写盘、不得返回成功。
	before := h.writeCount()
	if reenableAccountForFailover("idx-5", "wb-5") {
		t.Fatal("无本机制标记时不得返回成功")
	}
	if got := h.writeCount(); got != before {
		t.Fatalf("重复解除写盘次数 = %d, want %d", got, before)
	}
}

func TestFailoverProbeTargetsOnlyReturnsOwnDisabled(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("enabled", "idx-a", nil)
	h.addAccount("manual", "idx-b", map[string]any{"manual_disable": true})
	h.addAccount("mine", "idx-c", nil)

	disableAccountForFailover("mine", "glm-4.6")
	// 手动禁用的账号置成 disabled 但无本机制标记：不得进候选。
	h.docOf("idx-b")["disabled"] = true

	targets := failoverProbeTargets()
	if len(targets) != 1 {
		t.Fatalf("候选数 = %d, want 1: %+v", len(targets), targets)
	}
	if targets[0].authIndex != "idx-c" || targets[0].model != "glm-4.6" {
		t.Fatalf("候选不符: %+v", targets[0])
	}
}

func TestFailoverProbeTargetsRebuildsFromDiskAfterRestart(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	// 直接写盘模拟「进程重启后内存为空」：禁用标记与失败模型都只在文件里。
	h.addAccount("wb-6", "idx-6", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "deepseek-v4.1-flash",
		failoverDisableAtKey:    "2026-10-21T00:00:00Z",
	})

	targets := failoverProbeTargets()
	if len(targets) != 1 {
		t.Fatalf("重启后应仅靠盘上标记重建候选, got %+v", targets)
	}
	if targets[0].model != "deepseek-v4.1-flash" {
		t.Fatalf("盘上模型 = %q, want deepseek-v4.1-flash", targets[0].model)
	}
}

func TestRunFailoverProbeTickReenablesOnlyOnPingSuccess(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("bad", "idx-bad", nil)
	h.addAccount("good", "idx-good", nil)
	disableAccountForFailover("bad", "keep-bad-model")
	disableAccountForFailover("good", "glm-4.6")
	before := h.writeCount()

	// 两个账号都只回放自己失败的那个模型：bad 仍然不通，good 已恢复。
	failoverPingFn = func(sa *storedAuth, chosenModel string) error {
		switch sa.Account.UID {
		case "bad":
			return errors.New("still blocked")
		case "good":
			return nil
		}
		return errors.New("unexpected account " + sa.Account.UID)
	}
	resolve := func(authIndex string) *storedAuth {
		uid := strings.TrimPrefix(authIndex, "idx-")
		return &storedAuth{Account: storedAccount{UID: uid}}
	}

	if probed := runFailoverProbeTick(resolve); probed != 2 {
		t.Fatalf("本轮探测数 = %d, want 2", probed)
	}
	if h.docOf("idx-bad")["disabled"] != true {
		t.Fatal("失败模型探不通的账号应保持禁用")
	}
	if got := h.writeCount(); got != before+1 {
		t.Fatalf("写盘次数 = %d, want %d（仅 good 恢复）", got, before+1)
	}
	if h.docOf("idx-good")["disabled"] != false {
		t.Fatal("失败模型探通的账号应解除禁用")
	}
}

// TestRunFailoverProbeOtherModelSuccessDoesNotRecover 锁定 2026-10-22 用户裁定：
// 每个账号的每个模型额度彼此独立，别的模型探通不能证明失败模型已恢复，账号必须
// 保持禁用——否则放回路由的下一个同模型请求会照样失败。
func TestRunFailoverProbeOtherModelSuccessDoesNotRecover(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("model-blocked", "idx-mb", nil)
	disableAccountForFailover("model-blocked", "blocked-model")

	var order []string
	failoverPingFn = func(_ *storedAuth, m string) error {
		order = append(order, m)
		if m == "blocked-model" {
			return errors.New("11128 still blocked")
		}
		return nil
	}

	runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} })
	if len(order) != 1 || order[0] != "blocked-model" {
		t.Fatalf("只应探测失败模型一次, got %v", order)
	}
	if h.docOf("idx-mb")["disabled"] != true {
		t.Fatalf("失败模型未恢复时账号必须保持禁用, doc=%v", h.docOf("idx-mb"))
	}
}

// TestRunFailoverProbeStopsAfterOnePing 锁定「一个账号只发一次探活」：
// 无论成败都不得在同轮追加其它模型，避免烧掉失败模型以外的额度。
func TestRunFailoverProbeStopsAfterOnePing(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("quick-ok", "idx-q", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "glm-4.6",
	})

	pinged := 0
	failoverPingFn = func(*storedAuth, string) error { pinged++; return nil }
	runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} })
	if pinged != 1 {
		t.Fatalf("单账号单轮只应探活一次, pinged = %d, want 1", pinged)
	}
	if h.docOf("idx-q")["disabled"] != false {
		t.Fatal("探通后应解除禁用")
	}
}

// TestRunFailoverProbeWithoutFailedModelSkips 锁定缺失败模型时的处置：
// 没有可回放的目标就跳过，既不发探活请求、也不解除禁用。无法验证就不恢复，
// 比盲目放回安全。
func TestRunFailoverProbeWithoutFailedModelSkips(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	// 盘上有本机制标记但没有模型键：进程重启前写坏了或早期版本留下的形态。
	h.addAccount("nomodel", "idx-n", map[string]any{
		"disabled":         true,
		failoverDisableKey: true,
	})

	pinged := 0
	failoverPingFn = func(*storedAuth, string) error { pinged++; return nil }
	if probed := runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }); probed != 0 {
		t.Fatalf("无失败模型时应跳过, probed = %d", probed)
	}
	if pinged != 0 {
		t.Fatalf("无失败模型时不应发任何探活请求, pinged = %d", pinged)
	}
	if h.docOf("idx-n")["disabled"] != true {
		t.Fatal("跳过的账号必须保持禁用")
	}
}

// TestRunFailoverProbeSyncsLatestFailedModel 锁定内存表对盘上旧模型的覆盖：
// 同一账号换了个模型再次失败时，探活必须去验最新的那个，而不是盘上上次留下的。
func TestRunFailoverProbeSyncsLatestFailedModel(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("multi", "idx-multi", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "old-model",
	})
	rememberFailoverProbeModel("multi", "new-model")

	var order []string
	failoverPingFn = func(_ *storedAuth, m string) error {
		order = append(order, m)
		return nil
	}
	runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} })
	if len(order) != 1 || order[0] != "new-model" {
		t.Fatalf("应回放内存里最新的失败模型 new-model, got %v", order)
	}
}

func TestRunFailoverTickNonOverlapSkipsWhenBusy(t *testing.T) {
	// 用户裁定的调度语义：上一轮没跑完，下一轮触发立即跳过（不排队）。
	release := make(chan struct{})
	started := make(chan struct{})
	if !runFailoverTickNonOverlap(func() {
		close(started)
		<-release
	}) {
		t.Fatal("空闲时应能启动一轮")
	}
	<-started
	if runFailoverTickNonOverlap(func() {}) {
		t.Fatal("上一轮未结束时应跳过本轮")
	}
	close(release)

	// 上一轮结束后必须能再次进入，否则「跳过」会被永久化。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runFailoverTickNonOverlap(func() {}) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("上一轮结束后仍无法进入下一轮")
}

func TestFailoverProbeIntervalMatchesUserRuling(t *testing.T) {
	if failoverProbeInterval != 10*time.Minute {
		t.Fatalf("探活周期 = %v, want 10m（用户裁定：账号规模 200 级，不按 60 秒扫）", failoverProbeInterval)
	}
}

// TestFailoverMarkersSurviveWhitelistRebuild 锁定「标记搬运」这条唯一防线。
//
// buildAuthFileJSON 是白名单重建：只输出固定几个键 + extra，盘上其余顶层键
// 全部丢弃。lifecycle 的 disableAuth / deleteAuth / syncAuthNote 都走它，若某个
// 路径忘了 putFailoverMarkersIntoExtra，账号会保持 disabled 却丢掉 failover_disable
// 标记——探活循环按标记挑候选，从此再也找不到它，账号永久卡在禁用态（静默遗忘）。
func TestFailoverMarkersSurviveWhitelistRebuild(t *testing.T) {
	onDisk := []byte(`{
		"type":"workbuddy-provider","disabled":true,"note":"CN · 禁用",
		"failover_disable":true,"failover_disable_model":"glm-4.6",
		"failover_disable_at":"2026-10-21T02:13:07Z",
		"success_count":12
	}`)
	sa := &storedAuth{
		Auth:    storedTokens{AccessToken: "at", Domain: "www.codebuddy.cn"},
		Account: storedAccount{UID: "u1"},
	}

	// 重建路径的实际调用形态：先把调用方自己的 extra 与盘上标记合并，再重建。
	extra := putFailoverMarkersIntoExtra(map[string]any{"exhausted_disable": true}, onDisk)
	raw, err := buildAuthFileJSON(sa, true, "CN · 禁用 · 耗尽", extra)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m[failoverDisableKey] != true {
		t.Fatalf("重建后丢失 %s（账号会永久卡在禁用态）: %v", failoverDisableKey, m)
	}
	if m[failoverDisableModelKey] != "glm-4.6" {
		t.Fatalf("重建后丢失回放模型: %v", m[failoverDisableModelKey])
	}
	if m[failoverDisableAtKey] != "2026-10-21T02:13:07Z" {
		t.Fatalf("重建后丢失禁用时刻: %v", m[failoverDisableAtKey])
	}
	// 调用方自己的标记不得被搬运逻辑覆盖。
	if m["exhausted_disable"] != true {
		t.Fatalf("调用方 extra 被覆盖: %v", m["exhausted_disable"])
	}
	if m["disabled"] != true {
		t.Fatalf("重建必须保持 disabled: %v", m["disabled"])
	}
}

// TestFailoverMarkersAbsentForOthers 锁定搬运只在盘上确实带本机制标记时发生：
// 人工禁用 / 额度耗尽禁用的号重建后必须原样没有任何 failover 键，否则探活会去
// 「恢复」一个不属于自己的禁用。
func TestFailoverMarkersAbsentForOthers(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"manual", `{"disabled":true,"manual_disable":true}`},
		{"exhausted", `{"disabled":true,"exhausted_disable":true}`},
		{"markerless", `{"disabled":true}`},
		{"enabled", `{"disabled":false}`},
		{"malformed", `not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if markers := failoverMarkersFromJSON([]byte(tc.doc)); len(markers) != 0 {
				t.Fatalf("%s 不应产生搬运键: %v", tc.name, markers)
			}
			// extra 为 nil 时必须原样返回 nil，避免调用方误以为有内容。
			if got := putFailoverMarkersIntoExtra(nil, []byte(tc.doc)); got != nil {
				t.Fatalf("%s 应原样返回 nil, got %v", tc.name, got)
			}
		})
	}
}

// TestParseFailoverAuthDocFromDiskShape 锁定盘上文档解析：三个顶层键 + 别人的
// 标记都要被正确识别，解析失败一律走保守分支（不认领、不覆盖）。
func TestParseFailoverAuthDocFromDiskShape(t *testing.T) {
	doc := parseFailoverAuthDoc([]byte(`{
		"disabled":true,"failover_disable":true,
		"failover_disable_model":" deepseek-v4.1-flash ",
		"manual_disable":true
	}`))
	if !doc.disabled || !doc.failoverDisable || !doc.manualDisable {
		t.Fatalf("三个布尔键解析错误: %+v", doc)
	}
	if doc.failoverModel != "deepseek-v4.1-flash" {
		t.Fatalf("模型名未去空白: %q", doc.failoverModel)
	}
	bad := parseFailoverAuthDoc([]byte(`not json`))
	if bad.disabled || bad.failoverDisable || bad.failoverModel != "" || bad.manualDisable {
		t.Fatalf("非法文档必须解析为全空: %+v", bad)
	}
}

// TestReconcileFailoverLocalSetDropsExternallyEnabled 锁定「外部启用」这条恢复路径
// 也能解掉插件的排除：用户在 CPA 面板或宿主 UI 把号启用后，盘上 disabled 变回
// false，但这条路径不经过 reenableAccountForFailover。若本地集合不跟着对账，
// 调度与入口前置拦截会一直跳过这个明明可用的号，直到插件重启。
func TestReconcileFailoverLocalSetDropsExternallyEnabled(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-r", "idx-r", nil)
	disableAccountForFailover("wb-r", "glm-4.6")
	if !isFailoverDisabledLocal("wb-r") {
		t.Fatal("前置条件不成立：禁用后应被本地排除")
	}

	// 模拟外部启用：盘上改回 enabled，插件内存毫不知情。
	h.docOf("idx-r")["disabled"] = false
	if cleared := reconcileFailoverLocalSet(); cleared == 0 {
		t.Fatal("外部已启用的号必须被对账清除")
	}
	if isFailoverDisabledLocal("wb-r") {
		t.Fatal("对账后该号应重新可路由")
	}
	if got := failoverProbeModelFor("wb-r"); got != "" {
		t.Fatalf("对账应一并清掉滞留的失败模型, got %q", got)
	}
}

// TestReconcileFailoverLocalSetKeepsUnknownKeys 锁定对账的保守方向：
// 只清「盘上明确为 enabled」的键，列表里查不到的（RPC 未覆盖 / 账号刚建）保留，
// 否则一次列表抖动就会把真正的禁用号放回路由。
func TestReconcileFailoverLocalSetKeepsUnknownKeys(t *testing.T) {
	resetFailover(t)
	newFailoverFakeHost(t)
	markFailoverDisabledLocal("ghost-acc", "idx-ghost")
	if reconcileFailoverLocalSet() != 0 {
		t.Fatal("列表里没有的键不得被清除")
	}
	if !isFailoverDisabledLocal("ghost-acc") {
		t.Fatal("查不到的键应保留排除（宁可错杀不可漏杀的反向）")
	}

	// 列表 RPC 失败时同样不清任何东西。
	origList := failoverListFn
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return nil, errors.New("host unavailable")
	}
	if reconcileFailoverLocalSet() != 0 {
		t.Fatal("列表不可用时应原样保留")
	}
	failoverListFn = origList
	if !isFailoverDisabledLocal("ghost-acc") {
		t.Fatal("列表不可用不得影响排除状态")
	}
}

// TestReconcileMatchesDiskFileNameForm 锁定键形态对齐：记账时手里可能是裸 UID，
// 宿主列表给的是 workbuddy-<uid>.json，对账必须能跨形态命中，否则外部启用无效。
func TestReconcileMatchesDiskFileNameForm(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	uid := "29d35850-2d06-4bd5-9f8f-0c26d9e76421"
	h.addAccount(uid, "idx-f", nil)
	// 本地键写成裸 UID（markFailoverDisabledLocal 的归一化形态）。
	markFailoverDisabledLocal(uid, "")
	if !isFailoverDisabledLocal("workbuddy-" + uid + ".json") {
		t.Fatal("前置条件不成立：文件名形态应命中本地排除")
	}
	h.docOf("idx-f")["disabled"] = false
	if reconcileFailoverLocalSet() == 0 {
		t.Fatal("列表条目为 workbuddy-<uid>.json 时也必须匹配到裸 UID 键")
	}
	if isFailoverDisabledLocal(uid) {
		t.Fatal("对账后裸 UID 键应被清除")
	}
}

// TestAllAuthRebuildCallSitesCarryFailoverMarkers 是标记搬运的调用点审计闸门。
//
// 为什么要有这条：漏搬运标记是本方案最高频、也最难发现事故源——代码照常编译、
// 测试照常绿，只是某个禁用号从此再也进不了探活候选（静默遗忘，只能人工启用）。
// 靠人工记不住，所以在这里机械扫描每个走白名单重建的落点，要求其函数体内必须
// 出现 putFailoverMarkersIntoExtra（或显式登记在豁免表里并写明理由）。
// 以后新增 buildAuthFileJSON 调用点若忘了搬运，本用例立即失败。
func TestAllAuthRebuildCallSitesCarryFailoverMarkers(t *testing.T) {
	// 豁免表：不经盘上既有文档、或恢复时本就该丢掉本机制标记的重建落点。
	exempt := map[string]string{
		// 导入新凭据：盘上还没有这个号，不存在需要搬运的标记。
		"handleImportAuth": "导入新凭据，盘上无既有文档",
		// 额度恢复启用：写的是 disabled:false，且只对带 exhausted_disable 的文档
		// 可达（failover 与 exhausted 两个标记天然互斥），此处丢掉本机制标记是
		// 正确结果——号已重新可用，不该再被探活认领。
		"reenableAuth": "恢复启用，本就应清除禁用标记",
	}

	carryMarker := "putFailoverMarkersIntoExtra"
	// 按 gofmt 约束识别顶层函数：`func` 顶格开始、`}` 顶格结束。
	type site struct {
		file   string
		line   int
		fn     string
		hasCar bool
	}
	var sites []site

	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		lines := strings.Split(string(data), "\n")
		fnName := ""
		fnBody := []string(nil)
		for i, ln := range lines {
			if strings.HasPrefix(ln, "func ") {
				fnName = funcNameOf(ln)
				fnBody = nil
			}
			if fnName != "" {
				fnBody = append(fnBody, ln)
			}
			// 跳过注释行与函数定义本身，只认真实调用。
			trimmed := strings.TrimSpace(ln)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(ln, "func buildAuthFileJSON") {
				continue
			}
			if !strings.Contains(ln, "buildAuthFileJSON(") {
				continue
			}
			hasCar := false
			for _, b := range fnBody {
				if strings.Contains(b, carryMarker) {
					hasCar = true
					break
				}
			}
			sites = append(sites, site{file: p, line: i + 1, fn: fnName, hasCar: hasCar})
			if strings.HasPrefix(ln, "}") {
				fnName = ""
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk package: %v", err)
	}
	if len(sites) == 0 {
		t.Fatal("审计未找到任何 buildAuthFileJSON 调用点：扫描逻辑已失效，必须修扫描而不是删断言")
	}
	for _, s := range sites {
		t.Logf("审计调用点 %s:%d func=%s 已搬运=%v", s.file, s.line, s.fn, s.hasCar)
	}
	for _, s := range sites {
		if s.hasCar {
			continue
		}
		if _, ok := exempt[s.fn]; ok {
			continue
		}
		t.Errorf("%s:%d 走白名单重建但未搬运 failover 标记（函数 %s）：禁用号会被探活循环静默遗忘", s.file, s.line, s.fn)
	}
	// 豁免项若已不需要豁免（调用点消失），提示收紧而非放任。
	for fn, why := range exempt {
		found := false
		for _, s := range sites {
			if s.fn == fn {
				found = true
				break
			}
		}
		if !found {
			t.Logf("豁免项 %s（%s）已无对应调用点，可从表中删除", fn, why)
		}
	}
}

// funcNameOf 从一行顶层 func 声明里取出函数名。
// [参数] line：以 "func " 开头的源码行。
// [返回] 函数名；接收者方法返回 "方法名"，匿名/解析失败返回空串。
// 最近修改时间：2026-10-21；改动原因：新增，供调用点审计归属函数。
func funcNameOf(line string) string {
	s := strings.TrimSpace(strings.TrimPrefix(line, "func "))
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	// 接收者方法形如 "(t *T) Name"，取最后一段。
	if fields := strings.Fields(s); len(fields) > 0 {
		return fields[len(fields)-1]
	}
	return ""
}

// TestConcurrentDisableFromAliasIDsWritesOnce 锁定本地端到端实测到的缺陷：
// 同一次失败会被两个记账入口同时上报（宿主 auth ID 形态与账号 UID 形态），
// 两者解析到同一个物理文档。禁用写盘必须只发生一次，否则每个入口都会触发
// 一次宿主 watcher 增量重载。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-22；改动原因：新增，覆盖文档读改写的并发串行化。
func TestConcurrentDisableFromAliasIDsWritesOnce(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("workbuddy-e2etest01.json", "idx-alias", nil)
	// 第二个身份别名指向同一物理文档：复刻 usedAuthID（宿主文件名 ID）与
	// curAuthUID（账号 UID）两条记账入口。
	h.mu.Lock()
	h.index["e2etest01"] = "idx-alias"
	h.mu.Unlock()

	const goroutines = 24
	var wg sync.WaitGroup
	wg.Add(goroutines)
	ids := []string{"workbuddy-e2etest01.json", "e2etest01"}
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			disableAccountForFailover(ids[n%len(ids)], "fake-model-a")
		}(i)
	}
	wg.Wait()

	if got := h.writeCount(); got != 1 {
		t.Fatalf("同一物理文档的并发禁用写盘次数 = %d, want 1", got)
	}
	doc := h.docOf("idx-alias")
	if doc["disabled"] != true || doc[failoverDisableKey] != true {
		t.Fatalf("并发后盘上状态不完整: %v", doc)
	}
}

// TestParseAuthCarriesFailoverMarkersIntoMetadata 锁定端到端实测到的缺陷：
// 宿主吸收 auth 文件后会用插件回传的 StorageJSON + Metadata 反向重建物理文档
// （mergedStorageJSON）。插件的 Metadata 白名单里没有本机制的键，宿主每次回存
// 都把 failover_disable 抹掉，而 disabled 因为在白名单里反而存活——账号于是停在
// 「宿主永久禁用、探活却再也找不到标记」的死角，只能人工启用。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-22；改动原因：新增，覆盖标记经宿主回存通道存活。
func TestParseAuthCarriesFailoverMarkersIntoMetadata(t *testing.T) {
	uid := "e2etest-markers"
	raw := []byte(`{"type":"` + providerName + `","disabled":true,` +
		`"failover_disable":true,"failover_disable_model":"fake-model-a",` +
		`"failover_disable_at":"2026-10-22T00:00:00Z",` +
		`"auth":{"accessToken":"at","refreshToken":"rt","expiresAt":9999999999,"domain":""},` +
		`"account":{"uid":"` + uid + `","nickname":"n"}}`)

	body, _ := json.Marshal(pluginapi.AuthParseRequest{
		Provider: providerName,
		FileName: "workbuddy-" + uid + ".json",
		RawJSON:  raw,
	})
	out, err := handleParseAuth(body)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeParseAuth(t, out)
	if !resp.Handled {
		t.Fatal("本插件凭据必须被认领")
	}
	for key, want := range map[string]any{
		failoverDisableKey:      true,
		failoverDisableModelKey: "fake-model-a",
		failoverDisableAtKey:    "2026-10-22T00:00:00Z",
	} {
		if got := resp.Auth.Metadata[key]; got != want {
			t.Fatalf("回传 Metadata[%s] = %v, want %v（宿主回存会抹掉该标记）", key, got, want)
		}
	}
	if !resp.Auth.Disabled {
		t.Fatal("盘上 disabled 必须如实回传，否则宿主内存记录被重新启用")
	}
}

// TestRefreshAuthKeepsDisabledAndMarkers 锁定第二个同源口子：宿主挑选刷新候选
// 时不看 Disabled，插件若在刷新回传里固定 disabled:false，禁用中的号会被一次
// 后台 token 刷新静默解封、完全绕开 10 分钟探活。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-22；改动原因：新增，覆盖刷新路径不禁用解封。
func TestRefreshAuthKeepsDisabledAndMarkers(t *testing.T) {
	sa := &storedAuth{
		Auth:    storedTokens{AccessToken: "new-at", RefreshToken: "rt", Domain: "www.codebuddy.cn"},
		Account: storedAccount{UID: "u-refresh", Nickname: "n"},
	}
	hostMeta := map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "fake-model-a",
		"note":                  "CN · 已禁用",
	}

	ad := toAuthDataOpts(sa, nil, true)
	carryFailoverKeysIntoMetadata(ad.Metadata, hostMeta)
	if !ad.Disabled {
		t.Fatal("刷新不得改变禁用状态：回传 disabled:false 会绕开探活直接解封")
	}
	if ad.Metadata[failoverDisableKey] != true {
		t.Fatalf("刷新回传丢失 %s: %v", failoverDisableKey, ad.Metadata[failoverDisableKey])
	}
	if ad.Metadata[failoverDisableModelKey] != "fake-model-a" {
		t.Fatalf("刷新回传丢失 %s: %v", failoverDisableModelKey, ad.Metadata[failoverDisableModelKey])
	}
}

// TestParseAuthOmitsMarkersWhenNotFailoverDisabled 确认回传不是无条件搬键：
// 不带本机制标记的文档（用户手动禁用、健康号）不得被塞进 failover_disable，
// 否则会被探活循环误认领、把用户手动禁用的号自动启用。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-22；改动原因：新增，覆盖回传的认领边界。
func TestParseAuthOmitsMarkersWhenNotFailoverDisabled(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"用户手动禁用", `{"type":"` + providerName + `","disabled":true,"manual_disable":true,` +
			`"auth":{"accessToken":"at"},"account":{"uid":"u-manual"}}`},
		{"健康号", `{"type":"` + providerName + `","disabled":false,` +
			`"auth":{"accessToken":"at"},"account":{"uid":"u-ok"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(pluginapi.AuthParseRequest{
				Provider: providerName,
				RawJSON:  []byte(tc.doc),
			})
			out, err := handleParseAuth(body)
			if err != nil {
				t.Fatal(err)
			}
			resp := decodeParseAuth(t, out)
			if _, ok := resp.Auth.Metadata[failoverDisableKey]; ok {
				t.Fatalf("非本机制禁用的号被塞入 %s，探活会误接管其恢复", failoverDisableKey)
			}
		})
	}
}

// TestHostRebuildFromParseAuthDataKeepsMarkers 把宿主的回存算法在本机复现一遍，
// 证明「回传 Metadata」这条修复真的能让标记活过宿主重写。
//
// 宿主侧证据（CLIProxyAPI v8）：吸收插件 auth 文件后用插件回传的
// StorageJSON + Metadata 重建物理文档——
//
//	sdk/auth/filestore.go:113-122 把 disabled 写进 Metadata 再 SaveTokenToFile
//	internal/pluginhost/auth_provider.go:540 mergedStorageJSON(raw, meta)：
//	  先解析 StorageJSON，再用 Metadata 逐键覆盖，最后整体落盘。
//
// 因此只要标记进入 Metadata，宿主重建就会保留；不进入就一定被抹掉。
//
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-22；改动原因：新增，端到端缺陷的离线复现锁。
func TestHostRebuildFromParseAuthDataKeepsMarkers(t *testing.T) {
	uid := "e2etest-rebuild"
	raw := []byte(`{"type":"` + providerName + `","disabled":true,` +
		`"failover_disable":true,"failover_disable_model":"fake-model-a",` +
		`"failover_disable_at":"2026-10-22T00:00:00Z",` +
		`"auth":{"accessToken":"at","refreshToken":"rt","expiresAt":9999999999,"domain":""},` +
		`"account":{"uid":"` + uid + `","nickname":"n"}}`)

	body, _ := json.Marshal(pluginapi.AuthParseRequest{Provider: providerName, RawJSON: raw})
	out, err := handleParseAuth(body)
	if err != nil {
		t.Fatal(err)
	}
	ad := decodeParseAuth(t, out).Auth

	rebuilt := hostMergedStorageJSON(t, ad.StorageJSON, ad.Metadata)
	if rebuilt[failoverDisableKey] != true {
		t.Fatalf("宿主回存后 %s 丢失: %v", failoverDisableKey, rebuilt[failoverDisableKey])
	}
	if rebuilt[failoverDisableModelKey] != "fake-model-a" {
		t.Fatalf("宿主回存后探活模型丢失: %v", rebuilt[failoverDisableModelKey])
	}
	if rebuilt["disabled"] != true {
		t.Fatalf("宿主回存后禁用状态丢失: %v", rebuilt["disabled"])
	}
	// 重建文档必须能被探活循环认领：候选按盘上标记枚举。
	if !parseFailoverAuthDoc(mustMarshalMap(t, rebuilt)).failoverDisable {
		t.Fatal("重建后的文档不再被探活循环认领")
	}
}

// hostMergedStorageJSON 复刻宿主 mergedStorageJSON：StorageJSON 为底，Metadata 逐键覆盖。
// [参数] t：测试上下文；storage：插件回传的凭据存储；meta：插件回传的元数据。
// [返回] 宿主重建出的顶层文档。
// 最近修改时间：2026-10-22；改动原因：新增，供回存存活断言复用。
func hostMergedStorageJSON(t *testing.T, storage []byte, meta map[string]any) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(storage, &out); err != nil {
		t.Fatalf("storage parse: %v", err)
	}
	for k, v := range meta {
		out[k] = v
	}
	return out
}

// mustMarshalMap 把映射序列化回字节，失败即终止用例。
// [参数] t：测试上下文；doc：待序列化映射。
// [返回] JSON 字节。
// 最近修改时间：2026-10-22；改动原因：新增，供回存断言复用。
func mustMarshalMap(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// decodeParseAuth 把 handleParseAuth 的响应信封解成 AuthParseResponse。
// 国内版定义在 auth_identity_test.go，本插件未搬该文件，故就地提供等价实现。
//
// [参数] t：测试上下文；raw：handleParseAuth 返回的原始 JSON。
// [返回] 解析后的响应结构。
func decodeParseAuth(t *testing.T, raw []byte) pluginapi.AuthParseResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("not ok: %+v", env.Error)
	}
	var resp pluginapi.AuthParseResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("result: %v", err)
	}
	return resp
}

// TestRecordFailureScopedTriggersDiskDisable 锁定「失败记账 → 写盘禁用」这条接线：
// 双写机制的价值全在这里，若有人把 recordFailureScoped 里的
// disableAccountForFailover 调用删掉，本用例必须失败。
//
// [参数] t：测试上下文。
// [返回] 无。
func TestRecordFailureScopedTriggersDiskDisable(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-wire", "idx-wire", nil)

	// 429 属账号级失败，应同时写内存冷却与盘上禁用。
	if !recordAccountFailureForModel("wb-wire", "glm-4.6", 429, "") {
		t.Fatal("账号级失败应被记账")
	}
	if got := h.writeCount(); got == 0 {
		t.Fatal("失败记账必须触发写盘禁用，双写链路未接通")
	}
	doc := h.docOf("idx-wire")
	if doc["disabled"] != true {
		t.Fatalf("disabled = %v, want true", doc["disabled"])
	}
	if doc[failoverDisableKey] != true {
		t.Fatalf("%s = %v, want true", failoverDisableKey, doc[failoverDisableKey])
	}
	if !isFailoverDisabledLocal("wb-wire") {
		t.Fatal("写盘后必须同时进本地排除集合，供热路径快速判据")
	}
}

// withCPADisableSync 把管理 API 注入点替换为记录器，返回调用记录与恢复函数。
// [参数] t：测试上下文；enabled：是否配置密钥（false 模拟旧部署）。
// [返回] 调用记录切片指针与恢复函数。
// 最近修改时间：2026-10-29；改动原因：新增，供禁用同步路径断言复用。
func withCPADisableSync(t *testing.T, enabled bool) (*[]cpaAuthFileStatusRequest, func()) {
	t.Helper()
	origDo := cpaDisableSyncDo
	origURL, origKey := loadedCPAManagement()
	var calls []cpaAuthFileStatusRequest
	if enabled {
		setCPAManagement("http://127.0.0.1:8317", "test-key")
		cpaDisableSyncDo = func(req *http.Request) (*hostHTTPResponse, error) {
			var body cpaAuthFileStatusRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				return nil, err
			}
			calls = append(calls, body)
			return &hostHTTPResponse{StatusCode: http.StatusOK}, nil
		}
	} else {
		setCPAManagement("", "")
	}
	return &calls, func() {
		cpaDisableSyncDo = origDo
		setCPAManagement(origURL, origKey)
	}
}

// TestFailoverDisable_UsesCPAAPIWhenConfigured 锁定：配了密钥就走宿主 API，
// 且不再直写物理文件（写盘会让镜像与宿主 DB 再次分叉）。
func TestFailoverDisable_UsesCPAAPIWhenConfigured(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	calls, restore := withCPADisableSync(t, true)
	defer restore()

	h.addAccount("auth-api", "idx-api", nil)
	if !disableAccountForFailover("auth-api", "glm-4.6") {
		t.Fatal("禁用应成功")
	}
	if len(*calls) != 1 {
		t.Fatalf("API 调用次数 = %d; want 1", len(*calls))
	}
	if got := (*calls)[0]; !got.Disabled || got.AuthIndex != "idx-api" {
		t.Fatalf("API 请求体 = %+v", got)
	}
	if h.writeCount() != 0 {
		t.Fatalf("走 API 时不应写盘，实际写盘 %d 次", h.writeCount())
	}
}

// TestFailoverDisable_FallsBackToDiskWhenNotConfigured 锁定未配密钥的零回归。
func TestFailoverDisable_FallsBackToDiskWhenNotConfigured(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	_, restore := withCPADisableSync(t, false)
	defer restore()

	h.addAccount("auth-disk", "idx-disk", nil)
	if !disableAccountForFailover("auth-disk", "glm-4.6") {
		t.Fatal("禁用应成功")
	}
	if h.writeCount() != 1 {
		t.Fatalf("未配密钥应写盘一次，实际 %d", h.writeCount())
	}
	if h.docOf("idx-disk")["disabled"] != true {
		t.Fatal("盘上应被置为 disabled")
	}
}

// TestFailoverDisable_FallsBackToDiskOnAPIError 锁定 API 失败必须回落写盘。
func TestFailoverDisable_FallsBackToDiskOnAPIError(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	origDo := cpaDisableSyncDo
	origURL, origKey := loadedCPAManagement()
	defer func() {
		cpaDisableSyncDo = origDo
		setCPAManagement(origURL, origKey)
	}()
	setCPAManagement("http://127.0.0.1:8317", "test-key")
	cpaDisableSyncDo = func(*http.Request) (*hostHTTPResponse, error) {
		return &hostHTTPResponse{StatusCode: 500, Body: []byte(`{"error":"boom"}`)}, nil
	}

	h.addAccount("auth-err", "idx-err", nil)
	if !disableAccountForFailover("auth-err", "glm-4.6") {
		t.Fatal("API 失败时禁用仍应生效（走写盘）")
	}
	if h.writeCount() != 1 {
		t.Fatalf("应回落写盘一次，实际 %d", h.writeCount())
	}
}
