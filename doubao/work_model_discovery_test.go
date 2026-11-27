package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// workMenuFixture 是一段工作模式菜单响应样例（字段形状取自真实上游）。
const workMenuFixture = `{"code":0,"data":{"entry_list":[{"active_switch_conf":{"use_menu_conf_v2":true,` +
	`"menu_conf_v2":{"mode_list":{"item_list":[{"agent_mode":1,"mode_id":"3","model_list":{"item_list":[` +
	`{"model_item_key":"person-glm-5-4","name":"GLM 5.4","model_extra_params":{"memory_profile":"300k_230","total_window_size":"300000"},"reasoning_effort_config":{"default_level":5}},` +
	`{"model_item_key":"person-qwen4-max","name":"Qwen 4 Max","model_extra_params":{"memory_profile":"300k_230","total_window_size":"300000"},"reasoning_effort_config":{"default_level":5}}` +
	`]}},{"agent_mode":2,"mode_id":"1","model_list":{"item_list":[{"model_item_key":"0","name":"快速"}]}}]}}}}]}}`

func init() {
	// 默认把两个端点指向不可达地址：测试不得打真实上游。
	workLaunchEndpointOverride = "http://127.0.0.1:1/"
	workBriefEndpointOverride = "http://127.0.0.1:1/"
}

// TestWorkAliasesFromMenu 校验菜单解析出工作模式模型并跳过其他模式。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 23:35:00；改动原因：新增，锁定菜单解析与模式过滤。
func TestWorkAliasesFromMenu(t *testing.T) {
	var resp workBriefResponse
	if err := json.Unmarshal([]byte(workMenuFixture), &resp); err != nil {
		t.Fatalf("样例应为合法响应：%v", err)
	}
	aliases := workAliasesFromMenu(resp)
	if len(aliases) != 2 {
		t.Fatalf("应只解析出 2 条工作模式模型，实际 %d", len(aliases))
	}
	byKey := make(map[string]modelAlias, len(aliases))
	for _, alias := range aliases {
		byKey[alias.ModelItemKey] = alias
	}
	glm, ok := byKey["person-glm-5-4"]
	if !ok {
		t.Fatal("应解析出 person-glm-5-4")
	}
	if glm.ID != "glm-5.4" {
		t.Errorf("别名应为 glm-5.4，实际 %q", glm.ID)
	}
	if glm.DisplayName != "GLM 5.4" {
		t.Errorf("展示名应为 GLM 5.4，实际 %q", glm.DisplayName)
	}
	if glm.MemoryProfile != "300k_230" || glm.TotalWindow != "300000" {
		t.Errorf("记忆档位与窗口解析错误：%+v", glm)
	}
	if glm.ReasoningEffort != "5" {
		t.Errorf("推理档位应取默认级别 5，实际 %q", glm.ReasoningEffort)
	}
	if _, exists := byKey["0"]; exists {
		t.Error("agent_mode=2 的模型不应进入工作模式目录")
	}
}

// TestWorkAliasIDFallsBackToKey 校验纯中文名退回模型键。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 23:35:00；改动原因：新增，锁定别名派生兜底。
func TestWorkAliasIDFallsBackToKey(t *testing.T) {
	if got := workAliasID("豆包 2.1 Pro", "5"); got != "5" {
		t.Errorf("纯中文名应退回模型键，期望 5，实际 %q", got)
	}
	if got := workAliasID("Qwen 4 Max", "person-qwen4-max"); got != "qwen-4-max" {
		t.Errorf("应派生 qwen-4-max，实际 %q", got)
	}
}

// TestDiscoverWorkModelsEndToEnd 校验菜单发现走通启动与菜单两个端点。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 23:35:00；改动原因：新增，端到端覆盖动态发现链路。
func TestDiscoverWorkModelsEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case workLaunchPath:
			if _, errWrite := w.Write([]byte(`{"code":0,"data":{"assistant_bot_id":7338286299411103781}}`)); errWrite != nil {
				t.Errorf("写入启动响应失败：%v", errWrite)
			}
		case workModelDiscoveryPath:
			var body map[string]string
			if errDecode := json.NewDecoder(r.Body).Decode(&body); errDecode != nil {
				t.Errorf("菜单请求体应为 JSON：%v", errDecode)
			}
			if body["bot_id"] == "" {
				t.Errorf("菜单请求必须带 bot_id，实际 %v", body)
			}
			if _, errWrite := w.Write([]byte(workMenuFixture)); errWrite != nil {
				t.Errorf("写入菜单响应失败：%v", errWrite)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	defer swapWorkLaunchEndpoint(server.URL + workLaunchPath)()
	defer swapWorkBriefEndpoint(server.URL + workModelDiscoveryPath)()

	aliases, err := discoverWorkModels(workTestAccount(), 0)
	if err != nil {
		t.Fatalf("发现应成功：%v", err)
	}
	if len(aliases) != 2 {
		t.Fatalf("应发现 2 条模型，实际 %d", len(aliases))
	}
}

// TestAllAliasesMergesDynamic 校验动态模型只补静态未收录的键。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 23:35:00；改动原因：新增，锁定静态优先去重语义。
func TestAllAliasesMergesDynamic(t *testing.T) {
	resetWorkModelCache()
	staticSize := len(capabilityAliases) + len(workModelAliases)
	if got := len(allAliases()); got != staticSize {
		t.Fatalf("无缓存时目录应等于静态条目数 %d，实际 %d", staticSize, got)
	}

	// 注入一条与静态同键、一条全新键，只有全新键应被补充。
	workAliasCache.mu.Lock()
	workAliasCache.aliases = []modelAlias{
		{ID: "glm-5.4", ModelItemKey: "person-glm-5-4"},
		{ID: "brand-new-model", ModelItemKey: "person-brand-new"},
	}
	workAliasCache.mu.Unlock()
	defer resetWorkModelCache()

	got := allAliases()
	if len(got) != staticSize+1 {
		t.Fatalf("应只补充 1 条新模型，实际 %d（基线 %d）", len(got), staticSize)
	}
	found := false
	for _, alias := range got {
		if alias.ModelItemKey == "person-brand-new" {
			found = true
		}
	}
	if !found {
		t.Error("应包含动态补充的新模型")
	}
}
