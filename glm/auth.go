package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// maskRefreshToken 返回令牌脱敏短标识，用于面板标签。
// [参数] token: 刷新令牌。
// [返回] 脱敏标识。
// 最近修改时间：2026-11-28 新增, 令牌脱敏展示。
func maskRefreshToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 8 {
		return "glm ****"
	}
	return "glm " + token[len(token)-6:]
}

// handleParseAuth 解析宿主下发的凭据 JSON，识别本插件的 refresh_token 账号。
// [参数] raw: AuthParseRequest JSON。
// [返回] 解析响应、错误。
// 最近修改时间：2026-11-28 新增, refresh_token 账号导入入口。
func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	var probe struct {
		Type         string `json:"type"`
		Provider     string `json:"provider"`
		RefreshToken string `json:"refresh_token"`
		Label        string `json:"label"`
		Disabled     bool   `json:"disabled"`
	}
	_ = json.Unmarshal(req.RawJSON, &probe)

	declared := strings.ToLower(strings.TrimSpace(probe.Type))
	prov := strings.ToLower(strings.TrimSpace(probe.Provider))
	isGLM := declared == providerName || declared == "glm" ||
		prov == providerName || prov == "glm" ||
		strings.EqualFold(strings.TrimSpace(req.Provider), providerName) ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.FileName)), glmAuthFilePrefix)

	if !isGLM || strings.TrimSpace(probe.RefreshToken) == "" {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}

	label := strings.TrimSpace(probe.Label)
	if label == "" {
		label = maskRefreshToken(probe.RefreshToken)
	}
	ad := pluginapi.AuthData{
		Provider:    providerName,
		ID:          "",
		FileName:    strings.TrimSpace(req.FileName),
		Label:       label,
		Disabled:    probe.Disabled,
		StorageJSON: req.RawJSON,
		Metadata: map[string]any{
			"type":     providerName,
			"provider": providerName,
			"logo":     pluginLogoURL,
			"disabled": probe.Disabled,
		},
	}
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: ad})
}

// handleRefreshAuth 刷新账号 access_token。
// [参数] raw: AuthRefreshRequest JSON。
// [返回] 刷新响应、错误。
// 最近修改时间：2026-11-28 新增, 账号刷新入口。
func handleRefreshAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	var sa StoredAuth
	if len(req.StorageJSON) > 0 {
		if err := json.Unmarshal(req.StorageJSON, &sa); err != nil {
			return nil, fmt.Errorf("decode auth: %w", err)
		}
	} else if found := findAuthByID(req.AuthID); found != nil {
		sa = *found
	}
	if strings.TrimSpace(sa.RefreshToken) == "" {
		return nil, fmt.Errorf("refresh_token is empty")
	}
	cfg := currentConfig()
	result, err := requestToken(sa.RefreshToken, cfg)
	if err != nil {
		return nil, err
	}
	invalidateToken(sa.RefreshToken)
	sa.AccessToken = result.AccessToken
	sa.ExpiresAt = result.RefreshTime.Unix()
	if result.RefreshToken != "" {
		sa.RefreshToken = result.RefreshToken
	}
	sa.UpdatedAt = time.Now().Format(time.RFC3339)
	if sa.PhysicalPath != "" {
		if errWrite := writeAuthFileDirect(sa.PhysicalPath, &sa); errWrite != nil {
			return nil, fmt.Errorf("persist refreshed auth: %w", errWrite)
		}
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth: pluginapi.AuthData{
			Provider:    providerName,
			ID:          sa.AuthID,
			FileName:    sa.AuthID + ".json",
			Label:       sa.Label,
			Disabled:    sa.Disabled,
			StorageJSON: mustMarshal(&sa),
		},
		NextRefreshAfter: result.RefreshTime,
	})
}

// stringValue 从 map[string]any 中安全取字符串。
// [参数] v: 任意值。
// [返回] 字符串表示。
// 最近修改时间：2026-11-28 新增, 登录元数据取值。
func stringValue(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// mustMarshal 序列化凭据，失败返回空字节。
// [参数] v: 任意对象。
// [返回] JSON 字节。
// 最近修改时间：2026-11-28 新增, 刷新响应序列化。
func mustMarshal(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// saveImportedToken 直接落盘一个 refresh_token 账号，供面板管理接口调用。
// [参数] refreshToken: 刷新令牌；label: 标签。
// [返回] 账号、错误。
// 最近修改时间：2026-11-28 新增, 面板 token 导入。
func saveImportedToken(refreshToken, label string) (*StoredAuth, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh_token is empty")
	}
	cfg := currentConfig()
	if _, err := acquireToken(refreshToken, cfg); err != nil {
		return nil, fmt.Errorf("refresh_token 校验失败: %w", err)
	}
	authID := authIDFor(refreshToken)
	if label == "" {
		label = maskRefreshToken(refreshToken)
	}
	now := time.Now().Format(time.RFC3339)
	sa := &StoredAuth{
		Type:         glmProviderID,
		Provider:     glmProviderID,
		AuthID:       authID,
		RefreshToken: refreshToken,
		Label:        label,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	path := filepath.Join(getAccountsDir(), authID+".json")
	if err := writeAuthFileDirect(path, sa); err != nil {
		return nil, err
	}
	return sa, nil
}

// removeAuthFile 删除本地账号凭据文件。
// [参数] authID: 账号标识。
// [返回] 错误。
// 最近修改时间：2026-11-28 新增, 面板账号删除。
func removeAuthFile(authID string) error {
	authID = strings.TrimSuffix(strings.TrimSpace(authID), ".json")
	path := filepath.Join(getAccountsDir(), authID+".json")
	if !isSafeGLMAuthPath(path) {
		return fmt.Errorf("unsafe auth path")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
