package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
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
		clearFailoverProbeQuotaAll()
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
	name := "qoderwork-" + authID + ".json"
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

// clearFailoverProbeModels / clearFailoverDisabledLocalAll / clearFailoverProbeQuotaAll
// 是本机制三张内存表的测试清场入口。它们刻意放在测试侧而不是生产文件里：生产代码
// 没有任何「清空全部」的合理时机（禁用状态的真相源在盘上，进程重启后本就该从空
// 集合开始）。
// 最近修改时间：2026-10-22；改动原因：新增，把测试辅助从生产代码移出来。
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

func clearFailoverProbeQuotaAll() {
	failoverProbeModelMu.Lock()
	failoverProbeQuota = make(map[string]*failoverProbeDay)
	failoverProbeModelMu.Unlock()
}

func TestDisableAccountForFailoverWritesOwnMarkers(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("qw-1", "idx-1", map[string]any{"success_count": 9, "note": "ok"})

	if !disableAccountForFailover("qw-1", "qmodel_preview") {
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
	if doc[failoverDisableModelKey] != "qmodel_preview" {
		t.Fatalf("%s = %v, want qmodel_preview", failoverDisableModelKey, doc[failoverDisableModelKey])
	}
	// 既有顶层键必须原样保留：本机制逐键改文档，不重建。
	if doc["success_count"] != float64(9) || doc["note"] != "ok" {
		t.Fatalf("既有顶层键被重建丢失: %v", doc)
	}
}

func TestDisableAccountForFailoverIsIdempotent(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("qw-2", "idx-2", nil)

	if !disableAccountForFailover("qw-2", "qmodel_preview") {
		t.Fatal("首次禁用应成功")
	}
	// 宿主同请求轮转与并发请求会对同一账号反复记账，第二次起不得再写盘，
	// 否则 fsnotify 事件风暴会让宿主反复 reload。
	if !disableAccountForFailover("qw-2", "auto") {
		t.Fatal("已在本机制管辖下的账号应返回 true")
	}
	if got := h.writeCount(); got != 1 {
		t.Fatalf("重复禁用后的写盘次数 = %d, want 1", got)
	}
	// 不写盘也要更新内存失败模型，探活才能回放最新故障。
	if got := failoverProbeModelFor("qw-2"); got != "auto" {
		t.Fatalf("内存失败模型 = %q, want auto", got)
	}
}

func TestDisableAccountForFailoverNeverClaimsOthersDisabled(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]any
	}{
		{"manual", map[string]any{"manual_disable": true}},
		{"exhausted", map[string]any{"exhausted_disable": true}},
		// Qoder 专属：110 日限额的 24h 持久化冷却由 daily_cooldown.go 负责，
		// 本机制不能认领，否则探活会把日限额冷却当成自己的禁用解掉。
		{"daily_cooling", map[string]any{"daily_cooling_until": "2026-10-23T00:00:00Z"}},
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
			h.addAccount("qw-3", "idx-3", extra)

			if disableAccountForFailover("qw-3", "qmodel_preview") {
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
	h.addAccount("qw-4", "idx-4", nil)

	disableAccountForFailover("qw-4", "qmodel_preview")
	// 禁用期间用户又表达了手动禁用意图：本机制必须让位，绝不解除。
	h.docOf("idx-4")["manual_disable"] = true
	before := h.writeCount()

	if reenableAccountForFailover("idx-4", "qw-4") {
		t.Fatal("带 manual_disable 的账号不得被本机制解除禁用")
	}
	if got := h.writeCount(); got != before {
		t.Fatalf("写盘次数 = %d, want 不变(%d)", got, before)
	}
}

func TestReenableAccountForFailoverClearsKeysAndKeepsOthers(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("qw-5", "idx-5", map[string]any{"success_count": 3, "note": "n"})

	disableAccountForFailover("qw-5", "qmodel_preview")
	if !reenableAccountForFailover("idx-5", "qw-5") {
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
	if got := failoverProbeModelFor("qw-5"); got != "" {
		t.Fatalf("恢复后内存失败模型未清理: %q", got)
	}
	// 标记已不在，再解一次不得写盘、不得返回成功。
	before := h.writeCount()
	if reenableAccountForFailover("idx-5", "qw-5") {
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

	disableAccountForFailover("mine", "qmodel_preview")
	// 手动禁用的账号置成 disabled 但无本机制标记：不得进候选。
	h.docOf("idx-b")["disabled"] = true

	targets := failoverProbeTargets()
	if len(targets) != 1 {
		t.Fatalf("候选数 = %d, want 1: %+v", len(targets), targets)
	}
	if targets[0].authIndex != "idx-c" || targets[0].model != "qmodel_preview" {
		t.Fatalf("候选不符: %+v", targets[0])
	}
}

func TestFailoverProbeTargetsRebuildsFromDiskAfterRestart(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	// 直接写盘模拟「进程重启后内存为空」：禁用标记与失败模型都只在文件里。
	h.addAccount("qw-6", "idx-6", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "auto",
		failoverDisableAtKey:    "2026-10-22T00:00:00Z",
	})

	targets := failoverProbeTargets()
	if len(targets) != 1 {
		t.Fatalf("重启后应仅靠盘上标记重建候选, got %+v", targets)
	}
	if targets[0].model != "auto" {
		t.Fatalf("盘上模型 = %q, want auto", targets[0].model)
	}
}

func TestRunFailoverProbeTickReenablesOnlyOnPingSuccess(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("bad", "idx-bad", nil)
	h.addAccount("good", "idx-good", nil)
	disableAccountForFailover("bad", "keep-bad-model")
	disableAccountForFailover("good", "qmodel_preview")
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

	if probed := runFailoverProbeTick(resolve, time.Now()); probed != 2 {
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
// 保持禁用。Qoder 的代价更大——探活与真实推理共享同一份日调用配额。
func TestRunFailoverProbeOtherModelSuccessDoesNotRecover(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("model-blocked", "idx-mb", nil)
	disableAccountForFailover("model-blocked", "blocked-model")

	var order []string
	failoverPingFn = func(_ *storedAuth, m string) error {
		order = append(order, m)
		if m == "blocked-model" {
			return errors.New("still blocked")
		}
		return nil
	}

	runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }, time.Now())
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
		failoverDisableModelKey: "qmodel_preview",
	})

	pinged := 0
	failoverPingFn = func(*storedAuth, string) error { pinged++; return nil }
	runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }, time.Now())
	if pinged != 1 {
		t.Fatalf("单账号单轮只应探活一次, pinged = %d, want 1", pinged)
	}
	if h.docOf("idx-q")["disabled"] != false {
		t.Fatal("探通后应解除禁用")
	}
}

