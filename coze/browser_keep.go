package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件实现浏览器保活链路（TASK-07）。
//
// 背景：扣子把「裸 HTTP 打令牌端点」按客户端特征封了——同一份 Cookie，
// 从 Go 进程直连打 /passport/token/beat/v2/ 拿到的是
// error_code:401 会话过期，而在真实页面上下文里用同源 fetch 打同一个
// 端点却能正常换发（2026-11-21 实测）。差别不在 Cookie，而在请求发出的
// 环境：TLS 指纹、UA、埋点 Cookie、referer 与浏览器侧的前置会话状态。
//
// 因此这里不做「更聪明的 HTTP 客户端」，而是把换发动作整体搬进一个真实
// 浏览器实例：注入账号 Cookie → 打开扣子首页 → 页面内同源 fetch → 把浏览器
// 里的 Cookie 全量读回来。成功与否仍由 rotatedSessionCookies 判定，
// 与裸 HTTP 链路共用同一套判据，避免两套语义。

const (
	// browserKeepTimeout 是单账号一次保活的总超时。
	//
	// 冷启动 Chrome 3-8 秒，页面加载与两跳换发各几百毫秒到数秒，
	// 30 秒足够覆盖慢盘；再长就该判定为异常而不是继续等。
	browserKeepTimeout = 90 * time.Second
	// browserKeepProfileRoot 是 profile 根目录名，位于账号目录同级。
	browserKeepProfileRoot = "coze_browser_profiles"
	// browserKeepPageURL 是换发脚本的执行页面。
	//
	// 必须是扣子站内页面：同源 fetch 才能带上 Cookie，跨页会变成跨域请求
	// 并被浏览器的凭据策略挡下。
	browserKeepPageURL = cozeSiteHost + "/"
)

// errBrowserKeepDisabled 表示浏览器保活未启用。
var errBrowserKeepDisabled = errors.New("浏览器保活未启用")

// browserKeepHeadless 返回是否无头运行。
//
// 指针未配置时回落 true：生产容器没有桌面，开可见窗口等于起不来。
// [参数] 无。
// [返回] 无头运行时返回 true。
// 最近修改时间：2026-11-21；改动原因：新增，指针配置的读取出口。
func (c Config) browserKeepHeadless() bool {
	if c.BrowserKeepHeadless == nil {
		return true
	}
	return *c.BrowserKeepHeadless
}

// browserKeeper 串行化同一账号的保活动作。
//
// 为什么要串行：一个账号同时起两个浏览器会共用同一个 profile 目录，
// Chrome 的 profile 锁会让第二个实例直接退出；即使侥幸起来，两边写同一份
// Cookie 也会互相覆盖。
type browserKeeper struct {
	// mu 保护 running。
	mu sync.Mutex
	// running 记录正在保活的账号，防止并发重入。
	running map[string]struct{}
}

// browserKeeperDefault 是生产共用的保活实例。
var browserKeeperDefault = newBrowserKeeper()

// newBrowserKeeper 构造保活实例。
// [参数] 无。
// [返回] 已初始化的实例。
// 最近修改时间：2026-11-21；改动原因：新增，保活状态容器。
func newBrowserKeeper() *browserKeeper {
	return &browserKeeper{running: map[string]struct{}{}}
}

// tryAcquire 尝试占住某账号的保活权。
// [参数] key: 账号标识。
// [返回] 占住返回 true，已被占用返回 false。
// 最近修改时间：2026-11-21；改动原因：新增，防并发重入。
func (k *browserKeeper) tryAcquire(key string) bool {
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, busy := k.running[key]; busy {
		return false
	}
	k.running[key] = struct{}{}
	return true
}

// release 释放保活权。
// [参数] key: 账号标识。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，防并发重入。
func (k *browserKeeper) release(key string) {
	if k == nil {
		return
	}
	k.mu.Lock()
	delete(k.running, key)
	k.mu.Unlock()
}

// busy 判断某账号是否正在保活。
// [参数] key: 账号标识。
// [返回] 正在保活返回 true。
// 最近修改时间：2026-11-21；改动原因：新增，状态查询。
func (k *browserKeeper) busy(key string) bool {
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.running[key]
	return ok
}

