// model_case_normalize_test.go 覆盖「模型名大小写归一」这条修复链：
// 上游 CodeBuddy 的模型 ID 大小写敏感，客户端发 DeepSeek-V4-Pro 时上游返回
// 11102 model service info not found，而注册 ID 是小写 deepseek-v4-pro。
// 归一链由三段组成，缺任一段大小写名都会原样透传上游：
//  1. refreshUpstreamModelIDCache —— 从动态发现 + config 覆盖建「小写→注册ID」表
//  2. canonicalUpstreamModelID   —— 查表折回注册时的大小写
//  3. rewriteModelInPlace / rewriteModelInBody —— 精确比较后真正写回 body
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// installModelIDFixtures 装一批注册 ID 到归一表，返回恢复函数。
// 直接写归一表而非走 storeDynamicModels，避免用例依赖动态发现链路。
func installModelIDFixtures(t *testing.T, ids ...string) {
	t.Helper()
	upstreamModelIDCache.Lock()
	byLower := make(map[string]string, len(ids))
	for _, id := range ids {
		byLower[strings.ToLower(id)] = id
	}
	upstreamModelIDCache.byLower = byLower
	upstreamModelIDCache.Unlock()
	t.Cleanup(func() {
		upstreamModelIDCache.Lock()
		upstreamModelIDCache.byLower = nil
		upstreamModelIDCache.Unlock()
	})
}

