package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 本文件实现扣子凭据的兑换、校验与续期。
//
// 认证路线（2026-10-17 裁定 + 端到端验证）：用户在扣子网页完成官方登录后，
// 由同源脚本取一次性 x-tt-token 交给插件；插件用该 token 调 token beat
// 换回会话 Cookie。插件不实现登录表单、不读浏览器 Cookie 库。
//
// 续期是「Cookie → 新 token → 新 Cookie」的双向循环（实测成立）：
// 带 Cookie 调 token beat 返回新 x-tt-token，带该 token 调同一端点返回新
// Set-Cookie，因此会话可无限滚动，不存在 Cookie 快照硬失效。

const (
	// cozeAuthFilePrefix 是扣子凭据文件的命名前缀。
	cozeAuthFilePrefix = "coze-"

	// tokenTTLHint 是 x-tt-token 的保守有效期提示。
	//
	// 桌面端 AUTH_CALLBACK_TIMEOUT_MS（5 分钟）是它等待 deep link 的窗口，
	// 不是 token 的 TTL——真实 TTL 未实测，因此这里不做任何过期判定，
	// 只把上游返回的失败原样上报。
	tokenTTLHint = 5 * time.Minute
)

// endpointOverride 是端点覆盖点，供测试指向 httptest 服务端。
var endpointOverride struct {
	tokenBeat   string
	accountInfo string
}

// swapEndpoints 临时覆盖端点，返回恢复函数（仅测试使用）。
// [参数] tokenBeat: 令牌端点 URL；accountInfo: 判据端点 URL；空串表示不覆盖。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-10-18；改动原因：新增，让链路可在 httptest 下验证。
func swapEndpoints(tokenBeat, accountInfo string) func() {
	prev := endpointOverride
	endpointOverride.tokenBeat = tokenBeat
	endpointOverride.accountInfo = accountInfo
	return func() { endpointOverride = prev }
}

// resolveTokenBeatURL 返回实际使用的令牌端点。
// [参数] 无。
// [返回] 端点 URL。
// 最近修改时间：2026-10-18；改动原因：新增，测试覆盖入口。
func resolveTokenBeatURL() string {
	if endpointOverride.tokenBeat != "" {
		return endpointOverride.tokenBeat
	}
	return tokenBeatURL()
}

// resolveAccountInfoURL 返回实际使用的判据端点。
// [参数] 无。
// [返回] 端点 URL。
// 最近修改时间：2026-10-18；改动原因：新增，测试覆盖入口。
func resolveAccountInfoURL() string {
	if endpointOverride.accountInfo != "" {
		return endpointOverride.accountInfo
	}
	return accountInfoURL()
}

// exchangeResult 是一次令牌兑换的结果。
type exchangeResult struct {
	// Store 是本次换到的 Cookie 容器（已就绪，可直接发请求）。
	Store *cookieStore
	// Cookies 是带属性的快照，供落盘。
	Cookies []savedCookie
	// CookieHeader 是拼装好的 Cookie 请求头。
	CookieHeader string
	// Token 是上游返回的新 x-tt-token（带 Cookie 调用时才有）。
	Token string
	// SetCookieCount 是本次响应下发的 Set-Cookie 条数。
	SetCookieCount int
}

// httpDoer 抽象一次 HTTP 往返，便于测试注入。
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// newUpstreamClient 构造上游 HTTP 客户端。
// [参数] timeout: 请求超时。
// [返回] 客户端。
// 最近修改时间：2026-10-18；改动原因：新增，超时统一出口。
func newUpstreamClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = time.Duration(currentConfig().TimeoutSeconds) * time.Second
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// doTokenBeat 发起一次令牌端点请求。
//
// 两种调用形态：
//   - token 非空 → 请求头带 x-tt-token（不带 Cookie）→ 换 Cookie；
//   - cookieHeader 非空 → 请求头带 Cookie → 换新 token。
//
// [参数] client: HTTP 客户端；token: 一次性令牌；cookieHeader: 会话 Cookie 头。
// [返回] 响应体、Set-Cookie 列表、响应头令牌与错误。
// 最近修改时间：2026-10-18；改动原因：新增，令牌端点唯一调用点。
func doTokenBeat(client httpDoer, token, cookieHeader string) (*tokenBeatResponse, []*http.Cookie, string, error) {
	req, errReq := http.NewRequest(http.MethodPost, resolveTokenBeatURL(), bytes.NewReader([]byte("{}")))
	if errReq != nil {
		return nil, nil, "", fmt.Errorf("构造令牌请求失败：%w", errReq)
	}
	req.Header.Set("Content-Type", tokenBeatContentType)
	req.Header.Set("sdk-version", tokenBeatSDKVersion)
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Referer", cozeSiteHost+"/")
	if token != "" {
		req.Header.Set("x-tt-token", token)
	}
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, nil, "", fmt.Errorf("令牌请求失败：%w", errDo)
	}
	defer resp.Body.Close()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, nil, "", fmt.Errorf("读取令牌响应失败：%w", errRead)
	}

	var parsed tokenBeatResponse
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return nil, nil, "", fmt.Errorf("令牌响应解析失败（HTTP %d）：%w", resp.StatusCode, errUnmarshal)
	}

	// 该端点始终返回 HTTP 200，非 2xx 属网关层异常，单独上报。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &parsed, nil, "", fmt.Errorf("令牌端点返回 HTTP %d：%s", resp.StatusCode, parsed.describe())
	}

	setCookies := resp.Cookies()
	return &parsed, setCookies, strings.TrimSpace(resp.Header.Get("x-tt-token")), nil
}

