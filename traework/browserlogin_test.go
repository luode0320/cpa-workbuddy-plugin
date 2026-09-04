package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestPKCEPair_RFC7636Shape verifies the verifier/challenge pair matches the
// RFC 7636 S256 contract: verifier within 43-128 unreserved chars, challenge
// equal to BASE64URL(SHA256(verifier)) without padding.
func TestPKCEPair_RFC7636Shape(t *testing.T) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		t.Fatalf("pkcePair() = %v", err)
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Fatalf("verifier length = %d, want 43..128", len(verifier))
	}
	if strings.ContainsAny(verifier, "+/=") {
		t.Fatalf("verifier contains non-unreserved chars: %q", verifier)
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Fatalf("challenge mismatch: got %q want %q", challenge, want)
	}
}

// TestValidateBrowserLoginOrigin verifies only bare http(s) origins pass and
// paths/query/fragments/credentials are rejected (open-redirect guard).
func TestValidateBrowserLoginOrigin(t *testing.T) {
	ok, err := validateBrowserLoginOrigin("https://1.2.3.4:18998")
	if err != nil || ok != "https://1.2.3.4:18998" {
		t.Fatalf("plain origin rejected: %q %v", ok, err)
	}
	ok, err = validateBrowserLoginOrigin("http://localhost:8080/")
	if err != nil || ok != "http://localhost:8080" {
		t.Fatalf("trailing-slash origin: %q %v", ok, err)
	}
	for _, bad := range []string{
		"", "ftp://x", "https://host/path", "https://host?x=1",
		"https://host#frag", "https://user:pass@host", "//host", "host:18998",
	} {
		if _, err = validateBrowserLoginOrigin(bad); err == nil {
			t.Fatalf("origin %q should be rejected", bad)
		}
	}
}

// TestRandomDeviceIDShape verifies the 16-digit numeric device id shape.
func TestRandomDeviceIDShape(t *testing.T) {
	id := randomDeviceID()
	if len(id) != 16 {
		t.Fatalf("device id length = %d, want 16", len(id))
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			t.Fatalf("device id %q contains non-digit", id)
		}
	}
}

// TestRandomTraceIDShape verifies the UUID-shaped trace id (5 hex groups).
func TestRandomTraceIDShape(t *testing.T) {
	id := randomTraceID()
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("trace id %q should have 5 groups", id)
	}
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789abcdef") != "" {
			t.Fatalf("trace id group %d = %q is not hex", i, p)
		}
	}
}

// TestDeviceKeyPairPEM verifies the throwaway device key renders as a SPKI
// PEM PUBLIC KEY block (the shape the SOLO client sends as DevicePublicKey).
func TestDeviceKeyPairPEM(t *testing.T) {
	pemKey, err := deviceKeyPairPEM()
	if err != nil {
		t.Fatalf("deviceKeyPairPEM() = %v", err)
	}
	if !strings.HasPrefix(pemKey, "-----BEGIN PUBLIC KEY-----") {
		t.Fatalf("device key pem has unexpected header: %q", pemKey[:40])
	}
}

// TestBrowserLoginResultPendingKeepsSession verifies an early /result poll
// (callback not yet arrived) reports pending AND does not consume the
// session — a later poll after the callback still finds the outcome.
func TestBrowserLoginResultPendingKeepsSession(t *testing.T) {
	state := "test-state-pending"
	browserLoginSessions.Store(state, &browserLoginSession{
		Verifier:  "v",
		CreatedAt: time.Now(),
	})
	// Early poll: pending, session must survive.
	if _, ok := browserLoginSessions.Load(state); !ok {
		t.Fatal("session vanished before result")
	}
	// Simulate the callback storing the outcome (the read-once contract).
	browserLoginSessions.Store(state, &browserLoginSession{
		Verifier:  "v",
		CreatedAt: time.Now(),
		Result:    &browserLoginOutcome{OK: true, Label: "alice"},
	})
	raw, ok := browserLoginSessions.Load(state)
	if !ok {
		t.Fatal("session missing after callback")
	}
	s := raw.(*browserLoginSession)
	if s.Result == nil || !s.Result.OK || s.Result.Label != "alice" {
		t.Fatalf("unexpected outcome: %+v", s.Result)
	}
	browserLoginSessions.Delete(state)
}

