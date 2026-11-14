package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// resetPublicOrigin 清空已记录的公网来源，避免用例之间互相污染。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，公网来源用例隔离。
func resetPublicOrigin() {
	publicOriginMu.Lock()
	defer publicOriginMu.Unlock()
	publicOrigin = ""
}

// tokenBeatFixtureServer 起一个假的令牌端点，返回指定响应与 Set-Cookie。
// [参数] t: 测试上下文；body: 响应体；cookies: 下发的 Cookie。
// [返回] 端点 URL。
// 最近修改时间：2026-11-14；改动原因：新增，兑换链路无外网依赖。
func tokenBeatFixtureServer(t *testing.T, body string, cookies []*http.Cookie) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, c := range cookies {
			http.SetCookie(w, c)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestExchangeTokenForCookiesRejectsEmptyToken 校验空令牌被直接挡下。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，兑换入口边界。
func TestExchangeTokenForCookiesRejectsEmptyToken(t *testing.T) {
	if _, errExchange := exchangeTokenForCookies("   "); errExchange == nil {
		t.Fatal("空令牌必须报错")
	} else if !strings.Contains(errExchange.Error(), "令牌") {
		t.Fatalf("错误说明应提到令牌，实际 %q", errExchange.Error())
	}
}

// TestExchangeTokenForCookiesReturnsCookieHeader 校验令牌换成 Cookie 头。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，兑换主链路。
func TestExchangeTokenForCookiesReturnsCookieHeader(t *testing.T) {
	srv := tokenBeatFixtureServer(t, `{"message":"success","data":{}}`, []*http.Cookie{
		{Name: "sessionid", Value: "sess_value", Path: "/"},
		{Name: "ttwid", Value: "ttwid_value", Path: "/"},
	})
	restore := swapTokenBeatEndpoint(srv.URL)
	defer restore()

	exchanged, errExchange := exchangeTokenForCookies("token-ok")
	if errExchange != nil {
		t.Fatalf("兑换应成功：%v", errExchange)
	}
	if exchanged.SetCookieCount != 2 {
		t.Fatalf("应下发 2 条 Cookie，实际 %d", exchanged.SetCookieCount)
	}
	if !strings.Contains(exchanged.CookieHeader, "sessionid=sess_value") {
		t.Fatalf("Cookie 头应含 sessionid，实际 %q", exchanged.CookieHeader)
	}
	if len(exchanged.Cookies) == 0 {
		t.Fatal("必须带回 Cookie 快照，供账号落盘续用")
	}
}

// TestExchangeTokenForCookiesRejectsBusinessError 校验业务失败被识别。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，避免把 200 当成功。
func TestExchangeTokenForCookiesRejectsBusinessError(t *testing.T) {
	srv := tokenBeatFixtureServer(t, `{"message":"error","data":{"error_code":401,"error_name":"session_expired","description":"会话过期，请重新登录"}}`, nil)
	restore := swapTokenBeatEndpoint(srv.URL)
	defer restore()

	_, errExchange := exchangeTokenForCookies("token-expired")
	if errExchange == nil {
		t.Fatal("业务失败必须报错（该端点无论成败都返回 HTTP 200）")
	}
	if !strings.Contains(errExchange.Error(), "会话过期") {
		t.Fatalf("错误说明应带上游原文，实际 %q", errExchange.Error())
	}
}

// TestExchangeTokenForCookiesRejectsNoCookie 校验零 Set-Cookie 被识别。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，防止落盘空凭据。
func TestExchangeTokenForCookiesRejectsNoCookie(t *testing.T) {
	srv := tokenBeatFixtureServer(t, `{"message":"success","data":{}}`, nil)
	restore := swapTokenBeatEndpoint(srv.URL)
	defer restore()

	_, errExchange := exchangeTokenForCookies("token-ok")
	if errExchange == nil {
		t.Fatal("零 Cookie 必须报错")
	}
	if !strings.Contains(errExchange.Error(), "Cookie") {
		t.Fatalf("错误说明应提到 Cookie，实际 %q", errExchange.Error())
	}
}

// TestSaveFromTokenWritesAccount 校验令牌链路完整落盘。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，首次授权主链路。
func TestSaveFromTokenWritesAccount(t *testing.T) {
	dir := isolateAccounts(t)
	resetLoginSessions()

	beat := tokenBeatFixtureServer(t, `{"message":"success","data":{}}`, []*http.Cookie{
		{Name: "sessionid", Value: "sess_value", Path: "/"},
		{Name: "flow_cur_user_sec_id", Value: "sec_token_flow_0123456789", Path: "/"},
	})
	restoreBeat := swapTokenBeatEndpoint(beat.URL)
	defer restoreBeat()

	info := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"success","data":{"sec_user_id":"sec_token_for_test_0123456789","user_id":24681357,"screen_name":"令牌测试"}}`))
	}))
	defer info.Close()
	restoreInfo := swapAccountInfoEndpoint(info.URL + "/passport/account/info/v2/")
	defer restoreInfo()

	sa, errSave := saveFromToken("token-ok", "")
	if errSave != nil {
		t.Fatalf("令牌落盘应成功：%v", errSave)
	}
	if sa.AuthID == "" {
		t.Fatal("必须派生账号标识")
	}
	if sa.DeviceID == "" || sa.WebTabID == "" {
		t.Fatalf("设备标识必须固化，实际 device_id=%q web_tab_id=%q", sa.DeviceID, sa.WebTabID)
	}
	onDisk, errRead := readAuthFile(filepath.Join(dir, sa.AuthID+".json"))
	if errRead != nil {
		t.Fatalf("账号文件应已落盘：%v", errRead)
	}
	if onDisk.CookieHeader == "" || onDisk.DeviceID == "" {
		t.Fatalf("落盘账号应带 Cookie 与设备标识，实际 cookie=%q device_id=%q", onDisk.CookieHeader, onDisk.DeviceID)
	}
}

// TestBuildBookmarkletScriptKeepOriginPlaceholder 校验书签保留 origin 占位符。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，书签必须能在任意域名下复用。
func TestBuildBookmarkletScriptKeepOriginPlaceholder(t *testing.T) {
	script := buildBookmarkletScript()
	if !strings.HasPrefix(script, "javascript:") {
		t.Fatalf("书签必须以 javascript: 开头，实际 %q", truncateForTest(script))
	}
	if !strings.Contains(script, cpaOriginPlaceholder) {
		t.Fatal("书签必须保留 __CPA_ORIGIN__ 占位符，由页面脚本替换成实际域名")
	}
	if !strings.Contains(script, tokenBeatPath) {
		t.Fatalf("书签必须在豆包页面调用令牌端点 %q", tokenBeatPath)
	}
	if !strings.Contains(script, callbackPath) {
		t.Fatalf("书签必须跳回回调路径 %q", callbackPath)
	}
	if strings.Contains(script, "cookie") && !strings.Contains(script, "credentials") {
		t.Fatal("书签不应读取或上传 Cookie")
	}
}

// TestBuildAuthScriptCarriesState 校验一次性脚本带上 state 参数。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，state 只在一次性脚本里出现。
func TestBuildAuthScriptCarriesState(t *testing.T) {
	withState := buildAuthScript("state-abc")
	if !strings.Contains(withState, "state=state-abc") {
		t.Fatalf("带 state 时应拼进回调地址，实际 %q", truncateForTest(withState))
	}
	withoutState := buildAuthScript("")
	if strings.Contains(withoutState, "state=") {
		t.Fatal("无 state 时不应拼 query")
	}
}

// TestRememberPublicOriginPrefersForwardedHost 校验公网来源取值优先级。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，回调绝对地址依赖该值。
func TestRememberPublicOriginPrefersForwardedHost(t *testing.T) {
	resetPublicOrigin()
	t.Cleanup(resetPublicOrigin)

	h := http.Header{}
	h.Set("Host", "127.0.0.1:8317")
	rememberPublicOrigin(h)
	if got := currentPublicOrigin(); got != "https://127.0.0.1:8317" {
		t.Fatalf("回环请求也应记录，用于本机直连，实际 %q", got)
	}

	h2 := http.Header{}
	h2.Set("Host", "inner:8317")
	h2.Set("X-Forwarded-Host", "cpa.uode.vip")
	h2.Set("X-Forwarded-Proto", "https")
	rememberPublicOrigin(h2)
	if got := currentPublicOrigin(); got != "https://cpa.uode.vip" {
		t.Fatalf("应优先取 X-Forwarded-Host，实际 %q", got)
	}

	// 回环不得覆盖已记录的公网来源：容器内的健康检查会带本机 Host。
	rememberPublicOrigin(h)
	if got := currentPublicOrigin(); got != "https://cpa.uode.vip" {
		t.Fatalf("回环请求不得覆盖公网来源，实际 %q", got)
	}
}

// TestHandleLoginCallbackFirstOpenRendersShell 校验首次打开返回接收页。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，fragment 接收页契约。
func TestHandleLoginCallbackFirstOpenRendersShell(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	// 不带 state：固定书签/面板独立入口的首次打开，服务端拿不到 fragment。
	resp := handleLoginCallback(pluginapi.ManagementRequest{
		Method: http.MethodGet,
	})
	page := string(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("接收页应 200，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(page, "正在完成授权") {
		t.Fatalf("首次打开应渲染接收页，实际 %s", truncateText(page, 200))
	}
	if !strings.Contains(page, callbackTokenHeader) || !strings.Contains(page, "location.hash") {
		t.Fatal("接收页必须从 fragment 取令牌并经请求头回传")
	}
}

// TestHandleLoginCallbackRejectsExpiredState 校验失效 state 给出错误页。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，严格模式的会话校验。
func TestHandleLoginCallbackRejectsExpiredState(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	resp := handleLoginCallback(pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Query:   url.Values{"state": []string{"not-exist"}},
		Headers: http.Header{callbackTokenHeader: []string{"token-x"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("错误页也应 200（避免宿主判成鉴权失败），实际 %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "重新") {
		t.Fatalf("失效会话页应引导重新发起，实际 %s", truncateText(string(resp.Body), 200))
	}
}

// TestHandleLoginCallbackStrictSavesAndNotifies 校验严格模式落盘并交接。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，宿主弹窗自动关闭链路。
func TestHandleLoginCallbackStrictSavesAndNotifies(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()
	setupTokenFixtures(t)

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	resp := handleLoginCallback(pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Query:   url.Values{"state": []string{start.State}},
		Headers: http.Header{callbackTokenHeader: []string{"token-ok"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("授权应成功，实际 %d：%s", resp.StatusCode, truncateText(string(resp.Body), 200))
	}
	var out map[string]any
	if errUnmarshal := json.Unmarshal(resp.Body, &out); errUnmarshal != nil {
		t.Fatalf("结果应为合法 JSON：%v", errUnmarshal)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("结果 ok 应为 true，实际 %v", out)
	}

	// 宿主轮询必须拿到 success：这是弹窗自动关闭的唯一依据。
	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: start.State})
	pollRaw, _ := handlePollLogin(pollBody)
	var poll pluginapi.AuthLoginPollResponse
	decodeEnvelopeResult(t, pollRaw, &poll)
	if poll.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("授权后轮询应为 success，实际 %q（%s）", poll.Status, poll.Message)
	}
}

// TestHandleLoginCallbackReplayIsIdempotent 校验重放不二次兑换令牌。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，令牌只能兑换一次。
func TestHandleLoginCallbackReplayIsIdempotent(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()
	setupTokenFixtures(t)

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	req := pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Query:   url.Values{"state": []string{start.State}},
		Headers: http.Header{callbackTokenHeader: []string{"token-ok"}},
	}
	if resp := handleLoginCallback(req); resp.StatusCode != http.StatusOK {
		t.Fatalf("首次授权应成功，实际 %d", resp.StatusCode)
	}
	// 第二次带同一个令牌：此时令牌在上游已消费，必须走重放短路而不是再兑换。
	replay := handleLoginCallback(req)
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("重放应仍返回 200，实际 %d", replay.StatusCode)
	}
	if !strings.Contains(string(replay.Body), "已完成") {
		t.Fatalf("重放应提示已完成，实际 %s", truncateText(string(replay.Body), 200))
	}
}

// TestHandleLoginCallbackLooseWithoutState 校验固定书签的无 state 授权。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，面板独立入口与固定书签。
func TestHandleLoginCallbackLooseWithoutState(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()
	setupTokenFixtures(t)

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	resp := handleLoginCallback(pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Headers: http.Header{callbackTokenHeader: []string{"token-ok"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("无 state 也应能授权，实际 %d：%s", resp.StatusCode, truncateText(string(resp.Body), 200))
	}
	var out map[string]any
	if errUnmarshal := json.Unmarshal(resp.Body, &out); errUnmarshal != nil {
		t.Fatalf("结果应为合法 JSON：%v", errUnmarshal)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("结果 ok 应为 true，实际 %v", out)
	}

	// 认领等待中的会话：用户手动开着的宿主弹窗也应当自动关闭。
	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: start.State})
	pollRaw, _ := handlePollLogin(pollBody)
	var poll pluginapi.AuthLoginPollResponse
	decodeEnvelopeResult(t, pollRaw, &poll)
	if poll.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("无 state 授权后应认领会话，实际 %q（%s）", poll.Status, poll.Message)
	}
}

// TestHandleLoginCallbackReportsScriptError 校验脚本失败原因透传到会话。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，宿主弹窗要能看到真实失败原因。
func TestHandleLoginCallbackReportsScriptError(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	resp := handleLoginCallback(pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Query:   url.Values{"state": []string{start.State}},
		Headers: http.Header{callbackErrorHeader: []string{"no_token"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("失败也必须 200（否则面板会判成鉴权失效），实际 %d", resp.StatusCode)
	}
	var out map[string]any
	if errUnmarshal := json.Unmarshal(resp.Body, &out); errUnmarshal != nil {
		t.Fatalf("结果应为合法 JSON：%v", errUnmarshal)
	}
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("脚本报错时 ok 应为 false，实际 %v", out)
	}

	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: start.State})
	pollRaw, _ := handlePollLogin(pollBody)
	var poll pluginapi.AuthLoginPollResponse
	decodeEnvelopeResult(t, pollRaw, &poll)
	if !strings.Contains(poll.Message, "no_token") {
		t.Fatalf("轮询应透出真实失败原因，实际 %q", poll.Message)
	}
}

// TestLoginCallbackRouteIsDispatched 校验回调资源路由真的接到分发表。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，防止分发分支被删而测试不失灵。
func TestLoginCallbackRouteIsDispatched(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	path := "/v0/resource/plugins/" + providerName + callbackPath + "?state=abc"
	payload, _ := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   path,
		Query:  url.Values{"state": []string{"abc"}},
	})
	raw, errCall := handleManagement(payload)
	if errCall != nil {
		t.Fatalf("分发表不应报错：%v", errCall)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("信封解析失败：%v", errUnmarshal)
	}
	if !env.OK {
		t.Fatalf("回调路由应可达，实际信封 ok=false：%s", truncateText(string(raw), 200))
	}
	var resp pluginapi.ManagementResponse
	if errUnmarshal := json.Unmarshal(env.Result, &resp); errUnmarshal != nil {
		t.Fatalf("响应解析失败：%v", errUnmarshal)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("回调页应 200，实际 %d", resp.StatusCode)
	}

	// 注册表里必须能看到该资源，否则宿主的 resource 前缀不会放行。
	reg := managementRegistration()
	found := false
	for _, r := range reg.Resources {
		if r.Path == callbackPath {
			found = true
		}
	}
	if !found {
		t.Fatalf("资源注册表必须包含 %q", callbackPath)
	}
}

// setupTokenFixtures 装好令牌端点与判据端点夹具。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，回调链路用例复用。
func setupTokenFixtures(t *testing.T) {
	t.Helper()
	beat := tokenBeatFixtureServer(t, `{"message":"success","data":{}}`, []*http.Cookie{
		{Name: "sessionid", Value: "sess_value", Path: "/"},
		{Name: "flow_cur_user_sec_id", Value: "sec_token_flow_0123456789", Path: "/"},
	})
	restoreBeat := swapTokenBeatEndpoint(beat.URL)
	t.Cleanup(restoreBeat)

	info := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"success","data":{"sec_user_id":"sec_token_for_test_0123456789","user_id":24681357,"screen_name":"令牌测试"}}`))
	}))
	t.Cleanup(info.Close)
	restoreInfo := swapAccountInfoEndpoint(info.URL + "/passport/account/info/v2/")
	t.Cleanup(restoreInfo)
}
