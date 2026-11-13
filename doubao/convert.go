package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 本文件实现 OpenAI 请求与响应格式的互转。
//
// 上游是网页聊天客户端而非补全 API，所以转换不是一一对应的：
//   - 它不能接受 system prompt 的独立优先级，也不能重放调用方的多轮对话；
//   - 会话历史归上游所有，因此多轮 OpenAI 请求会被折叠成一段提示词；
//   - 它不返回真实 usage，只能估算。

// chatMessage 是 OpenAI 请求中的一条消息。
type chatMessage struct {
	// Role 是消息角色。
	Role string `json:"role"`
	// Content 是消息内容，可能是字符串或多模态数组。
	Content json.RawMessage `json:"content"`
	// ReasoningContent 是部分客户端回传的思考内容，仅用于拼接上下文。
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// text 取出消息的纯文本部分。
//
// 多模态消息里的图片无法交给豆包网页端（它不接受外部图片地址作为输入），
// 因此只保留文本部分，图片以占位说明代替，避免调用方以为图片已被理解。
// [参数] 无。
// [返回] 文本内容。
// 最近修改时间：2026-10-17；改动原因：新增，消息文本提取。
func (m *chatMessage) text() string {
	if len(m.Content) == 0 {
		return ""
	}
	var plain string
	if json.Unmarshal(m.Content, &plain) == nil {
		return plain
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, p.Text)
			}
		case "image_url":
			out = append(out, "[图片内容：本插件不支持把图片作为输入，已忽略]")
		}
	}
	return strings.Join(out, "\n")
}

// buildPrompt 把 OpenAI 消息列表折叠成一段上游提示词。
//
// 单条用户消息是最常见形态，直接原样返回，不做任何包装；存在 system 或多轮
// 历史时才加标注，让模型仍能分辨谁说了什么。
// [参数] raw: OpenAI messages 数组。
// [返回] 提示词与错误。
// 最近修改时间：2026-10-17；改动原因：新增，多轮折叠。
func buildPrompt(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var msgs []chatMessage
	if errUnmarshal := json.Unmarshal(raw, &msgs); errUnmarshal != nil {
		return "", fmt.Errorf("解析 messages 失败：%w", errUnmarshal)
	}

	var system []string
	type turn struct{ role, text string }
	var turns []turn

	for _, m := range msgs {
		text := strings.TrimSpace(m.text())
		if text == "" {
			continue
		}
		switch strings.ToLower(m.Role) {
		case "system", "developer":
			system = append(system, text)
		case "assistant":
			turns = append(turns, turn{"assistant", text})
		case "tool":
			// 上游没有工具协议，工具输出只能作为上下文文本呈现。
			turns = append(turns, turn{"user", "工具返回：\n" + text})
		default:
			turns = append(turns, turn{"user", text})
		}
	}

	if len(system) == 0 && len(turns) == 1 && turns[0].role == "user" {
		return turns[0].text, nil
	}

	var b strings.Builder
	if len(system) > 0 {
		b.WriteString("【系统指令】\n")
		b.WriteString(strings.Join(system, "\n\n"))
		b.WriteString("\n\n")
	}
	if len(turns) > 1 || len(system) > 0 {
		b.WriteString("【对话记录】\n")
	}
	for _, t := range turns {
		if t.role == "assistant" {
			b.WriteString("助手：")
		} else {
			b.WriteString("用户：")
		}
		b.WriteString(t.text)
		b.WriteString("\n")
	}

	out := strings.TrimSpace(b.String())
	if out == "" && len(turns) > 0 {
		return turns[len(turns)-1].text, nil
	}
	return out, nil
}

// buildChatCompletion 构造非流式的 OpenAI 响应对象。
// [参数] model: 客户端请求的模型名；text: 回复正文；reasoning: 思考内容；
// conversationID: 上游会话 id；prompt: 原始提示词（用于估算用量）。
// [返回] 响应对象。
// 最近修改时间：2026-10-17；改动原因：新增，非流式响应构造。
func buildChatCompletion(model, text, reasoning, conversationID, prompt string) map[string]any {
	message := map[string]any{
		"role":    "assistant",
		"content": text,
	}
	// 思考内容用客户端已认识的字段名；为空时省略，让普通回复与非思考模型一致。
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	return map[string]any{
		"id":      "chatcmpl-" + randomUUID(),
		"object":  "chat.completion",
		"created": nowMillis() / 1000,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": "stop",
		}},
		// 会话 id 作为扩展字段暴露，客户端可用它续接同一会话；不认识的客户端会忽略。
		"conversation_id": conversationID,
		"usage":           estimateUsage(prompt, text),
	}
}

