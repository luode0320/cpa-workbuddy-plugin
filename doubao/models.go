package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件声明插件对外暴露的模型目录。
//
// 目录只有一条能力别名（video）：插件只做生视频，别名描述**能力**而不是厂商
// SKU，上游换模型时语义仍成立。
//
// 未知名一律拒绝并返回明确错误：目录里已没有可回落的对话模型，静默透传
// 只会让调用方以为在测生视频，实际打到了别的东西上。
// 最近修改时间：2026-11-18 22:35:00；改动原因：按 CHG-001 删除 image 能力，只留视频。

// modelAlias 是一条对外暴露的模型别名。
type modelAlias struct {
	// ID 是客户端使用的模型名（不含 doubao/ 前缀）。
	ID string
	// DisplayName 是 /v1/models 中的展示名。
	DisplayName string
	// Description 说明该别名对应的能力。
	Description string
	// Skill 是该别名需要激活的上游 skill，0 表示未声明能力。
	Skill int
	// ModelItemKey 是该别名的上游模型主键；视频管线不使用该字段。
	ModelItemKey string
	// MemoryProfile 是该别名的上游记忆档位，必须与 TotalWindow 配对使用。
	MemoryProfile string
	// TotalWindow 是上下文窗口大小（十进制字符串）。
	TotalWindow string
	// ReasoningEffort 是推理档位（0-4）；空表示沿用上游默认档位。
	ReasoningEffort string
}

// capabilityAliases 是稳定的能力别名目录。
//
// 前缀固定为 doubao/：宿主按 provider 聚合多个插件的模型，裸名容易与其他
// 服务商的同名模型冲突。
// 最近修改时间：2026-11-18 22:35:00；改动原因：按 CHG-001 删除 image 能力别名。
var capabilityAliases = []modelAlias{
	{
		ID:          "video",
		DisplayName: "视频生成",
		Description: "生成视频。首轮返回参数确认与 conversation_id，带上该 id 再发一次才会真正生成。",
		Skill:       skillVideoGen,
	},
}

// defaultModelID 是模型名为空时的默认别名。
//
// 只在 test-active 未指定模型时使用。目录只剩视频一条，因此只能指向它。
const defaultModelID = "video"

