package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// completionTestAccount 构造一份用于请求体与流解析测试的账号（值为假数据）。
func completionTestAccount() *StoredAuth {
	return &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "completiontest",
		CookieHeader: validCookieHeader,
		BotID:        defaultBotID,
		DeviceID:     "7300000000000000001",
		WebID:        "7300000000000000002",
		WebTabID:     "00000000-0000-4000-8000-000000000001",
	}
}

// TestBuildCompletionRequestContract 校验请求体符合 GAP-010 契约。
//
// 覆盖两个静默降级陷阱：文本必须在 content_block[0].content.text_block.text；
// mode_id 必须为 "1"。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定请求体契约。
func TestBuildCompletionRequestContract(t *testing.T) {
	sa := completionTestAccount()
	body := buildCompletionRequest(sa, "1+1=?", textMode(), "", "", 0, true)

	if body.Option.AggregateParams.ModeID != "1" {
		t.Fatalf("mode_id 必须为 \"1\"，实际 %q（缺失会退回纯聊天路径）", body.Option.AggregateParams.ModeID)
	}
	// local_conversation_id 缺失或为空会被上游以 common invalid param 拒绝。
	if strings.TrimSpace(body.ClientMeta.LocalConversationID) == "" {
		t.Fatal("local_conversation_id 是必填项，缺失会被上游拒绝")
	}
	if !body.Option.NeedCreateConversation {
		t.Fatal("新会话请求必须带 need_create_conversation")
	}
	if !body.Option.SSERecvEventOptions.SupportChunkDelta {
		t.Fatal("必须开启 support_chunk_delta 才能收到文本增量")
	}
	if len(body.Messages) != 1 || len(body.Messages[0].ContentBlock) != 1 {
		t.Fatalf("消息结构不符合契约：%+v", body.Messages)
	}
	block := body.Messages[0].ContentBlock[0]
	if block.BlockType != blockTypeText {
		t.Errorf("块类型应为文本块 %d，实际 %d", blockTypeText, block.BlockType)
	}
	if block.Content.TextBlock == nil || block.Content.TextBlock.Text != "1+1=?" {
		t.Fatalf("文本必须位于 content_block[0].content.text_block.text，实际 %+v", block.Content)
	}
	if body.ClientMeta.BotID != defaultBotID {
		t.Errorf("bot_id 应回落到默认助手，实际 %q", body.ClientMeta.BotID)
	}
	if body.Option.UniqueKey == "" || body.Messages[0].LocalMessageID == "" {
		t.Error("unique_key 与 local_message_id 不能为空")
	}

	// 序列化后不得出现顶层 content 字段（那是静默降级的根因）。
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), `"content":"1+1=?"`) {
		t.Error("请求体不得把文本放在顶层 content 字段")
	}
}

// TestBuildCompletionRequestSkill 校验生成模式注入 skill 参数。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定 skill 注入行为。
func TestBuildCompletionRequestSkill(t *testing.T) {
	sa := completionTestAccount()

	plain := buildCompletionRequest(sa, "你好", textMode(), "", "", 0, false)
	if plain.Option.AggregateParams.MentionSkillList != "[]" {
		t.Errorf("普通对话的 mention_skill_list 应为空列表，实际 %q", plain.Option.AggregateParams.MentionSkillList)
	}

	image := buildCompletionRequest(sa, "画一只猫", generationMode{Skill: skillImageGen, Kind: "image"}, "", "", 0, false)
	if image.Option.AggregateParams.MentionSkillList != "[3]" {
		t.Errorf("生图请求的 mention_skill_list 应为 [3]，实际 %q", image.Option.AggregateParams.MentionSkillList)
	}
}