// exchangeTokenForCookies 用一次性 x-tt-token 换会话 Cookie。
//
// 契约（2026-10-17 真实账号实测）：HTTP 200 / message=success /
// error_code=0 / 13 条 Set-Cookie（含 sessionid、sessionid_ss、sid_tt、sid_guard）。
// [参数] token: 浏览器同源脚本取到的一次性令牌。
// [返回] 兑换结果与错误。
// 最近修改时间：2026-10-18；改动原因：新增，首次授权换 Cookie。
func exchangeTokenForCookies(token string) (*exchangeResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("令牌为空")
	}

	parsed, setCookies, _, errDo := doTokenBeat(newUpstreamClient(0), token, "")
	if errDo != nil {
		return nil, errDo
	}
	if !parsed.isSuccess() {
		return nil, fmt.Errorf("令牌兑换失败：%s", parsed.describe())
	}
	if len(setCookies) == 0 {
		return nil, fmt.Errorf("令牌兑换失败：上游未下发任何 Cookie")
	}

	store := newCookieStore()
	store.SetCookies(mustParseURL(cozeSiteHost+"/"), setCookies)
	return &exchangeResult{
		Store:          store,
		Cookies:        store.snapshot(),
		CookieHeader:   store.header(),
		SetCookieCount: len(setCookies),
	}, nil
}

// mustParseURL 解析 URL，失败时返回一个安全的最小值。
// [参数] raw: 原始 URL。
// [返回] 解析后的 URL。
// 最近修改时间：2026-10-18；改动原因：新增，内部常量 URL 的解析兜底。
func mustParseURL(raw string) *url.URL {
	u, errParse := url.Parse(raw)
	if errParse != nil || u.Host == "" {
		return &url.URL{Scheme: "https", Host: "www.coze.cn", Path: "/"}
	}
	return u
}

// probeAccountInfo 调用判据端点确认登录态是否有效。
//
// 判据：message == "success" 且 data.sec_user_id 非空（实测有区分度）。
// [参数] cookieHeader: 会话 Cookie 头。
// [返回] 判据响应与错误。
// 最近修改时间：2026-10-18；改动原因：新增，登录态有效性判据。
func probeAccountInfo(cookieHeader string) (*accountInfoResponse, error) {
	cookieHeader = strings.TrimSpace(cookieHeader)
	if cookieHeader == "" {
		return nil, fmt.Errorf("Cookie 为空")
	}

	req, errReq := http.NewRequest(http.MethodGet, resolveAccountInfoURL(), nil)
	if errReq != nil {
		return nil, fmt.Errorf("构造判据请求失败：%w", errReq)
	}
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Referer", cozeSiteHost+"/")

	resp, errDo := newUpstreamClient(0).Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("判据请求失败：%w", errDo)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("判据端点返回 HTTP %d", resp.StatusCode)
	}

	var out accountInfoResponse
	if errDecode := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); errDecode != nil {
		return nil, fmt.Errorf("判据响应解析失败：%w", errDecode)
	}
	if !out.isSuccess() {
		return &out, fmt.Errorf("登录态无效：判据端点返回 message=%q，请确认已登录扣子", out.Message)
	}
	return &out, nil
}

