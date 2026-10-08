// checkin.go 承载 WorkBuddy AI 国际版每日签到域：手动签到入口
// handleManualCheckin、每 4 小时自动签到 runAutoCheckin、单账号签到
// checkinOneAccount 与签到缓存合并 mergeCheckinCache。国际版全部为
// global 账号，因此不存在国内版的 CN/Global 分流逻辑。
package main

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// hostAuthListFn / hostAuthGetFn 是签到链路的包级测试接缝（沿用 traework
// hostHTTPDoFn / refreshOneAuthFn 模式）：生产指向真实 host RPC，单测替换
// 为内存桩，避免为可测性把 hostCall 本身改成变量。
var hostAuthListFn = hostAuthList
var hostAuthGetFn = hostAuthGet

// checkinSummary 是签到状态快照，同时作为面板 JSON 契约与缓存载荷。
// 字段兼容上游 snake_case 与 camelCase 两种形态（解析见
// fetchCheckinStatus）。
type checkinSummary struct {
	Active          bool     `json:"active"`
	TodayCheckedIn  bool     `json:"today_checked_in"`
	StreakDays      int64    `json:"streak_days"`
	DailyCredit     int64    `json:"daily_credit"`
	TodayCredit     int64    `json:"today_credit"`
	TotalCredits    int64    `json:"total_credits"`
	WeekCheckinDays int64    `json:"week_checkin_days"`
	ActivityName    string   `json:"activity_name"`
	Season          int64    `json:"season"`
	CheckinDates    []string `json:"checkin_dates,omitempty"`
}

// nextCheckinTime 计算下一次签到或保活调度触发时刻，取两个时段表中最早
// 且尚未到达的时间点（当日已过时段顺延到次日）。
// [参数] now：当前本地时间。
// [返回] 最近一次调度触发时间。
// 最近修改时间：2026-10-09；改动原因：签到与保活调度合并。
func nextCheckinTime(now time.Time) time.Time {
	var earliest time.Time
	// 合并签到与保活时段，确保定时器在最早触发的调度处醒来。
	hours := append([]int{}, checkinHours...)
	hours = append(hours, keepaliveHours...)
	for _, h := range hours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			// 当日时段已过，顺延到次日。
			t = t.Add(24 * time.Hour)
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// runAutoCheckin 是定时生命周期 tick（每 4 小时：00:00 / 04:00 / 08:00 /
// 12:00 / 16:00 / 20:00）。每账号任务并发执行（sem=4），避免 N 个账号
// 串行产生 3N 次上游 HTTP 往返；与 buildDashboardEx、handleManualCheckin
// 的并发模式一致。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-09；改动原因：国际版签到链路新建。
func runAutoCheckin() {
	checkinAutoMu.RLock()
	doCheckin := checkinAuto
	checkinAutoMu.RUnlock()
	// 签到关闭时仍可能因额度门禁需要跑生命周期巡检。
	if !doCheckin && !lifecycleEnabled() {
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
			processAutoCheckinAccount(f, doCheckin)
		}()
	}
	wg.Wait()
}

