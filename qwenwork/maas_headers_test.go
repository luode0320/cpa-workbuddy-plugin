// maas_headers_test.go 锁定出站 MaaS 请求头的会话绑定与客户端归因，
// 以及票据缓存的逐会话分片与淘汰。
//
// 为什么必须发这些头：官方桌面客户端的出站头全集里带 x-session-id 与
// x-mm-* 归因头（逆向证据：D:\qwork\QwenWorkCN\2.2.1-26112604\resources\
// app.asar → out/main/main.js 的 buildAuthenticatedUpstreamHeaders /
// buildUpstreamHeaders）。本插件此前只发 Authorization，票据与上游会话之间
// 没有绑定，请求退化成无归属流量——生产表现为上游排队、官方客户端不排队。
package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestApplyMaasHeaders_CarriesSessionBinding 锁定会话绑定头与客户端归因头。
func TestApplyMaasHeaders_CarriesSessionBinding(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://maas.example/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	sess := &maasSession{
		AccessKey: "sk-mm-v1.test",
		BaseURL:   "https://maas.example",
		SessionID: "11111111-2222-4333-8444-555555555555",
	}
	applyMaasHeaders(req, sess)

	if got := req.Header.Get("x-session-id"); got != sess.SessionID {
		t.Fatalf("x-session-id 必须绑定票据对应的上游会话，got %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+sess.AccessKey {
		t.Fatalf("Authorization 错误：got %q", got)
	}
	if got := req.Header.Get("x-mm-client-request-source"); got != maasClientRequestSource {
		t.Fatalf("x-mm-client-request-source 错误：got %q", got)
	}
	if req.Header.Get("x-mm-device-type") == "" {
		t.Fatal("x-mm-device-type 缺失")
	}
	if req.Header.Get("x-mm-client-request-id") == "" {
		t.Fatal("x-mm-client-request-id 缺失")
	}
	if req.Header.Get("x-mm-client-trace-id") == "" {
		t.Fatal("x-mm-client-trace-id 缺失")
	}
	if got := req.Header.Get("User-Agent"); got == "" || got == "Go-http-client/1.1" {
		t.Fatalf("User-Agent 必须显式设置，got %q", got)
	}
}

// TestApplyMaasHeaders_PerRequestIDsAreUnique 锁定每请求可区分性落在 traceparent 上。
//
// 亲和类归因头（x-mm-client-request-id / x-mm-client-trace-id）自 0.2.25 起是
// **会话级恒定**（官方为每 turn 恒定）；每请求的唯一性由 OTel 的 traceparent
// 的 span 段承担。
func TestApplyMaasHeaders_PerRequestIDsAreUnique(t *testing.T) {
	sess := &maasSession{AccessKey: "sk-mm-v1.test", SessionID: "sess-affinity-1"}
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		req, err := http.NewRequest(http.MethodPost, "https://maas.example/v1/chat/completions", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		applyMaasHeaders(req, sess)
		trace := req.Header.Get("traceparent")
		if trace == "" {
			t.Fatal("traceparent 必须发出")
		}
		span := strings.Split(trace, "-")[2]
		if seen[span] {
			t.Fatalf("traceparent 的 span 段必须每请求唯一，重复值 %q", span)
		}
		seen[span] = true
	}
}

// TestApplyMaasHeaders_NoSessionIDStillSendsAttribution 锁定票据无会话号时
// 归因头仍要发出（上游按来源区分流量，不能因为缺会话号就退化成裸请求）。
func TestApplyMaasHeaders_NoSessionIDStillSendsAttribution(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://maas.example/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	applyMaasHeaders(req, &maasSession{AccessKey: "sk-mm-v1.test"})
	if got := req.Header.Get("x-session-id"); got != "" {
		t.Fatalf("票据没有会话号时不得伪造 x-session-id，got %q", got)
	}
	if req.Header.Get("x-mm-client-request-source") == "" {
		t.Fatal("x-mm-client-request-source 缺失")
	}
}

