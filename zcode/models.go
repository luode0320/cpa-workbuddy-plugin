package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/zcode/internal/mimic"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var defaultModels = []string{
	"GLM-5.3",
	"GLM-5.3-Flash",
	"GLM-4-Plus",
	"GLM-4-Air",
	"GLM-4-Flash",
}

var (
	// modelHTTPClient 用于模型列表、令牌换取、额度查询等元数据短请求，15 秒整体超时即快速失败边界。
	modelHTTPClient = &http.Client{Timeout: 15 * time.Second}

	// inferenceHTTPClient 专用于推理请求（流式 / 非流式 / 探活）：不设整体超时，避免长思考输出在读响应体时被掐断；建连挂死改由按操作的 deadline 兜底。
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

	modelCache struct {
		sync.RWMutex
		byKey     map[string][]string
		expiresAt map[string]time.Time
	}
)

func init() {
	modelCache.byKey = make(map[string][]string)
	modelCache.expiresAt = make(map[string]time.Time)
}

type upstreamModelListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// fetchUpstreamModels 使用指定的 API Key 请求上游 /v1/models。
func fetchUpstreamModels(ctx context.Context, apiKey string) ([]string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return append([]string(nil), defaultModels...), nil
	}

	cfg := currentConfig()
	modelCache.RLock()
	if exp, ok := modelCache.expiresAt[apiKey]; ok && time.Now().Before(exp) {
		if list, found := modelCache.byKey[apiKey]; found && len(list) > 0 {
			defer modelCache.RUnlock()
			return append([]string(nil), list...), nil
		}
	}
	modelCache.RUnlock()

	targets := resolveUpstreamTargets(&StoredAuth{APIKey: apiKey}, currentConfig())
	if len(targets) == 0 {
		return append([]string(nil), defaultModels...), nil
	}
	target := targets[0]

	reqURL := target.URL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return append([]string(nil), defaultModels...), err
	}

	fp := mimic.New(cfg.Mimic, keyID(target.Credential))
	fp.ApplyRequest(req.Header, target.Credential, mimic.UUID4())
	applyTargetAuth(req.Header, target)

	resp, err := modelHTTPClient.Do(req)
	if err != nil {
		return append([]string(nil), defaultModels...), err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return append([]string(nil), defaultModels...), nil
	}

	var parsed upstreamModelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return append([]string(nil), defaultModels...), err
	}

	var models []string
	seen := make(map[string]struct{})
	for _, item := range parsed.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			models = append(models, id)
		}
	}

	if len(models) == 0 {
		models = append([]string(nil), defaultModels...)
	}

	ttl := time.Duration(cfg.ModelCacheSecs) * time.Second
	if ttl <= 0 {
		ttl = 300 * time.Second
	}

	modelCache.Lock()
	modelCache.byKey[apiKey] = models
	modelCache.expiresAt[apiKey] = time.Now().Add(ttl)
	modelCache.Unlock()

	return models, nil
}

// authModelsForIndex 获取指定账号（或默认）的可用模型列表。
func authModelsForIndex(authID string) []string {
	authID = strings.TrimSpace(authID)
	cleanID := strings.TrimSuffix(authID, ".json")
	accounts, err := listAllAuthFiles()
	if err == nil {
		for _, acc := range accounts {
			if acc.AuthID == cleanID || acc.AuthID == authID || authFileNameFor(acc.APIKey) == cleanID+".json" {
				if len(acc.Models) > 0 {
					return acc.Models
				}
				// 尝试从额度快照提取模型列表（entitlements 里的 show_name）
				if q := cachedQuota(acc.AuthID); q != nil && len(q.Entitlements) > 0 {
					var qModels []string
					qSeen := make(map[string]struct{})
					for _, ent := range q.Entitlements {
						name := strings.TrimSpace(ent.ShowName)
						if name == "" {
							name = strings.TrimPrefix(ent.Capability, "model:")
						}
						name = normalizeOfficialGLMModelID(name)
						if name != "" {
							if _, exists := qSeen[name]; !exists {
								qSeen[name] = struct{}{}
								qModels = append(qModels, name)
							}
						}
					}
					if len(qModels) > 0 {
						return qModels
					}
				}
				models, errFetch := fetchUpstreamModels(context.Background(), acc.APIKey)
				if errFetch == nil && len(models) > 0 {
					return models
				}
			}
		}
	}

	cfg := currentConfig()
	if cfg.APIKey != "" {
		models, errFetch := fetchUpstreamModels(context.Background(), cfg.APIKey)
		if errFetch == nil && len(models) > 0 {
			return models
		}
	}
	return append([]string(nil), defaultModels...)
}

