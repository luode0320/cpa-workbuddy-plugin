package main

import (
	"sync"
	"time"
)

// 本文件实现 1 小时一轮的全量积分巡检。
//
// 为什么必须有它：执行入口的积分闸门只读缓存（见 policy.go），缓存不更新
// 闸门就是瞎的——账号可能早就耗尽，闸门却仍以为有 500 分。面板的按需查询
// 不足以为此负责：没有用户打开面板时它根本不会跑。
//
// 为什么合成一轮而不是保留 5 分钟刷新 + 1 小时扫描两套循环：
// 签到是幂等的，5 分钟一次的签到触发毫无收益，却让上游压力翻倍，
// 还会产生「谁负责领签到」的歧义。一轮里按固定顺序做完四件事最省。
//
// 为什么禁用账号也要扫：积分回升是它们唯一的解禁途径，跳过禁用号
// 等于让它们永远停在「不知道还有没有钱」的状态。

const (
	// creditSweepStartupWait 是首轮巡检前等待宿主就绪的上限。
	// init() 早于宿主装配，立即扫描会拿不到 auth 列表。
	creditSweepStartupWait = 15 * time.Second

	// creditSweepReadyPoll 是等待宿主就绪时的轮询间隔。
	creditSweepReadyPoll = 250 * time.Millisecond

	// creditSweepFirstTickMaxDelay 是首轮抖动的上限。
	//
	// 多个插件同刻对齐会形成整点上游尖峰；把首轮摊到几分钟内，
	// 各插件就不会在同一秒打满上游。
	creditSweepFirstTickMaxDelay = 3 * time.Minute
)

var (
	// creditSweepMu 是单轮不重叠闸门：上一轮没跑完时本轮直接丢弃。
	//
	// 丢弃而不是排队：排队只会让同一批账号被连续扫两遍，
	// 第二遍拿到的还是刚刚的分数。
	creditSweepMu sync.Mutex

	// creditSweepRunning 标记当前是否有一轮在跑，供面板显示「扫描中」。
	creditSweepRunningMu sync.RWMutex
	creditSweepRunning   bool

	// creditSweepLast 保存最近一轮的统计，供面板与排障读取。
	creditSweepLastMu sync.RWMutex
	creditSweepLast   *creditSweepReport

	// 宿主读写与拉取注入点，供单测替换。
	creditSweepListFn    = listAllAuthFiles
	creditSweepFetchFn   = fetchCreditSummary
	creditSweepClaimFn   = claimDailyCreditForAccount
	creditSweepConfirmFn = confirmDailyCreditBySummary
	creditSweepStoreFn   = storeCreditSummary
	creditSweepRecordFn  = recordCreditSnapshot
	creditSweepGradeFn   = gradeCreditsForAccount
	creditSweepPersistFn = persistCreditSnapshot
	creditSweepPublishFn = publishCreditPool
	creditSweepLogf      = hostLogf
	creditSweepSleepFn   = time.Sleep
	creditSweepNowFn     = time.Now
)

// creditSweepDeps 是一轮巡检的可注入依赖。
//
// 抽出来只为让单测在不碰宿主、不发真实请求的前提下覆盖顺序、隔离与分级。
// 生产入口 runCreditSweep 使用包级注入点的默认值，测试用 runCreditSweepWith
// 传入自己的依赖，两者共用同一段执行体，不存在第二套逻辑。
type creditSweepDeps struct {
	list    func() ([]*StoredAuth, error)
	claim   func(*StoredAuth) bool
	fetch   func(*StoredAuth) (*creditSummary, error)
	confirm func(*StoredAuth, *creditSummary)
	store   func(*StoredAuth, *creditSummary)
	record  func(string, *creditSummary, string)
	grade   func(*StoredAuth, *creditSummary) creditTier
	persist func() error
	publish func([]*StoredAuth)
	logf    func(string, string, map[string]any)
	now     func() time.Time
}

