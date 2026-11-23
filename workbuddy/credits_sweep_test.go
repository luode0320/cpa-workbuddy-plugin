package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// withTempSnapshotDir 把 sidecar 指向临时目录，避免测试写到真实安装根目录。
// 通过切换工作目录实现：creditSnapshotPath 依赖 cliProxyRootFromWorkingDir。
func withTempSnapshotDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	// 造一个包含 plugins 子目录的结构，让根目录推导成功。
	if err := os.MkdirAll(filepath.Join(base, "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(base, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(orig)
		resetCreditSnapshotForTest()
	})
	resetCreditSnapshotForTest()
	return base
}

// TestCreditTierForBoundaries 锁定三档分级的四个边界：19/20/49/50。
// 边界值是这套机制最容易写反的部分（严格小于 vs 小于等于），必须逐点钉死。
func TestCreditTierForBoundaries(t *testing.T) {
	const disableBelow = 20
	const preserveBelow = 50

	cases := []struct {
		remain int64
		want   creditTier
		desc   string
	}{
		{19, creditTierDisabled, "低于禁用线"},
		{20, creditTierPreserve, "恰好等于禁用线不算低于，落入保活档"},
		{49, creditTierPreserve, "低于保活线"},
		{50, creditTierNormal, "恰好等于保活线属正常档"},
		{0, creditTierDisabled, "零积分"},
		{-1, creditTierNormal, "未知积分不降级"},
	}
	for _, c := range cases {
		if got := creditTierFor(c.remain, disableBelow, preserveBelow); got != c.want {
			t.Fatalf("creditTierFor(%d) = %v, want %v (%s)", c.remain, got, c.want, c.desc)
		}
	}
}

// TestCreditTierForDisabledWhenPreserveOff 锁定显式 0 关闭保活降级后，
// 高于禁用线的号全部回到正常档，不再被打保活标。
func TestCreditTierForDisabledWhenPreserveOff(t *testing.T) {
	if got := creditTierFor(200, 20, 0); got != creditTierNormal {
		t.Fatalf("保活线为 0 时 tier(200) = %v, want normal", got)
	}
	if got := creditTierFor(10, 20, 0); got != creditTierDisabled {
		t.Fatalf("保活线为 0 不应影响禁用线，tier(10) = %v, want disabled", got)
	}
}

// TestCreditSnapshotPersistAndReload 锁定落盘后重启可读回：写入 → 写盘 →
// 清空内存 → 重新载入，必须拿到与写入时一致的积分与档位。
func TestCreditSnapshotPersistAndReload(t *testing.T) {
	base := withTempSnapshotDir(t)
	restoreMin := setMinRoutableCredits(50)
	defer restoreMin()
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()

	recordCreditSnapshot("wb-persist", &creditsSummary{TotalRemain: 30, TotalUsed: 270, TotalSize: 300}, "nick", "cn")
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist: %v", err)
	}

	path := filepath.Join(base, "data", defaultCreditsSnapshotName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sidecar 未落盘到 %s: %v", path, err)
	}

	// 模拟进程重启：清空内存后重新载入。
	resetCreditSnapshotForTest()
	entry, ok := creditSnapshotGet("wb-persist")
	if !ok {
		t.Fatal("重启后未能从 sidecar 读回快照")
	}
	if entry.Remain != 30 {
		t.Fatalf("reload remain = %d, want 30", entry.Remain)
	}
	if entry.Tier != creditTierPreserve {
		t.Fatalf("reload tier = %v, want preserve", entry.Tier)
	}
	if entry.Credits == nil || entry.Credits.TotalUsed != 270 {
		t.Fatalf("reload 完整积分明细丢失: %+v", entry.Credits)
	}
}

