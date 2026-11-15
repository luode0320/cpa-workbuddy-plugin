package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mustURL 解析测试用 URL。
// [参数] t: 测试上下文；raw: URL 字符串。
// [返回] 解析后的 URL。
// 最近修改时间：2026-10-17；改动原因：新增，测试辅助。
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("URL 解析失败 %q：%v", raw, err)
	}
	return u
}

// TestDurableJarCapturesSetCookie 校验上游下发的 Cookie 被记录进快照。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，Set-Cookie 捕获（测试点①）。
func TestDurableJarCapturesSetCookie(t *testing.T) {
	j := newDurableJar()
	u := mustURL(t, "https://accounts.doubao.com/ttwid/check/")
	j.SetCookies(u, []*http.Cookie{{
		Name:   "gd_random",
		Value:  "probe_value",
		Path:   "/",
		MaxAge: 604800,
	}})

	snap := j.snapshot()
	if len(snap) != 1 {
		t.Fatalf("应捕获 1 条 Cookie，实际 %d", len(snap))
	}
	got := snap[0].Cookie
	if got.Name != "gd_random" {
		t.Errorf("Cookie 名错误：%q", got.Name)
	}
	// MaxAge 应被折算为绝对 Expires，且落在未来。
	if !got.Expires.After(time.Now()) {
		t.Errorf("MaxAge 应折算为未来 Expires，实际 %v", got.Expires)
	}
	if got.MaxAge != 0 {
		t.Errorf("折算后 MaxAge 应清零，实际 %d", got.MaxAge)
	}
	if snap[0].Source != "https://accounts.doubao.com/" {
		t.Errorf("Source 记录错误：%q", snap[0].Source)
	}
}

// TestDurableJarDropsExpiredCookie 校验过期/登出 Cookie 被从快照移除。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，过期清理（测试点②）。
func TestDurableJarDropsExpiredCookie(t *testing.T) {
	j := newDurableJar()
	u := mustURL(t, "https://www.doubao.com/")

	// 先写入一条有效 Cookie。
	j.SetCookies(u, []*http.Cookie{{Name: "sessionid", Value: "v1", Path: "/"}})
	if len(j.snapshot()) != 1 {
		t.Fatal("预置 Cookie 应写入")
	}

	// 上游以 MaxAge<0 表达登出，应从快照移除。
	j.SetCookies(u, []*http.Cookie{{Name: "sessionid", Value: "", Path: "/", MaxAge: -1}})
	if n := len(j.snapshot()); n != 0 {
		t.Fatalf("MaxAge<0 的 Cookie 应被移除，实际剩余 %d", n)
	}
}

// TestDurableJarHasLiveSession 校验会话存活判定。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，失效判定（测试点⑤）。
func TestDurableJarHasLiveSession(t *testing.T) {
	j := newDurableJar()
	u := mustURL(t, "https://www.doubao.com/")

	if j.hasLiveSession() {
		t.Fatal("空 Jar 不应判定为有会话")
	}

	// 非会话 Cookie 不足以判定存活。
	j.SetCookies(u, []*http.Cookie{{Name: "dbx-web-theme", Value: "dark", Path: "/"}})
	if j.hasLiveSession() {
		t.Fatal("仅非会话 Cookie 不应判定为有会话")
	}

	// 会话主 Cookie（sessionid）存在即判定存活。
	j.SetCookies(u, []*http.Cookie{{Name: "sessionid", Value: "v1", Path: "/"}})
	if !j.hasLiveSession() {
		t.Fatal("sessionid 存在时应判定为有会话")
	}
}

// TestDurableJarHasLiveSessionExpired 校验已过期的会话 Cookie 判定为失效。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，过期会话失效判定。
func TestDurableJarHasLiveSessionExpired(t *testing.T) {
	j := newDurableJar()
	u := mustURL(t, "https://www.doubao.com/")
	j.SetCookies(u, []*http.Cookie{{
		Name:    "sessionid",
		Value:   "v1",
		Path:    "/",
		Expires: time.Now().Add(-time.Hour),
	}})
	if j.hasLiveSession() {
		t.Fatal("已过期的 sessionid 不应判定为有会话")
	}
}

