package main

import "testing"

// TestBuildTraePayload_MaxTokens 验证 trae 请求体的输出上限处理。
//
// 2026-11-07 按社区共识改造后的契约：
//   - 客户端显式传值原样透传（不抬升、不按推理模式区分、不按统一上限截断）；
//   - 流式未传时补 streamDefaultMaxTokens —— trae 上游对未传会给极小上限，
//     这是有独立实测依据的必要补值，不是自研的上限控制；
//   - 非流式未传保持不设该字段（沿用既有语义）。
//
// 旧实现会对显式值做推理模式放宽、统一上限截断与 32768 地板抬升，
// 三类自研控制均已删除（详见 model_specs.go）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07；改动原因：删除自研 max_tokens 控制，对齐社区共识。
func TestBuildTraePayload_MaxTokens(t *testing.T) {
	cases := []struct {
		name    string
		maxTok  int
		stream  bool
		want    int
		wantHas bool
	}{
		{name: "显式 2000 原样保留（生产事故回归）", maxTok: 2000, stream: false, want: 2000, wantHas: true},
		{name: "推理模型显式 8192 不再放宽", maxTok: 8192, stream: true, want: 8192, wantHas: true},
		{name: "显式 200000 不再按统一上限截断", maxTok: 200000, stream: true, want: 200000, wantHas: true},
		{name: "显式 100 不再套 32768 地板", maxTok: 100, stream: false, want: 100, wantHas: true},
		{name: "流式未传补 trae 缺省值", maxTok: 0, stream: true, want: int(streamDefaultMaxTokens), wantHas: true},
		{name: "非流式未传不设字段", maxTok: 0, stream: false, want: 0, wantHas: false},
	}

	msgs := []map[string]any{{"role": "user", "content": "hi"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := buildTraePayload(msgs, "test-model", tc.stream, tc.maxTok, nil, nil, nil, "")
			got, has := payload["max_tokens"]
			if has != tc.wantHas {
				t.Fatalf("max_tokens 存在性 = %v, want %v; payload=%v", has, tc.wantHas, payload)
			}
			if !has {
				return
			}
			gotNum, ok := got.(int)
			if !ok {
				t.Fatalf("max_tokens 类型 = %T, want int", got)
			}
			if gotNum != tc.want {
				t.Fatalf("max_tokens = %d, want %d", gotNum, tc.want)
			}
		})
	}
}
