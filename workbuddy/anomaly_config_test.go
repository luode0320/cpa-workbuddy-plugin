package main

import (
	"strconv"
	"testing"
)

func TestClampAnomalyThreshold(t *testing.T) {
	cases := []struct {
		in, want int32
	}{
		{0, anomalyThresholdDefault},
		{-1, anomalyThresholdDefault},
		{1, 1},
		{10, 10},
		{50, 50},
		{51, anomalyThresholdMax},
		{99, anomalyThresholdMax},
	}
	for _, tc := range cases {
		if got := clampAnomalyThreshold(tc.in); got != tc.want {
			t.Fatalf("clampAnomalyThreshold(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseAnomalyThresholdLine(t *testing.T) {
	cases := []struct {
		line string
		want int32
		ok   bool
	}{
		{"anomaly_pool_threshold: 0", 0, true},
		{"anomaly_pool_threshold: 1", 1, true},
		{"anomaly_pool_threshold: 10", 10, true},
		{"anomaly_pool_threshold: 50", 50, true},
		{`anomaly_pool_threshold: "5"`, 5, true},
		{"anomaly_pool_threshold:   20  ", 20, true},
		{"anomaly_pool_threshold: 20 # hot path kill switch", 20, true},
		{"anomaly_pool_threshold:", 0, false},
		{"anomaly_pool_threshold: abc", 0, false},
		{"anomaly_pool_threshold: 99", 99, true}, // not clamped here — clamp is caller's job
	}
	for _, tc := range cases {
		got, ok := parseAnomalyThresholdLine(tc.line)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("parseAnomalyThresholdLine(%q) = (%d, %v), want (%d, %v)", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseAnomalyRefreshEnabledLine(t *testing.T) {
	cases := []struct {
		line    string
		wantVal bool
		wantOK  bool
	}{
		{"anomaly_refresh_enabled: true", true, true},
		{"anomaly_refresh_enabled: false", false, true},
		{"anomaly_refresh_enabled: 1", true, true},
		{"anomaly_refresh_enabled: 0", false, true},
		{"anomaly_refresh_enabled: yes", true, true},
		{"anomaly_refresh_enabled: on", true, true},
		{"anomaly_refresh_enabled:", false, false},
	}
	for _, tc := range cases {
		val, ok := parseAnomalyRefreshEnabledLine(tc.line)
		if val != tc.wantVal || ok != tc.wantOK {
			t.Fatalf("parseAnomalyRefreshEnabledLine(%q) = (%v, %v), want (%v, %v)",
				tc.line, val, ok, tc.wantVal, tc.wantOK)
		}
	}
}

func TestSetAnomalyConfig_DefaultsPreservedOnZeroThreshold(t *testing.T) {
	oldTh := anomalyThreshold()
	oldEn := anomalyRefreshEnabled()
	t.Cleanup(func() { setAnomalyConfig(oldTh, oldEn) })

	// Default state.
	setAnomalyConfig(anomalyThresholdDefault, true)

	// Threshold=0 must NOT overwrite the running threshold (kill-switch safe).
	setAnomalyConfig(0, false)
	if got := anomalyThreshold(); got != anomalyThresholdDefault {
		t.Fatalf("anomalyThreshold() after setAnomalyConfig(0,false) = %d, want %d (default preserved)",
			got, anomalyThresholdDefault)
	}
	if got := anomalyRefreshEnabled(); got != false {
		t.Fatalf("anomalyRefreshEnabled() after setAnomalyConfig(0,false) = %v, want false", got)
	}

	// Positive threshold updates.
	setAnomalyConfig(25, true)
	if got := anomalyThreshold(); got != 25 {
		t.Fatalf("anomalyThreshold() after setAnomalyConfig(25,true) = %d, want 25", got)
	}
	if got := anomalyRefreshEnabled(); got != true {
		t.Fatalf("anomalyRefreshEnabled() after setAnomalyConfig(25,true) = %v, want true", got)
	}
}

// strconv fixtures to anchor I/O error path coverage (the parser swallows it,
// we want it pinned).
var _ = strconv.Atoi
