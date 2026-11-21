package main

import (
	"context"
	"encoding/json"
	"io"
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

	video := buildCompletionRequest(sa, "生成一段海浪短视频", generationMode{Skill: skillVideoGen, Kind: "video"}, "", "", 0, false)
	if video.Option.AggregateParams.MentionSkillList != "[5]" {
		t.Errorf("生视频请求的 mention_skill_list 应为 [5]，实际 %q", video.Option.AggregateParams.MentionSkillList)
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
// 最近修改时间：2026-11-15 22:18:00；改动原因：调用签名随 fp 参数同步，并断言 fp 透传。
func TestBuildBusinessQueryComplete(t *testing.T) {
	const realFP = "verify_mw0wsekp_ccwh9CR4_Zdvn_4DXn_9y0B_qJcT8bxtinwc"
	q := buildBusinessQuery("dev-1", "web-1", "tab-1", realFP, nil)
	for _, key := range []string{
		"version_code", "language", "device_platform", "doubao_device_platform",
		"aid", "real_aid", "pkg_type", "device_id", "pc_version", "doubao_pc_version",
		"web_id", "tea_uuid", "region", "sys_region", "samantha_web",
		"web_platform", "use-olympus-account", "web_tab_id", "flow_im_arch",
		"fp",
	} {
		if !strings.Contains(q, key+"=") {
			t.Errorf("query 缺少必需参数 %s：%s", key, q)
		}
	}
	if !strings.Contains(q, "tea_uuid=web-1") {
		t.Errorf("tea_uuid 应与 web_id 同值，实际 %s", q)
	}
	if !strings.Contains(q, "fp="+realFP) {
		t.Errorf("fp 应原样透传账号真实值，实际 %s", q)
	}
	// 未采集 fp 时不应出现空参数：空值会被上游当成「设备指纹为空」再判一次风控。
	if bare := buildBusinessQuery("dev-1", "web-1", "tab-1", "", nil); strings.Contains(bare, "fp=") {
		t.Errorf("未采集 fp 时不应出现 fp 参数，实际 %s", bare)
	}
}

// TestStatusCodeHint 校验上游错误码映射到可操作文案。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 21:26:00；改动原因：-1 的成因改为「该账号未投放工作模式」，断言同步方向反转。
func TestStatusCodeHint(t *testing.T) {
	cases := map[int]string{
		710012001: "重新导入",
		710012000: "重新导入",
		710022004: "人机验证",
		710022002: "访问频繁",
		-1:        "没有被投放",
		710020202: "Content-Type",
		710020702: "重放",
		710022013: "签名",
		710010202: "重新导入账号",
	}
	for code, want := range cases {
		if got := statusCodeHint(code, ""); !strings.Contains(got, want) {
			t.Errorf("错误码 %d 的提示应包含 %q，实际 %q", code, want, got)
		}
	}
	// -1 的成因是账号未投放工作模式，不再是「请求体写错」：提示里不得再让用户
	// 去怀疑插件版本或请求体形状，那会把排查引向无效方向。
	if got := statusCodeHint(-1, ""); strings.Contains(got, "请求体") || strings.Contains(got, "最新版本") {
		t.Errorf("错误码 -1 的提示不应再归因请求体或建议升级版本，实际 %q", got)
	}
	// -1 必须给出可操作动作，否则用户只知道失败不知道怎么办。
	if got := statusCodeHint(-1, ""); !strings.Contains(got, "其他账号") {
		t.Errorf("错误码 -1 的提示必须给出换号这一可操作动作，实际 %q", got)
	}
	// 710022004 曾在 0.1.21 被误归因为设备指纹：不得再让人去重新点授权书签。
	if got := statusCodeHint(710022004, ""); strings.Contains(got, "授权书签") || strings.Contains(got, "设备指纹") {
		t.Errorf("710022004 的提示不应再归因设备指纹或要求重新授权，实际 %q", got)
	}
	if got := statusCodeHint(999999, ""); !strings.Contains(got, "未知错误") {
		t.Errorf("未知码应回落到通用提示，实际 %q", got)
	}
}

// TestStreamErrorHintReadsVerifyDecision 校验带 decision 的验证帧被识别为人机验证。
//
// 710022004 的 error_msg 写的是 "rate limited"，光看码和文案会误判成限流；
// 真正的成因在 extra.decision（type=verify / subtype=semantic_reasoning）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 06:10:00；改动原因：新增，验证帧口径回归。
func TestStreamErrorHintReadsVerifyDecision(t *testing.T) {
	// 1. 真实帧：decision 是嵌套 JSON 字符串，必须被解出来并按人机验证给文案。
	verifyFrame := `{"error_code":710022004,"error_msg":"rate limited","extra":{"decision":"{\"code\":\"10000\",\"from\":\"shark_admin\",\"type\":\"verify\",\"region\":\"cn\",\"subtype\":\"semantic_reasoning\",\"log_id\":\"20261116055224CD36C03412A894E2F020\",\"verify_scene\":\"doubao_message_web\"}","ack":"1"}}`
	e, ok := parseStreamError([]byte(verifyFrame))
	if !ok {
		t.Fatal("验证帧应被解析为错误")
	}
	d := e.verifyOf()
	if d == nil {
		t.Fatal("verifyOf 应识别 type=verify 的 decision")
	}
	if d.Subtype != "semantic_reasoning" || d.LogID != "20261116055224CD36C03412A894E2F020" {
		t.Fatalf("decision 字段解析不符：%+v", d)
	}
	msg := streamErrorHint(e)
	for _, want := range []string{"人机验证", "语义选图", "20261116055224CD36C03412A894E2F020"} {
		if !strings.Contains(msg, want) {
			t.Errorf("验证帧提示应包含 %q，实际 %q", want, msg)
		}
	}

	// 2. 没有 decision 的同码帧只能走兜底：文案里不能编造验证场景。
	plainFrame := `{"error_code":710022004,"error_msg":"rate limited"}`
	e2, ok2 := parseStreamError([]byte(plainFrame))
	if !ok2 {
		t.Fatal("无 decision 的错误帧同样应被解析")
	}
	if e2.verifyOf() != nil {
		t.Error("没有 decision 时 verifyOf 必须返回 nil")
	}

	// 3. decision 不是 verify 时不得套用验证文案。
	otherFrame := `{"error_code":710022004,"error_msg":"rate limited","extra":{"decision":"{\"type\":\"throttle\"}"}}`
	e3, ok3 := parseStreamError([]byte(otherFrame))
	if !ok3 {
		t.Fatal("throttle 帧应被解析")
	}
	if e3.verifyOf() != nil {
		t.Error("decision.type 不是 verify 时 verifyOf 必须返回 nil")
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

// resumeTestStream 是一段最小可用的 SSE：有正文与续接点。
//
// 只有 ACK + REPLY_END 会让 collectCompletion 判「上游没有返回内容」（正文为空、
// 思考为空、也没有生成结果）。续接点测试关心的是请求体，但仍需要一段正文才能
// 走到断言。
const resumeTestStream = "event: SSE_ACK\n" +
	"data: {\"ack_client_meta\":{\"conversation_id\":\"conv-9\",\"section_id\":\"sec-9\"}}\n\n" +
	"event: STREAM_MSG_NOTIFY\n" +
	"data: {\"content\":{\"content_block\":[{\"block_type\":10000,\"content\":{\"text_block\":{\"text\":\"好的\"}}}]}}\n\n" +
	"event: SSE_REPLY_END\n" +
	"data: {}\n\n"

// TestCollectCompletionPassesResumePoint 校验续接点被原样写进请求体的 client_meta。
//
// 视频是两段式的：首轮给参数确认，次轮必须指回同一条会话才会真正开跑。次轮的
// agent_mode / conversation_mode / model_item_key 全部留空，上游只认这三个字段
// （2026-11-17 抓包确认）。夹具取自确认轮请求体的 client_meta 原文。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:42:00；改动原因：新增，续接点透传锁定。
func TestCollectCompletionPassesResumePoint(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(resumeTestStream))
	}))
	defer srv.Close()
	restore := swapCompletionEndpoint(srv.URL)
	defer restore()

	sa := completionTestAccount()
	sa.PhysicalPath = ""
	resume := resumePoint{ConversationID: "38454121040780034", SectionID: "38454121040780290", MessageIndex: 5}

	if _, errRun := collectCompletionWithResume(context.Background(), sa, "确认，开始生成", generationMode{Skill: skillVideoGen, Kind: "video"}, resume, false, nil); errRun != nil {
		t.Fatalf("聚合失败：%v", errRun)
	}

	var sent completionRequest
	if errUnmarshal := json.Unmarshal(captured, &sent); errUnmarshal != nil {
		t.Fatalf("请求体不是合法 JSON：%v", errUnmarshal)
	}
	if sent.ClientMeta.ConversationID != resume.ConversationID {
		t.Errorf("conversation_id 应透传 %q，实际 %q", resume.ConversationID, sent.ClientMeta.ConversationID)
	}
	if sent.ClientMeta.LastSectionID != resume.SectionID {
		t.Errorf("last_section_id 应透传 %q，实际 %q", resume.SectionID, sent.ClientMeta.LastSectionID)
	}
	if sent.ClientMeta.LastMessageIndex == nil || *sent.ClientMeta.LastMessageIndex != resume.MessageIndex {
		t.Errorf("last_message_index 应透传 %d，实际 %v", resume.MessageIndex, sent.ClientMeta.LastMessageIndex)
	}
	// 确认轮不再新建会话，否则产物会落到另一条会话里。
	if sent.Option.NeedCreateConversation {
		t.Error("带续接点的请求不得要求上游新建会话")
	}
}

