// models_scene_union_test.go 覆盖模型发现的三处上游契约变化（qoder-ai 国际版）：
//
//  1. 多场景并集：上游 model/list 按场景返回（chat / assistant / quest / qwake /
//     app / inline / qwork / experts / nap / byok_*），同一账号在不同场景下可见
//     模型不同，只读 chat 会漏掉只存在于其它场景的模型（真实上游实测漏 9 个 key）；
//  2. 场景内嵌档位别名必须排除：quest-auto / qwork-advanced / experts-ultimate /
//     nap-auto 等只在该场景内有效，display_name 是 Auto/Standard/Premium/Advanced，
//     不是独立模型，并入对外清单会多出无法路由到具体型号的伪模型；
//  3. 上游 display_name 权威：上游改模型展示名时内部代号不变，对外主条目必须
//     跟随上游名，且请求要能把新名回译到内部代号，否则会静默落到 auto。
package main

import (
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
// 变异对照：把遍历改回只读 apiResp["chat"] → performance / inline 独有模型都不会出现，本用例 FAIL。
func TestParseModelsResponse_UnionsAllScenes(t *testing.T) {
	resetUpstreamModelNames(t)
	body := []byte(`{
	  "chat":        [{"key":"qfmodel","display_name":"Qwen3.8-Flash","enable":true}],
	  "assistant":   [{"key":"mmodel","display_name":"MiniMax-M3.1-Flash","enable":true}],
	  "qwake":       [{"key":"performance","display_name":"Performance","enable":true}],
	  "inline":      [{"key":"cmodel","display_name":"Cantus","enable":true}]
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
		"qfmodel":     "Qwen3.8-Flash",
		"mmodel":      "MiniMax-M3.1-Flash",
		"performance": "Performance",
		"cmodel":      "Cantus",
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

// TestParseModelsResponse_SkipsSceneScopedAliases 验证场景内嵌档位别名被排除。
// 变异对照：删除 isSceneScopedAlias 调用 → 8 个伪模型全部混入，本用例 FAIL。
func TestParseModelsResponse_SkipsSceneScopedAliases(t *testing.T) {
	resetUpstreamModelNames(t)
	body := []byte(`{
	  "chat":    [{"key":"gmodel","display_name":"GLM-5.3","enable":true}],
	  "quest":   [{"key":"gmodel","enable":true},
	              {"key":"quest-auto","display_name":"Auto","enable":true},
	              {"key":"quest-ultimate","display_name":"Ultimate","enable":true}],
	  "qwork":   [{"key":"qwork-auto","display_name":"Standard","enable":true},
	              {"key":"qwork-advanced","display_name":"Advanced","enable":true},
	              {"key":"qwork-ultimate","display_name":"Premium","enable":true}],
	  "experts": [{"key":"experts-auto","display_name":"Auto","enable":true},
	              {"key":"experts-ultimate","display_name":"Ultimate","enable":true}],
	  "nap":     [{"key":"nap-auto","display_name":"Auto","enable":true}]
	}`)
	models, err := parseModelsResponse(body)
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gmodel" {
		t.Fatalf("models = %+v, want only gmodel (all scene-scoped aliases skipped)", models)
	}
}

// TestIsSceneScopedAlias 直接锁死前缀判定的边界：大小写、前后空格、非别名不受影响。
func TestIsSceneScopedAlias(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"quest-auto", true},
		{"quest-ultimate", true},
		{"qwork-auto", true},
		{"qwork-advanced", true},
		{"experts-ultimate", true},
		{"nap-auto", true},
		{" QUEST-AUTO ", true},
		{"auto", false},
		{"performance", false},
		{"qmodel_38max", false},
		{"qwork", false},
		{"quest", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isSceneScopedAlias(tc.key); got != tc.want {
			t.Errorf("isSceneScopedAlias(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// TestParseModelsResponse_DedupAndSkipsDirtyScenes 验证跨场景去重与非数组场景跳过。
// 上游存在 byok_* 这类空数组场景，也可能出现非数组形态的脏数据；任一场景
// 解析失败都不应让整次发现失败（失败会连带开启负缓存，压住其它账号）。
func TestParseModelsResponse_DedupAndSkipsDirtyScenes(t *testing.T) {
	resetUpstreamModelNames(t)
	body := []byte(`{
	  "chat":            [{"key":"gmodel","display_name":"GLM-5.3","enable":true}],
	  "app":             [{"key":"gmodel","display_name":"GLM-5.3","enable":true}],
	  "byok_teams":      [],
	  "byok_enterprise": [],
	  "broken":          "not-an-array",
	  "mixed":           [{"key":"","enable":true},{"key":"gfmodel","enable":true}]
	}`)
	models, err := parseModelsResponse(body)
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2 (deduped gmodel + gfmodel)", len(models))
	}
	if models[0].ID != "gmodel" || models[1].ID != "gfmodel" {
		t.Fatalf("ids = %q/%q, want gmodel/gfmodel", models[0].ID, models[1].ID)
	}
}

// TestParseModelsResponse_SkipsDisabled 验证 enable=false 的模型不进入清单。
func TestParseModelsResponse_SkipsDisabled(t *testing.T) {
	resetUpstreamModelNames(t)
	body := []byte(`{"chat":[
	  {"key":"disabled-model","display_name":"Disabled","enable":false},
	  {"key":"enabled-model","display_name":"Enabled","enable":true}
	]}`)
	models, err := parseModelsResponse(body)
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if len(models) != 1 || models[0].ID != "enabled-model" {
		t.Fatalf("models = %+v, want only enabled-model", models)
	}
}

// TestParseModelsResponse_NoModelsIsError 验证所有场景都没有可用模型时返回错误，
// 让调用方按优先级链回退静态兜底，而不是把空列表当成"上游真的没有模型"。
func TestParseModelsResponse_NoModelsIsError(t *testing.T) {
	resetUpstreamModelNames(t)
	if _, err := parseModelsResponse([]byte(`{"chat":[],"app":[]}`)); err == nil {
		t.Fatal("parseModelsResponse with no models = nil error, want error")
	}
}

// TestParseModelsResponse_RegistersUpstreamNames 验证解析结果会登记「官方名→内部代号」，
// 供请求回译使用。别名（名字与代号相同）不登记，避免污染映射。
func TestParseModelsResponse_RegistersUpstreamNames(t *testing.T) {
	resetUpstreamModelNames(t)
	body := []byte(`{"chat":[
	  {"key":"kmodel_latest","display_name":"Kimi-K3.1-Pro","enable":true},
	  {"key":"auto","display_name":"auto","enable":true}
	]}`)
	if _, err := parseModelsResponse(body); err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if key, ok := lookupUpstreamKey("Kimi-K3.1-Pro"); !ok || key != "kmodel_latest" {
		t.Fatalf("lookupUpstreamKey(Kimi-K3.1-Pro) = %q/%v, want kmodel_latest/true", key, ok)
	}
	if _, ok := lookupUpstreamKey("auto"); ok {
		t.Fatal("name equal to key must not be registered")
	}
}

// TestExposeModelIDs_PrefersUpstreamDisplayName 验证主条目跟随上游改名。
// 变异对照：改回只用 internalKeyDisplayNames → 主条目仍是旧名 Kimi-K3，本用例 FAIL。
func TestExposeModelIDs_PrefersUpstreamDisplayName(t *testing.T) {
	resetUpstreamModelNames(t)
	got := exposeModelIDs([]pluginapi.ModelInfo{{ID: "kmodel_latest", DisplayName: "Kimi-K3.1-Pro"}})
	if len(got) != 2 {
		t.Fatalf("expose count = %d, want 2", len(got))
	}
	if got[0].ID != "Kimi-K3.1-Pro" || got[0].DisplayName != "Kimi-K3.1-Pro" {
		t.Fatalf("main entry = %+v, want upstream display name Kimi-K3.1-Pro", got[0])
	}
	if got[0].Name != "kmodel_latest" || got[1].ID != "kmodel_latest" {
		t.Fatalf("internal key lost: main.Name=%q compat.ID=%q", got[0].Name, got[1].ID)
	}
}

// TestExposeModelIDs_FallsBackToTableWhenUpstreamSilent 验证上游没给 display_name 时
// 回落到硬编码表，保证静态/配置来源与动态来源的命名口径一致。
func TestExposeModelIDs_FallsBackToTableWhenUpstreamSilent(t *testing.T) {
	resetUpstreamModelNames(t)
	got := exposeModelIDs([]pluginapi.ModelInfo{{ID: "qfmodel"}})
	if len(got) != 2 || got[0].ID != "Qwen3.8-Flash" {
		t.Fatalf("entries = %+v, want main ID Qwen3.8-Flash from table", got)
	}
}

// TestCpaToUpstreamKey_ResolvesRenamedModel 是本文件的核心不变量：
// 上游改名后客户端按新名请求，必须回译到原内部代号，否则静默落到 auto。
func TestCpaToUpstreamKey_ResolvesRenamedModel(t *testing.T) {
	resetUpstreamModelNames(t)
	registerUpstreamModelNames([]pluginapi.ModelInfo{
		{ID: "kmodel_latest", DisplayName: "Kimi-K3.1-Pro"},
		{ID: "mmodel", DisplayName: "MiniMax-M3.1-Flash"},
	})
	cases := []struct{ in, want string }{
		{"Kimi-K3.1-Pro", "kmodel_latest"},
		{"kimi-k3.1-pro", "kmodel_latest"},
		{"  Kimi-K3.1-Pro  ", "kmodel_latest"},
		{"MiniMax-M3.1-Flash", "mmodel"},
		{"Kimi-K3", "kmodel_latest"},
	}
	for _, tc := range cases {
		if got := cpaToUpstreamKey(tc.in); got != tc.want {
			t.Errorf("cpaToUpstreamKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolveUpstreamModel_RuntimeNameBeatsStaticTable 验证运行时映射的优先级高于
// 静态别名表：静态表只有旧名，若运行时名被静态表遮住，改名后的请求就回译不了。
func TestResolveUpstreamModel_RuntimeNameBeatsStaticTable(t *testing.T) {
	resetUpstreamModelNames(t)
	registerUpstreamModelNames([]pluginapi.ModelInfo{{ID: "kmodel_latest", DisplayName: "Kimi-K3.1-Pro"}})
	if got := resolveUpstreamModel("Kimi-K3.1-Pro", nil); got != "kmodel_latest" {
		t.Fatalf("resolveUpstreamModel = %q, want kmodel_latest", got)
	}
	// 账号级 alias 必须压倒运行时映射（用户显式配置优先）。
	attrs := map[string]string{"model_alias": `[{"name":"kmodel","alias":"Kimi-K3.1-Pro"}]`}
	if got := resolveUpstreamModel("Kimi-K3.1-Pro", attrs); got != "kmodel" {
		t.Fatalf("per-auth alias must win: got %q, want kmodel", got)
	}
}

// TestRegisterUpstreamModelNames_FirstWins 验证「先到先得」：后到的旧清单
// 不得覆盖先登记的映射，否则新旧账号缓存并存时回译会随发现顺序抖动。
func TestRegisterUpstreamModelNames_FirstWins(t *testing.T) {
	resetUpstreamModelNames(t)
	registerUpstreamModelNames([]pluginapi.ModelInfo{{ID: "kmodel_latest", DisplayName: "Kimi-K3.1-Pro"}})
	registerUpstreamModelNames([]pluginapi.ModelInfo{{ID: "kmodel_latest", DisplayName: "Kimi-K3"}})
	if got := cpaToUpstreamKey("Kimi-K3.1-Pro"); got != "kmodel_latest" {
		t.Fatalf("first registration lost: cpaToUpstreamKey = %q", got)
	}
}

// TestExposeModelIDs_AllStaticEntriesRoundTrip 保证静态兜底清单里每一条
// 对外 ID 都能回译，漏一条就会让客户端选中的模型静默落到 auto。
func TestExposeModelIDs_AllStaticEntriesRoundTrip(t *testing.T) {
	resetUpstreamModelNames(t)
	exposed := exposeModelIDs(wbModels())
	if len(exposed) == 0 {
		t.Fatal("no static models exposed")
	}
	for _, m := range exposed {
		if got := cpaToUpstreamKey(stripProviderPrefix(m.ID)); got != m.Name {
			t.Errorf("cpaToUpstreamKey(%q) = %q, want internal key %q", m.ID, got, m.Name)
		}
	}
}

// TestStaticFallbackCoveredByDynamicUnion 验证静态兜底清单是上游可用集合的子集，
// 避免静态表继续挂着上游已经没有的代号（会把不存在的模型暴露给客户端）。
func TestStaticFallbackCoveredByDynamicUnion(t *testing.T) {
	resetUpstreamModelNames(t)
	// 2026-10-25 真实国际版上游并集（已排除场景内嵌档位别名）的精简代表样本。
	upstream := []string{
		"auto", "ultimate", "performance", "efficient", "fast",
		"cmodel", "smodel",
		"qmodel_38max", "qfmodel", "qmodel_latest", "qmodel",
		"gmodel", "gfmodel", "kmodel_latest", "kmodel",
		"dmodel", "dfmodel", "mmodel",
	}
	available := make(map[string]struct{}, len(upstream))
	for _, k := range upstream {
		available[k] = struct{}{}
	}
	for _, m := range wbModels() {
		if _, ok := available[m.ID]; !ok {
			t.Errorf("static fallback %q is not in the live upstream union", m.ID)
		}
	}
}

// TestSceneUnion_NameEqualsKeyNotRegistered 验证「官方名与内部代号只差大小写」时
// 不登记运行时映射：这类代号本来就能被静态开关命中，登记只会让映射表里塞满
// 「performance → performance」这种恒等项，掩盖真正的改名映射。
func TestSceneUnion_NameEqualsKeyNotRegistered(t *testing.T) {
	resetUpstreamModelNames(t)
	models, err := parseModelsResponse([]byte(`{"qwake":[{"key":"performance","display_name":"Performance","enable":true}]}`))
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if len(models) != 1 || models[0].OwnedBy != providerName {
		t.Fatalf("models = %+v, want one owned_by=%s entry", models, providerName)
	}
	if _, ok := lookupUpstreamKey("Performance"); ok {
		t.Fatal("case-only difference must not be registered as a rename mapping")
	}
	if got := cpaToUpstreamKey("Performance"); got != "performance" {
		t.Fatalf("cpaToUpstreamKey(Performance) = %q, want performance", got)
	}
}
