package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestManagementRegistrationPaths(t *testing.T) {
	reg := managementRegistrationPayload()
	if len(reg.Routes) != 1 || reg.Routes[0].Method != http.MethodGet || reg.Routes[0].Path != "/plugins/safeguard-killer/state" {
		t.Fatalf("routes = %+v", reg.Routes)
	}
	// 资源路由两条：面板本身 + 面板自用的只读状态快照（Menu 留空，不占侧边栏）。
	if len(reg.Resources) != 2 {
		t.Fatalf("resources = %+v", reg.Resources)
	}
	if reg.Resources[0].Path != "/ui" || reg.Resources[0].Menu != "Safeguard Killer" {
		t.Fatalf("ui resource = %+v", reg.Resources[0])
	}
	if reg.Resources[1].Path != "/state" || reg.Resources[1].Menu != "" {
		t.Fatalf("state resource = %+v", reg.Resources[1])
	}
}

func callManagement(t *testing.T, method, path string) managementResponse {
	t.Helper()
	raw, errMarshal := json.Marshal(managementRequest{Method: method, Path: path})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	out, errHandle := handleManagement(raw)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(out, &env); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %s", out)
	}
	var resp managementResponse
	if errUnmarshal := json.Unmarshal(env.Result, &resp); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return resp
}

func TestHandleManagementState(t *testing.T) {
	resp := callManagement(t, http.MethodGet, "/v0/management/plugins/safeguard-killer/state")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Headers.Get("Content-Type"), "application/json") {
		t.Fatalf("status=%d headers=%v", resp.StatusCode, resp.Headers)
	}
	var state uiState
	if errUnmarshal := json.Unmarshal(resp.Body, &state); errUnmarshal != nil {
		t.Fatalf("body is not uiState JSON: %v: %s", errUnmarshal, resp.Body)
	}
}

func TestHandleManagementUI(t *testing.T) {
	resp := callManagement(t, http.MethodGet, "/v0/resource/plugins/safeguard-killer/ui")
	if !strings.HasPrefix(resp.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("headers=%v", resp.Headers)
	}
	// 面板读状态走免鉴权的 resource 路由，写配置仍走需要密钥的 v8 管理接口。
	for _, want := range []string{"/v0/resource/plugins/safeguard-killer/state", "/v8/management/config/plugins/configs/safeguard-killer"} {
		if !strings.Contains(string(resp.Body), want) {
			t.Fatalf("ui html does not reference %s", want)
		}
	}
	// 面板不该再出现让人手填管理密钥的输入框。
	if strings.Contains(string(resp.Body), "mgmtKey") {
		t.Fatal("ui html still contains the management key input")
	}
}

// 无鉴权的 resource 状态路由必须和需要密钥的管理路由返回同一份状态。
func TestHandleManagementStateResourcePath(t *testing.T) {
	resp := callManagement(t, http.MethodGet, "/v0/resource/plugins/safeguard-killer/state")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Headers.Get("Content-Type"), "application/json") {
		t.Fatalf("status=%d headers=%v", resp.StatusCode, resp.Headers)
	}
	var state uiState
	if errUnmarshal := json.Unmarshal(resp.Body, &state); errUnmarshal != nil {
		t.Fatalf("body is not uiState JSON: %v: %s", errUnmarshal, resp.Body)
	}
	if state.Version != version {
		t.Fatalf("version = %q, want %q", state.Version, version)
	}
}

func TestHandleManagementRejectsMalformed(t *testing.T) {
	if _, errHandle := handleManagement([]byte("{")); errHandle == nil {
		t.Fatal("malformed request accepted")
	}
	raw, _ := json.Marshal(managementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/safeguard-killer/other"})
	if _, errHandle := handleManagement(raw); errHandle == nil {
		t.Fatal("unknown path accepted")
	}
}
