// watchdog.go runs the periodic account watchdog — every interval (default
// 10 minutes) it fires the scheduled active ping (the "hi" reasoning request)
// for every workbuddy account whose 4h throttle has expired.
//
// The ping is what keeps accounts warm AND what produces the 「测试」tag: a
// scheduled ping that fails while the account still has credits writes
// `test_failed: true` (active_ping.go) so the panel can surface it for manual
// cleanup. A later successful ping clears the tag.
//
// 积分自动刷新已移交 credits_sweep.go（2026-10-23 用户裁定：每小时一轮、
// 串行、全部账号、结果落 sidecar）。本循环只保留探活与「测试」标签同步。
//
// 保号 (preserve) 池已于 2026-09-29 移除；2026-10-23 以三档分级重建：
// 低于禁用线的号写盘停用，禁用线与保活线之间的号降级排序（见 credit_disable.go）。
//
// Trigger sources (three entry points converge on this goroutine):
//  1. Periodic tick — every watchdogIntervalDefault.
//  2. configure()    — register / reconfigure drops a non-blocking tick.
//
// Both are idempotent and best-effort: a tick that finds nothing to refresh
// just exits.
//
// Lifecycle: the loop is started in init() and runs forever (the plugin
// shutdown path is a no-op due to SIGSEGV risk from touching Go sync
// primitives during host teardown — see checkin.go:31).
//
// First-tick race: init() runs *before* cliproxy_plugin_init sets hostAPI. An
// immediate first tick raced against hostAuthList() and returned silently on
// the very first run, leaving a 10-minute blind window. Now the loop waits for
// hostReadyForWatchdog() (host bridge up AND auth discovery alive) up to
// watchdogStartupWait before firing its first tick. requestWatchdogTick
// further coalesces configure() triggers into the same batch.
package main

import (
	"log"
	"time"
)

// Defaults for the account watchdog.
const (
	watchdogIntervalDefault time.Duration = 10 * time.Minute
	// Max wait for host to wire up before the first tick fires. Empirically
	// <1s; 15s covers cold docker pulls / slow auth-dir scans with margin.
	watchdogStartupWait = 15 * time.Second
	// Host-readiness poll interval during the startup wait.
	watchdogReadyPoll = 250 * time.Millisecond
)

// watchdogTickCh queues an asynchronous tick request. Buffered cap 1 so a
// burst (e.g. configure() + panel refresh) collapses into a single batched
// tick.
var watchdogTickCh = make(chan struct{}, 1)

// requestWatchdogTick asks the watchdog loop to run one tick as soon as the
// current sleep wakes up. Non-blocking: if a tick is already pending the call
// is a no-op. Safe to call from any goroutine (configure's RPC thread,
// dashboard handler, manual API).
func requestWatchdogTick() {
	select {
	case watchdogTickCh <- struct{}{}:
	default:
		// chan 满 = 已有一个 tick 在排队；丢弃即可。
	}
}

// hostReadyForWatchdog reports whether the host plugin-call table AND auth
// discovery are both alive. An empty auth list still counts as ready — IPC
// works; we just have no workbuddy files yet. This is the strongest probe
// available without reaching into private host state.
func hostReadyForWatchdog() bool {
	if !hostBridgeAvailable() {
		return false
	}
	_, err := hostAuthList()
	return err == nil
}

// waitHostReadyForWatchdog polls ready() until it returns true or the
// deadline passes. maxWait<=0 returns whatever ready() returns immediately
// (lets unit tests skip the wait without touching real time).
// On timeout it does NOT loop forever — the caller still proceeds and the
// next periodic tick catches up.
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
		// Capped sleep so a slow ready() (probe RPC) doesn't overshoot.
		remaining := time.Until(deadline)
		if remaining > watchdogReadyPoll {
			remaining = watchdogReadyPoll
		}
		if remaining > 0 {
			time.Sleep(remaining)
		}
	}
}

