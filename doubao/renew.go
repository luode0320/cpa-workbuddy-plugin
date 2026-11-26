package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// 本文件实现豆包账号的会话 Cookie 自动续期。
//
// 令牌端点双向可用：带会话 Cookie 调用返回新 x-tt-token，带该 token 调用
// 返回一批新 Set-Cookie。本文件把两步串成一轮续期，并用会话主项值变化作为
// 真换发判据，避免把只下发埋点 Cookie 的死会话响应误记为成功。

// renewResult 是一轮会话续期的结果。
type renewResult struct {
	// Renewed 表示本轮确实换到了新的会话主 Cookie。
	Renewed bool
	// CookieHeader 是续期后的 Cookie 头。
	CookieHeader string
	// Cookies 是续期后的 Cookie 快照。
	Cookies []savedCookie
	// Note 说明本轮实际走向，供日志与面板诊断。
	Note string
}

// parseCookieHeader 把 Cookie 头解析成小写名到值的映射。
// [参数] header: 完整 Cookie 头。
// [返回] 名值映射。
// 最近修改时间：2026-11-27；改动原因：新增，换发判据需要旧值基线。
func parseCookieHeader(header string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.Index(part, "=")
		if eq <= 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(part[:eq]))
		if name == "" {
			continue
		}
		out[name] = strings.TrimSpace(part[eq+1:])
	}
	return out
}

// rotatedSessionCookies 判断本轮下发是否真的换掉了会话主 Cookie。
// [参数] prevHeader: 账号现有 Cookie 头。
// [参数] fresh: 本轮上游下发的 Cookie。
// [返回] 会话主项存在且值确实变化时返回 true。
// 最近修改时间：2026-11-27；改动原因：新增，换发判据锚定会话主项值。
func rotatedSessionCookies(prevHeader string, fresh []*http.Cookie) bool {
	prev := parseCookieHeader(prevHeader)
	for _, c := range fresh {
		if c == nil || !isSessionCookieName(c.Name) {
			continue
		}
		value := strings.TrimSpace(c.Value)
		if value == "" {
			continue
		}
		if old, ok := prev[strings.ToLower(strings.TrimSpace(c.Name))]; ok && old == value {
			continue
		}
		return true
	}
	return false
}

// mergeSessionCookies 把本轮下发的 Cookie 叠加到既有会话上。
// [参数] prevHeader: 账号现有 Cookie 头。
// [参数] fresh: 本轮上游下发的 Cookie。
// [返回] 合并后的 Jar 与 Cookie 头。
// 最近修改时间：2026-11-27；改动原因：新增，避免整份替换丢会话主项。
func mergeSessionCookies(prevHeader string, fresh []*http.Cookie) (*durableJar, string) {
	jar := newDurableJar()
	jar.seedFromHeader(prevHeader, doubaoSiteHost+"/")
	if len(fresh) > 0 {
		jar.SetCookies(mustParseDoubaoURL(doubaoSiteHost+"/"), fresh)
	}
	return jar, cookieHeaderFromJar(jar)
}

// renewSession 执行一轮 Cookie→token→Cookie 会话续期。
// [参数] sa: 待续期账号。
// [返回] 续期结果与错误。
// 最近修改时间：2026-11-27；改动原因：新增，会话双向续期主链路。
func renewSession(sa *StoredAuth) (*renewResult, error) {
	if sa == nil || strings.TrimSpace(sa.CookieHeader) == "" {
		return nil, fmt.Errorf("账号凭据为空")
	}

	// 1. 带 Cookie 换新令牌。
	step1, step1Cookies, newToken, errStep1 := doTokenBeat("", sa.CookieHeader)
	if errStep1 != nil {
		return nil, errStep1
	}
	if !step1.isSuccess() {
		return nil, fmt.Errorf("续期失败（第一步）：%s", step1.describe())
	}

	// 2. 第一步已换掉会话主项时直接用，不再走第二步。
	if rotatedSessionCookies(sa.CookieHeader, step1Cookies) {
		jar, header := mergeSessionCookies(sa.CookieHeader, step1Cookies)
		return &renewResult{
			Renewed:      true,
			CookieHeader: header,
			Cookies:      jar.snapshot(),
			Note:         fmt.Sprintf("第一步直接下发 %d 条 Cookie（含会话主项）", len(step1Cookies)),
		}, nil
	}

	// 3. 只拿到令牌时用它再换一次 Cookie。
	if strings.TrimSpace(newToken) == "" {
		return &renewResult{Note: renewUnattemptedNote("上游既未下发会话 Cookie 也未返回令牌", len(step1Cookies))}, nil
	}
	step2, step2Cookies, _, errStep2 := doTokenBeat(newToken, "")
	if errStep2 != nil {
		return nil, errStep2
	}
	if !step2.isSuccess() {
		return nil, fmt.Errorf("续期失败（第二步）：%s", step2.describe())
	}
	if !rotatedSessionCookies(sa.CookieHeader, step2Cookies) {
		return &renewResult{Note: renewUnattemptedNote("第二步未换到会话主 Cookie", len(step2Cookies))}, nil
	}

	jar, header := mergeSessionCookies(sa.CookieHeader, step2Cookies)
	return &renewResult{
		Renewed:      true,
		CookieHeader: header,
		Cookies:      jar.snapshot(),
		Note:         fmt.Sprintf("双向换发完成：令牌换回 %d 条 Cookie（含会话主项）", len(step2Cookies)),
	}, nil
}

