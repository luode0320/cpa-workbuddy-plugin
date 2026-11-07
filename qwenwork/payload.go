// payload.go 在透传路径上对客户端请求体做最小改写。
//
// QwenWork 走标准 OpenAI 兼容协议，客户端请求体可以原样转发；只有两处必须
// 由插件兜底：强制 stream=true（上游按 SSE 返回），以及按模型官方规格自适应
// 放宽 max_tokens，避免思考模型被客户端保守值提前截断。
package main

import (
	"encoding/json"
	"strings"
)

// prepareUpstreamBody 对客户端请求体做透传前的最小改写。
//
// [参数] bodyRaw: 客户端原始请求体；upstreamModel: 已反解出的上游模型 ID。
// [返回] 改写后的请求体；解析失败时原样返回，保证请求仍可发出。
// 最近修改时间：2026-10-18 14:40:00；改动原因：请求体里的 model 必须改写为真实 ID。
func prepareUpstreamBody(bodyRaw []byte, upstreamModel string) []byte {
	if len(bodyRaw) == 0 {
		return bodyRaw
	}
	var obj map[string]any
	if err := json.Unmarshal(bodyRaw, &obj); err != nil {
		return bodyRaw
	}
	// 1. 上游按 SSE 返回；同步路径同样依赖事件流聚合。
	obj["stream"] = true
	// 2. 透传体会原样带上客户端模型名。宿主虽然已把 ExecutorRequest.Model
	// 反解为真实 ID，但 Payload 是客户端原文、里面的 model 仍是别名；不重写
	// 就会把别名发给上游，被判 invalid_request（400，param=None）。
	if m := strings.TrimSpace(upstreamModel); m != "" {
		obj["model"] = m
	}
	// 3. max_tokens 归一化：别名翻译 + GPT 系下限，其余原样透传。
	// 2026-11-07 按社区共识改造：不再放宽或抬升客户端显式值（详见 model_specs.go）。
	normalizeMaxTokensInPlace(obj)
	out, err := json.Marshal(obj)
	if err != nil {
		return bodyRaw
	}
	return out
}

// normalizeMaxTokensInPlace 已迁移至 model_specs.go：
// 该函数只做别名翻译与 GPT 系下限，不再放宽/抬升客户端显式值。

// jsonStr 读取对象里的字符串字段。
//
// [参数] m: 请求体对象；key: 字段名。
// [返回] 字段值；缺失或类型不符时返回空串。
// 最近修改时间：2026-10-18 00:36:00；改动原因：透传改写需要读取模型名。
func jsonStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
