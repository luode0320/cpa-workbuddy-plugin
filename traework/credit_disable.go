// credit_disable.go 把「积分低于硬禁用线」落成宿主认识的禁用状态，并在积分
// 回升后解除。它是积分三档分级里唯一会写 auth 文件的落点。
//
// 与既有三套禁用机制的边界（本文件最重要的约束）：
//   - manual_disable（用户手动面板开关）：一律不接管、不解除。
//   - failover_disable（失败探活）：一律不接管、不解除；两者可能同时存在，
//     各自独立判定，谁也不会替对方解禁。
//   - exhausted_disable（额度耗尽 lifecycle）：由 lifecycle.go 独占，本文件
//     只在「积分 > 0 但仍低于禁用线」这一档动作，不碰耗尽档的标记。
//
// 恢复为什么不做探活：积分档位的判据本身就是「还有多少钱」，与账号凭据是否
// 仍然有效无关；失败探活链路（failover_disable）已经独立覆盖凭据失活。若在这里
// 再加探活，一次上游抖动就会让本可恢复的号继续被挡在路由外，而积分回升这个
// 事实已经足以支撑「重新给它排队」——它仍然是排序靠后的保活档。
//
// 默认禁用线为 10：只停用「余量已不足以承载一次请求」的号，禁用线低于保活线
// （min_routable_credits，默认 20），从而保留「正常 / 保活降级 / 停用」三档完整语义。
// 把禁用线设 0 关闭本机制时，只停止新增禁用；本机制历史禁的号仍会在积分刷新时解禁。
//
// 归属必须落盘（2026-11-11 缺陷⑳）：写禁用与写解禁一律「先落盘、再打宿主管理 API」。
// 旧实现 API 成功即 return，标记从不落盘；而宿主重建 auth 文档只保留白名单键，标记
// 再也补不回来，账号于是既无人认领也无人解禁。生产实测 14 个号 remain 100~133 被
// 永久挡在候选池外。现在三键 + 停用 note 同时作为归属判据，note 落在宿主白名单里，
// 即使三键被宿主丢弃也能认领并解禁。
//
// 最近修改时间：2026-11-11；改动原因：修复禁用标记不落盘导致的账号永久孤儿化。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	// creditDisableKey 系列是本机制拥有的三个顶层键：标记表示归属，
	// below 记录禁用时的阈值（便于人工判断为什么被禁），at 供诊断。
	creditDisableKey      = "low_credit_disable"
	creditDisableBelowKey = "low_credit_disable_below"
	creditDisableAtKey    = "low_credit_disable_at"

	// creditDisableNotePrefix 是「积分不足停用」note 的固定开头，作为归属的第二判据。
	// 存在意义：宿主重建 auth 文档时会丢掉白名单外的顶层键（见 failover_state.go
	// 文件头），三键可能全部蒸发；note 在宿主白名单内且能存活，所以只要它还在，
	// 就能认出这个号是本机制停的，避免账号被永久卡在池外。
	creditDisableNotePrefix = "积分不足停用"
)

