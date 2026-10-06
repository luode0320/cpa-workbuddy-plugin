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

// wbModels 原为硬编码静态模型列表。现已根据规范彻底去除写死模型，
// 模型完全依赖上游动态拉取（自动获取），返回 nil。
func wbModels() []pluginapi.ModelInfo {
	return nil
}

var (
	configuredModels   []pluginapi.ModelInfo
	configuredModelsMu sync.RWMutex
)

func setConfiguredModels(models []pluginapi.ModelInfo) {
	configuredModelsMu.Lock()
	configuredModels = models
	configuredModelsMu.Unlock()
}

func clearConfiguredModels() {
	configuredModelsMu.Lock()
	configuredModels = nil
	configuredModelsMu.Unlock()
}

func getConfiguredModels() []pluginapi.ModelInfo {
	configuredModelsMu.RLock()
	defer configuredModelsMu.RUnlock()
	return configuredModels
}

func parseModelsConfig(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return
	}
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
			Context   int64  `json:"context"`
			MaxTokens int64  `json:"max_tokens"`
			Enabled   *bool  `json:"enabled"`
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

// resolveModels 按优先级链解析最终模型列表：动态拉取 > config_yaml 覆盖 > 静态兜底(nil)。
// 动态拉取或配置有值时直接返回该列表；未配置且动态不可用时返回空切片，严禁注入硬编码写死模型。
func resolveModels(dynamic, configured, fallback []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	if dm := nonEmptyModels(dynamic); len(dm) > 0 {
		return dm
	}
	if cm := nonEmptyModels(configured); len(cm) > 0 {
		return cm
	}
	return nonEmptyModels(fallback)
}

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
	if dynamicModelsCache.models == nil || time.Since(dynamicModelsCache.fetched) > dynamicModelsCacheTTL {
		return nil, false
	}
	out := make([]pluginapi.ModelInfo, len(dynamicModelsCache.models))
	copy(out, dynamicModelsCache.models)
	return out, true
}

func storeDynamicModels(models []pluginapi.ModelInfo) {
	dynamicModelsCache.Lock()
	dynamicModelsCache.models = models
	dynamicModelsCache.fetched = time.Now()
	dynamicModelsCache.Unlock()
}

// upstreamModelEntry 承载 models 接口返回的单个条目，兼容驼峰、下划线及对象形式。
type upstreamModelEntry struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Disabled           bool            `json:"disabled"`
	Status             string          `json:"status"`
	MaxInputTokens     *int64          `json:"maxInputTokens"`
	MaxOutputTokens    *int64          `json:"maxOutputTokens"`
	MaxAllowedSize     *int64          `json:"maxAllowedSize"`
	MaxContextLength   *int64          `json:"maxContextLength"`
	ContextWindow      json.RawMessage `json:"contextWindow"`
	MaxTokens          *int64          `json:"maxTokens"`
	MaxCompletionToken *int64          `json:"maxCompletionTokens"`

	// 兼容蛇形命名
	MaxInputTokensSnake     *int64 `json:"max_input_tokens"`
	MaxOutputTokensSnake    *int64 `json:"max_output_tokens"`
	MaxTokensSnake          *int64 `json:"max_tokens"`
	RecommendedContextSnake *int64 `json:"recommended_context"`
}

func firstPositive(vals ...*int64) int64 {
	for _, v := range vals {
		if v != nil && *v > 0 {
			return *v
		}
	}
	return 0
}

func (m upstreamModelEntry) contextWindowVal() *int64 {
	if len(m.ContextWindow) == 0 {
		return nil
	}
	var n int64
	if err := json.Unmarshal(m.ContextWindow, &n); err == nil && n > 0 {
		return &n
	}
	var cw struct {
		DefaultLength    *int64  `json:"defaultLength"`
		SupportedLengths []int64 `json:"supportedLengths"`
	}
	if err := json.Unmarshal(m.ContextWindow, &cw); err == nil {
		var maxLen int64
		for _, v := range cw.SupportedLengths {
			if v > maxLen {
				maxLen = v
			}
		}
		if maxLen > 0 {
			return &maxLen
		}
		if cw.DefaultLength != nil && *cw.DefaultLength > 0 {
			return cw.DefaultLength
		}
	}
	return nil
}

func (m upstreamModelEntry) contextLength() int64 {
	return firstPositive(m.MaxInputTokens, m.MaxAllowedSize, m.MaxContextLength, m.contextWindowVal(), m.MaxInputTokensSnake, m.RecommendedContextSnake)
}

func (m upstreamModelEntry) maxOutputTokens() int64 {
	return firstPositive(m.MaxOutputTokens, m.MaxCompletionToken, m.MaxTokens, m.MaxOutputTokensSnake, m.MaxTokensSnake)
}

