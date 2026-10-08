// management.go implements the WorkBuddy AI management API and web panel routes:
// account dashboard, credits, trial claim, test ping, import/export, and QR login.
package main

import (
	"bytes"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
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
			{Method: http.MethodGet, Path: base + "/accounts", Description: "List WorkBuddy AI accounts with credits and plan."},
			{Method: http.MethodPost, Path: base + "/refresh", Description: "Force refresh quota/cache for all accounts."},
			{Method: http.MethodGet, Path: base + "/refresh/status", Description: "Async refresh progress snapshot."},
			{Method: http.MethodGet, Path: base + "/credits", Description: "Get real-time credits for one or all accounts."},
			{Method: http.MethodPost, Path: base + "/checkin", Description: "Manually check in one account (auth_index) or all."},
			{Method: http.MethodPost, Path: base + "/checkin/config", Description: "Toggle auto check-in (enabled: true/false)."},
			{Method: http.MethodPost, Path: base + "/import", Description: "Import WorkBuddy AI credential JSON into host auth store."},
			{Method: http.MethodGet, Path: base + "/export", Description: "Export all WorkBuddy AI credentials as JSON backup."},
			{Method: http.MethodPost, Path: base + "/trial", Description: "Claim expert trial pack for one account (auth_index). 250 credits / 14 days."},
			{Method: http.MethodPost, Path: base + "/select", Description: "Select the active account card used for chat routing (body: {auth_index})."},
			{Method: http.MethodPost, Path: base + "/delete", Description: "Delete one WorkBuddy AI account and auth file (body: {auth_index})."},
			{Method: http.MethodPost, Path: base + "/keepalive", Description: "Manually refresh access tokens for all accounts."},
			{Method: http.MethodGet, Path: base + "/keepalive/status", Description: "Last keepalive run summary + config."},
			{Method: http.MethodPost, Path: base + "/test-active", Description: "Send an active ping chat inference request for one account."},
			{Method: http.MethodPost, Path: base + "/login/start", Description: "Start OAuth login flow and get auth URL and state."},
			{Method: http.MethodGet, Path: base + "/login/poll", Description: "Poll OAuth login flow status."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "WorkBuddy AI", Description: "WorkBuddy AI (International) dashboard: credits, trial, plan, import."},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimRight(req.Path, "/")

	resPrefix := "/v0/resource/plugins/" + providerName
	if req.Method == http.MethodGet && strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
		return okEnvelope(mgmtHTMLResponse(servePanel(sub)))
	}

	if req.Method == http.MethodPost || mutatingManagementPath(path) {
		if status, msg := checkManagementAuth(req); status != 0 {
			return okEnvelope(mgmtJSONResponse(status, map[string]any{"error": msg}))
		}
	}

	base := loadedManagementBasePath() + "/plugins/" + providerName

	switch {
	case req.Method == http.MethodGet && path == base+"/accounts":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, buildDashboardEx(false, false)))
	case req.Method == http.MethodPost && path == base+"/refresh":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleRefreshAsync()))
	case req.Method == http.MethodGet && path == base+"/refresh/status":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, globalRefresh.Snapshot()))
	case req.Method == http.MethodGet && path == base+"/credits":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCreditsQuery(req)))
	case req.Method == http.MethodPost && path == base+"/checkin":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleManualCheckin(req)))
	case req.Method == http.MethodPost && path == base+"/checkin/config":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCheckinConfig(req)))
	case req.Method == http.MethodPost && path == base+"/import":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleImportAuth(req)))
	case req.Method == http.MethodGet && path == base+"/export":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleExportAuth(req)))
	case req.Method == http.MethodPost && path == base+"/trial":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleClaimTrial(req)))
	case req.Method == http.MethodPost && path == base+"/select":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleSelectAuth(req)))
	case req.Method == http.MethodPost && path == base+"/delete":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleDeleteAuth(req)))
	case req.Method == http.MethodPost && path == base+"/test-active":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleTestActive(req)))
	case req.Method == http.MethodPost && path == base+"/keepalive":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleKeepaliveNow(req)))
	case req.Method == http.MethodGet && path == base+"/keepalive/status":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleKeepaliveStatus()))
	case req.Method == http.MethodPost && path == base+"/login/start":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleLoginStart(req)))
	case req.Method == http.MethodGet && path == base+"/login/poll":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleLoginPollRoute(req)))
	}
	return okEnvelope(mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": "not found: " + path}))
}

func handleLoginStart(req pluginapi.ManagementRequest) map[string]any {
	client := newLoginClient()
	data, _, err := doJSON(client, http.MethodPost, endpointAuthState, nil, bytes.NewReader([]byte("{}")))
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	var st authStateData
	_ = json.Unmarshal(data, &st)
	if st.State == "" || st.AuthURL == "" {
		return map[string]any{"ok": false, "error": "missing state or authUrl"}
	}
	loginStates.Store(st.State, &loginCtx{client: client, expires: time.Now().Add(loginTTL)})
	return map[string]any{
		"ok":    true,
		"url":   st.AuthURL,
		"state": st.State,
	}
}

func handleLoginPollRoute(req pluginapi.ManagementRequest) map[string]any {
	state := strings.TrimSpace(req.Query.Get("state"))
	if state == "" {
		var body struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(req.Body, &body)
		state = strings.TrimSpace(body.State)
	}
	if state == "" {
		return map[string]any{"status": "error", "error": "missing state parameter"}
	}
	pollReq, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: state})
	pollRespRaw, err := handlePollLogin(pollReq)
	if err != nil {
		return map[string]any{"status": "error", "error": err.Error()}
	}
	var env envelope
	if err := json.Unmarshal(pollRespRaw, &env); err != nil || !env.OK {
		msg := "poll error"
		if env.Error != nil {
			msg = env.Error.Message
		}
		return map[string]any{"status": "error", "error": msg}
	}
	var pollResp pluginapi.AuthLoginPollResponse
	_ = json.Unmarshal(env.Result, &pollResp)

	if pollResp.Status == pluginapi.AuthLoginStatusSuccess {
		if len(pollResp.Auth.StorageJSON) > 0 {
			sa, err := parseStored(pollResp.Auth.StorageJSON)
			if err == nil {
				fileName := authFileNameFor(sa)
				authDir := peerAuthDir()
				if authDir == "" {
					if home, _ := os.UserHomeDir(); home != "" {
						authDir = filepath.Join(home, ".cli-proxy-api")
					}
				}
				if authDir != "" {
					targetPath := filepath.Join(authDir, fileName)
					rawJSON, _ := buildAuthFileJSON(sa, false, "WorkBuddy AI", nil)
					_ = writeAuthFileDirect(targetPath, rawJSON)
				}
			}
		}
		return map[string]any{
			"status":  "success",
			"message": "login successful",
		}
	}
	return map[string]any{
		"status":  string(pollResp.Status),
		"message": pollResp.Message,
	}
}

func handleRefreshAsync() map[string]any {
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

func loadedManagementKey() string {
	managementAPIKeyMu.RLock()
	defer managementAPIKeyMu.RUnlock()
	return managementAPIKey
}

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

func mutatingManagementPath(path string) bool {
	base := loadedManagementBasePath() + "/plugins/" + providerName
	switch path {
	case base + "/refresh",
		base + "/checkin",
		base + "/checkin/config",
		base + "/import",
		base + "/export",
		base + "/trial",
		base + "/select",
		base + "/delete",
		base + "/keepalive",
		base + "/login/start":
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
