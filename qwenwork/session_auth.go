// session_auth.go implements per-conversation account routing for qwenwork.
//
// scheduler_mode=session spreads conversations across the available qwenwork
// accounts: the SAME conversation is pinned to the SAME account for a bounded
// window (sessionStickinessTTL, default 1h), while DIFFERENT conversations are
// assigned to different accounts (unbound-account first, then round-robin).
//
// Session identity comes from the scheduler pick request Options, which the
// host fills before every pick (see sdk/cliproxy/session.Enrich in
// CLIProxyAPI): Options.Metadata carries the host-derived/execution session
// identity, Options.Headers carries explicit client session headers. When no
// session signal is present (rare), routing falls back to the panel-selected
// account via pickActiveAuth — identical behavior to scheduler_mode=credits.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Session metadata keys written by the host (sdk/cliproxy/executor/types.go).
// Kept as local constants instead of importing the SDK executor package so the
// plugin never depends on a specific CLIProxyAPI version for routing keys.
const (
	executionSessionIDMetadataKey = "execution_session_id"
	derivedSessionIDMetadataKey   = "derived_session_id"
)

// sessionStickinessTTL bounds how long one conversation keeps its account.
// After expiry the binding is released and the next pick re-assigns (variable
// so tests can shrink it; default 1h per product requirement).
var sessionStickinessTTL = time.Hour

// sessionPruneInterval bounds how often expired bindings are swept.
const sessionPruneInterval = 5 * time.Minute

// sessionAuthBinding pins one conversation to one account until ExpiresAt.
type sessionAuthBinding struct {
	AuthID    string
	ExpiresAt time.Time
}

var (
	// sessionAuthMu guards sessionAuthBindings and sessionRR.
	sessionAuthMu sync.RWMutex
	// sessionAuthBindings maps a session key (see extractSessionKey) to its
	// pinned account. Entries are created on first pick of a conversation and
	// removed by the pruner after expiry.
	sessionAuthBindings = make(map[string]sessionAuthBinding)
	// sessionRR is a monotonically increasing counter used to spread new
	// conversations across accounts when every usable account already has
	// bindings.
	sessionRR uint64
)

func init() {
	go func() {
		ticker := time.NewTicker(sessionPruneInterval)
		defer ticker.Stop()
		for range ticker.C {
			pruneSessionBindings()
		}
	}()
}

// normalizeSessionID validates and trims a raw session identifier, mirroring
// the host's session.NormalizeExplicitID contract: opaque printable values up
// to 256 bytes; control-bearing or oversized values are rejected.
func normalizeSessionID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 256 {
		return ""
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return raw
}

// sessionHeaderValue returns the first normalized value of a case-insensitive
// header. Only printable non-empty values are accepted.
func sessionHeaderValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if id := normalizeSessionID(value); id != "" {
				return id
			}
		}
	}
	return ""
}

// minSessionEntropyChars 是会话键参与路由所需的最小信息量。
//
// 只有 1 个字符的会话标识没有身份意义，却会被当成「同一个会话」：生产实测
// 全量请求都带着 X-Session-ID: 1，于是所有客户端在 1 小时粘性窗口里被折叠成
// 同一个会话，选号退化成「钉死在一个账号上」，按负载平摊完全失效。
// 本判据与同仓 traework 插件 2026-11-10 的取证与实现完全一致。
// [参数] 无。
// [返回] 最小字符数。
// 最近修改时间：2026-11-28；改动原因：新增，移植 traework 的低熵会话标识剔除。
const minSessionEntropyChars = 4

