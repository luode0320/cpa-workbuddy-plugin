// failover_recovery_test.go 锁定「禁用 → 自动恢复」链路的三个生产缺陷修复：
//
//  1. 14018 配额超限曾被当作确定性故障，绕过连续失败门限，一次 429 就写盘
//     禁用（2026-11-19：盘上 failed_count=1 即 disabled）；
//  2. 探活模型名未归一化，DeepSeek-V4-Pro 被上游 11102 拒绝，导致探活永久
//     失败、6 小时零恢复；
//  3. disabled 但无任何归属标记的「孤儿」不在探活候选里，只能人工启用。
//
// 断言一律直接查假盘顶层键：函数返回值只表达「本机制是否认领」，不能证明
// 宿主与调度器看到的真相。
package main

import (
	"net/http"
	"testing"
)

// TestQuotaExceededRequiresConsecutiveFailures 锁定 14018 受写盘门限约束。
func TestQuotaExceededRequiresConsecutiveFailures(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-quota", "idx-quota", nil)

	body := `{"error":{"data":{"code":14018,"msg":"Your requests have exceeded the quota."}}}`
	for i := 1; i <= 2; i++ {
		if !recordAccountFailureForModel("wb-quota", "deepseek-v4.1-flash", http.StatusTooManyRequests, body) {
			t.Fatalf("第 %d 次 14018 应计入冷却", i)
		}
		if h.docOf("idx-quota")["disabled"] == true {
			t.Fatalf("第 %d 次 14018 不得写盘禁用（必须受连续失败门限约束）", i)
		}
	}
	if !recordAccountFailureForModel("wb-quota", "deepseek-v4.1-flash", http.StatusTooManyRequests, body) {
		t.Fatal("第 3 次 14018 应计入冷却")
	}
	if h.docOf("idx-quota")["disabled"] != true {
		t.Fatal("达到门限后应写盘禁用")
	}
	if h.docOf("idx-quota")[failoverDisableKey] != true {
		t.Fatalf("禁用必须带本机制归属标记: %v", h.docOf("idx-quota"))
	}
}

// TestOrphanDisabledIsProbeCandidate 锁定孤儿禁用进入探活、他人归属仍排除。
func TestOrphanDisabledIsProbeCandidate(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-orphan", "idx-orphan", map[string]any{"disabled": true})
	h.addAccount("wb-manual", "idx-manual", map[string]any{"disabled": true, "manual_disable": true})
	h.addAccount("wb-exhausted", "idx-exhausted", map[string]any{"disabled": true, "exhausted_disable": true})
	h.addAccount("wb-lowcredit", "idx-lowcredit", map[string]any{"disabled": true, "low_credit_disable": true})

	got := make(map[string]bool)
	for _, tc := range failoverProbeTargets() {
		got[tc.authID] = true
	}
	if !got["wb-orphan"] {
		t.Fatal("孤儿禁用（disabled 且无归属标记）必须进入探活候选")
	}
	for _, id := range []string{"wb-manual", "wb-exhausted", "wb-lowcredit"} {
		if got[id] {
			t.Fatalf("%s 由其它机制负责恢复，不得进入探活候选", id)
		}
	}
}

// TestProbeTickRecoversOrphanDisabled 锁定孤儿探通后真的写回 enabled。
func TestProbeTickRecoversOrphanDisabled(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-orphan", "idx-orphan", map[string]any{"disabled": true, "note": "keep"})

	origPing := failoverPingFn
	failoverPingFn = func(*storedAuth, string) error { return nil }
	t.Cleanup(func() { failoverPingFn = origPing })

	if probed := runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }); probed != 1 {
		t.Fatalf("本轮探测数 = %d, want 1", probed)
	}
	doc := h.docOf("idx-orphan")
	if doc["disabled"] != false {
		t.Fatalf("探通后必须写回 enabled: %v", doc)
	}
	if doc["note"] != "keep" {
		t.Fatalf("恢复只能改 disabled，不得动其它顶层键: %v", doc)
	}
}

// TestReenableKeepsManualDisabled 锁定 manual_disable 永不被自动恢复。
func TestReenableKeepsManualDisabled(t *testing.T) {
	resetFailover(t)
	isolateFailoverSidecar(t)
	h := newFailoverFakeHost(t)
	h.addAccount("wb-manual", "idx-manual", map[string]any{"disabled": true, "manual_disable": true})

	if reenableAccountForFailover("idx-manual", "wb-manual") {
		t.Fatal("manual_disable 不得被本机制恢复")
	}
	if h.docOf("idx-manual")["disabled"] != true {
		t.Fatal("manual_disable 的 disabled 状态不得被改写")
	}
}

// TestNormalizeProbeModelFoldsCase 锁定探活模型名的上游注册 ID 折叠。
func TestNormalizeProbeModelFoldsCase(t *testing.T) {
	upstreamModelIDCache.Lock()
	orig := upstreamModelIDCache.byLower
	upstreamModelIDCache.byLower = map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"}
	upstreamModelIDCache.Unlock()
	t.Cleanup(func() {
		upstreamModelIDCache.Lock()
		upstreamModelIDCache.byLower = orig
		upstreamModelIDCache.Unlock()
	})

	if got := normalizeProbeModel(nil, "  DeepSeek-V4-Pro "); got != "deepseek-v4-pro" {
		t.Fatalf("normalizeProbeModel = %q, want deepseek-v4-pro", got)
	}
	if got := normalizeProbeModel(nil, ""); got != "" {
		t.Fatalf("空模型应返回空串，got %q", got)
	}
}
