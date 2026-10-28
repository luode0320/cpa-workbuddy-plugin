// lifecycle.go implements credit-based auth lifecycle for workbuddy:
//   - Exhausted → AUTO-DISABLE: write disabled:true + top-level marker
//     exhausted_disable:true (policy update 2026-09-08, user mandate: 耗尽
//     停用保留，但必须能自动恢复). The 4h check-in reconcile then re-enables
//     the account automatically once refreshed credits are > 0.
//   - 国际版（Global）账号不再自动删号：Global 由 workbuddy-ai 插件专门承载，
//     本插件对 CN / Global 一视同仁，只写盘禁用、保留凭据。
//   - Manual disable (manual_disable marker, or a disabled doc without the
//     exhausted_disable marker written by the host UI) is NEVER auto-touched:
//     no automatic path re-enables or re-disables it.
//   - Unknown credits → no-op (never mis-kill)
//   - Hard credit errors from executor → recheck credits then apply policy
//   - Soft rate limits → do not touch the account
package main

import (
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	lifecycleState   sync.Map // auth_id (auth.ID) -> lifecycleStateEntry
	lifecycleSaveTTL = 30 * time.Second
)

type lifecycleStateEntry struct {
	disabled bool
	note     string
	at       time.Time
}

func lifecycleStateUnchanged(authID string, disabled bool, note string) bool {
	v, ok := lifecycleState.Load(authID)
	if !ok {
		return false
	}
	e := v.(*lifecycleStateEntry)
	if e.disabled != disabled || e.note != note {
		return false
	}
	return time.Since(e.at) < lifecycleSaveTTL
}

func rememberLifecycleState(authID string, disabled bool, note string) {
	lifecycleState.Store(authID, &lifecycleStateEntry{disabled: disabled, note: note, at: time.Now()})
}

// pruneLifecycleState removes entries for auth indices that no longer exist
// or whose TTL has expired. Called from dashboard prune to prevent unbounded growth.
func pruneLifecycleState() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[f.ID] = struct{}{}
	}
	lifecycleState.Range(func(key, value any) bool {
		idx, _ := key.(string)
		if _, ok := live[idx]; !ok {
			lifecycleState.Delete(key)
			return true
		}
		if e, ok := value.(*lifecycleStateEntry); ok && time.Since(e.at) > 10*time.Minute {
			lifecycleState.Delete(key)
		}
		return true
	})
}

