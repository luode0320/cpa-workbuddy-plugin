package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// catalogueSize 返回目录应有条目数（能力别名，假设无重名）。
// [参数] 无。
// [返回] 条目数。
// 最近修改时间：2026-11-18 23:00:00；改动原因：图片能力删除，目录只剩视频一条。
func catalogueSize() int {
	return len(capabilityAliases) + len(workModelAliases)
}

// TestCatalogueAliases 校验目录暴露能力别名与工作模式模型。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:30:00；改动原因：恢复工作模式模型目录，断言覆盖两类别名。
func TestCatalogueAliases(t *testing.T) {
	models := catalogueModels()
	if len(models) != catalogueSize() {
		t.Fatalf("应暴露 %d 个别名，实际 %d", catalogueSize(), len(models))
	}
	got := make(map[string]bool, len(models))
	for _, m := range models {
		got[m.ID] = true
		if m.Object != "model" {
			t.Errorf("%s 的 object 应为 model，实际 %q", m.ID, m.Object)
		}
		if m.OwnedBy != providerName {
			t.Errorf("%s 的 owned_by 应为 %s，实际 %q", m.ID, providerName, m.OwnedBy)
		}
		if m.DisplayName == "" || m.Description == "" {
			t.Errorf("%s 缺少展示名或描述", m.ID)
		}
	}
	if !got["doubao/video"] {
		t.Errorf("目录缺少能力别名 doubao/video")
	}
	for _, alias := range workModelAliases {
		if !got[modelIDPrefix+alias.ID] {
			t.Errorf("目录缺少工作模式别名 %s", modelIDPrefix+alias.ID)
		}
	}
}

// TestCatalogueAliasTypes 校验别名在 /v1/models 中的类型标记。
//
// 宿主按 Type 决定客户端如何展示与调用：生视频被标成 chat 会让客户端当成对话
// 模型，把视频地址塞进正文；工作模式模型是文本对话，标 text。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:30:00；改动原因：恢复工作模式模型，补文本类型标记断言。
func TestCatalogueAliasTypes(t *testing.T) {
	for _, m := range catalogueModels() {
		want := "chat"
		if m.ID == modelIDPrefix+"video" {
			want = "video"
		}
		if m.Type != want {
			t.Errorf("%s 的 type 应为 %s，实际 %q", m.ID, want, m.Type)
		}
	}
}

// TestModeForModel 校验模型名到生成模式的严格解析。
//
// 目录外一律返回 ok=false：静默回落会让调用方把「模型名写错」误判成
// 「账号不可用」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:30:00；改动原因：恢复工作模式模型解析，断言两类别名。
func TestModeForModel(t *testing.T) {
	if m, ok := modeForModel("doubao/video"); !ok || m.Skill != skillVideoGen || m.Kind != "video" {
		t.Errorf("doubao/video 应解析为生视频模式，实际 %+v ok=%v", m, ok)
	}
	// 工作模式模型名必须解析为工作模式并带 model_item_key。
	for _, alias := range workModelAliases {
		m, ok := modeForModel(modelIDPrefix + alias.ID)
		if !ok {
			t.Errorf("%s 应可解析", modelIDPrefix+alias.ID)
			continue
		}
		if !m.WorkMode {
			t.Errorf("%s 应走工作模式，实际 %+v", modelIDPrefix+alias.ID, m)
		}
		if m.ModelItemKey != alias.ModelItemKey {
			t.Errorf("%s 的 model_item_key 应为 %q，实际 %q", modelIDPrefix+alias.ID, alias.ModelItemKey, m.ModelItemKey)
		}
	}
	// 已删除的图片别名、上游内部代号与空名都必须判失败。
	for _, name := range []string{"doubao/image", "some-internal-code", ""} {
		if m, ok := modeForModel(name); ok {
			t.Errorf("%q 不在目录内，必须判失败，实际 %+v", name, m)
		}
	}
}

// TestNormalizeModelName 校验模型名归一化。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:00:00；改动原因：目录只剩 video，用例改用 video。
func TestNormalizeModelName(t *testing.T) {
	cases := map[string]string{
		"doubao/video":        "video",
		"doubao/video:latest": "video",
		"  video  ":           "video",
		"video":               "video",
	}
	for in, want := range cases {
		if got := normalizeModelName(in); got != want {
			t.Errorf("归一化 %q 应为 %q，实际 %q", in, want, got)
		}
	}
}

