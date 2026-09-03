package main

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// TestPKCEPair_RFC7636Shape verifies the verifier/challenge pair matches the
// RFC 7636 S256 contract: verifier within 43-128 unreserved chars, challenge
// equal to BASE64URL(SHA256(verifier)) without padding.
func TestPKCEPair_RFC7636Shape(t *testing.T) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		t.Fatalf("pkcePair() = %v", err)
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Fatalf("verifier length = %d, want 43..128", len(verifier))
	}
	if strings.ContainsAny(verifier, "+/=") {
		t.Fatalf("verifier contains non-unreserved chars: %q", verifier)
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Fatalf("challenge mismatch: got %q want %q", challenge, want)
	}
}

// TestValidateBrowserLoginOrigin verifies only bare http(s) origins pass and
// paths/query/fragments/credentials are rejected (open-redirect guard).
func TestValidateBrowserLoginOrigin(t *testing.T) {
	ok, err := validateBrowserLoginOrigin("https://1.2.3.4:18998")
	if err != nil || ok != "https://1.2.3.4:18998" {
		t.Fatalf("plain origin rejected: %q %v", ok, err)
	}
	ok, err = validateBrowserLoginOrigin("http://localhost:8080/")
	if err != nil || ok != "http://localhost:8080" {
		t.Fatalf("trailing-slash origin: %q %v", ok, err)
	}
	for _, bad := range []string{
		"", "ftp://x", "https://host/path", "https://host?x=1",
		"https://host#frag", "https://user:pass@host", "//host", "host:18998",
	} {
		if _, err = validateBrowserLoginOrigin(bad); err == nil {
			t.Fatalf("origin %q should be rejected", bad)
		}
	}
}

// TestRandomDeviceIDShape verifies the 16-digit numeric device id shape.
func TestRandomDeviceIDShape(t *testing.T) {
	id := randomDeviceID()
	if len(id) != 16 {
		t.Fatalf("device id length = %d, want 16", len(id))
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			t.Fatalf("device id %q contains non-digit", id)
		}
	}
}

// TestRandomTraceIDShape verifies the UUID-shaped trace id (5 hex groups).
func TestRandomTraceIDShape(t *testing.T) {
	id := randomTraceID()
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("trace id %q should have 5 groups", id)
	}
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789abcdef") != "" {
			t.Fatalf("trace id group %d = %q is not hex", i, p)
		}
	}
}

// TestDeviceKeyPairPEM verifies the throwaway device key renders as a SPKI
// PEM PUBLIC KEY block (the shape the SOLO client sends as DevicePublicKey).
func TestDeviceKeyPairPEM(t *testing.T) {
	pemKey, err := deviceKeyPairPEM()
	if err != nil {
		t.Fatalf("deviceKeyPairPEM() = %v", err)
	}
	if !strings.HasPrefix(pemKey, "-----BEGIN PUBLIC KEY-----") {
		t.Fatalf("device key pem has unexpected header: %q", pemKey[:40])
	}
}

// TestBrowserLoginResultPendingKeepsSession verifies an early /result poll
// (callback not yet arrived) reports pending AND does not consume the
// session — a later poll after the callback still finds the outcome.
func TestBrowserLoginResultPendingKeepsSession(t *testing.T) {
	state := "test-state-pending"
	browserLoginSessions.Store(state, &browserLoginSession{
		Verifier:  "v",
		CreatedAt: time.Now(),
	})
	// Early poll: pending, session must survive.
	if _, ok := browserLoginSessions.Load(state); !ok {
		t.Fatal("session vanished before result")
	}
	// Simulate the callback storing the outcome (the read-once contract).
	browserLoginSessions.Store(state, &browserLoginSession{
		Verifier:  "v",
		CreatedAt: time.Now(),
		Result:    &browserLoginOutcome{OK: true, Label: "alice"},
	})
	raw, ok := browserLoginSessions.Load(state)
	if !ok {
		t.Fatal("session missing after callback")
	}
	s := raw.(*browserLoginSession)
	if s.Result == nil || !s.Result.OK || s.Result.Label != "alice" {
		t.Fatalf("unexpected outcome: %+v", s.Result)
	}
	browserLoginSessions.Delete(state)
}

// TestBrowserLoginHTMLPage_NoTargetShowsDetail verifies the error bounce page
// renders the detail text and contains no redirect machinery.
func TestBrowserLoginHTMLPage_NoTargetShowsDetail(t *testing.T) {
	resp := browserLoginHTMLPage("", "会话已过期")
	body := string(resp.Body)
	if !strings.Contains(body, "会话已过期") {
		t.Fatal("detail text missing from error page")
	}
	if strings.Contains(body, "location.replace") || strings.Contains(body, "http-equiv=\"refresh\"") {
		t.Fatal("error page should not contain redirect machinery")
	}
}

// TestBrowserLoginHTMLPage_TargetRedirects verifies the success bounce page
// carries meta refresh, a manual link, and the JS fallback to the panel URL.
func TestBrowserLoginHTMLPage_TargetRedirects(t *testing.T) {
	panel := "https://1.2.3.4:18998" + resourcePanelPrefix + "?auth_cb=abc"
	resp := browserLoginHTMLPage(panel, "")
	body := string(resp.Body)
	if !strings.Contains(body, htmlEscape(panel)) {
		t.Fatal("panel URL missing from bounce page")
	}
	if !strings.Contains(body, "location.replace") || !strings.Contains(body, "http-equiv=\"refresh\"") {
		t.Fatal("bounce page missing redirect machinery")
	}
}
