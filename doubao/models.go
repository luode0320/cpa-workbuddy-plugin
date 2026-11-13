package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件声明插件对外暴露的模型目录。
//
// 目录分两层：
//   - 能力别名（default / pro / image / video）：描述**能力**而不是厂商 SKU，
//     上游换模型时语义仍成立；
//   - 工作模式模型（deepseek / kimi / glm / auto 等）：直指工作模式管线里的
//     真实上游模型主键（model_item_key），一个别名锁一套 model_item_key +
//     memory_profile + total_window_size + reasoning_effort。
//
// 未知名不做拦截，按工作模式默认模型透传：上游忽略不认识的主键并回落到账号
// 默认助手，这比直接报错更符合使用者预期。

// modelAlias 是一条对外暴露的模型别名。
type modelAlias struct {
	// ID 是客户端使用的模型名（不含 doubao/ 前缀）。
	ID string
	// DisplayName 是 /v1/models 中的展示名。
	DisplayName string
	// Description 说明该别名对应的能力。
	Description string
	// Skill 是该别名需要激活的上游 skill，0 表示普通对话。
	Skill int
	// ModelItemKey 是工作模式的上游模型主键；空表示该别名不暴露工作模式模型。
	ModelItemKey string
	// MemoryProfile 是上游记忆档位，必须与 TotalWindow 配对使用。
	MemoryProfile string
	// TotalWindow 是上下文窗口大小（十进制字符串）。
	TotalWindow string
	// ReasoningEffort 是推理档位（0-4）；空表示沿用工作模式默认档位。
	ReasoningEffort string
}

// capabilityAliases 是稳定的能力别名目录。
//
// 前缀固定为 doubao/：宿主按 provider 聚合多个插件的模型，裸名容易与其他
// 服务商的同名模型冲突。
var capabilityAliases = []modelAlias{
	{
		ID:          "default",
		DisplayName: "默认助手",
		Description: "工作模式默认模型（DeepSeek V4 Flash），等同于插件未指定模型时的行为。",
	},
	{
		ID:              "pro",
		DisplayName:     "深度思考",
		Description:     "工作模式默认模型，推理档拉满（最高档 4）。",
		ReasoningEffort: "4",
	},
	{
		ID:          "image",
		DisplayName: "图像生成",
		Description: "生成图片。回复以 OpenAI 多模态 content 返回，图片地址在 image_url 中。",
		Skill:       skillImageGen,
	},
	{
		ID:          "video",
		DisplayName: "视频生成",
		Description: "生成视频。视频地址以文本与 media 扩展字段返回。",
		Skill:       skillVideoGen,
	},
}

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

// skillForModel 返回模型名需要激活的上游 skill。
// [参数] requested: 客户端模型名。
// [返回] skill id；普通对话返回 0。
// 最近修改时间：2026-10-17；改动原因：新增，能力映射。
func skillForModel(requested string) int {
	name := normalizeModelName(requested)
	for _, alias := range capabilityAliases {
		if strings.EqualFold(name, alias.ID) {
			return alias.Skill
		}
	}
	return 0
}

// modeForModel 把客户端模型名解析为生成模式。
//
// 解析顺序：能力别名里的技能别名（image / video）→ 工作模式模型别名 →
// 能力别名里的纯对话别名（default / pro）→ 工作模式默认模型。技能别名必须
// 优先，否则 image / video 会被当成普通对话模型名发给工作模式管线。
// [参数] requested: 客户端模型名。
// [返回] 生成模式。
// 最近修改时间：2026-11-14 02:05:00；改动原因：新增工作模式解析分支，未知名改为回落工作模式默认模型。
func modeForModel(requested string) generationMode {
	// 1. 技能别名优先：image / video 只能走网页端 skill 管线。
	name := normalizeModelName(requested)
	for _, alias := range capabilityAliases {
		if alias.Skill != 0 && strings.EqualFold(name, alias.ID) {
			return workModeForAlias(alias, false)
		}
	}

	// 2. 工作模式模型名直连客户端管线。
	if alias, ok := aliasForWorkModel(name); ok {
		return workModeForAlias(alias, true)
	}

	// 3. 纯对话的默认语义由工作模式默认模型承接，未知模型名同样回落。
	for _, alias := range capabilityAliases {
		if strings.EqualFold(name, alias.ID) {
			return workModeForAlias(alias, true)
		}
	}
	alias, _ := aliasForWorkModel(defaultWorkModelID)
	return workModeForAlias(alias, true)
}