// TestCreditSnapshotCorruptFileDegrades 锁定损坏快照不 panic、不阻塞：
// 读到无法解析的内容时按空快照处理，下一次扫描会重新填满。
func TestCreditSnapshotCorruptFileDegrades(t *testing.T) {
	base := withTempSnapshotDir(t)
	path := filepath.Join(base, "data", defaultCreditsSnapshotName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	resetCreditSnapshotForTest()
	if _, ok := creditSnapshotGet("anything"); ok {
		t.Fatal("损坏文件必须降级为空快照")
	}
	// 降级后仍应能正常写入与落盘，不被损坏文件永久拖住。
	recordCreditSnapshot("wb-after-corrupt", &creditsSummary{TotalRemain: 500}, "", "cn")
	if err := persistCreditSnapshot(); err != nil {
		t.Fatalf("persist after corrupt: %v", err)
	}
}

// TestRunCreditSweepSerialAndPersists 锁定扫描的核心契约：串行遍历全部账号、
// 单号失败不影响其余、轮末落盘一次。
func TestRunCreditSweepSerialAndPersists(t *testing.T) {
	withTempSnapshotDir(t)
	restoreMin := setMinRoutableCredits(50)
	defer restoreMin()
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()

	origList := creditsSweepListFn
	origGet := creditsSweepGetFn
	origFetch := creditsSweepFetchFn
	origPersist := creditsSweepPersistFn
	origGrade := creditsSweepGradeFn
	origSleep := creditsSweepSleepFn
	t.Cleanup(func() {
		creditsSweepListFn = origList
		creditsSweepGetFn = origGet
		creditsSweepFetchFn = origFetch
		creditsSweepPersistFn = origPersist
		creditsSweepGradeFn = origGrade
		creditsSweepSleepFn = origSleep
	})
	creditsSweepSleepFn = func(time.Duration) {}

	creditsSweepListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{
			{ID: "a1", AuthIndex: "i1"},
			{ID: "a2", AuthIndex: "i2"},
			{ID: "a3", AuthIndex: "i3"},
		}, nil
	}
	creditsSweepGetFn = func(idx string) (*storedAuth, error) {
		return &storedAuth{}, nil
	}
	creditsSweepFetchFn = func(_ *storedAuth) (*creditsSummary, error) {
		// 用调用顺序证明串行：并发执行时这条断言会失败。
		creditsSweepFetchFn = func(_ *storedAuth) (*creditsSummary, error) { return nil, os.ErrDeadlineExceeded }
		return &creditsSummary{TotalRemain: 500}, nil
	}
	var graded []string
	creditsSweepGradeFn = func(_, authID string, _ *storedAuth, _ *creditsSummary) creditTier {
		graded = append(graded, authID)
		return creditTierNormal
	}
	persistCalls := 0
	creditsSweepPersistFn = func() error {
		persistCalls++
		return nil
	}

	report := runCreditSweep()
	if report == nil {
		t.Fatal("sweep 应返回统计")
	}
	if report.Total != 3 {
		t.Fatalf("total = %d, want 3", report.Total)
	}
	if report.Refreshed != 1 || report.Failed != 2 {
		t.Fatalf("refreshed=%d failed=%d, want 1/2（首号成功、其余失败）", report.Refreshed, report.Failed)
	}
	if len(graded) != 1 || graded[0] != "a1" {
		t.Fatalf("只有成功的账号应进入分级检查, got %v", graded)
	}
	if persistCalls != 1 {
		t.Fatalf("整轮必须只在末尾落盘一次, got %d", persistCalls)
	}
}