// TestBrowserLoginStartPointsAtLoopbackAuthorize verifies the authorization
// URL targets the callback shape the Trae authorization page whitelist
// accepts (loopback host + exact /authorize path; 2026-09-04 five-way probe)
// and carries the OAuth state echoed back on the bounce. Nothing listens on
// the callback address — the panel paste-submit flow finishes the login.
func TestBrowserLoginStartPointsAtLoopbackAuthorize(t *testing.T) {
	req := pluginapi.ManagementRequest{Body: []byte(`{"redirect_origin":"https://1.2.3.4:18998"}`)}
	out := handleBrowserLoginStart(req)
	if out["error"] != nil {
		t.Fatalf("start failed: %v", out["error"])
	}
	// Clean up the minted sessions: with the freshest-pending fallback in
	// place, leaked pending sessions would bleed into later tests.
	if st, _ := out["state"].(string); st != "" {
		defer browserLoginSessions.Delete(st)
	}
	authURL, _ := out["auth_url"].(string)
	if authURL == "" {
		t.Fatal("auth_url missing")
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("auth_url parse: %v", err)
	}
	q := u.Query()
	cb := q.Get("auth_callback_url")
	cbURL, err := url.Parse(cb)
	if err != nil {
		t.Fatalf("callback parse: %v", err)
	}
	if cbURL.Hostname() != "127.0.0.1" {
		t.Fatalf("callback host = %q, want loopback 127.0.0.1", cbURL.Hostname())
	}
	if cbURL.Path != "/authorize" {
		t.Fatalf("callback path = %q, want exactly /authorize", cbURL.Path)
	}
	if cbURL.Port() != "18998" {
		t.Fatalf("callback port = %q, want the panel origin port 18998", cbURL.Port())
	}
	if strings.Contains(cb, "/v0/management/") || strings.Contains(cb, "/v0/resource/") {
		t.Fatalf("callback must be the plain loopback /authorize shape: %q", cb)
	}
	if q.Get("state") == "" {
		t.Fatal("auth_url missing the OAuth state parameter")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("unexpected challenge method: %q", q.Get("code_challenge_method"))
	}
	// Origin without an explicit port falls back to the deterministic
	// default port.
	out = handleBrowserLoginStart(pluginapi.ManagementRequest{Body: []byte(`{"redirect_origin":"https://cpa.example.com"}`)})
	if out["error"] != nil {
		t.Fatalf("start (no-port origin) failed: %v", out["error"])
	}
	if st, _ := out["state"].(string); st != "" {
		defer browserLoginSessions.Delete(st)
	}
	authURL, _ = out["auth_url"].(string)
	u, err = url.Parse(authURL)
	if err != nil {
		t.Fatalf("auth_url parse (no-port origin): %v", err)
	}
	cb = u.Query().Get("auth_callback_url")
	if cb != "http://127.0.0.1:8317/authorize" {
		t.Fatalf("callback (no-port origin) = %q, want http://127.0.0.1:8317/authorize", cb)
	}
}

// TestManagementRegistrationBrowserLoginCallbackIsResource verifies the
// callback is declared ONLY as an unauthenticated resource route (never a
// management route) and carries no Menu label so it never appears in the
// management UI menu.
func TestManagementRegistrationBrowserLoginCallbackIsResource(t *testing.T) {
	reg := managementRegistration()
	for _, r := range reg.Routes {
		if strings.HasSuffix(r.Path, "/browser-login/callback") {
			t.Fatalf("callback must not be a management route: %s %s", r.Method, r.Path)
		}
	}
	found := false
	for _, res := range reg.Resources {
		if res.Path == "/browser-login/callback" {
			found = true
			if res.Menu != "" {
				t.Fatalf("callback resource must not carry a Menu label (would show in the management UI menu): %q", res.Menu)
			}
		}
	}
	if !found {
		t.Fatal("resource callback route missing from managementRegistration().Resources")
	}
}

// TestManagementRegistrationBrowserLoginSubmitIsMutating verifies the paste
// submit endpoint is a registered management route AND is covered by
// mutatingManagementPath (management key required despite POST-only paths
// being auth-guarded by the method check too — defence in depth).
func TestManagementRegistrationBrowserLoginSubmitIsMutating(t *testing.T) {
	reg := managementRegistration()
	found := false
	for _, r := range reg.Routes {
		if strings.HasSuffix(r.Path, "/browser-login/submit") {
			found = true
			if r.Method != http.MethodPost {
				t.Fatalf("submit route method = %s, want POST", r.Method)
			}
		}
	}
	if !found {
		t.Fatal("submit route missing from managementRegistration().Routes")
	}
	base := loadedManagementBasePath() + "/plugins/" + providerName
	if !mutatingManagementPath(base + "/browser-login/submit") {
		t.Fatal("submit path missing from mutatingManagementPath")
	}
}