// disableAuth 对单个账号执行耗尽停用决策。
//
// 双路径语义：
//   - 手动路径（extra 含 manual_disable:true）：写 disabled:true 并携带手动意图标记，
//     reconcile 永不自动解禁用户手动关掉的号。
//   - 自动耗尽路径（extra=nil，唯一剩余调用方是 reconcileOneAccount 的 lifecycleDisable）：
//     账号处于启用态时写 disabled:true + exhausted_disable:true；4 小时签到 reconcile
//     在积分恢复后清除这对标记。已禁用的号原样保留既有标记，不额外加标记。
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：凭据；cr：当前积分；
// reason：附加到 note 的原因片段；extra：需要并入 auth 文件的额外顶层键。
// [返回] 写盘错误。
// 最近修改时间：2026-10-23；改动原因：白名单重建时同步搬运低积分禁用标记。
func disableAuth(authIndex, authID string, sa *storedAuth, cr *creditsSummary, reason string, extra map[string]any) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	note := displayNote(sa, cr, true)
	if reason != "" && !strings.Contains(note, reason) {
		// keep note short; reason only if room
		if len(note)+len(reason) < 75 {
			note = note + " · " + reason
		}
	}
	// Prefer live physical file to preserve any extra fields if present.
	phys, err := hostAuthGetPhysical(authIndex)
	// Merge caller extras with any marker already on disk.
	merged := map[string]any{}
	for k, v := range extra {
		merged[k] = v
	}
	if err == nil && manualDisableFromAuthJSON(phys.JSON) {
		merged["manual_disable"] = true
	}
	// 本机制写的失败禁用标记必须原样搬运：buildAuthFileJSON 是白名单重建，
	// 不搬运就会把 failover_disable 抹掉，探活循环从此找不到该账号（静默遗忘）。
	if err == nil && phys != nil {
		merged = putFailoverMarkersIntoExtra(merged, phys.JSON)
		merged = putLowCreditMarkersIntoExtra(merged, phys.JSON)
	}
	// Determine whether this is a manual toggle or an auto-lifecycle call.
	// Manual toggle (extra contains manual_disable:true) writes disabled:true
	// with the intent marker. The auto exhaustion path (policy update
	// 2026-09-08) writes disabled:true + exhausted_disable:true when the
	// account is enabled; already-disabled files keep their existing flag and
	// markers untouched (manual_disable carries forward via merged above).
	_, isManualToggle := extra["manual_disable"]
	if !isManualToggle {
		// Auto-lifecycle: exhaustion disable with auto-recovery marker.
		finalDisabled := false
		if err == nil && phys != nil {
			finalDisabled = parseDisabledFromAuthJSON(phys.JSON)
			switch {
			case manualDisableFromAuthJSON(phys.JSON):
				// Manually disabled: user intent wins — the manual_disable
				// marker is already carried in merged; never add
				// exhausted_disable (must not become auto-recoverable).
			case exhaustedDisableFromAuthJSON(phys.JSON):
				// Already auto-disabled by this lifecycle: marker carries
				// forward so recovery stays armed.
				merged["exhausted_disable"] = true
			case finalDisabled:
				// Disabled by an unknown/manual writer without markers
				// (e.g. host-UI toggle): leave the flag exactly as-is.
			default:
				// Enabled → exhausted auto-disable (可自动恢复).
				finalDisabled = true
				merged["exhausted_disable"] = true
			}
		}
		note = displayNote(sa, cr, finalDisabled)
		if reason != "" && !strings.Contains(note, reason) {
			// keep note short; reason only if room
			if len(note)+len(reason) < 75 {
				note = note + " · " + reason
			}
		}
		// Skip the write when state+note are unchanged.
		if lifecycleStateUnchanged(authID, finalDisabled, note) {
			return nil
		}
		name := authFileNameFor(sa)
		path := ""
		legacyPath := ""
		if phys != nil {
			name, path, legacyPath = resolveAuthFileTarget(sa, phys)
		}
		raw, err := buildAuthFileJSON(sa, finalDisabled, note, merged)
		if err != nil {
			return err
		}
		// 自动耗尽路径同样优先经宿主管理 API 写进路由真相源；未配置密钥或调用
		// 失败时回落直写物理文件，保证排除不会完全失效。
		apiErr := setHostAuthDisabled(name, authIndex, finalDisabled)
		if apiErr != nil {
			if !errors.Is(apiErr, errCPAManagementNotConfigured) {
				log.Printf("[workbuddy] cpa disable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
			}
			if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
				return err
			}
		}
		rememberLifecycleState(authID, finalDisabled, note)
		accountCache.Delete(authID)
		return nil
	}
	// Manual toggle path: write disabled:true.
	// Skip the write only when state+note are unchanged AND nothing new to
	// merge — a manual toggle must persist even inside the lifecycle TTL.
	if lifecycleStateUnchanged(authID, true, note) {
		// already disabled; still refresh note if needed
		if lifecycleStateUnchanged(authID, true, note) {
			return nil
		}
	}
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	if phys != nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
	}
	raw, err := buildAuthFileJSON(sa, true, note, merged)
	if err != nil {
		return err
	}
	// Direct physical write (NOT host.auth.save): the save channel rebuilds
	// the record as Active and would silently re-enable the account. The file
	// watcher applies top-level disabled to the scheduler instead.
	if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
		return err
	}
	rememberLifecycleState(authID, true, note)
	accountCache.Delete(authID)
	return nil
}

