package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func wbModels() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{
		// 通用与默认
		{ID: "auto", Name: "Auto", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},

		// OpenAI (GPT 系列)
		{ID: "gpt-5.4", Name: "GPT-5.4", ContextLength: 1000000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", ContextLength: 1000000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gpt-5", Name: "GPT-5", ContextLength: 1000000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gpt-5-mini", Name: "GPT-5 mini", ContextLength: 1000000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gpt-5-nano", Name: "GPT-5 nano", ContextLength: 1000000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gpt-4.1", Name: "GPT-4.1", ContextLength: 1000000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gpt-4o", Name: "GPT-4o", ContextLength: 128000, MaxCompletionTokens: 4096, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gpt-4o-mini", Name: "GPT-4o mini", ContextLength: 128000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "o3", Name: "o3", ContextLength: 200000, MaxCompletionTokens: 100000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "o3-mini", Name: "o3-mini", ContextLength: 200000, MaxCompletionTokens: 100000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "o1", Name: "o1", ContextLength: 200000, MaxCompletionTokens: 100000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},

		// Google Gemini 系列
		{ID: "gemini-3.5-flash", Name: "Gemini 3.5 Flash", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gemini-3.1-pro-preview", Name: "Gemini 3.1 Pro", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gemini-3.1-flash", Name: "Gemini 3.1 Flash", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gemini-3.1-flash-lite", Name: "Gemini 3.1 Flash-Lite", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},

		// DeepSeek 系列
		{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "deepseek-v4-flash", Name: "DeepSeek-V4 Flash", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "deepseek-chat", Name: "DeepSeek Chat", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "deepseek-reasoner", Name: "DeepSeek Reasoner", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},

		// GLM / Kimi / MiniMax / 腾讯混元等主流模型
		{ID: "glm-5.3", Name: "GLM-5.3", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5.3-flash", Name: "GLM-5.3 Flash", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5.2", Name: "GLM-5.2", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5.1", Name: "GLM-5.1", ContextLength: 131072, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5v-turbo", Name: "GLM-5V Turbo", ContextLength: 131072, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kimi-k3-1", Name: "Kimi K3.1", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kimi-k2.8-preview", Name: "Kimi K2.8 Preview", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kimi-k2.7", Name: "Kimi K2.7", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kimi-k2.6", Name: "Kimi K2.6", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kimi-k2.5", Name: "Kimi K2.5", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "minimax-m3", Name: "MiniMax M3", ContextLength: 204800, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "minimax-m2.7", Name: "MiniMax M2.7", ContextLength: 204800, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "minimax-m2.5", Name: "MiniMax M2.5", ContextLength: 204800, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "hy4-preview", Name: "Hy4 Preview", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "hy3", Name: "Hy3", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "hy3-x", Name: "Hy3-X", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "space-bunny", Name: "Space Bunny", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
	}
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

func resolveModels(dynamic, configured, fallback []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	if cm := nonEmptyModels(configured); len(cm) > 0 {
		return cm
	}
	dm := nonEmptyModels(dynamic)
	fb := nonEmptyModels(fallback)
	if len(dm) == 0 {
		return fb
	}
	seen := make(map[string]struct{}, len(dm)+len(fb))
	out := make([]pluginapi.ModelInfo, 0, len(dm)+len(fb))
	for _, m := range dm {
		lower := strings.ToLower(m.ID)
		if _, exists := seen[lower]; !exists {
			seen[lower] = struct{}{}
			out = append(out, m)
		}
	}
	for _, m := range fb {
		lower := strings.ToLower(m.ID)
		if _, exists := seen[lower]; !exists {
			seen[lower] = struct{}{}
			out = append(out, m)
		}
	}
	return out
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
	var rawEnvelope struct {
		Code int `json:"code"`
		Data struct {
			List []struct {
				ID                 string `json:"id"`
				Name               string `json:"name"`
				MaxTokens          int64  `json:"max_tokens"`
				MaxInputTokens     int64  `json:"max_input_tokens"`
				MaxOutputTokens    int64  `json:"max_output_tokens"`
				RecommendedContext int64  `json:"recommended_context"`
				Status             string `json:"status"`
			} `json:"list"`
			Models []struct {
				ID                 string `json:"id"`
				Name               string `json:"name"`
				MaxTokens          int64  `json:"max_tokens"`
				MaxInputTokens     int64  `json:"max_input_tokens"`
				MaxOutputTokens    int64  `json:"max_output_tokens"`
				RecommendedContext int64  `json:"recommended_context"`
				Status             string `json:"status"`
				Disabled           bool   `json:"disabled"`
			} `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &rawEnvelope); err != nil {
		return nil, err
	}
	var out []pluginapi.ModelInfo
	for _, m := range rawEnvelope.Data.List {
		if strings.EqualFold(m.Status, "offline") || strings.EqualFold(m.Status, "disabled") {
			continue
		}
		ctxLen := m.RecommendedContext
		if ctxLen <= 0 {
			ctxLen = m.MaxInputTokens
		}
		maxTok := m.MaxOutputTokens
		if maxTok <= 0 {
			maxTok = m.MaxTokens
		}
		out = append(out, pluginapi.ModelInfo{
			ID:                         m.ID,
			Name:                       m.Name,
			ContextLength:              ctxLen,
			MaxCompletionTokens:        maxTok,
			OwnedBy:                    providerName,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	for _, m := range rawEnvelope.Data.Models {
		if m.Disabled || strings.EqualFold(m.Status, "offline") || strings.EqualFold(m.Status, "disabled") {
			continue
		}
		ctxLen := m.RecommendedContext
		if ctxLen <= 0 {
			ctxLen = m.MaxInputTokens
		}
		maxTok := m.MaxOutputTokens
		if maxTok <= 0 {
			maxTok = m.MaxTokens
		}
		out = append(out, pluginapi.ModelInfo{
			ID:                         m.ID,
			Name:                       m.Name,
			ContextLength:              ctxLen,
			MaxCompletionTokens:        maxTok,
			OwnedBy:                    providerName,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	return out, nil
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
