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

// 说明：recordSSEErrorFailureForModel 用 http.StatusOK 表达「响应本身是 200，
// 失败信息在正文里」，因此本文件需要 http 包。

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
//
// 双层冷却：账号级字段保持原语义（面板展示、全池让位），模型维度由
// modelStates 承担。一次失败按 classifyUpstreamFailure 的判定落到其中一层，
// 不再所有失败都写账号级——否则限流这类「只对某个模型成立」的失败会把整个
// 账号从所有模型上摘掉，反而缩小可用池。
type authFailoverState struct {
	count         int       // consecutive failures; reset to 0 on success
	cooldownUntil time.Time // zero means not cooling down
	// lastModel 记录最近一次触发冷却的上游模型（已归一化），供冷却前探活回放。
	lastModel string
	// modelStates 保存账号×模型级冷却，键为归一化后的上游模型名。
	modelStates map[string]*modelFailoverState
}

// modelFailoverState tracks consecutive failures for one account × model pair.
type modelFailoverState struct {
	count         int
	cooldownUntil time.Time
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
	if status == 0 || status >= 500 {
		return true
	}
	if isSoftRateLimit(status, body) || isHardCreditError(status, body) {
		return true
	}
	return isAccountLevel4xx(status)
}

// shouldRotateOnUpstreamErr 判定一次上游失败是否值得在同请求内换号。
// 账号级 4xx（401/403/404/405）与 429 沿用既有契约；HTTP 200 内的 SSE
// 业务错误帧（上游以 200 承载配额/限流等失败，见 traework 生产实证）
// 走 body-marker 分类，命中账号级同样换号。5xx/0/402 维持跨请求冷却
// 路径，不在同请求内烧预算。
// 最近修改时间：2026-09-06 17:00:00；改动原因：同步 traework 的 200 SSE 业务错误换号修复。
func shouldRotateOnUpstreamErr(status int, errBody string) bool {
	if status == http.StatusOK {
		return isAccountFailure(status, errBody)
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
// 429 (Too Many Requests / soft rate limit) is INCLUDED here as of v0.14.2:
// the upstream soft rate limit is usually per-account or per-tenant, so
// rotating to the next candidate (filtered by cooldown) is the
// cheapest way to recover inside a single request. The cross-request
// cooldown still applies in parallel via isAccountFailure /
// recordAccountFailure — i.e. a 429-triggered same-request rotation also
// lifts the failed account's laddered cooldown so subsequent requests route
// away from it. When the upstream limit is genuinely global (shared
// IP/region quota across every workbuddy account), same-request rotation
// burns the budget without progress; pickNextAuth still has to surface an
// ok=false signal once the pool is exhausted.
func isAccountLevel4xx(status int) bool {
	switch status {
	case http.StatusUnauthorized, // 401
		http.StatusForbidden,        // 403
		http.StatusNotFound,         // 404
		http.StatusMethodNotAllowed, // 405
		http.StatusTooManyRequests:  // 429 — v0.14.2: same-request rotation
		return true
	}
	return false
}

// normalizeFailoverKey canonicalizes an auth identifier across various representations:
// trims spaces, strips .json suffix, and removes provider prefixes.
func normalizeFailoverKey(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimSuffix(s, ".json")
	for _, p := range []string{"workbuddy-", "qoderwork-", "traework-"} {
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
// 异常池隔离已于 2026-09-08 移除：失败只延长冷却窗口，账号到期后自动重新
// 进入路由，不冻结、不做每日刷新、不需要人工解冻。
// [参数] authID: 账号唯一标识；status: 上游 HTTP 状态码；body: 上游响应正文。
// [返回] true 表示失败已计入冷却阶梯，false 表示按判定不予记账。
// 最近修改时间：2026-10-16 23:29:50；改动原因：软失败并入阶梯计数，移除 coolDownAccount 旁路。
func recordAccountFailure(authID string, status int, body string) bool {
	return recordAccountFailureForModel(authID, "", status, body)
}

// recordAccountFailureForModel 按分类维度记账：账号级失败写账号层，
// 账号×模型级失败写模型层并同时记录 lastModel。
//
// 这是周期 2 的核心入口。模型维度必须由调用方传入已归一化的上游模型名
// （resolveUpstreamModel 的结果）；传空串时退化为纯账号级记账，与历史行为一致，
// 供尚未改造的调用点继续工作。
//
// 记账判据仍由 classifyUpstreamFailure 统一给出，与历史
// isAccountFailure || isTransientThrottle 完全等价（见 failure_class_test.go）。
//
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// [返回] true 表示失败已计入冷却阶梯，false 表示按判定不予记账。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增账号×模型双层冷却。
func recordAccountFailureForModel(authID, model string, status int, body string) bool {
	if !failoverActive() {
		return false
	}
	return recordFailureScoped(authID, model, classifyUpstreamFailure(status, body))
}

// recordSSEErrorFailureForModel 记录 HTTP 200 承载的业务错误帧。
//
// 这类失败不经过状态码判据（响应本身是 200），调用方已确认它值得记账，
// 因此不允许被分类成「不冷却」。额度类错误帧仍归账号级（额度耗尽与模型无关），
// 其余（限流、并发、模型不可用）归账号×模型级——与第 3 节「HTTP 200 内的
// 账号级错误帧 → 账号×模型」一致。
//
// [参数] authID：账号唯一标识；model：已归一化的上游模型名；body：错误帧文本。
// [返回] true 表示已计入冷却。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增，SSE 业务错误帧按模型维度记账。
func recordSSEErrorFailureForModel(authID, model, body string) bool {
	if !failoverActive() {
		return false
	}
	scope := failureScopeAuthModel
	if isHardCreditError(http.StatusOK, body) {
		scope = failureScopeAccount
	}
	return recordFailureScoped(authID, model, scope)
}

// recordFailureScoped 按给定维度写入冷却状态，是记账的唯一落点。
// 调用方必须已完成分类与开关判定。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// scope：记账维度。
// [返回] true 表示已写入；scope 为 none 时返回 false。
// 最近修改时间：2026-10-18 23:40:00；改动原因：抽出唯一写入落点，避免多处复制阶梯逻辑。
func recordFailureScoped(authID, model string, scope failureScope) bool {
	if !recordCooldownScoped(authID, model, scope) {
		return false
	}
	disableAccountForFailover(authID, model)
	return true
}

// recordCooldownScoped 按给定维度写入内存阶梯冷却，是冷却侧的唯一写入落点。
// 不碰盘上禁用状态——写盘由 recordFailureScoped 在锁外单独发起，避免持锁做 IO。
//
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// scope：记账维度。
// [返回] true 表示已写入；scope 为 none 时返回 false。
// 最近修改时间：2026-10-24；改动原因：拆分出纯内存写入，供失败双写复用。
func recordCooldownScoped(authID, model string, scope failureScope) bool {
	if scope == failureScopeNone {
		return false
	}
	model = strings.TrimSpace(model)
	now := time.Now()
	failoverMu.Lock()
	defer failoverMu.Unlock()

	// 账号×模型级失败：只冷却该模型，不摘掉整个账号。
	// 模型名为空时无维度可写，退化为账号级，避免失败被静默丢弃。
	if scope == failureScopeAuthModel && model != "" {
		for _, k := range failoverKeysLocked(authID) {
			st := ensureFailoverStateLocked(k)
			st.lastModel = model
			ms := st.modelStates[model]
			if ms == nil {
				ms = &modelFailoverState{}
				st.modelStates[model] = ms
			}
			ms.count++
			ms.cooldownUntil = now.Add(failoverCooldownFor(ms.count))
		}
		return true
	}

	// 账号级失败：沿用既有阶梯与键别名逻辑，行为不变。
	curCount := 0
	if st := failoverStates[authID]; st != nil {
		curCount = st.count
	} else if norm := normalizeFailoverKey(authID); norm != "" {
		if stNorm := failoverStates[norm]; stNorm != nil {
			curCount = stNorm.count
		}
	}
	newCount := curCount + 1
	until := now.Add(failoverCooldownFor(newCount))
	setCooldownRecordLocked(authID, until, newCount, false)
	if model != "" {
		for _, k := range failoverKeysLocked(authID) {
			if st := failoverStates[k]; st != nil {
				st.lastModel = model
			}
		}
	}
	return true
}

// failoverKeysLocked 返回账号键及其归一化别名，供同一状态的多个键同步写入。
// 调用方必须已持有 failoverMu。
// [参数] authID：账号唯一标识。
// [返回] 去重后的键切片，至少包含 authID 本身。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增，消除重复的键别名构造。
func failoverKeysLocked(authID string) []string {
	keys := []string{authID}
	if norm := normalizeFailoverKey(authID); norm != "" && norm != authID {
		keys = append(keys, norm)
	}
	return keys
}

// ensureFailoverStateLocked 返回账号状态，不存在时创建并注册。
// 调用方必须已持有 failoverMu。
// [参数] key：failoverStates 的键。
// [返回] 该键对应的状态指针。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增，供模型维度惰性初始化。
func ensureFailoverStateLocked(key string) *authFailoverState {
	st := failoverStates[key]
	if st == nil {
		st = &authFailoverState{modelStates: make(map[string]*modelFailoverState)}
		failoverStates[key] = st
	} else if st.modelStates == nil {
		st.modelStates = make(map[string]*modelFailoverState)
	}
	return st
}

// isAccountCoolingDown reports whether the account is currently inside its
// cooldown window and should be skipped by routing.
// It supports both exact key match and normalized-key alias resolution
// (e.g. matching UID to workbuddy-<UID>.json).
func isAccountCoolingDown(authID string) bool {
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

// isAuthModelCoolingDown 判定账号在指定模型上是否处于冷却中。
//
// 路由与换号必须使用本函数而非 isAccountCoolingDown：账号级冷却会让该账号
// 在所有模型上让位，而账号×模型级冷却只应让位给同一个模型。模型名为空时
// 退化为账号级判定，保证调用方尚未传模型时行为与历史一致。
//
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空。
// [返回] true 表示该账号不应承载该模型的请求。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增账号×模型双层判定。
func isAuthModelCoolingDown(authID, model string) bool {
	if isAccountCoolingDown(authID) {
		return true
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
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
	for _, k := range failoverKeysLocked(authID) {
		st := failoverStates[k]
		if st == nil {
			continue
		}
		if ms := st.modelStates[model]; ms != nil && now.Before(ms.cooldownUntil) {
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
	for _, k := range failoverKeysLocked(authID) {
		if st := failoverStates[k]; st != nil {
			st.count = 0
			st.cooldownUntil = time.Time{}
			st.lastModel = ""
			// 一次成功只证明该账号整体可用；模型维度的残留冷却若不清，
			// 已恢复的账号仍会被单个历史模型挡住。
			st.modelStates = nil
		}
	}
}

// resetAuthModelFailover 只清除指定账号×模型的冷却，保留账号级状态。
// 供冷却前探活确认「该模型已恢复」时调用，避免把其它模型的冷却一起抹掉。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增，供探活精确清除模型维度。
func resetAuthModelFailover(authID, model string) {
	clearAuthModelCooldowns(authID, model)
}

// clearAuthModelCooldowns 清除某个模型上的冷却记录。
//
// 传空 authID 时清空该模型在所有账号上的记录，用于「一次成功证明模型已恢复」
// 的场景；传了 authID 时只清该账号。账号级冷却不受影响——它代表账号整体不可用，
// 与单个模型的恢复无关。
//
// [参数] authID：账号标识，空表示全部账号；model：已归一化的上游模型名。
// 最近修改时间：2026-10-20；改动原因：新增，统一模型维度清理的唯一落点。
func clearAuthModelCooldowns(authID, model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	authID = strings.TrimSpace(authID)
	failoverMu.Lock()
	defer failoverMu.Unlock()
	for k, st := range failoverStates {
		if st == nil || st.modelStates == nil {
			continue
		}
		if authID != "" && !failoverKeyMatches(k, authID) {
			continue
		}
		delete(st.modelStates, model)
	}
}

// failoverKeyMatches 判定一个 failoverStates 键是否指向给定账号。
// 键可能是 UID、auth.ID 或 auth_index，因此同时比对归一化别名。
// [参数] key：failoverStates 的键；authID：目标账号标识。
// [返回] true 表示该键属于目标账号。
// 最近修改时间：2026-10-20；改动原因：新增，供按账号清理模型冷却。
func failoverKeyMatches(key, authID string) bool {
	if key == authID {
		return true
	}
	return normalizeFailoverKey(key) == normalizeFailoverKey(authID)
}

// pruneFailoverStates removes zero-count states (successfully reset, no
// longer cooling down). Failed-but-not-cooling-down states are kept: their
// counter persists until a success resets it.
func pruneFailoverStates() {
	failoverMu.Lock()
	defer failoverMu.Unlock()
	for k, st := range failoverStates {
		if st == nil {
			delete(failoverStates, k)
			continue
		}
		// 账号层已成功清零且没有模型维度残留时整体删除。模型维度与账号层同语义：
		// 计数保留到一次成功为止，不按冷却到期清理——否则阶梯会反复回落到 60s。
		if st.count == 0 && len(st.modelStates) == 0 {
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

// authModelFailoverSnapshot 返回账号×模型维度的冷却状态，供单测与诊断使用。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名。
// [返回] 该模型的连续失败次数、冷却截止时间、是否存在记录。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增账号×模型双层诊断入口。
func authModelFailoverSnapshot(authID, model string) (count int, cooldownUntil time.Time, ok bool) {
	failoverMu.Lock()
	defer failoverMu.Unlock()
	st := failoverStates[authID]
	if st == nil {
		return 0, time.Time{}, false
	}
	ms := st.modelStates[strings.TrimSpace(model)]
	if ms == nil {
		return 0, time.Time{}, false
	}
	return ms.count, ms.cooldownUntil, true
}

// lastFailoverModel 返回账号最近一次触发冷却的模型名，供冷却前探活回放。
// [参数] authID：账号唯一标识。
// [返回] 归一化后的上游模型名；无记录时为空串。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增，供周期 3 探活读取失败模型。
func lastFailoverModel(authID string) string {
	failoverMu.Lock()
	defer failoverMu.Unlock()
	if st := failoverStates[strings.TrimSpace(authID)]; st != nil {
		return st.lastModel
	}
	return ""
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