// TestDurableJarSeedFromHeader 校验首次导入的 Cookie header 播种。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，导入播种路径。
func TestDurableJarSeedFromHeader(t *testing.T) {
	j := newDurableJar()
	j.seedFromHeader("flow_cur_user_sec_id=sec_v; sessionid=sess_v; ttwid=tw_v", doubaoSiteHost+"/")

	snap := j.snapshot()
	if len(snap) != 3 {
		t.Fatalf("应播种 3 条 Cookie，实际 %d", len(snap))
	}
	if !j.hasLiveSession() {
		t.Fatal("播种含 sessionid，应判定为有会话")
	}
}

// TestDurableJarPersistWritesSnapshot 校验快照写回账号文件并持久化。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，持久化生效（通过标准①②）。
func TestDurableJarPersistWritesSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doubao-persist.json")
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "persist",
		CookieHeader: validCookieHeader,
		Label:        "持久化测试",
	}
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("预置账号失败：%v", err)
	}

	j := newDurableJar()
	j.bind(path)
	j.seedFromHeader(validCookieHeader, doubaoSiteHost+"/")
	j.SetCookies(mustURL(t, "https://accounts.doubao.com/ttwid/check/"),
		[]*http.Cookie{{Name: "gd_random", Value: "rolled", Path: "/", MaxAge: 604800}})

	if err := j.persist(sa); err != nil {
		t.Fatalf("persist 失败：%v", err)
	}

	// 重新读取，确认滚动下发的 Cookie 已落盘（模拟插件重启后仍在）。
	back, errRead := readAuthFile(path)
	if errRead != nil {
		t.Fatalf("回读失败：%v", errRead)
	}
	foundRolled := false
	for _, rec := range back.Cookies {
		if rec.Cookie.Name == "gd_random" && rec.Cookie.Value == "rolled" {
			foundRolled = true
		}
	}
	if !foundRolled {
		t.Fatal("滚动下发的 gd_random 应已落盘并在重启后仍存在")
	}
}

// TestDurableJarPersistDefersToNewer 校验外部替换账号文件后本进程放弃覆盖。
//
// 场景：用户在面板重新导入了同一账号（或另一热重载实例更新了该文件），
// 本进程随后的一次滚动写回不得用旧快照把新凭据覆盖掉。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，防覆盖（测试点④）。
func TestDurableJarPersistDefersToNewer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doubao-defer.json")
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "defer",
		CookieHeader: validCookieHeader,
		Label:        "防覆盖测试",
	}
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("预置账号失败：%v", err)
	}

	j := newDurableJar()
	j.bind(path)

	// 外部替换：换掉文件内容与时间戳，模拟另一进程写入新凭据。
	newer := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "defer",
		CookieHeader: validCookieHeader,
		Label:        "外部新写入",
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatalf("模拟外部替换失败：%v", err)
	}
	if err := writeAuthFileDirect(path, newer); err != nil {
		t.Fatalf("外部写入失败：%v", err)
	}

	j.SetCookies(mustURL(t, "https://accounts.doubao.com/ttwid/check/"),
		[]*http.Cookie{{Name: "gd_random", Value: "stale", Path: "/", MaxAge: 604800}})

	if err := j.persist(sa); err != nil {
		t.Fatalf("persist 应静默放弃而非报错，实际：%v", err)
	}

	back, errRead := readAuthFile(path)
	if errRead != nil {
		t.Fatalf("回读失败：%v", errRead)
	}
	if back.Label != "外部新写入" {
		t.Fatalf("外部写入的账号不应被本进程旧快照覆盖，实际 label=%q", back.Label)
	}
	for _, rec := range back.Cookies {
		if rec.Cookie.Name == "gd_random" {
			t.Fatal("本进程不应把旧快照写回被外部替换的文件")
		}
	}
}

