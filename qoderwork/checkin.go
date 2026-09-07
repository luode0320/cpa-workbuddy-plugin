// checkin.go implements daily check-in for CN accounts: the manual
// handleManualCheckin endpoint, the every-4-hours auto scheduler, and the
// per-account mutex that prevents duplicate check-ins from racing browser
// tabs. CN accounts are excluded — they use one-shot trial claims instead.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	schedulerStop chan struct{}
	schedulerMu   sync.Mutex
)

func ensureScheduler() {
	schedulerMu.Lock()
	defer schedulerMu.Unlock()
	if schedulerStop != nil {
		return // already running
	}
	schedulerStop = make(chan struct{})
	go schedulerLoop(schedulerStop)
}

// Note: there is deliberately no stopCheckinScheduler. The plugin shutdown
// export is a no-op (see cliproxyPluginShutdown) because the host invokes it
// during its own runtime teardown, where touching Go sync primitives from the
// plugin's c-shared runtime caused SIGSEGV on every restart.

func nextCheckinTime(now time.Time) time.Time {
	var earliest time.Time
	// Consider both checkin and keepalive schedules so the timer wakes up for
	// whichever fires first (e.g. 00:00 checkin vs 00:00 keepalive → 00:00 wins,
	// then 04:00 keepalive fires on the next tick).
	hours := append([]int{}, checkinHours...)
	hours = append(hours, keepaliveHours...)
	for _, h := range hours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour) // slot already passed today → tomorrow
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

func schedulerLoop(stop chan struct{}) {
	for {
		next := nextCheckinTime(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			runAutoCheckin()
			// Fire keepalive if the current tick falls within its scheduled
			// window (e.g. 04:00 keepalive fires on the 04:00 tick even though
			// the previous checkin tick was 00:00).
			if shouldRunKeepaliveNow(time.Now()) {
				runTokenKeepalive()
			}
		}
	}
}

// runAutoCheckin is the scheduled lifecycle tick (every 4 hours: 00:00 / 04:00 / 08:00 / 12:00 / 16:00 / 20:00).
//
// v0.6.31: per-account work runs concurrently (sem=4) — was serial, so N accounts
// meant 3N serial HTTP round-trips on the billing API. Matches the pattern used
// by buildDashboardEx and handleManualCheckin.
func runAutoCheckin() {
	checkinAutoMu.RLock()
	doCheckin := checkinAuto
	checkinAutoMu.RUnlock()
	// Lifecycle may still run when check-in is off (credit gate).
	if !doCheckin && !lifecycleEnabled() {
		return
	}
	files, err := hostAuthList()
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, f := range files {
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			processAutoCheckinAccount(f, doCheckin)
		}()
	}
	wg.Wait()
}

// processAutoCheckinAccount handles one account's scheduled tick. Extracted so
// runAutoCheckin can fan out per-account work without duplicating logic.
func processAutoCheckinAccount(f pluginapi.HostAuthFileEntry, doCheckin bool) {
	// A-24: only fetch sa when needed (checkin). For lifecycle-only paths,
	// let reconcileOneAccount do the single hostAuthGetBundle internally.
	if doCheckin {
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			return
		}
		// CN: daily check-in when enabled.
		ci, err := fetchCheckinStatus(sa)
		if err == nil && ci != nil && ci.Active && !ci.TodayCheckedIn {
			if _, callErr := performCheckinCall(sa); callErr == nil {
				// Refresh once after a successful checkin call so cache reflects
				// the post-call state. If the status call fails keep the pre-call
				// snapshot rather than dropping it (v0.6.31: avoid shadowing ci
				// with a second fetch that could race with concurrent readers).
				if ci2, _ := fetchCheckinStatus(sa); ci2 != nil {
					ci = ci2
				}
				// P1-5: checkin grants new credits — refresh the credits cache
				// immediately so the panel shows the updated balance without
				// waiting for the async reconcile pass.
				if cr2, crErr := fetchUserResource(sa); crErr == nil && cr2 != nil {
					if v, ok := accountCache.Load(f.ID); ok {
						if prev, ok2 := v.(*accountCacheEntry); ok2 {
							fresh := *prev
							fresh.credits = cr2
							fresh.fetched = time.Now()
							accountCache.Store(f.ID, &fresh)
						}
					}
				}
			}
		}
		// Refresh cache with latest checkin status (merge, don't wipe credits/plan).
		if ci != nil {
			var prev *accountCacheEntry
			if v, ok := accountCache.Load(f.ID); ok {
				prev, _ = v.(*accountCacheEntry)
			}
			entry := &accountCacheEntry{checkin: ci, fetched: time.Now()}
			if prev != nil {
				entry.credits = prev.credits
				entry.plan = prev.plan
			}
			accountCache.Store(f.ID, entry)
		}
		if lifecycleEnabled() {
			_, _ = reconcileOneAccount(f.AuthIndex, f.ID, true)
		}
		return
	}
	// Lifecycle-only (checkin off): reconcile handles its own get.
	if lifecycleEnabled() {
		_, _ = reconcileOneAccount(f.AuthIndex, f.ID, true)
	}
}