// aliasForWorkModel 在目录中查找模型别名（能力别名与工作模式模型合并视图）。
// [参数] name: 已归一化的模型名。
// [返回] 命中的别名；found 表示是否存在同名条目。
// 最近修改时间：2026-11-14 02:05:00；改动原因：新增，工作模式模型名解析。
func aliasForWorkModel(name string) (modelAlias, bool) {
	// 1. 名称比较大小写不敏感，与宿主传参习惯无关。
	for _, alias := range workModelAliases {
		if strings.EqualFold(name, alias.ID) {
			return alias, true
		}
	}
	return modelAlias{}, false
}

// workModeForAlias 把别名展开成一次请求的生成模式。
//
// 技能别名的 Skill 必须保留：生图/生视频只能走网页端 skill 管线；其余别名
// 统一走工作模式，model_item_key 为空时由工作模式默认模型兜住。
// [参数] alias: 命中的别名；allowWorkMode: 是否允许走工作模式管线。
// [返回] 生成模式。
// 最近修改时间：2026-11-14 02:05:00；改动原因：新增，别名为空字段补齐配置默认值。
func workModeForAlias(alias modelAlias, allowWorkMode bool) generationMode {
	// 1. 技能别名只保留 skill，工作模式字段一律不填。
	if alias.Skill != 0 {
		return generationMode{Skill: alias.Skill, Kind: kindForAlias(alias)}
	}

	// 2. 普通别名展开成工作模式字段。
	mode := generationMode{
		Kind:            "text",
		ModelItemKey:    alias.ModelItemKey,
		MemoryProfile:   alias.MemoryProfile,
		TotalWindow:     alias.TotalWindow,
		ReasoningEffort: alias.ReasoningEffort,
	}
	if !allowWorkMode {
		return mode
	}

	// 3. 允许工作模式时补齐默认值，空字段会把上游模型名打成空串。
	cfg := currentConfig()
	mode.WorkMode = true
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
// [返回] text / image / video。
// 最近修改时间：2026-10-17；改动原因：新增，回复解析类型判定。
func kindForAlias(alias modelAlias) string {
	switch alias.Skill {
	case skillImageGen:
		return "image"
	case skillVideoGen:
		return "video"
	}
	return "text"
}

// catalogueModels 构造完整的模型目录。
//
// 顺序即优先级：能力别名在前（default / pro / image / video），工作模式模型
// 在后。同名条目只保留首个，避免两个来源给出不同的窗口与档位。
// [参数] 无。
// [返回] 宿主模型结构列表。
// 最近修改时间：2026-11-14 03:20:00；改动原因：合并工作模式模型目录。
func catalogueModels() []pluginapi.ModelInfo {
	// 1. 能力别名先入目录，占用同名条目的优先位。
	out := make([]pluginapi.ModelInfo, 0, len(capabilityAliases)+len(workModelAliases))
	seen := make(map[string]struct{}, len(capabilityAliases)+len(workModelAliases))
	for _, alias := range capabilityAliases {
		if _, dup := seen[strings.ToLower(alias.ID)]; dup {
			continue
		}
		seen[strings.ToLower(alias.ID)] = struct{}{}
		out = append(out, modelInfoForAlias(alias))
	}
	// 2. 工作模式模型追加在后，同名条目跳过以保留能力别名的窗口与档位。
	for _, alias := range workModelAliases {
		if _, dup := seen[strings.ToLower(alias.ID)]; dup {
			continue
		}
		seen[strings.ToLower(alias.ID)] = struct{}{}
		out = append(out, modelInfoForAlias(alias))
	}
	return out
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
	case skillImageGen:
		info.Type = "image"
	case skillVideoGen:
		info.Type = "video"
	}
	return info
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
// 这是按账号的模型目录：优先返回该账号的上游真实模型（model_list 透传），
// 再补上插件的能力别名（default / pro / image / video）。上游不可达时只返回
// 能力别名——宿主模型目录必须始终可用，不能因为一次网络失败让用户没模型可选。
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
// 顺序固定为能力别名在前：它们是稳定语义（生图/生视频必须靠别名激活 skill），
// 上游代号在后。同名时以能力别名为准，避免上游恰好返回 default/pro 时把
// 语义别名挤掉。
// [参数] req: 宿主按账号请求（含该账号的凭据快照）。
// [返回] 合并后的模型列表。
// 最近修改时间：2026-10-17；改动原因：新增，动态与静态目录合并。
func catalogueWithUpstream(req pluginapi.AuthModelRequest) []pluginapi.ModelInfo {
	aliases := catalogueModels()

	sa, errAuth := authForModelRequest(&req)
	if errAuth != nil {
		return aliases
	}
	upstream, errDiscover := cachedUpstreamModels(sa, false)
	if errDiscover != nil || len(upstream) == 0 {
		return aliases
	}

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
