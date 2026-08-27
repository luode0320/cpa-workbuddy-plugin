// models.go implements the model.static / model.for_auth capabilities. Trae
// Work SOLO accepts arbitrary model names as config_name (unknown names pass
// through), so the default list is a curated set of commonly available
// models; users can override via config_yaml `models:`.
package main

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// defaultTraeModels is the curated default model list (Trae Work SOLO pool).
// Unknown model names still work — the executor passes them through as
// config_name — so this list is informational for the client picker.
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

// handleModelForAuth returns the model list scoped to one auth record. Trae
// accounts share the same SOLO model pool, so this is the global list.
func handleModelForAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	_ = json.Unmarshal(raw, &req)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: loadedModels()})
}
