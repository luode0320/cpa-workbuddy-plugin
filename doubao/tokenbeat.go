package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 本文件实现「浏览器令牌 → 会话 Cookie」的一次性兑换。
//
// 认证路线（2026-11-14 改造）：豆包的关键会话 Cookie（sessionid / sid_guard）是
// HttpOnly，页面 JS 读不到，让用户在开发者工具里手工复制既麻烦又容易漏字段。
// 因此改成：用户在豆包页面点一次授权书签，由同源脚本调 passport 的令牌端点取
// 一次性 x-tt-token，交给插件用它换回完整 Set-Cookie。插件不读浏览器 Cookie
// 库、也不实现登录表单。
//
// 端点可行性（2026-11-14 实测）：www.doubao.com/passport/token/beat/v2/ 存在，
// 未带任何凭据时返回 HTTP 200 + {"message":"error","data":{"error_code":401,
// "error_name":"session_expired","description":"会话过期，请重新登录"}}，
// 与扣子插件侧的同名端点逐字段一致，说明两者共用同一套 passport 网关。

const (
	// tokenBeatPath 是令牌双向换发端点。
	//
	// 双向契约（与扣子插件同一网关）：带会话 Cookie 调用返回 x-tt-token；
	// 带 x-tt-token 请求头调用返回一批 Set-Cookie。本插件只用第二个方向——
	// 首次授权换 Cookie。续期仍走既有 Cookie 滚动，不在此处引入双向循环。
	tokenBeatPath = "/passport/token/beat/v2/"

	// tokenBeatSDKVersion 是令牌端点要求的 SDK 版本头值，缺失会被拒。
	tokenBeatSDKVersion = "2"

	// tokenBeatContentType 是令牌端点接受的 Content-Type。
	//
	// 必须精确为 application/json：工作模式的 /chat/completion 另有一套
	// 编码要求，两者不要互相套用。
	tokenBeatContentType = "application/json"
)

// tokenBeatEndpointOverride 是令牌端点的测试覆盖点；非空时优先使用。
var tokenBeatEndpointOverride string

// swapTokenBeatEndpoint 临时覆盖令牌端点，返回恢复函数（仅测试使用）。
// [参数] endpoint: 替换后的完整端点 URL；空串表示恢复默认。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-11-14；改动原因：新增，让兑换链路可在 httptest 下验证。
func swapTokenBeatEndpoint(endpoint string) func() {
	prev := tokenBeatEndpointOverride
	tokenBeatEndpointOverride = endpoint
	return func() { tokenBeatEndpointOverride = prev }
}

// tokenBeatURL 返回实际使用的令牌端点。
// [参数] 无。
// [返回] 端点 URL。
// 最近修改时间：2026-11-14；改动原因：新增，端点集中出口。
func tokenBeatURL() string {
	if tokenBeatEndpointOverride != "" {
		return tokenBeatEndpointOverride
	}
	return doubaoSiteHost + tokenBeatPath
}

// tokenBeatResponse 是令牌端点响应体。
//
// 该端点无论成败都返回 HTTP 200，成败只能读 message 与 data.error_code。
type tokenBeatResponse struct {
	Message string `json:"message"`
	Data    struct {
		ErrorCode   int    `json:"error_code"`
		ErrorName   string `json:"error_name"`
		Description string `json:"description"`
	} `json:"data"`
}

// isSuccess 判断响应是否为业务成功。
// [参数] r: 解析后的响应。
// [返回] 成功时返回 true。
// 最近修改时间：2026-11-14；改动原因：新增，业务成败判定。
func (r *tokenBeatResponse) isSuccess() bool {
	if r == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Message), "success") && r.Data.ErrorCode == 0
}

// describe 把失败响应转成可展示的错误说明。
// [参数] r: 解析后的响应。
// [返回] 错误说明；成功响应也返回可读文本（供诊断日志）。
// 最近修改时间：2026-11-14；改动原因：新增，错误文案统一。
func (r *tokenBeatResponse) describe() string {
	if r == nil {
		return "上游无响应"
	}
	if desc := strings.TrimSpace(r.Data.Description); desc != "" {
		return desc
	}
	if name := strings.TrimSpace(r.Data.ErrorName); name != "" {
		return name
	}
	return fmt.Sprintf("上游返回 message=%q error_code=%d", r.Message, r.Data.ErrorCode)
}

// tokenExchange 是一次令牌兑换的结果。
type tokenExchange struct {
	// CookieHeader 是拼装好的 Cookie 请求头，可直接交给 saveCredentials。
	CookieHeader string
	// Cookies 是带属性的 Cookie 快照，随账号一起落盘。
	Cookies []savedCookie
	// SetCookieCount 是本次响应下发的 Set-Cookie 条数。
	SetCookieCount int
}

