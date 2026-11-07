// failover_sync_test.go 锁定「失败禁用 ↔ 手动启用」的联动契约。
//
// 2026-11-07 生产事故的四个成因分别对应下面四组用例：
//  1. 渠道级 11128 被当成账号故障，一次请求把 19 个账号逐个写盘禁用；
//  2. 手动启用后盘上 failover_disable 标记不清，已启用的号每 10 分钟被重新
//     「恢复」一次，resetCooldownState 顺带清零连续失败计数，后续真实失败
//     永远攒不到写盘门限（表现为「失败不再禁用」）；
//  3. 对账只清内存与 sidecar，不清盘上标记，导致第 2 条永不收敛；
//  4. 没有全池保底，禁用可以直接把池摘到 0 个可用账号。
//
// 这些用例都直接断言盘上顶层键与内存状态，不看函数返回值——返回值只表达
// 「本机制是否认领」，不能证明宿主与探活看到的真相。
package main

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// isolateFailoverSidecar 把 sidecar 指到临时文件，避免用例之间经由真实 data 目录
// 互相污染（禁用状态会跨用例残留，表现为单独跑绿、整套跑红）。
func isolateFailoverSidecar(t *testing.T) {
	t.Helper()
	restore := isolateFailoverStatePathForTest(filepath.Join(t.TempDir(), "workbuddy-failover.json"))
	t.Cleanup(restore)
}

// TestChannelBlockErrorDoesNotDisableAccount 锁定渠道级 11128 不罚任何账号。
//
// 生产实证报文：400 {"code":11128,"msg":"Illegal API invocation from an
// unapproved channel"}。同一个进程换任何账号都会拿到同样的拒绝，把它记成
// 账号故障就会把整池摘光（2026-11-07 19:49，19 个账号在 3 秒内全禁）。
func TestChannelBlockErrorDoesNotDisableAccount(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ch-1", "idx-ch-1", nil)
	h.addAccount("wb-ch-2", "idx-ch-2", nil)

	body := `{"code":11128,"msg":"Illegal API invocation from an unapproved channel","requestId":"x"}`
	if noteUpstreamBlockHit("wb-ch-1", "deepseek-v4.1-flash", http.StatusBadRequest, body) {
		t.Fatal("渠道级拦截不得按账号×模型记账")
	}
	if h.docOf("idx-ch-1")["disabled"] != false {
		t.Fatalf("渠道级拦截不得写盘禁用: %v", h.docOf("idx-ch-1"))
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("渠道级拦截写盘次数 = %d, want 0", got)
	}
	if count, _, ok := failoverStateSnapshot("wb-ch-1"); ok && count != 0 {
		t.Fatalf("渠道级拦截不得写冷却计数, count=%d", count)
	}
	if failoverStateIsDisabled("wb-ch-1") {
		t.Fatal("渠道级拦截不得写 sidecar 禁用条目")
	}

	// 对照：没有渠道文案的 11128 仍按账号×模型拦截处理（旧语义保留）。
	plain := `{"code":11128,"msg":"request illegal"}`
	if !noteUpstreamBlockHit("wb-ch-1", "deepseek-v4.1-flash", http.StatusBadRequest, plain) {
		t.Fatal("普通 11128 仍应记账")
	}
	if h.docOf("idx-ch-1")["disabled"] != true {
		t.Fatal("普通 11128 应写盘禁用该账号")
	}
}

