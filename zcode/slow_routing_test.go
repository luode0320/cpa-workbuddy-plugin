// slow_routing_test.go 锁定慢速冷却在路由入口的接入点。
//
// zcode 的路由统一走 accountRoutable：pickActiveAuth / pickNextAuth /
// resolveTargetAuth 全部经过它，因此只要这一处接入，三条路径同时生效。
// 用例必须用真实路由入口驱动，而不是直接调 slowCoolingDown。
package main

import (
	"testing"
	"time"
)

// TestAccountRoutable_SlowCoolingExcluded 守住路由判定的慢速排除。
func TestAccountRoutable_SlowCoolingExcluded(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	const model = "glm-5.3"
	sa := &StoredAuth{AuthID: "zc-a", APIKey: "key-a"}

	if !accountRoutable(sa, model) {
		t.Fatal("前置：未冷却时应可路由")
	}

	seedSlowCooldown(t, "zc-a", model, time.Now().Add(10*time.Minute))
	if accountRoutable(sa, model) {
		t.Fatal("慢速冷却中的账号在该模型上必须不可路由")
	}

	// 换个模型不受影响：慢速冷却按账号×模型生效。
	if !accountRoutable(sa, "glm-5.3-flash") {
		t.Fatal("慢速冷却不得跨模型生效，否则换号会把可用号一并排除")
	}
}

// TestAccountRoutable_ExpiryRestoresRouting 守住到期自动恢复。
func TestAccountRoutable_ExpiryRestoresRouting(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	const model = "glm-5.3"
	sa := &StoredAuth{AuthID: "zc-b", APIKey: "key-b"}

	// 冷却截止时刻已过 → 立即恢复可路由，不需要任何探活。
	seedSlowCooldown(t, "zc-b", model, time.Now().Add(-time.Minute))
	if !accountRoutable(sa, model) {
		t.Fatal("冷却到期后必须自动恢复路由")
	}
}

// TestAccountRoutable_DisabledSwitchStillWins 守住开关关闭时零影响。
func TestAccountRoutable_DisabledSwitchStillWins(t *testing.T) {
	withSlowConfig(t, false, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	const model = "glm-5.3"
	sa := &StoredAuth{AuthID: "zc-c", APIKey: "key-c"}
	seedSlowCooldown(t, "zc-c", model, time.Now().Add(10*time.Minute))

	if !accountRoutable(sa, model) {
		t.Fatal("总开关关闭时慢速状态不得影响路由")
	}
}
