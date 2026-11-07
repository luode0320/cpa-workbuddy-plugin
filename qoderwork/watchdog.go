// watchdog.go runs the periodic account watchdog — every interval (default
// 10 minutes) it refreshes the credits snapshot for every qoderwork account.
//
// 保号 (preserve) 池已于同步中移除：低积分不再把账号摘出路由。自动定时探活
// 亦已于 2026-10-12 移除（上游对同账号突发调用限流），本循环因此只剩「刷新积分」
// 一项职责，不再翻转任何账号状态、也不再发起任何推理请求。
//
// 驱动源保留：loadCountersFromDisk、purgeLegacyAnomalyFlags 与 flushCounters
// 依然按 watchdog cadence（默认 10m）执行，保证计数落盘与遗留标签清扫不失效。
package main

import (
	"time"
)

// Defaults for the account watchdog.
const (
	watchdogIntervalDefault time.Duration = 10 * time.Minute
	// Max wait for host to wire up before the first tick fires.
	watchdogStartupWait = 15 * time.Second
	// Host-readiness poll interval during the startup wait.
	watchdogReadyPoll = 250 * time.Millisecond
)

// watchdogTickCh queues an asynchronous tick request.
var watchdogTickCh = make(chan struct{}, 1)

// requestWatchdogTick asks the watchdog loop to run one tick as soon as the
// current sleep wakes up.
func requestWatchdogTick() {
	select {
	case watchdogTickCh <- struct{}{}:
	default:
	}
}

// requestPreserveTick 是 requestWatchdogTick 的兼容别名。
func requestPreserveTick() {
	requestWatchdogTick()
}

// hostReadyForWatchdog reports whether the host plugin-call table AND auth
// discovery are both alive.
func hostReadyForWatchdog() bool {
	if !hostBridgeAvailable() {
		return false
	}
	_, err := hostAuthList()
	return err == nil
}

// waitHostReadyForWatchdog polls ready() until it returns true or the
// deadline passes.
func waitHostReadyForWatchdog(maxWait time.Duration, ready func() bool) bool {
	if maxWait <= 0 {
		return ready()
	}
	deadline := time.Now().Add(maxWait)
	for {
		if ready() {
			return true
		}
		now := time.Now()
		if now.After(deadline) {
			return ready()
		}
		remaining := time.Until(deadline)
		if remaining > watchdogReadyPoll {
			remaining = watchdogReadyPoll
		}
		if remaining > 0 {
			time.Sleep(remaining)
		}
	}
}

// runWatchdogTick walks every qoderwork auth and hands the whole fleet to the
// throttled concurrent refresh runner. Rebuilds the daily cooling mirror on
// the same cadence.
func runWatchdogTick() {
	refreshDailyCoolingSetFromDisk()
	files, err := hostAuthList()
	if err != nil {
		return
	}
	targets := make([]refreshTarget, 0, len(files))
	for _, f := range files {
		targets = append(targets, refreshTarget{AuthIndex: f.AuthIndex, AuthID: f.ID})
	}
	if len(targets) > 0 {
		globalRefresh.EnqueueAll(targets, "watchdog")
	}
}

// watchdogFirstTickDelay 返回启动首轮全量刷新前的抖动延迟。
//
// 六个插件都在 init() 启动 watchdog，并在宿主就绪后立刻跑首轮；就绪检查
// 是 250ms 一轮的轮询，会把各插件的首轮吸附到同一窗口，叠加成数倍于单
// 插件上限的瞬时并发。首轮错开后，后续每轮从各自上一轮的结束时刻起算，
// 相位不会重新对齐，因此只需抖动首轮。
//
// [参数] 无。
// [返回] [0, schedulerJitterMax) 内的随机延迟。
// 最近修改时间：2026-10-19；改动原因：新增，消除多插件同刻首轮刷新的并发尖峰。
func watchdogFirstTickDelay() time.Duration {
	return schedulerJitterFn()
}

// watchdogLoop runs forever.
func watchdogLoop() {
	waitHostReadyForWatchdog(watchdogStartupWait, hostReadyForWatchdog)
	// 首轮错峰：必须放在 drain 之前且不可被 tick 打断。configure 在插件加载
	// 时就会投递一次 tick，若让它提前结束等待，六个插件的首轮又会同刻对齐，
	// 抖动等于没加。等待期间投递的 tick 由下面的 drain 丢弃，首轮本身仍在
	// 抖动后的时刻执行，语义与等待期内丢弃 tick 一致。
	time.Sleep(watchdogFirstTickDelay())
	// 排空等待期间由 configure 投递的 tick，避免紧接着重复跑一轮。
	select {
	case <-watchdogTickCh:
	default:
	}
	loadCountersFromDisk()
	purgeLegacyAnomalyFlags()
	runWatchdogTick()
	for {
		timer := time.NewTimer(watchdogIntervalDefault)
		select {
		case <-watchdogTickCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		flushCounters()
		runWatchdogTick()
	}
}

func preserveWatchdogLoop() {
	watchdogLoop()
}

// startFailoverProbe 启动禁用账号探活循环。与 watchdog 分开起，因为两者的
// 调度语义不同：探活自带 10 分钟固定周期与不重叠保护，而 watchdog 管的是签到/
// 额度一类的 10 分钟巡检，混在一个 ticker 里会让探活受巡检耗时影响。
//
// 单独成函数是为了让 main.go 的启动序列显式可读（qoderwork 没有 init 启动惯用法）。
// 最近修改时间：2026-10-22；改动原因：探活周期改为 10 分钟，注释同步。
func startFailoverProbe() {
	go failoverProbeLoop()
	// 外部启用对账：每 2 分钟把「用户已手动启用」同步回插件侧（含盘上陈旧标记），
	// 否则手动启用的号最长 10 分钟仍被排除。
	go failoverReconcileLoop()
}
