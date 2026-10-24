// models_scene_union_test.go 覆盖模型发现的两处上游契约变化：
//
//  1. 多场景并集：上游 model/list 按场景返回（chat / developer / qwork / …），
//     同一账号在不同场景下可见模型不同，只读 chat 会漏掉只存在于其它场景的模型；
//  2. 上游 display_name 权威：上游改模型展示名时内部代号不变，对外主条目必须
//     跟随上游名，且请求要能把新名回译到内部代号，否则会静默落到 auto。
package main

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// resetUpstreamModelNames 清空运行时「官方名 → 内部代号」映射并在用例结束后恢复。
// 该映射是进程级全局状态，用例之间必须隔离，否则先跑的用例会让后跑的断言失真。
func resetUpstreamModelNames(t *testing.T) {
	t.Helper()
	upstreamModelNames.Lock()
	old := upstreamModelNames.byDisplay
	upstreamModelNames.byDisplay = nil
	upstreamModelNames.Unlock()
	t.Cleanup(func() {
		upstreamModelNames.Lock()
		upstreamModelNames.byDisplay = old
		upstreamModelNames.Unlock()
	})
}

// TestParseModelsResponse_UnionsAllScenes 验证解析取全部场景并集而非只读 chat。
// 变异对照：把遍历改回只读 apiResp["chat"] → qwork 独有模型与 enable=false 的
// 非 chat 场景模型都不会出现，本用例 FAIL。
func TestParseModelsResponse_UnionsAllScenes(t *testing.T) {
	resetUpstreamModelNames(t)
	body := []byte(`{
	  "chat":      [{"key":"qfmodel","display_name":"Qwen3.8-Flash","enable":true}],
	  "developer": [{"key":"mmodel","display_name":"MiniMax-M3.1-Flash","enable":true}],
	  "qwork":     [{"key":"kmodel_latest","display_name":"Kimi-K3.1-Pro","enable":true}]
	}`)
	models, err := parseModelsResponse(body)
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	got := make(map[string]string, len(models))
	for _, m := range models {
		got[m.ID] = m.DisplayName
	}
	want := map[string]string{
		"qfmodel":       "Qwen3.8-Flash",
		"mmodel":        "MiniMax-M3.1-Flash",
		"kmodel_latest": "Kimi-K3.1-Pro",
	}
	if len(got) != len(want) {
		t.Fatalf("model count = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for id, display := range want {
		if got[id] != display {
			t.Errorf("model %s display_name = %q, want %q", id, got[id], display)
		}
	}
}

// TestParseModelsResponse_DedupAndSkipsDirtyScenes 验证跨场景去重与非数组场景跳过。
// 上游存在 byok_* 这类空数组场景，也可能出现非数组形态的脏数据；任一场景
// 解析失败都不应让整次发现失败（失败会连带开启负缓存，压住其它账号）。
func TestParseModelsResponse_DedupAndSkipsDirtyScenes(t *testing.T) {
	resetUpstreamModelNames(t)
	body := []byte(`{
	  "chat":      [{"key":"gmodel","display_name":"GLM-5.3","enable":true}],
	  "app":       [{"key":"gmodel","display_name":"GLM-5.3","enable":true}],
	  "byok_teams":[],
	  "broken":    "not-an-array",
	  "mixed":     [{"key":"","enable":true},{"key":"gfmodel","enable":true}]
	}`)
	models, err := parseModelsResponse(body)
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2 (gmodel deduped + gfmodel); got %+v", len(models), models)
	}
	for _, m := range models {
		if m.ID == "" {
			t.Fatalf("empty-key model leaked into result: %+v", m)
		}
	}
}

// TestParseModelsResponse_SkipsDisabled 验证 enable=false 的条目被过滤。
// 官方目录里 Free 档大量模型是 enable=false（Qwen3.7 全系、GLM-5.2 等），
// 放进来会让用户选到必然失败的模型。
func TestParseModelsResponse_SkipsDisabled(t *testing.T) {
	resetUpstreamModelNames(t)
	body := []byte(`{"chat":[
	  {"key":"qmodel_latest","display_name":"Qwen3.7-Max","enable":false},
	  {"key":"qfmodel","display_name":"Qwen3.8-Flash","enable":true}
	]}`)
	models, err := parseModelsResponse(body)
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if len(models) != 1 || models[0].ID != "qfmodel" {
		t.Fatalf("models = %+v, want only qfmodel", models)
	}
}

