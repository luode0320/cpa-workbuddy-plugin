// models.go implements the ModelProvider capability: static and per-auth
// model lists, dynamic model discovery via the upstream models API, alias
// reverse resolution (client-facing alias → upstream model id), and the
// host-config oauth-excluded-models filter.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// wbModels is the static fallback model list for QoderWork CN. Keys mirror
// /root/qoderwork/models_list.json (KNOWLEDGE §6.2). Aliases use the qoder/
// prefix in AuthAttributes; bare IDs work too. Dynamic refresh via
// /algo/api/v2/model/list replaces this at runtime when an account is present.
func wbModels() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{
		{ID: "auto", Name: "Auto", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qmodel_preview", Name: "Qwen3.8-Max-Preview", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qmodel_latest", Name: "Qwen3.7-Max", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qmodel", Name: "Qwen3.7-Plus", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "q36fmodel", Name: "Qwen3.6-Flash", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "dmodel", Name: "DeepSeek-V4-Pro", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "dfmodel", Name: "DeepSeek-V4-Flash", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gm51model", Name: "GLM-5.2", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kmodel", Name: "Kimi-K2.7-Code", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "mmodel", Name: "MiniMax-M2.7", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
	}
}

// configuredModels holds the config_yaml `models:` override. Empty means
// fall back to dynamic discovery / wbModels(). Guarded by its own RWMutex:
// configure() runs on a host RPC thread while model.static / model.for_auth
// run on other RPC threads, so the slice must never be written/read unlocked.
// （同步自 workbuddy-provider 0.14.13：models 配置面板化闭环）
var (
	configuredModels   []pluginapi.ModelInfo
	configuredModelsMu sync.RWMutex
)

// setConfiguredModels 整体替换 config_yaml `models:` 覆盖列表。
// [参数] models：新的模型列表（非 nil 非空）
// [返回] 无
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func setConfiguredModels(models []pluginapi.ModelInfo) {
	configuredModelsMu.Lock()
	configuredModels = models
	configuredModelsMu.Unlock()
}

// clearConfiguredModels 清空覆盖，恢复动态获取 / 静态默认路径。
// [参数] 无
// [返回] 无
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func clearConfiguredModels() {
	configuredModelsMu.Lock()
	configuredModels = nil
	configuredModelsMu.Unlock()
}

// getConfiguredModels 返回当前覆盖列表的引用（调用方不得原地修改）。
// [参数] 无
// [返回] configuredModels 当前值，空切片语义为"未覆盖"
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func getConfiguredModels() []pluginapi.ModelInfo {
	configuredModelsMu.RLock()
	defer configuredModelsMu.RUnlock()
	return configuredModels
}

// parseModelsConfig decodes the config_yaml `models:` list. Items may be a
// string (model id) or an object {id, name, alias, context, max_tokens,
// enabled, reasoning}; enabled=false entries are skipped. An explicit empty
// list clears the override (back to dynamic discovery / static defaults); a
// list whose entries are all malformed leaves the current value untouched so
// a bad edit never silently wipes a working override.
// [参数] v：config_yaml `models:` 的 JSON 值（字符串列表或对象列表）
// [返回] 无（内部写入 configuredModels）
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func parseModelsConfig(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return
	}
	// 显式 `models: []` 表示"停止覆盖"，恢复动态获取 / 静态默认。
	if len(items) == 0 {
		clearConfiguredModels()
		return
	}
	var out []pluginapi.ModelInfo
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil && strings.TrimSpace(s) != "" {
			out = append(out, modelInfoFromConfig(strings.TrimSpace(s), "", 0, 0))
			continue
		}
		var mi struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Alias     string `json:"alias"`
			Context   int64  `json:"context"`
			MaxTokens int64  `json:"max_tokens"`
			Enabled   *bool  `json:"enabled"`
			Reasoning bool   `json:"reasoning"`
		}
		if err := json.Unmarshal(item, &mi); err != nil || strings.TrimSpace(mi.ID) == "" {
			continue
		}
		if mi.Enabled != nil && !*mi.Enabled {
			continue
		}
		out = append(out, modelInfoFromConfig(strings.TrimSpace(mi.ID), strings.TrimSpace(mi.Name), mi.Context, mi.MaxTokens))
	}
	if len(out) > 0 {
		setConfiguredModels(out)
	}
}

