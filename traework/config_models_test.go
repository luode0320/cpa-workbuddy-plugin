// config_models_test.go 覆盖 config_yaml `models:` 的多行 JSON 兼容：
// 面板/编辑器把单行 JSON 自动美化成多行 pretty-print 后，applyConfigLines
// 仍能跨行收集并整体解析生效；多行 YAML 列表依旧不解析（回归保护）。
package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// resetTraeConfiguredModels 清空全局覆盖列表并在用例结束后恢复。
func resetTraeConfiguredModels(t *testing.T) {
	t.Helper()
	configuredModels = nil
	t.Cleanup(func() { configuredModels = nil })
}

// traeLines 把 yaml 文本转成 applyConfigLines 的预处理行（去空行/注释行）。
func traeLines(t *testing.T, yaml string) []string {
	t.Helper()
	var out []string
	for _, ln := range strings.Split(yaml, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// parseModelsValue 单行 JSON：直接解析成功（回归）。
func TestTraeParseModelsValue_SingleLine(t *testing.T) {
	v, ok := parseModelsValue([]string{`models: [{"id": "a"}]`}, 0, `[{"id": "a"}]`)
	if !ok {
		t.Fatalf("single-line parse failed")
	}
	items, ok := v.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected value: %#v", v)
	}
}

// parseModelsValue 多行 pretty-print JSON：跨行收集到括号闭合后整体解析成功。
func TestTraeParseModelsValue_MultiLine(t *testing.T) {
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

// JSON 未闭合时返回 ok=false（不误吞后续配置行）。
func TestTraeParseModelsValue_Unclosed(t *testing.T) {
	lines := []string{
		"models: [",
		`  {"id": "a"},`,
		"checkin_auto: true",
	}
	if _, ok := parseModelsValue(lines, 0, "["); ok {
		t.Fatalf("unclosed JSON should fail")
	}
}

// 面板自动格式化成的跨行 pretty-print 经 applyConfigLines 全链路生效。
func TestTraeApplyConfigLines_ModelsMultiLinePretty(t *testing.T) {
	resetTraeConfiguredModels(t)
	applyConfigLines(&traeConfig{}, traeLines(t, `
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
	got := loadedModels()
	if len(got) != 2 || got[0].ID != "hy4-preview" || got[1].ID != "hy3" {
		t.Fatalf("loaded models = %+v, want [hy4-preview hy3]", got)
	}
}

// 单行 JSON 依旧生效（回归）。
func TestTraeApplyConfigLines_ModelsSingleLine(t *testing.T) {
	resetTraeConfiguredModels(t)
	applyConfigLines(&traeConfig{}, traeLines(t, `
models: [{"id": "glm-5.2"}, {"id": "hy3", "name": "Hy3"}]
`))
	got := loadedModels()
	if len(got) != 2 || got[0].ID != "glm-5.2" || got[1].ID != "hy3" {
		t.Fatalf("loaded models = %+v, want [glm-5.2 hy3]", got)
	}
	if got[1].Name != "Hy3" {
		t.Fatalf("models[1].Name = %q, want Hy3", got[1].Name)
	}
}

// 多行 YAML 列表（models: 换行逐行 - xxx）依旧不解析，保持现状（回归保护）。
func TestTraeApplyConfigLines_ModelsYAMLListStillIgnored(t *testing.T) {
	resetTraeConfiguredModels(t)
	parseModelsConfig([]any{"glm-5.2"})
	applyConfigLines(&traeConfig{}, traeLines(t, `
models:
- hy4-preview
- hy3
`))
	got := loadedModels()
	if len(got) != 1 || got[0].ID != "glm-5.2" {
		t.Fatalf("loaded models = %+v, want kept [glm-5.2]", got)
	}
}

// 宿主把面板 JSON 数组序列化回 YAML block sequence 的 models 形态
// （实测 cli-proxy-api config_store 落库形态：models: 换行逐行
// `- context: ... / id: ...`）经 configure() 全链路生效。
func TestTraeConfigure_ModelsYAMLBlock(t *testing.T) {
	resetTraeConfiguredModels(t)
	raw, err := json.Marshal(map[string]any{"config_yaml": []byte(`
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
`)})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	configure(raw)
	got := loadedModels()
	if len(got) != 2 || got[0].ID != "hy4-preview" || got[1].ID != "hy3" {
		t.Fatalf("loaded models = %+v, want [hy4-preview hy3]", got)
	}
	if got[0].Name != "Hy4 preview" {
		t.Fatalf("models[0].Name = %q, want Hy4 preview", got[0].Name)
	}
}

// parseModelsYAMLBlock 直接单测：真实缩进形态 + 类型同构（数字/字符串）。
func TestTraeParseModelsYAMLBlock(t *testing.T) {
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
	// enabled: true（缩进等于 baseIndent）必须是停止边界，不能被吞入条目。
	if _, exists := m0["enabled"]; exists {
		t.Fatalf("item[0] should not contain enabled, got %#v", m0)
	}
}
