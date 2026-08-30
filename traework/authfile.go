// authfile.go owns every physical auth-file path the plugin touches: the
// traework-<uid>.json naming rule, UID sanitization (path-traversal defense),
// path safety checks, and the read / write helpers that talk to the host's
// auth store via host.auth.* RPC. Callers above decide when to disable /
// re-enable / delete; this file decides how.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// authFilePrefix is the disk filename prefix for multi-account auth files
// (traework-<uid>.json). It is intentionally decoupled from providerName:
// the provider id is "traework-provider" (with the -provider suffix) while
// files on disk are named traework-*.json — the host identifies this
// plugin's credentials by the FILE prefix. Every reader (hostAuthList
// filter, path safety checks, import naming) must use this constant;
// filtering by providerName+"-" makes all auth files invisible to the
// panel (accounts list comes back empty while the host still routes
// traffic through those files — silent breakage, see workbuddy 0.9.6).
const authFilePrefix = "traework-"

// unsafeUIDChars matches characters that must not appear in a file name.
var unsafeUIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeUIDForFileName(uid string) string {
	uid = strings.TrimSpace(uid)
	uid = unsafeUIDChars.ReplaceAllString(uid, "_")
	if uid == "" || uid == "." || uid == ".." {
		return ""
	}
	if len(uid) > 64 {
		uid = uid[:64]
	}
	return uid
}

func authFileNameFor(a *traeAuth) string {
	if a != nil {
		if uid := sanitizeUIDForFileName(a.UserID); uid != "" {
			return authFilePrefix + uid + ".json"
		}
	}
	return authFileName
}

func isLegacyAuthName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), authFileName)
}

// hostAuthPhysical is the plugin-side view of one host auth record.
type hostAuthPhysical struct {
	AuthIndex string
	Name      string
	Path      string
	JSON      []byte
	Disabled  bool
}

// hostAuthGetPhysical fetches the physical auth record (path + raw JSON)
// from the host for one auth index.
func hostAuthGetPhysical(authIndex string) (*hostAuthPhysical, error) {
	body, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	raw, err := hostCall(pluginabi.MethodHostAuthGet, body)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.get: bad envelope")
	}
	var resp rpcHostAuthGetResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	return &hostAuthPhysical{
		AuthIndex: resp.AuthIndex,
		Name:      resp.Name,
		Path:      resp.Path,
		JSON:      resp.JSON,
		Disabled:  parseDisabledFromAuthJSON(resp.JSON),
	}, nil
}

// hostAuthPersist saves credential JSON via host.auth.save.
func hostAuthPersist(name, path string, raw []byte) error {
	_ = path // reserved for callers that still pass physical path for migrate logic
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty auth file name")
	}
	return hostAuthSaveJSON(name, raw)
}

// hostAuthSaveJSON persists raw credential JSON under name via host.auth.save.
func hostAuthSaveJSON(name string, raw []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty auth file name")
	}
	saveReq := pluginapi.HostAuthSaveRequest{
		Name: name,
		JSON: raw,
	}
	saveBody, _ := json.Marshal(saveReq)
	rawResp, err := hostCall(pluginabi.MethodHostAuthSave, saveBody)
	if err != nil {
		return fmt.Errorf("host.auth.save: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(rawResp, &env); err != nil || !env.OK {
		msg := "host.auth.save failed"
		if env.Error != nil && env.Error.Message != "" {
			msg = truncateRedacted(env.Error.Message, 200)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// buildAuthFileJSON produces the host-save payload: nested storage + top-level
// metadata. extra merges additional top-level keys (optional).
func buildAuthFileJSON(a *traeAuth, disabled bool, note string, extra map[string]any) ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("nil traeAuth")
	}
	storage, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	var nested map[string]any
	if err := json.Unmarshal(storage, &nested); err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":     providerName,
		"provider": providerName,
		"logo":     pluginLogoURL,
		"disabled": disabled,
		"note":     note,
	}
	// Flatten runtime fields into the top level (host rebuilders keep
	// unknown top-level keys; nested storage survives via `storage` too).
	for k, v := range nested {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return json.Marshal(out)
}

// parseDisabledFromAuthJSON reads the top-level disabled flag from physical
// auth JSON.
func parseDisabledFromAuthJSON(raw []byte) bool {
	var m struct {
		Disabled bool `json:"disabled"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Disabled
}

// isSafeAuthPath rejects non-traework filenames, empty paths, and traversal
// attempts. It validates both the basename pattern AND that the path does not
// escape via ".." segments.
func isSafeAuthPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if strings.Contains(filepath.ToSlash(path), "../") || strings.Contains(filepath.ToSlash(path), "/..") {
		return false
	}
	base := filepath.Base(path)
	lower := strings.ToLower(base)
	if !strings.HasPrefix(lower, authFilePrefix) && lower != "traework.json" {
		return false
	}
	if !strings.HasSuffix(lower, ".json") {
		return false
	}
	if base != filepath.Base(filepath.Clean(path)) {
		return false
	}
	return true
}

// isPathUnder reports whether path is inside dir (after cleaning both).
func isPathUnder(path, dir string) bool {
	path = strings.TrimSpace(path)
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return true
	}
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	if cleanPath == cleanDir {
		return false
	}
	rel, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && !strings.Contains(rel, string(filepath.Separator)+"..")
}

// writeAuthFileDirect writes raw JSON to a physical auth file via atomic
// temp-then-rename. This is the direct-write channel used by preserve /
// counter / lifecycle / keepalive: host.auth.save rebuilds the auth record
// and DROPS top-level fields the host doesn't recognize (preserve /
// success_count / etc.), so any plugin-owned top-level flag MUST go through
// this path and let the host's file watcher re-synthesize the record with
// the unknown fields intact. Confined to isSafeAuthPath + absolute paths.
//
// Generalized from anomaly.go's writeAnomalyFileDirect (same temp+rename
// algorithm) so all direct-write callers share one audited implementation.
func writeAuthFileDirect(path string, raw []byte) error {
	if !isSafeAuthPath(path) {
		return fmt.Errorf("refusing direct write to unsafe path: %s", path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("refusing direct write to relative path: %s", path)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".traework-auth-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace auth file: %w", err)
	}
	return nil
}

// persistAuthDirect is the high-level direct-write helper: it writes raw to
// the physical path and optionally removes a legacy single-file auth when the
// account migrated to a per-UID file (legacyPath non-empty and different).
// Mirrors workbuddy's persistAuthDirect contract (name used only for error
// messages). All plugin-owned top-level flag writers use this instead of
// host.auth.save.
func persistAuthDirect(name, path, legacyPath string, raw []byte) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("no physical auth path for %s", name)
	}
	if err := writeAuthFileDirect(path, raw); err != nil {
		return err
	}
	if legacyPath != "" && !strings.EqualFold(filepath.Base(legacyPath), filepath.Base(path)) {
		_ = deleteAuthFileInDir(legacyPath, filepath.Dir(legacyPath))
	}
	return nil
}

// deleteAuthFileInDir removes a physical auth file, requiring the path to be
// absolute, safe, and (when dir is non-empty) inside dir.
func deleteAuthFileInDir(path, dir string) error {
	if !isSafeAuthPath(path) {
		return fmt.Errorf("refusing to delete unsafe path: %s", path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("refusing to delete relative path: %s", path)
	}
	if dir != "" && !isPathUnder(path, dir) {
		return fmt.Errorf("refusing to delete path outside auth dir: %s (dir=%s)", path, dir)
	}
	err := os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}
