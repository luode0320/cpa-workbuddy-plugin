package translator

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestTranslateGeminiRequestToGeminiCLIEnvelope(t *testing.T) {
	resp, errTranslate := NewTranslator().TranslateRequest(context.Background(), pluginapi.RequestTransformRequest{
		FromFormat: "gemini",
		ToFormat:   FormatGeminiCLI,
		Model:      "gemini-2.5-pro",
		Body:       []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	})
	if errTranslate != nil {
		t.Fatalf("TranslateRequest returned error: %v", errTranslate)
	}
	if got := gjson.GetBytes(resp.Body, "model").String(); got != "gemini-2.5-pro" {
		t.Fatalf("model = %q, want gemini-2.5-pro", got)
	}
	if !gjson.GetBytes(resp.Body, "request.contents.0.parts.0.text").Exists() {
		t.Fatalf("translated request does not contain Gemini contents: %s", resp.Body)
	}
}

func TestTranslateOpenAIRequestToGeminiCLIEnvelope(t *testing.T) {
	resp, errTranslate := NewTranslator().TranslateRequest(context.Background(), pluginapi.RequestTransformRequest{
		FromFormat: "openai",
		ToFormat:   FormatGeminiCLI,
		Model:      "gemini-2.5-pro",
		Body:       []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	})
	if errTranslate != nil {
		t.Fatalf("TranslateRequest returned error: %v", errTranslate)
	}
	if got := gjson.GetBytes(resp.Body, "model").String(); got != "gemini-2.5-pro" {
		t.Fatalf("model = %q, want gemini-2.5-pro", got)
	}
	if !gjson.GetBytes(resp.Body, "request.contents").Exists() {
		t.Fatalf("translated request does not contain request.contents: %s", resp.Body)
	}
}

func TestTranslateGeminiCLIResponseToGemini(t *testing.T) {
	resp, errTranslate := NewTranslator().TranslateResponse(context.Background(), pluginapi.ResponseTransformRequest{
		FromFormat: FormatGeminiCLI,
		ToFormat:   "gemini",
		Model:      "gemini-2.5-pro",
		Body:       []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}}`),
	})
	if errTranslate != nil {
		t.Fatalf("TranslateResponse returned error: %v", errTranslate)
	}
	if got := gjson.GetBytes(resp.Body, "candidates.0.content.parts.0.text").String(); got != "hello" {
		t.Fatalf("response text = %q, want hello", got)
	}
}

func TestTranslateGeminiCLIStreamResponseToGeminiReturnsPayloadOnly(t *testing.T) {
	resp, errTranslate := NewTranslator().TranslateResponse(context.Background(), pluginapi.ResponseTransformRequest{
		FromFormat: FormatGeminiCLI,
		ToFormat:   "gemini",
		Model:      "gemini-2.5-pro",
		Stream:     true,
		Body:       []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}}`),
	})
	if errTranslate != nil {
		t.Fatalf("TranslateResponse returned error: %v", errTranslate)
	}
	if string(resp.Body) == `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}` {
		t.Fatalf("stream response still contains SSE prefix: %s", resp.Body)
	}
	if got := gjson.GetBytes(resp.Body, "candidates.0.content.parts.0.text").String(); got != "hello" {
		t.Fatalf("response text = %q, want hello", got)
	}
}
