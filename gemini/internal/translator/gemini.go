package translator

import "context"

func GeminiToGeminiCLI(model string, body []byte, stream bool) []byte {
	return toGeminiCLI(model, "gemini", body, stream)
}

func GeminiCLIToGemini(ctx context.Context, model string, originalRequest, translatedRequest, body []byte, stream bool) []byte {
	return fromGeminiCLI(ctx, "gemini", model, originalRequest, translatedRequest, body, stream)
}