// authIDForSecUserID 由 sec_user_id 派生稳定的账号标识。
//
// 用哈希而不是原值：sec_user_id 长 76 字符，直接做文件名既超长又暴露标识；
// 同一账号重复授权必须落到同一个文件，因此必须是确定性派生。
// [参数] secUserID: 账号句柄。
// [返回] 形如 coze-<hash8> 的账号标识。
// 最近修改时间：2026-10-18；改动原因：新增，账号标识派生。
func authIDForSecUserID(secUserID string) string {
	key := strings.TrimSpace(secUserID)
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return cozeAuthFilePrefix + hex.EncodeToString(digest[:8])
}

// saveFromToken 用一次性令牌完成一次完整的账号落盘。
//
// 链路：令牌 → token beat 换 Cookie → 判据端点校验 → 落盘。
// 判据不通过时不落盘，避免把无效凭据写成可用账号。
// [参数] token: 一次性令牌；label: 备注名（可空）。
// [返回] 落盘后的账号结构与错误。
// 最近修改时间：2026-10-18；改动原因：新增，首次授权主链路。
func saveFromToken(token, label string) (*StoredAuth, error) {
	exchanged, errExchange := exchangeTokenForCookies(token)
	if errExchange != nil {
		return nil, errExchange
	}

	info, errProbe := probeAccountInfo(exchanged.CookieHeader)
	if errProbe != nil {
		return nil, errProbe
	}

	authID := authIDForSecUserID(info.secUserID())
	if authID == "" {
		return nil, fmt.Errorf("无法派生账号标识：判据端点未返回 sec_user_id")
	}

	if strings.TrimSpace(label) == "" {
		label = strings.TrimSpace(info.screenName())
	}
	if label == "" {
		label = "扣子 " + maskIdentifier(info.secUserID())
	}

	now := time.Now().Format(time.RFC3339)
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       authID,
		CookieHeader: exchanged.CookieHeader,
		SecUserID:    info.secUserID(),
		UserID:       info.userID(),
		ScreenName:   info.screenName(),
		Mobile:       info.mobile(),
		Cookies:      exchanged.Cookies,
		LoginAt:      now,
		LastRenewAt:  now,
		Label:        label,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	fullPath := getAccountsDir() + "/" + authID + ".json"
	if errWrite := writeAuthFileDirect(fullPath, sa); errWrite != nil {
		return nil, errWrite
	}
	sa.PhysicalPath = fullPath
	return sa, nil
}

// renewResult 是一次续期的结果。
type renewResult struct {
	// Renewed 表示本轮确实完成了换发（Cookie 或 token 有更新）。
	Renewed bool
	// Store 是续期后的 Cookie 容器。
	Store *cookieStore
	// Cookies 是续期后的快照。
	Cookies []savedCookie
	// CookieHeader 是续期后的 Cookie 头。
	CookieHeader string
	// Note 说明本轮的实际走向（供日志与面板诊断）。
	Note string
}

