// failover_disable_switch_test.go 锁定「关闭失败写盘禁用、保留错误冷却」的语义。
//
// 背景（2026-11-09 用户裁定）：traework 不再用「连续失败 → 写盘 disabled →
// 10 分钟探活恢复」表达账号故障，只保留内存阶梯冷却。生产默认
// `failover_disable_enabled: false`，本文件是该默认值的回归防线：
//   - 关闭态下任何失败都不得写出 disabled / 禁用标记；
//   - 冷却阶梯照常累积与生效；
//   - 历史遗留的 failover_disable 必须被自动解除，不能因关闭功能而永久失联。
package main

import (
	"encoding/json"
	"testing"
)

// TestFailoverDisableOffKeepsCooldownOnly 锁定核心语义：关闭禁用后连续失败
// 只累积冷却，写盘次数恒为 0，账号仍处于冷却窗口内。
func TestFailoverDisableOffKeepsCooldownOnly(t *testing.T) {
	resetFailover(t)
	// resetFailover 为兼容既有用例默认开启禁用，这里显式关闭。
	setFailoverDisableEnabled(false)

	h := newFailoverFakeHost(t)
	h.addAccount("off-1", "idx-off-1", nil)

	for i := 0; i < 5; i++ {
		if !noteAccountFailureForModel("off-1", "glm-4.6", 500, "upstream 500: internal error") {
			t.Fatalf("第 %d 次失败应计入冷却", i+1)
		}
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("关闭失败禁用后写盘次数 = %d, want 0", got)
	}
	if !isAccountCoolingDown("off-1") {
		t.Fatal("关闭禁用后账号仍必须处于冷却窗口内（冷却才是唯一的排除手段）")
	}
	count, _, ok := failoverStateSnapshot("off-1")
	if !ok || count != 5 {
		t.Fatalf("连续失败计数 = %d(ok=%v), want 5（冷却阶梯必须正常累积）", count, ok)
	}
}

// TestFailoverDisableOffIgnoresDeterministicFailure 确认确定性失败（401）
// 同样不写盘：关闭开关后没有任何分类可以绕过。
func TestFailoverDisableOffIgnoresDeterministicFailure(t *testing.T) {
	resetFailover(t)
	setFailoverDisableEnabled(false)

	h := newFailoverFakeHost(t)
	h.addAccount("off-401", "idx-off-401", nil)

	if !noteAccountFailureForModel("off-401", "glm-4.6", 401, `{"error":"invalid token"}`) {
		t.Fatal("401 应计入冷却")
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("确定性失败写盘次数 = %d, want 0（开关关闭时不得禁用）", got)
	}
	if doc := h.docOf("idx-off-401"); doc["disabled"] == true {
		t.Fatalf("关闭态下账号不得被 disabled: %v", doc)
	}
}

// TestFailoverDisableOffRejectsDirectDisable 锁定直接调用点也被守卫罩住：
// 开关关闭时 disableAccountForFailover 必须直接拒绝，不写盘、不落 sidecar。
func TestFailoverDisableOffRejectsDirectDisable(t *testing.T) {
	resetFailover(t)
	setFailoverDisableEnabled(false)

	h := newFailoverFakeHost(t)
	h.addAccount("off-direct", "idx-off-direct", nil)

	if disableAccountForFailover("off-direct", "glm-4.6") {
		t.Fatal("关闭态下 disableAccountForFailover 必须返回 false")
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("直接调用写盘次数 = %d, want 0", got)
	}
	if failoverStateIsDisabled("off-direct") {
		t.Fatal("关闭态下不得写入 sidecar 禁用条目")
	}
}

// TestReconcileFailoverDisableOffReenablesHistoricalDisable 锁定关闭功能的收口：
// 历史版本写下的 failover_disable 禁用必须被自动解除，否则该号永久失联。
func TestReconcileFailoverDisableOffReenablesHistoricalDisable(t *testing.T) {
	resetFailover(t)
	setFailoverDisableEnabled(false)

	h := newFailoverFakeHost(t)
	h.addAccount("hist-1", "idx-hist-1", map[string]any{
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "glm-4.6",
		failoverDisableAtKey:    "2026-11-01T00:00:00Z",
	})

	if n := reconcileFailoverDisableOff(); n != 1 {
		t.Fatalf("清理历史禁用数 = %d, want 1", n)
	}
	doc := h.docOf("idx-hist-1")
	if doc["disabled"] == true {
		t.Fatalf("历史禁用必须被解除: %v", doc)
	}
	for _, key := range []string{failoverDisableKey, failoverDisableModelKey, failoverDisableAtKey} {
		if _, ok := doc[key]; ok {
			t.Fatalf("恢复后不得残留本机制标记 %q: %v", key, doc)
		}
	}
	if isAccountCoolingDown("hist-1") {
		t.Fatal("恢复后不应继续被排除")
	}
}

// TestReconcileFailoverDisableOffKeepsManualDisable 确认清理不越界：
// 没有本机制标记的手动禁用绝不能被关闭态清理碰掉。
func TestReconcileFailoverDisableOffKeepsManualDisable(t *testing.T) {
	resetFailover(t)
	setFailoverDisableEnabled(false)

	h := newFailoverFakeHost(t)
	h.addAccount("manual-1", "idx-manual-1", map[string]any{
		"disabled":       true,
		"manual_disable": true,
	})

	if n := reconcileFailoverDisableOff(); n != 0 {
		t.Fatalf("清理数 = %d, want 0（不得解除手动禁用）", n)
	}
	if h.docOf("idx-manual-1")["disabled"] != true {
		t.Fatal("手动禁用的账号必须保持 disabled")
	}
}

// TestFailoverDisableConfigToggle 锁定配置接通与 kill-switch 安全口径：
// 显式键生效，无关 reconfigure 不得把它冲回默认值。
func TestFailoverDisableConfigToggle(t *testing.T) {
	resetFailover(t)
	setFailoverDisableEnabled(false)

	cfg := func(yaml string) []byte {
		raw, err := json.Marshal(map[string]any{"config_yaml": []byte(yaml)})
		if err != nil {
			t.Fatalf("marshal config: %v", err)
		}
		return raw
	}

	configure(cfg("failover_disable_enabled: true\n"))
	if !failoverDisableActive() {
		t.Fatal("failover_disable_enabled: true 必须开启写盘禁用")
	}

	// 无关键的 reconfigure 不得把显式开启冲回默认关闭。
	configure(cfg("scheduler_mode: session\n"))
	if !failoverDisableActive() {
		t.Fatal("缺少该键的 reconfigure 不得重置开关（kill-switch 安全）")
	}

	configure(cfg("failover_disable_enabled: false\n"))
	if failoverDisableActive() {
		t.Fatal("failover_disable_enabled: false 必须关闭写盘禁用")
	}

	// 管理端必须能看到这个键，否则用户无法在面板上开关。
	found := false
	for _, field := range wbRegistration().Metadata.ConfigFields {
		if field.Name == "failover_disable_enabled" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("failover_disable_enabled 必须登记进 ConfigFields")
	}
}
