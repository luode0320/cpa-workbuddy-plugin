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
	// 元数据必须随主条目一起带出，否则上下文长度与输出上限会丢。
	if got[0].ContextLength != 1000000 {
		t.Fatalf("main context = %d, want 1000000", got[0].ContextLength)
	}
}

// TestExposeModelIDs_UnknownPassthrough 验证上游新增代号时的两种形态：
//   - 上游给了 display_name：主条目用上游官方名（客户端才看得到可读名字），
//     兼容条目保留内部代号（旧调用方与按代号路由的账号仍可用）；
//   - 上游没给 display_name：原样透传单条，不凭空造名字。
//
// 这里断言的是「不猜名字」，而不是「必须只有一条」：上游 display_name 是权威
// 名字，使用它不属于猜测。2026-10-24 生产实测的 Kimi-K3.1-Pro 就属第一种形态。
func TestExposeModelIDs_UnknownPassthrough(t *testing.T) {
	// 1. 上游未给官方名：原样透传，不追加兼容条目。
	got := exposeModelIDs([]pluginapi.ModelInfo{{ID: "upstream-new"}})
	if len(got) != 1 {
		t.Fatalf("expose count = %d, want 1", len(got))
	}
	if got[0].ID != "upstream-new" {
		t.Fatalf("entry = %+v, want passthrough id", got[0])
	}
	// 2. 上游给了官方名：主条目用官方名，兼容条目保留代号。
	named := exposeModelIDs([]pluginapi.ModelInfo{{ID: "upstream-new", DisplayName: "Upstream New"}})
	if len(named) != 2 {
		t.Fatalf("expose count = %d, want 2 (main + compat)", len(named))
	}
	if named[0].ID != "Upstream New" || named[0].DisplayName != "Upstream New" {
		t.Fatalf("main entry = %+v, want upstream display name", named[0])
	}
	if named[1].ID != "upstream-new" || named[1].Name != "upstream-new" {
		t.Fatalf("compat entry = %+v, want internal key", named[1])
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
