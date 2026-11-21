package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件覆盖浏览器保活的管理入口（TASK-08）。
//
// 只锁定「开关未开时入口怎么回话」「状态接口报什么」「路由是否登记」：真实起浏览器
// 需要镜像里有 Chrome，还会真的打上游，不属于单测范围。这几条保证的是容器里
// 最常见的那种状态——没装浏览器、开关没开——面板点下去能拿到一句可读的原因，
// 而不是一个裸 500 或者永远转圈。

// decodeMgmtEnvelope 拆管理响应信封，返回 HTTP 状态码与解码后的载荷。
//
// 载荷是 base64：ManagementResponse.Body 是 []byte，信封序列化后必然被编成
// base64 字符串，直接当 JSON 解会失败。
// [参数] t: 测试上下文；raw: 管理响应信封字节。
// [返回] HTTP 状态码与载荷对象。
// 最近修改时间：2026-11-21；改动原因：新增，管理入口断言共用。
func decodeMgmtEnvelope(t *testing.T, raw []byte) (int, map[string]any) {
	t.Helper()
	var env map[string]any
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("信封解码失败：%v（原文 %s）", errUnmarshal, string(raw))
	}
	if ok, _ := env["ok"].(bool); !ok {
		t.Fatalf("信封 ok 应为 true：%s", string(raw))
	}
	result, _ := env["result"].(map[string]any)
	if result == nil {
		t.Fatalf("信封缺少 result：%s", string(raw))
	}
	status := 0
	if v, ok := result["StatusCode"].(float64); ok {
		status = int(v)
	}
	bodyText, _ := result["Body"].(string)
	if strings.TrimSpace(bodyText) == "" {
		return status, nil
	}
	body, errDecode := base64.StdEncoding.DecodeString(bodyText)
	if errDecode != nil {
		t.Fatalf("载荷不是合法 base64：%v（%s）", errDecode, bodyText)
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		t.Fatalf("载荷解码失败：%v（原文 %s）", errUnmarshal, string(body))
	}
	return status, payload
}

// stubBrowserChromePath 临时改写配置里的浏览器路径。
//
// 与 stubBrowserConfig 分开：那个函数固定指向一个不存在的路径，这里要能
// 指向一个真实存在的文件，用来验证「探测到浏览器」的分支。
// [参数] t: 测试上下文；path: 目标路径。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，路径探测分支用例。
func stubBrowserChromePath(t *testing.T, path string) {
	t.Helper()
	prev, _ := activeConfig.Load().(Config)
	next := currentConfig()
	next.BrowserKeepChromePath = path
	activeConfig.Store(next)
	t.Cleanup(func() {
		if prev.TimeoutSeconds == 0 {
			activeConfig.Store(defaultConfig())
			return
		}
		activeConfig.Store(prev)
	})
}

// newManagedAuth 在临时账号目录里造一个可被 findAuthByID 命中的账号。
//
// 走 setAccountsDir 而不是伪造 listAllAuthFiles：入口函数本身就是
// 「查账号 → 起浏览器」两段，账号查找必须走真实路径才算覆盖到。
// [参数] t: 测试上下文。
// [返回] 账号标识。
// 最近修改时间：2026-11-21；改动原因：新增，入口用例的共享前置。
func newManagedAuth(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevDir := getAccountsDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir(prevDir) })

	seed := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       "coze-managed",
		CookieHeader: "sessionid=origin",
	}
	path := filepath.Join(dir, cozeAuthFilePrefix+"managed.json")
	if errWrite := writeAuthFileDirect(path, seed); errWrite != nil {
		t.Fatalf("准备账号失败：%v", errWrite)
	}
	return seed.AuthID
}

// TestHandleBrowserRevive_RequiresAuthID 验证缺少账号标识时回 400。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，入口参数校验。
func TestHandleBrowserRevive_RequiresAuthID(t *testing.T) {
	raw, errHandle := handleBrowserRevive([]byte("{}"))
	if errHandle != nil {
		t.Fatalf("不应返回 error：%v", errHandle)
	}
	status, _ := decodeMgmtEnvelope(t, raw)
	if status != 400 {
		t.Fatalf("缺 auth_id 应回 400，实际 %d", status)
	}
}