// TestCollectCompletionNewSessionOmitsResumePoint 校验新会话的 last_message_index 仍输出 null。
//
// 该字段不用 omitempty 是为了逐字节对齐网页请求；改成省略会让上游按另一种
// 形态解析（2026-11-16 已证伪一次，不能再退回去）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:42:00；改动原因：新增，新会话语义不得被续接点改造带偏。
func TestCollectCompletionNewSessionOmitsResumePoint(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(resumeTestStream))
	}))
	defer srv.Close()
	restore := swapCompletionEndpoint(srv.URL)
	defer restore()

	sa := completionTestAccount()
	sa.PhysicalPath = ""

	if _, errRun := collectCompletion(context.Background(), sa, "生成一段海浪短视频", generationMode{Skill: skillVideoGen, Kind: "video"}, "", true, nil); errRun != nil {
		t.Fatalf("聚合失败：%v", errRun)
	}

	var sent completionRequest
	if errUnmarshal := json.Unmarshal(captured, &sent); errUnmarshal != nil {
		t.Fatalf("请求体不是合法 JSON：%v", errUnmarshal)
	}
	if sent.ClientMeta.ConversationID != "" || sent.ClientMeta.LastSectionID != "" {
		t.Errorf("新会话不得带续接点：%+v", sent.ClientMeta)
	}
	if sent.ClientMeta.LastMessageIndex != nil {
		t.Errorf("新会话的 last_message_index 必须是 null，实际 %v", *sent.ClientMeta.LastMessageIndex)
	}
	if !sent.Option.NeedCreateConversation {
		t.Error("新会话必须要求上游新建会话")
	}
	// 字段必须真实出现在 JSON 里，而不是被 omitempty 吞掉。
	if !strings.Contains(string(captured), `"last_message_index":null`) {
		t.Error("last_message_index 必须显式输出 null")
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

// TestCollectCompletionKeepsSectionIDWhenAckEmpty 校验 ACK 里的空 section_id 不会抹掉续接点。
//
// 确认轮是带着上一轮的 section_id 进来的；上游 ACK 有时只回 conversation_id，
// section_id 留空。无条件赋值会把有效续接点抹成空串，而对外输出又是「非空才写」，
// 结果客户端下一次续接直接断链。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 13:30:00；改动原因：新增，锁定 ACK 逐字段非空覆盖。
func TestCollectCompletionKeepsSectionIDWhenAckEmpty(t *testing.T) {
	const stream = "event: SSE_ACK\n" +
		"data: {\"ack_client_meta\":{\"conversation_id\":\"conv-ack\",\"section_id\":\"\"}}\n\n" +
		"event: STREAM_MSG_NOTIFY\n" +
		"data: {\"content\":{\"content_block\":[{\"block_type\":10000,\"content\":{\"text_block\":{\"text\":\"好的\"}}}]}}\n\n" +
		"event: SSE_REPLY_END\n" +
		"data: {}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer srv.Close()
	restore := swapCompletionEndpoint(srv.URL)
	defer restore()

	sa := completionTestAccount()
	sa.PhysicalPath = ""
	resume := resumePoint{ConversationID: "38454121040780034", SectionID: "38454121040780290", MessageIndex: 5}

	res, errRun := collectCompletionWithResume(context.Background(), sa, "确认，开始生成", generationMode{Skill: skillVideoGen, Kind: "video"}, resume, false, nil)
	if errRun != nil {
		t.Fatalf("聚合失败：%v", errRun)
	}
	if res.SectionID != resume.SectionID {
		t.Errorf("ACK 里 section_id 为空时不得抹掉续接点，期望 %q，实际 %q", resume.SectionID, res.SectionID)
	}
	if res.ConversationID != "conv-ack" {
		t.Errorf("ACK 里的会话 id 应覆盖，实际 %q", res.ConversationID)
	}
}
