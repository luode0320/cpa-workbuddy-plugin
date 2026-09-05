// login.go implements the AuthProvider login surface for traework-provider.
//
// Since 0.1.45 the host's native "登录" button drives the SAME browser OAuth
// flow as the panel import. The Trae authorization page's callback shape is
// non-standard (authCodeInfo JSON, no ?code=&state= echo) and lands on a dead
// loopback URL, so the flow needs a bridge page the user can reach:
//
//  1. StartLogin mints a PKCE session (shared browserLoginSessions store)
//     and returns the Trae authorization URL, NOT a data: guide page.
//  2. The user logs in at Trae; the browser bounces to the dead loopback
//     /authorize URL. The user copies that full URL.
//  3. The user pastes it into the host UI's own callback input ("粘贴回调
//     URL"), which POSTs /oauth-callback {redirect_url}. The host extracts
//     state/code from the pasted URL — both empty for the Trae shape — so
//     THAT path is a dead end (live-verified against host source 2026-09-05).
//     Instead the user finishes on the plugin bridge page (resource prefix,
//     GET): it parses the pasted URL client-side, extracts AuthCode from
//     authCodeInfo, and reloads itself with ?code=<AuthCode>&state=<state>.
//  4. The bridge page's code branch settles the session (exchange + identity
//     + import) and PollLogin reports success with the AuthData record so
//     the HOST persists the credential file (the plugin does not double-write).
//
// Why the bridge page lives on the resource prefix: the Trae login tab is a
// plain browser context with no management key; resource routes are the only
// unauthenticated GET surface plugins own. POST is not available there, so
// the bridge form submits via GET self-reload with code+state in the query.
package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// loginStateTTL bounds a host-driven login session. The host registers its
// own OAuth session with a 30-minute TTL; the plugin-side session carries
// the PKCE secrets and must expire sooner so a forgotten tab cannot be
// replayed hours later.
const loginStateTTL = 10 * time.Minute

// hostLoginStatePrefix tags plugin login states minted for the host OAuth
// flow (distinct from panel browser-login states in logs and debug).
const hostLoginStatePrefix = "trw-"

// handleStartLogin implements AuthProvider.StartLogin for the host's native
// OAuth entry (GET /v0/management/traework-provider-auth-url). It reuses the
// panel browser-login machinery: mint PKCE pair + device key, store the
// session under a fresh state, and return the BRIDGE PAGE URL (relative
// path — the host UI opens it from the management origin, so local and
// remote deployments both resolve correctly).
//
// Returning the Trae authorization URL directly would strand the user: after
// login the browser lands on the dead loopback page and the host card's own
// paste input cannot digest the Trae shape (no ?code=&state= → 400). The
// bridge page owns the whole finish flow.
//
// [参数]
//   - raw: 宿主 RPC 请求体（AuthLoginStartRequest JSON，BaseURL 为宿主回调地址）
//
// [返回]
//   - AuthLoginStartResponse：URL=桥接页（相对路径），State=插件会话键
func handleStartLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginStartRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	// Reuse the panel start flow by minting the session directly (the panel
	// handler requires a validated redirect_origin body we do not have).
	// mintBrowserLoginSession stores the session under the state key itself;
	// the bridge page reloads it from the store, so the value is discarded.
	_, state, err := mintBrowserLoginSession()
	if err != nil {
		return nil, err
	}
	// The bridge URL must be RELATIVE: the host UI opens it with
	// window.open from the management origin, so it resolves to wherever
	// the user actually reaches the server (localhost or remote alike).
	bridgeURL := browserLoginBridgeURL(state)
	now := time.Now()
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       bridgeURL,
		State:     state,
		ExpiresAt: now.Add(loginStateTTL).UTC(),
		Metadata: map[string]any{
			"logo":       pluginLogoURL,
			"bridge_url": bridgeURL,
			"prompt":     "在打开的引导页中点击「打开 Trae 登录页面」完成登录，然后把浏览器地址栏的完整回调网址粘贴回引导页提交，此窗口会自动完成。",
		},
	})
}

