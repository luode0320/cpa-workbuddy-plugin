package cpa_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/cpa"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginabi"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
)

// newLoginTestPlugin 起一个只完成注册的插件实例，供登录面测试复用。
//
// 登录面全部走 management.handle，不需要真实上游；因此 hostCall 只回空结果。
// [参数] t 是测试上下文。
// [返回] 已注册完成、测试结束自动 shutdown 的插件实例。
// 最近修改时间：2026-11-21；改动原因：新增。
func newLoginTestPlugin(t *testing.T) *cpa.Plugin {
	t.Helper()

	// 先取临时目录再注册 shutdown 清理：t.Cleanup 是后进先出，
	// 只有让 shutdown 后注册，它才会先于 TempDir 删除执行，
	// 否则 Windows 上 requests.db 仍被占用会导致清理阶段报错。
	dataDir := t.TempDir()

	p := cpa.NewPlugin(func(method string, payload []byte) ([]byte, error) {
		return json.Marshal(pluginabi.Envelope{OK: true, Result: json.RawMessage(`{}`)})
	})
	t.Cleanup(func() {
		_, _ = p.HandleMethod(pluginabi.MethodPluginShutdown, nil)
	})

	cfgYAML := []byte("data_dir: " + filepath.ToSlash(dataDir) + "\nauto_start: false\n")
	regReq, err := json.Marshal(map[string]any{
		"config_yaml":    cfgYAML,
		"schema_version": pluginabi.SchemaVersion,
	})
	if err != nil {
		t.Fatalf("marshal register request: %v", err)
	}
	if _, err := p.HandleMethod(pluginabi.MethodPluginRegister, regReq); err != nil {
		t.Fatalf("plugin.register failed: %v", err)
	}
	return p
}

// callManagement 以给定路径与查询参数调用 management.handle。
//
// [参数] p 是插件实例；rawPath 是含查询串的原始路径；host 与 forwardedProto 用于伪造反向代理头。
// [返回] 已解码的管理响应。
// 最近修改时间：2026-11-21；改动原因：新增。
func callManagement(t *testing.T, p *cpa.Plugin, rawPath, host, forwardedProto string) pluginapi.ManagementResponse {
	t.Helper()

	headers := http.Header{}
	if host != "" {
		headers.Set("Host", host)
	}
	if forwardedProto != "" {
		headers.Set("X-Forwarded-Proto", forwardedProto)
	}
	parsed, err := url.Parse(rawPath)
	if err != nil {
		t.Fatalf("parse path %q: %v", rawPath, err)
	}

	body, err := json.Marshal(pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    rawPath,
		Headers: headers,
		Query:   parsed.Query(),
	})
	if err != nil {
		t.Fatalf("marshal management request: %v", err)
	}
	raw, err := p.HandleMethod(pluginabi.MethodManagementHandle, body)
	if err != nil {
		t.Fatalf("management.handle %q failed: %v", rawPath, err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal management response: %v", err)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(encoded, &resp); err != nil {
		t.Fatalf("unmarshal management response: %v", err)
	}
	return resp
}

// TestLoginBridgeRendersPairCode 校验桥接页渲染出了配对码与全部占位符替换结果。
//
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestLoginBridgeRendersPairCode(t *testing.T) {
	p := newLoginTestPlugin(t)
	resp := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login?state=state-under-test", "cpa.example.com", "https")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", resp.StatusCode, string(resp.Body))
	}
	if ct := resp.Headers.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("unexpected content type: %s", ct)
	}
	page := string(resp.Body)
	if strings.Contains(page, "{{") {
		t.Fatalf("bridge page still has unreplaced placeholders")
	}
	if !strings.Contains(page, "cpa|https://cpa.example.com|state-under-test") {
		t.Fatalf("bridge page missing pair code: %s", firstBytes(page, 400))
	}
	if !strings.Contains(page, "/v0/resource/plugins/gemini-web2api/login/submit") {
		t.Fatalf("bridge page missing submit url")
	}
	if !strings.Contains(page, "/v0/resource/plugins/gemini-web2api/login/extension.zip") {
		t.Fatalf("bridge page missing extension url")
	}
	// 桥接页必须带上 origin 上报地址：宿主转交的请求头里没有 Host，
	// 只有浏览器把 location.origin 报上来，授权链接才能落在真实域名上。
	if !strings.Contains(page, "/v0/resource/plugins/gemini-web2api/login/origin") {
		t.Fatalf("bridge page missing origin report url")
	}
}

