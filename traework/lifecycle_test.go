package main

import "testing"

// TestAuthDocFlags pins the single-parse reader for the three top-level
// lifecycle flags (disabled / manual_disable / exhausted_disable).
func TestAuthDocFlags(t *testing.T) {
	cases := []struct {
		name      string
		raw       []byte
		disabled  bool
		manual    bool
		exhausted bool
	}{
		{"empty", nil, false, false, false},
		{"bad json", []byte(`{not json`), false, false, false},
		{"plain enabled", []byte(`{"type":"traework-provider"}`), false, false, false},
		{"disabled only", []byte(`{"disabled":true}`), true, false, false},
		{"manual toggle", []byte(`{"disabled":true,"manual_disable":true}`), true, true, false},
		{"exhausted disable", []byte(`{"disabled":true,"exhausted_disable":true,"note":"耗尽停用 · 余0 已用2300（签到恢复积分后自动启用）"}`), true, false, true},
		{"manual wins over exhausted", []byte(`{"disabled":true,"manual_disable":true,"exhausted_disable":true}`), true, true, true},
		{"enabled with stale marker", []byte(`{"disabled":false,"exhausted_disable":true}`), false, false, true},
	}
	for _, tc := range cases {
		disabled, manual, exhausted := authDocFlags(tc.raw)
		if disabled != tc.disabled || manual != tc.manual || exhausted != tc.exhausted {
			t.Errorf("%s: got (disabled=%v manual=%v exhausted=%v), want (%v %v %v)",
				tc.name, disabled, manual, exhausted, tc.disabled, tc.manual, tc.exhausted)
		}
	}
}
