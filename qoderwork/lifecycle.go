// lifecycle.go 实现 qoderwork 基于积分的账号生命周期：
//   - 0 积分不自动禁用：免费模型仍可用，账号继续参与路由
//   - 积分未知时不动作，避免误判
//   - 带 exhausted_disable 标记的历史自动禁用账号在校验时恢复启用
//   - 手动禁用（manual_disable 标记，或宿主 UI 写入的无标记禁用）永不自动改动；
//     SESSION-DEAD / TOKEN_EXPIRE 备注守卫同样保留
//   - 上游硬额度错误 → 复查积分后按策略处理
//   - 软限流 → 不删除 CN 账号
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// reenableAuth 把历史自动禁用的账号恢复为启用态。
// [参数] authIndex：宿主账号索引；authID：账号标识；sa：账号凭据；cr：积分快照。
// [返回] 落盘错误；成功返回 nil。
// 最近修改时间 2026-10-17 15:12:00（0 积分不再是禁用理由，恢复不再依赖积分快照）
func reenableAuth(authIndex, authID string, sa *storedAuth, cr *creditsSummary) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

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
	// 生命周期恢复启用只对带 exhausted_disable 的文档可达，与本机制的
	// failover_disable 天然互斥，因此这里不搬运本机制标记——号已重新可用，
	// 不该再被禁用探活认领。
	raw, err := buildAuthFileJSON(sa, false, note, nil)
	if err != nil {
		return err
	}
	// 恢复优先走宿主管理 API：宿主真相源里的 disabled 不置回 false，
	// 宿主会继续挡住这个号。未配密钥或调用失败时回落直写物理文件。
	apiErr := setHostAuthDisabled(name, authIndex, false)
	if apiErr != nil {
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[qoderwork] cpa enable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
		}
		if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
			return err
		}
	}
	rememberLifecycleState(authID, false, note)
	accountCache.Delete(authID)
	return nil
}

// deleteAuth removes exhausted credentials from disk.
func deleteAuth(authIndex, authID string, sa *storedAuth) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(phys.Path)
	if path == "" {
		// Try to reconstruct path from peer qoderwork files' directory + canonical name.
		name := authFileNameFor(sa)
		if phys.Name != "" && !isLegacyAuthName(phys.Name) {
			name = phys.Name
		} else if strings.TrimSpace(phys.Name) != "" && isLegacyAuthName(phys.Name) {
			name = authFileNameFor(sa)
		}
		if dir := peerAuthDir(); dir != "" && name != "" {
			candidate := filepath.Join(dir, name)
			if isSafeAuthPath(candidate) {
				path = candidate
			}
		}
	}
	if path == "" {
		// Last resort: preserve the existing disabled flag (never auto-disable)
		// plus any intent markers, so a manual toggle or an armed
		// exhausted-disable survives the fallback rewrite.
		existingDisabled := parseDisabledFromAuthJSON(phys.JSON)
		note := displayNote(sa, nil, existingDisabled) + " · 应删除但无 path"
		extra := map[string]any{}
		if manualDisableFromAuthJSON(phys.JSON) {
			extra["manual_disable"] = true
		}
		if exhaustedDisableFromAuthJSON(phys.JSON) {
			extra["exhausted_disable"] = true
		}
		// 本机制写的失败禁用标记必须原样搬运：buildAuthFileJSON 是白名单重建，
		// 不搬运就会把 failover_disable 抹掉，探活循环从此找不到该账号（静默遗忘）。
		extra = putFailoverMarkersIntoExtra(extra, phys.JSON)
		raw, berr := buildAuthFileJSON(sa, existingDisabled, note, extra)
		if berr != nil {
			return fmt.Errorf("no path and build failed: %w", berr)
		}
		name := authFileNameFor(sa)
		if phys.Name != "" && !isLegacyAuthName(phys.Name) {
			name = phys.Name
		}
		if err := hostAuthPersistMigrate(name, "", "", raw); err != nil {
			return err
		}
		rememberLifecycleState(authID, true, note)
		accountCache.Delete(authID)
		clearActiveAuthIfMatch(authID)
		return nil
	}
	if err := deleteAuthFileInDir(path, filepath.Dir(path)); err != nil {
		return err
	}
	// Also remove legacy qoderwork.json if this UID was dual-named historically.
	if sa != nil && strings.TrimSpace(sa.Account.UID) != "" {
		if dir := filepath.Dir(path); dir != "" {
			legacy := filepath.Join(dir, authFileName)
			// A-36: same path safety as primary deleteAuthFileInDir.
			if isLegacyAuthName(filepath.Base(legacy)) {
				_ = deleteAuthFileInDir(legacy, dir)
			}
		}
	}
	clearDeletedAccountState(authID, authIndex, sa.Account.UID)
	return nil
}

