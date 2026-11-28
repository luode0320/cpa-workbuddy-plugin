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

// TestDiscoverChatModesFromHistory 验证从历史会话发现 chat_mode。
func TestDiscoverChatModesFromHistory(t *testing.T) {
	resetModeCache()
	access := fakeToken("mode-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/assistant/list"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0, "result": map[string]any{"assistant_list": []any{}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0,
				"result": map[string]any{
					"has_more": false,
					"conversation_list": []map[string]any{
						{"id": "c1", "meta_data": map[string]any{"chat_mode": "normal"}},
						{"id": "c2", "meta_data": map[string]any{"chat_mode": "zero"}},
						{"id": "c3", "meta_data": map[string]any{"chat_mode": "brand_new_mode"}},
						{"id": "c4", "meta_data": map[string]any{"chat_mode": "zero"}},
					},
				},
			})
		}
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.AssistantListEndpoint = srv.URL + "/assistant/list"
	cfg.ConversationListEndpoint = srv.URL + "/conversation/list"
	modes, err := discoverChatModes(access, cfg)
	if err != nil {
		t.Fatalf("discoverChatModes failed: %v", err)
	}
	want := []string{"normal", "zero", "brand_new_mode"}
	if len(modes) != len(want) {
		t.Fatalf("got %v want %v", modes, want)
	}
	for i, m := range want {
		if modes[i] != m {
			t.Fatalf("got %v want %v", modes, want)
		}
	}
}

// TestDynamicModelInfosKeepsRealModels 验证动态目录始终包含两个真实模型，
// 且新发现的模式只作为后缀扩展，不会新增模型。
func TestDynamicModelInfosKeepsRealModels(t *testing.T) {
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
					{"id": "c1", "meta_data": map[string]any{"chat_mode": "glm_next_gen"}},
				},
			},
		})
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.AssistantListEndpoint = srv.URL + "/assistant/list"
	cfg.ConversationListEndpoint = srv.URL + "/conversation/list"
	infos, warnings := dynamicModelInfos(access, cfg)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	var hasGLM53, hasFlash, hasNewMode bool
	for _, info := range infos {
		switch info.ID {
		case "glm/glm-5.3":
			hasGLM53 = true
		case "glm/glm-5.3-flash":
			hasFlash = true
		case "glm/glm-5.3-glm_next_gen":
			hasNewMode = true
		}
	}
	if !hasGLM53 || !hasFlash {
		t.Fatal("both real models must always be present")
	}
	if !hasNewMode {
		t.Fatal("discovered mode must extend the real model, not add a fake model")
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
