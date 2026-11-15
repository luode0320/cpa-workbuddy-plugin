package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// warmUpTimeout 是补取替代会话 Cookie 的单次请求超时。
//
// 取值只需覆盖一次首页 GET（生产实测 0.3~0.9s）；过大会让首条推理请求
// 在无网环境下被拖慢，过小则拿不到 ttwid。
const warmUpTimeout = 8 * time.Second

// 本文件实现可写回的 Cookie Jar 与登录态失效判定。
//
// 背景（需求 AC-002）：豆包上游会在响应中滚动下发 Set-Cookie（实测
// accounts.doubao.com/ttwid/check/ 会下发 gd_random），Go 标准库
// net/http/cookiejar 只在内存中接收、不会持久化。若不落盘，进程重启或热重载后
// 这些新 Cookie 全部丢失，凭据无法随上游续期。
//
// 架构参考：rex-zsd/doubao-cpa-plugin（MIT）的 durableJar 模式（见实施总览 EXT-002）；
// 本实现按本仓库风格重写，仅沿用其「包装标准 Jar + 记录快照 + 防覆盖」思路。

// sessionCookieNames 是判定登录态是否仍在的会话主 Cookie 名。
//
// 与 credentials.go 的必需字段口径一致：任一存在且未过期即视为会话仍在。
// 全部缺失说明用户已登出或会话被上游回收，此时应上报「待重新授权」。
//
// 注意：这组名字同时是**禁止发往 www.doubao.com 的黑名单**（见
// blockedHostCookieNames）。判据端点要它们，对话端点见它们就拒，两个口径必须分开。
var sessionCookieNames = []string{"sessionid", "sessionid_ss", "sid_tt", "sid_guard"}

// blockedHostCookieNames 是绝不能出现在 www.doubao.com 请求里的 Cookie 名。
//
// 实测（2026-11-15，生产机房 IP + Chrome uTLS 指纹 + 真实网页版 body，无签名）：
//   - 浏览器自己发往 /chat/completion 的 12 条 Cookie 里**没有任何一条** session 组；
//   - 只要往这 12 条里加入 sessionid / sessionid_ss / sid_tt / sid_guard 任意一条，
//     上游立刻回 710022004 rate limited，decision.subtype=semantic_reasoning；
//   - 逐一删除 24 条里的单条 session Cookie 仍是 710022004，删满这 4 条才恢复出字。
//
// 也就是说：这四条是「账号持有登录态的凭据」，一旦随推理请求发出去，风控就把
// 请求判定成「脚本拿着网页会话在冒充浏览器」，与账号是否健康无关。因此 Jar 在
// 发往站点域名时必须过滤它们，仅在判据/令牌端点（accounts.doubao.com）保留。
var blockedHostCookieNames = []string{"sessionid", "sessionid_ss", "sid_tt", "sid_guard"}

// fallbackSessionCookieName 是无会话 Cookie 时用于替代的公共 Cookie 名。
//
// 去掉 4 条 session 后如果什么都不补，请求会拿到空响应（实测 0.16s、零事件）；
// 只补 ttwid 即可稳定出字（STREAM_CHUNK 14~15），补 s_v_web_id 单独无效。
const fallbackSessionCookieName = "ttwid"

// savedCookie 是 Jar 中一条被持久化的 Cookie。
//
// Source 记录下发该 Cookie 的 URL，重放时用于重建 Domain / Path 归属；
// 仅存 Source 而非整个请求上下文，避免把无关请求信息写进账号文件。
type savedCookie struct {
	Source string      `json:"source"`
	Cookie http.Cookie `json:"cookie"`
}

// durableJar 是可写回的 Cookie Jar。
//
// 包装标准 cookiejar 负责常规读写；records 额外记录每条 Cookie 及其来源，
// 供 snapshot 落盘与 restore 重放。mu 保护 records 与 base 的并发访问——
// 推理请求可能并发触发 SetCookies，无锁会导致 map 竞态崩溃。
type durableJar struct {
	base    http.CookieJar
	mu      sync.Mutex
	records map[string]savedCookie

	// path 是持久化目标账号文件；saved 是上次读到/写入时的内容摘要。
	// 两者用于 deferToNewer 防覆盖判定（见 persist）。
	//
	// 为何用内容摘要而不是 os.FileInfo：实测在 Windows 上「删除后重建」会复用
	// 同一文件索引，若两份 JSON 又恰好等长，SameFile + ModTime + Size 三项都可能
	// 判为未变化，导致旧快照真的覆盖掉用户刚重新导入的凭据——正是本守卫要防的
	// 事故。摘要与文件身份无关，只认内容。
	path  string
	saved string
}

