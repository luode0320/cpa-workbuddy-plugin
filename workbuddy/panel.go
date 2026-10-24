// panel.go serves the management dashboard: the aggregated account list the
// web UI consumes (buildDashboardEx) and the embedded HTML page itself.
package main

import (
	_ "embed"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// wbAccount is one row of the dashboard.
type wbAccount struct {
	AuthIndex    string          `json:"auth_index"`
	AuthID       string          `json:"auth_id,omitempty"`
	Name         string          `json:"name"`
	Label        string          `json:"label"`
	Nickname     string          `json:"nickname"`
	UID          string          `json:"uid"`
	Region       string          `json:"region"` // "cn" or "global"
	Plan         string          `json:"plan"`
	Status       string          `json:"status"`
	CreatedAt    string          `json:"created_at,omitempty"` // account creation time (RFC3339), from JWT auth_time
	Disabled     bool            `json:"disabled"`
	Exhausted    bool            `json:"exhausted"`
	LowCredits   bool            `json:"low_credits"`           // 保活档：100~400，可路由但排最后
	CreditTier   string          `json:"credit_tier,omitempty"` // normal / preserve / disabled
	CreditsAt    string          `json:"credits_at,omitempty"`  // 积分快照时刻(RFC3339)，供面板显示新鲜度
	Selected     bool            `json:"selected"`              // panel active routing card
	TestFailed   bool            `json:"test_failed"`           // scheduled active-ping failed while credits remained
	Credits      *creditsSummary `json:"credits,omitempty"`
	Checkin      *checkinSummary `json:"checkin,omitempty"`
	TrialClaimed bool            `json:"trial_claimed,omitempty"` // Global: expert trial already claimed
	Error        string          `json:"error,omitempty"`
	// CPA-side cumulative counters (auth-file level, persisted by the host).
	Success int64 `json:"success,omitempty"`
	Failed  int64 `json:"failed,omitempty"`
	// Local in-memory failover state (cleared on process restart).
	FailCount int   `json:"fail_count,omitempty"`
	Cooling   bool  `json:"cooling,omitempty"`
	CoolUntil int64 `json:"cool_until,omitempty"` // cooldown deadline (unix secs); 0 = not cooling
	// 双写下的禁用侧：盘上的 failover_disable 标记才是恢复的唯一通道，冷却到期
	// 不会让账号回来，所以面板必须把它和冷却分开显示。
	FailoverDisabled bool   `json:"failover_disabled,omitempty"`
	FailoverModel    string `json:"failover_model,omitempty"` // 探活回放的模型（唯一目标）
	// FailoverUnconfirmed 表示盘上写着禁用但宿主列表仍为 enabled——写盘尚未被
	// 宿主吸收，这个号此刻仍会被路由。它是「显示与实际不符」的唯一在线判据。
	FailoverUnconfirmed bool `json:"failover_unconfirmed,omitempty"`
}

// failoverUnconfirmed 判定「盘上说已禁用、宿主仍在路由」这一不一致状态。
//
// 抽成纯函数是为了可测：buildDashboardEx 没有宿主 seam（它直接调 hostAuthList /
// hostAuthGetBundle），整函数无法在 cgo-shim 下单测，而这个判据恰好是面板文案
// 分叉的唯一来源，必须能被变异验证覆盖。
//
// [参数] diskDisabled：盘上带本机制标记且 disabled；hostDisabled：宿主列表的 disabled。
// [返回] true 表示写盘尚未被宿主吸收。
// 最近修改时间：2026-10-25；改动原因：新增，暴露写盘与宿主状态的分叉。
func failoverUnconfirmed(diskDisabled, hostDisabled bool) bool {
	return diskDisabled && !hostDisabled
}

// credits/checkin/plan fields are left empty — the panel renders skeletons
// and fetches them lazily via /credits?auth_index=<idx>. This avoids hitting
// upstream billing APIs for all accounts simultaneously on page load (which
// causes 500 from rate-limited /v2/billing/meter/get-user-resource).
//
// [参数] force：跳过缓存强制回源；fetchCredits：是否本轮拉取实时积分。
// [返回] 面板 dashboard 契约 map（accounts / summary 等）。
// 最近修改时间：2026-10-15 22:30:00 面板裁剪时同步清理成长中心锁（TASK-3.1）
func buildDashboardEx(force, fetchCredits bool) map[string]any {
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	// Prune cache entries for accounts that no longer exist (auth deleted via
	// CPA UI) or whose TTL expired long ago. Without this, accountCache grows
	// monotonically for the lifetime of the process.
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[f.ID] = struct{}{}
	}
	accountCache.Range(func(key, value any) bool {
		idx, _ := key.(string)
		if _, ok := live[idx]; !ok {
			accountCache.Delete(key)
			checkinLocks.Delete(key)
			lifecycleState.Delete(key)
			return true
		}
		if e, ok := value.(*accountCacheEntry); ok && time.Since(e.fetched) > 4*accountCacheTTL {
			accountCache.Delete(key)
		}
		return true
	})
	// Also prune stale lifecycle state and checkin locks for gone accounts.
	pruneLifecycleState()
	pruneCheckinLocks()
	out := make([]wbAccount, len(files))
	// Accounts are independent — fetch their dashboards concurrently. With 4
	// accounts this cuts cold-load latency from ~4×(3 serial upstream calls)
	// to roughly one slowest account.
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func(i int, f pluginapi.HostAuthFileEntry) {
			defer wg.Done()
			acct := wbAccount{
				AuthIndex: f.AuthIndex,
				AuthID:    f.ID,
				Name:      f.Name,
				Label:     f.Label,
				Status:    f.Status,
				CreatedAt: f.CreatedAt.Format(time.RFC3339),
				Disabled:  f.Disabled,
				Success:   f.Success,
				Failed:    f.Failed,
			}
			sa, phys, err := hostAuthGetBundle(f.AuthIndex)
			if err != nil {
				acct.Error = "load auth: " + err.Error()
				out[i] = acct
				return
			}
			// CreatedAt: prefer the JWT auth_time (the real account creation
			// moment). The host's HostAuthFileEntry.CreatedAt is unusable for
			// display — the file watcher re-stamps it with time.Now() on every
			// rescan, so it drifts to the latest credit refresh / keepalive /
			// test ping. Keep the host value only as a last-resort fallback for
			// legacy files whose token carries no auth_time.
			if t, ok := parseCreatedAtFromAccessToken(sa.Auth.AccessToken); ok {
				acct.CreatedAt = t.Format(time.RFC3339)
			}
			// Physical file is source of truth for disabled (host list may lag).
			if phys != nil {
				acct.Disabled = phys.Disabled
				acct.TestFailed = parseTestFailedFromAuthJSON(phys.JSON)
				// 禁用原因读盘：本机制的内存集合在插件热重载后为空，只靠它会让
				// 盘上仍 disabled 的号在面板里分不清是「探活中」还是人工禁用。
				// 只在盘上确实 disabled 时才认这个标记——宿主 UI 手工启用会把
				// disabled 改回 false 但保留自定义元数据，此时不该显示禁用徽章。
				if doc := parseFailoverAuthDoc(phys.JSON); doc.failoverDisable && doc.disabled {
					acct.FailoverDisabled = true
					acct.FailoverModel = doc.failoverModel
				}
				// 宿主是否真按这次禁用停掉了路由，只有宿主列表说得准：盘上写着
				// disabled:true 而宿主仍 enabled，说明写盘尚未被吸收（宿主每次请求
				// 结束都用内存 Metadata 重建文件，标记与 disabled 都可能被覆盖）。
				// 把不一致暴露出来，避免面板显示「已禁用」而实际仍在吃流量。
				acct.FailoverUnconfirmed = failoverUnconfirmed(acct.FailoverDisabled, f.Disabled)
				if phys.Name != "" {
					acct.Name = phys.Name
				}
			}
			acct.Nickname = sa.Account.Nickname
			acct.UID = sa.Account.UID
			// Success/Failed prefer the plugin-owned cumulative counters (survive
			// restart). The host-list values set above are the recent-window
			// (last ~200min) numbers — kept as the fallback for UID-less legacy
			// accounts. UID-bearing accounts read the in-memory cumulative value
			// (seeded from the persisted json at startup, then memory-first; see
			// counter.go) without re-reading json on every render.
			if strings.TrimSpace(acct.UID) != "" {
				ensureCounterLoaded(acct.UID, phys.JSON)
				acct.Success, acct.Failed = counterSnapshot(acct.UID)
			}
			acct.Region = accountRegion(sa)
			if fetchCredits {
				plan, ci, cr, errs := cachedAccountDetails(f.ID, sa, force)
				acct.Plan = plan
				acct.Checkin = ci
				acct.Credits = cr
				acct.Exhausted = isCreditsExhausted(cr)
				acct.CreditTier = creditTierFor(crRemainOf(cr), loadedLowCreditDisableBelow(), loadedMinRoutableCredits()).String()
				acct.LowCredits = acct.CreditTier == creditTierPreserve.String()
				if cr != nil && cr.FetchedAt != "" {
					acct.CreditsAt = cr.FetchedAt
				}
				if isGlobalDomain(sa.Auth.Domain) {
					acct.TrialClaimed = hasTrialPack(cr)
				}
				// Keep note in sync (throttled); do not block dashboard on save errors.
				_ = syncAuthNote(f.AuthIndex, f.ID, sa, cr, acct.Disabled)
				acct.Error = strings.Join(errs, "; ")
			} else {
				// 轻载：只读落盘快照，不打上游。
				//
				// 2026-10-23 用户裁定：进面板不刷新积分，直接读一小时一轮的
				// sidecar。快照缺失（新装 / 首轮还没跑）时才退回内存缓存，
				// 仍然不发上游请求——面板打开绝不应该触发一轮全量 billing 调用。
				if entry, ok := creditSnapshotGet(f.ID); ok && entry.Credits != nil {
					acct.Credits = entry.Credits
					acct.Exhausted = isCreditsExhausted(entry.Credits)
					acct.LowCredits = entry.Tier == creditTierPreserve
					acct.CreditTier = entry.Tier.String()
					if !entry.FetchedAt.IsZero() {
						acct.CreditsAt = entry.FetchedAt.Local().Format(time.RFC3339)
					}
					if isGlobalDomain(sa.Auth.Domain) {
						acct.TrialClaimed = hasTrialPack(entry.Credits)
					}
				} else if v, ok := accountCache.Load(f.ID); ok {
					if e, ok2 := v.(*accountCacheEntry); ok2 {
						acct.Plan = e.plan
						acct.Checkin = e.checkin
						acct.Credits = e.credits
						acct.Exhausted = isCreditsExhausted(e.credits)
						acct.LowCredits = e.credits != nil && !acct.Exhausted && e.credits.TotalRemain < loadedMinRoutableCredits()
						acct.CreditTier = authCreditTier(f.ID).String()
						if isGlobalDomain(sa.Auth.Domain) {
							acct.TrialClaimed = hasTrialPack(e.credits)
						}
					}
				}
			}
			out[i] = acct
		}(i, f)
	}
	wg.Wait()
	// After refresh (force), run lifecycle so exhaust→disable/delete is immediate.
	var life []map[string]any
	if force && lifecycleEnabled() {
		life = reconcileAllAccounts(true)
		// Drop accounts deleted during reconcile (Global exhaust) and refresh
		// disabled/exhausted from disk/cache (host list may lag after save).
		if files2, err2 := hostAuthList(); err2 == nil {
			live := make(map[string]struct{}, len(files2))
			disabledBy := make(map[string]bool, len(files2))
			for _, f := range files2 {
				live[f.AuthIndex] = struct{}{}
				// Prefer host list Disabled after reconcile; avoids N extra host.auth.get.
				// Dashboard row load already used hostAuthGetBundle for physical truth.
				disabledBy[f.AuthIndex] = f.Disabled
			}
			filtered := out[:0]
			for _, a := range out {
				if _, ok := live[a.AuthIndex]; !ok {
					continue
				}
				if d, ok := disabledBy[a.AuthIndex]; ok {
					a.Disabled = d
				}
				// Credits may have been refreshed during reconcile — re-read cache.
				if v, ok := accountCache.Load(a.AuthID); ok {
					if e, ok2 := v.(*accountCacheEntry); ok2 {
						if e.credits != nil {
							a.Credits = e.credits
							a.Exhausted = isCreditsExhausted(e.credits)
						}
						if e.plan != "" {
							a.Plan = e.plan
						}
						if e.checkin != nil {
							a.Checkin = e.checkin
						}
					}
				}
				filtered = append(filtered, a)
			}
			out = filtered
		}
	}
	checkinAutoMu.RLock()
	auto := checkinAuto
	checkinAutoMu.RUnlock()
	// Sync the 「测试」mirror BEFORE computing the panel selection, so
	// ensureDefaultActiveAuth applies the same availability contract as
	// scheduler.pick. The refresh also prunes entries for accounts that no
	// longer exist so the scheduler can't pin a session to a deleted auth.
	refreshTestFailedSetFromDisk()
	// Ensure default selection for panel + scheduler (lowest-credit available card).
	activeID := ensureDefaultActiveAuth(out)
	if force {
		// Re-mirror so the badge loop below sees any 测试 tag written by
		// another session since the pre-selection refresh above.
		refreshTestFailedSetFromDisk()
	}
	// Aggregate credits for panel/API consumers (all accounts currently in out).
	sum := summarizeCredits(out)
	// Mark selected account in list for UI; test_failed comes from the disk
	// mirror so the badge always matches the routing contract.
	for i := range out {
		out[i].Selected = out[i].AuthID == activeID
		out[i].TestFailed = isTestFailed(out[i].AuthID)
		// Failover state is in-memory only; surface it so the panel can show
		// consecutive failures + cooldown instead of a binary badge.
		if count, until, ok := failoverStateSnapshot(out[i].AuthID); ok && count > 0 {
			out[i].FailCount = count
			if until.After(time.Now()) {
				out[i].Cooling = true
				out[i].CoolUntil = until.Unix()
			}
		}
		// 禁用侧补漏：行构建读到的物理文件可能早于本次禁用落盘（写盘到 watcher
		// 生效的毫秒级窗口），此时盘上还没有标记但内存已登记，仍要显示「禁用探活中」。
		if !out[i].FailoverDisabled && isFailoverDisabledLocal(out[i].AuthID) {
			out[i].FailoverDisabled = true
			out[i].FailoverModel = failoverProbeModelFor(out[i].AuthID)
		}
	}
	resp := map[string]any{
		"accounts":       out,
		"active_auth":    activeID,
		"scheduler_mode": loadedSchedulerMode(),
		"checkin_auto":   auto,
		"lifecycle_auto": lifecycleEnabled(),
		"schedule":       []string{"00:00", "04:00", "08:00", "12:00", "16:00", "20:00"},
		"server_time":    time.Now().Format("2006-01-02 15:04:05"),
		// 低额保护阈值由后端下发，避免面板另存一份硬编码副本与调度口径漂移。
		"min_routable_credits":     loadedMinRoutableCredits(),
		"low_credit_disable_below": loadedLowCreditDisableBelow(),
		"credits_sweep":            lastCreditsSweep(),
		"summary":                  sum,
	}
	if len(life) > 0 {
		resp["lifecycle"] = life
	}
	return resp
}

