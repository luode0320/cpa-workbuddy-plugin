package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestWBModelsReturnsNil(t *testing.T) {
	models := wbModels()
	if models != nil {
		t.Fatalf("expected wbModels to return nil (no hardcoded models), got len=%d", len(models))
	}
}

func TestResolveModelsPriority(t *testing.T) {
	// 1. Dynamic takes precedence over configured and fallback
	dyn := []pluginapi.ModelInfo{{ID: "dyn-gpt-4o", Name: "Dynamic GPT-4o"}}
	cfg := []pluginapi.ModelInfo{{ID: "cfg-gpt-4o", Name: "Configured GPT-4o"}}
	fb := []pluginapi.ModelInfo{{ID: "fb-model", Name: "Fallback Model"}}

	res1 := resolveModels(dyn, cfg, fb)
	if len(res1) != 1 || res1[0].ID != "dyn-gpt-4o" {
		t.Fatalf("expected dynamic model to win, got %+v", res1)
	}

	// 2. Configured takes precedence when dynamic is empty
	res2 := resolveModels(nil, cfg, fb)
	if len(res2) != 1 || res2[0].ID != "cfg-gpt-4o" {
		t.Fatalf("expected configured model to win when dynamic empty, got %+v", res2)
	}

	// 3. Fallback when both empty
	res3 := resolveModels(nil, nil, fb)
	if len(res3) != 1 || res3[0].ID != "fb-model" {
		t.Fatalf("expected fallback model when dynamic and configured empty, got %+v", res3)
	}

	// 4. Fully empty when fallback is nil (wbModels() is nil)
	res4 := resolveModels(nil, nil, wbModels())
	if len(res4) != 0 {
		t.Fatalf("expected 0 models when no dynamic, no configured, and wbModels is nil, got len=%d", len(res4))
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

func TestParseModelsAPIResponse(t *testing.T) {
	// Case 1: Standard agents with cli filter
	respWithAgents := []byte(`{
		"code": 0,
		"data": {
			"agents": [{"name": "cli", "models": ["gpt-5", "o3"]}],
			"models": [
				{"id": "gpt-5", "name": "GPT-5", "maxInputTokens": 128000, "maxOutputTokens": 4096},
				{"id": "o3", "name": "o3", "contextWindow": 200000, "maxTokens": 100000},
				{"id": "web-only", "name": "Web Only"}
			]
		}
	}`)
	models1, err := parseModelsAPIResponse(respWithAgents)
	if err != nil {
		t.Fatalf("parseModelsAPIResponse failed: %v", err)
	}
	if len(models1) != 2 {
		t.Fatalf("expected 2 models matching cli agent, got %d", len(models1))
	}
	if models1[0].ID != "gpt-5" || models1[1].ID != "o3" {
		t.Fatalf("unexpected model IDs: %+v", models1)
	}
	if models1[0].ContextLength != 128000 || models1[0].MaxCompletionTokens != 4096 {
		t.Fatalf("unexpected token limits for gpt-5: %+v", models1[0])
	}
	if models1[1].ContextLength != 200000 || models1[1].MaxCompletionTokens != 100000 {
		t.Fatalf("unexpected token limits for o3: %+v", models1[1])
	}

	// Case 2: Enterprise models list without agents
	respWithoutAgents := []byte(`{
		"code": 0,
		"data": {
			"models": [
				{"id": "gemini-2.5-pro", "name": "Gemini 2.5 Pro", "max_input_tokens": 1000000, "max_output_tokens": 8192},
				{"id": "disabled-model", "disabled": true}
			]
		}
	}`)
	models2, err := parseModelsAPIResponse(respWithoutAgents)
	if err != nil {
		t.Fatalf("parseModelsAPIResponse failed: %v", err)
	}
	if len(models2) != 1 || models2[0].ID != "gemini-2.5-pro" {
		t.Fatalf("expected 1 active model, got %+v", models2)
	}
	if models2[0].ContextLength != 1000000 || models2[0].MaxCompletionTokens != 8192 {
		t.Fatalf("unexpected token limits for gemini: %+v", models2[0])
	}
}

func TestResolveUpstreamModelAuto(t *testing.T) {
	resolved := resolveUpstreamModel("auto", nil)
	if resolved != "default-model" {
		t.Fatalf("expected auto to resolve to default-model, got %s", resolved)
	}
	resolvedUpper := resolveUpstreamModel("Auto", nil)
	if resolvedUpper != "default-model" {
		t.Fatalf("expected Auto to resolve to default-model, got %s", resolvedUpper)
	}
	resolvedNormal := resolveUpstreamModel("gpt-5.4", nil)
	if resolvedNormal != "gpt-5.4" {
		t.Fatalf("expected gpt-5.4 to stay unchanged, got %s", resolvedNormal)
	}
}

func TestParseModelsAPIResponseV3Config(t *testing.T) {
	v3JSON := []byte(`{
		"code": 0,
		"data": {
			"agents": [{"name": "cli", "models": ["default-model", "gpt-6-astra", "deepseek-v4.1-flash"]}],
			"models": [
				{"id": "default-model", "name": "Auto", "maxInputTokens": 176000, "maxOutputTokens": 24000},
				{"id": "gpt-6-astra", "name": "GPT-6-Astra", "maxInputTokens": 1000000, "maxOutputTokens": 128000},
				{"id": "deepseek-v4.1-flash", "name": "Deepseek-V4.1-Flash", "maxInputTokens": 1000000, "maxOutputTokens": 128000},
				{"id": "ignored-web-model", "name": "Ignored"}
			]
		}
	}`)
	models, err := parseModelsAPIResponse(v3JSON)
	if err != nil {
		t.Fatalf("parseModelsAPIResponse failed: %v", err)
	}
	// default-model + auto alias + gpt-6-astra + deepseek-v4.1-flash = 4
	if len(models) != 4 {
		t.Fatalf("expected 4 models (including auto alias), got %d: %+v", len(models), models)
	}
	foundAuto := false
	for _, m := range models {
		if m.ID == "auto" {
			foundAuto = true
			if m.ContextLength != 176000 || m.MaxCompletionTokens != 24000 {
				t.Fatalf("auto alias did not inherit limits from default-model: %+v", m)
			}
		}
	}
	if !foundAuto {
		t.Fatal("expected auto alias to be generated")
	}
}
