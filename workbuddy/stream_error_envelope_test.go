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
