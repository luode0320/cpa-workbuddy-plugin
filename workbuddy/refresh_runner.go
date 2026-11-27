// refresh_runner.go implements the unified, concurrent account-refresh queue.
//
// Motivation: as the account fleet grows, refreshing every account in one
// concurrent burst (the historical /refresh and panel lazy-load behaviour)
// stampedes the upstream billing API and, on the refresh button, blocks the
// management handler for N×round-trip latency. This runner decouples "ask for
// a refresh" from "perform the refresh": every trigger just enqueues and
// returns immediately, while a background worker pool drains the queue with
// a hard-coded concurrency limit of 10.
//
// Trigger sources (three entry points converge on one runner):
//   1. panel enter (auto, no button)  — POST /refresh → EnqueueAll(source="panel")
//   2. account watchdog 10m tick      — EnqueueAll(source="watchdog")
//   3. GET /credits?track=1 (one card)— EnqueueOne(source="credits")
//
// All are idempotent. The runner keeps a per-account state machine
// (pending → running → done|failed) so the panel can poll GET /refresh/status
// and update cards incrementally instead of waiting for the whole batch.
//
// Concurrency: up to refreshConcurrency goroutines fetch in parallel. The
// upstream singleflight in cachedAccountDetails is reused unchanged — this
// runner does not add a second lock; it only limits *how many* accounts are
// fetched at once, so concurrent dashboard/reconcile callers still share the
// same upstream call for the same account.
package main

import (
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

// refreshConcurrency is the hard-coded max parallel fetches.
const refreshConcurrency = 4

// refreshStatus is the per-account lifecycle state surfaced to the panel.
type refreshStatus string

const (
	rsPending refreshStatus = "pending"
	rsRunning refreshStatus = "running"
	rsDone    refreshStatus = "done"
	rsFailed  refreshStatus = "failed"
)

// refreshTarget names one account to refresh.
type refreshTarget struct {
	AuthIndex string
	AuthID    string
}

// refreshJobState is one queue entry and its current state.
type refreshJobState struct {
	AuthIndex string        `json:"auth_index"`
	AuthID    string        `json:"auth_id"`
	Status    refreshStatus `json:"status"`
	Error     string        `json:"error,omitempty"`
	FetchedAt time.Time     `json:"fetched_at,omitempty"`
	Source    string        `json:"source,omitempty"`
}

// refreshSnapshot is the read-only view returned by GET /refresh/status.
type refreshSnapshot struct {
	Running      bool              `json:"running"`
	Total        int               `json:"total"`
	Done         int               `json:"done"`
	Failed       int               `json:"failed"`
	Pending      int               `json:"pending"`
	RunningIndex int               `json:"running_index"`
	Sources      []string          `json:"sources,omitempty"`
	PerAccount   []refreshJobState `json:"per_account,omitempty"`
	StartedAt    time.Time         `json:"started_at,omitempty"`
	FinishedAt   time.Time         `json:"finished_at,omitempty"`
}

// refreshRunner owns the queue and the single background worker. fetchFn is
// injectable so unit tests can drive the state machine without the host API;
// production uses doFetchOne.
type refreshRunner struct {
	mu           sync.Mutex
	batch        []refreshJobState
	idx          int // next index to fetch (monotonic within a generation)
	generation   int // monotonic round id; guards against a stale in-flight worker
	inFlight     bool // true from Enqueue until drainBatch fully completes
	sources      []string
	startedAt    time.Time
	finishedAt   time.Time
	wake         chan struct{}
	stop         chan struct{}
	fetchFn      func(authIndex, authID string) error
}

func newRefreshRunner() *refreshRunner {
	return &refreshRunner{
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
		fetchFn:      doFetchOne,
	}
}

// globalRefresh is the process-wide singleton; its worker is started in init.
var globalRefresh = newRefreshRunner()

func init() {
	go globalRefresh.run()
}

// EnqueueAll starts a full-fleet refresh round and wakes the worker. It
// returns the number of accounts queued and does not block on any fetch.
//
// Idempotent: if a round is already in flight (idx < len(batch)), the call is
// a no-op and returns 0 — a concurrent full refresh would redundantly re-fetch
// every account, and multiple triggers (panel enter, watchdog tick) must
// collapse into a single round.
func (r *refreshRunner) EnqueueAll(targets []refreshTarget, source string) int {
	r.mu.Lock()
	if r.inFlight {
		r.mu.Unlock()
		return 0
	}
	r.batch = make([]refreshJobState, 0, len(targets))
	for _, t := range targets {
		if t.AuthID == "" || t.AuthIndex == "" {
			continue
		}
		r.batch = append(r.batch, refreshJobState{
			AuthIndex: t.AuthIndex,
			AuthID:    t.AuthID,
			Status:    rsPending,
			Source:    source,
		})
	}
	r.idx = 0
	r.generation++
	r.sources = []string{source}
	r.startedAt = time.Now()
	r.finishedAt = time.Time{}
	r.inFlight = true
	n := len(r.batch)
	r.mu.Unlock()

	r.signal()
	return n
}

// EnqueueOne starts a single-account refresh round (used by the single-card
// refresh path). Idempotent: if a round is already in flight, the call is a
// no-op and returns false so a concurrent trigger cannot start a second round
// or duplicate an account already being refreshed.
func (r *refreshRunner) EnqueueOne(authIndex, authID, source string) bool {
	if authID == "" || authIndex == "" {
		return false
	}
	r.mu.Lock()
	if r.inFlight {
		r.mu.Unlock()
		return false
	}
	r.batch = []refreshJobState{{
		AuthIndex: authIndex, AuthID: authID,
		Status: rsPending, Source: source,
	}}
	r.idx = 0
	r.generation++
	r.sources = []string{source}
	r.startedAt = time.Now()
	r.finishedAt = time.Time{}
	r.inFlight = true
	r.mu.Unlock()

	r.signal()
	return true
}

// signal wakes the worker without blocking. Coalesces bursts (capacity 1).
func (r *refreshRunner) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// run is the worker goroutine. It waits for a signal, then drains the batch
// with up to refreshConcurrency parallel fetches.
func (r *refreshRunner) run() {
	for {
		select {
		case <-r.stop:
			return
		case <-r.wake:
		}

		r.mu.Lock()
		gen := r.generation
		r.mu.Unlock()

		r.drainBatch(gen)
	}
}

// drainBatch dispatches pending jobs to up to refreshConcurrency parallel
// workers and waits for all of them to finish before returning. The stop
// channel is closed when the runner exits; goroutines naturally drain.
func (r *refreshRunner) drainBatch(gen int) {
	sem := make(chan struct{}, refreshConcurrency)
	var wg sync.WaitGroup

	for {
		r.mu.Lock()
		if r.generation != gen {
			r.mu.Unlock()
			break
		}
		if r.idx >= len(r.batch) {
			r.mu.Unlock()
			break
		}
		// Claim slot.
		job := r.batch[r.idx]
		job.Status = rsRunning
		r.batch[r.idx] = job
		authIndex := job.AuthIndex
		authID := job.AuthID
		slot := r.idx
		r.idx++
		r.mu.Unlock()

		sem <- struct{}{}
		wg.Add(1)
		go func(slot int, authIndex, authID string) {
			defer wg.Done()
			defer func() { <-sem }()

			err := r.fetchFn(authIndex, authID)

			r.mu.Lock()
			if r.generation == gen && slot < len(r.batch) {
				j := r.batch[slot]
				j.FetchedAt = time.Now()
				if err != nil {
					j.Status = rsFailed
					j.Error = err.Error()
				} else {
					j.Status = rsDone
				}
				r.batch[slot] = j
			}
			r.mu.Unlock()
		}(slot, authIndex, authID)
	}

	wg.Wait()

	// Mark batch finished if this generation is still current.
	r.mu.Lock()
	if r.generation == gen {
		r.finishedAt = time.Now()
		r.inFlight = false
	}
	r.mu.Unlock()
}

// Snapshot returns a consistent read-only view of the current batch.
func (r *refreshRunner) Snapshot() refreshSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	snap := refreshSnapshot{
		Total:        len(r.batch),
		RunningIndex: r.idx,
		Sources:      append([]string(nil), r.sources...),
		StartedAt:    r.startedAt,
		FinishedAt:   r.finishedAt,
		PerAccount:   append([]refreshJobState(nil), r.batch...),
	}
	snap.Running = r.inFlight
	for i := range r.batch {
		switch r.batch[i].Status {
		case rsRunning:
		case rsDone:
			snap.Done++
		case rsFailed:
			snap.Failed++
		default: // pending + running
			snap.Pending++
		}
	}
	return snap
}

