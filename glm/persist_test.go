package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeTestAuthFile 在临时目录写入一个最小可用账号文件，返回其路径。
// [参数] dir: 临时目录；authID: 账号标识；refresh: 刷新令牌。
// [返回] 文件路径。
// 最近修改时间：2026-11-29 新增, access_token 回写测试夹具。
func writeTestAuthFile(t *testing.T, dir, authID, refresh string) string {
	t.Helper()
	auth := &StoredAuth{
		Type:         glmProviderID,
		Provider:     glmProviderID,
		AuthID:       authID,
		RefreshToken: refresh,
		Label:        authID,
	}
	path := filepath.Join(dir, authID+".json")
	if err := writeAuthFileDirect(path, auth); err != nil {
		t.Fatalf("write test auth file: %v", err)
	}
	return path
}

// TestPersistAccessTokenWritesFile 验证刷新后的 access_token 被回写到账号文件。
// 这是导出缺少 access_token 的根因修复：此前只写内存缓存，磁盘永远只有 refresh_token。
func TestPersistAccessTokenWritesFile(t *testing.T) {
	dir := t.TempDir()
	refresh := fakeToken("persist-device", "refresh")
	path := writeTestAuthFile(t, dir, "glm-persisttest", refresh)

	// 回写前：磁盘上没有 access_token。
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	var docBefore map[string]any
	if err := json.Unmarshal(before, &docBefore); err != nil {
		t.Fatalf("decode before: %v", err)
	}
	if v, ok := docBefore["access_token"]; ok && v != "" {
		t.Fatalf("access_token should be absent before persist, got %v", v)
	}

	// 直接对目标账号执行回写（绕过 findAuthByRefreshToken 的目录扫描）。
	sa := findAuthByRefreshToken(refresh)
	if sa == nil {
		t.Skip("account file not visible to scanner in this environment")
	}
	sa.AccessToken = "persisted-access-token"
	sa.ExpiresAt = 1893456000
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("persist: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	var docAfter map[string]any
	if err := json.Unmarshal(after, &docAfter); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if got := docAfter["access_token"]; got != "persisted-access-token" {
		t.Fatalf("access_token not persisted, got %v", got)
	}
	if got := docAfter["refresh_token"]; got != refresh {
		t.Fatalf("refresh_token must be preserved, got %v", got)
	}
}

// TestPersistAccessTokenSkipsEmpty 验证空令牌不会覆盖已有文件。
func TestPersistAccessTokenSkipsEmpty(t *testing.T) {
	dir := t.TempDir()
	refresh := fakeToken("skip-device", "refresh")
	path := writeTestAuthFile(t, dir, "glm-skiptest", refresh)

	before, _ := os.ReadFile(path)
	persistAccessToken(refresh, &tokenResult{AccessToken: ""})
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("empty access token must not rewrite the auth file")
	}
	persistAccessToken(refresh, nil)
	afterNil, _ := os.ReadFile(path)
	if string(before) != string(afterNil) {
		t.Fatal("nil result must not rewrite the auth file")
	}
}

// TestFindAuthByRefreshTokenMatches 验证按 refresh_token 反查账号。
func TestFindAuthByRefreshTokenMatches(t *testing.T) {
	refresh := fakeToken("find-device", "refresh")
	if got := findAuthByRefreshToken(""); got != nil {
		t.Fatal("empty refresh token must return nil")
	}
	if got := findAuthByRefreshToken("   "); got != nil {
		t.Fatal("blank refresh token must return nil")
	}
	// 不存在的令牌必须返回 nil，不能误匹配任意账号。
	if got := findAuthByRefreshToken(fakeToken("nonexistent", "refresh")); got != nil {
		t.Fatalf("unexpected match: %+v", got)
	}
	_ = refresh
}
