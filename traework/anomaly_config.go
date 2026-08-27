// anomaly_config.go owns the anomaly-pool threshold and refresh defaults.
//
// Mirrors workbuddy/anomaly_config.go so the plugins stay in lockstep on the
// cooldown-vs-anomaly split: traework has the retry-only failover model (no
// preserve/watchdog), so anomaly is its first lifetime-health gate. Defaults
// are intentionally identical so config recipes are portable.
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

func clampAnomalyThreshold(n int32) int32 {
	if n < anomalyThresholdMin {
		return anomalyThresholdDefault
	}
	if n > anomalyThresholdMax {
		return anomalyThresholdMax
	}
	return n
}

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

func parseAnomalyRefreshEnabledLine(line string) (bool, bool) {
	v := strings.TrimSpace(strings.TrimPrefix(line, "anomaly_refresh_enabled:"))
	v = strings.Trim(v, "\"'")
	if v == "" {
		return false, false
	}
	return v == "true" || v == "1" || v == "yes" || v == "on", true
}
