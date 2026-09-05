// management.go implements the traework management API: account dashboard
// (nickname, credits, failover/anomaly status, disabled flag), manual /
// auto check-in, points query, active-account selection, and enable/disable
// toggles. It backs the web panel (panel.go).
package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

var (
	managementBasePathCache   = "/v0/management"
	managementBasePathCacheMu sync.RWMutex
)

func loadedManagementBasePath() string {
	managementBasePathCacheMu.RLock()
	defer managementBasePathCacheMu.RUnlock()
	return managementBasePathCache
}

func setManagementBasePath(p string) {
	p = strings.TrimRight(strings.TrimSpace(p), "/")
	if p == "" {
		return
	}
	managementBasePathCacheMu.Lock()
	managementBasePathCache = p
	managementBasePathCacheMu.Unlock()
}

func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + providerName
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/accounts", Description: "List TraeWork accounts with credits, check-in and failover status."},
			{Method: http.MethodPost, Path: base + "/refresh", Description: "Force refresh points/cache for all accounts (async, throttled)."},
			{Method: http.MethodGet, Path: base + "/refresh/status", Description: "Async refresh progress snapshot."},
			{Method: http.MethodPost, Path: base + "/checkin", Description: "Manually check in one account (auth_index) or all."},
			{Method: http.MethodPost, Path: base + "/checkin/config", Description: "Toggle auto check-in (enabled: true/false)."},
			{Method: http.MethodGet, Path: base + "/checkin/retries", Description: "Snapshot of the check-in retry queue (1-minute cadence, max 60 attempts)."},
			{Method: http.MethodGet, Path: base + "/credits", Description: "Get real-time credits for one (auth_index query) or all accounts."},
			{Method: http.MethodPost, Path: base + "/select", Description: "Select the active account card used for chat routing (body: {auth_index})."},
			{Method: http.MethodPost, Path: base + "/enable", Description: "Enable one (body: {auth_index}) or all (empty body) accounts."},
			{Method: http.MethodPost, Path: base + "/disable", Description: "Disable one (body: {auth_index}) or all (empty body) accounts."},
			{Method: http.MethodPost, Path: base + "/unfreeze", Description: "Remove one (body: {auth_index}) or all (empty body) accounts from the anomaly pool."},
			{Method: http.MethodPost, Path: base + "/import", Description: "Import one Trae SOLO credential (body: {filename, content}); whole storage.json or raw credential value accepted."},
			{Method: http.MethodPost, Path: base + "/browser-login/start", Description: "Start a browser OAuth login: returns the Trae authorization URL (PKCE pair minted server-side)."},
			{Method: http.MethodPost, Path: base + "/browser-login/submit", Description: "Finish a browser OAuth login from the pasted callback URL (body: {url}); parses code/state and imports the account."},
			{Method: http.MethodPost, Path: base + "/browser-login/result", Description: "Fetch the one-time browser-login outcome for a state (body: {state}); read-once, credential-free."},
			{Method: http.MethodGet, Path: base + "/storage-path", Description: "Return the detected Trae SOLO globalStorage directory for the panel hint."},
			{Method: http.MethodPost, Path: base + "/keepalive", Description: "Manually refresh access tokens for all accounts (or one with auth_index)."},
			{Method: http.MethodGet, Path: base + "/keepalive/status", Description: "Last keepalive run summary + config."},
			{Method: http.MethodGet, Path: base + "/lifecycle", Description: "Lifecycle (auto-disable exhausted) toggle state."},
			{Method: http.MethodPost, Path: base + "/delete", Description: "Delete one TraeWork account and its physical auth file (body: {auth_index})."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "TraeWork", Description: "TraeWork dashboard: credits, check-in, enable/disable, failover status."},
			// OAuth bounce target: MUST stay on the unauthenticated resource
			// prefix (no Menu -> never shows up in the management UI menu).
			// Kept as the auto bounce target for setups that can reach it
			// (e.g. a local forwarder); the authorization URL itself points
			// at the loopback /authorize shape required by the Trae
			// whitelist, so the regular path is the panel paste-submit flow.
			{Path: "/browser-login/callback", Description: "Trae login page redirect target (?code=&state=); exchanges the code and imports the account, then bounces to the panel."},
			// Host-OAuth-flow bridge page (login.go): the host UI「登录」button
			// flow finishes here because the Trae callback shape (authCodeInfo
			// JSON, no code/state echo) cannot travel through the host's own
			// paste channel. GET-only, unauthenticated, one state per login.
			{Path: "/browser-login/bridge", Description: "Host OAuth login bridge: opens the Trae authorization page and finishes the login from the pasted callback URL."},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimRight(req.Path, "/")

	// Browser UI resource routes (unauthenticated). /browser-login/callback
	// is the OAuth bounce target: the Trae login page navigates here with a
	// plain browser GET and cannot carry the management key, so it must be
	// dispatched on this resource prefix (registered via .Resources above).
	resPrefix := "/v0/resource/plugins/" + providerName
	if req.Method == http.MethodGet && strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
		if sub == "/browser-login/callback" {
			return okEnvelope(handleBrowserLoginCallback(req))
		}
		if sub == "/browser-login/bridge" {
			return okEnvelope(handleBrowserLoginBridge(req))
		}
		return okEnvelope(mgmtHTMLResponse(servePanel(sub)))
	}

	// Plugin-layer auth for mutating endpoints (defence-in-depth on top of
	// the host middleware; skipped when no management_key is configured).
	if req.Method == http.MethodPost || mutatingManagementPath(path) {
		ip := managementClientIP(req)
		if status, msg := checkManagementAuth(req); status != 0 {
			if !allowManagementRequest(ip) {
				return okEnvelope(mgmtJSONResponse(http.StatusTooManyRequests, map[string]any{
					"error": "rate limit exceeded, try again later",
				}))
			}
			return okEnvelope(mgmtJSONResponse(status, map[string]any{"error": msg}))
		}
	}

	base := loadedManagementBasePath() + "/plugins/" + providerName
	switch {
	case req.Method == http.MethodGet && path == base+"/accounts":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleAccounts()))
	case req.Method == http.MethodPost && path == base+"/checkin":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleManualCheckin(req)))
	case req.Method == http.MethodPost && path == base+"/checkin/config":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCheckinConfig(req)))
	case req.Method == http.MethodGet && path == base+"/checkin/retries":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCheckinRetries()))
	case req.Method == http.MethodGet && path == base+"/credits":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCreditsQuery(req)))
	case req.Method == http.MethodPost && path == base+"/select":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleSelectAuth(req)))
	case req.Method == http.MethodPost && path == base+"/enable":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleToggleDisabled(req, false)))
	case req.Method == http.MethodPost && path == base+"/disable":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleToggleDisabled(req, true)))
	case req.Method == http.MethodPost && path == base+"/unfreeze":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleUnfreezeAuth(req)))
	case req.Method == http.MethodPost && path == base+"/import":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleImportCredential(req)))
	case req.Method == http.MethodPost && path == base+"/browser-login/start":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleBrowserLoginStart(req)))
	case req.Method == http.MethodPost && path == base+"/browser-login/submit":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleBrowserLoginSubmit(req)))
	case req.Method == http.MethodPost && path == base+"/browser-login/result":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleBrowserLoginResult(req)))
	case req.Method == http.MethodGet && path == base+"/export":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleExportAuth()))
	case req.Method == http.MethodGet && path == base+"/storage-path":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, map[string]any{"ok": true, "path": storageGlobalDir()}))
	case req.Method == http.MethodPost && path == base+"/refresh":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleRefreshAll()))
	case req.Method == http.MethodGet && path == base+"/refresh/status":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleRefreshStatus()))
	case req.Method == http.MethodPost && path == base+"/keepalive":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleKeepaliveNow(req)))
	case req.Method == http.MethodGet && path == base+"/keepalive/status":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleKeepaliveStatus()))
	case req.Method == http.MethodGet && path == base+"/lifecycle":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleLifecycleStatus()))
	case req.Method == http.MethodPost && path == base+"/delete":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleDeleteAuth(req)))
	}
	return okEnvelope(mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": "not found: " + path}))
}

