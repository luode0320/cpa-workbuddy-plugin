package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// signResult 是网页签名三元组。
type signResult struct {
	Timestamp string
	Nonce     string
	Sign      string
}

// tokenResult 是刷新端点返回的令牌对。
type tokenResult struct {
	AccessToken  string
	RefreshToken string
	RefreshTime  time.Time
}

// tokenCache 是 refresh_token 到 access_token 的缓存。
type tokenCache struct {
	mu      sync.Mutex
	entries map[string]*tokenResult
	waiting map[string][]*tokenWaiter
}

type tokenWaiter struct {
	done   chan struct{}
	result *tokenResult
	err    error
}

var globalTokenCache = &tokenCache{
	entries: make(map[string]*tokenResult),
	waiting: make(map[string][]*tokenWaiter),
}

var tokenHTTPClient = &http.Client{Timeout: 15 * time.Second}

// generateSign 生成智谱网页签名。
// [参数] secret: 签名密钥；nowMs: 毫秒时间戳；nonce: 随机串。
// [返回] 时间戳、随机串与签名。
// 最近修改时间：2026-11-28 新增, 移植双源一致的时间戳变形与 md5 签名算法。
func generateSign(secret string, nowMs int64, nonce string) signResult {
	stamp := strconv.FormatInt(nowMs, 10)
	digits := []byte(stamp)
	sum := 0
	for _, d := range digits {
		sum += int(d - '0')
	}
	if len(digits) >= 2 {
		sum -= int(digits[len(digits)-2] - '0')
	}
	check := sum % 10
	var b strings.Builder
	if len(digits) >= 2 {
		b.Write(digits[:len(digits)-2])
	}
	b.WriteByte(byte('0' + check))
	if len(digits) >= 1 {
		b.Write(digits[len(digits)-1:])
	}
	timestamp := b.String()
	raw := timestamp + "-" + nonce + "-" + secret
	digest := md5.Sum([]byte(raw))
	return signResult{Timestamp: timestamp, Nonce: nonce, Sign: hex.EncodeToString(digest[:])}
}

// newNonce 生成一个无连字符的随机串。
// [参数] 无。
// [返回] 随机串。
// 最近修改时间：2026-11-28 新增, 签名随机串来源。
func newNonce() string {
	return strings.ReplaceAll(fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixMicro()), "-", "")
}

// deviceIDFromRefreshToken 从 refresh_token 的 JWT 载荷中读取 device_id。
// [参数] refreshToken: chatglm_refresh_token。
// [返回] device_id；格式不符或取不到时返回空串。
// 最近修改时间：2026-11-28 新增，上游要求 X-Device-Id 与令牌载荷内的设备标识一致。
func deviceIDFromRefreshToken(refreshToken string) string {
	parts := strings.Split(refreshToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.DeviceID)
}

// refreshHeaders 构造刷新端点所需的全部伪装头。
// [参数] refreshToken: 刷新令牌；sign: 签名三元组；deviceID: 取自令牌载荷的设备标识。
// [返回] HTTP 头。
// 最近修改时间：2026-11-28 修正 deviceID 来源；上游按此头校验设备一致性，随机值必被拒。
func refreshHeaders(refreshToken string, sign signResult, deviceID string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+refreshToken)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
	h.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	h.Set("App-Name", "chatglm")
	h.Set("Origin", "https://chatglm.cn")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Site", "same-origin")
	h.Set("X-App-Fr", "browser_extension")
	h.Set("X-App-Platform", "pc")
	h.Set("X-App-Version", "0.0.1")
	h.Set("X-Lang", "zh")
	h.Set("X-Device-Id", deviceID)
	h.Set("X-Request-Id", newNonce())
	h.Set("X-Nonce", sign.Nonce)
	h.Set("X-Sign", sign.Sign)
	h.Set("X-Timestamp", sign.Timestamp)
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/123.0.0.0 Safari/537.36")
	return h
}

// requestToken 用 refresh_token 换取 access_token。
// [参数] refreshToken: 刷新令牌；cfg: 插件配置。
// [返回] 令牌结果、错误。
// 最近修改时间：2026-11-28 修正 X-Device-Id 取值，随机设备标识会被上游判 40004。
func requestToken(refreshToken string, cfg Config) (*tokenResult, error) {
	deviceID := deviceIDFromRefreshToken(refreshToken)
	if deviceID == "" {
		return nil, fmt.Errorf("refresh_token 缺少 device_id，请从 chatglm.cn 重新复制")
	}
	sign := generateSign(cfg.SignSecret, time.Now().UnixMilli(), newNonce())
	body := bytes.NewBufferString("{}")
	req, err := http.NewRequest(http.MethodPost, cfg.RefreshEndpoint, body)
	if err != nil {
		return nil, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header = refreshHeaders(refreshToken, sign, deviceID)
	resp, err := tokenHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh status %d: %s", resp.StatusCode, string(raw))
	}
	var envelope struct {
		Result struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			UserID       string `json:"user_id"`
			IsGuest      bool   `json:"is_guest"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	if envelope.Result.AccessToken == "" {
		if strings.Contains(string(raw), "40102") {
			return nil, fmt.Errorf("refresh_token 已过期，请重新获取")
		}
		return nil, fmt.Errorf("refresh response missing access_token")
	}
	return &tokenResult{
		AccessToken:  envelope.Result.AccessToken,
		RefreshToken: envelope.Result.RefreshToken,
		RefreshTime:  time.Now().Add(accessTokenLifetimeSec * time.Second),
	}, nil
}

// acquireToken 获取有效 access_token，命中缓存直接返回，过期或缺失时刷新。
// 同一 refresh_token 的并发刷新合并为一次。
// [参数] refreshToken: 刷新令牌；cfg: 插件配置。
// [返回] access_token、错误。
// 最近修改时间：2026-11-28 新增, access_token 缓存与并发单飞。
func acquireToken(refreshToken string, cfg Config) (string, error) {
	globalTokenCache.mu.Lock()
	if cached, ok := globalTokenCache.entries[refreshToken]; ok && time.Now().Before(cached.RefreshTime) {
		globalTokenCache.mu.Unlock()
		return cached.AccessToken, nil
	}
	if waiters, ok := globalTokenCache.waiting[refreshToken]; ok {
		waiter := &tokenWaiter{done: make(chan struct{})}
		globalTokenCache.waiting[refreshToken] = append(waiters, waiter)
		globalTokenCache.mu.Unlock()
		<-waiter.done
		if waiter.err != nil {
			return "", waiter.err
		}
		return waiter.result.AccessToken, nil
	}
	globalTokenCache.waiting[refreshToken] = make([]*tokenWaiter, 0)
	globalTokenCache.mu.Unlock()

	result, err := requestToken(refreshToken, cfg)
	if err == nil {
		globalTokenCache.mu.Lock()
		globalTokenCache.entries[refreshToken] = result
		globalTokenCache.mu.Unlock()
	}

	globalTokenCache.mu.Lock()
	pending := globalTokenCache.waiting[refreshToken]
	delete(globalTokenCache.waiting, refreshToken)
	for _, w := range pending {
		w.result = result
		w.err = err
		close(w.done)
	}
	globalTokenCache.mu.Unlock()

	if err != nil {
		return "", err
	}
	return result.AccessToken, nil
}

// invalidateToken 清除指定 refresh_token 的缓存，用于 401 后强制刷新。
// [参数] refreshToken: 刷新令牌。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 401 触发强制刷新。
func invalidateToken(refreshToken string) {
	globalTokenCache.mu.Lock()
	defer globalTokenCache.mu.Unlock()
	delete(globalTokenCache.entries, refreshToken)
}
