// context_budget.go 提供请求上下文的规模控制。
//
// 背景（2026-11-28 生产取证）：Trae SOLO 官方客户端有**上下文压缩**能力（官方社区确认
// 为 SOLO Agent 专属特性：上下文快满时自动分层摘要 + 关键 token 保留），而本插件此前
// **全量透传** messages、没有任何裁剪或压缩。生产实测该差异的后果：
//
//	session-d79dcc45：493 轮，input 从 166 token 单调涨到 486,400 token
//	session-c06c4d48：207 轮，input 583,489 -> 728,996
//	全量 input_tokens：p50=413,216  p90=611,102  p95=670,062  max=884,674
//	48% 的请求落在 300k-500k 区间
//
// 输入规模直接决定上游 prefill 成本（实测分桶 >20s 占比）：
//
//	0-100k:  9.6%    300-400k: 31.7%    500-600k: 24.8%    800k+: 100%
//
// 即「越用越慢」的机制是：同一会话每轮把完整历史重发，输入线性膨胀。官方客户端靠
// 压缩把输入压在可控范围，插件不做任何处理，于是同一账号同一模型下插件明显更慢。
//
// 本文件用**裁剪**（drop 最旧的中间轮次）而非**压缩**（调模型做摘要）来实现预算控制：
// 裁剪是纯本地操作，零额外 token 消耗、零额外延迟，也不会像压缩那样引入信息失真与
// 又一次上游调用。取舍是丢失被裁轮次的细节，因此策略必须保守——只裁最旧的部分，
// 永远保留 system、首轮 user（通常承载核心需求）与最近若干轮。
package main

import (
	"strings"
)

const (
	// defaultContextCharBudget 是默认的输入字符预算。
	//
	// 取值依据（2026-11-28 生产实测校准）：
	//   裁剪日志 input_chars=1199741 与 feed input_tokens=521369 对齐，
	//   得 **1 token ≈ 2.30 字符**（中文为主的长上下文）。
	//   生产原始输入 p50=440,013 token、p90=667,280、max=884,674，
	//   上游 >20s 占比：0-100k 15% / 300-400k 33% / 800k+ 100%。
	//
	// 首版取 1,200,000 字符，实测只压到 52 万 token，仍落在 >20s 占比 ~20% 的区间，
	// 效果不足。改为 460,000 字符 ≈ 200k token：把绝大多数会话压到「慢请求占比
	// 明显更低」的规模，同时仍保留约 200k token 历史供模型理解任务。
	// 需要更激进/更保守时用配置项 context_char_budget 覆盖，填 0 关闭裁剪。
	defaultContextCharBudget = 460000

	// contextKeepTailMessages 是裁剪时始终保留的最近消息条数。
	// 最近几轮承载当前任务上下文，裁掉会让模型失去即时语境。
	contextKeepTailMessages = 12

	// contextKeepHeadMessages 是裁剪时始终保留的最前消息条数。
	// 首轮通常是 system 提示与核心需求，是长会话里最不能丢的部分。
	contextKeepHeadMessages = 2
)

// contextCharBudget 是当前生效的上下文字符预算（可被配置覆盖）。
// 0 表示关闭裁剪（全量透传，与历史行为一致）。
var contextCharBudget = defaultContextCharBudget

// setContextCharBudget 设置上下文字符预算；负数忽略，0 表示关闭裁剪。
//
// [参数] n: 字符预算，0 关闭裁剪。
// [返回] 无。
// 最近修改时间：2026-11-28 18:30:00；改动原因：新增，使预算可运行时调整。
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
// 最近修改时间：2026-11-28 18:30:00；改动原因：与 setContextCharBudget 配对。
func loadedContextCharBudget() int {
	return contextCharBudget
}

