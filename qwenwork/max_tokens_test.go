package main

import (
	"encoding/json"
	"testing"
)

// TestNormalizeMaxTokensInPlace 验证输出上限字段的归一化行为。
//
// 2026-11-07 按社区共识改造后的契约：只做别名翻译与 GPT 系下限，
// 其余一律原样透传。改造前存在四类自研控制（统一 32768 地板、统一 128000
// 上限、推理模式放宽 <=8192、元数据无条件抬至 128000），GitHub 七家
// WorkBuddy 反代项目无一采用，且本方已有生产事故实证，全部删除。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07；改动原因：删除自研 max_tokens 控制，对齐社区共识。
func TestNormalizeMaxTokensInPlace(t *testing.T) {
	cases := []struct {
		name    string
		body    map[string]any
		want    int64
		wantHas bool
	}{
		{
			name:    "别名翻译：仅传 max_completion_tokens 8192",
			body:    map[string]any{"model": "qwen3.8-max", "max_completion_tokens": float64(8192)},
			want:    8192, wantHas: true,
		},
		{
			name: "显式 max_tokens 优先：别名只删不译",
			body: map[string]any{"model": "qwen3.8-max", "max_tokens": float64(4096),
				"max_completion_tokens": float64(8192)},
			want: 4096, wantHas: true,
		},
		{
			name:    "别名 0 不翻译",
			body:    map[string]any{"model": "qwen3.8-max", "max_completion_tokens": float64(0)},
			want:    0, wantHas: false,
		},
		{
			name:    "别名负数不翻译",
			body:    map[string]any{"model": "qwen3.8-max", "max_completion_tokens": float64(-1)},
			want:    0, wantHas: false,
		},
		{
			name:    "未传不补值",
			body:    map[string]any{"model": "qwen3.8-max"},
			want:    0, wantHas: false,
		},
		{
			name: "生产事故回归：推理模式 + 显式 2000 原样保留",
			body: map[string]any{"model": "qwen3.8-max", "reasoning_effort": "high",
				"max_tokens": float64(2000)},
			want: 2000, wantHas: true,
		},
		{
			name: "推理模式 + 保守 8192 不再放宽",
			body: map[string]any{"model": "qwen3.8-max", "reasoning_effort": "high",
				"max_tokens": float64(8192)},
			want: 8192, wantHas: true,
		},
		{
			name:    "显式小值不再套 32768 地板",
			body:    map[string]any{"model": "glm-5.2", "max_tokens": float64(100)},
			want:    100, wantHas: true,
		},
		{
			name:    "超出统一上限也不再预截断",
			body:    map[string]any{"model": "qwen3.8-max", "max_tokens": float64(200000)},
			want:    200000, wantHas: true,
		},
		{
			name:    "GPT 系 <16 抬至 16",
			body:    map[string]any{"model": "gpt-6-sol", "max_tokens": float64(5)},
			want:    16, wantHas: true,
		},
		{
			name:    "非 GPT 系 <16 原样保留",
			body:    map[string]any{"model": "glm-5.2", "max_tokens": float64(5)},
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
			if has {
				gotNum, ok := positiveIntValue(got)
				if !ok || gotNum != tc.want {
					t.Fatalf("max_tokens = %v (%d, ok=%v), want %d", got, gotNum, ok, tc.want)
				}
			}
			if _, leaked := tc.body["max_completion_tokens"]; leaked {
				t.Fatalf("max_completion_tokens 必须被删除，body=%v", tc.body)
			}
		})
	}
}

// TestPrepareUpstreamBody_MaxTokensPassthrough 验证端到端请求体不再改写显式值。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07；改动原因：删除自研 max_tokens 控制，对齐社区共识。
func TestPrepareUpstreamBody_MaxTokensPassthrough(t *testing.T) {
	in := []byte(`{"model":"qwen3.8-max","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}],"max_tokens":8192}`)
	out := prepareUpstreamBody(in, "qwen3.8-max")

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if got, _ := positiveIntValue(obj["max_tokens"]); got != 8192 {
		t.Fatalf("max_tokens = %d, want 8192（客户端显式值必须原样保留）; raw: %s", got, string(out))
	}

	// 未传 max_tokens -> 不得凭空补值
	in2 := []byte(`{"model":"qwen3.8-max","messages":[{"role":"user","content":"hi"}]}`)
	out2 := prepareUpstreamBody(in2, "qwen3.8-max")

	var obj2 map[string]any
	if err := json.Unmarshal(out2, &obj2); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if _, has := obj2["max_tokens"]; has {
		t.Fatalf("未传 max_tokens 时不得补值，raw: %s", string(out2))
	}
}
