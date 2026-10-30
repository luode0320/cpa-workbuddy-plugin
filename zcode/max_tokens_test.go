package main

import (
	"encoding/json"
	"testing"
)

// TestComputeAdaptiveMaxTokens_ZCode 验证统一上限下的自适应计算与 32768 最低输出底线。
//
// 2026-10-30 起不再按模型区分：推理模式统一 128k，非推理模式统一 64k。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：改为全局统一上限。
func TestComputeAdaptiveMaxTokens_ZCode(t *testing.T) {
	// 1. 未传 max_tokens (0) 且非推理模式：补足至统一上限 64000
	if got := computeAdaptiveMaxTokens("glm-5.3", 0, false); got != 64000 {
		t.Fatalf("glm-5.3 unconfigured non-reasoning = %d, want 64000", got)
	}

	// 2. 未传 max_tokens (0) 且为推理模式：补足至统一上限 128000
	if got := computeAdaptiveMaxTokens("glm-5.3", 0, true); got != 128000 {
		t.Fatalf("glm-5.3 unconfigured reasoning = %d, want 128000", got)
	}

	// 3. 客户端保守传入 8192 且为推理模式：放宽至 128000
	if got := computeAdaptiveMaxTokens("glm-5.3", 8192, true); got != 128000 {
		t.Fatalf("glm-5.3 8192 reasoning = %d, want 128000", got)
	}

	// 4. 推理模式超出上限 (200000)：截断回 128000
	if got := computeAdaptiveMaxTokens("glm-5.3", 200000, true); got != 128000 {
		t.Fatalf("glm-5.3 200000 reasoning = %d, want 128000", got)
	}

	// 5. 非推理模式超出上限 (200000)：截断回 64000
	if got := computeAdaptiveMaxTokens("glm-5.3", 200000, false); got != 64000 {
		t.Fatalf("glm-5.3 200000 non-reasoning = %d, want 64000", got)
	}

	// 6. 非推理模式传小值：抬升至 32768 底线
	if got := computeAdaptiveMaxTokens("glm-4-plus", 4096, false); got != 32768 {
		t.Fatalf("glm-4-plus 4096 non-reasoning = %d, want 32768", got)
	}
}

// TestResolveOfficialModelSpec_Extended 验证所有模型返回统一规格。
//
// 2026-10-30 起删除逐模型规则，不再有家族匹配与未收录兜底差异。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：改为全局统一上限。
func TestResolveOfficialModelSpec_Extended(t *testing.T) {
	models := []string{
		"GLM-5.3", "glm-4.5", "glm-4.7", "glm-4-plus", "kimi-k2.6",
		"deepseek-v4.1-flash", "openai/gpt-4o", "some-unlisted-model",
	}
	for _, m := range models {
		t.Run(m, func(t *testing.T) {
			spec := resolveOfficialModelSpec(m)
			if spec.MaxOutputTokens != defaultMaxOutputTokens {
				t.Errorf("MaxOutputTokens = %d, want %d", spec.MaxOutputTokens, defaultMaxOutputTokens)
			}
			if spec.ReasoningMax != defaultReasoningMaxTokens {
				t.Errorf("ReasoningMax = %d, want %d", spec.ReasoningMax, defaultReasoningMaxTokens)
			}
			if spec.ContextWindow != defaultContextWindow {
				t.Errorf("ContextWindow = %d, want %d", spec.ContextWindow, defaultContextWindow)
			}
		})
	}
}

// TestMutateModel_AdaptiveMaxTokens 验证 mutateModel 会就地重写自适应 max_tokens。
func TestMutateModel_AdaptiveMaxTokens(t *testing.T) {
	req := map[string]any{
		"model":      "glm-5.3",
		"max_tokens": 4096,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
		"reasoning_effort": "high",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	mutated := mutateModel(raw, "GLM-5.3")
	var parsed map[string]any
	if err := json.Unmarshal(mutated, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed["model"] != "GLM-5.3" {
		t.Fatalf("model = %v, want GLM-5.3", parsed["model"])
	}

	mt, ok := parsed["max_tokens"].(float64)
	if !ok || int64(mt) < 32768 {
		t.Fatalf("max_tokens = %v, want >= 32768", parsed["max_tokens"])
	}
}
