package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// TestGenerateSign 用独立算法计算期望签名，防止签名算法被改坏仍通过。
func TestGenerateSign(t *testing.T) {
	secret := "test-secret"
	nonce := "abc123"
	got := generateSign(secret, 1700000000123, nonce)

	stamp := "1700000000123"
	digits := []byte(stamp)
	sum := 0
	for _, d := range digits {
		sum += int(d - '0')
	}
	sum -= int(digits[len(digits)-2] - '0')
	check := sum % 10
	wantTS := string(digits[:len(digits)-2]) + string(rune('0'+check)) + string(digits[len(digits)-1:])
	if got.Timestamp != wantTS {
		t.Fatalf("timestamp mismatch: got %s want %s", got.Timestamp, wantTS)
	}
	digest := md5.Sum([]byte(wantTS + "-" + nonce + "-" + secret))
	wantSign := hex.EncodeToString(digest[:])
	if got.Sign != wantSign {
		t.Fatalf("sign mismatch: got %s want %s", got.Sign, wantSign)
	}
}

// TestSSEDeltaDedup 验证全量 parts 快照增量去重：重复帧不再产生重复文本。
func TestSSEDeltaDedup(t *testing.T) {
	frame := glmStreamFrame{Parts: []streamPart{{Content: []streamContent{
		{Type: "text", Text: "你好"},
	}}}}
	text1, _, n, _ := parseGLMFrame(frame, 0, 0)
	if text1 != "你好" || n != len("你好") {
		t.Fatalf("first delta mismatch: %q %d", text1, n)
	}
	text2, _, _, _ := parseGLMFrame(frame, n, 0)
	if text2 != "" {
		t.Fatalf("duplicate frame must not emit delta, got %q", text2)
	}
}

// TestConvertNonStreamDedupsSnapshots 验证非流式聚合不会因多帧全量快照而重复回答。
// 上游每一帧都发完整 parts 快照，实测 3 帧曾被拼成「收到收到收到」。
// 最近修改时间：2026-11-29 新增, 锁定非流式重复缺陷。
func TestConvertNonStreamDedupsSnapshots(t *testing.T) {
	frame := `{"conversation_id":"c1","parts":[{"role":"assistant","status":"finish","content":[{"type":"text","text":"收到"}]}]}`
	raw := []byte("data: " + frame + "\n\n" +
		"data: " + frame + "\n\n" +
		"data: " + frame + "\n\n")

	out := convertNonStream(raw, "glm/glm-5.3")
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("want 1 choice, got %d", len(resp.Choices))
	}
	if got := resp.Choices[0].Message.Content; got != "收到" {
		t.Fatalf("repeated snapshots must not duplicate content, got %q", got)
	}
}

// TestConvertNonStreamAccumulatesGrowingSnapshots 验证快照确实增长时仍能正确累积。
// 最近修改时间：2026-11-29 新增, 快照增量累积正确性。
func TestConvertNonStreamAccumulatesGrowingSnapshots(t *testing.T) {
	mk := func(text string) string {
		return `{"conversation_id":"c1","parts":[{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}]}`
	}
	raw := []byte("data: " + mk("你") + "\n\n" +
		"data: " + mk("你好") + "\n\n" +
		"data: " + mk("你好世界") + "\n\n")

	out := convertNonStream(raw, "glm/glm-5.3")
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := resp.Choices[0].Message.Content; got != "你好世界" {
		t.Fatalf("got %q want 你好世界", got)
	}
}

// TestSSEReasoningSplit 验证思考与正文分离。
func TestSSEReasoningSplit(t *testing.T) {
	frame := glmStreamFrame{Parts: []streamPart{{Content: []streamContent{
		{Type: "think", Think: "让我想想"},
		{Type: "text", Text: "答案"},
	}}}}
	text, reasoning, _, _ := parseGLMFrame(frame, 0, 0)
	if text != "答案" || reasoning != "让我想想" {
		t.Fatalf("split mismatch: text=%q reasoning=%q", text, reasoning)
	}
}

// TestDoneTerminatesImmediately 验证终止标记被识别。
func TestDoneTerminatesImmediately(t *testing.T) {
	cases := []string{"data: [DONE]", "event: done", "event: finish"}
	for _, c := range cases {
		if !isStreamDoneLine(c) {
			t.Fatalf("%q should be a done marker", c)
		}
	}
	if isStreamDoneLine("data: {\"status\":\"processing\"}") {
		t.Fatal("normal frame must not be done marker")
	}
}

// TestEventErrorEarlyExit 验证 HTTP 200 下的业务错误帧被识别。
func TestEventErrorEarlyExit(t *testing.T) {
	msg := sseErrorFrame(`{"error":{"code":"40102","message":"token expired"}}`)
	if msg == "" || msg[:5] != "40102" {
		t.Fatalf("error frame not detected: %q", msg)
	}
	if sseErrorFrame(`{"conversation_id":"x","status":"finish"}`) != "" {
		t.Fatal("normal frame must not be treated as error")
	}
}

// TestPrepareStreamRequestUsesTokenDeviceID 验证对话请求的 X-Device-Id 取自 access_token 载荷，
// 随机设备标识会被上游判 40004。
func TestPrepareStreamRequestUsesTokenDeviceID(t *testing.T) {
	cfg := defaultConfig()
	cfg.StreamEndpoint = "https://example.invalid/stream"
	access := fakeToken("chat-device", "access")
	req, err := prepareStreamRequest(context.Background(), []byte("{}"), cfg, access)
	if err != nil {
		t.Fatalf("prepareStreamRequest failed: %v", err)
	}
	if got := req.Header.Get("X-Device-Id"); got != "chat-device" {
		t.Fatalf("X-Device-Id = %q, want chat-device", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+access {
		t.Fatalf("Authorization = %q", got)
	}
	if req.Header.Get("X-Sign") == "" || req.Header.Get("X-Timestamp") == "" {
		t.Fatal("missing sign headers")
	}
}

// TestPrepareStreamRequestRejectsTokenWithoutDeviceID 验证缺 device_id 的令牌在发请求前即被拒。
func TestPrepareStreamRequestRejectsTokenWithoutDeviceID(t *testing.T) {
	cfg := defaultConfig()
	cfg.StreamEndpoint = "https://example.invalid/stream"
	if _, err := prepareStreamRequest(context.Background(), []byte("{}"), cfg, "not-a-jwt"); err == nil {
		t.Fatal("expected error for access token without device_id")
	}
}
