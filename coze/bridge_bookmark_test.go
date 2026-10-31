package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件覆盖「固定书签 + 无 state 导入」这条新增链路。
//
// 与 login_test.go 的分工：那个文件管令牌换发与授权会话生命周期，
// 这里只管两件事——书签脚本必须与 state 解耦（否则无法长期复用），
// 以及回调页在无 state 时仍能安全落盘。

// TestBuildBookmarkletScript_FixedAndStateless 验证书签脚本的固定形态。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，锁定书签契约。
func TestBuildBookmarkletScript_FixedAndStateless(t *testing.T) {
	script := buildBookmarkletScript()
	required := []string{
		"javascript:",
		cpaOriginPlaceholder,
		"/v0/resource/plugins/" + providerName + callbackPath + "#token=",
		"x-tt-token",
		"credentials:'include'",
	}
	for _, want := range required {
		if !strings.Contains(script, want) {
			t.Fatalf("书签脚本缺少 %q，实际：%s", want, script)
		}
	}
	// 一旦书签里烤进 state，它就只能用一次——这正是本次要消除的缺陷。
	if strings.Contains(script, "state=") {
		t.Fatalf("固定书签不应携带 state，实际：%s", script)
	}
}

// TestLoginCallback_LooseModeImportsToken 验证无 state 时仍能完成落盘。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，面板独立入口主链路。
func TestLoginCallback_LooseModeImportsToken(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	defer setAccountsDir("")

	token := &tokenBeatStub{t: t, cookieNames: defaultCookieNames}
	info := &accountInfoStub{valid: true, secUserID: "sec-loose"}
	cleanup := setupAuthStubs(t, token, info)
	defer cleanup()

	req := pluginapi.ManagementRequest{Query: url.Values{}, Headers: http.Header{}}
	req.Headers.Set(callbackTokenHeader, "one-time-token")

	resp := handleLoginCallback(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("宽松模式应返回 200，实际 %d", resp.StatusCode)
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(resp.Body, &payload); errUnmarshal != nil {
		t.Fatalf("响应应为 JSON：%v，原文 %s", errUnmarshal, string(resp.Body))
	}
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("宽松模式导入应成功，实际 %v", payload)
	}
	entries, errList := readDirNames(dir)
	if errList != nil {
		t.Fatalf("读取账号目录失败：%v", errList)
	}
	if len(entries) != 1 {
		t.Fatalf("应落盘 1 个账号文件，实际 %v", entries)
	}
	// 无 state 时不存在会话可登记，宿主轮询也不会被误触发。
	loginSessionsMu.Lock()
	pending := len(loginSessions)
	loginSessionsMu.Unlock()
	if pending != 0 {
		t.Fatalf("宽松模式不应创建授权会话，实际残留 %d 个", pending)
	}
}

// TestLoginCallback_LooseModeServesShell 验证空请求先返回接收页。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，fragment 接收页。
func TestLoginCallback_LooseModeServesShell(t *testing.T) {
	req := pluginapi.ManagementRequest{Query: url.Values{}, Headers: http.Header{}}
	resp := handleLoginCallback(req)
	body := string(resp.Body)
	if !strings.Contains(body, "正在接收令牌") {
		t.Fatalf("首开应返回接收页，实际：%s", body)
	}
	if !strings.Contains(body, "/v0/resource/plugins/"+providerName+callbackPath) {
		t.Fatalf("接收页应指向回调自身，实际：%s", body)
	}
}

