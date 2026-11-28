// sse_signature_test.go 锁定「首帧前观测」的结构签名与响应头白名单。
//
// 隐私红线优先：签名只能描述帧的**形态**（键名 + 枚举型标识），绝不能带出对话
// 内容；响应头只能取白名单里的调度信息，绝不能带出凭据类头。
package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestSseFrameSignature_KeysAndIdentifiers 锁定签名包含键名与标识字段。
func TestSseFrameSignature_KeysAndIdentifiers(t *testing.T) {
	sig := sseFrameSignature(`{"object":"chat.completion.chunk","type":"chunk","choices":[{"delta":{"content":"hi"}}]}`)
	for _, want := range []string{"keys=choices,object,type", "object=chat.completion.chunk", "type=chunk"} {
		if !strings.Contains(sig, want) {
			t.Fatalf("签名缺少 %q，实际 %q", want, sig)
		}
	}
}

// TestSseFrameSignature_NoContentLeak 锁定签名绝不带出对话内容。
func TestSseFrameSignature_NoContentLeak(t *testing.T) {
	secret := "用户私密内容-abc-123"
	sig := sseFrameSignature(`{"object":"chat.completion.chunk","choices":[{"delta":{"content":"` + secret + `"}}]}`)
	if strings.Contains(sig, secret) {
		t.Fatalf("签名泄漏了对话内容：%q", sig)
	}
	if strings.Contains(sig, "choices") == false {
		t.Fatalf("签名应保留键名：%q", sig)
	}
}

// TestSseLineSignature_EventAndDataLines 锁定各类 SSE 行的形态判定。
func TestSseLineSignature_EventAndDataLines(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"event: queued", "event-line:queued"},
		{"data: [DONE]", "data:[DONE]"},
		{"", "empty"},
		{"not json at all", "non-json"},
	}
	for _, tc := range cases {
		if got := sseLineSignature(tc.line); got != tc.want {
			t.Fatalf("sseLineSignature(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
	if got := sseLineSignature(`data: {"type":"chunk"}`); !strings.Contains(got, "type=chunk") {
		t.Fatalf("data 行应解析出帧签名，实际 %q", got)
	}
}

// TestUpstreamHeaderSummary_WhitelistOnly 锁定响应头白名单与凭据排除。
func TestUpstreamHeaderSummary_WhitelistOnly(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Request-Id", "req-123")
	h.Set("Retry-After", "7")
	h.Set("Ratelimit-Remaining-Tokens", "999")
	h.Set("Authorization", "Bearer sk-secret")
	h.Set("Cookie", "sid=secret")
	h.Set("Set-Cookie", "sid=secret")
	h.Set("Server", "qwen")

	got := upstreamHeaderSummary(h)
	lower := strings.ToLower(got)
	for _, want := range []string{"content-type=text/event-stream", "x-request-id=req-123", "retry-after=7", "ratelimit-remaining-tokens=999"} {
		if !strings.Contains(lower, want) {
			t.Fatalf("摘要缺少 %q，实际 %q", want, got)
		}
	}
	for _, banned := range []string{"sk-secret", "sid=secret", "authorization", "cookie"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("摘要泄漏了凭据类信息 %q：%q", banned, got)
		}
	}
}

// TestUpstreamHeaderSummary_Empty 锁定无响应头时返回空串。
func TestUpstreamHeaderSummary_Empty(t *testing.T) {
	if got := upstreamHeaderSummary(nil); got != "" {
		t.Fatalf("无响应头时必须返回空串，实际 %q", got)
	}
}

// TestSseUsageCollector_PreludeLimited 锁定首帧前只留有限帧且可导出摘要。
func TestSseUsageCollector_PreludeLimited(t *testing.T) {
	c := &sseUsageCollector{}
	for i := 0; i < 10; i++ {
		c.notePrelude(`{"type":"chunk","i":` + string(rune('0'+i)) + `}`)
	}
	if len(c.prelude) != ssePreludeLimit {
		t.Fatalf("prelude 帧数 = %d, want %d", len(c.prelude), ssePreludeLimit)
	}
	sig := c.preludeSignature()
	if !strings.Contains(sig, "1:keys=") || !strings.Contains(sig, "3:keys=") {
		t.Fatalf("摘要应含帧序号与键名，实际 %q", sig)
	}
}

// TestSseUsageCollector_PreludeEmpty 锁定无记录时摘要为空串。
func TestSseUsageCollector_PreludeEmpty(t *testing.T) {
	c := &sseUsageCollector{}
	if got := c.preludeSignature(); got != "" {
		t.Fatalf("无记录时摘要必须为空串，实际 %q", got)
	}
}
