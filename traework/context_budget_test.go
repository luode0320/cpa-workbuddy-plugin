// context_budget_test.go 锁定请求上下文预算的裁剪语义。
//
// 背景（2026-11-28 生产取证）：官方 Trae SOLO 客户端有上下文压缩能力，本插件此前
// 全量透传 messages，导致同一会话每轮重发完整历史、输入随轮次线性膨胀
// （生产实测 p50=413k token、最大 884k；session-d79dcc45 493 轮从 166 涨到 486,400）。
// 输入规模直接决定上游 prefill 成本（800k+ 实测 100% 超 20s），因此必须把输入
// 压回可控区间。
package main

import (
	"strconv"
	"strings"
	"testing"
)

// mkMsg 构造一条 Trae 归一化消息（content 为 text-parts 数组）。
func mkMsg(role, text string) map[string]any {
	return map[string]any{
		"role":    role,
		"content": []map[string]any{{"type": "text", "text": text}},
	}
}

// TestTrimMessagesNoopWhenUnderBudget 验证未超预算时原样返回、零裁剪。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 18:30:00；改动原因：新增预算控制的首个回归。
func TestTrimMessagesNoopWhenUnderBudget(t *testing.T) {
	msgs := []map[string]any{
		mkMsg("system", "you are helpful"),
		mkMsg("user", "hello"),
	}
	out, dropped := trimMessagesToBudget(msgs, 100000)
	if dropped != 0 {
		t.Fatalf("dropped = %d; want 0 when under budget", dropped)
	}
	if len(out) != len(msgs) {
		t.Fatalf("len(out) = %d; want %d", len(out), len(msgs))
	}
}

// TestTrimMessagesZeroBudgetDisablesTrimming 验证 budget<=0 时完全关闭裁剪。
// 这是 kill-switch：运维需要「全量透传」旧行为时必须能一键退回。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 18:30:00；改动原因：锁定 0 = 关闭的语义。
func TestTrimMessagesZeroBudgetDisablesTrimming(t *testing.T) {
	msgs := make([]map[string]any, 40)
	for i := range msgs {
		msgs[i] = mkMsg("user", strings.Repeat("x", 10000))
	}
	out, dropped := trimMessagesToBudget(msgs, 0)
	if dropped != 0 || len(out) != len(msgs) {
		t.Fatalf("dropped=%d len=%d; want 0/%d (budget 0 must disable trimming)", dropped, len(out), len(msgs))
	}
}

// TestTrimMessagesDropsOldestKeepsHeadAndTail 验证超预算时裁掉最旧的中间轮次，
// 且头部（system + 首轮需求）与尾部（最近上下文）必须保留。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 18:30:00；改动原因：锁定「只裁中间、保住头尾」的核心策略。
func TestTrimMessagesDropsOldestKeepsHeadAndTail(t *testing.T) {
	const chunk = 10000
	msgs := []map[string]any{
		mkMsg("system", "SYSTEM_HEAD_MARKER"),
		mkMsg("user", "FIRST_USER_MARKER"),
	}
	// 中间塞大量历史轮次。
	for i := 0; i < 30; i++ {
		msgs = append(msgs, mkMsg("user", strings.Repeat("m", chunk)))
		msgs = append(msgs, mkMsg("assistant", strings.Repeat("a", chunk)))
	}
	// 尾部放一个可识别的最近消息。
	msgs = append(msgs, mkMsg("user", "TAIL_MARKER"))

	out, dropped := trimMessagesToBudget(msgs, 200000)
	if dropped <= 0 {
		t.Fatalf("dropped = %d; want > 0 when over budget", dropped)
	}
	if len(out) >= len(msgs) {
		t.Fatalf("len(out) = %d; want < %d", len(out), len(msgs))
	}
	// 头部必须保留。
	if role, _ := out[0]["role"].(string); role != "system" {
		t.Fatalf("out[0].role = %q; want system", role)
	}
	if got := msgText(out[0]); !strings.Contains(got, "SYSTEM_HEAD_MARKER") {
		t.Fatalf("system head lost: %q", got)
	}
	if got := msgText(out[1]); !strings.Contains(got, "FIRST_USER_MARKER") {
		t.Fatalf("first user lost: %q", got)
	}
	// 尾部必须保留。
	last := out[len(out)-1]
	if got := msgText(last); !strings.Contains(got, "TAIL_MARKER") {
		t.Fatalf("tail marker lost: %q", got)
	}
	// 结果必须真的装进预算。
	if got := messageChars(out); got > 200000 {
		t.Fatalf("trimmed chars = %d; want <= 200000", got)
	}
}

