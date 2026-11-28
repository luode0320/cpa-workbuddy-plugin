package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuthMarkerReaders pins the three top-level flag readers used by the
// exhausted-disable lifecycle: disabled, manual_disable (user intent — never
// auto-touched) and exhausted_disable (历史自动禁用标记，校验时据此恢复启用).
func TestAuthMarkerReaders(t *testing.T) {
	if !parseDisabledFromAuthJSON([]byte(`{"disabled":true}`)) {
		t.Fatal("disabled:true must read true")
	}
	if parseDisabledFromAuthJSON(nil) {
		t.Fatal("nil input must read false")
	}

	if !manualDisableFromAuthJSON([]byte(`{"disabled":true,"manual_disable":true}`)) {
		t.Fatal("manual_disable:true must read true")
	}
	if manualDisableFromAuthJSON([]byte(`{"manual_disable":"true"}`)) {
		t.Fatal("non-bool manual_disable must read false")
	}
	if manualDisableFromAuthJSON(nil) {
		t.Fatal("nil input must read false")
	}

	if !exhaustedDisableFromAuthJSON([]byte(`{"disabled":true,"exhausted_disable":true}`)) {
		t.Fatal("exhausted_disable:true must read true")
	}
	if exhaustedDisableFromAuthJSON([]byte(`{"disabled":true}`)) {
		t.Fatal("missing exhausted_disable must read false")
	}
	if exhaustedDisableFromAuthJSON(nil) {
		t.Fatal("nil input must read false")
	}
}

// TestBuildAuthFileJSONExhaustedMarker verifies the exhausted_disable marker
// round-trips through buildAuthFileJSON extra (auto-disable path) and is
// dropped by the reenable rebuild (extra=nil) — the marker set/clear pair is
// what arms and disarms auto-recovery.
func TestBuildAuthFileJSONExhaustedMarker(t *testing.T) {
	sa := &storedAuth{Account: storedAccount{UID: "uid-ex-1"}}

	raw, err := buildAuthFileJSON(sa, true, "CN · 已禁用 · 耗尽 · 余0 已用2300", map[string]any{"exhausted_disable": true})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["exhausted_disable"] != true {
		t.Fatalf("exhausted_disable missing: %v", m["exhausted_disable"])
	}
	if m["disabled"] != true {
		t.Fatalf("disabled must be true: %v", m["disabled"])
	}

	// Re-enable rebuild (extra=nil) must clear both intent markers.
	raw2, err := buildAuthFileJSON(sa, false, "CN · 恢复启用", nil)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	var m2 map[string]any
	if err := json.Unmarshal(raw2, &m2); err != nil {
		t.Fatalf("decode2: %v", err)
	}
	if _, ok := m2["exhausted_disable"]; ok {
		t.Fatalf("reenable must clear exhausted_disable: %v", m2["exhausted_disable"])
	}
	if _, ok := m2["manual_disable"]; ok {
		t.Fatalf("reenable must clear manual_disable: %v", m2["manual_disable"])
	}
}

// TestExhaustedCreditsAreNotRoutable 验证耗尽账号在「付费模型」维度被排除并显示「耗尽」。
// 免费模型另由 accountUsableForModel 放行，见 free_models_test.go。
func TestExhaustedCreditsAreNotRoutable(t *testing.T) {
	zero := &creditsSummary{TotalRemain: 0, TotalUsed: 500, TotalSize: 500}

	// 1. 耗尽账号不可路由
	if !isCreditsLowForRouting(zero) {
		t.Fatal("exhausted account must be excluded from routing (no free models)")
	}
	if !isCreditsLowOrExhausted(zero) {
		t.Fatal("isCreditsLowOrExhausted must flag exhausted credits")
	}

	// 2. 只有正积分残血才触发低额保护
	low := &creditsSummary{TotalRemain: 5, TotalUsed: 495, TotalSize: 500}
	if !isCreditsLowForRouting(low) {
		t.Fatal("positive residual below the floor must be excluded")
	}

	// 3. 未知额度不得误判为耗尽
	if isCreditsExhausted(&creditsSummary{}) {
		t.Fatal("empty snapshot with no usage signal must stay unknown")
	}

	// 4. 面板备注明确显示「耗尽」
	if note := displayNote(&storedAuth{}, zero, false); !strings.Contains(note, "耗尽") {
		t.Fatalf("exhausted note must say 耗尽, got %q", note)
	}
}

