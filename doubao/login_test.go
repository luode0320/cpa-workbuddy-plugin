package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// isolateAccounts 把账号目录指向临时目录，并在测试结束后恢复。
// [参数] t: 测试上下文。
// [返回] 临时目录路径。
// 最近修改时间：2026-10-17；改动原因：新增，避免测试读写真实账号目录。
func isolateAccounts(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := getAccountsDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir(prev) })
	return dir
}

// resetLoginSessions 清空登录会话表，避免用例之间互相污染。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，用例隔离。
func resetLoginSessions() {
	loginSessionsMu.Lock()
	defer loginSessionsMu.Unlock()
	loginSessions = make(map[string]*loginSession)
}

// decodeEnvelopeResult 解析信封里的 result。
// [参数] t: 测试上下文；raw: 信封 JSON；dst: 目标结构。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，测试断言辅助。
func decodeEnvelopeResult(t *testing.T, raw []byte, dst any) {
	t.Helper()
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("信封解析失败：%v（原文 %s）", errUnmarshal, truncateText(string(raw), 200))
	}
	if !env.OK {
		t.Fatalf("信封 ok=false：%s", truncateText(string(raw), 200))
	}
	if errUnmarshal := json.Unmarshal(env.Result, dst); errUnmarshal != nil {
		t.Fatalf("result 解析失败：%v（原文 %s）", errUnmarshal, truncateText(string(env.Result), 200))
	}
}