// TestFilterExcludedModels 校验宿主排除配置生效。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:00:00；改动原因：图片能力删除，排除用例只剩 doubao/video。
func TestFilterExcludedModels(t *testing.T) {
	all := catalogueModels()

	// 未配置排除项时原样返回。
	if got := filterExcludedModels(all, pluginapi.HostConfigSummary{}); len(got) != len(all) {
		t.Fatalf("未配置排除项时不应过滤，实际 %d 条", len(got))
	}

	host := pluginapi.HostConfigSummary{
		ExcludedModels: map[string][]string{providerName: {"doubao/video", "doubao/image"}},
	}
	got := filterExcludedModels(all, host)
	if len(got) != len(all)-1 {
		t.Fatalf("应过滤掉 1 条，实际剩 %d 条", len(got))
	}
	for _, m := range got {
		if m.ID == "doubao/video" {
			t.Errorf("被排除的模型仍在目录中：%s", m.ID)
		}
	}

	// 入参切片不得被就地修改（共享缓存防护）。
	if len(all) != catalogueSize() {
		t.Errorf("过滤不得修改入参切片，实际长度 %d", len(all))
	}

	// provider 键大小写不同也应生效。
	host2 := pluginapi.HostConfigSummary{
		ExcludedModels: map[string][]string{"DOUBAO-PROVIDER": {"doubao/video"}},
	}
	if got := filterExcludedModels(all, host2); len(got) != len(all)-1 {
		t.Errorf("provider 键大小写不敏感匹配应生效，实际剩 %d 条", len(got))
	}
}

// TestModelStaticWithoutCredential 校验无凭据时仍返回完整目录。
//
// 宿主在启动期调用 model.static，此时可能还没有任何账号；目录必须照常返回，
// 否则用户在授权前没有模型可选。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，启动期可用性验证。
func TestModelStaticWithoutCredential(t *testing.T) {
	raw, err := modelStatic(nil)
	if err != nil {
		t.Fatalf("无凭据时不应报错：%v", err)
	}
	var env struct {
		OK     bool                    `json:"ok"`
		Result pluginapi.ModelResponse `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("响应应为合法信封：%v", err)
	}
	if !env.OK {
		t.Fatal("无凭据时 model.static 应返回成功")
	}
	if env.Result.Provider != providerName {
		t.Errorf("provider 应为 %s，实际 %q", providerName, env.Result.Provider)
	}
	if len(env.Result.Models) != catalogueSize() {
		t.Fatalf("应返回 %d 个模型，实际 %d", catalogueSize(), len(env.Result.Models))
	}
}

// TestModelStaticAppliesExclusions 校验 model.static 出口自行过滤排除项。
//
// 宿主不会替 model.static 过滤，插件必须在出口处理。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，排除语义在静态路径生效。
func TestModelStaticAppliesExclusions(t *testing.T) {
	req, _ := json.Marshal(pluginapi.StaticModelRequest{
		Host: pluginapi.HostConfigSummary{
			ExcludedModels: map[string][]string{providerName: {"doubao/video"}},
		},
	})
	raw, err := modelStatic(req)
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	var env struct {
		Result pluginapi.ModelResponse `json:"result"`
	}
	_ = json.Unmarshal(raw, &env)
	if len(env.Result.Models) != catalogueSize()-1 {
		t.Fatalf("排除后应剩 %d 个模型，实际 %d", catalogueSize()-1, len(env.Result.Models))
	}
	for _, m := range env.Result.Models {
		if strings.EqualFold(m.ID, "doubao/video") {
			t.Error("被排除的 doubao/video 仍在目录中")
		}
	}
}

// TestModelForAuthReturnsCatalogue 校验按账号路径返回同一套目录。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，按账号目录一致性。
func TestModelForAuthReturnsCatalogue(t *testing.T) {
	req, _ := json.Marshal(pluginapi.AuthModelRequest{
		AuthID:       doubaoAuthFilePrefix + "test",
		AuthProvider: providerName,
	})
	raw, err := modelForAuth(req)
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	var env struct {
		Result pluginapi.ModelResponse `json:"result"`
	}
	_ = json.Unmarshal(raw, &env)
	if len(env.Result.Models) != catalogueSize() {
		t.Fatalf("应返回 %d 个模型，实际 %d", catalogueSize(), len(env.Result.Models))
	}
}

// TestDefaultModelIDIsTestable 校验默认模型别名属于可测试目录。
//
// test-active 未指定模型时会拼出 modelIDPrefix+defaultModelID：该名字必须能在
// testableAliases 里命中，否则面板点「测试」会直接报「不支持的测试模型」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:00:00；改动原因：默认别名改为 video，用例随之验证。
func TestDefaultModelIDIsTestable(t *testing.T) {
	if defaultModelID != "video" {
		t.Errorf("默认别名应为 video，实际 %q", defaultModelID)
	}
	if _, ok := aliasForTestModel(modelIDPrefix + defaultModelID); !ok {
		t.Fatalf("默认别名 %s 必须可测试，实际不可测", modelIDPrefix+defaultModelID)
	}
}