// handleManualCheckin checks in one account (auth_index) or all qoderwork
// accounts. Unlike the workbuddy three-phase classify/execute/summarize flow,
// QoderWork's checkin is a simple Bearer GET+POST — we run it directly per
// account under an 8s timeout, no classify stage.
func handleManualCheckin(req pluginapi.ManagementRequest) map[string]any {
	t0 := time.Now()
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	single := authIndex != ""

	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var targets []pluginapi.HostAuthFileEntry
	for _, f := range files {
		if !single || f.AuthIndex == authIndex {
			targets = append(targets, f)
		}
	}
	if len(targets) == 0 {
		return map[string]any{"error": "no matching account"}
	}

	// Run checkins concurrently (one goroutine per account, bounded).
	type result struct {
		idx int
		out map[string]any
	}
	outCh := make(chan result, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, f := range targets {
		wg.Add(1)
		go func(i int, f pluginapi.HostAuthFileEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			outCh <- result{idx: i, out: checkinOneAccount(f)}
		}(i, f)
	}
	wg.Wait()
	close(outCh)

	results := make([]map[string]any, len(targets))
	for r := range outCh {
		results[r.idx] = r.out
	}
	successN, alreadyN, failN := 0, 0, 0
	for _, r := range results {
		if r["error"] != nil {
			failN++
			continue
		}
		if r["skipped"] == true {
			alreadyN++
			continue
		}
		if r["success"] == true {
			successN++
		} else {
			failN++
		}
	}
	return map[string]any{
		"results": results,
		"summary": map[string]any{
			"total":      len(targets),
			"success":    successN,
			"already":    alreadyN,
			"fail":       failN,
			"elapsed_ms": time.Since(t0).Milliseconds(),
		},
	}
}

// checkinOneAccount performs the actual GET status + POST claim flow for one
// account. 8s overall budget — never blocks past that.
func checkinOneAccount(f pluginapi.HostAuthFileEntry) map[string]any {
	out := map[string]any{
		"auth_index": f.AuthIndex,
		"name":       f.Name,
	}
	mu := checkinLockFor(f.AuthIndex)
	mu.Lock()
	defer mu.Unlock()

	sa, err := hostAuthGet(f.AuthIndex)
	if err != nil {
		out["error"] = "get auth: " + err.Error()
		return out
	}
	out["nickname"] = sa.Account.Nickname

	// Step 1: GET status (5s budget).
	ci, err := fetchCheckinStatus(sa)
	if err != nil {
		out["error"] = "status: " + err.Error()
		return out
	}
	if ci.TodayCheckedIn {
		out["success"] = true
		out["skipped"] = true
		out["reason"] = "already"
		out["message"] = "今日已签到"
		out["streak_days"] = ci.StreakDays
		out["total_credits"] = ci.TotalCredits
		return out
	}

	// Step 2: POST claim (5s budget).
	res, err := performCheckinCall(sa)
	if err != nil {
		out["error"] = "claim: " + err.Error()
		return out
	}
	// QoderWork returns {"result":"ALREADY_CLAIMED"} with HTTP 409 — surface
	// as already rather than error.
	if result, _ := res["result"].(string); result == "ALREADY_CLAIMED" {
		out["success"] = true
		out["skipped"] = true
		out["reason"] = "already"
		out["message"] = "今日已签到"
		if rc, ok := res["rewardCredits"].(float64); ok {
			out["reward_credits"] = int64(rc)
		}
		return out
	}
	if success, _ := res["success"].(bool); success {
		out["success"] = true
		if rc, ok := res["rewardCredits"].(float64); ok {
			out["reward_credits"] = int64(rc)
		}
		out["message"] = "签到成功"
		// Refresh the credits snapshot so the panel shows the post-checkin
		// balance immediately (check-in grants new credits). Best-effort:
		// a failure here must not flip the check-in result to error.
		if cr, crErr := fetchUserResource(sa); crErr == nil && cr != nil {
			out["credits"] = cr
		}
		return out
	}
	out["success"] = false
	out["message"] = res
	return out
}

func checkinLockFor(authIndex string) *sync.Mutex {
	v, _ := checkinLocks.LoadOrStore(authIndex, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// checkProUpgradeEligibility returns whether the account can still claim the
// one-time Pro Upgrade pack (+1800).
func checkProUpgradeEligibility(sa *storedAuth) (bool, error) {
	req, err := http.NewRequest(http.MethodGet, upstreamBaseCN+"/sash/api/v1/me/pro-upgrade/eligibility", nil)
	if err != nil {
		return false, err
	}
	billingHeaders(req, sa)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return false, err
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("http %d", resp.StatusCode)
	}
	var m struct {
		Eligible bool `json:"eligible"`
	}
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		return false, err
	}
	return m.Eligible, nil
}

// claimProUpgrade claims the one-time Pro Upgrade pack (+1800 credits).
func claimProUpgrade(sa *storedAuth) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, endpointProUpgrade, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	billingHeaders(req, sa)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return map[string]any{"success": false, "message": err.Error()}, nil
	}
	if resp.StatusCode >= 400 {
		return map[string]any{"success": false, "message": fmt.Sprintf("http %d: %s", resp.StatusCode, truncateRedacted(string(resp.Body), 200))}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		return nil, err
	}
	if _, ok := m["success"]; !ok {
		m["success"] = true
	}
	return m, nil
}