// resolveModels 按优先级链求最终模型列表：动态发现 > 配置 > 静态默认。
//
// 语义（2026-09-12 优先级反转）：
//   - 动态发现有结果时**完全忽略** config_yaml 覆盖与静态默认：上游是权威
//     全集，动态列表即最终列表，配置不再参与合并；
//   - 动态发现无结果（无凭据 / 上游失败 / 空列表）时，才使用配置覆盖；
//   - 配置也为空时，最后回退静态默认列表。
//
// 之所以要"动态优先"：上游新增模型只能从动态发现拿到；若配置优先，用户手工
// 配过的旧列表会永久遮蔽上游新模型。配置与静态列表降级为**保底**。
//
// [参数] dynamic：动态发现列表（可为空，空表示"动态不可用"）
//
//	configured：config_yaml `models:` 覆盖列表（可为空）
//	fallback：静态默认列表（wbModels()）
//
// [返回] 按优先级链选出的最终列表
// 最近修改时间 2026-09-12（由"配置优先"反转为"动态优先、配置保底"）
func resolveModels(dynamic, configured, fallback []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	// 先过滤空 ID 再看长度：只含空 ID 的动态列表语义上等于"没有结果"，
	// 不能因为它 len>0 就遮蔽配置或静态兜底。
	if dm := nonEmptyModels(dynamic); len(dm) > 0 {
		return dm
	}
	if cm := nonEmptyModels(configured); len(cm) > 0 {
		return cm
	}
	return nonEmptyModels(fallback)
}

