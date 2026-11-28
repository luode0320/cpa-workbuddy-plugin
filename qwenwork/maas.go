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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
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

// maasCacheMaxEntries 是票据缓存的条目上限。
//
// 上游会话键改为逐对话派生后，条目基数随对话数增长；不淘汰会让 map 无限膨胀，
// 而插件堆有 128MiB 软上限（memory_limit.go），GC 压力会直接进请求延迟。
const maasCacheMaxEntries = 512

// maasClientRequestSource 是上游客户端归因头 x-mm-client-request-source 的取值。
//
// 官方桌面客户端固定上报 "qwenwork_desktop"（逆向证据：D:\qwork\QwenWorkCN\
// 2.2.1-26112604\resources\app.asar → out/main/main.js 的
// DESKTOP_CLIENT_REQUEST_SOURCE 常量）。上游按该头区分客户端来源，缺失时
// 请求会退化成无归属流量，表现为上游排队。
const maasClientRequestSource = "qwenwork_desktop"

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

// maasSessionCacheKey 组装「账号×上游会话」的票据缓存键。
// 同一账号的不同会话必须各自持有独立票据：上游把票据绑定到 session_id，
// 共用一张票会把多个会话折叠成同一个上游会话。上游会话键为空时归入固定分片，
// 供探活等无会话场景使用。
//
// [参数] sa 账号凭据；maasSessionKey 上游 MaaS 会话键。
// [返回] 缓存键；sa 为空时返回空串。
// 最近修改时间：2026-11-28；改动原因：参数语义改为上游会话键，与路由会话键解耦。
func maasSessionCacheKey(sa *storedAuth, maasSessionKey string) string {
	base := maasCacheKeyPrefix(sa)
	if base == "" {
		return ""
	}
	if sid := maasSessionID(maasSessionKey); sid != "" {
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
	// 上游回签的 session_id 必须与请求一致（官方客户端据此判不一致即抛错）；
	// 上游没回时用本地派生的那一个兜底，保证请求头 x-session-id 永远有值。
	issuedSessionID := strings.TrimSpace(out.SessionID)
	if issuedSessionID == "" {
		issuedSessionID = maasSessionID(sessionID)
	}
	return &maasSession{
		AccessKey:   key,
		BaseURL:     base,
		SessionID:   issuedSessionID,
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
		storeMaasSessionLocked(key, sess)
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

// maasDeviceType 返回上游客户端归因头 x-mm-device-type 的取值。
//
// 官方客户端在 Windows 上固定上报 "windows"（逆向证据：out/main/main.js 的
// DESKTOP_DEVICE_TYPE 常量）。本插件随宿主运行在 Linux 上，如实上报运行平台，
// 不冒充其它平台。
//
// [参数] 无。
// [返回] 运行平台标识。
// 最近修改时间：2026-11-28；改动原因：新增，客户端归因头。
func maasDeviceType() string {
	return runtime.GOOS
}

// maasUserAgent 返回出站 User-Agent。
//
// [参数] 无。
// [返回] 形如 "QwenWork-CPA-Plugin/<版本>" 的 UA。
// 最近修改时间：2026-11-28；改动原因：新增，出站 UA 与插件版本对齐。
func maasUserAgent() string {
	return "QwenWork-CPA-Plugin/" + version
}

// newMaasRequestID 生成每请求唯一的 x-mm-client-request-id。
//
// [参数] 无。
// [返回] 16 位随机 hex。
// 最近修改时间：2026-11-28；改动原因：新增，客户端归因头。
func newMaasRequestID() string {
	return newMaasHex(8)
}

// newMaasTraceID 生成每请求唯一的 x-mm-client-trace-id。
//
// 官方客户端用 32 位随机 hex（crypto.randomUUID 去连字符），此处同口径。
//
// [参数] 无。
// [返回] 32 位随机 hex。
// 最近修改时间：2026-11-28；改动原因：新增，客户端归因头。
func newMaasTraceID() string {
	return newMaasHex(16)
}

// newMaasHex 生成指定字节数的随机 hex 串。
//
// 随机源不可用时退化为时间派生，保证归因头永不为空（空头会被上游判成
// 无归属流量，正是要修的形态）。
//
// [参数] n 随机字节数。
// [返回] hex 字符串。
// 最近修改时间：2026-11-28；改动原因：新增，随机 hex 的唯一实现。
func newMaasHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}

// storeMaasSessionLocked 写入一张票据并顺带淘汰超限条目。
//
// 调用方必须持有 globalMaasCache.mu。
//
// [参数] key 缓存键；sess 票据。
// [返回] 无。
// 最近修改时间：2026-11-28；改动原因：新增，票据写入与淘汰收口到唯一入口。
func storeMaasSessionLocked(key string, sess *maasSession) {
	globalMaasCache.sessions[key] = sess
	pruneMaasSessionsLocked(time.Now())
}

// pruneMaasSessionsLocked 清理票据缓存：先删已过期条目，仍超上限时按签发时间
// 从旧到新淘汰到上限内。
//
// 上游会话键改为逐对话派生后，缓存条目基数随对话数增长；不淘汰会让 map 无限
// 膨胀，而插件堆有 128MiB 软上限（memory_limit.go），GC 压力会直接进请求延迟。
//
// 调用方必须持有 globalMaasCache.mu。
//
// [参数] now 当前时间。
// [返回] 清理的条目数。
// 最近修改时间：2026-11-28；改动原因：新增，票据缓存淘汰。
func pruneMaasSessionsLocked(now time.Time) int {
	removed := 0
	for key, sess := range globalMaasCache.sessions {
		if sess == nil || sess.remaining(now) <= 0 {
			delete(globalMaasCache.sessions, key)
			removed++
		}
	}
	if len(globalMaasCache.sessions) <= maasCacheMaxEntries {
		return removed
	}
	type aged struct {
		key string
		at  int64
	}
	list := make([]aged, 0, len(globalMaasCache.sessions))
	for key, sess := range globalMaasCache.sessions {
		list = append(list, aged{key: key, at: sess.MintedAtMs})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at < list[j].at })
	overflow := len(list) - maasCacheMaxEntries
	for i := 0; i < overflow && i < len(list); i++ {
		delete(globalMaasCache.sessions, list[i].key)
		removed++
	}
	return removed
}