// reenableAuth writes disabled:false when CN has credits again.
func reenableAuth(authIndex, authID string, sa *storedAuth, cr *creditsSummary) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	if !shouldReenable(true, cr) {
		return nil
	}
	note := displayNote(sa, cr, false)
	if lifecycleStateUnchanged(authID, false, note) {
		return nil
	}
	phys, err := hostAuthGetPhysical(authIndex)
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	if err == nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
	}
	raw, err := buildAuthFileJSON(sa, false, note, nil)
	if err != nil {
		return err
	}
	// 恢复优先走宿主管理 API：宿主真相源里的 disabled 不置回 false，
	// 宿主会继续挡住这个号。未配密钥或调用失败时回落直写物理文件。
	apiErr := setHostAuthDisabled(name, authIndex, false)
	if apiErr != nil {
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[workbuddy] cpa enable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
		}
		// Direct physical write — host.auth.save would force the record Active
		// (fine here) but would ALSO reset the scheduler state via the persist
		// hook; the watcher path keeps every top-level field intact.
		if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
			return err
		}
	}
	rememberLifecycleState(authID, false, note)
	accountCache.Delete(authID)
	return nil
}

// clearDeletedAccountState removes every in-memory trace of a deleted account
// for each provided key (auth.ID, auth_index, and account UID may each have
// been used as a key by different code paths). Covers lifecycle state, cached
// credits/plan/checkin, active selection, failover cooldown/counter, and
// session bindings pinned to the account. Idempotent — safe to call when maps
// are empty or keys already absent.
func clearDeletedAccountState(keys ...string) {
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		lifecycleState.Delete(k)
		accountCache.Delete(k)
		clearActiveAuthIfMatch(k)
		clearFailoverStateForAuth(k)
		evictSessionBindingsForAuth(k)
	}
}

// syncAuthNote 只更新账号 note，不改变 disabled 状态。
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：凭据；
// cr：当前积分；disabled：写入 note 时依据的禁用态。
// [返回] 写盘错误。
// 最近修改时间：2026-10-23；改动原因：白名单重建时同步搬运低积分禁用标记。
func syncAuthNote(authIndex, authID string, sa *storedAuth, cr *creditsSummary, disabled bool) error {
	// 1. 凭据缺失或状态无变化时直接返回，避免无意义写盘。
	if sa == nil {
		return nil
	}
	note := displayNote(sa, cr, disabled)
	if lifecycleStateUnchanged(authID, disabled, note) {
		return nil
	}
	// 2. 加账号锁后按磁盘真实状态重算。
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()
	phys, err := hostAuthGetPhysical(authIndex)
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	extra := map[string]any{}
	if err == nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
		// re-read disabled from disk as source of truth
		disabled = parseDisabledFromAuthJSON(phys.JSON)
		note = displayNote(sa, cr, disabled)
		// carry the manual-disable marker forward (note refresh must not
		// clear it), same for the exhausted-disable marker (note refresh
		// must not disarm auto-recovery)
		if manualDisableFromAuthJSON(phys.JSON) {
			extra["manual_disable"] = true
		}
		if exhaustedDisableFromAuthJSON(phys.JSON) {
			extra["exhausted_disable"] = true
		}
		extra = putFailoverMarkersIntoExtra(extra, phys.JSON)
		// 低积分禁用标记同样必须搬运：它是分级恢复时「这号是不是我禁的」唯一判据。
		extra = putLowCreditMarkersIntoExtra(extra, phys.JSON)
	}
	if lifecycleStateUnchanged(authID, disabled, note) {
		return nil
	}
	raw, err := buildAuthFileJSON(sa, disabled, note, extra)
	if err != nil {
		return err
	}
	// Direct physical write keeps the disabled state and the manual_disable
	// marker intact (host.auth.save would rebuild the record as Active).
	if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
		return err
	}
	rememberLifecycleState(authID, disabled, note)
	return nil
}

