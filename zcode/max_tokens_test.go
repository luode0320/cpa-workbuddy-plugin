package main

import (
	"encoding/json"
	"testing"
)

// TestMutateModel_MaxTokens 验证 zcode 请求体经 mutateModel 后的输出上限。
//
// 2026-11-07 按社区共识改造后的契约：显式传值原样透传（不抬升、不按推理
// 模式区分、不按统一上限截断）；未传不补值，由上游决定。
// 旧实现会补足 128000、把 <=8192 放宽、把超限值截断、把小于 32768 的值抬回
// 地板，四类自研控制均已删除（详见 model_specs.go）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07；改动原因：删除自研 max_tokens 控制，对齐社区共识。
func TestMutateModel_MaxTokens(t *testing.T) {
	cases := []struct {
		name    string
		in      map[string]any
		want    int64
		wantHas bool
	}{
		{
			name:    "显式 4096 + 推理模式原样保留",
			in:      map[string]any{"max_tokens": 4096, "reasoning_effort": "high"},
			want:    4096, wantHas: true,
		},
		{
			name:    "生产事故回归：显式 2000 原样保留",
			in:      map[string]any{"max_tokens": 2000, "reasoning_effort": "high"},
			want:    2000, wantHas: true,
		},
		{
			name:    "显式 200000 不再按统一上限截断",
			in:      map[string]any{"max_tokens": 200000},
			want:    200000, wantHas: true,
		},
		{
			name:    "未传不补值",
			in:      map[string]any{},
			want:    0, wantHas: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"messages": []map[string]any{{"role": "user", "content": "hi"}}}
			for k, v := range tc.in {
				body[k] = v
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			mutated := mutateModel(raw, "GLM-5.3")
			var parsed map[string]any
			if err := json.Unmarshal(mutated, &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got, has := parsed["max_tokens"]
			if has != tc.wantHas {
				t.Fatalf("max_tokens 存在性 = %v, want %v; body=%v", has, tc.wantHas, parsed)
			}
			if !has {
				return
			}
			gotNum, ok := positiveIntValue(got)
			if !ok || gotNum != tc.want {
				t.Fatalf("max_tokens = %v (%d, ok=%v), want %d", got, gotNum, ok, tc.want)
			}
		})
	}
}

// TestNormalizeMaxTokensInPlace 覆盖别名翻译与 GPT 系下限两条归一化规则。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07；改动原因：对齐社区共识的最小干预策略。
func TestNormalizeMaxTokensInPlace(t *testing.T) {
	cases := []struct {
		name    string
		body    map[string]any
		want    int64
		wantHas bool
	}{
		{
			name:    "别名翻译：仅传 max_completion_tokens",
			body:    map[string]any{"model": "glm-5.3", "max_completion_tokens": float64(8192)},
			want:    8192, wantHas: true,
		},
		{
			name:    "显式 max_tokens 优先，别名只删不译",
			body:    map[string]any{"model": "glm-5.3", "max_tokens": float64(4096), "max_completion_tokens": float64(8192)},
			want:    4096, wantHas: true,
		},
		{
			name:    "别名非正整数不翻译",
			body:    map[string]any{"model": "glm-5.3", "max_completion_tokens": float64(0)},
			want:    0, wantHas: false,
		},
		{
			name:    "GPT 系 <16 抬至 16",
			body:    map[string]any{"model": "gpt-6-sol", "max_tokens": float64(5)},
			want:    16, wantHas: true,
		},
		{
			name:    "非 GPT 系 <16 原样保留",
			body:    map[string]any{"model": "glm-5.3", "max_tokens": float64(5)},
			want:    5, wantHas: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalizeMaxTokensInPlace(tc.body)
			got, has := tc.body["max_tokens"]
			if has != tc.wantHas {
				t.Fatalf("max_tokens 存在性 = %v, want %v; body=%v", has, tc.wantHas, tc.body)
			}
			if !has {
				return
			}
			gotNum, ok := positiveIntValue(got)
			if !ok || gotNum != tc.want {
				t.Fatalf("max_tokens = %v (%d, ok=%v), want %d", got, gotNum, ok, tc.want)
			}
			if _, leaked := tc.body["max_completion_tokens"]; leaked {
				t.Fatalf("max_completion_tokens 必须被删除，body=%v", tc.body)
			}
		})
	}
}