// newDurableJar 创建一个空的可写回 Jar。
// [参数] 无。
// [返回] 已初始化 records 的 Jar。
// 最近修改时间：2026-10-17；改动原因：新增，Jar 构造入口。
func newDurableJar() *durableJar {
	base, _ := cookiejar.New(nil)
	return &durableJar{base: base, records: map[string]savedCookie{}}
}

// Cookies 返回对指定 URL 生效的 Cookie（委托标准 Jar）。
//
// 发往站点域名时过滤掉 session 组：这四条会让 /chat/completion 被判
// 710022004 semantic_reasoning（依据见 blockedHostCookieNames 注释）。
// [参数] u: 目标 URL。
// [返回] 生效的 Cookie 列表。
// 最近修改时间：2026-11-15 15:52:00；改动原因：新增站点域名的会话 Cookie 过滤。
func (j *durableJar) Cookies(u *url.URL) []*http.Cookie {
	cookies := j.base.Cookies(u)
	if u == nil || !isSiteHost(u.Hostname()) {
		return cookies
	}
	// 1. 站点域名先剔除 session 组，再确认替代会话 Cookie 是否还在。
	filtered := filterHostCookies(cookies)
	// 2. 过滤后若一条 ttwid 都没有，补一条：实测只删不补会拿到空响应（0.16s、零事件），
	//    这种静默失败比报错更难查，因此即使过滤结果为空也要走兜底。
	if !hasFallbackSessionCookie(filtered) {
		filtered = j.withFallbackCookie(filtered)
	}
	return filtered
}

// withFallbackCookie 在出站 Cookie 里补一条替代会话的公共 Cookie。
//
// 依据（2026-11-15 实测）：账号 11 条 Cookie 去掉 4 条 session 后直接请求，
// 生产端 0.16s 返回零事件；补一条 ttwid 即稳定出字。ttwid 由站点自己下发，
// 不在账号凭据里时从 Jar 的其它记录中借用同名值。
// [参数] cookies: 已过滤的 Cookie 列表。
// [返回] 补入 ttwid 后的列表。
// 最近修改时间：2026-11-15 15:52:00；改动原因：新增，替代会话 Cookie 兜底。
func (j *durableJar) withFallbackCookie(cookies []*http.Cookie) []*http.Cookie {
	j.mu.Lock()
	value := ""
	for _, rec := range j.records {
		if strings.EqualFold(rec.Cookie.Name, fallbackSessionCookieName) && strings.TrimSpace(rec.Cookie.Value) != "" {
			value = rec.Cookie.Value
			break
		}
	}
	j.mu.Unlock()
	if value == "" {
		return cookies
	}
	out := make([]*http.Cookie, 0, len(cookies)+1)
	out = append(out, cookies...)
	out = append(out, &http.Cookie{Name: fallbackSessionCookieName, Value: value, Path: "/"})
	return out
}

// isSiteHost 判断主机名是否属于业务站点（www.doubao.com）。
// [参数] host: URL 的 Hostname()。
// [返回] 属于站点域名时返回 true。
// 最近修改时间：2026-11-15 15:52:00；改动原因：新增，站点域名判别，供 Cookie 过滤分流。
func isSiteHost(host string) bool {
	return strings.EqualFold(strings.TrimPrefix(host, "."), strings.TrimPrefix(siteHostname, "."))
}

// siteWarmUpOverride 是预热端点的测试覆盖点；非空时替换站点基址。
// 生产路径下始终为空，保证预热永远打真实站点。
var siteWarmUpOverride string

// warmUpFallbackCookie 在 Jar 缺少 ttwid 时先访问一次站点首页把它取回来。
//
// 为什么要这一步：推理请求必须带一条 ttwid 才会出字（依据见
// blockedHostCookieNames 注释）。账号凭据里通常没有它，因为它是站点首页
// 下发的访客 Cookie。这里用同一份凭据访问 /chat/ 让上游下发，再落入 Jar，
// 随后的 persist 会把它一并写回账号文件，下一次就不必再预热。
// [参数] jar: 目标 Jar；timeout: 单次请求超时。
// [返回] 预热错误；已有 ttwid 或预热失败时返回 nil（不阻断主链路）。
// 最近修改时间：2026-11-15 15:56:00；改动原因：新增，自动补齐替代会话 Cookie。
func warmUpFallbackCookie(jar *durableJar, timeout time.Duration) error {
	if jar == nil || jar.hasFallbackCookie() {
		return nil
	}
	if timeout <= 0 {
		timeout = warmUpTimeout
	}
	// 1. 预热基址可被测试覆盖：用 httptest 顶替真实站点，避免用例联网。
	base := doubaoSiteHost
	if siteWarmUpOverride != "" {
		base = siteWarmUpOverride
	}
	req, errReq := http.NewRequest(http.MethodGet, base+"/chat/", nil)
	if errReq != nil {
		return errReq
	}
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	client := &http.Client{Jar: jar, Transport: sharedUpstreamTransport, Timeout: timeout}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return errDo
	}
	defer resp.Body.Close()
	// 只关心 Set-Cookie，正文丢弃；读一点让连接可复用即可。
	_, _ = io.CopyN(io.Discard, resp.Body, 1<<12)
	return nil
}

