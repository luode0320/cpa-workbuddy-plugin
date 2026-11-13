package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// launchStub 启动一个假的上游模型清单端点。
//
// 真实上游 model_list 有 67 条但只有 26 个 distinct 名字（Aoe 出现 27 次，
// model_type 各不相同）。夹具刻意复现这个形态，用来验证按名字去重。
// [参数] t: 测试上下文；models: 返回的 model_list。
// [返回] 假服务端。
// 最近修改时间：2026-10-17；改动原因：新增，动态发现的上游桩。
func launchStub(t *testing.T, models []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("模型发现应使用 POST，实际 %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"model_list": models},
		})
	}))
}

// modelFixture 构造一条上游模型记录。
// [参数] name: 代号；modelType: 上游数字 ID。
// [返回] 上游记录。
// 最近修改时间：2026-10-17；改动原因：新增，夹具构造。
func modelFixture(name string, modelType int64) map[string]any {
	return map[string]any{
		"name":                      name,
		"model_name":                name,
		"model_type":                modelType,
		"support_pic_gen_switch":    false,
		"support_web_search_switch": false,
		"typewriter_switch":         false,
	}
}

// TestDiscoverUpstreamModels 校验能从上游拉取并去重模型清单。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，动态发现主链路回归。
func TestDiscoverUpstreamModels(t *testing.T) {
	srv := launchStub(t, []map[string]any{
		modelFixture("Aoe", 1737521813),
		modelFixture("Aoe", 1736929267),
		modelFixture("ogt", 1),
		modelFixture("anceba", 6),
	})
	defer srv.Close()
	defer swapLaunchEndpoint(srv.URL)()

	raw, errDiscover := discoverUpstreamModels(completionTestAccount(), 5*time.Second)
	if errDiscover != nil {
		t.Fatalf("模型发现不应报错：%v", errDiscover)
	}
	if len(raw) != 4 {
		t.Fatalf("原始记录应保留 4 条（含同名不同 ID），实际 %d", len(raw))
	}

	models := modelsFromUpstream(raw)
	if len(models) != 3 {
		t.Fatalf("按名字去重后应为 3 个模型，实际 %d", len(models))
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	joined := strings.Join(ids, ",")
	for _, want := range []string{"doubao/Aoe", "doubao/ogt", "doubao/anceba"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("应包含上游模型 %s，实际 %s", want, joined)
		}
	}
	if strings.Count(joined, "doubao/Aoe") != 1 {
		t.Fatalf("同名模型必须去重，实际 %s", joined)
	}
	// 排序保证下拉框稳定，避免每次刷新顺序抖动（按小写字典序）。
	if ids[0] != "doubao/anceba" {
		t.Fatalf("模型应按名字排序，实际首个为 %s", ids[0])
	}
}

// TestModelsFromUpstreamSkipsEmptyName 校验缺名字的记录被跳过。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，脏数据防护。
func TestModelsFromUpstreamSkipsEmptyName(t *testing.T) {
	models := modelsFromUpstream([]upstreamModel{
		{Name: "", ModelName: "", ModelType: 1},
		{Name: "  ", ModelType: 2},
		{Name: "ogt", ModelType: 3},
	})
	if len(models) != 1 {
		t.Fatalf("空名字应被跳过，实际保留 %d 条", len(models))
	}
	if models[0].ID != "doubao/ogt" {
		t.Fatalf("应保留有名字的记录，实际 %q", models[0].ID)
	}
}

// TestDiscoverUpstreamModelsRejectsBusinessError 校验上游业务错误被如实上报。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，错误路径回归。
func TestDiscoverUpstreamModelsRejectsBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":710012001,"data":{}}`))
	}))
	defer srv.Close()
	defer swapLaunchEndpoint(srv.URL)()

	if _, errDiscover := discoverUpstreamModels(completionTestAccount(), 5*time.Second); errDiscover == nil {
		t.Fatal("业务错误码必须报错，不能当成空清单")
	}
}

