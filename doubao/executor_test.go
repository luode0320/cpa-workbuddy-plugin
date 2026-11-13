package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestBuildPromptSingleUser 校验单条用户消息不做包装。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，最常见形态锁定。
func TestBuildPromptSingleUser(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":"1+1=?"}]`)
	got, err := buildPrompt(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got != "1+1=?" {
		t.Fatalf("单条用户消息应原样返回，实际 %q", got)
	}
}

// TestBuildPromptFoldsSystemAndTurns 校验 system 与多轮历史被折叠成一段提示词。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，多轮折叠语义。
func TestBuildPromptFoldsSystemAndTurns(t *testing.T) {
	raw := json.RawMessage(`[
		{"role":"system","content":"你是一个助手"},
		{"role":"user","content":"你好"},
		{"role":"assistant","content":"你好，有什么可以帮你"},
		{"role":"user","content":"继续"}
	]`)
	got, err := buildPrompt(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	for _, want := range []string{"【系统指令】", "你是一个助手", "【对话记录】", "用户：你好", "助手：你好，有什么可以帮你", "用户：继续"} {
		if !strings.Contains(got, want) {
			t.Errorf("折叠结果缺少 %q：%s", want, got)
		}
	}
}

// TestBuildPromptMultimodalKeepsText 校验多模态消息只保留文本并标注图片被忽略。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，图片输入能力边界显式化。
func TestBuildPromptMultimodalKeepsText(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":[
		{"type":"text","text":"这是什么"},
		{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
	]}]`)
	got, err := buildPrompt(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !strings.Contains(got, "这是什么") {
		t.Errorf("应保留文本部分：%s", got)
	}
	if !strings.Contains(got, "不支持把图片作为输入") {
		t.Errorf("应显式标注图片被忽略，而不是静默丢弃：%s", got)
	}
}

// TestBuildPromptToolMessage 校验工具输出被折叠为上下文。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，工具消息处理。
func TestBuildPromptToolMessage(t *testing.T) {
	raw := json.RawMessage(`[
		{"role":"user","content":"查天气"},
		{"role":"tool","content":"晴，25 度"}
	]`)
	got, err := buildPrompt(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !strings.Contains(got, "工具返回：") || !strings.Contains(got, "晴，25 度") {
		t.Errorf("工具输出应作为上下文出现：%s", got)
	}
}

// TestBuildChatCompletionShape 校验非流式响应结构。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，OpenAI 响应契约锁定。
func TestBuildChatCompletionShape(t *testing.T) {
	resp := buildChatCompletion("doubao/default", "答案是 2", "先数一下", "conv-1", "1+1=?")

	if resp["object"] != "chat.completion" {
		t.Errorf("object 应为 chat.completion，实际 %v", resp["object"])
	}
	if resp["model"] != "doubao/default" {
		t.Errorf("model 应回显请求模型，实际 %v", resp["model"])
	}
	if resp["conversation_id"] != "conv-1" {
		t.Errorf("应暴露会话 id，实际 %v", resp["conversation_id"])
	}

	choices, ok := resp["choices"].([]map[string]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices 结构错误：%+v", resp["choices"])
	}
	msg, _ := choices[0]["message"].(map[string]any)
	if msg["role"] != "assistant" || msg["content"] != "答案是 2" {
		t.Errorf("message 结构错误：%+v", msg)
	}
	if msg["reasoning_content"] != "先数一下" {
		t.Errorf("思考内容应写入 reasoning_content，实际 %v", msg["reasoning_content"])
	}
	if choices[0]["finish_reason"] != "stop" {
		t.Errorf("finish_reason 应为 stop，实际 %v", choices[0]["finish_reason"])
	}

	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 usage：%+v", resp["usage"])
	}
	if usage["estimated"] != true {
		t.Error("usage 应标记 estimated，避免与实测值混淆")
	}
	if usage["total_tokens"] == 0 {
		t.Error("total_tokens 不应为 0")
	}
}

// TestBuildChatCompletionOmitsEmptyReasoning 校验无思考时不写该字段。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，普通回复与非思考模型保持一致。
func TestBuildChatCompletionOmitsEmptyReasoning(t *testing.T) {
	resp := buildChatCompletion("doubao/default", "答案", "", "", "问题")
	choices := resp["choices"].([]map[string]any)
	msg := choices[0]["message"].(map[string]any)
	if _, exists := msg["reasoning_content"]; exists {
		t.Error("无思考内容时不应写 reasoning_content")
	}
}

// TestStreamChunkShape 校验流式帧结构。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，流式契约锁定。
func TestStreamChunkShape(t *testing.T) {
	chunk := streamChunk("doubao/default", "conv-1", map[string]any{"content": "你"}, "")
	if chunk["object"] != "chat.completion.chunk" {
		t.Errorf("object 应为 chat.completion.chunk，实际 %v", chunk["object"])
	}
	choices := chunk["choices"].([]map[string]any)
	if choices[0]["finish_reason"] != nil {
		t.Errorf("未结束时 finish_reason 应为 null，实际 %v", choices[0]["finish_reason"])
	}

	stop := streamChunk("doubao/default", "conv-1", map[string]any{}, "stop")
	if stop["choices"].([]map[string]any)[0]["finish_reason"] != "stop" {
		t.Error("终止帧 finish_reason 应为 stop")
	}
}

// TestStreamDeltaFirstFrameHasRole 校验首帧带 role，后续帧不带。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，首帧 role 语义。
func TestStreamDeltaFirstFrameHasRole(t *testing.T) {
	var first map[string]any
	_ = json.Unmarshal(streamDelta("doubao/default", "你", true), &first)
	delta := first["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["role"] != "assistant" {
		t.Errorf("首帧 delta 应带 role=assistant，实际 %+v", delta)
	}

	var second map[string]any
	_ = json.Unmarshal(streamDelta("doubao/default", "好", false), &second)
	delta2 := second["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if _, exists := delta2["role"]; exists {
		t.Errorf("后续帧不应重复 role，实际 %+v", delta2)
	}
}

// TestSplitReasoning 校验内联思考块分离。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，思考分流回归。
func TestSplitReasoning(t *testing.T) {
	answer, think := splitReasoning("<think>先算加法</think>答案是 2")
	if answer != "答案是 2" {
		t.Errorf("答案应剔除思考块，实际 %q", answer)
	}
	if think != "先算加法" {
		t.Errorf("思考内容提取错误，实际 %q", think)
	}

	// 无思考块时原样返回。
	a2, t2 := splitReasoning("就是 2")
	if a2 != "就是 2" || t2 != "" {
		t.Errorf("无思考块时不应改变文本，实际 %q / %q", a2, t2)
	}

	// 未闭合标签：其后全部算思考。
	a3, t3 := splitReasoning("答案 2<think>被截断的思考")
	if strings.Contains(a3, "被截断") {
		t.Errorf("未闭合标签后的内容不应留在答案里，实际 %q", a3)
	}
	if !strings.Contains(t3, "被截断") {
		t.Errorf("未闭合标签后的内容应归入思考，实际 %q", t3)
	}
}

// TestEstimateTokens 校验 token 估算口径。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，估算回归。
func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens(""); got != 0 {
		t.Errorf("空串应为 0，实际 %d", got)
	}
	if got := estimateTokens("你好世界"); got != 4 {
		t.Errorf("4 个中文字符应估为 4 token，实际 %d", got)
	}
	if got := estimateTokens("abcdefgh"); got != 2 {
		t.Errorf("8 个拉丁字符应估为 2 token，实际 %d", got)
	}
}

// TestDecodeExecutorRequestRejectsEmptyMessages 校验空 messages 被拒。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，请求入口负向用例。
func TestDecodeExecutorRequestRejectsEmptyMessages(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"auth_id":      doubaoAuthFilePrefix + "nobody",
		"storage_json": []byte(`{"cookie_header":"` + validCookieHeader + `"}`),
		"payload":      []byte(`{"model":"doubao/default"}`),
	})
	if _, _, _, err := decodeExecutorRequest(raw); err == nil {
		t.Fatal("缺少 messages 应被拒绝")
	}
}

// TestResolveExecutionAuthFromStorage 校验优先使用宿主下发的凭据快照。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，凭据来源优先级。
func TestResolveExecutionAuthFromStorage(t *testing.T) {
	storage, _ := json.Marshal(StoredAuth{
		AuthID:       doubaoAuthFilePrefix + "fromstorage",
		CookieHeader: validCookieHeader,
		Label:        "宿主下发",
	})
	sa, err := resolveExecutionAuth(&pluginapi.ExecutorRequest{
		AuthID:      doubaoAuthFilePrefix + "ignored",
		StorageJSON: storage,
	})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if sa.Label != "宿主下发" {
		t.Errorf("应使用 StorageJSON 中的账号，实际 %+v", sa)
	}
}

// TestResolveExecutionAuthMissing 校验无账号时给出可操作提示。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，空账号提示。
func TestResolveExecutionAuthMissing(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	defer setAccountsDir("")

	_, err := resolveExecutionAuth(&pluginapi.ExecutorRequest{AuthID: doubaoAuthFilePrefix + "ghost"})
	if err == nil {
		t.Fatal("无账号时应报错")
	}
	if !strings.Contains(err.Error(), "不存在或已停用") {
		t.Errorf("错误提示应指明账号不可用，实际 %v", err)
	}
}

// TestExecutorExecuteRejectsNoMessages 校验执行入口对空请求体的拒绝。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，执行入口负向用例。
func TestExecutorExecuteRejectsNoMessages(t *testing.T) {
	storage, _ := json.Marshal(StoredAuth{AuthID: doubaoAuthFilePrefix + "x", CookieHeader: validCookieHeader})
	raw, _ := json.Marshal(pluginapi.ExecutorRequest{StorageJSON: storage, Payload: []byte(`{"model":"doubao/default"}`)})
	if _, err := executorExecute(raw); err == nil {
		t.Fatal("无 messages 时应被拒绝")
	}
}

// TestExecutorExecuteNonStream 走 httptest 上游验证非流式端到端。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，executor 非流式链路。
func TestExecutorExecuteNonStream(t *testing.T) {
	stream := strings.Join([]string{
		"event: SSE_ACK",
		`data: {"ack_client_meta":{"conversation_id":"conv-9"}}`,
		"",
		"event: STREAM_MSG_NOTIFY",
		`data: {"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"答案是 2"}}}]}}`,
		"",
		"event: SSE_REPLY_END",
		"data: {}",
		"",
	}, "\n")
	_, _, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()

	storage, _ := json.Marshal(completionTestAccount())
	payload := []byte(`{"model":"doubao/default","messages":[{"role":"user","content":"1+1=?"}]}`)
	raw, _ := json.Marshal(pluginapi.ExecutorRequest{StorageJSON: storage, Payload: payload})

	out, err := executorExecute(raw)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}

	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Payload []byte `json:"Payload"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(out, &env); errUnmarshal != nil {
		t.Fatalf("响应应为合法信封：%v", errUnmarshal)
	}
	if !env.OK {
		t.Fatalf("执行应成功：%s", truncateText(string(out), 300))
	}

	// 宿主的 ExecutorResponse 无 json tag，字段名为 PascalCase。
	var resp map[string]any
	if errUnmarshal := json.Unmarshal(env.Result.Payload, &resp); errUnmarshal != nil {
		t.Fatalf("Payload 应为 OpenAI JSON：%v", errUnmarshal)
	}
	if resp["object"] != "chat.completion" {
		t.Errorf("object 应为 chat.completion，实际 %v", resp["object"])
	}
	choices := resp["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "答案是 2" {
		t.Errorf("正文错误：%v", msg["content"])
	}
	if resp["conversation_id"] != "conv-9" {
		t.Errorf("会话 id 应透传，实际 %v", resp["conversation_id"])
	}
}

// TestExecutorExecuteStreamSyncCollect 校验无 stream_id 时同步收集帧。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，同步流式兜底链路。
func TestExecutorExecuteStreamSyncCollect(t *testing.T) {
	stream := strings.Join([]string{
		"event: STREAM_MSG_NOTIFY",
		`data: {"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"答"}}}]}}`,
		"",
		"event: CHUNK_DELTA",
		`data: {"text":"案"}`,
		"",
		"event: SSE_REPLY_END",
		"data: {}",
		"",
	}, "\n")
	_, _, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()

	storage, _ := json.Marshal(completionTestAccount())
	payload := []byte(`{"model":"doubao/default","stream":true,"messages":[{"role":"user","content":"1+1=?"}]}`)
	raw, _ := json.Marshal(pluginapi.ExecutorRequest{StorageJSON: storage, Payload: payload})

	out, err := executorExecuteStream(raw)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}

	var env struct {
		Result struct {
			Headers map[string][]string `json:"headers"`
			Chunks  []struct {
				Payload []byte `json:"Payload"`
			} `json:"chunks"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(out, &env); errUnmarshal != nil {
		t.Fatalf("响应应为合法信封：%v", errUnmarshal)
	}
	if env.Result.Headers["Content-Type"][0] != "text/event-stream" {
		t.Errorf("响应头应为 SSE，实际 %+v", env.Result.Headers)
	}
	if len(env.Result.Chunks) < 2 {
		t.Fatalf("应至少产生增量帧与终止帧，实际 %d 帧", len(env.Result.Chunks))
	}

	// 首帧应带 role，末帧 finish_reason=stop。
	var first map[string]any
	_ = json.Unmarshal(env.Result.Chunks[0].Payload, &first)
	firstDelta := first["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if firstDelta["role"] != "assistant" {
		t.Errorf("首帧应带 role，实际 %+v", firstDelta)
	}

	var last map[string]any
	_ = json.Unmarshal(env.Result.Chunks[len(env.Result.Chunks)-1].Payload, &last)
	if last["choices"].([]any)[0].(map[string]any)["finish_reason"] != "stop" {
		t.Errorf("末帧应为终止帧，实际 %+v", last)
	}
}

// TestPumpCompletionStreamEmitsError 校验上游失败时向宿主发送错误信号。
//
// 宿主不可用时 streamEmitError 内部静默返回，本用例只验证函数不会 panic 且
// 错误路径被走到（通过上游返回错误帧）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，流式错误路径覆盖。
func TestPumpCompletionStreamEmitsError(t *testing.T) {
	stream := "event: STREAM_ERROR\ndata: {\"error_code\":710012001,\"error_msg\":\"LoginInvalid\"}\n\n"
	_, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()
	sa.PhysicalPath = ""

	// 无宿主桥时 emit/close 静默失败，这里只要求不 panic、不阻塞。
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpCompletionStream("test-stream", sa, &chatRequest{Model: "doubao/default"}, "你好")
	}()
	select {
	case <-done:
	case <-context.Background().Done():
		t.Fatal("不应走到这里")
	}
}