// TestStartLoginReturnsBridgePage 校验登录起点返回桥接页而非报错。
//
// 修复前插件未实现 auth.login.start，宿主 StartLogin 拿到 unknown_method 后
// 统一报 failed to generate authorization url——这是用户看到的错误。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定登录起点契约。
func TestStartLoginReturnsBridgePage(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	raw, errStart := handleStartLogin(nil)
	if errStart != nil {
		t.Fatalf("登录起点不应报错：%v", errStart)
	}
	var resp pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, raw, &resp)

	if strings.TrimSpace(resp.State) == "" {
		t.Fatal("必须返回 state，宿主用它登记 OAuth 会话")
	}
	if strings.ContainsAny(resp.State, `/\`) {
		t.Fatalf("state 不能含路径分隔符（宿主会校验）：%q", resp.State)
	}
	if !strings.HasPrefix(resp.URL, "/v0/resource/plugins/"+providerName+"/login") {
		t.Fatalf("URL 应为桥接页相对路径，实际 %q", resp.URL)
	}
	if !strings.Contains(resp.URL, "state="+resp.State) {
		t.Fatalf("桥接页 URL 必须带 state，实际 %q", resp.URL)
	}
	if !resp.ExpiresAt.After(time.Now()) {
		t.Fatalf("过期时间必须晚于当前时间，实际 %v", resp.ExpiresAt)
	}
}

// TestPollLoginPendingBeforeImport 校验未导入前轮询保持 pending。
//
// 防的是「点开弹窗就自动成功」这类假阳性：必须真的产生账号才算完成。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，登录轮询真值校验。
func TestPollLoginPendingBeforeImport(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: start.State})
	pollRaw, errPoll := handlePollLogin(pollBody)
	if errPoll != nil {
		t.Fatalf("轮询不应报错：%v", errPoll)
	}
	var poll pluginapi.AuthLoginPollResponse
	decodeEnvelopeResult(t, pollRaw, &poll)
	if poll.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("未导入时应为 pending，实际 %q（%s）", poll.Status, poll.Message)
	}
}

// TestPollLoginDetectsNewAccount 校验桥接页登记导入后轮询返回 success 且带凭据。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，桥接页结果回传验证。
func TestPollLoginDetectsNewAccount(t *testing.T) {
	dir := isolateAccounts(t)
	resetLoginSessions()

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	writeLoginFixture(t, dir, "doubao-login0001", "登录测试号", time.Now().Format(time.RFC3339))
	if !markLoginImported(start.State, "doubao-login0001") {
		t.Fatal("导入成功后应能登记到登录会话")
	}

	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: start.State})
	pollRaw, _ := handlePollLogin(pollBody)
	var poll pluginapi.AuthLoginPollResponse
	decodeEnvelopeResult(t, pollRaw, &poll)

	if poll.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("导入后应为 success，实际 %q（%s）", poll.Status, poll.Message)
	}
	if poll.Auth.ID != "doubao-login0001.json" {
		t.Fatalf("应回传宿主文件形态的账号标识，实际 %q", poll.Auth.ID)
	}
	if len(poll.Auth.StorageJSON) == 0 {
		t.Fatal("必须回传 StorageJSON，否则宿主无法落盘凭据")
	}
	if poll.Auth.Provider != providerName {
		t.Fatalf("provider 应为 %s，实际 %q", providerName, poll.Auth.Provider)
	}

	// 结果只消费一次：同一 state 再次轮询应报会话不存在。
	pollRaw2, _ := handlePollLogin(pollBody)
	var poll2 pluginapi.AuthLoginPollResponse
	decodeEnvelopeResult(t, pollRaw2, &poll2)
	if poll2.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("结果应只消费一次，第二次轮询实际 %q", poll2.Status)
	}
}

// TestPollLoginIgnoresRefreshedAccounts 校验账号被刷新不算登录成功。
//
// 生产实测发现的假成功：插件每次正常请求后会把滚动 Cookie 写回账号文件，
// 那会刷新 UpdatedAt。若轮询靠「账号变新」判断，用户还没粘贴 Cookie 时
// 宿主就会显示认证成功。成功信号只能来自桥接页的显式登记。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，历史账号不误判。
func TestPollLoginIgnoresRefreshedAccounts(t *testing.T) {
	dir := isolateAccounts(t)
	resetLoginSessions()

	old := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	writeLoginFixture(t, dir, "doubao-stale001", "历史账号", old)

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	// 模拟一次正常请求后的 Cookie 回写：账号内容变了，但没人导入。
	writeLoginFixture(t, dir, "doubao-stale001", "历史账号", time.Now().Format(time.RFC3339))

	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: start.State})
	pollRaw, _ := handlePollLogin(pollBody)
	var poll pluginapi.AuthLoginPollResponse
	decodeEnvelopeResult(t, pollRaw, &poll)
	if poll.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("账号被刷新不应判为登录成功，实际 %q", poll.Status)
	}
}

// TestPollLoginRejectsUnknownState 校验未知 state 返回可读错误。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，过期会话提示。
func TestPollLoginRejectsUnknownState(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: "not-exist"})
	pollRaw, errPoll := handlePollLogin(pollBody)
	if errPoll != nil {
		t.Fatalf("未知 state 不应报错，应返回状态：%v", errPoll)
	}
	var poll pluginapi.AuthLoginPollResponse
	decodeEnvelopeResult(t, pollRaw, &poll)
	if poll.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("未知 state 应为 error，实际 %q", poll.Status)
	}
	if !strings.Contains(poll.Message, "过期") && !strings.Contains(poll.Message, "不存在") {
		t.Fatalf("提示应说明会话失效，实际 %q", poll.Message)
	}
}

// TestLoginBridgePageRendersForm 校验桥接页可用且不含未替换的模板残留。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，桥接页渲染验证。
func TestLoginBridgePageRendersForm(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	resp := handleLoginBridge(start.State, "")
	page := string(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("桥接页应返回 200，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(page, "绑定豆包账号") {
		t.Fatal("桥接页应说明绑定流程")
	}
	if !strings.Contains(page, `id="cookie"`) || !strings.Contains(page, `id="submit"`) {
		t.Fatal("桥接页必须包含 Cookie 输入框与提交按钮")
	}
	if !strings.Contains(page, `data-provider="`+providerName+`"`) {
		t.Fatal("桥接页必须声明 provider，供 JS 拼出管理 API 路径")
	}
	if !strings.Contains(page, `data-state="`+start.State+`"`) {
		t.Fatal("桥接页必须携带 state")
	}
}

// TestLoginBridgePageRejectsUnknownState 校验失效 state 给出可操作错误页。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，失效会话提示。
func TestLoginBridgePageRejectsUnknownState(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	resp := handleLoginBridge("not-exist", "")
	page := string(resp.Body)
	if !strings.Contains(page, "重新") {
		t.Fatalf("失效会话页应引导用户重新发起，实际 %s", truncateText(page, 300))
	}
	if strings.Contains(page, `id="submit"`) {
		t.Fatal("失效会话不应渲染可提交表单")
	}
}

// TestParseAuthClaimsDoubaoFiles 校验宿主扫描账号文件时能认领豆包凭据。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，账号文件认领验证。
func TestParseAuthClaimsDoubaoFiles(t *testing.T) {
	sa := completionTestAccount()
	sa.AuthID = "doubao-parse0001"
	raw, _ := json.Marshal(sa)

	req, _ := json.Marshal(pluginapi.AuthParseRequest{FileName: "doubao-parse0001.json", RawJSON: raw})
	respRaw, errParse := handleParseAuth(req)
	if errParse != nil {
		t.Fatalf("解析不应报错：%v", errParse)
	}
	var resp pluginapi.AuthParseResponse
	decodeEnvelopeResult(t, respRaw, &resp)
	if !resp.Handled {
		t.Fatal("豆包账号文件必须被认领")
	}
	if resp.Auth.ID != "doubao-parse0001.json" {
		t.Fatalf("应返回宿主文件形态的账号标识，实际 %q", resp.Auth.ID)
	}

	// 非豆包文件必须交回宿主，不能抢占别人的账号。
	otherReq, _ := json.Marshal(pluginapi.AuthParseRequest{FileName: "codex-other.json", RawJSON: raw})
	otherRaw, _ := handleParseAuth(otherReq)
	var otherResp pluginapi.AuthParseResponse
	decodeEnvelopeResult(t, otherRaw, &otherResp)
	if otherResp.Handled {
		t.Fatal("非豆包文件不应被认领")
	}
}

// TestParseAuthRejectsFileWithoutCookie 校验缺 Cookie 的文件不被认领。
//
// 认领一个没有凭据的文件会让宿主登记出无法使用的账号。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，空凭据防护。
func TestParseAuthRejectsFileWithoutCookie(t *testing.T) {
	req, _ := json.Marshal(pluginapi.AuthParseRequest{
		FileName: "doubao-empty0001.json",
		RawJSON:  []byte(`{"type":"doubao-provider","auth_id":"doubao-empty0001"}`),
	})
	respRaw, _ := handleParseAuth(req)
	var resp pluginapi.AuthParseResponse
	decodeEnvelopeResult(t, respRaw, &resp)
	if resp.Handled {
		t.Fatal("缺 Cookie 的文件不应被认领")
	}
}

// TestRefreshAuthKeepsCredentials 校验刷新返回原凭据并推远下次刷新时间。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，刷新入口验证。
func TestRefreshAuthKeepsCredentials(t *testing.T) {
	sa := completionTestAccount()
	sa.AuthID = "doubao-refresh01"
	storage, _ := json.Marshal(sa)
	req, _ := json.Marshal(pluginapi.AuthRefreshRequest{AuthID: sa.AuthID, StorageJSON: storage})

	raw, errRefresh := handleRefreshAuth(req)
	if errRefresh != nil {
		t.Fatalf("刷新不应报错：%v", errRefresh)
	}
	var resp pluginapi.AuthRefreshResponse
	decodeEnvelopeResult(t, raw, &resp)
	if resp.Auth.ID != authFileNameOf(sa.AuthID) {
		t.Fatalf("应原样返回账号（宿主文件形态），实际 %q", resp.Auth.ID)
	}
	if !resp.NextRefreshAfter.After(time.Now()) {
		t.Fatalf("下次刷新时间应在未来，实际 %v", resp.NextRefreshAfter)
	}
}

// TestRefreshAuthRejectsEmptyStorage 校验空凭据时报错而不是伪造账号。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，空凭据防护。
func TestRefreshAuthRejectsEmptyStorage(t *testing.T) {
	req, _ := json.Marshal(pluginapi.AuthRefreshRequest{AuthID: "doubao-none"})
	if _, errRefresh := handleRefreshAuth(req); errRefresh == nil {
		t.Fatal("空凭据应报错")
	}
}

// TestAuthMethodsAreDispatched 校验四个认证方法都已接入分发表。
//
// 修复前只接了 auth.identifier，其余方法落到 unknown_method，宿主 OAuth
// 入口因此直接失败。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，防止方法分发再次漂移。
func TestAuthMethodsAreDispatched(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	for _, method := range []string{"auth.login.start", "auth.login.poll", "auth.refresh"} {
		raw, errCall := handleMethod(method, nil)
		// poll / refresh 对空载荷会返回业务错误（缺 state / 空凭据），这正说明
		// 方法已接入分发表；只有 unknown_method 才是未接入的信号。
		var env envelope
		if errCall == nil {
			if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
				t.Fatalf("%s 信封解析失败：%v", method, errUnmarshal)
			}
			if env.Error != nil && env.Error.Code == "unknown_method" {
				t.Fatalf("%s 未接入分发表（unknown_method）", method)
			}
			continue
		}
		if strings.Contains(errCall.Error(), "unknown_method") {
			t.Fatalf("%s 未接入分发表：%v", method, errCall)
		}
	}
}

// TestLoginBridgeImportWithoutManagementKey 校验桥接页提交不需要管理密钥。
//
// resource 路由由宿主以 GET 免密钥放行，桥接页是浏览器顶层打开的普通页面，
// 拿不到管理密钥也不该要求用户填。这条用例锁定「无 Authorization 也能导入」，
// 防止以后有人把它改回 management 路由。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定免密钥提交契约。
func TestLoginBridgeImportWithoutManagementKey(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"success","data":{"sec_user_id":"sec_bridge_for_test_0123456789abcdef","user_id":13572468,"screen_name":"桥接测试"}}`))
	}))
	defer srv.Close()
	restore := swapAccountInfoEndpoint(srv.URL + "/passport/account/info/v2/")
	defer restore()

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	req := pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/resource/plugins/" + providerName + "/login/import",
		Query:   url.Values{"state": []string{start.State}},
		Headers: http.Header{loginBridgeImportHeader: []string{validCookieHeader}},
	}
	resp := handleLoginBridgeImport(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("免密钥导入应成功，实际 %d：%s", resp.StatusCode, truncateText(string(resp.Body), 200))
	}
	var out map[string]any
	if errUnmarshal := json.Unmarshal(resp.Body, &out); errUnmarshal != nil {
		t.Fatalf("结果应为合法 JSON：%v", errUnmarshal)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("结果 ok 应为 true，实际 %v", out)
	}
	authID, _ := out["auth_id"].(string)
	if authID == "" {
		t.Fatal("结果应回传 auth_id")
	}

	// 导入后宿主轮询必须能拿到 success：这是桥接页与宿主之间的唯一交接点。
	pollBody, _ := json.Marshal(pluginapi.AuthLoginPollRequest{State: start.State})
	pollRaw, _ := handlePollLogin(pollBody)
	var poll pluginapi.AuthLoginPollResponse
	decodeEnvelopeResult(t, pollRaw, &poll)
	if poll.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("桥接页导入后轮询应为 success，实际 %q（%s）", poll.Status, poll.Message)
	}
	if poll.Auth.ID != authFileNameOf(authID) {
		t.Fatalf("轮询回传账号应为 %q，实际 %q", authFileNameOf(authID), poll.Auth.ID)
	}
}

