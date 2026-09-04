// browserlogin.go implements the panel-driven browser OAuth login for
// traework-provider ("浏览器授权登录").
//
// The Trae SOLO CN client (verified on 0.1.62, resources/app/out/main.js,
// out-build/vs/code/electron-main/oauth/userLogin/*) logs in through a real
// browser OAuth authorization-code + PKCE(S256) flow:
//
//  1. The client builds an authorization URL on www.trae.cn carrying
//     auth_callback_url=http://127.0.0.1:<port>/authorize, a S256
//     code_challenge, client_id=en1oxy7wnw8j9n (SOLO default, fn Bb in
//     main.js) and x_device_* fingerprint params.
//  2. After the user logs in, the page sends the authorization code to the
//     callback URL.
//  3. The client exchanges the code via
//     POST {loginHost}/trae/api/v3/oauth/ExchangeToken with body
//     {ClientID, AuthCode, CodeVerifier, DeviceInfo{..., DevicePublicKey,
//     PlatformCode:"SOLO_PC"}, IDEVersion}; the response wraps token fields
//     inside {ResponseMetadata, Result}.
//  4. It then loads the user profile via
//     POST {host}/cloudide/api/v3/trae/GetUserInfo (Bearer token, body
//     {ReqSource:"Lite"|"IDE", IDEVersion}) -> {Result:{UserID, Account...}}.
//
// This module replays that flow without an IDE install: the plugin mints the
// PKCE pair + a throwaway EC P-256 device key, points auth_callback_url at
// http://127.0.0.1:<port>/authorize — the ONLY callback shape the Trae
// authorization page accepts (2026-09-04 five-way probe: the whitelist
// requires a loopback host AND the exact /authorize path; protocol and port
// are irrelevant). Nothing listens on that address: after the login the
// browser lands on a dead local URL. IMPORTANT (live-verified 2026-09-05):
// the Trae authorization page is NOT a standard OAuth redirect — it never
// echoes ?code=&state=. The bounce URL instead carries the authorization
// code inside an authCodeInfo JSON query parameter:
//
//	http://127.0.0.1:<port>/authorize?isRedirect=true&scope=solo
//	  &authCodeInfo=%7B%22AuthCode%22:...%7D&loginTraceID=...&host=...
//	  &userRegion=cn&userInfo=%7B...%7D
//
// so both finish paths resolve the code from that JSON and locate the
// session via the state the panel remembers from /start (freshest pending
// session as fallback). The panel asks the user to paste the bounced URL
// back via POST /browser-login/submit (mirroring the host's own manual
// POST /v0/management/oauth/callback {redirect_url} channel). The legacy
// unauthenticated resource callback route
// (/v0/resource/plugins/<id>/browser-login/callback) is kept as the auto
// bounce target for setups that can reach it (e.g. a local forwarder); it
// shares the same session flow as the paste path. The plugin exchanges the
// code server-side, imports the account into the host auth store, and hands
// the panel a credential-free outcome. Tokens never travel in URLs.
//
// Token renewal afterwards is device-independent: the keepalive refresh call
// (POST /cloudide/api/v3/trae/oauth/ExchangeToken) carries only
// ClientID/RefreshToken/ClientSecret/UserID, no device fingerprint.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// SOLO CN client constants observed in TRAE SOLO CN 0.1.62 main.js. The
// client id is the Bb() default for SOLO; plugin_version / x_app_version are
// the shipping client versions mirrored in the authorization URL. Update
// these if upstream tightens version checks.
const (
	browserLoginClientID  = "en1oxy7wnw8j9n"
	browserLoginPluginVer = "2.3.79943"
	browserLoginAppVer    = "0.1.62"
	// browserLoginAuthHost serves the OAuth exchange + profile APIs. The Trae
	// authorization PAGE lives on www.trae.cn, but ExchangeToken/GetUserInfo
	// are API routes: www.trae.cn returns the SPA HTML shell for them
	// (live-verified 2026-09-05: 200 text/html, json parse fails with
	// "invalid character '<'"), while api.trae.cn answers JSON (400/401 on
	// bad credentials = real API). Both api.trae.cn and api.trae.com.cn
	// serve these routes; api.trae.cn matches the host stored in production
	// credentials ("host":"https://api.trae.cn") and the check-in default
	// (defaultAPIHost), so the plugin stays on one auth domain. The
	// authorization URL itself (browserLoginHost) remains www.trae.cn.
	browserLoginAuthHost     = "https://api.trae.cn"
	browserLoginHost         = "https://www.trae.cn"
	browserLoginExchangePath = "/trae/api/v3/oauth/ExchangeToken"
	browserLoginUserInfoPath = "/cloudide/api/v3/trae/GetUserInfo"

	// browserLoginTTL bounds a pending session (and its stored result).
	browserLoginTTL = 10 * time.Minute
)

