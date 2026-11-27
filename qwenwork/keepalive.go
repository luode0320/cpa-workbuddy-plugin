// keepalive.go implements proactive token refresh for qwenwork auths.
//
// QwenWork 的凭据是 Device Flow 签发的 dt-/drt- 对：access token 过期后所有
// 额度与推理调用都会 401，refresh token 失效则只能人工重新登录。因此按 4 小时
// 节拍主动续期，把账号维持在线。
//
//   - Runs on the existing schedulerLoop every 4 hours (keepaliveHours).
//   - Iterates all qwenwork auths via host.auth.list/get, calls
//     /api/v1/deviceToken/refresh via the host HTTP bridge (host.http.do).
//   - On success the auth file is persisted via host.auth.save (host watcher
//     reloads it; in-memory host token stays stale until then, which is fine
//     since the old access token typically remains valid for a long time).
//   - On TOKEN_EXPIRE (session dead) the auth is flagged disabled + note
//     prefixed "[SESSION-DEAD]" so it stops receiving traffic until re-login.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// keepaliveHours is the token refresh schedule (local time). Runs every 4 hours.
var keepaliveHours = []int{0, 4, 8, 12, 16, 20}

// keepaliveAuto gates the daily refresh. Default true; configurable via
// plugin config key "token_keepalive" (config_yaml line "token_keepalive: false").
var (
	keepaliveAuto   = true
	keepaliveAutoMu sync.RWMutex
)

func keepaliveEnabled() bool {
	keepaliveAutoMu.RLock()
	defer keepaliveAutoMu.RUnlock()
	return keepaliveAuto
}

// sessionDeadMarkers identify a server-side revoked / expired token.
// QwenWork returns TOKEN_EXPIRE when dt- or drt- is no longer valid.
var sessionDeadMarkers = []string{
	"TOKEN_EXPIRE",
	"12153",
	"Offline user session not found",
}

