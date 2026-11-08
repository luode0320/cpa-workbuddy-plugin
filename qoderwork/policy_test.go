package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestIsRequestShapeFailure 验证「客户端请求形状错误」不被当成账号故障。
//
// 生产缺陷回归（2026-11-07）：发一次 role=wizard 的请求，上游返回
// provider_error，旧逻辑按账号级失败记账并逐个写盘禁用，一次请求打穿整池，
// 随后 5/5 次正常请求全部 503 auth_unavailable。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07 22:30:00；改动原因：补请求形状豁免的回归覆盖。
func TestIsRequestShapeFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "非法 role（生产实测原文）", body: "upstream 200: provider_error: wizard is not one of ['system', 'assistant', 'user']", want: true},
		{name: "参数类型错误", body: "invalid_parameter: max_tokens must be integer", want: true},
		{name: "缺必填字段", body: "missing required field: messages", want: true},
		{name: "tool call failed 包装", body: "upstream 200: tool call failed: invalid request", want: true},
		{name: "空正文不误判", body: "", want: false},
		{name: "空白正文不误判", body: "   ", want: false},
		{name: "模型级故障不归此类", body: "all backends failed", want: false},
		{name: "限流文案不归此类", body: "too many requests, retry_after 30", want: false},
		{name: "额度耗尽不归此类", body: "quota exceeded", want: false},
		{name: "普通 5xx 文本不归此类", body: "the model provider is temporarily unavailable", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRequestShapeFailure(tc.body); got != tc.want {
				t.Fatalf("isRequestShapeFailure(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestIsEntitlementRequiredFailure 验证「套餐无该模型权益」的识别。
//
// 生产缺陷回归（2026-11-08）：上游对无权益模型返回 HTTP 200 + 业务码 112 +
// pricingUrl，旧逻辑无判据，既不计账也不拦换号，宿主逐个凭据重试，
// 24h 内 294 次零 token 失败、单请求扇出 2~6 个账号。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 14:05:00；改动原因：新增，堵住 112 跨账号扇出的回归。
func TestIsEntitlementRequiredFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "生产实测原文", body: `upstream 200: 112: {"pricingUrl":"https://qoder.com.cn/pricing?client=qoder"}`, want: true},
		{name: "帧首业务码", body: `112: {"pricingUrl":"https://qoder.com/pricing?client=qoder"}`, want: true},
		{name: "仅 pricingUrl 字段", body: `{"pricingUrl":"https://qoder.com.cn/pricing"}`, want: true},
		{name: "大写字段名", body: `{"PricingUrl":"x"}`, want: true},
		{name: "空正文不误判", body: "", want: false},
		{name: "空白正文不误判", body: "   ", want: false},
		{name: "排队帧不归此类", body: `upstream 200: 403: 10605: {"isQueued":true}`, want: false},
		{name: "110 日上限不归此类", body: "upstream 200: 110: Billing daily count exceeded", want: false},
		{name: "额度不足不归此类", body: "upstream 200: 额度不足", want: false},
		{name: "裸 112 数字不误判", body: "the answer is 112 tokens", want: false},
		{name: "非 200 前缀不误判", body: "upstream 500: 112: internal", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEntitlementRequiredFailure(tc.body); got != tc.want {
				t.Fatalf("isEntitlementRequiredFailure(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestEntitlementFailureIsModelLevelAndNotRotated 锁定 112 的两个行为：
// 归入模型级失败（不记账号），且不允许同请求换号。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 14:05:00；改动原因：新增，锁定 112 的记账与换号边界。
func TestEntitlementFailureIsModelLevelAndNotRotated(t *testing.T) {
	const body = `upstream 200: 112: {"pricingUrl":"https://qoder.com.cn/pricing?client=qoder"}`
	if !isModelLevelFailure(body) {
		t.Fatal("112 权益缺失应归入模型级失败，不记在账号头上")
	}
	if shouldRotateOnUpstreamErr(200, body) {
		t.Fatal("112 权益缺失不应换号：换任何账号结果相同")
	}
	// 上游若把同一业务码包在 4xx 里，同样不得换号。
	if shouldRotateOnUpstreamErr(403, body) {
		t.Fatal("112 权益缺失带 403 时也不应换号")
	}
	if got := envelopeHTTPStatusForError(body); got != httpStatusBadRequest {
		t.Fatalf("envelopeHTTPStatusForError(112) = %d, want %d", got, httpStatusBadRequest)
	}
}

// TestMarshalStreamErrorEnvelopeEntitlementJSON 锁定流式通道的权益缺失承载方式。
//
// 宿主流式 emit 只传纯文本 error，取不到 http_status；请求级判定因此必须靠
// 错误文本本身是合法 JSON 且 error.code 命中宿主白名单。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 15:47:00；改动原因：新增，流式路径缺状态码通道。
func TestMarshalStreamErrorEnvelopeEntitlementJSON(t *testing.T) {
	const body = `upstream 200: 112: {"pricingUrl":"https://qoder.com.cn/pricing?client=qoder"}`
	raw, err := marshalStreamErrorEnvelope("stream-ent", body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var errText string
	if err := json.Unmarshal(probe["error"], &errText); err != nil {
		t.Fatalf("error field: %v (raw=%s)", err, raw)
	}
	if !json.Valid([]byte(errText)) {
		t.Fatalf("entitlement error text must be valid JSON: %s", errText)
	}
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(errText), &parsed); err != nil {
		t.Fatalf("parse wrapped error: %v", err)
	}
	if parsed.Error.Code != "invalid_request_error" {
		t.Fatalf("code = %q, want invalid_request_error", parsed.Error.Code)
	}
	if !strings.Contains(parsed.Error.Message, "112") {
		t.Fatalf("message must keep the business code: %q", parsed.Error.Message)
	}

	// 普通错误仍以纯文本发送。
	plain, err := marshalStreamErrorEnvelope("stream-pl", "upstream 500: internal error")
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	var plainProbe map[string]json.RawMessage
	if err := json.Unmarshal(plain, &plainProbe); err != nil {
		t.Fatalf("unmarshal plain: %v", err)
	}
	var plainText string
	_ = json.Unmarshal(plainProbe["error"], &plainText)
	if json.Valid([]byte(plainText)) {
		t.Fatalf("non-entitlement error must stay plain text: %s", plainText)
	}
}

// TestValidateOpenAIMessages 验证请求体 role 前置校验。
//
// 生产缺陷回归（2026-11-07）：上游对非法 role 的报错文案不固定（qoderwork 只回
// 无信息量的 provider_error），靠正文匹配拦不住；必须在本地入口拦掉，
// 否则一次畸形请求会把池内账号逐个写盘禁用。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-07 23:10:00；改动原因：补前置校验的回归覆盖。
func TestValidateOpenAIMessages(t *testing.T) {
	cases := []struct {
		name     string
		messages []openAIMessage
		wantErr  bool
	}{
		{name: "正常对话", messages: []openAIMessage{{Role: "system"}, {Role: "user"}}},
		{name: "工具对话", messages: []openAIMessage{{Role: "user"}, {Role: "assistant"}, {Role: "tool"}}},
		{name: "developer 角色", messages: []openAIMessage{{Role: "developer"}, {Role: "user"}}},
		{name: "非法 role wizard（生产实测）", messages: []openAIMessage{{Role: "user"}, {Role: "wizard"}}, wantErr: true},
		{name: "空 role", messages: []openAIMessage{{Role: ""}}, wantErr: true},
		{name: "空列表放行", messages: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOpenAIMessages(tc.messages)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateOpenAIMessages() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestOpenAIRequest_MalformedMaxTokensDoesNotFailParse 锁定「畸形数值不让整包解析失败」。
//
// 生产缺陷回归（2026-11-08）：客户端发 `{"max_tokens":"abc"}` 时，int64 强类型让
// `json.Unmarshal` 整体失败，宿主把插件返回的解析错误当成账号故障并遍历整池，
// 最后合成 503。修复后此类畸形值降级为 0（=未声明），请求照常处理。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 01:00:00；改动原因：新增，堵住畸形数值打穿整池的回归。
func TestOpenAIRequest_MalformedMaxTokensDoesNotFailParse(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int64
	}{
		{name: "正常数字", raw: `{"max_tokens":2000}`, want: 2000},
		{name: "数字字符串", raw: `{"max_tokens":"2000"}`, want: 2000},
		{name: "非数字字符串（生产实测）", raw: `{"max_tokens":"abc"}`, want: 0},
		{name: "null", raw: `{"max_tokens":null}`, want: 0},
		{name: "对象", raw: `{"max_tokens":{"a":1}}`, want: 0},
		{name: "布尔", raw: `{"max_tokens":true}`, want: 0},
		{name: "负数", raw: `{"max_tokens":-5}`, want: 0},
		{name: "小数", raw: `{"max_tokens":10.5}`, want: 0},
		{name: "缺省", raw: `{}`, want: 0},
		{name: "别名同样容错", raw: `{"max_completion_tokens":"xyz"}`, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req openAIRequest
			if err := json.Unmarshal([]byte(tc.raw), &req); err != nil {
				t.Fatalf("json.Unmarshal 不应因 max_tokens 畸形而失败: %v (raw=%s)", err, tc.raw)
			}
			if got := effectiveMaxTokens(&req); got != tc.want {
				t.Fatalf("effectiveMaxTokens() = %d, want %d (raw=%s)", got, tc.want, tc.raw)
			}
		})
	}
}

// TestOpenAIRequest_MalformedStreamDoesNotFailParse 锁定「stream 畸形不整包失败」。
//
// 生产缺陷回归（2026-11-08）：`stream` 用 bool 强类型时，客户端发
// `{"stream":"yes"}` 会让整个 json.Unmarshal 失败，宿主把解析错误当成账号故障
// 并遍历整池（实测同一 requestId 扇出 5 个账号）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 01:30:00；改动原因：新增，堵住 stream 畸形打穿整池的回归。
func TestOpenAIRequest_MalformedStreamDoesNotFailParse(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "正常 true", raw: `{"stream":true}`, want: true},
		{name: "字符串 yes（生产实测）", raw: `{"stream":"yes"}`, want: false},
		{name: "字符串 true", raw: `{"stream":"true"}`, want: true},
		{name: "数字 1", raw: `{"stream":1}`, want: true},
		{name: "数字 0", raw: `{"stream":0}`, want: false},
		{name: "null", raw: `{"stream":null}`, want: false},
		{name: "对象", raw: `{"stream":{}}`, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req openAIRequest
			if err := json.Unmarshal([]byte(tc.raw), &req); err != nil {
				t.Fatalf("json.Unmarshal 不应因 stream 畸形而失败: %v (raw=%s)", err, tc.raw)
			}
			if got := req.Stream.Bool(); got != tc.want {
				t.Fatalf("Stream = %v, want %v (raw=%s)", got, tc.want, tc.raw)
			}
		})
	}
}
