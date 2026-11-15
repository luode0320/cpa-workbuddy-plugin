package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// catalogueSize 返回目录应有条目数（能力别名 + 工作模式模型，假设无重名）。
// [参数] 无。
// [返回] 条目数。
// 最近修改时间：2026-11-14 03:30:00；改动原因：新增，目录数量断言不再硬编码。
func catalogueSize() int {
	return len(capabilityAliases) + len(workModelAliases)
}

// TestCatalogueAliases 校验目录覆盖四个能力别名与全部工作模式模型。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:30:00；改动原因：目录合并工作模式模型，断言改为按目录长度校验。
func TestCatalogueAliases(t *testing.T) {
	models := catalogueModels()
	if len(models) != catalogueSize() {
		t.Fatalf("应暴露 %d 个别名，实际 %d", catalogueSize(), len(models))
	}
	want := []string{"doubao/default", "doubao/pro", "doubao/image", "doubao/video"}
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
	for _, id := range want {
		if !got[id] {
			t.Errorf("目录缺少别名 %s", id)
		}
	}
	// 工作模式模型必须逐条出现，否则客户端选不到该模型。
	for _, alias := range workModelAliases {
		if !got[modelIDPrefix+alias.ID] {
			t.Errorf("目录缺少工作模式模型 %s", modelIDPrefix+alias.ID)
		}
	}
}

// TestSkillForModel 校验别名到上游 skill 的映射。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，skill 映射回归。
func TestSkillForModel(t *testing.T) {
	cases := map[string]int{
		"doubao/default":      0,
		"doubao/pro":          0,
		"doubao/image":        skillImageGen,
		"doubao/video":        skillVideoGen,
		"doubao/image:latest": skillImageGen,
		"DOUBAO/IMAGE":        skillImageGen,
		"image":               skillImageGen,
		"unknown-model":       0,
	}
	for name, want := range cases {
		if got := skillForModel(name); got != want {
			t.Errorf("%s 的 skill 应为 %d，实际 %d", name, want, got)
		}
	}
}

// TestModeForModel 校验模型名到生成模式的解析。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，模式解析回归。
func TestModeForModel(t *testing.T) {
	if m := modeForModel("doubao/image"); m.Skill != skillImageGen || m.Kind != "image" {
		t.Errorf("doubao/image 应解析为生图模式，实际 %+v", m)
	}
	if m := modeForModel("doubao/video"); m.Skill != skillVideoGen || m.Kind != "video" {
		t.Errorf("doubao/video 应解析为生视频模式，实际 %+v", m)
	}
	if m := modeForModel("doubao/default"); m.Skill != 0 || m.Kind != "text" {
		t.Errorf("doubao/default 应解析为文本模式，实际 %+v", m)
	}
	// 未知名透传为文本模式：上游会忽略未知模型并回落账号默认。
	if m := modeForModel("some-internal-code"); m.Skill != 0 || m.Kind != "text" {
		t.Errorf("未知名应回落文本模式，实际 %+v", m)
	}
}

// TestNormalizeModelName 校验模型名归一化。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，前缀与后缀处理回归。
func TestNormalizeModelName(t *testing.T) {
	cases := map[string]string{
		"doubao/image":        "image",
		"doubao/image:latest": "image",
		"  image  ":           "image",
		"image":               "image",
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
// 最近修改时间：2026-10-17；改动原因：新增，AC-003 排除模型要求。
func TestFilterExcludedModels(t *testing.T) {
	all := catalogueModels()

	// 未配置排除项时原样返回。
	if got := filterExcludedModels(all, pluginapi.HostConfigSummary{}); len(got) != len(all) {
		t.Fatalf("未配置排除项时不应过滤，实际 %d 条", len(got))
	}

	host := pluginapi.HostConfigSummary{
		ExcludedModels: map[string][]string{providerName: {"doubao/image", "doubao/video"}},
	}
	got := filterExcludedModels(all, host)
	if len(got) != len(all)-2 {
		t.Fatalf("应过滤掉 2 条，实际剩 %d 条", len(got))
	}
	for _, m := range got {
		if m.ID == "doubao/image" || m.ID == "doubao/video" {
			t.Errorf("被排除的模型仍在目录中：%s", m.ID)
		}
	}

	// 入参切片不得被就地修改（共享缓存防护）。
	if len(all) != catalogueSize() {
		t.Errorf("过滤不得修改入参切片，实际长度 %d", len(all))
	}

	// provider 键大小写不同也应生效。
	host2 := pluginapi.HostConfigSummary{
		ExcludedModels: map[string][]string{"DOUBAO-PROVIDER": {"doubao/pro"}},
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
