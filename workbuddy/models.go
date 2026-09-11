// models.go implements the ModelProvider capability: static and per-auth
// model lists, dynamic model discovery via the upstream models API, alias
// reverse resolution (client-facing alias → upstream model id), and the
// host-config oauth-excluded-models filter.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func wbModels() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{
		{ID: "glm-5.2", Name: "GLM-5.2", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5.1", Name: "GLM-5.1", ContextLength: 131072, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5v-turbo", Name: "GLM-5V Turbo", ContextLength: 131072, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kimi-k2.7", Name: "Kimi K2.7", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "minimax-m3", Name: "MiniMax M3", ContextLength: 204800, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "hy3", Name: "Hy3", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "hy3-preview", Name: "Hy3 Preview", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "hy3-preview-agent", Name: "Hy3 Preview Agent", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
	}
}

// configuredModels holds the config_yaml `models:` override. Empty means
// fall back to dynamic discovery / wbModels(). Guarded by its own RWMutex:
// configure() runs on a host RPC thread while model.static / model.for_auth
// run on other RPC threads, so the slice must never be written/read unlocked.
var (
	configuredModels   []pluginapi.ModelInfo
	configuredModelsMu sync.RWMutex
)

// setConfiguredModels 整体替换 config_yaml `models:` 覆盖列表。
// [参数] models：新的模型列表（非 nil 非空）
// [返回] 无
// 最近修改时间 2026-08-28 00:41:58（新增 config_yaml models 覆盖支持）
func setConfiguredModels(models []pluginapi.ModelInfo) {
	configuredModelsMu.Lock()
	configuredModels = models
	configuredModelsMu.Unlock()
}

// clearConfiguredModels 清空覆盖，恢复动态获取 / 静态默认路径。
// [参数] 无
// [返回] 无
// 最近修改时间 2026-08-28 00:41:58（新增 config_yaml models 覆盖支持）
func clearConfiguredModels() {
	configuredModelsMu.Lock()
	configuredModels = nil
	configuredModelsMu.Unlock()
}

// getConfiguredModels 返回当前覆盖列表的引用（调用方不得原地修改）。
// [参数] 无
// [返回] configuredModels 当前值，空切片语义为"未覆盖"
// 最近修改时间 2026-08-28 00:41:58（新增 config_yaml models 覆盖支持）
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
// 最近修改时间 2026-08-28 00:41:58（新增 config_yaml models 覆盖支持）
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

// modelInfoFromConfig builds a ModelInfo for a config-declared model,
// normalizing fields to match the plugin's other model sources. alias /
// reasoning are accepted for schema compatibility but have no ModelInfo
// counterpart and are intentionally ignored here.
// [参数] id：模型 ID（必填）；name：显示名（空则用 id）；ctxLen：上下文长度；maxTok：最大输出 token 数
// [返回] 规范化后的 pluginapi.ModelInfo
// 最近修改时间 2026-08-28 00:41:58（新增 config_yaml models 覆盖支持）
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

// resolveModels 按优先级链求最终模型列表：动态发现 > 配置 > 静态默认。
//
// 语义（2026-09-12 优先级反转）：
//   - 动态发现**有结果**时**完全忽略** config_yaml 覆盖与静态默认：上游是
//     权威全集，动态列表即最终列表，配置不再参与合并；
//   - 动态发现无结果（无凭据 / 上游失败 / 空列表）时，才使用配置覆盖；
//   - 配置也为空时，最后回退静态默认列表。
//
// 之所以要"动态优先"：上游新增模型（如 deepseek-v4.1-flash）只能从动态发现
// 拿到；若配置优先，用户手工配过的旧列表会永久遮蔽上游新模型。配置与静态
// 列表降级为**保底**，仅在动态发现不可用时起作用。
//
// 返回新切片，不修改入参（动态列表可能来自 dynamicModelsCache 的共享底层
// 数组，原地修改会污染缓存）。
// [参数] dynamic：动态发现列表（可为空，空表示"动态不可用"）
//
//	configured：config_yaml `models:` 覆盖列表（可为空）
//	fallback：静态默认列表（wbModels()）
//
// [返回] 按优先级链选出的最终列表
// 最近修改时间 2026-09-12（由"合并去重、配置优先"反转为"动态优先、配置保底"）
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
// 共享底层数组（dynModelsCache / wbModels 的切片不可原地改写）。
// [参数] models：待过滤列表
// [返回] 去掉空 ID 后的新切片
// 最近修改时间 2026-09-12（随优先级反转新增，替代原合并逻辑的去重职责）
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