// renewSession 执行一轮「Cookie → 新 token → 新 Cookie」续期。
//
// 实测契约：带 Cookie 调 token beat 返回新 x-tt-token 且 Set-Cookie 为 0 条；
// 再用该 token 调同一端点才拿到新 Cookie。实现同时兼容「一次调用就下发
// Set-Cookie」的形态——若第一步已换掉会话主 Cookie，直接用，不再走第二步。
//
// 两条判据缺一不可（2026-11-21 生产复盘补入）：
//   - 是否换发看会话主项是否变化，不看下发条数；
//   - 新容器必须叠加在既有 Cookie 之上，不整份替换。
//
// [参数] sa: 待续期账号。
// [返回] 续期结果与错误。
// 最近修改时间：2026-11-21；改动原因：换发判据改为会话主项，Cookie 改为叠加合并。
func renewSession(sa *StoredAuth) (*renewResult, error) {
	if sa == nil || strings.TrimSpace(sa.CookieHeader) == "" {
		return nil, fmt.Errorf("账号凭据为空")
	}

	client := newUpstreamClient(0)

	// 步骤 1：带 Cookie 换新 token。
	step1, step1Cookies, newToken, errStep1 := doTokenBeat(client, "", sa.CookieHeader)
	if errStep1 != nil {
		return nil, errStep1
	}
	if !step1.isSuccess() {
		return nil, fmt.Errorf("续期失败（第一步）：%s", step1.describe())
	}

	// 形态 A：上游在第一步就换掉了会话主 Cookie，省去第二步。
	//
	// 判据必须锚定「会话主项是否真的变了」，不能只看下发条数：死会话打
	// beat 同样会下发若干埋点 Cookie，按条数判会把未换发误记成成功，
	// LastRenewAt 一路前进而会话早已失效。
	if rotatedSessionCookies(sa.CookieHeader, step1Cookies) {
		store := mergeSessionCookies(sa.CookieHeader, step1Cookies)
		return &renewResult{
			Renewed:      true,
			Store:        store,
			Cookies:      store.snapshot(),
			CookieHeader: store.header(),
			Note:         fmt.Sprintf("第一步直接下发 %d 条 Cookie（含会话主项）", len(step1Cookies)),
		}, nil
	}

	// 形态 B：只拿到 token，需要用 token 再换一次 Cookie。
	if newToken == "" {
		return &renewResult{
			Note: unattemptedNote("上游既未下发会话 Cookie 也未返回 x-tt-token", len(step1Cookies)),
		}, nil
	}

	step2, step2Cookies, _, errStep2 := doTokenBeat(client, newToken, "")
	if errStep2 != nil {
		return nil, errStep2
	}
	if !step2.isSuccess() {
		return nil, fmt.Errorf("续期失败（第二步）：%s", step2.describe())
	}
	if !rotatedSessionCookies(sa.CookieHeader, step2Cookies) {
		return &renewResult{
			Note: unattemptedNote("第二步未换到会话主 Cookie", len(step2Cookies)),
		}, nil
	}

	store := mergeSessionCookies(sa.CookieHeader, step2Cookies)
	return &renewResult{
		Renewed:      true,
		Store:        store,
		Cookies:      store.snapshot(),
		CookieHeader: store.header(),
		Note:         fmt.Sprintf("双向换发完成：token 换回 %d 条 Cookie（含会话主项）", len(step2Cookies)),
	}, nil
}

// unattemptedNote 拼装「未换发」的说明文案。
//
// 保留上游下发条数：运维据此区分「上游什么都没给」（条数 0，多为端点异常）
// 与「只给了埋点 Cookie」（条数 >0，会话已被上游吊销），避免面板上看不出差异。
// [参数] reason: 未换发原因；count: 本轮下发的 Cookie 条数。
// [返回] 说明文案。
// 最近修改时间：2026-11-21；改动原因：新增，未换发原因可区分。
func unattemptedNote(reason string, count int) string {
	if count == 0 {
		return reason + "（本轮下发 0 条 Cookie）"
	}
	return fmt.Sprintf("%s（本轮下发 %d 条 Cookie，无会话主项）", reason, count)
}

// renewCanWriteFn 是续期写盘的活跃实例闸门注入点，供单测替换。
//
// 默认接 ensureActiveInstanceBeforeWrite：插件热重载后旧实例仍在内存里跑着
// 定时器，它手里是上一版的 Cookie 与阈值，写盘会把新实例刚换到的凭据覆盖
// 回旧值。积分巡检早已接了这个闸门（credit_sweep.go:53），续期链路漏接。
var renewCanWriteFn = ensureActiveInstanceBeforeWrite

// applyRenewal 把续期结果写回账号并落盘。
//
// 防覆盖三闸门，顺序不可调换：
//  1. 只有真换发才写（Renewed=false 时不落盘，避免刷新 LastRenewAt）；
//  2. 当前实例必须仍是宿主认可的活跃实例（热重载后的旧实例不得写盘）；
//  3. 账号文件在读取之后未被外部替换（用户重新授权会换掉整份凭据）。
//
// 摘要必须在读取时锚定——写前才取会永远比对到自己。
// [参数] sa: 账号；res: 续期结果。
// [返回] 写入错误；被外部替换或实例退役而放弃时返回 nil。
// 最近修改时间：2026-11-21；改动原因：新增活跃实例闸门，堵住热重载后旧实例覆盖。
func applyRenewal(sa *StoredAuth, res *renewResult) error {
	if sa == nil || res == nil || !res.Renewed {
		return nil
	}
	if sa.PhysicalPath == "" {
		return fmt.Errorf("账号缺少物理路径，无法落盘")
	}
	if renewCanWriteFn != nil && !renewCanWriteFn(hostLogf) {
		return nil
	}
	if accountReplaced(sa) {
		return nil
	}

	// 失效状态要留旧值：告警只在真正发生迁移时推一条，
	// 每轮续期都成功的老账号不该产生任何告警。
	prevInvalid := sa.AuthInvalid
	sa.CookieHeader = res.CookieHeader
	sa.Cookies = res.Cookies
	sa.LastRenewAt = time.Now().Format(time.RFC3339)
	sa.UpdatedAt = sa.LastRenewAt
	// 续期成功即证明会话有效，清掉此前可能留下的上游失效标记。
	sa.AuthInvalid = false
	if errWrite := writeAuthFileDirect(sa.PhysicalPath, sa); errWrite != nil {
		return errWrite
	}
	// 写入成功后刷新基准，使同一进程内的后续轮次以新内容为起点。
	sa.LoadedDigest = fileDigest(sa.PhysicalPath)
	// 告警放在落盘之后：写盘失败时状态并没有真的迁移，
	// 提前推送会让人去查一个并不成立的状态变化。
	notifyAuthStateChange(prevInvalid, false, sa)
	return nil
}