// TestRunFailoverProbeWithoutFailedModelSkips 锁定缺失败模型时的处置。
//
// 2026-10-31 语义修正：旧实现「没有模型就跳过」看似安全，实际制造了
// 「禁用且永远探不了」的死角——2026-10-29 生产事故里主力快号就是这样被禁
// 8 小时零探活，只能人工启用。现在改为兜底出一个可回放模型（优先该账号的
// 可用模型），探通即解禁，探不通保持禁用。兜底同样受当日探活配额约束。
//
// 本测试锁定兜底行为本身，名字保留以便对照历史。
// 最近修改时间：2026-10-31；改动原因：语义从「跳过」改为「兜底回放」，避免禁用号失联。
func TestRunFailoverProbeWithoutFailedModelSkips(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	// 盘上有本机制标记但没有模型键：进程重启前写坏了或早期版本留下的形态。
	h.addAccount("nomodel", "idx-n", map[string]any{
		"disabled":         true,
		failoverDisableKey: true,
	})

	pingedModel := ""
	failoverPingFn = func(_ *storedAuth, m string) error { pingedModel = m; return nil }
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)
	if probed := runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }, now); probed != 1 {
		t.Fatalf("标记残缺必须兜底探活, probed = %d, want 1", probed)
	}
	if pingedModel == "" {
		t.Fatal("兜底探活必须带一个具体模型")
	}
	if h.docOf("idx-n")["disabled"] != false {
		t.Fatal("兜底模型探通后应解除禁用")
	}
	if got := failoverProbeFailsToday("nomodel", now); got != 0 {
		t.Fatalf("探通不得消耗当日失败配额, fails = %d", got)
	}
}

