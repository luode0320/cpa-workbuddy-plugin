package models

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestStaticModelsIncludeExpectedGeminiCLIMetadata(t *testing.T) {
	resp, errModels := NewProvider().StaticModels(context.Background(), pluginapi.StaticModelRequest{})
	if errModels != nil {
		t.Fatalf("StaticModels returned error: %v", errModels)
	}
	if len(resp.Models) == 0 {
		t.Fatal("StaticModels returned no models")
	}
	byID := make(map[string]pluginapi.ModelInfo, len(resp.Models))
	for _, model := range resp.Models {
		byID[model.ID] = model
	}
	model, ok := byID["gemini-2.5-pro"]
	if !ok {
		t.Fatalf("gemini-2.5-pro missing from models: %#v", byID)
	}
	if model.Thinking == nil {
		t.Fatal("gemini-2.5-pro thinking support is nil")
	}
	if model.Thinking.Min != 128 {
		t.Fatalf("thinking min = %d, want 128", model.Thinking.Min)
	}
	if model.Thinking.Max != 32768 {
		t.Fatalf("thinking max = %d, want 32768", model.Thinking.Max)
	}
	if len(model.Thinking.Levels) != 0 {
		t.Fatalf("thinking levels = %#v, want empty", model.Thinking.Levels)
	}
	model, ok = byID["gemini-2.5-flash-lite"]
	if !ok {
		t.Fatalf("gemini-2.5-flash-lite missing from models: %#v", byID)
	}
	if model.Thinking == nil {
		t.Fatal("gemini-2.5-flash-lite thinking support is nil")
	}
	if model.Thinking.Max != 24576 {
		t.Fatalf("flash-lite thinking max = %d, want 24576", model.Thinking.Max)
	}
	if !model.Thinking.ZeroAllowed {
		t.Fatal("flash-lite thinking zero_allowed = false, want true")
	}
	if len(model.Thinking.Levels) != 0 {
		t.Fatalf("flash-lite thinking levels = %#v, want empty", model.Thinking.Levels)
	}
}
