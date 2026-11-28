package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resetModeCache 清空模式发现缓存，避免测试间互相影响。
func resetModeCache() {
	modeDiscoveryCache.mu.Lock()
	modeDiscoveryCache.entries = make(map[string]*modeCacheEntry)
	modeDiscoveryCache.mu.Unlock()
}

// setConfigForTest 临时替换全局配置，供目录展开相关测试使用。
// [参数] t: 测试上下文；cfg: 待生效的配置。
// [返回] 无。
// 最近修改时间：2026-11-29 新增, 配置开关测试支撑。
func setConfigForTest(t *testing.T, cfg Config) {
	t.Helper()
	activeConfig.Store(cfg)
}

// TestModelCatalogOnlyRealModels 锁定核心契约：对外模型名必须能在网页模型菜单里找到对应项。
// 网页菜单只有 GLM-5.3 与 GLM-5.3 Flash，任何额外模型名都属于虚构，必须被这个测试挡住。
func TestModelCatalogOnlyRealModels(t *testing.T) {
	want := map[string]bool{
		"GLM-5.3":       true,  // if_plus_model=true
		"GLM-5.3 Flash": false, // if_plus_model=false
	}
	if len(modelCatalog) != len(want) {
		t.Fatalf("modelCatalog must contain exactly %d real models, got %d", len(want), len(modelCatalog))
	}
	for _, entry := range modelCatalog {
		plus, ok := want[entry.DisplayName]
		if !ok {
			t.Errorf("model %q (display %q) is not in the web model menu", entry.ID, entry.DisplayName)
			continue
		}
		if entry.PlusModel != plus {
			t.Errorf("%s: PlusModel = %v, want %v", entry.DisplayName, entry.PlusModel, plus)
		}
		delete(want, entry.DisplayName)
	}
	if len(want) != 0 {
		t.Errorf("missing real models: %v", want)
	}
}

// TestAllModelEntriesOnlyUseRealModelNames 验证展开后的每个 ID 都以真实模型名为前缀，
// 防止模式后缀把虚构名字带回来。
func TestAllModelEntriesOnlyUseRealModelNames(t *testing.T) {
	validPrefixes := []string{"glm/glm-5.3", "glm/glm-5.3-flash"}
	for _, entry := range allModelEntries() {
		ok := false
		for _, prefix := range validPrefixes {
			if strings.HasPrefix(entry.ID, prefix) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("model id %q does not derive from a real model", entry.ID)
		}
	}
}

// TestResolveUpstreamTargetMapsRealModels 验证两个真实模型解析到正确的 if_plus_model。
func TestResolveUpstreamTargetMapsRealModels(t *testing.T) {
	cases := []struct {
		model    string
		wantPlus bool
		wantMode string
	}{
		{"glm/glm-5.3", true, ""},
		{"glm/glm-5.3-flash", false, ""},
		{"glm/glm-5.3-think", true, "zero"},
		{"glm/glm-5.3-flash-think", false, "zero"},
		{"glm/glm-5.3-agent", true, "chat_agent"},
		{"glm/glm-5.3-flash-research", false, "deep_research"},
		{"glm-5.3", true, ""},
		{"glm-5.3-flash", false, ""},
	}
	for _, tc := range cases {
		assistantID, mode, plus := resolveUpstreamTarget(tc.model)
		if assistantID != defaultAssistantID {
			t.Errorf("%s: assistantID = %q", tc.model, assistantID)
		}
		if mode != tc.wantMode || plus != tc.wantPlus {
			t.Errorf("%s: got (mode=%q plus=%v) want (mode=%q plus=%v)",
				tc.model, mode, plus, tc.wantMode, tc.wantPlus)
		}
	}
}

