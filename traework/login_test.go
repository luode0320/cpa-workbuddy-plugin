package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// hostLoginTestEnv swaps the upstream exchange/userinfo seams and returns a
// restore func. Both fakes are non-fatal by default.
func hostLoginTestEnv(t *testing.T, exchange func(*browserLoginSession, string) (*browserLoginToken, string, error), userInfo func(string) (string, string, error)) {
	t.Helper()
	origExchange, origUserInfo := browserLoginExchangeFn, browserLoginUserInfoFn
	origSave := hostAuthSaveJSONFn
	browserLoginExchangeFn = exchange
	browserLoginUserInfoFn = userInfo
	// The cgo-shim sandbox has no host auth RPCs (hostCall fails), so the
	// hostPersists flow's plugin-side save would error. Stub it to succeed so
	// the happy-path tests reach the "登录成功" outcome; a dedicated test
	// (TestHostLoginBridgeWritesAuthFile) asserts the file-write contract by
	// swapping this seam to capture the call.
	hostAuthSaveJSONFn = func(name string, raw []byte) error { return nil }
	t.Cleanup(func() {
		browserLoginExchangeFn, browserLoginUserInfoFn = origExchange, origUserInfo
		hostAuthSaveJSONFn = origSave
	})
}

// decodeLoginStart decodes an auth.login.start ok envelope.
func decodeLoginStart(t *testing.T, raw []byte, err error) pluginapi.AuthLoginStartResponse {
	t.Helper()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("start envelope: %v ok=%v", err, env.OK)
	}
	var resp pluginapi.AuthLoginStartResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("start result: %v", err)
	}
	return resp
}

// decodeLoginPoll decodes an auth.login.poll ok envelope.
func decodeLoginPoll(t *testing.T, raw []byte, err error) pluginapi.AuthLoginPollResponse {
	t.Helper()
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("poll envelope: %v ok=%v", err, env.OK)
	}
	var resp pluginapi.AuthLoginPollResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("poll result: %v", err)
	}
	return resp
}

// TestHostLoginStartReturnsBridgeURLAndValidState verifies the StartLogin
// contract for the host OAuth entry: the BRIDGE PAGE URL (relative path —
// the host UI opens it from the management origin so localhost and remote
// deployments both resolve) plus a state that survives the host's charset
// validation ([a-z0-9-._,] ≤ 128). The real Trae authorization URL lives on
// the session: the bridge page renders it as the login link.
func TestHostLoginStartReturnsBridgeURLAndValidState(t *testing.T) {
	raw, err := handleStartLogin([]byte(`{}`))
	resp := decodeLoginStart(t, raw, err)
	// The URL must be the relative bridge page, NOT the Trae authorization
	// page: after login the browser lands on the dead loopback page and the
	// host card's paste input cannot digest the Trae callback shape (no
	// ?code=&state= → 400), so the bridge page must own the finish flow.
	if !strings.HasPrefix(resp.URL, "/v0/resource/plugins/"+providerName+"/browser-login/bridge?state=") {
		t.Fatalf("url must be the relative bridge page, got %q", resp.URL)
	}
	if strings.HasPrefix(resp.URL, "data:") {
		t.Fatal("url must not be a data: guide page anymore")
	}
	if strings.HasPrefix(resp.URL, "http") {
		t.Fatalf("url must stay relative so the host origin resolves it, got %q", resp.URL)
	}
	if resp.State == "" || len(resp.State) > 128 {
		t.Fatalf("state %q invalid", resp.State)
	}
	for _, r := range resp.State {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_' || r == ',':
		default:
			t.Fatalf("state %q carries illegal rune %q", resp.State, r)
		}
	}
	if !strings.HasPrefix(resp.State, hostLoginStatePrefix) {
		t.Fatalf("state %q must carry the host-flow prefix", resp.State)
	}
	// The session must exist under the returned state with the REAL Trae
	// authorization URL (the bridge page renders it as the login link).
	sRaw, ok := browserLoginSessions.Load(resp.State)
	if !ok {
		t.Fatal("session missing under returned state")
	}
	s := sRaw.(*browserLoginSession)
	if !strings.HasPrefix(s.AuthURL, browserLoginHost+"/authorization?") {
		t.Fatalf("session AuthURL %q must be the Trae authorization page", s.AuthURL)
	}
	if s.Verifier == "" || s.DevicePem == "" {
		t.Fatal("session must carry PKCE verifier + device key")
	}
	// The bridge URL must locate the same session via its state param.
	bu, err := url.Parse(resp.URL)
	if err != nil {
		t.Fatalf("parse bridge url: %v", err)
	}
	if bu.Query().Get("state") != resp.State {
		t.Fatalf("bridge url state %q != %q", bu.Query().Get("state"), resp.State)
	}
	// The authorization URL carries the state and the loopback /authorize
	// callback shape required by the Trae whitelist.
	u, err := url.Parse(s.AuthURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	q := u.Query()
	if q.Get("state") != resp.State {
		t.Fatalf("auth url state %q != %q", q.Get("state"), resp.State)
	}
	if cb := q.Get("auth_callback_url"); !strings.HasPrefix(cb, "http://127.0.0.1:") || !strings.HasSuffix(cb, "/authorize") {
		t.Fatalf("auth_callback_url %q must stay loopback /authorize", cb)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatal("auth url must carry S256 challenge")
	}
	// Metadata surfaces the bridge URL for the host UI prompt.
	if resp.Metadata == nil || !strings.Contains(fmt.Sprint(resp.Metadata["bridge_url"]), "/browser-login/bridge?state=") {
		t.Fatalf("metadata must carry bridge_url: %v", resp.Metadata)
	}
	defer browserLoginSessions.Delete(resp.State)
}