// SetCookies 接收上游下发的 Cookie 并记录快照。
//
// 归一化处理与原因：
//   - Path 为空时补默认路径，否则重放时 Cookie 无法命中目标路径；
//   - MaxAge > 0 时折算成绝对 Expires，避免重放后相对时长再次起算；
//   - MaxAge < 0 或 Expires 已过期时从 records 删除（上游以此表达登出/失效）。
//
// [参数] u: 下发 Cookie 的 URL；cookies: 本次下发的 Cookie 列表。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，Set-Cookie 捕获与快照记录。
func (j *durableJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.base.SetCookies(u, cookies)
	for _, c := range cookies {
		saved := *c
		if saved.Path == "" {
			saved.Path = "/"
		}
		if saved.MaxAge > 0 {
			saved.Expires = time.Now().Add(time.Duration(saved.MaxAge) * time.Second)
			saved.MaxAge = 0
		}
		domain := saved.Domain
		if domain == "" {
			domain = u.Hostname()
		}
		key := strings.TrimPrefix(domain, ".") + "|" + saved.Path + "|" + saved.Name
		if saved.MaxAge < 0 || (!saved.Expires.IsZero() && saved.Expires.Before(time.Now())) {
			delete(j.records, key)
			continue
		}
		j.records[key] = savedCookie{Source: u.Scheme + "://" + u.Host + "/", Cookie: saved}
	}
}

// snapshot 返回当前记录的快照副本，供落盘使用。
// [参数] 无。
// [返回] Cookie 快照列表（顺序不保证）。
// 最近修改时间：2026-10-17；改动原因：新增，快照导出。
func (j *durableJar) snapshot() []savedCookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

// snapshotLocked 是 snapshot 的无锁版本（调用方需已持 j.mu）。
// [参数] 无。
// [返回] Cookie 快照列表（顺序不保证）。
// 最近修改时间：2026-10-17；改动原因：新增，供 persist 在持锁状态下复用，避免自锁死锁。
func (j *durableJar) snapshotLocked() []savedCookie {
	out := make([]savedCookie, 0, len(j.records))
	for _, rec := range j.records {
		out = append(out, rec)
	}
	return out
}

// restore 用已落盘的快照重建 Jar 状态（账号加载时调用）。
// [参数] records: 待重放的 Cookie 快照。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，快照重放。
func (j *durableJar) restore(records []savedCookie) {
	for _, rec := range records {
		u, errParse := url.Parse(rec.Source)
		if errParse != nil || u.Host == "" {
			continue
		}
		c := rec.Cookie
		j.SetCookies(u, []*http.Cookie{&c})
	}
}

// seedFromHeader 用导入的 Cookie header 播种 Jar。
//
// 首次导入的账号只有 header 字符串、尚无 records；播种后第一条推理请求即可
// 带上全部凭据，同时后续上游下发的 Set-Cookie 能与既有 Cookie 合并。
// [参数] header: 完整 Cookie header；source: 归属 URL。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，首次导入的 Jar 播种。
func (j *durableJar) seedFromHeader(header, source string) {
	u, errParse := url.Parse(source)
	if errParse != nil || u.Host == "" {
		return
	}
	var cookies []*http.Cookie
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.Index(part, "=")
		if eq <= 0 {
			continue
		}
		cookies = append(cookies, &http.Cookie{
			Name:  strings.TrimSpace(part[:eq]),
			Value: strings.TrimSpace(part[eq+1:]),
			Path:  "/",
		})
	}
	if len(cookies) > 0 {
		j.SetCookies(u, cookies)
	}
}

// hasLiveSession 判断 Jar 中是否仍持有未过期的会话主 Cookie。
// [参数] 无。
// [返回] 存在任一未过期的会话主 Cookie 时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，登录态失效判定。
func (j *durableJar) hasLiveSession() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	for _, rec := range j.records {
		if !isSessionCookieName(rec.Cookie.Name) {
			continue
		}
		if rec.Cookie.Expires.IsZero() || rec.Cookie.Expires.After(now) {
			return true
		}
	}
	return false
}

