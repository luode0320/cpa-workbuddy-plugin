// lifecycle.go 实现 qwenwork 基于积分的账号生命周期：
//   - 额度耗尽（含上游 quota.exceeded）→ 自动禁用：写 disabled:true 与顶层
//     exhausted_disable:true；积分恢复后由校验流程自动重新启用
//   - 积分未知时不动作，避免误判
//   - 带 exhausted_disable 标记的历史自动禁用账号在校验时恢复启用
//   - 手动禁用（manual_disable 标记，或宿主 UI 写入的无标记禁用）永不自动改动；
//     SESSION-DEAD / TOKEN_EXPIRE 备注守卫同样保留
//   - 上游硬额度错误 → 复查积分后按策略处理
//   - 软限流 → 不删除 CN 账号
//
// QwenWork 没有免费模型：2026-10-18 生产实测 0 积分账号对
// qwen3.8-max-preview / qwen3.8-flash / qwen3.8-plus / glm-5.2 一律返回
// 429 balance_insufficient，故耗尽账号必须停止参与路由。
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
// 最近修改时间 2026-10-18 17:10:00（仅当积分恢复为正数时才重新启用）
func reenableAuth(authIndex, authID string, sa *storedAuth, cr *creditsSummary) error {
	mu := authFileLockFor(authIndex)
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
	raw, err := buildAuthFileJSON(sa, false, note, nil)
	if err != nil {
		return err
	}
	// 恢复优先走宿主管理 API：宿主真相源里的 disabled 不置回 false，
	// 宿主会继续挡住这个号。未配密钥或调用失败时回落直写物理文件。
	apiErr := setHostAuthDisabled(name, authIndex, false)
	if apiErr != nil {
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[qwenwork] cpa enable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
		}
		if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
			return err
		}
	}
	rememberLifecycleState(authID, false, note)
	accountCache.Delete(authID)
	return nil
}

// disableAuth 对额度耗尽的账号写入自动禁用状态。
// 语义与 workbuddy 对齐：仅在账号当前处于启用态时写 disabled:true 与
// exhausted_disable:true；已禁用的文件保持原状（manual_disable 与
// exhausted_disable 一律原样带过），避免把用户手动禁用的账号变成可自动恢复。
// [参数] authIndex：宿主账号索引；authID：账号标识；sa：账号凭据；
// cr：积分快照；reason：附加到备注的原因短语。
// [返回] 落盘错误；成功返回 nil。
// 最近修改时间：2026-10-18 17:10:00；改动原因：QwenWork 无免费模型，耗尽需自动禁用。
func disableAuth(authIndex, authID string, sa *storedAuth, cr *creditsSummary, reason string) error {
	mu := authFileLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	phys, err := hostAuthGetPhysical(authIndex)
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	extra := map[string]any{}
	finalDisabled := true
	// ownsDisable 标记本次是「本机制新写入的禁用」，只有它为真才允许调用
	// 宿主管理 API：manual 与未知写入方的禁用不归本机制管。
	ownsDisable := false
	if err == nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
		switch {
		case manualDisableFromAuthJSON(phys.JSON):
			// 用户手动禁用：保留意图标记，不写 exhausted_disable。
			extra["manual_disable"] = true
			finalDisabled = parseDisabledFromAuthJSON(phys.JSON)
		case exhaustedDisableFromAuthJSON(phys.JSON):
			// 已被本生命周期禁用：标记继续保留，恢复能力保持武装。
			extra["exhausted_disable"] = true
			finalDisabled = parseDisabledFromAuthJSON(phys.JSON)
		case parseDisabledFromAuthJSON(phys.JSON):
			// 无标记的禁用属未知写入方（如宿主 UI 切换）：保持原状。
			finalDisabled = true
		default:
			// 启用态 → 耗尽自动禁用（可自动恢复）。
			extra["exhausted_disable"] = true
			ownsDisable = true
		}
	}
	note := displayNote(sa, cr, finalDisabled)
	if reason != "" && !strings.Contains(note, reason) && len(note)+len(reason) < 75 {
		note = note + " · " + reason
	}
	if lifecycleStateUnchanged(authID, finalDisabled, note) {
		return nil
	}
	raw, err := buildAuthFileJSON(sa, finalDisabled, note, extra)
	if err != nil {
		return err
	}
	// 本机制新写入的禁用优先经宿主管理 API 写进路由真相源；未配置密钥或
	// 调用失败时回落直写物理文件。非本机关注的禁用（manual/未知）不调 API。
	if ownsDisable && finalDisabled {
		apiErr := setHostAuthDisabled(name, authIndex, true)
		if apiErr == nil {
			rememberLifecycleState(authID, finalDisabled, note)
			accountCache.Delete(authID)
			log.Printf("[qwenwork] exhausted-disable %s via cpa api (remain=%d)", authID, cr.TotalRemain)
			return nil
		}
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[qwenwork] cpa disable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
		}
	}
	if err := persistAuthDirect(name, path, legacyPath, raw); err != nil {
		return err
	}
	rememberLifecycleState(authID, finalDisabled, note)
	accountCache.Delete(authID)
	return nil
}