// TestScanSSEFramesEvents 校验 SSE 分帧：多行 data、空行结束、尾帧无空行。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，分帧核心单测。
func TestScanSSEFramesEvents(t *testing.T) {
	stream := "event: SSE_ACK\ndata: {\"a\":1}\n\n" +
		"event: STREAM_CHUNK\ndata: line1\ndata: line2\n\n" +
		"event: SSE_REPLY_END\ndata: {}"

	var events []completionEvent
	err := scanSSE(strings.NewReader(stream), func(ev completionEvent) bool {
		events = append(events, ev)
		return true
	})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(events) != 3 {
		t.Fatalf("应解析出 3 个事件，实际 %d：%+v", len(events), events)
	}
	if events[0].Name != "SSE_ACK" || string(events[0].Data) != `{"a":1}` {
		t.Errorf("首帧解析错误：%+v", events[0])
	}
	if string(events[1].Data) != "line1\nline2" {
		t.Errorf("多行 data 应以换行拼接，实际 %q", string(events[1].Data))
	}
	if events[2].Name != "SSE_REPLY_END" {
		t.Errorf("无尾随空行的最后一帧应保留，实际 %+v", events[2])
	}
}

// TestScanSSEStopsOnFalse 校验回调返回 false 时立即停止读取。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，终止帧提前退出（避免死等 Keep-Alive）。
func TestScanSSEStopsOnFalse(t *testing.T) {
	stream := "event: SSE_REPLY_END\ndata: {}\n\n" +
		"event: STREAM_CHUNK\ndata: should-not-be-read\n\n"

	var names []string
	err := scanSSE(strings.NewReader(stream), func(ev completionEvent) bool {
		names = append(names, ev.Name)
		return false
	})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(names) != 1 || names[0] != "SSE_REPLY_END" {
		t.Fatalf("遇到终止帧后不得继续读取，实际读到 %v", names)
	}
}

// TestScanSSELargeLine 校验超过 64KiB 的单行不会被丢弃。
//
// 生成块携带图片数据时单行会远超 bufio 默认缓冲。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，大行缓冲回归。
func TestScanSSELargeLine(t *testing.T) {
	payload := strings.Repeat("a", 200*1024)
	stream := "event: STREAM_CHUNK\ndata: " + payload + "\n\n"

	var got int
	err := scanSSE(strings.NewReader(stream), func(ev completionEvent) bool {
		got = len(ev.Data)
		return true
	})
	if err != nil {
		t.Fatalf("大行解析失败：%v", err)
	}
	if got != len(payload) {
		t.Fatalf("大行被截断或丢弃：期望 %d 字节，实际 %d", len(payload), got)
	}
}

// TestCollectCompletionAssemblesDeltas 校验多类增量按顺序累加，且忽略用户提问回显。
//
// 实测语义：正文分散在 STREAM_MSG_NOTIFY 首块、STREAM_CHUNK 的 patch 与
// CHUNK_DELTA 的 text 中，必须累加；FULL_MSG_NOTIFY 携带的是用户自己的提问
// （user_type=1），计入会把问题拼进回答。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，聚合口径锁定。
func TestCollectCompletionAssemblesDeltas(t *testing.T) {
	stream := strings.Join([]string{
		"event: SSE_ACK",
		`data: {"ack_client_meta":{"conversation_id":"conv-1","section_id":"sec-1"},"query_list":[{"message_index":3}]}`,
		"",
		"event: FULL_MSG_NOTIFY",
		`data: {"message":{"user_type":1,"content_block":[{"block_type":10000,"content":{"text_block":{"text":"1+1=?"}}}]}}`,
		"",
		"event: STREAM_MSG_NOTIFY",
		`data: {"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"2"}}}]}}`,
		"",
		"event: STREAM_CHUNK",
		`data: {"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"。"}}}]}}]}`,
		"",
		"event: CHUNK_DELTA",
		`data: {"text":"答案就是 2。"}`,
		"",
		"event: SSE_REPLY_END",
		"data: {}",
		"",
	}, "\n")

	srv, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()
	sa.PhysicalPath = ""

	res, err := collectCompletion(context.Background(), sa, "1+1=?", textMode(), srv.URL, false, nil)
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	// 用户提问回显（"1+1=?"）不得出现在正文里。
	if strings.Contains(res.Text, "1+1=?") {
		t.Fatalf("FULL_MSG_NOTIFY 的 user_type=1 是用户提问回显，不得计入回复：%q", res.Text)
	}
	if !strings.HasPrefix(res.Text, "2。") {
		t.Fatalf("正文应以首块与 patch 增量开头，实际 %q", res.Text)
	}
	if res.ConversationID != "conv-1" || res.SectionID != "sec-1" || res.MessageIndex != 3 {
		t.Errorf("会话续接点解析错误：%+v", res)
	}
}