// reconcileOneAccount refreshes credits and applies lifecycle for one auth.
// authIndex is used for host RPC (host.auth.get), authID (auth.ID) is used
// for cache keys (accountCache/lifecycleState) so it matches the scheduler's
// SchedulerAuthCandidate.ID.
// force ignores short-circuit only for credit fetch (uses force on cache via caller).
func reconcileOneAccount(authIndex, authID string, force bool) (action lifecycleAction, err error) {
	if !lifecycleEnabled() {
		return lifecycleNone, nil
	}
	// Single host.auth.get (A-19): previous hostAuthGet + hostAuthGetPhysical
	// doubled RPC on every reconcile tick (21 accounts × 2).
	sa, phys, err := hostAuthGetBundle(authIndex)
	if err != nil {
		return lifecycleNone, err
	}
	disabled := false
	if phys != nil {
		disabled = phys.Disabled
	}

	// Credits: use force path via fetchUserResource always when force,
	// else try cache first.
	var cr *creditsSummary
	if !force {
		if v, ok := accountCache.Load(authID); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 && e.credits != nil && time.Since(e.fetched) < accountCacheTTL {
				cr = e.credits
			}
		}
	}
	if cr == nil {
		// Route credits fetch through cachedAccountDetails so singleflight
		// serializes concurrent writers for the same authID (P0-2 fix: the
		// previous Load→Store sequence here had a check-then-act window
		// where a concurrent dashboard cachedAccountDetails write could
		// overwrite our merge with newer plan/checkin values).
		_, _, cr2, _ := cachedAccountDetails(authID, sa, true)
		cr = cr2
		if cr == nil {
			return lifecycleNone, nil
		}
	}

	if disabled {
		// Manual disable (manual_disable marker) must stick: never
		// auto-re-enable an account the user explicitly disabled, even when
		// credits recover.
		if phys != nil && manualDisableFromAuthJSON(phys.JSON) {
			_ = syncAuthNote(authIndex, authID, sa, cr, true)
			return lifecycleNone, nil
		}
		// Marker-gated recovery (policy update 2026-09-08): only docs this
		// lifecycle auto-disabled (exhausted_disable:true) re-enable
		// automatically once credits recover. A marker-less disabled doc is
		// host-UI manual intent or an unknown writer — sticky by design.
		if phys == nil || !exhaustedDisableFromAuthJSON(phys.JSON) {
			_ = syncAuthNote(authIndex, authID, sa, cr, true)
			return lifecycleNone, nil
		}
		if shouldReenable(true, cr) {
			if err := reenableAuth(authIndex, authID, sa, cr); err != nil {
				return lifecycleReenable, err
			}
			return lifecycleReenable, nil
		}
		// still disabled: refresh note
		_ = syncAuthNote(authIndex, authID, sa, cr, true)
		return lifecycleNone, nil
	}

	act := lifecycleActionFor(cr)
	switch act {
	case lifecycleDisable:
		return lifecycleDisable, disableAuth(authIndex, authID, sa, cr, "耗尽", nil)
	default:
		// healthy: keep note fresh (throttled)
		_ = syncAuthNote(authIndex, authID, sa, cr, false)
		return lifecycleNone, nil
	}
}

// reconcileAllAccounts walks workbuddy auths and applies lifecycle.
func reconcileAllAccounts(force bool) []map[string]any {
	if !lifecycleEnabled() {
		return nil
	}
	files, err := hostAuthList()
	if err != nil {
		return []map[string]any{{"error": err.Error()}}
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		act, err := reconcileOneAccount(f.AuthIndex, f.ID, force)
		row := map[string]any{"auth_index": f.AuthIndex, "action": act.String()}
		if err != nil {
			row["error"] = err.Error()
		}
		if act != lifecycleNone || err != nil {
			out = append(out, row)
		}
	}
	return out
}

// noteAccountFailure records an upstream failure against the account and,
// when it enters failover cooldown, evicts every session binding pinned to it
// so new requests route elsewhere. The executor AuthID is normally the same
// auth.ID the scheduler keys on, but legacy hosts may pass the account UID
// instead; a background resolve backfills the canonical key so pick still
// skips the account. Returns true when the failure was counted.
func noteAccountFailure(authID string, status int, body string) bool {
	return noteAccountFailureForModel(authID, "", status, body)
}

