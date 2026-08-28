// models_config_test.go 覆盖 config_yaml `models:` 覆盖机制：
// 解析（字符串/对象、enabled 过滤、空列表清空）、configure() 接线、
// 以及配置存在时 model.static / model.for_auth 优先返回配置列表。
package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// configYAMLForTest 组装 configure() 需要的 raw：config_yaml 经 host RPC
// 传输时是 []byte，测试按 json.Marshal(map{"config_yaml": []byte(yaml)}) 构造。
func configYAMLForTest(t *testing.T, yaml string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"config_yaml": []byte(yaml)})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return raw
}

// resetConfiguredModels 清空全局覆盖列表并在用例结束后恢复。
func resetConfiguredModels(t *testing.T) {
	t.Helper()
	clearConfiguredModels()
	t.Cleanup(clearConfiguredModels)
}

// assertModelIDs 按顺序断言模型列表的 ID 集合。
func assertModelIDs(t *testing.T, got []pluginapi.ModelInfo, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("model count = %d, want %d (%v); got %+v", len(got), len(want), want, got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("models[%d].ID = %q, want %q", i, got[i].ID, id)
		}
	}
}

// decodeModelResponse 解包 okEnvelope -> ModelResponse，供 handler 断言使用。
func decodeModelResponse(t *testing.T, raw []byte) pluginapi.ModelResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %+v", env.Error)
	}
	var resp pluginapi.ModelResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode model response: %v", err)
	}
	return resp
}

// 字符串条目与对象条目混合解析，context/max_tokens 正确映射。
func TestParseModelsConfig_StringAndObject(t *testing.T) {
	resetConfiguredModels(t)
	parseModelsConfig([]any{
		"glm-5.2",
		map[string]any{"id": "custom-model", "name": "Custom Model", "context": 131072, "max_tokens": 4096},
	})
	got := getConfiguredModels()
	assertModelIDs(t, got, "glm-5.2", "custom-model")
	if got[0].OwnedBy != providerName {
		t.Fatalf("models[0].OwnedBy = %q, want %q", got[0].OwnedBy, providerName)
	}
	if got[0].SupportedGenerationMethods == nil || len(got[0].SupportedGenerationMethods) != 1 {
		t.Fatalf("models[0].SupportedGenerationMethods = %v, want [chat]", got[0].SupportedGenerationMethods)
	}
	if got[1].ContextLength != 131072 || got[1].MaxCompletionTokens != 4096 {
		t.Fatalf("custom-model context/max_tokens = %d/%d, want 131072/4096",
			got[1].ContextLength, got[1].MaxCompletionTokens)
	}
}

// enabled=false 的条目被跳过；缺省 name 时回退为 id。
func TestParseModelsConfig_SkipsDisabled(t *testing.T) {
	resetConfiguredModels(t)
	parseModelsConfig([]any{
		map[string]any{"id": "a", "enabled": false},
		map[string]any{"id": "b", "enabled": true},
		"c",
	})
	got := getConfiguredModels()
	assertModelIDs(t, got, "b", "c")
	if got[0].Name != "b" {
		t.Fatalf("models[0].Name = %q, want fallback to id", got[0].Name)
	}
}

// 显式 `models: []` 清空覆盖，恢复动态获取 / 静态默认。
func TestParseModelsConfig_EmptyListClears(t *testing.T) {
	resetConfiguredModels(t)
	parseModelsConfig([]any{"glm-5.2"})
	if len(getConfiguredModels()) != 1 {
		t.Fatalf("precondition: configured models not set")
	}
	parseModelsConfig([]any{})
	if got := getConfiguredModels(); len(got) != 0 {
		t.Fatalf("configured models after empty list = %d, want 0", len(got))
	}
}

// 全非法条目时保持现状，不静默清空已有覆盖。
func TestParseModelsConfig_MalformedKeepsCurrent(t *testing.T) {
	resetConfiguredModels(t)
	parseModelsConfig([]any{"glm-5.2"})
	parseModelsConfig([]any{
		map[string]any{"enabled": true}, // 缺 id
		42,                              // 既非字符串也非对象
	})
	got := getConfiguredModels()
	assertModelIDs(t, got, "glm-5.2")
}

