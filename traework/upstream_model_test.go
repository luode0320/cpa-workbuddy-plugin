package main

import "testing"

// resetTraeModelFunctions 清空全局通道映射。
//
// 模型目录合并会把 config_name -> 通道 写入全局 sync.Map；测试用例之间共享该
// 状态会造成断言互相污染，因此需要显式清理。
//
// [返回] 无。
// 最近修改时间：2026-11-07；改动原因：新增双通道目录后隔离用例状态。
func resetTraeModelFunctions() {
	traeModelFunctions.Range(func(key, _ any) bool {
		traeModelFunctions.Delete(key)
		return true
	})
}

// TestResolveModelOptionsMapsSeedAliases 验证 Seed 客户端短 ID 会映射为上游精确 config_name。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-08-30 02:55:11；改动原因：补齐 Seed 别名兼容路径的回归覆盖与注释契约。
func TestResolveModelOptionsMapsSeedAliases(t *testing.T) {
	resetTraeModelFunctions() // 清全局通道映射，避免其它用例残留影响回退断言
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
	resetTraeModelFunctions() // 无通道记录时必须回退默认 SOLO 池
	// 1. 精确模型 ID 必须继续使用 SOLO 池并保持原值。
	fn, configName := resolveModelOptions("qwen3.8-max")
	if fn != soloWorkLite {
		t.Fatalf("function = %q, want %q", fn, soloWorkLite)
	}
	if configName != "qwen3.8-max" {
		t.Fatalf("config_name = %q, want qwen3.8-max", configName)
	}
}

// TestResolveModelOptionsUsesRecordedCatalogFunction 验证目录记录的通道优先于默认池。
//
// 上游按「配置 × 通道」判定模型是否上架，拿未上架通道的模型去问会回 4001，
// 因此目录合并时记录的通道必须被聊天请求采用。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-07；改动原因：新增 chat_v3 / solo_agent 双通道目录。
func TestResolveModelOptionsUsesRecordedCatalogFunction(t *testing.T) {
	resetTraeModelFunctions()
	t.Cleanup(resetTraeModelFunctions)
	traeModelFunctions.Store("qwen3.8-max", traeCatalogChat)

	fn, configName := resolveModelOptions("qwen3.8-max")
	if fn != traeCatalogChat || configName != "qwen3.8-max" {
		t.Fatalf("function = %q config_name = %q, want %q/qwen3.8-max", fn, configName, traeCatalogChat)
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

	// 2. 流式 + 显式 max_tokens：原样尊重，不再抬升至底线（2026-11-07 修复）。
	payload = buildTraePayload(nil, "qwen3.8-max", true, 4096, nil, nil, nil, "")
	if got := payload["max_tokens"]; got != 4096 {
		t.Fatalf("explicit stream max_tokens = %v, want 4096", got)
	}

	// 2b. 非流式 + 显式 max_tokens：同样原样尊重（原实现经 applyMaxTokensFloor 抬到 32768）。
	payload = buildTraePayload(nil, "qwen3.8-max", false, 2000, nil, nil, nil, "")
	if got := payload["max_tokens"]; got != 2000 {
		t.Fatalf("explicit non-stream max_tokens = %v, want 2000", got)
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
	// 推理模型显式传入 8192：原样透传（2026-11-07 修复，不再放宽至 128k/32768）。
	payload := buildTraePayload(nil, "deepseek-reasoner", true, 8192, nil, nil, nil, "")
	if got, ok := payload["max_tokens"].(int); !ok || got != 8192 {
		t.Fatalf("deepseek-reasoner max_tokens = %v, want 8192", payload["max_tokens"])
	}
}
