// credits_sweep.go 实现每小时一轮的全量积分扫描：串行刷新全部账号（不区分启用
// 与禁用），把结果写进 sidecar，再对每个账号跑一次三档分级检查。
//
// 为什么串行：26 个账号并发打 billing 接口会形成可识别的定时尖峰；串行把
// 一轮摊成 1~3 分钟的自然节奏，代价只是一轮跑得更久，而一小时的周期完全容得下。
// 为什么禁用账号也要刷：积分恢复是它们唯一的解禁途径，跳过禁用号等于让它们
// 永远停在「不知道还有没有钱」的状态。
//
// 最近修改时间：2026-10-23；改动原因：新增，积分从「被动按需拉取」改为「每小时
// 主动落盘」，供面板、调度与分级检查共用同一份真相。
package main

import (
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// creditsSweepInterval 是积分扫描周期。用户裁定为固定 1 小时：
	// 更短会把上游 billing 接口打成周期性压力，更长则积分过期太慢，
	// 低额账号在被发现之前可能已经连续失败。
	creditsSweepInterval = time.Hour

	// creditsSweepStartupWait 是首轮扫描前等待宿主就绪的上限。
	// init() 早于宿主装配，立即扫描会拿不到 auth 列表；与 watchdog 同口径。
	creditsSweepStartupWait = 15 * time.Second

	// creditsSweepReadyPoll 是等待宿主就绪时的轮询间隔。
	creditsSweepReadyPoll = 250 * time.Millisecond

	// creditsSweepPerAccountDelay 是账号之间的间隔。串行之外再加一点停顿，
	// 让上游看到的是一串稳定间隔的请求，而不是紧随其后的连发。
	creditsSweepPerAccountDelay = 300 * time.Millisecond
)

var (
	// creditsSweepMu 是单轮不重叠闸门：上一轮没跑完时本轮直接丢弃。
	creditsSweepMu sync.Mutex

	// 宿主读写与拉取注入点，供单测替换。
	creditsSweepListFn    = hostAuthList
	creditsSweepGetFn     = hostAuthGet
	creditsSweepFetchFn   = fetchUserResource
	creditsSweepPersistFn = persistCreditSnapshot
	creditsSweepGradeFn   = gradeCreditsForAuth
	creditsSweepSleepFn   = time.Sleep
	creditsSweepNowFn     = time.Now

	// creditsSweepLast 保存最近一轮的统计，供面板与排障读取。
	creditsSweepLastMu sync.RWMutex
	creditsSweepLast   *creditsSweepReport
)

// creditsSweepReport 是一轮扫描的结果摘要。
type creditsSweepReport struct {
	// StartedAt / FinishedAt 是这一轮的真实起止时刻。
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// Total 是本轮看到的账号数；Refreshed 是成功拉到积分的数量。
	Total     int `json:"total"`
	Refreshed int `json:"refreshed"`
	Failed    int `json:"failed"`
	// Disabled / Preserved 是本轮分级后落入各档的账号数。
	Disabled  int `json:"disabled"`
	Preserved int `json:"preserved"`
}

// runCreditSweep 执行一轮完整扫描：串行拉全部账号 → 落盘 → 逐个分级检查。
//
// 单账号失败只记日志、保留上一轮快照（stale-while-error）：上游抖动不该让
// 一个健康账号的积分变成「未知」，更不该因此把它降级或解禁。
// [参数] 无。
// [返回] 本轮统计；宿主列表不可用时返回 nil。
// 最近修改时间：2026-10-23；改动原因：新增。
func runCreditSweep() *creditsSweepReport {
	files, err := creditsSweepListFn()
	if err != nil {
		log.Printf("[workbuddy] credits sweep skipped: list auths: %v", err)
		return nil
	}
	report := &creditsSweepReport{StartedAt: creditsSweepNowFn()}
	report.Total = len(files)
	for i, f := range files {
		// 1. 账号之间留固定间隔，把上游压力摊平。
		if i > 0 && creditsSweepPerAccountDelay > 0 {
			creditsSweepSleepFn(creditsSweepPerAccountDelay)
		}
		authID := strings.TrimSpace(f.ID)
		if authID == "" {
			continue
		}
		// 2. 取凭据。禁用账号同样要取：积分恢复是它唯一的解禁途径。
		sa, errGet := creditsSweepGetFn(f.AuthIndex)
		if errGet != nil || sa == nil {
			report.Failed++
			continue
		}
		// 3. 拉积分。失败时保留上一轮快照，不写入「未知」覆盖真实数据。
		cr, errFetch := creditsSweepFetchFn(sa)
		if errFetch != nil || cr == nil {
			report.Failed++
			continue
		}
		report.Refreshed++
		recordCreditSnapshot(authID, cr, sa.Account.Nickname, accountRegion(sa))
		// 4. 分级检查：可能是禁用、解禁、或仅更新档位。
		switch creditsSweepGradeFn(f.AuthIndex, authID, sa, cr) {
		case creditTierDisabled:
			report.Disabled++
		case creditTierPreserve:
			report.Preserved++
		}
	}
	// 5. 整轮只在末尾落盘一次：中途崩溃时磁盘保留上一轮的完整快照。
	if err := creditsSweepPersistFn(); err != nil {
		log.Printf("[workbuddy] credits snapshot persist failed: %v", err)
	}
	report.FinishedAt = creditsSweepNowFn()
	log.Printf("[workbuddy] credits sweep done: total=%d refreshed=%d failed=%d disabled=%d preserved=%d in %s",
		report.Total, report.Refreshed, report.Failed, report.Disabled, report.Preserved,
		report.FinishedAt.Sub(report.StartedAt).Round(time.Second))
	recordCreditsSweep(report)
	return report
}