// TestRunFailoverProbeFallbackKeepsDisabledOnFailure 锁定兜底探活的失败侧：
// 探不通就保持禁用，不得因为「探错了模型」把号放回去。
// 最近修改时间：2026-10-31；改动原因：新增，防止兜底探活把故障号误放回。
func TestRunFailoverProbeFallbackKeepsDisabledOnFailure(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("nomodel2", "idx-n2", map[string]any{
		"disabled":         true,
		failoverDisableKey: true,
	})

	origPing := failoverPingFn
	t.Cleanup(func() { failoverPingFn = origPing })
	failoverPingFn = func(*storedAuth, string) error { return errors.New("qoderwork fake probe failed") }

	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)
	if probed := runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }, now); probed != 1 {
		t.Fatalf("应兜底探测 1 个禁用账号, got %d", probed)
	}
	if h.docOf("idx-n2")["disabled"] != true {
		t.Fatal("兜底探活失败必须保持禁用")
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
	runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }, time.Now())
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
	if failoverProbeFailLimit != 10 {
		t.Fatalf("当日探活上限 = %d, want 10（用户裁定）", failoverProbeFailLimit)
	}
}

// TestFailoverProbeDailyLimitStopsProbingUntilMidnight 锁定 Qoder 专属规则：
// 单账号当日累计探活失败达到 10 次后不再探测，本地日期翻篇才重新开始。
func TestFailoverProbeDailyLimitStopsProbingUntilMidnight(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("banned", "idx-ban", nil)
	disableAccountForFailover("banned", "qmodel_preview")

	// 所有模型都探不通。
	failoverPingFn = func(*storedAuth, string) error { return errors.New("still blocked") }
	resolve := func(string) *storedAuth { return &storedAuth{} }
	day1 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)

	// 前 9 次：每轮都真的探测，计数递增。
	for i := 1; i <= failoverProbeFailLimit-1; i++ {
		if probed := runFailoverProbeTick(resolve, day1); probed != 1 {
			t.Fatalf("第 %d 轮应探测, probed = %d", i, probed)
		}
		if got := failoverProbeFailsToday("banned", day1); got != i {
			t.Fatalf("第 %d 轮后当日失败计数 = %d, want %d", i, got, i)
		}
	}
	// 第 10 次：仍探测并记账，达到上限。
	if probed := runFailoverProbeTick(resolve, day1); probed != 1 {
		t.Fatalf("第 %d 轮应探测, probed = %d", failoverProbeFailLimit, probed)
	}
	if got := failoverProbeFailsToday("banned", day1); got != failoverProbeFailLimit {
		t.Fatalf("达到上限后计数 = %d, want %d", got, failoverProbeFailLimit)
	}
	// 第 11 次起：同一本地日内必须跳过（probed=0 表示没有发出任何探活请求）。
	if probed := runFailoverProbeTick(resolve, day1.Add(time.Hour)); probed != 0 {
		t.Fatalf("达到当日上限后不应再探测, probed = %d", probed)
	}
	// 账号仍保持禁用：跳过探测不等于解除禁用。
	if h.docOf("idx-ban")["disabled"] != true {
		t.Fatal("当日放弃探活后账号必须保持禁用")
	}
	// 本地日期翻篇：计数归零，重新开始探测。
	day2 := time.Date(2026, 10, 23, 0, 1, 0, 0, time.Local)
	if got := failoverProbeFailsToday("banned", day2); got != 0 {
		t.Fatalf("跨天后计数应归零, got %d", got)
	}
	if probed := runFailoverProbeTick(resolve, day2); probed != 1 {
		t.Fatalf("跨天后应恢复探测, probed = %d", probed)
	}
}