// TestClassifyChannelBlockIsUpstreamBusy 锁定分类维度：渠道级拦截既不冷却也不禁用。
func TestClassifyChannelBlockIsUpstreamBusy(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   failureScope
	}{
		{
			name:   "400 渠道级拦截",
			status: http.StatusBadRequest,
			body:   `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`,
			want:   failureScopeUpstreamBusy,
		},
		{
			name:   "403 渠道级拦截同样命中",
			status: http.StatusForbidden,
			body:   `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`,
			want:   failureScopeUpstreamBusy,
		},
		{
			name:   "普通 11128 保持账号×模型",
			status: http.StatusBadRequest,
			body:   `{"code":11128,"msg":"request illegal"}`,
			want:   failureScopeAuthModel,
		},
		{
			name:   "11140 账号封禁不受影响",
			status: http.StatusForbidden,
			body:   `{"code":11140,"msg":"request illegal"}`,
			want:   failureScopeAccount,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyUpstreamFailure(tc.status, tc.body); got != tc.want {
				t.Fatalf("classify(%d, %s) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestFailoverProbeTargetsSkipsEnabledAccountWithStaleMarker 锁定探活候选判据：
// 盘上 disabled:false + 残留 failover_disable 的号不是候选（陈旧标记交给对账清理）。
func TestFailoverProbeTargetsSkipsEnabledAccountWithStaleMarker(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	// 陈旧标记：来自历史禁用，用户已手动启用。
	h.addAccount("stale", "idx-stale", map[string]any{
		"disabled":              false,
		failoverDisableKey:      true,
		failoverDisableModelKey: "deepseek-v4.1-flash",
		failoverDisableAtKey:    "2026-11-03T08:55:55Z",
	})
	// 真正被本机制禁用的号：必须仍是候选。
	h.addAccount("mine", "idx-mine", nil)
	h.addAccount("spare", "idx-spare", nil)
	if !disableAccountForFailover("mine", "glm-4.6") {
		t.Fatal("前置条件不成立：应能禁用 mine")
	}

	targets := failoverProbeTargets()
	if len(targets) != 1 {
		t.Fatalf("候选数 = %d, want 1: %+v", len(targets), targets)
	}
	if targets[0].authIndex != "idx-mine" {
		t.Fatalf("候选取错: %+v", targets[0])
	}
}

// TestFailoverProbeTargetsDeduplicatesAuthIndex 锁定一轮内同一 auth_index 只探一次。
// 生产日志里同一账号在一轮探活中被探两次，白烧一次上游额度。
func TestFailoverProbeTargetsDeduplicatesAuthIndex(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("dup", "idx-dup", nil)
	h.addAccount("spare", "idx-spare", nil)
	if !disableAccountForFailover("dup", "glm-4.6") {
		t.Fatal("前置条件不成立：应能禁用 dup")
	}

	origList := failoverListFn
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		files, err := origList()
		if err != nil {
			return nil, err
		}
		// 复刻宿主返回重复条目（同一 auth_index 以不同 ID 形态出现）。
		out := append([]pluginapi.HostAuthFileEntry{}, files...)
		for _, f := range files {
			if f.AuthIndex == "idx-dup" {
				f.ID = "workbuddy-dup.json"
				out = append(out, f)
			}
		}
		return out, nil
	}
	defer func() { failoverListFn = origList }()

	targets := failoverProbeTargets()
	if len(targets) != 1 || targets[0].authIndex != "idx-dup" {
		t.Fatalf("重复条目必须按 auth_index 去重, got %+v", targets)
	}
}

// TestReenableClearsDiskMarkersOnAPIPath 锁定恢复必须同时清盘上标记。
//
// 旧实现只在 API 失败时写盘，于是走 API 恢复成功后盘上永久留着
// `disabled:false + failover_disable:true`——探活候选每轮把它重新「恢复」一次。
func TestReenableClearsDiskMarkersOnAPIPath(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	calls, restore := withCPADisableSync(t, true)
	defer restore()

	h.addAccount("wb-api", "idx-api", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "glm-4.6",
		failoverDisableAtKey:    "2026-11-07T11:58:07Z",
		"note":                  "keep",
	})
	if !reenableAccountForFailover("idx-api", "wb-api") {
		t.Fatal("恢复应成功")
	}
	if len(*calls) != 1 || (*calls)[0].Disabled {
		t.Fatalf("应走 API 置回 enabled, calls=%+v", *calls)
	}
	doc := h.docOf("idx-api")
	if doc["disabled"] != false {
		t.Fatalf("disabled = %v, want false", doc["disabled"])
	}
	for _, k := range []string{failoverDisableKey, failoverDisableModelKey, failoverDisableAtKey} {
		if _, ok := doc[k]; ok {
			t.Fatalf("API 恢复后盘上仍残留 %s: %v", k, doc)
		}
	}
	if doc["note"] != "keep" {
		t.Fatalf("清理标记不得动其它顶层键: %v", doc)
	}
}

// TestReenableClearsOrphanSidecarWhenMarkerLost 锁定归属丢失后的自愈：
// 宿主 disabled 但盘上没有本机制标记时，插件不得永久持有 sidecar 排除。
//
// 生产形态（2026-11-07）：宿主白名单重建抹掉 failover_disable，sidecar 仍记
// disabled；探活每次探通都因「没有标记」返回 false，sidecar 永不清除，账号在
// 用户手动启用后仍被 scheduler 跳过。
func TestReenableClearsOrphanSidecarWhenMarkerLost(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-orphan", "idx-orphan", map[string]any{"disabled": true})
	failoverStateMarkDisabled("wb-orphan", "glm-4.6")
	markFailoverDisabledLocal("wb-orphan", "idx-orphan")

	if reenableAccountForFailover("idx-orphan", "wb-orphan") {
		t.Fatal("无本机制标记时不得返回「已恢复」")
	}
	if failoverStateIsDisabled("wb-orphan") {
		t.Fatal("归属丢失的 sidecar 条目必须清除，否则账号永久被排除")
	}
	if isAccountCoolingDown("wb-orphan") {
		t.Fatal("归属丢失后不得继续排除该号")
	}
	if h.docOf("idx-orphan")["disabled"] != true {
		t.Fatal("不得替他人的禁用写盘启用")
	}
}

// TestReenableClearsSidecarWhenHostDocUnreadable 对照：宿主文档读不到时
// 调用方（探活成功 / 成功请求）已经证明该号可用，sidecar 必须解除，
// 否则它就是唯一且永不自愈的排除来源。
func TestReenableClearsSidecarWhenHostDocUnreadable(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	newFailoverFakeHost(t)
	failoverStateMarkDisabled("wb-nodoc", "glm-4.6")

	// 宿主文档读不到 → reenable 走 sidecar 分支，直接解除。
	if !reenableAccountForFailover("idx-nodoc", "wb-nodoc") {
		t.Fatal("宿主读不到时应按 sidecar 解除")
	}
	if failoverStateIsDisabled("wb-nodoc") {
		t.Fatal("宿主读不到时 sidecar 必须解除，否则自造不可恢复死角")
	}
}

// TestReconcileClearsStaleDiskMarkersAndCounters 锁定手动启用后的完整对账：
// 盘上标记、本地集合、sidecar、连续失败计数必须一起清。
func TestReconcileClearsStaleDiskMarkersAndCounters(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-manual", "idx-manual", map[string]any{
		"disabled":              false,
		failoverDisableKey:      true,
		failoverDisableModelKey: "deepseek-v4.1-flash",
		failoverDisableAtKey:    "2026-11-07T11:49:40Z",
	})
	h.addAccount("wb-spare", "idx-spare", nil)

	// 复刻「手动启用前的插件侧状态」：内存排除 + sidecar 禁用 + 连续失败计数。
	markFailoverDisabledLocal("wb-manual", "idx-manual")
	failoverStateMarkDisabled("wb-manual", "deepseek-v4.1-flash")
	recordCooldownScoped("wb-manual", "deepseek-v4.1-flash", failureScopeAuthModel)
	recordCooldownScoped("wb-manual", "deepseek-v4.1-flash", failureScopeAuthModel)

	if cleared := reconcileFailoverLocalSet(); cleared == 0 {
		t.Fatal("手动启用的号必须被对账")
	}
	if isAccountCoolingDown("wb-manual") {
		t.Fatal("对账后不得继续排除该号")
	}
	if failoverStateIsDisabled("wb-manual") {
		t.Fatal("对账后 sidecar 必须解除")
	}
	if _, ok := h.docOf("idx-manual")[failoverDisableKey]; ok {
		t.Fatalf("对账必须清掉盘上陈旧标记: %v", h.docOf("idx-manual"))
	}
	if count, _, ok := authModelFailoverSnapshot("wb-manual", "deepseek-v4.1-flash"); ok && count != 0 {
		t.Fatalf("对账后失败计数残留 %d", count)
	}
	if got := h.docOf("idx-manual")["disabled"]; got != false {
		t.Fatalf("对账不得改动 disabled, got %v", got)
	}
}

