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
	Exhausted    bool            `json:"exhausted"`
	Selected     bool            `json:"selected"`
	TestFailed   bool            `json:"test_failed"`
	Credits      *creditsSummary `json:"credits,omitempty"`
	TrialClaimed bool            `json:"trial_claimed,omitempty"`
	Error        string          `json:"error,omitempty"`
	Success      int64           `json:"success,omitempty"`
	Failed       int64           `json:"failed,omitempty"`
	FailCount    int             `json:"fail_count,omitempty"`
	Cooling      bool            `json:"cooling,omitempty"`
	CoolUntil    int64           `json:"cool_until,omitempty"`
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
				plan, cr, errs := cachedAccountDetails(f.ID, sa, force)
				acct.Plan = plan
				acct.Credits = cr
				acct.Exhausted = isCreditsExhausted(cr)
				acct.TrialClaimed = hasTrialPack(cr)
				_ = syncAuthNote(f.AuthIndex, f.ID, sa, cr, acct.Disabled)
				acct.Error = strings.Join(errs, "; ")
			} else {
				if v, ok := accountCache.Load(f.ID); ok {
					if e, ok2 := v.(*accountCacheEntry); ok2 {
						acct.Plan = e.plan
						acct.Credits = e.credits
						acct.Exhausted = isCreditsExhausted(e.credits)
						acct.TrialClaimed = hasTrialPack(e.credits)
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
							a.Exhausted = isCreditsExhausted(e.credits)
						}
						if e.plan != "" {
							a.Plan = e.plan
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
		"global_remain":   remain,
		"global_used":     used,
		"global_size":     size,
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
