// context_budget_test.go 锁定请求上下文预算的裁剪语义与「会话身份不受裁剪影响」。
//
// 背景（2026-11-28 生产取证）：上游首字随输入 token 线性增长（约 43ms/K token），
// 而官方客户端会在上下文快满时压缩历史；本插件此前全量透传 ⇒ 同一会话越用越慢。
package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// resetContextBudget 恢复默认预算，避免用例之间互相影响。
func resetContextBudget(t *testing.T) {
	t.Helper()
	old := loadedContextCharBudget()
	t.Cleanup(func() { setContextCharBudget(old) })
}

// makeMessages 造 n 条「每条 payload 字符」的消息。
func makeMessages(n, payload int) []any {
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{
			"role":    "user",
			"content": strings.Repeat("x", payload) + "-" + string(rune('a'+i%26)),
		})
	}
	return out
}

// TestTrimMessagesToBudget_UnderBudgetNoTrim 未超预算时零裁剪。
func TestTrimMessagesToBudget_UnderBudgetNoTrim(t *testing.T) {
	msgs := makeMessages(40, 10)
	out, dropped := trimMessagesToBudget(msgs, 100000)
	if dropped != 0 {
		t.Fatalf("未超预算时不得裁剪，dropped = %d", dropped)
	}
	if len(out) != len(msgs) {
		t.Fatalf("未超预算时消息条数不变，got %d want %d", len(out), len(msgs))
	}
}

// TestTrimMessagesToBudget_BudgetZeroDisables budget=0 时关闭裁剪。
func TestTrimMessagesToBudget_BudgetZeroDisables(t *testing.T) {
	msgs := makeMessages(40, 1000)
	out, dropped := trimMessagesToBudget(msgs, 0)
	if dropped != 0 || len(out) != len(msgs) {
		t.Fatalf("budget=0 必须关闭裁剪，dropped=%d len=%d", dropped, len(out))
	}
}

// TestTrimMessagesToBudget_KeepsHeadAndTail 只裁中间、头尾必须保住。
func TestTrimMessagesToBudget_KeepsHeadAndTail(t *testing.T) {
	// 消息大小刻意不均匀：中间段「最旧的大、最近的小」。
	// 裁剪方向反了（从旧往新累计）时，输出会严重超预算而不会被「条数」暴露。
	const budget = 20000
	msgs := make([]any, 0, 44)
	msgs = append(msgs, map[string]any{"role": "system", "content": strings.Repeat("h", 100)})
	msgs = append(msgs, map[string]any{"role": "user", "content": strings.Repeat("h", 100)})
	for i := 0; i < 20; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": strings.Repeat("L", 3000)})
	}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": strings.Repeat("s", 100)})
	}
	for i := 0; i < contextKeepTailMessages; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": strings.Repeat("t", 100)})
	}

	out, dropped := trimMessagesToBudget(msgs, budget)
	if dropped == 0 {
		t.Fatal("超预算时必须裁剪")
	}
	if len(out) >= len(msgs) {
		t.Fatalf("裁剪后消息数应变小，got %d want < %d", len(out), len(msgs))
	}
	// 裁剪结果必须真的落在预算内（方向反了会超预算）。
	if got := messageCharsOfAny(out); got > budget {
		t.Fatalf("裁剪后仍超预算：%d > %d（裁剪方向可能反了）", got, budget)
	}
	// 头部 2 条必须原样保留（首轮 user 是会话身份与核心需求的载体）。
	if !sameMessage(out[0], msgs[0]) || !sameMessage(out[1], msgs[1]) {
		t.Fatal("头部消息必须保留")
	}
	// 尾部必须保留到最后一条（当前轮次）。
	if !sameMessage(out[len(out)-1], msgs[len(msgs)-1]) {
		t.Fatal("最后一条消息必须保留")
	}
	// 被裁的必须是「最旧」的那一段：紧邻尾部的最新一条中间消息必须还在。
	newestMiddle := msgs[len(msgs)-1-contextKeepTailMessages]
	found := false
	for _, m := range out {
		if sameMessage(m, newestMiddle) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("紧邻尾部的最新中间消息必须保留（裁剪方向不得反）")
	}
}