// TestHandleBrowserRevive_UnknownAccount 验证账号不存在时给出明确原因。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，查找失败分支。
func TestHandleBrowserRevive_UnknownAccount(t *testing.T) {
	setAccountsDir(t.TempDir())
	t.Cleanup(func() { setAccountsDir("") })

	body, _ := json.Marshal(map[string]any{"auth_id": "not-exist"})
	raw, errHandle := handleBrowserRevive(body)
	if errHandle != nil {
		t.Fatalf("不应返回 error：%v", errHandle)
	}
	_, payload := decodeMgmtEnvelope(t, raw)
	if payload["ok"] == true {
		t.Fatal("账号不存在时不得报成功")
	}
	msg, _ := payload["error"].(string)
	if !strings.Contains(msg, "账号不存在") {
		t.Fatalf("应说明账号不存在，实际 %q", msg)
	}
}

// TestHandleBrowserRevive_ReportsDisabledSwitch 验证开关未开时给出可读原因。
//
// 这是最常走的分支：精简容器里既没浏览器也没开开关，面板点一下必须看到
// 一句人话，而不是一个内部错误码。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，最常见路径的可用性。
func TestHandleBrowserRevive_ReportsDisabledSwitch(t *testing.T) {
	stubBrowserConfig(t, false)
	authID := newManagedAuth(t)

	body, _ := json.Marshal(map[string]any{"auth_id": authID})
	raw, errHandle := handleBrowserRevive(body)
	if errHandle != nil {
		t.Fatalf("不应返回 error：%v", errHandle)
	}
	_, payload := decodeMgmtEnvelope(t, raw)
	if payload["ok"] == true {
		t.Fatal("开关未开时不得报成功")
	}
	msg, _ := payload["error"].(string)
	if !strings.Contains(msg, "浏览器保活未启用") {
		t.Fatalf("应提示开关未开，实际 %q", msg)
	}
}

// TestHandleBrowserStatus_ReportsUnavailableBinary 验证没有浏览器时状态可读。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，运维排障入口。
func TestHandleBrowserStatus_ReportsUnavailableBinary(t *testing.T) {
	stubBrowserConfig(t, true)

	payload := handleBrowserStatus()
	if payload["enabled"] != true {
		t.Fatalf("应回报开关已开，实际 %v", payload["enabled"])
	}
	if payload["available"] != false {
		t.Fatalf("桩配置指向不存在的路径，应报不可用，实际 %v", payload["available"])
	}
	msg, _ := payload["error"].(string)
	if !strings.Contains(msg, "chrome_path") {
		t.Fatalf("应指出配置的路径不可用，实际 %q", msg)
	}
}

// TestHandleBrowserStatus_ReportsResolvedPath 验证探测到浏览器时回报路径。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，确认探测成功分支。
func TestHandleBrowserStatus_ReportsResolvedPath(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "chrome")
	if errWrite := os.WriteFile(bin, []byte("stub"), 0755); errWrite != nil {
		t.Fatalf("准备假浏览器失败：%v", errWrite)
	}
	stubBrowserConfig(t, true)
	stubBrowserChromePath(t, bin)

	payload := handleBrowserStatus()
	if payload["available"] != true {
		t.Fatalf("应探测到浏览器，实际 %v（%v）", payload["available"], payload["error"])
	}
	if payload["resolved_path"] != bin {
		t.Fatalf("应回报解析到的路径，实际 %v", payload["resolved_path"])
	}
}

// TestManagementRegistration_IncludesBrowserRoutes 验证路由表登记了保活入口。
//
// 路由漏注册的表现是面板 404，而 404 在面板上只显示成一次空响应，
// 很难从现象反推原因，所以这里直接把两条路径钉住。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，防止路由漏注册。
func TestManagementRegistration_IncludesBrowserRoutes(t *testing.T) {
	reg := managementRegistration()
	found := map[string]bool{}
	for _, route := range reg.Routes {
		found[route.Method+" "+route.Path] = true
	}
	want := []string{
		"POST /plugins/" + providerName + "/browser-revive",
		"GET /plugins/" + providerName + "/browser-status",
	}
	for _, key := range want {
		if !found[key] {
			t.Fatalf("路由未注册：%s（现有 %v）", key, found)
		}
	}
}

// TestFindAuthByID_StripsJSONSuffix 验证带 .json 后缀也能命中。
//
// 面板有时传文件名形态的标识，宿主回传的是裸标识，两种都得认。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，标识形态兼容。
func TestFindAuthByID_StripsJSONSuffix(t *testing.T) {
	authID := newManagedAuth(t)

	sa, errFind := findAuthByID(authID + ".json")
	if errFind != nil {
		t.Fatalf("带后缀应能命中：%v", errFind)
	}
	if sa.AuthID != authID {
		t.Fatalf("应命中 %q，实际 %q", authID, sa.AuthID)
	}
}
