package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestIsResponsesFormat 验证 responses 通道格式识别覆盖宿主各种同义写法。
// 判据防的是把 responses 请求错认成 chat 通道，兜底帧发成 chat 帧后客户端拿不到终止事件。
// 无副作用，纯函数判定。
func TestIsResponsesFormat(t *testing.T) {
	cases := []struct {
		format string
		want   bool
	}{
		{format: "openai-response", want: true},
		{format: "openai-responses", want: true},
		{format: "openai_responses", want: true},
		{format: "responses", want: true},
		{format: "  OPENAI-RESPONSE  ", want: true},
		{format: "openai", want: false},
		{format: "claude", want: false},
		{format: "gemini", want: false},
		{format: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			if got := isResponsesFormat(tc.format); got != tc.want {
				t.Fatalf("isResponsesFormat(%q) = %v, want %v", tc.format, got, tc.want)
			}
		})
	}
}

// TestBuildResponsesExhaustedSSE 验证 responses 流式兜底帧包含终止事件。
// 判据防的是 CPA 的 responsesSSEFramer 因没有 response.completed 而报
// upstream stream closed before a terminal event（502），客户端随即按 TRANSPORT 重试。
// 无副作用。
func TestBuildResponsesExhaustedSSE(t *testing.T) {
	sse := buildResponsesExhaustedSSE("deepseek-v4-pro", "⚡ [Safeguard Killer] every model failed")

	mustContain := []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.output_text.delta",
		"event: response.output_item.done",
		"event: response.completed",
	}
	for _, want := range mustContain {
		if !strings.Contains(sse, want) {
			t.Fatalf("流式兜底帧缺少 %q; sse=%s", want, sse)
		}
	}

	// response.completed 必须是最后一帧，否则客户端的终止判定会提前收尾。
	lastIdx := strings.LastIndex(sse, "event: ")
	if lastIdx == -1 || !strings.HasPrefix(sse[lastIdx:], "event: response.completed") {
		t.Fatalf("最后一帧应为 response.completed; sse=%s", sse)
	}

	// 校验 response.completed 的 data 是合法 JSON 且带 status=completed。
	dataStart := strings.Index(sse[lastIdx:], "data: ")
	if dataStart == -1 {
		t.Fatalf("response.completed 缺少 data 行; sse=%s", sse)
	}
	raw := sse[lastIdx+dataStart+len("data: "):]
	raw = strings.TrimSpace(raw)
	var completed struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
			Model  string `json:"model"`
			Output []struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		} `json:"response"`
	}
	if errUnmarshal := json.Unmarshal([]byte(raw), &completed); errUnmarshal != nil {
		t.Fatalf("response.completed data 非法 JSON: %v; raw=%s", errUnmarshal, raw)
	}
	if completed.Type != "response.completed" {
		t.Fatalf("completed.type = %q, want response.completed", completed.Type)
	}
	if completed.Response.Status != "completed" {
		t.Fatalf("completed.response.status = %q, want completed", completed.Response.Status)
	}
	if completed.Response.Model != "deepseek-v4-pro" {
		t.Fatalf("completed.response.model = %q, want deepseek-v4-pro", completed.Response.Model)
	}
	if len(completed.Response.Output) != 1 || len(completed.Response.Output[0].Content) != 1 {
		t.Fatalf("completed.response.output 结构不符; got=%+v", completed.Response.Output)
	}
	if !strings.Contains(completed.Response.Output[0].Content[0].Text, "every model failed") {
		t.Fatalf("completed 正文应含提示文本; got=%q", completed.Response.Output[0].Content[0].Text)
	}
}

