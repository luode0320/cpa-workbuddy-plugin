// session_auth.go implements per-conversation account routing for traework.
//
// scheduler_mode=session spreads conversations across the available traework
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
//
// This is a straight port of workbuddy/session_auth.go (pure routing logic,
// no workbuddy protocol dependencies) — kept byte-compatible so future
// workbuddy fixes can be synced function-by-function.
package main

import (
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
	// sessionAuthMu guards sessionAuthBindings.
	sessionAuthMu sync.RWMutex
	// sessionAuthBindings maps a session key (see extractSessionKey) to its
	// pinned account. Entries are created on first pick of a conversation and
	// removed by the pruner after expiry.
	sessionAuthBindings = make(map[string]sessionAuthBinding)
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
// 全量流量都带着 X-Session-ID: 1，于是所有客户端在 1 小时粘性窗口里被折叠成
// 同一个会话，选号退化成「钉死在一个账号上」，按负载平摊完全失效。
// [参数] 无。
// [返回] 最小字符数。
// 最近修改时间：2026-11-10；改动原因：新增，见 parseSessionKeyEntropy 的取证说明。
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
// 最近修改时间：2026-11-10；改动原因：新增，避免单一弱标识把全量流量折成一个会话。
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
func extractSessionKeyFromSources(headers map[string][]string, metadata map[string]any) string {
	if v, ok := metadata[executionSessionIDMetadataKey].(string); ok {
		if id := normalizeSessionID(v); id != "" {
			return "execution:" + id
		}
	}
	// 弱标识必须剔除而不是采信：生产全量请求都带 X-Session-ID: 1，若照收就会把
	// 所有客户端折叠成同一个会话，1 小时粘性窗口内选号退化成钉死单一账号。
	// 剔除后继续沿优先级链往下找，实在没有强信号就返回 ""，由调度层按负载平摊。
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

// pickSessionAuth selects the account for one conversation.
//
// sessionKey != "" → sticky session routing:
//   - a fresh, still-usable binding is reused unchanged (1h stickiness);
//   - a stale binding (expired) or a binding whose account became
//     disabled/exhausted/cooling-down/测试失败 is re-assigned;
//   - new assignments prefer accounts with no live bindings, then round-robin
//     across all usable accounts (the candidate slice is already ordered
//     HIGH-CREDIT-FIRST — healthiest first — by handleSchedulerPick);
//   - when every account is unavailable, "" is returned so the scheduler
//     defers to the host's built-in scheduler (cross-provider failover); the
//     binding is kept for sticky recovery.
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
		if !c.Disabled && !c.Exhausted && accountRoutable(c.ID) {
			usable = append(usable, c.ID)
			usableSet[c.ID] = struct{}{}
		}
	}

	now := time.Now()
	sessionAuthMu.Lock()
	defer sessionAuthMu.Unlock()

	// Sticky hit: binding fresh AND its account still usable。
	//
	// 命中后还要过一次在跑数软上限：粘性只该保证「同一会话优先复用同一个号」，
	// 不能变成「把同一会话的并发子请求全塞给同一个号」。上游 Trae 每账号并行槽位
	// 默认只有 2，超出的部分不报错而是在账号侧排队，直接推高首字延迟。实测账号
	// 并发 1 → 首字 4596ms，15+ → 7795ms，而全局并发 19 时单个号就吃掉约 14 个。
	// 因此：命中且未超软上限 → 直接复用（保住缓存与上游亲和）；已超上限且池中
	// 存在更空闲的可用账号 → 改绑到最空闲的号。
	if b, ok := sessionAuthBindings[sessionKey]; ok {
		if _, isLive := live[b.AuthID]; isLive {
			if _, isUsable := usableSet[b.AuthID]; isUsable && now.Before(b.ExpiresAt) {
				current := accountLoadOf(b.AuthID)
				if current < sessionAccountInflightSoftCap {
					return b.AuthID
				}
				if alt := pickLessLoadedThan(usable, b.AuthID, current); alt != "" {
					sessionAuthBindings[sessionKey] = sessionAuthBinding{AuthID: alt, ExpiresAt: now.Add(sessionStickinessTTL)}
					return alt
				}
				return b.AuthID
			}
		}
	}

	if len(usable) == 0 {
		// Everything disabled/exhausted/unavailable: no healthy traework
		// account exists. Return "" so handleSchedulerPick defers (Handled:
		// false) to the host's built-in scheduler, which can fail over to
		// other providers' accounts (e.g. workbuddy) instead of pinning this
		// conversation to a dead account. The existing binding is kept: once
		// an account recovers, the sticky re-hit restores the assignment.
		return ""
	}

	// Fresh assignment: 在跑请求数最少优先，绑定会话数次之，平级保留 usable
	// 已有的高积分优先顺序。
	//
	// 改成按真实并发记账而不是按会话数选号，是 2026-11-10 生产取证的直接结论：
	// 上游 Trae 每个账号的并行槽位默认只有 2，超出部分不报错而是在账号侧排队，
	// 表现为首字延迟升高。按会话分配的旧逻辑下，一个会话连发多个并发子请求时
	// 全部压到同一个号，实测账号并发中位数 16、峰值 25，而活跃会话中位数只有 2；
	// 首字延迟随账号并发单调上升：并发 1 → 4596ms，2-3 → 5974ms，4-7 → 6299ms，
	// 8-14 → 7027ms，15+ → 7795ms。
	boundCounts := make(map[string]int, len(usable))
	for _, b := range sessionAuthBindings {
		if now.Before(b.ExpiresAt) {
			boundCounts[b.AuthID]++
		}
	}
	// 面板选中作为初始候选参与竞争：它是「希望走这个号」，不该变成
	// 「把所有并发都塞给这个号」，所以只在它不比别的号更忙时才保留。
	next := ""
	if active := getActiveAuthID(); active != "" {
		if _, isUsable := usableSet[active]; isUsable {
			next = active
		}
	}
	bestLoad, bestBound := -1, -1
	if next != "" {
		bestLoad, bestBound = accountLoadOf(next), boundCounts[next]
	}
	for _, id := range usable {
		load, bound := accountLoadOf(id), boundCounts[id]
		if bestLoad < 0 || load < bestLoad || (load == bestLoad && bound < bestBound) {
			next, bestLoad, bestBound = id, load, bound
		}
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
	sessionAuthMu.Unlock()
}