// lowEntropySessionID 判定会话标识的信息量是否不足以支撑会话粘性。
//
// 判据只有两条，都来自「这个值不可能是真实的会话身份」：
//  1. 长度小于 minSessionEntropyChars（"1"、"0"、"s1"）；
//  2. 全是同一个字符（"aaaa"、"0000"）。
//
// 纯数字的合法会话 id（如 "123456"）刻意放行：长度本身已经足够区分，误杀会把
// 正常粘性一起关掉。
// [参数] id：已归一化的会话标识。
// [返回] true 表示该标识信息量过低，应剔除后回退到后续信号。
// 最近修改时间：2026-11-28；改动原因：新增，移植 traework 的低熵会话标识剔除。
func lowEntropySessionID(id string) bool {
	if len(id) < minSessionEntropyChars {
		return true
	}
	first := id[0]
	for i := 1; i < len(id); i++ {
		if id[i] != first {
			return false
		}
	}
	return true
}

// extractSessionKeyFromSources derives a stable conversation key from raw
// headers + metadata. Both scheduler.SchedulerOptions and
// executor.ExecutorRequest expose these as flat (headers, metadata) fields,
// so the same priority chain works on both sides of the pick → execute
// handshake. Returns "" when no session signal is present; callers then fall
// back to the panel-selected account.
//
// Priority (strongest signal first):
//  1. explicit execution session metadata (e.g. Codex websocket call id)
//  2. explicit client session headers (Claude Code / Codex / Responses / OpenCode)
//  3. host-derived session identity (stable hash of the conversation root)
//
// Headers are checked before the derived identity: host session.Enrich runs
// before scheduling, so explicit signals are NOT re-derived (their metadata
// stays empty), hence headers win over the derived fallback.
//
// 弱标识必须剔除而不是采信：生产全量请求都带 X-Session-ID: 1，若照收就会把
// 所有客户端折叠成同一个会话，1 小时粘性窗口内选号退化成钉死单一账号，
// 全部流量压在一个上游 MaaS 会话上排队。剔除后继续沿优先级链往下找，
// 实在没有强信号就返回 ""，由调度层按负载平摊。
func extractSessionKeyFromSources(headers map[string][]string, metadata map[string]any) string {
	if v, ok := metadata[executionSessionIDMetadataKey].(string); ok {
		if id := normalizeSessionID(v); id != "" {
			return "execution:" + id
		}
	}
	for _, header := range []string{
		"X-Claude-Code-Session-Id", "Session-Id", "Session_id",
		"X-Session-ID", "X-Session-Affinity", "X-Client-Request-Id",
	} {
		sid := sessionHeaderValue(headers, header)
		if sid == "" || lowEntropySessionID(sid) {
			continue
		}
		return headerSessionPrefix(header) + sid
	}
	if v, ok := metadata[derivedSessionIDMetadataKey].(string); ok {
		if id := normalizeSessionID(v); id != "" {
			return "derived:" + id
		}
	}
	return ""
}

// extractSessionKey derives a stable conversation key from a scheduler pick
// request, using only information the host exposes to scheduler plugins.
// Thin adapter over extractSessionKeyFromSources — see that function for the
// priority chain and rationale.
func extractSessionKey(req pluginapi.SchedulerPickRequest) string {
	return extractSessionKeyFromSources(req.Options.Headers, req.Options.Metadata)
}

// headerSessionPrefix namespaces explicit session headers so different client
// types never collide on the same opaque id.
func headerSessionPrefix(header string) string {
	switch header {
	case "X-Claude-Code-Session-Id":
		return "claude:"
	case "Session-Id", "Session_id":
		return "codex:"
	case "X-Session-ID":
		return "header:"
	case "X-Session-Affinity":
		return "affinity:"
	case "X-Client-Request-Id":
		return "clientreq:"
	default:
		return "header:"
	}
}

