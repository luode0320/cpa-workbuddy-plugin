// failover_sync_test.go 锁定「失败禁用 ↔ 手动启用」的联动契约（workbuddy-ai）。
//
// 与 workbuddy 0.21.13 同批整改，覆盖四类生产形态：
//  1. 渠道级 11128（unapproved channel）不得惩罚任何账号；
//  2. 恢复走宿主 API 成功后必须同时清掉盘上归属标记；
//  3. 手动启用后对账要清掉盘上陈旧标记，只对确曾归属本机制的号重置计数；
//  4. 禁用不得把池内可用账号摘到 0。
package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestChannelBlockErrorDoesNotDisableAccount 锁定渠道级 11128 不罚任何账号。
func TestChannelBlockErrorDoesNotDisableAccount(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ai-ch-1", "idx-ch-1", nil)
	h.addAccount("wb-ai-ch-2", "idx-ch-2", nil)

	body := `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`
	if noteUpstreamBlockHit("wb-ai-ch-1", "deepseek-v4.1-flash", http.StatusBadRequest, body) {
		t.Fatal("渠道级拦截不得按账号×模型记账")
	}
	if h.docOf("idx-ch-1")["disabled"] != false {
		t.Fatalf("渠道级拦截不得写盘禁用: %v", h.docOf("idx-ch-1"))
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("渠道级拦截写盘次数 = %d, want 0", got)
	}
	if count, _, ok := failoverStateSnapshot("wb-ai-ch-1"); ok && count != 0 {
		t.Fatalf("渠道级拦截不得写冷却计数, count=%d", count)
	}

	// 对照：没有渠道文案的 11128 仍按账号×模型拦截处理（旧语义保留）。
	plain := `{"code":11128,"msg":"request illegal"}`
	if !noteUpstreamBlockHit("wb-ai-ch-1", "deepseek-v4.1-flash", http.StatusBadRequest, plain) {
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
		{"400 渠道级拦截", http.StatusBadRequest,
			`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, failureScopeUpstreamBusy},
		{"403 渠道级拦截同样命中", http.StatusForbidden,
			`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, failureScopeUpstreamBusy},
		{"普通 11128", http.StatusBadRequest, `{"code":11128,"msg":"request illegal"}`, failureScopeAuthModel},
		{"11140 账号封禁", http.StatusForbidden, `{"code":11140,"msg":"request illegal"}`, failureScopeAccount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyUpstreamFailure(tc.status, tc.body); got != tc.want {
				t.Fatalf("classify(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// TestReenableClearsDiskMarkersOnAPIPath 锁定恢复必须同时清盘上标记。
func TestReenableClearsDiskMarkersOnAPIPath(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	calls, restore := withCPADisableSync(t, true)
	defer restore()

	h.addAccount("wb-ai-api", "idx-api", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "glm-4.6",
		failoverDisableAtKey:    "2026-11-07T11:58:07Z",
		"note":                  "keep",
	})
	if !reenableAccountForFailover("idx-api", "wb-ai-api") {
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

// TestReenableClearsOrphanLocalStateWhenMarkerLost 锁定归属丢失后的自愈。
func TestReenableClearsOrphanLocalStateWhenMarkerLost(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ai-orphan", "idx-orphan", map[string]any{"disabled": true})
	markFailoverDisabledLocal("wb-ai-orphan", "idx-orphan")

	if reenableAccountForFailover("idx-orphan", "wb-ai-orphan") {
		t.Fatal("无本机制标记时不得返回「已恢复」")
	}
	if isAccountCoolingDown("wb-ai-orphan") {
		t.Fatal("归属丢失后不得继续排除该号")
	}
	if h.docOf("idx-orphan")["disabled"] != true {
		t.Fatal("不得替他人的禁用写盘启用")
	}
}

// TestReconcileClearsStaleDiskMarkersAndCounters 锁定手动启用后的完整对账。
func TestReconcileClearsStaleDiskMarkersAndCounters(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ai-manual", "idx-manual", map[string]any{
		"disabled":              false,
		failoverDisableKey:      true,
		failoverDisableModelKey: "deepseek-v4.1-flash",
		failoverDisableAtKey:    "2026-11-07T11:49:40Z",
	})
	h.addAccount("wb-ai-spare", "idx-spare", nil)

	markFailoverDisabledLocal("wb-ai-manual", "idx-manual")
	recordCooldownScoped("wb-ai-manual", "deepseek-v4.1-flash", failureScopeAuthModel)
	recordCooldownScoped("wb-ai-manual", "deepseek-v4.1-flash", failureScopeAuthModel)

	if cleared := reconcileFailoverLocalSet(); cleared == 0 {
		t.Fatal("手动启用的号必须被对账")
	}
	if isAccountCoolingDown("wb-ai-manual") {
		t.Fatal("对账后不得继续排除该号")
	}
	if _, ok := h.docOf("idx-manual")[failoverDisableKey]; ok {
		t.Fatalf("对账必须清掉盘上陈旧标记: %v", h.docOf("idx-manual"))
	}
	if count, _, ok := authModelFailoverSnapshot("wb-ai-manual", "deepseek-v4.1-flash"); ok && count != 0 {
		t.Fatalf("对账后失败计数残留 %d", count)
	}
	if got := h.docOf("idx-manual")["disabled"]; got != false {
		t.Fatalf("对账不得改动 disabled, got %v", got)
	}
}

// TestReconcileKeepsCounterForHealthyAccount 锁定对账不得无差别清零。
func TestReconcileKeepsCounterForHealthyAccount(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ai-healthy", "idx-healthy", nil)
	h.addAccount("wb-ai-spare", "idx-spare", nil)

	recordCooldownScoped("wb-ai-healthy", "deepseek-v4.1-flash", failureScopeAuthModel)
	recordCooldownScoped("wb-ai-healthy", "deepseek-v4.1-flash", failureScopeAuthModel)

	reconcileFailoverLocalSet()

	count, _, ok := authModelFailoverSnapshot("wb-ai-healthy", "deepseek-v4.1-flash")
	if !ok || count != 2 {
		t.Fatalf("健康号的失败计数被误清: ok=%v count=%d", ok, count)
	}
}

// TestDisableGuardKeepsLastUsableAccount 锁定全池保底。
func TestDisableGuardKeepsLastUsableAccount(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ai-first", "idx-first", nil)
	h.addAccount("wb-ai-last", "idx-last", nil)

	if !disableAccountForFailover("wb-ai-first", "glm-4.6") {
		t.Fatal("池里还有另一个可用号时应允许禁用")
	}
	if disableAccountForFailover("wb-ai-last", "glm-4.6") {
		t.Fatal("最后一个可用号不得被禁用")
	}
	if h.docOf("idx-last")["disabled"] != false {
		t.Fatalf("被保底的号不得写盘禁用: %v", h.docOf("idx-last"))
	}
}

// TestDisableGuardAllowsSingleAccountDeployment 锁定单账号部署不阻断。
func TestDisableGuardAllowsSingleAccountDeployment(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ai-only", "idx-only", nil)

	if !disableAccountForFailover("wb-ai-only", "glm-4.6") {
		t.Fatal("单账号池应允许禁用")
	}
	if h.docOf("idx-only")["disabled"] != true {
		t.Fatal("单账号池应写盘禁用")
	}
}

// TestFailoverDisableWouldEmptyPoolListErrorDoesNotBlock 锁定：宿主列表不可用时
// 保底闸门不得阻断——那是故障态，更需要允许摘号。
func TestFailoverDisableWouldEmptyPoolListErrorDoesNotBlock(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-ai-a", "idx-a", nil)
	h.addAccount("wb-ai-b", "idx-b", nil)

	origList := failoverListFn
	failoverListFn = func() ([]pluginapi.HostAuthFileEntry, error) { return nil, errors.New("host unavailable") }
	defer func() { failoverListFn = origList }()

	if failoverDisableWouldEmptyPool("idx-a") {
		t.Fatal("列表不可用时不得判定为「会摘空池」")
	}
}

// TestReconcileSkipsUnknownAccounts 锁定对账保守方向。
func TestReconcileSkipsUnknownAccounts(t *testing.T) {
	resetFailover(t)
	newFailoverFakeHost(t)
	markFailoverDisabledLocal("ghost", "idx-ghost")

	if cleared := reconcileFailoverLocalSet(); cleared != 0 {
		t.Fatalf("列表里没有的号不得被清除, cleared=%d", cleared)
	}
	if !isAccountCoolingDown("ghost") {
		t.Fatal("查不到的键应保留排除")
	}
}
