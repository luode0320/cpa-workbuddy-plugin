// checkin.go implements daily check-in for CN accounts: the manual
// handleManualCheckin endpoint, the every-4-hours auto scheduler, and the
// per-account mutex that prevents duplicate check-ins from racing browser
// tabs. Global accounts are excluded — they use one-shot trial claims instead.
package main

import (
	"encoding/json"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// hostAuthListFn / hostAuthGetFn 是账号枚举与凭据读取的包级测试接缝（沿用
// workbuddy-ai 同名模式）：生产指向真实 host RPC，单测替换为内存桩，避免为
// 可测性把 hostAuthList / hostAuthGet 本身改成变量而影响全部调用方。
// 签到链路与手动刷新链路共用：两者都建立在「先取凭据、再打上游」之上。
var hostAuthListFn = hostAuthList
var hostAuthGetFn = hostAuthGet

// hostAuthGetPhysicalFn 是凭据物理文件读取（host.auth.get）的包级测试接缝。
// zip 导出需要未解析的原始 JSON 字节（保留 nested/flat 形态与未知字段），
// 与返回 *storedAuth 的 hostAuthGetFn 不同，故单独设接缝。
var hostAuthGetPhysicalFn = hostAuthGetPhysical

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
//
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-24；改动原因：成长中心重新接入，tick 条件纳入 growth。
func runAutoCheckin() {
	checkinAutoMu.RLock()
	doCheckin := checkinAuto
	checkinAutoMu.RUnlock()
	doGrowth := growthEnabled()
	// Lifecycle may still run when check-in is off (credit gate).
	if !doCheckin && !lifecycleEnabled() && !doGrowth {
		return
	}
	files, err := hostAuthListFn()
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
			processAutoCheckinAccount(f, doCheckin, doGrowth)
		}()
	}
	wg.Wait()
	// 成长中心的账号级锁表需要随账号删除收缩，避免 sync.Map 无界增长
	pruneGrowthLocks()
}