// allConfiguredModels 返回所有已配置/已发现的模型 ModelInfo。
func allConfiguredModels() []pluginapi.ModelInfo {
	cfg := currentConfig()
	modelsSet := make(map[string]struct{})
	for _, m := range defaultModels {
		modelsSet[m] = struct{}{}
	}
	for _, m := range cfg.Models {
		if strings.TrimSpace(m) != "" {
			modelsSet[strings.TrimSpace(m)] = struct{}{}
		}
	}

	// 汇总所有账号的模型
	if accounts, err := listAllAuthFiles(); err == nil {
		for _, acc := range accounts {
			for _, m := range acc.Models {
				if strings.TrimSpace(m) != "" {
					modelsSet[strings.TrimSpace(m)] = struct{}{}
				}
			}
		}
	}

	var result []pluginapi.ModelInfo
	for id := range modelsSet {
		// 2026-11-07 按社区共识改造：zcode 的模型来自用户配置，没有上游输出
		// 上限元数据，故 MaxCompletionTokens 如实留零值（未知），不再用统一
		// 128000 虚构。上下文长度仍用高估值兜底——低估会让客户端误判上下文
		// 不足而提前截断历史。
		result = append(result, pluginapi.ModelInfo{
			ID:                         id,
			Object:                     "model",
			OwnedBy:                    zcodeProviderID,
			DisplayName:                id,
			Name:                       mappedModel(id, cfg),
			SupportedGenerationMethods: []string{"chat"},
			ContextLength:              defaultContextWindow,
			UserDefined:                true,
		})
	}
	return result
}

// filterExcludedModels 剔除宿主 oauth-excluded-models 中为 zcode-provider 列出的模型。
// 宿主只在 model.for_auth 的按账号注册路径上替插件过滤（sdk/cliproxy/service_executors.go
// 的 applyExcludedModels），model.static 返回的列表会被原样登记进全局模型目录并参与
// 路由选模（internal/pluginhost/adapters.go 的 RegisterModels 无排除调用），因此插件必须
// 在出口自行过滤；同名裸模型被多个 OAuth 插件同时认领时，这是唯一能把模型从某个服务商
// 摘掉的入口。
// [参数] models: 插件已解析完成的模型列表；host: 宿主随请求传入的配置摘要。
// [返回] 过滤后的模型列表；宿主未配置排除项时原样返回入参。
// 最近修改时间：2026-10-12 02:05:00；改动原因：补齐 zcode 与 workbuddy/qoder-ai/qoderwork/
// traework 一致的 oauth-excluded-models 过滤，使 GLM-5.3 这类多服务商同名模型可被定向摘除。
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
	// 返回新切片而非 models[:0] 就地压缩：入参可能来自账号模型缓存或
	// allConfiguredModels 的底层数组，就地写回会让缓存永久变成"已过滤列表"。
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if _, skip := excludeSet[strings.ToLower(m.ID)]; skip {
			continue
		}
		out = append(out, m)
	}
	return out
}

var modelAliasCache struct {
	sync.RWMutex
	byAlias map[string]string
}

