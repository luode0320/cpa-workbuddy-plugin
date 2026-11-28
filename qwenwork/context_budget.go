// context_budget.go 提供请求上下文的规模控制（OpenAI 兼容形态）。
//
// 背景（2026-11-28 生产取证，QwenWork MaaS）：
//  1. **上游首字随输入 token 线性增长**：实测 TTFT ≈ 3090 + 10.20 × input(K)；
//     上游模型服务自身耗时（响应头 `X-Envoy-Upstream-Service-Time`）4.7~14.7s，
//     换算约 43ms/K token ⇒ 70 万 token 的首字约 30s。这与插件无关，是模型的
//     prefill 成本。
//  2. **官方客户端有 `context_config`**（1M/200K/400K），接近上限即**压缩历史**；
//     本插件此前**全量透传**，同一会话每轮重发完整历史，输入随轮次线性膨胀。
//     ⇒ 「直接用 QwenWork 客户端时没有这个问题」的差就在这里：官方发给上游的是
//     **压缩后**的上下文，我们发的是全量。
//
// 本文件用**裁剪**（drop 最旧的中间轮次）而非**压缩**（调模型做摘要）实现预算控制：
// 裁剪是纯本地操作，零额外 token 消耗、零额外延迟，也不会像压缩那样引入信息失真
// 与又一次上游调用。取舍是丢失被裁轮次的细节，因此策略必须保守——只裁最旧的部分，
// 永远保留 system、首轮 user（通常承载核心需求）与最近若干轮。
//
// 同步自 traework-provider 0.5.65 的同名机制（那边是 Trae 专有消息形态，这里改成
// OpenAI 兼容形态：content 为字符串或多模态分片数组、tool_calls 为 OpenAI 结构、
// tool 结果用 role=tool + tool_call_id 配对）。
package main

import (
	"strings"
)

const (
	// defaultContextCharBudget 是默认的输入字符预算。
	//
	// 取值依据（2026-11-28 同仓生产实测校准）：
	//   裁剪日志 input_chars=1199741 与 feed input_tokens=521369 对齐，
	//   得 **1 token ≈ 2.30 字符**（中文为主的长上下文）。
	//   生产原始输入 p50=440,013 token、p90=667,280、max=884,674，
	//   上游 >20s 占比：0-100k 15% / 300-400k 33% / 800k+ 100%。
	//
	// 460,000 字符 ≈ 200k token：把绝大多数会话压到「慢请求占比明显更低」的规模
	// （按上游 43ms/K token 估算，200k token 的首字约 4~6s，落在 10s 以内），
	// 同时仍保留约 200k token 历史供模型理解任务。
	// 需要更激进/更保守时用配置项 context_char_budget 覆盖，填 0 关闭裁剪。
	defaultContextCharBudget = 460000

	// contextKeepTailMessages 是裁剪时始终保留的最近消息条数。
	// 最近几轮承载当前任务上下文，裁掉会让模型失去即时语境。
	contextKeepTailMessages = 12

	// contextKeepHeadMessages 是裁剪时始终保留的最前消息条数。
	// 首轮通常是 system 提示与核心需求，是长会话里最不能丢的部分。
	// 同时保证首轮 user 消息留在头部 ⇒ 会话身份派生不受裁剪影响。
	contextKeepHeadMessages = 2
)

// contextCharBudget 是当前生效的上下文字符预算（可被配置覆盖）。
// 0 表示关闭裁剪（全量透传，与历史行为一致）。
var contextCharBudget = defaultContextCharBudget

// setContextCharBudget 设置上下文字符预算；负数忽略，0 表示关闭裁剪。
//
// [参数] n: 字符预算，0 关闭裁剪。
// [返回] 无。
// 最近修改时间：2026-11-28；改动原因：新增，使预算可运行时调整。
func setContextCharBudget(n int) {
	if n < 0 {
		return
	}
	contextCharBudget = n
}

// loadedContextCharBudget 读取当前生效的上下文字符预算。
//
// [参数] 无。
// [返回] 字符预算；0 表示关闭裁剪。
// 最近修改时间：2026-11-28；改动原因：与 setContextCharBudget 配对。
func loadedContextCharBudget() int {
	return contextCharBudget
}