// handleAccounts builds the dashboard account list: for every traework auth
// it merges the physical record (disabled/anomaly), cached or live credits,
// and the failover snapshot. Also refreshes the active-auth pin.
func handleAccounts() map[string]any {
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	views := make([]traeAccountView, 0, len(files))
	for _, f := range files {
		if strings.TrimSpace(f.AuthIndex) == "" {
			continue
		}
		a, phys, loadErr := hostAuthGetBundle(f.AuthIndex)
		if loadErr != nil || a == nil {
			continue
		}
		view := traeAccountView{
			AuthID:    f.ID,
			AuthIndex: f.AuthIndex,
			Nickname:  a.Nickname,
			Label:     f.Label,
			Name:      f.Name,
			UID:       a.UserID,
			Disabled:  phys.Disabled || f.Disabled,
			Anomaly:   isAnomaly(f.ID),
			Preserved: isPreserve(f.ID),
		}
		// Cumulative success/failed counters (plugin-owned, survive restart).
		if strings.TrimSpace(a.UserID) != "" {
			ensureCounterLoaded(a.UserID, phys.JSON)
			view.SuccessCount, view.FailedCount = counterSnapshot(a.UserID)
		}
		// Credits: cached snapshot first; live query when missing. The full
		// quota snapshot (remain/used/size/packs) drives the panel progress
		// bar; the flat Remain stays for backward-compatible consumers.
		cr, cached := cachedCredits(f.ID)
		if !cached {
			if live, qerr := accountCredits(a); qerr == nil {
				cr = live
				cacheCredits(f.ID, cr)
			}
		}
		if cr != nil {
			view.Remain = cr.TotalRemain
			view.Credits = cr
			view.Exhausted = isCreditsExhausted(cr)
		}
		// Failover snapshot. cool_until is Unix seconds (panel countdown ticker).
		if count, until, ok := failoverStateSnapshot(f.ID); ok {
			view.FailCount = count
			view.CoolingDown = time.Now().Before(until)
			if view.CoolingDown {
				view.CooldownUntil = until.Unix()
			}
		}
		view.CheckinToday = checkinDoneToday(f.AuthIndex)
		view.Active = strings.TrimSpace(f.ID) == getActiveAuthID()
		views = append(views, view)
	}
	active := ensureDefaultActiveAuth(views)
	return map[string]any{
		"accounts":          views,
		"active_id":         active,
		"anomaly_pool_size": len(anomalySnapshot()),
		"checkin_auto":      autoCheckinEnabled(),
		"server_time":       time.Now().Format("2006-01-02 15:04:05"),
		// Plugin subsystem state for the panel header (watchdog / keepalive /
		// lifecycle toggles + their config). Kept in one /accounts payload so
		// the panel renders with a single fetch.
		"preserve": map[string]any{
			"threshold":        preserveThreshold(),
			"interval_seconds": int64(preserveWatchdogInterval().Seconds()),
			"enabled":          preserveWatchdogEnabled(),
			"pool_size":        len(preserveSnapshot()),
		},
		"lifecycle": map[string]any{
			"enabled": lifecycleEnabled(),
		},
		"keepalive": map[string]any{
			"enabled":  keepaliveEnabled(),
			"schedule": keepaliveHours,
			"last_run": getLastKeepalive(),
		},
	}
}

