package main

import (
	"encoding/json"
	"testing"
)

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

// TestEffectiveMaxTokens 验证客户端输出上限的取值优先级。
//
// 生产缺陷回归（2026-11-07）：请求结构体曾只解析 max_tokens，DSH / Codex 系
// 新客户端只发 max_completion_tokens，该值被 json.Unmarshal 静默丢弃，等价于
// 客户端从未声明上限——实测请求 12 token 仍产出 1091 字符 / 1119 tokens。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07 22:10:00；改动原因：补别名识别后的取值契约。
func TestEffectiveMaxTokens(t *testing.T) {
	cases := []struct {
		name    string
		maxTok  int
		maxComp int
		want    int
	}{
		{name: "仅显式 max_tokens", maxTok: 2000, want: 2000},
		{name: "仅别名 max_completion_tokens", maxComp: 12, want: 12},
		{name: "两者并存时显式优先", maxTok: 500, maxComp: 300, want: 500},
		{name: "两者都未传", want: 0},
		{name: "别名为 0 视为未传", maxComp: 0, want: 0},
		{name: "显式 0 且别名有效时取别名", maxTok: 0, maxComp: 64, want: 64},
		{name: "别名为负数视为未传", maxComp: -1, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &openAIRequest{Model: "test-model", MaxTokens: tc.maxTok, MaxCompletionTokens: tc.maxComp}
			if got := effectiveMaxTokens(req); got != tc.want {
				t.Fatalf("effectiveMaxTokens() = %d, want %d", got, tc.want)
			}
		})
	}

	if got := effectiveMaxTokens(nil); got != 0 {
		t.Fatalf("effectiveMaxTokens(nil) = %d, want 0", got)
	}
}

// TestOpenAIRequest_AliasUnmarshal 验证别名真实从 JSON 进入结构体。
//
// 只测 effectiveMaxTokens 无法覆盖「结构体缺 json tag」这一根因，必须走一遍
// json.Unmarshal —— 该路径才是生产上真正丢弃字段的地方。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07 22:10:00；改动原因：覆盖别名落库路径。
func TestOpenAIRequest_AliasUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{name: "只发别名", raw: `{"model":"m","max_completion_tokens":300}`, want: 300},
		{name: "只发旧字段", raw: `{"model":"m","max_tokens":2000}`, want: 2000},
		{name: "两者并存显式优先", raw: `{"model":"m","max_tokens":500,"max_completion_tokens":300}`, want: 500},
		{name: "都不发", raw: `{"model":"m"}`, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req openAIRequest
			if err := json.Unmarshal([]byte(tc.raw), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := effectiveMaxTokens(&req); got != tc.want {
				t.Fatalf("effectiveMaxTokens() = %d, want %d (raw=%s)", got, tc.want, tc.raw)
			}
		})
	}
}


// TestBuildTraePayload_KimiK3IncludesMaxTokens 锁定「插件确实把 max_tokens
// 写进了上行 payload」这一事实，用于把「模型忽略参数」与「插件漏传参数」区分开。
//
// 2026-11-08 生产实测：kimi-k3 在 max_tokens=5/20/50/5000 下分别输出
// 8522/13017/1091/1091 字符，参数完全不起约束作用。本用例证明这不是插件漏传。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 00:45:00；改动原因：为上游参数忽略的定性提供插件侧证据。
func TestBuildTraePayload_KimiK3IncludesMaxTokens(t *testing.T) {
	msgs := []map[string]any{{"role": "user", "content": "列出 1 到 300 的数字"}}
	for _, mt := range []int{5, 20, 50, 5000} {
		payload := buildTraePayload(msgs, "kimi-k3", true, mt, nil, nil, nil, "")
		got, ok := payload["max_tokens"]
		if !ok {
			t.Fatalf("max_tokens 未写入 payload（mt=%d）; payload=%v", mt, payload)
		}
		if got != mt {
			t.Fatalf("max_tokens 被改写: got=%v want=%d", got, mt)
		}
		// 上行体同时必须携带 config_name / model，否则上游会走错模型槽位。
		if _, ok := payload["config_name"]; !ok {
			t.Fatalf("config_name 缺失（mt=%d）; payload=%v", mt, payload)
		}
	}
}