// TestDurableJarPersistDetectsSameSizeReplacement 校验等长内容的替换也能被识别。
//
// 回归背景：原判定用 os.SameFile + ModTime + Size，在 Windows 上「删除后重建」
// 会复用文件索引，两份等长 JSON 又可能落在同一时间戳刻度内，三项全部判为未变，
// 于是旧快照真的覆盖了用户刚导入的凭据。改为内容摘要后本用例必须稳定通过。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，覆盖防覆盖判定的失效场景。
func TestDurableJarPersistDetectsSameSizeReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doubao-samesize.json")
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "samesize",
		CookieHeader: validCookieHeader,
		Label:        "AAAA",
	}
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("预置账号失败：%v", err)
	}

	j := newDurableJar()
	j.bind(path)

	// 只改 label（等长）后原位写回：文件大小不变，索引可能被复用。
	sa.Label = "BBBB"
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("外部写入失败：%v", err)
	}

	j.SetCookies(mustURL(t, "https://accounts.doubao.com/ttwid/check/"),
		[]*http.Cookie{{Name: "gd_random", Value: "stale", Path: "/", MaxAge: 604800}})
	if err := j.persist(sa); err != nil {
		t.Fatalf("persist 应静默放弃：%v", err)
	}

	back, errRead := readAuthFile(path)
	if errRead != nil {
		t.Fatalf("回读失败：%v", errRead)
	}
	if back.Label != "BBBB" {
		t.Fatalf("等长内容替换也必须被识别，实际 label=%q", back.Label)
	}
	for _, rec := range back.Cookies {
		if rec.Cookie.Name == "gd_random" {
			t.Fatal("外部替换后不得写回旧快照")
		}
	}
}

// TestDurableJarPersistSucceedsWhenUnchanged 校验文件未被替换时写回照常生效。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，确认内容摘要判定不会误伤正常写回。
func TestDurableJarPersistSucceedsWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doubao-unchanged.json")
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "unchanged",
		CookieHeader: validCookieHeader,
		Label:        "正常写回",
	}
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("预置账号失败：%v", err)
	}

	j := newDurableJar()
	j.bind(path)
	j.SetCookies(mustURL(t, "https://accounts.doubao.com/ttwid/check/"),
		[]*http.Cookie{{Name: "gd_random", Value: "fresh", Path: "/", MaxAge: 604800}})

	if err := j.persist(sa); err != nil {
		t.Fatalf("文件未变时写回应成功：%v", err)
	}
	back, errRead := readAuthFile(path)
	if errRead != nil {
		t.Fatalf("回读失败：%v", errRead)
	}
	found := false
	for _, rec := range back.Cookies {
		if rec.Cookie.Name == "gd_random" && rec.Cookie.Value == "fresh" {
			found = true
		}
	}
	if !found {
		t.Fatal("文件未变时滚动 Cookie 应被写回")
	}
}

