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
// 默认禁用线为 0（关闭）：未配置时本文件整体不动作，行为与 0.3.0 完全一致。
//
// 最近修改时间：2026-10-25；改动原因：新增，积分三档分级中的硬禁用落点。
package main

import (
	"encoding/json"
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
	disableBelow := loadedLowCreditDisableBelow()
	tier := creditTierFor(cr.TotalRemain, disableBelow, loadedMinRoutableCredits())

	// 1. 禁用线关闭时，本机制整体不动作（0 = kill switch，与 0.3.0 一致）。
	if disableBelow <= 0 {
		return tier
	}

	phys, errPhys := failoverGetPhysicalFn(strings.TrimSpace(authIndex))
	if errPhys != nil || phys == nil || len(phys.JSON) == 0 {
		// 读不到物理文档时只回档位、不写盘：没有真相就无从判断该不该动它。
		return tier
	}
	disabled := parseDisabledFromAuthJSON(phys.JSON)
	manual := parseFailoverAuthDoc(phys.JSON).manualDisable
	failover := parseFailoverAuthDoc(phys.JSON).failoverDisable
	ours := lowCreditDisableFromAuthJSON(phys.JSON)

	// 2. 低于禁用线：仅在无人认领时写盘。
	if tier == creditTierDisabled {
		if disabled {
			// 已被禁用：谁的禁用谁负责。manual / failover / exhausted 的号不归本机制管。
			return tier
		}
		if err := disableForLowCredits(authIndex, authID, sa, cr, disableBelow); err != nil {
			log.Printf("[traework] low-credit disable failed: auth=%s err=%v", authID, err)
		}
		return tier
	}

	// 3. 积分回到禁用线以上：只有本机制禁的号才考虑解禁。
	if !disabled || !ours || manual || failover {
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
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(phys.Name)
	if name == "" {
		name = authFileNameFor(sa)
	}
	if err := failoverPersistFn(name, phys.Path, "", raw); err != nil {
		return err
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
	if !lowCreditDisableFromAuthJSON(phys.JSON) || doc0.manualDisable || doc0.failoverDisable {
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

	// 3. 落盘（与 disableForLowCredits 同一出口）。
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(phys.Name)
	if name == "" {
		name = authFileNameFor(sa)
	}
	if err := failoverPersistFn(name, phys.Path, "", raw); err != nil {
		return err
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