// TestHostLoginPollPendingWhileUnsettled verifies the poll state machine
// reports pending until the bridge page settles the session.
func TestHostLoginPollPendingWhileUnsettled(t *testing.T) {
	raw, err := handleStartLogin([]byte(`{}`))
	resp := decodeLoginStart(t, raw, err)
	defer browserLoginSessions.Delete(resp.State)

	pollRaw, pollErr := handlePollLogin(pollBody(t, pluginapi.AuthLoginPollRequest{State: resp.State}))
	poll := decodeLoginPoll(t, pollRaw, pollErr)
	if poll.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("fresh session must poll pending, got %q", poll.Status)
	}
}

// TestHostLoginPollUnknownStateErrors verifies unknown/expired states
// surface as errors (the host marks the OAuth session failed).
func TestHostLoginPollUnknownStateErrors(t *testing.T) {
	if _, err := handlePollLogin(pollBody(t, pluginapi.AuthLoginPollRequest{State: ""})); err == nil {
		t.Fatal("empty state must error")
	}
	if _, err := handlePollLogin(pollBody(t, pluginapi.AuthLoginPollRequest{State: "trw-ghost"})); err == nil {
		t.Fatal("unknown state must error")
	}
}

// TestHostLoginPollExpiredSessionErrors verifies TTL expiry is enforced.
func TestHostLoginPollExpiredSessionErrors(t *testing.T) {
	state := "trw-expired"
	browserLoginSessions.Store(state, &browserLoginSession{Verifier: "v", CreatedAt: time.Now().Add(-11 * time.Minute)})
	defer browserLoginSessions.Delete(state)
	if _, err := handlePollLogin(pollBody(t, pluginapi.AuthLoginPollRequest{State: state})); err == nil {
		t.Fatal("expired session must error")
	}
}