// busyKeys 返回正在保活的账号标识，供状态接口展示。
// [参数] 无。
// [返回] 账号标识切片，按字典序排序。
// 最近修改时间：2026-11-21；改动原因：新增，面板可观测性。
func (k *browserKeeper) busyKeys() []string {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, 0, len(k.running))
	for key := range k.running {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// reviveSession 用真实浏览器重试一次会话换发。
//
// 只用在该账号的裸 HTTP 换发已被上游判定为会话失效之后：浏览器路径成本
// 高（起进程、加载页面、注入 Cookie），不能作为常规续期手段。
// [参数] sa: 账号。
// [返回] 换发结果；上游仍判失效时返回错误，暂时性故障也返回错误。
// 最近修改时间：2026-11-21；改动原因：新增，浏览器内换发。
func (k *browserKeeper) reviveSession(sa *StoredAuth) (*renewResult, error) {
	if sa == nil {
		return nil, errors.New("账号为空")
	}
	cfg := currentConfig()
	if !cfg.BrowserKeepEnabled {
		return nil, errBrowserKeepDisabled
	}
	if strings.TrimSpace(sa.CookieHeader) == "" {
		return nil, errors.New("账号凭据为空")
	}
	key := alertKeyOf(sa)
	if key == "" {
		key = sa.PhysicalPath
	}
	if !k.tryAcquire(key) {
		return nil, errors.New("该账号已有保活任务在跑")
	}
	defer k.release(key)

	ctx, cancel := context.WithTimeout(context.Background(), browserKeepTimeout)
	defer cancel()

	binary, errBinary := findChromeBinary(cfg.BrowserKeepChromePath)
	if errBinary != nil {
		return nil, errBinary
	}
	profileDir, errProfile := browserProfileDir(cfg.BrowserKeepProfileDir, key)
	if errProfile != nil {
		return nil, errProfile
	}
	// 每次保活结束都清掉 profile：留着只会累积磁盘，且下次启动时
	// Chrome 可能拿旧 profile 里已失效的 Cookie 覆盖我们注入的新值。
	defer func() { _ = os.RemoveAll(profileDir) }()

	proc, errLaunch := launchChrome(ctx, binary, profileDir, cfg.browserKeepHeadless())
	if errLaunch != nil {
		return nil, errLaunch
	}
	defer proc.close()

	client := &http.Client{Timeout: cdpCallTimeout}
	// launchChrome 用的是带总超时的 ctx；进程探活沿用同一个 ctx，
	// 保证整体不会超过 browserKeepTimeout。
	procCtx, procCancel := context.WithTimeout(ctx, cdpReadyTimeout)
	defer procCancel()
	if errReady := proc.waitChromeReady(procCtx, client); errReady != nil {
		return nil, errReady
	}
	cdp, errDial := dialCDPPage(ctx, client, proc.port)
	if errDial != nil {
		return nil, errDial
	}
	defer cdp.close()

	cookies := cookiesFromHeader(sa.CookieHeader)
	if errSet := cdp.setCookies(ctx, cookies); errSet != nil {
		return nil, fmt.Errorf("注入账号 Cookie 失败：%w", errSet)
	}
	if errNav := cdp.navigate(ctx, browserKeepPageURL); errNav != nil {
		return nil, fmt.Errorf("打开扣子页面失败：%w", errNav)
	}

	token, describe, errBeat := beatInPage(ctx, cdp, "")
	if errBeat != nil {
		return nil, errBeat
	}
	if token == "" {
		return nil, fmt.Errorf("浏览器内取令牌失败：%s", describe)
	}
	// 第二步用令牌换 Cookie：真实页面里这一步同时会把 Set-Cookie 落进
	// 浏览器 Cookie 罐，因此不必解析响应头，直接读罐子即可。
	if _, describe2, errBeat2 := beatInPage(ctx, cdp, token); errBeat2 != nil {
		return nil, errBeat2
	} else if describe2 != "" {
		// 第二步偶发业务失败（例如上游临时拒绝）：不当作致命错误，
		// 继续读罐子——只要会话主项真被换掉，本轮就是成功的。
		hostLogf("warn", "浏览器保活第二步未成功", map[string]any{
			"auth_id": sa.AuthID,
			"note":    describe2,
		})
	}

	fresh, errCookies := cdp.getAllCookies(ctx)
	if errCookies != nil {
		return nil, fmt.Errorf("读取浏览器 Cookie 失败：%w", errCookies)
	}
	// 用合并语义：浏览器罐里除了会话主项还混着大量扣子自有埋点，
	// 直接整份替换会让 Cookie 头膨胀且丢掉账号本来的域信息。
	store := mergeSessionCookies(sa.CookieHeader, fresh)
	res := &renewResult{
		Store:        store,
		Cookies:      store.snapshot(),
		CookieHeader: store.header(),
	}
	if !rotatedSessionCookies(sa.CookieHeader, fresh) {
		res.Note = unattemptedNote("浏览器换发未拿到新会话主 Cookie", len(fresh))
		return res, nil
	}
	res.Renewed = true
	res.Note = fmt.Sprintf("浏览器保活完成：换发 %d 条 Cookie（含会话主项）", len(fresh))
	return res, nil
}

// beatInPage 在页面上下文执行一次令牌端点请求。
//
// 脚本里必须显式 credentials:'include'：同源请求虽然默认带 Cookie，
// 但扣子页面会被 service worker 接管，显式声明更稳。
// [参数] ctx: 生命周期；cdp: 页面连接；token: 一次性令牌，空表示用 Cookie 换令牌。
// [返回] 响应头里的令牌、失败说明与执行错误。
// 最近修改时间：2026-11-21；改动原因：新增，页面内换发。
func beatInPage(ctx context.Context, cdp *cdpClient, token string) (string, string, error) {
	headers := "'sdk-version':'2','Content-Type':'application/json'"
	if token != "" {
		headers += fmt.Sprintf(",'x-tt-token':'%s'", jsStringEscape(token))
	}
	script := "(async()=>{" +
		"const r=await fetch('/passport/token/beat/v2/',{method:'POST',credentials:'include',headers:{" + headers + "},body:'{}'});" +
		"const j=await r.json().catch(()=>({}));" +
		"return JSON.stringify({token:r.headers.get('x-tt-token')||''," +
		"status:r.status," +
		"message:(j&&j.message)||''," +
		"error_code:(j&&j.data&&j.data.error_code)||0," +
		"description:(j&&j.data&&j.data.description)||''})" +
		"})()"
	raw, errEval := cdp.evaluate(ctx, script)
	if errEval != nil {
		return "", "", errEval
	}
	var parsed struct {
		Token       string `json:"token"`
		Status      int    `json:"status"`
		Message     string `json:"message"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if errUnmarshal := json.Unmarshal([]byte(raw), &parsed); errUnmarshal != nil {
		return "", "", fmt.Errorf("解析页面响应失败：%w", errUnmarshal)
	}
	// 该端点恒返回 HTTP 200，成败只能看 body（与裸 HTTP 链路同一教训）。
	ok := strings.EqualFold(strings.TrimSpace(parsed.Message), "success") && parsed.ErrorCode == 0
	if !ok {
		describe := strings.TrimSpace(parsed.Description)
		if describe == "" {
			describe = fmt.Sprintf("上游返回 message=%q error_code=%d", parsed.Message, parsed.ErrorCode)
		}
		if parsed.Status != http.StatusOK {
			describe = fmt.Sprintf("HTTP %d：%s", parsed.Status, describe)
		}
		return strings.TrimSpace(parsed.Token), describe, nil
	}
	return strings.TrimSpace(parsed.Token), "", nil
}

// cookiesFromHeader 把 Cookie 头解析成可注入浏览器的 Cookie 列表。
//
// 注入时不带域：域由 setCookies 统一按扣子主域补全，避免同一个会话在
// www 与裸域上各存一份。
// [参数] header: Cookie 头原文。
// [返回] Cookie 列表。
// 最近修改时间：2026-11-21；改动原因：新增，凭据注入前置。
func cookiesFromHeader(header string) []*http.Cookie {
	parsed := parseCookieHeader(header)
	if len(parsed) == 0 {
		return nil
	}
	out := make([]*http.Cookie, 0, len(parsed))
	for name, value := range parsed {
		if strings.TrimSpace(name) == "" {
			continue
		}
		out = append(out, &http.Cookie{Name: name, Value: value, Path: "/"})
	}
	return out
}

// browserProfileDir 计算某账号的独立 profile 目录。
//
// 每个账号必须独占一个目录：共用会让不同账号的 Cookie 在同一 Cookie 罐里
// 互相覆盖，等于把两个号的会话搅在一起。
// [参数] configured: 配置的根目录，空则用默认位置；key: 账号标识。
// [返回] 目录绝对路径。
// 最近修改时间：2026-11-21；改动原因：新增，profile 落点。
func browserProfileDir(configured, key string) (string, error) {
	root := strings.TrimSpace(configured)
	if root == "" {
		home, errHome := os.UserHomeDir()
		if errHome != nil {
			return "", fmt.Errorf("无法确定 profile 根目录：%w", errHome)
		}
		root = filepath.Join(home, ".antigravity_cockpit", browserKeepProfileRoot)
	}
	return filepath.Join(root, safeProfileName(key)), nil
}

// safeProfileName 把账号标识转成安全的目录名。
// [参数] key: 账号标识。
// [返回] 只含字母数字与下划线的名字。
// 最近修改时间：2026-11-21；改动原因：新增，避免路径穿越。
func safeProfileName(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