// defaultCreditSweepDeps 返回生产依赖。
// [参数] 无。
// [返回] 依赖集合。
// 最近修改时间：2026-10-25；改动原因：新增，生产装配。
func defaultCreditSweepDeps() creditSweepDeps {
	return creditSweepDeps{
		list:    creditSweepListFn,
		claim:   creditSweepClaimFn,
		fetch:   creditSweepFetchFn,
		confirm: creditSweepConfirmFn,
		store:   creditSweepStoreFn,
		record:  creditSweepRecordFn,
		grade:   creditSweepGradeFn,
		persist: creditSweepPersistFn,
		publish: creditSweepPublishFn,
		logf:    creditSweepLogf,
		now:     creditSweepNowFn,
	}
}

// creditSweepReport 是一轮巡检的结果摘要。
type creditSweepReport struct {
	// StartedAt / FinishedAt 是这一轮的真实起止时刻（RFC3339）。
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	// Total 是本轮看到的账号数；Refreshed 是成功拉到积分的数量。
	Total     int `json:"total"`
	Refreshed int `json:"refreshed"`
	Failed    int `json:"failed"`
	// Disabled / ReEnabled 是本轮分级后新禁用与新解禁的账号数。
	Disabled  int `json:"disabled"`
	ReEnabled int `json:"re_enabled"`
	// Redline 是本轮生效的阈值，让面板能解释「为什么这些号被禁了」。
	Redline int64 `json:"redline"`
}

// runCreditSweep 执行一轮完整巡检：并发拉全部账号 → 落盘 → 逐个分级。
// [参数] 无。
// [返回] 本轮统计；账号列表不可用时返回 nil。
// 最近修改时间：2026-10-25；改动原因：新增，替代原 5 分钟刷新循环。
func runCreditSweep() *creditSweepReport {
	return runCreditSweepWith(defaultCreditSweepDeps())
}

// runCreditSweepWith 是巡检的执行体，依赖全部可注入。
//
// 单账号失败只记日志、保留上一轮快照（stale-while-error）：上游抖动不该让
// 一个健康账号的积分变成「未知」，更不该因此把它禁用或解禁。
// [参数] deps：可注入依赖。
// [返回] 本轮统计；账号列表不可用时返回 nil。
// 最近修改时间：2026-10-25；改动原因：新增，执行体与生产入口分离以便测试。
func runCreditSweepWith(deps creditSweepDeps) *creditSweepReport {
	accounts, errList := deps.list()
	if errList != nil {
		deps.logf("warn", "积分巡检读取账号失败", map[string]any{"error": errList.Error()})
		return nil
	}
	report := &creditSweepReport{
		StartedAt: deps.now().Format(time.RFC3339),
		Total:     len(accounts),
		Redline:   loadedMinRoutableCredits(),
	}
	if len(accounts) == 0 {
		report.FinishedAt = deps.now().Format(time.RFC3339)
		recordCreditSweep(report)
		return report
	}

	// 1. 并发上限与账号数取小；账号少时不必要地开满 worker 只是浪费。
	workers := creditRefreshConcurrency
	if workers > len(accounts) {
		workers = len(accounts)
	}
	queue := make(chan *StoredAuth)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sa := range queue {
				outcome := sweepOneAccount(deps, sa)
				mu.Lock()
				switch outcome {
				case sweepOutcomeFailed:
					report.Failed++
				case sweepOutcomeRefreshed:
					report.Refreshed++
				case sweepOutcomeDisabled:
					report.Refreshed++
					report.Disabled++
				case sweepOutcomeReEnabled:
					report.Refreshed++
					report.ReEnabled++
				}
				mu.Unlock()
			}
		}()
	}

	// 2. 入队时不按 disabled 过滤：禁用号同样要签到与查余额（解禁唯一途径）。
	for _, sa := range accounts {
		if sa == nil {
			continue
		}
		queue <- sa
	}
	close(queue)
	wg.Wait()

	// 3. 整轮只在末尾落盘一次：中途崩溃时磁盘保留上一轮的完整快照。
	if errPersist := deps.persist(); errPersist != nil {
		deps.logf("warn", "积分快照落盘失败", map[string]any{"error": errPersist.Error()})
	}
	// 4. 刷新完成后再发布闸门快照：先发布会让闸门读到上一轮的余额。
	if deps.publish != nil {
		deps.publish(accounts)
	}

	report.FinishedAt = deps.now().Format(time.RFC3339)
	recordCreditSweep(report)
	deps.logf("info", "积分巡检完成", map[string]any{
		"total":      report.Total,
		"refreshed":  report.Refreshed,
		"failed":     report.Failed,
		"disabled":   report.Disabled,
		"re_enabled": report.ReEnabled,
		"redline":    report.Redline,
	})
	return report
}

