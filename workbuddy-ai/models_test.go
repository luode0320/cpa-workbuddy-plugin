package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestModelsContainGPTAndMerge(t *testing.T) {
	models := wbModels()
	foundGPT4o := false
	foundO3 := false
	for _, m := range models {
		if m.ID == "gpt-4o" {
			foundGPT4o = true
		}
		if m.ID == "o3" {
			foundO3 = true
		}
	}
	if !foundGPT4o {
		t.Fatalf("expected gpt-4o in wbModels, but not found")
	}
	if !foundO3 {
		t.Fatalf("expected o3 in wbModels, but not found")
	}

	// Test resolveModels dynamic merge
	dyn := []pluginapi.ModelInfo{
		{ID: "custom-dynamic-model", Name: "Custom Dynamic"},
	}
	resolved := resolveModels(dyn, nil, models)
	if len(resolved) <= len(models) {
		t.Fatalf("expected merged models to have dynamic model plus fallback models")
	}
	if resolved[0].ID != "custom-dynamic-model" {
		t.Fatalf("expected dynamic model to be prioritized at front, got %s", resolved[0].ID)
	}
}

func TestExtractAuthInfo(t *testing.T) {
	flatJSON := []byte(`{"accessToken":"tok_123","enterpriseId":"ent_456"}`)
	tok, ent, ok := extractAuthInfo(flatJSON)
	if !ok || tok != "tok_123" || ent != "ent_456" {
		t.Fatalf("flat extraction failed: tok=%s ent=%s ok=%v", tok, ent, ok)
	}

	nestedJSON := []byte(`{"auth":{"accessToken":"tok_nested"},"account":{"enterpriseId":"ent_nested"}}`)
	tok2, ent2, ok2 := extractAuthInfo(nestedJSON)
	if !ok2 || tok2 != "tok_nested" || ent2 != "ent_nested" {
		t.Fatalf("nested extraction failed: tok=%s ent=%s ok=%v", tok2, ent2, ok2)
	}
}

