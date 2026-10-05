package translator

import "context"

func ResponsesToGeminiCLI(model string, body []byte, stream bool) []byte {
	return toGeminiCLI(model, "openai-response", body, stream)
}

func GeminiCLIToResponses(ctx context.Context, model string, originalRequest, translatedRequest, body []byte, stream bool) []byte {
	return fromGeminiCLI(ctx, "openai-response", model, originalRequest, translatedRequest, body, stream)
}