// handleRefreshAll enqueues a full-fleet points refresh into the throttled
// runner and returns immediately (async). The panel polls /refresh/status
// and updates cards incrementally instead of blocking on N round-trips.
func handleRefreshAll() map[string]any {
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"started": false, "error": err.Error()}
	}
	targets := make([]refreshTarget, 0, len(files))
	for _, f := range files {
		targets = append(targets, refreshTarget{AuthIndex: f.AuthIndex, AuthID: f.ID})
	}
	n := globalRefresh.EnqueueAll(targets, "panel")
	return map[string]any{"started": n > 0, "source": "panel", "queued": n}
}

// handleRefreshStatus returns the async refresh progress snapshot for the
// panel's incremental card updates. The snapshot is returned FLAT (running /
// total / done / failed / per_account on the top-level object) — the panel
// reads those fields directly, so wrapping it in {"refresh": ...} would break
// the whole polling chain.
func handleRefreshStatus() any {
	return globalRefresh.Snapshot()
}

// handleManualCheckin checks in one account (auth_index) or all. Failed
// accounts whose failures are retryable are added to the persistent retry
// queue (1-minute cadence, up to checkinRetryMax attempts); the response
// reports how many accounts were newly queued so the panel can tell the
// user the failure is being worked on in the background.
func handleManualCheckin(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		okCount, results, scheduled := runFleetCheckin("manual")
		// Batch summary counters (panel contract, mirrors workbuddy):
		// success = fresh claims, already = upstream said "今日已签到",
		// fail = failed attempts, eligible = fresh + failed.
		successN, alreadyN, failN := 0, 0, 0
		for _, r := range results {
			switch {
			case r["error"] != nil:
				failN++
			case r["ok"] == true && r["already"] == true:
				alreadyN++
			case r["ok"] == true:
				successN++
			default:
				failN++
			}
		}
		return map[string]any{
			"ok": true, "checked_in": okCount, "results": results,
			"retries_scheduled": scheduled,
			"summary": map[string]any{
				"success": successN, "already": alreadyN,
				"fail": failN, "eligible": successN + failN,
			},
		}
	}
	a, err := hostAuthGet(authIndex)
	if err != nil || a == nil {
		return map[string]any{"error": "account not found: " + authIndex}
	}
	res := checkinAccount(a)
	out := map[string]any{"ok": res.OK, "message": res.Message, "auth_index": authIndex, "already": res.Already}
	out["nickname"] = a.Nickname
	if res.Points > 0 {
		out["points"] = res.Points
	}
	if res.OK {
		cancelCheckinRetry(authIndex)
		markCheckinDoneToday(authIndex)
	}
	// Locate the auth file ID once — used for retry queuing and the
	// credits cache refresh below.
	var fileID string
	if files, lerr := hostAuthList(); lerr == nil {
		for _, f := range files {
			if f.AuthIndex == authIndex {
				fileID = f.ID
				break
			}
		}
	}
	if !res.OK && scheduleCheckinRetry(authIndex, fileID, a.UserID, res.Message) {
		out["retry_scheduled"] = true
	}
	// Refresh credits cache for the panel.
	if fileID != "" {
		if cr, cerr := accountCredits(a); cerr == nil {
			cacheCredits(fileID, cr)
			out["remain"] = cr.TotalRemain
			out["credits"] = cr
		}
	}
	return out
}

