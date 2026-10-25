// usage_rotation.go 实现「单账号连续使用满 N 分钟后强制让位」的轮换机制。
//
// 定位：这是 accountFailover.go 的姊妹模块，但状态完全独立。失败冷却会在下一次
// 成功请求时被 resetAccountFailover 清零，用它承载「用满 1 小时就让位」会让冷却
// 被第一次成功立刻抹掉，因此这里另开一份状态。任何把轮换状态塞进 failoverStates
// 的改动都会让轮换在首个成功请求后失效。
//
// 为什么轮换能突破宿主的会话亲和：CPA 的 SessionAffinitySelector 命中缓存时用
// 跨 priority 层的候选集校验绑定，priority 降级对已绑定会话无效。本机制不依赖
// 宿主 selector，而是在 scheduler.pick 与请求入口主动换号，因此对已绑定会话同样生效。
//
// 计时语义（连续使用）：账号首次承载请求时打点，此后每次请求续期；若距离上次
// 使用超过 rotationIdleReset 则视为中断，重新计时。这样零散流量不会误伤，
// 只有真正持续承流的账号才会被轮换。
//
// 防抽干：决定冷却某账号前先检查「除它以外是否全部已冷却」，若是则放开一半账号
// 的冷却。否则全池同时冷却会让调度无号可选，机制反而失效。
//
// 状态只存内存：不写 auth 文件、不写 sidecar、不写 DB，进程重启即清空。
// 轮换是「负载均衡策略」而不是「故障状态」，重启后重新计时是正确语义。
//
// 最近修改时间：2026-10-25；改动原因：从 qoderwork/usage_rotation.go 移植，
// 禁用判据改为同时认本地标记与 sidecar。
package main

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// 默认值。全部可由插件配置覆盖，显式填 0 表示关闭对应能力。
const (
	// defaultRotationEnabled 是总开关默认值：开启。上游对单账号长时间承流有
	// 频率限制，默认轮换可把负载摊到全池。
	defaultRotationEnabled = true
	// defaultRotationServeMinutes 是单账号连续承流多久后让位（分钟）。
	defaultRotationServeMinutes = 60
	// defaultRotationCooldownMinutes 是让位后停留在冷却的时长（分钟）。
	defaultRotationCooldownMinutes = 60
	// defaultRotationIdleResetMinutes 是「中断多久算重新开始计时」（分钟）。
	defaultRotationIdleResetMinutes = 10
)

var (
	// rotationEnabled 是总开关，默认开启；关闭需显式配置 rotation_enabled: false。
	rotationEnabled   = defaultRotationEnabled
	rotationEnabledMu sync.RWMutex

	// rotationServeMinutes 对应 rotation_serve_minutes。
	rotationServeMinutes = defaultRotationServeMinutes
	// rotationCooldownMinutes 对应 rotation_cooldown_minutes。
	rotationCooldownMinutes = defaultRotationCooldownMinutes
	// rotationIdleResetMinutes 对应 rotation_idle_reset_minutes。
	rotationIdleResetMinutes = defaultRotationIdleResetMinutes
	rotationConfigMu         sync.RWMutex

	// rotationMu 保护 rotationStates。
	rotationMu     sync.Mutex
	rotationStates = make(map[string]*rotationState)

	// rotationPoolFn 是池视图来源，生产走宿主账号列表，单测可注入。
	rotationPoolFn = rotationPoolIDs
)

// rotationState 是单个账号的轮换状态。
type rotationState struct {
	// firstUsedAt 是本轮连续使用的起点。
	firstUsedAt time.Time
	// lastUsedAt 是最近一次承载请求的时间，用于判定中断。
	lastUsedAt time.Time
	// cooldownUntil 非零表示该账号因轮换而处于冷却中。
	cooldownUntil time.Time
}

// rotationConfig 是一次读取得到的配置快照，避免判定过程中反复取锁。
type rotationConfig struct {
	enabled  bool
	serve    time.Duration
	cooldown time.Duration
	idle     time.Duration
}

// setLoadedRotationConfig 写入轮换配置，供 configure 调用。
// 非正数的 serve / cooldown 视为关闭轮换（与显式 false 等价）。
// [参数] enabled：总开关；serve/cooldown/idle：分钟数。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，配置入口。
func setLoadedRotationConfig(enabled bool, serve, cooldown, idle int) {
	rotationEnabledMu.Lock()
	rotationEnabled = enabled
	rotationEnabledMu.Unlock()
	rotationConfigMu.Lock()
	rotationServeMinutes = serve
	rotationCooldownMinutes = cooldown
	rotationIdleResetMinutes = idle
	rotationConfigMu.Unlock()
}

