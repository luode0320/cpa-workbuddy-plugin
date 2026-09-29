// watchdog.go runs the periodic account watchdog — every interval (default
// 10 minutes) it refreshes the credits snapshot for every traework account
// and fires the scheduled active ping (the "hi" reasoning request) through
// the same concurrent refresh runner.
//
// The ping keeps accounts warm AND produces the 「测试」tag: a scheduled ping
// that fails while the account still has credits writes `test_failed: true`
// (active_ping.go) so the panel can surface it for manual cleanup. A later
// successful ping clears the tag. The credits refresh keeps the routing/panel
// snapshot fresh so the low-credit-first picker and the panel badges never
// read a stale balance.
//
// 保号 (preserve) 池已于 2026-09-29 移除：低积分不再把账号摘出路由，异常
// 账号统一由「测试」标签承载。本循环因此只剩「刷新积分 + 定时活跃测试」两
// 项职责，不再翻转任何账号状态。
//
// Trigger sources (two entry points converge on this goroutine):
//   1. Periodic tick — every watchdogIntervalDefault.
//   2. configure()    — register / reconfigure drops a non-blocking tick.
//
// Both are idempotent and best-effort: a tick that finds nothing to refresh
// just exits.
//
// Lifecycle: the loop is started in init() and runs forever.
//
// First-tick race: init() runs *before* cliproxy_plugin_init sets hostAPI.
// An immediate first tick raced against hostAuthList() and returned silently
// on the very first run, leaving a 10-minute blind window. Now the loop waits
// for hostReadyForWatchdog() (host bridge up AND auth discovery alive) up to
// watchdogStartupWait before firing its first tick.
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

// watchdogTickCh queues an asynchronous tick request. Buffered cap 1 so a
// burst (e.g. configure() + panel refresh) collapses into a single batched
// tick — protects the upstream points API from storms.
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
// works; we just have no traework files yet.
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

// runWatchdogTick walks every traework auth and hands the whole fleet to the
// concurrent refresh runner. The runner's per-account fetch
// (refresh_runner.doFetchOne) pulls fresh credits and then fires the scheduled
// active ping, which is what writes/clears the 「测试」tag. No upstream call
// happens in-tick, so a tick is cheap and never blocks the loop.
func runWatchdogTick() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	// Rebuild the 「测试」label mirror from disk on the same cadence, so a tag
	// written by the active-ping path (or another session) is reflected in
	// routing even when the panel is never opened.
	refreshTestFailedSetFromDisk()
	targets := make([]refreshTarget, 0, len(files))
	for _, f := range files {
		targets = append(targets, refreshTarget{AuthIndex: f.AuthIndex, AuthID: f.ID})
	}
	if len(targets) > 0 {
		globalRefresh.EnqueueAll(targets, "watchdog")
	}
}

// watchdogLoop runs forever. First tick fires after the host is
// ready (registered + auth list reachable) up to watchdogStartupWait;
// pending trigger requests collected during the wait are drained so the
// first batched tick covers both startup and any register-time trigger. Each
// iteration selects between the fixed interval timer and an external
// trigger via requestWatchdogTick — coalesced to a single tick per wake.
func watchdogLoop() {
	// Startup: wait for host to wire up. init() runs before the host sets
	// hostAPI, so an immediate tick races against hostAuthList() and returns
	// silently — leaving a 10-minute blind window.
	waitHostReadyForWatchdog(watchdogStartupWait, hostReadyForWatchdog)
	// Drain any trigger queued during the wait so we don't double-tick
	// immediately. The configured first tick still runs.
	select {
	case <-watchdogTickCh:
	default:
	}
	// Seed the in-memory success/failure counters from the persisted json so
	// the panel reads restart-recovered values (memory-first; see counter.go).
	loadCountersFromDisk()
	// Strip the dead legacy `anomaly` key from physical auth files (the
	// anomaly pool was removed; see anomaly_purge.go). Idempotent, runs once.
	purgeLegacyAnomalyFlags()
	// Re-enable accounts still carrying the pre-0.1.55 session-dead
	// auto-disable pair (disabled:true + "Session expired" note) — the
	// manual-toggle-only policy's legacy-flag sweep. Idempotent, runs once.
	purgeLegacyDisabledFlags()
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
		// Fold pending counter deltas into the auth files on the watchdog's
		// cadence (default 10m). The counters are best-effort backup: the
		// in-memory value stays the source of truth for the panel regardless
		// of this cadence.
		flushCounters()
		runWatchdogTick()
	}
}

func init() {
	go watchdogLoop()
}