// TestHostLoginBridgeSettlesAndPollReportsSuccess drives the full host-flow
// happy path: bridge page settles with ?code=, then PollLogin reports
// success with the AuthData record (ID empty so the host derives it from the
// file path) and consumes the session read-once.
func TestHostLoginBridgeSettlesAndPollReportsSuccess(t *testing.T) {
	hostLoginTestEnv(t,
		func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
			if code != "auth-code-1" {
				t.Fatalf("exchange got code %q", code)
			}
			return &browserLoginToken{Token: "tok-1", RefreshToken: "rtok-1", ExpiredAt: "2030-01-01T00:00:00Z"}, `{"raw":"exchange"}`, nil
		},
		func(token string) (string, string, error) {
			if token != "tok-1" {
				t.Fatalf("userinfo got token %q", token)
			}
			return "uid-777", "用户七七七", nil
		},
	)
	raw, err := handleStartLogin([]byte(`{}`))
	start := decodeLoginStart(t, raw, err)
	state := start.State
	defer browserLoginSessions.Delete(state)

	// 1) Bridge guide page renders the login link + paste form.
	guide := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {state}},
	})
	if guide.StatusCode != 200 {
		t.Fatalf("guide status %d", guide.StatusCode)
	}
	guideHTML := string(guide.Body)
	if !strings.Contains(guideHTML, "打开 Trae 登录页面") || !strings.Contains(guideHTML, "粘贴回调地址") {
		t.Fatal("guide page must render the login steps + paste form")
	}
	if !strings.Contains(guideHTML, url.PathEscape(providerName)) && !strings.Contains(guideHTML, providerName) {
		t.Fatal("guide page must reference the bridge path")
	}
	// No markdown backticks in the injected JS (they truncate Go raw strings).
	if strings.Contains(guideHTML, "`") {
		t.Fatal("bridge page HTML must not contain backticks")
	}

	// 2) Bridge code branch settles the session (hostPersists=true).
	done := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {state}, "code": {"auth-code-1"}},
	})
	if done.StatusCode != 200 {
		t.Fatalf("done status %d", done.StatusCode)
	}
	if !strings.Contains(string(done.Body), "登录成功") || !strings.Contains(string(done.Body), "用户七七七") {
		t.Fatalf("done page must show the imported label: %s", truncateRedacted(string(done.Body), 200))
	}

	// 3) PollLogin reports success with the AuthData record.
	pollRaw, pollErr := handlePollLogin(pollBody(t, pluginapi.AuthLoginPollRequest{State: state}))
	poll := decodeLoginPoll(t, pollRaw, pollErr)
	if poll.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("poll status %q msg %q", poll.Status, poll.Message)
	}
	if poll.Auth.Provider != providerName {
		t.Fatalf("auth provider %q", poll.Auth.Provider)
	}
	if poll.Auth.ID != "" {
		t.Fatalf("AuthData.ID must be empty (host derives from path), got %q", poll.Auth.ID)
	}
	if !strings.HasPrefix(poll.Auth.FileName, "traework-") || !strings.Contains(poll.Auth.FileName, "777") {
		t.Fatalf("AuthData.FileName %q must follow traework-<uid>.json", poll.Auth.FileName)
	}
	if poll.Auth.Label != "用户七七七" {
		t.Fatalf("auth label %q", poll.Auth.Label)
	}
	var stored map[string]any
	if err := json.Unmarshal(poll.Auth.StorageJSON, &stored); err != nil {
		t.Fatalf("storage json: %v", err)
	}

	// 4) Read-once: the next poll errors out (session consumed).
	if _, err := handlePollLogin(pollBody(t, pluginapi.AuthLoginPollRequest{State: state})); err == nil {
		t.Fatal("settled session must be consumed read-once")
	}
}

// TestHostLoginBridgeErrorOutcomePropagatesToPoll verifies a failed settle
// (exchange error) surfaces through PollLogin as an error status.
func TestHostLoginBridgeErrorOutcomePropagatesToPoll(t *testing.T) {
	hostLoginTestEnv(t,
		func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
			return nil, "", fmt.Errorf("upstream 500")
		},
		func(token string) (string, string, error) { return "", "", nil },
	)
	raw, err := handleStartLogin([]byte(`{}`))
	start := decodeLoginStart(t, raw, err)
	state := start.State
	defer browserLoginSessions.Delete(state)

	done := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {state}, "code": {"bad"}},
	})
	if !strings.Contains(string(done.Body), "登录失败") {
		t.Fatal("done page must show the failure")
	}
	pollRaw, pollErr := handlePollLogin(pollBody(t, pluginapi.AuthLoginPollRequest{State: state}))
	poll := decodeLoginPoll(t, pollRaw, pollErr)
	if poll.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("poll status %q, want error", poll.Status)
	}
	if !strings.Contains(poll.Message, "换取 token 失败") {
		t.Fatalf("poll message %q must carry the exchange failure", poll.Message)
	}
}

// TestHostLoginBridgeGuards verifies bridge-page input guards: missing state,
// unknown state, and double-submit of an already-settled session.
func TestHostLoginBridgeGuards(t *testing.T) {
	// Missing state.
	noState := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{},
	})
	if noState.StatusCode != 200 || !strings.Contains(string(noState.Body), "缺少 state") {
		t.Fatalf("missing state must render the error page")
	}
	// Unknown state.
	ghost := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {"trw-ghost"}},
	})
	if !strings.Contains(string(ghost.Body), "不存在或已完成") {
		t.Fatal("unknown state must render the error page")
	}
	// Double submit: settle once, then re-visit with the same code — the
	// stored outcome is shown without a second exchange.
	hostLoginTestEnv(t,
		func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
			return &browserLoginToken{Token: "tok"}, "{}", nil
		},
		func(token string) (string, string, error) { return "uid-1", "n", nil },
	)
	raw, err := handleStartLogin([]byte(`{}`))
	start := decodeLoginStart(t, raw, err)
	state := start.State
	defer browserLoginSessions.Delete(state)
	first := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {state}, "code": {"c1"}},
	})
	if !strings.Contains(string(first.Body), "登录成功") {
		t.Fatal("first submit must succeed")
	}
	second := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {state}, "code": {"c1"}},
	})
	if !strings.Contains(string(second.Body), "登录成功") {
		t.Fatal("double submit must replay the stored outcome")
	}
}

