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
	if len(reg.Resources) != 1 || reg.Resources[0].Path != "/ui" || reg.Resources[0].Menu != "Safeguard Killer" {
		t.Fatalf("resources = %+v", reg.Resources)
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
	for _, want := range []string{"/v0/management/plugins/safeguard-killer/state", "/v8/management/config/plugins/configs/safeguard-killer"} {
		if !strings.Contains(string(resp.Body), want) {
			t.Fatalf("ui html does not reference %s", want)
		}
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
