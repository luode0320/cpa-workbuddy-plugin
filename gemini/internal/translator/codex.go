package translator

import "context"

func CodexToGeminiCLI(model string, body []byte, stream bool) []byte {
	return toGeminiCLI(model, "codex", body, stream)
}

func GeminiCLIToCodex(ctx context.Context, model string, originalRequest, translatedRequest, body []byte, stream bool) []byte {
	return fromGeminiCLI(ctx, "codex", model, originalRequest, translatedRequest, body, stream)
}
