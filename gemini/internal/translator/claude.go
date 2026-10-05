package translator

import "context"

func ClaudeToGeminiCLI(model string, body []byte, stream bool) []byte {
	return toGeminiCLI(model, "claude", body, stream)
}

func GeminiCLIToClaude(ctx context.Context, model string, originalRequest, translatedRequest, body []byte, stream bool) []byte {
	return fromGeminiCLI(ctx, "claude", model, originalRequest, translatedRequest, body, stream)
}
