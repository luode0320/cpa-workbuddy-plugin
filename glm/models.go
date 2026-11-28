package main

import (
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// modelEntry 是模型目录的一项。
// GLM 网页版的模型菜单只有两项：GLM-5.3 与 GLM-5.3 Flash，
// 由请求体的 meta_data.if_plus_model 布尔值切换（true=GLM-5.3，false=Flash）。
// 其余形如 model / subscribe_id 的字段经注入实验证实被上游完全忽略，不可用于选模型。
type modelEntry struct {
	// ID 是对外模型名。
	ID string
	// DisplayName 是面板展示名，与网页菜单一致。
	DisplayName string
	// Note 是说明，用作面板 tooltip。
	Note string
	// PlusModel 对应上游 meta_data.if_plus_model。
	PlusModel bool
}

// modelCatalog 是上游真实存在的两个模型。
var modelCatalog = []modelEntry{
	{
		ID:          "glm/glm-5.3",
		DisplayName: "GLM-5.3",
		Note:        "if_plus_model=true，网页模型菜单第一项",
		PlusModel:   true,
	},
	{
		ID:          "glm/glm-5.3-flash",
		DisplayName: "GLM-5.3 Flash",
		Note:        "if_plus_model=false，网页模型菜单第二项（快速档）",
		PlusModel:   false,
	},
}

// chatModeEntry 是上游的模式维度，与模型正交。
// 实测取值：""（普通）、zero（思考）、chat_agent（智能体）、deep_research（深度研究）。
// 这些是「模式」而非「模型」，因此单独成组，不混进模型菜单。
type chatModeEntry struct {
	// Mode 是上游 chat_mode 取值。
	Mode string
	// Suffix 是模型名后缀，为空表示默认模式。
	Suffix string
	// Label 是展示名。
	Label string
}

// chatModeCatalog 是已知模式，顺序即默认档在前。
var chatModeCatalog = []chatModeEntry{
	{Mode: "", Suffix: "", Label: "标准"},
	{Mode: "zero", Suffix: "-think", Label: "思考"},
	{Mode: "chat_agent", Suffix: "-agent", Label: "智能体"},
	{Mode: "deep_research", Suffix: "-research", Label: "深度研究"},
}

// allModelEntries 生成对外模型目录。
// 默认只暴露网页模型菜单里的两个模型（GLM-5.3 / GLM-5.3 Flash），
// 与网页保持一一对应；模式（思考 / 智能体 / 深度研究）是正交维度，
// 仅在配置显式开启 expose_mode_aliases 时才追加带后缀的别名，
// 避免目录里出现网页上找不到的条目。
// [参数] 无。
// [返回] 条目列表。
// 最近修改时间：2026-11-29 重写, 默认只含网页真实两项，模式别名改为可选。
func allModelEntries() []modelEntry {
	entries := make([]modelEntry, 0, len(modelCatalog))
	for _, model := range modelCatalog {
		entries = append(entries, model)
	}
	if !currentConfig().ExposeModeAliases {
		return entries
	}
	for _, model := range modelCatalog {
		for _, mode := range chatModeCatalog {
			if mode.Mode == "" {
				continue
			}
			entries = append(entries, modelEntry{
				ID:          model.ID + mode.Suffix,
				DisplayName: model.DisplayName + " " + mode.Label,
				Note:        "if_plus_model=" + boolText(model.PlusModel) + ", chat_mode=" + mode.Mode,
				PlusModel:   model.PlusModel,
			})
		}
	}
	return entries
}

// boolText 把布尔转成上游风格的字符串。
// [参数] v: 布尔值。
// [返回] "true" 或 "false"。
// 最近修改时间：2026-11-29 新增, 说明文本拼接。
func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// baselineModelInfos 返回模型目录，供宿主注册与面板展示。
// 目录完全来自 modelCatalog 展开，不含任何虚构模型。
// [参数] 无。
// [返回] 模型信息列表。
// 最近修改时间：2026-11-29 重写, 只暴露上游真实存在的两模型。
func baselineModelInfos() []pluginapi.ModelInfo {
	entries := allModelEntries()
	infos := make([]pluginapi.ModelInfo, 0, len(entries))
	for _, entry := range entries {
		infos = append(infos, pluginapi.ModelInfo{
			ID:                         entry.ID,
			Object:                     "model",
			OwnedBy:                    providerName,
			DisplayName:                entry.DisplayName,
			Description:                entry.Note,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	return infos
}

// defaultModels 是模型 ID 列表，宿主注册与缺省测试模型使用。
var defaultModels = func() []string {
	entries := allModelEntries()
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}()

var (
	// modelHTTPClient 用于用户信息等元数据短请求。
	modelHTTPClient = &http.Client{Timeout: 15 * time.Second}

	// inferenceHTTPClient 专用于推理请求：不设整体超时，按操作 deadline 兜底。
	inferenceHTTPClient = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        20,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 5,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 90 * time.Second,
		},
	}
)

var agentIDPattern = regexp.MustCompile(`^[a-z0-9]{24,}$`)

// resolveUpstreamTarget 把对外模型名解析成上游三元组：assistant_id、chat_mode、if_plus_model。
// 解析顺序：裸智能体 ID → 历史别名 → 真实模型名 + 可选模式后缀 → 未知名字回落到默认模型。
// [参数] model: 请求的模型名。
// [返回] assistant_id、chat_mode、if_plus_model。
// 最近修改时间：2026-11-29 重写, 不再把内部取值当模型名透传。
func resolveUpstreamTarget(model string) (string, string, bool) {
	trimmed := strings.TrimSpace(model)
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	// 裸智能体 ID：直接当 assistant_id，用默认模型与默认模式。
	if agentIDPattern.MatchString(trimmed) {
		return trimmed, "", true
	}

	lower := strings.ToLower(trimmed)
	// 历史别名一律映射到 GLM-5.3 标准档，保持旧调用可用。
	switch lower {
	case "glm-5.1", "glm-4.7", "glm-4-plus", "glm-4", "glm-think",
		"glm-zero", "glm-deepresearch":
		plus, mode := legacyAliasTarget(lower)
		return defaultAssistantID, mode, plus
	}

	// 在目录里按 ID 精确匹配（含模式后缀）。
	for _, entry := range allModelEntries() {
		if strings.EqualFold(entry.ID, "glm/"+trimmed) {
			return defaultAssistantID, modeFromModelID(entry.ID), entry.PlusModel
		}
	}

	// 目录未暴露模式别名时，仍允许显式带上模式后缀，便于老调用方继续使用。
	for _, mode := range chatModeCatalog {
		if mode.Suffix == "" {
			continue
		}
		if !strings.HasSuffix(lower, mode.Suffix) {
			continue
		}
		base := strings.TrimSuffix(lower, mode.Suffix)
		for _, model := range modelCatalog {
			if strings.EqualFold(model.ID, "glm/"+base) {
				return defaultAssistantID, mode.Mode, model.PlusModel
			}
		}
	}

	// 未收录的名字：按是否含 flash 判定模型档，其余当作默认。
	plus := !strings.Contains(lower, "flash")
	return defaultAssistantID, "", plus
}

// legacyAliasTarget 返回历史别名的模型档与模式。
// [参数] lower: 小写模型名。
// [返回] if_plus_model、chat_mode。
// 最近修改时间：2026-11-29 新增, 别名迁移。
func legacyAliasTarget(lower string) (bool, string) {
	switch {
	case strings.Contains(lower, "deepresearch"):
		return true, "deep_research"
	case strings.Contains(lower, "think") || strings.Contains(lower, "zero"):
		return true, "zero"
	}
	return true, ""
}

// modeFromModelID 从模型 ID 反推 chat_mode。
// [参数] id: 形如 glm/glm-5.3-think 的模型 ID。
// [返回] chat_mode 取值。
// 最近修改时间：2026-11-29 新增, 模式后缀反解。
func modeFromModelID(id string) string {
	for _, mode := range chatModeCatalog {
		if mode.Suffix != "" && strings.HasSuffix(id, mode.Suffix) {
			return mode.Mode
		}
	}
	return ""
}

// resolveAssistantID 保留旧签名，供既有调用方使用。
// [参数] model: 请求模型名。
// [返回] assistant_id、chat_mode。
// 最近修改时间：2026-11-29 改为委托 resolveUpstreamTarget。
func resolveAssistantID(model string) (string, string) {
	assistantID, chatMode, _ := resolveUpstreamTarget(model)
	return assistantID, chatMode
}

// resolveUpstreamModel 把插件别名模型解析成上游 assistant_id。
// [参数] model: 请求模型名。
// [返回] 上游 assistant_id。
// 最近修改时间：2026-11-28 新增, 别名到智能体映射。
func resolveUpstreamModel(model string) string {
	assistantID, _ := resolveAssistantID(model)
	return assistantID
}

// allConfiguredModels 返回静态模型 ID 列表。
// [参数] 无。
// [返回] 模型 ID 列表。
// 最近修改时间：2026-11-28 新增, 模型目录。
func allConfiguredModels() []string {
	return append([]string(nil), defaultModels...)
}

// allModelInfos 返回模型列表。
// [参数] 无。
// [返回] 模型信息列表。
// 最近修改时间：2026-11-29 改为真实两模型目录。
func allModelInfos() []pluginapi.ModelInfo {
	return baselineModelInfos()
}