// trimMessagesToBudget 在输入超预算时裁掉最旧的历史消息，返回裁剪后的消息与被裁条数。
//
// 裁剪策略（保守，永不破坏协议结构）：
//  1. 未超预算直接原样返回；
//  2. 永远保留头部 contextKeepHeadMessages 条与尾部 contextKeepTailMessages 条；
//  3. 只从「头部之后、尾部之前」的中间段**从旧到新删除**，即保留紧邻尾部的
//     最近中间历史，丢掉最旧的中间历史；
//  4. 被删消息若是 assistant 的 tool_calls 而对应 tool 结果仍在，一并删除该 tool
//     结果，避免留下无主的 tool 消息（上游按 tool_call_id 校验会 400）。
//
// [参数] messages: OpenAI 形态的 messages 数组；budget: 字符预算，<=0 表示不裁剪。
// [返回] 裁剪后的消息（未裁剪时是入参本身）；被裁掉的条数。
// 最近修改时间：2026-11-28；改动原因：新增，对齐官方客户端的上下文规模控制。
func trimMessagesToBudget(messages []any, budget int) ([]any, int) {
	if budget <= 0 || len(messages) == 0 {
		return messages, 0
	}
	norm := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			// 形态不是对象数组：不裁剪（宁可不裁，也不要把请求改坏）。
			return messages, 0
		}
		norm = append(norm, mm)
	}
	if messageChars(norm) <= budget {
		return messages, 0
	}
	head := contextKeepHeadMessages
	tail := contextKeepTailMessages
	// 消息太少时不做裁剪：删到只剩头尾反而破坏上下文完整性。
	if len(norm) <= head+tail+1 {
		return messages, 0
	}

	// 1. 逐条累计尾部字符量，先确定「必须保留的尾部」起点。
	keepTail := len(norm) - tail
	tailChars := messageChars(norm[keepTail:])
	headChars := messageChars(norm[:head])

	// 2. 头尾本身就超预算时（长会话末尾常见：最近几轮可能各自带大文件内容），
	//    必须先把尾部也削短，否则「只删中间」根本压不进预算。此时从尾部里
	//    由旧到新删，保住最后一条（当前轮次）不动。
	if headChars+tailChars > budget {
		keepTail = shrinkTailToBudget(norm, head, keepTail, budget-headChars)
		tailChars = messageChars(norm[keepTail:])
	}

	// 3. 中间段**从最新往回**累计，只保留最近的那一段。
	//
	//    方向必须是从尾部向前：被裁的应是「最旧的中间历史」，保留的应是
	//    「紧邻尾部的最近中间历史」。反向（从旧往前累计）会把刚做完的工作
	//    整段丢掉、只留会话开头的老上下文，表现为模型「失忆」。
	remaining := budget - headChars - tailChars
	if remaining < 0 {
		// 头尾削完仍超预算：退化到只保留尾部（尾部才是当前任务）。
		out := dropOrphanToolMessages(norm[keepTail:])
		if len(out) == 0 {
			return messages, 0
		}
		return messagesToAny(out), len(norm) - len(out)
	}
	cut := keepTail
	used := 0
	for i := keepTail - 1; i >= head; i-- {
		c := messageChars(norm[i : i+1])
		if used+c > remaining {
			break
		}
		used += c
		cut = i
	}
	// 不允许越过头部区间。
	if cut < head {
		cut = head
	}

	// 4. 组装结果：头部 + 中间保留段(cut..keepTail) + 尾部(keepTail..)。
	//    头部之后到 cut 之前是被丢弃的最旧中间历史。
	out := make([]map[string]any, 0, head+(keepTail-cut)+(len(norm)-keepTail))
	out = append(out, norm[:head]...)
	if keepTail > cut {
		out = append(out, norm[cut:keepTail]...)
	}
	out = append(out, norm[keepTail:]...)
	dropped := len(norm) - len(out)

	// 5. 清理无主 tool 消息：被裁掉的 assistant.tool_calls 对应的 tool 结果必须
	//    一起走，否则上游按 tool_call_id 校验会报错。
	out = dropOrphanToolMessages(out)
	if len(out) == 0 {
		return messages, 0
	}
	return messagesToAny(out), dropped
}