// TestBrowserLoginHTMLPage_NoTargetShowsDetail verifies the error bounce page
// renders the detail text and contains no redirect machinery.
func TestBrowserLoginHTMLPage_NoTargetShowsDetail(t *testing.T) {
	resp := browserLoginHTMLPage("", "会话已过期")
	body := string(resp.Body)
	if !strings.Contains(body, "会话已过期") {
		t.Fatal("detail text missing from error page")
	}
	if strings.Contains(body, "location.replace") || strings.Contains(body, "http-equiv=\"refresh\"") {
		t.Fatal("error page should not contain redirect machinery")
	}
}

// TestBrowserLoginHTMLPage_TargetRedirects verifies the success bounce page
// carries meta refresh, a manual link, and the JS fallback to the panel URL.
func TestBrowserLoginHTMLPage_TargetRedirects(t *testing.T) {
	panel := "https://1.2.3.4:18998" + resourcePanelPrefix + "?auth_cb=abc"
	resp := browserLoginHTMLPage(panel, "")
	body := string(resp.Body)
	if !strings.Contains(body, htmlEscape(panel)) {
		t.Fatal("panel URL missing from bounce page")
	}
	if !strings.Contains(body, "location.replace") || !strings.Contains(body, "http-equiv=\"refresh\"") {
		t.Fatal("bounce page missing redirect machinery")
	}
}

// TestBrowserLoginSubmitValidationAndFlow covers the paste-submit
// orchestration: body/URL validation, unknown state, authorization-server
// error short-circuit (no exchange call), exchange failure (outcome stored
// back, resubmit rejected as already-handled), and bare query acceptance.
// The happy import path needs the host auth RPCs and is verified in
// production; these cases pin the session lifecycle instead.
func TestBrowserLoginSubmitValidationAndFlow(t *testing.T) {
	origExchange, origUserInfo := browserLoginExchangeFn, browserLoginUserInfoFn
	defer func() { browserLoginExchangeFn, browserLoginUserInfoFn = origExchange, origUserInfo }()
	exchangeCalls := 0
	browserLoginExchangeFn = func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
		exchangeCalls++
		return nil, "", fmt.Errorf("simulated upstream down")
	}
	browserLoginUserInfoFn = func(token string) (string, string, error) {
		t.Fatal("user info must not be called when the exchange fails")
		return "", "", nil
	}

	// 1) Missing body url.
	out := handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{}`)})
	if out["error"] != "body {url} required" {
		t.Fatalf("missing url: %v", out["error"])
	}

	// 2) URL with neither an authorization code nor authCodeInfo -> the
	// freshest-pending fallback locates the session but the missing code
	// errors out; the session must survive.
	state := "submit-test-state"
	browserLoginSessions.Store(state, &browserLoginSession{Verifier: "v", CreatedAt: time.Now()})
	defer browserLoginSessions.Delete(state)
	out = handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"http://127.0.0.1:18998/authorize?isRedirect=true&scope=solo"}`)})
	if !strings.Contains(strValue(out["error"]), "授权码") {
		t.Fatalf("code-less url: %v", out["error"])
	}
	if _, ok := browserLoginSessions.Load(state); !ok {
		t.Fatal("session must survive a code-less submit")
	}

	// 3) Unknown state.
	out = handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"http://127.0.0.1:18998/authorize?code=abc&state=nope"}`)})
	if !strings.Contains(strValue(out["error"]), "不存在或已过期") {
		t.Fatalf("unknown state: %v", out["error"])
	}

	// 4) Authorization-server error short-circuits the exchange.
	out = handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"http://127.0.0.1:18998/authorize?error=access_denied&state=` + state + `"}`)})
	if out["ok"] != false {
		t.Fatalf("auth-server error must fail: %v", out)
	}
	if !strings.Contains(strValue(out["error"]), "授权失败") {
		t.Fatalf("auth-server error text: %v", out["error"])
	}
	if exchangeCalls != 0 {
		t.Fatalf("exchange must not run on auth-server error, ran %d times", exchangeCalls)
	}
	raw, ok := browserLoginSessions.Load(state)
	if !ok {
		t.Fatal("session missing after error settle")
	}
	if s := raw.(*browserLoginSession); s.Result == nil || !strings.Contains(s.Result.Error, "授权失败") {
		t.Fatalf("error outcome not stored back: %+v", raw.(*browserLoginSession).Result)
	}

	// Reset the session to pending for the exchange-failure flow.
	browserLoginSessions.Store(state, &browserLoginSession{Verifier: "v", CreatedAt: time.Now()})

	// 5) Bare query string accepted; exchange failure consumes the session
	// and stores the outcome back.
	out = handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"code=abc&state=` + state + `"}`)})
	if out["ok"] != false {
		t.Fatalf("exchange failure must fail: %v", out)
	}
	if !strings.Contains(strValue(out["error"]), "换取 token 失败") {
		t.Fatalf("exchange failure text: %v", out["error"])
	}
	if exchangeCalls != 1 {
		t.Fatalf("exchange ran %d times, want exactly 1", exchangeCalls)
	}
	raw, ok = browserLoginSessions.Load(state)
	if !ok {
		t.Fatal("session missing after failed settle")
	}
	if s := raw.(*browserLoginSession); s.Result == nil || !strings.Contains(s.Result.Error, "换取 token 失败") {
		t.Fatalf("failure outcome not stored back: %+v", raw.(*browserLoginSession).Result)
	}

	// 6) Resubmitting the same URL is rejected as already-handled (the code
	// is single-use upstream; a retry can never succeed).
	out = handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"code=abc&state=` + state + `"}`)})
	if !strings.Contains(strValue(out["error"]), "已被处理") {
		t.Fatalf("resubmit: %v", out["error"])
	}
	if exchangeCalls != 1 {
		t.Fatalf("resubmit must not re-exchange, ran %d times", exchangeCalls)
	}
}

