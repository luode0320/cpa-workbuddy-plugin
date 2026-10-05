package translator

import "context"

func OpenAIToGeminiCLI(model string, body []byte, stream bool) []byte {
	return toGeminiCLI(model, "openai", body, stream)
}

func GeminiCLIToOpenAI(ctx context.Context, model string, originalRequest, translatedRequest, body []byte, stream bool) []byte {
	return fromGeminiCLI(ctx, "openai", model, originalRequest, translatedRequest, body, stream)
}