// loadedRotationConfig 读取当前配置快照。
// [参数] 无。
// [返回] 配置快照；serve/cooldown 非正数时返回 enabled=false，等同关闭。
// 最近修改时间：2026-10-25；改动原因：新增，轮换判定的唯一配置入口。
func loadedRotationConfig() rotationConfig {
	rotationEnabledMu.RLock()
	enabled := rotationEnabled
	rotationEnabledMu.RUnlock()

	rotationConfigMu.RLock()
	serve := rotationServeMinutes
	cooldown := rotationCooldownMinutes
	idle := rotationIdleResetMinutes
	rotationConfigMu.RUnlock()

	cfg := rotationConfig{
		enabled:  enabled,
		serve:    time.Duration(serve) * time.Minute,
		cooldown: time.Duration(cooldown) * time.Minute,
		idle:     time.Duration(idle) * time.Minute,
	}
	if cfg.serve <= 0 || cfg.cooldown <= 0 {
		cfg.enabled = false
	}
	return cfg
}

// rotationKeysFor 返回该账号在轮换表里可能出现的全部键（原文 + 归一化）。
// 与 failoverKeysLocked 同口径，保证调度器与执行入口用不同形态的 ID 时能命中同一状态。
// [参数] authID：账号标识。
// [返回] 去重后的键列表，首项恒为原始 ID（可能为空串时返回空切片）。
// 最近修改时间：2026-10-25；改动原因：新增，屏蔽宿主 auth 标识多形态差异。
func rotationKeysFor(authID string) []string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	keys := []string{authID}
	if norm := normalizeFailoverKey(authID); norm != "" && norm != authID {
		keys = append(keys, norm)
	}
	return keys
}

// rotationStateLocked 返回该账号的状态指针，不存在时按需创建。
// 调用方必须持有 rotationMu。
// [参数] authID：账号标识；create：不存在时是否创建。
// [返回] 状态指针；create=false 且不存在时返回 nil。
// 最近修改时间：2026-10-25；改动原因：新增，收敛多键映射的重复查找。
func rotationStateLocked(authID string, create bool) *rotationState {
	var first *rotationState
	for _, key := range rotationKeysFor(authID) {
		if st, ok := rotationStates[key]; ok {
			if first == nil {
				first = st
				continue
			}
			// 多键指向不同状态时合并到最早找到的那个，避免同一账号双份计时。
			if st.firstUsedAt.Before(first.firstUsedAt) || first.firstUsedAt.IsZero() {
				first.firstUsedAt = st.firstUsedAt
			}
			if st.lastUsedAt.After(first.lastUsedAt) {
				first.lastUsedAt = st.lastUsedAt
			}
			if st.cooldownUntil.After(first.cooldownUntil) {
				first.cooldownUntil = st.cooldownUntil
			}
			rotationStates[key] = first
		}
	}
	if first != nil {
		return first
	}
	if !create {
		return nil
	}
	st := &rotationState{}
	for _, key := range rotationKeysFor(authID) {
		rotationStates[key] = st
	}
	return st
}

// markAuthUsed 记录一次成功请求的落点，用于推进连续使用计时。
//
// 只在请求确实由该账号服务后调用；失败换号不推进计时，避免把重试打成「持续使用」。
// 到达轮换条件时不在这里冷却，交由 evaluateRotation 统一判定，
// 因为冷却决策需要看到全池状态。
// [参数] authID：实际承载请求的账号标识；now：当前时间，便于测试注入。
// [返回] 本次请求后该账号已连续使用的时长；未启用或标识为空时返回 0。
// 最近修改时间：2026-10-25；改动原因：新增，连续使用打点唯一入口。
func markAuthUsed(authID string, now time.Time) time.Duration {
	cfg := loadedRotationConfig()
	if !cfg.enabled {
		return 0
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return 0
	}
	rotationMu.Lock()
	defer rotationMu.Unlock()
	st := rotationStateLocked(authID, true)
	if st == nil {
		return 0
	}
	// 中断超过 idle 阈值视为新一轮连续使用。
	if !st.lastUsedAt.IsZero() && now.Sub(st.lastUsedAt) > cfg.idle {
		st.firstUsedAt = now
	}
	if st.firstUsedAt.IsZero() {
		st.firstUsedAt = now
	}
	st.lastUsedAt = now
	return now.Sub(st.firstUsedAt)
}

// rotationServingMinutes 返回账号本轮已连续使用多久（分钟）。
// 冷却中的账号不计入连续使用，返回 0，避免面板显示「冷却中仍在计时」。
// [参数] authID：账号标识；now：当前时间。
// [返回] 连续使用分钟数；无记录或冷却中返回 0。
// 最近修改时间：2026-10-25；改动原因：新增，面板与判定共用的时长口径。
func rotationServingMinutes(authID string, now time.Time) int {
	rotationMu.Lock()
	defer rotationMu.Unlock()
	st := rotationStateLocked(authID, false)
	if st == nil || st.firstUsedAt.IsZero() || now.Before(st.cooldownUntil) {
		return 0
	}
	return int(now.Sub(st.firstUsedAt).Minutes())
}

