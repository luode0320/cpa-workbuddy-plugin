// checkin.go implements Trae Work daily check-in and points queries, ported
// from the verified prototype (trae-gateway-go/internal/checkin, itself a
// port of trae-check electron/checkin.ts), plus the auto check-in loop
// (every 4 hours local time).
package main

import (
	"bytes"
	"crypto/sha256"
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
	Already bool   `json:"already,omitempty"`
}

// isValidCheckinDeviceID 校验设备标识是否符合客户端 16 位数字号段格式。
// [参数] id: 待校验的设备标识字符串。
// [返回] bool: 符合格式返回 true，否则返回 false。
// 最近修改时间：2026-10-10 22:00:00；改动原因：新增客户端 16 位数字设备标识格式校验。
func isValidCheckinDeviceID(id string) bool {
	// 1. 校验固定 16 位长度、首位 1~3 号段与非连续尾零后缀
	if len(id) != 16 || id[0] < '1' || id[0] > '3' || strings.HasSuffix(id, "0000") {
		return false
	}
	// 2. 校验全量字符均为十进制数字
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

// deriveCheckinDeviceID 根据种子字符串确定性派生 16 位纯数字设备标识。
// [参数] seed: 用于派生的账号与设备种子字符串。
// [返回] string: 16 位纯数字设备标识。
// 最近修改时间：2026-10-10 22:00:00；改动原因：新增账号级确定性 16 位数字设备标识派生。
func deriveCheckinDeviceID(seed string) string {
	// 1. 计算种子 SHA-256 摘要
	sum := sha256.Sum256([]byte(seed))
	var buf [16]byte
	// 2. 映射首位为 1~3、中间位为 0~9、末位为 1~9 的 16 位数字
	buf[0] = '1' + (sum[0] % 3)
	for i := 1; i < 15; i++ {
		buf[i] = '0' + (sum[i] % 10)
	}
	buf[15] = '1' + (sum[15] % 9)
	return string(buf[:])
}

// deviceIDFor 生成单账号签到使用的 16 位纯数字 x-device-id。
// [参数] baseDeviceID: 凭据中的原始设备标识；userID: 账号唯一标识。
// [返回] string: 账号对应的 16 位纯数字设备标识，输入均为空时返回空串。
// 最近修改时间：2026-10-10 22:00:00；改动原因：改为生成合规 16 位数字设备标识以消除拼接与尾零风控拦截。
func deviceIDFor(baseDeviceID, userID string) string {
	// 1. 清理输入空白并处理全空边界
	baseDeviceID = strings.TrimSpace(baseDeviceID)
	userID = strings.TrimSpace(userID)
	if baseDeviceID == "" && userID == "" {
		return ""
	}
	// 2. 仅提供合规基础设备号或合规用户号时直接复用
	if userID == "" && isValidCheckinDeviceID(baseDeviceID) {
		return baseDeviceID
	}
	if baseDeviceID == "" && isValidCheckinDeviceID(userID) {
		return userID
	}
	// 3. 其余场景按组合种子确定性派生 16 位纯数字设备标识
	return deriveCheckinDeviceID(baseDeviceID + ":" + userID)
}

// checkinUserAgent mimics the Trae client HTTP User-Agent.
const checkinUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// checkinClaimRequest 定义每日签到领取接口的请求体结构。
type checkinClaimRequest struct {
	ReqSource int `json:"req_source"`
}

// checkinAuthHeaders 构造签到与额度查询请求头。
// [参数] a: 已解析的 Trae 账号凭据；deviceID: 请求携带的设备标识。
// [返回] http.Header: 签到与额度接口使用的 HTTP 请求头。
// 最近修改时间：2026-10-10 22:00:00；改动原因：规范签到请求头构造与函数头元信息。
func checkinAuthHeaders(a *traeAuth, deviceID string) http.Header {
	// 1. 初始化基础请求头与认证凭据
	cfg := loadedConfig()
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if a != nil && a.Token != "" {
		h.Set("Authorization", "Cloud-IDE-JWT "+a.Token)
	}
	// 2. 注入客户端设备指纹与应用版本头
	if deviceID != "" {
		h.Set("x-device-id", deviceID)
	}
	if cfg.AppID != "" {
		h.Set("x-app-id", cfg.AppID)
	}
	if cfg.DeviceModel != "" {
		h.Set("x-device-brand", cfg.DeviceModel)
	}
	if cfg.OSName != "" {
		h.Set("x-device-type", cfg.OSName)
	}
	if cfg.OSVersion != "" {
		h.Set("x-os-version", cfg.OSVersion)
	}
	if v := ideVersion(); v != "" {
		h.Set("x-app-version", v)
	}
	h.Set("User-Agent", checkinUserAgent)
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

// checkinMaxAttempts 限制单次签到调用的最大尝试次数。
const checkinMaxAttempts = 4

// checkinAccount 执行单个账号的每日签到领取并在设备冲突或限流时轮换设备号重试。
// [参数] a: 已解析的 Trae 账号凭据。
// [返回] checkinResult: 签到执行结果。
// 最近修改时间：2026-10-10 22:00:00；改动原因：遇到 9074 限流或设备拦截时自动切换新 16 位数字设备号重试。
func checkinAccount(a *traeAuth) checkinResult {
	// 1. 校验账号凭据有效性并准备初始请求参数
	if a == nil || !a.hasToken() {
		return checkinResult{OK: false, Message: "no credential"}
	}
	host := a.checkinHost()
	deviceID := deviceIDFor(a.DeviceID, a.UserID)
	claimPayload, _ := json.Marshal(checkinClaimRequest{ReqSource: 2})
	// 2. 发起签到请求并在 9074 限流或设备去重冲突时轮换设备号重试
	for attempt := 0; attempt < checkinMaxAttempts; attempt++ {
		if attempt > 0 {
			deviceID = randomDeviceID()
		}
		req, err := http.NewRequest(http.MethodPost, host+claimPath, bytes.NewReader(claimPayload))
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
			return checkinResult{OK: true, Message: "今日已签到", Already: true}
		}
		if DeviceBlocked(msg) {
			if attempt+1 < checkinMaxAttempts {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			return checkinResult{OK: false, Message: msg + "（设备级拦截，稍后重试）"}
		}
		if isBusyThrottleMsg(msg) && attempt+1 < checkinMaxAttempts {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		return checkinResult{OK: false, Message: msg}
	}
	return checkinResult{OK: false, Message: "签到失败"}
}

// accountCredits queries the account's entitlement list and returns the full
// quota snapshot (remain / used / size / pack count) for the panel progress
// bar. Same upstream response as accountPoints — richer decode only.
func accountCredits(a *traeAuth) (*traeCredits, error) {
	if a == nil || !a.hasToken() {
		return nil, fmt.Errorf("no credential")
	}
	host := a.checkinHost()
	body, _ := json.Marshal(map[string]bool{"require_usage": true})
	req, err := http.NewRequest(http.MethodPost, host+pointsPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = checkinAuthHeaders(a, a.DeviceID)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	raw := resp.Body
	if isDefiniteHTTPFailure(resp.StatusCode) {
		return nil, fmt.Errorf("points HTTP %d: %s", resp.StatusCode, truncateRedacted(string(raw), 120))
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("points HTTP %d: empty response", resp.StatusCode)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	remain, used, size, packs := extractCreditsDetail(data)
	return &traeCredits{
		TotalRemain: remain,
		TotalUsed:   used,
		TotalSize:   size,
		PackCount:   packs,
		FetchedAt:   time.Now().Format(time.RFC3339),
	}, nil
}

// accountPoints queries the account's remaining credits and caches them.
func accountPoints(a *traeAuth) (int64, error) {
	cr, err := accountCredits(a)
	if err != nil {
		return 0, err
	}
	return cr.TotalRemain, nil
}

// extractCreditsDetail sums entitlement packs into (remain, used, size, pack
// count): per pack, credits_limit is the granted quota and usage.credits_amount
// the consumed part, so remain = limit - used (clamped at 0).
func extractCreditsDetail(data map[string]any) (int64, int64, int64, int) {
	packs, _ := data["user_entitlement_pack_list"].([]any)
	var remain, used, size int64
	count := 0
	for _, p := range packs {
		pack, _ := p.(map[string]any)
		base, _ := pack["entitlement_base_info"].(map[string]any)
		quota, _ := base["quota"].(map[string]any)
		limit := toInt64(quota["credits_limit"])
		if limit <= 0 {
			continue
		}
		usage, _ := pack["usage"].(map[string]any)
		packUsed := toInt64(usage["credits_amount"])
		remain += maxInt64(limit-packUsed, 0)
		used += packUsed
		size += limit
		count++
	}
	return remain, used, size, count
}

// extractRemainingCredits sums entitlement packs: credits_limit - credits_amount.
func extractRemainingCredits(data map[string]any) int64 {
	remain, _, _, _ := extractCreditsDetail(data)
	return remain
}

// -----------------------------------------------------------------------------
// Today-checked-in state (panel badge)
// -----------------------------------------------------------------------------

// checkinDoneMu/checkinDone record the local wall-clock date of the last
// successful claim (fresh or already) per auth_index so the panel can render
// a disabled "已签到" button. In-memory only: after a restart the badge is
// unknown until the next claim (the upstream answers "今日已签到" idempotently,
// which re-records it) — acceptable for a UI hint, no upstream contract change.
var (
	checkinDoneMu sync.Mutex
	checkinDone   = map[string]string{} // auth_index -> "2006-01-02"
)

func markCheckinDoneToday(authIndex string) {
	if strings.TrimSpace(authIndex) == "" {
		return
	}
	checkinDoneMu.Lock()
	checkinDone[authIndex] = time.Now().Format("2006-01-02")
	checkinDoneMu.Unlock()
}

func checkinDoneToday(authIndex string) bool {
	checkinDoneMu.Lock()
	defer checkinDoneMu.Unlock()
	return checkinDone[authIndex] == time.Now().Format("2006-01-02")
}

// -----------------------------------------------------------------------------
// Auto check-in scheduler (every 4 hours local)
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

// autoCheckinTimes are the local-time slots the loop targets (every 4 hours).
var autoCheckinTimes = []int{0, 4, 8, 12, 16, 20}

// nextAutoCheckinTime returns the earliest scheduled check-in slot strictly
// after now (local time). Slots already passed today roll over to tomorrow.
// Mirrors workbuddy/checkin.go nextCheckinTime: scheduling is anchored to an
// ABSOLUTE clock time so the timer can align to the exact slot instead of
// polling a fragile "current minute == 0" window.
//
// [参数] now: 用于定位当前本地时间的参考时刻。
// [返回] 严格晚于 now 的最近一个签到槽位绝对时间。
// 最近修改时间：2026-10-10；改动原因：修复自动签到 ticker 相位错配导致永不触发。
func nextAutoCheckinTime(now time.Time) time.Time {
	var earliest time.Time
	for _, h := range autoCheckinTimes {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour) // 槽位今天已过 → 顺延到明天
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// autoCheckinLoop 在到达每个签到槽位（本地时间 00/04/08/12/16/20 整点）时
// 执行一次全量签到。使用「计算下一个槽位绝对时间 + time.Timer 对齐」的方式，
// 保证到点触发与进程启动时刻无关——旧实现用 time.NewTicker 每 60 秒唤醒并
// 要求 now.Minute()==0，ticker 相位由 init 时刻锚定，只有启动秒相位恰为 0
// 时才命中，导致自动签到几乎永不触发（2026-10-10 生产实证）。
func autoCheckinLoop() {
	for {
		next := nextAutoCheckinTime(time.Now().Local())
		timer := time.NewTimer(time.Until(next))
		<-timer.C
		if autoCheckinEnabled() {
			go runFleetCheckin("auto")
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
			markCheckinDoneToday(f.AuthIndex)
		} else if scheduleCheckinRetry(f.AuthIndex, f.ID, a.UserID, res.Message) {
			scheduled++
			results = append(results, map[string]any{
				"auth_id": f.ID, "auth_index": f.AuthIndex, "uid": a.UserID,
				"nickname": a.Nickname, "ok": false, "message": res.Message,
				"retry_scheduled": true,
			})
			continue
		}
		results = append(results, map[string]any{
			"auth_id": f.ID, "auth_index": f.AuthIndex, "uid": a.UserID,
			"nickname": a.Nickname, "ok": res.OK, "already": res.Already,
			"message": res.Message, "points": res.Points,
		})
		// Refresh the credits cache after a successful claim. Use a live
		// accountPoints query — res.Points is THIS checkin's reward (could be
		// 200), NOT the account's total remaining quota. Writing the reward
		// as TotalRemain would corrupt the panel and leave it pinned to the
		// last check-in reward amount.
		if res.OK {
			if cr, cerr := accountCredits(a); cerr == nil {
				cacheCredits(f.ID, cr)
				// 4h recovery/disable hook: the fresh snapshot feeds the
				// exhausted lifecycle — an account with the exhausted_disable
				// marker whose credits recovered (>0) is re-enabled in the
				// same cycle; a newly exhausted account is auto-disabled.
				reconcileAfterCreditsRefresh(f.AuthIndex, f.ID)
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
