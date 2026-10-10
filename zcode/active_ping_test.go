package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestActivePing_ModelsQuery(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "zcode_ping_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	setAccountsDir(tempDir)

	sa := &StoredAuth{
		AuthID:   "zcode-test-1",
		APIKey:   "key1.secret",
		Models:   []string{"GLM-5.3", "GLM-5.3-Flash"},
		Disabled: false,
	}
	_ = writeAuthFileDirect(filepath.Join(tempDir, "zcode-test-1.json"), sa)

	data, err := handleModelsQuery("zcode-test-1")
	if err != nil {
		t.Fatalf("handleModelsQuery failed: %v", err)
	}

	var res map[string][]string
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("unmarshal models failed: %v", err)
	}
	if len(res["models"]) != 2 || res["models"][0] != "GLM-5.3" {
		t.Fatalf("models mismatch: %+v", res)
	}
}

func TestActivePing_MockServer(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "zcode_ping_mock_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	setAccountsDir(tempDir)

	// Mock 上游 server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "valid.key" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg-1","content":[{"type":"text","text":"pong"}]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_key"}`))
	}))
	defer server.Close()

	// 覆盖 BaseURL
	cfg := currentConfig()
	cfg.BaseURL = server.URL
	activeConfig.Store(cfg)

	saValid := &StoredAuth{AuthID: "zcode-valid", APIKey: "valid.key"}
	_ = writeAuthFileDirect(filepath.Join(tempDir, "zcode-valid.json"), saValid)

	saInvalid := &StoredAuth{AuthID: "zcode-invalid", APIKey: "invalid.key"}
	_ = writeAuthFileDirect(filepath.Join(tempDir, "zcode-invalid.json"), saInvalid)

	// 测试有效账号
	resValid := handleTestActiveWithAuth(saValid, "GLM-5.3")
	if !resValid.OK || resValid.Status != "active" {
		t.Fatalf("expected active status for valid account, got %+v", resValid)
	}

	// 测试无效账号
	resInvalid := handleTestActiveWithAuth(saInvalid, "GLM-5.3")
	if resInvalid.OK || resInvalid.Status != "failed" {
		t.Fatalf("expected failed status for invalid account, got %+v", resInvalid)
	}

	// 确认无效账号被打上 test_failed
	accAfter, _ := readAuthFile(filepath.Join(tempDir, "zcode-invalid.json"))
	if accAfter == nil || !accAfter.TestFailed {
		t.Fatalf("expected test_failed to be true, got %+v", accAfter)
	}
}