// TestDiscoverUpstreamModelsRejectsEmptyList 校验空清单被判定为失败。
//
// 空清单会让面板显示「暂无可用模型」，用户无法区分「上游没返回」与「账号真没模型」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，空清单防护。
func TestDiscoverUpstreamModelsRejectsEmptyList(t *testing.T) {
	srv := launchStub(t, nil)
	defer srv.Close()
	defer swapLaunchEndpoint(srv.URL)()

	if _, errDiscover := discoverUpstreamModels(completionTestAccount(), 5*time.Second); errDiscover == nil {
		t.Fatal("空模型清单必须报错")
	}
}

// TestCachedUpstreamModelsReusesPerAccount 校验缓存按账号隔离。
//
// 不同套餐的账号模型清单可能不同；跨账号复用会把 A 的模型暴露给 B。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，缓存隔离回归。
func TestCachedUpstreamModelsReusesPerAccount(t *testing.T) {
	resetModelCache()
	t.Cleanup(resetModelCache)

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"model_list": []map[string]any{modelFixture("ogt", 1)}},
		})
	}))
	defer srv.Close()
	defer swapLaunchEndpoint(srv.URL)()

	first := completionTestAccount()
	first.AuthID = "doubao-cache0001"
	if _, errModels := cachedUpstreamModels(first, false); errModels != nil {
		t.Fatalf("首次读取不应报错：%v", errModels)
	}
	if _, errModels := cachedUpstreamModels(first, false); errModels != nil {
		t.Fatalf("二次读取不应报错：%v", errModels)
	}
	if hits != 1 {
		t.Fatalf("同账号应命中缓存只打一次上游，实际 %d 次", hits)
	}

	second := completionTestAccount()
	second.AuthID = "doubao-cache0002"
	if _, errModels := cachedUpstreamModels(second, false); errModels != nil {
		t.Fatalf("换账号读取不应报错：%v", errModels)
	}
	if hits != 2 {
		t.Fatalf("换账号必须重新拉取，实际只打了 %d 次上游", hits)
	}

	// force 必须绕过缓存，供面板「刷新」使用。
	if _, errModels := cachedUpstreamModels(second, true); errModels != nil {
		t.Fatalf("强制刷新不应报错：%v", errModels)
	}
	if hits != 3 {
		t.Fatalf("force 应绕过缓存，实际 %d 次", hits)
	}
}

// TestModelForAuthMergesUpstreamAndAliases 校验按账号目录合并两类模型。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，宿主模型目录合并回归。
func TestModelForAuthMergesUpstreamAndAliases(t *testing.T) {
	resetModelCache()
	t.Cleanup(resetModelCache)

	srv := launchStub(t, []map[string]any{
		modelFixture("ogt", 1),
		modelFixture("default", 2), // 与能力别名同名，必须被别名占位
	})
	defer srv.Close()
	defer swapLaunchEndpoint(srv.URL)()

	sa := completionTestAccount()
	sa.AuthID = "doubao-merge00001"
	storage, _ := json.Marshal(sa)

	raw, errModel := modelForAuth(mustJSON(t, pluginapi.AuthModelRequest{
		AuthID:      sa.AuthID,
		StorageJSON: storage,
	}))
	if errModel != nil {
		t.Fatalf("按账号目录不应报错：%v", errModel)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil || !env.OK {
		t.Fatalf("信封异常：%v", errUnmarshal)
	}
	var resp pluginapi.ModelResponse
	if errUnmarshal := json.Unmarshal(env.Result, &resp); errUnmarshal != nil {
		t.Fatalf("结果解析失败：%v", errUnmarshal)
	}

	ids := make(map[string]int, len(resp.Models))
	for _, m := range resp.Models {
		ids[m.ID]++
	}
	for _, want := range []string{"doubao/default", "doubao/pro", "doubao/image", "doubao/video", "doubao/ogt"} {
		if ids[want] != 1 {
			t.Fatalf("模型 %s 应恰好出现一次，实际 %d（全部：%v）", want, ids[want], ids)
		}
	}
}

// mustJSON 把值编码成 JSON，失败即终止测试。
// [参数] t: 测试上下文；v: 待编码值。
// [返回] JSON 字节。
// 最近修改时间：2026-10-17；改动原因：新增，测试编码辅助。
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		t.Fatalf("编码失败：%v", errMarshal)
	}
	return raw
}