// sweepOutcome 是单账号在本轮巡检中的结局。
type sweepOutcome int

const (
	// sweepOutcomeFailed 表示积分拉取失败，本轮不做任何处置。
	sweepOutcomeFailed sweepOutcome = iota
	// sweepOutcomeRefreshed 表示积分已更新，档位无变化。
	sweepOutcomeRefreshed
	// sweepOutcomeDisabled 表示本轮新写入禁用。
	sweepOutcomeDisabled
	// sweepOutcomeReEnabled 表示本轮解除了本机制写入的禁用。
	sweepOutcomeReEnabled
)

// sweepOneAccount 处理单个账号：领签到 → 查积分 → 写缓存与快照 → 分级。
//
// 顺序不可调换：签到必须先于余额查询。反了会让刚领到 1500 分的账号
// 被当日旧余额判成 0 分，进而写盘禁用，而它下一轮才会解禁——
// 账号凭空下线一小时（见 daily_credit.go 的领取契约）。
// [参数] deps：可注入依赖；sa：账号。
// [返回] 本账号的结局。
// 最近修改时间：2026-10-25；改动原因：新增，单账号处理内核。
func sweepOneAccount(deps creditSweepDeps, sa *StoredAuth) sweepOutcome {
	if sa == nil {
		return sweepOutcomeFailed
	}
	// 1. 先领当日登录积分：领取会改变余额，顺序反了本轮拿到的是旧值。
	if deps.claim != nil {
		deps.claim(sa)
	}
	// 2. 查余额与明细。
	summary, errFetch := deps.fetch(sa)
	if errFetch != nil || summary == nil {
		// 3. 失败保留上一轮快照：不写「未知」覆盖真实数据，也不做分级。
		fields := map[string]any{"auth_id": sa.AuthID}
		if errFetch != nil {
			fields["error"] = errFetch.Error()
		}
		deps.logf("warn", "积分巡检单号失败", fields)
		return sweepOutcomeFailed
	}
	// 4. 到账事实只能从明细确认：check 的响应不携带发放结果。
	if deps.confirm != nil {
		deps.confirm(sa, summary)
	}
	// 5. 写内存缓存与快照镜像（落盘由轮末统一完成）。
	if deps.store != nil {
		deps.store(sa, summary)
	}
	if deps.record != nil {
		deps.record(sa.AuthID, summary, sa.ScreenName)
	}

	// 6. 分级：可能是新禁用、新解禁，或仅刷新档位。
	if deps.grade == nil {
		return sweepOutcomeRefreshed
	}
	wasDisabled := sa.Disabled
	tier := deps.grade(sa, summary)
	switch {
	case tier == creditTierDisabled && !wasDisabled && sa.Disabled:
		return sweepOutcomeDisabled
	case tier == creditTierNormal && wasDisabled && !sa.Disabled:
		return sweepOutcomeReEnabled
	}
	return sweepOutcomeRefreshed
}

// recordCreditSweep 保存最近一轮统计。
// [参数] r：本轮统计。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，面板读取最近一轮。
func recordCreditSweep(r *creditSweepReport) {
	creditSweepLastMu.Lock()
	creditSweepLast = r
	creditSweepLastMu.Unlock()
}

// lastCreditSweep 返回最近一轮统计，尚未跑过时返回 nil。
// [参数] 无。
// [返回] 最近一轮统计的副本。
// 最近修改时间：2026-10-25；改动原因：新增，避免调用方持有内部指针。
func lastCreditSweep() *creditSweepReport {
	creditSweepLastMu.RLock()
	defer creditSweepLastMu.RUnlock()
	if creditSweepLast == nil {
		return nil
	}
	copied := *creditSweepLast
	return &copied
}

// creditSweepIsRunning 返回当前是否有一轮巡检在跑。
// [参数] 无。
// [返回] 运行中时为 true。
// 最近修改时间：2026-10-25；改动原因：新增，面板「扫描中」状态。
func creditSweepIsRunning() bool {
	creditSweepRunningMu.RLock()
	defer creditSweepRunningMu.RUnlock()
	return creditSweepRunning
}

