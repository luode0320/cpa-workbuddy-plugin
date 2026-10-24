// panel.go serves the management dashboard: the aggregated account list the
// web UI consumes (buildDashboardEx) and the embedded HTML page itself.
package main

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// wbAccount is one row of the dashboard.
type wbAccount struct {
	AuthIndex  string          `json:"auth_index"`
	AuthID     string          `json:"auth_id,omitempty"`
	Name       string          `json:"name"`
	Label      string          `json:"label"`
	Nickname   string          `json:"nickname"`
	UID        string          `json:"uid"`
	Region     string          `json:"region"` // "cn" or "global"
	Plan       string          `json:"plan"`
	Status     string          `json:"status"`
	Disabled   bool            `json:"disabled"`
	Exhausted  bool            `json:"exhausted"`
	LowCredits bool            `json:"low_credits"` // 剩余额度低于安全红线(min_routable_credits)
	Selected   bool            `json:"selected"`    // panel active routing card
	Credits    *creditsSummary `json:"credits,omitempty"`
	Checkin    *checkinSummary `json:"checkin,omitempty"`
	Error      string          `json:"error,omitempty"`
	// CPA-side cumulative counters (auth-file level, persisted by the host).
	Success int64 `json:"success,omitempty"`
	Failed  int64 `json:"failed,omitempty"`
	// Local in-memory failover state (cleared on process restart).
	FailCount int   `json:"fail_count,omitempty"`
	Cooling   bool  `json:"cooling,omitempty"`
	CoolUntil int64 `json:"cool_until,omitempty"` // cooldown deadline (unix secs); 0 = not cooling
	// 24 小时每日调用上限持久化冷却状态
	DailyCooling       bool   `json:"daily_cooling,omitempty"`
	DailyCoolingUntil  int64  `json:"daily_cooling_until,omitempty"`
	DailyCoolingRemain string `json:"daily_cooling_remain,omitempty"`
	// 双写下的禁用侧：盘上的 failover_disable 标记才是恢复的唯一通道，冷却到期
	// 不会让账号回来，所以面板必须把它和冷却分开显示。
	FailoverDisabled bool   `json:"failover_disabled,omitempty"`
	FailoverModel    string `json:"failover_model,omitempty"` // 探活回放的模型（唯一目标）
	// 当日探活配额：达到上限后当日不再探测，本地 0 点恢复。
	ProbeFails int `json:"probe_fails,omitempty"`
	ProbeLimit int `json:"probe_limit,omitempty"`
	// 轮换让位：账号连续承流到阈值后被主动冷却，与失败冷却分表存储，
	// 因此两者的徽章要在面板上分开显示，否则运维无法区分「坏了」和「用太久」。
	RotationCooling bool  `json:"rotation_cooling,omitempty"`
	RotationUntil   int64 `json:"rotation_until,omitempty"`      // 轮换冷却截止（unix 秒）
	RotationServedM int   `json:"rotation_served_min,omitempty"` // 本轮已连续承流分钟数
}

