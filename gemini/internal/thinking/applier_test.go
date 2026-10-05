package thinking

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestApplyThinkingNoneDeletesThinkingConfig(t *testing.T) {
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":1024},"temperature":0.3}}}`)

	resp, errApply := NewApplier().ApplyThinking(context.Background(), pluginapi.ThinkingApplyRequest{
		Config: pluginapi.ThinkingConfig{Mode: "none"},
		Body:   body,
	})
	if errApply != nil {
		t.Fatalf("ApplyThinking returned error: %v", errApply)
	}
	if gjson.GetBytes(resp.Body, "request.generationConfig.thinkingConfig").Exists() {
		t.Fatalf("thinkingConfig was not deleted: %s", resp.Body)
	}
	if got := gjson.GetBytes(resp.Body, "request.generationConfig.temperature").Float(); got != 0.3 {
		t.Fatalf("temperature = %v, want 0.3", got)
	}
}

func TestApplyThinkingBudgetSetsBudget(t *testing.T) {
	resp, errApply := NewApplier().ApplyThinking(context.Background(), pluginapi.ThinkingApplyRequest{
		Config: pluginapi.ThinkingConfig{Mode: "budget", Budget: 4096},
		Body:   []byte(`{"request":{"generationConfig":{}}}`),
	})
	if errApply != nil {
		t.Fatalf("ApplyThinking returned error: %v", errApply)
	}
	if !gjson.GetBytes(resp.Body, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts was not enabled: %s", resp.Body)
	}
	if got := gjson.GetBytes(resp.Body, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 4096 {
		t.Fatalf("thinkingBudget = %d, want 4096", got)
	}
}

func TestApplyThinkingLevelSetsLevel(t *testing.T) {
	resp, errApply := NewApplier().ApplyThinking(context.Background(), pluginapi.ThinkingApplyRequest{
		Config: pluginapi.ThinkingConfig{Mode: "level", Level: "HIGH"},
		Body:   []byte(`{"request":{"generationConfig":{}}}`),
	})
	if errApply != nil {
		t.Fatalf("ApplyThinking returned error: %v", errApply)
	}
	if got := gjson.GetBytes(resp.Body, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
		t.Fatalf("thinkingLevel = %q, want high", got)
	}
}

func TestApplyThinkingLevelUsesBudgetForGemini25(t *testing.T) {
	resp, errApply := NewApplier().ApplyThinking(context.Background(), pluginapi.ThinkingApplyRequest{
		Model: pluginapi.ModelInfo{
			ID: "gemini-2.5-pro",
			Thinking: &pluginapi.ThinkingSupport{
				Min:            128,
				Max:            32768,
				DynamicAllowed: true,
			},
		},
		Config: pluginapi.ThinkingConfig{Mode: "level", Level: "HIGH"},
		Body:   []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low","thinking_budget":1024}}}}`),
	})
	if errApply != nil {
		t.Fatalf("ApplyThinking returned error: %v", errApply)
	}
	if gjson.GetBytes(resp.Body, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("thinkingLevel was not removed: %s", resp.Body)
	}
	if gjson.GetBytes(resp.Body, "request.generationConfig.thinkingConfig.thinking_level").Exists() {
		t.Fatalf("thinking_level was not removed: %s", resp.Body)
	}
	if got := gjson.GetBytes(resp.Body, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 24576 {
		t.Fatalf("thinkingBudget = %d, want 24576", got)
	}
}

func TestApplyThinkingLevelClampsBudgetForGemini25FlashLite(t *testing.T) {
	resp, errApply := NewApplier().ApplyThinking(context.Background(), pluginapi.ThinkingApplyRequest{
		Model: pluginapi.ModelInfo{
			ID: "gemini-2.5-flash-lite",
			Thinking: &pluginapi.ThinkingSupport{
				Max:            24576,
				ZeroAllowed:    true,
				DynamicAllowed: true,
			},
		},
		Config: pluginapi.ThinkingConfig{Mode: "level", Level: "xhigh"},
		Body:   []byte(`{"request":{"generationConfig":{}}}`),
	})
	if errApply != nil {
		t.Fatalf("ApplyThinking returned error: %v", errApply)
	}
	if got := gjson.GetBytes(resp.Body, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 24576 {
		t.Fatalf("thinkingBudget = %d, want 24576", got)
	}
	if gjson.GetBytes(resp.Body, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("thinkingLevel was not removed: %s", resp.Body)
	}
}
