// cache.go holds the in-memory credits cache keyed by auth ID. The panel and
// the scheduler both read it so multi-account routing can skip exhausted
// accounts without an upstream query on every request.
package main

import (
	"sync"
	"time"
)

// traeCredits is a snapshot of one account's Trae Work credits quota.
// TotalUsed/TotalSize/PackCount come from the same entitlement-list response
// as TotalRemain (pack-level credits_limit / usage.credits_amount); they power
// the panel progress bar. Legacy cache entries persisted before these fields
// existed decode with zero values — the panel treats unknown usage as "no
// bar" rather than 0 used.
type traeCredits struct {
	TotalRemain int64  `json:"total_remain"`
	TotalUsed   int64  `json:"total_used,omitempty"`
	TotalSize   int64  `json:"total_size,omitempty"`
	PackCount   int    `json:"pack_count,omitempty"`
	FetchedAt   string `json:"fetched_at,omitempty"`
}

// accountCacheEntry is one cached account snapshot.
type accountCacheEntry struct {
	credits *traeCredits
	updated time.Time
}

// accountCache maps auth.ID -> *accountCacheEntry. The scheduler reads it on
// every pick (cheap sync.Map load); the management layer refreshes it after
// credits queries and check-ins.
var accountCache sync.Map // authID -> *accountCacheEntry

// cacheCredits stores a credits snapshot for an auth ID.
func cacheCredits(authID string, cr *traeCredits) {
	if stringsTrim(authID) == "" {
		return
	}
	accountCache.Store(authID, &accountCacheEntry{credits: cr, updated: time.Now()})
}

// cachedCredits returns (credits, ok). ok=false when nothing cached.
func cachedCredits(authID string) (*traeCredits, bool) {
	v, ok := accountCache.Load(authID)
	if !ok {
		return nil, false
	}
	entry, ok := v.(*accountCacheEntry)
	if !ok {
		return nil, false
	}
	return entry.credits, true
}

// isCreditsExhausted reports whether a cached credits snapshot is empty or
// zero-remaining.
func isCreditsExhausted(cr *traeCredits) bool {
	return cr == nil || cr.TotalRemain <= 0
}

// cachedCreditsScore returns (remain, exhausted). remain is -1 when unknown.
func cachedCreditsScore(authID string) (int64, bool) {
	cr, ok := cachedCredits(authID)
	if !ok {
		return -1, false
	}
	return cr.TotalRemain, isCreditsExhausted(cr)
}

func stringsTrim(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
