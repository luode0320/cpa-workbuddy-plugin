// context_summary_test.go 锁定「保留摘要」上下文压缩语义。
//
// 背景（2026-11-28）：官方桌面客户端在上下文接近上限时不是本地裁剪，而是**调模型
// 做一次锚定摘要**，再把摘要以 role=user 的 <conversation-checkpoint> 消息注回
// 历史（逆向证据：客户端内嵌 harness 的 @opencode/SessionCompaction，模板 d1()、
// 常量 a9/Hie）。本插件按同一语义实现，但摘要异步生成、不阻塞主请求首字。
package main

import (
	"os"
	"strings"
	"testing"
)

// summaryTestMessages 造一条「系统 + 首次用户 + 大量中间 + 尾部」的历史。
//
// 刻意让中间段每条都很大：这样预算边界落在中间段里，裁剪点 cut 才落在
// 「头部之后、尾部之前」，摘要注入的语义才有意义。
func summaryTestMessages() []any {
	msgs := make([]any, 0, 64)
	msgs = append(msgs, map[string]any{"role": "system", "content": "SYSTEM-PROMPT"})
	msgs = append(msgs, map[string]any{"role": "user", "content": "FIRST-REQUEST"})
	for i := 0; i < 40; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": strings.Repeat("m", 2000)})
	}
	for i := 0; i < contextKeepTailMessages; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": strings.Repeat("t", 200)})
	}
	return msgs
}

// summaryCheckpointOf 找出结果里的 checkpoint 消息（官方 <conversation-checkpoint>）。
//
// [参数] msgs: 消息数组。
// [返回] checkpoint 正文；没有则返回空串。
// 最近修改时间：2026-11-28；改动原因：新增，断言辅助。
func summaryCheckpointOf(msgs []any) string {
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := mm["role"].(string); role != "user" {
			continue
		}
		content, _ := mm["content"].(string)
		if strings.Contains(content, "<conversation-checkpoint>") {
			return content
		}
	}
	return ""
}

// TestApplyContextSummary_NoCache_TrimsAndRegistersPending 无缓存时：裁剪照旧，
// 并登记待摘要区段（供后台异步补摘要）。
func TestApplyContextSummary_NoCache_TrimsAndRegistersPending(t *testing.T) {
	clearContextSummaryCache()
	t.Cleanup(clearContextSummaryCache)
	msgs := summaryTestMessages()
	out, outcome := applyContextSummary(msgs, 20000, "sess-none")
	if !outcome.Trimmed {
		t.Fatal("超预算时必须裁剪")
	}
	if outcome.SummaryInjected {
		t.Fatal("没有缓存摘要时不得注入")
	}
	if len(out) >= len(msgs) {
		t.Fatalf("必须裁剪，got %d want < %d", len(out), len(msgs))
	}
	if outcome.PendingTo <= outcome.PendingFrom {
		t.Fatalf("必须登记待摘要区段，from=%d to=%d", outcome.PendingFrom, outcome.PendingTo)
	}
	if len(outcome.PendingBlock) != outcome.PendingTo-outcome.PendingFrom {
		t.Fatalf("待摘要区段内容数量不对，got %d want %d", len(outcome.PendingBlock), outcome.PendingTo-outcome.PendingFrom)
	}
}