// TestLoginBridgeImportRejectsBadInput 校验提交入口的三类拒绝路径。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，免密钥入口的边界防护。
func TestLoginBridgeImportRejectsBadInput(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	cases := []struct {
		name    string
		req     pluginapi.ManagementRequest
		wantMsg string
	}{
		{
			name:    "缺 state",
			req:     pluginapi.ManagementRequest{Method: http.MethodGet, Headers: http.Header{loginBridgeImportHeader: []string{validCookieHeader}}},
			wantMsg: "登录会话",
		},
		{
			name:    "未知 state",
			req:     pluginapi.ManagementRequest{Method: http.MethodGet, Query: url.Values{"state": []string{"not-exist"}}, Headers: http.Header{loginBridgeImportHeader: []string{validCookieHeader}}},
			wantMsg: "登录会话",
		},
		{
			name:    "缺 Cookie",
			req:     pluginapi.ManagementRequest{Method: http.MethodGet, Query: url.Values{"state": []string{start.State}}},
			wantMsg: "cookie",
		},
	}
	for _, c := range cases {
		resp := handleLoginBridgeImport(c.req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s：应返回 400，实际 %d", c.name, resp.StatusCode)
			continue
		}
		if !strings.Contains(strings.ToLower(string(resp.Body)), strings.ToLower(c.wantMsg)) {
			t.Errorf("%s：提示应含 %q，实际 %s", c.name, c.wantMsg, truncateText(string(resp.Body), 200))
		}
	}
}