// fetchDynamicModelsFromStorage 拉取动态模型列表，**不**在此处兜底静态列表。
//
// 返回空切片语义为"动态发现不可用"（无凭据 / 上游失败 / 空结果），由调用方
// 按 resolveModels 的优先级链回退到配置或静态默认。若这里直接返回
// wbModels()，调用方将无法区分"上游真的只有这些模型"与"上游调用失败"，
// 导致新增模型被静默遮蔽且无任何可观测信号。
// [参数] storageJSON：宿主传入的账号凭据 JSON
// [返回] 动态模型列表；动态不可用时返回 nil
// 最近修改时间 2026-09-12（取消静默回退 wbModels，改由 resolveModels 统一兜底）
func fetchDynamicModelsFromStorage(storageJSON []byte) []pluginapi.ModelInfo {
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	accessToken := ""
	if len(storageJSON) > 0 {
		if tok, ok := extractAccessToken(storageJSON); ok {
			accessToken = tok
		}
	}
	if accessToken == "" {
		return nil
	}
	if dyn, err := callModelsAPI(accessToken); err == nil && len(dyn) > 0 {
		storeDynamicModels(dyn)
		return dyn
	}
	return nil
}

// fetchDynamicModels calls the WorkBuddy API to get the latest model list.
// Falls back to the hardcoded list on any error.
// extractAccessToken handles both flat (CPA UI) and nested (plugin OAuth) auth file shapes.
func extractAccessToken(raw []byte) (string, bool) {
	// flat shape from CPA-Manager-Plus UI
	var flat struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(raw, &flat); err == nil && strings.TrimSpace(flat.AccessToken) != "" {
		return flat.AccessToken, true
	}
	// nested shape from plugin OAuth
	var nested storedAuth
	if err := json.Unmarshal(raw, &nested); err == nil && strings.TrimSpace(nested.Auth.AccessToken) != "" {
		return nested.Auth.AccessToken, true
	}
	return "", false
}