// configure() 的 case "models" 接线：YAML 列表经整行 JSON 解析后生效。
func TestConfigure_ModelsFromConfigYAML(t *testing.T) {
	resetConfiguredModels(t)
	configure(configYAMLForTest(t, `
checkin_auto: true
models: ["glm-5.2", {"id": "x-model", "context": 65536}]
`))
	got := getConfiguredModels()
	assertModelIDs(t, got, "glm-5.2", "x-model")
	if got[1].ContextLength != 65536 {
		t.Fatalf("x-model context = %d, want 65536", got[1].ContextLength)
	}
}

// 配置存在时 model.static 返回"配置 + 静态默认补充"（合并去重、配置优先）。
func TestConfiguredModels_OverrideStaticHandler(t *testing.T) {
	resetConfiguredModels(t)
	parseModelsConfig([]any{"glm-5.2", "custom-a"})
	raw, err := handleModelStatic([]byte("{}"))
	if err != nil {
		t.Fatalf("handleModelStatic: %v", err)
	}
	resp := decodeModelResponse(t, raw)
	// 配置条目在前，wbModels() 中未配置的补充在后，同 ID 以配置为准（glm-5.2 去重）。
	var want []string
	for _, m := range wbModels() {
		if m.ID != "glm-5.2" {
			want = append(want, m.ID)
		}
	}
	want = append([]string{"glm-5.2", "custom-a"}, want...)
	assertModelIDs(t, resp.Models, want...)
}

// 配置存在时 model.for_auth 返回"配置 + 动态列表补充"（合并去重、配置优先）。
func TestConfiguredModels_OverrideForAuthHandler(t *testing.T) {
	resetConfiguredModels(t)
	parseModelsConfig([]any{"custom-a"})
	req, err := json.Marshal(pluginapi.AuthModelRequest{})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	raw, err := handleModelForAuth(req)
	if err != nil {
		t.Fatalf("handleModelForAuth: %v", err)
	}
	resp := decodeModelResponse(t, raw)
	// 无 storageJSON → 动态回退 wbModels()；配置 custom-a 在前，默认列表补充在后。
	var want []string
	for _, m := range wbModels() {
		want = append(want, m.ID)
	}
	want = append([]string{"custom-a"}, want...)
	assertModelIDs(t, resp.Models, want...)
}

// 未配置时 model.static 回退到静态默认列表（回归保护）。
func TestNoConfiguredModels_FallsBackToStatic(t *testing.T) {
	resetConfiguredModels(t)
	raw, err := handleModelStatic([]byte("{}"))
	if err != nil {
		t.Fatalf("handleModelStatic: %v", err)
	}
	resp := decodeModelResponse(t, raw)
	if len(resp.Models) != len(wbModels()) {
		t.Fatalf("fallback model count = %d, want %d", len(resp.Models), len(wbModels()))
	}
}

// parseModelsValue 单行 JSON：直接解析成功（回归）。
func TestParseModelsValue_SingleLine(t *testing.T) {
	v, ok := parseModelsValue([]string{`models: [{"id": "a"}]`}, 0, `[{"id": "a"}]`)
	if !ok {
		t.Fatalf("single-line parse failed")
	}
	items, ok := v.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected value: %#v", v)
	}
}

// parseModelsValue 多行 pretty-print JSON（面板/编辑器自动美化形态）：
// 跨行收集到括号闭合后整体解析成功。
func TestParseModelsValue_MultiLine(t *testing.T) {
	lines := []string{
		"models: [",
		"  {",
		`    "context": 2000000,`,
		`    "id": "hy4-preview",`,
		`    "max_tokens": 20000,`,
		`    "name": "Hy4 preview"`,
		"  },",
		"  {",
		`    "context": 2000000,`,
		`    "id": "hy3",`,
		`    "max_tokens": 20000,`,
		`    "name": "Hy3"`,
		"  }",
		"]",
		"checkin_auto: true",
	}
	v, ok := parseModelsValue(lines, 0, "[")
	if !ok {
		t.Fatalf("multi-line parse failed")
	}
	items, ok := v.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("unexpected value: %#v", v)
	}
}

