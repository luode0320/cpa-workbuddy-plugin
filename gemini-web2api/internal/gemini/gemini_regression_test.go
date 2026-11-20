package gemini

import (
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeCookieSubdomainSharingAndHostPrefix(t *testing.T) {
	client, err := NewClient(AccountSource{
		ID: "test",
		Cookies: []Cookie{
			{Name: "__Secure-1PSID", Value: "psid-val", Domain: "google.com", Path: "/", Secure: true},
			{Name: "__Host-GAPS", Value: "gaps-val", Domain: "accounts.google.com", Path: "/", Secure: true},
		},
	}, "", false)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	geminiURL, _ := url.Parse("https://gemini.google.com/app")
	gotGemini := client.httpClient.GetCookies(geminiURL)
	foundPSID := false
	for _, c := range gotGemini {
		if c.Name == "__Secure-1PSID" && c.Value == "psid-val" {
			foundPSID = true
		}
		if c.Name == "__Host-GAPS" {
			t.Fatalf("__Host- cookie for accounts.google.com must not leak to gemini.google.com")
		}
	}
	if !foundPSID {
		t.Fatalf("expected cookie with domain 'google.com' to be sent to gemini.google.com")
	}
}

func TestIsDisallowedRemoteIPBlocksPrivateAndMetadataRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"::1",
		"10.0.0.8",
		"172.16.5.4",
		"192.168.1.1",
		"169.254.169.254",
		"100.64.0.1",
		"0.0.0.0",
		"::ffff:127.0.0.1",
	}
	for _, raw := range blocked {
		if !IsDisallowedRemoteIP(netip.MustParseAddr(raw)) {
			t.Fatalf("expected IP %s to be disallowed", raw)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, raw := range allowed {
		if IsDisallowedRemoteIP(netip.MustParseAddr(raw)) {
			t.Fatalf("expected public IP %s to be allowed", raw)
		}
	}
}

func TestVariantModelGuardWithReconstructedTurn(t *testing.T) {
	base := Model{ID: "gemini-4-carbon", DisplayName: "4 Carbon", Hash: "hash_default", Mode: 3, Default: true}
	task := Model{ID: "gemini-4-carbon-task", DisplayName: "4 Carbon Task", Hash: "var_carbon", Mode: 3, Variant: "var_carbon", Feature: FeatureTask}
	catalog := ModelCatalog{
		models:       []Model{base, task},
		lookup:       map[string]int{"gemini-4-carbon": 0, "gemini-4-carbon-task": 1},
		defaultIndex: 0,
	}

	if !catalog.MatchesBaseOrDefaultHash(task, "hash_default") {
		t.Fatalf("expected task variant to match base/default hash")
	}
	if catalog.MatchesBaseOrDefaultHash(task, "hash_downgraded_flash") {
		t.Fatalf("expected unrelated model hash not to match variant")
	}

	guard := newModelGuard(task, func(Event) error { return nil })
	meta := EventMetadataData{
		ModelHash: "hash_default",
		ModelName: strings.TrimSuffix(task.DisplayName, " Task"),
	}
	if err := guard.Emit(Event{Kind: EventMetadata, Metadata: &meta}); err != nil {
		t.Fatalf("expected guard.Emit to accept matching variant turn, got %v", err)
	}
	if err := guard.Complete(); err != nil {
		t.Fatalf("expected guard.Complete to succeed, got %v", err)
	}
}

func TestRenderWidgetsFastPathAndPendingPrefix(t *testing.T) {
	codeWithAngles := "if a < b && c < d {\n\treturn a\n}"
	if got := renderWidgets(nil, codeWithAngles); got != codeWithAngles {
		t.Fatalf("expected fast path to preserve code with '<', got %q", got)
	}

	withPendingTag := "Here are the steps:\n<Seq"
	if got := renderWidgets(nil, withPendingTag); got != "Here are the steps:" {
		t.Fatalf("expected trailing incomplete widget prefix to be held, got %q", got)
	}
}