// TestLoginBridgeImportRouteIsDispatched 校验 resource 提交路由真的接到分发表。
//
// 只测 handleLoginBridgeImport 本身不够：把分发分支删掉、改成 404，函数级用例
// 仍会全绿。这条用例走 handleManagement，锁定「路由可达」这一层。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，防止分发分支被删而测试不失灵。
func TestLoginBridgeImportRouteIsDispatched(t *testing.T) {
	isolateAccounts(t)
	resetLoginSessions()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"success","data":{"sec_user_id":"sec_route_for_test_0123456789abcdef","user_id":24681357,"screen_name":"路由测试"}}`))
	}))
	defer srv.Close()
	restore := swapAccountInfoEndpoint(srv.URL + "/passport/account/info/v2/")
	defer restore()

	startRaw, _ := handleStartLogin(nil)
	var start pluginapi.AuthLoginStartResponse
	decodeEnvelopeResult(t, startRaw, &start)

	path := "/v0/resource/plugins/" + providerName + "/login/import?state=" + url.QueryEscape(start.State)
	payload, _ := json.Marshal(pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    path,
		Query:   url.Values{"state": []string{start.State}},
		Headers: http.Header{loginBridgeImportHeader: []string{validCookieHeader}},
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
		t.Fatalf("提交路由应可达，实际信封 ok=false：%s", truncateText(string(raw), 200))
	}
	var resp pluginapi.ManagementResponse
	if errUnmarshal := json.Unmarshal(env.Result, &resp); errUnmarshal != nil {
		t.Fatalf("响应解析失败：%v", errUnmarshal)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("提交应成功，实际 %d：%s", resp.StatusCode, truncateText(string(resp.Body), 200))
	}
}

// writeLoginFixture 在账号目录写一份最小可用的豆包账号文件。
// [参数] t: 测试上下文；dir: 账号目录；authID: 账号标识；label: 备注名；
// updatedAt: 更新时间（RFC3339）。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，登录链路夹具。
func writeLoginFixture(t *testing.T, dir, authID, label, updatedAt string) {
	t.Helper()
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       authID,
		CookieHeader: validCookieHeader,
		Label:        label,
		ScreenName:   label,
		BotID:        defaultBotID,
		CreatedAt:    updatedAt,
		UpdatedAt:    updatedAt,
	}
	if errWrite := writeAuthFileDirect(dir+"/"+authID+".json", sa); errWrite != nil {
		t.Fatalf("写账号夹具失败：%v", errWrite)
	}
}

// TestAuthDataIDMatchesHostFileName 校验登录回传的注册 ID 与宿主文件名同形。
//
// 宿主两条注册路径的 ID 来源不同：host.auth.save 用相对路径（含 .json），
// 登录结果直接用 AuthData.ID。ID 不一致时同一个物理文件会注册成两条记录，
// 管理页和插件面板都会出现两个一模一样的账号（2026-10-17 生产实测）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，防重复注册回归。
func TestAuthDataIDMatchesHostFileName(t *testing.T) {
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       "doubao-idform0001",
		CookieHeader: validCookieHeader,
		Label:        "ID 形态测试",
	}

	auth := authDataForLogin(sa)
	if auth.ID != auth.FileName {
		t.Fatalf("注册 ID 必须与文件名一致，实际 ID=%q FileName=%q", auth.ID, auth.FileName)
	}
	if auth.ID != "doubao-idform0001.json" {
		t.Fatalf("注册 ID 应为宿主文件形态，实际 %q", auth.ID)
	}

	// 已带后缀的标识不得叠成 .json.json。
	again := authDataForLogin(&StoredAuth{AuthID: "doubao-idform0002.json", CookieHeader: validCookieHeader})
	if again.ID != "doubao-idform0002.json" {
		t.Fatalf("重复后缀应被归一，实际 %q", again.ID)
	}
}

// TestListAllAuthFilesDedupesByAuthID 校验同一账号的多种 ID 形态只保留一条。
//
// 修复前的重复注册会在宿主侧留下「doubao-xxx」与「doubao-xxx.json」两条记录，
// 它们指向同一物理文件。宿主来源在测试环境不可用，故直接覆盖去重内核。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，列表去重回归。
func TestListAllAuthFilesDedupesByAuthID(t *testing.T) {
	first := &StoredAuth{AuthID: "doubao-dedupe0001", Label: "先出现的"}
	duplicate := &StoredAuth{AuthID: "doubao-dedupe0001", Label: "重复的"}
	other := &StoredAuth{AuthID: "doubao-dedupe0002", Label: "另一个号"}

	out := dedupeAuthByID([]*StoredAuth{first, duplicate, nil, other})
	if len(out) != 2 {
		t.Fatalf("应保留 2 个不同账号，实际 %d", len(out))
	}
	if out[0].Label != "先出现的" {
		t.Fatalf("应保留先出现的条目，实际 %q", out[0].Label)
	}
	if out[1].AuthID != "doubao-dedupe0002" {
		t.Fatalf("不同账号不应被合并，实际 %q", out[1].AuthID)
	}
}