// credits/checkin/plan fields are left empty — the panel renders skeletons
// and fetches them lazily via /credits?auth_index=<idx>. This avoids hitting
// upstream billing APIs for all accounts simultaneously on page load (which
// causes 500 from rate-limited /v2/billing/meter/get-user-resource).
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
			// Physical file is source of truth for disabled (host list may lag).
			if phys != nil {
				acct.Disabled = phys.Disabled
				// 禁用原因读盘：只在盘上确实 disabled 且带本机制标记时才显示
				// 「禁用探活中」。宿主 UI 手工启用会把 disabled 改回 false 但
				// 保留自定义元数据，此时不该继续显示禁用徽章。
				if doc := parseFailoverAuthDoc(phys.JSON); doc.failoverDisable && doc.disabled {
					acct.FailoverDisabled = true
					acct.FailoverModel = doc.failoverModel
				}
				if phys.Name != "" {
					acct.Name = phys.Name
				}
			}
			acct.Nickname = sa.Account.Nickname
			acct.UID = sa.Account.UID
			// Success/Failed prefer the plugin-owned cumulative counters
			// (survive restart). The host-list values set above are the
			// recent-window (last ~200min) numbers — kept as the fallback for
			// UID-less legacy accounts. UID-bearing accounts read the
			// in-memory cumulative value (seeded from the persisted json at
			// startup, then memory-first; see counter.go) without re-reading
			// json on every render. （同步自 workbuddy 0.14.10/0.14.11）
			if strings.TrimSpace(acct.UID) != "" && phys != nil {
				ensureCounterLoaded(acct.UID, phys.JSON)
				acct.Success, acct.Failed = counterSnapshot(acct.UID)
			}
			acct.Region = "cn"
			if fetchCredits {
				plan, ci, cr, errs := cachedAccountDetails(f.ID, sa, force)
				acct.Plan = plan
				acct.Checkin = ci
				acct.Credits = cr
				acct.Exhausted = isCreditsExhausted(cr)
				acct.LowCredits = isCreditsLowForRouting(cr)
				// Keep note in sync (throttled); do not block dashboard on save errors.
				_ = syncAuthNote(f.AuthIndex, f.ID, sa, cr, acct.Disabled)
				acct.Error = strings.Join(errs, "; ")
			} else {
				// Light load: use cached values if available, but don't fetch upstream.
				loaded := false
				if v, ok := accountCache.Load(f.ID); ok {
					if e, ok2 := v.(*accountCacheEntry); ok2 {
						acct.Plan = e.plan
						acct.Checkin = e.checkin
						acct.Credits = e.credits
						acct.Exhausted = isCreditsExhausted(e.credits)
						acct.LowCredits = isCreditsLowForRouting(e.credits)
						loaded = true
					}
				}
				// 内存缓存是空的时候（插件刚热重载、或本轮尚未刷新过这个号），
				// 回落到 sidecar 快照：否则面板首屏会整片显示"积分未知"，
				// 与实际状态不符，也会让运维误判整池数据丢失。
				if !loaded {
					if snap, ok := creditSnapshotGet(f.ID); ok {
						acct.Plan = snap.Plan
						acct.Credits = snap.Credits
						acct.Exhausted = isCreditsExhausted(snap.Credits)
						acct.LowCredits = isCreditsLowForRouting(snap.Credits)
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
		// Drop accounts deleted during reconcile (CN exhaust) and refresh
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
							a.LowCredits = isCreditsLowForRouting(e.credits)
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
	// Ensure default selection for panel + scheduler (lowest-credit available card).
	activeID := ensureDefaultActiveAuth(out)
	// Aggregate credits for panel/API consumers (all accounts currently in out).
	sum := summarizeCredits(out)
	// Mark selected account in list for UI.
	for i := range out {
		out[i].Selected = out[i].AuthID == activeID
		// Failover state is in-memory only; surface it so the panel can show
		// consecutive failures + cooldown.
		if count, until, ok := failoverStateSnapshot(out[i].AuthID); ok && count > 0 {
			out[i].FailCount = count
			if until.After(time.Now()) {
				out[i].Cooling = true
				out[i].CoolUntil = until.Unix()
			}
		}
		// 禁用侧补漏：本机制的内存集合在插件热重载后为空，只靠它会让盘上仍
		// disabled 的号在面板里分不清是「探活中」还是人工禁用。盘上有标记
		// 且确实 disabled 才算；外部启用后 disabled=false，这里自然不再标。
		if !out[i].FailoverDisabled && isFailoverDisabledLocal(out[i].AuthID) {
			out[i].FailoverDisabled = true
			out[i].FailoverModel = failoverProbeModelFor(out[i].AuthID)
		}
		// sidecar 才是禁用的权威来源（宿主会按内存 Status 写回覆盖 disabled、
		// fsnotify 也会静默失效）。盘上标记丢失但 sidecar 仍禁用时，
		// 面板必须继续显示禁用，否则运维会以为账号已恢复。
		if !out[i].FailoverDisabled {
			if entry, ok := failoverStateGet(out[i].AuthID); ok && entry.Disabled {
				out[i].FailoverDisabled = true
				out[i].FailoverModel = entry.Model
			}
		}
		if out[i].FailoverDisabled {
			out[i].ProbeFails = failoverProbeQuotaSnapshot(out[i].AuthID)
			out[i].ProbeLimit = failoverProbeQuotaBudget()
		}
		// 轮换让位与失败冷却分开标记：前者是负载均衡，后者是故障。
		if rotationCoolingDown(out[i].AuthID, time.Now()) {
			out[i].RotationCooling = true
			rotationMu.Lock()
			if st := rotationStateLocked(out[i].AuthID, false); st != nil {
				out[i].RotationUntil = st.cooldownUntil.Unix()
			}
			rotationMu.Unlock()
		}
		out[i].RotationServedM = rotationServingMinutes(out[i].AuthID, time.Now())
		if isAccountDailyCooling(out[i].AuthID) {
			out[i].DailyCooling = true
			until := getAccountDailyCoolingUntil(out[i].AuthID)
			out[i].DailyCoolingUntil = until.Unix()
			remain := time.Until(until)
			if remain > 0 {
				h := int(remain.Hours())
				m := int(remain.Minutes()) % 60
				out[i].DailyCoolingRemain = fmt.Sprintf("%02d:%02d", h, m)
			}
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
		"summary":        sum,
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