func parseModelsAPIResponse(body []byte) ([]pluginapi.ModelInfo, error) {
	var rawEnvelope struct {
		Code int `json:"code"`
		Data struct {
			List   []upstreamModelEntry `json:"list"`
			Models []upstreamModelEntry `json:"models"`
			Agents []struct {
				Name   string   `json:"name"`
				Models []string `json:"models"`
			} `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &rawEnvelope); err != nil {
		return nil, err
	}
	if rawEnvelope.Code != 0 {
		return nil, fmt.Errorf("models API code %d", rawEnvelope.Code)
	}

	var cliModelIDs []string
	for _, a := range rawEnvelope.Data.Agents {
		if a.Name == "cli" {
			cliModelIDs = a.Models
			break
		}
	}

	var out []pluginapi.ModelInfo
	seen := make(map[string]struct{})

	addEntry := func(m upstreamModelEntry) {
		if m.ID == "" || m.Disabled || strings.EqualFold(m.Status, "offline") || strings.EqualFold(m.Status, "disabled") {
			return
		}
		lower := strings.ToLower(m.ID)
		if _, exists := seen[lower]; exists {
			return
		}
		seen[lower] = struct{}{}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, pluginapi.ModelInfo{
			ID:                         m.ID,
			Name:                       name,
			ContextLength:              m.contextLength(),
			MaxCompletionTokens:        m.maxOutputTokens(),
			OwnedBy:                    providerName,
			SupportedGenerationMethods: []string{"chat"},
		})
	}

	if len(cliModelIDs) > 0 {
		modelMap := make(map[string]upstreamModelEntry, len(rawEnvelope.Data.Models))
		for _, m := range rawEnvelope.Data.Models {
			modelMap[m.ID] = m
		}
		for _, id := range cliModelIDs {
			if m, ok := modelMap[id]; ok {
				addEntry(m)
			}
		}
	} else {
		for _, m := range rawEnvelope.Data.Models {
			addEntry(m)
		}
		for _, m := range rawEnvelope.Data.List {
			addEntry(m)
		}
	}

	return out, nil
}

func callModelsAPI(accessToken, enterpriseID string) ([]pluginapi.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	targetURL := endpointModels
	if enterpriseID != "" {
		targetURL = upstreamBase + "/console/enterprises/" + enterpriseID + "/config/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", originReferer)
	req.Header.Set("Referer", originReferer+"/")
	req.Header.Set("User-Agent", clientUA)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models API status %d", resp.StatusCode)
	}
	return parseModelsAPIResponse(resp.Body)
}

func extractAuthInfo(raw []byte) (string, string, bool) {
	var flat struct {
		AccessToken  string `json:"accessToken"`
		EnterpriseID string `json:"enterpriseId"`
	}
	token := ""
	entID := ""
	if err := json.Unmarshal(raw, &flat); err == nil {
		token = strings.TrimSpace(flat.AccessToken)
		entID = strings.TrimSpace(flat.EnterpriseID)
	}
	var nested storedAuth
	if err := json.Unmarshal(raw, &nested); err == nil {
		if token == "" {
			token = strings.TrimSpace(nested.Auth.AccessToken)
		}
		if entID == "" {
			entID = strings.TrimSpace(nested.Account.EnterpriseID)
		}
	}
	return token, entID, token != ""
}

func fetchDynamicModelsFromStorage(storageJSON []byte) []pluginapi.ModelInfo {
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	accessToken := ""
	enterpriseID := ""
	if len(storageJSON) > 0 {
		if tok, ent, ok := extractAuthInfo(storageJSON); ok {
			accessToken = tok
			enterpriseID = ent
		}
	}
	if accessToken == "" {
		return nil
	}
	dyn, err := callModelsAPI(accessToken, enterpriseID)
	if err != nil || len(dyn) == 0 {
		return nil
	}
	storeDynamicModels(dyn)
	return dyn
}

func dynamicModelsFromCacheOrAuth() []pluginapi.ModelInfo {
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	files, err := hostAuthList()
	if err != nil || len(files) == 0 {
		return nil
	}
	for _, f := range files {
		if f.Disabled {
			continue
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil || sa == nil || sa.Auth.AccessToken == "" {
			continue
		}
		dyn, err := callModelsAPI(sa.Auth.AccessToken, sa.Account.EnterpriseID)
		if err == nil && len(dyn) > 0 {
			storeDynamicModels(dyn)
			return dyn
		}
	}
	return nil
}

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
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if _, skip := excludeSet[strings.ToLower(m.ID)]; skip {
			continue
		}
		out = append(out, m)
	}
	return out
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

func resolveUpstreamModel(model string, authAttrs map[string]string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return model
	}
	lower := strings.ToLower(model)
	for _, k := range []string{"model_alias", "model-alias", "oauth-model-alias"} {
		if mapping, ok := authAttrs[k]; ok {
			var m map[string]string
			if err := json.Unmarshal([]byte(mapping), &m); err == nil {
				for a, r := range m {
					if strings.ToLower(strings.TrimSpace(a)) == lower {
						return strings.TrimSpace(r)
					}
				}
			}
		}
	}
	modelAliasCache.RLock()
	defer modelAliasCache.RUnlock()
	if realID, ok := modelAliasCache.byAlias[lower]; ok && realID != "" {
		return realID
	}
	return model
}

func handleModelStatic(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cacheModelAliases(req.Host)
	models := resolveModels(dynamicModelsFromCacheOrAuth(), getConfiguredModels(), wbModels())
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

func handleModelForAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cacheModelAliases(req.Host)
	models := resolveModels(fetchDynamicModelsFromStorage(req.StorageJSON), getConfiguredModels(), wbModels())
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}
