package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestNeedsKeepalive covers the refresh decision: no refresh token → caller
// skips; parseable expiry far in the future → no refresh; expiry within the
// lead window → refresh; already expired → refresh; unparseable → refresh.
func TestNeedsKeepalive(t *testing.T) {
	now := time.Now()
	future := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)
	soon := now.Add(time.Hour).Format(time.RFC3339)
	past := now.Add(-time.Hour).Format(time.RFC3339)

	cases := []struct {
		name string
		sa   *traeAuth
		want bool
	}{
		{"far future → no refresh", &traeAuth{ExpiredAt: future}, false},
		{"within 24h → refresh", &traeAuth{ExpiredAt: soon}, true},
		{"already expired → refresh", &traeAuth{ExpiredAt: past}, true},
		{"empty expiry → refresh (conservative)", &traeAuth{}, true},
		{"malformed expiry → refresh", &traeAuth{ExpiredAt: "not-a-date"}, true},
	}
	for _, c := range cases {
		if got := needsKeepalive(c.sa); got != c.want {
			t.Errorf("%s: needsKeepalive = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestFoldKeepaliveIntoDoc verifies the runtime token fold: token / refreshToken
// / expiredAt / refreshExpiredAt are updated at the top level while every other
// key (disabled, preserve, counters, credential blob) is preserved.
func TestFoldKeepaliveIntoDoc(t *testing.T) {
	base := []byte(`{"type":"traework-provider","disabled":false,"preserve":true,"success_count":3,"credential":"blob","token":"old-token","refreshToken":"old-refresh"}`)
	sa := &traeAuth{
		Token:            "new-token",
		RefreshToken:     "new-refresh",
		ExpiredAt:        "2030-01-01T00:00:00Z",
		RefreshExpiredAt: "2031-01-01T00:00:00Z",
	}
	out := foldKeepaliveIntoDoc(base, sa)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("result not JSON: %v", err)
	}
	if m["token"] != "new-token" {
		t.Fatalf("token = %v, want new-token", m["token"])
	}
	if m["refreshToken"] != "new-refresh" {
		t.Fatalf("refreshToken = %v, want new-refresh", m["refreshToken"])
	}
	if m["expiredAt"] != "2030-01-01T00:00:00Z" {
		t.Fatalf("expiredAt = %v", m["expiredAt"])
	}
	if m["refreshExpiredAt"] != "2031-01-01T00:00:00Z" {
		t.Fatalf("refreshExpiredAt = %v", m["refreshExpiredAt"])
	}
	// Preserved keys.
	if m["disabled"] != false || m["preserve"] != true || m["success_count"] != float64(3) || m["credential"] != "blob" {
		t.Fatalf("preserved keys lost: %v", m)
	}
}

// TestShouldRunKeepaliveNow verifies the 22:00 local-time window.
func TestShouldRunKeepaliveNow(t *testing.T) {
	loc := time.Local
	// 21:59 → no; 22:00 → yes; 22:30 → yes; 23:00 → no.
	at := func(h, m int) time.Time { return time.Date(2026, 8, 30, h, m, 0, 0, loc) }
	if shouldRunKeepaliveNow(at(21, 59)) {
		t.Fatal("21:59 must not run")
	}
	if !shouldRunKeepaliveNow(at(22, 0)) {
		t.Fatal("22:00 must run")
	}
	if !shouldRunKeepaliveNow(at(22, 30)) {
		t.Fatal("22:30 must run (within window)")
	}
	if shouldRunKeepaliveNow(at(23, 0)) {
		t.Fatal("23:00 must not run")
	}
}

// TestTokenExpiry verifies the RFC3339 parser.
func TestTokenExpiry(t *testing.T) {
	if _, ok := tokenExpiry(""); ok {
		t.Fatal("empty expiry should be not-ok")
	}
	if _, ok := tokenExpiry("garbage"); ok {
		t.Fatal("malformed expiry should be not-ok")
	}
	tt, ok := tokenExpiry("2030-01-01T00:00:00Z")
	if !ok || tt.Year() != 2030 {
		t.Fatalf("valid expiry = %v ok=%v", tt, ok)
	}
}

// TestIsRefreshDeadError classifies refresh failures: 4xx auth rejections are
// dead-token; transport/5xx are transient.
func TestIsRefreshDeadError(t *testing.T) {
	if !isRefreshDeadError("ExchangeToken: HTTP 401 unauthorized") {
		t.Fatal("401 must be dead-token")
	}
	if !isRefreshDeadError("ExchangeToken: HTTP 403 forbidden") {
		t.Fatal("403 must be dead-token")
	}
	if isRefreshDeadError("ExchangeToken: HTTP 500 internal") {
		t.Fatal("500 must NOT be dead-token")
	}
	if isRefreshDeadError("dial tcp: connection refused") {
		t.Fatal("transport error must NOT be dead-token")
	}
}