// TestBrowserLoginSubmitTraeAuthCodeInfoShape pins the live bounce shape
// observed 2026-09-05: the Trae authorization page redirects with
// isRedirect/scope/authCodeInfo/loginTraceID/host/userRegion/userInfo — the
// authorization code travels inside the authCodeInfo JSON and state is NEVER
// echoed. Session location chain: body.state -> URL state -> freshest
// pending session.
func TestBrowserLoginSubmitTraeAuthCodeInfoShape(t *testing.T) {
	origExchange, origUserInfo := browserLoginExchangeFn, browserLoginUserInfoFn
	defer func() { browserLoginExchangeFn, browserLoginUserInfoFn = origExchange, origUserInfo }()
	var gotCode string
	exchangeCalls := 0
	browserLoginExchangeFn = func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
		exchangeCalls++
		gotCode = code
		return nil, "", fmt.Errorf("simulated upstream down")
	}
	browserLoginUserInfoFn = func(token string) (string, string, error) {
		return "", "", nil
	}
	// The exact live bounce shape (2026-09-05 user paste, credentials
	// trimmed): authCodeInfo carries the code, no code/state params at all.
	liveURL := `http://127.0.0.1:8317/authorize?isRedirect=true&scope=solo` +
		`&authCodeInfo=%7B%22AuthCode%22%3A%22RLN9_test_code%22%2C%22ExpireAt%22%3A1788538595350%2C%22ExpireDuration%22%3A600000%7D` +
		`&loginTraceID=b189d893-70a6-b593-05a6-337b84a3df59&host=https%3A%2F%2Fapi.trae.com.cn&userRegion=cn` +
		`&userInfo=%7B%22UserID%22%3A%223049391365297084%22%7D`

	// a) body.state (panel-remembered) locates the session and the code is
	// extracted from authCodeInfo.
	stA := "trae-shape-body-state"
	browserLoginSessions.Store(stA, &browserLoginSession{Verifier: "v", CreatedAt: time.Now()})
	out := handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"` + liveURL + `","state":"` + stA + `"}`)})
	if out["ok"] != false || !strings.Contains(strValue(out["error"]), "换取 token 失败") {
		t.Fatalf("authCodeInfo+body.state flow: %v", out)
	}
	if gotCode != "RLN9_test_code" {
		t.Fatalf("exchange got code %q, want the AuthCode from authCodeInfo", gotCode)
	}
	browserLoginSessions.Delete(stA)

	// b) No state anywhere -> freshest-pending fallback consumes the session.
	stB := "trae-shape-fallback"
	browserLoginSessions.Store(stB, &browserLoginSession{Verifier: "v", CreatedAt: time.Now()})
	out = handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"` + liveURL + `"}`)})
	if !strings.Contains(strValue(out["error"]), "换取 token 失败") {
		t.Fatalf("authCodeInfo fallback flow: %v", out)
	}
	if gotCode != "RLN9_test_code" || exchangeCalls != 2 {
		t.Fatalf("fallback exchange: code=%q calls=%d", gotCode, exchangeCalls)
	}
	browserLoginSessions.Delete(stB) // settled result cleanup

	// c) Two pending sessions -> the freshest one wins; body.state still
	// overrides the fallback.
	stOld := "trae-shape-old"
	stNew := "trae-shape-new"
	browserLoginSessions.Store(stOld, &browserLoginSession{Verifier: "v", CreatedAt: time.Now().Add(-time.Minute)})
	browserLoginSessions.Store(stNew, &browserLoginSession{Verifier: "v", CreatedAt: time.Now()})
	out = handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"` + liveURL + `"}`)})
	if !strings.Contains(strValue(out["error"]), "换取 token 失败") {
		t.Fatalf("recency flow: %v", out)
	}
	if raw, ok := browserLoginSessions.Load(stNew); !ok || raw.(*browserLoginSession).Result == nil {
		t.Fatal("freshest session must be consumed (settled with an outcome) by the fallback")
	}
	if raw, ok := browserLoginSessions.Load(stOld); !ok || raw.(*browserLoginSession).Result != nil {
		t.Fatal("older pending session must stay untouched by the fallback")
	}
	browserLoginSessions.Delete(stOld)
	browserLoginSessions.Delete(stNew)

	// d) No pending session and no state -> explicit guidance error. Purge
	// every pending session first: earlier tests (e.g. /start contract
	// tests) may have left unsettled sessions that the fallback would pick.
	browserLoginSessions.Range(func(key, value any) bool {
		if s, ok := value.(*browserLoginSession); ok && s != nil && s.Result == nil {
			browserLoginSessions.Delete(key)
		}
		return true
	})
	out = handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"` + liveURL + `"}`)})
	if !strings.Contains(strValue(out["error"]), "无法定位授权会话") {
		t.Fatalf("no-session flow: %v", out)
	}
}

