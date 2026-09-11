package main

import (
	"encoding/json"
	"testing"
)

// Regression: the host serializes pluginapi.HTTPResponse WITHOUT json tags
// (v7.2.x), so the wire shape is PascalCase {"StatusCode":200,...}. The old
// parser used `json:"status_code"`, which can never match that key — every
// non-stream bridge response came back with StatusCode=0 (while Headers/Body
// still matched case-insensitively), silently killing dynamic model discovery
// and billing credits on Linux production. Windows was unaffected because
// hostHTTPDo bypasses the bridge there (GOOS mitigation).
//
// Fixture: json.Marshal of the host's actual value — untagged
// pluginapi.HTTPResponse{StatusCode, Headers, Body}.
func TestParseHostHTTPDoResult_HostUntaggedPascalCase(t *testing.T) {
	// Reproduce exactly what the host emits: marshal an untagged struct.
	hostResp := struct {
		StatusCode int
		Headers    map[string][]string
		Body       []byte
	}{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"code":0,"msg":"ok"}`),
	}
	wire, err := json.Marshal(hostResp)
	if err != nil {
		t.Fatalf("marshal host fixture: %v", err)
	}

	resp, err := parseHostHTTPDoResult(wire)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200 (old tag-based parser returned 0 here)", resp.StatusCode)
	}
	if string(resp.Body) != `{"code":0,"msg":"ok"}` {
		t.Fatalf("Body = %q, want upstream body", resp.Body)
	}
	if resp.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", resp.Headers.Get("Content-Type"))
	}
}

// Defensive: if a future host version adds snake_case tags
// ({"status_code":200,"headers":...,"body":...}), parsing must still work.
func TestParseHostHTTPDoResult_SnakeCaseTaggedVariant(t *testing.T) {
	wire := []byte(`{"status_code":401,"headers":{"Www-Authenticate":["Bearer"]},"body":"dW5hdXRo"}`)
	resp, err := parseHostHTTPDoResult(wire)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("StatusCode = %d, want 401", resp.StatusCode)
	}
	if string(resp.Body) != "unauth" {
		t.Fatalf("Body = %q, want unauth", resp.Body)
	}
	if resp.Headers.Get("Www-Authenticate") != "Bearer" {
		t.Fatalf("Www-Authenticate = %q, want Bearer", resp.Headers.Get("Www-Authenticate"))
	}
}

// Malformed inner payload must surface an error (caller logs + falls back to
// direct HTTP), not a zero-value response.
func TestParseHostHTTPDoResult_Malformed(t *testing.T) {
	if _, err := parseHostHTTPDoResult([]byte(`{not json`)); err == nil {
		t.Fatal("expected error for malformed payload, got nil")
	}
}