// TestCanonicalUpstreamModelID_FoldsCase 确认大写/混合大小写名都折回注册 ID。
func TestCanonicalUpstreamModelID_FoldsCase(t *testing.T) {
	installModelIDFixtures(t, "deepseek-v4-pro", "glm-5.3-flash", "kimi-k3.1-pro")
	cases := []struct {
		in   string
		want string
	}{
		{"deepseek-v4-pro", "deepseek-v4-pro"},
		{"DeepSeek-V4-Pro", "deepseek-v4-pro"},
		{"DEEPSEEK-V4-PRO", "deepseek-v4-pro"},
		{"deepseek-v4-Pro", "deepseek-v4-pro"},
		{"  DeepSeek-V4-Pro  ", "deepseek-v4-pro"},
		{"GLM-5.3-Flash", "glm-5.3-flash"},
		{"Kimi-K3.1-Pro", "kimi-k3.1-pro"},
	}
	for _, c := range cases {
		if got := canonicalUpstreamModelID(c.in); got != c.want {
			t.Errorf("canonicalUpstreamModelID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCanonicalUpstreamModelID_PassthroughWhenUnknown 确认归一表未命中时
// 只做去空白，不猜大小写。表为空（上游模型列表还没拉到）时同样透传。
func TestCanonicalUpstreamModelID_PassthroughWhenUnknown(t *testing.T) {
	installModelIDFixtures(t, "deepseek-v4-pro")
	if got := canonicalUpstreamModelID("Some-Unknown-Model"); got != "Some-Unknown-Model" {
		t.Errorf("unknown model should pass through, got %q", got)
	}
	if got := canonicalUpstreamModelID("  spaced-model  "); got != "spaced-model" {
		t.Errorf("unknown model should be trimmed, got %q", got)
	}
	// 空归一表：上游模型列表不可用时不能凭空改大小写。
	upstreamModelIDCache.Lock()
	upstreamModelIDCache.byLower = nil
	upstreamModelIDCache.Unlock()
	if got := canonicalUpstreamModelID("DeepSeek-V4-Pro"); got != "DeepSeek-V4-Pro" {
		t.Errorf("empty table must preserve input case, got %q", got)
	}
}

// TestResolveUpstreamModel_NormalizesCase 确认归一发生在 resolveUpstreamModel
// 这一层：客户端大写名进来，出去的必须是上游注册的小写 ID。
func TestResolveUpstreamModel_NormalizesCase(t *testing.T) {
	installModelIDFixtures(t, "deepseek-v4-pro", "glm-5.3-flash")
	modelAliasCache.Lock()
	modelAliasCache.byAlias = nil
	modelAliasCache.Unlock()
	t.Cleanup(func() {
		modelAliasCache.Lock()
		modelAliasCache.byAlias = nil
		modelAliasCache.Unlock()
	})

	if got := resolveUpstreamModel("DeepSeek-V4-Pro", nil); got != "deepseek-v4-pro" {
		t.Errorf("uppercase request must normalize to registered id, got %q", got)
	}
	if got := resolveUpstreamModel("DEEPSEEK-V4-PRO", nil); got != "deepseek-v4-pro" {
		t.Errorf("all-caps request must normalize to registered id, got %q", got)
	}
	if got := resolveUpstreamModel("deepseek-v4-pro", nil); got != "deepseek-v4-pro" {
		t.Errorf("exact id must stay unchanged, got %q", got)
	}
}

// TestResolveUpstreamModel_NormalizesAliasTarget 确认 alias 命中的目标名
// 也要过一遍归一：alias 表本身按小写建索引，但其 value 可能是上游注册 ID 的
// 任意大小写写法，同样会撞上上游大小写敏感。
func TestResolveUpstreamModel_NormalizesAliasTarget(t *testing.T) {
	installModelIDFixtures(t, "deepseek-v4-pro")
	modelAliasCache.Lock()
	modelAliasCache.byAlias = map[string]string{"deepseek-pro": "DeepSeek-V4-Pro"}
	modelAliasCache.Unlock()
	t.Cleanup(func() {
		modelAliasCache.Lock()
		modelAliasCache.byAlias = nil
		modelAliasCache.Unlock()
	})

	if got := resolveUpstreamModel("deepseek-pro", nil); got != "deepseek-v4-pro" {
		t.Errorf("alias target must be normalized to registered id, got %q", got)
	}
}

// TestRewriteModelInPlace_RewritesCaseOnlyDifference 是本次修复的核心回归：
// 目标名与当前名只差大小写时必须真正写回。旧实现用 EqualFold 判"已相等"
// 直接 return false，大写名原样透传上游导致 11102。
func TestRewriteModelInPlace_RewritesCaseOnlyDifference(t *testing.T) {
	cases := []struct {
		name string
		cur  string
		want string
	}{
		{"all-lower-to-lower", "deepseek-v4-pro", "deepseek-v4-pro"},
		{"title-to-lower", "DeepSeek-V4-Pro", "deepseek-v4-pro"},
		{"upper-to-lower", "DEEPSEEK-V4-PRO", "deepseek-v4-pro"},
		{"mixed-to-lower", "deepseek-v4-Pro", "deepseek-v4-pro"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obj := map[string]any{"model": c.cur}
			rewriteModelInPlace(obj, c.want)
			if got, _ := obj["model"].(string); got != c.want {
				t.Fatalf("model = %q, want %q (case-only difference must rewrite)", got, c.want)
			}
		})
	}
}

// TestRewriteModelInPlace_SkipsExactMatch 确认完全一致时不动 body，
// 避免无意义的写回（返回 false）。
func TestRewriteModelInPlace_SkipsExactMatch(t *testing.T) {
	obj := map[string]any{"model": "deepseek-v4-pro"}
	if rewriteModelInPlace(obj, "deepseek-v4-pro") {
		t.Fatal("exact match should not report modification")
	}
	if rewriteModelInPlace(obj, "  ") {
		t.Fatal("empty target should not report modification")
	}
}

// TestRewriteModelInBody_RewritesCaseOnlyDifference 覆盖函数式入口的同一条回归。
func TestRewriteModelInBody_RewritesCaseOnlyDifference(t *testing.T) {
	in := []byte(`{"model":"DeepSeek-V4-Pro","messages":[{"role":"user","content":"hi"}]}`)
	out := rewriteModelInBody(in, "deepseek-v4-pro")
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got, _ := obj["model"].(string); got != "deepseek-v4-pro" {
		t.Fatalf("model = %q, want deepseek-v4-pro", got)
	}
}

// TestPrepareUpstreamBody_NormalizesUppercaseModel 端到端走一遍
// prepareUpstreamBody：大写模型名进来，出去的 body 必须是注册 ID。
func TestPrepareUpstreamBody_NormalizesUppercaseModel(t *testing.T) {
	installModelIDFixtures(t, "deepseek-v4-pro")
	modelAliasCache.Lock()
	modelAliasCache.byAlias = nil
	modelAliasCache.Unlock()
	t.Cleanup(func() {
		modelAliasCache.Lock()
		modelAliasCache.byAlias = nil
		modelAliasCache.Unlock()
	})

	in := []byte(`{"model":"DeepSeek-V4-Pro","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	upstream := resolveUpstreamModel("DeepSeek-V4-Pro", nil)
	out := prepareUpstreamBody(in, nil, &storedAuth{}, upstream)

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got, _ := obj["model"].(string); got != "deepseek-v4-pro" {
		t.Fatalf("prepared body model = %q, want deepseek-v4-pro", got)
	}
}

// TestRefreshUpstreamModelIDCache_FromConfiguredModels 确认归一表能从
// config_yaml 覆盖列表建起来（动态发现不可用时的保底数据源）。
func TestRefreshUpstreamModelIDCache_FromConfiguredModels(t *testing.T) {
	resetDynamicModelsCache(t)
	resetConfiguredModels(t)
	setConfiguredModels([]pluginapi.ModelInfo{
		{ID: "deepseek-v4-pro"},
		{ID: "GLM-5.3"},
		{ID: ""},
	})
	if got := canonicalUpstreamModelID("DEEPSEEK-V4-PRO"); got != "deepseek-v4-pro" {
		t.Errorf("configured id not registered, got %q", got)
	}
	// 注册 ID 自身的大小写要原样保留（GLM-5.3 是大写注册的）。
	if got := canonicalUpstreamModelID("glm-5.3"); got != "GLM-5.3" {
		t.Errorf("registered case must be preserved, got %q", got)
	}
}

// TestRefreshUpstreamModelIDCache_FromDynamicModels 确认动态发现结果同样入表，
// 且整体重建后不残留上一轮的旧 ID。
func TestRefreshUpstreamModelIDCache_FromDynamicModels(t *testing.T) {
	resetConfiguredModels(t)
	resetDynamicModelsCache(t)
	storeDynamicModels([]pluginapi.ModelInfo{{ID: "kimi-k3.1-pro"}})
	if got := canonicalUpstreamModelID("Kimi-K3.1-Pro"); got != "kimi-k3.1-pro" {
		t.Errorf("dynamic id not registered, got %q", got)
	}
	// 换成另一批模型后，旧 ID 必须从表中消失（重建而非增量）。
	storeDynamicModels([]pluginapi.ModelInfo{{ID: "glm-5.3-flash"}})
	if got := canonicalUpstreamModelID("Kimi-K3.1-Pro"); got != "Kimi-K3.1-Pro" {
		t.Errorf("stale id still registered, got %q", got)
	}
}