// handleCheckinRetries returns the pending check-in retry queue snapshot.
func handleCheckinRetries() map[string]any {
	snapshot := checkinRetrySnapshot()
	return map[string]any{
		"ok":       true,
		"interval": checkinRetryInterval.String(),
		"max":      checkinRetryMax,
		"pending":  len(snapshot),
		"retries":  snapshot,
	}
}

// handleCheckinConfig toggles the daily auto check-in loop.
func handleCheckinConfig(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || body.Enabled == nil {
		return map[string]any{"enabled": autoCheckinEnabled(), "error": "body {enabled: bool} required"}
	}
	setCheckinAuto(*body.Enabled)
	return map[string]any{"ok": true, "enabled": autoCheckinEnabled()}
}

// handleCreditsQuery returns real-time credits for one account (auth_index
// query) or all accounts, refreshing the cache. With track=1 the single-account
// branch enqueues a throttled background refresh instead of blocking on the
// upstream call (panel lazy-refresh path, mirrors workbuddy).
func handleCreditsQuery(req pluginapi.ManagementRequest) map[string]any {
	authIndex := strings.TrimSpace(req.Headers.Get("X-Auth-Index"))
	if v := req.Query.Get("auth_index"); v != "" {
		authIndex = v
	}
	if authIndex != "" {
		files, lerr := hostAuthList()
		if lerr != nil {
			return map[string]any{"error": lerr.Error()}
		}
		var fileID string
		for _, f := range files {
			if f.AuthIndex == authIndex {
				fileID = f.ID
				break
			}
		}
		if t := req.Query.Get("track"); t == "1" || t == "true" {
			if fileID == "" {
				return map[string]any{"error": "account not found: " + authIndex}
			}
			globalRefresh.EnqueueOne(authIndex, fileID, "credits")
			return map[string]any{"queued": true, "auth_index": authIndex, "status": globalRefresh.Snapshot()}
		}
		a, err := hostAuthGet(authIndex)
		if err != nil || a == nil {
			return map[string]any{"error": "account not found: " + authIndex}
		}
		cr, qerr := accountCredits(a)
		if qerr != nil {
			return map[string]any{"error": qerr.Error(), "auth_index": authIndex}
		}
		if fileID != "" {
			cacheCredits(fileID, cr)
		}
		return map[string]any{
			"ok": true, "auth_index": authIndex, "remain": cr.TotalRemain,
			"credits": cr, "exhausted": isCreditsExhausted(cr),
		}
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	type entry struct {
		AuthIndex string `json:"auth_index"`
		UID       string `json:"uid"`
		Nickname  string `json:"nickname"`
		Remain    int64  `json:"remain"`
	}
	out := make([]entry, 0, len(files))
	for _, f := range files {
		if strings.TrimSpace(f.AuthIndex) == "" {
			continue
		}
		a, err := hostAuthGet(f.AuthIndex)
		if err != nil || a == nil {
			continue
		}
		remain, qerr := accountPoints(a)
		if qerr != nil {
			continue
		}
		cacheCredits(f.ID, &traeCredits{TotalRemain: remain, FetchedAt: time.Now().Format(time.RFC3339)})
		out = append(out, entry{AuthIndex: f.AuthIndex, UID: a.UserID, Nickname: a.Nickname, Remain: remain})
	}
	return map[string]any{"ok": true, "accounts": out}
}

// handleSelectAuth sets the active account used for routing.
func handleSelectAuth(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || strings.TrimSpace(body.AuthIndex) == "" {
		return map[string]any{"error": "body {auth_index: string} required"}
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	for _, f := range files {
		if f.AuthIndex == body.AuthIndex || f.ID == body.AuthIndex {
			setActiveAuthID(f.ID)
			return map[string]any{"ok": true, "auth_index": f.AuthIndex, "id": f.ID}
		}
	}
	return map[string]any{"error": "account not found: " + body.AuthIndex}
}

// handleToggleDisabled enables (on=false) or disables (on=true) one account
// (auth_index) or all accounts. The disabled flag is persisted at the top
// level of the physical auth file via host.auth.save.
func handleToggleDisabled(req pluginapi.ManagementRequest, disable bool) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	action := "enabled"
	if disable {
		action = "disabled"
	}
	if authIndex == "" {
		files, err := hostAuthList()
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		n := 0
		for _, f := range files {
			if strings.TrimSpace(f.AuthIndex) == "" {
				continue
			}
			if err := persistDisabledToggle(f.AuthIndex, f.ID, disable); err == nil {
				n++
			}
		}
		return map[string]any{"ok": true, "action": action, "count": n}
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	for _, f := range files {
		if f.AuthIndex == authIndex || f.ID == authIndex {
			if err := persistDisabledToggle(f.AuthIndex, f.ID, disable); err != nil {
				return map[string]any{"error": err.Error(), "auth_index": f.AuthIndex}
			}
			if disable {
				clearActiveAuthIfMatch(f.ID)
			}
			return map[string]any{"ok": true, "action": action, "auth_index": f.AuthIndex, "id": f.ID}
		}
	}
	return map[string]any{"error": "account not found: " + authIndex}
}

// persistDisabledToggle writes the top-level disabled flag via host.auth.save
// (the physical auth JSON round-trips through the host's rebuild, which
// preserves recognized top-level fields).
func persistDisabledToggle(authIndex, authID string, disabled bool) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	var doc map[string]any
	if uerr := json.Unmarshal(phys.JSON, &doc); uerr != nil {
		doc = map[string]any{}
	}
	doc["disabled"] = disabled
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(phys.Name)
	if name == "" {
		name = "traework-" + sanitizeUIDForFileName(authID) + ".json"
	}
	return hostAuthSaveJSON(name, raw)
}

// -----------------------------------------------------------------------------
// Management auth + rate limit (defence-in-depth)
// -----------------------------------------------------------------------------

const (
	mgmtRateLimitCapacity = 5
	mgmtRateLimitRefill   = time.Minute / 10
	mgmtRateLimitTTL      = 10 * time.Minute
)

type mgmtRateEntry struct {
	tokens   float64
	lastSeen time.Time
}

var (
	mgmtRateLimit   = map[string]*mgmtRateEntry{}
	mgmtRateLimitMu sync.Mutex

	managementAPIKeyMu sync.RWMutex
	managementAPIKey   string
)

func loadedManagementKey() string {
	managementAPIKeyMu.RLock()
	defer managementAPIKeyMu.RUnlock()
	return managementAPIKey
}

func setManagementKey(k string) {
	managementAPIKeyMu.Lock()
	managementAPIKey = strings.TrimSpace(k)
	managementAPIKeyMu.Unlock()
}

// checkManagementAuth returns an HTTP status + error message when the request
// should be rejected (status=0 means allow). Mirrors the community-plugin
// convention: trust the host middleware by default; enforce a configured
// management_key as defence-in-depth.
func checkManagementAuth(req pluginapi.ManagementRequest) (int, string) {
	want := loadedManagementKey()
	if want == "" {
		return 0, ""
	}
	got := strings.TrimSpace(req.Headers.Get("Authorization"))
	if !strings.HasPrefix(got, "Bearer ") {
		return http.StatusUnauthorized, "missing Bearer token"
	}
	token := strings.TrimSpace(strings.TrimPrefix(got, "Bearer "))
	if subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		return http.StatusForbidden, "invalid management key"
	}
	return 0, ""
}

