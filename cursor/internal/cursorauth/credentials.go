package cursorauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type jwtPayload struct {
	Subject json.RawMessage `json:"sub"`
	Email   string          `json:"email"`
	Expiry  int64           `json:"exp"`
}

// ProviderType 是本插件凭据的规范化类型标识，必须与插件 id、executor 标识符
// 以及认证文件顶层 "type" 字段保持一致，否则宿主无法把凭据路由到本执行器。
const ProviderType = "cursor-provider"

func credentialsFromTokens(accessToken, refreshToken string, now time.Time) (Credentials, error) {
	if accessToken == "" || refreshToken == "" {
		return Credentials{}, errors.New("Cursor credentials require access and refresh tokens")
	}
	payload := parseJWTPayload(accessToken)
	if payload.Expiry == 0 {
		payload = parseJWTPayload(refreshToken)
	}
	expiresAt := now.Add(time.Hour).UTC()
	if payload.Expiry > 0 {
		expiresAt = time.Unix(payload.Expiry, 0).Add(-5 * time.Minute).UTC()
	}
	return Credentials{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		AccountID:    parseSubject(payload.Subject),
		Email:        strings.ToLower(strings.TrimSpace(payload.Email)),
		Type:         ProviderType,
	}, nil
}

func ParseCredentials(raw []byte) (Credentials, error) {
	var credentials Credentials
	if err := json.Unmarshal(raw, &credentials); err != nil {
		return Credentials{}, fmt.Errorf("decode Cursor credentials: %w", err)
	}
	if credentials.Type != ProviderType || credentials.AccessToken == "" || credentials.RefreshToken == "" {
		return Credentials{}, errors.New("Cursor credentials are incomplete")
	}
	return credentials, nil
}

func MarshalCredentials(credentials Credentials) ([]byte, error) {
	raw, err := json.Marshal(credentials)
	if err != nil {
		return nil, fmt.Errorf("encode Cursor credentials: %w", err)
	}
	return raw, nil
}

// SessionTokenCredentials 从 Cursor 会话 JWT（"user_<id>::<jwt>" 中 "::" 之后
// 的片段）构造种子凭据：校验 JWT 三段结构并解析账号身份，AccessToken 留空，
// 由后续 Refresh 用标准 OAuth 令牌端点换取。Type 统一为 ProviderType。
func SessionTokenCredentials(jwtSegment string) (Credentials, error) {
	jwtSegment = strings.TrimSpace(jwtSegment)
	parts := strings.Split(jwtSegment, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Credentials{}, errors.New("Cursor session token is not a valid JWT")
	}
	payload := parseJWTPayload(jwtSegment)
	return Credentials{
		RefreshToken: jwtSegment,
		AccountID:    parseSubject(payload.Subject),
		Email:        strings.ToLower(strings.TrimSpace(payload.Email)),
		Type:         ProviderType,
	}, nil
}

func parseJWTPayload(token string) jwtPayload {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtPayload{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtPayload{}
	}
	var payload jwtPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return jwtPayload{}
	}
	return payload
}

func parseSubject(raw json.RawMessage) string {
	var subject string
	if err := json.Unmarshal(raw, &subject); err == nil {
		return subject
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return strconv.FormatInt(number, 10)
	}
	return ""
}
