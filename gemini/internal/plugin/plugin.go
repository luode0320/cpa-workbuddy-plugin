package plugin

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	authpkg "github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/auth"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/executor"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/models"
	thinkingpkg "github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/thinking"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/translator"
)

const Provider = "gemini-cli"
const executorFormat = "gemini"

type GeminiCLIPlugin struct {
	auth       *authpkg.Provider
	models     *models.Provider
	thinking   *thinkingpkg.Applier
	translator *translator.Translator
	executor   *executor.Executor
}

func New() *GeminiCLIPlugin {
	return &GeminiCLIPlugin{
		auth:       authpkg.NewProvider(),
		models:     models.NewProvider(),
		thinking:   thinkingpkg.NewApplier(),
		translator: translator.NewTranslator(),
		executor:   executor.NewExecutor(),
	}
}

func Build(configYAML []byte) pluginapi.Plugin {
	p := New()
	return pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             "Gemini CLI Provider",
			Version:          "0.1.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPIPlugins",
		},
		Capabilities: pluginapi.Capabilities{
			AuthProvider:          p,
			ModelProvider:         p,
			Executor:              p,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{executorFormat},
			ExecutorOutputFormats: []string{executorFormat},
			RequestTranslator:     p,
			ResponseTranslator:    p,
			ThinkingApplier:       p,
			CommandLinePlugin:     p,
		},
	}
}

func (p *GeminiCLIPlugin) Identifier() string { return Provider }

func (p *GeminiCLIPlugin) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return p.auth.ParseAuth(ctx, req)
}

func (p *GeminiCLIPlugin) StartLogin(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return p.auth.StartLogin(ctx, req)
}

func (p *GeminiCLIPlugin) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return p.auth.PollLogin(ctx, req)
}

func (p *GeminiCLIPlugin) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return p.auth.RefreshAuth(ctx, req)
}

func (p *GeminiCLIPlugin) StaticModels(ctx context.Context, req pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return p.models.StaticModels(ctx, req)
}

func (p *GeminiCLIPlugin) ModelsForAuth(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return p.models.ModelsForAuth(ctx, req)
}

func (p *GeminiCLIPlugin) TranslateRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return p.translator.TranslateRequest(ctx, req)
}

func (p *GeminiCLIPlugin) TranslateResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return p.translator.TranslateResponse(ctx, req)
}

func (p *GeminiCLIPlugin) ApplyThinking(ctx context.Context, req pluginapi.ThinkingApplyRequest) (pluginapi.PayloadResponse, error) {
	return p.thinking.ApplyThinking(ctx, req)
}

func (p *GeminiCLIPlugin) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return p.executor.Execute(ctx, req)
}

func (p *GeminiCLIPlugin) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	return p.executor.ExecuteStream(ctx, req)
}

func (p *GeminiCLIPlugin) CountTokens(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return p.executor.CountTokens(ctx, req)
}

func (p *GeminiCLIPlugin) HttpRequest(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	return p.executor.HttpRequest(ctx, req)
}

func (p *GeminiCLIPlugin) RegisterCommandLine(ctx context.Context, req pluginapi.CommandLineRegistrationRequest) (pluginapi.CommandLineRegistrationResponse, error) {
	return p.auth.RegisterCommandLine(ctx, req)
}

func (p *GeminiCLIPlugin) ExecuteCommandLine(ctx context.Context, req pluginapi.CommandLineExecutionRequest) (pluginapi.CommandLineExecutionResponse, error) {
	return p.auth.ExecuteCommandLine(ctx, req)
}

var _ pluginapi.AuthProvider = (*GeminiCLIPlugin)(nil)
var _ pluginapi.ModelProvider = (*GeminiCLIPlugin)(nil)
var _ pluginapi.ProviderExecutor = (*GeminiCLIPlugin)(nil)
var _ pluginapi.RequestTranslator = (*GeminiCLIPlugin)(nil)
var _ pluginapi.ResponseTranslator = (*GeminiCLIPlugin)(nil)
var _ pluginapi.ThinkingApplier = (*GeminiCLIPlugin)(nil)
var _ pluginapi.CommandLinePlugin = (*GeminiCLIPlugin)(nil)
