// maas_session_id_test.go 覆盖 MaaS session_id 的 UUID 派生。
//
// 缺陷背景（2026-10-18 生产实证）：调度侧会话键形如
// "x-client-request-id:abc"，被直接当作上游 session_id 发送，上游强校验
// UUID 后返回 `422 value is not a valid UUID`，导致执行器路径全部失败，
// 而面板测试（传空 sessionID）正常——两条路径的差异掩盖了该缺陷。
package main

import (
	"regexp"
	"testing"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestMaasSessionID_DerivesValidUUID 验证任意会话键都派生出合法 UUID。
func TestMaasSessionID_DerivesValidUUID(t *testing.T) {
	keys := []string{
		"x-client-request-id:abc",
		"session-id:20261018",
		"derived:conv-123",
		"execution:task-9",
		"中文会话键",
	}
	for _, k := range keys {
		got := maasSessionID(k)
		if !uuidPattern.MatchString(got) {
			t.Errorf("maasSessionID(%q) = %q, not a valid v4 UUID", k, got)
		}
	}
}

// TestMaasSessionID_Deterministic 验证同一会话键恒得同一 UUID（保持会话粘性），
// 不同会话键得到不同 UUID。
func TestMaasSessionID_Deterministic(t *testing.T) {
	a1 := maasSessionID("x-client-request-id:abc")
	a2 := maasSessionID("x-client-request-id:abc")
	if a1 != a2 {
		t.Fatalf("same session key must map to same UUID: %q vs %q", a1, a2)
	}
	b := maasSessionID("x-client-request-id:def")
	if a1 == b {
		t.Fatalf("different session keys must map to different UUIDs, both = %q", a1)
	}
}

// TestMaasSessionID_EmptyReturnsEmpty 验证空键返回空串，交由上游自行分配。
func TestMaasSessionID_EmptyReturnsEmpty(t *testing.T) {
	if got := maasSessionID(""); got != "" {
		t.Errorf("empty session key must return empty, got %q", got)
	}
	if got := maasSessionID("   "); got != "" {
		t.Errorf("blank session key must return empty, got %q", got)
	}
}

// TestMintPayload_SessionIDIsUUID 直接断言发往上游的请求体。
//
// 这是变异能命中的关键：只测 maasSessionID 无法发现调用方漏用派生结果，
// 必须验证「实际组装出来的 payload」里 session_id 是 UUID。
func TestMintPayload_SessionIDIsUUID(t *testing.T) {
	payload := mintPayload("x-client-request-id:abc")
	sid, _ := payload["session_id"].(string)
	if !uuidPattern.MatchString(sid) {
		t.Fatalf("payload session_id = %q, not a valid v4 UUID (upstream rejects with 422)", sid)
	}
	// 空会话键时不得写入该字段，避免空串触发上游校验。
	if p := mintPayload(""); len(p) != 0 {
		t.Fatalf("empty session key must produce empty payload, got %v", p)
	}
}

// TestMaasSessionCacheKey_PerSession 验证票据缓存键按会话隔离：
// 同账号不同会话必须各自独立，否则上游会话绑定会被折叠成同一个。
func TestMaasSessionCacheKey_PerSession(t *testing.T) {
	sa := &storedAuth{Account: storedAccount{UID: "uid-1"}}
	k1 := maasSessionCacheKey(sa, "conv-a")
	k2 := maasSessionCacheKey(sa, "conv-b")
	if k1 == k2 {
		t.Fatalf("different sessions must get different cache keys, both = %q", k1)
	}
	if again := maasSessionCacheKey(sa, "conv-a"); again != k1 {
		t.Fatalf("same session must be stable: %q vs %q", k1, again)
	}
	if got := maasSessionCacheKey(sa, ""); got != "uid-1|nosession" {
		t.Fatalf("empty session key must use the nosession shard, got %q", got)
	}
	// 不同账号即使会话相同也必须隔离。
	other := &storedAuth{Account: storedAccount{UID: "uid-2"}}
	if maasSessionCacheKey(other, "conv-a") == k1 {
		t.Fatal("different accounts must not share a cache key")
	}
}

// TestInvalidateMaasSession_ClearsAllSessions 验证按账号失效会清掉该账号
// 全部会话分片以及升级前的纯账号键，且不影响其他账号。
func TestInvalidateMaasSession_ClearsAllSessions(t *testing.T) {
	sa := &storedAuth{Account: storedAccount{UID: "uid-inv"}}
	other := &storedAuth{Account: storedAccount{UID: "uid-keep"}}
	base := maasCacheKeyPrefix(sa)
	keys := []string{
		base, // 升级前的纯账号键
		maasSessionCacheKey(sa, "conv-a"),
		maasSessionCacheKey(sa, "conv-b"),
		maasSessionCacheKey(other, "conv-a"),
	}
	globalMaasCache.mu.Lock()
	for _, k := range keys {
		globalMaasCache.sessions[k] = &maasSession{AccessKey: "sk-mm-v1.test"}
	}
	globalMaasCache.mu.Unlock()
	t.Cleanup(func() {
		globalMaasCache.mu.Lock()
		for _, k := range keys {
			delete(globalMaasCache.sessions, k)
		}
		globalMaasCache.mu.Unlock()
	})

	invalidateMaasSession(sa)

	globalMaasCache.mu.Lock()
	defer globalMaasCache.mu.Unlock()
	for _, k := range keys[:3] {
		if _, ok := globalMaasCache.sessions[k]; ok {
			t.Fatalf("entry %q must be cleared for the invalidated account", k)
		}
	}
	if _, ok := globalMaasCache.sessions[keys[3]]; !ok {
		t.Fatal("other accounts' entries must survive invalidation")
	}
}
