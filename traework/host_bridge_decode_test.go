package main

import (
	"encoding/json"
	"testing"
)

// TestDecodeBridgeHTTPResponse_TolerantStatus locks in the 0.1.10 fix: host
// versions differ in the JSON key used for the upstream status inside the
// host.http.do result. Decoding only "status_code" silently produced
// StatusCode 0, which callers treated as a hard HTTP failure even when the
// body was intact — check-in/points broke on Linux (bridge path) while the
// Windows direct path kept working, hiding the bug in dev.
func TestDecodeBridgeHTTPResponse_TolerantStatus(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want int
	}{
		{"snake", `{"status_code":200,"body":"e30="}`, 200},
		{"camel", `{"statusCode":201,"body":"e30="}`, 201},
		{"status-number", `{"status":200,"body":"e30="}`, 200},
		{"status-string", `{"status":"200","body":"e30="}`, 200},
		{"code-number", `{"code":200,"body":"e30="}`, 200},
		{"code-string", `{"code":"403","body":"e30="}`, 403},
		{"absent", `{"body":"e30="}`, 0},
	}
	for _, tc := range cases {
		resp, err := decodeBridgeHTTPResponse(json.RawMessage(tc.wire))
		if err != nil {
			t.Fatalf("%s: decode error: %v", tc.name, err)
		}
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: StatusCode=%d; want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}

// TestDecodeBridgeHTTPResponse_BodyIntact ensures the body survives the
// tolerant decode (the check-in business code rides in the body even when
// the status key is unrecognized).
func TestDecodeBridgeHTTPResponse_BodyIntact(t *testing.T) {
	wire := `{"status":200,"headers":{"Content-Type":["application/json"]},"body":"eyJjb2RlIjo5MDc0fQ=="}`
	resp, err := decodeBridgeHTTPResponse(json.RawMessage(wire))
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if string(resp.Body) != `{"code":9074}` {
		t.Fatalf("body=%q; want {\"code\":9074}", string(resp.Body))
	}
	if resp.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("headers lost in decode")
	}
}

func TestIsDefiniteHTTPFailure(t *testing.T) {
	if !isDefiniteHTTPFailure(500) || !isDefiniteHTTPFailure(403) {
		t.Fatalf("definite non-200 statuses must fail")
	}
	if isDefiniteHTTPFailure(200) {
		t.Fatalf("200 must not fail")
	}
	if isDefiniteHTTPFailure(0) {
		t.Fatalf("0 = undecodable bridge status; must NOT be a definite failure (body decides)")
	}
}

func TestIsBusyThrottleMsg(t *testing.T) {
	if !isBusyThrottleMsg("当前参与用户太多，请稍后再试") {
		t.Fatalf("Trae 9074 throttle message must match")
	}
	if isBusyThrottleMsg("签到成功") {
		t.Fatalf("success message must not match throttle")
	}
}