// workModelAliases 是工作模式（agent_mode=1）模型目录。
//
// 每条别名锁定一套上游 model_item_key + 记忆档位 + 上下文窗口：上游按
// model_item_key 选模型，按 memory_profile / total_window_size 配额，三者
// 不匹配会被拒或提前截断。窗口为 256000 的档位配 256k，300000 的配 300k_230。
var workModelAliases = []modelAlias{
	{
		ID:            "auto",
		DisplayName:   "自动选择",
		Description:   "由上游按问题自动挑模型，免费额度可用。",
		ModelItemKey:  "9",
		MemoryProfile: "256k",
		TotalWindow:   "256000",
	},
	{
		ID:            "doubao-2.1-lite",
		DisplayName:   "豆包 2.1 Lite",
		Description:   "轻量对话模型，适合闲聊与短问答。",
		ModelItemKey:  "seed-lite-7b",
		MemoryProfile: "256k",
		TotalWindow:   "256000",
	},
	{
		ID:            "doubao-2.1-pro",
		DisplayName:   "豆包 2.1 Pro",
		Description:   "豆包旗舰对话模型，通用能力较强。",
		ModelItemKey:  "5",
		MemoryProfile: "256k",
		TotalWindow:   "256000",
	},
	{
		ID:            "deepseek-v4-flash",
		DisplayName:   "DeepSeek V4 Flash",
		Description:   "工作模式默认模型，速度快、额度消耗低。",
		ModelItemKey:  "person-deepseek-v4-1-flash-261017",
		MemoryProfile: "300k_230",
		TotalWindow:   "300000",
	},
	{
		ID:            "deepseek-v4-pro",
		DisplayName:   "DeepSeek V4 Pro",
		Description:   "DeepSeek 高配档，复杂推理与长文更稳。",
		ModelItemKey:  "person-deepseek-v4-pro-ga-260813",
		MemoryProfile: "300k_230",
		TotalWindow:   "300000",
	},
	{
		ID:            "kimi-k3",
		DisplayName:   "Kimi K3",
		Description:   "长上下文写作与资料整理见长。",
		ModelItemKey:  "person-kimi-k3-261111",
		MemoryProfile: "300k_230",
		TotalWindow:   "300000",
	},
	{
		ID:            "kimi-k3.1-pro",
		DisplayName:   "Kimi K3.1 Pro",
		Description:   "Kimi 新版旗舰，指令跟随更稳。",
		ModelItemKey:  "person-kimi-k3-1-pro-261111",
		MemoryProfile: "300k_230",
		TotalWindow:   "300000",
	},
	{
		ID:            "kimi-k3.1-pro-fast",
		DisplayName:   "Kimi K3.1 Pro 高速版",
		Description:   "Kimi 新版高速档，延迟更低。",
		ModelItemKey:  "person-kimi-k3-1-pro-highspeed-261111",
		MemoryProfile: "300k_230",
		TotalWindow:   "300000",
	},
	{
		ID:            "glm-5.3",
		DisplayName:   "GLM 5.3",
		Description:   "智谱 GLM 旗舰档；额度不足时上游会回显错误话术。",
		ModelItemKey:  "person-glm-5-3-261111",
		MemoryProfile: "300k_230",
		TotalWindow:   "300000",
	},
	{
		ID:            "glm-5.3-flash",
		DisplayName:   "GLM 5.3 Flash",
		Description:   "智谱 GLM 轻量档；额度不足时上游会回显错误话术。",
		ModelItemKey:  "person-glm-5-3-flash-261111",
		MemoryProfile: "300k_230",
		TotalWindow:   "300000",
	},
	{
		ID:              "glm-5.4",
		DisplayName:     "GLM 5.4",
		Description:     "智谱 GLM 新版旗舰，新增视觉能力。",
		ModelItemKey:    "person-glm-5-4",
		MemoryProfile:   "300k_230",
		TotalWindow:     "300000",
		ReasoningEffort: "5",
	},
	{
		ID:              "qwen3.8-max",
		DisplayName:     "Qwen 3.8 Max",
		Description:     "通义千问 3.8 旗舰档。",
		ModelItemKey:    "person-qwen3-8-max",
		MemoryProfile:   "300k_230",
		TotalWindow:     "300000",
		ReasoningEffort: "5",
	},
	{
		ID:              "qwen3.8-flash",
		DisplayName:     "Qwen 3.8 Flash",
		Description:     "通义千问 3.8 轻量档，额度消耗低。",
		ModelItemKey:    "person-qwen3-8-flash",
		MemoryProfile:   "300k_230",
		TotalWindow:     "300000",
		ReasoningEffort: "5",
	},
	{
		ID:              "qwen4-max",
		DisplayName:     "Qwen 4 Max",
		Description:     "通义千问新一代旗舰档。",
		ModelItemKey:    "person-qwen4-max",
		MemoryProfile:   "300k_230",
		TotalWindow:     "300000",
		ReasoningEffort: "5",
	},
	{
		ID:              "mimo-v2.6-pro",
		DisplayName:     "MiMo V2.6 Pro",
		Description:     "小米 MiMo V2.6 旗舰档。",
		ModelItemKey:    "person-mimo-v2-6-pro",
		MemoryProfile:   "300k_230",
		TotalWindow:     "300000",
		ReasoningEffort: "5",
	},
	{
		ID:              "mimo-v2.6-flash",
		DisplayName:     "MiMo V2.6 Flash",
		Description:     "小米 MiMo V2.6 轻量档，额度消耗极低。",
		ModelItemKey:    "person-mimo-v2-6-flash",
		MemoryProfile:   "300k_230",
		TotalWindow:     "300000",
		ReasoningEffort: "5",
	},
}

// defaultWorkModelID 是工作模式的默认模型别名。
const defaultWorkModelID = "deepseek-v4-flash"

// modelIDPrefix 是对外模型名的前缀。
const modelIDPrefix = "doubao/"

// referenceCreated 是目录条目的固定创建时间。
//
// 用固定值而非 time.Now()：该字段参与客户端缓存判定，每次轮询都变化会让
// 客户端误以为模型目录在不停更新。
const referenceCreated = 1767225600 // 2026-01-01T00:00:00Z

