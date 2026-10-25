// quota_disable.go 把「额度真实耗尽」也落成宿主可见的写盘禁用。
//
// 与 failover_disable.go 的分工（本文件最重要的约束）：
//   - failover_disable.go 管上游失败（鉴权失效、限流、传输错误），恢复靠 10 分钟探活。
//   - 本文件管额度耗尽（`HasUsage && TotalRemain <= 0`），恢复靠「越过最早重置时刻
//     且探活成功」双条件。
//   - 两套标记同时存在时各自独立判定，谁都不替对方解禁：失败探活成功只清
//     failover_disable 三键，额度恢复只清 quota_disable 三键；`disabled` 只有在
//     两套标记都不存在时才置回 false。
//
// 为什么必须写盘：额度耗尽原先只体现在 cachedQuota 的 PlanStatus 过滤里，宿主选择器
// 看不见，同一个请求内仍会把已耗尽的号发给插件，拿一次 200+1005 才换下一个号。
//
// 为什么必须等重置时刻：日额按北京时间 00:00 重置（上游实测 period_end = 当日
// 23:59:59 CST）。重置前探活必然还是 1005，拿它当恢复判据只会白烧请求。
//
// 最近修改时间：2026-10-25；改动原因：新增，额度耗尽纳入写盘禁用与重置恢复。
package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// quotaDisableKey 系列是本机制拥有的三个顶层键：标记表示归属，at 是禁用时刻，
// reset 是预期的重置时刻（Unix 秒），恢复判据靠它。
const (
	quotaDisableKey      = "quota_disable"
	quotaDisableAtKey    = "quota_disable_at"
	quotaDisableResetKey = "quota_disable_reset"
)

// quotaExhausted 判断快照是否表示「额度真实耗尽」。
//
// 三个条件缺一不可：HasUsage 为真（只有 balance 通道才有真实用量）、重置时刻已知、
// 剩余量不大于 0。HasUsage 为假时剩余量口径回退为授予量，若据此判耗尽会把所有
// 只走 current 通道的账号全部误禁——这是本函数存在的首要原因。
// [参数] snap：额度快照，可为 nil。
// [返回] true 表示该快照可用于触发额度禁用。
// 最近修改时间：2026-10-25；改动原因：新增，额度判定的唯一入口。
func quotaExhausted(snap *zcodeQuotaSnapshot) bool {
	if snap == nil || !snap.HasUsage {
		return false
	}
	if snap.EarliestReset <= 0 {
		// 重置时刻未知就不动手：无法验证恢复的禁用等于永久禁用。
		return false
	}
	return snap.TotalRemain <= 0
}

// disableAccountForQuota 把额度耗尽落成写盘禁用并打本机制标记。
//
// 认领规则与失败禁用一致：盘上已 disabled 且带本机制标记视为幂等成功；带失败禁用
// 标记时各自独立——本函数不覆盖、不清除对方的标记，只在盘上未禁用时写自己的标记。
// [参数] authIndex：宿主 auth_index；authID：账号标识；resetAt：预期重置时刻（Unix 秒）。
// [返回] true 表示该账号当前处于本机制的额度禁用管辖之下。
// 最近修改时间：2026-10-25；改动原因：新增，额度耗尽的写盘落点。
func disableAccountForQuota(authIndex, authID string, resetAt int64) bool {
	authIndex = strings.TrimSpace(authIndex)
	authID = strings.TrimSpace(authID)
	if authIndex == "" || resetAt <= 0 {
		return false
	}
	// 锁必须罩住「读盘 → 判状态 → 写盘」整段，理由同 failover_disable.go：
	// 并发记账各写一次会各触发一次宿主 reload。
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	doc, path, err := failoverGetDocFn(authIndex)
	if err != nil || doc == nil || path == "" {
		log.Printf("[zcode] quota disable skipped: no physical doc for auth=%s", authID)
		return false
	}
	if quotaMarked, _, _ := parseQuotaMarkers(doc); quotaMarked && docDisabled(doc) {
		return true
	}
	if docDisabled(doc) {
		// 已被别的机制禁用：可能是失败禁用，也可能是用户手动禁用。
		// 不接管、不抢恢复职责，交给原机制处理。
		return false
	}

	at := failoverNowFn().UTC().Format(time.RFC3339)
	doc["disabled"] = true
	doc[quotaDisableKey] = true
	doc[quotaDisableAtKey] = at
	doc[quotaDisableResetKey] = resetAt
	if errWrite := failoverWriteDocFn(path, doc); errWrite != nil {
		log.Printf("[zcode] quota disable write failed: auth=%s err=%v", authID, errWrite)
		return false
	}
	log.Printf("[zcode] quota disabled account: auth=%s reset_at=%d", authID, resetAt)
	return true
}

// parseQuotaMarkers 从盘上文档读取本机制的三标记状态。
// [参数] doc：顶层键值表。
// [返回] 是否带本机制标记、禁用时刻、预期重置时刻。
// 最近修改时间：2026-10-25；改动原因：新增，额度禁用与恢复共用。
func parseQuotaMarkers(doc map[string]any) (bool, string, int64) {
	if doc == nil {
		return false, "", 0
	}
	marked, _ := doc[quotaDisableKey].(bool)
	at, _ := doc[quotaDisableAtKey].(string)
	var resetAt int64
	switch v := doc[quotaDisableResetKey].(type) {
	case float64:
		resetAt = int64(v)
	case int64:
		resetAt = v
	case int:
		resetAt = int64(v)
	}
	return marked, strings.TrimSpace(at), resetAt
}