// rotationCoolingDown 判定账号是否因轮换处于冷却中。
//
// 轮换是账号级的，与模型无关：一个账号被让位后，它承载的所有模型都应让位，
// 否则同一账号会在别的模型上继续累积负载，轮换目标落空。
// [参数] authID：账号标识；now：当前时间。
// [返回] true 表示仍在轮换冷却期内。
// 最近修改时间：2026-10-25；改动原因：新增，路由层排除的唯一判据。
func rotationCoolingDown(authID string, now time.Time) bool {
	cfg := loadedRotationConfig()
	if !cfg.enabled {
		return false
	}
	rotationMu.Lock()
	defer rotationMu.Unlock()
	st := rotationStateLocked(authID, false)
	if st == nil {
		return false
	}
	return now.Before(st.cooldownUntil)
}

// rotationCooldownUntil 返回账号本次轮换冷却的截止时刻，供面板倒计时显示。
// [参数] authID：账号标识。
// [返回] 截止时刻与是否存在；不在冷却中时返回 false。
// 最近修改时间：2026-10-25；改动原因：新增，面板需要显示让位剩余时间。
func rotationCooldownUntil(authID string) (time.Time, bool) {
	rotationMu.Lock()
	defer rotationMu.Unlock()
	st := rotationStateLocked(authID, false)
	if st == nil || st.cooldownUntil.IsZero() {
		return time.Time{}, false
	}
	return st.cooldownUntil, true
}

