// affinity_test.go 锁定「亲和类客户端归因头必须是会话级恒定」这条契约。
//
// 背景（2026-11-28 生产取证）：同一账号 + 同一会话 + 99.8% 前缀缓存命中，首字
// 仍在 5.9s~23.2s 之间大幅波动，而上下文规模、账号、会话、缓存命中四个维度与
// 首字均无相关性。波动只能来自「这次请求有没有落到持有该上下文的上游实例」。
// 官方桌面客户端把 x-mm-client-request-id / x-mm-client-trace-id / x-mm-request-set
// 做成**每 turn 恒定**，本插件此前是每请求随机 —— 这就是插件侧的 bug。
package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestMaasAffinityHeaders_SessionScoped 锁定同一会话派生出的亲和头恒定。
func TestMaasAffinityHeaders_SessionScoped(t *testing.T) {
	const sid = "11111111-2222-4333-8444-555555555555"
	a1 := maasAffinityHeaders(sid)
	a2 := maasAffinityHeaders(sid)
	if a1.requestID != a2.requestID {
		t.Fatalf("x-mm-client-request-id 必须会话级恒定，got %q vs %q", a1.requestID, a2.requestID)
	}
	if a1.traceID != a2.traceID {
		t.Fatalf("x-mm-client-trace-id 必须会话级恒定，got %q vs %q", a1.traceID, a2.traceID)
	}
	if a1.requestSet != a2.requestSet {
		t.Fatalf("x-mm-request-set 必须会话级恒定，got %q vs %q", a1.requestSet, a2.requestSet)
	}
	if a1.requestID != sid || a1.requestSet != sid {
		t.Fatalf("request-id / request-set 必须绑定上游会话标识，got %q / %q", a1.requestID, a1.requestSet)
	}
	if len(a1.traceID) != 32 {
		t.Fatalf("trace-id 必须是 32 位 hex，got %q", a1.traceID)
	}

	// 不同会话必须派生不同取值（否则不同对话会粘到同一实例）。
	other := maasAffinityHeaders("99999999-8888-4777-8666-555555555555")
	if other.requestID == a1.requestID || other.traceID == a1.traceID {
		t.Fatal("不同会话必须派生不同的亲和头")
	}
}

// TestMaasAffinityHeaders_EmptySessionFallsBack 锁定无会话标识时退回随机。
func TestMaasAffinityHeaders_EmptySessionFallsBack(t *testing.T) {
	a := maasAffinityHeaders("")
	if a.requestID == "" || a.traceID == "" {
		t.Fatalf("无会话标识时必须仍有可用取值，got %+v", a)
	}
	if a.requestSet != "" || a.traceparent != "" {
		t.Fatalf("无会话标识时不得伪造 request-set / traceparent，got %+v", a)
	}
	b := maasAffinityHeaders("")
	if a.requestID == b.requestID {
		t.Fatal("无会话标识时必须退回每请求随机")
	}
}

// TestApplyMaasHeaders_AffinityHeadersAreSessionScoped 锁定出站请求头行为。
func TestApplyMaasHeaders_AffinityHeadersAreSessionScoped(t *testing.T) {
	const sid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	sess := &maasSession{AccessKey: "sk-mm-v1.test", SessionID: sid}

	newReq := func() *http.Request {
		req, err := http.NewRequest(http.MethodPost, "https://maas.example/v1/chat/completions", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		applyMaasHeaders(req, sess)
		return req
	}
	r1 := newReq()
	r2 := newReq()

	for _, h := range []string{"x-mm-client-request-id", "x-mm-client-trace-id", "x-mm-request-set", "x-session-id"} {
		if r1.Header.Get(h) != r2.Header.Get(h) {
			t.Fatalf("%s 必须会话级恒定，got %q vs %q", h, r1.Header.Get(h), r2.Header.Get(h))
		}
	}
	if r1.Header.Get("x-session-id") != sid {
		t.Fatalf("x-session-id 必须绑定票据的上游会话，got %q", r1.Header.Get("x-session-id"))
	}
	if r1.Header.Get("x-mm-request-set") == "" {
		t.Fatal("x-mm-request-set 必须发出（官方客户端同一任务的所有调用共用它）")
	}

	// traceparent：trace 段恒定、span 段每请求唯一。
	tp1 := r1.Header.Get("traceparent")
	tp2 := r2.Header.Get("traceparent")
	if tp1 == "" || tp2 == "" {
		t.Fatal("traceparent 必须发出")
	}
	p1 := strings.Split(tp1, "-")
	p2 := strings.Split(tp2, "-")
	if len(p1) != 4 || len(p2) != 4 {
		t.Fatalf("traceparent 必须是 OTel 四段格式，got %q", tp1)
	}
	if p1[1] != p2[1] {
		t.Fatalf("traceparent 的 trace 段必须会话级恒定，got %q vs %q", p1[1], p2[1])
	}
	if p1[2] == p2[2] {
		t.Fatal("traceparent 的 span 段必须每请求唯一")
	}
}

// TestApplyMaasHeaders_NoSessionIDKeepsAttribution 锁定无会话标识时仍发归因头。
func TestApplyMaasHeaders_NoSessionIDKeepsAttribution(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://maas.example/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	applyMaasHeaders(req, &maasSession{AccessKey: "sk-mm-v1.test"})
	if req.Header.Get("x-session-id") != "" {
		t.Fatalf("无会话标识时不得伪造 x-session-id，got %q", req.Header.Get("x-session-id"))
	}
	if req.Header.Get("x-mm-client-request-id") == "" {
		t.Fatal("x-mm-client-request-id 必须发出")
	}
	if req.Header.Get("x-mm-client-trace-id") == "" {
		t.Fatal("x-mm-client-trace-id 必须发出")
	}
}
