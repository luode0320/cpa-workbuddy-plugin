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

const (
	traeModelDetailPath = "/api/ide/v1/get_detail_param"
	// traeCatalogChat / traeCatalogSolo 是模型目录的两条上架通道。
	// 上游按「配置 × 通道」判定模型是否上架：只问一个通道会漏掉仅在另一通道
	// 上架的模型（对标开源 trae-cn-provider 的 chat_v3 + solo_agent 双查）。
	traeCatalogChat = "chat_v3"
	traeCatalogSolo = "solo_agent"
)

// traeCatalogFunctions 是模型发现的通道集合：两份目录并发拉取后按
// config_name 去重合并，先到先得。
var traeCatalogFunctions = []string{traeCatalogChat, traeCatalogSolo}

// traeModelFunctions 记录每个模型最近一次上架的通道，供聊天请求选择对应的
// function。拿未在该通道上架的模型去问，上游会回 4001。
// key 为 config_name 小写。
var traeModelFunctions sync.Map

// traeCanonicalModelNames 记录 小写模型名 → 目录登记的精确 config_name。
// 客户端按品牌大小写请求时，上游只认目录里的原始写法。
var traeCanonicalModelNames sync.Map

// traeModelDetailRequest 对齐 Trae get_detail_param 的模型发现请求结构。
type traeModelDetailRequest struct {
	Function          string   `json:"function"`            // 模型池功能类型，取 traeCatalogFunctions 中的通道。
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

// traeModelConfigInfo 保存可直接用于 llm_utils_chat 的精确模型标识和展示信息，
// 以及判定「是否对用户可选」所需的元数据。
type traeModelConfigInfo struct {
	ConfigName        string                 `json:"config_name"`          // 上游精确模型 ID，必须原样用于 config_name。
	DisplayConfig     traeModelDisplayConfig `json:"display_config"`       // 客户端展示信息。
	IsInvisibleToUser bool                   `json:"is_invisible_to_user"` // true 表示已对用户隐藏（退役/内部）。
	ConfigSwitch      *bool                  `json:"config_switch"`        // false 表示开关关闭；缺省视为开启。
	Usage             string                 `json:"usage"`                // 用途标签，非 chat_completion 多为内部模型。
	RawChatFunction   string                 `json:"raw_chat_function"`    // 上游标注的聊天通道，优先于请求通道。
}

// traeModelDisplayConfig 保存 Trae 提供的模型展示名称与能力标记。
type traeModelDisplayConfig struct {
	DisplayName   string `json:"display_name"`    // 面向客户端模型选择器的名称。
	IsCustomModel bool   `json:"is_custom_model"` // true 表示用户自定义模型，不进入公共目录。
	MaxMode       bool   `json:"max_mode"`        // true 表示该配置存在 __max 变体。
	Multimodal    bool   `json:"multimodal"`      // true 表示支持图片输入。
}

// traeInternalModelUsage 是内部用途标签集合，带这些标签的模型不可作为聊天模型暴露。
var traeInternalModelUsage = map[string]struct{}{
	"summary":           {},
	"fast_apply":        {},
	"fast_apply_new":    {},
	"title_generation":  {},
	"input_optimization": {},
	"compact_prompt":    {},
	"query_rewrite":     {},
	"custom_model":      {},
}

// traeInternalModelPrefixes 是内部模型的前缀黑名单。
var traeInternalModelPrefixes = []string{
	"custom_", "search_agent", "browser_use", "computer_use", "file_search", "explore_",
}

// defaultTraeModels 原为精选兜底列表。现已完全剔除写死模型，
// 模型列表完全依靠上游动态发现，无配置且动态不可用时返回空切片。
var defaultTraeModels []pluginapi.ModelInfo

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
			id := strings.TrimSpace(s)
			out = append(out, pluginapi.ModelInfo{
				ID:                         id,
				Name:                       id,
				OwnedBy:                    providerName,
				SupportedGenerationMethods: []string{"chat"},
			})
			continue
		}
		var mi pluginapi.ModelInfo
		if err := json.Unmarshal(item, &mi); err == nil && strings.TrimSpace(mi.ID) != "" {
			if mi.Name == "" {
				mi.Name = mi.ID
			}
			if mi.OwnedBy == "" {
				mi.OwnedBy = providerName
			}
			if len(mi.SupportedGenerationMethods) == 0 {
				mi.SupportedGenerationMethods = []string{"chat"}
			}
			out = append(out, mi)
		}
	}
	if len(out) > 0 {
		configuredModels = out
	}
}

