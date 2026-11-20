package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 本文件覆盖模型动态发现的纯逻辑：类型映射、离线过滤、去重、兜底目录。
// 真实网络链路由 models_live_test.go 的 TestLiveModelDiscovery 覆盖。

// modelListStub 模拟 /v1/model/list，按 model_type 返回预置数据。
//
// 发现链路会并发打各类型端点，calls 必须加锁：不加会在 -race 下报数据竞争。
type modelListStub struct {
	byType map[string][]upstreamModel
	// failTypes 中的类型返回业务错误码。
	failTypes map[string]bool
	mu        sync.Mutex
	calls     []string
}

// ServeHTTP 实现模型目录端点桩。
// [参数] w: 响应写入器；r: 请求。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，模型目录桩；calls 改为并发安全。
func (s *modelListStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ModelType string `json:"model_type"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	s.calls = append(s.calls, req.ModelType)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if s.failTypes[req.ModelType] {
		_, _ = w.Write([]byte(`{"code":700012006,"msg":"登录校验不合法"}`))
		return
	}
	resp := map[string]any{
		"code": 0,
		"msg":  "success",
		"data": map[string]any{"model_info_list": s.byType[req.ModelType]},
	}
	raw, _ := json.Marshal(resp)
	_, _ = w.Write(raw)
}

// multimodalStub 模拟多模态目录端点。
type multimodalStub struct {
	catalog multimodalCatalog
	fail    bool
}

// ServeHTTP 实现多模态目录桩。
// [参数] w: 响应写入器；r: 请求。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，多模态目录桩。
func (s *multimodalStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.fail {
		_, _ = w.Write([]byte(`{"code":1000001,"msg":"参数错误"}`))
		return
	}
	raw, _ := json.Marshal(s.catalog)
	_, _ = w.Write(raw)
}

// swapModelEndpoints 临时覆盖模型目录端点，返回恢复函数。
// [参数] listURL: 模型清单端点；catalogURL: 多模态目录端点。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-10-18；改动原因：新增，测试端点注入。
func swapModelEndpoints(listURL, catalogURL string) func() {
	prevList, prevCatalog := modelListOverride, multimodalOverride
	modelListOverride = listURL
	multimodalOverride = catalogURL
	return func() {
		modelListOverride = prevList
		multimodalOverride = prevCatalog
	}
}

// sampleLLM 构造一条对话模型。
// [参数] id: 模型 id。
// [返回] 上游模型。
// 最近修改时间：2026-10-18；改动原因：新增，测试数据构造。
func sampleLLM(id string) upstreamModel {
	return upstreamModel{
		ModelID:   id,
		ModelName: id,
		ShowName:  strings.ToUpper(id),
		ModelDesc: "测试模型",
		ModelType: modelTypeLLM,
	}
}

// TestDiscoverModels_MapsTypesAndSkipsOffline 验证类型映射与离线模型过滤。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，主链路用例。
func TestDiscoverModels_MapsTypesAndSkipsOffline(t *testing.T) {
	list := &modelListStub{
		byType: map[string][]upstreamModel{
			modelTypeLLM:   {sampleLLM("chat-a")},
			modelTypeImage: {{ModelID: "img-a", ModelName: "img-a", ModelType: modelTypeImage}},
			// 已下线模型不得出现在目录里。
			modelTypeVideo: {{ModelID: "video-gone", ModelName: "video-gone", IsOffline: true}},
		},
	}
	listSrv := httptest.NewServer(list)
	defer listSrv.Close()
	catalogSrv := httptest.NewServer(&multimodalStub{fail: true})
	defer catalogSrv.Close()
	defer swapModelEndpoints(listSrv.URL, catalogSrv.URL)()

	models, errDiscover := discoverModels(&StoredAuth{AuthID: "coze-x", CookieHeader: "sid=1"})
	if errDiscover != nil {
		t.Fatalf("发现应成功：%v", errDiscover)
	}

	byID := map[string]string{}
	for _, m := range models {
		byID[m.ID] = m.Type
	}
	if byID[modelIDPrefix+"chat-a"] != "chat" {
		t.Fatalf("对话模型类型应为 chat，实际 %q", byID[modelIDPrefix+"chat-a"])
	}
	if byID[modelIDPrefix+"img-a"] != openAIImageModelType {
		t.Fatalf("图像模型类型应为 %s，实际 %q", openAIImageModelType, byID[modelIDPrefix+"img-a"])
	}
	if _, exists := byID[modelIDPrefix+"video-gone"]; exists {
		t.Fatal("已下线模型不应登记")
	}
}

// TestDiscoverModels_PartialFailureStillReturns 验证单类型失败不阻断整体。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，部分失败容错用例。
func TestDiscoverModels_PartialFailureStillReturns(t *testing.T) {
	list := &modelListStub{
		byType: map[string][]upstreamModel{
			modelTypeLLM: {sampleLLM("chat-a")},
		},
		failTypes: map[string]bool{modelTypeImage: true},
	}
	listSrv := httptest.NewServer(list)
	defer listSrv.Close()
	catalogSrv := httptest.NewServer(&multimodalStub{fail: true})
	defer catalogSrv.Close()
	defer swapModelEndpoints(listSrv.URL, catalogSrv.URL)()

	models, errDiscover := discoverModels(&StoredAuth{AuthID: "coze-x", CookieHeader: "sid=1"})
	if errDiscover != nil {
		t.Fatalf("部分失败不应整体失败：%v", errDiscover)
	}
	if len(models) != 1 {
		t.Fatalf("应只保留成功类别的模型，实际 %d", len(models))
	}
}

// TestDiscoverModels_AllFailReturnsError 验证全部失败时如实报错。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，全失败用例。
func TestDiscoverModels_AllFailReturnsError(t *testing.T) {
	failAll := map[string]bool{}
	for _, entry := range modelListTypes {
		failAll[entry.Upstream] = true
	}
	list := &modelListStub{byType: map[string][]upstreamModel{}, failTypes: failAll}
	listSrv := httptest.NewServer(list)
	defer listSrv.Close()
	catalogSrv := httptest.NewServer(&multimodalStub{fail: true})
	defer catalogSrv.Close()
	defer swapModelEndpoints(listSrv.URL, catalogSrv.URL)()

	if _, errDiscover := discoverModels(&StoredAuth{AuthID: "coze-x", CookieHeader: "sid=1"}); errDiscover == nil {
		t.Fatal("全部失败必须返回错误，不得伪造空目录为成功")
	}
}

// TestDiscoverModels_MultimodalAddsVideoFamily 验证多模态目录补齐视频模型族。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，视频族补充用例。
func TestDiscoverModels_MultimodalAddsVideoFamily(t *testing.T) {
	list := &modelListStub{byType: map[string][]upstreamModel{modelTypeLLM: {sampleLLM("chat-a")}}}
	listSrv := httptest.NewServer(list)
	defer listSrv.Close()

	var catalog multimodalCatalog
	catalog.Code = 0
	catalog.Data.Capabilities = []struct {
		CapabilityType   string `json:"capability_type"`
		DisplayName      string `json:"display_name"`
		DefaultModelName string `json:"default_model_name"`
		Models           []struct {
			ModelName    string `json:"model_name"`
			DisplayName  string `json:"display_name"`
			Description  string `json:"description"`
			UserLevelGte int    `json:"user_level_gte"`
		} `json:"models"`
	}{
		{
			CapabilityType: "generate_video",
			DisplayName:    "视频生成",
			Models: []struct {
				ModelName    string `json:"model_name"`
				DisplayName  string `json:"display_name"`
				Description  string `json:"description"`
				UserLevelGte int    `json:"user_level_gte"`
			}{
				{ModelName: "seedance2.5_pro", DisplayName: "Seedance2.5"},
			},
		},
	}
	catalogSrv := httptest.NewServer(&multimodalStub{catalog: catalog})
	defer catalogSrv.Close()
	defer swapModelEndpoints(listSrv.URL, catalogSrv.URL)()

	models, errDiscover := discoverModels(&StoredAuth{AuthID: "coze-x", CookieHeader: "sid=1"})
	if errDiscover != nil {
		t.Fatalf("发现应成功：%v", errDiscover)
	}
	found := false
	for _, m := range models {
		if m.ID == modelIDPrefix+"seedance2.5_pro" && m.Type == "video" {
			found = true
		}
	}
	if !found {
		t.Fatalf("多模态目录的视频模型应被补充，实际 %+v", models)
	}
}

// TestStaticFallbackModels_NotEmpty 验证无账号时兜底目录非空。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，兜底目录用例。
func TestStaticFallbackModels_NotEmpty(t *testing.T) {
	models := staticFallbackModels()
	if len(models) == 0 {
		t.Fatal("兜底目录不能为空，否则用户授权前没有模型可选")
	}
	hasChat := false
	for _, m := range models {
		if m.Type == "chat" {
			hasChat = true
		}
		if !strings.HasPrefix(m.ID, modelIDPrefix) {
			t.Fatalf("模型 id 必须带 %q 前缀，实际 %q", modelIDPrefix, m.ID)
		}
	}
	if !hasChat {
		t.Fatal("兜底目录必须至少包含一个对话模型")
	}
}

// TestStaticFallbackModels_SkipsOfflineSeed 验证兜底目录不再包含上游已下线的模型。
//
// 兜底种子会被宿主原样登记进全局模型目录，一旦写了上游已下线的模型，
// 用户选中它只会拿到 503 auth_not_found（2026-11-20 生产实证：
// coze/doubao-seed-2-0-pro-260215 仍在目录里、直连必然 503）。
// 因此把「已下线」名单固定成断言，种子再被改回旧值时必须 FAIL。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-20；改动原因：新增，锁定下线模型不得回到兜底目录。
func TestStaticFallbackModels_SkipsOfflineSeed(t *testing.T) {
	offline := map[string]bool{
		modelIDPrefix + "doubao-seed-2-0-pro-260215": true,
	}
	for _, model := range staticFallbackModels() {
		if offline[model.ID] {
			t.Errorf("兜底目录不得包含上游已下线的模型 %q，选中它必然 503", model.ID)
		}
	}
}

// TestStaticFallbackModels_HasTurboChat 验证旗舰位由现役 turbo 模型顶上。
//
// 上游 llm 类型（2026-11-20 实测）只剩 turbo-260628 / lite / mini 三条，
// 兜底目录必须至少提供其中的旗舰位，否则用户授权前拿不到最强对话模型。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-20；改动原因：新增，锁定旗舰兜底模型。
func TestStaticFallbackModels_HasTurboChat(t *testing.T) {
	want := modelIDPrefix + "doubao-seed-2-1-turbo-260628"
	for _, model := range staticFallbackModels() {
		if model.ID == want {
			if model.Type != "chat" {
				t.Fatalf("%q 类型为 %q，期望 chat", model.ID, model.Type)
			}
			return
		}
	}
	t.Fatalf("兜底目录缺少现役旗舰对话模型 %q", want)
}

// TestModelStatic_ReturnsFallbackWithoutAccounts 验证无账号时 model.static 可用。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，宿主启动期调用用例。
func TestModelStatic_ReturnsFallbackWithoutAccounts(t *testing.T) {
	setAccountsDir(t.TempDir())
	defer setAccountsDir("")

	raw, errStatic := modelStatic(nil)
	if errStatic != nil {
		t.Fatalf("model.static 不应失败：%v", errStatic)
	}
	if !strings.Contains(string(raw), `"Models"`) {
		t.Fatalf("响应应含 Models 字段：%s", raw)
	}
}

// TestHostTypeForCapability 验证能力类型映射。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，映射用例。
func TestHostTypeForCapability(t *testing.T) {
	cases := map[string]string{
		"image_gen":      openAIImageModelType,
		"generate_video": "video",
		"unknown":        "",
	}
	for in, want := range cases {
		if got := hostTypeForCapability(in); got != want {
			t.Fatalf("%s 应映射为 %q，实际 %q", in, want, got)
		}
	}
}

// TestCachedDiscoveredModels_IsolatedPerAccount 验证缓存按账号隔离。
//
// 单条缓存时多账号会互相覆盖，表现为每次 for_auth 都重新打上游；
// 这里用两次调用断言第二次命中缓存（上游只被打一轮）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，多账号缓存隔离用例。
func TestCachedDiscoveredModels_IsolatedPerAccount(t *testing.T) {
	list := &modelListStub{byType: map[string][]upstreamModel{modelTypeLLM: {sampleLLM("chat-a")}}}
	listSrv := httptest.NewServer(list)
	defer listSrv.Close()
	catalogSrv := httptest.NewServer(&multimodalStub{fail: true})
	defer catalogSrv.Close()
	defer swapModelEndpoints(listSrv.URL, catalogSrv.URL)()
	resetModelCatalogCache()
	defer resetModelCatalogCache()

	a := &StoredAuth{AuthID: "coze-a", CookieHeader: "sid=1"}
	b := &StoredAuth{AuthID: "coze-b", CookieHeader: "sid=2"}
	if _, errA := cachedDiscoveredModels(a, false); errA != nil {
		t.Fatalf("账号 A 发现失败：%v", errA)
	}
	list.mu.Lock()
	afterFirst := len(list.calls)
	list.mu.Unlock()

	if _, errB := cachedDiscoveredModels(b, false); errB != nil {
		t.Fatalf("账号 B 发现失败：%v", errB)
	}
	list.mu.Lock()
	afterSecond := len(list.calls)
	list.mu.Unlock()
	if afterSecond <= afterFirst {
		t.Fatal("账号 B 未被单独缓存，说明缓存未按账号隔离")
	}

	// 再次调用账号 A 应命中缓存，不再新增上游请求。
	if _, errA2 := cachedDiscoveredModels(a, false); errA2 != nil {
		t.Fatalf("账号 A 二次发现失败：%v", errA2)
	}
	list.mu.Lock()
	afterThird := len(list.calls)
	list.mu.Unlock()
	if afterThird != afterSecond {
		t.Fatalf("账号 A 二次调用应命中缓存，上游调用从 %d 增至 %d", afterSecond, afterThird)
	}
}
