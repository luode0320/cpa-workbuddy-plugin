// lifecycle.go maintains auth-document notes and legacy-flag migration for
// WorkBuddy AI.
//
// 产品口径（2026-10-24 用户裁定）：额度耗尽不代表账号不可用——零积分账号
// 依然可以正常推理。因此本文件不再有任何「按额度禁用/删除账号」的自动动
// 作，只负责：
//   - 刷新 auth 文档上的展示备注（note）；
//   - 把历史上被 `exhausted_disable` 标记自动禁用的账号重新启用（一次性
//     迁移，残留标记随白名单重建被清除）；
//   - 用户手动禁用（manual_disable / 宿主 UI 写入的无标记 disabled）一律
//     不碰，保持用户意图。
//   - 未知额度 → 同样不做任何动作。
package main

import (
	"fmt"
	"path/filepath"
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

// shouldReenableLegacyExhausted 判定盘上的禁用状态是否由旧版「额度耗尽自动
// 禁用」写入、因而应当被解除。
//
// 只认本插件旧版写的 exhausted_disable 标记：用户手动禁用（manual_disable）
// 优先，无标记的 disabled 视为宿主 UI 意图或其他写入方，一律不动。
// [参数] raw：物理 auth 文档原文。
// [返回] true 表示应由本插件解禁。
// 最近修改时间：2026-10-24；改动原因：额度耗尽不再触发自动禁用。
func shouldReenableLegacyExhausted(raw []byte) bool {
	if manualDisableFromAuthJSON(raw) {
		return false
	}
	return exhaustedDisableFromAuthJSON(raw)
}

// reenableLegacyExhaustedAuth 解除历史「额度耗尽自动禁用」遗留的禁用状态。
//
// 额度耗尽不再构成禁用理由（零积分账号可正常推理），因此历史上被
// exhausted_disable 标记自动禁用的账号需要一次性恢复：写入 disabled:false，
// 标记随白名单重建被清除，此后不再产生。
// 用户手动禁用（manual_disable，或宿主 UI 写入的无标记 disabled）一律不碰。
//
// [参数] authIndex：宿主索引；authID：账号缓存键；sa：账号凭证。
// [返回] 写盘失败时返回错误。
// 最近修改时间：2026-10-24；改动原因：额度耗尽不再触发自动禁用。
func reenableLegacyExhaustedAuth(authIndex, authID string, sa *storedAuth) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil || phys == nil {
		return fmt.Errorf("load physical auth: %w", err)
	}
	if !shouldReenableLegacyExhausted(phys.JSON) {
		return nil
	}
	note := displayNote(sa, nil, false)
	name, path, legacyPath := resolveAuthFileTarget(sa, phys)
	// 本机制标记必须原样搬运，否则白名单重建会把 failover_disable 抹掉，
	// 禁用探活从此认不出这个号。
	extra := putFailoverMarkersIntoExtra(map[string]any{}, phys.JSON)
	raw, err := buildAuthFileJSON(sa, false, note, extra)
	if err != nil {
		return err
	}
	// Direct physical write — host.auth.save would force the record Active
	// (fine here) but would ALSO reset the scheduler state via the persist
	// hook; the watcher path keeps every top-level field intact.
	if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
		return err
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

// peerAuthDir returns the directory of any workbuddy auth file known to the host.
// Uses HostAuthFileEntry.Path from the list response (A-38: was N+1 — list + getPhysical per file).
func peerAuthDir() string {
	files, err := hostAuthList()
	if err != nil {
		return ""
	}
	for _, f := range files {
		p := strings.TrimSpace(f.Path)
		if p != "" {
			return filepath.Dir(p)
		}
	}
	return ""
}

// syncAuthNote writes note without changing disabled state.
func syncAuthNote(authIndex, authID string, sa *storedAuth, cr *creditsSummary, disabled bool) error {
	if sa == nil {
		return nil
	}
	note := displayNote(sa, cr, disabled)
	if lifecycleStateUnchanged(authID, disabled, note) {
		return nil
	}
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
		// clear it)。exhausted_disable 同样必须搬运：本函数可能先于
		// reconcileOneAccount 的解禁路径执行，一旦抹标记却保留 disabled，
		// 该账号就再也认不出是历史耗尽禁用，永久卡死。
		if manualDisableFromAuthJSON(phys.JSON) {
			extra["manual_disable"] = true
		}
		if exhaustedDisableFromAuthJSON(phys.JSON) {
			extra["exhausted_disable"] = true
		}
		extra = putFailoverMarkersIntoExtra(extra, phys.JSON)
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

// reconcileOneAccount 刷新积分快照并维护 auth 文档备注。
//
// 额度耗尽不再触发任何禁用/删除动作（零积分账号可正常推理），因此这里只做
// 三件事：刷新缓存备注；把历史上被 exhausted_disable 自动禁用的账号一次性
// 解禁；其余禁用状态（manual_disable / 无标记 disabled）保持不动。
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
		// auto-re-enable an account the user explicitly disabled.
		if phys != nil && manualDisableFromAuthJSON(phys.JSON) {
			_ = syncAuthNote(authIndex, authID, sa, cr, true)
			return lifecycleNone, nil
		}
		// 历史遗留解禁：仅认领本插件旧版写的 exhausted_disable 标记。
		// 无标记的 disabled 视为宿主 UI 手动意图或未知写入方，保持原样。
		if phys == nil || !exhaustedDisableFromAuthJSON(phys.JSON) {
			_ = syncAuthNote(authIndex, authID, sa, cr, true)
			return lifecycleNone, nil
		}
		if err := reenableLegacyExhaustedAuth(authIndex, authID, sa); err != nil {
			return lifecycleReenable, err
		}
		return lifecycleReenable, nil
	}

	// 启用中的账号只刷新备注，绝不因额度为 0 而禁用。
	_ = syncAuthNote(authIndex, authID, sa, cr, false)
	return lifecycleNone, nil
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
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增账号×模型双层冷却。
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

// reconcileAfterExecutorError records an executor failure for the account.
// AuthID from the executor may be the credential ID (UID) rather than runtime auth_index;
// we resolve via host.auth.list when direct get fails.
// 额度类硬错误不再触发禁用生命周期（零积分账号可正常推理），只走失败冷却记账。
func reconcileAfterExecutorError(authID string, status int, body string) {
	reconcileAfterExecutorErrorForModel(authID, "", status, body)
}

// reconcileAfterExecutorErrorForModel 带模型维度的执行器错误收尾。
// 只做失败记账与绑定驱逐；额度耗尽不再影响账号可用性，因此不再触发任何
// 禁用/删除动作。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// 最近修改时间：2026-10-24；改动原因：移除额度耗尽触发的自动禁用。
func reconcileAfterExecutorErrorForModel(authID, model string, status int, body string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	// Failover first — independent of lifecycle_auto: any upstream failure
	// (429/402/5xx/transport) counts toward cooldown and evicts bindings.
	noteAccountFailureForModel(authID, model, status, body)
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

// noteSSEErrorFailure 记录 HTTP 200 承载的业务错误帧，按模型维度记账并驱逐绑定。
// 与 noteAccountFailureForModel 的分工：SSE 帧没有可用的状态码，不能走
// classifyUpstreamFailure 的状态码判据，必须由调用方明确指出「这是一次值得
// 记账的业务失败」。
// [参数] authID：账号唯一标识；model：已归一化的上游模型名；body：错误帧文本。
// [返回] true 表示已计入冷却。
// 最近修改时间：2026-10-18 23:40:00；改动原因：新增，避免 SSE 帧被状态码判据漏记。
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

// reconcileByUID records an executor failure by account UID.
func reconcileByUID(uid string, status int, body string) {
	reconcileByUIDForModel(uid, "", status, body)
}

// reconcileByUIDForModel 带模型维度的 UID 侧收尾，供流式路径按实际失败模型记账。
// 只做失败记账；额度耗尽不再影响账号可用性。
// [参数] uid：账号 UID；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// 最近修改时间：2026-10-24；改动原因：移除额度耗尽触发的自动禁用。
func reconcileByUIDForModel(uid, model string, status int, body string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	// Failover first — independent of lifecycle_auto. reconcileByUID receives
	// the account UID; noteAccountFailure resolves it to the auth.ID key the
	// scheduler uses and evicts bindings when cooldown triggers.
	noteAccountFailureForModel(uid, model, status, body)
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

var (
	checkinLocks   sync.Map // authIndex -> *sync.Mutex
	checkinLocksMu sync.Mutex
)

func checkinLockFor(authIndex string) *sync.Mutex {
	v, ok := checkinLocks.Load(authIndex)
	if ok {
		return v.(*sync.Mutex)
	}
	checkinLocksMu.Lock()
	defer checkinLocksMu.Unlock()
	v, ok = checkinLocks.Load(authIndex)
	if ok {
		return v.(*sync.Mutex)
	}
	m := &sync.Mutex{}
	checkinLocks.Store(authIndex, m)
	return m
}

func pruneCheckinLocks() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[f.AuthIndex] = struct{}{}
	}
	checkinLocks.Range(func(key, value any) bool {
		idx, _ := key.(string)
		if _, ok := live[idx]; !ok {
			checkinLocks.Delete(key)
		}
		return true
	})
}