// TestHostLoginGuidePageSubmitsWholeURL verifies the injected client-side
// script submits the WHOLE pasted URL to the bridge as the url param — the
// server parses code/authCodeInfo/userInfo (same extractAuthCode path as the
// panel submit). No client-side parser may remain: the 0.1.45 JS-side
// extractCode dropped the userInfo parameter and disabled the
// GetUserInfo-401 fallback (field report "获取用户信息失败：HTTP 401").
func TestHostLoginGuidePageSubmitsWholeURL(t *testing.T) {
	raw, err := handleStartLogin([]byte(`{}`))
	start := decodeLoginStart(t, raw, err)
	defer browserLoginSessions.Delete(start.State)
	page := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {start.State}},
	})
	html := string(page.Body)
	// The submit must carry the whole URL, URL-encoded.
	if !strings.Contains(html, "'&url='+encodeURIComponent(") {
		t.Fatal("guide JS must submit the whole pasted URL as the url param")
	}
	// It must navigate to the bridge path with the state preserved.
	if !strings.Contains(html, "/browser-login/bridge?state=") {
		t.Fatal("guide JS must reload the bridge page with the state")
	}
	// No leftover client-side code parser (the server owns parsing now).
	if strings.Contains(html, "extractCode") {
		t.Fatal("guide JS must not carry a client-side code parser")
	}
}

// TestHostLoginBridgeURLParamUserInfoFallback is the 0.1.46 regression test:
// the paste box submits the whole callback URL via the url param; the server
// must parse BOTH the authorization code (authCodeInfo JSON) and the bounce
// userInfo JSON so the GetUserInfo-401 fallback still identifies the account.
func TestHostLoginBridgeURLParamUserInfoFallback(t *testing.T) {
	hostLoginTestEnv(t,
		func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
			if code != "AuthCode-live-1" {
				t.Fatalf("exchange got code %q, want the authCodeInfo payload", code)
			}
			return &browserLoginToken{Token: "tok-fb", RefreshToken: "rtok-fb", ExpiredAt: "2030-01-01T00:00:00Z"}, `{"raw":"exchange"}`, nil
		},
		func(token string) (string, string, error) {
			// Live shape 2026-09-05: the freshly-exchanged token is rejected
			// by this cookie-session route — the fallback MUST kick in.
			return "", "", fmt.Errorf("HTTP 401 {\"Code\":\"20310\",\"Message\":\"The user is not logged in\"}")
		},
	)
	raw, err := handleStartLogin([]byte(`{}`))
	start := decodeLoginStart(t, raw, err)
	state := start.State
	defer browserLoginSessions.Delete(state)

	// The live bounce shape: authCodeInfo + userInfo JSON params, no ?code=,
	// no state echo.
	pasted := "http://127.0.0.1:8317/authorize?isRedirect=true&scope=solo" +
		`&authCodeInfo=%7B%22AuthCode%22%3A%22AuthCode-live-1%22%2C%22ExpireAt%22%3A1790000000000%7D` +
		`&loginTraceID=8f6b6e2a-0000-4000-8000-deadbeef0000&host=https%3A%2F%2Fapi.trae.com.cn&userRegion=cn` +
		`&userInfo=%7B%22UserID%22%3A%223049391365297084%22%2C%22ScreenName%22%3A%22%E7%94%A8%E6%88%B724034744679%22%7D`
	done := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {state}, "url": {pasted}},
	})
	if !strings.Contains(string(done.Body), "登录成功") || !strings.Contains(string(done.Body), "用户24034744679") {
		t.Fatalf("url-param settle must fall back to the bounce userInfo: %s", truncateRedacted(string(done.Body), 200))
	}
	pollRaw, pollErr := handlePollLogin(pollBody(t, pluginapi.AuthLoginPollRequest{State: state}))
	poll := decodeLoginPoll(t, pollRaw, pollErr)
	if poll.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("poll status %q msg %q", poll.Status, poll.Message)
	}
	if poll.Auth.Label != "用户24034744679" || poll.Auth.ID != "" {
		t.Fatalf("auth label %q id %q — fallback identity must ride AuthData with empty ID", poll.Auth.Label, poll.Auth.ID)
	}
}

