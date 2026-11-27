// maas.go owns the QwenWork MaaS session credential lifecycle.
//
// QwenWork 与 qoderwork 的关键差异：推理不走 COSY 签名链路，而是先用宿主
// access token 到 /api/desktop-sessions 换一张短期的 MaaS access key
// （sk-mm-v1. 前缀，实测 TTL 2 小时），再以裸 Bearer 打
// {base_url}/v1/chat/completions。
//
// 本文件只负责这张短期票据：签发、缓存、到期前轮换、失效清理。宿主
// token 的刷新仍归 oauth.go 管。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maasSession 是一次 MaaS 签发的完整结果。
type maasSession struct {
	AccessKey   string `json:"accessKey"`
	BaseURL     string `json:"baseUrl"`
	SessionID   string `json:"sessionId"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
	TTLMs       int64  `json:"ttlMs"`
	MintedAtMs  int64  `json:"mintedAtMs"`
}

// remaining 返回距过期的剩余时长；过期或未设置过期时间时返回 0。
func (m *maasSession) remaining(now time.Time) time.Duration {
	if m == nil || m.ExpiresAtMs <= 0 {
		return 0
	}
	return time.Until(time.UnixMilli(m.ExpiresAtMs))
}

// usable 判断该票据是否仍可安全使用：剩余时间必须超过刷新提前量。
func (m *maasSession) usable(now time.Time) bool {
	return m != nil && m.AccessKey != "" && m.remaining(now) > maasRefreshLead
}

// maasRefreshLead 是提前轮换窗口。实测 TTL 为 2 小时，提前 30 分钟换新，
// 留出足够的失败重试余量。
const maasRefreshLead = 30 * time.Minute

// maasRequestTimeout 是签发与轮换单次调用的上限。这两个调用都在请求关键
// 路径上，不能沿用 sharedHTTPClient 的 120s 兜底。
const maasRequestTimeout = 15 * time.Second

// maasDefaultBaseURL 是签发接口未下发 base_url 时的兜底值，与客户端
// DEFAULT_MAAS_BASE 常量一致。
const maasDefaultBaseURL = "https://maas.xiaoluozi.cn"

// maasMintResponse 镜像 POST /api/desktop-sessions 的响应。
type maasMintResponse struct {
	AccessToken string `json:"access_token"`
	BaseURL     string `json:"base_url"`
	SessionID   string `json:"session_id"`
	ExpiresAtMs int64  `json:"expires_at_ms"`
	TTLMs       int64  `json:"ttl_ms"`
}

// maasExchangeResponse 镜像 POST /v1/access-tokens/exchange 的响应。
type maasExchangeResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   int64  `json:"expires_at"`
}

// maasCache 按账号缓存 MaaS 票据。key 用账号 UID，避免同一账号的并发
// 请求各自签发。零值可用。
type maasCache struct {
	mu       sync.Mutex
	sessions map[string]*maasSession
	inflight map[string]*maasMintCall
}

// maasMintCall 合并同一账号的并发签发请求，保证一次签发只打一次上游。
type maasMintCall struct {
	done chan struct{}
	sess *maasSession
	err  error
}

var globalMaasCache = &maasCache{
	sessions: make(map[string]*maasSession),
	inflight: make(map[string]*maasMintCall),
}

// maasCacheKeyPrefix 生成账号级缓存键前缀。
// 票据缓存按「账号×会话」分片，前缀部分只标识账号。
//
// [参数] sa 账号凭据。
// [返回] 前缀；sa 为空时返回空串。
// 最近修改时间：2026-11-27；改动原因：票据改用会话维度分片，本函数降为前缀。
func maasCacheKeyPrefix(sa *storedAuth) string {
	if sa == nil {
		return ""
	}
	if uid := strings.TrimSpace(sa.Account.UID); uid != "" {
		return uid
	}
	return "token:" + fingerprintToken(sa.Auth.AccessToken)
}

// maasSessionCacheKey 组装「账号×会话」缓存键。
// 同一账号的不同会话必须各自持有独立票据：上游把票据绑定到 session_id，
// 共用一张票会把多个会话折叠成同一个上游会话。会话键为空时归入固定分片，
// 供探活等无会话场景使用。
//
// [参数] sa 账号凭据；sessionKey 调度侧会话键。
// [返回] 缓存键；sa 为空时返回空串。
// 最近修改时间：2026-11-27；改动原因：修复跨会话票据混用。
func maasSessionCacheKey(sa *storedAuth, sessionKey string) string {
	base := maasCacheKeyPrefix(sa)
	if base == "" {
		return ""
	}
	if sid := maasSessionID(sessionKey); sid != "" {
		return base + "|" + sid
	}
	return base + "|nosession"
}

// maasSessionID 把调度用的会话键转换为上游要求的 UUID 形态。
//
// 上游 POST /api/desktop-sessions 的 session_id 字段强校验 UUID：传入
// "x-client-request-id:abc" 这类调度键会得到
// `422 value is not a valid UUID`（2026-10-18 生产实证）。调度键与上游
// 会话 ID 是两套语义，不能直接复用，因此这里按会话键确定性派生 UUID：
// 同一会话恒得同一 UUID（保持会话粘性），不同会话得到不同 UUID。
//
// [参数] sessionKey 调度侧会话键，可为空。
// [返回] 合法 UUID 字符串；sessionKey 为空时返回空串（交由上游分配）。
// 最近修改时间：2026-10-18 14:45:00；改动原因：session_id 强校验 UUID。
func maasSessionID(sessionKey string) string {
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("qwenwork-maas-session:" + key))
	// 按 RFC 4122 v4 形态拼装：版本位固定为 4，变体位固定为 8/9/a/b。
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// mintPayload 组装 POST /api/desktop-sessions 的请求体。
//
// 独立成函数以便测试直接断言「发给上游的 session_id 是 UUID 形态」——
// 只测 maasSessionID 本身无法发现调用方漏用派生结果。
//
// [参数] sessionID 调度侧会话键，可为空。
// [返回] 上游请求体；会话键为空时不含 session_id 字段，由上游分配。
// 最近修改时间：2026-10-18 14:45:00；改动原因：session_id 强校验 UUID。
func mintPayload(sessionID string) map[string]any {
	payload := map[string]any{}
	if sid := maasSessionID(sessionID); sid != "" {
		payload["session_id"] = sid
	}
	return payload
}

// mintMaasSession 调 POST /api/desktop-sessions 换新票据。
//
// [参数] hostToken 宿主 access token（dt- 或 device_token）；sessionID 可空，
// 用于让上游把票据绑定到具体会话，空时上游自行分配。
// [返回] 成功返回票据；HTTP 非 2xx 或响应缺 access_token 时返回错误。
// 最近修改时间：2026-10-18 14:45:00；改动原因：session_id 必须是合法 UUID。
func mintMaasSession(hostToken, sessionID string) (*maasSession, error) {
	if strings.TrimSpace(hostToken) == "" {
		return nil, fmt.Errorf("maas mint: empty host token")
	}
	body, _ := json.Marshal(mintPayload(sessionID))
	// 签发在请求关键路径上：sharedHTTPClient 的 120s 兜底太长，单独收紧。
	ctx, cancel := context.WithTimeout(context.Background(), maasRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointMintDesktopSession, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	commonHeaders(req)
	req.Header.Set("Authorization", "Bearer "+hostToken)
	resp, err := sharedHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("maas mint transport: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// 实测签发成功返回 201 Created，不能只认 200。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("maas mint: http %d body=%s", resp.StatusCode, truncateRedacted(string(raw), 200))
	}
	var out maasMintResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("maas mint parse: %w", err)
	}
	key := strings.TrimSpace(out.AccessToken)
	if key == "" {
		return nil, fmt.Errorf("maas mint: empty access_token in response")
	}
	base := strings.TrimRight(strings.TrimSpace(out.BaseURL), "/")
	if base == "" {
		base = maasDefaultBaseURL
	}
	return &maasSession{
		AccessKey:   key,
		BaseURL:     base,
		SessionID:   out.SessionID,
		ExpiresAtMs: out.ExpiresAtMs,
		TTLMs:       out.TTLMs,
		MintedAtMs:  time.Now().UnixMilli(),
	}, nil
}

// exchangeMaasSession 用现有票据换一张续期票据。
//
// 实测：POST {base_url}/v1/access-tokens/exchange，body {ttl_ms}，
// 成功返回 201 + {access_token, expires_at}。失败时调用方应直接重新签发
// 而不是重试本接口。
//
// [参数] cur 当前票据。
// [返回] 续期后的票据；票据为空或上游拒绝时返回错误。
// 最近修改时间：2026-10-18 00:45:00；改动原因：补齐函数注释元信息。
func exchangeMaasSession(cur *maasSession) (*maasSession, error) {
	if cur == nil || cur.AccessKey == "" {
		return nil, fmt.Errorf("maas exchange: empty current key")
	}
	ttl := cur.TTLMs
	if ttl <= 0 {
		ttl = int64(maasRefreshLead * 2 / time.Millisecond)
	}
	body, _ := json.Marshal(map[string]int64{"ttl_ms": ttl})
	url := strings.TrimRight(cur.BaseURL, "/") + "/v1/access-tokens/exchange"
	ctx, cancel := context.WithTimeout(context.Background(), maasRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	commonHeaders(req)
	req.Header.Set("Authorization", "Bearer "+cur.AccessKey)
	resp, err := sharedHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("maas exchange transport: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("maas exchange: http %d body=%s", resp.StatusCode, truncateRedacted(string(raw), 200))
	}
	var out maasExchangeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("maas exchange parse: %w", err)
	}
	key := strings.TrimSpace(out.AccessToken)
	if key == "" {
		return nil, fmt.Errorf("maas exchange: empty access_token in response")
	}
	next := *cur
	next.AccessKey = key
	next.ExpiresAtMs = out.ExpiresAt
	next.MintedAtMs = time.Now().UnixMilli()
	return &next, nil
}

// ensureMaasSession 返回一个可用的 MaaS 票据，必要时签发或轮换。
//
// 顺序：内存命中 → 轮换旧票据 → 重新签发。轮换失败直接降级为重新签发，
// 因为宿主 token 仍有效时重新签发一定可行，而轮换可能因票据被上游回收
// 而失败。
//
// [参数] sa 账号凭据；sessionID 会话标识，可空。
// [返回] 可用票据；宿主 token 缺失或签发失败时返回错误。
// 最近修改时间：2026-10-17 23:45:00；改动原因：新建 QwenWork MaaS 签发层。
func ensureMaasSession(sa *storedAuth, sessionID string) (*maasSession, error) {
	key := maasSessionCacheKey(sa, sessionID)
	if key == "" {
		return nil, fmt.Errorf("maas: empty account key")
	}
	now := time.Now()

	globalMaasCache.mu.Lock()
	if cached := globalMaasCache.sessions[key]; cached.usable(now) {
		globalMaasCache.mu.Unlock()
		return cached, nil
	}
	// 同账号并发请求合并到一次签发
	if call := globalMaasCache.inflight[key]; call != nil {
		globalMaasCache.mu.Unlock()
		<-call.done
		return call.sess, call.err
	}
	call := &maasMintCall{done: make(chan struct{})}
	globalMaasCache.inflight[key] = call
	prev := globalMaasCache.sessions[key]
	globalMaasCache.mu.Unlock()

	sess, err := refreshOrMint(sa, prev, sessionID)

	globalMaasCache.mu.Lock()
	delete(globalMaasCache.inflight, key)
	if err == nil && sess != nil {
		globalMaasCache.sessions[key] = sess
	}
	globalMaasCache.mu.Unlock()

	call.sess, call.err = sess, err
	close(call.done)
	return sess, err
}

// refreshOrMint 先尝试轮换，失败后回退到重新签发。
//
// [参数] sa 账号凭据；prev 上次票据（可空）；sessionID 会话标识（可空）。
// [返回] 可用票据；宿主 token 缺失或签发失败时返回错误。
// 最近修改时间：2026-10-18 00:45:00；改动原因：补齐函数注释元信息。
func refreshOrMint(sa *storedAuth, prev *maasSession, sessionID string) (*maasSession, error) {
	hostToken := strings.TrimSpace(sa.Auth.AccessToken)
	if hostToken == "" {
		return nil, fmt.Errorf("maas: account has no host access token")
	}
	if prev != nil && prev.AccessKey != "" && prev.usable(time.Now()) {
		return prev, nil
	}
	if prev != nil && prev.AccessKey != "" {
		if next, err := exchangeMaasSession(prev); err == nil {
			return next, nil
		}
	}
	return mintMaasSession(hostToken, sessionID)
}

// invalidateMaasSession 清掉某账号全部会话的缓存票据。宿主 token 刷新、账号
// 禁用或上游返回 401 时调用，确保下一次请求重新签发。
//
// [参数] sa 账号凭据。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：票据按会话分片后需清除全部会话条目。
func invalidateMaasSession(sa *storedAuth) {
	base := maasCacheKeyPrefix(sa)
	if base == "" {
		return
	}
	prefix := base + "|"
	globalMaasCache.mu.Lock()
	for k := range globalMaasCache.sessions {
		// 兼容升级前按纯账号键写入的历史条目。
		if k == base || strings.HasPrefix(k, prefix) {
			delete(globalMaasCache.sessions, k)
		}
	}
	globalMaasCache.mu.Unlock()
}

// invalidateAllMaasSessions 清空全部缓存，用于配置重载与停机。
//
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-18 00:45:00；改动原因：补齐函数注释元信息。
func invalidateAllMaasSessions() {
	globalMaasCache.mu.Lock()
	globalMaasCache.sessions = make(map[string]*maasSession)
	globalMaasCache.inflight = make(map[string]*maasMintCall)
	globalMaasCache.mu.Unlock()
}

// maasInferenceURL 拼接推理端点。
//
// [参数] sess 票据（可空）；path 端点路径。
// [返回] 完整 URL；票据未下发 base_url 时用默认域名。
// 最近修改时间：2026-10-18 00:45:00；改动原因：补齐函数注释元信息。
func maasInferenceURL(sess *maasSession, path string) string {
	base := maasDefaultBaseURL
	if sess != nil && strings.TrimSpace(sess.BaseURL) != "" {
		base = strings.TrimRight(strings.TrimSpace(sess.BaseURL), "/")
	}
	return base + path
}

// fingerprintToken 返回 token 的短指纹，用于日志与缓存键，绝不输出原值。
//
// [参数] token 原始凭据。
// [返回] sha256 前 12 位；空串返回 "<empty>"。
// 最近修改时间：2026-10-18 00:05:00；改动原因：新建 QwenWork MaaS 签发层。
func fingerprintToken(token string) string {
	if token == "" {
		return "<empty>"
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:12]
}
