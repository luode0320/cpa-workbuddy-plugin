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
// different devices (trae-check verified strategy). An empty base device
// must NOT yield a leading-dash id ("-<uid>") — return the uid directly.
func deviceIDFor(baseDeviceID, userID string) string {
	switch {
	case baseDeviceID != "" && userID != "":
		return baseDeviceID + "-" + userID
	case baseDeviceID != "":
		return baseDeviceID
	default:
		return userID
	}
}

// checkinUserAgent mimics the Trae Work web client. Requests leaving the
// host bridge carry Go's default "Go-http-client/1.1" UA, which Trae's
// activity WAF throttles aggressively (observed 2026-08-30: 16 consecutive
// 9074 "当前参与用户太多" rejections over 20 minutes on the claim endpoint
// while same-parameter probes with a browser UA succeeded instantly; the
// points endpoint is unaffected). Sending the UA the real check-in page
// uses avoids that penalty box.
const checkinUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

func checkinAuthHeaders(a *traeAuth, deviceID string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Cloud-IDE-JWT "+a.Token)
	h.Set("x-device-id", deviceID)
	h.Set("User-Agent", checkinUserAgent)
	h.Set("Origin", "https://work.trae.cn")
	h.Set("Referer", "https://work.trae.cn/")
	return h
}

// isDefiniteHTTPFailure reports whether the bridge status code is a definite
// non-200. Status 0 means the bridge status was undecodable (host wire drift)
// — Trae answers HTTP 200 with a business code inside the JSON body, so an
// undecodable status with a non-empty body must NOT be treated as failure
// (that bug broke check-in and points on Linux while Windows direct calls
// kept working, hiding it in dev).
func isDefiniteHTTPFailure(status int) bool {
	return status != http.StatusOK && status != 0
}

// isBusyThrottleMsg matches Trae's transient peak-hour throttling message
// (business code 9074: "当前参与用户太多，请稍后再试").
func isBusyThrottleMsg(msg string) bool {
	return strings.Contains(msg, "太多") || strings.Contains(msg, "稍后再试")
}

// checkinAccount performs one claim for a parsed account.
func checkinAccount(a *traeAuth) checkinResult {
	if a == nil || !a.hasToken() {
		return checkinResult{OK: false, Message: "no credential"}
	}
	host := a.checkinHost()
	deviceID := deviceIDFor(a.DeviceID, a.UserID)
	// One deferred retry absorbs Trae's transient peak-hour throttle window.
	for attempt := 0; ; attempt++ {
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
		if isDefiniteHTTPFailure(resp.StatusCode) {
			return checkinResult{OK: false, Message: fmt.Sprintf("HTTP %d %s", resp.StatusCode, truncateRedacted(body, 160))}
		}
		if len(body) == 0 {
			return checkinResult{OK: false, Message: fmt.Sprintf("HTTP %d empty response", resp.StatusCode)}
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
		if attempt == 0 && isBusyThrottleMsg(msg) {
			time.Sleep(3 * time.Second)
			continue
		}
		return checkinResult{OK: false, Message: msg}
	}
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
	if isDefiniteHTTPFailure(resp.StatusCode) {
		return 0, fmt.Errorf("points HTTP %d: %s", resp.StatusCode, truncateRedacted(string(raw), 120))
	}
	if len(raw) == 0 {
		return 0, fmt.Errorf("points HTTP %d: empty response", resp.StatusCode)
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
// Returns the success count plus per-account results so the panel can show
// WHY an account failed (throttle / device block / credential problems)
// instead of a bare "成功 0 个". Retryable failures are pushed into the
// persistent retry queue (1-minute cadence, up to checkinRetryMax attempts);
// the third return value counts accounts newly added to that queue.
func runFleetCheckin(source string) (int, []map[string]any, int) {
	files, err := hostAuthList()
	if err != nil {
		return 0, nil, 0
	}
	okCount := 0
	scheduled := 0
	results := make([]map[string]any, 0, len(files))
	for _, f := range files {
		if strings.TrimSpace(f.AuthIndex) == "" || strings.TrimSpace(f.ID) == "" {
			continue
		}
		a, err := hostAuthGet(f.AuthIndex)
		if err != nil || a == nil {
			results = append(results, map[string]any{"auth_id": f.ID, "uid": a2UID(a), "ok": false, "message": "凭据加载失败"})
			continue
		}
		res := checkinAccount(a)
		if res.OK {
			okCount++
			// A late success (retry queue or earlier failed run) clears
			// any pending retry entry for this account.
			cancelCheckinRetry(f.AuthIndex)
		} else if scheduleCheckinRetry(f.AuthIndex, f.ID, a.UserID, res.Message) {
			scheduled++
			results = append(results, map[string]any{
				"auth_id": f.ID, "uid": a.UserID, "ok": false, "message": res.Message,
				"retry_scheduled": true,
			})
			continue
		}
		results = append(results, map[string]any{"auth_id": f.ID, "uid": a.UserID, "ok": res.OK, "message": res.Message})
		// Refresh the credits cache after a successful claim. Use a live
		// accountPoints query — res.Points is THIS checkin's reward (could be
		// 200), NOT the account's total remaining quota. Writing the reward
		// as TotalRemain would corrupt the panel and leave it pinned to the
		// last check-in reward amount.
		if res.OK {
			if remain, qerr := accountPoints(a); qerr == nil {
				cacheCredits(f.ID, &traeCredits{TotalRemain: remain, FetchedAt: time.Now().Format(time.RFC3339)})
			}
		}
	}
	return okCount, results, scheduled
}

// a2UID safely reads the UserID of a possibly-nil auth (fleet error entries).
func a2UID(a *traeAuth) string {
	if a == nil {
		return ""
	}
	return a.UserID
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