// TestApplyContextSummary_CacheHit_InjectsCheckpoint 命中缓存：被裁掉的语义以
// checkpoint 消息还回请求，且头部（系统提示 + 首次请求）逐字保留。
func TestApplyContextSummary_CacheHit_InjectsCheckpoint(t *testing.T) {
	clearContextSummaryCache()
	t.Cleanup(clearContextSummaryCache)
	msgs := summaryTestMessages()
	norm, ok := toMessageObjects(msgs)
	if !ok {
		t.Fatal("消息形态异常")
	}
	// 摘要覆盖到裁剪点之后：本次被裁掉的全部区段都已被摘要覆盖。
	covered := 40
	storeContextSummary("sess-hit", &contextSummaryEntry{
		summary:  "## Objective\n- 做这件事",
		covered:  covered,
		headHash: contextSummaryPrefixHash(norm, covered),
	})
	out, outcome := applyContextSummary(msgs, 20000, "sess-hit")
	if !outcome.SummaryInjected {
		t.Fatal("命中缓存时必须注入摘要")
	}
	cp := summaryCheckpointOf(out)
	if cp == "" {
		t.Fatal("注入结果里必须有 <conversation-checkpoint> 消息")
	}
	if !strings.Contains(cp, "做这件事") {
		t.Fatal("checkpoint 必须带摘要正文")
	}
	if !strings.Contains(cp, "Treat it as historical context") {
		t.Fatal("checkpoint 必须保留官方的「历史上下文而非新指令」声明")
	}
	// 头部逐字保留：系统提示与首次请求不能被摘要吞掉。
	first, _ := out[0].(map[string]any)
	if first == nil || first["content"] != "SYSTEM-PROMPT" {
		t.Fatalf("系统提示必须逐字保留，got %v", first)
	}
	second, _ := out[1].(map[string]any)
	if second == nil || second["content"] != "FIRST-REQUEST" {
		t.Fatalf("首次请求必须逐字保留，got %v", second)
	}
	if outcome.PendingTo > outcome.PendingFrom {
		t.Fatal("摘要已覆盖全部被裁区段时不应再有待摘要区段")
	}
}

// TestApplyContextSummary_PartialCoverage_RollsForward 摘要只覆盖了一部分时：
// 注入已有摘要，同时登记剩余区段做滚动合并。
func TestApplyContextSummary_PartialCoverage_RollsForward(t *testing.T) {
	clearContextSummaryCache()
	t.Cleanup(clearContextSummaryCache)
	msgs := summaryTestMessages()
	norm, ok := toMessageObjects(msgs)
	if !ok {
		t.Fatal("消息形态异常")
	}
	covered := 10
	storeContextSummary("sess-partial", &contextSummaryEntry{
		summary:  "旧摘要",
		covered:  covered,
		headHash: contextSummaryPrefixHash(norm, covered),
	})
	out, outcome := applyContextSummary(msgs, 20000, "sess-partial")
	if !outcome.SummaryInjected {
		t.Fatal("部分覆盖也要注入已有摘要")
	}
	if !strings.Contains(summaryCheckpointOf(out), "旧摘要") {
		t.Fatal("注入的必须是已缓存的旧摘要")
	}
	if outcome.PendingTo <= outcome.PendingFrom {
		t.Fatal("未覆盖的区段必须登记待摘要")
	}
	if outcome.PendingFrom != covered {
		t.Fatalf("待摘要区段必须从已覆盖处接续，got %d want %d", outcome.PendingFrom, covered)
	}
	if outcome.PendingPreviousSummary != "旧摘要" {
		t.Fatal("滚动时必须带上旧摘要")
	}
}

// TestApplyContextSummary_StaleHistory_DiscardsSummary 客户端改写历史后，
// 旧摘要必须作废（既不注入也不继续滚动）。
func TestApplyContextSummary_StaleHistory_DiscardsSummary(t *testing.T) {
	clearContextSummaryCache()
	t.Cleanup(clearContextSummaryCache)
	msgs := summaryTestMessages()
	norm, ok := toMessageObjects(msgs)
	if !ok {
		t.Fatal("消息形态异常")
	}
	storeContextSummary("sess-stale", &contextSummaryEntry{
		summary:  "旧摘要",
		covered:  20,
		headHash: contextSummaryPrefixHash(norm, 20),
	})
	// 客户端回退/改写了第 5 条历史 ⇒ 签名失配。
	changed := make([]any, len(msgs))
	copy(changed, msgs)
	changed[5] = map[string]any{"role": "assistant", "content": "REWRITTEN"}
	out, outcome := applyContextSummary(changed, 20000, "sess-stale")
	if outcome.SummaryInjected {
		t.Fatal("历史被改写后不得注入旧摘要")
	}
	if summaryCheckpointOf(out) != "" {
		t.Fatal("结果里不得出现 checkpoint 消息")
	}
	if outcome.PendingPreviousSummary != "" {
		t.Fatal("历史被改写后不得继续滚动旧摘要")
	}
}