// runWatchdogTick 在本拍做两件事：同步「测试」标签镜像，以及触发定时探活。
//
// 积分自动更新已移交 credits_sweep.go（2026-10-23 用户裁定）：每小时一轮、
// 串行、全部账号、结果落 sidecar。watchdog 不再驱动全量积分刷新，否则两条
// 路径会各自打一轮上游，既浪费配额又让快照来源混乱。
// 探活仍留在本拍：它是「账号是否还能用」的独立信号，与积分无关，且
// doActivePing 自带 4 小时节流，所以每 10 分钟一拍实际只对到期账号真发请求。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-23；改动原因：全量积分刷新移交每小时串行扫描，保留定时探活。
func runWatchdogTick() {
	// 1. 从磁盘重建「测试」标签镜像，让探活写入的标签在本拍就影响路由。
	refreshTestFailedSetFromDisk()

	// 2. 逐个账号触发定时探活；doActivePing 自带 4 小时节流。
	files, err := hostAuthList()
	if err != nil {
		return
	}
	for _, f := range files {
		sa, errGet := hostAuthGet(f.AuthIndex)
		if errGet != nil || sa == nil {
			continue
		}
		if pingErr := triggerActivePing(f.AuthIndex, f.ID, sa); pingErr != nil {
			log.Printf("[workbuddy] scheduled active ping account %s warning: %v", f.ID, pingErr)
		}
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

// watchdogLoop runs forever. First tick fires after the host is ready
// (registered + auth list reachable) up to watchdogStartupWait; pending
// trigger requests collected during the wait are drained so the first batched
// tick covers both startup and any register-time trigger. Each iteration
// selects between the fixed interval timer and an external trigger via
// requestWatchdogTick — coalesced to a single tick per wake.
//
// 最近修改时间：2026-11-22 17:40:00；改动原因：补实例守卫闸门，热重载后旧实例不再继续探活。
func watchdogLoop() {
	// Startup: wait for host to wire up. init() runs before the host sets
	// hostAPI, so an immediate tick races against hostAuthList() and returns
	// silently — leaving a 10-minute blind window.
	waitHostReadyForWatchdog(watchdogStartupWait, hostReadyForWatchdog)
	// 首轮错峰：必须放在 drain 之前且不可被 tick 打断。configure 在插件加载
	// 时就会投递一次 tick，若让它提前结束等待，六个插件的首轮又会同刻对齐，
	// 抖动等于没加。等待期间投递的 tick 由下面的 drain 丢弃，首轮本身仍在
	// 抖动后的时刻执行，语义与等待期内丢弃 tick 一致。
	time.Sleep(watchdogFirstTickDelay())
	// Drain any trigger queued during the wait so we don't double-tick
	// immediately. The configured first tick still runs.
	select {
	case <-watchdogTickCh:
	default:
	}
	// 首轮前先判实例：热重载后旧实例在这里直接退出，不必先打一轮探活。
	if retireIfSuperseded("watchdog loop") {
		return
	}
	// Seed the in-memory success/failure counters from the persisted json so
	// the panel reads restart-recovered values (memory-first; see counter.go).
	loadCountersFromDisk()
	// One-time sweep: strip the dead legacy top-level `anomaly` key from
	// physical auth files (the anomaly pool was removed on 2026-09-08).
	purgeLegacyAnomalyFlags()
	runWatchdogTick()
	for {
		timer := time.NewTimer(watchdogIntervalDefault)
		select {
		case <-watchdogTickCh:
			// External trigger: stop the interval timer so we don't fire
			// twice in quick succession. Drain the channel value if the
			// timer already fired before select picked the trigger branch.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		// 每轮先判实例：被取代后立即停手，避免旧实例与新实例并行探活。
		if retireIfSuperseded("watchdog loop") {
			return
		}
		// Fold pending counter deltas into the auth files on the watchdog's
		// cadence (default 10m). The counters are best-effort backup: the
		// in-memory value stays the source of truth for the panel regardless
		// of this cadence.
		flushCounters()
		runWatchdogTick()
	}
}
