// models_expose_test.go 覆盖模型对外暴露口径：内部代号展开成
// 「官方名主条目 + 兼容代号条目」，并保证每个对外 ID 都能回译到上游 key。
package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestExposeModelIDs_KnownCode 验证已知内部代号展开为两条：
// 官方名主条目（ID/DisplayName 均为官方名）与兼容代号条目。
func TestExposeModelIDs_KnownCode(t *testing.T) {
	got := exposeModelIDs([]pluginapi.ModelInfo{{ID: "qmodel_38max", ContextLength: 1000000}})
	if len(got) != 2 {
		t.Fatalf("expose count = %d, want 2", len(got))
	}
	if got[0].ID != "Qwen3.8-Max" || got[0].DisplayName != "Qwen3.8-Max" || got[0].Name != "qmodel_38max" {
		t.Fatalf("main entry = %+v, want ID/DisplayName=Qwen3.8-Max Name=qmodel_38max", got[0])
	}
	if got[1].ID != "qmodel_38max" || got[1].Name != "qmodel_38max" {
		t.Fatalf("compat entry = %+v, want ID/Name=qmodel_38max", got[1])
	}
	if got[0].ContextLength != 1000000 {
		t.Fatalf("main context = %d, want 1000000", got[0].ContextLength)
	}
}

// TestExposeModelIDs_CaseOnlyDiff 验证代号与官方名只差大小写时仍产出两条：
// 国际版 ultimate/performance/efficient 属于这一类，漏注册会让旧调用方
// 反查不到 provider。
func TestExposeModelIDs_CaseOnlyDiff(t *testing.T) {
	got := exposeModelIDs([]pluginapi.ModelInfo{{ID: "ultimate"}})
	if len(got) != 2 {
		t.Fatalf("expose count = %d, want 2", len(got))
	}
	if got[0].ID != "Ultimate" || got[1].ID != "ultimate" {
		t.Fatalf("IDs = %q/%q, want Ultimate/ultimate", got[0].ID, got[1].ID)
	}
}

// TestExposeModelIDs_UnknownPassthrough 验证未登记官方名的 ID 的两种语义：
// 上游没给 display_name 时原样透传一条；上游给了 display_name 时以上游名为主条目
// 并追加兼容代号条目（上游命名是权威，不是"凭空造名"）。
func TestExposeModelIDs_UnknownPassthrough(t *testing.T) {
	got := exposeModelIDs([]pluginapi.ModelInfo{{ID: "upstream-new"}})
	if len(got) != 1 {
		t.Fatalf("expose count = %d, want 1 (no display_name => passthrough only)", len(got))
	}
	if got[0].ID != "upstream-new" {
		t.Fatalf("entry = %+v, want passthrough id", got[0])
	}

	got = exposeModelIDs([]pluginapi.ModelInfo{{ID: "upstream-new", DisplayName: "Upstream New"}})
	if len(got) != 2 {
		t.Fatalf("expose count = %d, want 2 (display_name => main + compat)", len(got))
	}
	if got[0].ID != "Upstream New" || got[0].DisplayName != "Upstream New" || got[0].Name != "upstream-new" {
		t.Fatalf("main entry = %+v, want ID/DisplayName=Upstream New Name=upstream-new", got[0])
	}
	if got[1].ID != "upstream-new" {
		t.Fatalf("compat entry = %+v, want ID=upstream-new", got[1])
	}
}

// TestExposeModelIDs_StripsProviderPrefix 验证带 provider 前缀的输入按裸代号处理。
func TestExposeModelIDs_StripsProviderPrefix(t *testing.T) {
	got := exposeModelIDs([]pluginapi.ModelInfo{{ID: providerName + "/qfmodel"}})
	if len(got) != 2 {
		t.Fatalf("expose count = %d, want 2", len(got))
	}
	if got[0].ID != "Qwen3.8-Flash" || got[1].ID != "qfmodel" {
		t.Fatalf("IDs = %q/%q, want Qwen3.8-Flash/qfmodel", got[0].ID, got[1].ID)
	}
}

// TestExposeModelIDs_Dedup 验证重复输入不会重复产出条目。
func TestExposeModelIDs_Dedup(t *testing.T) {
	got := exposeModelIDs([]pluginapi.ModelInfo{{ID: "gmodel"}, {ID: "gmodel"}})
	if len(got) != 2 {
		t.Fatalf("expose count = %d, want 2 (deduped)", len(got))
	}
}

// TestExposeModelIDs_RoundTripToUpstreamKey 是本文件的核心不变量：
// 对外暴露的每一个 ID 都必须能被 cpaToUpstreamKey 回译成正确的上游 key。
// 漏一条就会让客户端选中的模型静默落到 auto，参数与计费全错。
func TestExposeModelIDs_RoundTripToUpstreamKey(t *testing.T) {
	codes := make([]string, 0, len(internalKeyDisplayNames))
	for code := range internalKeyDisplayNames {
		codes = append(codes, code)
	}
	exposed := exposeModelIDs(toModelInfos(codes))
	if len(exposed) != len(codes)*2 {
		t.Fatalf("expose count = %d, want %d (each code yields main + compat)", len(exposed), len(codes)*2)
	}
	for _, m := range exposed {
		if got := cpaToUpstreamKey(stripProviderPrefix(m.ID)); got != m.Name {
			t.Errorf("cpaToUpstreamKey(%q) = %q, want internal key %q", m.ID, got, m.Name)
		}
	}
}

// toModelInfos 把内部代号切片转成模型条目，供展开用例复用。
func toModelInfos(codes []string) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(codes))
	for _, code := range codes {
		out = append(out, pluginapi.ModelInfo{ID: code})
	}
	return out
}

// TestFilterExcludedModels_MultiForm 验证排除规则在官方名、内部代号、
// 完整 ID 三种形态下都能命中。
func TestFilterExcludedModels_MultiForm(t *testing.T) {
	exposed := exposeModelIDs([]pluginapi.ModelInfo{{ID: "qmodel_38max"}, {ID: "gmodel"}})
	cases := []struct {
		name     string
		excluded string
		wantKept []string
	}{
		{"官方名", "Qwen3.8-Max", []string{"GLM-5.3", "gmodel"}},
		{"内部代号", "qmodel_38max", []string{"GLM-5.3", "gmodel"}},
		{"大小写不敏感", "qwen3.8-max", []string{"GLM-5.3", "gmodel"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := pluginapi.HostConfigSummary{
				ExcludedModels: map[string][]string{providerName: {tc.excluded}},
			}
			got := filterExcludedModels(exposed, host)
			if len(got) != len(tc.wantKept) {
				t.Fatalf("kept = %d, want %d", len(got), len(tc.wantKept))
			}
			for i, want := range tc.wantKept {
				if got[i].ID != want {
					t.Fatalf("kept[%d] = %q, want %q", i, got[i].ID, want)
				}
			}
		})
	}
}

// TestFilterExcludedModels_DoesNotCorruptInput 验证过滤返回新切片，
// 不会原地改写传入的动态缓存底层数组。
func TestFilterExcludedModels_DoesNotCorruptInput(t *testing.T) {
	exposed := exposeModelIDs([]pluginapi.ModelInfo{{ID: "gmodel"}})
	host := pluginapi.HostConfigSummary{
		ExcludedModels: map[string][]string{providerName: {"GLM-5.3"}},
	}
	_ = filterExcludedModels(exposed, host)
	if len(exposed) != 2 || exposed[0].ID != "GLM-5.3" {
		t.Fatalf("input slice mutated: %+v", exposed)
	}
}