// TestParseModelsResponse_NoModelsIsError 验证全空响应按失败处理，而不是返回空列表。
// 返回空列表会被上层当成"动态发现成功但无模型"，从而遮蔽配置与静态兜底。
func TestParseModelsResponse_NoModelsIsError(t *testing.T) {
	resetUpstreamModelNames(t)
	for _, body := range []string{`{"chat":[]}`, `{"byok_teams":[]}`, `{}`} {
		if _, err := parseModelsResponse([]byte(body)); err == nil {
			t.Errorf("parseModelsResponse(%s) = nil error, want error", body)
		}
	}
}

// TestExposeModelIDs_PrefersUpstreamDisplayName 是本次修复的核心不变量：
// 上游 display_name 必须压倒硬编码表。上游把 kmodel_latest 改名后，硬编码表
// 仍写着旧名 Kimi-K3；若主条目继续用表名，客户端模型表里就永远看不到新版模型。
func TestExposeModelIDs_PrefersUpstreamDisplayName(t *testing.T) {
	exposed := exposeModelIDs([]pluginapi.ModelInfo{
		{ID: "kmodel_latest", Name: "Kimi-K3", DisplayName: "Kimi-K3.1-Pro"},
	})
	ids := make([]string, 0, len(exposed))
	for _, m := range exposed {
		ids = append(ids, m.ID)
	}
	if len(exposed) != 2 {
		t.Fatalf("expose count = %d (%v), want 2 (main + compat)", len(exposed), ids)
	}
	if exposed[0].ID != "Kimi-K3.1-Pro" || exposed[0].DisplayName != "Kimi-K3.1-Pro" {
		t.Fatalf("main entry = %+v, want upstream name Kimi-K3.1-Pro", exposed[0])
	}
	// 兼容条目仍须保留内部代号，否则旧调用方按代号请求会 400。
	if exposed[1].ID != "kmodel_latest" || exposed[1].Name != "kmodel_latest" {
		t.Fatalf("compat entry = %+v, want kmodel_latest", exposed[1])
	}
}

// TestExposeModelIDs_FallsBackToTableWhenUpstreamSilent 验证上游没给 display_name
// （或给了与代号相同的值）时回落硬编码表，静态兜底与配置来源仍能拿到官方名。
func TestExposeModelIDs_FallsBackToTableWhenUpstreamSilent(t *testing.T) {
	for _, m := range []pluginapi.ModelInfo{
		{ID: "gmodel"},
		{ID: "gmodel", DisplayName: "gmodel"},
	} {
		exposed := exposeModelIDs([]pluginapi.ModelInfo{m})
		if len(exposed) == 0 || exposed[0].ID != "GLM-5.3" {
			t.Fatalf("input %+v -> exposed %+v, want main ID GLM-5.3 from table", m, exposed)
		}
	}
}

