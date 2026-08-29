// models.go 实现 model.static 与 model.for_auth 能力。
// Trae Work SOLO 要求精确的 config_name，因此账号级能力优先读取上游实时模型目录；
// 凭据不可用或上游瞬时失败时，继续使用配置列表或内置列表兜底。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const traeModelDetailPath = "/api/ide/v1/get_detail_param"

// traeModelDetailRequest 对齐 Trae get_detail_param 的模型发现请求结构。
type traeModelDetailRequest struct {
	Function          string   `json:"function"`            // 模型池功能类型，固定使用 solo_work_lite。
	ConfigNames       []string `json:"config_names"`        // nil 表示请求账号可用的完整模型列表。
	NeedPrompt        bool     `json:"need_prompt"`         // 模型发现不需要返回提示词正文。
	CurrentConfigInfo any      `json:"current_config_info"` // 当前模型配置；全量发现时为空。
	PolyPrompt        bool     `json:"poly_prompt"`         // 保持与 Trae 桌面端请求契约一致。
	ModeType          any      `json:"mode_type"`           // 模式过滤；全量发现时为空。
	AgentType         any      `json:"agent_type"`          // 智能体过滤；全量发现时为空。
}

// traeModelDetailResponse 承载上游返回的账号模型配置列表。
type traeModelDetailResponse struct {
	ConfigInfoList []traeModelConfigInfo `json:"config_info_list"` // 账号当前可用的模型配置。
}

// traeModelConfigInfo 保存可直接用于 llm_utils_chat 的精确模型标识和展示信息。
type traeModelConfigInfo struct {
	ConfigName    string                 `json:"config_name"`    // 上游精确模型 ID，必须原样用于 config_name。
	DisplayConfig traeModelDisplayConfig `json:"display_config"` // 客户端展示信息。
}

// traeModelDisplayConfig 保存 Trae 提供的模型展示名称。
type traeModelDisplayConfig struct {
	DisplayName string `json:"display_name"` // 面向客户端模型选择器的名称。
}

// defaultTraeModels 是 Trae Work SOLO 模型池的精选兜底列表。
// model.for_auth 优先使用上游目录；model.static 或动态发现失败时，若 config_yaml 未覆盖则使用本列表。
var defaultTraeModels = []pluginapi.ModelInfo{
	{ID: "glm-5.2", Name: "glm-5.2", Description: "Trae Work 默认模型 (GLM-5.2)"},
	{ID: "glm-4.7", Name: "glm-4.7", Description: "GLM-4.7"},
	{ID: "deepseek-v4", Name: "deepseek-v4", Description: "DeepSeek V4"},
	{ID: "deepseek-v4-flash", Name: "deepseek-v4-flash", Description: "DeepSeek V4 Flash"},
	{ID: "qwen-max-latest", Name: "qwen-max-latest", Description: "Qwen Max"},
	{ID: "doubao-1.5-pro-32k-250428", Name: "doubao-1.5-pro-32k-250428", Description: "Doubao 1.5 Pro"},
}

// configuredModels holds the config_yaml `models:` override (nil = use
// defaultTraeModels).
var configuredModels []pluginapi.ModelInfo

func loadedModels() []pluginapi.ModelInfo {
	if len(configuredModels) > 0 {
		return configuredModels
	}
	return defaultTraeModels
}

// parseModelsConfig accepts the config_yaml models list. Items may be a
// string (id) or an object {id, name, alias, context, max_tokens, enabled,
// reasoning}. Malformed entries are skipped.
func parseModelsConfig(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return
	}
	var out []pluginapi.ModelInfo
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil && strings.TrimSpace(s) != "" {
			out = append(out, pluginapi.ModelInfo{ID: strings.TrimSpace(s), Name: strings.TrimSpace(s)})
			continue
		}
		var mi pluginapi.ModelInfo
		if err := json.Unmarshal(item, &mi); err == nil && strings.TrimSpace(mi.ID) != "" {
			if mi.Name == "" {
				mi.Name = mi.ID
			}
			out = append(out, mi)
		}
	}
	if len(out) > 0 {
		configuredModels = out
	}
}