// doTokenBeat 用一次性令牌换取会话 Cookie。
//
// 令牌只能兑换一次且立即消费，插件内存里不保留。
// [参数] token: 浏览器同源脚本取到的一次性令牌。
// [返回] 响应体、Set-Cookie 列表与错误。
// 最近修改时间：2026-11-14；改动原因：新增，令牌端点唯一调用点。
func doTokenBeat(token string) (*tokenBeatResponse, []*http.Cookie, error) {
	req, errReq := http.NewRequest(http.MethodPost, tokenBeatURL(), bytes.NewReader([]byte("{}")))
	if errReq != nil {
		return nil, nil, fmt.Errorf("构造令牌请求失败：%w", errReq)
	}
	req.Header.Set("Content-Type", tokenBeatContentType)
	req.Header.Set("sdk-version", tokenBeatSDKVersion)
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Referer", doubaoSiteHost+"/")
	req.Header.Set("x-tt-token", strings.TrimSpace(token))

	timeout := time.Duration(currentConfig().TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, nil, fmt.Errorf("令牌请求失败：%w", errDo)
	}
	defer resp.Body.Close()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, nil, fmt.Errorf("读取令牌响应失败：%w", errRead)
	}

	var parsed tokenBeatResponse
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return nil, nil, fmt.Errorf("令牌响应解析失败（HTTP %d）：%w", resp.StatusCode, errUnmarshal)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &parsed, nil, fmt.Errorf("令牌端点返回 HTTP %d：%s", resp.StatusCode, parsed.describe())
	}
	return &parsed, resp.Cookies(), nil
}

// exchangeTokenForCookies 用一次性令牌换回会话 Cookie。
//
// 契约（与扣子侧同一网关）：HTTP 200 / message=success / error_code=0 /
// 若干条 Set-Cookie（含 sessionid 等会话主 Cookie）。
// [参数] token: 浏览器同源脚本取到的一次性令牌。
// [返回] 兑换结果与错误。
// 最近修改时间：2026-11-14；改动原因：新增，首次授权换 Cookie。
func exchangeTokenForCookies(token string) (*tokenExchange, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("令牌为空")
	}

	parsed, setCookies, errDo := doTokenBeat(token)
	if errDo != nil {
		return nil, errDo
	}
	if !parsed.isSuccess() {
		return nil, fmt.Errorf("令牌兑换失败：%s", parsed.describe())
	}
	if len(setCookies) == 0 {
		return nil, fmt.Errorf("令牌兑换失败：上游未下发任何 Cookie")
	}

	jar := newDurableJar()
	jar.SetCookies(mustParseDoubaoURL(doubaoSiteHost+"/"), setCookies)
	return &tokenExchange{
		CookieHeader:   cookieHeaderFromJar(jar),
		Cookies:        jar.snapshot(),
		SetCookieCount: len(setCookies),
	}, nil
}

// cookieHeaderFromJar 把 Jar 里的 Cookie 快照拼成 Cookie 请求头。
//
// 标准库 cookiejar 没有「导出整个 Jar」的能力，快照才是唯一可用的来源；
// 同名 Cookie 以最后一条为准，与浏览器行为一致。
// [参数] jar: 已灌入 Cookie 的 Jar。
// [返回] 形如 a=1; b=2 的 Cookie 头。
// 最近修改时间：2026-11-14；改动原因：新增，快照转请求头。
func cookieHeaderFromJar(jar *durableJar) string {
	if jar == nil {
		return ""
	}
	var order []string
	values := make(map[string]string)
	for _, rec := range jar.snapshot() {
		name := strings.TrimSpace(rec.Cookie.Name)
		if name == "" {
			continue
		}
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = rec.Cookie.Value
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}

// mustParseDoubaoURL 解析 URL，失败时返回豆包主域的最小安全值。
// [参数] raw: 原始 URL。
// [返回] 解析后的 URL。
// 最近修改时间：2026-11-14；改动原因：新增，内部常量 URL 的解析兜底。
func mustParseDoubaoURL(raw string) *url.URL {
	u, errParse := url.Parse(raw)
	if errParse != nil || u.Host == "" {
		return &url.URL{Scheme: "https", Host: "www.doubao.com", Path: "/"}
	}
	return u
}

// saveFromToken 用一次性令牌完成一次完整的账号落盘。
//
// 链路：令牌 → token beat 换 Cookie → 判据端点校验 → 落盘。复用 saveCredentials
// 而不是另写落盘逻辑：设备标识固化（device_id / web_id / web_tab_id / bot_id）
// 与文件命名口径必须与手动导入完全一致，否则同一账号会出现两份凭据。
//
// 个别换发路径可能不下发 flow_cur_user_sec_id（它是 IM 端点 key 的字段，
// 网页登录态常态存在）。此时用判据端点返回的 sec_user_id 补齐——两者都是
// 同一账号的稳定句柄，缺字段会让后续工作模式请求被上游按未登录拒绝。
// [参数] token: 一次性令牌；label: 备注名（可空）。
// [返回] 落盘后的账号结构与错误。
// 最近修改时间：2026-11-14；改动原因：新增，首次授权主链路。
func saveFromToken(token, label string) (*StoredAuth, error) {
	exchanged, errExchange := exchangeTokenForCookies(token)
	if errExchange != nil {
		return nil, errExchange
	}
	header := strings.TrimSpace(exchanged.CookieHeader)
	if cookieValue(header, "flow_cur_user_sec_id") == "" {
		probe, errProbe := probeAccountInfo(&credentials{CookieHeader: header}, 0)
		if errProbe != nil {
			return nil, errProbe
		}
		if header != "" && !strings.HasSuffix(header, ";") {
			header += ";"
		}
		if header != "" {
			header += " "
		}
		header += "flow_cur_user_sec_id=" + strings.TrimSpace(probe.SecUserID)
	}

	timeout := time.Duration(currentConfig().TimeoutSeconds) * time.Second
	return saveCredentials(header, label, timeout)
}
