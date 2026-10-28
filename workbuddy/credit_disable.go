// credit_disable.go 把「积分低于硬禁用线」落成宿主认识的禁用状态，并在积分回升
// 且探活通过后解除。它是三档分级里唯一会写 auth 文件的落点。
//
// 与既有两套禁用机制的边界（本文件最重要的约束）：
//   - manual_disable（用户手动面板开关）：一律不接管、不解除。
//   - failover_disable（失败探活）：一律不接管、不解除；两者可能同时存在，
//     各自独立判定，谁也不会替对方解禁。
//   - exhausted_disable（额度耗尽 lifecycle）：旧路径，本文件不读也不写。
//
// 恢复为什么必须探活：积分只证明「账户里还有钱」，不证明「这个账号还能用」。
// 一个 token 已死的账号积分可能仍然是满的，直接放回路由只会让下一个请求
// 又失败一次。因此恢复条件是「积分 > 禁用线 且 探活成功」，缺一不可。
//
// 最近修改时间：2026-10-23；改动原因：新增，三档分级中的硬禁用落点。
package main

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// creditDisableKey 系列是本机制拥有的三个顶层键：标记表示归属，
	// below 记录禁用时的阈值（便于人工判断为什么被禁），at 供诊断。
	creditDisableKey      = "low_credit_disable"
	creditDisableBelowKey = "low_credit_disable_below"
	creditDisableAtKey    = "low_credit_disable_at"
)

var (
	// creditDisableMu 保护 creditDisableProbe 这张「本轮探活结论」表。
	// 键为归一化账号标识，值表示该账号在本轮扫描里探活是否成功。
	creditDisableMu sync.Mutex
	// creditDisableProbe 缓存本轮探活结果，避免同一次分级检查重复打上游。
	// 它是内存加速，不是真相源：真相源是盘上的积分余额与 disabled 状态。
	creditDisableProbe = map[string]bool{}

	// 注入点：生产用真实实现，单测替换为假宿主。
	creditDisablePingFn = sendActivePingWorkbuddy
)

