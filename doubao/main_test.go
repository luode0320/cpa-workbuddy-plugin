package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestDoubaoRegistrationIdentity 校验插件注册元信息的身份字段。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定插件 id 与能力声明，防止改名漂移导致宿主加载失败。
func TestDoubaoRegistrationIdentity(t *testing.T) {
	reg := doubaoRegistration()

	if reg.Metadata.Name != "doubao-provider" {
		t.Fatalf("插件名应为 doubao-provider，实际为 %q", reg.Metadata.Name)
	}
	if reg.Metadata.Version == "" {
		t.Fatal("插件版本不能为空")
	}
	if reg.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema 版本应为 %d，实际为 %d", pluginabi.SchemaVersion, reg.SchemaVersion)
	}

	caps := reg.Capabilities
	if !caps.ModelProvider || !caps.AuthProvider || !caps.Executor || !caps.ManagementAPI {
		t.Fatalf("核心能力声明缺失：%+v", caps)
	}
	if !caps.Scheduler {
		t.Fatal("调度能力应声明为 true")
	}
	if len(caps.ExecutorInputFormats) == 0 || caps.ExecutorInputFormats[0] != "openai" {
		t.Fatalf("执行器输入格式应为 openai，实际为 %v", caps.ExecutorInputFormats)
	}
}

// TestManagementRegistrationRoutes 校验管理路由注册表的关键路径。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定面板与账号路由，防止路径漂移导致面板 404。
func TestManagementRegistrationRoutes(t *testing.T) {
	reg := managementRegistration()

	var hasAccounts, hasPanel bool
	for _, r := range reg.Routes {
		if r.Path == "/plugins/doubao-provider/accounts" && r.Method == http.MethodGet {
			hasAccounts = true
		}
	}
	for _, res := range reg.Resources {
		if res.Path == "/panel" {
			hasPanel = true
		}
	}
	if !hasAccounts {
		t.Error("缺少 GET /plugins/doubao-provider/accounts 路由")
	}
	if !hasPanel {
		t.Error("缺少 /panel 资源页注册")
	}
}

// TestNormalizeMgmtSubPath 校验三种宿主前缀形态的归一化结果。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，覆盖 management 前缀、resource 前缀与裸路径。
func TestNormalizeMgmtSubPath(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"management 前缀", "/v0/management/plugins/doubao-provider/accounts", "/accounts"},
		{"短前缀", "/plugins/doubao-provider/accounts", "/accounts"},
		{"resource 前缀", "/v0/resource/plugins/doubao-provider/panel", "/panel"},
		{"resource 空前缀", "/v0/resource/plugins/doubao-provider", "/panel"},
		{"带查询串", "/v0/management/plugins/doubao-provider/accounts?x=1", "/accounts"},
	}
	for _, c := range cases {
		got, _ := normalizeMgmtSubPath(c.raw)
		if got != c.want {
			t.Errorf("%s：normalizeMgmtSubPath(%q) = %q，期望 %q", c.name, c.raw, got, c.want)
		}
	}
}

// decodeMgmtBody 解析管理响应信封并 base64 解码内层响应体。
// [参数] t: 测试上下文；raw: handleManagement 返回的信封 JSON。
// [返回] 内层响应体字符串。
// 最近修改时间：2026-10-17；改动原因：新增，pluginapi.ManagementResponse.Body 为 []byte，信封内以 base64 传输。
func decodeMgmtBody(t *testing.T, raw []byte) string {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			StatusCode int    `json:"StatusCode"`
			Body       []byte `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解析管理信封失败：%v", err)
	}
	return string(env.Result.Body)
}

// TestHandleManagementPanelRoute 校验面板路由返回 HTML 内容类型。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，确认面板页出口返回 text/html。
func TestHandleManagementPanelRoute(t *testing.T) {
	req := pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/doubao-provider/panel",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}

	out, err := handleManagement(raw)
	if err != nil {
		t.Fatalf("handleManagement 返回错误：%v", err)
	}

	if !strings.Contains(string(out), "text/html") {
		t.Errorf("面板响应应声明 text/html，实际响应：%s", truncateForTest(string(out)))
	}
}

// TestHandleManagementAccountsRoute 校验账号列表路由返回空列表结构。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，骨架期确认账号接口契约形状。
func TestHandleManagementAccountsRoute(t *testing.T) {
	req := pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/doubao-provider/accounts",
	}
	raw, _ := json.Marshal(req)

	out, err := handleManagement(raw)
	if err != nil {
		t.Fatalf("handleManagement 返回错误：%v", err)
	}
	if body := decodeMgmtBody(t, out); !strings.Contains(body, `"accounts"`) {
		t.Errorf("账号响应应含 accounts 字段，实际响应体：%s", truncateForTest(body))
	}
}

// TestHandleManagementUnknownRoute 校验未知路由返回 not_found。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，确认兜底分支不误吞未知路径。
func TestHandleManagementUnknownRoute(t *testing.T) {
	req := pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/doubao-provider/not-exist",
	}
	raw, _ := json.Marshal(req)

	out, err := handleManagement(raw)
	if err != nil {
		t.Fatalf("handleManagement 返回错误：%v", err)
	}
	if body := decodeMgmtBody(t, out); !strings.Contains(body, "not_found") {
		t.Errorf("未知路由应返回 not_found，实际响应体：%s", truncateForTest(body))
	}
}

// TestHandleMethodUnknown 校验未知宿主方法返回 unknown_method。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，确认方法分发表兜底。
func TestHandleMethodUnknown(t *testing.T) {
	out, err := handleMethod("no.such.method", nil)
	if err != nil {
		t.Fatalf("handleMethod 返回错误：%v", err)
	}
	if !strings.Contains(string(out), "unknown_method") {
		t.Errorf("未知方法应返回 unknown_method，实际响应：%s", truncateForTest(string(out)))
	}
}

// TestConfigurePluginDefaults 校验空配置下回退默认值。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，确认配置解析不因空载荷崩溃。
func TestConfigurePluginDefaults(t *testing.T) {
	if err := configurePlugin(nil); err != nil {
		t.Fatalf("空配置解析不应报错：%v", err)
	}
	cfg := currentConfig()
	if cfg.TimeoutSeconds <= 0 || cfg.StreamTimeoutSeconds <= 0 || cfg.ModelCacheSecs <= 0 {
		t.Fatalf("默认配置应全部为正数，实际：%+v", cfg)
	}
}

// truncateForTest 截断测试输出，避免断言失败时打印超长响应。
// [参数] s: 原始字符串。
// [返回] 截断后的字符串。
// 最近修改时间：2026-10-17；改动原因：新增，测试辅助。
func truncateForTest(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "..."
}
