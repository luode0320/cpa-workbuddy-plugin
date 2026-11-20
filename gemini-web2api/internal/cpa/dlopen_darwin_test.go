//go:build darwin

package cpa_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/cpa"
)

func TestDynamicLibraryDLOpenABI(t *testing.T) {
	dylibPath, err := filepath.Abs(filepath.Join("..", "..", "dist", "plugins", "darwin", "arm64", "gemini-web2api.dylib"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dylibPath); err != nil {
		t.Skipf("dylib not built at %s: %v", dylibPath, err)
	}

	got, err := cpa.CallSharedLibraryABI(dylibPath, "plugin.register", `{"schema_version":10}`)
	if err != nil {
		t.Fatalf("CallSharedLibraryABI failed: %v", err)
	}
	if !strings.Contains(got, `"ok":true`) || !strings.Contains(got, `"Name":"gemini-web2api"`) || !strings.Contains(got, `"Version":"`+expectedPluginVersion(t)+`"`) {
		t.Fatalf("unexpected dlopen response from plugin.register: %s", got)
	}
	t.Logf("dlopen C ABI plugin.register response: %s", got)
}
