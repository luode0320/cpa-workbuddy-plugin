package main

import "testing"

// TestResolveModelOptionsMapsSeedAliases 验证 Seed 客户端短 ID 会映射为上游精确 config_name。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-30 02:55:11；改动原因：补齐 Seed 别名兼容路径的回归覆盖与注释契约。
func TestResolveModelOptionsMapsSeedAliases(t *testing.T) {
	// 1. 枚举所有已确认的 Seed 短 ID 与上游精确 ID。
	tests := []struct {
		clientID string
		want     string
	}{
		{clientID: "seed-evolving", want: "Doubao-Seed-Evolving"},
		{clientID: "seed-2.1-pro", want: "Doubao-Seed-2.1-Pro"},
		{clientID: "seed-2.1-turbo", want: "Doubao-Seed-2.1-Turbo"},
	}

	// 2. 每个短 ID 都必须使用 SOLO 队列池，并映射到对应的精确 config_name。
	for _, tc := range tests {
		t.Run(tc.clientID, func(t *testing.T) {
			// 2.1 同时断言功能池和模型 ID，避免只校验映射值而漏掉路由变化。
			fn, configName := resolveModelOptions(tc.clientID)
			if fn != soloWorkLite {
				t.Fatalf("function = %q, want %q", fn, soloWorkLite)
			}
			if configName != tc.want {
				t.Fatalf("config_name = %q, want %q", configName, tc.want)
			}
		})
	}
}

// TestResolveModelOptionsPassesThroughExactConfigName 验证已是精确 config_name 的模型 ID 保持原样。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-30 02:55:11；改动原因：锁定 qwen3.8-max 等真实模型 ID 的透传语义。
func TestResolveModelOptionsPassesThroughExactConfigName(t *testing.T) {
	// 1. 精确模型 ID 必须继续使用 SOLO 池并保持原值。
	fn, configName := resolveModelOptions("qwen3.8-max")
	if fn != soloWorkLite {
		t.Fatalf("function = %q, want %q", fn, soloWorkLite)
	}
	if configName != "qwen3.8-max" {
		t.Fatalf("config_name = %q, want qwen3.8-max", configName)
	}
}

// TestResolveModelOptionsAutoUsesInlineChat 验证 auto 继续使用通用 inline_chat 池且不发送 config_name。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-30 02:55:11；改动原因：防止别名兼容逻辑改变 auto 的既有路由语义。
func TestResolveModelOptionsAutoUsesInlineChat(t *testing.T) {
	// 1. auto 不应被别名表影响，也不能向上游发送 config_name。
	fn, configName := resolveModelOptions("auto")
	if fn != inlineChat || configName != "" {
		t.Fatalf("resolveModelOptions(auto) = (%q, %q), want (%q, empty)", fn, configName, inlineChat)
	}
}

// TestBuildTraePayloadStreamDefaultMaxTokens 验证流式请求 max_tokens 的缺省补齐与下调至底线行为：
// 客户端未传时补默认值，显式传入低于全局输出底线的值统一抬升至底线，非流式路径不受影响。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-10-13 15:00:00；改动原因：max_tokens 统一服从 32768 最低输出底线。
func TestBuildTraePayloadStreamDefaultMaxTokens(t *testing.T) {
	// 1. 流式 + 未传 max_tokens：必须补默认值。
	payload := buildTraePayload([]map[string]any{{"role": "user", "content": "hi"}}, "qwen3.8-max", true, 0, nil, nil, nil, "")
	if got := payload["max_tokens"]; got != int(streamDefaultMaxTokens) {
		t.Fatalf("stream max_tokens = %v, want %d", got, streamDefaultMaxTokens)
	}

	// 2. 流式 + 显式 max_tokens 低于底线：抬升至 32768。
	payload = buildTraePayload(nil, "qwen3.8-max", true, 4096, nil, nil, nil, "")
	if got := payload["max_tokens"]; got != int(minMaxTokensFloor) {
		t.Fatalf("explicit stream max_tokens = %v, want %d", got, minMaxTokensFloor)
	}

	// 3. 非流式 + 未传 max_tokens：不补默认值，保持无 max_tokens 字段。
	payload = buildTraePayload(nil, "qwen3.8-max", false, 0, nil, nil, nil, "")
	if _, ok := payload["max_tokens"]; ok {
		t.Fatalf("non-stream max_tokens present = %v, want absent", payload["max_tokens"])
	}
}

// TestBuildTraePayloadAdaptiveMaxTokens 验证流式推理模型自适应扩展 max_tokens。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-10-13 02:40:00；改动原因：验证 Trae 流式推理模型自适应扩展 token 上限。
func TestBuildTraePayloadAdaptiveMaxTokens(t *testing.T) {
	// 1. 推理模型传入 8192 时，应自适应扩展至官方规格上限 (如 deepseek-reasoner 为 65536)
	payload := buildTraePayload(nil, "deepseek-reasoner", true, 8192, nil, nil, nil, "")
	if got, ok := payload["max_tokens"].(int); !ok || got < 32768 {
		t.Fatalf("deepseek-reasoner max_tokens = %v, want >= 32768", payload["max_tokens"])
	}
}