// realmFromToken decodes the JWT iss claim to determine the account realm.
// Global tokens have iss=...workbuddy.ai...; CN tokens have iss=...codebuddy.cn...
// Returns true if the token is Global.
func isGlobalToken(accessToken string) bool {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return false
	}
	payload := parts[1]
	// base64url padding
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	var claims struct {
		ISS string `json:"iss"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return false
	}
	return strings.Contains(strings.ToLower(claims.ISS), "workbuddy.ai")
}

// callModelsAPI GETs /console/enterprises/personal/models from the upstream.
// Uses the shared client (connection pooling) with a per-request 15s budget;
// the shared client's own 120s timeout stays as the outer bound.
func callModelsAPI(accessToken string) ([]pluginapi.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Model discovery is per-realm: Global tokens must query workbuddy.ai,
	// not copilot.tencent.com (which 500s for Global tokens). Decode JWT iss.
	isGlobal := isGlobalToken(accessToken)
	modelsURL := endpointModels
	origin := originReferer
	if isGlobal {
		modelsURL = upstreamBaseGlobal + "/console/enterprises/personal/models"
		origin = originRefererGlobal
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", clientUA)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	body := resp.Body
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models API status %d", resp.StatusCode)
	}
	return parseModelsAPIResponse(body)
}

// parseModelsAPIResponse 把上游 /console/enterprises/personal/models 的响应体
// 解析为模型列表。
//
// 抽取为纯函数便于用真实上游响应做回归测试：字段名与实际响应不匹配这类问题
// 在集成路径上表现为"静默取到 0"，没有单元测试很难发现（实测上游给的是
// maxInputTokens / maxOutputTokens，而非早期假设的 contextWindow / maxTokens）。
//
// [参数] body：上游响应体原始字节
// [返回] 模型列表；HTTP 之外的结构性错误（code!=0 / 无 cli 白名单）返回 error
// 最近修改时间 2026-09-12（字段名对齐真实上游并抽取为可测纯函数）
func parseModelsAPIResponse(body []byte) ([]pluginapi.ModelInfo, error) {
	var apiResp struct {
		Code int `json:"code"`
		Data struct {
			Models []upstreamModelEntry `json:"models"`
			Agents []struct {
				Name   string   `json:"name"`
				Models []string `json:"models"`
			} `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, err
	}
	if apiResp.Code != 0 {
		return nil, fmt.Errorf("models API code %d", apiResp.Code)
	}
	var cliModelIDs []string
	for _, a := range apiResp.Data.Agents {
		if a.Name == "cli" {
			cliModelIDs = a.Models
			break
		}
	}
	if len(cliModelIDs) == 0 {
		return nil, fmt.Errorf("no cli agent models found")
	}
	dynMap := make(map[string]upstreamModelEntry, len(apiResp.Data.Models))
	for _, m := range apiResp.Data.Models {
		dynMap[m.ID] = m
	}
	out := make([]pluginapi.ModelInfo, 0, len(cliModelIDs))
	for _, id := range cliModelIDs {
		m, ok := dynMap[id]
		if !ok {
			continue
		}
		if m.Disabled {
			continue
		}
		out = append(out, pluginapi.ModelInfo{
			ID:                         m.ID,
			Name:                       m.Name,
			ContextLength:              m.contextLength(),
			MaxCompletionTokens:        m.maxOutputTokens(),
			OwnedBy:                    providerName,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	return out, nil
}

// upstreamModelEntry 是上游 models 数组的单个条目。
//
// 字段名以真实上游响应为准（2026-09-12 实测 copilot.tencent.com）：
// 上下文上限是 maxInputTokens / maxAllowedSize，输出上限是 maxOutputTokens。
// 旧实现读 contextWindow / maxTokens —— 这两个字段上游从不返回，导致
// 所有动态模型的 ContextLength / MaxCompletionTokens 恒为 0。
type upstreamModelEntry struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Disabled           bool   `json:"disabled"`
	MaxInputTokens     *int64 `json:"maxInputTokens"`
	MaxOutputTokens    *int64 `json:"maxOutputTokens"`
	MaxAllowedSize     *int64 `json:"maxAllowedSize"`
	MaxContextLength   *int64 `json:"maxContextLength"`
	ContextWindow      *int64 `json:"contextWindow"`
	MaxTokens          *int64 `json:"maxTokens"`
	MaxCompletionToken *int64 `json:"maxCompletionTokens"`
}

// firstPositive 返回第一个非 nil 且为正数的值，全无时返回 0。
// [参数] vals：候选值指针列表（按优先级排列）
// [返回] 首个有效值；都不满足时 0
// 最近修改时间 2026-09-12（随字段名对齐新增，兼容新旧字段形态）
func firstPositive(vals ...*int64) int64 {
	for _, v := range vals {
		if v != nil && *v > 0 {
			return *v
		}
	}
	return 0
}

// contextLength 取上下文上限，优先真实字段，兼容旧字段别名。
// [参数] 无（接收者为上游条目）
// [返回] 上下文长度；缺失时 0
// 最近修改时间 2026-09-12（对齐上游 maxInputTokens）
func (m upstreamModelEntry) contextLength() int64 {
	return firstPositive(m.MaxInputTokens, m.MaxAllowedSize, m.MaxContextLength, m.ContextWindow)
}

// maxOutputTokens 取最大输出 token 数，优先真实字段，兼容旧字段别名。
// [参数] 无（接收者为上游条目）
// [返回] 最大输出 token 数；缺失时 0
// 最近修改时间 2026-09-12（对齐上游 maxOutputTokens）
func (m upstreamModelEntry) maxOutputTokens() int64 {
	return firstPositive(m.MaxOutputTokens, m.MaxCompletionToken, m.MaxTokens)
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
// the workbuddy provider. The host passes this config via HostConfigSummary.
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

// handleModelStatic 返回宿主要求的全局模型列表。
//
// 优先级链与 handleModelForAuth 一致（动态 > 配置 > 静态默认）：静态路径过去
// 只返回 wbModels() 而从不打上游，导致 CPA 配置刷新走静态路径时用户永远看不到
// 上游新增模型；两条路径行为不对称是"自动拉取没生效"的根因之一。
// StaticModelRequest 不带账号凭据，因此动态发现只能命中已有缓存（5 分钟 TTL），
// 缓存未命中即正常回退到配置 / 静态默认。
// [参数] raw：宿主传入的 StaticModelRequest
// [返回] 成功 envelope；请求解析失败时返回错误
// 最近修改时间 2026-09-12（接入动态发现缓存，与 for_auth 统一优先级链）
func handleModelStatic(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cacheModelAliases(req.Host)
	models := resolveModels(dynamicModelsFromCache(), getConfiguredModels(), wbModels())
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

// dynamicModelsFromCache 只读返回未过期的动态模型缓存，不触发上游请求。
// 供 model.static 这类没有账号凭据的路径使用。
// [参数] 无
// [返回] 缓存中的模型列表；无有效缓存时返回 nil
// 最近修改时间 2026-09-12（随 handleModelStatic 接入动态发现新增）
func dynamicModelsFromCache() []pluginapi.ModelInfo {
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	return nil
}

// handleModelForAuth 返回指定账号的模型列表，优先级为动态 > 配置 > 静态默认。
// [参数] raw：宿主传入的 AuthModelRequest（含 StorageJSON 凭据）
// [返回] 成功 envelope；请求解析失败时返回错误
// 最近修改时间 2026-09-12（优先级由"配置优先合并"反转为"动态优先、配置保底"）
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
	models := resolveModels(fetchDynamicModelsFromStorage(req.StorageJSON), getConfiguredModels(), wbModels())
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}
