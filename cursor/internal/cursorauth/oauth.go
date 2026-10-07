package cursorauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const maxAuthResponseBytes = 1 << 20

type Endpoints struct {
	LoginURL   string
	PollURL    string
	RefreshURL string
}

type Service struct {
	client      *http.Client
	endpoints   Endpoints
	now         func() time.Time
	mu          sync.Mutex
	loginStates map[string]loginState
}

type LoginStart struct {
	URL       string
	State     string
	ExpiresAt time.Time
}

type LoginStatus string

const (
	LoginPending LoginStatus = "pending"
	LoginSuccess LoginStatus = "success"
)

type Credentials struct {
	AccessToken        string    `json:"access_token"`
	RefreshToken       string    `json:"refresh_token"`
	ExpiresAt          time.Time `json:"expires_at"`
	AccountID          string    `json:"account_id,omitempty"`
	Email              string    `json:"email,omitempty"`
	Type               string    `json:"type"`
	DisabledModels     []string  `json:"disabled_models,omitempty"`
	ToolLoopGuardTools []string  `json:"tool_loop_guard_tools,omitempty"`
}

type PollResult struct {
	Status      LoginStatus
	Credentials Credentials
}

type loginState struct {
	Verifier string    `json:"verifier"`
	UUID     string    `json:"uuid"`
	Expires  time.Time `json:"expires"`
}

type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// oauthTokenResponse 是 Cursor OAuth 令牌端点（/oauth/token）的响应体。
// 与登录轮询使用的 tokenResponse 区分：OAuth 端点返回 snake_case 字段，
// 且刷新场景可能不返回 refresh_token（此时调用方沿用持有的会话 JWT）。
type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

func DefaultEndpoints() Endpoints {
	return Endpoints{
		LoginURL: "https://cursor.com/loginDeepControl",
		PollURL:  "https://api2.cursor.sh/auth/poll",
		// 2026-10 实测：旧端点 /auth/exchange_user_api_key 已失效（401），
		// 会话 JWT 现可作为 refresh_token 走标准 OAuth 令牌端点换取 access_token。
		RefreshURL: "https://api2.cursor.sh/oauth/token",
	}
}

func NewService(client *http.Client, endpoints Endpoints) *Service {
	return &Service{
		client:      client,
		endpoints:   endpoints,
		now:         time.Now,
		loginStates: make(map[string]loginState),
	}
}

func (service *Service) StartLogin(context.Context) (LoginStart, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return LoginStart{}, fmt.Errorf("generate Cursor PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	uuid, err := randomUUID()
	if err != nil {
		return LoginStart{}, err
	}
	expiresAt := service.now().Add(15 * time.Minute).UTC()
	state, err := randomUUID()
	if err != nil {
		return LoginStart{}, err
	}
	service.mu.Lock()
	for key, value := range service.loginStates {
		if service.now().After(value.Expires) {
			delete(service.loginStates, key)
		}
	}
	service.loginStates[state] = loginState{Verifier: verifier, UUID: uuid, Expires: expiresAt}
	service.mu.Unlock()
	loginURL, err := url.Parse(service.endpoints.LoginURL)
	if err != nil {
		return LoginStart{}, fmt.Errorf("parse Cursor login URL: %w", err)
	}
	query := loginURL.Query()
	query.Set("challenge", challenge)
	query.Set("uuid", uuid)
	query.Set("mode", "login")
	query.Set("redirectTarget", "cli")
	loginURL.RawQuery = query.Encode()
	return LoginStart{URL: loginURL.String(), State: state, ExpiresAt: expiresAt}, nil
}

func (service *Service) PollLogin(ctx context.Context, encodedState string) (PollResult, error) {
	service.mu.Lock()
	state, ok := service.loginStates[encodedState]
	service.mu.Unlock()
	if !ok {
		return PollResult{}, errors.New("Cursor login state is unknown")
	}
	if service.now().After(state.Expires) {
		service.mu.Lock()
		delete(service.loginStates, encodedState)
		service.mu.Unlock()
		return PollResult{}, errors.New("Cursor login state expired")
	}
	pollURL, err := url.Parse(service.endpoints.PollURL)
	if err != nil {
		return PollResult{}, fmt.Errorf("parse Cursor poll URL: %w", err)
	}
	query := pollURL.Query()
	query.Set("uuid", state.UUID)
	query.Set("verifier", state.Verifier)
	pollURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL.String(), nil)
	if err != nil {
		return PollResult{}, fmt.Errorf("create Cursor poll request: %w", err)
	}
	response, err := service.client.Do(request)
	if err != nil {
		return PollResult{}, fmt.Errorf("poll Cursor login: %w", err)
	}
	if response.StatusCode == http.StatusNotFound {
		if err := response.Body.Close(); err != nil {
			return PollResult{}, fmt.Errorf("close Cursor pending response: %w", err)
		}
		return PollResult{Status: LoginPending}, nil
	}
	if response.StatusCode != http.StatusOK {
		status := response.StatusCode
		if err := response.Body.Close(); err != nil {
			return PollResult{}, errors.Join(fmt.Errorf("Cursor login poll returned HTTP %d", status), err)
		}
		return PollResult{}, fmt.Errorf("Cursor login poll returned HTTP %d", status)
	}
	var tokens tokenResponse
	if err := decodeJSON(response.Body, &tokens); err != nil {
		return PollResult{}, fmt.Errorf("decode Cursor login response: %w", err)
	}
	credentials, err := credentialsFromTokens(tokens.AccessToken, tokens.RefreshToken, service.now())
	if err != nil {
		return PollResult{}, err
	}
	service.mu.Lock()
	delete(service.loginStates, encodedState)
	service.mu.Unlock()
	return PollResult{Status: LoginSuccess, Credentials: credentials}, nil
}

func randomUUID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate Cursor UUID: %w", err)
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	encoded := fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
	return encoded, nil
}

func decodeJSON(body io.ReadCloser, target any) error {
	raw, readErr := io.ReadAll(io.LimitReader(body, maxAuthResponseBytes+1))
	closeErr := body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if len(raw) > maxAuthResponseBytes {
		return errors.New("Cursor auth response exceeds 1 MiB")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	return nil
}
