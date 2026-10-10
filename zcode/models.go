package main

import (
	"context"
	"encoding/json"
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
	modelHTTPClient = &http.Client{Timeout: 15 * time.Second}
	modelCache      struct {
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

	reqURL := cfg.BaseURL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return append([]string(nil), defaultModels...), err
	}

	fp := mimic.New(cfg.Mimic, keyID(apiKey))
	fp.ApplyRequest(req.Header, apiKey, mimic.UUID4())

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
	accounts, err := listAllAuthFiles()
	if err == nil {
		for _, acc := range accounts {
			if acc.AuthID == authID || authFileNameFor(acc.APIKey) == authID+".json" {
				if len(acc.Models) > 0 {
					return acc.Models
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
		result = append(result, pluginapi.ModelInfo{
			ID:                         id,
			Object:                     "model",
			OwnedBy:                    zcodeProviderID,
			DisplayName:                id,
			Name:                       mappedModel(id, cfg),
			SupportedGenerationMethods: []string{"chat"},
			ContextLength:              200000,
			MaxCompletionTokens:        128000,
			UserDefined:                true,
		})
	}
	return result
}

func mappedModel(model string, cfg Config) string {
	if upstream, exists := cfg.ModelMap[model]; exists && strings.TrimSpace(upstream) != "" {
		return strings.TrimSpace(upstream)
	}
	return model
}

func keyID(apiKey string) string {
	if idx := strings.Index(apiKey, "."); idx > 0 {
		return apiKey[:idx]
	}
	return apiKey
}
