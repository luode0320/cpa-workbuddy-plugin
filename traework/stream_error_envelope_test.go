package main

// 跨平台失败切换回归：终态错误必须经宿主 stream.emit 信封的 "error" 字段
// （宿主映射为 chunk.Err）发出，绝不能当 payload 数据帧发出。
// 背景：旧实现把 {"error": ...} 当 payload 发送，宿主 conductor 视请求为
// 成功——traework 全部账号失败后（如 "upstream account pool exhausted"），
// 宿主不轮换凭据、不切换 workbuddy 账号，客户端直接收到失败。

import (
	"encoding/json"
	"testing"
)

func TestMarshalStreamErrorEnvelopeUsesErrorField(t *testing.T) {
	raw, err := marshalStreamErrorEnvelope("stream-1", "upstream account pool exhausted after 2 attempt(s)")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m["stream_id"]; got != "stream-1" {
		t.Fatalf("stream_id = %v, want stream-1", got)
	}
	if got := m["error"]; got != "upstream account pool exhausted after 2 attempt(s)" {
		t.Fatalf("error = %v, want exhausted message", got)
	}
	if _, ok := m["payload"]; ok {
		t.Fatalf("envelope must not carry a payload field: error must travel as chunk.Err, not as a data frame")
	}
}

// 哨兵：若有人把错误改回 payload 数据帧，本测试通过 marshal 输出含 "payload"
// 键而失败，证明错误通道语义被破坏。
func TestMarshalStreamErrorEnvelopeNeverWrapsInPayload(t *testing.T) {
	raw, err := marshalStreamErrorEnvelope("s", "boom")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if json.Valid(raw) == false {
		t.Fatalf("envelope must be valid JSON")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := probe["payload"]; ok {
		t.Fatalf("payload key present: %s", raw)
	}
}

func TestEmitTraeAsyncErrorUsesErrorChannelAndCloses(t *testing.T) {
	var (
		gotStreamID string
		gotMessage  string
		closed      []string
	)
	deps := traeAsyncStreamDeps{
		EmitError: func(streamID, message string) error {
			gotStreamID = streamID
			gotMessage = message
			return nil
		},
		Close: func(streamID string) { closed = append(closed, streamID) },
	}
	emitTraeAsyncError("stream-9", "pool exhausted", deps)
	if gotStreamID != "stream-9" || gotMessage != "pool exhausted" {
		t.Fatalf("emit via wrong channel: stream_id=%q message=%q", gotStreamID, gotMessage)
	}
	if len(closed) != 1 || closed[0] != "stream-9" {
		t.Fatalf("close calls = %v, want exactly one for stream-9", closed)
	}
}
