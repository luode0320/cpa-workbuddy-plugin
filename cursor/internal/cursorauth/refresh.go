package cursorauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// cursorOAuthClientID 是 Cursor 官方 CLI 使用的 OAuth 客户端 ID。会话 JWT
// 作为 refresh_token 走标准 refresh_token 授权时必填（2026-10 实测）。
const cursorOAuthClientID = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"

func (service *Service) Refresh(ctx context.Context, current Credentials) (Credentials, error) {
	if current.RefreshToken == "" {
		return Credentials{}, errors.New("Cursor refresh token is required")
	}
	body, err := json.Marshal(struct {
		GrantType    string `json:"grant_type"`
		ClientID     string `json:"client_id"`
		RefreshToken string `json:"refresh_token"`
	}{
		GrantType:    "refresh_token",
		ClientID:     cursorOAuthClientID,
		RefreshToken: current.RefreshToken,
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("encode Cursor refresh request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, service.endpoints.RefreshURL, bytes.NewReader(body))
	if err != nil {
		return Credentials{}, fmt.Errorf("create Cursor refresh request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := service.client.Do(request)
	if err != nil {
		return Credentials{}, fmt.Errorf("refresh Cursor token: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		status := response.StatusCode
		return Credentials{}, errors.Join(fmt.Errorf("Cursor token refresh returned HTTP %d", status), response.Body.Close())
	}
	var tokens oauthTokenResponse
	if err := decodeJSON(response.Body, &tokens); err != nil {
		return Credentials{}, fmt.Errorf("decode Cursor refresh response: %w", err)
	}
	if tokens.AccessToken == "" {
		return Credentials{}, errors.New("Cursor refresh response is missing access token")
	}
	// OAuth 令牌端点通常不返回新的 refresh_token；此时沿用持有的会话 JWT 继续刷新。
	refreshToken := tokens.RefreshToken
	if refreshToken == "" {
		refreshToken = current.RefreshToken
	}
	refreshed, err := credentialsFromTokens(tokens.AccessToken, refreshToken, service.now())
	if err != nil {
		return Credentials{}, err
	}
	// 新 access_token 未必携带可解析的账号身份；此时保留原凭据中的身份字段。
	if refreshed.AccountID == "" {
		refreshed.AccountID = current.AccountID
	}
	if refreshed.Email == "" {
		refreshed.Email = current.Email
	}
	refreshed.DisabledModels = append([]string(nil), current.DisabledModels...)
	refreshed.ToolLoopGuardTools = append([]string(nil), current.ToolLoopGuardTools...)
	return refreshed, nil
}
