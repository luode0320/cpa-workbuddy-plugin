// checkin_retry.go owns the persistent retry queue for failed check-ins.
//
// Trae's check-in endpoint intermittently rejects claims with transient
// errors (business code 9074 "当前参与用户太多，请稍后再试" during peak
// hours, device-level blocks, blank bridge responses). One 3-second retry
// inside checkinAccount absorbs only the shortest throttle windows; this
// queue keeps working the failed accounts on a 1-minute cadence for up to
// 60 attempts (~1 hour), so a peak-hour throttle no longer loses the day's
// check-in points.
//
// Design notes:
//   - Entries are keyed by auth_index; re-scheduling an already queued
//     account preserves its attempt count and next-run time (idempotent).
//   - A successful check-in (manual, fleet, or retry) cancels the entry.
//   - Permanent failures ("no credential", credential load failures) are
//     never queued — retrying cannot succeed.
//   - Due entries are popped from the map before processing so concurrent
//     fleet runs cannot double-fire; a failing attempt re-queues itself.
package main

import (
	"strings"
	"sync"
	"time"
)

const (
	// checkinRetryInterval is how long a failed account waits between
	// attempts. One minute keeps the pressure on Trae's throttling window
	// low (a fleet burst is spread out) while still converging fast.
	checkinRetryInterval = 1 * time.Minute
	// checkinRetryMax caps total attempts per queued account. 60 x 1min
	// gives a one-hour window, which comfortably covers observed peak-hour
	// throttle periods, while a hard cap guarantees no perpetual spin.
	checkinRetryMax = 60
	// checkinRetryTick bounds how often the retry loop scans for due work.
	checkinRetryTick = 15 * time.Second
)

// checkinRetryEntry tracks one account's retry state.
type checkinRetryEntry struct {
	AuthIndex   string    `json:"auth_index"`
	AuthID      string    `json:"auth_id"`
	UID         string    `json:"uid,omitempty"`
	Attempts    int       `json:"attempts"`
	LastMessage string    `json:"last_message,omitempty"`
	ScheduledAt time.Time `json:"scheduled_at"`
	NextAt      time.Time `json:"next_at"`
}

var (
	checkinRetryMu    sync.Mutex
	checkinRetryQueue = make(map[string]*checkinRetryEntry)
)

// checkinRetryable reports whether a failure message is worth retrying.
// Credential-less accounts can never succeed, so they are excluded.
func checkinRetryable(message string) bool {
	m := strings.ToLower(message)
	if strings.Contains(m, "no credential") || strings.Contains(message, "凭据加载失败") {
		return false
	}
	return true
}

// scheduleCheckinRetry queues (or refreshes) a failed account. Returns true
// when a NEW entry was added; re-scheduling an existing entry only updates
// the last message — attempts and the next-run time stay untouched so a
// concurrent fleet run cannot postpone or reset the backoff.
func scheduleCheckinRetry(authIndex, authID, uid, message string) bool {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || !checkinRetryable(message) {
		return false
	}
	now := time.Now()
	checkinRetryMu.Lock()
	defer checkinRetryMu.Unlock()
	if e, ok := checkinRetryQueue[authIndex]; ok {
		e.LastMessage = message
		if uid != "" {
			e.UID = uid
		}
		return false
	}
	checkinRetryQueue[authIndex] = &checkinRetryEntry{
		AuthIndex:   authIndex,
		AuthID:      authID,
		UID:         uid,
		Attempts:    1,
		LastMessage: message,
		ScheduledAt: now,
		NextAt:      now.Add(checkinRetryInterval),
	}
	return true
}

// cancelCheckinRetry drops an account from the retry queue (success path).
func cancelCheckinRetry(authIndex string) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return
	}
	checkinRetryMu.Lock()
	delete(checkinRetryQueue, authIndex)
	checkinRetryMu.Unlock()
}

// checkinRetrySnapshot returns the queued entries ordered by next attempt.
func checkinRetrySnapshot() []map[string]any {
	checkinRetryMu.Lock()
	entries := make([]*checkinRetryEntry, 0, len(checkinRetryQueue))
	for _, e := range checkinRetryQueue {
		entries = append(entries, e)
	}
	checkinRetryMu.Unlock()
	// Oldest next-attempt first.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].NextAt.Before(entries[j-1].NextAt); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"auth_index":   e.AuthIndex,
			"auth_id":      e.AuthID,
			"uid":          e.UID,
			"attempts":     e.Attempts,
			"max_attempts": checkinRetryMax,
			"last_message": e.LastMessage,
			"next_at":      e.NextAt.Format(time.RFC3339),
			"scheduled_at": e.ScheduledAt.Format(time.RFC3339),
		})
	}
	return out
}

// checkinRetryLoop pops due entries and re-attempts their check-in until
// success or the attempt cap. Started once from init().
func checkinRetryLoop() {
	ticker := time.NewTicker(checkinRetryTick)
	defer ticker.Stop()
	for range ticker.C {
		for _, e := range popDueCheckinRetries() {
			go processCheckinRetry(e)
		}
	}
}

// popDueCheckinRetries removes every entry whose NextAt has passed and
// returns them. Removal-before-processing prevents double firing if a
// manual fleet check-in runs concurrently.
func popDueCheckinRetries() []*checkinRetryEntry {
	now := time.Now()
	checkinRetryMu.Lock()
	defer checkinRetryMu.Unlock()
	var due []*checkinRetryEntry
	for idx, e := range checkinRetryQueue {
		if !e.NextAt.After(now) {
			due = append(due, e)
			delete(checkinRetryQueue, idx)
		}
	}
	return due
}

// processCheckinRetry re-runs the claim for one queued account.
func processCheckinRetry(e *checkinRetryEntry) {
	a, err := hostAuthGet(e.AuthIndex)
	if err != nil || a == nil {
		requeueCheckinRetry(e, "凭据加载失败")
		return
	}
	res := checkinAccount(a)
	if res.OK {
		// Refresh the credits cache so the panel reflects the late win.
		remain := res.Points
		if r, qerr := accountPoints(a); qerr == nil {
			remain = r
		}
		if remain > 0 {
			cacheCredits(e.AuthID, &traeCredits{TotalRemain: remain, FetchedAt: time.Now().Format(time.RFC3339)})
		}
		return
	}
	requeueCheckinRetry(e, res.Message)
}

// requeueCheckinRetry puts a failed retry attempt back into the queue, or
// drops it once the attempt cap is reached.
func requeueCheckinRetry(e *checkinRetryEntry, message string) {
	if !checkinRetryable(message) {
		return
	}
	e.Attempts++
	e.LastMessage = message
	if e.Attempts >= checkinRetryMax {
		// Attempt budget exhausted — surface the final state via snapshot
		// only if it was never re-queued by a fleet run; simplest correct
		// behavior is to drop it (the daily auto slots will try again).
		return
	}
	e.NextAt = time.Now().Add(checkinRetryInterval)
	checkinRetryMu.Lock()
	// Re-add only if neither a fleet run re-scheduled it nor a success
	// cancelled it in the meantime (it was popped, so the key is absent —
	// but a concurrent scheduleCheckinRetry may have re-created it).
	if _, exists := checkinRetryQueue[e.AuthIndex]; !exists {
		checkinRetryQueue[e.AuthIndex] = e
	}
	checkinRetryMu.Unlock()
}

func init() {
	go checkinRetryLoop()
}
