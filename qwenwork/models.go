// models.go implements the ModelProvider capability: static and per-auth
// model lists, dynamic model discovery via the upstream models API, alias
// reverse resolution (client-facing alias → upstream model id), and the
// host-config oauth-excluded-models filter.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// wbModels 是 QwenWork 的静态兜底模型表。
//
// 实测 /api/desktop/chat-modes 返回 3 个逻辑档位（flash / pro /
// qwen3.8-max-preview），全部 openai/chat-completions。这里同时保留几个
// 可直接推理的物理模型名作为兜底，便于用户手动指定。
//
// [参数] 无。
// [返回] 静态模型列表，每项已按官方规格填充 MaxCompletionTokens。
// 最近修改时间：2026-10-17 23:55:00；改动原因：替换为 QwenWork 真实档位。
func wbModels() []pluginapi.ModelInfo {
	list := []pluginapi.ModelInfo{
		{ID: "flash", Name: "标准", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "pro", Name: "专业", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qwen3.8-max-preview", Name: "Qwen3.8-Max Preview", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qwen3.8-max", Name: "Qwen3.8-Max", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5.2", Name: "GLM-5.2", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
	}
	// 2026-11-07 按社区共识改造：上游未提供输出上限时不再用统一 128000
	// 虚构 MaxCompletionTokens，如实留空（0 表示未知）。
	return list
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

// callModelsAPIFn 是 callModelsAPI 的可替换入口（同 sendActivePingQoderFn 惯例），
// 供负缓存行为单测注入假上游，避免真实网络与宿主 RPC 依赖。
var callModelsAPIFn = callModelsAPI

// fetchDynamicModelsFallbackFn 是存储入口回退到全量扫描的调用点接缝。
// 单测据此断言「回退发生在开启负缓存窗口之前」——顺序颠倒会让窗口
// 立刻拦住回退本身，使另一账号本可成功的扫描永远走不到。
var fetchDynamicModelsFallbackFn = fetchDynamicModels

// inDynamicModelsFailWindow 判断当前是否处于模型发现负缓存窗口内。
// [返回] true 表示窗口内，调用方应直接放弃本次逐账号扫描。
func inDynamicModelsFailWindow() bool {
	dynamicModelsCache.RLock()
	defer dynamicModelsCache.RUnlock()
	return !dynamicModelsCache.failedAt.IsZero() && time.Since(dynamicModelsCache.failedAt) < dynamicModelsFailCacheTTL
}

// markDynamicModelsFailed 记录一次「全账号扫描失败」，开启负缓存窗口。
func markDynamicModelsFailed() {
	dynamicModelsCache.Lock()
	dynamicModelsCache.failedAt = time.Now()
	dynamicModelsCache.Unlock()
}

func storeDynamicModels(models []pluginapi.ModelInfo) {
	dynamicModelsCache.Lock()
	dynamicModelsCache.models = models
	dynamicModelsCache.fetched = time.Now()
	dynamicModelsCache.failedAt = time.Time{} // 成功即清除负缓存窗口
	dynamicModelsCache.Unlock()
}

// fetchDynamicModels 按宿主已注册的 qwenwork 凭据逐个尝试动态发现。
//
// **不**在此处兜底静态列表：返回空切片语义为"动态发现不可用"，由调用方按
// resolveModels 的优先级链回退配置或静态默认。若这里返回 wbModels()，
// 调用方无法区分"上游真的只有这些模型"与"上游调用失败"。
// [参数] 无
// [返回] 动态模型列表；动态不可用时返回 nil
// 最近修改时间 2026-10-19 13:10:00；改动原因：新增失败负缓存窗口拦截，切断 N+1 重扫风暴
func fetchDynamicModels() []pluginapi.ModelInfo {
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	// 负缓存窗口内直接放弃逐账号重扫：宿主每次 auth 重写都会触发
	// model.for_auth，若失败不留窗口，N 账号 × 每事件一次全扫会放大成
	// N+1 请求风暴（生产 OOM 主因）。
	if inDynamicModelsFailWindow() {
		return nil
	}
	files, err := hostAuthListFiles()
	if err != nil || len(files) == 0 {
		return nil
	}
	// Strict filename-prefix match — same filter as host_auth.go hostAuthList.
	// (Earlier code also matched files containing "codebuddy" anywhere, which
	// would wrongly include workbuddy-*.json auths here and cause us to call
	// the qwenwork models API with a workbuddy token.)
	prefix := authFilePrefix
	scanned := 0
	for _, f := range files {
		if !strings.HasPrefix(strings.ToLower(f.Name), prefix) {
			continue
		}
		scanned++
		raw, err := hostAuthGetByIndex(f.AuthIndex)
		if err != nil {
			continue
		}
		sa, err := parseStored(raw)
		if err != nil || sa == nil {
			continue
		}
		dyn, err := callModelsAPIFn(sa)
		if err == nil && len(dyn) > 0 {
			storeDynamicModels(dyn)
			return dyn
		}
	}
	if scanned > 0 {
		markDynamicModelsFailed()
	}
	return nil
}

// fetchDynamicModelsFromStorage 用请求自带的账号凭据优先尝试动态发现，
// 失败时再回退到已注册凭据扫描。**不**兜底配置与静态列表。
// [参数] storageJSON：宿主传入的账号凭据 JSON
// [返回] 动态模型列表；动态不可用时返回 nil
// 最近修改时间 2026-10-19 13:10:00；改动原因：新增失败负缓存窗口拦截与失败标记
func fetchDynamicModelsFromStorage(storageJSON []byte) []pluginapi.ModelInfo {
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	// 负缓存窗口内直接返回 nil，让上层走 resolveModels 静态兜底。
	if inDynamicModelsFailWindow() {
		return nil
	}
	sa, err := parseStored(storageJSON)
	if err != nil || sa == nil {
		return fetchDynamicModelsFallbackFn()
	}
	dyn, dynErr := callModelsAPIFn(sa)
	if dynErr == nil && len(dyn) > 0 {
		storeDynamicModels(dyn)
		return dyn
	}
	// 本账号被拒后先回退全量扫描，回退必须在开窗之前：窗口一旦置位，
	// 全量扫描入口立即返回 nil，本函数声明的「回退到已注册凭据扫描」
	// 就变成死路径（另一账号本可成功）。
	if dyn := fetchDynamicModelsFallbackFn(); len(dyn) > 0 {
		return dyn
	}
	// 回退也拿不到结果才开窗：本账号确实打过上游，属真实失败而非
	// 「本地没账号」，可以安全进入负缓存。
	markDynamicModelsFailed()
	return nil
}

// callModelsAPI 调 GET /api/desktop/chat-modes 拉取模型目录。
//
// 该端点需要宿主 access token（不是 MaaS key），返回 schema_version=2 的
// catalog 数组。实测只有 3 个逻辑档位，全部 openai/chat-completions。
//
// [参数] sa 账号凭据，用于取宿主 token；为 nil 时返回错误。
// [返回] 启用的模型列表；请求失败或解析失败时返回错误。
// 最近修改时间：2026-10-17 23:55:00；改动原因：替换 /algo/api/v2/model/list。
func callModelsAPI(sa *storedAuth) ([]pluginapi.ModelInfo, error) {
	if sa == nil {
		return nil, fmt.Errorf("models: nil stored auth")
	}
	hostToken := strings.TrimSpace(sa.Auth.AccessToken)
	if hostToken == "" {
		return nil, fmt.Errorf("models: account has no host access token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointChatModes, nil)
	if err != nil {
		return nil, err
	}
	commonHeaders(req)
	req.Header.Set("Authorization", "Bearer "+hostToken)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("[qwenwork] callModelsAPI: upstream status %d, body: %s", resp.StatusCode, truncateRedacted(string(resp.Body), 200))
		return nil, fmt.Errorf("chat-modes API status %d", resp.StatusCode)
	}
	out, err := parseChatModesCatalog(resp.Body)
	if err != nil {
		return nil, err
	}
	log.Printf("[qwenwork] callModelsAPI: dynamic discovery ok: %d models", len(out))
	return out, nil
}

// parseChatModesCatalog 解析 GET /api/desktop/chat-modes 的目录响应。
//
// 只保留 enable=true 且 format 为 openai* 的条目：上游对 OpenAI 与 Anthropic
// 两种协议都接受，但 CPA 侧统一按 chat-completions 转发。
//
// [参数] raw 上游响应体。
// [返回] 可用模型列表；解析失败或没有可用模型时返回错误。
// 最近修改时间：2026-10-18 00:42:00；改动原因：抽成纯函数以便单测。
func parseChatModesCatalog(raw []byte) ([]pluginapi.ModelInfo, error) {
	var apiResp struct {
		SchemaVersion int `json:"schema_version"`
		Catalog       []struct {
			Key            string `json:"key"`
			Enable         bool   `json:"enable"`
			Format         string `json:"format"`
			IsReasoning    bool   `json:"is_reasoning"`
			MaxInputTokens int64  `json:"max_input_tokens"`
			I18N           struct {
				DisplayName map[string]string `json:"display_name"`
			} `json:"i18n"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return nil, fmt.Errorf("chat-modes parse: %w", err)
	}
	var out []pluginapi.ModelInfo
	for _, m := range apiResp.Catalog {
		if !m.Enable || strings.TrimSpace(m.Key) == "" {
			continue
		}
		if f := strings.TrimSpace(m.Format); f != "" && !strings.HasPrefix(f, "openai") {
			continue
		}
		ctxLen := int64(1000000)
		if m.MaxInputTokens > 0 {
			ctxLen = m.MaxInputTokens
		}
		name := strings.TrimSpace(m.I18N.DisplayName["zh"])
		if name == "" {
			name = strings.TrimSpace(m.I18N.DisplayName["en"])
		}
		if name == "" {
			name = m.Key
		}
		// 2026-11-07 按社区共识改造：上游 catalog 只提供 max_input_tokens，
		// 不提供输出上限，故不再用统一 128000 虚构 MaxCompletionTokens。
		out = append(out, pluginapi.ModelInfo{
			ID:                         m.Key,
			Name:                       name,
			ContextLength:              ctxLen,
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

// defaultModelAliasMap 把客户端模型名映射到上游模型 ID。
//
// QwenWork 的模型 ID 与上游一致（OpenAI 兼容），无需 Qoder 那种 qmodel_* 重写；
// 这里只保留空表，别名能力仍由 auth 属性 model_alias 提供。
var defaultModelAliasMap = map[string]string{}

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
	if canonical, ok := defaultModelAliasMap[key]; ok {
		return canonical
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
// the qwenwork provider. The host passes this config via HostConfigSummary.
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

// authModelsForIndex 返回指定账号当前可用的模型列表，供面板「测试」弹窗选择。
//
// 复用账号级动态发现链路：优先未过期缓存，未命中时用该账号凭据实时拉取；
// 末尾追加随机兜底模型，保证上游瞬时失败时弹窗仍有一个可测选项。
//
// [参数] authIndex：宿主账号索引。
// [返回] 去重后的模型 ID 列表；账号凭据不可用时返回 nil。
// 最近修改时间 2026-10-10（新增测试按钮指定模型选择支持，同步自 workbuddy）
func authModelsForIndex(authIndex string) []string {
	sa, err := hostAuthGet(authIndex)
	if err != nil || sa == nil {
		return nil
	}
	models, _ := cachedDynamicModels()
	if len(models) == 0 && strings.TrimSpace(sa.Auth.AccessToken) != "" {
		if dyn, ferr := callModelsAPI(sa); ferr == nil && len(dyn) > 0 {
			storeDynamicModels(dyn)
			models = dyn
		} else if ferr != nil {
			log.Printf("[qwenwork] authModelsForIndex: dynamic discovery failed (%v), falling back to static list", ferr)
		}
	}
	if len(models) == 0 {
		models = resolveModels(nil, getConfiguredModels(), wbModels())
	}
	seen := make(map[string]struct{}, len(models)+1)
	out := make([]string, 0, len(models)+1)
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if fallback := pickRandomQoderModel(sa); fallback != "" {
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
// 最近修改时间 2026-10-10（新增测试按钮指定模型选择支持，同步自 workbuddy）
func handleModelsQuery(req pluginapi.ManagementRequest) map[string]any {
	authIndex := ""
	if vals := req.Query["auth_index"]; len(vals) > 0 {
		authIndex = strings.TrimSpace(vals[0])
	}
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required"}
	}
	return map[string]any{"models": authModelsForIndex(authIndex)}
}
