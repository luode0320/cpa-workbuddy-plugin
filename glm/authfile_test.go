package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestStoredAuthRoundTrip 验证 refresh_token 账号写入后可读回且字段一致。
func TestStoredAuthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })

	sa := &StoredAuth{
		Type:         glmProviderID,
		Provider:     glmProviderID,
		AuthID:       authIDFor("test-refresh-token"),
		RefreshToken: "test-refresh-token",
		Label:        "测试账号",
		CreatedAt:    "2026-11-28T04:00:00+08:00",
		UpdatedAt:    "2026-11-28T04:00:00+08:00",
	}
	path := filepath.Join(dir, sa.AuthID+".json")
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("writeAuthFileDirect failed: %v", err)
	}
	got, err := readAuthFile(path)
	if err != nil {
		t.Fatalf("readAuthFile failed: %v", err)
	}
	if got.RefreshToken != sa.RefreshToken || got.AuthID != sa.AuthID {
		t.Fatalf("round trip mismatch: got %+v", got)
	}
	if !isSafeGLMAuthPath(path) || !isGLMAuthFileName(filepath.Base(path)) {
		t.Fatal("auth path not recognized as glm namespace")
	}
}

// TestWriteAuthFileDirectPreservesUnknownFields 验证写盘保留未知顶层键。
func TestWriteAuthFileDirectPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })

	authID := authIDFor("unknown-fields-token")
	path := filepath.Join(dir, authID+".json")
	seed := map[string]any{
		"type":          glmProviderID,
		"refresh_token": "old-token",
		"custom_marker": "keep-me",
	}
	seedRaw, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(path, seedRaw, 0600); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	sa := &StoredAuth{AuthID: authID, RefreshToken: "new-token"}
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("writeAuthFileDirect failed: %v", err)
	}
	doc, _, err := readAuthDoc(path)
	if err != nil {
		t.Fatalf("readAuthDoc failed: %v", err)
	}
	if doc["custom_marker"] != "keep-me" {
		t.Fatalf("unknown field lost: %+v", doc)
	}
	if doc["refresh_token"] != "new-token" {
		t.Fatalf("known field not updated: %+v", doc)
	}
}

// TestAuthIDStable 验证同一 refresh_token 生成稳定且带前缀的 AuthID。
func TestAuthIDStable(t *testing.T) {
	id1 := authIDFor("token-a")
	id2 := authIDFor("token-a")
	id3 := authIDFor("token-b")
	if id1 != id2 {
		t.Fatal("authIDFor not stable")
	}
	if id1 == id3 {
		t.Fatal("different tokens must yield different ids")
	}
	if len(id1) <= len(glmAuthFilePrefix) || id1[:len(glmAuthFilePrefix)] != glmAuthFilePrefix {
		t.Fatalf("auth id missing prefix: %s", id1)
	}
}