// TestFailoverProbeDailyLimitResetsOnRecovery 锁定恢复成功后配额清零：
// 账号探通即恢复正常，当日失败计数不应残留下来影响下一次故障的处理。
func TestFailoverProbeDailyLimitResetsOnRecovery(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("flaky", "idx-fl", nil)
	disableAccountForFailover("flaky", "qmodel_preview")

	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)
	failoverPingFn = func(*storedAuth, string) error { return errors.New("down") }
	runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }, now)
	if got := failoverProbeFailsToday("flaky", now); got != 1 {
		t.Fatalf("前置条件不成立：当日失败计数 = %d, want 1", got)
	}

	// 上游恢复：同一天内探通，账号被解除禁用。
	failoverPingFn = func(*storedAuth, string) error { return nil }
	runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }, now)
	if h.docOf("idx-fl")["disabled"] != false {
		t.Fatal("探通后应解除禁用")
	}
	if got := failoverProbeFailsToday("flaky", now); got != 0 {
		t.Fatalf("恢复后当日失败计数应清零, got %d", got)
	}
}

// TestParseFailoverAuthDocFromDiskShape 锁定盘上文档解析：三个顶层键 + 别人的
// 标记都要被正确识别，解析失败一律走保守分支（不认领、不覆盖）。
func TestParseFailoverAuthDocFromDiskShape(t *testing.T) {
	doc := parseFailoverAuthDoc([]byte(`{
		"disabled":true,"failover_disable":true,
		"failover_disable_model":" auto ",
		"manual_disable":true,
		"daily_cooling_until":"2026-10-23T00:00:00Z"
	}`))
	if !doc.disabled || !doc.failoverDisable || !doc.manualDisable || !doc.dailyCooling {
		t.Fatalf("布尔/标记解析错误: %+v", doc)
	}
	if doc.failoverModel != "auto" {
		t.Fatalf("模型名未去空白: %q", doc.failoverModel)
	}
	bad := parseFailoverAuthDoc([]byte(`not json`))
	if bad.disabled || bad.failoverDisable || bad.failoverModel != "" || bad.manualDisable || bad.dailyCooling {
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
	h.addAccount("qw-r", "idx-r", nil)
	disableAccountForFailover("qw-r", "qmodel_preview")
	if !isAccountCoolingDown("qw-r") {
		t.Fatal("前置条件不成立：禁用后应被本地排除")
	}

	// 模拟外部启用：盘上改回 enabled，插件内存毫不知情。
	h.docOf("idx-r")["disabled"] = false
	if cleared := reconcileFailoverLocalSet(); cleared == 0 {
		t.Fatal("外部已启用的号必须被对账清除")
	}
	if isAccountCoolingDown("qw-r") {
		t.Fatal("对账后该号应重新可路由")
	}
	if got := failoverProbeModelFor("qw-r"); got != "" {
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
	if !isAccountCoolingDown("ghost-acc") {
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
	if !isAccountCoolingDown("ghost-acc") {
		t.Fatal("列表不可用不得影响排除状态")
	}
}

// TestReconcileMatchesDiskFileNameForm 锁定键形态对齐：记账时手里可能是裸 UID，
// 宿主列表给的是 qoderwork-<uid>.json，对账必须能跨形态命中，否则外部启用无效。
func TestReconcileMatchesDiskFileNameForm(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	uid := "29d35850-2d06-4bd5-9f8f-0c26d9e76421"
	h.addAccount(uid, "idx-f", nil)
	// 本地键写成裸 UID（markFailoverDisabledLocal 的归一化形态）。
	markFailoverDisabledLocal(uid, "")
	if !isAccountCoolingDown("qoderwork-" + uid + ".json") {
		t.Fatal("前置条件不成立：文件名形态应命中本地排除")
	}
	h.docOf("idx-f")["disabled"] = false
	if reconcileFailoverLocalSet() == 0 {
		t.Fatal("列表条目为 qoderwork-<uid>.json 时也必须匹配到裸 UID 键")
	}
	if isAccountCoolingDown(uid) {
		t.Fatal("对账后裸 UID 键应被清除")
	}
}

// TestConcurrentDisableFromAliasIDsWritesOnce 锁定 workbuddy 端到端实测到的缺陷
// 在 qoderwork 上的同构形态：同一次失败会被多个记账入口同时上报（宿主 auth ID
// 形态与账号 UID 形态，qoderwork 还多一条 reconcileByUID），它们解析到同一个
// 物理文档。禁用写盘必须只发生一次，否则每个入口都触发一次宿主 watcher 重载。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-22；改动原因：新增，覆盖文档读改写的并发串行化。
func TestConcurrentDisableFromAliasIDsWritesOnce(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("qoderwork-e2etest01.json", "idx-alias", nil)
	// 第二个身份别名指向同一物理文档：复刻 usedAuthID（宿主文件名 ID）与
	// curAuthUID（账号 UID）两条记账入口。
	h.mu.Lock()
	h.index["e2etest01"] = "idx-alias"
	h.mu.Unlock()

	const goroutines = 24
	var wg sync.WaitGroup
	wg.Add(goroutines)
	ids := []string{"qoderwork-e2etest01.json", "e2etest01"}
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

// decodeQoderworkParseAuth 解出 auth.parse 的信封结果。
// [参数] t：测试上下文；raw：handleParseAuth 返回的信封字节。
// [返回] 解析后的响应。
// 最近修改时间：2026-10-22；改动原因：新增，供标记回传用例复用。
func decodeQoderworkParseAuth(t *testing.T, raw []byte) pluginapi.AuthParseResponse {
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
		`"auth":{"accessToken":"dt-at","refreshToken":"drt-rt","expiresAt":9999999999},` +
		`"account":{"uid":"` + uid + `","nickname":"n"}}`)

	body, _ := json.Marshal(pluginapi.AuthParseRequest{
		Provider: providerName,
		FileName: "qoderwork-" + uid + ".json",
		RawJSON:  raw,
	})
	out, err := handleParseAuth(body)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeQoderworkParseAuth(t, out)
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

// TestRefreshAuthKeepsDisabledAndMarkers 锁定第二个同源风险：宿主挑选刷新候选
// 时不看 Disabled，插件若在刷新回传里固定 disabled:false，禁用中的号会被一次
// 后台 token 刷新静默解封、完全绕开 10 分钟禁用探活。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-22；改动原因：新增，覆盖刷新路径不解封禁用号。
func TestRefreshAuthKeepsDisabledAndMarkers(t *testing.T) {
	sa := &storedAuth{
		Auth:    storedTokens{AccessToken: "new-dt-at", RefreshToken: "drt-rt"},
		Account: storedAccount{UID: "u-refresh", Nickname: "n"},
	}
	hostMeta := map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "fake-model-a",
		"note":                  "已禁用",
	}

	ad := toAuthDataForRefresh(sa, hostMeta)
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
			`"auth":{"accessToken":"dt-at"},"account":{"uid":"u-manual"}}`},
		{"健康号", `{"type":"` + providerName + `","disabled":false,` +
			`"auth":{"accessToken":"dt-at"},"account":{"uid":"u-ok"}}`},
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
			resp := decodeQoderworkParseAuth(t, out)
			if _, ok := resp.Auth.Metadata[failoverDisableKey]; ok {
				t.Fatalf("非本机制禁用的号被塞入 %s，探活会误接管其恢复", failoverDisableKey)
			}
		})
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
		`"auth":{"accessToken":"dt-at","refreshToken":"drt-rt","expiresAt":9999999999},` +
		`"account":{"uid":"` + uid + `","nickname":"n"}}`)

	body, _ := json.Marshal(pluginapi.AuthParseRequest{Provider: providerName, RawJSON: raw})
	out, err := handleParseAuth(body)
	if err != nil {
		t.Fatal(err)
	}
	ad := decodeQoderworkParseAuth(t, out).Auth

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