// clearDeletedAccountState removes every in-memory trace of a deleted account
// for each provided key (auth.ID, auth_index, and account UID may each have
// been used as a key by different code paths). Covers lifecycle state, cached
// credits/plan/checkin, active selection, and failover
// cooldown/counter. （同步自 workbuddy 0.14.7 账号删除功能；保号池与
// session 绑定清理随对应功能同步后在此补充）
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
		// 本机制的本地排除集合与当日探活配额同样要清：账号删除后宿主列表里
		// 不再有它，对账逻辑也找不到它，留着只会让内存表无限增长。
		clearFailoverDisabledLocal(k, "")
		forgetFailoverProbeModel(k)
		resetFailoverProbeQuota(k)
	}
}

// peerAuthDir returns the directory of any qoderwork auth file known to the host.
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

// syncAuthNote writes note without changing disabled state. Intent markers on
// disk (manual_disable / exhausted_disable) are carried forward — a note
// refresh must never clear the manual intent nor disarm auto-recovery.
// persistAuthDirect keeps every other top-level field intact (host.auth.save
// rebuilds and drops unknown fields).
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
		// carry intent markers forward (note refresh must not clear them)
		if manualDisableFromAuthJSON(phys.JSON) {
			extra["manual_disable"] = true
		}
		if exhaustedDisableFromAuthJSON(phys.JSON) {
			extra["exhausted_disable"] = true
		}
		// 同上：备注刷新绝不能顺手抹掉本机制的禁用标记。
		extra = putFailoverMarkersIntoExtra(extra, phys.JSON)
	}
	if lifecycleStateUnchanged(authID, disabled, note) {
		return nil
	}
	raw, err := buildAuthFileJSON(sa, disabled, note, extra)
	if err != nil {
		return err
	}
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
		// Don't re-enable accounts marked session-dead by keepalive.
		// The credits snapshot may still look healthy, but the session
		// was revoked server-side — re-enabling would cause 401 storms.
		if phys != nil {
			var pjson struct {
				Note string `json:"note"`
			}
			if json.Unmarshal(phys.JSON, &pjson) == nil && pjson.Note != "" &&
				(strings.Contains(pjson.Note, "SESSION-DEAD") || strings.Contains(pjson.Note, "TOKEN_EXPIRE")) {
				return lifecycleNone, nil
			}
		}
		// Manual disable (manual_disable marker) must stick: never
		// auto-re-enable an account the user explicitly disabled, even when
		// credits recover.
		if phys != nil && manualDisableFromAuthJSON(phys.JSON) {
			_ = syncAuthNote(authIndex, authID, sa, cr, true)
			return lifecycleNone, nil
		}
		// 仅恢复带 exhausted_disable 标记的账号；无标记的禁用属用户手动意图，保持不动
		if phys == nil || !exhaustedDisableFromAuthJSON(phys.JSON) {
			_ = syncAuthNote(authIndex, authID, sa, cr, true)
			return lifecycleNone, nil
		}
		if err := reenableAuth(authIndex, authID, sa, cr); err != nil {
			return lifecycleReenable, err
		}
		return lifecycleReenable, nil
	}

	// 账号可用：仅刷新备注，0 积分不再触发自动禁用
	_ = syncAuthNote(authIndex, authID, sa, cr, false)
	return lifecycleNone, nil
}

// reconcileAllAccounts walks qoderwork auths and applies lifecycle.
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

