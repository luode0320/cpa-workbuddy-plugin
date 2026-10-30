package main

import (
	"encoding/json"
	"testing"
)

// TestAdaptiveMaxTokens 验证统一上限下的自适应计算。
//
// 2026-10-30 起不再按模型区分：推理模式统一 128k，非推理模式统一 64k，
// 所有模型（含未收录与别称）行为一致。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：改为全局统一上限。
func TestAdaptiveMaxTokens(t *testing.T) {
	cases := []struct {
		name        string
		model       string
		requested   int64
		isReasoning bool
		wantTokens  int64
	}{
		{
			name:        "非推理模式 + 未传 -> 补足为 64000",
			model:       "Qwen3.8-Flash",
			requested:   0,
			isReasoning: false,
			wantTokens:  64000,
		},
		{
			name:        "推理模式 + 传 8192 -> 放宽至 128000",
			model:       "Qwen3.8-Flash",
			requested:   8192,
			isReasoning: true,
			wantTokens:  128000,
		},
		{
			name:        "推理模式 + 未传 -> 补足为 128000",
			model:       "GLM-5.3",
			requested:   0,
			isReasoning: true,
			wantTokens:  128000,
		},
		{
			name:        "推理模式 + 超限 200000 -> 截断至 128000",
			model:       "qfmodel",
			requested:   200000,
			isReasoning: true,
			wantTokens:  128000,
		},
		{
			name:        "非推理模式 + 超限 200000 -> 截断至 64000",
			model:       "qfmodel",
			requested:   200000,
			isReasoning: false,
			wantTokens:  64000,
		},
		{
			name:        "未收录模型 + 推理模式 + 未传 -> 同样 128000",
			model:       "some-unlisted-model",
			requested:   0,
			isReasoning: true,
			wantTokens:  128000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeAdaptiveMaxTokens(tc.model, tc.requested, tc.isReasoning)
			if got != tc.wantTokens {
				t.Fatalf("computeAdaptiveMaxTokens(%s, %d, %v) = %d, want %d",
					tc.model, tc.requested, tc.isReasoning, got, tc.wantTokens)
			}
		})
	}
}

// TestWbModels_OfficialSpecs 验证模型注册时对外声明的输出上限为统一值。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：对齐统一上限 128000。
func TestWbModels_OfficialSpecs(t *testing.T) {
	models := exposeModelIDs(wbModels())
	for _, m := range models {
		if m.MaxCompletionTokens != 128000 {
			t.Fatalf("%s MaxCompletionTokens = %d, want 128000", m.ID, m.MaxCompletionTokens)
		}
	}
}

// TestBuildQoderBody_AdaptiveMaxTokens 验证端到端 body 构建后的 max_tokens。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：对齐统一上限（推理 128k / 非推理 64k）。
func TestBuildQoderBody_AdaptiveMaxTokens(t *testing.T) {
	// 1. 非推理模式未传 max_tokens -> 补足为 64000
	req1 := &openAIRequest{
		Model:    "Qwen3.8-Flash",
		Messages: []openAIMessage{{Role: "user", Content: "hi"}},
	}
	body1, err := buildQoderBody(req1, "qfmodel", "personal_standard")
	if err != nil {
		t.Fatalf("buildQoderBody: %v", err)
	}
	var parsed1 map[string]any
	if err := json.Unmarshal(body1, &parsed1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if mt := jsonI64(parsed1["parameters"].(map[string]any), "max_tokens"); mt != 64000 {
		t.Fatalf("parameters.max_tokens = %d, want 64000", mt)
	}

	// 2. 思考模式下传入保守小值 8192 -> 放宽至 128000
	req2 := &openAIRequest{
		Model:           "Qwen3.8-Flash",
		Messages:        []openAIMessage{{Role: "user", Content: "hi"}},
		MaxTokens:       8192,
		ReasoningEffort: "high",
	}
	body2, err := buildQoderBody(req2, "qfmodel", "personal_standard")
	if err != nil {
		t.Fatalf("buildQoderBody: %v", err)
	}
	var parsed2 map[string]any
	if err := json.Unmarshal(body2, &parsed2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if mt := jsonI64(parsed2["parameters"].(map[string]any), "max_tokens"); mt != 128000 {
		t.Fatalf("parameters.max_tokens = %d, want 128000", mt)
	}
}