// TestLoginBridgeEscapesInjectedOrigin 校验 Host 头注入不会逃出属性值。
//
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestLoginBridgeEscapesInjectedOrigin(t *testing.T) {
	p := newLoginTestPlugin(t)
	resp := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login",
		`evil"><script>alert(1)</script>`, "https")

	page := string(resp.Body)
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Fatalf("bridge page did not escape injected host")
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("expected escaped host in page: %s", firstBytes(page, 400))
	}
	if strings.Contains(page, `evil">`) {
		t.Fatalf("bridge page leaked unescaped quote from host")
	}
}

// TestLoginSubmitRejectsBadInput 校验缺 Cookie、缺 state 两种非法请求都被拒。
//
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestLoginSubmitRejectsBadInput(t *testing.T) {
	p := newLoginTestPlugin(t)

	noCookie := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login/submit?state=abc", "", "")
	if noCookie.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing cookies, got %d", noCookie.StatusCode)
	}

	noState := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login/submit?cookies=SAPISID%3Dx", "", "")
	if noState.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing state, got %d", noState.StatusCode)
	}

	unknownState := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login/submit?state=unknown&cookies=SAPISID%3Dx", "", "")
	if unknownState.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown state, got %d", unknownState.StatusCode)
	}
}

// TestLoginPollReportsExpiredForUnknownState 校验轮询未知会话返回 expired。
//
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestLoginPollReportsExpiredForUnknownState(t *testing.T) {
	p := newLoginTestPlugin(t)
	resp := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login/poll?state=missing", "", "")

	var payload map[string]any
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal poll payload: %v", err)
	}
	if payload["status"] != "expired" {
		t.Fatalf("expected expired, got %v", payload["status"])
	}
}

// TestLoginExtensionZipContainsThreeFiles 校验扩展下载返回可解压的三文件包。
//
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestLoginExtensionZipContainsThreeFiles(t *testing.T) {
	p := newLoginTestPlugin(t)
	resp := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login/extension.zip", "", "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if ct := resp.Headers.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("unexpected content type: %s", ct)
	}
	if cd := resp.Headers.Get("Content-Disposition"); !strings.Contains(cd, "gemini-web2api-auth-helper.zip") {
		t.Fatalf("unexpected content disposition: %s", cd)
	}

	reader, err := zip.NewReader(bytes.NewReader(resp.Body), int64(len(resp.Body)))
	if err != nil {
		t.Fatalf("open extension zip: %v", err)
	}
	got := make(map[string]string, len(reader.File))
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", file.Name, err)
		}
		content, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read zip entry %s: %v", file.Name, err)
		}
		got[file.Name] = string(content)
	}
	for _, name := range []string{"manifest.json", "popup.html", "popup.js"} {
		if strings.TrimSpace(got[name]) == "" {
			t.Fatalf("zip missing entry %q, entries=%v", name, keysOf(got))
		}
	}
	if !strings.Contains(got["manifest.json"], `"manifest_version": 3`) {
		t.Fatalf("manifest is not MV3: %s", firstBytes(got["manifest.json"], 200))
	}
	if !strings.Contains(got["popup.js"], "/v0/resource/plugins/gemini-web2api/login/submit") {
		t.Fatalf("popup.js missing submit path")
	}
}

