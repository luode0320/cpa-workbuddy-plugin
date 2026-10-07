package plugin

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/openai"
)

const (
	requestAuthTTL      = 15 * time.Minute
	requestAuthCapacity = 4096
)

type requestKey [sha256.Size]byte

type requestAuthEntry struct {
	key       requestKey
	authID    string
	expiresAt time.Time
}

type localUsageStatus struct {
	Scope        string     `json:"scope"`
	StartedAt    time.Time  `json:"started_at"`
	Estimated    bool       `json:"estimated"`
	ExecutorRuns int64      `json:"executor_runs"`
	Requests     int64      `json:"requests"`
	Succeeded    int64      `json:"succeeded"`
	Failed       int64      `json:"failed"`
	LastOutcome  string     `json:"last_outcome,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	TotalTokens  int64      `json:"total_tokens"`
}

type usageStore struct {
	mu                 sync.RWMutex
	startedAt          time.Time
	byAuth             map[string]localUsageStatus
	requestAuth        map[requestKey]*list.Element
	requestAuthOrder   *list.List
	requestCompletions *requestCompletionFilter
	checkpoints        *checkpointMetrics
	now                func() time.Time
}

func newUsageStore() *usageStore {
	return &usageStore{
		startedAt: time.Now().UTC(), byAuth: make(map[string]localUsageStatus),
		requestAuth: make(map[requestKey]*list.Element), requestAuthOrder: list.New(),
		requestCompletions: newRequestCompletionFilter(),
		checkpoints:        newCheckpointMetrics(), now: time.Now,
	}
}

func (store *usageStore) recordTokens(authID string, usageEstimate openai.Usage) {
	if authID == "" {
		return
	}
	now := time.Now().UTC()
	store.mu.Lock()
	defer store.mu.Unlock()
	usage := store.byAuth[authID]
	usage.Scope = "plugin_process"
	usage.StartedAt = store.startedAt
	usage.Estimated = true
	usage.ExecutorRuns++
	usage.UpdatedAt = &now
	usage.InputTokens += int64(usageEstimate.PromptTokens)
	usage.OutputTokens += int64(usageEstimate.CompletionTokens)
	usage.TotalTokens += int64(usageEstimate.TotalTokens)
	store.byAuth[authID] = usage
}

func (store *usageStore) selectRequestAuth(requestID, authID string) {
	if requestID == "" {
		return
	}
	key := requestAuthKey(requestID)
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	store.expireRequestAuthLocked(now)
	if store.requestCompletions.contains(key, now) {
		return
	}
	store.removeRequestAuthLocked(key)
	if authID == "" {
		return
	}
	for len(store.requestAuth) >= requestAuthCapacity {
		store.removeOldestRequestAuthLocked()
	}
	entry := requestAuthEntry{key: key, authID: authID, expiresAt: now.Add(requestAuthTTL)}
	store.requestAuth[key] = store.requestAuthOrder.PushBack(entry)
}

func (store *usageStore) completeRequest(requestID, authID string, selected bool, outcome string) {
	if requestID == "" {
		return
	}
	key := requestAuthKey(requestID)
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	store.expireRequestAuthLocked(now)
	if store.requestCompletions.contains(key, now) {
		return
	}
	trackedAuthID := ""
	if element := store.requestAuth[key]; element != nil {
		trackedAuthID = element.Value.(requestAuthEntry).authID
	}
	store.removeRequestAuthLocked(key)
	store.requestCompletions.add(key, now)
	if !selected {
		authID = trackedAuthID
	}
	if authID == "" {
		return
	}
	usage := store.byAuth[authID]
	usage.Scope = "plugin_process"
	usage.StartedAt = store.startedAt
	usage.Estimated = true
	usage.Requests++
	usage.LastOutcome = outcome
	updatedAt := now.UTC()
	usage.UpdatedAt = &updatedAt
	if outcome == "succeeded" {
		usage.Succeeded++
	} else {
		usage.Failed++
	}
	store.byAuth[authID] = usage
}

func requestAuthKey(requestID string) requestKey {
	return sha256.Sum256([]byte(requestID))
}

func (store *usageStore) expireRequestAuthLocked(now time.Time) {
	for {
		oldest := store.requestAuthOrder.Front()
		if oldest == nil || oldest.Value.(requestAuthEntry).expiresAt.After(now) {
			return
		}
		store.removeOldestRequestAuthLocked()
	}
}

func (store *usageStore) removeOldestRequestAuthLocked() {
	oldest := store.requestAuthOrder.Front()
	if oldest == nil {
		return
	}
	entry := oldest.Value.(requestAuthEntry)
	delete(store.requestAuth, entry.key)
	store.requestAuthOrder.Remove(oldest)
}

func (store *usageStore) removeRequestAuthLocked(key requestKey) {
	element := store.requestAuth[key]
	if element == nil {
		return
	}
	delete(store.requestAuth, key)
	store.requestAuthOrder.Remove(element)
}

func (store *usageStore) snapshot(authID string) localUsageStatus {
	store.mu.RLock()
	defer store.mu.RUnlock()
	usage := store.byAuth[authID]
	usage.Scope = "plugin_process"
	usage.StartedAt = store.startedAt
	usage.Estimated = true
	return usage
}

func mergeLocalUsage(left, right localUsageStatus) localUsageStatus {
	merged := left
	merged.Scope = "plugin_process"
	if merged.StartedAt.IsZero() || (!right.StartedAt.IsZero() && right.StartedAt.Before(merged.StartedAt)) {
		merged.StartedAt = right.StartedAt
	}
	merged.Estimated = left.Estimated || right.Estimated
	merged.ExecutorRuns += right.ExecutorRuns
	merged.Requests += right.Requests
	merged.Succeeded += right.Succeeded
	merged.Failed += right.Failed
	merged.InputTokens += right.InputTokens
	merged.OutputTokens += right.OutputTokens
	merged.TotalTokens += right.TotalTokens
	if right.UpdatedAt != nil && (merged.UpdatedAt == nil || !right.UpdatedAt.Before(*merged.UpdatedAt)) {
		updatedAt := *right.UpdatedAt
		merged.UpdatedAt = &updatedAt
		merged.LastOutcome = right.LastOutcome
	}
	return merged
}