// pruneCheckinLocks removes lock entries for auth indices that no longer
// exist in hostAuthList. Call after dashboard prune.
// Lock keys are auth_index (used for host RPC), so live map needs auth_index too.
func pruneCheckinLocks() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[f.ID] = struct{}{}
		live[f.AuthIndex] = struct{}{} // checkinLockFor uses auth_index as key
	}
	checkinLocks.Range(func(key, _ any) bool {
		idx, _ := key.(string)
		if _, ok := live[idx]; !ok {
			checkinLocks.Delete(key)
		}
		return true
	})
}

// handleClaimPro checks pro-upgrade eligibility for one account and claims
// the one-time Pro pack (+ credits) when eligible. Surfaced as a panel
// per-card button — NOT called automatically during login (login writes the
// auth file first; this is a user-triggered post-login action).
func handleClaimPro(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required"}
	}
	sa, err := hostAuthGet(authIndex)
	if err != nil {
		return map[string]any{"error": "get auth: " + err.Error()}
	}
	out := map[string]any{
		"auth_index": authIndex,
		"nickname":   sa.Account.Nickname,
	}
	eligible, err := checkProUpgradeEligibility(sa)
	if err != nil {
		out["error"] = "eligibility: " + err.Error()
		return out
	}
	if !eligible {
		out["success"] = false
		out["message"] = "不可领取（已领或活动未开放）"
		return out
	}
	res, err := claimProUpgrade(sa)
	if err != nil {
		out["error"] = "claim: " + err.Error()
		return out
	}
	for k, v := range res {
		out[k] = v
	}
	if _, ok := out["success"]; !ok {
		out["success"] = true
	}
	// Refresh credits snapshot so panel shows updated balance immediately.
	if cr, crErr := fetchUserResource(sa); crErr == nil && cr != nil {
		out["credits"] = cr
	}
	return out
}