// normalizeModelName 归一化客户端传入的模型名。
//
// 处理三类常见写法：大小写差异、"doubao/" 前缀、":latest" 后缀。
// [参数] requested: 原始模型名。
// [返回] 去掉前缀与后缀的裸别名（保留原大小写，比较时另行忽略大小写）。
// 最近修改时间：2026-10-17；改动原因：新增，模型名归一化入口。
func normalizeModelName(requested string) string {
	name := strings.TrimSpace(requested)
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSuffix(name, ":latest")
	return name
}

// modeForModel 把客户端模型名解析为生成模式。
//
// 技能别名优先（video 靠 Skill 激活上游 skill 管线），其次是工作模式模型名。
// 目录外的名字一律判失败，绝不静默替换成别的模型——静默回落会把
// 「测生视频」变成「测别的东西」。
// [参数] requested: 客户端模型名。
// [返回] 生成模式；ok 表示是否命中目录。
// 最近修改时间：2026-11-27 00:55:00；改动原因：恢复工作模式模型目录与解析。
func modeForModel(requested string) (generationMode, bool) {
	name := normalizeModelName(requested)

	// 1. 技能别名优先：只有它能激活网页端 skill 管线。
	for _, alias := range capabilityAliases {
		if strings.EqualFold(name, alias.ID) {
			return modeForAlias(alias), true
		}
	}

	// 2. 工作模式模型名直连 agent 管线。
	if alias, ok := aliasForWorkModel(name); ok {
		return workModeForAlias(alias), true
	}
	return generationMode{}, false
}

// modeForAlias 把技能别名展开成一次请求的生成模式。
//
// Skill 必须保留：生视频只能靠它激活上游 skill 管线。
// [参数] alias: 命中的别名。
// [返回] 生成模式。
// 最近修改时间：2026-11-27 00:55:00；改动原因：注释口径同步，函数体不变。
func modeForAlias(alias modelAlias) generationMode {
	return generationMode{Skill: alias.Skill, Kind: kindForAlias(alias)}
}

// aliasForWorkModel 在工作模式目录中查找模型别名。
//
// 静态别名优先保证命名稳定；静态未收录的新模型由上游菜单动态兜底。
// [参数] name: 已归一化的模型名。
// [返回] 命中的别名；found 表示是否存在同名条目。
// 最近修改时间：2026-11-27 23:25:00；改动原因：新增动态菜单兜底，新模型无需改代码即可用。
func aliasForWorkModel(name string) (modelAlias, bool) {
	for _, alias := range workModelAliases {
		if strings.EqualFold(name, alias.ID) {
			return alias, true
		}
	}
	for _, alias := range workModelsSnapshot() {
		if strings.EqualFold(name, alias.ID) {
			return alias, true
		}
	}
	return modelAlias{}, false
}

// workModeForAlias 把工作模式别名展开成一次请求的生成模式。
//
// 空字段用运行期配置兜底：model_item_key 为空会把上游模型名打成空串。
// [参数] alias: 命中的工作模式别名。
// [返回] 生成模式。
// 最近修改时间：2026-11-27 00:55:00；改动原因：恢复工作模式别名展开。
func workModeForAlias(alias modelAlias) generationMode {
	cfg := currentConfig()
	mode := generationMode{
		Kind:            "text",
		ModelItemKey:    alias.ModelItemKey,
		MemoryProfile:   alias.MemoryProfile,
		TotalWindow:     alias.TotalWindow,
		ReasoningEffort: alias.ReasoningEffort,
		WorkMode:        true,
	}
	if mode.ModelItemKey == "" {
		if fallback, ok := aliasForWorkModel(defaultWorkModelID); ok {
			mode.ModelItemKey = fallback.ModelItemKey
			mode.MemoryProfile = fallback.MemoryProfile
			mode.TotalWindow = fallback.TotalWindow
		}
	}
	if mode.MemoryProfile == "" {
		mode.MemoryProfile = cfg.WorkMemoryProfile
	}
	if mode.TotalWindow == "" {
		mode.TotalWindow = cfg.WorkTotalWindow
	}
	if mode.ReasoningEffort == "" {
		mode.ReasoningEffort = cfg.WorkReasoningEffort
	}
	return mode
}

