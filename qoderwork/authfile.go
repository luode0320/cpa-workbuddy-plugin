// authfile.go owns every physical auth-file path the plugin touches: the
// qoderwork-<uid>.json naming rule, UID sanitization (path-traversal defense),
// path safety checks, and the read / write / delete helpers that talk to the
// host's auth store via host.auth.* RPC. Callers above (lifecycle reconcile)
// decide when to disable / re-enable / delete; this file decides how.
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
// (qoderwork-<uid>.json). It is intentionally decoupled from providerName:
// the provider id is "qoderwork-provider" (with the -provider suffix) while
// files on disk are named qoderwork-*.json — the host identifies this
// plugin's credentials by the FILE prefix. Every reader (hostAuthList
// filter, models prefix match, path safety checks) must use this constant;
// filtering by providerName+"-" makes all auth files invisible to the
// panel (same silent-breakage class as traework 0.1.8 / workbuddy 0.9.6).
const authFilePrefix = "qoderwork-"

// authFileNameFor matches toAuthData naming: always qoderwork-<uid>.json when UID is known.
// Bare "qoderwork.json" is legacy single-account only (no UID).
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

func authFileNameFor(sa *storedAuth) string {
	if sa != nil {
		if uid := sanitizeUIDForFileName(sa.Account.UID); uid != "" {
			return authFilePrefix + uid + ".json"
		}
	}
	return authFileName
}

// isLegacyAuthName reports the historical single-file name that collides
// with multi-account qoderwork-<uid>.json for the same credential.

func isLegacyAuthName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), authFileName)
}

// isQoderworkAuthFileName reports whether a bare filename (no directory)
// follows the qoderwork naming rule: qoderwork-<uid>.json or the legacy
// qoderwork.json. Case-insensitive. Used by the panel delete path to assert
// QoderWork ownership before touching a physical file.
// （同步自 workbuddy 0.14.7 账号删除功能）
func isQoderworkAuthFileName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" || !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.HasPrefix(base, authFilePrefix) || base == "qoderwork.json"
}

// resolveAuthFileTarget picks the canonical file name + path for save/delete.
// Prefer qoderwork-<uid>.json; if the host still points at legacy qoderwork.json
// for a UID-bearing account, rewrite to the uid name and schedule legacy removal.

func resolveAuthFileTarget(sa *storedAuth, phys *hostAuthPhysical) (name, path string, legacyPath string) {
	name = authFileNameFor(sa)
	if phys != nil {
		path = strings.TrimSpace(phys.Path)
		physName := strings.TrimSpace(phys.Name)
		if physName != "" && !isLegacyAuthName(physName) {
			// Already on multi-account name — keep host name (should match uid form).
			name = physName
		}
		if isLegacyAuthName(physName) || isLegacyAuthName(filepath.Base(path)) {
			if sa != nil && strings.TrimSpace(sa.Account.UID) != "" {
				// Migrate: write canonical, delete legacy path after save.
				legacyPath = path
				if isLegacyAuthName(filepath.Base(path)) {
					// path stays legacy until we write canonical beside it
				}
				// After persist to name, remove legacyPath if different.
			}
		}
	}
	return name, path, legacyPath
}

// hostAuthPersist saves via host API only. Dual-writing the physical path after
// a successful host.auth.save is redundant (host already WriteFile) and can
// re-fire the watcher → extra re-parse / transient dual registration risk.

type hostAuthPhysical struct {
	AuthIndex string
	Name      string
	Path      string
	JSON      []byte
	Disabled  bool
}

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

// hostAuthSaveJSON persists credential JSON via host.auth.save.

func hostAuthPersist(name, path string, raw []byte) error {
	_ = path // reserved for callers that still pass physical path for migrate logic
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty auth file name")
	}
	return hostAuthSaveJSON(name, raw)
}

// hostAuthPersistMigrate is like hostAuthPersist but also removes a legacy path
// when the canonical name differs (qoderwork.json → qoderwork-<uid>.json).