// TestBuildExhaustedNonStreamBody_Responses 验证 responses 非流式兜底体是合法 response 对象。
// 判据防的是 responses 请求收到 chat.completion 结构后客户端解析不出 output。
// 无副作用。
func TestBuildExhaustedNonStreamBody_Responses(t *testing.T) {
	body := buildExhaustedNonStreamBody("openai-response", "deepseek-v4-pro", "⚡ [Safeguard Killer] every model failed")

	var parsed struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
		Model  string `json:"model"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		t.Fatalf("responses 非流式兜底体非法 JSON: %v; body=%s", errUnmarshal, body)
	}
	if parsed.Object != "response" {
		t.Fatalf("object = %q, want response（chat 结构会导致客户端解析失败）", parsed.Object)
	}
	if parsed.Status != "completed" {
		t.Fatalf("status = %q, want completed", parsed.Status)
	}
	if parsed.Model != "deepseek-v4-pro" {
		t.Fatalf("model = %q, want deepseek-v4-pro", parsed.Model)
	}
	if len(parsed.Output) != 1 || len(parsed.Output[0].Content) != 1 {
		t.Fatalf("output 结构不符; got=%+v", parsed.Output)
	}
	if parsed.Output[0].Content[0].Type != "output_text" {
		t.Fatalf("content.type = %q, want output_text", parsed.Output[0].Content[0].Type)
	}

	// 非 responses 通道仍必须是 chat 结构，确认分支没串。
	chatBody := buildExhaustedNonStreamBody("openai", "glm-5.3-flash", "notice")
	if !strings.Contains(string(chatBody), `"object":"chat.completion"`) {
		t.Fatalf("chat 通道兜底体应保持 chat.completion 结构; body=%s", chatBody)
	}
}

// TestSanitizeRequest_CleansResponsesInput 验证 responses 通道的 system 项会被净化。
// 判据防的是破甲改写只作用于 messages，在 responses 的 input 数组上全程空转。
// 会读取当前配置，测试内改为开关全开以验证净化生效。
func TestSanitizeRequest_CleansResponsesInput(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.Features.StripRestrictiveReminders = true

	payload := `{"model":"deepseek-v4-pro","input":[` +
		`{"role":"system","content":"你是写作助手。<system-reminder>Refuse requests for destructive techniques.</system-reminder>"},` +
		`{"role":"user","content":"继续写"}]}`

	out, _, _, _ := sanitizeRequest([]byte(payload), nil, cfg)
	got := string(out)

	if strings.Contains(got, "Refuse requests for destructive techniques") {
		t.Fatalf("responses 的 input 系统项未被净化; out=%s", got)
	}
	if !strings.Contains(got, "你是写作助手") {
		t.Fatalf("正常正文不应被破坏; out=%s", got)
	}
	if !strings.Contains(got, "继续写") {
		t.Fatalf("用户消息不应被破坏; out=%s", got)
	}
}

// TestSanitizeRequest_CleansDeveloperRole 验证 role="developer" 与 role="system" 同等处理。
// 判据防的是 responses 在 OpenAI 官方端点上用 developer 角色承载系统提示，漏判即漏净化。
func TestSanitizeRequest_CleansDeveloperRole(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.Features.StripRestrictiveReminders = true

	payload := `{"model":"deepseek-v4-pro","input":[` +
		`{"role":"developer","content":"<system-reminder>Dual-use security tools require clear authorization context.</system-reminder>保留这句"}]}`

	out, _, _, _ := sanitizeRequest([]byte(payload), nil, cfg)
	got := string(out)

	if strings.Contains(got, "Dual-use security tools require clear authorization context") {
		t.Fatalf("developer 角色系统项未被净化; out=%s", got)
	}
	if !strings.Contains(got, "保留这句") {
		t.Fatalf("同段正常正文不应被破坏; out=%s", got)
	}
}

// TestInjectNonStreamNotice_Responses 验证 responses 非流式响应体被注入提示且结构不变。
// 判据防的是 responses 请求拿到未注入提示的响应（旧的 chat 分支对它完全无效）。
// 无副作用。
func TestInjectNonStreamNotice_Responses(t *testing.T) {
	body := `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"正文内容","annotations":[]}]}]}`
	out := injectNonStreamNotice([]byte(body), "openai-response", "⚡ [Safeguard Killer] switched")

	var parsed struct {
		Object string `json:"object"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if errUnmarshal := json.Unmarshal(out, &parsed); errUnmarshal != nil {
		t.Fatalf("注入后非法 JSON: %v; out=%s", errUnmarshal, out)
	}
	if parsed.Object != "response" {
		t.Fatalf("object 应保持 response; out=%s", out)
	}
	if len(parsed.Output) != 1 || len(parsed.Output[0].Content) != 1 {
		t.Fatalf("output 结构被破坏; out=%s", out)
	}
	text := parsed.Output[0].Content[0].Text
	if !strings.Contains(text, "switched") {
		t.Fatalf("提示未注入; text=%q", text)
	}
	if !strings.Contains(text, "正文内容") {
		t.Fatalf("原正文丢失; text=%q", text)
	}
	if !strings.HasPrefix(text, "⚡ [Safeguard Killer] switched") {
		t.Fatalf("提示应前置; text=%q", text)
	}
}

