// policy.go owns the failure-classification and check-in verdict helpers.
// The status/body → failure mapping mirrors the workbuddy-provider plugin
// (accountFailover.go) with markers adapted to the Trae SOLO upstream
// (4011 rate-limit, quota markers, device-dedupe wording).
package main

import "strings"

const httpStatusPaymentRequired = 402

// hardCreditMarkers match upstream bodies that indicate the account's credit
// quota is exhausted (as opposed to a transient/rate-limit failure).
var hardCreditMarkers = []string{
	"credit", "quota", "额度", "积分不足", "余额不足", "已用完", "exhausted",
}

// isHardCreditError reports whether the upstream response indicates the
// account ran out of credits (402 or a marker in the body).
func isHardCreditError(status int, body string) bool {
	if status == httpStatusPaymentRequired {
		return true
	}
	lower := strings.ToLower(body)
	for _, m := range hardCreditMarkers {
		if strings.Contains(lower, strings.ToLower(m)) || strings.Contains(body, m) {
			return true
		}
	}
	return false
}

// isSoftRateLimit reports whether the response is a soft per-account rate
// limit (429 or a marker), distinct from a hard credit error.
func isSoftRateLimit(status int, body string) bool {
	if isHardCreditError(status, body) {
		return false
	}
	if status == 429 {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "throttl") ||
		strings.Contains(lower, "4011") // Trae WAF rate-limit event code
}

// DeviceBlocked reports whether the check-in body indicates a device-level
// block (Trae dedupes check-in per device per day).
func DeviceBlocked(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(body, "设备") ||
		strings.Contains(lower, "device") ||
		strings.Contains(lower, "machine")
}

// AlreadyCheckedIn reports whether the body indicates the account already
// checked in today (as opposed to a device-level block).
func AlreadyCheckedIn(body string) bool {
	if DeviceBlocked(body) {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(body, "已签到") ||
		strings.Contains(body, "已经签到") ||
		strings.Contains(body, "明日再来") ||
		strings.Contains(body, "今日已完成") ||
		strings.Contains(body, "已领取") ||
		strings.Contains(lower, "already") ||
		strings.Contains(lower, "checked") ||
		strings.Contains(lower, "claimed")
}