func hostAuthPersistMigrate(name, path, legacyPath string, raw []byte) error {
	if err := hostAuthPersist(name, path, raw); err != nil {
		return err
	}
	// If path was legacy and name is canonical, also write canonical path next to it.
	if legacyPath != "" && !strings.EqualFold(filepath.Base(legacyPath), name) {
		// host.auth.save already wrote name under auth dir; drop legacy file.
		// A-36: use deleteAuthFileInDir (abs path + dir confine) for consistency.
		if isLegacyAuthName(filepath.Base(legacyPath)) {
			_ = deleteAuthFileInDir(legacyPath, filepath.Dir(legacyPath))
		}
	}
	// If path points at legacy but name is uid form, do not dual-write path (would keep legacy alive).
	return nil
}

// buildAuthFileJSON produces host-save payload: nested storage + top-level metadata.
// extra merges additional top-level keys (optional).

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

// lifecycleStateUnchanged avoids redundant saves when note/disabled unchanged.

func buildAuthFileJSON(sa *storedAuth, disabled bool, note string, extra map[string]any) ([]byte, error) {
	if sa == nil {
		return nil, fmt.Errorf("nil storedAuth")
	}
	storage, err := json.Marshal(sa)
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
		"auth":     nested["auth"],
		"account":  nested["account"],
	}
	for k, v := range extra {
		out[k] = v
	}
	return json.Marshal(out)
}

// parseDisabledFromAuthJSON reads top-level disabled from physical auth JSON.

func parseDisabledFromAuthJSON(raw []byte) bool {
	var m struct {
		Disabled bool `json:"disabled"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Disabled
}

// isSafeAuthPath rejects non-qoderwork filenames, empty paths, and
// traversal attempts. It validates both the basename pattern AND that the path
// does not escape via ".." segments. Callers that need to confine deletes to
// a specific directory should additionally check isPathUnder(path, dir).

func isSafeAuthPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	// Reject any path containing ".." — prevents traversal regardless of basename.
	if strings.Contains(filepath.ToSlash(path), "../") || strings.Contains(filepath.ToSlash(path), "/..") {
		return false
	}
	base := filepath.Base(path)
	lower := strings.ToLower(base)
	if !strings.HasPrefix(lower, authFilePrefix) && lower != "qoderwork.json" {
		return false
	}
	if !strings.HasSuffix(lower, ".json") {
		return false
	}
	// Path traversal / absolute weirdness: base must equal cleaned base.
	if base != filepath.Base(filepath.Clean(path)) {
		return false
	}
	return true
}

// isPathUnder reports whether path is inside dir (after cleaning both).
// Empty dir means "no constraint" (returns true for any safe path).

func isPathUnder(path, dir string) bool {
	path = strings.TrimSpace(path)
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return true
	}
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	if cleanPath == cleanDir {
		return false // path is the dir itself, not under it
	}
	rel, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && !strings.Contains(rel, string(filepath.Separator)+"..")
}

// deleteAuthFileInDir is like deleteAuthFileAt but additionally requires the
// path to be under dir. Use for lifecycle deletes where the auth directory is
// known — prevents a malicious/buggy host path from deleting arbitrary files.
// The path MUST be absolute (defense against relative-path CWD deletion).

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

// writeAuthFileDirect atomically replaces the physical auth file at path with
// raw, via a temp file + rename in the same directory. Refuses relative or
// unsafe paths. （同步自 workbuddy 0.14.10 计数持久化：host.auth.save 会丢
// 未知顶层字段，计数器/保号/异常标记落盘一律走直写通道）
func writeAuthFileDirect(path string, raw []byte) error {
	if !isSafeAuthPath(path) {
		return fmt.Errorf("refusing direct write to unsafe path: %s", path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("refusing direct write to relative path: %s", path)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".qoderwork-write-*.tmp")
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

// persistAuthDirect writes raw to the physical auth path (if known) and drops
// a legacy sibling file when the canonical name differs. It fails loudly when
// no physical path is available — callers must never fall back to host.auth.save
// for disable/re-enable writes, since that channel re-enables the account.
// （同步自 workbuddy 0.14.10）
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
