package main

import (
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// modelEntry 是模型目录的一项，带真实档位说明。
type modelEntry struct {
	// ID 是对外模型名，形如 glm/glm-4-plus。
	ID string
	// DisplayName 是面板展示名，写清真实档位。
	DisplayName string
	// Note 是档位细节，用作面板 tooltip。
	Note string
}

// legacyAliasModels 是历史兼容别名。
// glm-5.1 / glm-4.7 在上游与默认档完全等价（同 assistant_id、同 chat_mode），
// 保留只为不打断既有调用方，面板展示时标明同档。
var legacyAliasModels = []modelEntry{
	{ID: "glm/glm-5.1", DisplayName: "GLM 标准（同档别名）", Note: "兼容别名，上游与默认档等价"},
	{ID: "glm/glm-4.7", DisplayName: "GLM 标准（同档别名）", Note: "兼容别名，上游与默认档等价"},
}

// baselineModelInfos 返回不回源时的基线目录：上游已知档位 + 兼容别名。
// 真实目录由 dynamicModelInfos 在拿到 access_token 后动态生成。
// [参数] 无。
// [返回] 模型信息列表。
// 最近修改时间：2026-11-29 改写，基线只作回源失败时的兜底。
func baselineModelInfos() []pluginapi.ModelInfo {
	infos := make([]pluginapi.ModelInfo, 0, len(knownChatModes)+len(legacyAliasModels))
	for _, mode := range knownChatModes {
		infos = append(infos, pluginapi.ModelInfo{
			ID:                         chatModeModelID(mode),
			Object:                     "model",
			OwnedBy:                    providerName,
			DisplayName:                chatModeDisplayName(mode),
			Description:                "chat_mode=" + mode,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	for _, entry := range legacyAliasModels {
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

// defaultModels 是静态模型 ID 列表，宿主注册与缺省测试模型使用。
var defaultModels = func() []string {
	ids := make([]string, 0, len(knownChatModes)+len(legacyAliasModels))
	for _, mode := range knownChatModes {
		ids = append(ids, chatModeModelID(mode))
	}
	for _, entry := range legacyAliasModels {
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

// resolveAssistantID 根据模型名解析上游 assistant_id 与 chat_mode。
// 支持三种形式：glm/<chat_mode>（如 glm/chat_agent）、glm/<assistant_id>、历史别名。
// [参数] model: 请求的模型名。
// [返回] assistant_id、chat_mode。
// 最近修改时间：2026-11-29 改写，档位名直接作为模型名，未知档位原样透传。
func resolveAssistantID(model string) (string, string) {
	assistantID := defaultAssistantID
	trimmed := strings.TrimSpace(model)
	// 剥离 glm/ 前缀，动态档位与动态智能体都按该形式暴露。
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	// 裸智能体 ID：直接当 assistant_id。
	if agentIDPattern.MatchString(trimmed) {
		return trimmed, ""
	}
	lower := strings.ToLower(trimmed)
	switch {
	case lower == "glm-think" || lower == "glm-zero" || lower == "zero":
		return assistantID, "zero"
	case lower == "glm-deepresearch" || lower == "deep_research":
		return assistantID, "deep_research"
	case lower == "glm-4-plus" || lower == "glm-5.1" || lower == "glm-4.7" || lower == "glm-4":
		// 历史别名与默认档等价。
		return assistantID, ""
	case lower == "normal":
		// 上游历史把默认档记成 normal，请求侧归一到空串。
		return assistantID, ""
	case strings.Contains(lower, "deepresearch"):
		return assistantID, "deep_research"
	case strings.Contains(lower, "think"):
		return assistantID, "zero"
	}
	// 其余一律视为上游 chat_mode 取值（含 chat_agent 与未来新增档位）。
	return assistantID, trimmed
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

// allModelInfos 返回不回源时的基线模型列表。
// [参数] 无。
// [返回] 模型信息列表。
// 最近修改时间：2026-11-29 改写，真实目录由 dynamicModelInfos 动态生成。
func allModelInfos() []pluginapi.ModelInfo {
	return baselineModelInfos()
}
