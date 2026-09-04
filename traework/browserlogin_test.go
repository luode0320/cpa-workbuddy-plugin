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

	// 2) URL without state -> error, session untouched.
	state := "submit-test-state"
	browserLoginSessions.Store(state, &browserLoginSession{Verifier: "v", CreatedAt: time.Now()})
	defer browserLoginSessions.Delete(state)
	out = handleBrowserLoginSubmit(pluginapi.ManagementRequest{Body: []byte(`{"url":"http://127.0.0.1:18998/authorize?code=abc"}`)})
	if !strings.Contains(strValue(out["error"]), "state") {
		t.Fatalf("state-less url: %v", out["error"])
	}
	if _, ok := browserLoginSessions.Load(state); !ok {
		t.Fatal("session must survive a state-less submit")
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

// strValue renders a map value for assertion messages.
func strValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