// reenableForQuotaReset 在越过重置时刻且探活成功后解除额度禁用。
//
// 三个条件同时成立才动手：盘上带本机制标记、当前时刻已越过预期重置时刻、探活成功。
// 任一不成立都保持禁用——重置前探活必然失败；重置后探活失败说明额度没回来或账号
// 另有问题，放回路由只会让用户拿到 1005。
//
// 只清自己的三键。若同时存在失败禁用标记，`disabled` 保持 true，由失败探活决定。
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：探活用的账号凭据。
// [返回] true 表示已解除禁用。
// 最近修改时间：2026-10-25；改动原因：新增，额度恢复的唯一落点。
func reenableForQuotaReset(authIndex, authID string, sa *StoredAuth) bool {
	authIndex = strings.TrimSpace(authIndex)
	authID = strings.TrimSpace(authID)
	if authIndex == "" || sa == nil {
		return false
	}
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	doc, path, err := failoverGetDocFn(authIndex)
	if err != nil || doc == nil || path == "" {
		return false
	}
	marked, _, resetAt := parseQuotaMarkers(doc)
	if !marked {
		return false
	}
	if resetAt <= 0 || failoverNowFn().Unix() <= resetAt {
		// 还没到重置时刻：不动，等下一轮。
		return false
	}

	model := failoverProbeModelFor(authID)
	if errPing := failoverPingFn(sa, model); errPing != nil {
		log.Printf("[zcode] quota reset probe failed: auth=%s model=%s err=%v", authID, model, errPing)
		return false
	}

	delete(doc, quotaDisableKey)
	delete(doc, quotaDisableAtKey)
	delete(doc, quotaDisableResetKey)
	// 失败禁用标记还在时必须保持 disabled，否则等于替失败机制解禁。
	if failoverMarked, _, _ := parseFailoverMarkers(doc); !failoverMarked {
		doc["disabled"] = false
	}
	if errWrite := failoverWriteDocFn(path, doc); errWrite != nil {
		log.Printf("[zcode] quota re-enable write failed: auth=%s err=%v", authID, errWrite)
		return false
	}
	forgetFailoverProbeModel(authID)
	log.Printf("[zcode] quota re-enabled account: auth=%s model=%s", authID, model)
	return true
}

// gradeQuotaForAuth 按额度快照给账号分级：耗尽则写盘禁用，已到重置时刻则尝试恢复。
//
// 由额度刷新链路在拿到新鲜快照后调用。未耗尽且未禁用时什么都不做——本函数不负责
// 解除「别的原因」造成的禁用。
// [参数] authIndex：宿主 auth_index；authID：账号标识；sa：账号凭据；snap：额度快照。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，刷新链路的额度分级入口。
func gradeQuotaForAuth(authIndex, authID string, sa *StoredAuth, snap *zcodeQuotaSnapshot) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if quotaExhausted(snap) {
		disableAccountForQuota(authIndex, authID, snap.EarliestReset)
		return
	}
	// 未耗尽不代表不用管：可能是上一周期耗尽、本周期已重置。恢复仍需探活验证。
	reenableForQuotaReset(authIndex, authID, sa)
}

// quotaDisableSummary 返回账号的额度禁用摘要，供日志与诊断读取。
// [参数] doc：顶层键值表。
// [返回] 是否额度禁用、预期重置时刻；非本机制禁用时返回 false 与 0。
// 最近修改时间：2026-10-25；改动原因：新增，可观测性最小接口。
func quotaDisableSummary(doc map[string]any) (bool, int64) {
	marked, _, resetAt := parseQuotaMarkers(doc)
	if !marked || !docDisabled(doc) {
		return false, 0
	}
	return true, resetAt
}

// isQuotaDisabled 判定账号当前是否处于「由本机制写入的额度禁用」状态。
// [参数] authID：账号标识。
// [返回] 是否额度禁用、预期重置时刻；未禁用或读取失败返回 false 与 0。
// 最近修改时间：2026-10-25；改动原因：新增，管理面板展示额度禁用状态。
func isQuotaDisabled(authID string) (bool, int64) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false, 0
	}
	authIndex, _ := failoverResolveIdxFn(authID)
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return false, 0
	}
	doc, _, err := failoverGetDocFn(authIndex)
	if err != nil || doc == nil {
		return false, 0
	}
	return quotaDisableSummary(doc)
}

// quotaResetText 把重置时刻渲染成北京时间文案，供面板展示。
// [参数] resetAt：Unix 秒。
// [返回] 形如「10-26 00:00（北京时间）」的文案；时刻非法时返回空串。
// 最近修改时间：2026-10-25；改动原因：新增，面板恢复倒计时的格式来源。
func quotaResetText(resetAt int64) string {
	if resetAt <= 0 {
		return ""
	}
	cst := time.FixedZone("CST", 8*3600)
	return fmt.Sprintf("%s（北京时间）", time.Unix(resetAt, 0).In(cst).Format("01-02 15:04"))
}
