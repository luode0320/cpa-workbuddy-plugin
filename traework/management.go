// management.go implements the traework management API: account dashboard
// (nickname, credits, failover/anomaly status, disabled flag), manual /
// auto check-in, points query, active-account selection, and enable/disable
// toggles. It backs the web panel (panel.go).
package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
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
			{Method: http.MethodPost, Path: base + "/checkin", Description: "Manually check in one account (auth_index) or all."},
			{Method: http.MethodPost, Path: base + "/checkin/config", Description: "Toggle auto check-in (enabled: true/false)."},
			{Method: http.MethodGet, Path: base + "/credits", Description: "Get real-time credits for one (auth_index query) or all accounts."},
			{Method: http.MethodPost, Path: base + "/select", Description: "Select the active account card used for chat routing (body: {auth_index})."},
			{Method: http.MethodPost, Path: base + "/enable", Description: "Enable one (body: {auth_index}) or all (empty body) accounts."},
			{Method: http.MethodPost, Path: base + "/disable", Description: "Disable one (body: {auth_index}) or all (empty body) accounts."},
			{Method: http.MethodPost, Path: base + "/unfreeze", Description: "Remove one (body: {auth_index}) or all (empty body) accounts from the anomaly pool."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "TraeWork", Description: "TraeWork dashboard: credits, check-in, enable/disable, failover status."},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimRight(req.Path, "/")

	// Browser UI resource routes (unauthenticated).
	resPrefix := "/v0/resource/plugins/" + providerName
	if req.Method == http.MethodGet && strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
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
			UID:       a.UserID,
			Disabled:  phys.Disabled || f.Disabled,
			Anomaly:   isAnomaly(f.ID),
		}
		// Credits: cached snapshot first; live query when missing.
		cr, cached := cachedCredits(f.ID)
		if !cached {
			if remain, qerr := accountPoints(a); qerr == nil {
				cr = &traeCredits{TotalRemain: remain, FetchedAt: time.Now().Format(time.RFC3339)}
				cacheCredits(f.ID, cr)
			}
		}
		if cr != nil {
			view.Remain = cr.TotalRemain
			view.Exhausted = isCreditsExhausted(cr)
		}
		// Failover snapshot.
		if count, until, ok := failoverStateSnapshot(f.ID); ok {
			view.FailCount = count
			view.CoolingDown = time.Now().Before(until)
			if view.CoolingDown {
				view.CooldownUntil = until.Format(time.RFC3339)
			}
		}
		view.Active = strings.TrimSpace(f.ID) == getActiveAuthID()
		views = append(views, view)
	}
	active := ensureDefaultActiveAuth(views)
	return map[string]any{
		"accounts":          views,
		"active_id":         active,
		"anomaly_pool_size": len(anomalySnapshot()),
	}
}

// handleManualCheckin checks in one account (auth_index) or all.
func handleManualCheckin(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		okCount := runFleetCheckin("manual")
		return map[string]any{"ok": true, "checked_in": okCount}
	}
	a, err := hostAuthGet(authIndex)
	if err != nil || a == nil {
		return map[string]any{"error": "account not found: " + authIndex}
	}
	res := checkinAccount(a)
	out := map[string]any{"ok": res.OK, "message": res.Message, "auth_index": authIndex}
	if res.Points > 0 {
		out["points"] = res.Points
	}
	// Refresh credits cache for the panel.
	if files, lerr := hostAuthList(); lerr == nil {
		for _, f := range files {
			if f.AuthIndex == authIndex {
				if remain, qerr := accountPoints(a); qerr == nil {
					cacheCredits(f.ID, &traeCredits{TotalRemain: remain, FetchedAt: time.Now().Format(time.RFC3339)})
					out["remain"] = remain
				}
				break
			}
		}
	}
	return out
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
// query) or all accounts, refreshing the cache.
func handleCreditsQuery(req pluginapi.ManagementRequest) map[string]any {
	authIndex := strings.TrimSpace(req.Headers.Get("X-Auth-Index"))
	if v := req.Query.Get("auth_index"); v != "" {
		authIndex = v
	}
	if authIndex != "" {
		a, err := hostAuthGet(authIndex)
		if err != nil || a == nil {
			return map[string]any{"error": "account not found: " + authIndex}
		}
		remain, qerr := accountPoints(a)
		if qerr != nil {
			return map[string]any{"error": qerr.Error(), "auth_index": authIndex}
		}
		return map[string]any{"ok": true, "auth_index": authIndex, "remain": remain}
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

func mutatingManagementPath(path string) bool {
	base := loadedManagementBasePath() + "/plugins/" + providerName
	switch path {
	case base + "/checkin",
		base + "/checkin/config",
		base + "/select",
		base + "/enable",
		base + "/disable",
		base + "/unfreeze":
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