// TestLoginCallback_StrictModeRejectsUnknownState 验证带 state 时仍严格校验。
//
// 面板宽松模式不能削弱宿主弹窗链路的安全语义：伪造 state 必须被拒绝，
// 且不得落盘任何凭据。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，严格模式回归。
func TestLoginCallback_StrictModeRejectsUnknownState(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	defer setAccountsDir("")

	token := &tokenBeatStub{t: t, cookieNames: defaultCookieNames}
	info := &accountInfoStub{valid: true, secUserID: "sec-strict"}
	cleanup := setupAuthStubs(t, token, info)
	defer cleanup()

	req := pluginapi.ManagementRequest{
		Query:   url.Values{"state": []string{"never-issued-state"}},
		Headers: http.Header{},
	}
	req.Headers.Set(callbackTokenHeader, "one-time-token")

	resp := handleLoginCallback(req)
	if !strings.Contains(string(resp.Body), "授权会话不存在或已过期") {
		t.Fatalf("伪造 state 应被拒绝，实际：%s", string(resp.Body))
	}
	entries, _ := readDirNames(dir)
	if len(entries) != 0 {
		t.Fatalf("拒绝时不应落盘，实际 %v", entries)
	}
}

// TestLoginBridge_NoStateRendersBookmarklet 验证独立入口页渲染固定书签。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，入口页形态。
func TestLoginBridge_NoStateRendersBookmarklet(t *testing.T) {
	resp := handleLoginBridge("")
	body := string(resp.Body)
	for _, want := range []string{
		`id="bmLink"`,
		"javascript:",
		cpaOriginPlaceholder,
		`id="openCoze"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("独立入口页缺少 %q", want)
		}
	}
	if strings.Contains(body, "缺少 state 参数") {
		t.Fatal("无 state 不应再被当成错误")
	}
	// 兜底通道仍在，但内容不能带 state——否则它又变成一次性的。
	if !strings.Contains(body, `id="authScript"`) {
		t.Fatal("独立入口应保留 Console 兜底脚本")
	}
	if strings.Contains(body, "?state=") {
		t.Fatal("独立入口的兜底脚本不应携带 state")
	}
}

// TestLoginBridge_WithStateKeepsScript 验证宿主弹窗入口仍保留一次性脚本。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，宿主链路回归。
func TestLoginBridge_WithStateKeepsScript(t *testing.T) {
	state := randomState()
	loginSessionsMu.Lock()
	loginSessions[state] = &loginSession{startedAt: nowForTest()}
	loginSessionsMu.Unlock()
	defer func() {
		loginSessionsMu.Lock()
		delete(loginSessions, state)
		loginSessionsMu.Unlock()
	}()

	resp := handleLoginBridge(state)
	body := string(resp.Body)
	if !strings.Contains(body, `id="authScript"`) {
		t.Fatal("有 state 时应保留一次性脚本（宿主弹窗靠它自动关闭）")
	}
	if !strings.Contains(body, `id="bmLink"`) {
		t.Fatal("有 state 时也应提供书签，便于用户跳过 Console")
	}
}

// TestAdoptPendingLoginSession_ClaimsNewest 验证无 state 导入能认领宿主弹窗会话。
//
// 这是「书签授权成功但弹窗一直等待」的回归用例：固定书签不带 state，
// 若落盘后不认领，宿主 auth.login.poll 永远拿不到结果。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，弹窗不关闭缺陷回归。
func TestAdoptPendingLoginSession_ClaimsNewest(t *testing.T) {
	resetLoginSessionsForTest()
	defer resetLoginSessionsForTest()

	state := randomState()
	loginSessionsMu.Lock()
	loginSessions[state] = &loginSession{startedAt: time.Now()}
	loginSessionsMu.Unlock()

	if !adoptPendingLoginSession("coze-adopted") {
		t.Fatal("存在待完成会话时应认领成功")
	}
	if got := loginImportedAuthID(state); got != "coze-adopted" {
		t.Fatalf("会话应登记本次账号，实际 %q", got)
	}
}

// TestAdoptPendingLoginSession_SkipsImported 验证已完成的会话不会被改写。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，防串号。
func TestAdoptPendingLoginSession_SkipsImported(t *testing.T) {
	resetLoginSessionsForTest()
	defer resetLoginSessionsForTest()

	state := randomState()
	loginSessionsMu.Lock()
	loginSessions[state] = &loginSession{startedAt: time.Now(), importedAuthID: "coze-old"}
	loginSessionsMu.Unlock()

	if adoptPendingLoginSession("coze-new") {
		t.Fatal("已导入的会话不应被重复认领")
	}
	if got := loginImportedAuthID(state); got != "coze-old" {
		t.Fatalf("已完成会话的账号不应被覆盖，实际 %q", got)
	}
}

// TestAdoptPendingLoginSession_NoSession 验证没有弹窗会话时不误报。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，无会话分支。
func TestAdoptPendingLoginSession_NoSession(t *testing.T) {
	resetLoginSessionsForTest()
	defer resetLoginSessionsForTest()
	if adoptPendingLoginSession("coze-x") {
		t.Fatal("无待完成会话时应返回 false")
	}
}

// TestHandleStartLogin_AbsoluteURL 验证授权入口返回绝对地址。
//
// 宿主把该字段原样展示给用户，相对路径无法复制成完整 URL。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，完整地址复制缺陷回归。
func TestHandleStartLogin_AbsoluteURL(t *testing.T) {
	resetLoginSessionsForTest()
	defer resetLoginSessionsForTest()

	publicOriginMu.Lock()
	old := publicOrigin
	publicOrigin = "https://cpa.example.com"
	publicOriginMu.Unlock()
	defer func() {
		publicOriginMu.Lock()
		publicOrigin = old
		publicOriginMu.Unlock()
	}()

	raw, err := handleStartLogin([]byte("{}"))
	if err != nil {
		t.Fatalf("登录起点不应失败：%v", err)
	}
	want := "https://cpa.example.com/v0/resource/plugins/" + providerName + "/login?state="
	if !strings.Contains(string(raw), want) {
		t.Fatalf("入口 URL 应为绝对地址，实际：%s", string(raw))
	}
}

// TestRememberPublicOrigin 验证转发头优先级与多级代理取值。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，origin 解析。
func TestRememberPublicOrigin(t *testing.T) {
	publicOriginMu.Lock()
	old := publicOrigin
	publicOrigin = ""
	publicOriginMu.Unlock()
	defer func() {
		publicOriginMu.Lock()
		publicOrigin = old
		publicOriginMu.Unlock()
	}()

	h := http.Header{}
	h.Set("X-Forwarded-Host", "cpa.example.com, inner.local")
	h.Set("X-Forwarded-Proto", "https")
	rememberPublicOrigin(h)
	if got := currentPublicOrigin(); got != "https://cpa.example.com" {
		t.Fatalf("应取转发头首段，实际 %q", got)
	}

	// 没有转发头时回退 Host；协议默认 https。
	rememberPublicOrigin(http.Header{"Host": []string{"10.0.0.1:8317"}})
	if got := currentPublicOrigin(); got != "https://10.0.0.1:8317" {
		t.Fatalf("应回退 Host，实际 %q", got)
	}

	// 空头不覆盖已有值。
	rememberPublicOrigin(http.Header{})
	if got := currentPublicOrigin(); got != "https://10.0.0.1:8317" {
		t.Fatalf("空头不应覆盖，实际 %q", got)
	}

	// 回环请求不得覆盖已观测到的外部域名（容器内自测、健康检查都会这样打）。
	rememberPublicOrigin(http.Header{"Host": []string{"127.0.0.1:8317"}})
	if got := currentPublicOrigin(); got != "https://10.0.0.1:8317" {
		t.Fatalf("回环地址不应覆盖外部 origin，实际 %q", got)
	}

	// 从未观测到外部 origin 时，回环作为兜底可用。
	publicOriginMu.Lock()
	publicOrigin = ""
	publicOriginMu.Unlock()
	rememberPublicOrigin(http.Header{"Host": []string{"127.0.0.1:8317"}})
	if got := currentPublicOrigin(); got != "https://127.0.0.1:8317" {
		t.Fatalf("空状态时应接受回环兜底，实际 %q", got)
	}
}

// resetLoginSessionsForTest 清空授权会话表，保证用例之间互不影响。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，全局状态隔离。
func resetLoginSessionsForTest() {
	loginSessionsMu.Lock()
	loginSessions = make(map[string]*loginSession)
	loginSessionsMu.Unlock()
}