// TestManagementRegistrationIncludesLoginResources 校验登录面路由都已注册。
//
// 资源路由必须先注册，否则宿主 ServeResourceHTTP 查不到会直接 404；
// /login/submit 与 /login/poll 不挂 Menu，因此不进控制台菜单但仍可命中。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestManagementRegistrationIncludesLoginResources(t *testing.T) {
	p := newLoginTestPlugin(t)
	raw, err := p.HandleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("management.register failed: %v", err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal registration: %v", err)
	}

	var reg pluginapi.ManagementRegistrationResponse
	if err := json.Unmarshal(encoded, &reg); err != nil {
		t.Fatalf("unmarshal registration: %v", err)
	}

	byPath := make(map[string]pluginapi.ResourceRoute, len(reg.Resources))
	for _, item := range reg.Resources {
		byPath[item.Path] = item
	}
	// 历史面板路径也必须注册：宿主 ServeResourceHTTP 按完整路径精确匹配，
	// 只留分发分支而不注册路由，线上访问旧收藏链接会直接 404。
	for _, path := range []string{"/panel", "/accounts", "/webui", "/login", "/login/extension.zip", "/login/submit", "/login/poll", "/login/origin"} {
		if _, ok := byPath[path]; !ok {
			t.Fatalf("resource route %q not registered, got %v", path, keysOf(byPath))
		}
	}
	// 控制台只能挂一条菜单：多挂会让左侧插件树出现两个同名子项。
	menuCount := 0
	for path, item := range byPath {
		if strings.TrimSpace(item.Menu) == "" {
			continue
		}
		menuCount++
		if path != "/panel" {
			t.Fatalf("only /panel should declare a console menu, got %q", path)
		}
		if item.Menu != "Gemini Web" {
			t.Fatalf("unexpected console menu name: got %q want %q", item.Menu, "Gemini Web")
		}
	}
	if menuCount != 1 {
		t.Fatalf("expected exactly one console menu, got %d", menuCount)
	}
	for _, path := range []string{"/accounts", "/webui", "/login", "/login/submit", "/login/poll", "/login/extension.zip", "/login/origin"} {
		if strings.TrimSpace(byPath[path].Menu) != "" {
			t.Fatalf("%q should not declare a console menu", path)
		}
	}
}

// TestLoginOriginEndpointRecordsOrigin 校验 origin 上报端点把真实域名写进缓存。
//
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增，覆盖授权链接回落 127.0.0.1 的修复。
func TestLoginOriginEndpointRecordsOrigin(t *testing.T) {
	p := newLoginTestPlugin(t)

	missing := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login/origin?origin=", "", "")
	if missing.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing origin, got %d", missing.StatusCode)
	}

	invalid := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login/origin?origin=cpa.example.com", "", "")
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for relative origin, got %d", invalid.StatusCode)
	}

	ok := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login/origin?origin=https%3A%2F%2Fcpa.example.com", "", "")
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for valid origin, got %d body=%s", ok.StatusCode, string(ok.Body))
	}
	var payload map[string]any
	if err := json.Unmarshal(ok.Body, &payload); err != nil {
		t.Fatalf("unmarshal origin payload: %v", err)
	}
	if payload["origin"] != "https://cpa.example.com" {
		t.Fatalf("origin cache not updated, got %v", payload["origin"])
	}

	// 上报之后，宿主发起登录必须用真实域名而不是容器内的 127.0.0.1:8317。
	t.Setenv("GEMINI_WEB2API_NO_AUTO_BROWSER", "1")
	reqBody, err := json.Marshal(map[string]any{"BaseURL": "http://127.0.0.1:8317/v0/management/oauth-callback"})
	if err != nil {
		t.Fatalf("marshal login start request: %v", err)
	}
	raw, err := p.HandleMethod(pluginabi.MethodAuthLoginStart, reqBody)
	if err != nil {
		t.Fatalf("auth.login.start failed: %v", err)
	}
	startResp, okCast := raw.(pluginapi.AuthLoginStartResponse)
	if !okCast {
		t.Fatalf("unexpected login start response type: %T", raw)
	}
	want := "https://cpa.example.com/v0/resource/plugins/gemini-web2api/login"
	if startResp.URL != want {
		t.Fatalf("login url should use reported origin, got %q want %q", startResp.URL, want)
	}
}