// maasSessionKeyFromPayload 从客户端请求体派生「上游 MaaS 会话键」。
//
// 为什么要与路由会话键（extractSessionKeyFromSources）分开：
//   - 路由会话键回答「这条请求该粘在哪个账号上」，它只需要区分对话；
//   - MaaS 会话键回答「这条请求该用哪张上游票据、落到哪个上游 session_id」，
//     它必须逐对话隔离——上游按 session_id 承载会话与前缀缓存，把所有对话
//     压成同一个 session 会同时造成排队与缓存串味。
//
// 种子优先级（对齐 GitHub 社区三份独立实现的口径：dsh-qwenwork-connector
// src/adapter/session.ts:76-103、dsh-qwenwork-connect 同款、
// Agent2Providers wasm/qwenwork/src/protocol.rs:45-56）：
//  1. prompt_cache_key —— 客户端显式声明的缓存键，天然按对话区分；
//  2. user —— 客户端显式声明的终端用户标识；
//  3. system 提示 + 首条 user 消息的 sha256 —— 兜底，保证同一对话恒定、
//     不同对话不同。
//
// 前缀与路由会话键的命名空间（execution:/claude:/codex:/header:/affinity:/
// clientreq:/derived:）不重叠，两种键不会互相串味。
//
// [参数] obj：已反序列化的客户端请求体对象；nil 表示请求体不可解析。
// [返回] MaaS 会话键；无法派生时返回空串（调用方退回账号级固定分片）。
// 最近修改时间：2026-11-28；改动原因：新增，按社区口径把上游会话从「全账号
// 共用一张票」改成逐对话隔离，消除上游排队与前缀缓存串味。
func maasSessionKeyFromPayload(obj map[string]any) string {
	if obj == nil {
		return ""
	}
	if key := strings.TrimSpace(payloadString(obj["prompt_cache_key"])); key != "" {
		return "cache:" + key
	}
	if user := strings.TrimSpace(payloadString(obj["user"])); user != "" {
		return "user:" + user
	}
	// 兜底：system 提示 + 首条 user 消息。只取首条 user 消息，保证同一对话
	// 在多轮追加后仍得到同一个键。
	seed := strings.TrimSpace(payloadString(obj["instructions"]))
	messages, _ := obj["messages"].([]any)
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(payloadString(msg["role"]))) {
		case "system":
			if seed == "" {
				seed = payloadTextFromContent(msg["content"])
			}
		case "user":
			seed += "\x00" + payloadTextFromContent(msg["content"])
			return maasSessionKeyFromSeed(seed)
		}
	}
	return ""
}

// maasSessionKeyFromSeed 把会话种子压成稳定的短键。
//
// [参数] seed：会话种子文本。
// [返回] 形如 "msg:<16 hex>" 的会话键；种子为空时返回空串。
// 最近修改时间：2026-11-28；改动原因：新增，会话种子的唯一散列入口。
func maasSessionKeyFromSeed(seed string) string {
	if strings.TrimSpace(seed) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("qwenwork-maas-session:" + seed))
	return "msg:" + hex.EncodeToString(sum[:8])
}

// payloadString 从请求体对象里读取字符串字段。
//
// [参数] v：字段值。
// [返回] 字符串值；类型不符时返回空串。
// 最近修改时间：2026-11-28；改动原因：新增，请求体字段读取的统一入口。
func payloadString(v any) string {
	s, _ := v.(string)
	return s
}

// payloadTextFromContent 从 OpenAI content 字段取文本。
//
// content 既可能是纯字符串，也可能是多模态分片数组；两种形态都要能取到文本，
// 否则多模态客户端的会话键会退化到空串。
// [参数] v：content 字段值。
// [返回] 拼接后的文本；无法识别时返回空串。
// 最近修改时间：2026-11-28；改动原因：新增，支持多模态 content 形态。
func payloadTextFromContent(v any) string {
	switch content := v.(type) {
	case string:
		return content
	case []any:
		var sb strings.Builder
		for _, part := range content {
			piece, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text := payloadString(piece["text"]); text != "" {
				sb.WriteString(text)
			}
		}
		return sb.String()
	}
	return ""
}

