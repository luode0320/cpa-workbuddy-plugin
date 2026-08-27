// checkin.go implements Trae Work daily check-in and points queries, ported
// from the verified prototype (trae-gateway-go/internal/checkin, itself a
// port of trae-check electron/checkin.ts), plus the daily auto check-in
// loop (09:00 / 21:00 local time).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	claimPath  = "/trae/api/v2/ug/checkin_credits/claim"
	pointsPath = "/trae/api/v2/pay/user_current_entitlement_list"
)

// checkinResult is the outcome of one check-in attempt.
type checkinResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Points  int64  `json:"points,omitempty"`
}

// deviceIDFor builds the per-account x-device-id. Trae dedupes check-in per
// device per day; appending the userId makes different accounts look like
// different devices (trae-check verified strategy).
func deviceIDFor(baseDeviceID, userID string) string {
	if userID != "" {
		return baseDeviceID + "-" + userID
	}
	return baseDeviceID
}

func checkinAuthHeaders(a *traeAuth, deviceID string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Cloud-IDE-JWT "+a.Token)
	h.Set("x-device-id", deviceID)
	return h
}

// checkinAccount performs one claim for a parsed account.
func checkinAccount(a *traeAuth) checkinResult {
	if a == nil || !a.hasToken() {
		return checkinResult{OK: false, Message: "no credential"}
	}
	host := a.checkinHost()
	deviceID := deviceIDFor(a.DeviceID, a.UserID)
	req, err := http.NewRequest(http.MethodPost, host+claimPath, bytes.NewReader([]byte("{}")))
	if err != nil {
		return checkinResult{OK: false, Message: err.Error()}
	}
	req.Header = checkinAuthHeaders(a, deviceID)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return checkinResult{OK: false, Message: "checkin request: " + err.Error()}
	}
	body := string(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return checkinResult{OK: false, Message: fmt.Sprintf("HTTP %d %s", resp.StatusCode, truncateRedacted(body, 160))}
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return checkinResult{OK: false, Message: "decode: " + err.Error()}
	}
	if apiSucceeded(data) {
		return checkinResult{OK: true, Message: msgOf(data, "签到成功"), Points: pointsOf(data)}
	}
	msg := msgOf(data, "签到失败")
	if AlreadyCheckedIn(msg) {
		return checkinResult{OK: true, Message: "今日已签到"}
	}
	if DeviceBlocked(msg) {
		return checkinResult{OK: false, Message: msg + "（设备级拦截，稍后重试）"}
	}
	return checkinResult{OK: false, Message: msg}
}

// accountPoints queries the account's remaining credits and caches them.
func accountPoints(a *traeAuth) (int64, error) {
	if a == nil || !a.hasToken() {
		return 0, fmt.Errorf("no credential")
	}
	host := a.checkinHost()
	body, _ := json.Marshal(map[string]bool{"require_usage": true})
	req, err := http.NewRequest(http.MethodPost, host+pointsPath, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header = checkinAuthHeaders(a, a.DeviceID)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return 0, err
	}
	raw := resp.Body
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("points HTTP %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, err
	}
	return extractRemainingCredits(data), nil
}

// extractRemainingCredits sums entitlement packs: credits_limit - credits_amount.
func extractRemainingCredits(data map[string]any) int64 {
	packs, _ := data["user_entitlement_pack_list"].([]any)
	var total int64
	for _, p := range packs {
		pack, _ := p.(map[string]any)
		base, _ := pack["entitlement_base_info"].(map[string]any)
		quota, _ := base["quota"].(map[string]any)
		limit := toInt64(quota["credits_limit"])
		if limit <= 0 {
			continue
		}
		usage, _ := pack["usage"].(map[string]any)
		used := toInt64(usage["credits_amount"])
		total += maxInt64(limit-used, 0)
	}
	return total
}

// -----------------------------------------------------------------------------
// Auto check-in scheduler (09:00 / 21:00 local)
// -----------------------------------------------------------------------------

var (
	checkinAutoMu sync.RWMutex
	checkinAuto   = defaultCheckinAuto
)

// setCheckinAuto toggles the daily auto check-in loop (config / management).
func setCheckinAuto(on bool) {
	checkinAutoMu.Lock()
	checkinAuto = on
	checkinAutoMu.Unlock()
}

func autoCheckinEnabled() bool {
	checkinAutoMu.RLock()
	defer checkinAutoMu.RUnlock()
	return checkinAuto
}

// checkinTickInterval bounds how often the auto loop checks the wall clock.
const checkinTickInterval = 1 * time.Minute

// autoCheckinTimes are the local-time slots the loop targets.
var autoCheckinTimes = []int{9, 21}

// autoCheckinLoop wakes every minute and runs a fleet check-in when the local
// clock crosses one of the configured slots. The lastRun guard ensures each
// slot fires exactly once per day per hour.
func autoCheckinLoop() {
	ticker := time.NewTicker(checkinTickInterval)
	defer ticker.Stop()
	lastRun := make(map[int]int) // hour -> day
	for range ticker.C {
		if !autoCheckinEnabled() {
			continue
		}
		now := time.Now().Local()
		for _, hour := range autoCheckinTimes {
			if now.Hour() == hour && now.Minute() == 0 && lastRun[hour] != now.Day() {
				lastRun[hour] = now.Day()
				go runFleetCheckin("auto")
			}
		}
	}
}

func init() {
	go autoCheckinLoop()
}

// runFleetCheckin iterates every traework account and claims check-in.
func runFleetCheckin(source string) int {
	files, err := hostAuthList()
	if err != nil {
		return 0
	}
	okCount := 0
	for _, f := range files {
		if strings.TrimSpace(f.AuthIndex) == "" || strings.TrimSpace(f.ID) == "" {
			continue
		}
		a, err := hostAuthGet(f.AuthIndex)
		if err != nil || a == nil {
			continue
		}
		res := checkinAccount(a)
		if res.OK {
			okCount++
		}
		// Refresh the credits cache after a successful claim.
		if res.OK && res.Points > 0 {
			cacheCredits(f.ID, &traeCredits{TotalRemain: res.Points, FetchedAt: time.Now().Format(time.RFC3339)})
		}
	}
	return okCount
}

// -----------------------------------------------------------------------------
// Response helpers
// -----------------------------------------------------------------------------

func apiSucceeded(data map[string]any) bool {
	if data == nil {
		return false
	}
	if code, ok := data["code"].(float64); ok {
		if int(code) == 0 || int(code) == 200 {
			return true
		}
	}
	if data["success"] == true {
		return true
	}
	if s, _ := data["status"].(string); s == "success" {
		return true
	}
	return false
}

func msgOf(data map[string]any, fallback string) string {
	for _, k := range []string{"message", "msg"} {
		if s, ok := data[k].(string); ok && s != "" {
			return s
		}
	}
	return fallback
}

func pointsOf(data map[string]any) int64 {
	if d, ok := data["data"].(map[string]any); ok {
		if p := toInt64(d["points"]); p > 0 {
			return p
		}
	}
	if p := toInt64(data["points"]); p > 0 {
		return p
	}
	return 200
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		var n int64
		_, _ = fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
