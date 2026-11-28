// accountFailover.go 实现按账号的阶梯冷却路由排除。
//
// 上游账号失败（HTTP 429 / 402 / 5xx 或传输层错误）后进入冷却窗口，窗口期内
// 调度器路由的新请求一律跳过该账号，让会话改落到健康账号，而不是继续往同一个
// 已耗尽的账号上堆失败。
//
// 冷却时长按连续失败次数阶梯递增：第 1 次 60s、第 2 次 120s、第 3 次 180s……
// 封顶 10 分钟。瞬时过载类失败（429 无额度标记 / 软限流文案 / 上游零字节断流）
// 与硬失败走同一条阶梯路径，不再单独豁免计数。连续失败计数同时用于面板
// 「失败N次」展示；异常池隔离已于 2026-09-08 移除，计数不再触发任何冻结或停用。
//
// Qoder 系专属：排队帧（isQueueFrame）仍保持「不计数不冷却」——它是上游容量
// 状态而非账号故障；110 日限额（isDailyCountExceeded）另有 24 小时持久化冷却
// （daily_cooling），不受本阶梯影响。
//
// 任意一次成功请求都会清零计数并立即解除冷却。冷却状态只存内存：不写 auth
// 文件、不写 DB，进程重启即清空。可通过插件配置 `account_failover: false`
// 整体关闭该机制。
//
// 最近修改时间：2026-10-19；改动原因：阶梯基数由 30s 调整为 60s（仍为 base × count、上限 10 分钟），软失败并入计数。
package main

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// failoverCooldownBase 是阶梯基数：每次连续失败使冷却窗口增加一个基数，
// 首次失败即冷却一个基数（60s），第 n 次失败冷却 n × 60s。
const failoverCooldownBase = 60 * time.Second

// failoverCooldownMax 是阶梯上限：连续失败再多也不超过 10 分钟。
const failoverCooldownMax = 10 * time.Minute

// failoverCooldownLadderCap 是达到上限所需的连续失败次数（10min / 60s = 10）。
// 先比较次数再相乘，避免 count 极大时的乘法溢出。
const failoverCooldownLadderCap = int(failoverCooldownMax / failoverCooldownBase)

// failoverPruneInterval bounds how often stale (zero-count) failover states
// are swept from memory. Aligned with the session-binding pruner.
const failoverPruneInterval = 5 * time.Minute

var (
	// failoverEnabled gates the whole mechanism. Default true; set false via
	// plugin config account_failover: false to restore pre-failover behavior.
	failoverEnabled   = true
	failoverEnabledMu sync.RWMutex

	// failoverMu guards failoverStates.
	failoverMu     sync.Mutex
	failoverStates = make(map[string]*authFailoverState)
)

// authFailoverState tracks consecutive failures for one account.
type authFailoverState struct {
	count         int       // consecutive failures; reset to 0 on success
	cooldownUntil time.Time // zero means not cooling down
}

func init() {
	go func() {
		ticker := time.NewTicker(failoverPruneInterval)
		defer ticker.Stop()
		for range ticker.C {
			pruneFailoverStates()
		}
	}()
}

// failoverActive reports whether the failover mechanism is enabled.
func failoverActive() bool {
	failoverEnabledMu.RLock()
	defer failoverEnabledMu.RUnlock()
	return failoverEnabled
}

// setFailoverEnabled toggles the whole mechanism (config / tests).
func setFailoverEnabled(on bool) {
	failoverEnabledMu.Lock()
	failoverEnabled = on
	failoverEnabledMu.Unlock()
}

// failoverCooldownFor 返回第 count 次连续失败对应的阶梯冷却时长。
// 时长按 failoverCooldownBase 线性递增并在 failoverCooldownMax 封顶，
// count <= 0 返回零值表示不冷却。
// [参数] count: 当前连续失败次数，由 recordAccountFailure 递增、成功后清零。
// [返回] 本次失败应施加的冷却时长。
// 最近修改时间：2026-10-19；改动原因：阶梯基数由 30s 调整为 60s（仍为 base × count、上限 10 分钟）。
func failoverCooldownFor(count int) time.Duration {
	if count <= 0 {
		return 0
	}
	if count >= failoverCooldownLadderCap {
		return failoverCooldownMax
	}
	return failoverCooldownBase * time.Duration(count)
}

// isAccountFailure reports whether an upstream response counts as an account
// failure for failover purposes. Transport-level failures (status 0), 5xx,
// rate limiting (429 / body markers), hard credit errors and account-level
// 4xx (401/403/404/405 — wrong token, missing permission, endpoint or
// method not available for THIS account) all count. Business 4xx (400) is
// excluded: it reflects the request, not the account.
func isAccountFailure(status int, body string) bool {
	if isDailyCountExceeded(status, body) {
		return true
	}
	if status == 0 || status >= 500 {
		return true
	}
	if isSoftRateLimit(status, body) || isHardCreditError(status, body) {
		return true
	}
	return isAccountLevel4xx(status)
}