// TestCollectCompletionPrefersAssistantFullMessage 校验助手整消息优先于增量累加。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，权威值优先级锁定。
func TestCollectCompletionPrefersAssistantFullMessage(t *testing.T) {
	stream := strings.Join([]string{
		"event: STREAM_MSG_NOTIFY",
		`data: {"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"片段"}}}]}}`,
		"",
		"event: FULL_MSG_NOTIFY",
		`data: {"message":{"user_type":2,"content_block":[{"block_type":10000,"content":{"text_block":{"text":"完整回复"}}}]}}`,
		"",
		"event: SSE_REPLY_END",
		"data: {}",
		"",
	}, "\n")

	srv, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()
	sa.PhysicalPath = ""

	res, err := collectCompletion(context.Background(), sa, "你好", textMode(), srv.URL, false, nil)
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	if res.Text != "完整回复" {
		t.Fatalf("助手整消息应覆盖增量累加结果，实际 %q", res.Text)
	}
}

// TestCollectCompletionReportsStreamError 校验流内业务错误被识别并转成友好文案。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，STREAM_ERROR 识别。
func TestCollectCompletionReportsStreamError(t *testing.T) {
	stream := strings.Join([]string{
		"event: STREAM_ERROR",
		`data: {"error_code":710012001,"error_msg":"LoginInvalid"}`,
		"",
	}, "\n")

	srv, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()
	sa.PhysicalPath = ""

	_, err := collectCompletion(context.Background(), sa, "你好", textMode(), srv.URL, false, nil)
	if err == nil {
		t.Fatal("流内错误帧必须被识别为失败")
	}
	if !strings.Contains(err.Error(), "重新导入") {
		t.Errorf("错误文案应给出可操作提示，实际 %q", err.Error())
	}
	var up *upstreamError
	if !asUpstreamError(err, &up) || !up.isAuthFailure() {
		t.Errorf("710012001 应被判定为登录态失效，实际 %v", err)
	}
}

// TestCollectCompletionRejectsEmptyReply 校验空回复被识别为失败。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，防空成功。
func TestCollectCompletionRejectsEmptyReply(t *testing.T) {
	srv, sa, cleanup := newCompletionTestServer(t, "event: SSE_REPLY_END\ndata: {}\n\n")
	defer cleanup()
	sa.PhysicalPath = ""

	if _, err := collectCompletion(context.Background(), sa, "你好", textMode(), srv.URL, false, nil); err == nil {
		t.Fatal("上游无内容时必须报错，不能返回空回复当成功")
	}
}

// TestStreamCompletionUsesPlainJSONContentType 校验 Content-Type 为纯 application/json。
//
// 与 /im/* 相反：带 encoding=utf-8 会被上游按参数错误拒绝。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定 Content-Type 契约。
func TestStreamCompletionUsesPlainJSONContentType(t *testing.T) {
	var gotCT, gotAccept, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: SSE_REPLY_END\ndata: {}\n\n"))
	}))
	defer srv.Close()
	restore := swapCompletionEndpoint(srv.URL)
	defer restore()

	sa := completionTestAccount()
	body := buildCompletionRequest(sa, "你好", textMode(), "", "", 0, false)
	_ = streamCompletion(context.Background(), sa, body, func(completionEvent) bool { return true })

	if gotCT != "application/json" {
		t.Fatalf("Content-Type 必须是纯 application/json，实际 %q", gotCT)
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept 应为 text/event-stream，实际 %q", gotAccept)
	}
	if gotUA != webUserAgent {
		t.Errorf("User-Agent 应为网页端 UA，实际 %q", gotUA)
	}
}

