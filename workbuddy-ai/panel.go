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
	Region       string          `json:"region"` // "global"
	Plan         string          `json:"plan"`
	Status       string          `json:"status"`
	CreatedAt    string          `json:"created_at,omitempty"`
	Disabled     bool            `json:"disabled"`
	Selected     bool            `json:"selected"`
	TestFailed   bool            `json:"test_failed"`
	Credits      *creditsSummary `json:"credits,omitempty"`
	Checkin      *checkinSummary `json:"checkin,omitempty"`
	TrialClaimed bool            `json:"trial_claimed,omitempty"`
	Registered   bool            `json:"registered"` // 已完成注册地填写；false 且无额度时为「未注册」态
	Error        string          `json:"error,omitempty"`
	Success      int64           `json:"success,omitempty"`
	Failed       int64           `json:"failed,omitempty"`
	FailCount    int             `json:"fail_count,omitempty"`
	Cooling      bool            `json:"cooling,omitempty"`
	CoolUntil    int64           `json:"cool_until,omitempty"`
}

// resolveRegisteredState 判定账号是否已完成注册。已有 plan（trial/free/付费）的账号
// 必然走过注册流程，直接判定为已注册、不产生额外上游调用；仅对无 plan 的疑似未注册
// 账号查询上游 areaInfoComplete，并把结果缓存到 accountCache 避免每轮重查。
// [参数] authID 账号缓存键；sa 账号凭证；plan 当前套餐（空表示无套餐）
// [返回] true 表示已完成注册
// 最近修改时间：2026-10-15 18:20:00；改动原因：新增补注册链路的面板状态透出
func resolveRegisteredState(authID string, sa *storedAuth, plan string) bool {
	if strings.TrimSpace(plan) != "" {
		return true
	}
	if v, ok := accountCache.Load(authID); ok {
		if e, ok2 := v.(*accountCacheEntry); ok2 && e.registered != nil {
			return *e.registered
		}
	}
	registered, err := fetchRegistrationStatus(sa)
	if err != nil {
		return false
	}
	if v, ok := accountCache.Load(authID); ok {
		if e, ok2 := v.(*accountCacheEntry); ok2 {
			fresh := *e
			fresh.registered = &registered
			accountCache.Store(authID, &fresh)
		}
	}
	return registered
}

func buildDashboardEx(force, fetchCredits bool) map[string]any {
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
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
	pruneLifecycleState()
	pruneCheckinLocks()

	out := make([]wbAccount, len(files))
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
			if t, ok := parseCreatedAtFromAccessToken(sa.Auth.AccessToken); ok {
				acct.CreatedAt = t.Format(time.RFC3339)
			}
			if phys != nil {
				acct.Disabled = phys.Disabled
				acct.TestFailed = parseTestFailedFromAuthJSON(phys.JSON)
				if phys.Name != "" {
					acct.Name = phys.Name
				}
			}
			acct.Nickname = sa.Account.Nickname
			acct.UID = sa.Account.UID
			if strings.TrimSpace(acct.UID) != "" {
				ensureCounterLoaded(acct.UID, phys.JSON)
				acct.Success, acct.Failed = counterSnapshot(acct.UID)
			}
			acct.Region = "global"

			if fetchCredits {
				plan, ci, cr, errs := cachedAccountDetails(f.ID, sa, force)
				acct.Plan = plan
				acct.Checkin = ci
				acct.Credits = cr
				acct.TrialClaimed = hasTrialPack(cr)
				acct.Registered = resolveRegisteredState(f.ID, sa, plan)
				_ = syncAuthNote(f.AuthIndex, f.ID, sa, cr, acct.Disabled)
				acct.Error = strings.Join(errs, "; ")
			} else {
				if v, ok := accountCache.Load(f.ID); ok {
					if e, ok2 := v.(*accountCacheEntry); ok2 {
						acct.Plan = e.plan
						acct.Checkin = e.checkin
						acct.Credits = e.credits
						acct.TrialClaimed = hasTrialPack(e.credits)
						acct.Registered = resolveRegisteredState(f.ID, sa, e.plan)
					}
				}
				// 缓存缺失时用盘上快照兜底：进程刚重启、watchdog 还没跑完
				// 首轮回灌，或缓存条目已被 4×TTL 的 GC 清掉时，面板首屏
				// 也不会退回「加载中…」。顺手回灌缓存，避免每次渲染都重读盘。
				if acct.Credits == nil && phys != nil {
					if cr := parseCreditsSnapshotFromAuthJSON(phys.JSON); cr != nil {
						acct.Credits = cr
						acct.TrialClaimed = hasTrialPack(cr)
						accountCache.Store(f.ID, &accountCacheEntry{credits: cr, fetched: time.Now()})
					}
				}
			}
			out[i] = acct
		}(i, f)
	}
	wg.Wait()

	var life []map[string]any
	if force && lifecycleEnabled() {
		life = reconcileAllAccounts(true)
		if files2, err2 := hostAuthList(); err2 == nil {
			live := make(map[string]struct{}, len(files2))
			disabledBy := make(map[string]bool, len(files2))
			for _, f := range files2 {
				live[f.AuthIndex] = struct{}{}
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
				if v, ok := accountCache.Load(a.AuthID); ok {
					if e, ok2 := v.(*accountCacheEntry); ok2 {
						if e.credits != nil {
							a.Credits = e.credits
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

	refreshTestFailedSetFromDisk()
	activeID := ensureDefaultActiveAuth(out)
	if force {
		refreshTestFailedSetFromDisk()
	}
	checkinAutoMu.RLock()
	auto := checkinAuto
	checkinAutoMu.RUnlock()
	sum := summarizeCredits(out)
	for i := range out {
		out[i].Selected = out[i].AuthID == activeID
		out[i].TestFailed = isTestFailed(out[i].AuthID)
		if count, until, ok := failoverStateSnapshot(out[i].AuthID); ok && count > 0 {
			out[i].FailCount = count
			if until.After(time.Now()) {
				out[i].Cooling = true
				out[i].CoolUntil = until.Unix()
			}
		}
	}
	resp := map[string]any{
		"accounts":       out,
		"active_auth":    activeID,
		"scheduler_mode": loadedSchedulerMode(),
		"checkin_auto":   auto,
		"lifecycle_auto": lifecycleEnabled(),
		"server_time":    time.Now().Format("2006-01-02 15:04:05"),
		"summary":        sum,
	}
	if len(life) > 0 {
		resp["lifecycle"] = life
	}
	return resp
}

func summarizeCredits(accounts []wbAccount) map[string]any {
	var remain, used, size int64
	var known, disabledN, packs int
	for _, a := range accounts {
		if a.Disabled {
			disabledN++
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
	}
	total := remain + used
	if size > total {
		total = size
	}
	return map[string]any{
		"account_count":  len(accounts),
		"known_count":    known,
		"disabled_count": disabledN,
		"pack_count":     packs,
		"total_remain":   remain,
		"total_used":     used,
		"total_size":     size,
		"total":          total,
		"global_remain":  remain,
		"global_used":    used,
		"global_size":    size,
	}
}

func servePanel(sub string) []byte {
	if sub != "" && sub != "/" && sub != "/panel" && sub != "/panel.html" {
		return []byte("<h1>404</h1>")
	}
	return panelHTML
}

//go:embed panel.html
var panelHTML []byte