// TestHostLoginBridgeURLParamNoCodeErrors verifies a pasted URL without any
// code/authCodeInfo renders the parse error page instead of silently
// re-showing the guide (the user would think the submit was ignored).
func TestHostLoginBridgeURLParamNoCodeErrors(t *testing.T) {
	raw, err := handleStartLogin([]byte(`{}`))
	start := decodeLoginStart(t, raw, err)
	defer browserLoginSessions.Delete(start.State)
	done := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {start.State}, "url": {"https://www.trae.cn/authorization?foo=1"}},
	})
	if !strings.Contains(string(done.Body), "未能从粘贴的地址中解析出授权码") {
		t.Fatalf("pasted-no-code must render the parse error: %s", truncateRedacted(string(done.Body), 200))
	}
}

// TestHostLoginBridgeWritesAuthFile is the 0.1.48 regression test: the host
// OAuth flow must persist the credential file PLUGIN-SIDE (dedup-by-uid +
// hostAuthSaveJSON), because on the production server the host's
// savePluginLoginRecords does not write the file (qoderwork runs the same
// contract yet yields zero qoderwork-*.json — field report 2026-09-05). This
// overrides the save seam to capture the filename+payload and asserts the
// write actually happens with the correct traework-<uid>.json name.
func TestHostLoginBridgeWritesAuthFile(t *testing.T) {
	hostLoginTestEnv(t,
		func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
			return &browserLoginToken{Token: "tok-write", RefreshToken: "rtok-write", ExpiredAt: "2030-01-01T00:00:00Z"}, `{"raw":"write"}`, nil
		},
		func(token string) (string, string, error) {
			// Live shape: freshly-exchanged token is rejected by this
			// cookie-session route — the bounce userInfo fallback must run.
			return "", "", fmt.Errorf("HTTP 401 {\"Code\":\"20310\",\"Message\":\"The user is not logged in\"}")
		},
	)
	var savedName string
	var savedRaw []byte
	origSave := hostAuthSaveJSONFn
	hostAuthSaveJSONFn = func(name string, raw []byte) error {
		savedName, savedRaw = name, raw
		return nil
	}
	t.Cleanup(func() { hostAuthSaveJSONFn = origSave })

	raw, err := handleStartLogin([]byte(`{}`))
	start := decodeLoginStart(t, raw, err)
	state := start.State
	defer browserLoginSessions.Delete(state)

	pasted := "http://127.0.0.1:8317/authorize?isRedirect=true&scope=solo" +
		`&authCodeInfo=%7B%22AuthCode%22%3A%22AuthCode-write-1%22%2C%22ExpireAt%22%3A1790000000000%7D` +
		`&userRegion=cn&userInfo=%7B%22UserID%22%3A%22438080225149472%22%2C%22ScreenName%22%3A%22%E7%94%A8%E6%88%B753849661984%22%7D`
	done := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {state}, "url": {pasted}},
	})
	if !strings.Contains(string(done.Body), "登录成功") || !strings.Contains(string(done.Body), "用户53849661984") {
		t.Fatalf("host flow must reach success: %s", truncateRedacted(string(done.Body), 200))
	}
	if savedName == "" {
		t.Fatal("hostPersists flow must call hostAuthSaveJSON (plugin-side fallback write)")
	}
	if !strings.HasPrefix(savedName, "traework-") || !strings.Contains(savedName, "438080225149472") {
		t.Fatalf("saved file name %q must follow traework-<uid>.json", savedName)
	}
	var stored map[string]any
	if err := json.Unmarshal(savedRaw, &stored); err != nil {
		t.Fatalf("saved payload must be valid JSON: %v", err)
	}
}

func TestHostLoginPanelFlowUnchanged(t *testing.T) {
	hostLoginTestEnv(t,
		func(session *browserLoginSession, code string) (*browserLoginToken, string, error) {
			return &browserLoginToken{Token: "t"}, "{}", nil
		},
		func(token string) (string, string, error) { return "uid-9", "n9", nil },
	)
	s := &browserLoginSession{Verifier: "v", DeviceID: "d", CreatedAt: time.Now()}
	outcome := settleBrowserLogin("trw-panel-state", s, "c", "", "", false)
	defer browserLoginSessions.Delete("trw-panel-state")
	if outcome.HostAuth != nil {
		t.Fatal("panel flow must NOT carry HostAuth (plugin persists itself)")
	}
	// In the cgo-shim sandbox host.auth.save is unavailable, so the expected
	// outcome is the import-stage error; with the host RPC live it succeeds.
	if !outcome.OK {
		if !strings.Contains(outcome.Error, "凭据") && !strings.Contains(outcome.Error, "host") {
			t.Fatalf("unexpected import-stage error: %v", outcome.Error)
		}
	}
}

// pollBody marshals a poll request or fails the test.
func pollBody(t *testing.T, req pluginapi.AuthLoginPollRequest) []byte {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
