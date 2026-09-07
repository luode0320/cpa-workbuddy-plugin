// keepalive.go implements proactive daily token refresh for qoderwork auths.
//
// Motivation: upstream (openapi.qoder.com.cn) issues jobTokens (24h) and
// refreshTokens (48h). When they expire mid-flight, every billing and
// inference call is rejected with TOKEN_EXPIRE. Refreshing daily (plus
// PAT re-exchange fallback) keeps accounts alive automatically.
//
//   - Runs on the existing schedulerLoop every 4 hours (keepaliveHours is
//     separate from checkinHours so the two cadences can evolve independently).
//     separate from checkinHours so the two cadences can evolve independently).
//   - Iterates all qoderwork auths via host.auth.list/get, calls
//     {realm-base}/v2/plugin/auth/token/refresh with X-Refresh-Token via
//     the host HTTP bridge (host.http.do).
//   - On success the auth file is persisted via host.auth.save (host watcher
//     reloads it; in-memory host token stays stale until then, which is fine
//     since the old access token typically remains valid for a long time).
//   - On 12153 (session dead) the auth is flagged disabled + note prefixed
//     "[SESSION-DEAD]" so it stops receiving traffic until manual re-login.
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

// keepaliveHours is the token refresh schedule (local time). Runs every 4 hours
// alongside the checkin schedule so the two cadences stay aligned.
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
// QoderWork returns TOKEN_EXPIRE when jt- or jrt- is no longer valid.
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

// refreshCall refreshes the token pair for one auth, routing by credential
// family:
//   - device-token accounts (PersonalToken == "", drt- refresh token):
//     POST /api/v1/deviceToken/refresh — the OAuth device flow's own endpoint.
//   - legacy PAT accounts: POST /api/v1/jobToken/refresh with jrt-, falling
//     back to jobToken/exchange with the PAT when the jrt- has expired.
//
// Returns the decoded data + raw body + status (raw needed for error
// classification — doRawJSON collapses 4xx bodies into a generic error and
// drops the business code, e.g. TOKEN_EXPIRE).
func refreshCall(sa *storedAuth) (json.RawMessage, []byte, int, error) {
	// Device family (drt-): use the device flow's own refresh endpoint.
	// Routing is by token prefix, not PersonalToken presence — a PAT may
	// coexist as fallback and must not hijack OAuth refreshes.
	if strings.HasPrefix(sa.Auth.RefreshToken, "drt-") {
		body, _ := json.Marshal(map[string]string{"refresh_token": sa.Auth.RefreshToken})
		data, status, err := doRawJSON(sharedHTTPClient(), http.MethodPost, upstreamBaseCN+"/api/v1/deviceToken/refresh", nil, bytes.NewReader(body))
		if err == nil {
			return data, data, status, nil
		}
		return nil, nil, status, err
	}
	// Legacy PAT family: try jrt- refresh first.
	body, _ := json.Marshal(map[string]string{"refresh_token": sa.Auth.RefreshToken})
	data, status, err := doRawJSON(sharedHTTPClient(), http.MethodPost, endpointJobTokenRefresh, nil, bytes.NewReader(body))
	if err == nil {
		return data, data, status, nil
	}
	// Fallback: PAT re-exchange (only when a PAT is actually present).
	if sa.Auth.PersonalToken != "" {
		patBody, _ := json.Marshal(map[string]string{"personal_token": sa.Auth.PersonalToken})
		data2, status2, err2 := doRawJSON(sharedHTTPClient(), http.MethodPost, endpointJobTokenExchange, nil, bytes.NewReader(patBody))
		if err2 == nil {
			return data2, data2, status2, nil
		}
	}
	return nil, nil, status, err
}

// refreshOneAuth refreshes the access token for a single qoderwork auth and
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

	// Parse both response shapes: jobToken refresh returns {"token":...},
	// deviceToken refresh returns {"device_token":...} (or "token") plus an
	// RFC3339 expires_at instead of expires_in.
	var tok jobTokenResponse
	_ = json.Unmarshal(data, &tok)
	expiry := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Millisecond).Unix()
	if tok.Token == "" {
		var dt deviceTokenResponse
		_ = json.Unmarshal(data, &dt)
		tok.Token = dt.accessToken()
		tok.RefreshToken = dt.RefreshToken
		expiry = deviceExpiryUnix(&dt)
	}
	if tok.Token == "" {
		return "failed", fmt.Errorf("refresh_failed: no token in response (raw=%s)", truncateRedacted(string(data), 200))
	}
	sa.Auth.AccessToken = tok.Token
	if tok.RefreshToken != "" {
		sa.Auth.RefreshToken = tok.RefreshToken
	}
	sa.Auth.ExpiresAt = preserveExpiry(expiry, sa.Auth.ExpiresAt)
	if err := persistAuthTokens(authIndex, sa); err != nil {
		return "error", fmt.Errorf("persist: %w", err)
	}
	// Invalidate cached COSY session so subsequent requests use the new
	// access token. Without this, the old jt- signature persists and all
	// inference requests fail with 401 until process restart.
	invalidateCosySession(sa.Account.UID)
	return "refreshed", nil
}

// persistAuthTokens writes the updated credential back through the host API.
// The host's file watcher reloads it; we deliberately do NOT dual-write the
// physical path (same rule as hostAuthPersist).
//
// MUST go through buildAuthFileJSON with the CURRENT top-level fields from
// the physical file — a bare json.Marshal(sa) would drop type/provider/logo/
// disabled/note, resurrecting accounts that lifecycle disabled (P0: a keepalive
// keepalive refresh used to wipe disabled:true and put the account back into
// rotation).
func persistAuthTokens(authIndex string, sa *storedAuth) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	name := phys.Name
	if name == "" {
		name = authFileNameFor(sa)
	}
	// Carry over the note currently on disk (lifecycle writes credit/status
	// notes there; dropping it would regress the panel display).
	note := ""
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err == nil {
		if s, ok := doc["note"].(string); ok {
			note = s
		}
	}
	raw, err := buildAuthFileJSON(sa, phys.Disabled, note, nil)
	if err != nil {
		return err
	}
	return hostAuthSaveJSON(name, raw)
}

// markSessionDead logs the session-dead condition but does NOT write
// disabled:true — the user explicitly requested that only manual panel
// toggle controls the disabled flag. Auto-failover routing handles dead
// accounts via the failure cooldown (fixed 15s) without needing
// disabled:true.
func markSessionDead(authIndex, authID string, sa *storedAuth) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
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
	return hostAuthSaveJSON(name, raw)
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

// runTokenKeepalive refreshes every qoderwork auth once. Returns the summary.
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
	sem := make(chan struct{}, 4)
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
// to fire keepalive on the same tick as checkin when the schedules coincide.
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