// noteAccountFailureForModel 是带模型维度的记账入口。
// 模型级失败只冷却该模型并驱逐该账号的会话绑定；账号级失败行为与历史一致。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// [返回] true 表示失败已计入冷却阶梯。
// 最近修改时间：2026-10-19 00:40:00；改动原因：新增账号×模型双层冷却。
func noteAccountFailureForModel(authID, model string, status int, body string) bool {
	if !failoverActive() || strings.TrimSpace(authID) == "" {
		return false
	}
	if !recordAccountFailureForModel(authID, model, status, body) {
		return false
	}
	// 会话绑定一律驱逐：无论记到哪一层，绑定到该账号的会话都应重新选号，
	// 否则亲和会把同一个会话反复送回刚失败的位置。
	evictSessionBindingsForAuth(authID)
	go func() {
		_, id := resolveAuthIndexAndID(authID)
		if id == "" || id == authID {
			return
		}
		if recordAccountFailureForModel(id, model, status, body) {
			evictSessionBindingsForAuth(id)
		}
	}()
	return true
}

// noteSSEErrorFailure 记录 HTTP 200 承载的业务错误帧，按模型维度记账并驱逐绑定。
// 与 noteAccountFailureForModel 的分工：SSE 帧没有可用的状态码，不能走
// classifyUpstreamFailure 的状态码判据，必须由调用方明确指出「这是一次值得
// 记账的业务失败」。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名；body：错误帧文本。
// [返回] true 表示已计入冷却。
// 最近修改时间：2026-10-19 00:40:00；改动原因：新增，避免 SSE 帧被状态码判据漏记。
func noteSSEErrorFailure(authID, model, body string) bool {
	if !failoverActive() || strings.TrimSpace(authID) == "" {
		return false
	}
	if !recordSSEErrorFailureForModel(authID, model, body) {
		return false
	}
	evictSessionBindingsForAuth(authID)
	return true
}

// reconcileAfterExecutorError triggers lifecycle when upstream reports hard credit failure.
// AuthID from the executor may be the credential ID (UID) rather than runtime auth_index;
// we resolve via host.auth.list when direct get fails.
func reconcileAfterExecutorError(authID string, status int, body string) {
	reconcileAfterExecutorErrorForModel(authID, "", status, body)
}

// reconcileAfterExecutorErrorForModel 带模型维度的执行器错误收尾。
// 除按维度记账外，其余（硬额度生命周期）行为与历史一致。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// 最近修改时间：2026-10-19 00:40:00；改动原因：新增账号×模型双层冷却。
func reconcileAfterExecutorErrorForModel(authID, model string, status int, body string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	// Failover first — independent of lifecycle_auto: any upstream failure
	// (429/402/5xx/transport) counts toward cooldown and evicts bindings.
	noteAccountFailureForModel(authID, model, status, body)
	// Hard credit errors additionally trigger the disable/delete lifecycle.
	if !lifecycleEnabled() || !isHardCreditError(status, body) {
		return
	}
	go func() {
		idx, id := resolveAuthIndexAndID(authID)
		if idx == "" {
			return
		}
		_, _ = reconcileOneAccount(idx, id, true)
	}()
}

// resolveAuthIndexAndID maps executor AuthID (index, file id, or account UID)
// to host auth_index AND auth.ID. Returns ("", "") if not found.
func resolveAuthIndexAndID(authID string) (string, string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return "", ""
	}
	// Fast path: already an auth_index the host understands.
	if _, err := hostAuthGet(authID); err == nil {
		// Find the matching file entry to get auth.ID.
		if files, err := hostAuthList(); err == nil {
			for _, f := range files {
				if f.AuthIndex == authID {
					return authID, f.ID
				}
			}
		}
		return authID, ""
	}
	files, err := hostAuthList()
	if err != nil {
		return "", ""
	}
	// Prefer O(list) name/id match before per-account host.auth.get (A-22).
	// Multi-account files are workbuddy-<uid>.json; list Name/ID usually carry that.
	wantName := "workbuddy-" + authID + ".json"
	for _, f := range files {
		if f.AuthIndex == authID || f.ID == authID || f.Name == authID {
			return f.AuthIndex, f.ID
		}
		if listEntryMatchesUID(f, authID, wantName) {
			return f.AuthIndex, f.ID
		}
	}
	// Slow path: only when list metadata lacks uid (rare legacy shapes).
	for _, f := range files {
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			continue
		}
		if strings.TrimSpace(sa.Account.UID) == authID {
			return f.AuthIndex, f.ID
		}
	}
	return "", ""
}

