// keepalive.go implements proactive token refresh for traework auths.
//
// Motivation: Trae access tokens carry an expiredAt; when the token passes it,
// every chat / points call for the account fails auth (the upstream returns a
// 4xx instead of a completion). The Trae OAuth flow rotates tokens via the
// cloudide ExchangeToken endpoint — the same endpoint the reference gateway
// (F:\trae-proto\trae-gateway-go\internal\credential\refresh.go) uses. This
// module refreshes the access token before it expires so a long-lived account
// keeps working without a manual storage.json re-import.
//
// Design:
//   - Runs every 4 hours (keepaliveHours: 0,4,8,12,16,20), checking
//     each auth whose ExpiredAt is within keepaliveLeadWindow (24h) or past.
//   - POST {host}/cloudide/api/v3/trae/oauth/ExchangeToken with the stored
//     refresh token via the host HTTP bridge (host.http.do).
//   - On success the runtime fields (token/refreshToken/expiredAt/
//     refreshExpiredAt) are persisted at the TOP LEVEL of the physical auth
//     file via persistAuthDirect — keepalive never rewrites the client-
//     encrypted credential blob (see parseTraeAuth priority note in
//     credential.go: top-level runtime fields win over the blob).
//   - On a 4xx refresh rejection (refresh token dead) the auth is flagged
//     disabled + note "Session expired: re-login required" so it stops
//     receiving traffic until manual re-import.
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

// keepaliveLeadWindow is how far ahead of expiry we refresh. An account whose
// token expires within this window (or already expired) gets refreshed.
const keepaliveLeadWindow = 24 * time.Hour

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

func setKeepaliveEnabled(on bool) {
	keepaliveAutoMu.Lock()
	keepaliveAuto = on
	keepaliveAutoMu.Unlock()
}

// OAuth client identity for the cloudide ExchangeToken endpoint — the shared
// client id/secret from the reference gateway (all Trae SOLO clients share
// these; the secret is the literal "-" placeholder).
const (
	oauthClientID     = "ono9krqynydwx5"
	oauthClientSecret = "-"
	exchangeTokenPath = "/cloudide/api/v3/trae/oauth/ExchangeToken"
)

