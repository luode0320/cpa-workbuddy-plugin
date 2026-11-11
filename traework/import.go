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
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// icubeCloudideKey is the storage.json key holding the Trae SOLO credential
// blob. It mirrors the authKey constant in credential.go (same literal).
const icubeCloudideKey = "iCubeAuthInfo://icube.cloudide"

// credentialField 是导出备份条目里承载内层凭据的字段名。
const credentialField = "credential"

// parseCredentialImport turns user-supplied credential content into a
// traeAuth. Two input shapes are accepted:
//
//  1. the raw credential value (tc blob / plaintext JSON) — the same shape
//     the paste flow accepts;
//  2. the whole storage.json file — the iCubeAuthInfo://icube.cloudide
//     value is extracted and parsed.
//
// [参数]
//   - content: 用户导入的原始内容（storage.json 整文件、导出备份条目或单条凭据值）
//
// [返回]
//   - *traeAuth: 解析成功后的插件侧认证模型；error: 所有形状都无法解析时返回最贴切的错误
//
// 最近修改时间：2026-11-11；改动原因：导出备份条目里的 credential 字符串被宿主
// HTML 实体转义（" → &#34;），叠加内层响应包装时整条被误判为失败，且真实错误被
// storage.json 分支覆盖，用户只看到与原因无关的缺 icube 键提示。
func parseCredentialImport(content []byte) (*traeAuth, error) {
	// 1. 先按「导出备份条目」归一化：把内层 credential（HTML 实体已还原、
	//    响应包装已展平）铺回顶层，让后续解析看到真实字段。
	normalized, nerr := normalizeExportEntry(content)
	if nerr != nil {
		return nil, nerr
	}
	// 2. Shape 1: raw credential value (flat / nested paste shapes).
	a, parseErr := parseNormalizedCredential(normalized)
	if parseErr == nil {
		return a, nil
	}
	// 3. 备份条目归一化后仍失败时，错误已经指向真实原因（缺 token / 凭据不可解析），
	//    不能再落到 storage.json 分支——那会把错误替换成与输入无关的「缺 icube 键」，
	//    用户按提示去找 storage.json 永远修不好。
	if isExportEntry(content) {
		return nil, parseErr
	}
	// 4. Shape 2: whole storage.json → extract the icube credential value.
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
	wrapped, err := json.Marshal(map[string]string{credentialField: val})
	if err != nil {
		return nil, err
	}
	return parseTraeAuth(wrapped)
}

// isExportEntry reports whether content is one entry of an /export backup
// document (top-level credential field present).
//
// [参数]
//   - content: /import 收到的原始 JSON
//
// [返回]
//   - bool: 含顶层 credential 字段时为 true
func isExportEntry(content []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(content, &m); err != nil {
		return false
	}
	_, ok := m[credentialField]
	return ok
}