// TestApplyContextSummary_DisabledFlag_FallsBackToTrim 关闭摘要开关时退化为纯裁剪。
func TestApplyContextSummary_DisabledFlag_FallsBackToTrim(t *testing.T) {
	clearContextSummaryCache()
	t.Cleanup(clearContextSummaryCache)
	setContextSummaryEnabled(false)
	t.Cleanup(func() { setContextSummaryEnabled(true) })
	msgs := summaryTestMessages()
	out, outcome := applyContextSummary(msgs, 20000, "sess-off")
	if !outcome.Trimmed {
		t.Fatal("仍需裁剪")
	}
	if outcome.SummaryInjected {
		t.Fatal("关闭开关时不得注入摘要")
	}
	if len(out) >= len(msgs) {
		t.Fatal("关闭开关时也必须裁剪")
	}
}

// TestBuildContextSummaryPrompt_FirstSummary 首次摘要的提示词必须带官方三件套：
// <conversation> 块、「Create a new anchored summary」指令、锚定 Markdown 模板。
func TestBuildContextSummaryPrompt_FirstSummary(t *testing.T) {
	block := []map[string]any{
		{"role": "user", "content": "帮我把 X 改成 Y"},
		{"role": "assistant", "content": "好的，已经改完"},
	}
	prompt := buildContextSummaryPrompt("", block)
	for _, want := range []string{
		"<conversation>",
		"</conversation>",
		"Create a new anchored summary",
		"## Objective",
		"## Important Details",
		"## Work State",
		"## Next Move",
		"## Relevant Files",
		"[User]: ",
		"[Assistant]: ",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("提示词缺少 %q", want)
		}
	}
	if strings.Contains(prompt, "<prior-summary>") {
		t.Fatal("首次摘要不得带 <prior-summary>")
	}
}

// TestBuildContextSummaryPrompt_RollingSummary 滚动摘要必须带 <prior-summary>
// 与官方合并规则（旧摘要没带入的内容即丢失）。
func TestBuildContextSummaryPrompt_RollingSummary(t *testing.T) {
	block := []map[string]any{{"role": "user", "content": "继续"}}
	prompt := buildContextSummaryPrompt("旧摘要正文", block)
	for _, want := range []string{
		"<prior-summary>",
		"旧摘要正文",
		"Here is the summary of the conversation before the <conversation> above:",
		"anything you do not carry into the new summary is lost",
		"the conversation wins",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("滚动提示词缺少 %q", want)
		}
	}
}

// TestSerializeSummaryBlock_Markers 摘要输入必须用官方的交替文本标记。
func TestSerializeSummaryBlock_Markers(t *testing.T) {
	block := []map[string]any{
		{"role": "system", "content": "SYS"},
		{"role": "user", "content": "U1"},
		{"role": "assistant", "content": "A1"},
		{"role": "assistant", "reasoning_content": "R1", "content": "A2"},
		{"role": "tool", "content": strings.Repeat("t", contextSummaryToolResultMaxChars+50)},
		{"role": "assistant", "tool_calls": []any{map[string]any{
			"function": map[string]any{"name": "read_file", "arguments": `{"p":"a.go"}`},
		}}},
	}
	out := serializeSummaryBlock(block)
	for _, want := range []string{
		"[System update]: SYS",
		"[User]: U1",
		"[Assistant]: A1",
		"[Assistant reasoning]: R1",
		"[Tool result]: ",
		"[Assistant tool call]: read_file(",
		"[truncated]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("摘要序列化缺少 %q，got %q", want, out)
		}
	}
}

// TestContextSummaryPrefixHash 签名：内容不变则签名不变，改一个字即变。
func TestContextSummaryPrefixHash(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "world"},
	}
	h1 := contextSummaryPrefixHash(msgs, 2)
	h2 := contextSummaryPrefixHash(msgs, 2)
	if h1 == "" || h1 != h2 {
		t.Fatalf("签名必须稳定：%q vs %q", h1, h2)
	}
	msgs[1]["content"] = "world!"
	if contextSummaryPrefixHash(msgs, 2) == h1 {
		t.Fatal("内容变化后签名必须变化")
	}
	if contextSummaryPrefixHash(msgs, 0) != "" {
		t.Fatal("n<=0 必须返回空签名")
	}
}

