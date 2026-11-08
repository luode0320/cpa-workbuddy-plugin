package main

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 桥接页「复制授权链接」回归测试。
//
// 锁定四件事：
//   - 复制按钮与只读链接框同时存在（只有按钮没有全文，自动复制失败时仍要手动选中）；
//   - 链接全文落在只读框里，复制逻辑从框内取值（页面里不另存第二份可复制副本）；
//   - 剪贴板主通道与 execCommand 兜底都要在（宿主页面可能不是安全上下文）；
//   - 复制脚本不带 markdown 反引号（会截断 Go 原始字符串）。

// TestBridgePageOffersCopyableAuthURL 验证引导页给出可复制的授权链接。
//
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-08 21:25:00；改动原因：新增，锁定桥接页复制授权链接能力。
func TestBridgePageOffersCopyableAuthURL(t *testing.T) {
	// 1. 构造一个带授权链接的会话，走引导页渲染分支。
	authURL := "https://www.trae.cn/authorization?state=trw-copy&code_challenge=abc"
	state := "trw-copy"
	browserLoginSessions.Store(state, &browserLoginSession{
		Verifier:  "v",
		CreatedAt: time.Now(),
		AuthURL:   authURL,
	})
	t.Cleanup(func() { browserLoginSessions.Delete(state) })

	resp := handleBrowserLoginBridge(pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/" + providerName + "/browser-login/bridge",
		Query:  url.Values{"state": {state}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("bridge status %d", resp.StatusCode)
	}
	html := string(resp.Body)

	// 2. 复制按钮与只读链接框必须同时在场。
	if !strings.Contains(html, "id=\"copyAuthUrl\"") {
		t.Fatal("bridge page must offer the copy-authorization-link button")
	}
	if !strings.Contains(html, "id=\"authUrlBox\"") {
		t.Fatal("bridge page must show the read-only authorization URL box")
	}
	if !strings.Contains(html, "复制授权链接") {
		t.Fatal("copy button must carry a Chinese label")
	}

	// 3. 链接全文必须落在只读框里（页面按 HTML 规则转义 & 等字符）。
	//    只读框是复制逻辑的唯一取值来源：页面上不允许再存第二份可复制副本。
	boxIdx := strings.Index(html, "id=\"authUrlBox\"")
	if boxIdx < 0 {
		t.Fatal("bridge page must show the read-only authorization URL box")
	}
	escaped := htmlEscape(authURL)
	boxTail := html[boxIdx:]
	if !strings.Contains(boxTail, escaped) {
		t.Fatal("read-only box must carry the full authorization URL")
	}
	if !strings.Contains(boxTail[:strings.Index(boxTail, "</textarea>")], escaped) {
		t.Fatal("authorization URL must live inside the read-only box")
	}

	// 4. 复制逻辑必须从只读框取值并落到剪贴板（含非安全上下文兜底）。
	if !strings.Contains(html, "document.getElementById('authUrlBox')") {
		t.Fatal("copy handler must read the URL from the read-only box")
	}
	if !strings.Contains(html, "navigator.clipboard.writeText") {
		t.Fatal("copy handler must use navigator.clipboard")
	}
	if !strings.Contains(html, "execCommand('copy')") {
		t.Fatal("copy handler must fall back to execCommand when clipboard API is unavailable")
	}
	if !strings.Contains(html, "id=\"copyAuthUrlHint\"") {
		t.Fatal("copy handler must report the result to the user")
	}

	// 5. 页面 JS 不能含 markdown 反引号（会截断 Go 原始字符串）。
	if strings.Contains(html, "`") {
		t.Fatal("bridge page HTML must not contain backticks")
	}
}

// TestBrowserLoginStartStoresAuthURLOnSession 验证面板发起的会话也带上授权链接。
//
// 面板路径的 start 会话此前没填 AuthURL，桥接页按 authURL 非空决定是否渲染
// 复制按钮，导致面板发起的登录在页面上看不到复制入口。
//
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-08 21:25:00；改动原因：新增，锁定面板路径会话携带授权链接。
func TestBrowserLoginStartStoresAuthURLOnSession(t *testing.T) {
	resp := handleBrowserLoginStart(pluginapi.ManagementRequest{
		Method: "POST",
		Path:   "/v0/management/plugins/" + providerName + "/browser-login/start",
		Body:   []byte(`{"redirect_origin":"https://cpa.example.com"}`),
	})
	state, _ := resp["state"].(string)
	if state == "" {
		t.Fatalf("start must return a state: %v", resp)
	}
	t.Cleanup(func() { browserLoginSessions.Delete(state) })

	authURL, _ := resp["auth_url"].(string)
	if !strings.HasPrefix(authURL, browserLoginHost+"/authorization?") {
		t.Fatalf("auth_url %q must be the Trae authorization page", authURL)
	}

	sRaw, ok := browserLoginSessions.Load(state)
	if !ok {
		t.Fatal("session missing under returned state")
	}
	s := sRaw.(*browserLoginSession)
	if s.AuthURL != authURL {
		t.Fatalf("session AuthURL %q must equal the returned auth_url %q", s.AuthURL, authURL)
	}
}