// shrinkTailToBudget 在「头 + 尾」已超预算时，把尾部从旧到新削短到给定额度内。
//
// 保留最后一条（当前轮次）是硬要求：它是本次请求真正要回答的内容，削掉它等于
// 请求失去意义。若连最后一条都超额度，则原样返回它（宁可超预算也不能丢当前轮）。
//
// [参数] messages: 全部消息；head: 头部条数；keepTail: 当前尾部起点；allowance: 尾部可用的字符额度。
// [返回] 新的尾部起点下标。
// 最近修改时间：2026-11-28；改动原因：长会话末尾单轮即超预算时需能削短尾部。
func shrinkTailToBudget(messages []map[string]any, head, keepTail, allowance int) int {
	if allowance < 0 {
		allowance = 0
	}
	// 从后往前累计，至少保留最后一条。
	last := len(messages) - 1
	used := messageChars(messages[last : last+1])
	cut := last
	for i := last - 1; i >= keepTail; i-- {
		c := messageChars(messages[i : i+1])
		if used+c > allowance {
			break
		}
		used += c
		cut = i
	}
	// 不允许越过头部区间。
	if cut < head {
		cut = head
	}
	return cut
}

// messageChars 统计一组消息的正文与 tool 调用负载字符数。
//
// 计入 tool_calls 参数是因为它们同样占用上游 token，漏算会让预算判断偏乐观；
// reasoning_content 同样计入（客户端回传的思考内容也是上下文的一部分）。
//
// [参数] messages: 待统计消息。
// [返回] 估算字符数。
// 最近修改时间：2026-11-28；改动原因：新增，供预算判断使用。
func messageChars(messages []map[string]any) int {
	var total int
	for _, m := range messages {
		switch c := m["content"].(type) {
		case string:
			total += len(c)
		case []any:
			for _, p := range c {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if t, ok := pm["text"].(string); ok {
					total += len(t)
				}
			}
		}
		if r, ok := m["reasoning_content"].(string); ok {
			total += len(r)
		}
		tcs, ok := m["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tc := range tcs {
			tm, ok := tc.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := tm["function"].(map[string]any)
			if !ok {
				continue
			}
			if args, ok := fn["arguments"].(string); ok {
				total += len(args)
			}
			if name, ok := fn["name"].(string); ok {
				total += len(name)
			}
		}
	}
	return total
}

// messageCharsOfAny 统计任意形态 messages 数组的字符数（供日志与预算判断复用）。
//
// [参数] messages: messages 数组（元素应为对象）。
// [返回] 估算字符数。
// 最近修改时间：2026-11-28；改动原因：新增，供裁剪日志给出裁剪前规模。
func messageCharsOfAny(messages []any) int {
	norm := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		norm = append(norm, mm)
	}
	return messageChars(norm)
}

// dropOrphanToolMessages 删除找不到对应 assistant.tool_calls 的 tool 消息。
//
// 裁剪后可能出现「assistant 调用消息被删、它的 tool 结果还在」的残缺结构，
// 上游按 tool_call_id 匹配会直接 400，因此必须成对清理。
//
// [参数] messages: 已裁剪消息。
// [返回] 清理后的消息（顺序不变）。
// 最近修改时间：2026-11-28；改动原因：新增，防止裁剪破坏工具调用配对。
func dropOrphanToolMessages(messages []map[string]any) []map[string]any {
	// 1. 收集所有仍存在的 tool_call id。
	known := make(map[string]struct{}, 16)
	for _, m := range messages {
		tcs, ok := m["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tc := range tcs {
			tm, ok := tc.(map[string]any)
			if !ok {
				continue
			}
			if id, ok := tm["id"].(string); ok && strings.TrimSpace(id) != "" {
				known[id] = struct{}{}
			}
		}
	}
	// 2. 过滤掉引用未知 id 的 tool 消息。
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		role, _ := m["role"].(string)
		if role == "tool" {
			id, _ := m["tool_call_id"].(string)
			if _, ok := known[strings.TrimSpace(id)]; !ok {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// messagesToAny 把消息对象数组还原成可写回请求体的通用数组。
//
// [参数] messages: 消息对象数组。
// [返回] 等价的 any 数组。
// 最近修改时间：2026-11-28；改动原因：新增，裁剪结果写回请求体用。
func messagesToAny(messages []map[string]any) []any {
	out := make([]any, 0, len(messages))
	for _, m := range messages {
		out = append(out, m)
	}
	return out
}