// filterExcludedModels 剔除宿主 oauth-excluded-models 中为 traework-provider 列出的模型。
// 宿主只在 model.for_auth 的按账号注册路径上替插件过滤（sdk/cliproxy/service_executors.go
// 的 applyExcludedModels），model.static 返回的列表会被原样登记进全局模型目录，因此插件必须
// 在出口自行过滤；同名裸模型被多个 OAuth 插件同时认领时，这是唯一能把模型从某个服务商摘掉的治理入口。
// [参数] models: 已完成优先级解析的插件模型列表；host: 宿主随请求传入的配置摘要。
// [返回] 过滤后的模型列表；宿主未配置排除项时原样返回入参。
// 最近修改时间：2026-10-11 21:20:00；改动原因：补齐 traework 与 workbuddy/qoder-ai/qoderwork
// 一致的 oauth-excluded-models 过滤，使 deepseek-v4.1-flash 这类多服务商同名模型可被定向摘除。
func filterExcludedModels(models []pluginapi.ModelInfo, host pluginapi.HostConfigSummary) []pluginapi.ModelInfo {
	if len(host.ExcludedModels) == 0 {
		return models
	}
	// Try exact provider match, then case-insensitive scan.
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
	// Use a fresh slice — models[:0] would alias the input's backing array,
	// which may be the dynamicTraeModelsCache's own slice. Mutating it in place
	// would corrupt the cache for subsequent callers (P0 bug: after one
	// filterExcludedModels call, cache returns the filtered list as the
	// "full" list on the next fetch).
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if _, skip := excludeSet[strings.ToLower(m.ID)]; skip {
			continue
		}
		out = append(out, m)
	}
	return out
}

