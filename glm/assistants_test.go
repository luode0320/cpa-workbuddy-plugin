package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

// TestDynamicModelInfosAppendsAssistants 验证预置档位保留、自建智能体按 glm/<id> 追加且不重复。
func TestDynamicModelInfosAppendsAssistants(t *testing.T) {
	access := fakeToken("assist-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.AssistantListEndpoint = srv.URL
	infos, warnings := dynamicModelInfos(access, cfg)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(infos) != len(modelCatalog)+1 {
		t.Fatalf("want %d models (catalog + 1 dynamic), got %d", len(modelCatalog)+1, len(infos))
	}
	last := infos[len(infos)-1]
	if last.ID != "glm/aaaabbbbccccddddeeeeffff" {
		t.Fatalf("dynamic model id = %q", last.ID)
	}
	if last.DisplayName != "我的助手（自建智能体）" {
		t.Fatalf("dynamic display name = %q", last.DisplayName)
	}
}

// TestDynamicModelInfosFallsBackOnError 验证回源失败时降级为纯预置目录并给出 warning。
func TestDynamicModelInfosFallsBackOnError(t *testing.T) {
	access := fakeToken("assist-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500}`))
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.AssistantListEndpoint = srv.URL
	infos, warnings := dynamicModelInfos(access, cfg)
	if len(infos) != len(modelCatalog) {
		t.Fatalf("want fallback to catalog (%d), got %d", len(modelCatalog), len(infos))
	}
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %v", warnings)
	}
}

// TestResolveAssistantIDSplitsGLMPrefix 验证 glm/<assistant_id> 解析回裸 ID，
// 且档位判断不受 glm/ 前缀影响。
func TestResolveAssistantIDSplitsGLMPrefix(t *testing.T) {
	cases := []struct {
		model    string
		wantID   string
		wantMode string
	}{
		{"glm/65940acff94777010aa6b796", "65940acff94777010aa6b796", ""},
		{"65940acff94777010aa6b796", "65940acff94777010aa6b796", ""},
		{"glm/glm-4-plus", defaultAssistantID, ""},
		{"glm/glm-think", defaultAssistantID, "zero"},
		{"glm/glm-deepresearch", defaultAssistantID, "deep_research"},
		{"glm/glm-5.1", defaultAssistantID, ""},
	}
	for _, tc := range cases {
		gotID, gotMode := resolveAssistantID(tc.model)
		if gotID != tc.wantID || gotMode != tc.wantMode {
			t.Errorf("%s: got (%s,%s) want (%s,%s)",
				tc.model, gotID, gotMode, tc.wantID, tc.wantMode)
		}
	}
}

// TestModelCatalogMarksEquivalentAliases 锁定目录契约：等价别名必须在 DisplayName 里如实标注，
// 避免再次出现「看起来五个模型、实际三档」的误导。
func TestModelCatalogMarksEquivalentAliases(t *testing.T) {
	want := map[string]string{
		"glm/glm-4-plus":       "GLM 标准",
		"glm/glm-think":        "GLM 思考",
		"glm/glm-deepresearch": "GLM 深度研究",
	}
	for _, entry := range modelCatalog {
		if label, ok := want[entry.ID]; ok {
			if entry.DisplayName != label {
				t.Errorf("%s DisplayName = %q, want %q", entry.ID, entry.DisplayName, label)
			}
			delete(want, entry.ID)
		}
	}
	if len(want) != 0 {
		t.Errorf("catalog missing entries: %v", want)
	}
	// 等价别名必须写明同档，否则又变成误导。
	for _, entry := range modelCatalog {
		if entry.ID == "glm/glm-5.1" || entry.ID == "glm/glm-4.7" {
			if entry.DisplayName == entry.ID {
				t.Errorf("%s must not display bare id as name", entry.ID)
			}
		}
	}
}
