package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// fakeStorageFile builds a storage.json-shaped document carrying a plaintext
// credential under the icube key (mirrors the real Trae SOLO layout without
// touching a real credential).
func fakeStorageFile(icubeVal string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"telemetry.machineId":              "m1",
		"iCubeAuthInfo://icube.cloudide":   icubeVal,
		"iCubeServerData://icube.cloudide": `{"entitlementInfo":{"identityStr":"Free"}}`,
	})
	return raw
}

// TestParseCredentialImport_WholeStorageFile verifies that a whole
// storage.json file (the shape the import button reads from disk) yields a
// fully decrypted traeAuth with token/uid/nickname populated.
func TestParseCredentialImport_WholeStorageFile(t *testing.T) {
	plain := `{"token":"tok-1","userId":"uid-1","account":{"username":"alice"}}`
	a, err := parseCredentialImport(fakeStorageFile(plain))
	if err != nil {
		t.Fatalf("parseCredentialImport(whole storage.json) = %v", err)
	}
	if a.Token != "tok-1" {
		t.Fatalf("token = %q, want tok-1", a.Token)
	}
	if a.UserID != "uid-1" {
		t.Fatalf("uid = %q, want uid-1", a.UserID)
	}
	if a.Nickname != "alice" {
		t.Fatalf("nickname = %q, want alice", a.Nickname)
	}
	if !a.hasToken() {
		t.Fatal("parsed auth should carry a usable token")
	}
}

// TestParseCredentialImport_RawValue verifies the paste-style input (raw
// plaintext credential JSON) still parses directly.
func TestParseCredentialImport_RawValue(t *testing.T) {
	plain := `{"token":"tok-2","userId":"uid-2"}`
	a, err := parseCredentialImport([]byte(plain))
	if err != nil {
		t.Fatalf("parseCredentialImport(raw value) = %v", err)
	}
	if a.Token != "tok-2" || a.UserID != "uid-2" {
		t.Fatalf("unexpected auth: token=%q uid=%q", a.Token, a.UserID)
	}
}

// TestParseCredentialImport_NoIcubeKey verifies a storage.json without the
// icube credential key is rejected with a clear error.
func TestParseCredentialImport_NoIcubeKey(t *testing.T) {
	if _, err := parseCredentialImport([]byte(`{"foo":1}`)); err == nil {
		t.Fatal("expected error for storage.json without icube key")
	}
}

// TestParseCredentialImport_WholeFileWithoutCredential verifies that a
// storage.json whose icube value is not a credential string is rejected.
func TestParseCredentialImport_WholeFileWithoutCredential(t *testing.T) {
	if _, err := parseCredentialImport(fakeStorageFile("not-a-credential")); err == nil {
		t.Fatal("expected error for non-credential icube value")
	}
}

// TestStorageGlobalDir_UsesAPPDATA verifies the directory probe prefers the
// %APPDATA% env var and appends the Trae SOLO relative path.
func TestStorageGlobalDir_UsesAPPDATA(t *testing.T) {
	t.Setenv("APPDATA", `C:\Users\test\AppData\Roaming`)
	got := storageGlobalDir()
	want := filepath.Join(`C:\Users\test\AppData\Roaming`, "TRAE SOLO CN", "User", "globalStorage")
	if got != want {
		t.Fatalf("storageGlobalDir() = %q, want %q", got, want)
	}
}

// TestStorageGlobalDir_FallbackHome verifies the home-dir fallback when
// APPDATA is unset.
func TestStorageGlobalDir_FallbackHome(t *testing.T) {
	t.Setenv("APPDATA", "")
	got := storageGlobalDir()
	if got == "" || got == "%APPDATA%\\TRAE SOLO CN\\User\\globalStorage" {
		t.Fatalf("storageGlobalDir() fallback looks wrong: %q", got)
	}
}