// handleModelStatic 返回宿主要求的全局模型列表。
//
// 优先级链与 handleModelForAuth 一致（动态 > 配置 > 静态默认）：静态默认已去除
// 写死模型，完全依靠动态获取。StaticModelRequest 不带账号凭据，优先复用已有的
// 动态缓存；若缓存为空，主动从宿主已有的可用凭据拉取一次并填补缓存。
//
// [参数] raw: 宿主传入的 StaticModelRequest。
// [返回] 成功 envelope；动态不可用时为配置或空列表。
// 最近修改时间：2026-10-11 21:20:00；改动原因：模型列表出口应用宿主 oauth-excluded-models 过滤。
func handleModelStatic(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	_ = json.Unmarshal(raw, &req)
	models := resolveTraeModels(dynamicTraeModelsFromCacheOrAuth(), configuredModels, defaultTraeModels)
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

// dynamicTraeModelsFromCacheOrAuth 优先使用未过期缓存；若缓存未命中，尝试从宿主已有的有效凭据拉取一次并写入缓存。
func dynamicTraeModelsFromCacheOrAuth() []pluginapi.ModelInfo {
	if models := cachedTraeDynamicModels(); len(models) > 0 {
		return models
	}
	return fetchDynamicTraeModelsFromAnyAuth()
}

// fetchDynamicTraeModelsFromAnyAuth 在没有显式传入凭据的场景下（如 model.static），
// 尝试通过宿主 auth 列表读取任意可用 traework 账号拉取动态模型并写入缓存。
func fetchDynamicTraeModelsFromAnyAuth() []pluginapi.ModelInfo {
	files, err := hostAuthList()
	if err != nil || len(files) == 0 {
		return nil
	}
	// 优先挑选未禁用的账号
	for _, f := range files {
		if f.Disabled {
			continue
		}
		a, err := hostAuthGet(f.AuthIndex)
		if err != nil || a == nil {
			continue
		}
		dyn, err := fetchTraeModels(a)
		if err == nil && len(dyn) > 0 {
			storeTraeDynamicModels(dyn)
			return dyn
		}
	}
	// 若未禁用的账号拉取失败，回退尝试其它账号
	for _, f := range files {
		if !f.Disabled {
			continue
		}
		a, err := hostAuthGet(f.AuthIndex)
		if err != nil || a == nil {
			continue
		}
		dyn, err := fetchTraeModels(a)
		if err == nil && len(dyn) > 0 {
			storeTraeDynamicModels(dyn)
			return dyn
		}
	}
	return nil
}

// handleModelForAuth 返回指定 Trae 账号实时可用的模型列表。
//
// 优先级链：动态发现 > 配置 > 静态默认。动态发现成功时配置与静态列表都不参与，
// 避免手工配过的旧列表永久遮蔽上游新增模型。
//
// [参数] raw: 宿主传入的账号级模型请求，StorageJSON 包含账号凭据。
// [返回] 成功 envelope；账号解析、上游请求或模型解析失败时按链回退。
// 最近修改时间：2026-10-11 21:20:00；改动原因：两个返回出口都应用宿主 oauth-excluded-models 过滤。
func handleModelForAuth(raw []byte) ([]byte, error) {
	// 1. 请求本身无法解析时也保持模型能力可用，继续走优先级链兜底
	// （无凭据可用，因此动态这一级必然为空，直接落配置 / 静态）。
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(pluginapi.ModelResponse{
			Provider: providerName,
			Models:   filterExcludedModels(resolveTraeModels(nil, configuredModels, defaultTraeModels), req.Host),
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
	// 宿主排除项在最终列表上生效，避免被摘掉的服务商同名模型重新出现在 /models。
	return okEnvelope(pluginapi.ModelResponse{
		Provider: providerName,
		Models:   filterExcludedModels(resolveTraeModels(dynamic, configuredModels, defaultTraeModels), req.Host),
	})
}

// traeCatalogEntry 是单通道目录中的一条可选模型及其归属通道。
type traeCatalogEntry struct {
	model    pluginapi.ModelInfo
	function string
}

// fetchTraeModels 并行拉取 chat_v3 / solo_agent 两条通道的账号模型目录，
// 过滤内部与自定义模型后按 config_name 去重合并。
//
// 上游按「配置 × 通道」判定模型是否上架：只问一个通道会漏掉仅在另一通道
// 上架的模型，且拿未上架通道的模型去聊天会回 4001，因此合并时记录每个模型
// 的归属通道（traeModelFunctions），由 resolveModelOptions 在聊天时取用。
//
// [参数] auth: 已解析且包含访问令牌的 Trae 账号。
// [返回] 去重后的可选模型列表；两条通道都失败或均无可选模型时返回错误。
// 最近修改时间：2026-11-07；改动原因：单通道 solo_work_lite 无法覆盖
// chat_v3 / solo_agent 上架的模型，且原实现零过滤导致内部模型混入目录。
func fetchTraeModels(auth *traeAuth) ([]pluginapi.ModelInfo, error) {
	type catalogResult struct {
		function string
		entries  []traeCatalogEntry
		err      error
	}
	results := make(chan catalogResult, len(traeCatalogFunctions))
	for _, function := range traeCatalogFunctions {
		go func(function string) {
			entries, err := fetchTraeCatalog(auth, function)
			results <- catalogResult{function: function, entries: entries, err: err}
		}(function)
	}

	collected := make(map[string]catalogResult, len(traeCatalogFunctions))
	for range traeCatalogFunctions {
		result := <-results
		collected[result.function] = result
	}

	seen := make(map[string]struct{}, 64)
	models := make([]pluginapi.ModelInfo, 0, 64)
	failures := make([]string, 0, len(traeCatalogFunctions))
	// 按 traeCatalogFunctions 的固定顺序合并（chat_v3 优先），保证模型顺序与
	// 通道归属不随 goroutine 完成时机漂移。
	for _, function := range traeCatalogFunctions {
		result, ok := collected[function]
		if !ok {
			continue
		}
		if result.err != nil {
			failures = append(failures, function+": "+result.err.Error())
			continue
		}
		for _, entry := range result.entries {
			key := strings.ToLower(entry.model.ID)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			models = append(models, entry.model)
			traeModelFunctions.Store(key, entry.function)
			if canonical := strings.TrimSpace(entry.model.ID); canonical != "" {
				traeCanonicalModelNames.Store(key, canonical)
			}
		}
	}
	if len(models) == 0 {
		if len(failures) > 0 {
			return nil, fmt.Errorf("get_detail_param failed on all channels: %s", strings.Join(failures, "; "))
		}
		return nil, fmt.Errorf("get_detail_param returned no models")
	}
	return models, nil
}

// fetchTraeCatalog 拉取单条通道的模型目录并剔除不可选条目。
//
// [参数] auth: 已解析账号；function: chat_v3 或 solo_agent。
// [返回] 该通道下对用户可选的模型；请求失败、非 2xx、解析失败或过滤后为空均返回错误。
// 最近修改时间：2026-11-07；改动原因：从 fetchTraeModels 拆出单通道实现。
func fetchTraeCatalog(auth *traeAuth, function string) ([]traeCatalogEntry, error) {
	payload := traeModelDetailRequest{
		Function:          function,
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

	// 通过宿主 HTTP 桥接执行请求，保证生产代理与请求观测策略继续生效。
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, fmt.Errorf("get_detail_param transport: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &UpstreamError{Status: resp.StatusCode, Body: truncateRedacted(string(resp.Body), 300)}
	}

	var detail traeModelDetailResponse
	if err := json.Unmarshal(resp.Body, &detail); err != nil {
		return nil, fmt.Errorf("decode get_detail_param response: %w", err)
	}
	entries := make([]traeCatalogEntry, 0, len(detail.ConfigInfoList))
	for _, item := range detail.ConfigInfoList {
		if !isSelectableTraeModel(item) {
			continue
		}
		name := item.DisplayConfig.DisplayName
		if name == "" {
			name = item.ConfigName
		}
		// 上游若标注了 raw_chat_function，以它为准；否则回落到本次请求的通道。
		functionForModel := function
		if rawFunction := strings.TrimSpace(item.RawChatFunction); rawFunction != "" {
			functionForModel = rawFunction
		}
		entries = append(entries, traeCatalogEntry{
			model: pluginapi.ModelInfo{
				ID:                         item.ConfigName,
				Name:                       name,
				OwnedBy:                    providerName,
				SupportedGenerationMethods: []string{"chat"},
			},
			function: functionForModel,
		})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s returned no selectable models", function)
	}
	return entries, nil
}

// isSelectableTraeModel 判定目录条目是否可以暴露给客户端。
//
// 过滤维度对标开源实现：对用户隐藏、开关关闭、自定义模型、内部用途标签、
// 内部模型前缀，以及 -auto 兜底条目。放宽这些过滤不会多出可用模型，
// 只会把内部模型暴露成噪音（并诱发 4001/4023）。
//
// [参数] item: 上游 config_info_list 中的单条配置。
// [返回] 可作为聊天模型暴露时返回 true。
// 最近修改时间：2026-11-07；改动原因：新增，补齐此前完全缺失的目录过滤。
func isSelectableTraeModel(item traeModelConfigInfo) bool {
	name := strings.TrimSpace(item.ConfigName)
	if name == "" {
		return false
	}
	if item.IsInvisibleToUser {
		return false
	}
	if item.ConfigSwitch != nil && !*item.ConfigSwitch {
		return false
	}
	if item.DisplayConfig.IsCustomModel {
		return false
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, "-auto") {
		return false
	}
	if _, internal := traeInternalModelUsage[strings.ToLower(strings.TrimSpace(item.Usage))]; internal {
		return false
	}
	for _, prefix := range traeInternalModelPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	return true
}

// authModelsForIndex 返回指定账号当前可用的模型列表，供面板「测试」弹窗选择。
//
// 复用账号级动态发现链路：优先未过期缓存，未命中时用该账号凭据实时拉取；
// 末尾追加随机兜底模型，保证上游瞬时失败时弹窗仍有一个可测选项。
//
// [参数] authIndex：宿主账号索引。
// [返回] 去重后的模型 ID 列表；账号凭据不可用时返回 nil。
// 最近修改时间 2026-10-09（新增测试按钮指定模型选择支持）
func authModelsForIndex(authIndex string) []string {
	sa, err := hostAuthGet(authIndex)
	if err != nil || sa == nil {
		return nil
	}
	models := dynamicTraeModelsFromCacheOrAuth()
	if len(models) == 0 {
		if dyn, ferr := fetchTraeModels(sa); ferr == nil && len(dyn) > 0 {
			storeTraeDynamicModels(dyn)
			models = dyn
		}
	}
	seen := make(map[string]struct{}, len(models)+1)
	out := make([]string, 0, len(models)+1)
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" || isInternalNonChatTraeModel(id) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if fallback := pickRandomTraeModel(sa); fallback != "" {
		if _, ok := seen[fallback]; !ok {
			out = append(out, fallback)
		}
	}
	return out
}

// handleModelsQuery 返回指定账号当前可用模型列表，供面板「测试」弹窗选择。
//
// [参数] req：管理请求，query 参数 auth_index 指定账号。
// [返回] {models:[...]}；缺少 auth_index 时返回 {error}。
// 最近修改时间 2026-10-11 21:58:00（打开测试弹窗时后台预热流式 TLS 连接）
func handleModelsQuery(req pluginapi.ManagementRequest) map[string]any {
	authIndex := ""
	if vals := req.Query["auth_index"]; len(vals) > 0 {
		authIndex = strings.TrimSpace(vals[0])
	}
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required"}
	}
	go prewarmStreamHTTPClient(apiHostFor(nil))
	return map[string]any{"models": authModelsForIndex(authIndex)}
}
