// checkin_headers_test.go guards the check-in request shaping: browser-like
// headers (WAF penalty avoidance) and sane device-id composition (no
// leading-dash ids when the credential carries no device fingerprint).
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDeviceIDFor(t *testing.T) {
	cases := []struct {
		base, uid, want string
	}{
		{"dev123", "u1", "dev123-u1"}, // full fingerprint + user
		{"dev123", "", "dev123"},     // fingerprint only
		{"", "u1", "u1"},             // empty base must NOT produce "-u1"
		{"", "", ""},                 // nothing known
	}
	for _, c := range cases {
		if got := deviceIDFor(c.base, c.uid); got != c.want {
			t.Errorf("deviceIDFor(%q,%q) = %q, want %q", c.base, c.uid, got, c.want)
		}
	}
	if got := deviceIDFor("", "2033439621254311"); strings.HasPrefix(got, "-") {
		t.Errorf("leading-dash device id regressed: %q", got)
	}
}

func TestCheckinAuthHeaders_ClientLike(t *testing.T) {
	a := &traeAuth{Token: "tok", DeviceID: "dev", UserID: "u1"}
	h := checkinAuthHeaders(a, "dev-u1")
	if got := h.Get("User-Agent"); !strings.HasPrefix(got, "Mozilla/5.0") {
		t.Errorf("User-Agent = %q, want client UA", got)
	}
	if got := h.Get("Origin"); got != "" {
		t.Errorf("Origin = %q, want empty (client request must NOT contain web origin)", got)
	}
	if got := h.Get("Referer"); got != "" {
		t.Errorf("Referer = %q, want empty (client request must NOT contain web referer)", got)
	}
	if got := h.Get("Authorization"); got != "Cloud-IDE-JWT tok" {
		t.Errorf("Authorization = %q, unchanged expected", got)
	}
	if got := h.Get("x-device-id"); got != "dev-u1" {
		t.Errorf("x-device-id = %q", got)
	}
	if got := h.Get("x-device-brand"); got == "" {
		t.Error("x-device-brand missing")
	}
	if got := h.Get("x-device-type"); got == "" {
		t.Error("x-device-type missing")
	}
	if got := h.Get("x-os-version"); got == "" {
		t.Error("x-os-version missing")
	}
	if got := h.Get("x-app-version"); got == "" {
		t.Error("x-app-version missing")
	}
	if got := h.Get("x-app-id"); got == "" {
		t.Error("x-app-id missing")
	}
	if h.Get("Content-Type") != "application/json" {
		t.Error("Content-Type missing")
	}
}

func TestCheckinClaimRequest_JSONPayload(t *testing.T) {
	payload, err := json.Marshal(checkinClaimRequest{ReqSource: 2})
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if string(payload) != `{"req_source":2}` {
		t.Errorf("got payload %s, want {\"req_source\":2}", string(payload))
	}
}

func TestCheckinAuthHeaders_NoBackticks(t *testing.T) {
	// Guard against raw-string-breaking characters sneaking into header values.
	a := &traeAuth{Token: "tok"}
	h := checkinAuthHeaders(a, "d")
	for k, vs := range h {
		for _, v := range vs {
			if strings.ContainsAny(v, "`") {
				t.Errorf("header %s contains backtick: %q", k, v)
			}
		}
	}
	_ = http.Header(h)
}