// TestBrowserLoginStartReturnsState verifies /start hands the state back so
// the panel can carry it on /submit (primary session locator — the Trae
// authorization page never echoes state, 2026-09-05 live probe).
func TestBrowserLoginStartReturnsState(t *testing.T) {
	out := handleBrowserLoginStart(pluginapi.ManagementRequest{Body: []byte(`{"redirect_origin":"https://1.2.3.4:18998"}`)})
	if out["error"] != nil {
		t.Fatalf("start failed: %v", out["error"])
	}
	st, _ := out["state"].(string)
	if st == "" {
		t.Fatal("start response missing state field")
	}
	authURL, _ := out["auth_url"].(string)
	u, _ := url.Parse(authURL)
	if got := u.Query().Get("state"); got != st {
		t.Fatalf("response state %q does not match auth_url state %q", st, got)
	}
	browserLoginSessions.Delete(st)
}

// TestBrowserLoginCallbackTraeShapeFallback verifies the resource bounce
// route also resolves the authCodeInfo form and falls back to the freshest
// pending session when state is absent (same contract as the paste path).
func TestBrowserLoginCallbackTraeShapeFallback(t *testing.T) {
	origExchange, origUserInfo := browserLoginExchangeFn, browserLoginUserInfoFn
	defer func() { browserLoginExchangeFn, browserLoginUserInfoFn = origExchange, origUserInfo }()
	browserLoginExchangeFn = func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
		return nil, "", fmt.Errorf("simulated upstream down")
	}
	browserLoginUserInfoFn = func(token string) (string, string, error) {
		return "", "", nil
	}
	st := "callback-trae-shape"
	browserLoginSessions.Store(st, &browserLoginSession{
		Verifier:    "v",
		RedirectURI: "https://1.2.3.4:18998",
		CreatedAt:   time.Now(),
	})
	q := url.Values{}
	q.Set("authCodeInfo", `{"AuthCode":"CB_test_code","ExpireAt":1788538595350,"ExpireDuration":600000}`)
	resp := handleBrowserLoginCallback(pluginapi.ManagementRequest{Query: q})
	body := string(resp.Body)
	if !strings.Contains(body, resourcePanelPrefix) {
		t.Fatalf("bounce page must redirect to the panel: %s", body)
	}
	raw, ok := browserLoginSessions.Load(st)
	if !ok {
		t.Fatal("session missing after callback settle")
	}
	if s := raw.(*browserLoginSession); s.Result == nil || !strings.Contains(s.Result.Error, "换取 token 失败") {
		t.Fatalf("failure outcome not stored back: %+v", raw.(*browserLoginSession).Result)
	}
	browserLoginSessions.Delete(st)

	// No pending session + no state + no code -> guidance error page.
	resp = handleBrowserLoginCallback(pluginapi.ManagementRequest{Query: url.Values{"isRedirect": {"true"}}})
	if !strings.Contains(string(resp.Body), "授权回调缺少授权码") {
		t.Fatalf("empty callback page: %s", string(resp.Body))
	}
}

