// sse_signature.go 描述上游 SSE 每一帧的「形态」，不描述「内容」。
//
// 为什么需要它：首字延迟的排查卡在一个死结上——插件侧 TTFT 与客户端首帧时间
// 逐条一致（只差 ~0.2s），但**看不出上游在首帧之前做了什么**。上游在慢请求里
// 到底是「在排队」「在预取缓存前缀」还是「在跑推理」，从插件侧看全是同一件事：
// 长时间没有首帧。
//
// 官方客户端的出站实现里存在「排队事件」（社区记载：排队事件带 request_set_id），
// 说明上游**可能**会在首帧前发出排队/路由提示。而本插件的 SSE 扫描器此前对
// 不认识的行直接 continue ⇒ 上游真发了这类事件也会被忽略。
//
// 因此这里做两件事：
//  1. 捕获上游**响应头**里与排队/限流有关的键值（白名单，截断）；
//  2. 捕获首帧前若干帧的**结构签名**（顶层键名 + 少量标识字段取值）。
//
// ⚠️ 隐私红线：签名只含**键名**与**枚举型标识**（event/type/status/object/index/
// error.code 之类）的取值；消息内容、增量文本、token 一律不落日志——对话内容是
// 用户隐私，不能进日志、不能进用量 feed。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// ssePreludeLimit 是首帧前最多记录多少帧的结构签名。
//
// 取 3：足够覆盖「排队提示 → 路由确认 → 首个内容帧」这类序列，又不至于把
// 日志变成流水账。
const ssePreludeLimit = 3

// sseFrameSignature 返回一帧 JSON 的结构签名。
//
// [参数] rawJSON：帧的 JSON 文本。
// [返回] 形如 "keys=choices,object,type type=chunk object=chat.completion.chunk" 的签名。
// 最近修改时间：2026-11-28；改动原因：新增，观测上游首帧前的帧形态。
func sseFrameSignature(rawJSON string) string {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		return "empty"
	}
	var chunk map[string]any
	if json.Unmarshal([]byte(trimmed), &chunk) != nil {
		return "non-json"
	}
	keys := make([]string, 0, len(chunk))
	for k := range chunk {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{"keys=" + strings.Join(keys, ",")}
	for _, field := range []string{"event", "type", "status", "object", "index"} {
		v, ok := chunk[field]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if len(t) <= 40 {
				parts = append(parts, field+"="+t)
			}
		case float64, bool:
			parts = append(parts, fmt.Sprintf("%s=%v", field, t))
		}
	}
	// error 帧只取 code / type：message 可能含上游内部细节，不落日志。
	if e, ok := chunk["error"].(map[string]any); ok {
		for _, field := range []string{"code", "type"} {
			if s, ok := e[field].(string); ok && len(s) <= 40 {
				parts = append(parts, "error."+field+"="+s)
			}
		}
	}
	return strings.Join(parts, " ")
}

// sseLineSignature 返回一行 SSE 的形态签名。
//
// [参数] line：SSE 原始行（可能带 "event:" / "data:" 前缀）。
// [返回] 形态签名；非 JSON 且非事件行时只给 "non-json"（不回显原文）。
// 最近修改时间：2026-11-28；改动原因：新增，观测上游首帧前的行形态。
func sseLineSignature(line string) string {
	trimmed := strings.TrimSpace(line)
	switch {
	case trimmed == "":
		return "empty"
	case strings.HasPrefix(trimmed, "event:"):
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		if len(name) > 40 {
			name = name[:40] + "…"
		}
		return "event-line:" + name
	case strings.HasPrefix(trimmed, "data:"):
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "[DONE]" {
			return "data:[DONE]"
		}
		return sseFrameSignature(data)
	default:
		return sseFrameSignature(trimmed)
	}
}

// upstreamHeaderSummary 摘出上游响应头里与排队/限流有关的键值。
//
// 只保留白名单前缀（x-* / ratelimit-*）与少数固定头，凭据类头一律不取；
// 取值截断，避免把上游的长文本写进日志。
//
// [参数] h：上游响应头。
// [返回] 形如 "content-type=text/event-stream x-request-id=abc" 的摘要；无头时返回空串。
// 最近修改时间：2026-11-28；改动原因：新增，此前上游响应头在调用点被直接丢弃。
func upstreamHeaderSummary(h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-") || strings.HasPrefix(lk, "ratelimit-") ||
			lk == "retry-after" || lk == "content-type" || lk == "server" || lk == "via" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.TrimSpace(h.Get(k))
		if len(v) > 80 {
			v = v[:80] + "…"
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}
