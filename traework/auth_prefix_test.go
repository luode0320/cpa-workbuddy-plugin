package main

import "testing"

// TestAuthFilePrefix_DecoupledFromProviderName locks in the 0.1.8 bug fix:
// disk files are named traework-<uid>.json (authFilePrefix), but hostAuthList
// filtered host.auth.list results with providerName+"-" ("traework-provider-"),
// which matched NOTHING — the panel showed an empty accounts list while the
// host still routed traffic through those files. The prefix must stay stable
// across provider-id renames; see workbuddy auth_prefix_test.go (0.9.6).
func TestAuthFilePrefix_DecoupledFromProviderName(t *testing.T) {
	if authFilePrefix != "traework-" {
		t.Fatalf("authFilePrefix=%q; this constant is the disk filename prefix and must remain stable across plugin-id renames. If you intentionally changed it, audit every test/asset/release that references the legacy prefix first.", authFilePrefix)
	}
	if providerName+"-" == authFilePrefix {
		t.Fatalf("providerName(%q)+\"-\" collides with authFilePrefix; the hostAuthList filter must use authFilePrefix only", providerName)
	}
}

// TestAuthFileNameFor_UsesPrefixConstant ensures the file-writing side and the
// list-filter side both go through the same constant.
func TestAuthFileNameFor_UsesPrefixConstant(t *testing.T) {
	want := authFilePrefix + "ab12cd34" + ".json"
	if got := authFileNameFor(&traeAuth{UserID: "ab12cd34"}); got != want {
		t.Fatalf("authFileNameFor=%q; want %q (must equal authFilePrefix+uid+'.json')", got, want)
	}
}
