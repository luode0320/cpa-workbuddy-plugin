// keepalive_test.go 覆盖 refresh token 家族识别与刷新后的票据失效。
package main

import "testing"

// TestIsDeviceRefreshFamily 验证可刷新家族判定：drt- 与 ory_ 均可走
// deviceToken 刷新，其余前缀与空值一律不认。
func TestIsDeviceRefreshFamily(t *testing.T) {
	cases := []struct {
		name string
		tok  string
		want bool
	}{
		{"oauth device family", "drt-abc123", true},
		{"new gateway family", "ory_rt_abc123", true},
		{"leading space trimmed", "  ory_rt_abc123  ", true},
		{"legacy jwt family rejected", "jrt-abc123", false},
		{"empty", "", false},
		{"blank", "   ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isDeviceRefreshFamily(c.tok); got != c.want {
				t.Fatalf("isDeviceRefreshFamily(%q) = %v, want %v", c.tok, got, c.want)
			}
		})
	}
}

// TestInvalidateForRefresh_ClearsStaleKey 验证 token 轮换后必须同时清掉
// 刷新前的快照键：账号缺 UID 时缓存键取自 access token 指纹，只按新凭证
// 清理会漏删旧票据（旧票据泄漏）。
func TestInvalidateForRefresh_ClearsStaleKey(t *testing.T) {
	stale := &storedAuth{}
	stale.Auth.AccessToken = "old-token-value-for-fingerprint"
	fresh := &storedAuth{}
	fresh.Auth.AccessToken = "new-token-value-for-fingerprint"

	oldKey := maasCacheKeyPrefix(stale)
	newKey := maasCacheKeyPrefix(fresh)
	if oldKey == "" || newKey == "" {
		t.Fatalf("cache keys must not be empty: old=%q new=%q", oldKey, newKey)
	}
	if oldKey == newKey {
		t.Fatal("token change must produce different cache keys when UID is empty")
	}

	globalMaasCache.mu.Lock()
	globalMaasCache.sessions[oldKey] = &maasSession{AccessKey: "sk-mm-v1.stale"}
	globalMaasCache.mu.Unlock()
	t.Cleanup(func() {
		globalMaasCache.mu.Lock()
		delete(globalMaasCache.sessions, oldKey)
		delete(globalMaasCache.sessions, newKey)
		globalMaasCache.mu.Unlock()
	})

	// 反证：只按刷新后的凭证清理删不到旧键（修复前的行为）。
	invalidateMaasSession(fresh)
	globalMaasCache.mu.Lock()
	_, stillThere := globalMaasCache.sessions[oldKey]
	globalMaasCache.mu.Unlock()
	if !stillThere {
		t.Fatal("sanity: invalidating with the new token must not remove the old-key entry")
	}

	// 修复后的行为：刷新后同时清旧快照与新凭证。
	invalidateForRefresh(stale, fresh)
	globalMaasCache.mu.Lock()
	_, left := globalMaasCache.sessions[oldKey]
	globalMaasCache.mu.Unlock()
	if left {
		t.Fatal("invalidateForRefresh must remove the stale-key entry")
	}
}