func allowManagementRequest(ip string) bool {
	if ip == "" {
		ip = "_global"
	}
	mgmtRateLimitMu.Lock()
	defer mgmtRateLimitMu.Unlock()
	now := time.Now()
	e, ok := mgmtRateLimit[ip]
	if !ok {
		e = &mgmtRateEntry{tokens: mgmtRateLimitCapacity, lastSeen: now}
		mgmtRateLimit[ip] = e
	}
	elapsed := now.Sub(e.lastSeen)
	e.tokens += float64(elapsed) / float64(mgmtRateLimitRefill)
	if e.tokens > mgmtRateLimitCapacity {
		e.tokens = mgmtRateLimitCapacity
	}
	e.lastSeen = now
	if e.tokens < 1 {
		return false
	}
	e.tokens--
	if len(mgmtRateLimit) > 1024 {
		for k, v := range mgmtRateLimit {
			if now.Sub(v.lastSeen) > mgmtRateLimitTTL {
				delete(mgmtRateLimit, k)
			}
		}
	}
	return true
}

func managementClientIP(req pluginapi.ManagementRequest) string {
	if xff := strings.TrimSpace(req.Headers.Get("X-Forwarded-For")); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	if xr := strings.TrimSpace(req.Headers.Get("X-Real-Ip")); xr != "" {
		return xr
	}
	return ""
}

