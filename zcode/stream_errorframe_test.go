package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSSEErrorFrameTable 覆盖上游错误帧的四种形态与三种非错误帧。
// 200 承载的业务错误如果识别不出来，插件会把失败当成功并清掉失败状态。
func TestSSEErrorFrameTable(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "对象带 code 与 message",
			content: `{"error":{"code":"11128","message":"request illegal"}}`,
			want:    "11128: request illegal",
		},
		{
			name:    "对象只有 message",
			content: `{"error":{"message":"rate limited"}}`,
			want:    "rate limited",
		},
		{
			name:    "错误为纯字符串",
			content: `{"error":"insufficient credits"}`,
			want:    "insufficient credits",
		},
		{
			name:    "对象 message 为空",
			content: `{"error":{"code":"X"}}`,
			want:    "",
		},
		{
			name:    "正常增量帧",
			content: `{"type":"content_block_delta","delta":{"text":"hi"}}`,
			want:    "",
		},
		{
			name:    "error 为 null",
			content: `{"error":null}`,
			want:    "",
		},
		{
			name:    "非 JSON",
			content: `not json`,
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sseErrorFrame(tc.content); got != tc.want {
				t.Fatalf("sseErrorFrame(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

// TestPumpUpstreamStreamErrorFrameFails 断言 200 响应体内的错误帧被按失败收尾：
// 必须发错误帧、按失败上报、且不进入成功分支（成功分支会清掉失败状态）。
func TestPumpUpstreamStreamErrorFrameFails(t *testing.T) {
	prevEmit, prevClose, prevErr := streamEmitFn, streamCloseFn, streamEmitErrorFn
	t.Cleanup(func() { streamEmitFn, streamCloseFn, streamEmitErrorFn = prevEmit, prevClose, prevErr })

	var forwarded []string
	var errFrames []string
	streamEmitFn = func(_ string, payload []byte) error {
		forwarded = append(forwarded, string(payload))
		return nil
	}
	streamCloseFn = func(string) {}
	streamEmitErrorFn = func(_ string, msg string) {
		errFrames = append(errFrames, msg)
	}

	body := "data: {\"error\":{\"code\":\"11128\",\"message\":\"request illegal\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"should_not_reach\"}}\n"
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}

	pumpUpstreamStream(context.Background(), func() {}, "", resp, "auth-1", "label", "GLM-5.3", "glm-5.3", time.Now())

	if len(errFrames) != 1 {
		t.Fatalf("应发出 1 个错误帧，实际 %d 个：%v", len(errFrames), errFrames)
	}
	if !strings.Contains(errFrames[0], "11128") {
		t.Fatalf("错误帧应包含上游错误码，实际为 %q", errFrames[0])
	}
	for _, frame := range forwarded {
		if strings.Contains(frame, "should_not_reach") {
			t.Fatalf("错误帧之后的数据不应继续转发，实际转发了 %q", frame)
		}
		if strings.Contains(frame, "error") {
			t.Fatalf("错误帧本身不应作为正常数据转发，实际转发了 %q", frame)
		}
	}
}

// TestPumpUpstreamStreamErrorFrameRedacted 断言错误帧进入宿主前已脱敏。
func TestPumpUpstreamStreamErrorFrameRedacted(t *testing.T) {
	prevEmit, prevClose, prevErr := streamEmitFn, streamCloseFn, streamEmitErrorFn
	t.Cleanup(func() { streamEmitFn, streamCloseFn, streamEmitErrorFn = prevEmit, prevClose, prevErr })

	var errFrames []string
	streamEmitFn = func(string, []byte) error { return nil }
	streamCloseFn = func(string) {}
	streamEmitErrorFn = func(_ string, msg string) { errFrames = append(errFrames, msg) }

	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	body := "data: {\"error\":{\"message\":\"invalid token " + jwt + "\"}}\n"
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}

	pumpUpstreamStream(context.Background(), func() {}, "", resp, "auth-1", "label", "GLM-5.3", "glm-5.3", time.Now())

	if len(errFrames) != 1 {
		t.Fatalf("应发出 1 个错误帧，实际 %d 个", len(errFrames))
	}
	if strings.Contains(errFrames[0], jwt) {
		t.Fatalf("错误帧未脱敏，JWT 仍在：%q", errFrames[0])
	}
}

// TestPumpUpstreamStreamNormalStreamSucceeds 断言无错误帧的普通流不受影响，
// 防止错误帧识别把正常帧误判成失败。
func TestPumpUpstreamStreamNormalStreamSucceeds(t *testing.T) {
	prevEmit, prevClose, prevErr := streamEmitFn, streamCloseFn, streamEmitErrorFn
	t.Cleanup(func() { streamEmitFn, streamCloseFn, streamEmitErrorFn = prevEmit, prevClose, prevErr })

	var forwarded []string
	var errFrames []string
	streamEmitFn = func(_ string, payload []byte) error {
		forwarded = append(forwarded, string(payload))
		return nil
	}
	streamCloseFn = func(string) {}
	streamEmitErrorFn = func(_ string, msg string) { errFrames = append(errFrames, msg) }

	body := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hello\"}}\n" +
		"data: {\"type\":\"message_stop\"}\n"
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}

	pumpUpstreamStream(context.Background(), func() {}, "", resp, "auth-1", "label", "GLM-5.3", "glm-5.3", time.Now())

	if len(errFrames) != 0 {
		t.Fatalf("正常流不应发出错误帧，实际 %v", errFrames)
	}
	if len(forwarded) != 2 {
		t.Fatalf("正常流应转发 2 帧，实际 %d 帧：%v", len(forwarded), forwarded)
	}
}
