// checkin.go implements daily check-in for CN accounts: the manual
// handleManualCheckin endpoint, the every-4-hours auto scheduler, and the
// per-account mutex that prevents duplicate check-ins from racing browser
// tabs. CN accounts are excluded — they use one-shot trial claims instead.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
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

// schedulerJitterMax 是槽位触发时刻的随机抖动上限。
//
// 各插件的槽位都对齐本地整点（00/04/08/12/16/20），所有插件会在同一秒
// 一起醒来，账号规模上来后形成整点尖峰。给每个插件、每一轮叠加一段
// 不大于该值的随机偏移，把尖峰摊平到数分钟窗口内。
//
// 上界取 5 分钟：远小于 4 小时槽位间隔（不会跨槽串位），也远小于
// shouldRunKeepaliveNow 的 1 小时窗口（抖动后仍落在同一窗口内）。
const schedulerJitterMax = 5 * time.Minute

// schedulerJitterFn 是可替换的抖动源，单测注入确定值；生产用 [0, max) 均匀分布。
var schedulerJitterFn = func() time.Duration {
	return time.Duration(rand.N(int64(schedulerJitterMax)))
}

// nextCheckinFireTime 在纯函数 nextCheckinTime 的结果上叠加抖动。
//
// nextCheckinTime 保持无副作用，供既有调度断言继续验证绝对槽位；抖动只在
// 真正创建定时器时施加，避免把随机性带进可测的排期计算。
//
// [参数] now：当前时间。
// [返回] 实际唤醒时刻，严格晚于 now。
func nextCheckinFireTime(now time.Time) time.Time {
	next := nextCheckinTime(now)
	jitter := schedulerJitterFn()
	if jitter > 0 {
		next = next.Add(jitter)
	}
	// 抖动不得把唤醒时刻推到 now 之前（极小概率出现在槽位前一瞬间抖动越界）。
	if !next.After(now) {
		return now.Add(time.Second)
	}
	return next
}

// schedulerLoop 驱动签到与 token 保活共用的定时唤醒：到达并集时刻后执行签到，
// 命中保活窗口时同 tick 执行一次 token 刷新。
//
// [参数] stop: 关闭信号通道，收到后停止定时器并退出循环。
// [返回] 无（阻塞循环，随 stop 关闭返回）。
// 最近修改时间：2026-10-14 23:26:48；改动原因：签到恢复每 4 小时，移除已冗余的时段门控。
func schedulerLoop(stop chan struct{}) {
	for {
		// 叠加随机抖动：各插件槽位同刻对齐时会形成整点尖峰，抖动把全机
		// 唤醒摊平到数分钟窗口内。
		next := nextCheckinFireTime(time.Now())
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
		if err != nil {
			// 定时路径此前对 status 失败完全静默：运维只能靠面板空白猜原因。
			logCheckinFailure("auto-status", sa, err)
		}
		if err == nil && ci != nil && ci.Active && !ci.TodayCheckedIn {
			callRes, callErr := performCheckinCall(sa, ci.CampaignID)
			if callErr != nil {
				// 领取失败必须留痕：否则「今天没签上」在生产完全不可查。
				logCheckinFailure("auto-claim", sa, callErr)
			} else {
				credit, _ := callRes["rewardCredits"].(float64)
				log.Printf("[qoderwork] auto checkin ok account=%s campaign=%s credit=+%d",
					checkinLogTag(sa), ci.CampaignID, int64(credit))
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
		} else if err == nil && ci != nil {
			// 定时跳过（无活动 / 今日已签）也留一行：这是「为什么今天没加积分」
			// 的唯一可查证据。已签分支只在首个槽位有价值，其余槽位会重复，
			// 因此统一压成一行 info。
			reason := "no_campaign"
			if ci.TodayCheckedIn {
				reason = "already"
			}
			log.Printf("[qoderwork] auto checkin skip account=%s reason=%s show_campaign=%v", checkinLogTag(sa), reason, ci.ShowCampaign)
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
	successN, alreadyN, failN, noActivityN := 0, 0, 0, 0
	for _, r := range results {
		if r["error"] != nil {
			failN++
			continue
		}
		if r["skipped"] == true {
			alreadyN++
			continue
		}
		if r["reason"] == "global" {
			// 无可领取活动（活动未开放/账号不适用）不是失败，单列。
			noActivityN++
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
			"total":       len(targets),
			"success":     successN,
			"already":     alreadyN,
			"no_activity": noActivityN,
			"fail":        failN,
			"elapsed_ms":  time.Since(t0).Milliseconds(),
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
		out["total_credits"] = ci.TotalCredits
		// 不返回 streak_days：上游 campaigns 不提供连续天数，编造值曾让面板
		// 显示「连续 1 天」（见 billing.go fetchCheckinStatus 注释）。
		log.Printf("[qoderwork] checkin skip account=%s reason=already campaign=%s", checkinLogTag(sa), ci.CampaignID)
		return out
	}
	if !ci.Active {
		out["success"] = false
		out["reason"] = "global"
		out["message"] = checkinNoActivityMessage(ci)
		// 无活动是「上游按账号下发」的结果，不是失败：必须留可查证日志，
		// 否则运维无法区分「没活动」与「签到链路坏了」（2026-11-07 反馈）。
		log.Printf("[qoderwork] checkin skip account=%s reason=no_campaign show_campaign=%v", checkinLogTag(sa), ci.ShowCampaign)
		return out
	}

	// Step 2: POST campaign claim (5s budget).
	res, err := performCheckinCall(sa, ci.CampaignID)
	if err != nil {
		out["error"] = "claim: " + err.Error()
		logCheckinFailure("claim", sa, err)
		return out
	}
	// claim 返回 status=CLAIMED + replayed=true 表示本轮已领过，归一为 already
	// 而非失败。
	if success, _ := res["success"].(bool); success {
		out["success"] = true
		if already, _ := res["already"].(bool); already {
			out["skipped"] = true
			out["reason"] = "already"
			out["message"] = "今日已签到"
		} else {
			out["message"] = "签到成功"
		}
		if rc, ok := res["rewardCredits"].(float64); ok {
			out["reward_credits"] = int64(rc)
			log.Printf("[qoderwork] checkin ok account=%s campaign=%s credit=+%d already=%v",
				checkinLogTag(sa), ci.CampaignID, int64(rc), out["reason"] == "already")
		} else {
			log.Printf("[qoderwork] checkin ok account=%s campaign=%s credit=unknown already=%v",
				checkinLogTag(sa), ci.CampaignID, out["reason"] == "already")
		}
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

// checkinNoActivityMessage 为「无可领取活动」区分两种成因并返回对应提示。
// [参数] ci: 签到摘要（读 ShowCampaign）。
// [返回] 中文提示文案。
// 最近修改时间：2026-10-17 02:10:00 新增，区分活动未下发与账号不适用。
func checkinNoActivityMessage(ci *checkinSummary) string {
	// 1. 活动平台开启但列表无 CLAIM_BENEFIT 条目 = 服务端未下发
	if ci != nil && ci.ShowCampaign {
		return "签到活动暂未下发到该账号"
	}
	return "该账号不适用签到活动"
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
