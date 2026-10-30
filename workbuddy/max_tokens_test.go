package main

import (
	"encoding/json"
	"testing"
)

// TestAdaptiveMaxTokens 验证统一上限下的自适应计算。
//
// 2026-10-30 起不再按模型区分：推理模式统一 128k，非推理模式统一 64k，
// 所有模型（含未收录）行为一致。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：改为全局统一上限，覆盖推理/非推理与未收录模型。
func TestAdaptiveMaxTokens(t *testing.T) {
	cases := []struct {
		name        string
		model       string
		requested   int64
		isReasoning bool
		wantTokens  int64
	}{
		{
			name:        "推理模式 + 保守 8192 -> 放宽至 128000",
			model:       "deepseek-v4.1-flash",
			requested:   8192,
			isReasoning: true,
			wantTokens:  128000,
		},
		{
			name:        "非推理模式 + 未传 -> 补足为 64000",
			model:       "deepseek-v4.1-flash",
			requested:   0,
			isReasoning: false,
			wantTokens:  64000,
		},
		{
			name:        "推理模式 + 未传 -> 补足为 128000",
			model:       "glm-5.3",
			requested:   0,
			isReasoning: true,
			wantTokens:  128000,
		},
		{
			name:        "非推理模式 + 传 8192 -> 抬升至 32768 底线",
			model:       "claude-3-5-sonnet",
			requested:   8192,
			isReasoning: false,
			wantTokens:  32768,
		},
		{
			name:        "推理模式 + 超限 200000 -> 截断至 128000",
			model:       "deepseek-v4.1-flash",
			requested:   200000,
			isReasoning: true,
			wantTokens:  128000,
		},
		{
			name:        "非推理模式 + 超限 200000 -> 截断至 64000",
			model:       "deepseek-v4.1-flash",
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
		{
			name:        "未收录模型 + 非推理模式 + 未传 -> 同样 64000",
			model:       "some-unlisted-model",
			requested:   0,
			isReasoning: false,
			wantTokens:  64000,
		},
		{
			name:        "合理区间内保留客户端值（推理）",
			model:       "gpt-5.6-luna",
			requested:   32768,
			isReasoning: true,
			wantTokens:  32768,
		},
		{
			name:        "合理区间内保留客户端值（非推理）",
			model:       "gpt-5.6-luna",
			requested:   40000,
			isReasoning: false,
			wantTokens:  40000,
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

// TestRelaxMaxTokensInPlace_Adaptive 验证 payload 反序列化后的自适应放宽/截断行为。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：对齐统一上限（推理 128k / 非推理 64k）。
func TestRelaxMaxTokensInPlace_Adaptive(t *testing.T) {
	// 场景 1：推理模式 + max_tokens: 8192 -> 放宽至 128000
	body := map[string]any{
		"model":            "deepseek-v4.1-flash",
		"reasoning_effort": "high",
		"max_tokens":       float64(8192),
	}
	changed := relaxMaxTokensInPlace(body)
	if !changed {
		t.Fatal("expected changed = true")
	}
	if got := jsonI64(body, "max_tokens"); got != 128000 {
		t.Fatalf("max_tokens = %d, want 128000", got)
	}

	// 场景 2：带 max_completion_tokens 8192 -> 提升为 128000 且移除旧字段
	body2 := map[string]any{
		"model":                 "deepseek-v4.1-flash",
		"reasoning_effort":      "high",
		"max_completion_tokens": float64(8192),
	}
	relaxMaxTokensInPlace(body2)
	if got := jsonI64(body2, "max_tokens"); got != 128000 {
		t.Fatalf("max_tokens = %d, want 128000", got)
	}
	if _, exists := body2["max_completion_tokens"]; exists {
		t.Fatal("expected max_completion_tokens to be removed")
	}

	// 场景 3：非推理模式 + 未传 -> 补足为 64000
	body3 := map[string]any{"model": "deepseek-v4.1-flash"}
	relaxMaxTokensInPlace(body3)
	if got := jsonI64(body3, "max_tokens"); got != 64000 {
		t.Fatalf("max_tokens = %d, want 64000", got)
	}
}

// TestPrepareUpstreamBody_AdaptiveMaxTokens 验证端到端 prepareUpstreamBody 的自适应结果。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-10-30；改动原因：对齐统一上限。
func TestPrepareUpstreamBody_AdaptiveMaxTokens(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4.1-flash","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}],"max_tokens":8192}`)
	out := prepareUpstreamBody(in, nil, nil, "deepseek-v4.1-flash")

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if got := jsonI64(obj, "max_tokens"); got != 128000 {
		t.Fatalf("max_tokens = %d, want 128000; raw: %s", got, string(out))
	}
}
