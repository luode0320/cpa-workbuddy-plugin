package main

// 跨平台失败切换回归：终态错误必须经宿主 stream.emit 信封的 "error" 字段
// （宿主映射为 chunk.Err）发出，绝不能当 payload 数据帧发出。否则宿主
// conductor 视请求为成功，workbuddy 全部账号失败后宿主不会切换 traework 账号。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalStreamErrorEnvelopeUsesErrorField(t *testing.T) {
	raw, err := marshalStreamErrorEnvelope("stream-1", "upstream account pool exhausted after 2 attempt(s)")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var errMsg string
	if err := json.Unmarshal(probe["error"], &errMsg); err != nil {
		t.Fatalf("error field must be a string: %v", err)
	}
	if errMsg != "upstream account pool exhausted after 2 attempt(s)" {
		t.Fatalf("error = %q, want exhausted message", errMsg)
	}
	if _, ok := probe["payload"]; ok {
		t.Fatalf("payload key present: error must travel as chunk.Err, not as a data frame: %s", raw)
	}
	var streamID string
	if err := json.Unmarshal(probe["stream_id"], &streamID); err != nil || streamID != "stream-1" {
		t.Fatalf("stream_id = %q err=%v, want stream-1", streamID, err)
	}
}

// TestMarshalStreamErrorEnvelopeCarriesResetStatus 锁定断流失败的信封状态码：
// 上游读取中断必须以 502 交给宿主（网关类失败），已泄漏断流与普通错误
// 不携带该字段，避免宿主对同一请求重试造成重复输出。
func TestMarshalStreamErrorEnvelopeCarriesResetStatus(t *testing.T) {
	// 1. 断流错误 → http_status=502
	resetMsg := "upstream stream read error: read tcp 10.0.0.1:443: connection reset by peer"
	raw, err := marshalStreamErrorEnvelope("stream-r", resetMsg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var status int
	if err := json.Unmarshal(probe["http_status"], &status); err != nil {
		t.Fatalf("http_status must be present for stream read errors: %v (raw=%s)", err, raw)
	}
	if status != 502 {
		t.Fatalf("stream read error http_status = %d, want 502", status)
	}
	var errMsg string
	if err := json.Unmarshal(probe["error"], &errMsg); err != nil || errMsg != resetMsg {
		t.Fatalf("error = %q err=%v, want reset message", errMsg, err)
	}
	if _, ok := probe["payload"]; ok {
		t.Fatalf("payload key present: %s", raw)
	}

	// 2. 已泄漏断流：带泄漏标记的文本不得携带 http_status
	leakedMsg := "upstream stream read error: connection reset by peer (" + streamReadErrorLeakedMarker + ")"
	leaked, err := marshalStreamErrorEnvelope("stream-l", leakedMsg)
	if err != nil {
		t.Fatalf("marshal leaked: %v", err)
	}
	if strings.Contains(string(leaked), "http_status") {
		t.Fatalf("leaked stream read error must omit http_status: %s", leaked)
	}

	// 3. 普通错误 → 不携带 http_status
	plain, err := marshalStreamErrorEnvelope("stream-p", "some other failure")
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	if strings.Contains(string(plain), "http_status") {
		t.Fatalf("non-classified error must omit http_status: %s", plain)
	}
}

// TestErrorEnvelopeCarriesChannelBlockStatus 锁定非流式失败信封的状态码通道。
//
// 生产缺陷回归（2026-11-08）：envelopeError 只有 code + message，没有
// http_status 字段，宿主 statusCodeFromError 恒得 0，isRequestInvalidError
// 判否，于是逐个凭据重试——实测单请求扇出 17 个账号。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 19:30:00；改动原因：新增，堵住非流式路径缺状态码的回归。
func TestErrorEnvelopeCarriesChannelBlockStatus(t *testing.T) {
	blocked := `upstream 400: {"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`
	raw := errorEnvelope("plugin_error", blocked)
	var probe struct {
		OK    bool `json:"ok"`
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			HTTPStatus int    `json:"http_status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal envelope: %v (raw=%s)", err, raw)
	}
	if probe.OK {
		t.Fatal("failure envelope must not be ok")
	}
	if probe.Error.HTTPStatus != httpStatusBadRequest {
		t.Fatalf("http_status = %d, want %d", probe.Error.HTTPStatus, httpStatusBadRequest)
	}

	// 普通失败不得携带状态码，避免改变既有分类。
	plain := errorEnvelope("plugin_error", "upstream 500: internal error")
	if strings.Contains(string(plain), "http_status") {
		t.Fatalf("plain failure must omit http_status: %s", plain)
	}
}

// TestMarshalStreamErrorEnvelopeChannelBlockJSON 锁定流式通道的上游拦截承载方式。
//
// 宿主流式 emit 只传纯文本 error，取不到 http_status；请求级判定因此必须靠
// 错误文本本身是合法 JSON 且 error.code 命中宿主白名单。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 15:40:00；改动原因：新增，流式路径缺状态码通道。
func TestMarshalStreamErrorEnvelopeChannelBlockJSON(t *testing.T) {
	blocked := `upstream 400: {"code":11128,"msg":"Illegal API invocation from an unapproved channel","retry_policy":{"retry":false}}`
	raw, err := marshalStreamErrorEnvelope("stream-cb", blocked)
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
	// 错误文本本身必须是合法 JSON，且 error.code 命中宿主请求级白名单。
	if !json.Valid([]byte(errText)) {
		t.Fatalf("channel block error text must be valid JSON: %s", errText)
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
	if !strings.Contains(parsed.Error.Message, "11128") {
		t.Fatalf("message must keep the upstream code for triage: %q", parsed.Error.Message)
	}

	// 普通错误仍以纯文本发送，不得被包装成请求级故障。
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
		t.Fatalf("non-block error must stay plain text: %s", plainText)
	}
}

// TestEnvelopeHTTPStatusForChannelBlock 锁定上游拦截（11128）的信封状态码。
//
// 生产缺陷回归（2026-11-08）：渠道级拦截在插件内已停止换号，但信封不带
// 状态码时宿主 statusCodeFromError 恒得 0，isRequestInvalidError 判否，
// 于是逐个凭据重试——实测单请求扇出 17 个账号、3 小时 583 次失败。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 14:45:00；改动原因：新增，堵住 11128 跨凭据扇出的回归。
func TestEnvelopeHTTPStatusForChannelBlock(t *testing.T) {
	blocked := `upstream 400: {"code":11128,"msg":"Illegal API invocation from an unapproved channel","retry_policy":{"retry":false}}`
	if got := envelopeHTTPStatusForError(blocked); got != httpStatusBadRequest {
		t.Fatalf("channel block http_status = %d, want %d", got, httpStatusBadRequest)
	}
	// 账号级封禁 11140 不走该通道：宿主对 400 不轮转，仍由插件记账。
	ban := `upstream 403: {"code":11140,"msg":"Your account has been restricted"}`
	if got := envelopeHTTPStatusForError(ban); got == httpStatusBadRequest {
		t.Fatalf("11140 must not be treated as channel block, got %d", got)
	}
	// 普通业务错误保持无状态码。
	if got := envelopeHTTPStatusForError("upstream 400: some other failure"); got != 0 {
		t.Fatalf("plain 400 error http_status = %d, want 0", got)
	}
}