// handlePollLogin implements AuthProvider.PollLogin for the host OAuth flow.
// State machine over the shared browserLoginSessions store:
//
//   - session settled (Result != nil): report success and hand the host the
//     AuthData record so IT persists the credential file (plugin does not
//     double-write); read-once semantics match the panel result endpoint.
//   - session missing/expired: report error (host marks the session failed).
//   - otherwise: pending — the user has not pasted the callback yet.
func handlePollLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("poll: %w", err)
	}
	state := strings.TrimSpace(req.State)
	if state == "" {
		return nil, fmt.Errorf("poll: empty state")
	}
	rawSession, ok := browserLoginSessions.Load(state)
	if !ok {
		return nil, fmt.Errorf("poll: 授权会话不存在或已完成（10 分钟有效期），请在面板重新发起登录")
	}
	s, ok := rawSession.(*browserLoginSession)
	if !ok || s == nil {
		return nil, fmt.Errorf("poll: 授权会话数据异常，请重新发起登录")
	}
	if time.Now().Sub(s.CreatedAt) > loginStateTTL {
		browserLoginSessions.Delete(state)
		return nil, fmt.Errorf("poll: 登录已超时（10 分钟），请重新发起")
	}
	if s.Result != nil {
		// Settled: the bridge page already exchanged + imported via the panel
		// path (settleBrowserLogin stores the outcome under the same state).
		// Report success with the imported record so the host can persist it.
		browserLoginSessions.Delete(state)
		if !s.Result.OK {
			return okEnvelope(pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: s.Result.Error,
			})
		}
		ad := s.Result.HostAuth
		if ad == nil {
			return okEnvelope(pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: "导入结果缺少凭据记录",
			})
		}
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusSuccess,
			Message: "导入成功：" + s.Result.Label,
			Auth:    *ad,
		})
	}
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusPending,
		Message: "等待在 Trae 页面完成登录，并通过「粘贴回调完成登录」页面提交回调地址",
	})
}

// browserLoginBridgeURL builds the absolute bridge page URL for a state.
// The host's BaseURL points at its own oauth-callback route; the plugin
// resource prefix is fixed (/v0/resource/plugins/<id>/...) and served on the
// same origin, so the bridge URL replaces only the path.
func browserLoginBridgeURL(state string) string {
	return "/v0/resource/plugins/" + providerName + "/browser-login/bridge?state=" + url.QueryEscape(state)
}

// mintBrowserLoginSession mints a fresh PKCE login session (shared with the
// panel flow) and returns it plus the state key. Split out of
// handleBrowserLoginStart so the host OAuth entry can mint the same shape.
func mintBrowserLoginSession() (*browserLoginSession, string, error) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		return nil, "", fmt.Errorf("生成 PKCE 失败: %w", err)
	}
	pemKey, err := deviceKeyPairPEM()
	if err != nil {
		return nil, "", fmt.Errorf("生成设备密钥失败: %w", err)
	}
	machineID := randomHex(32)
	deviceID := randomDeviceID()
	state := hostLoginStatePrefix + randomHex(20)

	// Callback must match the Trae authorization-page whitelist (loopback
	// host + exact /authorize path). Port is cosmetic — nothing listens.
	callbackURL := "http://127.0.0.1:" + browserLoginFallbackPort + "/authorize"
	q := url.Values{}
	q.Set("login_version", "1")
	q.Set("auth_from", "solo")
	q.Set("login_channel", "native_ide")
	q.Set("plugin_version", browserLoginPluginVer)
	q.Set("auth_type", "local")
	q.Set("client_id", browserLoginClientID)
	q.Set("redirect", "0")
	q.Set("login_trace_id", randomTraceID())
	q.Set("auth_callback_url", callbackURL)
	q.Set("machine_id", machineID)
	q.Set("device_id", deviceID)
	q.Set("x_device_id", deviceID)
	q.Set("x_machine_id", machineID)
	q.Set("x_device_brand", "Unknown")
	q.Set("x_device_type", "windows")
	q.Set("x_os_version", "Windows 10 Pro")
	q.Set("x_env", "")
	q.Set("x_app_version", browserLoginAppVer)
	q.Set("x_app_type", "stable")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("hide_saas_login", "true")
	q.Set("channel_name", "common")

	session := &browserLoginSession{
		Verifier:  verifier,
		DeviceID:  deviceID,
		MachineID: machineID,
		DevicePem: pemKey,
		CreatedAt: time.Now(),
	}
	session.AuthURL = browserLoginHost + "/authorization?" + q.Encode()
	browserLoginPurge()
	browserLoginSessions.Store(state, session)
	return session, state, nil
}
