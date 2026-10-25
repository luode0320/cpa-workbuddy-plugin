// scheduler.go implements the CPA scheduler.pick capability for qoderwork.
//
// Routing uses the panel-selected active account (region from that card's
// domain). When the selection is exhausted/disabled/missing, randomly switch
// to another non-exhausted qoderwork candidate. Non-qoderwork candidates are
// always deferred so the built-in scheduler handles them.
//
// scheduler_mode=session additionally enables per-conversation routing: each
// conversation is pinned to one account for up to 1h and conversations are
// spread across accounts (see session_auth.go).
package main

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Legacy config values kept for configure() compatibility; pick always uses
// panel active-auth selection now (not credit-max ranking).
const (
	schedulerModeOff     = "off"
	schedulerModeCredits = "credits"
	schedulerModeSession = "session"
)

var (
	schedulerMode   = schedulerModeSession
	schedulerModeMu sync.RWMutex
)

// setSchedulerMode is a test helper that returns a restore func.
func setSchedulerMode(mode string) func() {
	schedulerModeMu.Lock()
	old := schedulerMode
	schedulerMode = mode
	schedulerModeMu.Unlock()
	return func() {
		schedulerModeMu.Lock()
		schedulerMode = old
		schedulerModeMu.Unlock()
	}
}

func loadedSchedulerMode() string {
	schedulerModeMu.RLock()
	defer schedulerModeMu.RUnlock()
	return schedulerMode
}

// handleSchedulerPick selects a qoderwork auth candidate based on the
// panel-selected active account. Non-qoderwork candidates are always deferred
// (Handled: false) so the built-in scheduler handles them.
//
// scheduler_mode:
//   - "off"     → plugin does NOT handle routing; defer everything to built-in.
//   - "credits" → plugin picks via panel-selected active account (sticky, with
//     fallback when that account becomes exhausted/disabled).
//   - "session" → per-conversation routing: same conversation sticks to one
//     account for up to 1h, different conversations spread across accounts;
//     requests without a session identity fall back to the panel-selected
//     account (same as credits).
//
// Default is session (see schedulerMode init). Users opting into the
// built-in routing should set scheduler_mode: off in plugin config.
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	// v0.6.31: actually honor the scheduler_mode toggle. Previously the config
	// was parsed but never read here, so "off" silently behaved like "credits".
	mode := loadedSchedulerMode()
	if mode != schedulerModeCredits && mode != schedulerModeSession {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	// Collect qoderwork candidates only. Accounts in failover cooldown are
	// skipped so new requests route to a healthy account instead. If EVERY
	// qoderwork account is cooling down, defer (Handled: false) so the host's
	// built-in scheduler can fail over to OTHER providers' accounts instead
	// of pinning the request to a dead account.
	var wbCandidates []pluginapi.SchedulerAuthCandidate
	for _, c := range req.Candidates {
		if c.Provider != providerName {
			continue
		}
		if candidateDisabled(c) {
			continue
		}
		wbCandidates = append(wbCandidates, c)
	}
	if len(wbCandidates) == 0 {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	// Hard exclusion: 冷却 (failover cooldown) accounts never carry traffic —
	// "not available" by product definition, so they are filtered
	// unconditionally. There is NO "keep the full list" fallback: when every
	// candidate is excluded we defer (Handled: false) so the host's built-in
	// scheduler can fail over to OTHER providers instead of pinning the request
	// to an account we just declared unusable.
	// 慢速冷却判定带模型维度：账号×模型级冷却只挡同一模型，不能把整个账号摘掉。
	// 调度请求带的是客户端模型名，需与记账侧一样归一化后再比对。
	upstreamModel := resolveUpstreamModel(req.Model, nil)
	available := make([]pluginapi.SchedulerAuthCandidate, 0, len(wbCandidates))
	rotationNow := time.Now()
	for _, c := range wbCandidates {
		if isAccountCoolingDown(c.ID) {
			continue
		}
		// 轮换冷却：账号连续承流到阈值后被主动让位，与失败冷却分表存储，
		// 因此不会被成功请求的 resetAccountFailover 抹掉。
		if rotationCoolingDown(c.ID, rotationNow) {
			continue
		}
		// 慢速冷却：该号在这个模型上显著慢于同池水平时让位。只影响当前模型，
		// 该号在其它模型上仍可正常承流。
		if slowCoolingDown(c.ID, upstreamModel, rotationNow) {
			continue
		}
		_, lowCredits := cachedCreditsScore(c.ID)
		if lowCredits {
			continue
		}
		available = append(available, c)
	}
	if len(available) == 0 {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	wbCandidates = available

	// 健康平摊轮询：废除残血优先（Low-Credit-First）反模式。
	// 健康可用账号按高积分优先排序（未测 -1 排最后），并在同一健康阶梯内平摊负载。
	sort.SliceStable(wbCandidates, func(i, j int) bool {
		left, _ := cachedCreditsScore(wbCandidates[i].ID)
		right, _ := cachedCreditsScore(wbCandidates[j].ID)
		if (left < 0) != (right < 0) {
			return left >= 0
		}
		return left > right
	})

	// Build thin view for active-auth picker. 0 积分账号仍参与选择（免费模型可用）。
	cands := make([]activeAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		cands = append(cands, activeAuthCandidate{
			ID:       c.ID,
			Disabled: false, // already filtered
		})
	}
	picked := ""
	if mode == schedulerModeSession {
		picked = pickSessionAuth(extractSessionKey(req), cands)
	} else {
		picked = pickActiveAuth(cands)
	}
	if picked == "" {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	return okEnvelope(pluginapi.SchedulerPickResponse{
		AuthID:  picked,
		Handled: true,
	})
}

// candidateDisabled reports host-disabled auth from Status/metadata.
func candidateDisabled(c pluginapi.SchedulerAuthCandidate) bool {
	st := strings.ToLower(strings.TrimSpace(c.Status))
	if st == "disabled" {
		return true
	}
	if c.Metadata != nil {
		if v, ok := c.Metadata["disabled"]; ok {
			switch t := v.(type) {
			case bool:
				return t
			case string:
				return strings.EqualFold(strings.TrimSpace(t), "true")
			}
		}
	}
	return false
}

// cachedCreditsScore 返回账号积分快照的剩余值与残血标记。
// [参数] authID：账号标识（auth.ID，与调度候选 ID 一致）。
// [返回] remain 为未知时 -1；lowCredits 表示正积分低于安全红线（0 积分为 false）。
// 最近修改时间 2026-10-17 15:12:00（0 积分账号不再视为不可路由）
func cachedCreditsScore(authID string) (int64, bool) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return -1, false
	}
	v, ok := accountCache.Load(authID)
	if !ok {
		if norm := normalizeFailoverKey(authID); norm != "" && norm != authID {
			v, ok = accountCache.Load(norm)
		}
	}
	if !ok {
		target := normalizeFailoverKey(authID)
		if target != "" {
			accountCache.Range(func(key, val any) bool {
				kStr, _ := key.(string)
				if normalizeFailoverKey(kStr) == target {
					v = val
					ok = true
					return false
				}
				return true
			})
		}
	}
	if !ok {
		return -1, false
	}
	entry, ok := v.(*accountCacheEntry)
	if !ok || entry.credits == nil {
		return -1, false
	}
	return entry.credits.TotalRemain, isCreditsLowForRouting(entry.credits)
}
