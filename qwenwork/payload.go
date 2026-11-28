// payload.go 在透传路径上对客户端请求体做最小改写。
//
// QwenWork 走标准 OpenAI 兼容协议，客户端请求体可以原样转发；只有两处必须
// 由插件兜底：强制 stream=true（上游按 SSE 返回），以及按模型官方规格自适应
// 放宽 max_tokens，避免思考模型被客户端保守值提前截断。
package main

import (
	"encoding/json"
	"log"
	"strings"
)

// prepareUpstreamBody 对客户端请求体做透传前的最小改写。
//
// 同时返回已反序列化的**原始**请求体对象，供 MaaS 会话键派生复用：生产请求体普遍在
// 1MB 以上（400K tokens 量级），再解析一次的成本与内存都不小。
//
// ⚠️ 返回的是**原始**对象（改写前的浅拷贝）：会话身份必须基于客户端原文派生。
// 上下文预算裁剪会改变 messages，若用裁剪后的对象派生会话键，同一对话的票据与
// 上游会话会随裁剪结果漂移（每轮重建票据、失去上游会话绑定）。
//
// [参数] bodyRaw: 客户端原始请求体；upstreamModel: 已反解出的上游模型 ID。
// [返回] 改写后的请求体与原始请求体对象；解析失败时原样返回请求体、对象为 nil。
// 最近修改时间：2026-11-28；改动原因：随 MaaS 会话键派生补出解析结果，避免二次解析。
func prepareUpstreamBody(bodyRaw []byte, upstreamModel string) ([]byte, map[string]any) {
	if len(bodyRaw) == 0 {
		return bodyRaw, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(bodyRaw, &obj); err != nil {
		return bodyRaw, nil
	}
	// 原始对象的浅拷贝：会话键只读 messages，后续裁剪会整体替换 obj["messages"]，
	// 浅拷贝保留原数组头指针 ⇒ 会话身份不受裁剪影响。
	origin := make(map[string]any, len(obj))
	for k, v := range obj {
		origin[k] = v
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
	// 4. 上下文预算：超预算时裁掉最旧的中间历史，把上游看到的输入压回可控规模。
	//    依据：上游首字随输入 token 线性增长（实测 ~43ms/K token，70 万 token ≈
	//    30s 首字），而官方客户端会在上下文快满时压缩历史；本插件此前全量透传，
	//    同一会话每轮重发完整历史 ⇒ 「越用越慢」。裁剪只影响 messages，不改其它字段。
	if budget := loadedContextCharBudget(); budget > 0 {
		if raw, ok := obj["messages"].([]any); ok {
			if trimmed, dropped := trimMessagesToBudget(raw, budget); dropped > 0 {
				obj["messages"] = trimmed
				log.Printf("[qwenwork] context trimmed: dropped_messages=%d kept=%d budget_chars=%d input_chars=%d",
					dropped, len(trimmed), budget, messageCharsOfAny(raw))
			}
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return bodyRaw, origin
	}
	return out, origin
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