// summarizeCredits aggregates remain/used across dashboard accounts.
func summarizeCredits(accounts []wbAccount) map[string]any {
	var remain, used, size, cnRemain, cnUsed, cnSize, glRemain, glUsed, glSize int64
	var known, disabledN, exhaustedN, packs int
	for _, a := range accounts {
		if a.Disabled {
			disabledN++
		}
		if a.Exhausted {
			exhaustedN++
		}
		if a.Credits == nil {
			continue
		}
		cr := a.Credits
		if cr.TotalRemain == 0 && cr.TotalUsed == 0 && cr.TotalSize == 0 && len(cr.Packages) == 0 {
			continue
		}
		known++
		remain += cr.TotalRemain
		used += cr.TotalUsed
		size += cr.TotalSize
		packs += cr.PackCount
		if a.Region == "global" {
			glRemain += cr.TotalRemain
			glUsed += cr.TotalUsed
			glSize += cr.TotalSize
		} else {
			cnRemain += cr.TotalRemain
			cnUsed += cr.TotalUsed
			cnSize += cr.TotalSize
		}
	}
	total := remain + used
	if size > total {
		total = size
	}
	return map[string]any{
		"account_count":   len(accounts),
		"known_count":     known,
		"disabled_count":  disabledN,
		"exhausted_count": exhaustedN,
		"pack_count":      packs,
		"total_remain":    remain,
		"total_used":      used,
		"total_size":      size,
		"total":           total,
		"cn_remain":       cnRemain,
		"cn_used":         cnUsed,
		"cn_size":         cnSize,
		"global_remain":   glRemain,
		"global_used":     glUsed,
		"global_size":     glSize,
	}
}

// Web panel (self-contained HTML, no external assets)
// -----------------------------------------------------------------------------

func servePanel(sub string) []byte {
	if sub != "" && sub != "/" && sub != "/panel" && sub != "/panel.html" {
		return []byte("<h1>404</h1>")
	}
	return panelHTML
}

//go:embed panel.html
var panelHTML []byte