// TestRunCreditSweepPersistsOnceForMultipleAccounts 与上一个用例互补：上一个
// 只有首个账号成功，逐账号落盘与轮末落盘的次数同为 1，挡不住改成中途落盘。
// 这里让三个账号全部成功，一旦实现改为每号落盘，次数会变成 3 而被抓住。
func TestRunCreditSweepPersistsOnceForMultipleAccounts(t *testing.T) {
	withTempSnapshotDir(t)
	restoreMin := setMinRoutableCredits(50)
	defer restoreMin()
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()

	origList := creditsSweepListFn
	origGet := creditsSweepGetFn
	origFetch := creditsSweepFetchFn
	origPersist := creditsSweepPersistFn
	origGrade := creditsSweepGradeFn
	origSleep := creditsSweepSleepFn
	t.Cleanup(func() {
		creditsSweepListFn = origList
		creditsSweepGetFn = origGet
		creditsSweepFetchFn = origFetch
		creditsSweepPersistFn = origPersist
		creditsSweepGradeFn = origGrade
		creditsSweepSleepFn = origSleep
	})
	creditsSweepSleepFn = func(time.Duration) {}
	creditsSweepListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{
			{ID: "m1", AuthIndex: "i1"},
			{ID: "m2", AuthIndex: "i2"},
			{ID: "m3", AuthIndex: "i3"},
		}, nil
	}
	creditsSweepGetFn = func(string) (*storedAuth, error) { return &storedAuth{}, nil }
	creditsSweepFetchFn = func(*storedAuth) (*creditsSummary, error) {
		return &creditsSummary{TotalRemain: 500, TotalSize: 500}, nil
	}
	creditsSweepGradeFn = func(_, _ string, _ *storedAuth, _ *creditsSummary) creditTier {
		return creditTierNormal
	}
	persistCalls := 0
	creditsSweepPersistFn = func() error {
		persistCalls++
		return nil
	}

	report := runCreditSweep()
	if report == nil {
		t.Fatal("sweep 应返回统计")
	}
	if report.Refreshed != 3 {
		t.Fatalf("refreshed = %d, want 3", report.Refreshed)
	}
	if persistCalls != 1 {
		t.Fatalf("多账号全部成功时仍须只在轮末落盘一次, got %d", persistCalls)
	}
}

// TestGradeCreditsDisablesBelowLine 锁定「低于禁用线写盘停用」，
// 且写入的是本机制自己的标记，不碰别人的。
func TestGradeCreditsDisablesBelowLine(t *testing.T) {
	resetFailover(t)
	resetCreditProbeForTest()
	h := newFailoverFakeHost(t)
	h.addAccount("wb-low", "idx-low", nil)
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()

	sa := &storedAuth{}
	cr := &creditsSummary{TotalRemain: 19, TotalUsed: 81, TotalSize: 100}
	tier := gradeCreditsForAuth("idx-low", "wb-low", sa, cr)
	if tier != creditTierDisabled {
		t.Fatalf("tier = %v, want disabled", tier)
	}
	doc := h.docOf("idx-low")
	if doc["disabled"] != true {
		t.Fatalf("disabled = %v, want true", doc["disabled"])
	}
	if doc[creditDisableKey] != true {
		t.Fatalf("%s = %v, want true", creditDisableKey, doc[creditDisableKey])
	}
	if doc[creditDisableBelowKey] == nil {
		t.Fatalf("必须记录禁用时的阈值，便于人工诊断")
	}
	if h.writeCount() != 1 {
		t.Fatalf("写盘次数 = %d, want 1", h.writeCount())
	}
}

// TestGradeCreditsPreserveDoesNotDisable 锁定 20~50 只打保活标、绝不写 disabled。
func TestGradeCreditsPreserveDoesNotDisable(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-mid", "idx-mid", nil)
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()
	restoreMin := setMinRoutableCredits(50)
	defer restoreMin()

	before := h.writeCount()
	tier := gradeCreditsForAuth("idx-mid", "wb-mid", &storedAuth{}, &creditsSummary{TotalRemain: 30})
	if tier != creditTierPreserve {
		t.Fatalf("tier = %v, want preserve", tier)
	}
	if h.writeCount() != before {
		t.Fatalf("保活档不得写盘, writes=%d want %d", h.writeCount(), before)
	}
	if doc := h.docOf("idx-mid"); doc["disabled"] == true {
		t.Fatal("保活档绝不能把账号写死")
	}
}

