package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
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

// TestIsRefreshDeadError classifies refresh failures: only business-level
// 401/403 on the correct auth host are dead-token. 404 is a gateway page for
// a route that does not exist on that host (the 2026-09-05 mass-disable
// incident), 400 is parameter validation, 5xx/transport are transient.
func TestIsRefreshDeadError(t *testing.T) {
	if !isRefreshDeadError("ExchangeToken: HTTP 401 unauthorized") {
		t.Fatal("401 must be dead-token")
	}
	if !isRefreshDeadError("ExchangeToken: HTTP 403 forbidden") {
		t.Fatal("403 must be dead-token")
	}
	if isRefreshDeadError("ExchangeToken: HTTP 404 <html>TLB 404 Not Found</html>") {
		t.Fatal("404 (missing route / gateway page) must NOT be dead-token — it killed healthy accounts on 2026-09-05")
	}
	if isRefreshDeadError("ExchangeToken: HTTP 400 {\"code\":10101,\"message\":\"无效参数\"}") {
		t.Fatal("400 (parameter validation) must NOT be dead-token")
	}
	if isRefreshDeadError("ExchangeToken: HTTP 500 internal") {
		t.Fatal("500 must NOT be dead-token")
	}
	if isRefreshDeadError("dial tcp: connection refused") {
		t.Fatal("transport error must NOT be dead-token")
	}
}

// TestKeepaliveExchangeHostFixed pins the ExchangeToken auth host to
// defaultAPIHost (api.trae.cn) regardless of the account's stored sa.Host —
// reusing the chat/account host caused the 2026-09-05 mass-disable incident
// (TLB 404 on trae-api-cn.mchost.guru was misread as a dead refresh token).
func TestKeepaliveExchangeHostFixed(t *testing.T) {
	orig := hostHTTPDoFn
	var capturedURL string
	hostHTTPDoFn = func(req *http.Request) (*hostHTTPResponse, error) {
		capturedURL = req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
		return nil, fmt.Errorf("stub: no network in tests")
	}
	t.Cleanup(func() { hostHTTPDoFn = orig })

	sa := &traeAuth{
		Host:         "https://some-chat-host.example.com",
		RefreshToken: "tok",
		UserID:       "u1",
	}
	if _, err := keepaliveExchange(sa); err == nil {
		t.Fatal("expected stub error from keepaliveExchange")
	}
	want := defaultAPIHost + "/cloudide/api/v3/trae/oauth/ExchangeToken"
	if capturedURL != want {
		t.Fatalf("ExchangeToken host must be fixed to %s, got %s", want, capturedURL)
	}
}

// retryTargetsLen mirrors retryFailedRefreshes' target filter: how many
// accounts would the 10-minute retry tick pick up right now.
func retryTargetsLen() int {
	refreshRetryMu.Lock()
	defer refreshRetryMu.Unlock()
	n := 0
	for _, e := range refreshRetry {
		if e.retryable && !e.maxed && !e.inFlight && e.attempts < refreshRetryMax {
			n++
		}
	}
	return n
}

// TestRefreshRetryBudget verifies the per-account 50-attempt daily budget:
// failures consume it, exhaustion removes the account from the retry
// scheduler, and a successful refresh resets everything.
func TestRefreshRetryBudget(t *testing.T) {
	orig := refreshOneAuthFn
	calls := 0
	refreshOneAuthFn = func(authIndex, authID string) (string, error) {
		calls++
		return "failed", fmt.Errorf("ExchangeToken: HTTP 400 bad param")
	}
	t.Cleanup(func() { refreshOneAuthFn = orig })
	resetRetryBudgets()
	t.Cleanup(resetRetryBudgets)

	for i := 0; i < refreshRetryMax; i++ {
		if st, _ := refreshAuthGuarded("idx-a", "id-a"); st != "failed" {
			t.Fatalf("attempt %d: want failed, got %s", i+1, st)
		}
	}
	if calls != refreshRetryMax {
		t.Fatalf("expected exactly %d underlying refresh calls, got %d", refreshRetryMax, calls)
	}
	if n := retryTargetsLen(); n != 0 {
		t.Fatalf("exhausted account must not be retried, targets=%d", n)
	}

	// A successful refresh resets the budget (retryable cleared).
	refreshOneAuthFn = func(authIndex, authID string) (string, error) {
		return "refreshed", nil
	}
	if st, _ := refreshAuthGuarded("idx-a", "id-a"); st != "refreshed" {
		t.Fatalf("want refreshed, got %s", st)
	}
	if n := retryTargetsLen(); n != 0 {
		t.Fatalf("successful account must not be retryable, targets=%d", n)
	}
}

// TestPerAuthRefreshMutex verifies the user-mandated rule that the same
// account is never refreshed concurrently: 10 parallel guarded calls for one
// authID must serialize to a peak in-flight count of exactly 1.
func TestPerAuthRefreshMutex(t *testing.T) {
	orig := refreshOneAuthFn
	var cur, peak int32
	refreshOneAuthFn = func(authIndex, authID string) (string, error) {
		c := atomic.AddInt32(&cur, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if c <= p || atomic.CompareAndSwapInt32(&peak, p, c) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond) // widen the race window
		atomic.AddInt32(&cur, -1)
		return "failed", fmt.Errorf("ExchangeToken: HTTP 400 bad param")
	}
	t.Cleanup(func() { refreshOneAuthFn = orig })
	resetRetryBudgets()
	t.Cleanup(resetRetryBudgets)

	const n = 10
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = refreshAuthGuarded("idx-m", "id-m")
		}()
	}
	wg.Wait()

	if atomic.LoadInt32(&peak) != 1 {
		t.Fatalf("same-account refresh must never run concurrently, peak=%d", peak)
	}
	// Only 1 of the 10 calls actually executed: the other 9 hit the in-flight
	// mutex and were rejected as skipped (NOT queued), so exactly 1 attempt
	// is recorded.
	refreshRetryMu.Lock()
	e := refreshRetry["id-m"]
	refreshRetryMu.Unlock()
	if e == nil || e.attempts != 1 {
		t.Fatalf("expected exactly 1 recorded attempt (9 concurrent calls must be rejected, not queued), got %+v", e)
	}
}
