// watchdog.go runs the credits-preserve watchdog — every interval (default
// 10 minutes) it pulls fresh credits for every traework account and flips
// the preserve flag on disk when an account's remaining credits drop below
// the configured threshold (default 50).
//
// Why a watchdog instead of event-driven only (mirrors workbuddy/watchdog.go):
// scheduler.pick only reads the cached credits snapshot. Without a periodic
// refresh, an account can sit at remain=49 in the cache forever even though
// the user manually recharged it via the Trae web UI. The watchdog closes
// that gap — every interval it queries accountPoints through the throttled
// refresh runner (one account per second), then decides whether the account
// needs to be parked in the preserve set so routing stops burning its
// remaining credits.
//
// Trigger sources (three entry points converge on this goroutine):
//   1. Periodic tick — every preserveWatchdogInterval.
//   2. configure()    — register / reconfigure drops a non-blocking tick.
//   3. Panel force    — buildDashboardEx(force=true) reuses the already-fetched
//                       credits from the dashboard pass to flip flags synchronously.
//
// All three are idempotent and best-effort: a tick that finds nothing to
// flip just exits.
//
// Lifecycle: the loop is started in init() and runs forever. config-driven
// enable/disable is checked every iteration so we never need to restart the
// goroutine.
//
// First-tick race: init() runs *before* cliproxy_plugin_init sets hostAPI.
// An immediate first tick raced against hostAuthList() and returned silently
// on the very first run, leaving a 10-minute blind window. Now the loop waits
// for hostReadyForWatchdog() (host bridge up AND auth discovery alive) up to
// preserveWatchdogStartupWait before firing its first tick.
package main

import (
	"time"
)

// Defaults for the preserve watchdog. All overridable via config_yaml.
const (
	preserveThresholdDefault        int64         = 50
	preserveWatchdogIntervalDefault time.Duration = 10 * time.Minute
	preserveWatchdogEnabledDefault                = true
	preserveWatchdogDisabledPoll                  = 30 * time.Second // how often we re-read config when disabled
	// Max wait for host to wire up before the first tick fires.
	preserveWatchdogStartupWait = 15 * time.Second
	// Host-readiness poll interval during the startup wait.
	preserveWatchdogReadyPoll = 250 * time.Millisecond
)

// preserveTickCh queues an asynchronous tick request. Buffered cap 1 so a
// burst (e.g. configure() + panel refresh + manual trigger) collapses into a
// single batched tick — protects the upstream points API from storms.
var preserveTickCh = make(chan struct{}, 1)

// requestPreserveTick asks the watchdog loop to run one tick as soon as the
// current sleep wakes up. Non-blocking: if a tick is already pending the call
// is a no-op. Safe to call from any goroutine (configure's RPC thread,
// dashboard handler, manual API).
func requestPreserveTick() {
	select {
	case preserveTickCh <- struct{}{}:
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
		if remaining > preserveWatchdogReadyPoll {
			remaining = preserveWatchdogReadyPoll
		}
		if remaining > 0 {
			time.Sleep(remaining)
		}
	}
}

// preserveFlipDecision is one account whose preserve state must change.
type preserveFlipDecision struct {
	AuthIndex string
	AuthID    string
	// Preserve is the desired disk state (true=park, false=release).
	Preserve bool
}

// preserveFlipsNeeded computes which accounts need a state change based on
// each account's credits vs threshold. Pure decision (no host RPC), unit-
// testable. Skips accounts without a credits snapshot (unknown state — never
// auto-flag an account whose balance we couldn't read).
//
// Currently-preserved accounts whose credits recovered above threshold are
// returned with Preserve=false (release). The opposite edge — enter
// preserve — is also returned.
func preserveFlipsNeeded(accounts []traeAccountView, threshold int64) []preserveFlipDecision {
	out := make([]preserveFlipDecision, 0, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		if a == nil || a.AuthID == "" || a.AuthIndex == "" {
			continue
		}
		// No credits snapshot in the view → unknown state, never auto-flag.
		if !viewHasCredits(a) {
			continue
		}
		currentlyPreserved := isPreserve(a.AuthID)
		shouldPreserve := a.Remain < threshold
		if shouldPreserve == currentlyPreserved {
			continue
		}
		out = append(out, preserveFlipDecision{
			AuthIndex: a.AuthIndex,
			AuthID:    a.AuthID,
			Preserve:  shouldPreserve,
		})
	}
	return out
}

// viewHasCredits reports whether a dashboard view carries a usable credits
// snapshot. A remain of -1 (unknown) or an explicit "not fetched" marker is
// not a snapshot; 0 is (exhausted is still a decision input).
func viewHasCredits(a *traeAccountView) bool {
	if a == nil {
		return false
	}
	// -1 is the scheduler's "unknown" sentinel (cachedCreditsScore); views
	// built from cachedCreditsScore carry it when nothing was cached.
	return a.Remain >= 0
}