// TestPanelReportsOrigin 校验面板页带着 origin 上报调用。
//
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestPanelReportsOrigin(t *testing.T) {
	p := newLoginTestPlugin(t)
	resp := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/panel", "", "")
	page := string(resp.Body)
	if !strings.Contains(page, "login/origin") {
		t.Fatalf("panel should report its origin for the auth bridge")
	}
	// 用户反馈「这些功能没什么作用」的服务开关必须已经下线。
	for _, gone := range []string{"toggleService", "relaunchGuest", "stopGuest", "loginWithChrome", "chromeBtn"} {
		if strings.Contains(page, gone) {
			t.Fatalf("panel still references removed control %q", gone)
		}
	}
	if !strings.Contains(page, "账号汇总 · 全部账号") {
		t.Fatalf("panel summary should follow the workbuddy wording")
	}
}

// TestAuthLoginStartPointsAtBridge 校验宿主发起登录时返回的是桥接页地址。
//
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestAuthLoginStartPointsAtBridge(t *testing.T) {
	p := newLoginTestPlugin(t)
	reqBody, err := json.Marshal(map[string]any{"BaseURL": "https://cpa.example.com"})
	if err != nil {
		t.Fatalf("marshal login start request: %v", err)
	}
	raw, err := p.HandleMethod(pluginabi.MethodAuthLoginStart, reqBody)
	if err != nil {
		t.Fatalf("auth.login.start failed: %v", err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal login start response: %v", err)
	}

	var resp pluginapi.AuthLoginStartResponse
	if err := json.Unmarshal(encoded, &resp); err != nil {
		t.Fatalf("unmarshal login start response: %v", err)
	}
	want := "https://cpa.example.com/v0/resource/plugins/gemini-web2api/login"
	if resp.URL != want {
		t.Fatalf("unexpected login url: got %q want %q", resp.URL, want)
	}
	if strings.TrimSpace(resp.State) == "" {
		t.Fatalf("login start response missing state")
	}
	pairCode, _ := resp.Metadata["pair_code"].(string)
	if !strings.HasPrefix(pairCode, "cpa|https://cpa.example.com|") {
		t.Fatalf("unexpected pair code: %v", resp.Metadata["pair_code"])
	}
}

// TestManagementWebUIPrefixStillServed 校验面板 HTML 在 /panel 与历史路径上都能取到。
//
// 控制台入口统一到 /panel；/accounts 与 /webui 作为历史收藏保留兼容，
// 四条路径都必须返回面板，且都不能被登录桥接页的逻辑吞掉。
//
// [参数] t 是测试上下文。
// [返回] 无返回值，断言失败时终止当前用例。
// 最近修改时间：2026-11-21；改动原因：面板入口统一到 /panel，补历史路径兼容断言。
func TestManagementWebUIPrefixStillServed(t *testing.T) {
	p := newLoginTestPlugin(t)

	for _, rawPath := range []string{
		"/v0/resource/plugins/gemini-web2api/panel",
		"/v0/resource/plugins/gemini-web2api/accounts",
		"/v0/resource/plugins/gemini-web2api/webui",
		"/v0/resource/plugins/gemini-web2api/",
	} {
		resp := callManagement(t, p, rawPath, "", "")
		page := string(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, "<html") {
			t.Fatalf("%s: expected dashboard html, got %d %s", rawPath, resp.StatusCode, firstBytes(page, 200))
		}
		if strings.Contains(page, "cpa|") {
			t.Fatalf("%s: dashboard should not embed a pair code", rawPath)
		}
		if !strings.Contains(page, "Gemini Web 面板") {
			t.Fatalf("%s: dashboard title missing", rawPath)
		}
	}
}

// firstBytes 截断过长字符串，避免断言失败时刷屏。
//
// [参数] text 是原始文本；limit 是保留的最大字节数。
// [返回] 截断后的文本。
// 最近修改时间：2026-11-21；改动原因：新增。
func firstBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

// keysOf 返回 map 的键列表，用于断言失败时打印实际集合。
//
// [参数] items 是任意键为 string 的 map。
// [返回] 键名切片。
// 最近修改时间：2026-11-21；改动原因：新增。
func keysOf[V any](items map[string]V) []string {
	out := make([]string, 0, len(items))
	for key := range items {
		out = append(out, key)
	}
	return out
}