// trimMessagesToBudget 在输入超预算时裁掉最旧的历史消息，返回裁剪后的消息与被裁条数。
//
// 裁剪策略（保守，永不破坏协议结构）：
//  1. 未超预算直接原样返回，零拷贝开销之外的副作用；
//  2. 永远保留头部 contextKeepHeadMessages 条与尾部 contextKeepTailMessages 条；
//  3. 只从「头部之后、尾部之前」的中间段**从旧到新删除**，即保留紧邻尾部的
//     最近中间历史，丢掉最旧的中间历史；
//  4. 被删消息若是 assistant 的 tool_calls 而对应 tool 结果仍在，一并删除该 tool 结果，
//     避免留下无主的 tool 消息（上游会因 tool_call_id 对不上而 400）。
//
// [参数] messages: 已归一化的 Trae 消息；budget: 字符预算，<=0 表示不裁剪。
// [返回] 裁剪后的消息；被裁掉的条数。
// 最近修改时间：2026-11-28 18:30:00；改动原因：新增，对齐官方客户端的上下文规模控制。
func trimMessagesToBudget(messages []map[string]any, budget int) ([]map[string]any, int) {
	if budget <= 0 || len(messages) == 0 {
		return messages, 0
	}
	if messageChars(messages) <= budget {
		return messages, 0
	}
	head := contextKeepHeadMessages
	tail := contextKeepTailMessages
	// 消息太少时不做裁剪：删到只剩头尾反而破坏上下文完整性。
	if len(messages) <= head+tail+1 {
		return messages, 0
	}

	// 1. 逐条累计尾部字符量，先确定「必须保留的尾部」起点。
	keepTail := len(messages) - tail
	tailChars := messageChars(messages[keepTail:])
	headChars := messageChars(messages[:head])

	// 2. 头尾本身就超预算时（长会话末尾常见：最近几轮可能各自带大文件内容），
	//    必须先把尾部也削短，否则「只删中间」根本压不进预算。此时从尾部里
	//    由旧到新删，保住最后一条（当前轮次）不动。
	if headChars+tailChars > budget {
		keepTail = shrinkTailToBudget(messages, head, keepTail, budget-headChars)
		tailChars = messageChars(messages[keepTail:])
	}

	// 3. 中间段**从最新往回**累计，只保留最近的那一段。
	//
	//    方向必须是从尾部向前：被裁的应是「最旧的中间历史」，保留的应是
	//    「紧邻尾部的最近中间历史」。反向（从旧往前累计）会把刚做完的工作
	//    整段丢掉、只留会话开头的老上下文，表现为模型「失忆」——生产实测
	//    dropped_messages=1160/kept=73 即为此形态（1233 条里只留最早 59 条
	//    中间消息 + 最近 12 条，中间 1160 条刚完成的工作全部消失）。
	remaining := budget - headChars - tailChars
	if remaining < 0 {
		// 头尾削完仍超预算：退化到只保留尾部（尾部才是当前任务）。
		out := dropOrphanToolMessages(messages[keepTail:])
		if len(out) == 0 {
			return messages, 0
		}
		return out, len(messages) - len(out)
	}
	cut := keepTail
	used := 0
	for i := keepTail - 1; i >= head; i-- {
		c := messageChars(messages[i : i+1])
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
	out := make([]map[string]any, 0, head+(keepTail-cut)+(len(messages)-keepTail))
	out = append(out, messages[:head]...)
	if keepTail > cut {
		out = append(out, messages[cut:keepTail]...)
	}
	out = append(out, messages[keepTail:]...)
	dropped := len(messages) - len(out)

	// 5. 清理无主 tool 消息：被裁掉的 assistant.tool_calls 对应的 tool 结果必须一起走，
	//    否则上游按 tool_call_id 校验会报错。反过来，若 tool 结果被裁而 assistant 还在，
	//    也补不回缺失的调用对，因此这里只处理「tool 结果失去对应 assistant」的方向。
	out = dropOrphanToolMessages(out)
	if len(out) == 0 {
		return messages, 0
	}
	return out, dropped
}

// shrinkTailToBudget 在「头 + 尾」已超预算时，把尾部从旧到新削短到给定额度内。
//
// 保留最后一条（当前轮次）是硬要求：它是本次请求真正要回答的内容，削掉它等于
// 请求失去意义。若连最后一条都超额度，则原样返回它（宁可超预算也不能丢当前轮）。
//
// [参数] messages: 全部消息；head: 头部条数；keepTail: 当前尾部起点；allowance: 尾部可用的字符额度。
// [返回] 新的尾部起点下标。
// 最近修改时间：2026-11-28 18:35:00；改动原因：长会话末尾单轮即超预算时需能削短尾部。
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
// 计入 tool_calls 参数是因为它们同样占用上游 token，漏算会让预算判断偏乐观。
// [参数] messages: 待统计消息。
// [返回] 估算字符数。
// 最近修改时间：2026-11-28 18:30:00；改动原因：新增，供预算判断使用。
func messageChars(messages []map[string]any) int {
	var total int
	for _, m := range messages {
		parts, _ := m["content"].([]map[string]any)
		for _, p := range parts {
			if txt, ok := p["text"].(string); ok {
				total += len(txt)
			}
		}
		if tcs, ok := m["tool_calls"].([]map[string]any); ok {
			for _, tc := range tcs {
				if fn, ok := tc["function"].(map[string]any); ok {
					if args, ok := fn["arguments"].(string); ok {
						total += len(args)
					}
					if name, ok := fn["name"].(string); ok {
						total += len(name)
					}
				}
			}
		}
	}
	return total
}

// dropOrphanToolMessages 删除找不到对应 assistant.tool_calls 的 tool 消息。
//
// 裁剪后可能出现「assistant 调用消息被删、它的 tool 结果还在」的残缺结构，
// 上游按 tool_call_id 匹配会直接 400，因此必须成对清理。
// [参数] messages: 已裁剪消息。
// [返回] 清理后的消息（顺序不变）。
// 最近修改时间：2026-11-28 18:30:00；改动原因：新增，防止裁剪破坏工具调用配对。
func dropOrphanToolMessages(messages []map[string]any) []map[string]any {
	// 1. 收集所有仍存在的 tool_call id。
	known := make(map[string]struct{}, 16)
	for _, m := range messages {
		if tcs, ok := m["tool_calls"].([]map[string]any); ok {
			for _, tc := range tcs {
				if id, ok := tc["id"].(string); ok && strings.TrimSpace(id) != "" {
					known[id] = struct{}{}
				}
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
