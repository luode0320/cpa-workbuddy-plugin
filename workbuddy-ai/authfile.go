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

var unsafeUIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// authFilePrefix is "workbuddyai-" to strictly avoid substring clash with domestic "workbuddy-".
const authFilePrefix = "workbuddyai-"

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

func isLegacyWorkbuddyAuthName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), authFileName)
}

func isWorkbuddyAuthFileName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" || !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.HasPrefix(base, authFilePrefix) || base == authFileName
}

func resolveAuthFileTarget(sa *storedAuth, phys *hostAuthPhysical) (name, path string, legacyPath string) {
	name = authFileNameFor(sa)
	if phys != nil {
		path = strings.TrimSpace(phys.Path)
		physName := strings.TrimSpace(phys.Name)
		if physName != "" && !isLegacyWorkbuddyAuthName(physName) {
			name = physName
		}
		if isLegacyWorkbuddyAuthName(physName) || isLegacyWorkbuddyAuthName(filepath.Base(path)) {
			if sa != nil && strings.TrimSpace(sa.Account.UID) != "" {
				legacyPath = path
			}
		}
	}
	return name, path, legacyPath
}

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

func writeAuthFileDirect(path string, raw []byte) error {
	if !isSafeWorkbuddyAuthPath(path) {
		return fmt.Errorf("refusing direct write to unsafe path: %s", path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("refusing direct write to relative path: %s", path)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".workbuddyai-write-*.tmp")
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

func deleteAuthFileInDir(filePath, dir string) error {
	filePath = strings.TrimSpace(filePath)
	dir = strings.TrimSpace(dir)
	if filePath == "" || dir == "" {
		return fmt.Errorf("empty path or directory")
	}
	cleanFile := filepath.Clean(filePath)
	cleanDir := filepath.Clean(dir)
	rel, err := filepath.Rel(cleanDir, cleanFile)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("unsafe delete path: outside directory")
	}
	if err := os.Remove(cleanFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

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

func isSafeWorkbuddyAuthPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if strings.Contains(filepath.ToSlash(path), "../") || strings.Contains(filepath.ToSlash(path), "/..") {
		return false
	}
	base := filepath.Base(path)
	lower := strings.ToLower(base)
	if !strings.HasPrefix(lower, authFilePrefix) && lower != authFileName {
		return false
	}
	if !strings.HasSuffix(lower, ".json") {
		return false
	}
	return true
}

func isPathUnder(path, dir string) bool {
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	rel, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// mergeAuthDoc replaces the nested auth/account of an existing physical auth
// doc with refreshed values while preserving every other top-level key.
func mergeAuthDoc(raw []byte, sa *storedAuth) ([]byte, error) {
	if sa == nil {
		return nil, fmt.Errorf("nil storedAuth")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		doc = map[string]any{}
	}
	storage, err := json.Marshal(sa)
	if err != nil {
		return nil, err
	}
	var nested map[string]any
	if err := json.Unmarshal(storage, &nested); err != nil {
		return nil, err
	}
	doc["auth"] = nested["auth"]
	doc["account"] = nested["account"]
	return json.Marshal(doc)
}

// parseDisabledFromAuthJSON reads top-level disabled from physical auth JSON.
func parseDisabledFromAuthJSON(raw []byte) bool {
	var m struct {
		Disabled bool `json:"disabled"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Disabled
}

// manualDisableFromAuthJSON reads the top-level manual_disable flag.
func manualDisableFromAuthJSON(raw []byte) bool {
	var m struct {
		ManualDisable bool `json:"manual_disable"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.ManualDisable
}

// exhaustedDisableFromAuthJSON reads the top-level exhausted_disable flag.
func exhaustedDisableFromAuthJSON(raw []byte) bool {
	var m struct {
		ExhaustedDisable bool `json:"exhausted_disable"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.ExhaustedDisable
}

// hostAuthPersist persists credential JSON via host.auth.save.
func hostAuthPersist(name, path string, raw []byte) error {
	_ = path
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty auth file name")
	}
	return hostAuthSaveJSON(name, raw)
}

// hostAuthPersistMigrate is like hostAuthPersist but also removes a legacy path
// when the canonical name differs.
func hostAuthPersistMigrate(name, path, legacyPath string, raw []byte) error {
	if err := hostAuthPersist(name, path, raw); err != nil {
		return err
	}
	if legacyPath != "" && !strings.EqualFold(filepath.Base(legacyPath), name) {
		if isLegacyWorkbuddyAuthName(filepath.Base(legacyPath)) {
			_ = deleteAuthFileInDir(legacyPath, filepath.Dir(legacyPath))
		}
	}
	return nil
}