// TestCpaToUpstreamKey_ResolvesRenamedModel 验证上游改名后请求能回译到内部代号。
// 变异对照：删除 cpaToUpstreamKey 里的 lookupUpstreamKey 调用 → 新名被透传，
// 上游静默落到 auto（参数与计费全错且无报错），本用例 FAIL。
func TestCpaToUpstreamKey_ResolvesRenamedModel(t *testing.T) {
	resetUpstreamModelNames(t)
	registerUpstreamModelNames([]pluginapi.ModelInfo{
		{ID: "kmodel_latest", Name: "Kimi-K3", DisplayName: "Kimi-K3.1-Pro"},
		{ID: "mmodel", Name: "MiniMax-M3", DisplayName: "MiniMax-M3.1-Flash"},
	})
	cases := map[string]string{
		"Kimi-K3.1-Pro":      "kmodel_latest",
		"kimi-k3.1-pro":      "kmodel_latest", // 大小写不敏感
		"MiniMax-M3.1-Flash": "mmodel",
		"  Kimi-K3.1-Pro  ":  "kmodel_latest", // 首尾空白
	}
	for input, want := range cases {
		if got := cpaToUpstreamKey(input); got != want {
			t.Errorf("cpaToUpstreamKey(%q) = %q, want %q", input, got, want)
		}
		if got := resolveUpstreamModel(input, nil); got != want {
			t.Errorf("resolveUpstreamModel(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestCpaToUpstreamKey_RuntimeNameBeatsStaticTable 验证运行时上游名优先于静态表。
// 上游改名后静态表仍是旧名；若静态表抢先命中，新名就永远回译不到正确代号。
func TestCpaToUpstreamKey_RuntimeNameBeatsStaticTable(t *testing.T) {
	resetUpstreamModelNames(t)
	registerUpstreamModelNames([]pluginapi.ModelInfo{
		{ID: "gmodel", DisplayName: "GLM-5.4"},
	})
	if got := cpaToUpstreamKey("GLM-5.4"); got != "gmodel" {
		t.Fatalf("cpaToUpstreamKey(GLM-5.4) = %q, want gmodel", got)
	}
	// 旧名仍须可用：存量客户端还在按旧名请求。
	if got := cpaToUpstreamKey("GLM-5.3"); got != "gmodel" {
		t.Fatalf("cpaToUpstreamKey(GLM-5.3) = %q, want gmodel", got)
	}
}

// TestRegisterUpstreamModelNames_FirstWins 验证映射登记是先到先得。
// 不同账号的清单可能不同（旧账号缓存仍带旧名），后到的旧名不得覆盖已登记的
// 新名，否则同一次升级会被较晚返回的旧清单打回原形。
func TestRegisterUpstreamModelNames_FirstWins(t *testing.T) {
	resetUpstreamModelNames(t)
	registerUpstreamModelNames([]pluginapi.ModelInfo{
		{ID: "kmodel_latest", DisplayName: "Kimi-K3.1-Pro"},
	})
	registerUpstreamModelNames([]pluginapi.ModelInfo{
		{ID: "kmodel_latest", DisplayName: "Kimi-K3"},
	})
	if got := cpaToUpstreamKey("Kimi-K3.1-Pro"); got != "kmodel_latest" {
		t.Fatalf("cpaToUpstreamKey(Kimi-K3.1-Pro) = %q, want kmodel_latest", got)
	}
	if got := cpaToUpstreamKey("Kimi-K3"); got != "kmodel_latest" {
		t.Fatalf("late legacy name should still map via static table, got %q", got)
	}
}

// TestStaticFallbackCoveredByDynamicUnion 验证生产实测口径：
// 静态兜底列出的每个代号都必须能被上游官方名回译，且不含当前档位不可用的模型。
// 该用例把「静态表与上游对齐」这件事锁成不变量，避免下次又漂移回旧清单。
func TestStaticFallbackCoveredByDynamicUnion(t *testing.T) {
	resetUpstreamModelNames(t)
	upstreamAvailable := []string{
		"auto", "qmodel_38max", "qfmodel", "gmodel", "gfmodel",
		"kmodel_latest", "kmodel", "dmodel", "dfmodel", "mmodel",
	}
	staticIDs := make(map[string]struct{}, len(upstreamAvailable))
	for _, m := range wbModels() {
		staticIDs[m.ID] = struct{}{}
	}
	for _, key := range upstreamAvailable {
		if _, ok := staticIDs[key]; !ok {
			t.Errorf("static fallback missing upstream-available model: %s", key)
		}
	}
	if len(staticIDs) != len(upstreamAvailable) {
		t.Errorf("static fallback size = %d, want %d (%v)", len(staticIDs), len(upstreamAvailable), staticIDs)
	}
}

// TestExposeModelIDs_AllStaticEntriesRoundTrip 验证静态兜底的每个对外 ID
// 都能回译到自己的内部代号。漏一条就会让客户端选中的模型静默落到 auto。
func TestExposeModelIDs_AllStaticEntriesRoundTrip(t *testing.T) {
	resetUpstreamModelNames(t)
	for _, m := range exposeModelIDs(wbModels()) {
		got := cpaToUpstreamKey(stripProviderPrefix(m.ID))
		if got != m.Name {
			t.Errorf("cpaToUpstreamKey(%q) = %q, want internal key %q", m.ID, got, m.Name)
		}
	}
}

// TestSceneUnion_DoesNotLeakIntoOtherProviders 确认多场景并集只在本插件内生效：
// 并集结果仍是内部代号，对外命名与回译口径与单场景路径完全一致。
func TestSceneUnion_DoesNotLeakIntoOtherProviders(t *testing.T) {
	resetUpstreamModelNames(t)
	body := []byte(fmt.Sprintf(`{
	  "chat":  [{"key":"qfmodel","display_name":"Qwen3.8-Flash","enable":true}],
	  "quest": [{"key":"fast","display_name":"Fast","enable":true}]
	}`))
	models, err := parseModelsResponse(body)
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	for _, m := range models {
		if m.OwnedBy != providerName {
			t.Errorf("model %s OwnedBy = %q, want %q", m.ID, m.OwnedBy, providerName)
		}
	}
	exposed := exposeModelIDs(models)
	for _, m := range exposed {
		if got := cpaToUpstreamKey(stripProviderPrefix(m.ID)); got != m.Name {
			t.Errorf("cpaToUpstreamKey(%q) = %q, want %q", m.ID, got, m.Name)
		}
	}
}