// reconcileByUID finds workbuddy auth by account UID and applies executor-error lifecycle.
func reconcileByUID(uid string, status int, body string) {
	reconcileByUIDForModel(uid, "", status, body)
}

// reconcileByUIDForModel 带模型维度的 UID 侧收尾，供流式路径按实际失败模型记账。
// [参数] uid：账号 UID；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// 最近修改时间：2026-10-19 00:40:00；改动原因：新增账号×模型双层冷却。
func reconcileByUIDForModel(uid, model string, status int, body string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	// Failover first — independent of lifecycle_auto. reconcileByUID receives
	// the account UID; noteAccountFailure resolves it to the auth.ID key the
	// scheduler uses and evicts bindings when cooldown triggers.
	noteAccountFailureForModel(uid, model, status, body)
	if !lifecycleEnabled() || !isHardCreditError(status, body) {
		return
	}
	idx, id := resolveAuthIndexAndID(uid)
	if idx == "" {
		return
	}
	_, _ = reconcileOneAccount(idx, id, true)
}

// invalidateAccountCredits drops cached credits so the next panel/reconcile
// fetch hits upstream. Call after a successful chat completion — otherwise a
// short TTL cache makes "used" look frozen while the user is burning credits.
func invalidateAccountCredits(authID, authUID string) {
	// Invalidate credits only — keep plan/checkin in cache.
	invalidateCredits := func(id string) {
		if v, ok := accountCache.Load(id); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 {
				fresh := *e
				fresh.credits = nil
				fresh.fetched = time.Now()
				accountCache.Store(id, &fresh)
			}
		}
	}
	if authID != "" {
		invalidateCredits(authID)
	}
	if authUID == "" || authUID == authID {
		return
	}
	// Also drop any cache keyed by auth_index that maps to this UID.
	files, err := hostAuthList()
	if err != nil {
		return
	}
	wantName := "workbuddy-" + authUID + ".json"
	matchedByName := false
	for _, f := range files {
		if f.AuthIndex == authID || f.ID == authID || f.Name == authID {
			invalidateCredits(f.ID)
			continue
		}
		if listEntryMatchesUID(f, authUID, wantName) {
			invalidateCredits(f.ID)
			matchedByName = true
		}
	}
	if matchedByName {
		return
	}
	// Slow path: legacy names without uid in list metadata.
	for _, f := range files {
		if f.AuthIndex == authID {
			continue
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			continue
		}
		if strings.TrimSpace(sa.Account.UID) == authUID {
			invalidateCredits(f.ID)
		}
	}
}

// listEntryMatchesUID reports whether host list metadata already encodes the UID
// (workbuddy-<uid>.json naming). Pure helper for O(list) cache invalidation.
func listEntryMatchesUID(f pluginapi.HostAuthFileEntry, uid, wantName string) bool {
	if uid == "" {
		return false
	}
	if strings.EqualFold(f.Name, wantName) || strings.EqualFold(f.ID, wantName) {
		return true
	}
	base := strings.TrimSuffix(f.Name, ".json")
	return strings.EqualFold(base, "workbuddy-"+uid)
}

// enrichAuthMetadata builds Metadata map for AuthData (type/logo/note/disabled).
func enrichAuthMetadata(sa *storedAuth, cr *creditsSummary, disabled bool) map[string]any {
	note := displayNote(sa, cr, disabled)
	return map[string]any{
		"type":     providerName,
		"provider": providerName,
		"logo":     pluginLogoURL,
		"note":     note,
		"disabled": disabled,
	}
}