// pickSessionAuth selects the account for one conversation.
//
// sessionKey != "" → sticky session routing:
//   - a fresh, still-usable binding is reused unchanged (1h stickiness);
//   - a stale binding (expired) or a binding whose account became
//     disabled/cooling-down/low-credit is re-assigned;
//   - new assignments prefer accounts with no live bindings, then round-robin
//     across all routable accounts;
//   - when no routable account exists, the current pin is kept if the
//     account still exists, else the first candidate.
//
// sessionKey == "" → fall back to the panel-selected account (same behavior as
// scheduler_mode=credits).
func pickSessionAuth(sessionKey string, candidates []activeAuthCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	if sessionKey == "" {
		return pickActiveAuth(candidates)
	}

	live := make(map[string]struct{}, len(candidates))
	usable := make([]string, 0, len(candidates))
	usableSet := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		live[c.ID] = struct{}{}
		if !c.Disabled && !c.Exhausted && !isAccountCoolingDown(c.ID) {
			usable = append(usable, c.ID)
			usableSet[c.ID] = struct{}{}
		}
	}

	now := time.Now()
	sessionAuthMu.Lock()
	defer sessionAuthMu.Unlock()

	// Sticky hit: binding fresh AND its account still usable.
	if b, ok := sessionAuthBindings[sessionKey]; ok {
		if _, isLive := live[b.AuthID]; isLive {
			if _, isUsable := usableSet[b.AuthID]; isUsable && now.Before(b.ExpiresAt) {
				return b.AuthID
			}
		}
	}

	if len(usable) == 0 {
		// Everything disabled/cooling-down/low-credit: no routable account
		// exists. Return "" so handleSchedulerPick defers (Handled: false) to
		// the host's built-in scheduler (cross-provider failover); keep the
		// binding so recovery can re-pin it once an account recovers.
		return ""
	}

	// Fresh assignment: prefer accounts with no live bindings (spreads
	// conversations across accounts), then round-robin.
	boundCounts := make(map[string]int, len(usable))
	for _, b := range sessionAuthBindings {
		if now.Before(b.ExpiresAt) {
			boundCounts[b.AuthID]++
		}
	}
	next := ""
	for i := 0; i < len(usable); i++ {
		candID := usable[(int(sessionRR)+i)%len(usable)]
		if boundCounts[candID] == 0 {
			next = candID
			sessionRR++
			break
		}
	}
	if next == "" {
		next = usable[int(sessionRR%uint64(len(usable)))]
		sessionRR++
	}

	sessionAuthBindings[sessionKey] = sessionAuthBinding{AuthID: next, ExpiresAt: now.Add(sessionStickinessTTL)}
	return next
}

// pruneSessionBindings removes expired bindings. Called by the background
// pruner; also safe to call from tests directly.
func pruneSessionBindings() {
	now := time.Now()
	sessionAuthMu.Lock()
	defer sessionAuthMu.Unlock()
	for key, b := range sessionAuthBindings {
		if !now.Before(b.ExpiresAt) {
			delete(sessionAuthBindings, key)
		}
	}
}

// evictSessionBindingsForAuth removes every binding pinned to the given
// account (regardless of expiry), so conversations stuck on a failing account
// are re-assigned on the next pick. Returns the number of bindings removed.
// Called when the account enters failover cooldown.
func evictSessionBindingsForAuth(authID string) int {
	if authID == "" {
		return 0
	}
	sessionAuthMu.Lock()
	defer sessionAuthMu.Unlock()
	evicted := 0
	for key, b := range sessionAuthBindings {
		if b.AuthID == authID {
			delete(sessionAuthBindings, key)
			evicted++
		}
	}
	return evicted
}

// clearSessionBindings wipes all session bindings. Test helper; never called
// in production paths.
func clearSessionBindings() {
	sessionAuthMu.Lock()
	sessionAuthBindings = make(map[string]sessionAuthBinding)
	sessionRR = 0
	sessionAuthMu.Unlock()
}
