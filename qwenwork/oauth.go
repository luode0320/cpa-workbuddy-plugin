// oauth.go implements QwenWork's device-authorization login and token refresh.
//
// QwenWork 只有一种凭据家族：Device Flow + PKCE。流程为
//
//	StartLogin  生成 verifier/challenge + nonce/machine_id，返回授权页 URL
//	用户        在浏览器完成登录与授权
//	PollLogin   轮询 /api/v1/deviceToken/poll 拿 token + refresh_token
//	后续        用 /api/v1/deviceToken/refresh 续期
//
// 授权页必须走 gateway 域名（qwenwork.cn 上同路径 404），且 nonce 一次性：
// 链接被请求第二次会返回 DEVICE_FLOW_REPLAYED。插件侧只轮询、不预检。
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// userInfoResponse 是账号身份的简化视图，字段来自 account-context 的
// data.user 与 data.plan。
type userInfoResponse struct {
	ID       string `json:"id"`
	Nickname string `json:"name"`
	Email    string `json:"email"`
	UserType string `json:"userType"`
}

// doRawJSON 发一次请求并返回原始响应体与状态码。
//
// [参数] client HTTP 客户端；method 方法；fullURL 完整地址；headers 头注入
// 函数（nil 时用 commonHeaders）；body 请求体。
// [返回] 响应体、状态码、错误。非 2xx 也返回响应体供调用方判定。
// 最近修改时间：2026-10-18 00:07:00；改动原因：从 jobToken 段独立出来。
func doRawJSON(client *http.Client, method, fullURL string, headers func(*http.Request), body io.Reader) (json.RawMessage, int, error) {
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, 0, err
	}
	if headers != nil {
		headers(req)
	} else {
		commonHeaders(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, nil
}

// fetchUserInfo 拉取账号身份，用于填充 auth 文件的 account 段。
//
// [参数] hostToken 宿主 access token。
// [返回] 身份信息；请求或解析失败时返回错误。
// 最近修改时间：2026-10-18 00:07:00；改动原因：改走 account-context。
func fetchUserInfo(hostToken string) (*userInfoResponse, error) {
	req, err := http.NewRequest(http.MethodGet, endpointUserInfo, nil)
	if err != nil {
		return nil, err
	}
	commonHeaders(req)
	req.Header.Set("Authorization", "Bearer "+hostToken)
	resp, err := sharedHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("userinfo: http %d body=%s", resp.StatusCode, truncateRedacted(string(raw), 200))
	}
	var env struct {
		Data struct {
			User struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Username string `json:"username"`
				Email    string `json:"email"`
			} `json:"user"`
			Plan struct {
				UserType string `json:"user_type"`
			} `json:"plan"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("userinfo parse: %w (body=%s)", err, truncateRedacted(string(raw), 200))
	}
	name := env.Data.User.Name
	if name == "" {
		name = env.Data.User.Username
	}
	return &userInfoResponse{
		ID:       env.Data.User.ID,
		Nickname: name,
		Email:    env.Data.User.Email,
		UserType: env.Data.Plan.UserType,
	}, nil
}

// uiUserType 返回账号类型标识，缺省按个人版处理。
func uiUserType(ui *userInfoResponse) string {
	if ui == nil || ui.UserType == "" {
		return "personal"
	}
	return ui.UserType
}

// -----------------------------------------------------------------------------
// Device-authorization login
//
// 常量来自客户端 app.asar 的 out/main/main.js（1.3.1-26101419），并经
// 2026-10-17 真实账号闭环实测：
//   AUTH_BASE   = gateway.qwenwork.cn（qwenwork.cn 上同路径 404）
//   CLIENT_ID   = e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb
//   REDIRECT    = qwenwork-cn://
// 授权页会 302 到 qwenwork.cn/oauth2/auth，网关自建 PKCE 并把 code 绑到
// 我们的 nonce，因此插件只需轮询，不参与 code 交换。
// -----------------------------------------------------------------------------

const (
	// 授权页必须走 gateway 域名：同一 /device/selectAccounts 路径在
	// qwenwork.cn 上返回 404（2026-10-17 对照实测）。
	qwenworkAuthBase    = "https://gateway.qwenwork.cn"
	qwenworkClientID    = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"
	qwenworkRedirectURI = "qwenwork-cn://"
)

// deviceTokenResponse mirrors /api/v1/deviceToken/{poll,refresh} payloads.
// expires_in / refresh_token_expires_in are MILLISECONDS (client main.js and
// the live Global sample agree).
type deviceTokenResponse struct {
	Token                 string `json:"token"`
	DeviceToken           string `json:"device_token"` // refresh path returns this key instead
	RefreshToken          string `json:"refresh_token"`
	UserID                string `json:"user_id"`
	ExpiresAt             string `json:"expires_at"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresAt string `json:"refresh_token_expires_at"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
}

func (d *deviceTokenResponse) accessToken() string {
	if d.Token != "" {
		return d.Token
	}
	return d.DeviceToken
}

// makePKCE returns (verifier, challenge) per RFC 7636 (S256).
func makePKCE() (string, string) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failure is fatal for PKCE; fall back to uuid material.
		s := uuid.NewString() + uuid.NewString()
		sum := sha256.Sum256([]byte(s))
		v := base64.RawURLEncoding.EncodeToString(sum[:])
		return v, v
	}
	verifier := make([]byte, 64)
	for i, b := range raw {
		verifier[i] = alphabet[int(b)%len(alphabet)]
	}
	sum := sha256.Sum256(verifier)
	return string(verifier), base64.RawURLEncoding.EncodeToString(sum[:])
}

// handleStartLogin implements AuthProvider.StartLogin: build the device
// authorization URL and stash the PKCE verifier under the returned state.
func handleStartLogin(raw []byte) ([]byte, error) {
	verifier, challenge := makePKCE()
	nonce := uuid.NewString()
	machineID := uuid.NewString()

	q := url.Values{}
	q.Set("challenge", challenge)
	q.Set("challenge_method", "S256")
	q.Set("nonce", nonce)
	q.Set("machine_id", machineID)
	q.Set("client_id", qwenworkClientID)
	q.Set("redirect_uri", qwenworkRedirectURI)
	authURL := qwenworkAuthBase + "/device/selectAccounts?" + q.Encode()

	now := time.Now()
	state := fmt.Sprintf("qw-%d", now.UnixNano())
	loginStates.Store(state, &loginCtx{verifier: verifier, nonce: nonce, expires: now.Add(loginTTL), startedAt: now.UnixNano()})
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       authURL,
		State:     state,
		ExpiresAt: now.Add(loginTTL).UTC(),
		Metadata: map[string]any{
			"logo":   pluginLogoURL,
			"prompt": "在打开的页面中登录并授权 QwenWork（设备授权，无需 PAT）。完成后此窗口会自动关闭。",
		},
	})
}

// pollDeviceToken performs one GET against /api/v1/deviceToken/poll.
// Returns (tok, pending, error): pending=true means the user hasn't finished
// authorizing yet (upstream 404/202) — the host should keep polling.
func pollDeviceToken(nonce, verifier string) (*deviceTokenResponse, bool, error) {
	q := url.Values{}
	q.Set("nonce", nonce)
	q.Set("verifier", verifier)
	q.Set("challenge_method", "S256")
	fullURL := upstreamBaseCN + "/api/v1/deviceToken/poll?" + q.Encode()

	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "QwenWork")
	resp, err := sharedHTTPClient().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusAccepted {
		return nil, true, nil
	}
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("deviceToken poll: http %d body=%s", resp.StatusCode, truncateRedacted(string(raw), 200))
	}
	var out deviceTokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, fmt.Errorf("deviceToken poll parse: %w", err)
	}
	if out.accessToken() == "" {
		return nil, true, nil // 200 without token yet — treat as pending
	}
	return &out, false, nil
}

// refreshDeviceToken calls POST /api/v1/deviceToken/refresh with a drt-.
func refreshDeviceToken(drt string) (*deviceTokenResponse, error) {
	body, _ := json.Marshal(map[string]string{"refresh_token": drt})
	data, _, err := doRawJSON(sharedHTTPClient(), http.MethodPost, upstreamBaseCN+"/api/v1/deviceToken/refresh", nil, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var out deviceTokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("deviceToken refresh parse: %w", err)
	}
	if out.accessToken() == "" || out.RefreshToken == "" {
		return nil, fmt.Errorf("deviceToken refresh: incomplete token pair in response")
	}
	return &out, nil
}

// handlePollLogin implements AuthProvider.PollLogin. Two paths:
//  1. Legacy PAT pasted into the modal (kept for backward compatibility).
//  2. Device-authorization poll (the real OAuth flow via pollDeviceToken).
func handlePollLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	state := strings.TrimSpace(req.State)
	if state == "" {
		return nil, fmt.Errorf("poll: empty state")
	}
	v, ok := loginStates.Load(state)
	if !ok {
		return nil, fmt.Errorf("poll: unknown state (restart login)")
	}
	lc := v.(*loginCtx)
	if time.Now().After(lc.expires) {
		loginStates.Delete(state)
		return nil, fmt.Errorf("poll: login expired (10 min timeout)")
	}

	// Device-authorization path: poll the grant. The verifier never left
	// this process; the nonce pairs it to the auth URL we handed out.
	tok, pending, err := pollDeviceToken(lc.nonce, lc.verifier)
	if err != nil {
		return nil, fmt.Errorf("device authorization poll: %w", err)
	}
	if !pending && tok != nil {
		// Auth file must land IMMEDIATELY — no upstream calls before return.
		// The host writes the auth file from AuthLoginPollResponse, so any
		// blocking work (fetchUserInfo, hostAuthList) delays persistence.
		// UserID is already in the poll response; nickname is fetched lazily
		// by the panel on first load. PAT coalescing happens on keepalive.
		sa := buildStoredAuthFromDeviceToken(tok, nil)
		loginStates.Delete(state)
		// CRITICAL: leave ID empty so the host computes it from the file path
		// (authIDForPath). Setting ID=uid here while the watcher registers
		// with ID=<filename> produces two in-memory keys for the same file →
		// duplicate accounts in the panel.
		ad := toAuthDataOpts(sa, nil, false)
		ad.ID = ""
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status: pluginapi.AuthLoginStatusSuccess,
			Auth:   ad,
		})
	}

	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusPending,
		Message: "等待浏览器完成 QwenWork 设备授权",
	})
}

// deviceExpiryUnix resolves the access-token expiry from a deviceToken
// poll/refresh response. Poll returns expires_in (ms); refresh returns only
// expires_at (RFC3339). Default: 30 days (observed dt- lifetime).
func deviceExpiryUnix(tok *deviceTokenResponse) int64 {
	if tok.ExpiresIn > 0 {
		return time.Now().Add(time.Duration(tok.ExpiresIn) * time.Millisecond).Unix()
	}
	if tok.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, tok.ExpiresAt); err == nil {
			return t.Unix()
		}
	}
	return time.Now().Add(30 * 24 * time.Hour).Unix()
}

// buildStoredAuthFromDeviceToken maps a device-token grant onto storedAuth.
// AccessToken=dt-, RefreshToken=drt-. When ui is nil (fast path during
// PollLogin), UserID comes from the poll response and nickname is empty —
// the panel fills it lazily on first load. preservePAT=false skips the
// hostAuthList scan to avoid blocking auth-file persistence.
func buildStoredAuthFromDeviceToken(tok *deviceTokenResponse, ui *userInfoResponse) *storedAuth {
	expiresAt := deviceExpiryUnix(tok)
	uid := tok.UserID
	nickname := ""
	if ui != nil {
		if ui.ID != "" {
			uid = ui.ID
		}
		nickname = ui.Nickname
	}
	// When ui is nil (fast PollLogin path), derive a readable nickname from
	// the uid so the panel doesn't fall back to the raw filename label.
	if nickname == "" {
		if len(uid) > 8 {
			nickname = "u" + uid[len(uid)-8:]
		} else {
			nickname = "u" + uid
		}
	}
	return &storedAuth{
		Auth: storedTokens{
			AccessToken:  tok.accessToken(),
			RefreshToken: tok.RefreshToken,
			ExpiresAt:    expiresAt,
			Domain:       "qwenwork.cn",
		},
		Account: storedAccount{UID: uid, Nickname: nickname},
	}
}
func handleRefreshAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}

	if isDeviceRefreshFamily(sa.Auth.RefreshToken) {
		// Device 家族（drt- / ory_）—— deviceToken/refresh ONLY。
		tok, err := refreshDeviceToken(sa.Auth.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("refresh rejected: %w — deviceToken refresh failed; re-login via OAuth required", err)
		}
		sa.Auth.AccessToken = tok.accessToken()
		sa.Auth.RefreshToken = tok.RefreshToken
		sa.Auth.ExpiresAt = preserveExpiry(deviceExpiryUnix(tok), sa.Auth.ExpiresAt)
		invalidateMaasSession(sa)
		return okEnvelope(pluginapi.AuthRefreshResponse{Auth: toAuthDataForRefresh(sa)})
	}

	return nil, fmt.Errorf("refresh rejected: unsupported token family")
}

// preserveExpiry reuses the previous token's expiresAt when the refresh
// response omits expiresIn. Zero would tell the host the credential is
// permanently expired and trigger a refresh storm on every request.
func preserveExpiry(newExpiry, oldExpiry int64) int64 {
	if newExpiry > 0 {
		return newExpiry
	}
	return oldExpiry
}

// toAuthDataForRefresh mirrors the workbuddy helper: blank out FileName and
// ID so the host backfills from the original auth path (prevents ID mismatch
// duplicate files when Refresh round-trips the record).
func toAuthDataForRefresh(sa *storedAuth) pluginapi.AuthData {
	ad := toAuthDataOpts(sa, nil, false)
	ad.FileName = "" // let host backfill original
	ad.ID = ""       // let host compute from path (prevents ID mismatch dupes)
	return ad
}
