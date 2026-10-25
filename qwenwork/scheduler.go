// scheduler.go implements the CPA scheduler.pick capability for qwenwork.
//
// Routing uses the panel-selected active account (region from that card's
// domain). When the selection is exhausted/disabled/missing, randomly switch
// to another non-exhausted qwenwork candidate. Non-qwenwork candidates are
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

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"time"
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

// handleSchedulerPick selects a qwenwork auth candidate based on the
// panel-selected active account. Non-qwenwork candidates are always deferred
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

	// Collect qwenwork candidates only. Accounts in failover cooldown are
	// skipped so new requests route to a healthy account instead. If EVERY
	// qwenwork account is cooling down, defer (Handled: false) so the host's
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
	available := make([]pluginapi.SchedulerAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		if isAccountCoolingDown(c.ID) {
			continue
		}
		// 慢速冷却：该号在这个模型上显著慢于同池水平时让位，只影响当前模型。
		if slowCoolingDownForRequest(c.ID, req.Model) {
			continue
		}
		_, lowOrExhausted := cachedCreditsScore(c.ID)
		if lowOrExhausted {
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

	// Build thin view for active-auth picker. 耗尽账号已在上面被过滤。
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

// cachedCreditsScore 返回账号积分快照的剩余值与不可路由标记。
// [参数] authID：账号标识（auth.ID，与调度候选 ID 一致）。
// [返回] remain 为未知时 -1；lowOrExhausted 表示已耗尽或正积分低于安全红线。
// 最近修改时间 2026-10-18 17:10:00（QwenWork 无免费模型，耗尽账号不可路由）
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
	return entry.credits.TotalRemain, isCreditsLowOrExhausted(entry.credits)
}

// slowCoolingDownForRequest 判断调度阶段该账号是否应在当前请求的模型上让位。
//
// 调度请求给的是客户端模型名，而慢速状态按上游模型名记账；两者必须先按
// 与执行链相同的规则归一化，否则「客户端叫 A、上游叫 B」时判定永远不命中。
// [参数] authID：账号标识；clientModel：客户端请求的模型名。
// [返回] true 表示该账号在该模型上处于慢速冷却。
// 最近修改时间：2026-10-26；改动原因：新增，调度入口的模型归一化封装。
func slowCoolingDownForRequest(authID, clientModel string) bool {
	model := strings.TrimSpace(resolveUpstreamModel(clientModel, nil))
	if model == "" {
		return false
	}
	return slowCoolingDown(authID, model, time.Now())
}