// processAutoCheckinAccount 处理单个账号的定时 tick，从 runAutoCheckin
// 拆出以便并发扇出而不重复逻辑。
// [参数] f：host.auth.list 条目；doCheckin：签到开关快照。
// [返回] 无（结果写入 accountCache，供面板与巡检读取）。
// 最近修改时间：2026-10-09；改动原因：国际版签到链路新建，删除国内版
// 的 Global 跳过分支。
func processAutoCheckinAccount(f pluginapi.HostAuthFileEntry, doCheckin bool) {
	// 仅在需要签到时读取账号快照；纯生命周期路径交给
	// reconcileOneAccount 内部的 hostAuthGetBundle 单次读取。
	if doCheckin {
		sa, err := hostAuthGetFn(f.AuthIndex)
		if err != nil {
			return
		}
		ci, err := fetchCheckinStatus(sa)
		if err == nil && ci != nil && ci.Active && !ci.TodayCheckedIn {
			if _, callErr := performCheckinCall(sa); callErr == nil {
				// 领取成功后刷新一次状态，让缓存反映调用后的真实状态；
				// 刷新失败则保留调用前快照，避免并发读窗口读到空值。
				if ci2, _ := fetchCheckinStatus(sa); ci2 != nil {
					ci = ci2
				}
				// 签到会新增积分包：立即刷新积分缓存，避免面板等到下一
				// 次异步巡检才显示新余额。
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
		// 用最新签到状态合并缓存（不覆盖 credits/plan）。
		if ci != nil {
			mergeCheckinCache(f.ID, ci)
		}
		if lifecycleEnabled() {
			_, _ = reconcileOneAccount(f.AuthIndex, f.ID, true)
		}
		return
	}
	// 仅生命周期路径：reconcile 自带账号读取。
	if lifecycleEnabled() {
		_, _ = reconcileOneAccount(f.AuthIndex, f.ID, true)
	}
}

// handleManualCheckin 服务 POST /checkin。
//
// 单账号模式（body.auth_index 非空）直接跑 checkinOneAccount：一次
// hostAuthGet + 最多两次上游调用（状态、领取），避免历史三段式管线在
// 管理 API 30s 超时内跑不完的问题。
//
// 批量模式（auth_index 为空）对每账号扇出 checkinOneAccount（sem=4），
// 保持输入顺序与面板依赖的 {results, summary} 响应形态。
// [参数] req：管理请求（body 可带 auth_index）。
// [返回] {results, summary} 或 {error}。
// 最近修改时间：2026-10-09；改动原因：国际版签到链路新建，summary 去掉
// 国内版的 skipped_global 计数。
func handleManualCheckin(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	single := authIndex != ""

	files, err := hostAuthListFn()
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

	// summary 字段名是面板契约：panel.html 的 checkinAll 读取
	// success/already/fail/eligible。
	successN, failN, alreadyN, eligibleN := 0, 0, 0, 0
	for _, out := range results {
		if out == nil {
			continue
		}
		if out["error"] != nil {
			failN++
			continue
		}
		reason, _ := out["reason"].(string)
		if reason == "already" {
			alreadyN++
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
			"total":     len(targets),
			"eligible":  eligibleN,
			"success":   successN,
			"already":   alreadyN,
			"fail":      failN,
			"attempted": eligibleN,
		},
	}
}

// checkinOneAccount 以最少往返完成单账号签到：
// hostAuthGet ×1（RPC）→ fetchCheckinStatus ×1（失败可容忍，上游幂等）→
// performCheckinCall ×1。不加锁内二次读取、不跑生命周期巡检（那是定时
// runAutoCheckin 的职责）；缓存更新按合并语义保留 credits/plan。
// [参数] f：host.auth.list 条目。
// [返回] 面板结果 map（auth_index/nickname/success/skipped/reason/
// message/credit/streak_days 等）。
// 最近修改时间：2026-10-09；改动原因：国际版签到链路新建，删除国内版
// Global 跳过分支，业务错误码映射见 checkinErrorResult。
func checkinOneAccount(f pluginapi.HostAuthFileEntry) map[string]any {
	out := map[string]any{"auth_index": f.AuthIndex}

	sa, err := hostAuthGetFn(f.AuthIndex)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["nickname"] = sa.Account.Nickname

	mu := checkinLockFor(f.AuthIndex)
	mu.Lock()
	defer mu.Unlock()

	// 状态探测失败不算致命：下面的领取调用在上游幂等，其业务消息本身
	// 会告知 already 状态。
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
	// 业务软失败（already checked in 家族）视为完成而非失败；
	// 显式错误码（1001/1002/1003）已由 checkinErrorResult 映射。
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

	// 调用后缓存刷新：最多补一次状态查询；查询失败时写入
	// TodayCheckedIn 占位，避免面板停留在旧状态。
	if ci2, err2 := fetchCheckinStatus(sa); err2 == nil && ci2 != nil {
		mergeCheckinCache(f.ID, ci2)
	} else {
		mergeCheckinCache(f.ID, &checkinSummary{TodayCheckedIn: true})
	}
	return out
}

// mergeCheckinCache 写入最新签到快照，同时保留既有缓存条目的
// credits/plan 字段（合并而非替换）。
// [参数] authID：auth.ID 缓存键；ci：最新签到快照（调用方保证非 nil）。
// [返回] 无。
// 最近修改时间：2026-10-09；改动原因：国际版签到链路新建。
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