// recordCreditsSweep 保存最近一轮统计。
// [参数] r：本轮统计。
// [返回] 无。
// 最近修改时间：2026-10-23；改动原因：新增。
func recordCreditsSweep(r *creditsSweepReport) {
	creditsSweepLastMu.Lock()
	creditsSweepLast = r
	creditsSweepLastMu.Unlock()
}

// lastCreditsSweep 返回最近一轮统计，尚未跑过时返回 nil。
// [参数] 无。
// [返回] 最近一轮统计的副本。
// 最近修改时间：2026-10-23；改动原因：新增。
func lastCreditsSweep() *creditsSweepReport {
	// 1. 加读锁取出指针。
	creditsSweepLastMu.RLock()
	defer creditsSweepLastMu.RUnlock()
	if creditsSweepLast == nil {
		return nil
	}
	// 2. 返回副本，调用方改动不会污染内部状态。
	copied := *creditsSweepLast
	return &copied
}

// runCreditSweepNonOverlap 在不重叠的前提下执行一轮扫描。
//
// 上一轮还在跑时 TryLock 失败，本轮直接丢弃而不是排队：排队只会让同一批账号
// 被连续扫两遍，第二遍拿到的还是刚刚的分数。
// [参数] 无。
// [返回] true 表示本轮已启动。
// 最近修改时间：2026-10-23；改动原因：新增。
func runCreditSweepNonOverlap() bool {
	// 1. 抢扫描锁；上一轮未结束时本轮直接放弃。
	if !creditsSweepMu.TryLock() {
		return false
	}
	// 2. 起 goroutine 执行，扫描结束时释放锁。
	go func() {
		defer creditsSweepMu.Unlock()
		runCreditSweep()
	}()
	return true
}

// creditSweepLoop 是积分扫描的常驻循环。
//
// 首轮有随机抖动：六个插件同刻对齐会形成整点尖峰，抖动把首轮摊到数分钟内。
// 之后固定 1 小时一轮；单轮串行可能超过一小时（账号数 × 上游延迟），
// 期间后续 tick 会被 runCreditSweepNonOverlap 逐个跳过。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-22 17:40:00；改动原因：补实例守卫闸门，热重载后旧实例不再继续扫描。
func creditSweepLoop() {
	// 1. 等宿主就绪后再开始，避免插件加载期就刷上游。
	waitHostReadyForWatchdog(creditsSweepStartupWait, hostReadyForWatchdog)
	// 2. 首轮抖动用 watchdog 的同一套机制，避免新增一套随机源。
	creditsSweepSleepFn(watchdogFirstTickDelay())
	// 3. 首轮立即扫一次。被更新实例取代时在这里直接退出，不必先打一轮上游。
	if creditSweepTickOnce() {
		return
	}
	ticker := time.NewTicker(creditsSweepInterval)
	defer ticker.Stop()
	// 4. 之后按 1 小时周期推进，每轮先判实例再扫描。
	for range ticker.C {
		if creditSweepTickOnce() {
			return
		}
	}
}

// creditSweepTickOnce 执行一轮「先判实例、再启动扫描」的 tick，是循环唯一的
// 执行入口：首轮与后续 ticker 共用同一段逻辑，避免两处守卫写法漂移。
//
// 为什么每轮都要判：宿主热重载不调用 plugin_shutdown，旧实例的循环在
// 0.21.27 → 0.21.29 三代并存期间仍在每小时扫一遍全部账号（2026-11-22 生产
// 实证：每小时固定 3 轮），多代写者互相覆盖同一份快照。
// [参数] 无。
// [返回] true 表示本实例已被取代，调用方必须立即退出循环。
// 最近修改时间：2026-11-22 17:52:00；改动原因：新增，把实例守卫收敛成可测的统一入口。
func creditSweepTickOnce() bool {
	// 1. 先判实例：被取代后连扫描都不启动，避免旧实例争同一份快照。
	if retireIfSuperseded("credits sweep loop") {
		return true
	}
	// 2. 启动一轮扫描；上一轮未结束时由 TryLock 丢弃本轮。
	runCreditSweepNonOverlap()
	return false
}