// isSessionCookieName 判断 Cookie 名是否属于会话主 Cookie。
// [参数] name: Cookie 名。
// [返回] 属于时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，会话 Cookie 名判定。
func isSessionCookieName(name string) bool {
	for _, n := range sessionCookieNames {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// isBlockedHostCookieName 判断 Cookie 是否禁止发往站点域名。
// [参数] name: Cookie 名。
// [返回] 属于黑名单时返回 true。
// 最近修改时间：2026-11-15 15:52:00；改动原因：新增，站点域名 Cookie 黑名单判定。
func isBlockedHostCookieName(name string) bool {
	for _, n := range blockedHostCookieNames {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// filterHostCookies 剔除发往站点域名时会导致风控的 Cookie。
//
// 为什么要在出站前过滤而不是在导入时丢弃：判据端点（accounts.doubao.com）
// 与令牌兑换都必须带 session 组才能识别账号身份，只有 /chat/completion 这条
// 推理链路不能带。故保留在 Jar 里、只在这一条链路的出站 Cookie 上剔除。
// [参数] cookies: Jar 为该 URL 选出的 Cookie。
// [返回] 过滤后的 Cookie（原切片无被剔除项时原样返回）。
// 最近修改时间：2026-11-15 15:52:00；改动原因：新增，推理出站 Cookie 过滤。
func filterHostCookies(cookies []*http.Cookie) []*http.Cookie {
	if len(cookies) == 0 {
		return cookies
	}
	out := make([]*http.Cookie, 0, len(cookies))
	for _, c := range cookies {
		if isBlockedHostCookieName(c.Name) {
			continue
		}
		out = append(out, c)
	}
	if len(out) == len(cookies) {
		return cookies
	}
	return out
}

// hasFallbackSessionCookie 判断导出 Cookie 里是否已含替代会话的公共 Cookie。
// [参数] cookies: 待检查的 Cookie 列表。
// [返回] 含 ttwid 时返回 true。
// 最近修改时间：2026-11-15 15:52:00；改动原因：新增，替代会话 Cookie 判定。
func hasFallbackSessionCookie(cookies []*http.Cookie) bool {
	for _, c := range cookies {
		if strings.EqualFold(c.Name, fallbackSessionCookieName) && strings.TrimSpace(c.Value) != "" {
			return true
		}
	}
	return false
}

// hasFallbackCookie 判断 Jar 中是否已持有替代会话的公共 Cookie。
// [参数] 无。
// [返回] 已持有可用的 ttwid 时返回 true。
// 最近修改时间：2026-11-15 15:56:00；改动原因：新增，预热前置判定。
func (j *durableJar) hasFallbackCookie() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, rec := range j.records {
		if strings.EqualFold(rec.Cookie.Name, fallbackSessionCookieName) && strings.TrimSpace(rec.Cookie.Value) != "" {
			return true
		}
	}
	return false
}

// bind 绑定持久化目标文件并记录其当前内容摘要。
// [参数] path: 账号文件路径。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，防覆盖判定的基准快照。
func (j *durableJar) bind(path string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.path = path
	j.saved = fileDigest(path)
}

// fileDigest 计算文件内容摘要；文件不存在或不可读时返回空串。
// [参数] path: 文件路径。
// [返回] 十六进制摘要。
// 最近修改时间：2026-10-17；改动原因：新增，内容级身份判定。
func fileDigest(path string) string {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// replacedLocked 判断账号文件是否已被本进程之外的一方替换（调用方需持 j.mu）。
// [参数] 无。
// [返回] 文件内容与绑定时的摘要不一致时返回 true。
// 最近修改时间：2026-10-17；改动原因：由文件身份改为内容摘要，修复 Windows 索引复用导致的误判。
func (j *durableJar) replacedLocked() bool {
	if j.path == "" {
		return false
	}
	return fileDigest(j.path) != j.saved
}

// persist 把当前 Jar 快照合并回账号文件并落盘。
//
// 为何要防覆盖（deferToNewer）：provider 是长驻进程，启动时读到账号文件后
// 会长期持有内存态。若期间用户在面板重新导入了同一账号（写入新 Cookie），
// 或另一进程/热重载实例更新了该文件，本进程随后的一次滚动写回就会用**旧快照**
// 覆盖掉刚导入的新凭据，导致用户刚粘贴的登录态凭空失效。故写入前先比对文件
// 内容：一旦发现被替换，本次放弃写入，把新值留给持有者，避免互相踩踏。
// [参数] sa: 待更新的账号结构（其 Cookies 字段会被快照覆盖）。
// [返回] 写入错误；被替换而主动放弃时返回 nil。
// 最近修改时间：2026-10-17；改动原因：新增，Jar 写回与防覆盖闸门。
func (j *durableJar) persist(sa *StoredAuth) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.path == "" {
		return nil
	}
	if j.replacedLocked() {
		return nil
	}

	sa.Cookies = j.snapshotLocked()
	if errWrite := writeAuthFileDirect(j.path, sa); errWrite != nil {
		return errWrite
	}
	j.saved = fileDigest(j.path)
	return nil
}