// TestExceededFlagMarksExhausted 验证上游 quota.exceeded 是权威耗尽判据。
// 免费版账号没有额度包，total_used/total_size 恒为 0，仅靠数值会被误判成未知。
func TestExceededFlagMarksExhausted(t *testing.T) {
	// 三项数值全 0 + exceeded=true：必须判为耗尽
	upstreamExceeded := &creditsSummary{Exceeded: true}
	if !isCreditsExhausted(upstreamExceeded) {
		t.Fatal("quota.exceeded=true must be treated as exhausted")
	}
	if !isCreditsLowForRouting(upstreamExceeded) {
		t.Fatal("quota.exceeded=true must be excluded from routing")
	}

	// 三项数值全 0 且无 exceeded：未知，不得误判
	if isCreditsExhausted(&creditsSummary{}) {
		t.Fatal("zero snapshot without exceeded must stay unknown")
	}

	// 有余额时即使 exceeded 残留也不得判为耗尽
	withRemain := &creditsSummary{TotalRemain: 100, Exceeded: true}
	if isCreditsExhausted(withRemain) {
		t.Fatal("positive remain must win over a stale exceeded flag")
	}
}

// TestDisableAuth_API_SuccessStillPersistsMarker 验证走宿主管理 API 成功后，
// exhausted_disable 归属标记仍必须落盘：宿主 API 只写 disabled、不回写插件
// 自定义顶层字段，漏写会让账号变成「无标记禁用」，充值后永不自动恢复。
func TestDisableAuth_API_SuccessStillPersistsMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qwenwork-uid-marker.json")
	initial := []byte(`{"type":"qwenwork-provider","provider":"qwenwork-provider","disabled":false,` +
		`"auth":{"accessToken":"dt-x"},"account":{"uid":"uid-marker"}}`)
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}

	origGet, origSet := hostAuthGetPhysicalFn, setHostAuthDisabledFn
	t.Cleanup(func() {
		hostAuthGetPhysicalFn = origGet
		setHostAuthDisabledFn = origSet
	})
	hostAuthGetPhysicalFn = func(string) (*hostAuthPhysical, error) {
		return &hostAuthPhysical{Name: "qwenwork-uid-marker.json", Path: path, JSON: initial}, nil
	}
	var apiCalls []bool
	setHostAuthDisabledFn = func(_ string, _ string, disabled bool) error {
		apiCalls = append(apiCalls, disabled)
		return nil
	}

	sa := &storedAuth{Account: storedAccount{UID: "uid-marker"}}
	sa.Auth.AccessToken = "dt-x"
	cr := &creditsSummary{TotalRemain: 0, TotalUsed: 100, TotalSize: 100}
	if err := disableAuth("idx-marker", "uid-marker", sa, cr, "耗尽"); err != nil {
		t.Fatalf("disableAuth: %v", err)
	}

	if len(apiCalls) != 1 || !apiCalls[0] {
		t.Fatalf("host api disable calls = %v, want exactly one true call", apiCalls)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(written, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc["exhausted_disable"] != true {
		t.Fatalf("exhausted_disable marker must be persisted even when the host API succeeded, got %v", doc["exhausted_disable"])
	}
	if doc["disabled"] != true {
		t.Fatalf("disabled must be true in the persisted file, got %v", doc["disabled"])
	}
}

// TestDisableAuth_MarkerPersistFailureRollsBackHostDisable 验证标记落盘失败时
// 必须回滚宿主禁用，避免留下「已禁用但无归属标记」的半成品状态。
func TestDisableAuth_MarkerPersistFailureRollsBackHostDisable(t *testing.T) {
	// Path 留空 → persistAuthDirect 必然失败。
	initial := []byte(`{"type":"qwenwork-provider","disabled":false,"auth":{"accessToken":"dt-y"},"account":{"uid":"uid-rb"}}`)

	origGet, origSet := hostAuthGetPhysicalFn, setHostAuthDisabledFn
	t.Cleanup(func() {
		hostAuthGetPhysicalFn = origGet
		setHostAuthDisabledFn = origSet
	})
	hostAuthGetPhysicalFn = func(string) (*hostAuthPhysical, error) {
		return &hostAuthPhysical{Name: "qwenwork-uid-rb.json", Path: "", JSON: initial}, nil
	}
	var apiCalls []bool
	setHostAuthDisabledFn = func(_ string, _ string, disabled bool) error {
		apiCalls = append(apiCalls, disabled)
		return nil
	}

	sa := &storedAuth{Account: storedAccount{UID: "uid-rb"}}
	sa.Auth.AccessToken = "dt-y"
	cr := &creditsSummary{TotalRemain: 0, TotalUsed: 50, TotalSize: 50}
	err := disableAuth("idx-rb", "uid-rb", sa, cr, "耗尽")
	if err == nil {
		t.Fatal("disableAuth must fail when the marker cannot be persisted")
	}
	if len(apiCalls) != 2 || !apiCalls[0] || apiCalls[1] {
		t.Fatalf("expected disable then rollback (true,false), got %v", apiCalls)
	}
}
