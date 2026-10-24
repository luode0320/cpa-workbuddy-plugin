// daily_cooldown.go 实现 Qoder 账号遭遇 110 (Billing daily count exceeded) 时的
// 24 小时持久化冷却引擎。
//
// Qoder 账号存在每日请求调用次数上限，遭遇 110 限制时意味着当日配额耗尽。
// 该机制将冷却截止时间持久化写入物理 auth JSON 文件的 daily_cooling_until 字段，
// 并由内存镜像保障调度器与换号逻辑高效排除该账号，持续整整 24 小时。
// 严格遵循「仅到期自然解除」策略，同时支持面板管理接口人工手动解除。
package main

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"
)

// dailyCooldownDuration 定义 Qoder 账号遭遇日请求次数上限后的固定冷却时长（24 小时）。
const dailyCooldownDuration = 24 * time.Hour

var (
	dailyCoolingMu  sync.RWMutex
	dailyCoolingMap = make(map[string]time.Time) // authID / normalizedKey -> 冷却截止时间
)

// parseDailyCoolingUntilFromAuthJSON 从物理 auth 文件 JSON 载荷中解析 daily_cooling_until。
// [参数] raw: 物理认证文件原始 JSON 字节流。
// [返回] 解析成功的截止时间；缺失或格式错误时返回 time.Time{} 零值。
func parseDailyCoolingUntilFromAuthJSON(raw []byte) time.Time {
	var doc struct {
		DailyCoolingUntil string `json:"daily_cooling_until"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return time.Time{}
	}
	rawStr := strings.TrimSpace(doc.DailyCoolingUntil)
	if rawStr == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, rawStr)
	if err != nil {
		return time.Time{}
	}
	return t
}

// isAccountDailyCooling 判定指定账号当前是否仍处于 24 小时日限额冷却窗口内。
// 支持原始 auth_id、文件名以及 normalizeFailoverKey 双向模糊归一化匹配。
// [参数] authID: 账号唯一标识。
// [返回] 处于冷却窗口内返回 true；未冷却或已自然过期返回 false。
func isAccountDailyCooling(authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	dailyCoolingMu.RLock()
	defer dailyCoolingMu.RUnlock()

	now := time.Now()
	if until, ok := dailyCoolingMap[authID]; ok && now.Before(until) {
		return true
	}
	target := normalizeFailoverKey(authID)
	if target == "" {
		return false
	}
	if until, ok := dailyCoolingMap[target]; ok && now.Before(until) {
		return true
	}
	for k, until := range dailyCoolingMap {
		if !now.Before(until) {
			continue
		}
		normK := normalizeFailoverKey(k)
		if normK == target || strings.Contains(normK, target) || strings.Contains(target, normK) {
			return true
		}
	}
	return false
}

// getAccountDailyCoolingUntil 获取指定账号的冷却截止时间。
// [参数] authID: 账号唯一标识。
// [返回] 仍在冷却中时返回截止时间；否则返回零值。
func getAccountDailyCoolingUntil(authID string) time.Time {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return time.Time{}
	}
	dailyCoolingMu.RLock()
	defer dailyCoolingMu.RUnlock()

	now := time.Now()
	if until, ok := dailyCoolingMap[authID]; ok && now.Before(until) {
		return until
	}
	target := normalizeFailoverKey(authID)
	if target == "" {
		return time.Time{}
	}
	if until, ok := dailyCoolingMap[target]; ok && now.Before(until) {
		return until
	}
	for k, until := range dailyCoolingMap {
		if !now.Before(until) {
			continue
		}
		normK := normalizeFailoverKey(k)
		if normK == target || strings.Contains(normK, target) || strings.Contains(target, normK) {
			return until
		}
	}
	return time.Time{}
}

// dailyCoolingUntilFor 读取内存镜像里该账号的冷却截止时间。
// [参数] authID：账号标识（auth.ID / 归一化键任一形态）。
// [返回] 冷却截止时间；无记录或已过期时返回零值。
// 最近修改时间：2026-10-25；改动原因：新增，供重建盘上字段。
func dailyCoolingUntilFor(authID string) time.Time {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return time.Time{}
	}
	now := time.Now()
	dailyCoolingMu.RLock()
	defer dailyCoolingMu.RUnlock()
	if until, ok := dailyCoolingMap[authID]; ok && now.Before(until) {
		return until
	}
	if norm := normalizeFailoverKey(authID); norm != "" {
		if until, ok := dailyCoolingMap[norm]; ok && now.Before(until) {
			return until
		}
	}
	return time.Time{}
}

// restoreDailyCoolingIntoDoc 在落盘前把 daily_cooling_until 补回文档。
//
// 为什么需要：宿主在每次请求结束后都会用内存里的 Metadata 重建 auth 文件，
// 而那份 Metadata 只含白名单键，daily_cooling_until 不在其中，于是「插件写入
// 24h 冷却 → 宿主 persist」之后盘上只剩白名单键。字段一丢，进程重启或
// refreshDailyCoolingSetFromDisk 就再也恢复不出这个号的冷却，账号会提前回到路由。
//
// 只补「内存里确实还在冷却期内」的账号：已到期的不补，避免把该恢复的号重新冻结。
//
// [参数] doc：待落盘的顶层键映射（原地修改）；authID：账号标识。
// [返回] true 表示本次补写了字段。
// 最近修改时间：2026-10-25；改动原因：新增，堵住宿主 persist 抹冷却字段的缺口。
func restoreDailyCoolingIntoDoc(doc map[string]any, authID string) bool {
	if doc == nil {
		return false
	}
	if existing, _ := doc["daily_cooling_until"].(string); strings.TrimSpace(existing) != "" {
		return false
	}
	until := dailyCoolingUntilFor(authID)
	if until.IsZero() {
		return false
	}
	doc["daily_cooling_until"] = until.UTC().Format(time.RFC3339)
	return true
}

// triggerDailyCooldown 将指定账号置入 24 小时每日调用上限冷却状态，并持久化到物理文件。
// [参数] authIndex: 宿主 RPC 索引（可空，为空时自动反查）；authID: 账号唯一标识。
// [返回] 写入与持久化成功返回 nil，失败返回对应错误。
func triggerDailyCooldown(authIndex, authID string) error {
	authIndex = strings.TrimSpace(authIndex)
	authID = strings.TrimSpace(authID)
	if authIndex == "" && authID != "" {
		if idx, err := hostAuthIndexForPhys(authID); err == nil {
			authIndex = idx
		}
	}
	if authIndex == "" {
		return errAuthIndexRequired()
	}

	until := time.Now().Add(dailyCooldownDuration)

	// 1. 同步更新内存镜像
	dailyCoolingMu.Lock()
	dailyCoolingMap[authID] = until
	if norm := normalizeFailoverKey(authID); norm != "" {
		dailyCoolingMap[norm] = until
	}
	dailyCoolingMu.Unlock()

	// 2. 直写物理 auth 文件完成持久化落盘
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}

	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil {
		doc = make(map[string]any)
	}
	doc["daily_cooling_until"] = until.UTC().Format(time.RFC3339)

	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := persistAuthDirect(phys.Name, phys.Path, "", raw); err != nil {
		return err
	}

	log.Printf("[qoder-ai] account %s (auth_index=%s) triggered 24h daily cooling until %s", authID, authIndex, until.Format(time.RFC3339))
	return nil
}

// clearDailyCooldown 手动解除指定账号的每日限额冷却状态，并从物理文件中移除字段。
// [参数] authIndex: 宿主 RPC 索引（可空，为空时自动反查）；authID: 账号唯一标识。
// [返回] 清除与落盘成功返回 nil。
func clearDailyCooldown(authIndex, authID string) error {
	authIndex = strings.TrimSpace(authIndex)
	authID = strings.TrimSpace(authID)
	if authIndex == "" && authID != "" {
		if idx, err := hostAuthIndexForPhys(authID); err == nil {
			authIndex = idx
		}
	}
	if authIndex == "" {
		return errAuthIndexRequired()
	}

	// 1. 清理内存状态
	dailyCoolingMu.Lock()
	if authID != "" {
		delete(dailyCoolingMap, authID)
		if norm := normalizeFailoverKey(authID); norm != "" {
			delete(dailyCoolingMap, norm)
		}
	}
	dailyCoolingMu.Unlock()

	// 2. 物理文件持久化剥离字段
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}

	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil {
		return nil
	}
	if _, ok := doc["daily_cooling_until"]; !ok {
		return nil
	}
	delete(doc, "daily_cooling_until")

	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := persistAuthDirect(phys.Name, phys.Path, "", raw); err != nil {
		return err
	}

	log.Printf("[qoder-ai] account %s (auth_index=%s) daily cooling cleared manually", authID, authIndex)
	return nil
}

// refreshDailyCoolingSetFromDisk 从磁盘物理文件中重新扫描并同步内存冷却时间字典。
// [返回] 当前处于冷却窗口内的账号数。
func refreshDailyCoolingSetFromDisk() int {
	files, err := hostAuthList()
	if err != nil {
		dailyCoolingMu.RLock()
		defer dailyCoolingMu.RUnlock()
		return len(dailyCoolingMap)
	}

	now := time.Now()
	nextMap := make(map[string]time.Time)

	for _, f := range files {
		phys, physErr := hostAuthGetPhysical(f.AuthIndex)
		if physErr != nil || phys == nil || len(phys.JSON) == 0 {
			continue
		}
		until := parseDailyCoolingUntilFromAuthJSON(phys.JSON)
		if !until.IsZero() && now.Before(until) {
			nextMap[f.ID] = until
			if norm := normalizeFailoverKey(f.ID); norm != "" {
				nextMap[norm] = until
			}
		}
	}

	dailyCoolingMu.Lock()
	dailyCoolingMap = nextMap
	count := len(nextMap)
	dailyCoolingMu.Unlock()
	return count
}

// hostAuthIndexForPhys 根据 authID 反查对应的宿主 auth_index。
// [参数] authID: 账号认证唯一标识。
// [返回] 匹配的 auth_index；未找到返回 errAuthMissing()。
func hostAuthIndexForPhys(authID string) (string, error) {
	files, err := hostAuthList()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.ID == authID {
			return f.AuthIndex, nil
		}
	}
	return "", errAuthMissing()
}