// markSessionInvalid 记录「上游明确判定会话失效」。
//
// 只在错误文案来自上游会话判定时调用：网络抖动、限流等瞬态失败不能
// 把账号标成失效，否则面板会误报，用户会去重新授权一个本来可用的账号。
//
// 同样受活跃实例闸门约束：热重载后的旧实例手里是旧凭据，它判出的「失效」
// 可能只是旧 Cookie 的问题，写进文件会把新实例刚续好的账号标成待授权。
// [参数] sa: 账号；reason: 上游错误文案。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增活跃实例闸门，旧实例不得写失效标记。
func markSessionInvalid(sa *StoredAuth, reason string) {
	if sa == nil || sa.PhysicalPath == "" || !isSessionExpiredError(reason) {
		return
	}
	if renewCanWriteFn != nil && !renewCanWriteFn(hostLogf) {
		return
	}
	if accountReplaced(sa) {
		return
	}
	prevInvalid := sa.AuthInvalid
	sa.AuthInvalid = true
	sa.UpdatedAt = time.Now().Format(time.RFC3339)
	if errWrite := writeAuthFileDirect(sa.PhysicalPath, sa); errWrite != nil {
		return
	}
	sa.LoadedDigest = fileDigest(sa.PhysicalPath)
	// 同一账号连续 N 轮判定失效只推第一条：去重表按账号记住已推送状态。
	notifyAuthStateChange(prevInvalid, true, sa)
}

// isSessionExpiredError 判断错误是否表示上游判定会话已失效。
//
// 判据是上游固定文案「会话过期」/「登录认证失败」与扣子错误码 700012006；
// 其余错误（超时、限流、5xx）都属瞬态，不得据此标记账号失效。
// [参数] reason: 错误文案。
// [返回] 属于会话失效时返回 true。
// 最近修改时间：2026-10-19；改动原因：新增，失效判定唯一出口。
func isSessionExpiredError(reason string) bool {
	text := strings.ToLower(strings.TrimSpace(reason))
	if text == "" {
		return false
	}
	return strings.Contains(text, "会话过期") ||
		strings.Contains(text, "登录认证失败") ||
		strings.Contains(text, "login verification is invalid") ||
		strings.Contains(text, "700012006")
}

// accountReplaced 判断账号文件在读取之后是否被外部改写。
//
// 没有读取基准（例如内存里新建、尚未落盘的账号）时返回 false：
// 那种情况下不存在「被外部覆盖」的对象。
// [参数] sa: 账号。
// [返回] 已被外部替换时返回 true。
// 最近修改时间：2026-10-18；改动原因：新增，读取基准防覆盖判据。
func accountReplaced(sa *StoredAuth) bool {
	if sa == nil || sa.PhysicalPath == "" || sa.LoadedDigest == "" {
		return false
	}
	return fileDigest(sa.PhysicalPath) != sa.LoadedDigest
}

// renewAccountByID 按账号标识执行一轮续期并落盘。
// [参数] authID: 账号标识。
// [返回] 续期结果与错误。
// 最近修改时间：2026-11-21；改动原因：查找段抽出为 findAuthByID 后改为复用，去掉重复遍历。
func renewAccountByID(authID string) (*renewResult, error) {
	sa, errFind := findAuthByID(authID)
	if errFind != nil {
		return nil, errFind
	}
	res, errRenew := renewSession(sa)
	if errRenew != nil {
		// 上游明确说会话过期时留下标记：否则面板会一直显示「可用」，
		// 用户看不出这个账号已经需要重新授权。
		markSessionInvalid(sa, errRenew.Error())
		return nil, errRenew
	}
	if errApply := applyRenewal(sa, res); errApply != nil {
		return nil, errApply
	}
	return res, nil
}