// doFetchOne 手动刷新单个账号：拉积分 → 写快照 → 跑三档分级 → 探活。
//
// 这是「用户主动要求刷新」的通道，因此 force 拉取上游。定时刷新不走这里：
// 自动积分更新由 credits_sweep.go 的每小时串行扫描负责，避免两条路径同时
// 打 billing 接口（2026-10-23 用户裁定：自动刷新每小时一次且串行）。
//
// 它只把「积分本身拉不到」当作错误上报——那是面板的核心字段；探活失败只记日志，
// 因为探活是另一套语义（test_failed 标签由 active_ping 自己维护）。
// [参数] authIndex：宿主 auth_index；authID：账号标识。
// [返回] 积分拉取失败时返回错误。
// 最近修改时间：2026-10-23；改动原因：改为手动刷新专用，并接入快照落盘与分级。
func doFetchOne(authIndex, authID string) error {
	sa, err := hostAuthGetFn(authIndex)
	if err != nil {
		return err
	}
	_, _, cr, errs := cachedAccountDetails(authID, sa, true)
	for _, e := range errs {
		if strings.Contains(e, "credits:") {
			return errors.New(e)
		}
	}
	// 写快照 + 分级：手动刷新同样必须立刻反映到路由档位上。
	if cr != nil {
		recordCreditSnapshot(authID, cr, sa.Account.Nickname, accountRegion(sa))
		gradeCreditsForAuth(authIndex, authID, sa, cr)
		if persistErr := persistCreditSnapshot(); persistErr != nil {
			log.Printf("[workbuddy] credits snapshot persist failed: %v", persistErr)
		}
	}
	if pingErr := triggerActivePing(authIndex, authID, sa); pingErr != nil {
		log.Printf("[workbuddy] active ping account %s warning: %v", authID, pingErr)
	}
	return nil
}