// TestDurableJarConcurrentSetCookies 校验并发写入不发生竞态。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，并发安全（6-review 关注点）。
func TestDurableJarConcurrentSetCookies(t *testing.T) {
	j := newDurableJar()
	u := mustURL(t, "https://accounts.doubao.com/")
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for k := 0; k < 50; k++ {
				j.SetCookies(u, []*http.Cookie{{
					Name:   "gd_random",
					Value:  "v",
					Path:   "/",
					MaxAge: 60,
				}})
				_ = j.snapshot()
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if len(j.snapshot()) != 1 {
		t.Fatalf("同一 key 的并发写入应收敛为 1 条，实际 %d", len(j.snapshot()))
	}
}

// TestAuthStateOf 校验登录态状态上报。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，状态上报（通过标准③）。
func TestAuthStateOf(t *testing.T) {
	live := &StoredAuth{CookieHeader: validCookieHeader}
	if got := authStateOf(live); got != "ok" {
		t.Errorf("含 sessionid 的账号应上报 ok，实际 %q", got)
	}

	dead := &StoredAuth{CookieHeader: "flow_cur_user_sec_id=sec_only"}
	if got := authStateOf(dead); got != "expired" {
		t.Errorf("无会话 Cookie 的账号应上报 expired，实际 %q", got)
	}

	// 快照优先：快照中会话已过期时，即使 header 看似齐备也应上报 expired。
	expiredSnapshot := &StoredAuth{
		CookieHeader: validCookieHeader,
		Cookies: []savedCookie{{
			Source: "https://www.doubao.com/",
			Cookie: http.Cookie{
				Name:    "sessionid",
				Value:   "v",
				Path:    "/",
				Expires: time.Now().Add(-time.Hour),
			},
		}},
	}
	if got := authStateOf(expiredSnapshot); got != "expired" {
		t.Errorf("快照会话过期时应上报 expired，实际 %q", got)
	}
}

// TestCookiesKeepsSessionCookiesForSiteHost 校验发往站点域名时保留 session 组。
//
// 0.1.21 的回归点：0.1.19 曾把 sessionid / sessionid_ss / sid_tt / sid_guard
// 当黑名单从推理请求里剔除，导致请求退化成「匿名设备」稳定回 710022002。
// 2026-11-15 四象限交替实测证明真正的原因是 fp 用了插件自拼的假值，会话组
// 反而是出字的必要条件，因此这里断言它们必须原样发出去。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 22:10:00；改动原因：断言方向翻转，出站不再过滤 session 组。
func TestCookiesKeepsSessionCookiesForSiteHost(t *testing.T) {
	j := newDurableJar()
	j.seedFromHeader("flow_cur_user_sec_id=sec_v; sessionid=s1; sessionid_ss=s2; sid_tt=s3; sid_guard=s4; uid_tt=u1", doubaoSiteHost+"/")

	seen := map[string]bool{}
	for _, c := range j.Cookies(mustURL(t, doubaoSiteHost+"/chat/completion")) {
		seen[c.Name] = true
	}
	for _, name := range []string{"sessionid", "sessionid_ss", "sid_tt", "sid_guard"} {
		if !seen[name] {
			t.Fatalf("发往站点域名时必须保留 %q，实际 %v", name, seen)
		}
	}
	if !seen["flow_cur_user_sec_id"] || !seen["uid_tt"] {
		t.Fatalf("非 session Cookie 同样应保留，实际 %v", seen)
	}
}

// TestCookiesKeepsSessionCookiesForAccountHost 校验判据端点仍能拿到 session 组。
//
// 判据端点（accounts.doubao.com）靠 session 组识别账号身份，若在导入/判据阶段
// 就把它丢掉，授权会直接失败。站点域名与判据域名的选 Cookie 逻辑走同一个 Jar，
// 这里一并锁定，避免以后为修推理链路把判据链路带坏。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 22:10:00；改动原因：理由口径同步，断言不变。
func TestCookiesKeepsSessionCookiesForAccountHost(t *testing.T) {
	j := newDurableJar()
	j.seedFromHeader("flow_cur_user_sec_id=sec_v; sessionid=s1; sid_tt=s3", accountInfoHost+"/")

	got := j.Cookies(mustURL(t, accountInfoHost+"/passport/account/info/v2/"))
	seen := map[string]bool{}
	for _, c := range got {
		seen[c.Name] = true
	}
	if !seen["sessionid"] || !seen["sid_tt"] {
		t.Fatalf("判据端点必须保留 session 组，实际 %v", seen)
	}
}

// TestCookiesInjectsFallbackCookieWhenMissing 校验缺 ttwid 时补出访客 Cookie。
//
// 账号凭据里通常没有 ttwid（它是站点首页下发的访客 Cookie）；缺这条时推理
// 请求会拿到空响应（0.16s、零事件），该分支保证不会退化成静默失败。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 22:10:00；改动原因：注释口径同步，断言不变。
func TestCookiesInjectsFallbackCookieWhenMissing(t *testing.T) {
	j := newDurableJar()
	// ttwid 只写在 accounts 域名下，站点域名选不到它，必须由兜底逻辑补出。
	j.seedFromHeader("flow_cur_user_sec_id=sec_v; sessionid=s1; ttwid=tw_from_account", accountInfoHost+"/")

	got := j.Cookies(mustURL(t, doubaoSiteHost+"/chat/completion"))
	hasTw := false
	for _, c := range got {
		if c.Name == fallbackSessionCookieName && strings.TrimSpace(c.Value) != "" {
			hasTw = true
		}
	}
	if !hasTw {
		t.Fatalf("缺 ttwid 时应补出访客 Cookie，实际 %v", cookieNamesOf(got))
	}
}

// TestCookiesSkipsFallbackWhenAlreadyPresent 校验已有 ttwid 时不重复补。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 22:10:00；改动原因：注释口径同步，断言不变。
func TestCookiesSkipsFallbackWhenAlreadyPresent(t *testing.T) {
	j := newDurableJar()
	j.seedFromHeader("flow_cur_user_sec_id=sec_v; sessionid=s1; ttwid=tw_site", doubaoSiteHost+"/")

	count := 0
	for _, c := range j.Cookies(mustURL(t, doubaoSiteHost+"/chat/completion")) {
		if c.Name == fallbackSessionCookieName {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ttwid 应恰好 1 条，实际 %d", count)
	}
}

// cookieNamesOf 提取 Cookie 名列表，供失败信息使用。
// [参数] cookies: Cookie 列表。
// [返回] 名列表。
// 最近修改时间：2026-11-15 15:58:00；改动原因：新增，测试辅助。
func cookieNamesOf(cookies []*http.Cookie) []string {
	out := make([]string, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, c.Name)
	}
	return out
}

// TestWarmUpFallbackCookieFetchesTtwid 校验缺 ttwid 时预热能把它取回来。
//
// 用 httptest 顶替站点：上游在首页响应里下发 ttwid，Jar 捕获后应能被站点域名选中。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 16:04:00；改动原因：新增，预热补 Cookie 回归。
func TestWarmUpFallbackCookieFetchesTtwid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: fallbackSessionCookieName, Value: "tw_from_warmup", Path: "/"})
		_, _ = w.Write([]byte("<html>ok</html>"))
	}))
	defer srv.Close()

	prev := siteWarmUpOverride
	siteWarmUpOverride = srv.URL
	defer func() { siteWarmUpOverride = prev }()

	j := newDurableJar()
	j.seedFromHeader("flow_cur_user_sec_id=sec_v; sessionid=s1", doubaoSiteHost+"/")
	if err := warmUpFallbackCookie(j, 3*time.Second); err != nil {
		t.Fatalf("预热不应失败：%v", err)
	}
	hasTw := false
	for _, c := range j.Cookies(mustURL(t, doubaoSiteHost+"/chat/completion")) {
		if c.Name == fallbackSessionCookieName && c.Value == "tw_from_warmup" {
			hasTw = true
		}
	}
	if !hasTw {
		t.Fatalf("预热后站点域名应能选中 ttwid，实际 %v", cookieNamesOf(j.Cookies(mustURL(t, doubaoSiteHost+"/chat/completion"))))
	}
}

// TestWarmUpFallbackCookieSkipsWhenPresent 校验已有 ttwid 时不再发预热请求。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 16:04:00；改动原因：新增，预热幂等回归。
func TestWarmUpFallbackCookieSkipsWhenPresent(t *testing.T) {
	hit := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
	}))
	defer srv.Close()

	prev := siteWarmUpOverride
	siteWarmUpOverride = srv.URL
	defer func() { siteWarmUpOverride = prev }()

	j := newDurableJar()
	j.seedFromHeader("flow_cur_user_sec_id=sec_v; ttwid=tw_present", doubaoSiteHost+"/")
	if err := warmUpFallbackCookie(j, 3*time.Second); err != nil {
		t.Fatalf("已有 ttwid 时不应报错：%v", err)
	}
	if n := atomic.LoadInt32(&hit); n != 0 {
		t.Fatalf("已有 ttwid 时不应预热，实际请求 %d 次", n)
	}
}
