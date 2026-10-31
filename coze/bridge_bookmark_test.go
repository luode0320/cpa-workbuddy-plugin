package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

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
