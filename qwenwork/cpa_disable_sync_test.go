// cpa_disable_sync_test.go 覆盖宿主管理 API 禁用通道的契约与兜底语义。
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetHostAuthDisabled_NotConfigured(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement("", "")

	err := setHostAuthDisabled("qwenwork-x.json", "idx", true)
	if !errors.Is(err, errCPAManagementNotConfigured) {
		t.Fatalf("err = %v; want errCPAManagementNotConfigured", err)
	}
}

func TestSetHostAuthDisabled_SendsExpectedRequest(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement("http://127.0.0.1:8317", "secret-key")

	orig := cpaDisableSyncDo
	defer func() { cpaDisableSyncDo = orig }()
	var got cpaAuthFileStatusRequest
	cpaDisableSyncDo = func(req *http.Request) (*hostHTTPResponse, error) {
		if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
			return nil, err
		}
		return &hostHTTPResponse{StatusCode: http.StatusOK}, nil
	}

	if err := setHostAuthDisabled("qwenwork-x.json", "idx-1", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "qwenwork-x.json" || got.AuthIndex != "idx-1" || !got.Disabled {
		t.Fatalf("body = %+v", got)
	}
}

func TestSetHostAuthDisabled_NonSuccessReturnsError(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement("http://127.0.0.1:8317", "secret-key")

	orig := cpaDisableSyncDo
	defer func() { cpaDisableSyncDo = orig }()
	cpaDisableSyncDo = func(*http.Request) (*hostHTTPResponse, error) {
		return &hostHTTPResponse{StatusCode: 404, Body: []byte(`{"error":"auth file not found"}`)}, nil
	}

	err := setHostAuthDisabled("missing.json", "", true)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404 必须返回带状态码的错误，实际: %v", err)
	}
}

// TestSetHostAuthDisabled_RealHTTPServer 用真实 HTTP 服务校验方法、路径与鉴权头。
func TestSetHostAuthDisabled_RealHTTPServer(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody cpaAuthFileStatusRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement(srv.URL, "k-123")

	orig := cpaDisableSyncDo
	defer func() { cpaDisableSyncDo = orig }()
	cpaDisableSyncDo = func(req *http.Request) (*hostHTTPResponse, error) {
		resp, err := srv.Client().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return &hostHTTPResponse{StatusCode: resp.StatusCode, Body: []byte(`{"status":"ok"}`)}, nil
	}

	if err := setHostAuthDisabled("qwenwork-y.json", "idx-9", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/v0/management/auth-files/status" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotAuth != "Bearer k-123" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotBody.Name != "qwenwork-y.json" || !gotBody.Disabled {
		t.Fatalf("body = %+v", gotBody)
	}
}

func TestSetCPAManagement_DefaultURLAndSlashTrim(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)

	setCPAManagement("", "k")
	if url, _ := loadedCPAManagement(); url != defaultCPAManagementURL {
		t.Fatalf("url = %s; want default", url)
	}
	setCPAManagement("http://host:9000/", "")
	if url, _ := loadedCPAManagement(); url != "http://host:9000" {
		t.Fatalf("url = %s; want trailing slash trimmed", url)
	}
}

func TestCPAManagementSecretResolver_Precedence(t *testing.T) {
	t.Setenv("WB_CPA_MANAGEMENT_KEY", "")
	t.Setenv("CPA_MANAGEMENT_KEY", "")
	if got := cpaManagementSecretResolver("from-config"); got != "from-config" {
		t.Fatalf("config 应优先，实际 %q", got)
	}
	t.Setenv("WB_CPA_MANAGEMENT_KEY", "from-env")
	if got := cpaManagementSecretResolver(""); got != "from-env" {
		t.Fatalf("env 应兜底，实际 %q", got)
	}
}