// TestGradeCreditsReenableRequiresProbe 锁定解禁双条件：积分回到禁用线以上
// 但探活失败时，必须保持禁用；探活成功后同一函数才解除。
func TestGradeCreditsRequiresProbeBeforeReenable(t *testing.T) {
	resetFailover(t)
	resetCreditProbeForTest()
	h := newFailoverFakeHost(t)
	h.addAccount("wb-back", "idx-back", nil)
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()

	// 先禁用。
	gradeCreditsForAuth("idx-back", "wb-back", &storedAuth{}, &creditsSummary{TotalRemain: 10})
	if h.docOf("idx-back")["disabled"] != true {
		t.Fatal("前置条件不成立：应先被禁用")
	}

	origPing := creditDisablePingFn
	t.Cleanup(func() { creditDisablePingFn = origPing })
	creditDisablePingFn = func(*storedAuth, string) error { return os.ErrDeadlineExceeded }

	// 积分已回到线上，但探活失败 → 保持禁用。
	gradeCreditsForAuth("idx-back", "wb-back", &storedAuth{}, &creditsSummary{TotalRemain: 500})
	if h.docOf("idx-back")["disabled"] != true {
		t.Fatal("探活失败时绝不能解禁")
	}

	// 换一轮探活成功 → 解禁。
	resetCreditProbeForTest()
	creditDisablePingFn = func(*storedAuth, string) error { return nil }
	gradeCreditsForAuth("idx-back", "wb-back", &storedAuth{}, &creditsSummary{TotalRemain: 500})
	doc := h.docOf("idx-back")
	if doc["disabled"] != false {
		t.Fatalf("探活成功后应解禁, disabled = %v", doc["disabled"])
	}
	if doc[creditDisableKey] != nil {
		t.Fatal("解禁必须清掉本机制标记")
	}
}

// TestGradeCreditsYieldsToOthers 锁定「别人禁的号本机制不碰」。
// 这里刻意让账号同时带 low_credit_disable 和对方的标记：只带对方标记时，
// reenableForLowCredits 内部那层归属复核就能挡住写入，外层短路即使被删掉
// 测试也依然通过（探活被白跑一次但结果不变），测不出真正的护栏。
func TestGradeCreditsYieldsToOthers(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]any
	}{
		{"manual_disable", map[string]any{"manual_disable": true}},
		{"failover_disable", map[string]any{failoverDisableKey: true, failoverDisableModelKey: "glm-4.6"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetFailover(t)
			resetCreditProbeForTest()
			h := newFailoverFakeHost(t)
			extra := map[string]any{"disabled": true, creditDisableKey: true, creditDisableBelowKey: 20}
			for k, v := range c.extra {
				extra[k] = v
			}
			h.addAccount("wb-other", "idx-other", extra)
			restoreDisable := setLowCreditDisableBelow(20)
			defer restoreDisable()

			origPing := creditDisablePingFn
			t.Cleanup(func() { creditDisablePingFn = origPing })
			probes := 0
			creditDisablePingFn = func(*storedAuth, string) error {
				probes++
				return nil
			}

			before := h.writeCount()
			gradeCreditsForAuth("idx-other", "wb-other", &storedAuth{}, &creditsSummary{TotalRemain: 500})
			if h.docOf("idx-other")["disabled"] != true {
				t.Fatalf("%s 的账号不得被本机制解禁", c.name)
			}
			if h.writeCount() != before {
				t.Fatalf("%s 的账号不得被本机制改写, writes=%d want %d", c.name, h.writeCount(), before)
			}
			// 归属判定必须发生在探活之前：命中别人的标记就该直接返回，
			// 不该为一个注定不会解禁的号去打上游。
			if probes != 0 {
				t.Fatalf("%s 的账号不该触发探活, probes=%d", c.name, probes)
			}
		})
	}
}