func cacheModelAliases(host pluginapi.HostConfigSummary) {
	entries := host.OAuthModelAlias[providerName]
	if len(entries) == 0 {
		for channel, list := range host.OAuthModelAlias {
			if strings.EqualFold(strings.TrimSpace(channel), providerName) {
				entries = list
				break
			}
		}
	}
	byAlias := make(map[string]string, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		alias := strings.TrimSpace(e.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		byAlias[strings.ToLower(alias)] = name
	}
	modelAliasCache.Lock()
	modelAliasCache.byAlias = byAlias
	modelAliasCache.Unlock()
}

// resolveUpstreamModel 将客户端请求的别名或非标准模型标识映射为上游真实模型 ID。
// 匹配顺序：账号属性级覆盖 > 宿主全局别名表 > 官方 GLM 模型名大小写归一化。
func resolveUpstreamModel(model string, attributes map[string]string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return model
	}
	key := strings.ToLower(m)
	if name, ok := parseModelAliasAttribute(attributes)[key]; ok {
		return normalizeOfficialGLMModelID(name)
	}
	modelAliasCache.RLock()
	name, ok := modelAliasCache.byAlias[key]
	modelAliasCache.RUnlock()
	if ok {
		return normalizeOfficialGLMModelID(name)
	}
	return normalizeOfficialGLMModelID(m)
}

func parseModelAliasAttribute(attributes map[string]string) map[string]string {
	if len(attributes) == 0 {
		return nil
	}
	raw := ""
	for _, k := range []string{"model_alias", "model-alias", "oauth-model-alias"} {
		if v := strings.TrimSpace(attributes[k]); v != "" {
			raw = v
			break
		}
	}
	if raw == "" {
		return nil
	}
	out := make(map[string]string)
	add := func(name, alias string) {
		name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
		if name != "" && alias != "" && !strings.EqualFold(name, alias) {
			out[strings.ToLower(alias)] = name
		}
	}
	if strings.HasPrefix(raw, "[") {
		var list []struct {
			Name  string `json:"name"`
			Alias string `json:"alias"`
		}
		if json.Unmarshal([]byte(raw), &list) == nil {
			for _, e := range list {
				add(e.Name, e.Alias)
			}
			return out
		}
	}
	if strings.HasPrefix(raw, "{") {
		var m map[string]string
		if json.Unmarshal([]byte(raw), &m) == nil {
			for alias, name := range m {
				add(name, alias)
			}
			return out
		}
	}
	for _, pair := range strings.Split(raw, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			add(kv[1], kv[0])
		}
	}
	return out
}

// normalizeOfficialGLMModelID 将模型名规范化为上游 Anthropic 兼容端点识别的权威大小写格式。
// 对齐 ZCode 官方客户端 normalizeOfficialGlmModelId 行为：
// 任何以 glm- 开头的模型名变体自动映射为官方标准大写，如 glm-5.3 -> GLM-5.3, glm-5.3-flash -> GLM-5.3-Flash。
func normalizeOfficialGLMModelID(model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return m
	}
	canonicalMap := map[string]string{
		"glm-5.3":       "GLM-5.3",
		"glm-5.3-flash": "GLM-5.3-Flash",
		"glm-5.2":       "GLM-5.2",
		"glm-5.1":       "GLM-5.1",
		"glm-5-turbo":   "GLM-5-Turbo",
		"glm-4.7":       "GLM-4.7",
		"glm-4.6":       "GLM-4.6",
		"glm-4.5":       "GLM-4.5",
		"glm-4.5-air":   "GLM-4.5-Air",
		"glm-4-plus":    "GLM-4-Plus",
		"glm-4-air":     "GLM-4-Air",
		"glm-4-flash":   "GLM-4-Flash",
		"glm-4v":        "GLM-4V",
		"glm-4v-plus":   "GLM-4V-Plus",
	}
	if standard, ok := canonicalMap[strings.ToLower(m)]; ok {
		return standard
	}
	if strings.HasPrefix(strings.ToLower(m), "glm-") {
		return "GLM-" + m[4:]
	}
	return m
}

func mappedModel(model string, cfg Config) string {
	if upstream, exists := cfg.ModelMap[model]; exists && strings.TrimSpace(upstream) != "" {
		return normalizeOfficialGLMModelID(strings.TrimSpace(upstream))
	}
	return resolveUpstreamModel(model, nil)
}

func keyID(apiKey string) string {
	if idx := strings.Index(apiKey, "."); idx > 0 {
		return apiKey[:idx]
	}
	return apiKey
}
