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
	browserLoginExchangeFn = exchange
	browserLoginUserInfoFn = userInfo
	t.Cleanup(func() { browserLoginExchangeFn, browserLoginUserInfoFn = origExchange, origUserInfo })
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

// TestHostLoginGuidePageParsesAuthCodeInfoJS verifies the injected client-side
// parser handles the REAL Trae bounce shape (authCodeInfo JSON with the code)
// and a bare ?code= shape — mirrors the page contract the user pastes into.
func TestHostLoginGuidePageParsesAuthCodeInfoJS(t *testing.T) {
	raw, err := handleStartLogin([]byte(`{}`))
	start := decodeLoginStart(t, raw, err)
	defer browserLoginSessions.Delete(start.State)
	page := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {start.State}},
	})
	html := string(page.Body)
	// The parser must reference both accepted query keys.
	if !strings.Contains(html, "authCodeInfo") || !strings.Contains(html, "'code'") {
		t.Fatal("guide JS must parse authCodeInfo and plain code params")
	}
	// It must navigate to the bridge path with the state preserved.
	if !strings.Contains(html, "/browser-login/bridge?state=") {
		t.Fatal("guide JS must reload the bridge page with the state")
	}
}

// TestHostLoginPanelFlowUnchanged verifies the panel (non-host) flow still
// persists credentials itself: settleBrowserLogin(hostPersists=false) stores
// NO HostAuth on the outcome (the plugin writes the file via host.auth.save).
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
