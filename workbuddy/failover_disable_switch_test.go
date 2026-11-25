// failover_disable_switch_test.go 覆盖「失败即写盘禁用」总开关
// failover_disable_enabled 的默认值、配置接线与关闭语义。
//
// 背景：写盘禁用会把账号从宿主路由里彻底摘掉，恢复只能靠每 10 分钟一轮的
// 禁用探活。用户要求该链路默认关闭，只在显式配置时开启。
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestFailoverDisableEnabledDefaultsOff 锁定默认值：进程初始态必须是关闭，
// 否则「默认关闭」这个对外承诺在新部署上不成立。
// 最近修改时间：2026-11-25；改动原因：新增，锁定默认关闭语义。
func TestFailoverDisableEnabledDefaultsOff(t *testing.T) {
	if defaultFailoverDisableEnabled {
		t.Fatal("defaultFailoverDisableEnabled 必须为 false：默认不写盘禁用")
	}
	if defaultFailoverDisableMinCount < 1 {
		t.Fatalf("defaultFailoverDisableMinCount = %d, want >= 1", defaultFailoverDisableMinCount)
	}
}

// TestFailoverDisableEnabledOffSkipsDisable 锁定关闭语义：即使连续失败远超门限，
// 也不得写盘禁用，且内存冷却照常记账（否则关闭后连换号都失效）。
// 最近修改时间：2026-11-25；改动原因：新增，锁定关闭后不写盘但仍冷却。
func TestFailoverDisableEnabledOffSkipsDisable(t *testing.T) {
	resetFailover(t)
	setFailoverDisableEnabled(false)
	setFailoverDisableMinCount(1)
	t.Cleanup(func() { setFailoverDisableMinCount(defaultFailoverDisableMinCount) })

	h := newFailoverFakeHost(t)
	h.addAccount("sw-1", "idx-sw-1", nil)

	for i := 0; i < 5; i++ {
		if !noteAccountFailureForModel("sw-1", "glm-4.6", 500, "upstream 500: internal error") {
			t.Fatal("关闭写盘禁用后，5xx 仍应计入内存冷却")
		}
	}
	if got := h.writeCount(); got != 0 {
		t.Fatalf("总开关关闭时写盘次数 = %d, want 0", got)
	}
	if doc := h.docOf("idx-sw-1"); doc["disabled"] == true {
		t.Fatalf("总开关关闭时不得写盘禁用: %v", doc)
	}
	// 冷却仍要成立，否则账号既不冷却也不禁用，等于彻底失去失败保护。
	if count, _, ok := failoverStateSnapshot("sw-1"); !ok || count < 1 {
		t.Fatalf("总开关关闭后仍应有内存冷却记账, count=%d ok=%v", count, ok)
	}
}

// TestFailoverDisableEnabledOffSkipsDeterministicDisable 锁定确定性失败也受总开关约束：
// 11128 模型拦截走 recordFailureScoped（无门限、直接禁用），关闭总开关后同样不得写盘。
// 最近修改时间：2026-11-25；改动原因：新增，确认两个记账入口都被开关覆盖。
func TestFailoverDisableEnabledOffSkipsDeterministicDisable(t *testing.T) {
	resetFailover(t)
	setFailoverDisableEnabled(false)

	h := newFailoverFakeHost(t)
	h.addAccount("sw-2", "idx-sw-2", nil)

	noteModelBlockedHit("sw-2", "glm-4.6")

	if got := h.writeCount(); got != 0 {
		t.Fatalf("总开关关闭时确定性失败写盘次数 = %d, want 0", got)
	}
	if doc := h.docOf("idx-sw-2"); doc["disabled"] == true {
		t.Fatalf("总开关关闭时确定性失败不得写盘禁用: %v", doc)
	}
}

// TestFailoverDisableEnabledOnStillDisables 锁定开启后行为不变：
// 配置开启时写盘禁用链路与改造前完全一致。
// 最近修改时间：2026-11-25；改动原因：新增，锁定开启路径无回归。
func TestFailoverDisableEnabledOnStillDisables(t *testing.T) {
	resetFailover(t)
	setFailoverDisableEnabled(true)
	setFailoverDisableMinCount(1)
	t.Cleanup(func() { setFailoverDisableMinCount(defaultFailoverDisableMinCount) })

	h := newFailoverFakeHost(t)
	h.addAccount("sw-3", "idx-sw-3", nil)

	noteAccountFailureForModel("sw-3", "glm-4.6", 500, "upstream 500: internal error")

	if got := h.writeCount(); got != 1 {
		t.Fatalf("总开关开启且门限=1 时写盘次数 = %d, want 1", got)
	}
	if doc := h.docOf("idx-sw-3"); doc["disabled"] != true {
		t.Fatalf("总开关开启时达门限应写盘禁用: %v", doc)
	}
}

// TestFailoverDisableEnabledConfig_LoadsViaConfigure 锁定配置接线：
// config_yaml 里的 failover_disable_enabled 必须真的改变运行时开关。
// 最近修改时间：2026-11-25；改动原因：新增，避免「配置读了但没接上」。
func TestFailoverDisableEnabledConfig_LoadsViaConfigure(t *testing.T) {
	old := failoverDisableActive()
	t.Cleanup(func() { setFailoverDisableEnabled(old) })

	cfg := func(yaml string) []byte {
		raw, _ := json.Marshal(map[string]any{"config_yaml": []byte(yaml)})
		return raw
	}

	// 1. 显式开启。
	setFailoverDisableEnabled(false)
	configure(cfg("failover_disable_enabled: true\n"))
	if !failoverDisableActive() {
		t.Fatal("configure(failover_disable_enabled: true) 后开关应为开启")
	}

	// 2. 各种真值写法都要认。
	for _, v := range []string{"true", "1", "yes", "on", `"true"`} {
		setFailoverDisableEnabled(false)
		configure(cfg("failover_disable_enabled: " + v + "\n"))
		if !failoverDisableActive() {
			t.Fatalf("configure(failover_disable_enabled: %s) 后开关应为开启", v)
		}
	}

	// 3. 显式关闭。
	setFailoverDisableEnabled(true)
	configure(cfg("failover_disable_enabled: false\n"))
	if failoverDisableActive() {
		t.Fatal("configure(failover_disable_enabled: false) 后开关应为关闭")
	}

	// 4. 键缺失时回落到默认关闭（不能沿用上一次的开启态）。
	setFailoverDisableEnabled(true)
	configure(cfg("checkin_auto: true\n"))
	if failoverDisableActive() {
		t.Fatal("配置缺该键时应回落到默认关闭，而不是沿用上一次的开启态")
	}
}

// TestFailoverDisableEnabled_RegisteredAsConfigField 锁定面板可见性：
// 只被 configure() 解析还不够，必须登记到 ConfigFields，管理端才会渲染成可编辑项。
// 最近修改时间：2026-11-25；改动原因：新增，锁定配置项对外暴露。
func TestFailoverDisableEnabled_RegisteredAsConfigField(t *testing.T) {
	fields := wbRegistration().Metadata.ConfigFields
	for _, f := range fields {
		if f.Name != "failover_disable_enabled" {
			continue
		}
		if f.Type != pluginapi.ConfigFieldTypeBoolean {
			t.Fatalf("failover_disable_enabled type = %v, want boolean", f.Type)
		}
		if !strings.Contains(f.Description, "默认关闭") {
			t.Fatalf("description 必须写明默认关闭，got %q", f.Description)
		}
		return
	}
	t.Fatal("failover_disable_enabled 未登记到 ConfigFields：用户在配置里改了也不会被管理端暴露")
}