// lowCreditDisableFromAuthJSON 读取本机制的顶层标记。
// [参数] raw：物理 auth JSON 原文。
// [返回] true 表示该账号由本机制禁用。
// 最近修改时间：2026-10-25；改动原因：新增。
func lowCreditDisableFromAuthJSON(raw []byte) bool {
	var m struct {
		LowCreditDisable bool `json:"low_credit_disable"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.LowCreditDisable
}

// lowCreditDisableFromNote 按 note 指纹判断该文档是否由本机制停用。
//
// 它只认停用文案，不认「积分回升启用」文案：后者表示本机制已经解禁，不能再
// 当成归属依据，否则会把已经自由流通的号重新纳入本机制的解除逻辑。
// [参数] raw：物理 auth JSON 原文。
// [返回] true 表示 note 带本机制停用指纹。
// 最近修改时间：2026-11-11；改动原因：新增，宿主丢键时的第二归属判据。
func lowCreditDisableFromNote(raw []byte) bool {
	var m struct {
		Note string `json:"note"`
	}
	_ = json.Unmarshal(raw, &m)
	return strings.HasPrefix(strings.TrimSpace(m.Note), creditDisableNotePrefix)
}

// gradeCreditsForAuth 对单个账号执行一次三档分级检查，并在必要时写盘。
//
// 判定顺序（每一步都必须先确认「这号不归别人管」，否则会互相解禁）：
//  1. 积分未知 → 不动。
//  2. 禁用线关闭（0）→ 不动，只回档位。
//  3. 低于禁用线 → 若盘上未被他人禁用，则写盘禁用。
//  4. 高于禁用线且盘上由本机制禁用 → 解禁。
//  5. 其余 → 只回档位，不改 disabled。
//
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：凭据，可为 nil；
// cr：本次拉到的积分（可为 nil）。
// [返回] 该账号当前所属档位。
// 最近修改时间：2026-10-25；改动原因：新增，三档分级的唯一执行点。
func gradeCreditsForAuth(authIndex, authID string, sa *traeAuth, cr *traeCredits) creditTier {
	if cr == nil {
		// 积分未知时不做任何动作：没有数据不等于没钱，误判会误禁健康号。
		return creditTierNormal
	}
	disableBelow := effectiveLowCreditDisableBelow()
	tier := creditTierFor(cr.TotalRemain, disableBelow, loadedMinRoutableCredits())

	phys, errPhys := failoverGetPhysicalFn(strings.TrimSpace(authIndex))
	if errPhys != nil || phys == nil || len(phys.JSON) == 0 {
		// 读不到物理文档时只回档位、不写盘：没有真相就无从判断该不该动它。
		return tier
	}
	disabled := parseDisabledFromAuthJSON(phys.JSON)
	manual := parseFailoverAuthDoc(phys.JSON).manualDisable
	failover := parseFailoverAuthDoc(phys.JSON).failoverDisable
	// ours 是归属判据：宿主 API 成功路径曾经不写盘，宿主重建文档时又会丢掉
	// 白名单外的顶层键，三键可能已经不在——但 note 仍带停用指纹，同样算本机制
	// 停的（2026-11-11 生产实证：14 个号 remain 100~133 被永久挡在池外）。
	ours := lowCreditDisableFromAuthJSON(phys.JSON) || lowCreditDisableFromNote(phys.JSON)

	// 2. 低于禁用线且线处于开启状态：仅在无人认领时写盘。
	//
	// disableBelow <= 0 是 kill switch（关闭本机制），此时只跳过「新增禁用」，
	// 绝不能连解禁一起跳过：否则关掉开关后，历史被本机制禁掉的账号再也不会自动
	// 解禁，开关变成单向阀，账号永久留在池外。
	if disableBelow > 0 && tier == creditTierDisabled {
		if disabled {
			// 已被禁用：谁的禁用谁负责。manual / failover / exhausted 的号不归本机制管。
			return tier
		}
		if err := disableForLowCredits(authIndex, authID, sa, cr, disableBelow); err != nil {
			log.Printf("[traework] low-credit disable failed: auth=%s err=%v", authID, err)
		}
		return tier
	}

	// 3. 积分回到禁用线以上（或禁用线已关闭）：只有本机制禁的号才考虑解禁。
	//
	// 这里不把盘上 !disabled 当作「已经解禁」：宿主 API 成功路径可能只改了宿主
	// 真相源而没落盘，盘上是陈旧 false、路由仍然被挡。归属（ours）成立就再走一遍
	// 解禁，写盘是幂等的，note 也会被刷成「积分回升启用」，下次自然不再进来。
	if !ours || manual || failover {
		return tier
	}
	if err := reenableForLowCredits(authIndex, authID, sa, cr); err != nil {
		log.Printf("[traework] low-credit re-enable failed: auth=%s err=%v", authID, err)
	}
	return tier
}

// disableForLowCredits 写盘禁用：disabled:true + 三个自有键。
//
// 走「读物理文件 → 改顶层键 → 整体写回」，不用 host.auth.save：
// 后者会重建记录并丢掉未知顶层字段，等于把刚写的标记抹掉。
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：凭据；
// cr：触发禁用的积分；below：当时的禁用线。
// [返回] 写盘错误。
// 最近修改时间：2026-10-25；改动原因：新增。
func disableForLowCredits(authIndex, authID string, sa *traeAuth, cr *traeCredits, below int64) error {
	authIndex = strings.TrimSpace(authIndex)
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
	// 3. 写入禁用状态与三个自有标记。
	doc["disabled"] = true
	doc[creditDisableKey] = true
	doc[creditDisableBelowKey] = below
	doc[creditDisableAtKey] = time.Now().UTC().Format(time.RFC3339)
	doc["note"] = lowCreditNote(cr, below, true)

	// 4. 落盘。走 failoverPersistFn 而不是 persistAuthDirect：两者生产路径相同
	// （都是直写物理文件，不经 host.auth.save），但前者有测试注入点，与
	// failover_disable.go 的写盘出口保持同一条。
	// 优先经宿主管理 API 写进路由真相源；未配置密钥或调用失败时回落写盘。
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(phys.Name)
	if name == "" {
		name = authFileNameFor(sa)
	}
	// 5. 先把标记落盘，再打宿主管理 API。
	//
	// 顺序不能反过来（2026-11-11 修正，缺陷⑳）：旧实现先调 API，成功就
	// `return nil` 不写盘，结果是宿主真相源里 disabled=true 而盘上没有任何
	// 本机制标记。宿主重建文档只保留白名单键，标记永远补不回来，于是这些号
	// 既没人认领、也没人解禁，余额 100+ 仍被永久挡在候选池外，生产实测 14 个。
	// 落盘是幂等的，宿主 API 成功与否都不该省掉这一步。
	if err := failoverPersistFn(name, phys.Path, "", raw); err != nil {
		// 写盘失败时仍要尝试宿主 API：让路由先挡住这个号，比继续消耗它更安全。
		log.Printf("[traework] low-credit persist failed, still disabling via cpa api: auth=%s err=%v", authID, err)
		apiErr := setHostAuthDisabled(name, authIndex, true)
		if apiErr != nil && !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[traework] cpa disable api failed: auth=%s err=%v", authID, apiErr)
		}
		return err
	}
	if apiErr := setHostAuthDisabled(name, authIndex, true); apiErr != nil {
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			// 盘上标记已就位，路由侧由 failover 判据兜住，不需要再写一次盘。
			log.Printf("[traework] cpa disable api failed, disk marker already written: auth=%s err=%v", authID, apiErr)
		}
	}
	log.Printf("[traework] low-credit disabled: auth=%s remain=%d below=%d", authID, cr.TotalRemain, below)
	return nil
}

// reenableForLowCredits 解除本机制写入的禁用。
//
// 只认自己的标记，且 manual / failover 任一存在时都不解除——那些号有自己的
// 恢复路径，被本机制抢先放开会让它们绕过各自的校验。
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：凭据；cr：恢复时的积分。
// [返回] 写盘错误。
// 最近修改时间：2026-10-25；改动原因：新增。
func reenableForLowCredits(authIndex, authID string, sa *traeAuth, cr *traeCredits) error {
	authIndex = strings.TrimSpace(authIndex)
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	// 1. 读取物理文档并复核归属：只解本机制禁的号。
	phys, err := failoverGetPhysicalFn(authIndex)
	if err != nil || phys == nil || len(phys.JSON) == 0 {
		return err
	}
	doc0 := parseFailoverAuthDoc(phys.JSON)
	// 归属复核与 gradeCreditsForAuth 同口径：三键与停用 note 任一成立即算本机制停的。
	// 只认三键会让「宿主丢键、只剩 note」的号永远走不到解禁（2026-11-11 缺陷⑳）。
	ours := lowCreditDisableFromAuthJSON(phys.JSON) || lowCreditDisableFromNote(phys.JSON)
	if !ours || doc0.manualDisable || doc0.failoverDisable {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil || doc == nil {
		return err
	}
	// 2. 清掉禁用状态与三个自有标记。
	doc["disabled"] = false
	doc["note"] = lowCreditNote(cr, 0, false)
	delete(doc, creditDisableKey)
	delete(doc, creditDisableBelowKey)
	delete(doc, creditDisableAtKey)

	// 3. 落盘（与 disableForLowCredits 同一出口）。恢复优先走宿主管理 API：
	// 宿主真相源里的 disabled 不置回 false，宿主会继续挡住这个号。
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(phys.Name)
	if name == "" {
		name = authFileNameFor(sa)
	}
	// 4. 先清盘上标记，再打宿主管理 API。理由同 disableForLowCredits：
	// API 成功路径若跳过写盘，标记会永远留在陈旧状态，账号再也回不到候选池。
	// 解禁方向的失败代价更大（号被永久挡在池外），所以更不能省这一步。
	if err := failoverPersistFn(name, phys.Path, "", raw); err != nil {
		log.Printf("[traework] low-credit re-enable persist failed, still enabling via cpa api: auth=%s err=%v", authID, err)
		apiErr := setHostAuthDisabled(name, authIndex, false)
		if apiErr != nil && !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[traework] cpa enable api failed: auth=%s err=%v", authID, apiErr)
		}
		return err
	}
	if apiErr := setHostAuthDisabled(name, authIndex, false); apiErr != nil {
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[traework] cpa enable api failed, disk marker already cleared: auth=%s err=%v", authID, apiErr)
		}
	}
	log.Printf("[traework] low-credit re-enabled: auth=%s remain=%d", authID, cr.TotalRemain)
	return nil
}

// lowCreditNote 生成积分档位的 note 文案。
// [参数] cr：积分快照，可为 nil；below：禁用线；disabled：是否正在禁用。
// [返回] note 文本。
// 最近修改时间：2026-10-25；改动原因：新增，面板与人工排查需要可见原因。
func lowCreditNote(cr *traeCredits, below int64, disabled bool) string {
	var remain int64
	if cr != nil {
		remain = cr.TotalRemain
	}
	if disabled {
		return fmt.Sprintf("积分不足停用 · 余%d < 禁用线%d（积分回升后自动启用）", remain, below)
	}
	return fmt.Sprintf("积分回升启用 · 余%d", remain)
}
