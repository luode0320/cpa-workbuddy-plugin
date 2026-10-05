package translator

import (
	"bytes"
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/builtin"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/compat"
)

const FormatGeminiCLI = "gemini-cli"

type requestTranslator func(model string, body []byte, stream bool) []byte
type responseTranslator func(ctx context.Context, model string, originalRequest, translatedRequest, body []byte, stream bool) []byte

type Translator struct {
	registry *sdktranslator.Registry
}

func NewTranslator() *Translator {
	return &Translator{registry: builtin.Registry()}
}

func (t *Translator) TranslateRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	key := req.FromFormat + "\x00" + req.ToFormat
	fn := requestTranslators[key]
	if fn == nil {
		return pluginapi.PayloadResponse{}, fmt.Errorf("unsupported request translation %s -> %s", req.FromFormat, req.ToFormat)
	}
	return pluginapi.PayloadResponse{Body: fn(req.Model, req.Body, req.Stream)}, nil
}

func (t *Translator) TranslateResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	key := req.FromFormat + "\x00" + req.ToFormat
	fn := responseTranslators[key]
	if fn == nil {
		return pluginapi.PayloadResponse{}, fmt.Errorf("unsupported response translation %s -> %s", req.FromFormat, req.ToFormat)
	}
	return pluginapi.PayloadResponse{Body: fn(ctx, req.Model, req.OriginalRequest, req.TranslatedRequest, req.Body, req.Stream)}, nil
}

func translateToGemini(model string, from string, body []byte, stream bool) []byte {
	return builtin.Registry().TranslateRequest(sdktranslator.FromString(from), sdktranslator.FormatGemini, model, body, stream)
}

func translateFromGemini(ctx context.Context, to string, model string, originalRequest, translatedRequest, body []byte, stream bool) []byte {
	if stream {
		chunks := builtin.Registry().TranslateStream(ctx, sdktranslator.FormatGemini, sdktranslator.FromString(to), model, originalRequest, translatedRequest, body, nil)
		if len(chunks) == 0 {
			return body
		}
		return bytes.Join(chunks, nil)
	}
	return builtin.Registry().TranslateNonStream(ctx, sdktranslator.FormatGemini, sdktranslator.FromString(to), model, originalRequest, translatedRequest, body, nil)
}

func toGeminiCLI(model string, from string, body []byte, stream bool) []byte {
	if from == FormatGeminiCLI {
		return append([]byte(nil), body...)
	}
	if from == sdktranslator.FormatGemini.String() {
		return compat.WrapRequest(model, body)
	}
	return compat.WrapRequest(model, translateToGemini(model, from, body, stream))
}

func fromGeminiCLI(ctx context.Context, to string, model string, originalRequest, translatedRequest, body []byte, stream bool) []byte {
	unwrapped := compat.UnwrapResponse(body)
	if to == FormatGeminiCLI {
		return append([]byte(nil), body...)
	}
	if to == sdktranslator.FormatGemini.String() {
		return unwrapped
	}
	return translateFromGemini(ctx, to, model, originalRequest, translatedRequest, unwrapped, stream)
}

var requestTranslators = map[string]requestTranslator{
	"openai\x00gemini-cli":          OpenAIToGeminiCLI,
	"openai-response\x00gemini-cli": ResponsesToGeminiCLI,
	"claude\x00gemini-cli":          ClaudeToGeminiCLI,
	"gemini\x00gemini-cli":          GeminiToGeminiCLI,
	"codex\x00gemini-cli":           CodexToGeminiCLI,
}

var responseTranslators = map[string]responseTranslator{
	"gemini-cli\x00openai":          GeminiCLIToOpenAI,
	"gemini-cli\x00openai-response": GeminiCLIToResponses,
	"gemini-cli\x00claude":          GeminiCLIToClaude,
	"gemini-cli\x00gemini":          GeminiCLIToGemini,
	"gemini-cli\x00codex":           GeminiCLIToCodex,
}