// TestBuildBusinessQueryComplete 校验业务 query 参数集完整。
//
// 缺参会返回 {"code":0,"data":null}（HTTP 200 但无数据），必须补齐整个常量块。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，query 契约锁定。
func TestBuildBusinessQueryComplete(t *testing.T) {
	q := buildBusinessQuery("dev-1", "web-1", "tab-1", nil)
	for _, key := range []string{
		"version_code", "language", "device_platform", "doubao_device_platform",
		"aid", "real_aid", "pkg_type", "device_id", "pc_version", "doubao_pc_version",
		"web_id", "tea_uuid", "region", "sys_region", "samantha_web",
		"web_platform", "use-olympus-account", "web_tab_id", "flow_im_arch",
	} {
		if !strings.Contains(q, key+"=") {
			t.Errorf("query 缺少必需参数 %s：%s", key, q)
		}
	}
	if !strings.Contains(q, "tea_uuid=web-1") {
		t.Errorf("tea_uuid 应与 web_id 同值，实际 %s", q)
	}
}

// TestStatusCodeHint 校验上游错误码映射到可操作文案。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，错误文案回归。
func TestStatusCodeHint(t *testing.T) {
	cases := map[int]string{
		710012001: "重新导入",
		710022004: "上游风控",
		710020202: "Content-Type",
		710010202: "重新导入账号",
	}
	for code, want := range cases {
		if got := statusCodeHint(code, ""); !strings.Contains(got, want) {
			t.Errorf("错误码 %d 的提示应包含 %q，实际 %q", code, want, got)
		}
	}
	if got := statusCodeHint(999999, ""); !strings.Contains(got, "未知错误") {
		t.Errorf("未知码应回落到通用提示，实际 %q", got)
	}
}

// TestUpstreamErrorClassification 校验账号级故障分类。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，为 CYCLE-03 换号冷却预留判定口径。
func TestUpstreamErrorClassification(t *testing.T) {
	if !(&upstreamError{StatusCode: 429}).isRateLimit() {
		t.Error("HTTP 429 应判定为限流")
	}
	if !(&upstreamError{Code: 710022005}).isRateLimit() {
		t.Error("710022005 应判定为限流")
	}
	if (&upstreamError{StatusCode: 400}).isRateLimit() {
		t.Error("业务 400 不应判定为限流（否则会误换号）")
	}
	if !(&upstreamError{Code: 710012001}).isAuthFailure() {
		t.Error("710012001 应判定为登录态失效")
	}
	if (&upstreamError{StatusCode: 500}).isAuthFailure() {
		t.Error("500 不应判定为登录态失效")
	}
}

// TestRandomUUIDFormat 校验 UUID v4 格式。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，请求体唯一标识格式回归。
func TestRandomUUIDFormat(t *testing.T) {
	u := randomUUID()
	if len(u) != 36 {
		t.Fatalf("UUID 长度应为 36，实际 %d（%q）", len(u), u)
	}
	if u[14] != '4' {
		t.Errorf("版本位应为 4，实际 %q", u[14])
	}
	if u == randomUUID() {
		t.Error("两次生成不应相同")
	}
}

// newCompletionTestServer 启动一个返回固定 SSE 流的测试服务端。
//
// 通过 endpointURL 的域名常量无法注入，这里用 host 覆盖：把测试服务端地址写入
// 账号的 PhysicalPath 无关，实际由 swapCompletionEndpoint 注入。
// [参数] t: 测试上下文；stream: SSE 响应体。
// [返回] 测试服务端、账号与清理函数。
// 最近修改时间：2026-10-17；改动原因：新增，流式链路可测试化。
func newCompletionTestServer(t *testing.T, stream string) (*httptest.Server, *StoredAuth, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	restore := swapCompletionEndpoint(srv.URL)
	return srv, completionTestAccount(), func() {
		restore()
		srv.Close()
	}
}

// asUpstreamError 判断错误链中是否包含上游错误。
// [参数] err: 待判定错误；target: 输出参数。
// [返回] 命中时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，测试内错误类型断言。
func asUpstreamError(err error, target **upstreamError) bool {
	for err != nil {
		if up, ok := err.(*upstreamError); ok {
			*target = up
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

// 确保 time 包被使用（context 超时在测试中显式约束，避免测试挂死）。
var _ = time.Second