// JSON 未闭合时返回 ok=false（保持现状，不误吞后续配置行）。
func TestParseModelsValue_Unclosed(t *testing.T) {
	lines := []string{
		"models: [",
		`  {"id": "a"},`,
		"checkin_auto: true",
	}
	if _, ok := parseModelsValue(lines, 0, "["); ok {
		t.Fatalf("unclosed JSON should fail")
	}
}

// 面板保存自动格式化成跨行 pretty-print 后，configure() 全链路应生效
// （修复前该形态静默失败、回退默认列表）。
func TestConfigure_ModelsMultiLinePretty(t *testing.T) {
	resetConfiguredModels(t)
	configure(configYAMLForTest(t, `
checkin_auto: true
models: [
  {
    "context": 2000000,
    "id": "hy4-preview",
    "max_tokens": 20000,
    "name": "Hy4 preview"
  },
  {
    "context": 2000000,
    "id": "hy3",
    "max_tokens": 20000,
    "name": "Hy3"
  }
]
`))
	got := getConfiguredModels()
	assertModelIDs(t, got, "hy4-preview", "hy3")
	if got[0].ContextLength != 2000000 || got[0].MaxCompletionTokens != 20000 {
		t.Fatalf("hy4-preview context/max_tokens = %d/%d, want 2000000/20000",
			got[0].ContextLength, got[0].MaxCompletionTokens)
	}
}

// 多行 YAML 列表（models: 换行逐行 - xxx）依旧不解析，保持现状（回归保护）。
func TestConfigure_ModelsYAMLListStillIgnored(t *testing.T) {
	resetConfiguredModels(t)
	parseModelsConfig([]any{"glm-5.2"})
	configure(configYAMLForTest(t, `
models:
- hy4-preview
- hy3
`))
	got := getConfiguredModels()
	assertModelIDs(t, got, "glm-5.2")
}

// 宿主把面板 JSON 数组序列化回 YAML block sequence 的 models 形态
// （实测 cli-proxy-api config_store 落库形态：models: 换行逐行
// `- context: ... / id: ...`）经 configure() 全链路生效，且字段完整映射。
func TestConfigure_ModelsYAMLBlock(t *testing.T) {
	resetConfiguredModels(t)
	configure(configYAMLForTest(t, `
checkin_auto: true
      models:
        - context: 2000000
          id: hy4-preview
          max_tokens: 20000
          name: Hy4 preview
        - context: 2000000
          id: hy3
          max_tokens: 20000
          name: Hy3
      enabled: true
`))
	got := getConfiguredModels()
	assertModelIDs(t, got, "hy4-preview", "hy3")
	if got[0].Name != "Hy4 preview" || got[0].ContextLength != 2000000 || got[0].MaxCompletionTokens != 20000 {
		t.Fatalf("models[0] = %+v, want name=Hy4 preview context=2000000 max_tokens=20000", got[0])
	}
}

// block 形态中 enabled: false 条目跳过（与 JSON 形态语义一致）。
func TestConfigure_ModelsYAMLBlock_EnabledFalse(t *testing.T) {
	resetConfiguredModels(t)
	configure(configYAMLForTest(t, `
      models:
        - context: 2000000
          id: hy4-preview
          max_tokens: 20000
          name: Hy4 preview
        - context: 2000000
          id: hy3
          max_tokens: 20000
          name: Hy3
          enabled: false
`))
	got := getConfiguredModels()
	assertModelIDs(t, got, "hy4-preview")
}

// parseModelsYAMLBlock 直接单测：缩进边界正确，enabled 等兄弟 key 不被吞入。
func TestParseModelsYAMLBlock(t *testing.T) {
	lines := []string{
		"      models:",
		"        - context: 2000000",
		"          id: hy4-preview",
		"          max_tokens: 20000",
		"          name: Hy4 preview",
		"        - context: 2000000",
		"          id: hy3",
		"          max_tokens: 20000",
		"          name: Hy3",
		"      enabled: true",
	}
	v, ok := parseModelsYAMLBlock(lines, 1, 6)
	if !ok {
		t.Fatalf("yaml block parse failed")
	}
	items, ok := v.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("unexpected value: %#v", v)
	}
	m0, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item[0] not map: %#v", items[0])
	}
	if m0["id"] != "hy4-preview" || m0["context"] != int64(2000000) || m0["max_tokens"] != int64(20000) {
		t.Fatalf("item[0] = %#v, want id/context/max_tokens", m0)
	}
	if _, exists := m0["enabled"]; exists {
		t.Fatalf("item[0] should not contain enabled, got %#v", m0)
	}
}