// TestReconcileKeepsCounterForHealthyAccount 锁定对账不得无差别清零：
// 从不属于本机制的健康号，其连续失败计数必须保留，否则门限永不触发。
func TestReconcileKeepsCounterForHealthyAccount(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-healthy", "idx-healthy", nil)
	h.addAccount("wb-spare", "idx-spare", nil)

	recordCooldownScoped("wb-healthy", "deepseek-v4.1-flash", failureScopeAuthModel)
	recordCooldownScoped("wb-healthy", "deepseek-v4.1-flash", failureScopeAuthModel)

	reconcileFailoverLocalSet()

	count, _, ok := authModelFailoverSnapshot("wb-healthy", "deepseek-v4.1-flash")
	if !ok || count != 2 {
		t.Fatalf("健康号的失败计数被误清: ok=%v count=%d", ok, count)
	}
}

// TestDisableGuardKeepsLastUsableAccount 锁定全池保底：不准把最后一个可用号摘掉。
func TestDisableGuardKeepsLastUsableAccount(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-first", "idx-first", nil)
	h.addAccount("wb-last", "idx-last", nil)

	if !disableAccountForFailover("wb-first", "glm-4.6") {
		t.Fatal("池里还有另一个可用号时应允许禁用")
	}
	if disableAccountForFailover("wb-last", "glm-4.6") {
		t.Fatal("最后一个可用号不得被禁用（会把池摘到 0 个账号）")
	}
	if h.docOf("idx-last")["disabled"] != false {
		t.Fatalf("被保底的号不得写盘禁用: %v", h.docOf("idx-last"))
	}
}