// TestExtractCompletionContent 从聚合后的 completion 里取正文。
func TestExtractCompletionContent(t *testing.T) {
	if got := extractCompletionContent([]byte(`{"choices":[{"message":{"content":"摘要正文"}}]}`)); got != "摘要正文" {
		t.Fatalf("正文提取错误，got %q", got)
	}
	if got := extractCompletionContent([]byte(`{"choices":[{"text":"旧形态"}]}`)); got != "旧形态" {
		t.Fatalf("text 形态正文提取错误，got %q", got)
	}
	if got := extractCompletionContent([]byte(`not json`)); got != "" {
		t.Fatalf("非法 JSON 必须返回空串，got %q", got)
	}
	if got := extractCompletionContent(nil); got != "" {
		t.Fatalf("空输入必须返回空串，got %q", got)
	}
}

// TestStoreContextSummary_EvictsOldest 缓存容量封顶，淘汰最旧条目。
func TestStoreContextSummary_EvictsOldest(t *testing.T) {
	clearContextSummaryCache()
	t.Cleanup(clearContextSummaryCache)
	for i := 0; i < contextSummaryCacheMaxEntries+5; i++ {
		storeContextSummary("sess-"+string(rune('a'+i%26))+string(rune('0'+i/26)), &contextSummaryEntry{
			summary: "s",
			covered: 1,
		})
	}
	contextSummaryMu.Lock()
	n := len(contextSummaryCache)
	contextSummaryMu.Unlock()
	if n > contextSummaryCacheMaxEntries {
		t.Fatalf("缓存条目数必须封顶，got %d want <= %d", n, contextSummaryCacheMaxEntries)
	}
}

// TestExecutors_EnqueueContextSummary 锁定两个执行入口都会登记后台摘要任务。
//
// 装配层缺陷只有接线断言能覆盖：摘要逻辑本身测对了，调用点漏了接线 ⇒
// 被裁掉的历史永远补不回摘要，模型持续失忆。
//
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-28；改动原因：新增，覆盖摘要接线回归。
func TestExecutors_EnqueueContextSummary(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	text := string(raw)
	if n := strings.Count(text, "maybeEnqueueContextSummary(prep,"); n < 2 {
		t.Errorf("两个执行入口都必须登记后台摘要任务，got %d", n)
	}
}

// TestBuildContextSummaryBody_NoTools 摘要请求不得带工具（官方摘要调用也不给工具）。
func TestBuildContextSummaryBody_NoTools(t *testing.T) {
	body := buildContextSummaryBody("qwen3.8-max", "prompt")
	if len(body) == 0 {
		t.Fatal("摘要请求体不得为空")
	}
	if strings.Contains(string(body), `"tools"`) {
		t.Fatal("摘要请求不得带 tools")
	}
	if !strings.Contains(string(body), `"stream":true`) {
		t.Fatal("摘要请求必须走流式（宿主按 SSE 聚合）")
	}
}

// TestClaimContextSummarySlot_DedupesSameSession 同一会话不得并发跑两个摘要任务。
//
// 背景（2026-11-29 生产观测）：0.2.31 上线后同一会话在 3 秒内生成了两份摘要
// （covered=1169 两次）——摘要请求会带上整段待摘要历史（约 460K 字符 / 13 万
// token），重复登记等于重复烧上游额度，且会多占一个账号在跑槽位、与主请求抢
// 同一个账号。
func TestClaimContextSummarySlot_DedupesSameSession(t *testing.T) {
	contextSummaryMu.Lock()
	base := contextSummaryInFlight
	contextSummaryInFlight = 0
	contextSummaryMu.Unlock()
	defer func() {
		contextSummaryMu.Lock()
		contextSummaryInFlight = base
		contextSummaryMu.Unlock()
	}()

	key := "session-dedup-test"
	other := "session-dedup-other"
	defer releaseContextSummarySlot(key)
	defer releaseContextSummarySlot(other)

	if !claimContextSummarySlot(key) {
		t.Fatal("首次占用摘要槽位应成功")
	}
	if claimContextSummarySlot(key) {
		t.Fatal("同一会话不得并发跑两个摘要任务（重复烧上游额度并多占账号槽位）")
	}
	if !claimContextSummarySlot(other) {
		t.Fatal("不同会话应能各自占用槽位")
	}
	releaseContextSummarySlot(key)
	if !claimContextSummarySlot(key) {
		t.Fatal("释放后应能重新占用同一会话的槽位")
	}
}