// nonEmptyModels 过滤空 ID 条目并返回新切片，避免调用方拿到含空 ID 的列表或
// 共享底层数组（dynamicModelsCache / wbModels 的切片不可原地改写）。
// [参数] models：待过滤列表
// [返回] 去掉空 ID 后的新切片
// 最近修改时间 2026-09-12（随优先级反转新增）
func nonEmptyModels(models []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// resolveModelsFromStorage 是 handleModelForAuth 的判定入口：先尝试账号级
// 动态发现，成功即返回动态列表，否则交给优先级链回退配置 / 静态默认。
// [参数] storageJSON：宿主传入的账号凭据 JSON
// [返回] 按优先级链选出的最终列表
// 最近修改时间 2026-09-12（动态成功不再让配置遮蔽上游模型）
func resolveModelsFromStorage(storageJSON []byte) []pluginapi.ModelInfo {
	dynamic := fetchDynamicModelsFromStorage(storageJSON)
	return resolveModels(dynamic, getConfiguredModels(), wbModels())
}

// modelInfoFromConfig builds a ModelInfo for a config-declared model,
// normalizing fields to match the plugin's other model sources. alias /
// reasoning are accepted for schema compatibility but have no ModelInfo
// counterpart and are intentionally ignored here.
// [参数] id：模型 ID（必填）；name：显示名（空则用 id）；ctxLen：上下文长度；maxTok：最大输出 token 数
// [返回] 规范化后的 pluginapi.ModelInfo
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func modelInfoFromConfig(id, name string, ctxLen, maxTok int64) pluginapi.ModelInfo {
	if name == "" {
		name = id
	}
	return pluginapi.ModelInfo{
		ID:                         id,
		Name:                       name,
		ContextLength:              ctxLen,
		MaxCompletionTokens:        maxTok,
		OwnedBy:                    providerName,
		SupportedGenerationMethods: []string{"chat"},
	}
}

func cachedDynamicModels() ([]pluginapi.ModelInfo, bool) {
	dynamicModelsCache.RLock()
	defer dynamicModelsCache.RUnlock()
	if len(dynamicModelsCache.models) > 0 && time.Since(dynamicModelsCache.fetched) < dynamicModelsCacheTTL {
		return dynamicModelsCache.models, true
	}
	return nil, false
}

func storeDynamicModels(models []pluginapi.ModelInfo) {
	dynamicModelsCache.Lock()
	dynamicModelsCache.models = models
	dynamicModelsCache.fetched = time.Now()
	dynamicModelsCache.Unlock()
}

// fetchDynamicModels 按宿主已注册的 qoderwork 凭据逐个尝试动态发现。
//
// **不**在此处兜底静态列表：返回空切片语义为"动态发现不可用"，由调用方按
// resolveModels 的优先级链回退配置或静态默认。若这里返回 wbModels()，
// 调用方无法区分"上游真的只有这些模型"与"上游调用失败"。
// [参数] 无
// [返回] 动态模型列表；动态不可用时返回 nil
// 最近修改时间 2026-09-12（取消静默回退 wbModels，改由 resolveModels 统一兜底）
func fetchDynamicModels() []pluginapi.ModelInfo {
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	files, err := hostAuthListFiles()
	if err != nil || len(files) == 0 {
		return nil
	}
	// Strict filename-prefix match — same filter as host_auth.go hostAuthList.
	// (Earlier code also matched files containing "codebuddy" anywhere, which
	// would wrongly include workbuddy-*.json auths here and cause us to call
	// the qoderwork models API with a workbuddy token.)
	prefix := authFilePrefix
	for _, f := range files {
		if !strings.HasPrefix(strings.ToLower(f.Name), prefix) {
			continue
		}
		raw, err := hostAuthGetByIndex(f.AuthIndex)
		if err != nil {
			continue
		}
		sa, err := parseStored(raw)
		if err != nil || sa == nil {
			continue
		}
		dyn, err := callModelsAPI(sa)
		if err == nil && len(dyn) > 0 {
			storeDynamicModels(dyn)
			return dyn
		}
	}
	return nil
}

// fetchDynamicModelsFromStorage 用请求自带的账号凭据优先尝试动态发现，
// 失败时再回退到已注册凭据扫描。**不**兜底配置与静态列表。
// [参数] storageJSON：宿主传入的账号凭据 JSON
// [返回] 动态模型列表；动态不可用时返回 nil
// 最近修改时间 2026-09-12（取消配置短路与静态兜底，交给 resolveModels 判定）
func fetchDynamicModelsFromStorage(storageJSON []byte) []pluginapi.ModelInfo {
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	sa, err := parseStored(storageJSON)
	if err == nil && sa != nil {
		if dyn, dynErr := callModelsAPI(sa); dynErr == nil && len(dyn) > 0 {
			storeDynamicModels(dyn)
			return dyn
		}
	}
	return fetchDynamicModels()
}

// fetchDynamicModels calls the QoderWork API to get the latest model list.
// Falls back to the hardcoded list on any error.
// callModelsAPI GETs /algo/api/v2/model/list from the QoderWork gateway
// with COSY signing (same as inference). Returns plain JSON (not QoderEncoding).
// Falls back to wbModels() on any error.
func callModelsAPI(sa *storedAuth) ([]pluginapi.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// model/list needs no body but COSY still requires a body string for signing.
	// An empty JSON object works (verified in reference_impl.py).
	encodedBody := qoderEncode([]byte("{}"))
	rawURL := endpointModels // includes ?Encode=1
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// COSY signing needs the encoded body even for GET (signature includes body).
	if err := applyCosyHeaders(req, sa, encodedBody, rawURL, "", false); err != nil {
		return nil, fmt.Errorf("cosy sign: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", clientUA)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models API status %d", resp.StatusCode)
	}
	// Response is plain JSON: {"chat":[{key,display_name,...}], "developer":[...], ...}
	var apiResp map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body, &apiResp); err != nil {
		return nil, fmt.Errorf("models parse: %w", err)
	}
	// Prefer the "chat" scene (matches our inference use case).
	chatRaw, ok := apiResp["chat"]
	if !ok {
		return nil, fmt.Errorf("no chat scene in models response")
	}
	var models []struct {
		Key            string  `json:"key"`
		DisplayName    string  `json:"display_name"`
		Enable         bool    `json:"enable"`
		IsReasoning    bool    `json:"is_reasoning"`
		IsVL           bool    `json:"is_vl"`
		MaxInputTokens int64   `json:"max_input_tokens"`
		PriceFactor    float64 `json:"price_factor"`
	}
	if err := json.Unmarshal(chatRaw, &models); err != nil {
		return nil, fmt.Errorf("chat scene parse: %w", err)
	}
	var out []pluginapi.ModelInfo
	for _, m := range models {
		if !m.Enable {
			continue
		}
		ctx2 := int64(180000)
		if m.MaxInputTokens > 0 {
			ctx2 = m.MaxInputTokens
		}
		out = append(out, pluginapi.ModelInfo{
			ID:                         m.Key,
			Name:                       m.DisplayName,
			ContextLength:              ctx2,
			MaxCompletionTokens:        8192,
			OwnedBy:                    providerName,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no enabled chat models")
	}
	return out, nil
}

func cacheModelAliases(host pluginapi.HostConfigSummary) {
	entries := host.OAuthModelAlias[providerName]
	if len(entries) == 0 {
		// Host may key the channel case-insensitively; fall back to a scan.
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

// resolveUpstreamModel maps an aliased requested model back to the real
// upstream model ID. Returns the input unchanged when nothing matches.
func resolveUpstreamModel(model string, attributes map[string]string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return model
	}
	key := strings.ToLower(m)
	if name, ok := parseModelAliasAttribute(attributes)[key]; ok {
		return name
	}
	modelAliasCache.RLock()
	name, ok := modelAliasCache.byAlias[key]
	modelAliasCache.RUnlock()
	if ok {
		return name
	}
	return m
}

// parseModelAliasAttribute decodes a per-auth alias override from auth
// attributes. Accepts JSON ([{"name":...,"alias":...}] or {alias:name}) or
// comma-separated "alias=name" pairs.
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

// filterExcludedModels removes models listed in oauth-excluded-models for
// the qoderwork provider. The host passes this config via HostConfigSummary.
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
	// which may be the dynamicModelsCache's own slice. Mutating it in place
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

// publishUsage reports one upstream attempt into CPAMP request monitoring.
// requestedModel is client-facing (may be alias); upstreamModel is resolved.

// handleModelStatic 返回宿主要求的全局模型列表，优先级为动态 > 配置 > 静态默认。
// 动态发现仅能命中已有缓存（StaticModelRequest 不带账号凭据），缓存未命中时
// fetchDynamicModels 会扫描已注册凭据；都失败则回退配置 / 静态默认。
// [参数] raw：宿主传入的 StaticModelRequest
// [返回] 成功 envelope；请求解析失败时返回错误
// 最近修改时间 2026-09-12（由"配置非空即不查动态"反转为动态优先）
func handleModelStatic(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cacheModelAliases(req.Host)
	models := resolveModels(fetchDynamicModels(), getConfiguredModels(), wbModels())
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

// handleModelForAuth 返回指定账号的模型列表，优先级为动态 > 配置 > 静态默认。
// [参数] raw：宿主传入的 AuthModelRequest（含 StorageJSON 凭据）
// [返回] 成功 envelope；请求解析失败时返回错误
// 最近修改时间 2026-09-12（动态成功时完全忽略配置，避免遮蔽上游新增模型）
func handleModelForAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// Always return the plugin's canonical provider key. The host skips any
	// response whose Provider doesn't match the auth's provider, so echoing
	// req.AuthProvider back would silently drop the model list whenever the
	// auth file carries a non-canonical provider string.
	cacheModelAliases(req.Host)
	models := resolveModelsFromStorage(req.StorageJSON)
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}
