package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
)

// cursorTokenSessionPrefix 是浏览器 Cookie 中会话令牌的常见前缀。
const cursorTokenSessionPrefix = "WorkosCursorSessionToken="

// parseCursorTokenImport 把用户粘贴的 Cursor 会话令牌解析为可兑换的会话 JWT，
// 支持以下形态：
//  1. "user_<id>::<jwt>" 或 URL 编码的 "user_<id>%3A%3A<jwt>"；
//  2. 直接粘贴的三段式 JWT；
//  3. 带 "WorkosCursorSessionToken=" 前缀的 Cookie 形态。
//
// 统一取 "::" 之后的 JWT 片段；带 "user_" 前缀的整串会被上游拒绝（401）。
func parseCursorTokenImport(content string) (cursorauth.Credentials, error) {
	value := strings.TrimSpace(content)
	if value == "" {
		return cursorauth.Credentials{}, fmt.Errorf("token is empty")
	}
	// 容忍整行粘贴的 Cookie（仅取前缀后的值）。
	if idx := strings.Index(value, cursorTokenSessionPrefix); idx >= 0 {
		value = value[idx+len(cursorTokenSessionPrefix):]
		// 浏览器 Cookie 常在令牌值后追加 "; Path=/; Domain=..." 等属性；
		// 令牌本身不含 ";"，故在第一个 ";" 处截断，只保留令牌值。
		if end := strings.IndexByte(value, ';'); end >= 0 {
			value = value[:end]
		}
		value = strings.TrimSpace(strings.Trim(value, "\""))
	}
	// URL 解码（%3A%3A -> ::），解码失败时按原文继续。
	if decoded, err := url.QueryUnescape(value); err == nil {
		value = decoded
	}
	value = strings.TrimSpace(value)
	// 取 "::" 之后的 JWT 片段；无分隔符时整串当作 JWT。
	if idx := strings.LastIndex(value, "::"); idx >= 0 {
		value = strings.TrimSpace(value[idx+2:])
	}
	credentials, err := cursorauth.SessionTokenCredentials(value)
	if err != nil {
		return cursorauth.Credentials{}, err
	}
	if strings.TrimSpace(credentials.AccountID) == "" && strings.TrimSpace(credentials.Email) == "" {
		return cursorauth.Credentials{}, fmt.Errorf("token does not expose an account identity")
	}
	return credentials, nil
}

// importCredential 实现 POST /plugins/cursor-provider/import。
// 请求体：{filename, content}。content 为粘贴的会话令牌；解析后按账号身份
// 去重（命中 host.auth.list + host.auth.get 已存在账号则不重复写入），
// 再用标准 OAuth 令牌端点兑换 access_token，最后以 host.auth.save 落盘为
// cursor-<hash8>.json（顶层带 type=cursor-provider）。
func (handler *Handler) importCredential(ctx context.Context, body []byte) (managementResponse, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return managementError(http.StatusBadRequest, "invalid JSON body"), nil
	}
	var content string
	if raw, ok := fields["content"]; ok {
		_ = json.Unmarshal(raw, &content)
	}
	if strings.TrimSpace(content) == "" {
		return managementError(http.StatusBadRequest, "body {filename, content} required"), nil
	}
	seed, err := parseCursorTokenImport(content)
	if err != nil {
		return managementError(http.StatusBadRequest, "invalid Cursor token: "+err.Error()), nil
	}
	if handler.host == nil {
		return managementError(http.StatusBadGateway, "host callbacks are unavailable"), nil
	}
	// 去重：同一 account_id / email 已存在时不重复导入。
	if duplicate := handler.findDuplicateCredential(ctx, seed); duplicate != "" {
		return managementJSON(http.StatusOK, map[string]any{
			"ok": true, "duplicate": true, "auth_index": duplicate,
			"label":   cursorCredentialLabel(seed),
			"message": "账号已存在，未重复导入",
		})
	}
	// 兑换：会话 JWT 作为 refresh_token 换取 access_token。
	exchanged, err := handler.auth.Refresh(ctx, seed)
	if err != nil {
		return managementError(http.StatusBadGateway, "兑换 Cursor 令牌失败: "+err.Error()), nil
	}
	if strings.TrimSpace(exchanged.AccountID) == "" {
		exchanged.AccountID = seed.AccountID
	}
	if strings.TrimSpace(exchanged.Email) == "" {
		exchanged.Email = seed.Email
	}
	name := authFileNameFor(exchanged)
	if name == "" {
		return managementError(http.StatusBadRequest, "无法从令牌确定账号身份"), nil
	}
	raw, err := buildCursorAuthFileJSON(exchanged, false, "imported via panel")
	if err != nil {
		return managementError(http.StatusInternalServerError, err.Error()), nil
	}
	if _, err := handler.host.Call(ctx, "host.auth.save", map[string]any{
		"name": name, "json": json.RawMessage(raw),
	}); err != nil {
		return managementError(http.StatusBadGateway, "保存 Cursor 凭据失败: "+err.Error()), nil
	}
	return managementJSON(http.StatusOK, map[string]any{
		"ok": true, "duplicate": false, "file_name": name,
		"label":   cursorCredentialLabel(exchanged),
		"message": "导入成功：" + cursorCredentialLabel(exchanged),
	})
}

// findDuplicateCredential 在既有 Cursor 凭据中查找与 seed 相同身份的账号，
// 命中时返回其 auth_index（否则返回空串）。
func (handler *Handler) findDuplicateCredential(ctx context.Context, seed cursorauth.Credentials) string {
	files, err := handler.cursorAuthFiles(ctx)
	if err != nil {
		return ""
	}
	seedIdentities := cursorCredentialIdentities(seed)
	for _, file := range files {
		if strings.TrimSpace(file.AuthIndex) == "" {
			continue
		}
		credential, credentialErr := handler.getCursorCredential(ctx, file.AuthIndex)
		if credentialErr != nil {
			continue
		}
		for _, identity := range cursorCredentialIdentities(credential) {
			for _, seedIdentity := range seedIdentities {
				if identity == seedIdentity {
					return file.AuthIndex
				}
			}
		}
	}
	return ""
}

// cursorCredentialLabel 生成账号展示名（优先邮箱，其次 account_id）。
func cursorCredentialLabel(credentials cursorauth.Credentials) string {
	if email := strings.TrimSpace(credentials.Email); email != "" {
		return email
	}
	if accountID := strings.TrimSpace(credentials.AccountID); accountID != "" {
		return accountID
	}
	return providerName
}