// resourcePanelPrefix mirrors the resource route prefix in
// handleManagement (management.go); the panel redirect URL is built on it.
const resourcePanelPrefix = "/v0/resource/plugins/" + providerName + "/panel"

// browserLoginCallbackURLPath is the legacy OAuth bounce target served on the
// plugin's unauthenticated resource prefix (host constant, deliberately NOT
// under the configurable management base path: the Trae login page cannot
// present a management key). Kept as the auto bounce target for setups that
// can reach it (e.g. a local forwarder); the authorization URL itself now
// points at the loopback /authorize shape required by the Trae whitelist.
const browserLoginCallbackURLPath = "/v0/resource/plugins/" + providerName + "/browser-login/callback"

// browserLoginFallbackPort is used when the panel origin carries no explicit
// port. The value is cosmetic only: the authorization page whitelist checks
// the host and path, and nothing needs to listen on the port — the user past
// the bounced URL back into the panel instead.
const browserLoginFallbackPort = "8317"

// browserLoginLocalCallback builds the authorization-page callback URL. The
// Trae authorization page only accepts loopback hosts whose path is exactly
// "/authorize" (2026-09-04 five-way probe; protocol and port irrelevant), so
// the plugin mirrors the native client's shape and reuses the panel origin's
// port for a deterministic address the user can recognise.
func browserLoginLocalCallback(origin string) string {
	port := ""
	if u, err := url.Parse(origin); err == nil {
		port = u.Port()
	}
	if port == "" {
		port = browserLoginFallbackPort
	}
	return "http://127.0.0.1:" + port + "/authorize"
}

// browserLoginSession holds one pending (or finished) login attempt.
type browserLoginSession struct {
	Verifier    string
	DeviceID    string
	MachineID   string
	DevicePem   string
	RedirectURI string // panel origin, validated at start
	CreatedAt   time.Time

	// Result is filled by the callback handler; the panel picks it up via
	// /browser-login/result (read-once). Tokens are NOT stored here — the
	// account is already persisted by then.
	Result *browserLoginOutcome
}

// browserLoginOutcome is the panel-facing result snapshot (no credentials).
type browserLoginOutcome struct {
	OK    bool   `json:"ok"`
	Label string `json:"label,omitempty"`
	Error string `json:"error,omitempty"`
}

// browserLoginSessions is keyed by the one-time state value.
var browserLoginSessions sync.Map

// browserLoginPurge drops expired sessions. Called on every start.
func browserLoginPurge() {
	now := time.Now()
	browserLoginSessions.Range(func(key, value any) bool {
		if s, ok := value.(*browserLoginSession); ok && now.Sub(s.CreatedAt) > browserLoginTTL {
			browserLoginSessions.Delete(key)
		}
		return true
	})
}

// browserLoginAuthCodeInfo mirrors the JSON the Trae authorization page
// attaches to the bounce URL as the authCodeInfo query parameter
// (live-verified 2026-09-05: the page is NOT a standard OAuth 302 — it never
// echoes code/state; the authorization code travels inside this JSON).
type browserLoginAuthCodeInfo struct {
	AuthCode       string `json:"AuthCode"`
	ExpireAt       int64  `json:"ExpireAt"`
	ExpireDuration int64  `json:"ExpireDuration"`
}