// isAccountLevel4xx reports whether a 4xx status reflects an account-level
// problem (the credential/endpoint on this account is wrong) rather than a
// request-level problem. 401/403/404/405 mean the upstream rejected access
// for THIS account: token expired, no permission, route missing, or method
// not allowed. Retrying with a different account has a real chance of
// succeeding. 400 is intentionally excluded — it almost always means the
// request body was malformed by us and would fail identically on every
// other account.
//
// 429 (Too Many Requests / soft rate limit) is INCLUDED here as of v0.9.1:
// the upstream soft rate limit is usually per-account or per-tenant, so
// rotating to the next candidate (filtered by cooldown) is the
// cheapest way to recover inside a single request. The cross-request
// cooldown still applies in parallel via isAccountFailure /
// recordAccountFailure — i.e. a 429-triggered same-request rotation also
// lifts the failed account's laddered cooldown so subsequent requests route
// away from it. When the upstream limit is genuinely global (shared
// IP/region quota across every qwenwork account), same-request rotation
// burns the budget without progress; pickNextAuth still has to surface an
// ok=false signal once the pool is exhausted.
func isAccountLevel4xx(status int) bool {
	switch status {
	case http.StatusUnauthorized, // 401
		http.StatusForbidden,        // 403
		http.StatusNotFound,         // 404
		http.StatusMethodNotAllowed, // 405
		http.StatusTooManyRequests:  // 429 — v0.9.1: same-request rotation
		return true
	}
	return false
}

// shouldRotateOnStatus 判定一个非 2xx 上游状态是否值得在同一请求内换号续试。
//
// 与 isAccountLevel4xx 的分工：本函数覆盖同请求换号的全集，即在账号级 4xx
// 之外额外纳入 5xx。5xx 在高峰期几乎必然发生（上游过载通常只影响部分后端），
// 过去只在跨请求冷却层级处理，等于让用户直接承受第一次失败。
//
// 仍然排除的形态：
//   - 402 硬额度：换号也可能撞额度墙，且明确由生命周期写盘禁用接管；
//   - 400/422 请求形状：换任何账号都同样失败，白烧预算。
//
// [参数] status：上游 HTTP 状态码。
// [返回] true 表示应在同请求内换下一个账号。
// 最近修改时间：2026-11-28；改动原因：新增，把 5xx 纳入同请求换号（对齐 workbuddy）。
func shouldRotateOnStatus(status int) bool {
	if isAccountLevel4xx(status) {
		return true
	}
	return status >= 500 && status <= 599
}

// shouldRotateOnUpstreamErr is the unified same-request rotation gate shared
// by the execute loop and the stream paths. HTTP 200 means the transport
// succeeded, but the SSE body may still carry a business-level failure (an
// OpenAI-convention error frame); those are classified through
// isAccountFailure (credit / rate-limit markers). Any other status goes
// through shouldRotateOnStatus, which covers account-level 4xx plus 5xx.
//
// （同步自 workbuddy：200 SSE 业务错误换号 2026-09-06；5xx 纳入换号 2026-11-28）
func shouldRotateOnUpstreamErr(status int, errBody string) bool {
	if status == http.StatusOK {
		return isAccountFailure(status, errBody)
	}
	return shouldRotateOnStatus(status)
}

