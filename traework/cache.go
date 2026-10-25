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
//
// 内存未命中时回落 sidecar 快照：插件热重载/宿主重启会清空 sync.Map，而面板
// 冷加载与调度排序都读这里。没有回落的话首屏每个账号都要现查一次上游，且在
// 查回来之前排序里全是「未知」。快照只读，不回写内存——写入方仍是真实拉取。
func cachedCredits(authID string) (*traeCredits, bool) {
	if v, ok := accountCache.Load(authID); ok {
		entry, ok := v.(*accountCacheEntry)
		if !ok {
			return nil, false
		}
		return entry.credits, true
	}
	if snap, ok := creditSnapshotGet(authID); ok && snap.Credits != nil {
		return snap.Credits, true
	}
	return nil, false
}

// isCreditsExhausted reports whether a cached credits snapshot is empty or
// zero-remaining.
func isCreditsExhausted(cr *traeCredits) bool {
	return cr == nil || cr.TotalRemain <= 0
}

// isCreditsLowOrExhausted 判定账号是否额度耗尽或已低于硬禁用线。
//
// 语义收窄（0.4.0）：这里只表达「不可路由」，因此必须与 creditTierFor 的
// creditTierDisabled 同边界——低于禁用线或耗尽。保活线（min_routable_credits）
// 只管排序降级，不再在这里硬排除：把它留在硬判据里会让 remain 在 20–99 的
// 现有生产账号继续被踢出路由，与「放宽而非收紧」的目标相反。
// 禁用线默认 0（关闭），因此未配置时本函数等价于「仅判耗尽」，与 0.3.0 一致。
func isCreditsLowOrExhausted(cr *traeCredits) bool {
	if cr == nil || isCreditsExhausted(cr) {
		return true
	}
	below := loadedLowCreditDisableBelow()
	return below > 0 && cr.TotalRemain < below
}

// cachedCreditsScore returns (remain, exhaustedOrLow). remain is -1 when unknown.
func cachedCreditsScore(authID string) (int64, bool) {
	cr, ok := cachedCredits(authID)
	if !ok || cr == nil {
		return -1, false
	}
	return cr.TotalRemain, isCreditsLowOrExhausted(cr)
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