// TestResolveUpstreamTargetUnknownDefaultsToGLM53 验证未知模型名回落到 GLM-5.3，
// 不再把内部取值当模型名透传。
func TestResolveUpstreamTargetUnknownDefaultsToGLM53(t *testing.T) {
	for _, name := range []string{"glm/totally-bogus", "glm/glm-4-plus", "glm/glm-5.1"} {
		_, mode, plus := resolveUpstreamTarget(name)
		if mode != "" {
			t.Errorf("%s: unknown name must not become a chat_mode, got %q", name, mode)
		}
		if !plus {
			t.Errorf("%s: unknown name must fall back to GLM-5.3 (plus=true)", name)
		}
	}
}

// TestResolveUpstreamTargetKeepsAgentID 验证裸智能体 ID 仍被当作 assistant_id。
func TestResolveUpstreamTargetKeepsAgentID(t *testing.T) {
	id := "aaaabbbbccccddddeeeeffff"
	gotID, mode, plus := resolveUpstreamTarget("glm/" + id)
	if gotID != id || mode != "" || !plus {
		t.Fatalf("got (%s,%s,%v)", gotID, mode, plus)
	}
}

// TestNormalizeChatModeCollapsesDefault 验证上游记录的 normal 与请求侧 "" 归一到同一模式。
func TestNormalizeChatModeCollapsesDefault(t *testing.T) {
	cases := map[string]string{
		"normal":         "",
		"Normal":         "",
		"  normal  ":     "",
		"":               "",
		"zero":           "zero",
		"chat_agent":     "chat_agent",
		"deep_research":  "deep_research",
		"brand_new_mode": "brand_new_mode",
	}
	for in, want := range cases {
		if got := normalizeChatMode(in); got != want {
			t.Errorf("normalizeChatMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestListUpstreamAssistantsParsesList 验证智能体列表解析与必填参数。
func TestListUpstreamAssistantsParsesList(t *testing.T) {
	access := fakeToken("assist-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if body["page"] == nil || body["page_size"] == nil {
			t.Errorf("page/page_size are required upstream, got %v", body)
		}
		if got := r.Header.Get("X-Device-Id"); got != "assist-device" {
			t.Errorf("X-Device-Id must come from token payload, got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": 0, "message": "success",
			"result": map[string]any{
				"has_more": false,
				"assistant_list": []map[string]any{
					{"assistant_id": "aaaabbbbccccddddeeeeffff", "name": "我的助手"},
				},
			},
		})
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.AssistantListEndpoint = srv.URL
	got, err := listUpstreamAssistants(access, cfg)
	if err != nil {
		t.Fatalf("listUpstreamAssistants failed: %v", err)
	}
	if len(got) != 1 || got[0].AssistantID != "aaaabbbbccccddddeeeeffff" {
		t.Fatalf("unexpected assistants: %+v", got)
	}
}

// TestDiscoverChatModesFiltersUnknown 验证历史里的未知 chat_mode 被白名单丢弃。
// 这是 0.3.0 线上事故的回归测试：探测残留（zzz-bogus 等）曾被当成模式写进模型目录。
func TestDiscoverChatModesFiltersUnknown(t *testing.T) {
	resetModeCache()
	access := fakeToken("mode-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/assistant/list") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0, "result": map[string]any{"assistant_list": []any{}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": 0,
			"result": map[string]any{
				"has_more": false,
				"conversation_list": []map[string]any{
					{"id": "c1", "meta_data": map[string]any{"chat_mode": "normal"}},
					{"id": "c2", "meta_data": map[string]any{"chat_mode": "zero"}},
					{"id": "c3", "meta_data": map[string]any{"chat_mode": "zzz-bogus"}},
					{"id": "c4", "meta_data": map[string]any{"chat_mode": "___definitely_not_a_real_value___"}},
					{"id": "c5", "meta_data": map[string]any{"chat_mode": "turbo"}},
					{"id": "c6", "meta_data": map[string]any{"chat_mode": "zero"}},
				},
			},
		})
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.AssistantListEndpoint = srv.URL + "/assistant/list"
	cfg.ConversationListEndpoint = srv.URL + "/conversation/list"
	modes, err := discoverChatModes(access, cfg)
	if err != nil {
		t.Fatalf("discoverChatModes failed: %v", err)
	}
	want := []string{"normal", "zero"}
	if len(modes) != len(want) {
		t.Fatalf("unknown modes must be filtered, got %v want %v", modes, want)
	}
	for i, m := range want {
		if modes[i] != m {
			t.Fatalf("got %v want %v", modes, want)
		}
	}
}

// TestDynamicModelInfosIgnoresPollutedHistory 验证被污染的会话历史不会再扩出假模型。
// 线上 0.3.0 曾出现 glm/glm-5.3-zzz-bogus 等 19 个垃圾条目，此测试锁定该缺陷。
func TestDynamicModelInfosIgnoresPollutedHistory(t *testing.T) {
	resetModeCache()
	access := fakeToken("dyn-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/assistant/list") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0, "result": map[string]any{"assistant_list": []any{}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": 0,
			"result": map[string]any{
				"conversation_list": []map[string]any{
					{"id": "c1", "meta_data": map[string]any{"chat_mode": "zzz-bogus"}},
					{"id": "c2", "meta_data": map[string]any{"chat_mode": "___definitely_not_a_real_value___"}},
					{"id": "c3", "meta_data": map[string]any{"chat_mode": "glm-5.3"}},
				},
			},
		})
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.ExposeModeAliases = false
	cfg.AssistantListEndpoint = srv.URL + "/assistant/list"
	cfg.ConversationListEndpoint = srv.URL + "/conversation/list"
	infos, warnings := dynamicModelInfos(access, cfg)
	if len(warnings) != 0 {
		t.Fatalf("filtered modes must not warn, got %v", warnings)
	}
	if len(infos) != len(modelCatalog) {
		t.Fatalf("catalog must stay at %d real models, got %d: %+v",
			len(modelCatalog), len(infos), infos)
	}
	for _, info := range infos {
		if info.ID != "glm/glm-5.3" && info.ID != "glm/glm-5.3-flash" {
			t.Fatalf("polluted history leaked into catalog: %s", info.ID)
		}
	}
}

// TestAllModelEntriesWithoutModeAliases 验证默认配置下目录只有网页那两个模型。
func TestAllModelEntriesWithoutModeAliases(t *testing.T) {
	cfg := defaultConfig()
	cfg.ExposeModeAliases = false
	setConfigForTest(t, cfg)
	defer setConfigForTest(t, defaultConfig())
	entries := allModelEntries()
	if len(entries) != 2 {
		t.Fatalf("default catalog must be exactly 2 models, got %d", len(entries))
	}
	if entries[0].ID != "glm/glm-5.3" || entries[1].ID != "glm/glm-5.3-flash" {
		t.Fatalf("unexpected default catalog: %+v", entries)
	}
}

// TestAllModelEntriesWithModeAliases 验证开启开关后追加模式别名，且 ID 仍以真实模型为前缀。
func TestAllModelEntriesWithModeAliases(t *testing.T) {
	cfg := defaultConfig()
	cfg.ExposeModeAliases = true
	setConfigForTest(t, cfg)
	defer setConfigForTest(t, defaultConfig())
	entries := allModelEntries()
	if len(entries) != len(modelCatalog)*(1+len(chatModeCatalog)-1) {
		t.Fatalf("unexpected alias count: %d", len(entries))
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.ID, "glm/glm-5.3") {
			t.Fatalf("alias %q must derive from a real model", entry.ID)
		}
	}
}

// TestDynamicModelInfosFallsBackOnError 验证回源失败时仍返回真实模型目录。
func TestDynamicModelInfosFallsBackOnError(t *testing.T) {
	resetModeCache()
	access := fakeToken("assist-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500}`))
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.AssistantListEndpoint = srv.URL
	cfg.ConversationListEndpoint = srv.URL
	infos, warnings := dynamicModelInfos(access, cfg)
	if len(warnings) == 0 {
		t.Fatal("want warnings on upstream failure")
	}
	if len(infos) != len(allModelEntries()) {
		t.Fatalf("want %d entries, got %d", len(allModelEntries()), len(infos))
	}
}