// TestWhitelistRebuildSitesCarryFailoverMarkers 是标记搬运的调用点审计闸门。
//
// 漏搬运标记是本方案最高频、也最难发现的事故源——代码照常编译、测试照常绿，
// 只是某个禁用号从此再也进不了探活候选（静默遗忘，只能人工启用）。
// qoderwork 有三处白名单重建会作用于既有账号（deleteAuth 兜底、syncAuthNote、
// reenableAuth），靠人工记不住，所以在这里机械扫描每个落点。
//
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-22；改动原因：新增，防止重建路径静默抹掉禁用标记。
func TestWhitelistRebuildSitesCarryFailoverMarkers(t *testing.T) {
	// 允许不搬运的落点：恢复启用（只对 exhausted_disable 可达，与本机制天然互斥，
	// 号重新可用后不该再被探活认领）。
	allowed := map[string]string{
		"reenableAuth":    "恢复启用，本就应清除禁用标记",
		"handleImportPAT": "导入新凭据，盘上无既有文档",
	}

	carryMarker := "putFailoverMarkersIntoExtra"
	type site struct {
		file   string
		fn     string
		hasCar bool
	}
	var sites []site

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, rerr := os.ReadFile(name)
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		fnName := ""
		fnLines := []string(nil)
		flush := func() {
			if fnName == "" {
				return
			}
			hasCall, hasCar := false, false
			for _, ln := range fnLines {
				trimmed := strings.TrimSpace(ln)
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.Contains(ln, "buildAuthFileJSON(") {
					hasCall = true
				}
				if strings.Contains(ln, carryMarker) {
					hasCar = true
				}
			}
			if hasCall && !strings.Contains(fnName, "buildAuthFileJSON") {
				sites = append(sites, site{file: name, fn: fnName, hasCar: hasCar})
			}
		}
		for _, ln := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(ln, "func ") {
				flush()
				fnName = funcNameOf(ln)
				fnLines = nil
			}
			if fnName != "" {
				fnLines = append(fnLines, ln)
			}
		}
		flush()
	}

	if len(sites) == 0 {
		t.Fatal("审计未找到任何 buildAuthFileJSON 调用点：扫描逻辑已失效，必须修扫描而不是删断言")
	}
	for _, s := range sites {
		if s.hasCar {
			continue
		}
		if _, ok := allowed[s.fn]; ok {
			continue
		}
		t.Errorf("%s 的 %s 走白名单重建但未搬运 failover 标记：禁用号会被探活循环静默遗忘",
			s.file, s.fn)
	}
}

// funcNameOf 从一行顶层 func 声明里取出函数名。
// [参数] line：以 "func " 开头的源码行。
// [返回] 函数名；接收者方法返回方法名，解析失败返回空串。
// 最近修改时间：2026-10-22；改动原因：新增，供调用点审计归属函数。
func funcNameOf(line string) string {
	s := strings.TrimSpace(strings.TrimPrefix(line, "func "))
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if fields := strings.Fields(s); len(fields) > 0 {
		return fields[len(fields)-1]
	}
	return ""
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

// TestFailoverDisable_UsesCPAAPIWhenConfigured 锁定：配了密钥就走宿主 API，不写盘。
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
		t.Fatalf("走 API 时不应写盘，实际 %d", h.writeCount())
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