// preserveApplyFlips executes disk writes + session evictions for a batch of
// preserve-flip decisions. Idempotent on duplicates; errors are swallowed per
// row so one bad file doesn't block the others.
func preserveApplyFlips(flips []preserveFlipDecision) {
	for _, d := range flips {
		if d.Preserve {
			if err := persistPreserveToggle(d.AuthIndex, d.AuthID, true); err != nil {
				continue
			}
			// Entering preserve: force-migrate any sticky session so the
			// next request re-picks a non-preserved account instead of
			// finishing off the buffer.
			evictSessionBindingsForAuth(d.AuthID)
		} else {
			// Exiting preserve: just clear the flag. Picker will see the
			// account as routable again on the next scheduler.pick.
			_ = persistPreserveToggle(d.AuthIndex, d.AuthID, false)
		}
	}
}

// preserveReconcileFromAccounts flips preserve flags on disk using the
// credits already fetched by the dashboard pass. Costs zero additional
// upstream QPS — the panel response that supplied `accounts` is the source
// of truth. Triggered from the dashboard on force=true so an interactive
// "刷新" click never reveals stale badge state.
func preserveReconcileFromAccounts(accounts []traeAccountView) {
	if len(accounts) == 0 {
		return
	}
	threshold := preserveThreshold()
	flips := preserveFlipsNeeded(accounts, threshold)
	preserveApplyFlips(flips)
}

// preserveShouldFlip computes whether an account's preserve flag must change
// given a fresh credits snapshot and its current preserve state. Pure logic
// (no host RPC) so the watchdog's decision is unit-testable.
//
// Contract: preserve is entered when remain < threshold (strictly below —
// an account exactly at the threshold keeps routing). Exiting happens when
// credits recover to >= threshold.
func preserveShouldFlip(remain int64, threshold int64, currentlyPreserved bool) (shouldPreserve bool, changed bool) {
	shouldPreserve = remain < threshold
	return shouldPreserve, shouldPreserve != currentlyPreserved
}

// runPreserveWatchdogTick walks every traework auth, flips the preserve flag
// using the cached credits snapshot (no upstream fetch in-tick), and hands the
// whole fleet to the throttled refresh runner so fresh credits arrive
// asynchronously at one account per second.
func runPreserveWatchdogTick() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	threshold := preserveThreshold()
	targets := make([]refreshTarget, 0, len(files))
	for _, f := range files {
		cr, ok := cachedCredits(f.ID)
		if ok && cr != nil {
			currentlyPreserved := isPreserve(f.ID)
			shouldPreserve, changed := preserveShouldFlip(cr.TotalRemain, threshold, currentlyPreserved)
			if changed {
				if shouldPreserve {
					if err := persistPreserveToggle(f.AuthIndex, f.ID, true); err == nil {
						evictSessionBindingsForAuth(f.ID)
					}
				} else {
					_ = persistPreserveToggle(f.AuthIndex, f.ID, false)
				}
			}
		}
		targets = append(targets, refreshTarget{AuthIndex: f.AuthIndex, AuthID: f.ID})
	}
	if len(targets) > 0 {
		globalRefresh.EnqueueAll(targets, "watchdog")
	}
}

// preserveWatchdogLoop runs forever. First tick fires after the host is
// ready (registered + auth list reachable) up to preserveWatchdogStartupWait;
// pending trigger requests collected during the wait are drained so the
// first batched tick covers both startup and any register-time trigger. Each
// iteration selects between the configured interval timer and an external
// trigger via requestPreserveTick — coalesced to a single tick per wake.
func preserveWatchdogLoop() {
	// Startup: wait for host to wire up. init() runs before the host sets
	// hostAPI, so an immediate tick races against hostAuthList() and returns
	// silently — leaving a 10-minute blind window.
	waitHostReadyForWatchdog(preserveWatchdogStartupWait, hostReadyForWatchdog)
	// Drain any trigger queued during the wait so we don't double-tick
	// immediately. The configured first tick still runs.
	select {
	case <-preserveTickCh:
	default:
	}
	// Seed the in-memory success/failure counters from the persisted json so
	// the panel reads restart-recovered values (memory-first; see counter.go).
	loadCountersFromDisk()
	runPreserveWatchdogTick()
	for {
		enabled := preserveWatchdogEnabled()
		interval := preserveWatchdogInterval()
		sleep := preserveWatchdogDisabledPoll
		if enabled && interval > 0 {
			sleep = interval
		}
		timer := time.NewTimer(sleep)
		select {
		case <-preserveTickCh:
			// External trigger: stop the interval timer so we don't fire
			// twice in quick succession.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		// Fold pending counter deltas into the auth files on the watchdog's
		// cadence (default 10m when enabled, 30s poll when disabled). The
		// counters are best-effort backup: the in-memory value stays the
		// source of truth for the panel regardless of this cadence.
		flushCounters()
		if !preserveWatchdogEnabled() {
			continue
		}
		runPreserveWatchdogTick()
	}
}

func init() {
	go preserveWatchdogLoop()
}