// TestDisableGuardAllowsSingleAccountDeployment 锁定单账号部署不阻断：
// 没有第二个号可保，阻断只会让这个号永远禁不掉。
func TestDisableGuardAllowsSingleAccountDeployment(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-only", "idx-only", nil)

	if !disableAccountForFailover("wb-only", "glm-4.6") {
		t.Fatal("单账号池应允许禁用")
	}
	if h.docOf("idx-only")["disabled"] != true {
		t.Fatal("单账号池应写盘禁用")
	}
}

// TestFailoverDisableWouldEmptyPoolListErrorDoesNotBlock 锁定：宿主列表不可用
// 时保底闸门不得阻断——那是故障态，更需要允许摘号。
func TestFailoverDisableWouldEmptyPoolListErrorDoesNotBlock(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-a", "idx-a", nil)
	h.addAccount("wb-b", "idx-b", nil)

	origList := failoverListFn
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		return nil, errors.New("host unavailable")
	}
	defer func() { failoverListFn = origList }()

	if failoverDisableWouldEmptyPool("idx-a") {
		t.Fatal("列表不可用时不得判定为「会摘空池」")
	}
}

// TestReconcileSkipsUnknownAccounts 锁定对账保守方向：列表里查不到的号不动。
func TestReconcileSkipsUnknownAccounts(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	newFailoverFakeHost(t)
	markFailoverDisabledLocal("ghost", "idx-ghost")
	failoverStateMarkDisabled("ghost", "glm-4.6")

	if cleared := reconcileFailoverLocalSet(); cleared != 0 {
		t.Fatalf("列表里没有的号不得被清除, cleared=%d", cleared)
	}
	if !failoverStateIsDisabled("ghost") {
		t.Fatal("查不到的号必须保留 sidecar 禁用")
	}
}

// 静态断言：探活与对账周期必须分离（探活 10 分钟 / 对账 2 分钟），
// 合并回一个循环就会退回「手动启用最长 10 分钟不生效」。
func TestFailoverReconcileIntervalIsShorterThanProbe(t *testing.T) {
	if failoverReconcileInterval >= failoverProbeInterval {
		t.Fatalf("对账周期 %v 必须短于探活周期 %v", failoverReconcileInterval, failoverProbeInterval)
	}
	if failoverReconcileInterval > 5*time.Minute {
		t.Fatalf("对账周期 %v 过长，手动启用会长时间不生效", failoverReconcileInterval)
	}
}
