// scheduler.go implements the CPA scheduler.pick capability for workbuddy.
//
// Routing uses the panel-selected active account (region from that card's
// domain). When the selection is exhausted/disabled/missing, randomly switch
// to another non-exhausted workbuddy candidate. Non-workbuddy candidates are
// always deferred so the built-in scheduler handles them, and when EVERY
// workbuddy candidate is exhausted/cooling-down the pick is deferred as well
// (Handled: false) so the host can fail over to other providers' accounts.
//
// Only "available" accounts participate: 「测试」标签 (test_failed) and 冷却
// (failover cooldown) are hard-excluded — they are unusable by product
// definition, so there is no fallback that re-admits them. Survivors are
// ordered HIGH-CREDIT-FIRST: the healthiest accounts (most remaining credits)
// lead and unmeasured (-1) accounts rank last, so load spreads across healthy
// accounts instead of burning the weakest down first.
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

// handleSchedulerPick 根据面板选中的活动账号挑选 workbuddy 候选。
// 非 workbuddy 候选一律延迟（Handled: false），交给宿主内置调度器处理。
//
// scheduler_mode 取值：
//   - "off"：插件不接管路由，全部交给宿主。
//   - "credits"：按面板选中的活动账号挑选，失效时回退。
//   - "session"：按会话路由，同一会话粘在一个账号上，不同会话分散；
//     无会话标识的请求回落到面板选中账号。
//
// 默认 off。需要在插件配置里把 scheduler_mode 设为 credits 或 session 才会接管。
// [参数] raw：SchedulerPickRequest 的 JSON 原文。
// [返回] 挑选结果的响应 JSON；不接管时返回 Handled:false。
// 最近修改时间：2026-10-23；改动原因：积分判据改为三档，并新增保活档整体让位。
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	// 1. scheduler_mode 未开启时直接让位给宿主。
	mode := loadedSchedulerMode()
	if mode != schedulerModeCredits && mode != schedulerModeSession {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	// 2. 只保留本插件的候选，宿主侧已禁用的直接跳过。
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

	// 3. 排除「测试」标签、账号×模型冷却、轮换冷却、以及低于禁用线/已耗尽的账号。
	// 全部被排除时延迟给宿主，让宿主切到其它 provider，而不是钉死在不可用账号上。
	upstreamModel := resolveUpstreamModel(req.Model, nil)
	rotationNow := time.Now()
	available := make([]pluginapi.SchedulerAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		if isTestFailed(c.ID) || isAuthModelCoolingDown(c.ID, upstreamModel) {
			continue
		}
		// 轮换冷却：连续使用满阈值的号主动让位，让新会话落到其它账号。
		// 这里与 isAuthModelCoolingDown 并列而非合并，因为两者生命周期不同——
		// 失败冷却会被一次成功清零，轮换冷却必须稳定持续到窗口结束。
		if rotationCoolingDown(c.ID, rotationNow) {
			continue
		}
		// 三档语义（2026-10-23）：只有低于硬禁用线或已耗尽的号才真正摘出；
		// 禁用线与保活线之间的号仍可路由，只是被下面的档位过滤降到最后。
		if _, lowOrExhausted := cachedCreditsScore(c.ID); lowOrExhausted {
			continue
		}
		available = append(available, c)
	}
	if len(available) == 0 {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	wbCandidates = available

	// 4. 保活档整体让位：存在正常档候选时，保活档不参与本次挑选。
	//
	// 用过滤而非排序：pickActiveAuth 会尊重面板选中，保活号若正好被选中，
	// 排序压不住它；过滤后保活档只在正常档全空时兜底，才是「优先级最低」。
	if normal := filterCandidatesByTier(wbCandidates, creditTierNormal); len(normal) > 0 {
		wbCandidates = normal
	}

	// 5. 同档内按积分降序平摊：未测 (-1) 排最后。
	sort.SliceStable(wbCandidates, func(i, j int) bool {
		left, _ := cachedCreditsScore(wbCandidates[i].ID)
		right, _ := cachedCreditsScore(wbCandidates[j].ID)
		if (left < 0) != (right < 0) {
			return left >= 0
		}
		return left > right
	})

	// 6. 转成挑选器需要的瘦视图。
	cands := make([]activeAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		_, exhausted := cachedCreditsScore(c.ID)
		cands = append(cands, activeAuthCandidate{
			ID:        c.ID,
			Disabled:  false, // already filtered
			Exhausted: exhausted,
		})
	}
	// 7. 会话模式按会话粘性挑，其余按面板选中挑；挑不出则延迟给宿主。
	var picked string
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

// candidateDisabled 判定宿主侧是否已禁用该账号（Status / metadata）。
// [参数] c：调度候选。
// [返回] true 表示已禁用。
// 最近修改时间：2026-10-23；改动原因：补函数头元信息。
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

// filterCandidatesByTier 返回属于指定档位的候选。
// [参数] candidates：候选列表；tier：目标档位。
// [返回] 新切片，不修改入参。
// 最近修改时间：2026-10-23；改动原因：新增，保活档整体让位需要按档位过滤。
func filterCandidatesByTier(candidates []pluginapi.SchedulerAuthCandidate, tier creditTier) []pluginapi.SchedulerAuthCandidate {
	out := make([]pluginapi.SchedulerAuthCandidate, 0, len(candidates))
	for _, c := range candidates {
		if authCreditTier(c.ID) == tier {
			out = append(out, c)
		}
	}
	return out
}

// authCreditTier 返回账号当前所属积分档位，供排序把保活档整体沉底。
//
// 优先读 sidecar 快照：它是跨重启存活的唯一真相源，且已经固化了分级结果，
// 不必在这里重算阈值。快照缺失时退回内存缓存现算，两者都没有则按正常档处理
// （未知不等于没钱，误降级会让健康号被无谓地排到最后）。
// [参数] authID：账号标识。
// [返回] 该账号的档位。
// 最近修改时间：2026-10-23；改动原因：新增，三档分级的排序唯一入口。
func authCreditTier(authID string) creditTier {
	if entry, ok := creditSnapshotGet(authID); ok && entry.Tier != "" {
		return entry.Tier
	}
	remain, lowOrExhausted := cachedCreditsScore(authID)
	if lowOrExhausted {
		return creditTierDisabled
	}
	return creditTierFor(remain, loadedLowCreditDisableBelow(), loadedMinRoutableCredits())
}

// cachedCreditsScore returns (remain, exhaustedOrLow) from accountCache.
// remain is -1 when unknown; exhaustedOrLow uses isCreditsLowOrExhausted.
// Key is auth.ID (same as SchedulerAuthCandidate.ID and activeAuthID), with
// normalized fallback resolution across key formats.
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