func isSessionDeadError(msg string) bool {
	for _, m := range sessionDeadMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// isDeviceRefreshFamily 报告 refresh token 是否属于 Device Flow 家族，
// 即能否直接走 POST /api/v1/deviceToken/refresh。
// 实测 drt-（OAuth 设备流）与 ory_（新版网关）两种前缀都可正常刷新并轮换。
// [参数] refreshToken：账号保存的 refresh token。
// [返回] true 表示可走 deviceToken 刷新端点。
// 最近修改时间：2026-11-27；改动原因：纳入 ory_ 家族，修复保活对生产账号全部失效。
func isDeviceRefreshFamily(refreshToken string) bool {
	rt := strings.TrimSpace(refreshToken)
	return strings.HasPrefix(rt, "drt-") || strings.HasPrefix(rt, "ory_")
}

// refreshCall refreshes the token pair for one auth via the Device Flow's own
// refresh endpoint (POST /api/v1/deviceToken/refresh with the drt-/ory_ token).
//
// Returns the decoded data + raw body + status (raw needed for error
// classification — doRawJSON collapses 4xx bodies into a generic error and
// drops the business code, e.g. TOKEN_EXPIRE).
func refreshCall(sa *storedAuth) (json.RawMessage, []byte, int, error) {
	// Device 家族：统一走 deviceToken 刷新端点。路由按 token 前缀判定，
	// 不存在 PAT / jobToken 体系。
	if isDeviceRefreshFamily(sa.Auth.RefreshToken) {
		body, _ := json.Marshal(map[string]string{"refresh_token": sa.Auth.RefreshToken})
		data, status, err := doRawJSON(sharedHTTPClient(), http.MethodPost, upstreamBaseCN+"/api/v1/deviceToken/refresh", nil, bytes.NewReader(body))
		if err == nil {
			return data, data, status, nil
		}
		return nil, nil, status, err
	}
	// 走到这里说明 refreshToken 不属于任何已知可刷新家族或已缺失，
	// 直接报错由调用方降级。
	return nil, nil, 0, fmt.Errorf("refresh: unsupported token family")
}

// refreshOneAuth refreshes the access token for a single qwenwork auth and
// persists the result. Returns a short status string for logging/tests.
func refreshOneAuth(authIndex, authID string) (string, error) {
	sa, err := hostAuthGet(authIndex)
	if err != nil {
		return "error", fmt.Errorf("get auth: %w", err)
	}
	if strings.TrimSpace(sa.Auth.RefreshToken) == "" {
		return "skipped", fmt.Errorf("no refreshToken")
	}

	data, raw, status, err := refreshCall(sa)
	if err != nil {
		msg := string(raw)
		if status == 401 && isSessionDeadError(msg) {
			// Upstream killed the offline session: refresh token is dead and
			// every API call for this account will 401. Flag disabled so the
			// scheduler stops routing traffic to it until manual re-login.
			if derr := markSessionDead(authIndex, authID, sa); derr != nil {
				return "session-dead", fmt.Errorf("session dead; flag failed: %v", derr)
			}
			return "session-dead", fmt.Errorf("session dead (TOKEN_EXPIRE): flagged disabled")
		}
		return "failed", fmt.Errorf("refresh rejected (HTTP %d): %s", status, truncateRedacted(err.Error(), 120))
	}

	// deviceToken refresh 返回 {"device_token":...}（或 "token"）加 RFC3339
	// expires_at；QwenWork 没有 jobToken 体系，只解析这一种形态。
	var dt deviceTokenResponse
	_ = json.Unmarshal(data, &dt)
	tok := dt.accessToken()
	if tok == "" {
		return "failed", fmt.Errorf("refresh_failed: no token in response (raw=%s)", truncateRedacted(string(data), 200))
	}
	// 刷新前的凭证快照：MaaS 缓存键在账号缺 UID 时取自 access token 指纹，
	// token 一旦换新，用新值算键会删不到旧票据。
	staleSA := *sa
	sa.Auth.AccessToken = tok
	if dt.RefreshToken != "" {
		sa.Auth.RefreshToken = dt.RefreshToken
	}
	sa.Auth.ExpiresAt = preserveExpiry(deviceExpiryUnix(&dt), sa.Auth.ExpiresAt)
	if err := persistAuthTokens(authIndex, sa); err != nil {
		return "error", fmt.Errorf("persist: %w", err)
	}
	// 宿主 token 换了，缓存的 MaaS 票据也要作废：MaaS key 是绑在宿主
	// 身份上的，继续用旧票会 401。
	invalidateForRefresh(&staleSA, sa)
	return "refreshed", nil
}

// invalidateForRefresh 用刷新前后的两份凭证清理 MaaS 缓存。
// MaaS 缓存键在账号缺 UID 时取自 access token 指纹：只按刷新后的凭证清理
// 会漏删旧票据，因此必须同时清旧快照与新凭证（UID 存在时两者同键，幂等）。
// [参数] stale：刷新前的凭证快照；fresh：刷新后的凭证。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：修复 token 轮换后旧票据泄漏。
func invalidateForRefresh(stale, fresh *storedAuth) {
	invalidateMaasSession(stale)
	invalidateMaasSession(fresh)
}

// persistAuthTokens 把刷新后的凭据写回磁盘。
//
// 1. 走物理直写而不是 host.auth.save：后者只接受插件给出的整份 JSON，
// 重建 payload 会丢掉任何未显式搬运的顶层字段。
// 2. 折叠而非重建：以磁盘现有文档为基线，只替换 auth/account 两棵子树，
// 其余顶层字段（disabled / note / manual_disable / exhausted_disable /
// success_count / daily_cooling_until …）原样保留。
//
// [参数] authIndex 宿主 auth 索引；sa 刷新后的账号凭据。
// [返回] 落盘成功返回 nil。
// 最近修改时间：2026-10-18 01:50:00；改动原因：改为保留式折叠，修复刷新丢顶层字段。
func persistAuthTokens(authIndex string, sa *storedAuth) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	name := phys.Name
	if name == "" {
		name = authFileNameFor(sa)
	}
	raw, err := foldCredentialIntoDoc(phys.JSON, sa)
	if err != nil {
		return err
	}
	return persistAuthDirect(name, phys.Path, "", raw)
}

// foldCredentialIntoDoc 把刷新后的凭据折叠进现有 auth 文档。
//
// 只覆盖 auth 与 account 两棵子树，其余顶层字段全部保留；这是「刷新令牌
// 不应改变账号运营状态」的直接表达。
//
// [参数] base 磁盘上的原始 auth JSON；sa 刷新后的账号凭据。
// [返回] 折叠后的 JSON；base 无法解析时返回错误而不是静默重建。
// 最近修改时间：2026-10-18 01:50:00；新增函数。
func foldCredentialIntoDoc(base []byte, sa *storedAuth) ([]byte, error) {
	if sa == nil {
		return nil, fmt.Errorf("nil storedAuth")
	}
	var doc map[string]any
	if err := json.Unmarshal(base, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("parse existing auth json: %w", err)
	}
	storage, err := json.Marshal(sa)
	if err != nil {
		return nil, err
	}
	var nested map[string]any
	if err := json.Unmarshal(storage, &nested); err != nil {
		return nil, err
	}
	doc["auth"] = nested["auth"]
	doc["account"] = nested["account"]
	return json.Marshal(doc)
}

