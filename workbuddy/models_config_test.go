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

// 配置存在时 model.static 优先返回配置列表而非静态默认。
func TestConfiguredModels_OverrideStaticHandler(t *testing.T) {
	resetConfiguredModels(t)
	parseModelsConfig([]any{"glm-5.2", "custom-a"})
	raw, err := handleModelStatic([]byte("{}"))
	if err != nil {
		t.Fatalf("handleModelStatic: %v", err)
	}
	resp := decodeModelResponse(t, raw)
	assertModelIDs(t, resp.Models, "glm-5.2", "custom-a")
}

// 配置存在时 model.for_auth 在无 token 情况下也返回配置列表（配置优先）。
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
	assertModelIDs(t, resp.Models, "custom-a")
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