// kindForAlias 返回别名对应的回复类型。
// [参数] alias: 别名。
// [返回] video；未声明 skill 时返回 text。
// 最近修改时间：2026-11-18 22:35:00；改动原因：删除 image 分支。
func kindForAlias(alias modelAlias) string {
	switch alias.Skill {
	case skillVideoGen:
		return "video"
	}
	return "text"
}

// catalogueModels 构造完整的模型目录。
//
// 能力别名在前（稳定语义），工作模式模型在后。同名去重保留：重复登记
// 同一个 ID 会让宿主看到两条冲突条目。
// [参数] 无。
// [返回] 宿主模型结构列表。
// 最近修改时间：2026-11-27 00:55:00；改动原因：恢复工作模式模型目录。
func catalogueModels() []pluginapi.ModelInfo {
	aliases := allAliases()
	out := make([]pluginapi.ModelInfo, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		if _, dup := seen[strings.ToLower(alias.ID)]; dup {
			continue
		}
		seen[strings.ToLower(alias.ID)] = struct{}{}
		out = append(out, modelInfoForAlias(alias))
	}
	return out
}

// allAliases 返回能力别名、静态工作模型与上游动态菜单的合并视图。
//
// 静态在前保证命名稳定；动态按 model_item_key 去重，只补静态未收录的新模型。
// [参数] 无。
// [返回] 别名列表（新副本，调用方改动不会污染目录）。
// 最近修改时间：2026-11-27 23:25:00；改动原因：新增上游菜单动态补充，新模型自动出现。
func allAliases() []modelAlias {
	out := make([]modelAlias, 0, len(capabilityAliases)+len(workModelAliases))
	out = append(out, capabilityAliases...)
	out = append(out, workModelAliases...)

	// 动态菜单只补静态没有的模型键，避免同键两条冲突条目。
	seen := make(map[string]struct{}, len(out))
	for _, alias := range out {
		seen[strings.TrimSpace(alias.ModelItemKey)] = struct{}{}
	}
	for _, alias := range workModelsSnapshot() {
		key := strings.TrimSpace(alias.ModelItemKey)
		if _, exists := seen[key]; exists && key != "" {
			continue
		}
		if key != "" {
			seen[key] = struct{}{}
		}
		out = append(out, alias)
	}
	return out
}

// testableAliases 返回活跃测试可选的模型别名。
//
// 面板测试必须能覆盖工作模式模型，因此目录里的别名全部可测。
// [参数] 无。
// [返回] 别名列表（返回副本，调用方改动不会污染目录）。
// 最近修改时间：2026-11-27 00:55:00；改动原因：恢复工作模式模型可测。
func testableAliases() []modelAlias {
	return allAliases()
}

// testableModelIDs 返回活跃测试可选的模型 id（含 doubao/ 前缀）。
// [参数] 无。
// [返回] 模型 id 列表。
// 最近修改时间：2026-11-14 15:55:00；改动原因：新增，替代活跃测试里的局部模型列表。
func testableModelIDs() []string {
	aliases := testableAliases()
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		out = append(out, modelIDPrefix+alias.ID)
	}
	return out
}

// testableModelDetails 返回活跃测试可选模型的展示信息。
//
// 面板需要可读名：只给 doubao/video 这类裸 id，用户分不清能力类型。
// default 标记供面板给默认别名加注，避免用户误选。
// [参数] 无。
// [返回] 每项含 id / name / description / default。
// 最近修改时间：2026-11-17 00:42:00；改动原因：工作模式模型删除，注释样例改为能力别名。
func testableModelDetails() []map[string]any {
	aliases := testableAliases()
	out := make([]map[string]any, 0, len(aliases))
	for _, alias := range aliases {
		out = append(out, map[string]any{
			"id":          modelIDPrefix + alias.ID,
			"name":        alias.DisplayName,
			"description": alias.Description,
			"default":     alias.ID == defaultModelID,
		})
	}
	return out
}