// extractAuthCode resolves the authorization code from a bounced callback
// query: standard OAuth ?code= first, then the Trae page's authCodeInfo JSON
// form (a case variant is accepted for safety; a malformed JSON yields "").
func extractAuthCode(q url.Values) string {
	if code := strings.TrimSpace(q.Get("code")); code != "" {
		return code
	}
	raw := strings.TrimSpace(q.Get("authCodeInfo"))
	if raw == "" {
		raw = strings.TrimSpace(q.Get("AuthCodeInfo"))
	}
	if raw == "" {
		return ""
	}
	var info browserLoginAuthCodeInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return ""
	}
	return strings.TrimSpace(info.AuthCode)
}

// parseBounceUserInfo extracts (userID, nickname) from the userInfo JSON
// query parameter the Trae authorization page attaches to the bounce URL
// (live shape 2026-09-05: {"AIRegion":"CN","ScreenName":"用户24034744679",
// "TenantID":"...","UserID":"3049391365297084",...}). The SOLO client treats
// this as the PRIMARY identity source (main.js: r ?? await getUserInfo(...)),
// so it is a trusted fallback when the token-based GetUserInfo call fails.
func parseBounceUserInfo(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return "", ""
	}
	userID := pickString(info, "UserID", "userId")
	nickname := pickString(info, "ScreenName", "screenName", "Nickname", "nickname", "Username", "username")
	return userID, nickname
}

// newestPendingSession returns the key and session of the most recently
// created unsettled session. The Trae authorization page never echoes the
// OAuth state back (2026-09-05 live probe), so when neither the pasted URL
// nor the panel body carries one, the pending session is located by recency:
// the panel is a single-operator page and the freshest start is the one the
// user just logged in through. Settled sessions and expired ones are skipped.
func newestPendingSession() (string, *browserLoginSession) {
	var bestKey string
	var best *browserLoginSession
	now := time.Now()
	browserLoginSessions.Range(func(key, value any) bool {
		s, ok := value.(*browserLoginSession)
		if !ok || s == nil || s.Result != nil || now.Sub(s.CreatedAt) > browserLoginTTL {
			return true
		}
		if best == nil || s.CreatedAt.After(best.CreatedAt) {
			if k, ok := key.(string); ok {
				bestKey, best = k, s
			}
		}
		return true
	})
	return bestKey, best
}

// randomTraceID mints a UUID-shaped trace id (random v4-like, no semantics).
// Reuses the shared randomHex helper from upstream.go.
func randomTraceID() string {
	h := randomHex(16)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// randomDeviceID mints a numeric device id (client sample: 3418807932843306,
// 16 digits).
func randomDeviceID() string {
	s := randomHex(8)
	// Keep digits only so the shape matches the native client.
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
		if b.Len() == 16 {
			break
		}
	}
	out := b.String()
	for len(out) < 16 {
		out += "0"
	}
	return out
}