// handleExportAuth returns every traework credential as a parsed JSON backup
// ({version, exported_at, plugin, count, accounts:[{name, auth_index, uid,
// nickname, credential}]}). The frontend downloads it as a dated file; the
// same wrapper can be re-imported later. Carries full credentials — must stay
// in mutatingManagementPath so the management key is required despite being GET.
func handleExportAuth() map[string]any {
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": "host.auth.list failed: " + err.Error(), "count": 0, "accounts": []any{}}
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		if strings.TrimSpace(f.AuthIndex) == "" {
			continue
		}
		a, phys, gerr := hostAuthGetBundle(f.AuthIndex)
		if gerr != nil || phys == nil {
			out = append(out, map[string]any{
				"name":       f.Name,
				"auth_index": f.AuthIndex,
				"load_error": errString(gerr),
			})
			continue
		}
		var cred any
		_ = json.Unmarshal(phys.JSON, &cred)
		entry := map[string]any{
			"name":       f.Name,
			"auth_index": f.AuthIndex,
			"credential": cred,
		}
		if a != nil {
			entry["uid"] = a.UserID
			entry["nickname"] = a.Nickname
		}
		out = append(out, entry)
	}
	return map[string]any{
		"version":     1,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"plugin":      providerName,
		"count":       len(out),
		"accounts":    out,
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func mutatingManagementPath(path string) bool {
	base := loadedManagementBasePath() + "/plugins/" + providerName
	switch path {
	case base + "/checkin",
		base + "/checkin/config",
		base + "/select",
		base + "/enable",
		base + "/disable",
		base + "/unfreeze",
		base + "/import",
		base + "/browser-login/start",
		base + "/browser-login/submit",
		base + "/browser-login/result",
		base + "/export",
		base + "/delete":
		return true
	}
	return false
}

func mgmtJSONResponse(status int, v any) pluginapi.ManagementResponse {
	body, _ := json.Marshal(v)
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	return pluginapi.ManagementResponse{StatusCode: status, Headers: h, Body: body}
}

func mgmtHTMLResponse(body []byte) pluginapi.ManagementResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: body}
}

