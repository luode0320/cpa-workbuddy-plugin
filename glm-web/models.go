package main

import (
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var defaultModels = []string{
	"glm-web/glm-5.1",
	"glm-web/glm-4.7",
	"glm-web/glm-4-plus",
	"glm-web/glm-think",
	"glm-web/glm-deepresearch",
}

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
	var infos []pluginapi.ModelInfo
	for _, id := range defaultModels {
		infos = append(infos, pluginapi.ModelInfo{
			ID:                         id,
			Object:                     "model",
			OwnedBy:                    providerName,
			DisplayName:                id,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	return infos
}