// pkcePair mints an RFC 7636 verifier + S256 challenge pair.
func pkcePair() (verifier, challenge string, err error) {
	raw := make([]byte, 64)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// deviceKeyPairPEM mints a throwaway EC P-256 key pair and returns the public
// key as SPKI PEM (the client sends the same shape as DevicePublicKey).
func deviceKeyPairPEM() (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// validateBrowserLoginOrigin accepts only a bare http(s) origin (no path,
// query, or fragment) so the callback URL can never be weaponized as an open
// redirect or pointed at a third-party host silently.
func validateBrowserLoginOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("redirect_origin 解析失败: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("redirect_origin 必须是 http/https 地址")
	}
	if u.Host == "" {
		return "", fmt.Errorf("redirect_origin 缺少主机名")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("redirect_origin 只允许填 origin，不允许带路径")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("redirect_origin 不允许携带 query/fragment/用户信息")
	}
	return u.Scheme + "://" + u.Host, nil
}

// handleBrowserLoginStart implements POST /browser-login/start.
// Body: {redirect_origin}. Returns {ok, auth_url, expires_in}.
// The route requires the management key (mutatingManagementPath).
//
// [参数]
//   - req: 宿主转发来的管理 API 请求（Body 为 {redirect_origin} JSON）
//
// [返回]
//   - map[string]any: {ok, auth_url, expires_in} 或 {error}
func handleBrowserLoginStart(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		RedirectOrigin string `json:"redirect_origin"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return map[string]any{"error": "body {redirect_origin} required"}
	}
	origin, err := validateBrowserLoginOrigin(body.RedirectOrigin)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	browserLoginPurge()

	verifier, challenge, err := pkcePair()
	if err != nil {
		return map[string]any{"error": "生成 PKCE 失败: " + err.Error()}
	}
	pemKey, err := deviceKeyPairPEM()
	if err != nil {
		return map[string]any{"error": "生成设备密钥失败: " + err.Error()}
	}
	machineID := randomHex(32)
	deviceID := randomDeviceID()
	state := randomHex(24)

	// The callback must match the Trae authorization-page whitelist shape
	// (loopback host + exact /authorize path). Nothing listens there: the
	// panel asks the user to paste the bounced URL back via
	// /browser-login/submit (see handleBrowserLoginSubmit).
	callbackURL := browserLoginLocalCallback(origin)
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
	// OAuth state: the authorization server must echo it back on the
	// callback (?code=&state=) so handleBrowserLoginCallback can match the
	// bounce to the minted session. Without it the callback cannot find
	// the session and every login would fail after the exchange-less
	// state lookup.
	q.Set("state", state)
	q.Set("hide_saas_login", "true")
	q.Set("channel_name", "common")

	browserLoginSessions.Store(state, &browserLoginSession{
		Verifier:    verifier,
		DeviceID:    deviceID,
		MachineID:   machineID,
		DevicePem:   pemKey,
		RedirectURI: origin,
		CreatedAt:   time.Now(),
	})
	log.Printf("[browser-login] session started: state=%s callback=%s", stateLogPrefix(state), truncateRedacted(callbackURL, 120))
	return map[string]any{
		"ok":       true,
		"auth_url": browserLoginHost + "/authorization?" + q.Encode(),
		// The panel remembers this and sends it back on /browser-login/submit:
		// the Trae authorization page never echoes state on the bounce
		// (2026-09-05 live probe), so this is the primary session locator.
		"state":      state,
		"expires_in": int(browserLoginTTL.Seconds()),
	}
}

// handleBrowserLoginCallback implements the OAuth bounce target registered as
// a RESOURCE route: /v0/resource/plugins/<id>/browser-login/callback. The Trae
// login page navigates here with ?code=...&state=... after a successful
// login. This route is intentionally free of the management key (resource
// routes bypass the host management-key middleware, and their responses skip
// the host HTML escaping applied to management JSON): the one-time state
// value IS the credential for finishing the flow (echoed back by the
// authorization server from the ?state= minted at start), mirroring how the
// native client's 127.0.0.1 callback server accepts any local request.
//
// On success the account is imported into the host auth store, a result
// snapshot is stored under the state key, and the browser is bounced back to
// the panel with ?auth_cb=<state>. The panel then reads the (credential-free)
// outcome via POST /browser-login/result.
func handleBrowserLoginCallback(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	q := req.Query
	state := strings.TrimSpace(q.Get("state"))
	code := extractAuthCode(q)
	authErr := strings.TrimSpace(q.Get("error"))
	if authErr == "" {
		authErr = strings.TrimSpace(q.Get("error_description"))
	}
	if state == "" {
		// The Trae authorization page never echoes state (2026-09-05 live
		// probe): locate the pending session by recency, mirroring the
		// paste-submit path.
		state, _ = newestPendingSession()
	}
	if state == "" || (code == "" && authErr == "") {
		return browserLoginHTMLPage("", "授权回调缺少授权码，请回到面板重新发起浏览器授权登录。")
	}
	rawSession, ok := browserLoginSessions.Load(state)
	if !ok {
		return browserLoginHTMLPage("", "授权会话不存在或已过期（10 分钟有效期），请回到面板重新发起。")
	}
	session := rawSession.(*browserLoginSession)
	panelURL := session.RedirectURI + resourcePanelPrefix + "?auth_cb=" + url.QueryEscape(state)
	outcome := settleBrowserLogin(state, session, code, authErr, strings.TrimSpace(q.Get("userInfo")))
	logBrowserLoginOutcome(state, outcome)
	return browserLoginHTMLPage(panelURL, "")
}

// settleBrowserLogin consumes the pending session (one state, one exchange
// attempt), runs the code exchange + account import, and re-stores the
// session WITH the outcome under the same state key so the panel can pick it
// up read-once via /browser-login/result (tokens are never included;
// CreatedAt is preserved so the purge TTL also covers stale results). Shared
// by the auto resource callback and the manual paste submit path; authErr
// short-circuits the exchange (authorization-server-reported failure).
func settleBrowserLogin(state string, session *browserLoginSession, code, authErr, bounceUserInfo string) *browserLoginOutcome {
	browserLoginSessions.Delete(state)
	var outcome *browserLoginOutcome
	if authErr != "" {
		outcome = &browserLoginOutcome{Error: "授权失败：" + authErr}
	} else {
		outcome = finishBrowserLogin(session, code, bounceUserInfo)
	}
	browserLoginSessions.Store(state, &browserLoginSession{
		Verifier:    session.Verifier,
		DeviceID:    session.DeviceID,
		MachineID:   session.MachineID,
		DevicePem:   session.DevicePem,
		RedirectURI: session.RedirectURI,
		CreatedAt:   session.CreatedAt,
		Result:      outcome,
	})
	return outcome
}

// stateLogPrefix renders the redacted log prefix for a state value. The
// submit path accepts user-pasted URLs, so the state may be any length.
func stateLogPrefix(state string) string {
	if len(state) <= 8 {
		return state
	}
	return state[:8] + "..."
}

// logBrowserLoginOutcome emits the shared redacted outcome log line.
func logBrowserLoginOutcome(state string, outcome *browserLoginOutcome) {
	if outcome.Error != "" {
		log.Printf("[browser-login] exchange failed: state=%s error=%s", stateLogPrefix(state), truncateRedacted(outcome.Error, 200))
	} else {
		log.Printf("[browser-login] account imported: state=%s label=%s", stateLogPrefix(state), truncateRedacted(outcome.Label, 80))
	}
}

// handleBrowserLoginSubmit implements POST /browser-login/submit (management
// key guarded). Body: {url, state?} — the FULL bounced callback URL the user
// pasted from the browser address bar, plus the state the panel remembered
// from /start (primary session locator; optional). The Trae authorization
// page only accepts a loopback /authorize callback and nothing listens
// there, so after the login the browser lands on a dead local URL. That
// bounce is NOT standard OAuth (live-verified 2026-09-05): it carries the
// authorization code inside authCodeInfo={AuthCode,...} and never echoes
// code/state, so the code is resolved via extractAuthCode and the session is
// located by body.state, then ?state=, then the freshest pending session
// (single-operator panel). A bare "code=...&state=..." query string is
// accepted too.
//
// [参数]
//   - req: 宿主转发来的管理 API 请求（Body 为 {url, state?} JSON）
//
// [返回]
//   - map[string]any: {ok, label?, error?}；结果同时写回 state 会话供
//     /browser-login/result 读后即焚。
func handleBrowserLoginSubmit(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || trimSpace(body.URL) == "" {
		return map[string]any{"error": "body {url} required"}
	}
	raw := strings.TrimSpace(body.URL)
	if !strings.Contains(raw, "://") {
		raw = "http://localhost/?" + strings.TrimPrefix(raw, "?")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return map[string]any{"error": "回调地址解析失败：" + err.Error()}
	}
	q := u.Query()
	state := strings.TrimSpace(body.State)
	if state == "" {
		state = strings.TrimSpace(q.Get("state"))
	}
	code := extractAuthCode(q)
	authErr := strings.TrimSpace(q.Get("error"))
	if authErr == "" {
		authErr = strings.TrimSpace(q.Get("error_description"))
	}
	// Locate the pending session: explicit state (panel-remembered or URL
	// echoed) first, freshest-pending fallback when absent.
	var session *browserLoginSession
	if state != "" {
		rawSession, ok := browserLoginSessions.Load(state)
		if !ok {
			return map[string]any{"error": "授权会话不存在或已过期（10 分钟有效期），请回到面板重新发起浏览器授权登录"}
		}
		s, ok := rawSession.(*browserLoginSession)
		if !ok || s == nil {
			return map[string]any{"error": "授权会话数据异常，请重新发起浏览器授权登录"}
		}
		session = s
	} else {
		state, session = newestPendingSession()
		if session == nil {
			return map[string]any{"error": "无法定位授权会话：请回到面板重新发起浏览器授权登录，登录后再粘贴回调地址"}
		}
	}
	if session.Result != nil {
		return map[string]any{"error": "该授权已被处理（结果只保留一次），如需重新登录请回到面板再次发起"}
	}
	if code == "" && authErr == "" {
		return map[string]any{"error": "回调地址缺少授权码（code）：请在登录成功后复制浏览器地址栏的完整网址"}
	}
	outcome := settleBrowserLogin(state, session, code, authErr, strings.TrimSpace(q.Get("userInfo")))
	logBrowserLoginOutcome(state, outcome)
	return map[string]any{
		"ok":    outcome.OK,
		"label": outcome.Label,
		"error": outcome.Error,
	}
}

// Test seams: the exchange and profile calls hit the real Trae upstream via
// the host HTTP bridge; tests swap these to fake upstream responses.
var (
	browserLoginExchangeFn = browserLoginExchange
	browserLoginUserInfoFn = browserLoginUserInfo
)

// finishBrowserLogin exchanges the auth code, fetches the user profile, and
// imports the account. Returns the panel-facing outcome (never credentials).
// The bounce URL's userInfo query parameter (JSON with UserID/ScreenName)
// serves as the identity fallback: the SOLO client itself prefers it over
// calling GetUserInfo (main.js: o = r ?? await this.getUserInfo(...)), and
// live testing 2026-09-05 showed the freshly-exchanged token can fail
// GetUserInfo with 401 "The user is not logged in" (cookie-session based —
// the bearer token alone does not authenticate that route).
func finishBrowserLogin(session *browserLoginSession, code string, bounceUserInfo string) *browserLoginOutcome {
	result, rawResult, err := browserLoginExchangeFn(session, code)
	if err != nil {
		return &browserLoginOutcome{Error: "换取 token 失败：" + err.Error()}
	}
	userID, nickname, err := browserLoginUserInfoFn(result.Token)
	if err != nil {
		// Fall back to the bounce URL's userInfo (the authorization page
		// already knows who logged in — it put the profile in the callback).
		fbID, fbNick := parseBounceUserInfo(bounceUserInfo)
		if fbID == "" {
			return &browserLoginOutcome{Error: "获取用户信息失败：" + err.Error()}
		}
		userID, nickname = fbID, fbNick
	}
	if strings.TrimSpace(userID) == "" {
		return &browserLoginOutcome{Error: "上游未返回 userId，无法入库"}
	}
	a := &traeAuth{
		Token:            result.Token,
		RefreshToken:     result.RefreshToken,
		ExpiredAt:        result.ExpiredAt,
		RefreshExpiredAt: result.RefreshExpiredAt,
		UserID:           userID,
		Nickname:         nickname,
		// Host stays empty: chat/checkin resolve via the plugin's own
		// host constants (same behavior as fresh paste imports).
		DeviceID:      session.DeviceID,
		MachineID:     session.MachineID,
		CredentialRaw: rawResult,
	}
	// Dedup by UserID against the host auth list (same contract as
	// handleImportCredential): re-importing an existing account must not
	// create a second auth record.
	duplicate := ""
	if files, lerr := hostAuthList(); lerr == nil {
		for _, f := range files {
			if trimSpace(f.AuthIndex) == "" {
				continue
			}
			ex, _, gerr := hostAuthGetBundle(f.AuthIndex)
			if gerr != nil || ex == nil {
				continue
			}
			if trimSpace(ex.UserID) != "" && trimSpace(ex.UserID) == trimSpace(a.UserID) {
				duplicate = f.AuthIndex
				break
			}
		}
	}
	label := a.Nickname
	if label == "" {
		label = a.UserID
	}
	if duplicate != "" {
		return &browserLoginOutcome{OK: true, Label: label + "（账号已存在，未重复导入）"}
	}
	raw, berr := buildAuthFileJSON(a, false, "imported via browser login", nil)
	if berr != nil {
		return &browserLoginOutcome{Error: "凭据文件构建失败：" + berr.Error()}
	}
	if serr := hostAuthSaveJSON(authFileNameFor(a), raw); serr != nil {
		return &browserLoginOutcome{Error: "凭据保存失败：" + serr.Error()}
	}
	return &browserLoginOutcome{OK: true, Label: label}
}

// browserLoginToken mirrors the rotated token envelope shared with
// keepaliveExchange; fields are read case-insensitively from Result.
type browserLoginToken struct {
	Token            string
	RefreshToken     string
	ExpiredAt        string
	RefreshExpiredAt string
	TokenReleaseAt   string
}

// pickString returns the first non-empty value among case variants.
func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && trimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

// browserLoginExchange calls POST {host}/trae/api/v3/oauth/ExchangeToken with
// the auth code + PKCE verifier + device info, mirroring
// exchangeTokenByAuthCode in the SOLO client main.js. The response wraps the
// token fields inside {ResponseMetadata, Result}; Result key casing has been
// observed both PascalCase and camelCase upstream, so both are accepted.
func browserLoginExchange(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
	deviceInfo := map[string]any{
		"DeviceID":        session.DeviceID,
		"MachineID":       session.MachineID,
		"PlatformCode":    "SOLO_PC",
		"DeviceType":      "PC",
		"DeviceName":      "TRAE-SOLO",
		"DeviceModel":     "",
		"ClientVersion":   browserLoginAppVer,
		"DevicePublicKey": session.DevicePem,
		"DeviceBrand":     "Unknown",
		"DeviceCPU":       "",
		"OSInfo":          "Windows",
		"OSVersion":       "Windows 10 Pro",
	}
	body, _ := json.Marshal(map[string]any{
		"ClientID":     browserLoginClientID,
		"AuthCode":     code,
		"CodeVerifier": session.Verifier,
		"DeviceInfo":   deviceInfo,
		"IDEVersion":   browserLoginAppVer,
	})
	req, err := http.NewRequest(http.MethodPost, browserLoginAuthHost+browserLoginExchangePath, strings.NewReader(string(body)))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, "", err
	}
	raw := resp.Body
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("HTTP %d %s", resp.StatusCode, truncateRedacted(string(raw), 160))
	}
	var env struct {
		ResponseMetadata *struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"ResponseMetadata"`
		Result map[string]any `json:"Result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "", fmt.Errorf("响应解析失败: %w", err)
	}
	if env.ResponseMetadata != nil && env.ResponseMetadata.Error != nil && trimSpace(env.ResponseMetadata.Error.Code) != "" {
		return nil, "", fmt.Errorf("上游错误 %s: %s", env.ResponseMetadata.Error.Code, truncateRedacted(env.ResponseMetadata.Error.Message, 120))
	}
	if env.Result == nil {
		return nil, "", fmt.Errorf("响应缺少 Result: %s", truncateRedacted(string(raw), 120))
	}
	tok := &browserLoginToken{
		Token:            pickString(env.Result, "Token", "token"),
		RefreshToken:     pickString(env.Result, "RefreshToken", "refreshToken"),
		ExpiredAt:        pickString(env.Result, "ExpiredAt", "expiredAt"),
		RefreshExpiredAt: pickString(env.Result, "RefreshExpiredAt", "refreshExpiredAt"),
		TokenReleaseAt:   pickString(env.Result, "TokenReleaseAt", "tokenReleaseAt"),
	}
	if trimSpace(tok.Token) == "" {
		return nil, "", fmt.Errorf("响应缺少 token 字段: %s", truncateRedacted(string(raw), 120))
	}
	return tok, string(raw), nil
}

// browserLoginUserInfo loads the user profile via
// POST {host}/cloudide/api/v3/trae/GetUserInfo (Bearer token, body
// {ReqSource:"Lite", IDEVersion}) and returns (userID, nickname).
// ReqSource "Lite" is what the SOLO client sends (qr(this.d) -> Lite).
func browserLoginUserInfo(token string) (string, string, error) {
	body, _ := json.Marshal(map[string]string{
		"ReqSource":  "Lite",
		"IDEVersion": browserLoginAppVer,
	})
	req, err := http.NewRequest(http.MethodPost, browserLoginAuthHost+browserLoginUserInfoPath, strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("HTTP %d %s", resp.StatusCode, truncateRedacted(string(resp.Body), 160))
	}
	var env struct {
		ResponseMetadata *struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"ResponseMetadata"`
		Result map[string]any `json:"Result"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return "", "", fmt.Errorf("响应解析失败: %w", err)
	}
	if env.ResponseMetadata != nil && env.ResponseMetadata.Error != nil && trimSpace(env.ResponseMetadata.Error.Code) != "" {
		return "", "", fmt.Errorf("上游错误 %s: %s", env.ResponseMetadata.Error.Code, truncateRedacted(env.ResponseMetadata.Error.Message, 120))
	}
	if env.Result == nil {
		// camelCase envelope variant observed on cloudide routes.
		var envLower struct {
			Result map[string]any `json:"result"`
		}
		if json.Unmarshal(resp.Body, &envLower) == nil && envLower.Result != nil {
			env.Result = envLower.Result
		}
	}
	if env.Result == nil {
		return "", "", fmt.Errorf("响应缺少 Result: %s", truncateRedacted(string(resp.Body), 120))
	}
	userID := pickString(env.Result, "UserID", "userId")
	nickname := pickString(env.Result, "Nickname", "nickname", "Username", "username")
	if nickname == "" {
		if acc, ok := env.Result["Account"].(map[string]any); ok {
			nickname = pickString(acc, "username", "Username", "email", "Email")
		}
	}
	return userID, nickname, nil
}

// handleBrowserLoginResult implements POST /browser-login/result.
// Body: {state}. Returns the stored outcome snapshot (read-once) so the
// panel can show the result after the redirect. No credentials are included.
func handleBrowserLoginResult(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(req.Body, &body)
	state := trimSpace(body.State)
	if state == "" {
		return map[string]any{"error": "body {state} required"}
	}
	raw, ok := browserLoginSessions.Load(state)
	if !ok {
		return map[string]any{"error": "授权结果不存在或已被读取（结果只保留一次，10 分钟有效）"}
	}
	session, ok := raw.(*browserLoginSession)
	if !ok || session == nil || session.Result == nil {
		// Session still pending: the callback has not arrived (yet). Keep
		// the session intact — an early poll must not kill the flow.
		return map[string]any{"ok": false, "pending": true, "error": "授权尚未完成：请在浏览器中完成 TRAE 登录"}
	}
	// Read-once: drop the session only after handing out the outcome.
	browserLoginSessions.Delete(state)
	return map[string]any{
		"ok":    session.Result.OK,
		"label": session.Result.Label,
		"error": session.Result.Error,
	}
}

// browserLoginHTMLPage renders the post-callback bounce page. The Trae login
// tab is redirected back to the panel via meta refresh + JS + a manual link
// (triple fallback so the redirect survives hosts that strip headers).
// detail is shown only when something went wrong before a panel URL exists.
func browserLoginHTMLPage(panelURL, detail string) pluginapi.ManagementResponse {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<title>TraeWork 授权登录</title>")
	if panelURL != "" {
		b.WriteString("<meta http-equiv=\"refresh\" content=\"1;url=" + htmlEscape(panelURL) + "\">")
	}
	b.WriteString("<style>body{font-family:-apple-system,'Segoe UI','Microsoft YaHei',sans-serif;background:#f5f6f8;color:#1f2328;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}.card{background:#fff;border-radius:12px;box-shadow:0 2px 12px rgba(0,0,0,.08);padding:28px 36px;max-width:480px;text-align:center}p{line-height:1.7;font-size:14px;margin:8px 0}a{color:#2563eb}</style>")
	b.WriteString("</head><body><div class=\"card\">")
	if panelURL != "" {
		b.WriteString("<p>授权处理完成，正在返回 TraeWork 面板…</p>")
		b.WriteString("<p><a href=\"" + htmlEscape(panelURL) + "\">若未自动跳转，请点击这里</a></p>")
		b.WriteString("<script>setTimeout(function(){location.replace(" + jsQuote(panelURL) + ")},50)</script>")
	} else {
		b.WriteString("<p>" + htmlEscape(detail) + "</p>")
	}
	b.WriteString("</div></body></html>")
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: []byte(b.String())}
}

// htmlEscape escapes the five HTML-critical characters for attribute/text use.
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// jsQuote renders a JS double-quoted string literal with JSON escaping.
func jsQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
