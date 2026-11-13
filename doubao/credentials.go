package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// 本文件实现豆包登录态凭据的解析与有效性校验。
//
// 认证路线（需求 v1.5 裁定）：复用用户浏览器登录态——用户在自有浏览器登录豆包后，
// 从浏览器导出会话 Cookie 并导入本插件；插件不实现登录流程（不扫码、不手机号、
// 不内嵌浏览器）。
//
// 关键事实（2026-10-17 实测）：
//   - 豆包关键会话 Cookie（sessionid / sid_guard 等）为 HttpOnly，页面 JS 不可读，
//     故必须由用户从浏览器侧导出；
//   - flow_cur_user_sec_id 非 HttpOnly，是 IM 端点真正 key 的字段，与 sessionid 一并
//     作为必需字段校验。

const (
	// doubaoAuthFilePrefix 是豆包凭据文件的命名前缀。
	doubaoAuthFilePrefix = "doubao-"

	// accountInfoPath 是登录态有效性判据端点。
	//
	// 为何不用 /alice/basic/launch：实测该端点对未登录请求同样返回 code:0，
	// 且伪造 Cookie（sessionid=fake）仍返回 code:0 + 67 条 model_list，
	// 响应中根本没有 sec_user_id 字段——三个要素全部无区分度。
	// account/info/v2 的区分度已实测：有会话 message="success" + sec_user_id 非空；
	// 无会话 message="error" 且无该字段。
	accountInfoPath = "/passport/account/info/v2/"

	// accountInfoHost 承载判据端点的域名。
	accountInfoHost = "https://accounts.doubao.com"

	// doubaoSiteHost 是业务 API 域名。
	doubaoSiteHost = "https://www.doubao.com"
)

// credentials 代表一份豆包登录态凭据。
type credentials struct {
	// CookieHeader 是完整的浏览器 Cookie header 原文（导入时用户提供）。
	CookieHeader string `json:"cookie_header"`
	// FlowSecUserID 是 flow_cur_user_sec_id 的值，IM 端点按此字段识别账号。
	FlowSecUserID string `json:"flow_cur_user_sec_id"`
	// SecUserID 是判据端点返回的账号句柄，作为落盘文件名的稳定来源。
	SecUserID string `json:"sec_user_id,omitempty"`
	// UserID 是判据端点返回的数字用户 ID。
	UserID int64 `json:"user_id,omitempty"`
	// ScreenName 是判据端点返回的昵称。
	ScreenName string `json:"screen_name,omitempty"`
	// Mobile 是判据端点返回的脱敏手机号（仅用于面板展示，不落库到日志）。
	Mobile string `json:"mobile,omitempty"`
}

// cookieValue 从 Cookie header 中提取指定名称的值。
// [参数] header: 完整 Cookie header；name: Cookie 名。
// [返回] Cookie 值；不存在时返回空串。
// 最近修改时间：2026-10-17；改动原因：新增，凭据字段提取入口。
func cookieValue(header, name string) string {
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.Index(part, "=")
		if eq <= 0 {
			continue
		}
		if strings.TrimSpace(part[:eq]) == name {
			return strings.TrimSpace(part[eq+1:])
		}
	}
	return ""
}

// hasAnyCookie 判断 Cookie header 中是否存在任一指定名称。
// [参数] header: 完整 Cookie header；names: 候选 Cookie 名。
// [返回] 存在任一时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，会话标识多形态兼容判定。
func hasAnyCookie(header string, names ...string) bool {
	for _, n := range names {
		if cookieValue(header, n) != "" {
			return true
		}
	}
	return false
}

// validate 校验凭据的必需字段是否齐备。
//
// 必需字段与理由（对齐 Lxapk 的 credentials.go:validate 契约，并经实测确认）：
//   - flow_cur_user_sec_id：IM 端点真正 key 的字段；
//   - sessionid / sessionid_ss / sid_tt 任一：会话标识本体。
//
// 本函数只做字段齐备性检查（挡住「语法像 Cookie 的垃圾值」），不发起网络请求；
// 登录态是否真实有效由 probeAccountInfo 判定。
// [参数] c: 待校验凭据。
// [返回] 校验错误；通过时返回 nil。
// 最近修改时间：2026-10-17；改动原因：新增，凭据必需字段闸门。
func (c *credentials) validate() error {
	if c == nil {
		return fmt.Errorf("凭据为空")
	}
	if strings.TrimSpace(c.CookieHeader) == "" {
		return fmt.Errorf("Cookie 不能为空，请粘贴浏览器中复制出的完整 Cookie")
	}
	if cookieValue(c.CookieHeader, "flow_cur_user_sec_id") == "" {
		return fmt.Errorf("Cookie 缺少 flow_cur_user_sec_id，请确认复制的是登录后的完整 Cookie")
	}
	if !hasAnyCookie(c.CookieHeader, "sessionid", "sessionid_ss", "sid_tt") {
		return fmt.Errorf("Cookie 缺少 sessionid，请确认复制的是登录后的完整 Cookie")
	}
	return nil
}