// tokenExpiry parses an RFC3339 expiredAt and returns the expiry time.
// ok=false when the field is empty or malformed.
func tokenExpiry(expiredAt string) (time.Time, bool) {
	expiredAt = strings.TrimSpace(expiredAt)
	if expiredAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, expiredAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// needsKeepalive reports whether the auth's token needs refreshing: no
// parseable expiry (unknown → be conservative) or expiry within the lead
// window. No refresh token → skipped by the caller.
func needsKeepalive(sa *traeAuth) bool {
	exp, ok := tokenExpiry(sa.ExpiredAt)
	if !ok {
		return true // unparseable expiry: refresh to be safe
	}
	return time.Now().Add(keepaliveLeadWindow).After(exp)
}

// Test seam: keepaliveExchange routes its upstream call through this var so
// tests can capture the outbound request (URL/auth host) without hitting the
// network — hostHTTPDo itself falls back to a real direct HTTP call when the
// host bridge is unavailable (unit tests).
var hostHTTPDoFn = hostHTTPDo

// keepaliveExchange calls the cloudide ExchangeToken endpoint with the stored
// refresh token and returns the rotated token envelope. The auth host is
// FIXED to defaultAPIHost (api.trae.cn): the ExchangeToken route lives there
// (verified 2026-09-06 — TLB 404 on the chat host trae-api-cn.mchost.guru),
// and reusing sa.Host (a chat/account host) is what caused the 2026-09-05
// keepalive mass-disable incident. The refresh token is sent as-is (no extra
// headers — the endpoint authenticates via the body).
func keepaliveExchange(sa *traeAuth) (*traeCredential, error) {
	host := defaultAPIHost
	body, _ := json.Marshal(map[string]string{
		"ClientID":     oauthClientID,
		"RefreshToken": strings.TrimSpace(sa.RefreshToken),
		"ClientSecret": oauthClientSecret,
		"UserID":       strings.TrimSpace(sa.UserID),
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(host, "/")+exchangeTokenPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hostHTTPDoFn(req)
	if err != nil {
		return nil, err
	}
	raw := resp.Body
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ExchangeToken: HTTP %d %s", resp.StatusCode, truncateRedacted(string(raw), 160))
	}
	var out struct {
		Token            string `json:"token"`
		RefreshToken     string `json:"refreshToken"`
		ExpiredAt        string `json:"expiredAt"`
		RefreshExpiredAt string `json:"refreshExpiredAt"`
		TokenReleaseAt   string `json:"tokenReleaseAt"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ExchangeToken decode: %w", err)
	}
	if out.Token == "" {
		return nil, fmt.Errorf("ExchangeToken returned no token")
	}
	return &traeCredential{
		Token:            out.Token,
		RefreshToken:     out.RefreshToken,
		ExpiredAt:        out.ExpiredAt,
		RefreshExpiredAt: out.RefreshExpiredAt,
		TokenReleaseAt:   out.TokenReleaseAt,
	}, nil
}

// refreshOneAuth refreshes the access token for a single traework auth and
// persists the runtime fields. Returns a short status string for logs/tests:
// refreshed | skipped | failed | session-dead | error.
func refreshOneAuth(authIndex, authID string) (string, error) {
	sa, err := hostAuthGet(authIndex)
	if err != nil {
		return "error", fmt.Errorf("get auth: %w", err)
	}
	if strings.TrimSpace(sa.RefreshToken) == "" {
		return "skipped", fmt.Errorf("no refreshToken")
	}
	if !needsKeepalive(sa) {
		return "skipped", fmt.Errorf("token not near expiry")
	}

	next, err := keepaliveExchange(sa)
	if err != nil {
		if isRefreshDeadError(err.Error()) {
			if derr := markSessionDead(authIndex, authID, sa); derr != nil {
				return "session-dead", fmt.Errorf("refresh token dead; flag failed: %v", derr)
			}
			return "session-dead", fmt.Errorf("refresh token dead (4xx): flagged disabled")
		}
		return "failed", fmt.Errorf("refresh rejected: %s", truncateRedacted(err.Error(), 160))
	}

	// Apply rotated fields onto the in-memory auth.
	if next.Token != "" {
		sa.Token = next.Token
	}
	if next.RefreshToken != "" {
		sa.RefreshToken = next.RefreshToken
	}
	if next.ExpiredAt != "" {
		sa.ExpiredAt = next.ExpiredAt
	}
	if next.RefreshExpiredAt != "" {
		sa.RefreshExpiredAt = next.RefreshExpiredAt
	}
	if err := persistKeepaliveTokens(authIndex, sa); err != nil {
		return "error", fmt.Errorf("persist: %w", err)
	}
	return "refreshed", nil
}

// isRefreshDeadError reports whether a refresh failure means the refresh
// token itself is dead (business-level auth rejection on the correct auth
// host), not a transport/gateway/parameter problem. Only 401/403 count: 404
// here is the TLB gateway page for a route that does not exist on that host
// (the 2026-09-05 mass-disable incident killed healthy accounts this way),
// 400 is request-parameter validation (the token is never evaluated), and
// 5xx/transport are transient.
func isRefreshDeadError(msg string) bool {
	for _, marker := range []string{"HTTP 401", "HTTP 403"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// persistKeepaliveTokens writes the rotated runtime token fields onto the TOP
// LEVEL of the physical auth file, preserving every other key (disabled/note/
// preserve/anomaly/counters/credential). Goes through persistAuthDirect — the
// same direct-write channel as preserve/counter, because host.auth.save
// rebuilds the record and drops unknown top-level fields.
func persistKeepaliveTokens(authIndex string, sa *traeAuth) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	raw := foldKeepaliveIntoDoc(phys.JSON, sa)
	name := phys.Name
	if name == "" {
		name = authFileNameFor(sa)
	}
	return persistAuthDirect(name, phys.Path, "", raw)
}

// foldKeepaliveIntoDoc updates the runtime token fields on a top-level auth
// JSON doc, preserving every other key. Extracted so the fold is unit-testable
// without host RPC.
func foldKeepaliveIntoDoc(base []byte, sa *traeAuth) []byte {
	var doc map[string]any
	if json.Unmarshal(base, &doc) != nil || doc == nil {
		doc = map[string]any{}
	}
	if sa != nil {
		doc["token"] = sa.Token
		if sa.RefreshToken != "" {
			doc["refreshToken"] = sa.RefreshToken
		}
		if sa.ExpiredAt != "" {
			doc["expiredAt"] = sa.ExpiredAt
		}
		if sa.RefreshExpiredAt != "" {
			doc["refreshExpiredAt"] = sa.RefreshExpiredAt
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return base
	}
	return raw
}

// markSessionDead logs the session-dead condition but does NOT write
// disabled:true — the user explicitly requested that only manual panel
// toggle controls the disabled flag. Auto-failover routing handles dead
// accounts via anomaly/failure counters without needing disabled:true.
func markSessionDead(authIndex, authID string, sa *traeAuth) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys.Disabled {
		// Already manually disabled — nothing to do.
		return nil
	}
	log.Printf("[keepalive] session dead for %s (auth_index=%s): refresh token expired, not auto-disabling (manual-toggle-only policy)", authID, authIndex)
	// Write a note so the panel can surface the stale-session state, but
	// do NOT set disabled:true — the routing layer will handle it via
	// failover/anomaly counters.
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil {
		return err
	}
	note := "Session expired (refresh token dead): re-login required"
	doc["note"] = note
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

// refreshRetryMax is the per-account daily retry budget for failed refreshes
// (user decision 2026-09-06: retry every 10 minutes, at most 50 attempts per
// account, never concurrently for the same account; the scheduled pass
// resets the budget).
const refreshRetryMax = 50

// refreshRetryEntry tracks one account's refresh attempts since the last
// daily pass. retryable marks a failed refresh worth re-running; maxed marks
// an account whose budget is exhausted for the day.
type refreshRetryEntry struct {
	authIndex string
	attempts  int
	retryable bool
	maxed     bool
	inFlight  bool
}

var (
	refreshRetryMu sync.Mutex
	refreshRetry   = map[string]*refreshRetryEntry{} // key: authID (empty → authIndex)
)

// Test seam: refreshAuthGuarded delegates the actual refresh here so the
// retry-budget and in-flight-mutex behavior is unit-testable without the
// host bridge.
var refreshOneAuthFn = refreshOneAuth

func resetRetryBudgets() {
	refreshRetryMu.Lock()
	refreshRetry = map[string]*refreshRetryEntry{}
	refreshRetryMu.Unlock()
}

// refreshAuthGuarded runs the refresh under the per-account in-flight mutex
// (the same account must never refresh concurrently — daily pass, retry
// ticks and manual triggers all funnel through here) and maintains the retry
// budget. Budget exhaustion only filters the retry scheduler's target list;
// the daily pass resets budgets and manual single-account runs are never
// blocked by it.
func refreshAuthGuarded(authIndex, authID string) (string, error) {
	key := strings.TrimSpace(authID)
	if key == "" {
		key = strings.TrimSpace(authIndex)
	}
	refreshRetryMu.Lock()
	e := refreshRetry[key]
	if e == nil {
		e = &refreshRetryEntry{authIndex: authIndex}
		refreshRetry[key] = e
	}
	if authIndex != "" {
		e.authIndex = authIndex
	}
	if e.inFlight {
		refreshRetryMu.Unlock()
		return "skipped", fmt.Errorf("refresh already in flight for this account")
	}
	e.inFlight = true
	refreshRetryMu.Unlock()

	status, err := refreshOneAuthFn(authIndex, authID)

	refreshRetryMu.Lock()
	e.inFlight = false
	switch status {
	case "refreshed":
		e.attempts, e.retryable, e.maxed = 0, false, false
	case "failed":
		e.attempts++
		e.retryable = true
		if e.attempts >= refreshRetryMax {
			e.maxed = true
		}
	default: // skipped / session-dead / error — nothing worth retrying
		e.retryable = false
	}
	refreshRetryMu.Unlock()
	return status, err
}

// retryFailedRefreshes re-runs refreshes that failed since the last daily
// pass, every 10 minutes, honouring the per-account daily budget. Cross-
// account concurrency stays at 4 like the daily pass; per-account mutual
// exclusion is enforced inside refreshAuthGuarded.
func retryFailedRefreshes() {
	refreshRetryMu.Lock()
	type retryTarget struct{ authIndex, authID string }
	var targets []retryTarget
	for id, e := range refreshRetry {
		if e.retryable && !e.maxed && !e.inFlight && e.attempts < refreshRetryMax {
			targets = append(targets, retryTarget{authIndex: e.authIndex, authID: id})
		}
	}
	refreshRetryMu.Unlock()
	if len(targets) == 0 {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, tg := range targets {
		tg := tg
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			status, err := refreshAuthGuarded(tg.authIndex, tg.authID)
			if status == "skipped" && err == nil {
				return
			}
			detail := ""
			if err != nil {
				detail = truncateRedacted(err.Error(), 200)
			}
			log.Printf("[keepalive] retry %s: %s (%s)", tg.authID, status, detail)
		}()
	}
	wg.Wait()
}

// keepaliveSummary is one row set of the daily run, surfaced via the
// management route for observability.
type keepaliveSummary struct {
	When    time.Time      `json:"when"`
	Results []keepaliveRow `json:"results"`
}

type keepaliveRow struct {
	AuthIndex string `json:"auth_index"`
	Nickname  string `json:"nickname,omitempty"`
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

// runTokenKeepalive refreshes every traework auth that needs it. Returns the
// summary. Manual runs ignore the token_keepalive toggle — the toggle gates
// only the scheduled auto-run.
func runTokenKeepalive() *keepaliveSummary {
	sum := &keepaliveSummary{When: time.Now()}
	resetRetryBudgets() // daily pass resets the per-account retry budgets
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
			if sa, err := hostAuthGet(f.AuthIndex); err == nil {
				row.Nickname = sa.Nickname
			}
			status, err := refreshAuthGuarded(f.AuthIndex, f.ID)
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

// handleKeepaliveNow triggers a manual refresh (all accounts, or one when the
// body carries auth_index).
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
	row := keepaliveRow{AuthIndex: authIndex, Nickname: sa.Nickname}
	row.Status, err = refreshAuthGuarded(authIndex, authIndex)
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

// shouldRunKeepaliveNow reports whether the current local time is within
// one hour after any scheduled keepalive hour today.
func shouldRunKeepaliveNow(now time.Time) bool {
	for _, h := range keepaliveHours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !now.Before(t) && now.Before(t.Add(time.Hour)) {
			return true
		}
	}
	return false
}

// keepaliveLoop wakes every minute; when the local clock crosses a scheduled
// keepalive hour it runs runTokenKeepalive once per day, and every 10 minutes
// it re-runs refreshes that failed since the daily pass (per-account budget:
// 50 attempts/day, no same-account concurrency). Mirrors the anomaly
// refresh loop's day-guard pattern.
func keepaliveLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastDay := -1
	for range ticker.C {
		if !keepaliveEnabled() {
			continue
		}
		now := time.Now().Local()
		if shouldRunKeepaliveNow(now) && now.Day() != lastDay {
			lastDay = now.Day()
			sum := runTokenKeepalive()
			refreshed, failed, dead := 0, 0, 0
			for _, r := range sum.Results {
				switch r.Status {
				case "refreshed":
					refreshed++
				case "failed":
					failed++
				case "session-dead":
					dead++
				}
			}
			log.Printf("[keepalive] daily run: refreshed=%d failed=%d session_dead=%d total=%d",
				refreshed, failed, dead, len(sum.Results))
			// Per-account details into the container log — the summary line
			// alone made the 2026-09-05 mass-disable undiagnosable without
			// manually replaying refresh calls on the server.
			for _, r := range sum.Results {
				if r.Status == "skipped" {
					continue
				}
				log.Printf("[keepalive]   %s: %s (%s)", r.Nickname, r.Status, r.Detail)
			}
		}
		if now.Minute()%10 == 0 {
			retryFailedRefreshes()
		}
	}
}

func init() {
	go keepaliveLoop()
}