// renewUnattemptedNote 拼装未换发的说明文案。
// [参数] reason: 未换发原因。
// [参数] count: 本轮下发 Cookie 条数。
// [返回] 说明文案。
// 最近修改时间：2026-11-27；改动原因：新增，保留下发条数便于区分端点异常与会话吊销。
func renewUnattemptedNote(reason string, count int) string {
	if count == 0 {
		return reason + "（本轮下发 0 条 Cookie）"
	}
	return fmt.Sprintf("%s（本轮下发 %d 条 Cookie，无会话主项）", reason, count)
}

// applyRenewal 把续期结果写回账号并落盘。
// [参数] sa: 账号。
// [参数] res: 续期结果。
// [参数] bindDigest: 续期开始时锚定的文件内容摘要，空串表示无需防覆盖判定。
// [返回] 写入错误；摘要不符而放弃时返回 nil。
// 最近修改时间：2026-11-27；改动原因：新增，会话续期落盘。
func applyRenewal(sa *StoredAuth, res *renewResult, bindDigest string) error {
	if sa == nil || res == nil || !res.Renewed {
		return nil
	}
	if sa.PhysicalPath == "" {
		return fmt.Errorf("账号缺少物理路径，无法落盘")
	}
	if bindDigest != "" && fileDigest(sa.PhysicalPath) != bindDigest {
		return nil
	}

	now := time.Now().Format(time.RFC3339)
	sa.CookieHeader = res.CookieHeader
	sa.Cookies = res.Cookies
	sa.LastRenewAt = now
	sa.UpdatedAt = now
	sa.AuthInvalid = false
	return writeAuthFileDirect(sa.PhysicalPath, sa)
}

// isSessionExpiredError 判断错误文案是否来自上游会话失效判定。
// [参数] reason: 错误文案。
// [返回] 属于会话失效时返回 true。
// 最近修改时间：2026-11-27；改动原因：新增，失效标记只认上游明确判定。
func isSessionExpiredError(reason string) bool {
	text := strings.ToLower(strings.TrimSpace(reason))
	if text == "" {
		return false
	}
	return strings.Contains(text, "会话过期") ||
		strings.Contains(text, "登录已过期") ||
		strings.Contains(text, "login invalid") ||
		strings.Contains(text, "session_expired") ||
		strings.Contains(text, "710012001")
}

// markSessionInvalid 记录上游明确判定的会话失效。
// [参数] sa: 账号。
// [参数] reason: 上游错误文案。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，失效账号面板提示重新授权。
func markSessionInvalid(sa *StoredAuth, reason string) {
	if sa == nil || sa.PhysicalPath == "" || !isSessionExpiredError(reason) {
		return
	}
	if sa.AuthInvalid {
		return
	}
	sa.AuthInvalid = true
	sa.UpdatedAt = time.Now().Format(time.RFC3339)
	if errWrite := writeAuthFileDirect(sa.PhysicalPath, sa); errWrite != nil {
		log.Printf("[doubao] mark session invalid failed for %s: %v", sa.AuthID, errWrite)
	}
}
