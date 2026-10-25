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
// 2026-10-22 起与盘上禁用双写：失败同时写这份内存冷却与物理文件的 disabled 标记
// （见 failover_disable.go），恢复由 10 分钟禁用探活统一裁决。冷却因此不再是唯一的
// 排除手段，它只覆盖写盘到宿主 watcher 生效之间的窗口与写盘失败的情况。
//
// 最近修改时间：2026-10-22；改动原因：失败记账改为冷却 + 禁用双写，冷却探活移除。
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
//
// 双层冷却：账号级字段保持原语义（面板展示、全池让位），模型维度由
// modelStates 承担。一次失败按 classifyUpstreamFailure 的判定落到其中一层，
// 不再所有失败都写账号级——否则限流这类「只对某个模型成立」的失败会把整个
// 账号从所有模型上摘掉，反而缩小可用池。
type authFailoverState struct {
	count         int       // consecutive failures; reset to 0 on success
	cooldownUntil time.Time // zero means not cooling down
	// lastModel 记录最近一次触发冷却的上游模型（已归一化），供禁用探活回放。
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
	// 硬额度（402 / 额度标记）优先级最高：账号确实没额度了，必须记账号级，
	// 不能被正文里可能同时出现的模型级文案覆盖掉。
	if isHardCreditError(status, body) {
		return true
	}
	// 模型级/后端池级故障与账号无关：换号结果相同，判断为「需换号」只会把
	// 同一个错误在池里逐个重放（单请求轮满整池）。必须先于其余状态码判据短路。
	if isModelLevelFailure(body) {
		return false
	}
	if status == 0 || status >= 500 {
		return true
	}
	if isSoftRateLimit(status, body) {
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
// rotating to the next candidate (filtered by cooldown + anomaly) is the
// cheapest way to recover inside a single request. The cross-request
// cooldown still applies in parallel via isAccountFailure /
// recordAccountFailure — i.e. a 429-triggered same-request rotation also
// lifts the failed account's laddered cooldown so subsequent requests route
// away from it. When the upstream limit is genuinely global (shared
// IP/region quota across every traework account), same-request rotation
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
// 模型维度必须由调用方传入已归一化的上游模型名（resolveModelOptions 的
// config_name 或 circuitModel 的结果）；传空串时退化为纯账号级记账，
// 与历史行为一致，供尚未改造的调用点继续工作。
//
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// [返回] true 表示失败已计入冷却阶梯，false 表示按判定不予记账。
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增账号×模型双层冷却。
func recordAccountFailureForModel(authID, model string, status int, body string) bool {
	if !failoverActive() {
		return false
	}
	return recordFailureScoped(authID, model, classifyUpstreamFailure(status, body))
}

// recordFailureScoped 按给定维度记账，是失败处理的唯一落点：先写内存阶梯冷却，
// 再把账号写盘禁用（双写）。
//
// 为什么必须双写：冷却只存在插件内存里，宿主选择器看不见它，同一个请求内宿主仍会
// 把这个已知的坏号发过来、拿一次失败再换下一个号。禁用走的是宿主自己的路由判据，
// 同请求轮转与跨请求路由一起被挡住；冷却负责覆盖写盘到 watcher 生效之间的毫秒级
// 窗口，并在写盘失败（宿主 RPC 不通）时作为唯一的排除手段。
//
// 禁用写在锁外：disableAccountForFailover 会发宿主 RPC（解析 auth_index、读盘、
// 写盘），压在 failoverMu 上会把所有并发记账串行化。
//
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// scope：记账维度。
// [返回] true 表示已写入；scope 为 none 时返回 false。
// 最近修改时间：2026-10-25；改动原因：sidecar 写入下沉到 disableAccountForFailover，
// 避免在「解析不到 auth_index」或「账号已被他人禁用」时留下无人认领的禁用条目。
func recordFailureScoped(authID, model string, scope failureScope) bool {
	if !recordCooldownScoped(authID, model, scope) {
		return false
	}
	// 禁用写盘内部同时落 sidecar（路由判据的权威来源）。写在这里会绕过该函数的
	// 认领规则：宿主查不到账号或盘上已是别人的禁用时，sidecar 会记下一个永远
	// 没有本地标记、也没有探活候选的禁用状态——任何成功请求都解不掉它。
	disableAccountForFailover(authID, model)
	return true
}

// recordCooldownScoped 按给定维度写入内存阶梯冷却，是冷却侧的唯一写入落点。
//
// 从 recordFailureScoped 拆出，只为让 RPC 留在锁外；冷却语义与拆分前完全一致。
//
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// scope：记账维度。
// [返回] true 表示已写入冷却；scope 为 none 时返回 false。
// 最近修改时间：2026-10-22；改动原因：拆出锁内部分，供双写在锁外追加禁用。
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
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增，消除重复的键别名构造。
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
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增，供模型维度惰性初始化。
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

// recordForcedFailure records a failure for an account regardless of
// isAccountFailure classification. Used by pseudo-completion detection: the
// upstream returns a protocol-valid HTTP 200 + done with near-empty output
// (silent per-account throttling) that does not match any normal failure
// marker, yet must still drive failover so routing moves off the flagged
// account.
func recordForcedFailure(authID string, body string) bool {
	return recordForcedFailureForModel(authID, "", body)
}

// recordForcedFailureForModel 在强制记账的基础上补记失败模型。
//
// 伪完成（HTTP 200 + done + 近空输出）本质是上游对账号的窗口性限流，仍归账号级；
// 但记录模型名能让禁用探活用正确的模型回放，否则这类禁用只能全量轮测、白烧额度。
//
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；body：失败原因文本。
// [返回] true 表示已计入冷却。
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增，让伪完成冷却也能被探活覆盖。
func recordForcedFailureForModel(authID, model, body string) bool {
	if !failoverActive() {
		return false
	}
	return recordFailureScoped(authID, model, failureScopeAccount)
}

// isAccountCoolingDown reports whether the account is currently inside its
// cooldown window and should be skipped by routing.
// It supports both exact key match and normalized-key alias resolution.
func isAccountCoolingDown(authID string) bool {
	if !failoverActive() {
		return false
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	// 双写判据第一层：本进程已把该号写盘禁用。禁用探活把它恢复前，
	// 无论阶梯是否已到期，都不应再承载流量——冷却到期不代表故障消除。
	if isFailoverDisabledLocal(authID) {
		return true
	}
	// 第二层：插件自有 sidecar。它是跨重启与宿主回写的唯一真相源——宿主 persist
	// 会抹掉盘上标记、fsnotify 也可能静默失效，只有这里不受影响。
	if failoverStateIsDisabled(authID) {
		return true
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
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增账号×模型双层判定。
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

// resetCooldownState 清掉账号在内存里的全部冷却痕迹（计数、窗口、失败模型、
// 模型维度）。成功请求与探活成功都用它，避免两处各写一遍清零字段清单。
//
// [参数] authID：账号唯一标识。
// 最近修改时间：2026-10-22；改动原因：抽出，供成功路径与禁用恢复路径复用。
func resetCooldownState(authID string) {
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
	// sidecar 同步清冷却与计数，但保留 Disabled：成功一次不等于探活已确认
	// 这个号的故障解除。Disabled 只能由探活恢复或外部启用对账清除。
	failoverStateReset(authID)
}

// resetAccountFailover clears the failure counter and cooldown after a
// successful request. Call it on every upstream success.
//
// 双写之下它还要负责解除盘上禁用：冷却与禁用是同一个故障的两份记录，只清内存会
// 留下「宿主仍在跳过、插件已认为可用」的割裂状态。
// 最近修改时间：2026-10-22；改动原因：成功路径同时解除本机制写入的禁用。
func resetAccountFailover(authID string) {
	if !failoverActive() {
		return
	}
	resetCooldownState(authID)
	// 只有本进程确实写过禁用，才值得为它发两次宿主 RPC（解析 index + 读盘 +
	// 写盘）。本函数在每个成功请求上调用，无条件解析会把 RPC 塞进热路径；
	// 而盘上被禁用的号宿主本就不会路由过来，走不到这里。
	//
	// 锁顺序：此处已释放 failoverMu 才能取 failoverProbeModelMu，两处判据不得嵌套。
	if !isFailoverDisabledLocal(authID) {
		return
	}
	// 本地排除先无条件清掉，且早于任何 RPC：写盘通道不可用时下面的 index 解析
	// 会失败，若只依赖 reenable 的清理，这个号在本进程里就再也无法被判为可用，
	// 而盘上根本没禁它（宿主仍会路由过来），等于自造静默遗忘。
	clearFailoverDisabledLocal(authID, "")
	forgetFailoverProbeModel(authID)
	// sidecar 同步解除。它能走到这里说明本进程确认写过禁用，而成功请求已经
	// 证明这个号现在可用；不一起清会让 scheduler.pick 继续排除它。
	failoverStateClearDisabled(authID)
	authIndex, _ := failoverResolveIndexFn(strings.TrimSpace(authID))
	if strings.TrimSpace(authIndex) == "" {
		return
	}
	reenableAccountForFailover(authIndex, authID)
}

// resetAuthModelFailover 只清除指定账号×模型的冷却，保留账号级状态。
// 供模型维度清理使用，避免把其它模型的冷却一起抹掉。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名。
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增，供探活精确清除模型维度。
func resetAuthModelFailover(authID, model string) {
	if !failoverActive() {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	failoverMu.Lock()
	defer failoverMu.Unlock()
	for _, k := range failoverKeysLocked(strings.TrimSpace(authID)) {
		if st := failoverStates[k]; st != nil {
			delete(st.modelStates, model)
		}
	}
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
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增账号×模型双层诊断入口。
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

// lastFailoverModel 返回账号最近一次触发冷却的模型名，供诊断与禁用探活回放。
// [参数] authID：账号唯一标识。
// [返回] 归一化后的上游模型名；无记录时为空串。
// 最近修改时间：2026-10-22；改动原因：注释对齐禁用探活口径（冷却探活已移除）。
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
//
// 同时清空 sidecar（内存镜像 + 磁盘文件）与本地禁用表：这两者都是判据来源，
// 只清内存冷却会让上一个用例留下的禁用条目继续影响下一个用例（进程级共享
// 状态），表现为「单跑绿、整套跑红」的随机 FAIL。
func clearFailoverStates() {
	failoverMu.Lock()
	failoverStates = make(map[string]*authFailoverState)
	failoverMu.Unlock()
	clearFailoverStateForTest()
	clearFailoverDisabledLocalAll()
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