// TestTrimMessagesToBudget_DropsOrphanToolMessages 被裁的 tool_calls 其结果必须一起走。
func TestTrimMessagesToBudget_DropsOrphanToolMessages(t *testing.T) {
	resetContextBudget(t)
	// 构造：头 2 条 + 中间一对 assistant/tool_calls + 尾部若干条。
	msgs := []any{
		map[string]any{"role": "system", "content": "sys"},
		map[string]any{"role": "user", "content": "req"},
		map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "f", "arguments": strings.Repeat("a", 2000)}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": strings.Repeat("r", 2000)},
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": strings.Repeat("b", 1000)})
	}
	out, dropped := trimMessagesToBudget(msgs, 5000)
	if dropped == 0 {
		t.Fatal("超预算时必须裁剪")
	}
	// 裁剪后不得留下找不到对应 assistant.tool_calls 的 tool 消息。
	known := map[string]struct{}{}
	for _, m := range out {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		tcs, ok := mm["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tc := range tcs {
			tm, _ := tc.(map[string]any)
			if id, ok := tm["id"].(string); ok {
				known[id] = struct{}{}
			}
		}
	}
	for _, m := range out {
		mm, _ := m.(map[string]any)
		if role, _ := mm["role"].(string); role == "tool" {
			id, _ := mm["tool_call_id"].(string)
			if _, ok := known[id]; !ok {
				t.Fatalf("残留了无主的 tool 消息（tool_call_id=%q）", id)
			}
		}
	}
}

// TestSetContextCharBudget 锁定预算可配置、负数忽略。
func TestSetContextCharBudget(t *testing.T) {
	resetContextBudget(t)
	setContextCharBudget(12345)
	if got := loadedContextCharBudget(); got != 12345 {
		t.Fatalf("预算 = %d, want 12345", got)
	}
	setContextCharBudget(-1)
	if got := loadedContextCharBudget(); got != 12345 {
		t.Fatalf("负数必须被忽略，预算变成 %d", got)
	}
	setContextCharBudget(0)
	if got := loadedContextCharBudget(); got != 0 {
		t.Fatalf("0 必须被接受（关闭裁剪），预算 = %d", got)
	}
}

// TestDefaultContextCharBudget 锁定默认预算取值（与兄弟插件 traework 一致）。
func TestDefaultContextCharBudget(t *testing.T) {
	if defaultContextCharBudget != 460000 {
		t.Fatalf("默认预算必须是 460000 字符（≈200k token），当前 %d", defaultContextCharBudget)
	}
	if contextKeepHeadMessages != 2 || contextKeepTailMessages != 12 {
		t.Fatalf("头尾保留条数变更：head=%d tail=%d", contextKeepHeadMessages, contextKeepTailMessages)
	}
}

// TestPrepareUpstreamBody_SessionKeyUnaffectedByTrim 锁定「裁剪不改变会话身份」。
//
// 这是最关键的一条：裁剪会改写 messages，若会话键基于裁剪后的消息派生，同一对话
// 的票据与上游会话会随裁剪结果漂移 ⇒ 每轮重建票据、失去上游会话绑定。
func TestPrepareUpstreamBody_SessionKeyUnaffectedByTrim(t *testing.T) {
	resetContextBudget(t)
	setContextCharBudget(20000)

	msgs := makeMessages(60, 1000)
	body, _ := json.Marshal(map[string]any{"model": "minimax-m3.1-flash", "messages": msgs})
	out, origin := prepareUpstreamBody(body, "minimax-m3.1-flash")
	if origin == nil {
		t.Fatal("必须返回原始请求体对象")
	}
	origMsgs, _ := origin["messages"].([]any)
	if len(origMsgs) != len(msgs) {
		t.Fatalf("返回的原始对象必须保留全部消息，got %d want %d", len(origMsgs), len(msgs))
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改写后不是合法 JSON：%v", err)
	}
	newMsgs, _ := got["messages"].([]any)
	if len(newMsgs) >= len(msgs) {
		t.Fatalf("请求体应被裁剪，got %d want < %d", len(newMsgs), len(msgs))
	}
	// 会话键基于原始对象 ⇒ 与裁剪前一致。
	want := maasSessionKeyFromPayload(map[string]any{"messages": msgs})
	gotKey := maasSessionKeyFromPayload(origin)
	if want == "" || gotKey != want {
		t.Fatalf("会话键受裁剪影响：want %q got %q", want, gotKey)
	}
}

// sameMessage 比较两条消息是否完全相同。
func sameMessage(a, b any) bool {
	am, ok1 := a.(map[string]any)
	bm, ok2 := b.(map[string]any)
	if !ok1 || !ok2 {
		return false
	}
	ac, _ := am["content"].(string)
	bc, _ := bm["content"].(string)
	return ac == bc
}
