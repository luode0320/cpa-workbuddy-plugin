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

// captureDisableSync 把 HTTP 注入点替换为直连 httptest，并返回调用的记录通道。
// 生产路径走宿主 http 桥接，单测里没有宿主 RPC，必须直连。
// [参数] t：测试上下文。
// [返回] 请求记录切片指针与恢复函数。
// 最近修改时间：2026-10-28；改动原因：新增。
func captureDisableSync(t *testing.T) (*[]cpaAuthFileStatusRequest, func()) {
	t.Helper()
	orig := cpaDisableSyncDo
	var got []cpaAuthFileStatusRequest
	cpaDisableSyncDo = func(req *http.Request) (*hostHTTPResponse, error) {
		var body cpaAuthFileStatusRequest
		dec := json.NewDecoder(req.Body)
		if err := dec.Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		got = append(got, body)
		return &hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"status":"ok"}`)}, nil
	}
	return &got, func() { cpaDisableSyncDo = orig }
}

func TestSetHostAuthDisabled_NotConfigured(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement("", "")

	err := setHostAuthDisabled("workbuddy-x.json", "idx", true)
	if !errors.Is(err, errCPAManagementNotConfigured) {
		t.Fatalf("err = %v; want errCPAManagementNotConfigured", err)
	}
}

func TestSetHostAuthDisabled_SendsExpectedRequest(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement("http://127.0.0.1:8317", "secret-key")

	got, restore := captureDisableSync(t)
	defer restore()

	if err := setHostAuthDisabled("workbuddy-x.json", "idx-1", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("calls = %d; want 1", len(*got))
	}
	body := (*got)[0]
	if body.Name != "workbuddy-x.json" || body.AuthIndex != "idx-1" || !body.Disabled {
		t.Fatalf("body = %+v; want name/auth_index/disabled all set", body)
	}
}

func TestSetHostAuthDisabled_EnableSendsFalse(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement("http://127.0.0.1:8317", "secret-key")

	got, restore := captureDisableSync(t)
	defer restore()

	if err := setHostAuthDisabled("workbuddy-x.json", "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*got) != 1 || (*got)[0].Disabled {
		t.Fatalf("disabled 应为 false，实际 %+v", *got)
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
	if err == nil {
		t.Fatal("404 必须返回错误，让调用方回落写盘")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("错误应包含状态码，实际: %v", err)
	}
}

func TestSetHostAuthDisabled_EmptyNameRejected(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement("http://127.0.0.1:8317", "secret-key")

	if err := setHostAuthDisabled("  ", "", true); err == nil {
		t.Fatal("空 name 必须拒绝")
	}
}

// TestSetHostAuthDisabled_RealHTTPServer 用真实 HTTP 服务校验方法、路径与鉴权头，
// 避免注入点掩盖 wire 层错误。
func TestSetHostAuthDisabled_RealHTTPServer(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotBody   cpaAuthFileStatusRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
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
		buf := make([]byte, 256)
		n, _ := resp.Body.Read(buf)
		return &hostHTTPResponse{StatusCode: resp.StatusCode, Body: buf[:n]}, nil
	}

	if err := setHostAuthDisabled("workbuddy-y.json", "idx-9", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/v0/management/auth-files/status" {
		t.Fatalf("path = %s; want /v0/management/auth-files/status", gotPath)
	}
	if gotAuth != "Bearer k-123" {
		t.Fatalf("auth header = %q; want Bearer k-123", gotAuth)
	}
	if gotBody.Name != "workbuddy-y.json" || !gotBody.Disabled {
		t.Fatalf("body = %+v", gotBody)
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
	if got := cpaManagementSecretResolver("from-config"); got != "from-config" {
		t.Fatalf("config 仍应优先于 env，实际 %q", got)
	}
	if got := cpaManagementSecretResolver(""); got == "" {
		t.Fatal("env 已配置时不应为空")
	}
}

func TestSetCPAManagement_DefaultURL(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)

	setCPAManagement("", "k")
	url, key := loadedCPAManagement()
	if url != defaultCPAManagementURL {
		t.Fatalf("url = %s; want default %s", url, defaultCPAManagementURL)
	}
	if key != "k" {
		t.Fatalf("key = %q", key)
	}

	// 末尾斜杠必须被裁掉，否则拼路径会出现双斜杠。
	setCPAManagement("http://host:9000/", "")
	url, _ = loadedCPAManagement()
	if url != "http://host:9000" {
		t.Fatalf("url = %s; want trailing slash trimmed", url)
	}
}

func TestCPADisableSyncConfigured(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)

	setCPAManagement("", "")
	if cpaDisableSyncConfigured() {
		t.Fatal("无密钥时应判未配置")
	}
	setCPAManagement("", "k")
	if !cpaDisableSyncConfigured() {
		t.Fatal("有密钥时应判已配置")
	}
}
