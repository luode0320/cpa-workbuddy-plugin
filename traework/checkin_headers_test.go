// checkin_headers_test.go guards the check-in request shaping: browser-like
// headers (WAF penalty avoidance) and sane device-id composition (no
// leading-dash ids when the credential carries no device fingerprint).
package main

import (
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

func TestCheckinAuthHeaders_BrowserLike(t *testing.T) {
	a := &traeAuth{Token: "tok", DeviceID: "dev", UserID: "u1"}
	h := checkinAuthHeaders(a, "dev-u1")
	if got := h.Get("User-Agent"); !strings.HasPrefix(got, "Mozilla/5.0") {
		t.Errorf("User-Agent = %q, want a browser UA (Go default gets WAF-throttled)", got)
	}
	if got := h.Get("Origin"); got != "https://work.trae.cn" {
		t.Errorf("Origin = %q, want https://work.trae.cn", got)
	}
	if got := h.Get("Authorization"); got != "Cloud-IDE-JWT tok" {
		t.Errorf("Authorization = %q, unchanged expected", got)
	}
	if got := h.Get("x-device-id"); got != "dev-u1" {
		t.Errorf("x-device-id = %q", got)
	}
	if h.Get("Content-Type") != "application/json" {
		t.Error("Content-Type missing")
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
