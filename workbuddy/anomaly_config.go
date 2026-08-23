// anomaly_config.go owns the anomaly-pool threshold and refresh defaults.
//
// anomalyThresholdDefault — the consecutive-failure count at which an
// account is moved into the anomaly set. Surviving accounts (not
// quarantined) keep serving traffic. Once an account enters the set, it
// stays until the operator manually unfreezes it via the panel or the
// daily 00:00 refresh loop clears the set (anomaly.go).
//
// Out-of-range user config is clamped to [anomalyThresholdMin,
// anomalyThresholdMax] (range guards against accidental "freeze every
// account on a single failure" misconfig). Setting threshold to a value
// <= 0 disables the auto-freeze mechanism entirely (kill-switch safe,
// mirrors retry_on_4xx=0 behavior).
//
// anomalyRefreshEnabledDefault — when true, anomalyRefreshLoop clears
// every entry in the anomaly set at local 00:00 every day so a hard
// outage doesn't permanently lock out a recoverable account. Operators
// may disable this via `anomaly_refresh_enabled: false` if they want a
// permanent quarantine until manual unfreeze.
package main

import (
	"strconv"
	"strings"
)

const (
	anomalyThresholdDefault int32 = 10
	anomalyThresholdMin     int32 = 1
	anomalyThresholdMax     int32 = 50

	anomalyRefreshEnabledDefault = true
)

// clampAnomalyThreshold returns n clamped to
// [anomalyThresholdMin, anomalyThresholdMax]. Out-of-range and
// unparseable values fall back to the default; the call site decides
// whether to log the substitution.
func clampAnomalyThreshold(n int32) int32 {
	if n < anomalyThresholdMin {
		return anomalyThresholdDefault
	}
	if n > anomalyThresholdMax {
		return anomalyThresholdMax
	}
	return n
}

// parseAnomalyThresholdLine extracts an integer from an
// `anomaly_pool_threshold: N` config_yaml line. Returns (value, ok).
// Whitespace and inline comments after the number are tolerated.
func parseAnomalyThresholdLine(line string) (int32, bool) {
	v := strings.TrimSpace(strings.TrimPrefix(line, "anomaly_pool_threshold:"))
	v = strings.Trim(v, "\"'")
	if v == "" {
		return 0, false
	}
	if i := strings.Index(v, "#"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

// parseAnomalyRefreshEnabledLine parses `anomaly_refresh_enabled: true|false`
// (or 1/0/yes/on). Returns (value, ok).
func parseAnomalyRefreshEnabledLine(line string) (bool, bool) {
	v := strings.TrimSpace(strings.TrimPrefix(line, "anomaly_refresh_enabled:"))
	v = strings.Trim(v, "\"'")
	if v == "" {
		return false, false
	}
	return v == "true" || v == "1" || v == "yes" || v == "on", true
}
