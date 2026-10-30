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
			model:       "flash",
			requested:   200000,
			isReasoning: true,
			wantTokens:  128000,
		},
		{
			name:        "非推理模式 + 超限 200000 -> 截断至 64000",
			model:       "flash",
			requested:   200000,
			isReasoning: false,
			wantTokens:  64000,
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

// TestWbModels_OfficialSpecs 验证静态模型表的输出上限按官方规格填充。
func TestWbModels_OfficialSpecs(t *testing.T) {
	for _, m := range wbModels() {
		if m.MaxCompletionTokens < minMaxTokensFloor {
			t.Fatalf("model %s MaxCompletionTokens = %d, want >= %d",
				m.ID, m.MaxCompletionTokens, minMaxTokensFloor)
		}
	}
}

// TestPrepareUpstreamBody_ForcesStreamAndRelaxesTokens 验证透传改写：
// 强制 stream=true，并在思考模式下把保守的 8192 放宽到官方推理上限。
func TestPrepareUpstreamBody_ForcesStreamAndRelaxesTokens(t *testing.T) {
	raw := []byte(`{"model":"qwen3.8-max-preview","stream":false,"max_tokens":8192,` +
		`"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	out := prepareUpstreamBody(raw, "")
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("prepareUpstreamBody produced invalid JSON: %v", err)
	}
	if stream, _ := obj["stream"].(bool); !stream {
		t.Fatal("stream must be forced to true")
	}
	if got := jsonI64(obj, "max_tokens"); got != 128000 {
		t.Fatalf("max_tokens = %d, want 128000", got)
	}
	// 原请求的其余字段必须原样保留。
	if _, ok := obj["messages"]; !ok {
		t.Fatal("messages must be preserved by passthrough rewrite")
	}
}

// TestPrepareUpstreamBody_RewritesModelToUpstreamID 验证透传体会把客户端
// 模型名改写为已反解的上游真实 ID。
//
// 缺陷背景（2026-10-18 生产实证）：宿主虽把 ExecutorRequest.Model 反解成
// 真实 ID，但 Payload 是客户端原文、里面仍是别名。插件此前直接透传 Payload，
// 上游收到别名后返回 400 invalid_request（param=None）。
func TestPrepareUpstreamBody_RewritesModelToUpstreamID(t *testing.T) {
	raw := []byte(`{"model":"qwen-3.8-max","stream":true,` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	out := prepareUpstreamBody(raw, "qwen3.8-max-preview")
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("prepareUpstreamBody produced invalid JSON: %v", err)
	}
	if got := jsonStr(obj, "model"); got != "qwen3.8-max-preview" {
		t.Fatalf("body model = %q, want qwen3.8-max-preview (alias must be rewritten)", got)
	}
	// 空上游模型名时不得清空原值。
	out2 := prepareUpstreamBody(raw, "")
	var obj2 map[string]any
	if err := json.Unmarshal(out2, &obj2); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got := jsonStr(obj2, "model"); got != "qwen-3.8-max" {
		t.Fatalf("empty upstreamModel must keep original model, got %q", got)
	}
}

// TestPrepareUpstreamBody_InvalidJSONPassthrough 验证非法 JSON 原样返回，
// 保证请求仍能发到上游由其报错，而不是被插件吞掉。
func TestPrepareUpstreamBody_InvalidJSONPassthrough(t *testing.T) {
	raw := []byte(`not-json`)
	if got := prepareUpstreamBody(raw, ""); string(got) != string(raw) {
		t.Fatalf("invalid JSON must pass through unchanged, got %q", got)
	}
}