// 无任何可识别条目（纯字符串列表）返回 ok=false，保持现状（回归保护）。
func TestParseModelsYAMLBlock_StringItemsOnly(t *testing.T) {
	lines := []string{
		"models:",
		"- hy4-preview",
		"- hy3",
	}
	if _, ok := parseModelsYAMLBlock(lines, 1, 0); ok {
		t.Fatalf("string-only yaml block should fail")
	}
}

// 合并去重：同 ID 配置优先（字段以配置为准），配置没有的自动获取模型追加在后。
func TestMergeConfiguredAndDynamic_ConfigWins(t *testing.T) {
	configured := []pluginapi.ModelInfo{
		{ID: "a", Name: "ConfigA", ContextLength: 100},
		{ID: "c", Name: "ConfigC"},
	}
	dynamic := []pluginapi.ModelInfo{
		{ID: "a", Name: "DynA", ContextLength: 999},
		{ID: "b", Name: "DynB"},
	}
	got := mergeConfiguredAndDynamic(configured, dynamic)
	assertModelIDs(t, got, "a", "c", "b")
	if got[0].Name != "ConfigA" || got[0].ContextLength != 100 {
		t.Fatalf("configured entry should win: %+v", got[0])
	}
}

// 自动获取独有模型追加；空配置原样返回动态列表；空动态原样返回配置。
func TestMergeConfiguredAndDynamic_AppendsUnique(t *testing.T) {
	configured := []pluginapi.ModelInfo{{ID: "a"}}
	dynamic := []pluginapi.ModelInfo{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	assertModelIDs(t, mergeConfiguredAndDynamic(configured, dynamic), "a", "b", "c")
	assertModelIDs(t, mergeConfiguredAndDynamic(nil, dynamic), "a", "b", "c")
	assertModelIDs(t, mergeConfiguredAndDynamic(configured, nil), "a")
}

// 合并不修改入参（保护 dynamicModelsCache / wbModels 的共享底层数组）。
func TestMergeConfiguredAndDynamic_NoMutation(t *testing.T) {
	configured := []pluginapi.ModelInfo{{ID: "a"}}
	dynamic := []pluginapi.ModelInfo{{ID: "a"}, {ID: "b"}}
	_ = mergeConfiguredAndDynamic(configured, dynamic)
	if len(dynamic) != 2 || dynamic[0].ID != "a" || dynamic[1].ID != "b" {
		t.Fatalf("dynamic input mutated: %+v", dynamic)
	}
	if len(configured) != 1 || configured[0].ID != "a" {
		t.Fatalf("configured input mutated: %+v", configured)
	}
}

// 全链路：预置动态缓存（模拟上游已拉取），配置存在时 for_auth 返回
// "配置优先 + 动态独有模型补充"。
func TestConfiguredModels_ForAuthMergesDynamicCache(t *testing.T) {
	resetConfiguredModels(t)
	storeDynamicModels([]pluginapi.ModelInfo{
		{ID: "glm-5.2", Name: "Dyn GLM"},
		{ID: "hy4-preview", Name: "Hy4 preview"},
	})
	t.Cleanup(func() { storeDynamicModels(nil) })
	parseModelsConfig([]any{
		map[string]any{"id": "glm-5.2", "name": "Cfg GLM", "context": 2000000, "max_tokens": 20000},
	})
	req, err := json.Marshal(pluginapi.AuthModelRequest{})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	raw, err := handleModelForAuth(req)
	if err != nil {
		t.Fatalf("handleModelForAuth: %v", err)
	}
	resp := decodeModelResponse(t, raw)
	assertModelIDs(t, resp.Models, "glm-5.2", "hy4-preview")
	if resp.Models[0].Name != "Cfg GLM" || resp.Models[0].ContextLength != 2000000 {
		t.Fatalf("configured glm-5.2 should win: %+v", resp.Models[0])
	}
}
