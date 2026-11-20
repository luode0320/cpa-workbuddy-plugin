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
	for _, path := range []string{"/accounts", "/login", "/login/extension.zip", "/login/submit", "/login/poll"} {
		if _, ok := byPath[path]; !ok {
			t.Fatalf("resource route %q not registered, got %v", path, keysOf(byPath))
		}
	}
	if strings.TrimSpace(byPath["/login"].Menu) == "" {
		t.Fatalf("/login should keep a console menu")
	}
	for _, path := range []string{"/login/submit", "/login/poll", "/login/extension.zip"} {
		if strings.TrimSpace(byPath[path].Menu) != "" {
			t.Fatalf("%q should not declare a console menu", path)
		}
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

// TestManagementWebUIPrefixStillServed 校验面板前缀仍返回面板而不是登录页。
//
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestManagementWebUIPrefixStillServed(t *testing.T) {
	p := newLoginTestPlugin(t)
	resp := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/accounts", "", "")

	page := string(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "<html") {
		t.Fatalf("expected dashboard html, got %d %s", resp.StatusCode, firstBytes(page, 200))
	}
	if strings.Contains(page, "cpa|") {
		t.Fatalf("dashboard should not embed a pair code")
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