// handleDeleteAuth removes one TraeWork account and its physical auth file.
// This panel entry is strict (unlike lifecycle disableAuth, which only
// disables): it refuses to act unless the target account exists, belongs to
// TraeWork, and its physical path is safe and known. Only auth_index is
// accepted from the body; the host's own list/get responses are the sole
// source of truth for the delete target (never a client-supplied
// uid/name/path). Ported from workbuddy's handleDeleteAuth (0.14.7) with the
// authFilePrefix/name helpers adapted to the traework namespace.
func handleDeleteAuth(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required"}
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": "host.auth.list: " + err.Error()}
	}
	for _, f := range files {
		if f.AuthIndex != authIndex {
			continue
		}
		// hostAuthList already prefix-filters on traework-*, but double-check
		// the concrete name so a legacy or mis-shaped entry can't slip through.
		if !isTraeworkAuthFileName(f.Name) {
			return map[string]any{"error": "不是 TraeWork 认证文件", "auth_index": authIndex}
		}
		sa, phys, err := hostAuthGetBundle(authIndex)
		if err != nil {
			return map[string]any{"error": "host.auth.get: " + err.Error(), "auth_index": authIndex}
		}
		if sa == nil {
			return map[string]any{"error": "认证内容解析失败", "auth_index": authIndex}
		}
		if phys == nil || strings.TrimSpace(phys.AuthIndex) != authIndex {
			return map[string]any{"error": "认证索引不一致", "auth_index": authIndex}
		}
		path := strings.TrimSpace(phys.Path)
		if path == "" {
			return map[string]any{"error": "认证文件路径缺失，无法安全删除", "auth_index": authIndex}
		}
		if !isSafeAuthPath(path) {
			return map[string]any{"error": "认证文件路径不安全，已拒绝删除", "auth_index": authIndex}
		}
		nickname := sa.Nickname
		uid := sa.UserID
		// Physical delete confined to the auth directory.
		if err := deleteAuthFileInDir(path, filepath.Dir(path)); err != nil {
			return map[string]any{"error": "删除认证文件失败: " + err.Error(), "auth_index": authIndex}
		}
		clearDeletedAccountState(f.ID, authIndex, uid)
		return map[string]any{
			"ok":         true,
			"auth_index": authIndex,
			"nickname":   nickname,
			"uid":        uid,
			"deleted":    f.Name,
		}
	}
	return map[string]any{"error": "account not found", "auth_index": authIndex}
}

// clearDeletedAccountState removes every in-memory trace of a deleted account
// for each provided key (auth.ID, auth_index, and account UID may each have
// been used as a key by different code paths). Covers cached credits/plan,
// active selection, preserve flag, anomaly membership, failover
// cooldown/counter, and session bindings pinned to the account. Idempotent —
// safe to call when maps are empty or keys already absent.
func clearDeletedAccountState(keys ...string) {
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		accountCache.Delete(k)
		clearActiveAuthIfMatch(k)
		preserveSetClear(k)
		anomalySetClear(k)
		clearFailoverStateForAuth(k)
		evictSessionBindingsForAuth(k)
	}
}

// clearFailoverStateForAuth drops one account's failover cooldown/counter
// state (the account no longer exists, so the entry is dead weight).
func clearFailoverStateForAuth(authID string) {
	if authID == "" {
		return
	}
	failoverMu.Lock()
	delete(failoverStates, authID)
	failoverMu.Unlock()
}
