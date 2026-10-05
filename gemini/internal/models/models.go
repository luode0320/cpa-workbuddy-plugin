package models

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type Provider struct{}

func NewProvider() *Provider { return &Provider{} }

func (p *Provider) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Models: staticModels()}, nil
}

func (p *Provider) ModelsForAuth(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Models: staticModels()}, nil
}

func staticModels() []pluginapi.ModelInfo {
	definitions := []struct {
		id          string
		displayName string
		input       int64
		output      int64
		thinking    *pluginapi.ThinkingSupport
	}{
		{"gemini-2.5-pro", "Gemini 2.5 Pro", 1048576, 65536, gemini25ProThinking()},
		{"gemini-2.5-flash", "Gemini 2.5 Flash", 1048576, 65536, gemini25FlashThinking()},
		{"gemini-2.5-flash-lite", "Gemini 2.5 Flash Lite", 1048576, 65536, gemini25FlashThinking()},
		{"gemini-3-pro-preview", "Gemini 3 Pro Preview", 1048576, 65536, defaultThinking()},
		{"gemini-3.1-pro-preview", "Gemini 3.1 Pro Preview", 1048576, 65536, defaultThinking()},
		{"gemini-3-flash-preview", "Gemini 3 Flash Preview", 1048576, 65536, defaultThinking()},
		{"gemini-3.1-flash-lite-preview", "Gemini 3.1 Flash Lite Preview", 1048576, 65536, defaultThinking()},
		{"gemini-3.5-flash", "Gemini 3.5 Flash", 1048576, 65536, defaultThinking()},
	}
	models := make([]pluginapi.ModelInfo, 0, len(definitions))
	for _, def := range definitions {
		models = append(models, pluginapi.ModelInfo{
			ID:                         def.id,
			Object:                     "model",
			OwnedBy:                    "google",
			Type:                       "chat",
			DisplayName:                def.displayName,
			Name:                       def.id,
			Description:                def.displayName + " via Gemini CLI",
			InputTokenLimit:            def.input,
			OutputTokenLimit:           def.output,
			ContextLength:              def.input,
			MaxCompletionTokens:        def.output,
			SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent", "countTokens"},
			SupportedInputModalities:   []string{"text", "image", "audio", "video"},
			SupportedOutputModalities:  []string{"text"},
			SupportedParameters:        []string{"temperature", "top_p", "top_k", "max_output_tokens", "stop", "tools", "thinking"},
			Thinking:                   cloneThinking(def.thinking),
		})
	}
	return models
}

func defaultThinking() *pluginapi.ThinkingSupport {
	return &pluginapi.ThinkingSupport{
		Min:            0,
		Max:            65536,
		ZeroAllowed:    true,
		DynamicAllowed: true,
		Levels:         []string{"none", "auto", "low", "medium", "high", "xhigh"},
	}
}

func gemini25ProThinking() *pluginapi.ThinkingSupport {
	return &pluginapi.ThinkingSupport{
		Min:            128,
		Max:            32768,
		DynamicAllowed: true,
	}
}

func gemini25FlashThinking() *pluginapi.ThinkingSupport {
	return &pluginapi.ThinkingSupport{
		Max:            24576,
		ZeroAllowed:    true,
		DynamicAllowed: true,
	}
}

func cloneThinking(in *pluginapi.ThinkingSupport) *pluginapi.ThinkingSupport {
	if in == nil {
		return nil
	}
	return &pluginapi.ThinkingSupport{
		Min:            in.Min,
		Max:            in.Max,
		ZeroAllowed:    in.ZeroAllowed,
		DynamicAllowed: in.DynamicAllowed,
		Levels:         append([]string(nil), in.Levels...),
	}
}