// TestTrimMessagesDropsOrphanToolResults 验证被裁掉的 assistant.tool_calls
// 对应的 tool 结果会一并清理，避免上游按 tool_call_id 校验报 400。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 18:30:00；改动原因：裁剪不能破坏工具调用配对。
func TestTrimMessagesDropsOrphanToolResults(t *testing.T) {
	msgs := []map[string]any{
		mkMsg("system", "sys"),
		mkMsg("user", "task"),
	}
	// 早期的一对 tool 调用 + 结果（应被裁掉）。
	early := map[string]any{
		"role": "assistant",
		"content": []map[string]any{
			{"type": "text", "text": strings.Repeat("e", 20000)},
		},
		"tool_calls": []map[string]any{
			{"id": "call_early", "type": "function",
				"function": map[string]any{"name": "read_file", "arguments": "{}"}},
		},
	}
	earlyResult := map[string]any{
		"role":         "tool",
		"tool_call_id": "call_early",
		"content":      []map[string]any{{"type": "text", "text": "early result"}},
	}
	msgs = append(msgs, early, earlyResult)
	// 大量中间历史把 early 挤出保留窗口。
	for i := 0; i < 30; i++ {
		msgs = append(msgs, mkMsg("user", strings.Repeat("m", 10000)))
		msgs = append(msgs, mkMsg("assistant", strings.Repeat("a", 10000)))
	}
	msgs = append(msgs, mkMsg("user", "TAIL"))

	out, _ := trimMessagesToBudget(msgs, 150000)
	// 不允许出现无主 tool 消息。
	known := map[string]struct{}{}
	for _, m := range out {
		if tcs, ok := m["tool_calls"].([]map[string]any); ok {
			for _, tc := range tcs {
				if id, ok := tc["id"].(string); ok {
					known[id] = struct{}{}
				}
			}
		}
	}
	for _, m := range out {
		if role, _ := m["role"].(string); role != "tool" {
			continue
		}
		id, _ := m["tool_call_id"].(string)
		if _, ok := known[id]; !ok {
			t.Fatalf("orphan tool message survived trimming: tool_call_id=%q", id)
		}
	}
}