// lowCreditDisableFromAuthJSON 读取本机制的顶层标记。
// [参数] raw：物理 auth JSON 原文。
// [返回] true 表示该账号由本机制禁用。
// 最近修改时间：2026-10-23；改动原因：新增。
func lowCreditDisableFromAuthJSON(raw []byte) bool {
	// 1. 只解析本机制的顶层标记，其余字段忽略。
	var m struct {
		LowCreditDisable bool `json:"low_credit_disable"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.LowCreditDisable
}

// gradeCreditsForAuth 对单个账号执行一次三档分级检查，并在必要时写盘。
//
// 判定顺序（每一步都必须先确认「这号不归别人管」，否则会互相解禁）：
//  1. 积分未知 → 不动。
//  2. 低于禁用线 → 若盘上未被他人禁用，则写盘禁用。
//  3. 高于禁用线且盘上由本机制禁用 → 探活成功才解禁；探活失败保持禁用。
//  4. 其余 → 只更新 note，不改 disabled。
//
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：凭据；
// cr：本次拉到的积分（不可为 nil）。
// [返回] 该账号当前所属档位。
// 最近修改时间：2026-10-23；改动原因：新增，三档分级的唯一执行点。
func gradeCreditsForAuth(authIndex, authID string, sa *storedAuth, cr *creditsSummary) creditTier {
	if cr == nil {
		return creditTierNormal
	}
	disableBelow := loadedLowCreditDisableBelow()
	tier := creditTierFor(cr.TotalRemain, disableBelow, loadedMinRoutableCredits())

	// 1. 禁用线关闭时，本机制整体不动作（0 = kill switch）。
	if disableBelow <= 0 {
		return tier
	}

	phys, errPhys := failoverGetPhysicalFn(authIndex)
	if errPhys != nil || phys == nil || len(phys.JSON) == 0 {
		// 读不到物理文档时只回档位、不写盘：没有真相就无从判断该不该动它。
		return tier
	}
	disabled := parseDisabledFromAuthJSON(phys.JSON)
	manual := manualDisableFromAuthJSON(phys.JSON)
	failover := parseFailoverAuthDoc(phys.JSON).failoverDisable
	ours := lowCreditDisableFromAuthJSON(phys.JSON)

	// 2. 低于禁用线：仅在无人认领时写盘。
	if tier == creditTierDisabled {
		if disabled {
			// 已被禁用：谁的禁用谁负责。manual / failover 的号不归本机制管。
			return tier
		}
		if err := disableForLowCredits(authIndex, authID, sa, cr, disableBelow); err != nil {
			log.Printf("[workbuddy] low-credit disable failed: auth=%s err=%v", authID, err)
		}
		return tier
	}

	// 3. 积分回到禁用线以上：只有本机制禁的号才考虑解禁。
	if !disabled || !ours || manual || failover {
		return tier
	}
	if !creditProbeOK(authIndex, authID, sa) {
		log.Printf("[workbuddy] low-credit re-enable deferred: probe failed auth=%s remain=%d", authID, cr.TotalRemain)
		return tier
	}
	if err := reenableForLowCredits(authIndex, authID, sa, cr); err != nil {
		log.Printf("[workbuddy] low-credit re-enable failed: auth=%s err=%v", authID, err)
	}
	return tier
}

// creditProbeOK 判定账号本轮探活是否成功。
//
// 探活目标是「账号还能不能用」，与失败探活不同：这里没有「失败模型」的概念，
// 任一候选模型成功即视为可用。逐个模型串行尝试的代价由 sendActivePingWorkbuddy
// 自身的超时兜底，不需要在这里再加预算。
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：凭据。
// [返回] true 表示至少一个模型探通。
// 最近修改时间：2026-10-23；改动原因：新增。
func creditProbeOK(authIndex, authID string, sa *storedAuth) bool {
	// 1. 归一化键；空键无从缓存，直接判不可用。
	key := normalizeFailoverKey(authID)
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(authID))
	}
	if key == "" {
		return false
	}
	// 2. 命中本轮缓存则直接复用，避免同一轮重复打上游。
	creditDisableMu.Lock()
	if ok, seen := creditDisableProbe[key]; seen {
		creditDisableMu.Unlock()
		return ok
	}
	creditDisableMu.Unlock()

	// 3. 任一模型探通即视为账号可用，命中后立即停止。
	models := orderedAllWorkbuddyModels(sa)
	ok := false
	for _, m := range models {
		if err := creditDisablePingFn(sa, m); err == nil {
			ok = true
			break
		}
	}
	// 4. 结果写入缓存，同一轮内后续调用不再打上游。
	creditDisableMu.Lock()
	creditDisableProbe[key] = ok
	creditDisableMu.Unlock()
	_ = authIndex
	return ok
}

// forgetCreditProbe 清掉账号的探活缓存，供解禁后与测试清场使用。
// [参数] authID：账号标识。
// [返回] 无。
// 最近修改时间：2026-10-23；改动原因：新增。
func forgetCreditProbe(authID string) {
	// 1. 归一化键后从缓存删除。
	key := normalizeFailoverKey(authID)
	if key == "" {
		return
	}
	creditDisableMu.Lock()
	delete(creditDisableProbe, key)
	creditDisableMu.Unlock()
}

// disableForLowCredits 写盘禁用：disabled:true + 三个自有键。
//
// 走「读物理文件 → 改顶层键 → 整体写回」，不用 host.auth.save：
// 后者会重建记录并把状态硬编码回 active，等于把刚写的禁用又抹掉。
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：凭据；
// cr：触发禁用的积分；below：当时的禁用线。
// [返回] 写盘错误。
// 最近修改时间：2026-10-23；改动原因：新增。
func disableForLowCredits(authIndex, authID string, sa *storedAuth, cr *creditsSummary, below int64) error {
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	// 1. 读取物理文档；读不到就放弃写盘。
	phys, err := failoverGetPhysicalFn(authIndex)
	if err != nil || phys == nil || len(phys.JSON) == 0 {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil || doc == nil {
		return err
	}
	// 2. 拿到文档锁后重判一次：另一条路径可能刚把号禁掉。
	if parseDisabledFromAuthJSON(phys.JSON) {
		return nil
	}
	// 3. 写入禁用状态与三个自有标记，note 同步展示原因。
	doc["disabled"] = true
	doc[creditDisableKey] = true
	doc[creditDisableBelowKey] = below
	doc[creditDisableAtKey] = time.Now().UTC().Format(time.RFC3339)
	doc["note"] = displayNote(sa, cr, true)

	// 4. 落盘并同步内存状态，最后异步确认宿主已接受新状态。
	//    优先经宿主管理 API 写进路由真相源；未配置密钥或调用失败时回落到直写
	//    物理文件，保证排除不会完全失效。
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name, path, legacy := resolveAuthFileTarget(sa, phys)
	apiErr := setHostAuthDisabled(name, authIndex, true)
	if apiErr != nil {
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[workbuddy] cpa disable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
		}
		if err := failoverPersistFn(name, path, legacy, raw); err != nil {
			return err
		}
	}
	rememberLifecycleState(authID, true, displayNote(sa, cr, true))
	if apiErr == nil {
		log.Printf("[workbuddy] low-credit disabled via cpa api: auth=%s remain=%d below=%d", authID, cr.TotalRemain, below)
		return nil
	}
	log.Printf("[workbuddy] low-credit disabled: auth=%s remain=%d below=%d", authID, cr.TotalRemain, below)
	go confirmFailoverHostState(failoverWaitConfirmed, authIndex, true, authID, path)
	return nil
}

// reenableForLowCredits 解除本机制写入的禁用。
//
// 只认自己的标记，且 manual / failover 任一存在时都不解除——那些号有自己的
// 恢复路径，被本机制抢先放开会让它们绕过各自的校验。
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：凭据；cr：恢复时的积分。
// [返回] 写盘错误。
// 最近修改时间：2026-10-23；改动原因：新增。
func reenableForLowCredits(authIndex, authID string, sa *storedAuth, cr *creditsSummary) error {
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	// 1. 读取物理文档并复核归属：只解本机制禁的号。
	phys, err := failoverGetPhysicalFn(authIndex)
	if err != nil || phys == nil || len(phys.JSON) == 0 {
		return err
	}
	if !lowCreditDisableFromAuthJSON(phys.JSON) ||
		manualDisableFromAuthJSON(phys.JSON) ||
		parseFailoverAuthDoc(phys.JSON).failoverDisable {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil || doc == nil {
		return err
	}
	// 2. 清掉禁用状态与三个自有标记。
	doc["disabled"] = false
	doc["note"] = displayNote(sa, cr, false)
	delete(doc, creditDisableKey)
	delete(doc, creditDisableBelowKey)
	delete(doc, creditDisableAtKey)

	// 3. 落盘并同步内存状态。恢复同样优先走宿主管理 API：宿主真相源里的
	//    disabled 不置回 false，宿主会继续挡住这个号，插件侧解除等于空转。
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name, path, legacy := resolveAuthFileTarget(sa, phys)
	apiErr := setHostAuthDisabled(name, authIndex, false)
	if apiErr != nil {
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[workbuddy] cpa enable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
		}
		if err := failoverPersistFn(name, path, legacy, raw); err != nil {
			return err
		}
	}
	// 4. 清掉内存冷却与探活缓存，避免「盘上可用、插件仍排除」。
	resetCooldownState(authID)
	forgetCreditProbe(authID)
	rememberLifecycleState(authID, false, displayNote(sa, cr, false))
	if apiErr == nil {
		log.Printf("[workbuddy] low-credit re-enabled via cpa api: auth=%s remain=%d", authID, cr.TotalRemain)
		return nil
	}
	log.Printf("[workbuddy] low-credit re-enabled: auth=%s remain=%d", authID, cr.TotalRemain)
	go confirmFailoverHostState(failoverWaitConfirmed, authIndex, false, authID, path)
	return nil
}

// lowCreditMarkersFromJSON 取出盘上已有的本机制顶层键。
//
// 为什么必须搬运：lifecycle 的 disableAuth / syncAuthNote 走 buildAuthFileJSON
// （白名单重建，只输出固定几个键 + extra）。若账号正被本机制禁用，重建会把
// low_credit_disable 抹掉——号仍是 disabled，但分级检查按标记判断「是不是我禁的」，
// 从此再也找不到它，账号永久卡在禁用态且无法自动恢复（静默遗忘）。
// [参数] raw：物理 auth JSON 原文。
// [返回] 需要并入 extra 的键值；账号不属于本机制时返回 nil。
// 最近修改时间：2026-10-23；改动原因：新增，防止白名单重建抹掉禁用标记。
func lowCreditMarkersFromJSON(raw []byte) map[string]any {
	// 1. 解析失败或不是本机制禁用的号，直接返回空。
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil
	}
	if doc[creditDisableKey] != true {
		return nil
	}
	// 2. 搬运三个自有键，其它顶层键不碰。
	out := map[string]any{creditDisableKey: true}
	for _, k := range []string{creditDisableBelowKey, creditDisableAtKey} {
		if v, ok := doc[k]; ok {
			out[k] = v
		}
	}
	return out
}

// putLowCreditMarkersIntoExtra 把盘上已有的本机制键并入 extra 映射。
// [参数] extra：待补全的映射，可为 nil；raw：物理 auth JSON 原文。
// [返回] 合并后的映射（无标记时原样返回 extra）。
// 最近修改时间：2026-10-23；改动原因：新增。
func putLowCreditMarkersIntoExtra(extra map[string]any, raw []byte) map[string]any {
	// 1. 取出盘上标记；不是本机制禁用的号原样返回。
	markers := lowCreditMarkersFromJSON(raw)
	if len(markers) == 0 {
		return extra
	}
	// 2. 按需新建映射后逐个并入。
	if extra == nil {
		extra = make(map[string]any, len(markers))
	}
	for k, v := range markers {
		extra[k] = v
	}
	return extra
}

// resetCreditProbeForTest 清空探活缓存，仅供测试使用。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-23；改动原因：新增。
func resetCreditProbeForTest() {
	creditDisableMu.Lock()
	creditDisableProbe = map[string]bool{}
	creditDisableMu.Unlock()
}