// noteAccountFailure records an upstream failure against the account so it
// enters failover cooldown and new requests route elsewhere. The executor
// AuthID is normally the same auth.ID the scheduler keys on, but legacy hosts
// may pass the account UID instead; a background resolve backfills the
// canonical key so pick still skips the account. Returns true when the
// failure was counted. Session bindings pinned to the failed account are
// evicted so conversations re-assign on the next pick (see session_auth.go).
func noteAccountFailure(authID string, status int, body string) bool {
	return noteAccountFailureForModel(authID, "", status, body)
}

// noteAccountFailureForModel 是带模型维度的记账入口。
//
// qoderwork 的冷却阶梯只有账号层，模型维度不参与冷却判定；这里记录模型名只有一个
// 用途——禁用探活只回放当初失败的那个模型，没有这个键账号就无法被验证恢复。
//
// [参数] authID：账号唯一标识；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// [返回] true 表示失败已计入冷却阶梯。
// 最近修改时间：2026-10-22；改动原因：新增，供禁用探活回放失败模型。
func noteAccountFailureForModel(authID, model string, status int, body string) bool {
	if !failoverActive() || strings.TrimSpace(authID) == "" {
		return false
	}
	// 模型名必须先落进内存表：recordAccountFailure 内部双写时会读取它，
	// 若在记账之后才写，本次禁用就会带着空模型落盘（探活退化为全量轮测）。
	rememberFailoverProbeModel(authID, model)
	if !recordAccountFailure(authID, status, body) {
		return false
	}
	evictSessionBindingsForAuth(authID)
	go func() {
		_, id := resolveAuthIndexAndID(authID)
		if id == "" || id == authID {
			return
		}
		rememberFailoverProbeModel(id, model)
		if recordAccountFailure(id, status, body) {
			evictSessionBindingsForAuth(id)
		}
	}()
	return true
}

// reconcileAfterExecutorError triggers lifecycle when upstream reports hard credit failure.
// AuthID from the executor may be the credential ID (UID) rather than runtime auth_index;
// we resolve via host.auth.list when direct get fails.
func reconcileAfterExecutorError(authID string, status int, body string) {
	reconcileAfterExecutorErrorForModel(authID, "", status, body)
}

// reconcileAfterExecutorErrorForModel 是带模型维度的版本，供执行器把失败的模型
// 一并带进来——禁用探活要靠它回放原故障现场。
// [参数] authID：账号标识；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// 最近修改时间：2026-10-22；改动原因：新增，让生命周期路径也能记录失败模型。
func reconcileAfterExecutorErrorForModel(authID, model string, status int, body string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	// Failover first — independent of lifecycle_auto: any upstream failure
	// (429/402/5xx/transport) counts toward cooldown.
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
	// Multi-account files are qoderwork-<uid>.json; list Name/ID usually carry that.
	wantName := "qoderwork-" + authID + ".json"
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

// reconcileByUID finds qoderwork auth by account UID and applies executor-error lifecycle.
func reconcileByUID(uid string, status int, body string) {
	reconcileByUIDForModel(uid, "", status, body)
}

// reconcileByUIDForModel 是带模型维度的版本。
// [参数] uid：账号 UID；model：已归一化的上游模型名，可为空；
// status：上游 HTTP 状态码；body：上游响应正文。
// 最近修改时间：2026-10-22；改动原因：新增，让 UID 形态的记账也记录失败模型。
func reconcileByUIDForModel(uid, model string, status int, body string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	// Failover first — independent of lifecycle_auto. reconcileByUID receives
	// the account UID; noteAccountFailure resolves it to the auth.ID key the
	// scheduler uses.
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
	wantName := "qoderwork-" + authUID + ".json"
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
// (qoderwork-<uid>.json naming). Pure helper for O(list) cache invalidation.
func listEntryMatchesUID(f pluginapi.HostAuthFileEntry, uid, wantName string) bool {
	if uid == "" {
		return false
	}
	if strings.EqualFold(f.Name, wantName) || strings.EqualFold(f.ID, wantName) {
		return true
	}
	base := strings.TrimSuffix(f.Name, ".json")
	return strings.EqualFold(base, "qoderwork-"+uid)
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
