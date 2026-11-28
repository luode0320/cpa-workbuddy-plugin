package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// resetModeCache 清空档位发现缓存，避免测试间互相影响。
func resetModeCache() {
	modeDiscoveryCache.mu.Lock()
	modeDiscoveryCache.entries = make(map[string]*modeCacheEntry)
	modeDiscoveryCache.mu.Unlock()
}

// TestListUpstreamAssistantsParsesList 验证智能体列表端点被正确解析，
// 并断言请求带上了上游必填的 page/page_size 与载荷派生的 X-Device-Id。
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
		if r.Header.Get("X-Sign") == "" {
			t.Errorf("missing sign header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  0,
			"message": "success",
			"result": map[string]any{
				"has_more": false,
				"assistant_list": []map[string]any{
					{
						"assistant_id": "aaaabbbbccccddddeeeeffff",
						"name":         "我的助手",
						"description":  "自建智能体",
						"chat_mode":    "normal",
					},
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
	if len(got) != 1 {
		t.Fatalf("want 1 assistant, got %d", len(got))
	}
	if got[0].AssistantID != "aaaabbbbccccddddeeeeffff" || got[0].Name != "我的助手" {
		t.Fatalf("unexpected assistant: %+v", got[0])
	}
}

// TestDiscoverChatModesFromHistory 验证从历史会话中发现 chat_mode，
// 这是模型目录动态化的核心：上游新增档位会先出现在历史里。
func TestDiscoverChatModesFromHistory(t *testing.T) {
	resetModeCache()
	access := fakeToken("mode-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/assistant/list"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0, "result": map[string]any{"assistant_list": []any{}},
			})
		case strings.Contains(r.URL.Path, "/conversation/list"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0,
				"result": map[string]any{
					"has_more": false,
					"conversation_list": []map[string]any{
						{"id": "c1", "meta_data": map[string]any{"chat_mode": "normal"}},
						{"id": "c2", "meta_data": map[string]any{"chat_mode": "zero"}},
						{"id": "c3", "meta_data": map[string]any{"chat_mode": "chat_agent"}},
						{"id": "c4", "meta_data": map[string]any{"chat_mode": "brand_new_mode"}},
						{"id": "c5", "meta_data": map[string]any{"chat_mode": "zero"}},
					},
				},
			})
		default:
			http.NotFound(w, r)
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
	want := []string{"normal", "zero", "chat_agent", "brand_new_mode"}
	if len(modes) != len(want) {
		t.Fatalf("got %v, want %v", modes, want)
	}
	for i, m := range want {
		if modes[i] != m {
			t.Fatalf("got %v, want %v", modes, want)
		}
	}
}

// TestEffectiveChatModesUnionsBaselineAndDiscovered 验证基线与发现结果求并集且去重保序。
func TestEffectiveChatModesUnionsBaselineAndDiscovered(t *testing.T) {
	got := effectiveChatModes([]string{"zero", "chat_agent", "future_mode"})
	seen := map[string]int{}
	for _, m := range got {
		seen[m]++
	}
	for _, base := range knownChatModes {
		if seen[base] != 1 {
			t.Errorf("baseline mode %q count=%d, want 1", base, seen[base])
		}
	}
	if seen["future_mode"] != 1 {
		t.Errorf("discovered new mode must be included once, got %d", seen["future_mode"])
	}
	// 基线的顺序必须保持在最前，保证目录稳定。
	for i, base := range knownChatModes {
		if got[i] != base {
			t.Errorf("position %d = %q, want baseline %q", i, got[i], base)
		}
	}
}

// TestDynamicModelInfosIncludesDiscoveredModes 验证新发现的档位自动进入目录，
// 且不需要改任何代码 —— 这是用户要求的「未来新增模型不用改插件」。
func TestDynamicModelInfosIncludesDiscoveredModes(t *testing.T) {
	resetModeCache()
	access := fakeToken("dyn-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/assistant/list"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0, "result": map[string]any{"assistant_list": []any{}},
			})
		case strings.Contains(r.URL.Path, "/conversation/list"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0,
				"result": map[string]any{
					"conversation_list": []map[string]any{
						{"id": "c1", "meta_data": map[string]any{"chat_mode": "glm_next_gen"}},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.AssistantListEndpoint = srv.URL + "/assistant/list"
	cfg.ConversationListEndpoint = srv.URL + "/conversation/list"
	infos, warnings := dynamicModelInfos(access, cfg)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	var found bool
	for _, info := range infos {
		if info.ID == "glm/glm_next_gen" {
			found = true
			if info.DisplayName != "GLM glm_next_gen" {
				t.Errorf("unknown mode display = %q", info.DisplayName)
			}
		}
	}
	if !found {
		t.Fatalf("discovered mode must appear in catalog, got %d entries", len(infos))
	}
}

// TestDynamicModelInfosAppendsAssistants 验证自建智能体按 glm/<id> 追加且不重复。
func TestDynamicModelInfosAppendsAssistants(t *testing.T) {
	resetModeCache()
	access := fakeToken("assist-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/assistant/list"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0,
				"result": map[string]any{
					"assistant_list": []map[string]any{
						{"assistant_id": "aaaabbbbccccddddeeeeffff", "name": "我的助手"},
						{"assistant_id": "", "name": "空 ID 应被跳过"},
						{"assistant_id": "aaaabbbbccccddddeeeeffff", "name": "重复项"},
					},
				},
			})
		case strings.Contains(r.URL.Path, "/conversation/list"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0, "result": map[string]any{"conversation_list": []any{}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.AssistantListEndpoint = srv.URL + "/assistant/list"
	cfg.ConversationListEndpoint = srv.URL + "/conversation/list"
	infos, warnings := dynamicModelInfos(access, cfg)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	var dyn *pluginapi.ModelInfo
	for i := range infos {
		if infos[i].ID == "glm/aaaabbbbccccddddeeeeffff" {
			dyn = &infos[i]
		}
	}
	if dyn == nil {
		t.Fatal("dynamic assistant model missing")
	}
	if dyn.DisplayName != "我的助手（自建智能体）" {
		t.Fatalf("dynamic display name = %q", dyn.DisplayName)
	}
}

// TestDynamicModelInfosFallsBackOnError 验证回源失败时降级为基线目录并给出 warning。
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
	if len(infos) != len(baselineModelInfos()) {
		t.Fatalf("want baseline fallback (%d), got %d", len(baselineModelInfos()), len(infos))
	}
}

// TestResolveAssistantIDMapsModes 验证模型名到 chat_mode 的映射，
// 含 chat_agent 与未来未知档位（必须原样透传而不是丢弃）。
func TestResolveAssistantIDMapsModes(t *testing.T) {
	cases := []struct {
		model    string
		wantID   string
		wantMode string
	}{
		{"glm/chat_agent", defaultAssistantID, "chat_agent"},
		{"chat_agent", defaultAssistantID, "chat_agent"},
		{"glm/future_mode", defaultAssistantID, "future_mode"},
		{"glm/glm-think", defaultAssistantID, "zero"},
		{"glm/glm-deepresearch", defaultAssistantID, "deep_research"},
		{"glm/glm-4-plus", defaultAssistantID, ""},
		{"glm/glm-5.1", defaultAssistantID, ""},
		{"glm/65940acff94777010aa6b796", "65940acff94777010aa6b796", ""},
	}
	for _, tc := range cases {
		gotID, gotMode := resolveAssistantID(tc.model)
		if gotID != tc.wantID || gotMode != tc.wantMode {
			t.Errorf("%s: got (%s,%s) want (%s,%s)",
				tc.model, gotID, gotMode, tc.wantID, tc.wantMode)
		}
	}
}

// TestChatModeModelIDRoundTrip 验证档位到模型名再到档位可无损往返。
func TestChatModeModelIDRoundTrip(t *testing.T) {
	for _, mode := range []string{"", "zero", "chat_agent", "deep_research", "brand_new"} {
		id := chatModeModelID(mode)
		_, gotMode := resolveAssistantID(id)
		wantMode := mode
		if mode == "" {
			wantMode = ""
		}
		if gotMode != wantMode {
			t.Errorf("mode %q -> id %q -> mode %q, want %q", mode, id, gotMode, wantMode)
		}
	}
}
