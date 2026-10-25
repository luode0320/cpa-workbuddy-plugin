// scheduler.go implements the CPA scheduler.pick capability for traework.
//
// Routing uses the panel-selected active account; when the selection is
// exhausted/disabled/missing/cooling-down, it switches to another
// healthy candidate. Non-traework candidates are always deferred so the
// built-in scheduler handles them, and when EVERY traework candidate is
// exhausted/cooling-down the pick is deferred as well (Handled: false) so
// the host can fail over to other providers' accounts (e.g. workbuddy).
// Only active when scheduler_mode: credits or session is configured
// (default off).
//
// Only "available" accounts participate: 「测试」标签 (test_failed) and 冷却
// (failover cooldown) are hard-excluded — they are unusable by product
// definition, so there is no fallback that re-admits them. Survivors are
// ordered HIGH-CREDIT-FIRST: the healthiest accounts (most remaining credits)
// lead and unmeasured (-1) accounts rank last, so load spreads across healthy
// accounts instead of burning the weakest down first.
package main

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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

// handleSchedulerPick selects a traework auth candidate based on the
// panel-selected active account. Non-traework candidates are always deferred
// (Handled: false) so the built-in scheduler handles them. When every
// traework candidate is exhausted or cooling down, the pick is deferred too
// (Handled: false) so the built-in scheduler can fail over to other
// providers' accounts — the plugin never answers with a dead candidate.
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	if loadedSchedulerMode() != schedulerModeCredits && loadedSchedulerMode() != schedulerModeSession {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	// Collect traework candidates only. Accounts in failover cooldown are
	// skipped so new requests route to a healthy account instead. If EVERY
	// traework account is cooling down, defer (Handled: false) so the host's
	// built-in scheduler can fail over to OTHER providers' accounts (e.g.
	// workbuddy) instead of pinning the request to a dead account.
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
	// Hard exclusion: 「测试」标签 (scheduled active ping failed while credits
	// remained) / 冷却 (failover cooldown) accounts never carry traffic. Both
	// are "not available" by product definition, so they are filtered
	// unconditionally — there is NO "keep the full list" fallback: when every
	// candidate is excluded we defer (Handled: false) so the host's built-in
	// scheduler can fail over to OTHER providers' accounts (e.g. workbuddy)
	// instead of pinning the request to an account we just declared unusable.
	// 冷却判定带模型维度：账号×模型级冷却只应挡住同一模型的请求，
	// 否则一次限流就会把该账号从其它模型上一并摘掉。调度请求的 Model 是
	// 客户端模型名，需与记账侧一样归一化后再比对。
	upstreamModel := circuitModel(stripProviderPrefix(req.Model))
	// 轮换冷却在循环外取一次时钟：同一次调度里所有候选必须用同一个「现在」，
	// 否则逐个取时间会让先判的候选与后判的候选处在不同时刻，短窗口判定抖动。
	rotationNow := time.Now()
	available := make([]pluginapi.SchedulerAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		if isTestFailed(c.ID) || isAuthModelCoolingDown(c.ID, upstreamModel) {
			continue
		}
		// 轮换让位与故障冷却同属「暂时不该接流量」，但成因完全不同：前者是
		// 负载均衡策略（用满 N 分钟主动让位），后者是故障隔离。两者都必须被
		// 调度层跳过，否则轮换只写状态却仍被派流量，机制等于没接上。
		if rotationCoolingDown(c.ID, rotationNow) {
			continue
		}
		// 慢速冷却：该号在这个模型上显著慢于同池水平时让位。同为独立生命周期，
		// 且只影响当前模型——该号在其它模型上仍可正常承流。
		if slowCoolingDown(c.ID, upstreamModel, rotationNow) {
			continue
		}
		if _, lowOrExhausted := cachedCreditsScore(c.ID); lowOrExhausted {
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
	//
	// 保活档（低于 min_routable_credits 但高于禁用线）不再硬排除，只是排在
	// normal 之后：这是放宽而不是收紧——0.3.0 时期这些号会被直接踢出池子，
	// 现在它们仍有承接机会，只是不优先。
	preserveBelow := loadedMinRoutableCredits()
	preserveRank := func(id string) int {
		remain, _ := cachedCreditsScore(id)
		if creditTierFor(remain, loadedLowCreditDisableBelow(), preserveBelow) == creditTierPreserve {
			return 1
		}
		return 0
	}
	sort.SliceStable(wbCandidates, func(i, j int) bool {
		leftRank, rightRank := preserveRank(wbCandidates[i].ID), preserveRank(wbCandidates[j].ID)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		left, _ := cachedCreditsScore(wbCandidates[i].ID)
		right, _ := cachedCreditsScore(wbCandidates[j].ID)
		if (left < 0) != (right < 0) {
			return left >= 0
		}
		return left > right
	})

	// Build thin view for active-auth picker. Exhausted accounts are passed
	// through with Exhausted=true (rather than pre-filtered) so the picker can
	// skip them and still report "no healthy account" when they are all spent.
	cands := make([]activeAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		_, exhausted := cachedCreditsScore(c.ID)
		cands = append(cands, activeAuthCandidate{
			ID:        c.ID,
			Disabled:  false, // already filtered
			Exhausted: exhausted,
		})
	}
	var picked string
	if loadedSchedulerMode() == schedulerModeSession {
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