// rotationCandidateIDs 返回全池账号标识（去重后的原始键），供全冷判定使用。
// [参数] pool：账号标识列表。
// [返回] 去重列表。
// 最近修改时间：2026-10-25；改动原因：新增，全冷检查需要稳定的池视图。
func rotationCandidateIDs(pool []string) []string {
	seen := make(map[string]struct{}, len(pool))
	out := make([]string, 0, len(pool))
	for _, id := range pool {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// releaseSharedCooldownsLocked 在「除目标账号外全池都已冷却」时放开一半账号。
//
// 放开的是冷却剩余时间最长的那些账号（按 cooldownUntil 降序），因为它们的冷却
// 最不可能是短窗口残留；同时清空其连续使用起点，让它们按全新一轮重新计时，
// 否则下一轮会在放开后立刻又触发轮换。
//
// 调用方必须持有 rotationMu。
// [参数] pool：全池账号标识；excluding：即将被冷却的目标账号，不参与放开；
// now：当前时间。
// [返回] 被放开的账号标识列表。
// 最近修改时间：2026-10-25；改动原因：新增，实现防抽干策略。
func releaseSharedCooldownsLocked(pool []string, excluding string, now time.Time) []string {
	excludeNorm := normalizeFailoverKey(excluding)
	type cooling struct {
		id  string
		top time.Time
	}
	var list []cooling
	for _, id := range rotationCandidateIDs(pool) {
		if id == excluding {
			continue
		}
		if excludeNorm != "" && normalizeFailoverKey(id) == excludeNorm {
			continue
		}
		st := rotationStateLocked(id, false)
		if st == nil || !now.Before(st.cooldownUntil) {
			continue
		}
		list = append(list, cooling{id: id, top: st.cooldownUntil})
	}
	if len(list) == 0 {
		return nil
	}
	// 放开一半（向上取整）。
	release := (len(list) + 1) / 2
	sort.SliceStable(list, func(i, j int) bool { return list[i].top.After(list[j].top) })

	released := make([]string, 0, release)
	for i := 0; i < release && i < len(list); i++ {
		st := rotationStateLocked(list[i].id, false)
		if st == nil {
			continue
		}
		st.cooldownUntil = time.Time{}
		st.firstUsedAt = time.Time{}
		st.lastUsedAt = time.Time{}
		released = append(released, list[i].id)
	}
	return released
}

// evaluateRotation 判定是否应让当前账号进入轮换冷却，必要时先放开一半账号。
//
// 只有连续使用达到 serve 阈值才冷却；冷却前做全冷检查，避免把最后一个可用号
// 也关掉导致调度无号可选。
// [参数] authID：刚承载完请求的账号；pool：全池账号标识；now：当前时间。
// [返回] true 表示该账号本次被置入轮换冷却。
// 最近修改时间：2026-10-25；改动原因：新增，轮换决策唯一入口。
func evaluateRotation(authID string, pool []string, now time.Time) bool {
	cfg := loadedRotationConfig()
	if !cfg.enabled {
		return false
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}

	rotationMu.Lock()
	defer rotationMu.Unlock()
	st := rotationStateLocked(authID, false)
	if st == nil || st.firstUsedAt.IsZero() {
		return false
	}
	if now.Before(st.cooldownUntil) {
		return false
	}
	if now.Sub(st.firstUsedAt) < cfg.serve {
		return false
	}

	// 全冷检查：除本账号外的池成员是否已全部冷却。
	othersCooling := 0
	othersTotal := 0
	excludeNorm := normalizeFailoverKey(authID)
	for _, id := range rotationCandidateIDs(pool) {
		if id == authID {
			continue
		}
		if excludeNorm != "" && normalizeFailoverKey(id) == excludeNorm {
			continue
		}
		othersTotal++
		other := rotationStateLocked(id, false)
		if other != nil && now.Before(other.cooldownUntil) {
			othersCooling++
		}
	}
	// 除本账号外全部已冷却（且池里至少还有一个别的号）时，先放开一半再冷却本号，
	// 否则本号一关就是全池冷却，调度层将无号可选。
	if othersTotal > 0 && othersCooling == othersTotal {
		releaseSharedCooldownsLocked(pool, authID, now)
	}

	st.cooldownUntil = now.Add(cfg.cooldown)
	// 冷却结束后重新计时，不继承上一轮的起点。
	st.firstUsedAt = time.Time{}
	st.lastUsedAt = time.Time{}
	return true
}

// rotationPoolIDs 返回参与轮换的全池账号标识。
//
// 只统计当前仍可路由的账号：已禁用的号本就不参与路由，把它们算进「全冷」判定
// 会让放开策略误以为池子还很大，实际可用号早已所剩无几。
// [参数] 无。
// [返回] 可路由账号的标识列表；宿主 RPC 失败时返回 nil（调用方退化为不做全冷检查）。
// 最近修改时间：2026-10-25；改动原因：禁用判据同时认本地标记与 sidecar——只查盘上
// disabled 会把本机制刚禁掉、但宿主尚未感知的号算回池里，全冷判定因此失真。
func rotationPoolIDs() []string {
	files, err := hostAuthList()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(files))
	for _, f := range files {
		if f.Disabled {
			continue
		}
		if failoverStateIsDisabled(f.ID) || isFailoverDisabledLocal(f.ID) {
			continue
		}
		if id := strings.TrimSpace(f.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// noteRotationUsage 是请求成功后的轮换打点入口：推进计时，必要时让位。
//
// 让位判定与计时分开：计时必须在每次成功时推进，让位只在对齐全池视图后决定，
// 因为「是否还有别的号可用」取决于全池当时的状态。全池视图要走宿主 RPC，
// 因此只在连续使用确实达到阈值时才取，避免每个成功请求都多一次宿主调用。
// [参数] authID：实际承载请求的账号标识。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，把宿主 RPC 依赖挡在纯逻辑之外。
func noteRotationUsage(authID string) {
	cfg := loadedRotationConfig()
	if !cfg.enabled {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	now := time.Now()
	if markAuthUsed(authID, now) < cfg.serve {
		return
	}
	if evaluateRotation(authID, rotationPoolFn(), now) {
		pruneRotationCooldowns(now)
		log.Printf("[traework] rotation: account served %v continuously, cooling down for %v: auth=%s",
			cfg.serve, cfg.cooldown, authID)
	}
}

// pruneRotationCooldowns 清理已到期且无计时痕迹的状态，防止 map 无限增长。
// [参数] now：当前时间。
// [返回] 清理的条目数。
// 最近修改时间：2026-10-25；改动原因：新增，与 failover 状态清理同口径。
func pruneRotationCooldowns(now time.Time) int {
	rotationMu.Lock()
	defer rotationMu.Unlock()
	removed := 0
	seen := make(map[*rotationState]struct{}, len(rotationStates))
	idle := loadedRotationConfig().idle
	for key, st := range rotationStates {
		if st == nil {
			delete(rotationStates, key)
			removed++
			continue
		}
		if _, ok := seen[st]; ok {
			delete(rotationStates, key)
			removed++
			continue
		}
		seen[st] = struct{}{}
		if now.Before(st.cooldownUntil) {
			continue
		}
		if !st.lastUsedAt.IsZero() && now.Sub(st.lastUsedAt) <= idle {
			continue
		}
		delete(rotationStates, key)
		removed++
	}
	return removed
}

// clearRotationStates 清空轮换状态，测试入口。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func clearRotationStates() {
	rotationMu.Lock()
	rotationStates = make(map[string]*rotationState)
	rotationMu.Unlock()
}
