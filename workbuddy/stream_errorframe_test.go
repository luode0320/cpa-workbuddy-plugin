// stream_errorframe_test.go locks the HTTP-200 in-stream error-frame
// semantics: extraction, account-level rotation classification, and the
// aggregator fail-fast behaviour. Regression guard for the traework
// production evidence (stream 389/391 — quota failures carried inside a
// 200 SSE body, previously rotated by nobody on the async path).
package main

import (
	"strings"
	"testing"
)

// TestSSEErrorFrame 表驱动验证错误帧提取：对象形态（带/不带 code）、字符串
// 形态、error:null、正常 chunk、非 JSON 均不误报。
// 最近修改时间：2026-09-06 17:00:00；改动原因：锁定 sseErrorFrame 提取契约。
func TestSSEErrorFrame(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"object with code", `{"error":{"code":"14018","message":"Your requests have exceeded the quota."}}`, "14018: Your requests have exceeded the quota."},
		{"object without code", `{"error":{"message":"rate limited"}}`, "rate limited"},
		{"string form", `{"error":"insufficient credit"}`, "insufficient credit"},
		{"null error", `{"error":null}`, ""},
		{"normal chunk", `{"id":"1","choices":[{"index":0,"delta":{"content":"hi"}}]}`, ""},
		{"usage chunk", `{"usage":{"total_tokens":10}}`, ""},
		{"not json", `upstream exploded`, ""},
		{"empty message object", `{"error":{"code":"500"}}`, ""},
	}
	for _, tc := range cases {
		if got := sseErrorFrame(tc.content); got != tc.want {
			t.Fatalf("%s: sseErrorFrame = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestShouldRotateOnUpstreamErr 表驱动锁定换号判定边界：账号级 4xx/429 换号、
// 200 内错误帧按 body marker 分类、5xx/0/402 维持跨请求冷却不换号、
// 业务 400 不换号。
// 最近修改时间：2026-09-06 17:00:00；改动原因：锁定 200 错误帧的换号扩展边界。
func TestShouldRotateOnUpstreamErr(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"401 rotates", 401, "unauthorized", true},
		{"403 rotates", 403, "forbidden", true},
		{"404 rotates", 404, "not found", true},
		{"405 rotates", 405, "method not allowed", true},
		{"429 rotates", 429, "too many requests", true},
		{"200 quota frame rotates", 200, "upstream 200: quota exceeded for this account", true},
		{"200 credit frame rotates", 200, `upstream 200: {"error":{"message":"insufficient credit"}}`, true},
		{"200 rate-limit frame rotates", 200, "upstream 200: rate limit exceeded", true},
		// workbuddy 的 hardCreditMarkers 是精确短语体系（quota exceeded 等），
		// traework 式宽文案 "exceeded the quota" 不命中属预期保守行为（不误换号）。
		{"200 unmarked frame does not rotate", 200, "upstream 200: Your requests have exceeded the quota.", false},
		{"200 unknown frame does not rotate", 200, "upstream 200: invalid message format", false},
		{"500 stays cooldown-only", 500, "internal error", false},
		{"0 transport stays cooldown-only", 0, "http_error: dial tcp", false},
		{"402 stays cooldown-only", 402, "payment required", false},
		{"400 business stays", 400, "bad request", false},
	}
	for _, tc := range cases {
		if got := shouldRotateOnUpstreamErr(tc.status, tc.body); got != tc.want {
			t.Fatalf("%s: shouldRotateOnUpstreamErr(%d, %q) = %v, want %v", tc.name, tc.status, tc.body, got, tc.want)
		}
	}
}

// TestAggregateSSEWithCollectorErrorFrame 验证同步聚合在 200 内错误帧上
// fail-fast：错误以 canonical "upstream 200:" 前缀返回，错误帧本身与帧后
// 内容不进入 chunks。
// 最近修改时间：2026-09-06 17:00:00；改动原因：锁定 collectUpstreamStream 路径的错误帧检测。
func TestAggregateSSEWithCollectorErrorFrame(t *testing.T) {
	sse := "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n" +
		"data: {\"error\":{\"code\":\"14018\",\"message\":\"Your requests have exceeded the quota.\"}}\n\n" +
		"data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"never\"}}]}\n\n"
	collector := &sseUsageCollector{}
	_, err := aggregateSSEWithCollector(strings.NewReader(sse), 200, false, collector)
	if err == nil {
		t.Fatal("expected error frame to abort collection")
	}
	if !strings.Contains(err.Error(), "upstream 200:") || !strings.Contains(err.Error(), "exceeded the quota") {
		t.Fatalf("error = %v, want upstream 200 prefixed quota message", err)
	}
}

// TestAggregateCompletionErrorFrame 验证非流式聚合在 200 内错误帧上返回
// canonical 错误而不是拼出一个静默空 completion。
// 最近修改时间：2026-09-06 17:00:00；改动原因：锁定 doExecuteOnce 路径的错误帧检测。
func TestAggregateCompletionErrorFrame(t *testing.T) {
	sse := "data: {\"error\":{\"message\":\"Your requests have exceeded the quota.\"}}\n\ndata: [DONE]\n\n"
	_, err := aggregateCompletion(strings.NewReader(sse), "m", 200, nil)
	if err == nil {
		t.Fatal("expected error frame to fail the completion")
	}
	if !strings.Contains(err.Error(), "upstream 200:") || !strings.Contains(err.Error(), "exceeded the quota") {
		t.Fatalf("error = %v, want upstream 200 prefixed quota message", err)
	}
}

// TestAggregateCompletionHealthyWithoutErrorFrame 验证正常流不受影响：
// delta 中含 "error" 字样的正文内容不会被误判为错误帧。
// 最近修改时间：2026-09-06 17:00:00；改动原因：锁定热路径零行为变化。
func TestAggregateCompletionHealthyWithoutErrorFrame(t *testing.T) {
	sse := "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"the word error appears here\"}}]}\n\ndata: [DONE]\n\n"
	out, err := aggregateCompletion(strings.NewReader(sse), "m", 200, nil)
	if err != nil {
		t.Fatalf("healthy stream must not error: %v", err)
	}
	if !strings.Contains(string(out), "error appears here") {
		t.Fatalf("content mangled: %s", out)
	}
}