// markSessionDead logs the session-dead condition but does NOT write
// disabled:true — the user explicitly requested that only manual panel
// toggle controls the disabled flag. Auto-failover routing handles dead
// accounts via the failure cooldown (laddered 60s..10min) without needing
// disabled:true.
//
// [参数] authIndex 宿主 auth 索引；authID 宿主 auth.ID（仅用于日志）；
// sa 账号凭据（用于兜底文件名）。
// [返回] 落盘成功返回 nil。
// 最近修改时间：2026-10-18 01:52:00；改动原因：改直写，避免 host.auth.save 丢顶层字段。
func markSessionDead(authIndex, authID string, sa *storedAuth) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	if phys.Disabled {
		return nil // already disabled; nothing to do
	}
	log.Printf("[keepalive] session dead for %s (auth_index=%s): refresh token expired, not auto-disabling (manual-toggle-only policy)", authID, authIndex)
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil {
		return err
	}
	doc["note"] = "Session dead (TOKEN_EXPIRE): re-login required"
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name := phys.Name
	if name == "" {
		name = authFileNameFor(sa)
	}
	return persistAuthDirect(name, phys.Path, "", raw)
}

// keepaliveSummary is one row of the daily run, surfaced via the
// management route for observability.
type keepaliveSummary struct {
	When    time.Time      `json:"when"`
	Results []keepaliveRow `json:"results"`
}

type keepaliveRow struct {
	AuthIndex string `json:"auth_index"`
	Nickname  string `json:"nickname,omitempty"`
	Region    string `json:"region"`
	Status    string `json:"status"` // refreshed | skipped | failed | session-dead | error
	Detail    string `json:"detail,omitempty"`
}

var (
	lastKeepaliveMu sync.RWMutex
	lastKeepalive   *keepaliveSummary
)

func recordKeepalive(s *keepaliveSummary) {
	lastKeepaliveMu.Lock()
	lastKeepalive = s
	lastKeepaliveMu.Unlock()
}

func getLastKeepalive() *keepaliveSummary {
	lastKeepaliveMu.RLock()
	defer lastKeepaliveMu.RUnlock()
	return lastKeepalive
}

// runTokenKeepalive refreshes every qwenwork auth once. Returns the summary.
func runTokenKeepalive() *keepaliveSummary {
	sum := &keepaliveSummary{When: time.Now()}
	if !keepaliveEnabled() {
		return sum
	}
	files, err := hostAuthList()
	if err != nil {
		sum.Results = append(sum.Results, keepaliveRow{Status: "error", Detail: err.Error()})
		recordKeepalive(sum)
		return sum
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 2)
	var mu sync.Mutex
	for _, f := range files {
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			row := keepaliveRow{AuthIndex: f.AuthIndex}
			// Cheap pre-read for nickname/region; refreshOneAuth re-reads anyway
			// but the row should be populated even when refresh errors early.
			if sa, err := hostAuthGet(f.AuthIndex); err == nil {
				row.Nickname = sa.Account.Nickname
				row.Region = "cn"
			}
			status, err := refreshOneAuth(f.AuthIndex, f.ID)
			row.Status = status
			if err != nil {
				row.Detail = truncateRedacted(err.Error(), 200)
			}
			mu.Lock()
			sum.Results = append(sum.Results, row)
			mu.Unlock()
		}()
	}
	wg.Wait()
	recordKeepalive(sum)
	return sum
}

// shouldRunKeepaliveNow reports whether the current local time is within
// one hour after any scheduled keepalive hour today. Used by schedulerLoop
// to decide whether the tick should fire a refresh.
func shouldRunKeepaliveNow(now time.Time) bool {
	for _, h := range keepaliveHours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		// Within [t, t+1h) window.
		if !now.Before(t) && now.Before(t.Add(time.Hour)) {
			return true
		}
	}
	return false
}

// handleKeepaliveNow triggers a manual refresh (all accounts, or one when the
// body carries auth_index). Manual runs ignore the token_keepalive toggle —
// the toggle gates only the scheduled auto-run.
func handleKeepaliveNow(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		sum := runTokenKeepalive()
		return map[string]any{"when": sum.When, "results": sum.Results}
	}
	sa, err := hostAuthGet(authIndex)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	row := keepaliveRow{AuthIndex: authIndex, Nickname: sa.Account.Nickname, Region: "cn"}
	row.Status, err = refreshOneAuth(authIndex, "")
	if err != nil {
		row.Detail = truncateRedacted(err.Error(), 200)
	}
	return map[string]any{"when": time.Now(), "results": []keepaliveRow{row}}
}

// handleKeepaliveStatus returns the last scheduled-run summary plus config.
func handleKeepaliveStatus() map[string]any {
	return map[string]any{
		"enabled":  keepaliveEnabled(),
		"schedule": keepaliveHours,
		"last_run": getLastKeepalive(),
	}
}
