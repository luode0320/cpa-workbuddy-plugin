package thinking

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type Applier struct{}

func NewApplier() *Applier { return &Applier{} }

func (a *Applier) Identifier() string { return "gemini-cli" }

func (a *Applier) ApplyThinking(_ context.Context, req pluginapi.ThinkingApplyRequest) (pluginapi.PayloadResponse, error) {
	body := append([]byte(nil), req.Body...)
	config := normalizeThinkingConfig(req.Config)
	path := "request.generationConfig.thinkingConfig"

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	if shouldUseBudgetFormat(req.Model) {
		if config.Mode == "level" {
			if converted, ok := levelConfigToBudget(config); ok {
				config = converted
			}
		}
		switch config.Mode {
		case "budget", "auto", "none":
			config.Budget = clampThinkingBudget(config.Budget, req.Model.Thinking)
		}
	}

	switch config.Mode {
	case "none":
		if shouldUseBudgetFormat(req.Model) {
			body = applyBudgetConfig(body, path, config)
			return pluginapi.PayloadResponse{Body: body}, nil
		}
		updated, errDelete := sjson.DeleteBytes(body, path)
		if errDelete == nil {
			body = updated
		}
	case "budget":
		body = applyBudgetConfig(body, path, config)
	case "level":
		body = applyLevelConfig(body, path, config)
	case "auto":
		config.Budget = -1
		body = applyBudgetConfig(body, path, config)
	}
	return pluginapi.PayloadResponse{Body: body}, nil
}

func normalizeThinkingConfig(config pluginapi.ThinkingConfig) pluginapi.ThinkingConfig {
	config.Mode = strings.ToLower(strings.TrimSpace(config.Mode))
	config.Level = strings.ToLower(strings.TrimSpace(config.Level))
	switch config.Mode {
	case "none", "budget", "level", "auto":
	default:
		config.Mode = "auto"
	}
	if config.Mode == "level" && config.Level == "" {
		config.Mode = "auto"
	}
	if config.Mode == "budget" && config.Budget <= 0 {
		config.Mode = "none"
	}
	return config
}

func shouldUseBudgetFormat(model pluginapi.ModelInfo) bool {
	if model.UserDefined || model.Thinking == nil {
		return false
	}
	return len(model.Thinking.Levels) == 0
}

func levelConfigToBudget(config pluginapi.ThinkingConfig) (pluginapi.ThinkingConfig, bool) {
	switch strings.ToLower(strings.TrimSpace(config.Level)) {
	case "none":
		config.Mode = "none"
		config.Budget = 0
	case "auto":
		config.Mode = "auto"
		config.Budget = -1
	case "minimal":
		config.Mode = "budget"
		config.Budget = 512
	case "low":
		config.Mode = "budget"
		config.Budget = 1024
	case "medium":
		config.Mode = "budget"
		config.Budget = 8192
	case "high":
		config.Mode = "budget"
		config.Budget = 24576
	case "xhigh":
		config.Mode = "budget"
		config.Budget = 32768
	case "max":
		config.Mode = "budget"
		config.Budget = 128000
	default:
		return config, false
	}
	config.Level = ""
	return config, true
}

func clampThinkingBudget(value int, support *pluginapi.ThinkingSupport) int {
	if support == nil || value == -1 {
		return value
	}
	if support.Min == 0 && support.Max == 0 {
		return value
	}
	if value == 0 {
		if support.ZeroAllowed {
			return 0
		}
		return support.Min
	}
	if support.Min > 0 && value < support.Min {
		return support.Min
	}
	if support.Max > 0 && value > support.Max {
		return support.Max
	}
	return value
}

func applyLevelConfig(body []byte, path string, config pluginapi.ThinkingConfig) []byte {
	includeThoughts := includeThoughtsFromBody(body, path, true)
	body = deleteThinkingField(body, path+".thinkingBudget")
	body = deleteThinkingField(body, path+".thinking_budget")
	body = deleteThinkingField(body, path+".thinking_level")
	body = deleteThinkingField(body, path+".include_thoughts")
	body = setThinkingString(body, path+".thinkingLevel", config.Level)
	body = setThinkingBool(body, path+".includeThoughts", includeThoughts)
	return body
}

func applyBudgetConfig(body []byte, path string, config pluginapi.ThinkingConfig) []byte {
	budget := config.Budget
	if config.Mode == "auto" && budget == 0 {
		budget = -1
	}
	defaultIncludeThoughts := budget > 0 || config.Mode == "auto"
	includeThoughts := includeThoughtsFromBody(body, path, defaultIncludeThoughts)

	body = deleteThinkingField(body, path+".thinkingLevel")
	body = deleteThinkingField(body, path+".thinking_level")
	body = deleteThinkingField(body, path+".thinking_budget")
	body = deleteThinkingField(body, path+".include_thoughts")

	body = setThinkingInt(body, path+".thinkingBudget", budget)
	if config.Mode == "none" {
		body = setThinkingBool(body, path+".includeThoughts", false)
		return body
	}

	body = setThinkingBool(body, path+".includeThoughts", includeThoughts)
	return body
}

func deleteThinkingField(body []byte, path string) []byte {
	updated, errDelete := sjson.DeleteBytes(body, path)
	if errDelete != nil {
		return body
	}
	return updated
}

func includeThoughtsFromBody(body []byte, path string, fallback bool) bool {
	if includeThoughts := gjson.GetBytes(body, path+".includeThoughts"); includeThoughts.Exists() {
		return includeThoughts.Bool()
	}
	if includeThoughts := gjson.GetBytes(body, path+".include_thoughts"); includeThoughts.Exists() {
		return includeThoughts.Bool()
	}
	return fallback
}

func setThinkingBool(body []byte, path string, value bool) []byte {
	updated, errSet := sjson.SetBytes(body, path, value)
	if errSet != nil {
		return body
	}
	return updated
}

func setThinkingInt(body []byte, path string, value int) []byte {
	updated, errSet := sjson.SetBytes(body, path, value)
	if errSet != nil {
		return body
	}
	return updated
}

func setThinkingString(body []byte, path string, value string) []byte {
	updated, errSet := sjson.SetBytes(body, path, value)
	if errSet != nil {
		return body
	}
	return updated
}