// aliasForTestModel 判断模型名是否属于可测试别名。
//
// 与 modeForModel 同源但不共用：两者都要严格——活跃测试点上游内部代号时必须
// 直接失败，不能静默打到默认模型，否则界面显示「测试成功（模型: doubao/anceba）」
// 而实际测的是别的模型，测试结果与所选模型无关。
// [参数] requested: 客户端模型名（允许带 doubao/ 前缀与 :latest 后缀）。
// [返回] 命中的别名与是否存在。
// 最近修改时间：2026-11-17 00:42:00；改动原因：工作模式模型目录删除，严格语义成为两条路径的共识。
func aliasForTestModel(requested string) (modelAlias, bool) {
	name := normalizeModelName(requested)
	for _, alias := range testableAliases() {
		if strings.EqualFold(name, alias.ID) {
			return alias, true
		}
	}
	return modelAlias{}, false
}

// modelInfoForAlias 把别名转换成宿主模型条目。
// [参数] alias: 模型别名。
// [返回] 宿主模型条目。
// 最近修改时间：2026-11-14 03:20:00；改动原因：从 catalogueModels 抽出，供两条目录复用。
func modelInfoForAlias(alias modelAlias) pluginapi.ModelInfo {
	info := pluginapi.ModelInfo{
		ID:          modelIDPrefix + alias.ID,
		Object:      "model",
		Created:     referenceCreated,
		OwnedBy:     providerName,
		Type:        "chat",
		DisplayName: alias.DisplayName,
		Name:        alias.ID,
		Description: alias.Description,
	}
	// 1. 技能别名改变类型，其余一律 chat。
	switch alias.Skill {
	case skillVideoGen:
		info.Type = "video"
	}
	return info
}

// testModeForModel 把模型名展开成活跃测试用的生成模式。
//
// 未命中别名时直接判失败：活跃测试要给用户「这个模型能不能用」的确切答案，
// 静默替换模型会让结论失去意义。
// [参数] requested: 客户端模型名。
// [返回] 生成模式、命中的别名与是否命中。
// 最近修改时间：2026-11-14 16:05:00；改动原因：新增，活跃测试严格解析模型名。
func testModeForModel(requested string) (generationMode, modelAlias, bool) {
	alias, ok := aliasForTestModel(requested)
	if !ok {
		return generationMode{}, modelAlias{}, false
	}
	return modeForAlias(alias), alias, true
}

// filterExcludedModels 剔除宿主 oauth-excluded-models 中为本插件列出的模型。
//
// 宿主只在 model.for_auth 的按账号路径上替插件过滤；model.static 的返回会被原样
// 登记进全局模型目录，因此插件必须在出口自行过滤——这是同名模型被多个 provider
// 同时认领时唯一能把模型从本服务商摘掉的入口。
// [参数] models: 已解析的模型列表；host: 宿主配置摘要。
// [返回] 过滤后的列表；未配置排除项时原样返回入参。
// 最近修改时间：2026-10-17；改动原因：新增，与既有插件保持一致的排除语义。
func filterExcludedModels(models []pluginapi.ModelInfo, host pluginapi.HostConfigSummary) []pluginapi.ModelInfo {
	if len(host.ExcludedModels) == 0 {
		return models
	}
	excluded := host.ExcludedModels[providerName]
	if len(excluded) == 0 {
		for channel, list := range host.ExcludedModels {
			if strings.EqualFold(strings.TrimSpace(channel), providerName) {
				excluded = list
				break
			}
		}
	}
	if len(excluded) == 0 {
		return models
	}

	excludeSet := make(map[string]struct{}, len(excluded))
	for _, m := range excluded {
		excludeSet[strings.ToLower(strings.TrimSpace(m))] = struct{}{}
	}

	// 返回新切片而非就地压缩：入参可能来自共享缓存，就地写回会把缓存
	// 永久变成「已过滤列表」。
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if _, skip := excludeSet[strings.ToLower(m.ID)]; skip {
			continue
		}
		out = append(out, m)
	}
	return out
}

// modelStatic 响应 model.register / model.static。
//
// 宿主在启动期与按需调用本方法，此时可能还没有任何账号；目录必须照常返回，
// 否则用户在授权前没有模型可选。
// [参数] request: 宿主请求体。
// [返回] 信封 JSON 与错误。
// 最近修改时间：2026-10-17；改动原因：新增，静态目录入口。
func modelStatic(request []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	if len(request) > 0 {
		_ = json.Unmarshal(request, &req)
	}
	return okEnvelope(pluginapi.ModelResponse{
		Provider: providerName,
		Models:   filterExcludedModels(catalogueModels(), req.Host),
	})
}