// handleModelStatic returns the model list the host exposes to clients.
func handleModelStatic(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	_ = json.Unmarshal(raw, &req)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: loadedModels()})
}

// handleModelForAuth 返回指定 Trae 账号实时可用的模型列表。
//
// [参数] raw: 宿主传入的账号级模型请求，StorageJSON 包含账号凭据。
// [返回] 成功 envelope；账号解析、上游请求或模型解析失败时回退配置/静态列表。
// 最近修改时间：2026-08-30 02:43:13；改动原因：用上游真实 config_name 替代容易失真的手工模型 ID。
func handleModelForAuth(raw []byte) ([]byte, error) {
	// 1. 请求本身无法解析时也保持模型能力可用，继续返回已配置的兜底列表。
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: loadedModels()})
	}

	// 2. 只有账号凭据和动态接口都有效时才替换兜底列表，避免瞬时上游失败清空客户端模型。
	auth, err := parseTraeAuth(req.StorageJSON)
	if err == nil {
		if models, fetchErr := fetchTraeModels(auth); fetchErr == nil && len(models) > 0 {
			return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
		}
	}

	// 3. 动态发现不可用时保留 config_yaml 覆盖语义，否则使用内置静态列表。
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: loadedModels()})
}

// fetchTraeModels 从 get_detail_param 获取当前账号可用的精确 config_name。
//
// [参数] auth: 已解析且包含访问令牌的 Trae 账号。
// [返回] 去重后的模型列表；请求、非 2xx、响应解析或空列表均返回错误。
// 最近修改时间：2026-08-30 02:43:13；改动原因：建立账号级动态模型事实源并拒绝把空响应当成功。
func fetchTraeModels(auth *traeAuth) ([]pluginapi.ModelInfo, error) {
	// 1. 按 Trae 桌面端契约请求 SOLO 模型详情，不指定 config_names 以获取完整账号模型池。
	payload := traeModelDetailRequest{
		Function:          soloWorkLite,
		NeedPrompt:        false,
		CurrentConfigInfo: nil,
		PolyPrompt:        true,
		ModeType:          nil,
		AgentType:         nil,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal model detail request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, apiHostFor(auth)+traeModelDetailPath, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("create model detail request: %w", err)
	}
	req.Header = buildTraeAuthHeaders(auth)

	// 2. 通过宿主 HTTP 桥接执行请求，保证生产代理与请求观测策略继续生效。
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, fmt.Errorf("get_detail_param transport: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &UpstreamError{Status: resp.StatusCode, Body: truncateRedacted(string(resp.Body), 300)}
	}

	// 3. 结构化解析并按 config_name 去重；展示名缺失时使用精确 ID，空列表触发上层回退。
	var detail traeModelDetailResponse
	if err := json.Unmarshal(resp.Body, &detail); err != nil {
		return nil, fmt.Errorf("decode get_detail_param response: %w", err)
	}
	seen := make(map[string]struct{}, len(detail.ConfigInfoList))
	models := make([]pluginapi.ModelInfo, 0, len(detail.ConfigInfoList))
	for _, item := range detail.ConfigInfoList {
		// 3.1 空 ID 和重复 ID 不进入客户端模型列表，避免暴露不可调用或重复选项。
		if item.ConfigName == "" {
			continue
		}
		if _, exists := seen[item.ConfigName]; exists {
			continue
		}
		seen[item.ConfigName] = struct{}{}
		name := item.DisplayConfig.DisplayName
		if name == "" {
			name = item.ConfigName
		}
		models = append(models, pluginapi.ModelInfo{ID: item.ConfigName, Name: name})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("get_detail_param returned no models")
	}
	return models, nil
}
