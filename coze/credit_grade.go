package main

import (
	"time"
)

// 本文件把「积分低于红线」落成宿主认识的禁用状态，并在积分回升后解除。
// 它是整条巡检链路里唯一会写 auth 文件的落点。
//
// 与其它禁用来源的边界（本文件最重要的约束）：
//   - 用户手动禁用（面板开关 / 批量禁用）：无归属标记，一律不接管、不解禁。
//   - 其它机制写入的 disabled：同样无标记，一律不动。
//   - 只有带 low_credit_disable 标记的账号才归本机制管，可被自动解禁。
//
// 恢复为什么不需要探活：coze 查余额本身就要用有效 Cookie，余额查询成功
// 已经隐含登录态有效；而推理探活会真实消耗积分，每小时对被禁号烧一次
// 与被禁的目的直接矛盾（见设计文档「恢复判据」一节）。

// creditTier 是一条积分快照所属的路由档位。
type creditTier string

const (
	// creditTierDisabled 低于红线：写盘禁用，宿主级停用。
	creditTierDisabled creditTier = "disabled"
	// creditTierNormal 高于或等于红线：正常参与路由。
	creditTierNormal creditTier = "normal"
	// creditTierUnknown 积分未知：不参与任何降级判定。
	creditTierUnknown creditTier = "unknown"
)

// String 返回档位的字符串形式，便于日志与序列化直接使用。
// [参数] 无。
// [返回] 档位字面量（disabled / normal / unknown）。
// 最近修改时间：2026-10-25；改动原因：新增，面板与日志共用。
func (t creditTier) String() string {
	return string(t)
}

// creditTierFor 按阈值把积分余量判成一档。纯函数，边界语义由测试锁定。
//
// 未知积分（负数）一律判 unknown 而不是 disabled：没有数据不等于没钱，
// 误判会把健康账号踢出路由并写盘禁用，恢复还得等下一轮积分到账。
// [参数] remain：积分余量，负数表示未知；redline：硬红线。
// [返回] 该余量所属档位。
// 最近修改时间：2026-10-25；改动原因：新增，分级的唯一判定点。
func creditTierFor(remain, redline int64) creditTier {
	// 1. 未知积分不降级。
	if remain < 0 {
		return creditTierUnknown
	}
	// 2. 低于红线：禁用。边界取严格小于：199 禁用、200 不禁用。
	if redline > 0 && remain < redline {
		return creditTierDisabled
	}
	// 3. 其余正常。
	return creditTierNormal
}

// gradeCreditsForAccount 对单个账号执行一次分级检查，并在必要时写盘。
//
// 判定顺序（每一步都必须先确认「这号归本机制管」，否则会互相解禁）：
//  1. 积分未知 → 不动。
//  2. 红线为 0（kill switch）→ 整体不动作。
//  3. 低于红线 → 若盘上尚未被禁用，则写盘禁用并打标记。
//  4. 高于红线且盘上由本机制禁用 → 清标记解禁。
//  5. 其余 → 不动。
//
// [参数] sa：账号（须带 PhysicalPath）；cr：本次拉到的积分，可为 nil。
// [返回] 该账号本轮所属档位。
// 最近修改时间：2026-10-25；改动原因：新增，分级的唯一执行点。
func gradeCreditsForAccount(sa *StoredAuth, cr *creditSummary) creditTier {
	// 1. 缺少落盘路径时无从判定：没有物理文件就没有可写回的真相。
	if sa == nil || sa.PhysicalPath == "" {
		return creditTierUnknown
	}
	redline := loadedMinRoutableCredits()
	// 2. 积分未知或红线关闭时只回档位，不做任何写盘动作。
	if cr == nil {
		return creditTierUnknown
	}
	tier := creditTierFor(cr.Remain, redline)
	if redline <= 0 || tier == creditTierUnknown {
		return tier
	}
	// 3. 低于红线：写盘禁用（锁内重判，避免覆盖用户刚做的操作）。
	if tier == creditTierDisabled {
		disableForLowCredits(sa, cr, redline)
		return tier
	}
	// 4. 高于红线：只有本机制禁的号才考虑解禁。
	reenableForLowCredits(sa, cr)
	return tier
}