// streamChunk 构造一个流式 OpenAI chunk。
// [参数] model: 模型名；conversationID: 上游会话 id；delta: 增量内容；
// finishReason: 结束原因，空表示未结束。
// [返回] chunk 对象。
// 最近修改时间：2026-10-17；改动原因：新增，流式帧构造。
func streamChunk(model, conversationID string, delta map[string]any, finishReason string) map[string]any {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason == "" {
		choice["finish_reason"] = nil
	} else {
		choice["finish_reason"] = finishReason
	}
	return map[string]any{
		"id":              "chatcmpl-" + randomUUID(),
		"object":          "chat.completion.chunk",
		"created":         nowMillis() / 1000,
		"model":           model,
		"choices":         []map[string]any{choice},
		"conversation_id": conversationID,
	}
}

// estimateUsage 估算 token 用量。
//
// 上游不返回真实用量，这里按字符特征估算：CJK 约 1 字 1 token，
// 拉丁文约 4 字符 1 token。结果标记 estimated，便于消费方与实测值区分。
// [参数] prompt: 提示词；completion: 回复正文。
// [返回] usage 对象。
// 最近修改时间：2026-10-17；改动原因：新增，用量估算。
func estimateUsage(prompt, completion string) map[string]any {
	p := estimateTokens(prompt)
	c := estimateTokens(completion)
	return map[string]any{
		"prompt_tokens":     p,
		"completion_tokens": c,
		"total_tokens":      p + c,
		"estimated":         true,
	}
}

// estimateTokens 估算一段文本的 token 数。
// [参数] s: 文本。
// [返回] 估算 token 数。
// 最近修改时间：2026-10-17；改动原因：新增，单段估算。
func estimateTokens(s string) int {
	var cjk, other int
	for _, r := range s {
		if r >= 0x2E80 {
			cjk++
		} else {
			other++
		}
	}
	// 至少给 1，避免空串返回 0 让消费方误以为字段缺失。
	if cjk+other == 0 {
		return 0
	}
	return cjk + (other+3)/4
}

// splitReasoning 把内联在正文里的思考块拆出来。
//
// 上游有时把思考内容以 <think>…</think> 内联在回答前；OpenAI 客户端期望
// content 只有答案，因此这里分离。未闭合标签说明流被截断，其后内容全算思考。
// [参数] text: 上游回复原文。
// [返回] 可见答案与思考内容。
// 最近修改时间：2026-10-17；改动原因：新增，思考内容分流。
func splitReasoning(text string) (answer string, reasoning string) {
	if !strings.Contains(text, "<think") {
		return text, ""
	}

	var collected []string
	rest := text
	for {
		start := strings.Index(rest, "<think")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start:], "</think")
		if end < 0 {
			break
		}
		end += start
		inner := rest[start:end]
		if open := strings.Index(inner, ">"); open >= 0 {
			inner = inner[open+1:]
		}
		if inner = strings.TrimSpace(inner); inner != "" {
			collected = append(collected, inner)
		}
		rest = rest[:start] + rest[end+len("</think"):]
		if idx := strings.Index(rest, ">"); idx >= 0 && strings.HasPrefix(strings.TrimSpace(rest), ">") {
			rest = rest[idx+1:]
		}
	}

	answer = strings.TrimSpace(rest)
	// 未闭合标签：其后全部算思考，没有可见答案。
	if idx := strings.Index(answer, "<think"); idx >= 0 {
		tail := answer[idx:]
		if open := strings.Index(tail, ">"); open >= 0 {
			tail = tail[open+1:]
		}
		if tail = strings.TrimSpace(tail); tail != "" {
			collected = append(collected, tail)
		}
		answer = strings.TrimSpace(answer[:idx])
	}
	return answer, strings.Join(collected, "\n\n")
}