// TestMaasSessionCacheKey_SeparatesUpstreamSessions 锁定票据按上游会话分片。
//
// 不同上游会话必须拿到不同的票据缓存键，否则上游会话被折叠成一个，
// 排队与前缀缓存串味一起发生。
func TestMaasSessionCacheKey_SeparatesUpstreamSessions(t *testing.T) {
	sa := &storedAuth{Account: storedAccount{UID: "uid-1"}, Auth: storedTokens{AccessToken: "dt-secret"}}
	keyA := maasSessionCacheKey(sa, "cache:conv-a")
	keyB := maasSessionCacheKey(sa, "cache:conv-b")
	if keyA == keyB {
		t.Fatalf("不同上游会话必须派生不同缓存键，got %q", keyA)
	}
	if keyA == maasSessionCacheKey(sa, "") {
		t.Fatalf("有会话键时不得落到 nosession 固定分片，got %q", keyA)
	}
	if got := maasSessionCacheKey(sa, ""); got != "uid-1|nosession" {
		t.Fatalf("无会话键时必须落到 nosession 固定分片，got %q", got)
	}
}

// TestPruneMaasSessionsLocked_RemovesExpired 锁定过期票据被清理。
func TestPruneMaasSessionsLocked_RemovesExpired(t *testing.T) {
	globalMaasCache.mu.Lock()
	defer globalMaasCache.mu.Unlock()
	globalMaasCache.sessions = map[string]*maasSession{
		"expired": {AccessKey: "old", ExpiresAtMs: time.Now().Add(-time.Minute).UnixMilli()},
		"fresh":   {AccessKey: "new", ExpiresAtMs: time.Now().Add(time.Hour).UnixMilli()},
	}
	removed := pruneMaasSessionsLocked(time.Now())
	if removed != 1 {
		t.Fatalf("必须只清理 1 条过期票据，got %d", removed)
	}
	if _, ok := globalMaasCache.sessions["expired"]; ok {
		t.Fatal("过期票据必须被删除")
	}
	if _, ok := globalMaasCache.sessions["fresh"]; !ok {
		t.Fatal("未过期票据必须保留")
	}
}

// TestPruneMaasSessionsLocked_EnforcesCapacity 锁定缓存条目数上限。
//
// 上游会话键逐对话化后条目基数会涨，不淘汰就撞插件堆 128MiB 软上限。
func TestPruneMaasSessionsLocked_EnforcesCapacity(t *testing.T) {
	globalMaasCache.mu.Lock()
	defer globalMaasCache.mu.Unlock()
	sessions := make(map[string]*maasSession, maasCacheMaxEntries+10)
	base := time.Now().UnixMilli()
	for i := 0; i < maasCacheMaxEntries+10; i++ {
		sessions[fmt.Sprintf("k-%d", i)] = &maasSession{
			AccessKey:   "k",
			ExpiresAtMs: time.Now().Add(time.Hour).UnixMilli(),
			MintedAtMs:  base + int64(i),
		}
	}
	globalMaasCache.sessions = sessions
	pruneMaasSessionsLocked(time.Now())
	if len(globalMaasCache.sessions) > maasCacheMaxEntries {
		t.Fatalf("清理后条目数必须不超过上限 %d，got %d", maasCacheMaxEntries, len(globalMaasCache.sessions))
	}
}

// TestStoreMaasSessionLocked_PrunesOverCapacity 锁定写入票据时顺带淘汰。
//
// 淘汰必须挂在唯一写入入口上：调用点漏一处就会让 map 无限膨胀，
// 而插件堆有 128MiB 软上限。
func TestStoreMaasSessionLocked_PrunesOverCapacity(t *testing.T) {
	globalMaasCache.mu.Lock()
	defer globalMaasCache.mu.Unlock()
	globalMaasCache.sessions = make(map[string]*maasSession, maasCacheMaxEntries+5)
	base := time.Now().UnixMilli()
	for i := 0; i < maasCacheMaxEntries+5; i++ {
		globalMaasCache.sessions[fmt.Sprintf("k-%d", i)] = &maasSession{
			AccessKey:   "k",
			ExpiresAtMs: time.Now().Add(time.Hour).UnixMilli(),
			MintedAtMs:  base + int64(i),
		}
	}
	storeMaasSessionLocked("k-new", &maasSession{
		AccessKey:   "new",
		ExpiresAtMs: time.Now().Add(time.Hour).UnixMilli(),
		MintedAtMs:  base + int64(maasCacheMaxEntries+20),
	})
	if len(globalMaasCache.sessions) > maasCacheMaxEntries {
		t.Fatalf("写入后条目数必须不超过上限 %d，got %d", maasCacheMaxEntries, len(globalMaasCache.sessions))
	}
	if _, ok := globalMaasCache.sessions["k-new"]; !ok {
		t.Fatal("新票据必须保留")
	}
}