// deleteAuth removes exhausted credentials from disk.
func deleteAuth(authIndex, authID string, sa *storedAuth) error {
	mu := authFileLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(phys.Path)
	if path == "" {
		// Try to reconstruct path from peer qwenwork files' directory + canonical name.
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
	// Also remove legacy qwenwork.json if this UID was dual-named historically.
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
// credits/plan, active selection, and failover cooldown/counter.
// （同步自 workbuddy 0.14.7 账号删除功能；保号池与 session 绑定清理随对应功能同步后在此补充）
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

// peerAuthDir returns the directory of any qwenwork auth file known to the host.
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
	mu := authFileLockFor(authIndex)
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
		// overwrite our merge with newer plan values).
		_, cr2, _ := cachedAccountDetails(authID, sa, true)
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
		// 积分仍耗尽则保持禁用，只刷新备注（避免无谓落盘）。
		if isCreditsExhausted(cr) {
			_ = syncAuthNote(authIndex, authID, sa, cr, true)
			return lifecycleNone, nil
		}
		if err := reenableAuth(authIndex, authID, sa, cr); err != nil {
			return lifecycleReenable, err
		}
		return lifecycleReenable, nil
	}

	// 账号启用中：额度耗尽则自动禁用（可自动恢复），否则仅刷新备注。
	if isCreditsExhausted(cr) {
		if err := disableAuth(authIndex, authID, sa, cr, "耗尽"); err != nil {
			return lifecycleDisable, err
		}
		return lifecycleDisable, nil
	}
	_ = syncAuthNote(authIndex, authID, sa, cr, false)
	return lifecycleNone, nil
}

// reconcileAllAccounts walks qwenwork auths and applies lifecycle.
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
	if !failoverActive() || strings.TrimSpace(authID) == "" {
		return false
	}
	if !recordAccountFailure(authID, status, body) {
		return false
	}
	evictSessionBindingsForAuth(authID)
	go func() {
		_, id := resolveAuthIndexAndID(authID)
		if id == "" || id == authID {
			return
		}
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
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	// Failover first — independent of lifecycle_auto: any upstream failure
	// (429/402/5xx/transport) counts toward cooldown.
	noteAccountFailure(authID, status, body)
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
	// Multi-account files are qwenwork-<uid>.json; list Name/ID usually carry that.
	wantName := "qwenwork-" + authID + ".json"
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

// reconcileByUID finds qwenwork auth by account UID and applies executor-error lifecycle.
func reconcileByUID(uid string, status int, body string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	// Failover first — independent of lifecycle_auto. reconcileByUID receives
	// the account UID; noteAccountFailure resolves it to the auth.ID key the
	// scheduler uses.
	noteAccountFailure(uid, status, body)
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
	// Invalidate credits only — keep plan in cache.
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
	wantName := "qwenwork-" + authUID + ".json"
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
// (qwenwork-<uid>.json naming). Pure helper for O(list) cache invalidation.
func listEntryMatchesUID(f pluginapi.HostAuthFileEntry, uid, wantName string) bool {
	if uid == "" {
		return false
	}
	if strings.EqualFold(f.Name, wantName) || strings.EqualFold(f.ID, wantName) {
		return true
	}
	base := strings.TrimSuffix(f.Name, ".json")
	return strings.EqualFold(base, "qwenwork-"+uid)
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

// authFileLockFor 返回某个 authIndex 的文件级互斥锁。
//
// 生命周期改写（启用/禁用/删除）与保活刷新可能并发触碰同一个物理文件，
// 用 per-authIndex 锁串行化，避免读-改-写互相覆盖。
//
// [参数] authIndex 宿主分配的账号索引。
// [返回] 该索引专属的互斥锁。
// 最近修改时间：2026-10-18 00:09:00；改动原因：签到模块移除后独立出锁。
func authFileLockFor(authIndex string) *sync.Mutex {
	v, _ := authFileLocks.LoadOrStore(authIndex, &sync.Mutex{})
	return v.(*sync.Mutex)
}

var authFileLocks sync.Map // authIndex -> *sync.Mutex
