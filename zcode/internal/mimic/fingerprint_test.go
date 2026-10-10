package mimic

import (
	"net/http"
	"strings"
	"testing"
)

func TestFingerprint_ApplyRequest(t *testing.T) {
	cfg := DefaultConfig()
	fp := New(cfg, "testkey123")

	h := make(http.Header)
	fp.ApplyRequest(h, "testkey123.secret", "req-1")

	if h.Get("x-api-key") != "testkey123.secret" {
		t.Fatalf("expected x-api-key, got %s", h.Get("x-api-key"))
	}
	if h.Get("http-referer") != "https://zcode.z.ai" {
		t.Fatalf("expected referer, got %s", h.Get("http-referer"))
	}
	if h.Get("User-Agent") != "ZCode/3.14.4" {
		t.Fatalf("expected User-Agent, got %s", h.Get("User-Agent"))
	}
	if h.Get("x-zcode-agent") != "glm" {
		t.Fatalf("expected agent glm, got %s", h.Get("x-zcode-agent"))
	}
	if h.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("expected anthropic-version, got %s", h.Get("anthropic-version"))
	}
	if h.Get("x-request-id") != "req-1" {
		t.Fatalf("expected request id req-1, got %s", h.Get("x-request-id"))
	}
	if h.Get("x-query-id") == "" {
		t.Fatalf("expected query id to be non-empty")
	}
	if h.Get("x-session-id") == "" {
		t.Fatalf("expected session id to be non-empty")
	}
}

func TestFingerprint_Disabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	fp := New(cfg, "testkey")

	h := make(http.Header)
	fp.ApplyRequest(h, "my-api-key", "req-2")

	if h.Get("x-api-key") != "my-api-key" {
		t.Fatalf("expected x-api-key, got %s", h.Get("x-api-key"))
	}
	if h.Get("User-Agent") != "" {
		t.Fatalf("expected empty User-Agent when disabled, got %s", h.Get("User-Agent"))
	}
}

func TestUUID_Formats(t *testing.T) {
	u4 := UUID4()
	parts := strings.Split(u4, "-")
	if len(parts) != 5 {
		t.Fatalf("expected 5 uuid parts, got %d for %s", len(parts), u4)
	}

	u7 := UUID7()
	parts7 := strings.Split(u7, "-")
	if len(parts7) != 5 {
		t.Fatalf("expected 5 uuid parts, got %d for %s", len(parts7), u7)
	}
}