// parseNormalizedCredential 按「平铺凭据 → 明文凭据 schema」两段解析已归一化的内容。
//
// [参数]
//   - content: 归一化后的凭据 JSON
//
// [返回]
//   - *traeAuth: 可用认证模型；error: 无可用 token 或结构不可解析
func parseNormalizedCredential(content []byte) (*traeAuth, error) {
	a, err := parseTraeAuth(content)
	if err != nil {
		return nil, err
	}
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

// normalizeExportEntry 把 /export 产出的备份条目归一化成可直接解析的凭据 JSON。
//
// 备份条目实测是两层嵌套：外层 {name, auth_index, uid, credential}，其中内层
// credential 是宿主物理文档；物理文档里还可能再带一个 credential 字段，值是
// 被宿主 HTML 实体转义的字符串（引号成了 &#34;，本身不是合法 JSON），或内层再包
// 一层云 API 响应 {ResponseMetadata, Result:{Token, RefreshToken}}。
// 因此必须循环展开到不再含内层 credential 为止，并把每层平铺回顶层；
// 否则展平一层后残留的 credential 字符串会被当成 CredentialRaw 再走解密并失败。
// 非备份条目（storage.json / 裸凭据）原样返回，交由后续形状分支处理。
//
// [参数]
//   - content: /import 收到的原始 JSON
//
// [返回]
//   - []byte: 归一化后的凭据 JSON（无内层 credential 时等于入参）；error: JSON 本身不可解析
//
// 最近修改时间：2026-11-11；改动原因：新增，支持导出备份条目的多层 credential 嵌套。
func normalizeExportEntry(content []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(content, &m); err != nil {
		// 不是 JSON 对象（例如裸 tc blob 字符串），交给形状分支按凭据值处理。
		return content, nil
	}
	// 1. 逐层展开 credential：最多 maxCredentialDepth 层，防御互相引用的畸形输入。
	current := m
	blob := "" // 内层是 tc blob 字符串时保留原值，由解密路径处理
	for depth := 0; depth < maxCredentialDepth; depth++ {
		rawCred, ok := current[credentialField]
		if !ok {
			break
		}
		inner, innerIsObject := unwrapCredentialValue(rawCred)
		if len(inner) == 0 {
			break
		}
		if !innerIsObject {
			blob = string(inner)
			delete(current, credentialField)
			break
		}
		// 云 API 响应包装 → 取 Result 层。
		if result := pickWrappedResult(inner); len(result) > 0 {
			inner = result
		}
		var innerMap map[string]json.RawMessage
		if err := json.Unmarshal(inner, &innerMap); err != nil {
			break
		}
		// 平铺回当前层：已有字段优先（keepalive 刷新过的 token 比导出快照新）。
		delete(current, credentialField)
		for k, v := range innerMap {
			if _, exists := current[k]; exists {
				continue
			}
			current[k] = v
		}
	}
	// 2. tc blob 形状：还原成 credential 字段，保持 decryptCredentialString 可处理。
	if blob != "" {
		quoted, qerr := json.Marshal(blob)
		if qerr == nil {
			current[credentialField] = quoted
		}
	}
	return json.Marshal(current)
}

// maxCredentialDepth 限制 credential 嵌套展开层数，避免畸形输入无限展开。
const maxCredentialDepth = 4

// unwrapCredentialValue 把内层 credential 原始值还原成凭据文档字节。
//
// [参数]
//   - rawCred: credential 字段的原始 JSON（对象或字符串）
//
// [返回]
//   - []byte: 还原后的内容；isObject 为 true 表示它是可展平的 JSON 对象
func unwrapCredentialValue(rawCred json.RawMessage) (data []byte, isObject bool) {
	trimmed := strings.TrimSpace(string(rawCred))
	if trimmed == "" || trimmed == "null" {
		return nil, false
	}
	// 对象形态：直接使用。
	if trimmed[0] == '{' {
		return []byte(trimmed), true
	}
	// 字符串形态：先解出字面值，再做 HTML 实体还原（宿主对 management 响应体
	// 强制 html.EscapeString，引号会变成 &#34;，还原前不是合法 JSON）。
	var s string
	if err := json.Unmarshal(rawCred, &s); err != nil {
		return nil, false
	}
	s = strings.TrimSpace(unescapeHTMLEntities(s))
	if s == "" {
		return nil, false
	}
	// 还原后仍是 JSON 对象（外来导出格式）→ 可展平；否则是 tc blob 凭据串。
	if s[0] == '{' && json.Valid([]byte(s)) {
		return []byte(s), true
	}
	return []byte(s), false
}

// pickWrappedResult 识别云 API 响应包装并返回 Result 层 JSON。
//
// [参数]
//   - inner: 内层凭据 JSON
//
// [返回]
//   - []byte: Result 层 JSON；非响应包装时返回 nil
func pickWrappedResult(inner []byte) []byte {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(inner, &probe); err != nil {
		return nil
	}
	if _, isWrapped := probe["ResponseMetadata"]; !isWrapped {
		return nil
	}
	result, ok := probe["Result"]
	if !ok || len(strings.TrimSpace(string(result))) == 0 {
		return nil
	}
	return result
}

// unescapeHTMLEntities 还原宿主注入的 HTML 实体（&#34; / &amp; / &lt; 等）。
//
// 只在确实含实体时才走 html.UnescapeString：Token 里没有 & 与引号，常规凭据
// 一律原样返回，避免无谓改动原始字符串。
//
// [参数]
//   - s: 可能被实体转义的字符串
//
// [返回]
//   - string: 还原后的字符串
func unescapeHTMLEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return html.UnescapeString(s)
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
		return map[string]any{"ok": false, "error": "body {filename, content} required", "message": "body {filename, content} required"}
	}
	a, err := parseCredentialImport([]byte(body.Content))
	if err != nil {
		return map[string]any{"ok": false, "error": "invalid trae credential: " + err.Error(), "message": "凭据解析失败：" + err.Error()}
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
		return map[string]any{"ok": false, "error": berr.Error(), "message": "凭据文件构建失败：" + berr.Error()}
	}
	if serr := hostAuthSaveJSON(name, raw); serr != nil {
		return map[string]any{"ok": false, "error": serr.Error(), "message": "凭据保存失败：" + serr.Error()}
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
