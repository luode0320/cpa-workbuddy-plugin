package main

import (
	"encoding/json"
	"testing"
)

// TestAuthMarkerReaders pins the three top-level flag readers used by the
// exhausted-disable lifecycle (policy update 2026-09-08): disabled,
// manual_disable (user intent — never auto-touched) and exhausted_disable
// (set by disableAuth auto path, cleared by reenableAuth).
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
