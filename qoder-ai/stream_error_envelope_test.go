package main

// 跨平台失败切换回归：终态错误必须经宿主 stream.emit 信封的 "error" 字段
// （宿主映射为 chunk.Err）发出，绝不能当 payload 数据帧发出。否则宿主
// conductor 视请求为成功，qoderwork 全部账号失败后宿主不会切换其他平台账号。

import (
	"encoding/json"
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
