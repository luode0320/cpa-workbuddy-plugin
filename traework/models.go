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
	"sync"

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
// 仅在动态发现与 config_yaml 覆盖都不可用时使用（优先级链最后一级）。
var defaultTraeModels = []pluginapi.ModelInfo{
	{ID: "glm-5.2", Name: "glm-5.2", Description: "Trae Work 默认模型 (GLM-5.2)"},
	{ID: "glm-4.7", Name: "glm-4.7", Description: "GLM-4.7"},
	{ID: "deepseek-v4", Name: "deepseek-v4", Description: "DeepSeek V4"},
	{ID: "deepseek-v4-flash", Name: "deepseek-v4-flash", Description: "DeepSeek V4 Flash"},
	{ID: "qwen-max-latest", Name: "qwen-max-latest", Description: "Qwen Max"},
	{ID: "doubao-1.5-pro-32k-250428", Name: "doubao-1.5-pro-32k-250428", Description: "Doubao 1.5 Pro"},
}

// configuredModels 保存 config_yaml `models:` 覆盖列表（为空表示未覆盖）。
var configuredModels []pluginapi.ModelInfo

// dynamicModelsCache 缓存最近一次成功的动态模型发现结果，供没有账号凭据的
// model.static 路径复用。model.for_auth 每次都会重新拉取（上游是权威全集），
// 因此这里只服务于静态路径，不做 TTL 判定——缓存只在成功时被写入并在动态
// 失败时保持上一次成功值，避免瞬时上游故障让静态路径失去动态模型。
var dynamicModelsCache struct {
	mu     sync.RWMutex
	models []pluginapi.ModelInfo
}

// cachedTraeDynamicModels 只读返回最近一次成功缓存的动态模型列表。
//
// [参数] 无。
// [返回] 缓存的模型列表；无有效缓存时返回 nil。
// 最近修改时间：2026-09-12；改动原因：为 model.static 提供动态发现来源。
func cachedTraeDynamicModels() []pluginapi.ModelInfo {
	dynamicModelsCache.mu.RLock()
	defer dynamicModelsCache.mu.RUnlock()
	return dynamicModelsCache.models
}

// storeTraeDynamicModels 覆盖动态模型缓存。
//
// [参数] models: 最近一次成功发现的模型列表。
// [返回] 无。
// 最近修改时间：2026-09-12；改动原因：随动态优先优先级链新增缓存写入点。
func storeTraeDynamicModels(models []pluginapi.ModelInfo) {
	dynamicModelsCache.mu.Lock()
	dynamicModelsCache.models = models
	dynamicModelsCache.mu.Unlock()
}

// resolveTraeModels 按优先级链求最终模型列表：动态发现 > 配置 > 静态默认。
//
// 语义（2026-09-12 优先级反转）：
//   - 动态发现有结果时**完全忽略** config_yaml 覆盖与静态默认：上游返回的
//     config_name 是唯一可精确调用的模型事实源，配置不得遮蔽上游新模型；
//   - 动态发现不可用时才使用配置覆盖；
//   - 配置也为空时最后回退静态默认列表。
//
// [参数] dynamic: 动态发现列表（可为空，空表示"动态不可用"）；
//
//	configured: config_yaml `models:` 覆盖列表；
//	fallback: 静态默认列表（defaultTraeModels）。
//
// [返回] 按优先级链选出的最终列表（新切片，可安全交给调用方）。
// 最近修改时间：2026-09-12；改动原因：由"动态完全替换"改为显式三态优先级链。
func resolveTraeModels(dynamic, configured, fallback []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	// 1. 先过滤空 ID 再看长度：只含空 ID 的动态列表语义上等于"没有结果"。
	if dm := nonEmptyTraeModels(dynamic); len(dm) > 0 {
		return dm
	}
	if cm := nonEmptyTraeModels(configured); len(cm) > 0 {
		return cm
	}
	return nonEmptyTraeModels(fallback)
}

// nonEmptyTraeModels 过滤空 ID 条目并返回新切片，避免调用方拿到含空 ID 的
// 列表或共享底层数组。
//
// [参数] models: 待过滤列表。
// [返回] 去掉空 ID 后的新切片。
// 最近修改时间：2026-09-12；改动原因：随优先级链新增，替代原兜底分支的过滤职责。
func nonEmptyTraeModels(models []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		out = append(out, m)
	}
	return out
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

// handleModelStatic 返回宿主要求的全局模型列表。
//
// 优先级链与 handleModelForAuth 一致（动态 > 配置 > 静态默认）：静态路径过去
// 只返回配置 / 内置列表而从不使用动态发现，导致上游新增模型在静态路径下不可见。
// StaticModelRequest 不带账号凭据，因此这里只复用 model.for_auth 留下的动态
// 缓存（见 dynamicModelsCache），缓存为空时正常回退配置 / 静态默认。
//
// [参数] raw: 宿主传入的 StaticModelRequest。
// [返回] 成功 envelope；动态不可用时为配置或内置兜底列表。
// 最近修改时间：2026-09-12；改动原因：接入动态发现缓存，与 for_auth 统一优先级链。
func handleModelStatic(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	_ = json.Unmarshal(raw, &req)
	models := resolveTraeModels(cachedTraeDynamicModels(), configuredModels, defaultTraeModels)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

// handleModelForAuth 返回指定 Trae 账号实时可用的模型列表。
//
// 优先级链：动态发现 > 配置 > 静态默认。动态发现成功时配置与静态列表都不参与，
// 避免手工配过的旧列表永久遮蔽上游新增模型。
//
// [参数] raw: 宿主传入的账号级模型请求，StorageJSON 包含账号凭据。
// [返回] 成功 envelope；账号解析、上游请求或模型解析失败时按链回退。
// 最近修改时间：2026-09-12；改动原因：动态成功改为写入缓存并统一走优先级链。
func handleModelForAuth(raw []byte) ([]byte, error) {
	// 1. 请求本身无法解析时也保持模型能力可用，继续走优先级链兜底
	// （无凭据可用，因此动态这一级必然为空，直接落配置 / 静态）。
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(pluginapi.ModelResponse{
			Provider: providerName,
			Models:   resolveTraeModels(nil, configuredModels, defaultTraeModels),
		})
	}

	// 2. 尝试动态发现；成功则刷新缓存并让动态列表整体胜出。
	// 注意：这里是"本次拉取"的结果，不使用历史缓存——若把缓存当结果，动态
	// 失败将永远命中上一次成功的值，配置兜底语义就失效了。
	var dynamic []pluginapi.ModelInfo
	if auth, err := parseTraeAuth(req.StorageJSON); err == nil {
		if models, fetchErr := fetchTraeModels(auth); fetchErr == nil && len(models) > 0 {
			storeTraeDynamicModels(models)
			dynamic = models
		}
	}

	// 3. 动态不可用时保留 config_yaml 覆盖语义，否则使用内置静态列表。
	return okEnvelope(pluginapi.ModelResponse{
		Provider: providerName,
		Models:   resolveTraeModels(dynamic, configuredModels, defaultTraeModels),
	})
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