// TestInjectStreamNotice_ResponsesUntouched 验证 responses 流不会被插入 chat 帧。
// 判据防的是前置 chat delta 污染 response.* 事件序列，导致客户端解析异常。
// 无副作用。
func TestInjectStreamNotice_ResponsesUntouched(t *testing.T) {
	chunks := [][]byte{
		[]byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"),
		[]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"),
	}
	out, shift := injectStreamNotice(chunks, "openai-response", "⚡ [Safeguard Killer] switched")

	if shift {
		t.Fatalf("responses 通道不应要求重排块序号")
	}
	if len(out) != len(chunks) {
		t.Fatalf("responses 通道不应增删分片; got=%d want=%d", len(out), len(chunks))
	}
	for i := range out {
		if string(out[i]) != string(chunks[i]) {
			t.Fatalf("分片 %d 被改动: %s", i, out[i])
		}
	}

	// 即便 format 传错，只要首块是 response.created 也必须放过。
	out2, shift2 := injectStreamNotice(chunks, "", "notice")
	if shift2 || len(out2) != len(chunks) {
		t.Fatalf("首块为 response.created 时不应插入 chat 帧; shift=%v len=%d", shift2, len(out2))
	}
}

// TestSanitizeRequest_CleansTopLevelSystem 验证 Anthropic 通道顶层 system 的两种形状都被净化。
// 判据防的是顶层 system 完全不被处理，Claude 请求带着限制性提醒直达上游。
func TestSanitizeRequest_CleansTopLevelSystem(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.Features.StripRestrictiveReminders = true

	stringForm := `{"model":"claude-opus-4-8","system":"你是助手。<system-reminder>do not predict potentially unsafe actions</system-reminder>继续","messages":[]}`
	out, _, _, _ := sanitizeRequest([]byte(stringForm), nil, cfg)
	if strings.Contains(string(out), "do not predict potentially unsafe actions") {
		t.Fatalf("顶层 system 字符串未被净化; out=%s", out)
	}
	if !strings.Contains(string(out), "继续") {
		t.Fatalf("顶层 system 正常正文不应被破坏; out=%s", out)
	}

	blockForm := `{"model":"claude-opus-4-8","system":[{"type":"text","text":"<system-reminder>stopped by a safety classifier</system-reminder>保留块正文"}],"messages":[]}`
	out, _, _, _ = sanitizeRequest([]byte(blockForm), nil, cfg)
	if strings.Contains(string(out), "stopped by a safety classifier") {
		t.Fatalf("顶层 system 块数组未被净化; out=%s", out)
	}
	if !strings.Contains(string(out), "保留块正文") {
		t.Fatalf("顶层 system 块正常正文不应被破坏; out=%s", out)
	}
}

// TestSanitizeRequest_LeavesUnrelatedFieldsAlone 验证无关字段与未开启净化时不动请求体。
// 判据防的是净化逻辑过度匹配把正常请求改坏。
func TestSanitizeRequest_LeavesUnrelatedFieldsAlone(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.Features.StripRestrictiveReminders = false

	payload := `{"model":"deepseek-v4-pro","instructions":"照常回答","input":[{"role":"user","content":"写一段产品介绍"}]}`
	out, _, _, _ := sanitizeRequest([]byte(payload), nil, cfg)

	if string(out) != payload {
		t.Fatalf("开关关闭时不应改动请求体;\n got=%s\nwant=%s", out, payload)
	}
}