// TestTrimMessagesKeepsNewestMiddleHistory 锁定裁剪方向：被保留的中间段必须是
// 「最新的」那一段，被丢弃的必须是「最旧的」那一段。
//
// 背景（2026-11-28 生产实测）：首版实现的累计循环从 head 向后推进，留下的是最旧的
// 中间段、丢掉的是最近刚做完的轮次。生产日志形态为 dropped_messages=1160 kept=73，
// 即 2（头）+ 59（最旧中间）+ 12（尾）——模型看不到自己刚做的工作，只能重复劳动。
// 旧用例 TestTrimMessagesDropsOldestKeepsHeadAndTail 只断言「头尾保留 + 进预算」，
// 方向反了也全绿，因此必须单独锁定方向语义。
//
// 用例设计要点（2026-11-28 修正）：中间段必须显著大于 contextKeepTailMessages，
// 否则「保留段」整个落在尾部窗口内，两种方向的实现都会保留最新消息，用例失去区分力。
// 这里用 middle=40 条中间历史、每条 10k 字符（400k 总量），预算 200k：
// 头 2 条 + 尾 12 条约占 140k，剩余约 60k 只够装 6 条中间历史，因此保留段必然
// 落在「中间区」而不是尾部区，方向写反会被直接暴露。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 21:40:00；改动原因：原用例保留段落在尾部窗口内，
// 无法区分裁剪方向（旧实现也全绿），加大中间段使其必须落在中间区。
func TestTrimMessagesKeepsNewestMiddleHistory(t *testing.T) {
	const chunk = 10000
	msgs := []map[string]any{
		mkMsg("system", "SYSTEM_HEAD"),
		mkMsg("user", "FIRST_USER"),
	}
	// 中间历史：第 i 条带唯一标记 M<i>，i 越大越新。
	const middle = 40
	for i := 0; i < middle; i++ {
		msgs = append(msgs, mkMsg("user", "M"+strconv.Itoa(i)+strings.Repeat("x", chunk)))
	}
	// 尾部 12 条全部带 TAIL 标记，确保它们不参与「中间段方向」的判定。
	const tail = contextKeepTailMessages
	for i := 0; i < tail; i++ {
		msgs = append(msgs, mkMsg("user", "TAIL_NEWEST_"+strconv.Itoa(i)))
	}

	// 预算只够留下「头 + 少量中间 + 尾」，必须留下最新的中间段。
	out, dropped := trimMessagesToBudget(msgs, 200000)
	if dropped <= 0 {
		t.Fatalf("dropped = %d; want > 0 when over budget", dropped)
	}

	kept := map[int]bool{}
	for _, m := range out {
		text := msgText(m)
		if !strings.HasPrefix(text, "M") {
			continue
		}
		// 形如 M<idx>xxxx...，取 M 与第一个 x 之间的数字。
		end := strings.Index(text[1:], "x")
		if end <= 0 {
			continue
		}
		n, err := strconv.Atoi(text[1 : 1+end])
		if err != nil {
			continue
		}
		kept[n] = true
	}
	if len(kept) == 0 {
		t.Fatalf("no middle history kept; want the newest ones")
	}
	// 关键断言 1：保留段必须包含最靠近尾部的中间消息（离当前任务最近）。
	if !kept[middle-1] {
		t.Fatalf("newest middle message M%d was dropped; kept=%v", middle-1, kept)
	}
	// 关键断言 2：最旧的一条中间历史 M0 必须被丢弃（它离当前任务最远）。
	if kept[0] {
		t.Fatalf("oldest middle message M0 was kept; trimming direction is inverted")
	}
	// 关键断言 3：保留的中间段必须连续且紧贴尾部——不允许「留旧弃新」。
	maxKept := -1
	minKept := middle
	for n := range kept {
		if n > maxKept {
			maxKept = n
		}
		if n < minKept {
			minKept = n
		}
	}
	if maxKept != middle-1 {
		t.Fatalf("max kept middle index = %d; want %d (newest must survive)", maxKept, middle-1)
	}
	if minKept == 0 {
		t.Fatalf("min kept middle index = 0; oldest must be the one dropped")
	}
	// 关键断言 4：保留段必须真的落在中间区——即存在被丢弃的中间消息「比保留段更新」，
	// 这在方向正确的实现里不可能发生；反之若实现方向写反，保留段会是最旧的一段。
	for n := 0; n < middle; n++ {
		if kept[n] && n < minKept {
			t.Fatalf("kept non-contiguous middle index %d < minKept %d", n, minKept)
		}
	}
	// 头尾仍然必须在。
	if got := msgText(out[0]); !strings.Contains(got, "SYSTEM_HEAD") {
		t.Fatalf("system head lost: %q", got)
	}
	last := msgText(out[len(out)-1])
	if !strings.HasPrefix(last, "TAIL_NEWEST_") {
		t.Fatalf("tail lost: %q", last)
	}
	if got := messageChars(out); got > 200000 {
		t.Fatalf("trimmed chars = %d; want <= 200000", got)
	}
}

// TestContextCharBudgetConfigurable 验证预算可配置，0 关闭、负数忽略。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 18:30:00；改动原因：新增运行时旋钮需锁定边界。
func TestContextCharBudgetConfigurable(t *testing.T) {
	orig := loadedContextCharBudget()
	t.Cleanup(func() { setContextCharBudget(orig) })

	setContextCharBudget(500000)
	if got := loadedContextCharBudget(); got != 500000 {
		t.Fatalf("budget = %d; want 500000", got)
	}
	setContextCharBudget(0)
	if got := loadedContextCharBudget(); got != 0 {
		t.Fatalf("budget = %d; want 0 (disabled)", got)
	}
	setContextCharBudget(-1)
	if got := loadedContextCharBudget(); got != 0 {
		t.Fatalf("budget = %d after -1; want unchanged 0", got)
	}
}

// msgText 取出消息正文，供断言使用。
func msgText(m map[string]any) string {
	parts, _ := m["content"].([]map[string]any)
	var b strings.Builder
	for _, p := range parts {
		if txt, ok := p["text"].(string); ok {
			b.WriteString(txt)
		}
	}
	return b.String()
}