// parseCredentials 解析用户粘贴的 Cookie header 为凭据结构。
// [参数] header: 完整 Cookie header 原文。
// [返回] 凭据结构与解析错误。
// 最近修改时间：2026-10-17；改动原因：新增，导入入口的解析层。
func parseCredentials(header string) (*credentials, error) {
	header = strings.TrimSpace(header)
	// 用户可能连同 "Cookie: " 前缀一起复制，此处做容错剥离。
	if strings.HasPrefix(strings.ToLower(header), "cookie:") {
		header = strings.TrimSpace(header[len("cookie:"):])
	}
	c := &credentials{
		CookieHeader:  header,
		FlowSecUserID: cookieValue(header, "flow_cur_user_sec_id"),
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// authIDFor 根据凭据生成稳定的账号标识。
//
// 以 sec_user_id 优先、flow_cur_user_sec_id 兜底：前者是判据端点返回的账号句柄，
// 后者是 Cookie 中已有字段，两者都稳定；用哈希避免把凭据原值写进文件名。
// [参数] c: 凭据结构。
// [返回] 形如 doubao-<hash8> 的账号标识。
// 最近修改时间：2026-10-17；改动原因：新增，账号标识派生。
func authIDFor(c *credentials) string {
	key := strings.TrimSpace(c.SecUserID)
	if key == "" {
		key = strings.TrimSpace(c.FlowSecUserID)
	}
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return doubaoAuthFilePrefix + hex.EncodeToString(digest[:8])
}

// authFileNameFor 根据凭据生成稳定的文件名。
// [参数] c: 凭据结构。
// [返回] 形如 doubao-<hash8>.json 的文件名。
// 最近修改时间：2026-10-17；改动原因：新增，凭据文件名派生。
func authFileNameFor(c *credentials) string {
	id := authIDFor(c)
	if id == "" {
		return ""
	}
	return id + ".json"
}

// accountInfoResponse 是判据端点 /passport/account/info/v2/ 的响应。
type accountInfoResponse struct {
	Data    map[string]any `json:"data"`
	Message string         `json:"message"`
}

// accountInfoEndpointOverride 是判据端点的测试覆盖点；非空时优先使用。
// 生产路径下始终为空，端点由 accountInfoHost + accountInfoPath 常量拼装。
var accountInfoEndpointOverride string

// swapAccountInfoEndpoint 临时覆盖判据端点，返回恢复函数（仅测试使用）。
// [参数] endpoint: 替换后的完整判据端点 URL；空串表示恢复默认。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-10-17；改动原因：新增，让判据链路可在 httptest 服务端下验证。
func swapAccountInfoEndpoint(endpoint string) func() {
	prev := accountInfoEndpointOverride
	accountInfoEndpointOverride = endpoint
	return func() { accountInfoEndpointOverride = prev }
}

// probeAccountInfo 调用判据端点确认登录态是否有效。
//
// 判据（2026-10-17 实测确认区分度）：message == "success" 且 data.sec_user_id 非空。
// 无会话时端点返回 message == "error" 且 data 中无 sec_user_id。
// [参数] c: 待探测凭据；timeout: 请求超时。
// [返回] 更新后的凭据（含 sec_user_id / user_id / screen_name / mobile）与错误。
// 最近修改时间：2026-10-17；改动原因：新增，登录态有效性判据入口。
func probeAccountInfo(c *credentials, timeout time.Duration) (*credentials, error) {
	endpoint := accountInfoEndpointOverride
	if endpoint == "" {
		// 实测缺参会返回空 data，必须补齐 device_platform / aid / region 等常量块。
		endpoint = accountInfoHost + accountInfoPath + "?" + buildAccountInfoQuery()
	}
	return probeAccountInfoAgainst(c, endpoint, timeout)
}

// probeAccountInfoAgainst 对指定端点执行判据探测。
//
// 与 probeAccountInfo 分离是为了让测试可指向 httptest 服务端，避免真实网络依赖。
// [参数] c: 待探测凭据；endpoint: 完整判据端点 URL；timeout: 请求超时。
// [返回] 更新后的凭据与错误。
// 最近修改时间：2026-10-17；改动原因：新增，判据核心逻辑与端点解析解耦。
func probeAccountInfoAgainst(c *credentials, endpoint string, timeout time.Duration) (*credentials, error) {
	if c == nil {
		return nil, fmt.Errorf("凭据为空")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	req, errReq := http.NewRequest(http.MethodGet, endpoint, nil)
	if errReq != nil {
		return nil, fmt.Errorf("构造判据请求失败：%w", errReq)
	}
	req.Header.Set("Cookie", c.CookieHeader)
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Referer", doubaoSiteHost+"/")

	client := &http.Client{Timeout: timeout}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("判据请求失败：%w", errDo)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("判据端点返回 HTTP %d", resp.StatusCode)
	}

	var out accountInfoResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&out); errDecode != nil {
		return nil, fmt.Errorf("判据响应解析失败：%w", errDecode)
	}

	// 1. 判据第一重：message 必须为 success。
	if !strings.EqualFold(strings.TrimSpace(out.Message), "success") {
		return nil, fmt.Errorf("登录态无效：判据端点返回 message=%q，请确认浏览器已登录豆包且 Cookie 未过期", out.Message)
	}

	// 2. 判据第二重：data.sec_user_id 必须非空。
	secUserID, _ := out.Data["sec_user_id"].(string)
	if strings.TrimSpace(secUserID) == "" {
		return nil, fmt.Errorf("登录态无效：判据端点未返回 sec_user_id")
	}

	// 3. 回填身份字段，供落盘与面板展示。
	c.SecUserID = strings.TrimSpace(secUserID)
	if v, ok := out.Data["user_id"].(float64); ok {
		c.UserID = int64(v)
	}
	if v, ok := out.Data["screen_name"].(string); ok {
		c.ScreenName = strings.TrimSpace(v)
	}
	if v, ok := out.Data["mobile"].(string); ok {
		c.Mobile = strings.TrimSpace(v)
	}
	return c, nil
}