// TestLowCreditMarkersSurviveDocRebuild 锁定标记在白名单重建后仍然存活。
// 丢了它账号会永远卡在禁用态（分级恢复认不出「这是我禁的」）。
func TestLowCreditMarkersSurviveDocRebuild(t *testing.T) {
	raw := []byte(`{"disabled":true,"low_credit_disable":true,"low_credit_disable_below":100,"note":"x"}`)
	extra := putLowCreditMarkersIntoExtra(nil, raw)
	if extra[creditDisableKey] != true {
		t.Fatalf("%s 未搬运, extra=%v", creditDisableKey, extra)
	}
	if extra[creditDisableBelowKey] == nil {
		t.Fatalf("%s 未搬运", creditDisableBelowKey)
	}

	// 重建后的文档必须仍然带标记。
	rebuilt, err := buildAuthFileJSON(&storedAuth{}, true, "note", extra)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(rebuilt, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc[creditDisableKey] != true {
		t.Fatal("白名单重建后标记丢失，账号会永久卡在禁用态")
	}
}

// TestCreditDisableLineNotAbovePreserveLine 锁定阈值配反时的归一：
// 禁用线高于保活线会把「高于保活线却被禁用」这种自相矛盾状态放出来。
func TestCreditDisableLineNotAbovePreserveLine(t *testing.T) {
	restoreMin := setMinRoutableCredits(defaultMinRoutableCredits)
	defer restoreMin()
	restoreDisable := setLowCreditDisableBelow(defaultLowCreditDisableBelow)
	defer restoreDisable()
	restoreMode := setSchedulerMode(schedulerModeSession)
	defer restoreMode()

	cfg, _ := json.Marshal(map[string]any{
		"config_yaml": []byte("min_routable_credits: 400\nlow_credit_disable_below: 900\n"),
	})
	configure(cfg)
	if got, limit := loadedLowCreditDisableBelow(), loadedMinRoutableCredits(); got > limit {
		t.Fatalf("禁用线 %d 不得高于保活线 %d", got, limit)
	}
}

// newBillingStub 起一个假上游，按路径返回不同响应；用于驱动真实
// fetchUserResource / fetchCheckinStatus，而不是替换这些函数本身。
// [参数] t：测试句柄；handler：路径 → 响应体。
// [返回] 无（内部注册 setBillingBase 还原）。
func newBillingStub(t *testing.T, handler func(path string) (int, string)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = body
		status, resp := handler(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	restore := setBillingBase(srv.URL)
	t.Cleanup(restore)
}

// creditsEnvelope 把积分余量包成上游 get-user-resource 的真实信封形状。
func creditsEnvelope(remain, size int64) string {
	return fmt.Sprintf(`{"code":0,"msg":"OK","data":{"Response":{"Data":{"TotalCount":1,"TotalDosage":%d,"Accounts":[{"PackageName":"p","CycleCapacityRemain":%d,"CycleCapacitySize":%d}]}}}}`, size, remain, size)
}

// TestDoFetchOneRecordsSnapshotAndGrades 锁定需求第 3 条：面板手动刷新
// 不能只更新面板数字，必须同时写快照并跑三档检查（该禁就禁）。
func TestDoFetchOneRecordsSnapshotAndGrades(t *testing.T) {
	withTempSnapshotDir(t)
	resetFailover(t)
	resetCreditProbeForTest()
	h := newFailoverFakeHost(t)
	h.addAccount("wb-manual", "idx-manual", nil)
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()
	restoreMin := setMinRoutableCredits(50)
	defer restoreMin()

	origGet := hostAuthGetFn
	t.Cleanup(func() { hostAuthGetFn = origGet })
	hostAuthGetFn = func(string) (*storedAuth, error) { return &storedAuth{}, nil }

	// 积分 10 < 禁用线 20：刷新本身成功，但必须顺手把号停用。
	newBillingStub(t, func(path string) (int, string) {
		switch path {
		case "/v2/billing/meter/get-user-resource":
			return 200, creditsEnvelope(10, 100)
		default:
			return 200, `{"code":0,"msg":"OK","data":{}}`
		}
	})
	origPing := triggerActivePingFn
	t.Cleanup(func() { triggerActivePingFn = origPing })
	triggerActivePingFn = func(string, string, *storedAuth) error { return nil }

	if err := doFetchOne("idx-manual", "wb-manual"); err != nil {
		t.Fatalf("doFetchOne: %v", err)
	}
	if entry, ok := creditSnapshotGet("wb-manual"); !ok || entry.Remain != 10 {
		t.Fatalf("手动刷新必须写入快照, got %+v ok=%v", entry, ok)
	}
	if h.docOf("idx-manual")["disabled"] != true {
		t.Fatal("手动刷新拉到低于禁用线的积分时，必须立刻停用账号")
	}
	// 快照必须在函数返回前落盘，否则进程重启后回到「积分未知」。
	if raw, err := os.ReadFile(creditSnapshotPath()); err != nil || len(raw) == 0 {
		t.Fatalf("手动刷新必须落盘 sidecar, err=%v", err)
	}
}

// TestDoFetchOneHealthyCreditsKeepsAccount 反向对照：积分健康时刷新不得
// 误伤账号——防止上一条用例因为「永远禁用」这种写法而假通过。
func TestDoFetchOneHealthyCreditsKeepsAccount(t *testing.T) {
	withTempSnapshotDir(t)
	resetFailover(t)
	resetCreditProbeForTest()
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ok", "idx-ok", nil)
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()
	restoreMin := setMinRoutableCredits(50)
	defer restoreMin()

	origGet := hostAuthGetFn
	t.Cleanup(func() { hostAuthGetFn = origGet })
	hostAuthGetFn = func(string) (*storedAuth, error) { return &storedAuth{}, nil }

	newBillingStub(t, func(path string) (int, string) {
		if path == "/v2/billing/meter/get-user-resource" {
			return 200, creditsEnvelope(800, 1000)
		}
		return 200, `{"code":0,"msg":"OK","data":{}}`
	})
	origPing := triggerActivePingFn
	t.Cleanup(func() { triggerActivePingFn = origPing })
	triggerActivePingFn = func(string, string, *storedAuth) error { return nil }

	if err := doFetchOne("idx-ok", "wb-ok"); err != nil {
		t.Fatalf("doFetchOne: %v", err)
	}
	entry, ok := creditSnapshotGet("wb-ok")
	if !ok || entry.Remain != 800 {
		t.Fatalf("健康账号也应写入快照, got %+v ok=%v", entry, ok)
	}
	if entry.Tier != creditTierNormal {
		t.Fatalf("tier = %v, want normal", entry.Tier)
	}
	if h.docOf("idx-ok")["disabled"] == true {
		t.Fatal("积分健康时不得停用账号")
	}
}

// TestProcessAutoCheckinGradesAfterCheckin 锁定需求第 4 条：签到会改变余额，
// 必须立刻重跑三档检查，而不是等下一轮小时扫描。
func TestProcessAutoCheckinGradesAfterCheckin(t *testing.T) {
	withTempSnapshotDir(t)
	resetFailover(t)
	resetCreditProbeForTest()
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ci", "idx-ci", nil)
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()
	restoreMin := setMinRoutableCredits(50)
	defer restoreMin()

	origGet := hostAuthGetFn
	t.Cleanup(func() { hostAuthGetFn = origGet })
	hostAuthGetFn = func(string) (*storedAuth, error) { return &storedAuth{}, nil }

	checkinDone := false
	newBillingStub(t, func(path string) (int, string) {
		switch path {
		case "/v2/billing/meter/checkin-activity-status", "/v2/billing/meter/checkin-status":
			// 首次未签到 → 触发签到；签到后再查返回已签到。
			done := "false"
			if checkinDone {
				done = "true"
			}
			return 200, fmt.Sprintf(`{"code":0,"msg":"OK","data":{"active":true,"today_checked_in":%s}}`, done)
		case "/v2/billing/meter/daily-checkin":
			checkinDone = true
			return 200, `{"code":0,"msg":"OK","data":{"success":true}}`
		case "/v2/billing/meter/get-user-resource":
			// 签到后余额仍低于禁用线：必须被分级检查抓住。
			return 200, creditsEnvelope(10, 100)
		default:
			return 200, `{"code":0,"msg":"OK","data":{}}`
		}
	})

	processAutoCheckinAccount(pluginapi.HostAuthFileEntry{ID: "wb-ci", AuthIndex: "idx-ci"}, true, false)

	if !checkinDone {
		t.Fatal("前置条件不成立：签到请求未被发出")
	}
	entry, ok := creditSnapshotGet("wb-ci")
	if !ok || entry.Remain != 10 {
		t.Fatalf("签到后必须刷新快照, got %+v ok=%v", entry, ok)
	}
	if h.docOf("idx-ci")["disabled"] != true {
		t.Fatal("签到后积分仍低于禁用线时，必须立刻停用")
	}
	if raw, err := os.ReadFile(creditSnapshotPath()); err != nil || len(raw) == 0 {
		t.Fatalf("签到后必须落盘 sidecar, err=%v", err)
	}
}

// TestHandleCreditsQuerySingleAccountWritesSnapshot 锁定面板单卡「刷新」。
//
// 该按钮走 GET /credits?auth_index=<idx>，与批量刷新 / 签到 / 小时扫描是
// 四条独立入口。用户实测：新加账号点「刷新」后，卡片仍显示「加载中…」。
// 原因就是这条路径只更新 accountCache，从不写 sidecar；面板重新加载走轻载
// 分支读快照，查不到该账号便回落为「未知」。因此本用例断言刷新必须落盘。
func TestHandleCreditsQuerySingleAccountWritesSnapshot(t *testing.T) {
	withTempSnapshotDir(t)
	resetFailover(t)
	resetCreditProbeForTest()
	h := newFailoverFakeHost(t)
	h.addAccount("wb-single", "idx-single", nil)
	restoreDisable := setLowCreditDisableBelow(20)
	defer restoreDisable()
	restoreMin := setMinRoutableCredits(50)
	defer restoreMin()

	// 宿主枚举与凭据读取走既有接缝，单卡路径必须同样可注入。
	origList := hostAuthListFn
	origGet := hostAuthGetFn
	t.Cleanup(func() {
		hostAuthListFn = origList
		hostAuthGetFn = origGet
	})
	hostAuthListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{{
			ID: "wb-single", AuthIndex: "idx-single", Name: "wb-single", Label: "新号",
		}}, nil
	}
	sa := &storedAuth{}
	sa.Account.UID = "wb-single"
	sa.Account.Nickname = "新号"
	sa.Auth.Domain = "www.codebuddy.cn"
	hostAuthGetFn = func(string) (*storedAuth, error) { return sa, nil }

	newBillingStub(t, func(path string) (int, string) {
		switch path {
		case "/v2/billing/meter/get-user-resource":
			return 200, creditsEnvelope(2100, 2100)
		default:
			return 200, `{"code":0,"msg":"OK","data":{}}`
		}
	})

	res := handleCreditsQuery(pluginapi.ManagementRequest{
		Method: "GET",
		Query:  url.Values{"auth_index": []string{"idx-single"}},
	})
	accounts, _ := res["accounts"].([]map[string]any)
	if len(accounts) != 1 {
		t.Fatalf("单卡查询应返回 1 条账号, got %#v", res)
	}
	if accounts[0]["credits"] == nil {
		t.Fatalf("单卡刷新必须返回积分, got %#v", accounts[0])
	}

	// 核心断言：快照必须收录该账号，否则面板重载会退回「加载中…」。
	entry, ok := creditSnapshotGet("wb-single")
	if !ok || entry.Remain != 2100 {
		t.Fatalf("单卡刷新必须写入快照, got %+v ok=%v", entry, ok)
	}
	if raw, err := os.ReadFile(creditSnapshotPath()); err != nil || len(raw) == 0 {
		t.Fatalf("单卡刷新必须落盘 sidecar, err=%v", err)
	}
}

// TestHandleCreditsQuerySingleAccountPersistsOnFailure 反向对照：上游拉取失败
// 时不得写快照——否则会用「未知」覆盖已有的真实积分。
func TestHandleCreditsQuerySingleAccountPersistsOnFailure(t *testing.T) {
	withTempSnapshotDir(t)
	resetFailover(t)
	resetCreditProbeForTest()
	h := newFailoverFakeHost(t)
	h.addAccount("wb-single2", "idx-single2", nil)

	origList := hostAuthListFn
	origGet := hostAuthGetFn
	t.Cleanup(func() {
		hostAuthListFn = origList
		hostAuthGetFn = origGet
	})
	hostAuthListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{{
			ID: "wb-single2", AuthIndex: "idx-single2", Name: "wb-single2",
		}}, nil
	}
	sa := &storedAuth{}
	sa.Account.UID = "wb-single2"
	sa.Auth.Domain = "www.codebuddy.cn"
	hostAuthGetFn = func(string) (*storedAuth, error) { return sa, nil }

	newBillingStub(t, func(path string) (int, string) {
		return 500, `{"code":500,"msg":"boom"}`
	})

	res := handleCreditsQuery(pluginapi.ManagementRequest{
		Method: "GET",
		Query:  url.Values{"auth_index": []string{"idx-single2"}},
	})
	accounts, _ := res["accounts"].([]map[string]any)
	if len(accounts) != 1 {
		t.Fatalf("单卡查询应返回 1 条账号, got %#v", res)
	}
	if _, ok := creditSnapshotGet("wb-single2"); ok {
		t.Fatal("上游拉取失败时不得写入快照")
	}
}