// setCreditSweepRunning 更新巡检运行标记。
// [参数] running：目标状态。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func setCreditSweepRunning(running bool) {
	creditSweepRunningMu.Lock()
	creditSweepRunning = running
	creditSweepRunningMu.Unlock()
}

// runCreditSweepNonOverlap 在不重叠的前提下异步执行一轮巡检。
// [参数] 无。
// [返回] true 表示本轮已启动；false 表示上一轮未结束、本轮被丢弃。
// 最近修改时间：2026-10-25；改动原因：新增，定时器与手动触发共用入口。
func runCreditSweepNonOverlap() bool {
	// 1. 抢扫描锁；上一轮未结束时本轮直接放弃。
	if !creditSweepMu.TryLock() {
		return false
	}
	// 2. 起 goroutine 执行，扫描结束时释放锁并清运行标记。
	go func() {
		defer creditSweepMu.Unlock()
		setCreditSweepRunning(true)
		defer setCreditSweepRunning(false)
		runCreditSweep()
	}()
	return true
}

// creditSweepLoop 是积分巡检的常驻循环。
//
// 首轮有随机抖动：多个插件同刻对齐会形成整点尖峰。
// 之后按配置周期（默认 1 小时）推进；单轮耗时超过一个周期时，
// 后续 tick 会被 runCreditSweepNonOverlap 逐个跳过。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，替代 creditLoop。
func creditSweepLoop() {
	// 1. 等宿主就绪后再开始，避免插件加载期就刷上游。
	waitHostReadyForRenew(creditSweepStartupWait, hostReadyForRenew)
	// 2. 首轮抖动，把各插件的扫描错开。
	creditSweepSleepFn(sweepFirstTickDelay())
	// 3. 首轮立即扫一次，之后按配置周期推进。
	runCreditSweepNonOverlap()
	for {
		timer := time.NewTimer(loadedCreditSweepInterval())
		select {
		case <-creditStopCh:
			timer.Stop()
			return
		case <-creditTickCh:
			// reconfigure 改了周期：重排而不立即重扫，避免配置热更新
			// 意外触发一轮全量上游请求。
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		select {
		case <-creditStopCh:
			return
		default:
		}
		runCreditSweepNonOverlap()
	}
}

// sweepFirstTickDelay 返回首轮抖动时长。
//
// 用固定比例而不是随机数：确定性抖动已足以错开同刻启动的插件，
// 还省掉一个需要测试的随机源。
// [参数] 无。
// [返回] 抖动时长。
// 最近修改时间：2026-10-25；改动原因：新增。
func sweepFirstTickDelay() time.Duration {
	if creditSweepFirstTickMaxDelay <= 0 {
		return 0
	}
	return creditSweepFirstTickMaxDelay / 3
}

// loadedCreditSweepInterval 返回当前生效的巡检间隔。
// [参数] 无。
// [返回] 巡检间隔；配置非法时回退默认值。
// 最近修改时间：2026-10-25；改动原因：新增，配置热更新后立即生效。
func loadedCreditSweepInterval() time.Duration {
	seconds := currentConfig().CreditRefreshSeconds
	if seconds <= 0 {
		seconds = creditRefreshDefaultSeconds
	}
	return time.Duration(seconds) * time.Second
}

// requestCreditTick 请求立即重排巡检定时器（非阻塞）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，配置热更新触发重排。
func requestCreditTick() {
	select {
	case creditTickCh <- struct{}{}:
	default:
	}
}

// startCreditSweep 启动巡检循环（只生效一次）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，宿主 init 时装配。
func startCreditSweep() {
	creditStartOnce.Do(func() {
		go creditSweepLoop()
	})
}

// stopCreditSweep 通知巡检循环退出。
//
// 与续期循环同口径：只关闭 channel 不等待 goroutine，
// 宿主热重载会立即卸载动态库，阻塞等待会拖住卸载流程。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，热重载时停止循环。
func stopCreditSweep() {
	creditStopOnce.Do(func() { close(creditStopCh) })
}