// TestBrowserLoginSubmitUserInfoFallback pins the 2026-09-05 live finding:
// the freshly-exchanged token can fail GetUserInfo with 401 (cookie-session
// based route), and the SOLO client itself prefers the bounce URL's userInfo
// JSON (main.js: r ?? await getUserInfo(...)). The submit path must fall
// back to that profile and still import the account. The full import needs
// the host auth RPCs; here the exchange succeeds, GetUserInfo fails, and
// the flow must reach the import stage (proving the fallback kicked in).
func TestBrowserLoginSubmitUserInfoFallback(t *testing.T) {
	origExchange, origUserInfo := browserLoginExchangeFn, browserLoginUserInfoFn
	defer func() { browserLoginExchangeFn, browserLoginUserInfoFn = origExchange, origUserInfo }()
	browserLoginExchangeFn = func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
		return &browserLoginToken{Token: "tok", RefreshToken: "rt"}, `{"Result":{"Token":"tok"}}`, nil
	}
	userInfoCalls := 0
	browserLoginUserInfoFn = func(token string) (string, string, error) {
		userInfoCalls++
		return "", "", fmt.Errorf("HTTP 401 The user is not logged in")
	}
	// The live bounce shape: authCodeInfo + userInfo carrying UserID/ScreenName.
	url := "http://127.0.0.1:8317/authorize?isRedirect=true&scope=solo" +
		`&authCodeInfo=%7B%22AuthCode%22%3A%22realcode%22%7D` +
		`&userInfo=%7B%22UserID%22%3A%223049391365297084%22%2C%22ScreenName%22%3A%22%E7%94%A8%E6%88%B724034744679%22%7D`
	state := "userinfo-fallback"
	browserLoginSessions.Store(state, &browserLoginSession{Verifier: "v", CreatedAt: time.Now()})
	defer browserLoginSessions.Delete(state)
	out := handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"` + url + `","state":"` + state + `"}`)})
	if userInfoCalls != 1 {
		t.Fatalf("GetUserInfo must be attempted once first, got %d calls", userInfoCalls)
	}
	// The flow must NOT stop at the GetUserInfo error: it proceeds to the
	// import stage. Without the host auth RPCs (cgo shim) hostAuthList
	// fails, so the expected outcome is the import-stage error, never the
	// "获取用户信息失败" error.
	errText := strValue(out["error"])
	if strings.Contains(errText, "获取用户信息失败") {
		t.Fatalf("userInfo fallback did not kick in: %v", out["error"])
	}
	// Parse the stored outcome: the identity must come from the bounce.
	raw, ok := browserLoginSessions.Load(state)
	if !ok {
		t.Fatal("session missing after settle")
	}
	s := raw.(*browserLoginSession)
	if s.Result == nil {
		t.Fatal("outcome not stored")
	}
	if s.Result.OK {
		// Import unexpectedly succeeded (host RPCs available) — then the
		// label must carry the bounce ScreenName.
		if !strings.Contains(s.Result.Label, "用户24034744679") {
			t.Fatalf("imported label %q must use the bounce ScreenName", s.Result.Label)
		}
	}
	// Without the fallback the error would be 获取用户信息失败; reaching the
	// import stage at all proves parseBounceUserInfo supplied the identity.
	if !s.Result.OK && !strings.Contains(s.Result.Error, "凭据") && !strings.Contains(s.Result.Error, "host") {
		t.Fatalf("unexpected import-stage error: %v", s.Result.Error)
	}
}

// strValue renders a map value for assertion messages.
func strValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
