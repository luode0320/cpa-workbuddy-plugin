// auth_thirdparty.go 解析第三方导出工具产出的账号记录形态：
// {"uid":..,"access_token":..,"expires_at":..,"auth_raw":{..},"profile_raw":{..}}。
// 该形态与插件自身落盘的嵌套文档差别在于令牌藏在 auth_raw、过期时间用毫秒，
// 解析后统一归一化成 storedAuth，供导入 / 认领 / 执行链路共用。
package main

import (
	"encoding/json"
	"strings"
)

// thirdPartyRecord 是第三方导出记录里本插件关心的顶层字段。
type thirdPartyRecord struct {
	UID          string         `json:"uid"`
	Nickname     string         `json:"nickname"`
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	ExpiresAt    int64          `json:"expires_at"`
	AuthRaw      map[string]any `json:"auth_raw"`
	ProfileRaw   map[string]any `json:"profile_raw"`
}

// isThirdPartyStored 判断凭证文档是否带第三方导出形态的顶层键。
// [参数] probe：文档顶层键到原文的映射。
// [返回] 含 auth_raw / profile_raw / access_token 任一键时为 true。
// 最近修改时间：2026-11-12 22:45:00；改动原因：新增，用于 parseStored 的形态分发。
func isThirdPartyStored(probe map[string]json.RawMessage) bool {
	for _, key := range []string{"auth_raw", "profile_raw", "access_token"} {
		if _, ok := probe[key]; ok {
			return true
		}
	}
	return false
}

// parseThirdPartyStored 把第三方账号记录归一化成插件内部凭证结构。
// [参数] raw：第三方导出的单条账号记录原文。
// [返回] 归一化凭证；字段缺失留零值，accessToken 校验由 parseStored 统一负责。
// 最近修改时间：2026-11-12 22:45:00；改动原因：新增，兼容 auth_raw/profile_raw 导出形态。
func parseThirdPartyStored(raw []byte) storedAuth {
	var rec thirdPartyRecord
	_ = json.Unmarshal(raw, &rec)
	// 1. 令牌优先取 auth_raw，缺失时回落到顶层 snake_case 字段。
	tokens := rec.AuthRaw
	accessToken := firstNonEmpty(jsonStr(tokens, "accessToken", "access_token"), rec.AccessToken)
	refreshToken := firstNonEmpty(jsonStr(tokens, "refreshToken", "refresh_token"), rec.RefreshToken)
	// 2. 过期时间统一成秒：auth_raw.expiresAt 是毫秒，顶层 expires_at 已是秒。
	expiresAt := epochsToSeconds(jsonI64(tokens, "expiresAt", "expires_at"))
	if expiresAt == 0 {
		expiresAt = epochsToSeconds(rec.ExpiresAt)
	}
	// 3. 账号信息优先取 profile_raw，缺失时回落到记录顶层字段。
	profile := rec.ProfileRaw
	return storedAuth{
		Auth: storedTokens{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			ExpiresAt:    expiresAt,
			Domain:       strings.TrimSpace(jsonStr(tokens, "domain")),
		},
		Account: storedAccount{
			UID:          firstNonEmpty(jsonStr(profile, "uid"), rec.UID),
			EnterpriseID: jsonStr(profile, "enterpriseId", "enterprise_id", "tenantId", "tenant_id"),
			Nickname:     firstNonEmpty(jsonStr(profile, "nickname"), rec.Nickname),
		},
	}
}

// epochsToSeconds 把毫秒级时间戳换算成秒，已是秒的输入原样返回。
// [参数] v：秒级或毫秒级 Unix 时间戳。
// [返回] 秒级时间戳；非正值返回 0。
// 最近修改时间：2026-11-12 22:45:00；改动原因：新增，第三方导出用毫秒而插件落盘用秒。
func epochsToSeconds(v int64) int64 {
	if v <= 0 {
		return 0
	}
	// 秒级时间戳到公元 5138 年才达到 1e11，超过即按毫秒处理。
	if v > 1e11 {
		return v / 1000
	}
	return v
}