// disableForLowCredits 写盘禁用：disabled=true + 三个归属标记。
//
// 走锁内读改写而不是 writeAuthFileDirect：判定必须基于磁盘当前内容，
// 否则扫描期间用户刚点的「禁用」会被这里的旧判定覆盖成「自动禁用」，
// 次日积分到账后又被自动解禁——用户的操作被静默吞掉。
// [参数] sa：账号；cr：触发禁用的积分；redline：当时的红线。
// [返回] 无（失败只记日志，不阻断本轮其它账号）。
// 最近修改时间：2026-10-25；改动原因：新增，低额禁用的唯一写盘点。
func disableForLowCredits(sa *StoredAuth, cr *creditSummary, redline int64) {
	if sa == nil || sa.PhysicalPath == "" || cr == nil {
		return
	}
	// 1. 锁内重读后判定：已被禁用就谁的禁用谁负责，不覆盖归属。
	written, errMutate := mutateAuthFile(sa.PhysicalPath, func(auth *StoredAuth) bool {
		if auth.Disabled {
			return false
		}
		auth.Disabled = true
		auth.LowCreditDisable = true
		auth.LowCreditDisableBelow = redline
		auth.LowCreditDisableAt = time.Now().Format(time.RFC3339)
		return true
	})
	if errMutate != nil {
		hostLogf("warn", "低额禁用写盘失败", map[string]any{
			"auth_id": sa.AuthID,
			"error":   errMutate.Error(),
		})
		return
	}
	if !written {
		return
	}
	// 2. 内存副本同步，避免同一轮后续判定仍看到旧状态。
	sa.Disabled = true
	sa.LowCreditDisable = true
	sa.LowCreditDisableBelow = redline
	hostLogf("info", "账号积分低于红线，已自动禁用", map[string]any{
		"auth_id": sa.AuthID,
		"remain":  cr.Remain,
		"redline": redline,
	})
}

// reenableForLowCredits 解除本机制写入的禁用。
//
// 只认自己的标记：用户手动禁用的号没有标记，永不被这里放开；
// 否则用户刚点完禁用，下一轮巡检就会把号放回路由。
// [参数] sa：账号；cr：恢复时的积分。
// [返回] 无（失败只记日志）。
// 最近修改时间：2026-10-25；改动原因：新增，自动解禁的唯一写盘点。
func reenableForLowCredits(sa *StoredAuth, cr *creditSummary) {
	if sa == nil || sa.PhysicalPath == "" {
		return
	}
	// 1. 锁内重读后判定：只有「被禁用 + 带本机制标记」的号才解禁。
	written, errMutate := mutateAuthFile(sa.PhysicalPath, func(auth *StoredAuth) bool {
		if !auth.Disabled || !auth.LowCreditDisable {
			return false
		}
		auth.Disabled = false
		auth.LowCreditDisable = false
		auth.LowCreditDisableBelow = 0
		auth.LowCreditDisableAt = ""
		return true
	})
	if errMutate != nil {
		hostLogf("warn", "低额解禁写盘失败", map[string]any{
			"auth_id": sa.AuthID,
			"error":   errMutate.Error(),
		})
		return
	}
	if !written {
		return
	}
	// 2. 内存副本同步，避免同一轮后续判定仍看到禁用状态。
	sa.Disabled = false
	sa.LowCreditDisable = false
	sa.LowCreditDisableBelow = 0
	remain := creditUnknownRemain
	if cr != nil {
		remain = cr.Remain
	}
	hostLogf("info", "账号积分已回升，已自动解禁", map[string]any{
		"auth_id": sa.AuthID,
		"remain":  remain,
	})
}

// clearLowCreditMarkers 清掉本机制的归属标记，但不动 disabled 本身。
//
// 人工启停必须调用它：用户手动禁用一个「已被自动禁用」的账号时，
// disabled 已经是 true，若不清标记，次日签到到账会违背用户意愿自动解禁。
// [参数] auth：账号结构（原地修改）。
// [返回] 是否发生了改动。
// 最近修改时间：2026-10-25；改动原因：新增，防串权的关键一环。
func clearLowCreditMarkers(auth *StoredAuth) bool {
	if auth == nil || !auth.LowCreditDisable {
		return false
	}
	auth.LowCreditDisable = false
	auth.LowCreditDisableBelow = 0
	auth.LowCreditDisableAt = ""
	return true
}