// processAutoCheckinAccount handles one account's scheduled tick. Extracted so
// runAutoCheckin can fan out per-account work without duplicating logic.
//
// [参数] f：账号条目；doCheckin：本轮是否执行签到；doGrowth：本轮是否执行成长中心。
// [返回] 无。
// 最近修改时间：2026-10-24；改动原因：成长中心重新接入，增加独立 growth 分支。
func processAutoCheckinAccount(f pluginapi.HostAuthFileEntry, doCheckin, doGrowth bool) {
	// A-24: only fetch sa when needed (checkin or growth). For lifecycle-only
	// paths, let reconcileOneAccount do the single hostAuthGetBundle internally.
	if doCheckin || doGrowth {
		sa, err := hostAuthGetFn(f.AuthIndex)
		if err != nil {
			return
		}
		if isGlobalDomain(sa.Auth.Domain) {
			// Global: never check-in, auto-claim trial. Lifecycle only.
			// Invalidate cache (copy entry, set credits=nil, keep plan/checkin).
			if v, ok := accountCache.Load(f.ID); ok {
				if e, ok2 := v.(*accountCacheEntry); ok2 {
					fresh := *e
					fresh.credits = nil
					fresh.fetched = time.Now()
					accountCache.Store(f.ID, &fresh)
				}
			}
			if lifecycleEnabled() {
				_, _ = reconcileOneAccount(f.AuthIndex, f.ID, true)
			}
			return
		}
		// CN: 成长中心先执行——它可能发放积分，随后的签到缓存刷新能带上新余额。
		if doGrowth {
			runScheduledGrowth(sa, f)
		}
		if !doCheckin {
			if lifecycleEnabled() {
				_, _ = reconcileOneAccount(f.AuthIndex, f.ID, true)
			}
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
					// 签到会改变余额，因此必须立即重跑三档分级：新积分可能
					// 让一个被本机制禁用的号达到解禁条件，也可能让档位从
					// 保活升回正常。等待下一轮小时扫描会让这段窗口白等一小时。
					recordCreditSnapshot(f.ID, cr2, sa.Account.Nickname, accountRegion(sa))
					gradeCreditsForAuth(f.AuthIndex, f.ID, sa, cr2)
					if persistErr := persistCreditSnapshot(); persistErr != nil {
						log.Printf("[workbuddy] credits snapshot persist failed: %v", persistErr)
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

// handleManualCheckin serves POST /checkin.
//
// Single-account mode (body.auth_index set): runs checkinOneAccount directly —
// one hostAuthGet + at most two upstream calls (status, then check-in). The
// old three-phase classify/execute/summarize pipeline ran the same framework
// for one account as for thirty (4 RPC + 5 HTTP + lock-held re-reads), which
// routinely blew past the 30s management API timeout: the check-in succeeded
// upstream but the response never reached the panel.
//
// Batch mode (empty auth_index): fans out checkinOneAccount per account
// (sem=4), preserving input order and the {results, summary} response shape
// the panel depends on.
func handleManualCheckin(req pluginapi.ManagementRequest) map[string]any {
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

	results := make([]map[string]any, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, f := range targets {
		wg.Add(1)
		go func(i int, f pluginapi.HostAuthFileEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = checkinOneAccount(f)
		}(i, f)
	}
	wg.Wait()

	// Summary counters (field names are a panel contract — panel.html
	// checkinAll reads success/already/fail/skipped_global/eligible).
	successN, failN, alreadyN, globalN, eligibleN := 0, 0, 0, 0, 0
	for _, out := range results {
		if out == nil {
			continue
		}
		if out["error"] != nil {
			failN++
			continue
		}
		reason, _ := out["reason"].(string)
		switch reason {
		case "already":
			alreadyN++
			continue
		case "global":
			globalN++
			continue
		}
		eligibleN++
		if out["success"] == true {
			successN++
		} else {
			failN++
		}
	}
	return map[string]any{
		"results": results,
		"summary": map[string]any{
			"total":          len(targets),
			"eligible":       eligibleN,
			"success":        successN,
			"already":        alreadyN,
			"skipped_global": globalN,
			"fail":           failN,
			"attempted":      eligibleN,
		},
	}
}

// checkinOneAccount performs the daily check-in for one account with the
// minimum number of round-trips:
//
//	hostAuthGet ×1 (RPC) → domain gate → fetchCheckinStatus ×1 (failure
//	tolerated, upstream is idempotent) → performCheckinCall ×1.
//
// No lock-held re-reads, no lifecycle reconcile (that is the scheduled
// runAutoCheckin path's job), no redundant status refetches. Cache updates
// merge into the previous entry so credits/plan survive.
func checkinOneAccount(f pluginapi.HostAuthFileEntry) map[string]any {
	out := map[string]any{"auth_index": f.AuthIndex}

	sa, err := hostAuthGet(f.AuthIndex)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["nickname"] = sa.Account.Nickname

	if isGlobalDomain(sa.Auth.Domain) {
		out["success"] = false
		out["skipped"] = true
		out["reason"] = "global"
		out["message"] = "国际版账号不支持签到，请使用领取专家加油包"
		return out
	}

	mu := checkinLockFor(f.AuthIndex)
	mu.Lock()
	defer mu.Unlock()

	// Status probe: a failure here is NOT fatal — the check-in call below is
	// idempotent upstream and its business message tells us "already" anyway.
	ci, ciErr := fetchCheckinStatus(sa)
	if ciErr == nil && ci != nil && ci.TodayCheckedIn {
		mergeCheckinCache(f.ID, ci)
		out["success"] = true
		out["skipped"] = true
		out["reason"] = "already"
		out["message"] = "already checked in today"
		return out
	}

	res, err := performCheckinCall(sa)
	if err != nil {
		out["error"] = err.Error()
		out["success"] = false
		return out
	}
	for k, v := range res {
		out[k] = v
	}
	// Business soft-fail ("already checked in" family) → done, not failure.
	if msg, _ := out["message"].(string); msg != "" && out["success"] == false {
		low := strings.ToLower(msg)
		if strings.Contains(low, "already") || strings.Contains(msg, "已签") || strings.Contains(msg, "今日") {
			out["success"] = true
			out["skipped"] = true
			out["reason"] = "already"
		}
	}
	if _, ok := out["success"]; !ok {
		out["success"] = true
	}

	// Post-call cache refresh: one extra status fetch at most; on failure
	// write a TodayCheckedIn placeholder rather than leaving the panel stale.
	if ci2, err2 := fetchCheckinStatus(sa); err2 == nil && ci2 != nil {
		mergeCheckinCache(f.ID, ci2)
	} else {
		mergeCheckinCache(f.ID, &checkinSummary{TodayCheckedIn: true})
	}
	return out
}

// mergeCheckinCache stores the latest check-in snapshot while preserving the
// credits/plan fields of any previous cache entry (merge, not replace).
func mergeCheckinCache(authID string, ci *checkinSummary) {
	var prev *accountCacheEntry
	if v, ok := accountCache.Load(authID); ok {
		prev, _ = v.(*accountCacheEntry)
	}
	entry := &accountCacheEntry{checkin: ci, fetched: time.Now()}
	if prev != nil {
		entry.credits = prev.credits
		entry.plan = prev.plan
	}
	accountCache.Store(authID, entry)
}

func checkinLockFor(authIndex string) *sync.Mutex {
	v, _ := checkinLocks.LoadOrStore(authIndex, &sync.Mutex{})
	return v.(*sync.Mutex)
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