// modelForAuth 响应 model.for_auth。
//
// 这是按账号的模型目录：能力别名（video）在前，该账号的上游真实模型
// （model_list 透传）在后。上游不可达时只返回能力别名——宿主模型目录必须始终
// 可用，不能因为一次网络失败让用户没模型可选。
// [参数] request: 宿主请求体。
// [返回] 信封 JSON 与错误。
// 最近修改时间：2026-10-17；改动原因：接入上游动态模型清单。
func modelForAuth(request []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if len(request) > 0 {
		_ = json.Unmarshal(request, &req)
	}
	return okEnvelope(pluginapi.ModelResponse{
		Provider: providerName,
		Models:   filterExcludedModels(catalogueWithUpstream(req), req.Host),
	})
}

// catalogueWithUpstream 组合能力别名与上游真实模型。
//
// 顺序固定为能力别名在前：它们是稳定语义（生视频必须靠别名激活 skill），
// 上游代号在后。同名时以能力别名为准，避免上游恰好返回同名模型时把语义别名
// 挤掉。
// [参数] req: 宿主按账号请求（含该账号的凭据快照）。
// [返回] 合并后的模型列表。
// 最近修改时间：2026-10-17；改动原因：新增，动态与静态目录合并。
func catalogueWithUpstream(req pluginapi.AuthModelRequest) []pluginapi.ModelInfo {
	sa, errAuth := authForModelRequest(&req)
	if errAuth == nil {
		// 先刷新工作模式菜单：成功时 allAliases 会把上游新增模型一并带出。
		refreshWorkModels(sa)
	}

	aliases := catalogueModels()
	if errAuth != nil {
		return aliases
	}
	upstream, errDiscover := cachedUpstreamModels(sa, false)
	if errDiscover != nil || len(upstream) == 0 {
		return aliases
	}
	return mergeModelInfo(aliases, upstream)
}

// mergeModelInfo 合并别名与上游代号，同名以先出现的为准。
// [参数] aliases: 别名列表；upstream: 上游模型列表。
// [返回] 合并结果。
// 最近修改时间：2026-11-27 23:28:00；改动原因：新增，收敛目录合并逻辑供两条分支复用。
func mergeModelInfo(aliases, upstream []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	seen := make(map[string]struct{}, len(aliases)+len(upstream))
	out := make([]pluginapi.ModelInfo, 0, len(aliases)+len(upstream))
	for _, m := range aliases {
		seen[strings.ToLower(m.ID)] = struct{}{}
		out = append(out, m)
	}
	for _, m := range upstream {
		if _, exists := seen[strings.ToLower(m.ID)]; exists {
			continue
		}
		seen[strings.ToLower(m.ID)] = struct{}{}
		out = append(out, m)
	}
	return out
}

// authForModelRequest 从宿主请求中解析出要查询模型的账号。
//
// 宿主在 model.for_auth 里下发该账号的凭据快照；没有快照时按 AuthID 回落。
// 解析不出来不算错误路径——调用方会退回纯能力别名。
// [参数] req: 宿主按账号请求。
// [返回] 账号与错误。
// 最近修改时间：2026-10-17；改动原因：新增，模型发现的账号解析。
func authForModelRequest(req *pluginapi.AuthModelRequest) (*StoredAuth, error) {
	if req == nil {
		return nil, fmt.Errorf("请求为空")
	}
	if len(req.StorageJSON) > 0 {
		var sa StoredAuth
		if errUnmarshal := json.Unmarshal(req.StorageJSON, &sa); errUnmarshal == nil && sa.CookieHeader != "" {
			if sa.AuthID == "" {
				sa.AuthID = strings.TrimSuffix(req.AuthID, ".json")
			}
			return &sa, nil
		}
	}
	authID := strings.TrimSuffix(strings.TrimSpace(req.AuthID), ".json")
	if authID == "" {
		return nil, fmt.Errorf("缺少账号标识")
	}
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return nil, errList
	}
	for _, account := range accounts {
		if account.AuthID == authID {
			return account, nil
		}
	}
	return nil, fmt.Errorf("账号不存在：%s", authID)
}