// normalizeFailoverKey canonicalizes an auth identifier across various representations:
// trims spaces, strips .json suffix, and removes provider prefixes.
func normalizeFailoverKey(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimSuffix(s, ".json")
	for _, p := range []string{"workbuddy-", "qwenwork-", "traework-"} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

func setCooldownRecordLocked(authID string, until time.Time, count int, bumpCount bool) {
	keys := []string{authID}
	if norm := normalizeFailoverKey(authID); norm != "" && norm != authID {
		keys = append(keys, norm)
	}
	for _, k := range keys {
		st := failoverStates[k]
		if st == nil {
			st = &authFailoverState{}
			failoverStates[k] = st
		}
		if bumpCount {
			st.count++
		} else if count > 0 {
			st.count = count
		}
		st.cooldownUntil = until
	}
}

// recordAccountFailure 递增账号连续失败计数并按阶梯延长冷却窗口。
// 返回 true 表示该失败已计入。调用方须使用与调度器相同的 auth.ID 作为键。
//
// 瞬时过载类失败（429 无额度标记、软限流文案、上游零字节断流，见
// isTransientThrottle）与硬失败（额度耗尽 / 401/403/404/405 / 5xx / 传输错误）
// 共用同一条计数与阶梯路径：软失败不再豁免计数，网关抖动会让账号沿阶梯
// 逐次冷却更久，直到出现一次成功请求才清零回落到 60s。
//
// Qoder 系专属红线（不改）：排队帧 isQueueFrame 是上游容量状态而非账号故障，
// 保持「不计数不冷却」；110 日限额 isDailyCountExceeded 走独立 24h 持久化冷却
// （daily_cooling），不在本阶梯路径内。
//
// 异常池隔离已于 2026-09-08 移除：失败只延长冷却窗口，账号到期后自动重新
// 进入路由，不冻结、不做每日刷新、不需要人工解冻。
// [参数] authID: 账号唯一标识；status: 上游 HTTP 状态码；body: 上游响应正文。
// [返回] true 表示失败已计入冷却阶梯，false 表示按判定不予记账。
// 最近修改时间：2026-10-16 23:29:50；改动原因：软失败并入阶梯计数，移除 coolDownAccount 旁路。
func recordAccountFailure(authID string, status int, body string) bool {
	if !failoverActive() {
		return false
	}
	// 排队帧是上游容量状态，与账号健康度无关：既不推进计数也不冷却账号。
	// 若在此冷却，上游排队持续期间每次请求都会把路由到的健康账号重新打进
	// 冷却，整池被永久冷掉后宿主只能报 auth_unavailable（2026-10-15 生产实证：
	// serviceAvailable:true 且 6 账号 queueCount 单调递增，证明饱和的是上游队列）。
	// 快速失败与跨 provider 换道分别由插件流路径、宿主 503 状态码通道负责。
	if isQueueFrame(body) {
		return false
	}
	if !isAccountFailure(status, body) && !isTransientThrottle(status, body) {
		return false
	}
	now := time.Now()
	failoverMu.Lock()
	defer failoverMu.Unlock()
	st := failoverStates[authID]
	curCount := 0
	if st != nil {
		curCount = st.count
	} else if norm := normalizeFailoverKey(authID); norm != "" {
		if stNorm := failoverStates[norm]; stNorm != nil {
			curCount = stNorm.count
		}
	}
	newCount := curCount + 1
	until := now.Add(failoverCooldownFor(newCount))
	setCooldownRecordLocked(authID, until, newCount, false)
	return true
}

// isAccountCoolingDown reports whether the account is currently inside its
// cooldown window and should be skipped by routing.
// It supports both exact key match and normalized-key alias resolution.
func isAccountCoolingDown(authID string) bool {
	if isAccountDailyCooling(authID) {
		return true
	}
	if !failoverActive() {
		return false
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	failoverMu.Lock()
	defer failoverMu.Unlock()
	now := time.Now()
	if st := failoverStates[authID]; st != nil && now.Before(st.cooldownUntil) {
		return true
	}
	target := normalizeFailoverKey(authID)
	if target == "" {
		return false
	}
	if st := failoverStates[target]; st != nil && now.Before(st.cooldownUntil) {
		return true
	}
	for k, st := range failoverStates {
		if st == nil || !now.Before(st.cooldownUntil) {
			continue
		}
		normK := normalizeFailoverKey(k)
		if normK == target || strings.Contains(normK, target) || strings.Contains(target, normK) {
			return true
		}
	}
	return false
}

// resetAccountFailover clears the failure counter and cooldown after a
// successful request. Call it on every upstream success.
func resetAccountFailover(authID string) {
	if !failoverActive() {
		return
	}
	failoverMu.Lock()
	defer failoverMu.Unlock()
	keys := []string{authID}
	if norm := normalizeFailoverKey(authID); norm != "" && norm != authID {
		keys = append(keys, norm)
	}
	for _, k := range keys {
		if st := failoverStates[k]; st != nil {
			st.count = 0
			st.cooldownUntil = time.Time{}
		}
	}
}

// pruneFailoverStates removes zero-count states (successfully reset, no
// longer cooling down). Failed-but-not-cooling-down states are kept: their
// counter persists until a success resets it.
func pruneFailoverStates() {
	failoverMu.Lock()
	defer failoverMu.Unlock()
	for k, st := range failoverStates {
		if st.count == 0 {
			delete(failoverStates, k)
		}
	}
}

// failoverStateSnapshot returns a copy of the account's failover state.
// Used by tests and by the dashboard (panel.go) to surface the consecutive
// failure count + cooldown window in the account cards.
func failoverStateSnapshot(authID string) (count int, cooldownUntil time.Time, ok bool) {
	failoverMu.Lock()
	defer failoverMu.Unlock()
	st, ok := failoverStates[authID]
	if !ok {
		return 0, time.Time{}, false
	}
	return st.count, st.cooldownUntil, true
}

// clearFailoverStates wipes all failover state. Test helper; never called in
// production paths.
func clearFailoverStates() {
	failoverMu.Lock()
	failoverStates = make(map[string]*authFailoverState)
	failoverMu.Unlock()
}

// clearFailoverStateForAuth removes failover state for a single account key.
// Called when an account is deleted so its cooldown/counter don't leak into a
// future auth that reuses the same auth.ID (or keep a dead entry in memory).
// （同步自 workbuddy 0.14.7 账号删除功能）
func clearFailoverStateForAuth(authID string) {
	if authID == "" {
		return
	}
	failoverMu.Lock()
	delete(failoverStates, authID)
	failoverMu.Unlock()
}

// setFailoverCooldownUntil overrides the cooldown deadline for an account.
// Test helper (lets tests simulate cooldown expiry); never called in
// production paths.
func setFailoverCooldownUntil(authID string, until time.Time) {
	failoverMu.Lock()
	defer failoverMu.Unlock()
	st := failoverStates[authID]
	if st == nil {
		st = &authFailoverState{}
		failoverStates[authID] = st
	}
	st.cooldownUntil = until
}
