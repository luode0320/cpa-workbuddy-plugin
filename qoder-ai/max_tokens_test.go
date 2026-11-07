package main

import (
	"encoding/json"
	"testing"
)

// TestBuildQoderBody_MaxTokens 验证 qoder 请求体的输出上限处理。
//
// 2026-11-07 按社区共识改造后的契约：
//   - 客户端显式传值原样透传（不抬升、不按推理模式区分、不截断）；
//   - 客户端未传时不覆盖，保留 baseprompt.json 里官方客户端模板自带的
//     max_tokens: 32768（该值来自上游官方模板，不是本插件自研的魔数）。
//
// 旧实现会在未传时补 128000、把 <=8192 放宽到 128000、把超限值截断到
// 128000，三类自研控制均已删除（详见 model_specs.go）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07；改动原因：删除自研 max_tokens 控制，对齐社区共识。
func TestBuildQoderBody_MaxTokens(t *testing.T) {
	// templateMaxTokens 是 baseprompt.json 中官方客户端模板自带的输出上限。
	const templateMaxTokens int64 = 32768

	cases := []struct {
		name   string
		maxTok int64
		effort string
		want   int64
	}{
		{name: "显式 8192 原样透传（推理模式）", maxTok: 8192, effort: "high", want: 8192},
		{name: "生产事故回归：显式 2000 原样保留", maxTok: 2000, effort: "high", want: 2000},
		{name: "显式 200000 不再按统一上限截断", maxTok: 200000, effort: "", want: 200000},
		{name: "显式 100 不再套 32768 地板", maxTok: 100, effort: "", want: 100},
		{name: "未传不覆盖，保留官方模板值", maxTok: 0, effort: "high", want: templateMaxTokens},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &openAIRequest{
				Model:           "kmodel",
				Messages:        []openAIMessage{{Role: "user", Content: "hi"}},
				MaxTokens:       tc.maxTok,
				ReasoningEffort: tc.effort,
			}
			raw, err := buildQoderBody(req, "kmodel", "")
			if err != nil {
				t.Fatalf("buildQoderBody: %v", err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			params, ok := body["parameters"].(map[string]any)
			if !ok {
				t.Fatalf("parameters missing: %s", string(raw))
			}
			got, has := params["max_tokens"]
			if !has {
				t.Fatalf("max_tokens missing; params=%v", params)
			}
			gotNum, ok := positiveIntValue(got)
			if !ok {
				t.Fatalf("max_tokens 不是正整数: %v (%T)", got, got)
			}
			if gotNum != tc.want {
				t.Fatalf("max_tokens = %d, want %d", gotNum, tc.want)
			}
		})
	}
}
