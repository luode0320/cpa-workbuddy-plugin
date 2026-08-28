// import.go implements the panel credential import for traework-provider.
//
// Trae Work SOLO stores its credential inside the client's storage.json
// under the "iCubeAuthInfo://icube.cloudide" key (tc-header encrypted blob
// or plaintext JSON). Users can paste that single value through the host's
// paste flow, or — with the panel import button — pick the whole
// storage.json file directly. This file owns the "whole file → credential"
// extraction and the import management route that persists the parsed auth
// into the host auth store under a traework-<uid>.json name.
//
// Why not reuse the host AuthParse flow for the file import? The host calls
// AuthParse with the raw file payload; a tc blob (base64, not JSON) makes
// the plugin's ownership probe fail and the host then tags the record as an
// unknown "other" type. Importing inside the plugin keeps the filename
// prefix and top-level type/provider fields under our control, so the
// imported account is always recognized as traework.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// icubeCloudideKey is the storage.json key holding the Trae SOLO credential
// blob. It mirrors the authKey constant in credential.go (same literal).
const icubeCloudideKey = "iCubeAuthInfo://icube.cloudide"

// parseCredentialImport turns user-supplied credential content into a
// traeAuth. Two input shapes are accepted:
//
//  1. the raw credential value (tc blob / plaintext JSON) — the same shape
//     the paste flow accepts;
//  2. the whole storage.json file — the iCubeAuthInfo://icube.cloudide
//     value is extracted and parsed.
//
// [参数]
//   - content: 用户导入的原始内容（storage.json 整文件或单条凭据值）
//
// [返回]
//   - *traeAuth: 解析成功后的插件侧认证模型；error: 两种形状都无法解析时返回错误
func parseCredentialImport(content []byte) (*traeAuth, error) {
	// Shape 1: raw credential value (flat / nested paste shapes).
	if a, err := parseTraeAuth(content); err == nil {
		// The flat path can swallow a plaintext Trae credential JSON
		// ({token, userId, account}) because traeAuth maps userId→uid and
		// would leave UserID/Nickname empty. Re-parse with the credential
		// schema when identity fields are missing.
		if a.UserID == "" && len(content) > 0 && content[0] == '{' {
			if ca := parsePlainCredential(content); ca != nil {
				return ca, nil
			}
		}
		return a, nil
	}
	// Shape 2: whole storage.json → extract the icube credential value.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(content, &m); err != nil {
		return nil, fmt.Errorf("storage_parse_error: %w", err)
	}
	rawVal, ok := m[icubeCloudideKey]
	if !ok {
		return nil, fmt.Errorf("no %s key in storage.json", icubeCloudideKey)
	}
	var val string
	if err := json.Unmarshal(rawVal, &val); err != nil || trimSpace(val) == "" {
		return nil, fmt.Errorf("%s value is not a credential string", icubeCloudideKey)
	}
	// Wrap the extracted value into the flat shape parseTraeAuth expects
	// ({credential: <blob-or-json>}); its decrypt path fills token/uid/
	// nickname from the parsed credential.
	wrapped, err := json.Marshal(map[string]string{"credential": val})
	if err != nil {
		return nil, err
	}
	return parseTraeAuth(wrapped)
}

// parsePlainCredential parses a plaintext Trae credential JSON (the shape
// traeCredential uses: token / userId / account / host) into a traeAuth.
// Returns nil when the content is not such a credential.
//
// [参数]
//   - content: 明文 Trae 凭据 JSON（token/userId/account 键）
//
// [返回]
//   - *traeAuth: 解析成功且含有效 token 的认证模型；否则 nil
func parsePlainCredential(content []byte) *traeAuth {
	var cred traeCredential
	if err := json.Unmarshal(content, &cred); err != nil {
		return nil
	}
	a := &traeAuth{
		Token:         cred.Token,
		RefreshToken:  cred.RefreshToken,
		ExpiredAt:     cred.ExpiredAt,
		UserID:        cred.UserID,
		Nickname:      cred.accountName(),
		Host:          cred.Host,
		CredentialRaw: string(content),
	}
	if !a.hasToken() {
		return nil
	}
	return a
}

// handleImportCredential implements POST /plugins/traework-provider/import.
//
// Body: {filename, content}. The content is parsed (whole storage.json or
// raw credential value), deduplicated by UserID against the host auth list,
// and persisted via host.auth.save as traework-<uid>.json. Duplicate
// imports are reported without writing a second record.
//
// [参数]
//   - req: 宿主转发来的管理 API 请求（Body 为 {filename, content} JSON）
//
// [返回]
//   - map[string]any: {ok, duplicate, file_name, label, message} 或 {error}
func handleImportCredential(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || trimSpace(body.Content) == "" {
		return map[string]any{"error": "body {filename, content} required"}
	}
	a, err := parseCredentialImport([]byte(body.Content))
	if err != nil {
		return map[string]any{"error": "invalid trae credential: " + err.Error()}
	}
	// Deduplicate by UserID: importing the same account twice must not
	// create a second auth record. hostAuthList failures are tolerated
	// (host RPC unavailable) — proceed to a single save attempt.
	duplicate := ""
	if files, lerr := hostAuthList(); lerr == nil {
		for _, f := range files {
			if trimSpace(f.AuthIndex) == "" {
				continue
			}
			ex, _, gerr := hostAuthGetBundle(f.AuthIndex)
			if gerr != nil || ex == nil {
				continue
			}
			if trimSpace(ex.UserID) != "" && trimSpace(ex.UserID) == trimSpace(a.UserID) {
				duplicate = f.AuthIndex
				break
			}
		}
	}
	label := a.Nickname
	if label == "" {
		label = a.UserID
	}
	if label == "" {
		label = providerName
	}
	if duplicate != "" {
		return map[string]any{
			"ok": true, "duplicate": true, "auth_index": duplicate,
			"label": label, "message": "账号已存在（uid=" + a.UserID + "），未重复导入",
		}
	}
	name := authFileNameFor(a)
	raw, berr := buildAuthFileJSON(a, false, "imported via panel", nil)
	if berr != nil {
		return map[string]any{"error": berr.Error()}
	}
	if serr := hostAuthSaveJSON(name, raw); serr != nil {
		return map[string]any{"error": serr.Error()}
	}
	return map[string]any{
		"ok": true, "duplicate": false, "file_name": name,
		"label": label, "message": "导入成功：" + label,
	}
}

// storageGlobalDir returns the Trae SOLO globalStorage directory on this
// machine, used for the panel hint text and the copy-path button. Falls back
// to %APPDATA% expansion when the env var is absent.
//
// [返回]
//   - string: 目录绝对路径（探测失败时返回占位文案）
func storageGlobalDir() string {
	if ap := trimSpace(os.Getenv("APPDATA")); ap != "" {
		return filepath.Join(ap, "TRAE SOLO CN", "User", "globalStorage")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "AppData", "Roaming", "TRAE SOLO CN", "User", "globalStorage")
	}
	return "%APPDATA%\\TRAE SOLO CN\\User\\globalStorage"
}
