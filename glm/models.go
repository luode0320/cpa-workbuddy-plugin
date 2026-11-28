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

// modelCatalog 是模型的真实目录。
// GLM 网页版的「模型」只由两维决定：assistant_id 与 chat_mode。
// if_plus_model 经实测对响应形态无影响（true/false 均返回 text 段），故不单列；
// glm-5.1 与 glm-4.7 在上游与 glm-4-plus 完全等价，仅作兼容别名保留并如实标注。
var modelCatalog = []modelEntry{
	{ID: "glm/glm-4-plus", DisplayName: "GLM 标准", Note: "chat_mode=normal，网页版默认档"},
	{ID: "glm/glm-think", DisplayName: "GLM 思考", Note: "chat_mode=zero，返回 think 思考段"},
	{ID: "glm/glm-deepresearch", DisplayName: "GLM 深度研究", Note: "chat_mode=deep_research"},
	{ID: "glm/glm-5.1", DisplayName: "GLM 标准（glm-4-plus 同档）", Note: "兼容别名，上游与 glm-4-plus 等价"},
	{ID: "glm/glm-4.7", DisplayName: "GLM 标准（glm-4-plus 同档）", Note: "兼容别名，上游与 glm-4-plus 等价"},
}

// defaultModels 是静态模型 ID 列表，宿主注册与缺省测试模型使用。
var defaultModels = func() []string {
	ids := make([]string, 0, len(modelCatalog))
	for _, entry := range modelCatalog {
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
// [参数] model: 请求的模型名。
// [返回] assistant_id、chat_mode。
// 最近修改时间：2026-11-28 新增, 智能体 ID 与思考模式映射。
func resolveAssistantID(model string) (string, string) {
	assistantID := defaultAssistantID
	chatMode := ""
	trimmed := strings.TrimSpace(model)
	// 兼容 glm/<assistant_id> 形式：动态智能体按该形式对外暴露模型名。
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	if agentIDPattern.MatchString(trimmed) {
		assistantID = trimmed
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "deepresearch") {
		chatMode = "deep_research"
	} else if strings.Contains(lower, "think") || strings.Contains(lower, "zero") {
		chatMode = "zero"
	}
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

// allModelInfos 返回带元信息的模型列表。
// [参数] 无。
// [返回] 模型信息列表。
// 最近修改时间：2026-11-28 新增, 模型目录。
func allModelInfos() []pluginapi.ModelInfo {
	infos := make([]pluginapi.ModelInfo, 0, len(modelCatalog))
	for _, entry := range modelCatalog {
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
