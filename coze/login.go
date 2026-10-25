package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件实现扣子授权登录的状态机与桥接页。
//
// 为什么不能一键授权（2026-10-17 源码取证）：
//   - /sign/oauth 的 redirect_uri 硬编码为 www.coze.cn/auth/callback；
//   - /desktop-login 的 redirect_uri 协议白名单只认 coze-desktop: 系列；
//   - /desktop-login 的 redirect 参数不承载登录令牌。
// 因此插件无法作为 OAuth 回调接收方。可行路径是：用户在扣子网页完成官方
// 登录后，执行插件提供的一次性同源脚本取 x-tt-token，再交给插件回调页。

// loginStateTTL 是一次授权会话的有效期。
//
// 与桌面端等待 deep link 的 5 分钟窗口对齐：桥接页只承载一次脚本执行，
// 忘了关的标签页不该在很久之后还能把凭据塞进来。
const loginStateTTL = 15 * time.Minute

// loginSession 是一次授权会话。
type loginSession struct {
	// startedAt 是会话创建时间，用于判定过期。
	startedAt time.Time
	// importedAuthID 是脚本回调成功落盘后的账号标识。
	//
	// 这是唯一的成功判据：不能用「账号集合变化」或 UpdatedAt 推断——
	// 续期巡检也会刷新 UpdatedAt，会把「用户还没执行脚本」的会话误判成完成。
	importedAuthID string
	// lastError 记录最近一次失败的说明，供面板与轮询反馈。
	lastError string
}

var (
	loginSessionsMu sync.Mutex
	loginSessions   = make(map[string]*loginSession)
)

// randomState 生成一次性会话标识。
// [参数] 无。
// [返回] 32 字符十六进制串；熵源失败时回退时间戳。
// 最近修改时间：2026-10-18；改动原因：新增，state 生成。
func randomState() string {
	buf := make([]byte, 16)
	if _, errRead := rand.Read(buf); errRead != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// loginBridgeURL 构造桥接页的相对 URL。
//
// 必须是相对路径：宿主 UI 从管理域 origin 打开它，相对路径在本地与远程
// 部署下都能解析到正确的服务地址（与 copilot / doubao 同约定）。
// [参数] state: 授权会话标识。
// [返回] 桥接页相对 URL。
// 最近修改时间：2026-10-18；改动原因：新增，宿主 OAuth 弹窗落地页。
func loginBridgeURL(state string) string {
	return "/v0/resource/plugins/" + providerName + "/login?state=" + url.QueryEscape(state)
}

// callbackPath 是脚本回传令牌的 resource 路径。
const callbackPath = "/login/callback"

// handleStartLogin 响应宿主 auth.login.start。
//
// 不做任何上游请求：登记会话并返回桥接页地址。宿主弹窗打开的就是本页，
// 用户在页内按引导完成登录并执行脚本，宿主通过 auth.login.poll 拿到结果。
// [参数] raw: 宿主登录起点请求载荷。
// [返回] 登录起点响应信封与错误。
// 最近修改时间：2026-10-18；改动原因：新增，授权起点。
func handleStartLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginStartRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	state := randomState()

	loginSessionsMu.Lock()
	pruneExpiredLoginSessionsLocked()
	loginSessions[state] = &loginSession{startedAt: time.Now()}
	loginSessionsMu.Unlock()

	bridgeURL := loginBridgeURL(state)
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       bridgeURL,
		State:     state,
		ExpiresAt: time.Now().Add(loginStateTTL).UTC(),
		Metadata: map[string]any{
			"logo":       pluginLogoURL,
			"bridge_url": bridgeURL,
			"prompt":     "在打开的引导页中按步骤完成扣子登录并执行一次性脚本，成功后此窗口会自动完成。",
		},
	})
}

// handlePollLogin 响应宿主 auth.login.poll。
//
// 成功判据是脚本回调显式登记了导入结果（见 markLoginImported）。
// [参数] raw: 宿主登录轮询请求载荷。
// [返回] 轮询结果信封与错误。
// 最近修改时间：2026-10-18；改动原因：新增，桥接页结果回传。
func handlePollLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("解析轮询请求失败：%w", errUnmarshal)
		}
	}
	state := strings.TrimSpace(req.State)
	if state == "" {
		return nil, fmt.Errorf("缺少登录会话标识")
	}

	loginSessionsMu.Lock()
	session, ok := loginSessions[state]
	if ok && time.Since(session.startedAt) > loginStateTTL {
		delete(loginSessions, state)
		ok = false
	}
	var importedAuthID, lastError string
	if ok {
		importedAuthID = session.importedAuthID
		lastError = session.lastError
	}
	loginSessionsMu.Unlock()

	if !ok {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "授权会话不存在或已过期，请回到管理页重新发起",
		})
	}
	if importedAuthID == "" {
		msg := "等待在引导页完成登录并执行一次性脚本"
		if lastError != "" {
			msg = "上次尝试失败：" + lastError
		}
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: msg,
		})
	}

	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return nil, fmt.Errorf("读取账号失败：%w", errList)
	}
	sa := findAccountByID(accounts, importedAuthID)
	if sa == nil {
		// 落盘成功后账号文件被删除：报错而不是继续 pending，避免宿主空等。
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "账号已被删除，请重新授权",
		})
	}

	// 结果只消费一次：成功后立即删除会话，避免同一 state 被重复领取。
	loginSessionsMu.Lock()
	delete(loginSessions, state)
	loginSessionsMu.Unlock()
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: "扣子账号授权成功",
		Auth:    authDataForLogin(sa),
	})
}

// loginImportedAuthID 返回该会话已成功导入的账号标识。
// [参数] state: 授权会话标识。
// [返回] 账号标识；未导入或会话无效时返回空串。
// 最近修改时间：2026-10-18；改动原因：新增，回调页重放幂等判定。
func loginImportedAuthID(state string) string {
	state = strings.TrimSpace(state)
	if state == "" {
		return ""
	}
	loginSessionsMu.Lock()
	defer loginSessionsMu.Unlock()
	session, ok := loginSessions[state]
	if !ok || session == nil {
		return ""
	}
	if time.Since(session.startedAt) > loginStateTTL {
		return ""
	}
	return session.importedAuthID
}

// markLoginImported 记录脚本回调已成功落盘的账号，供宿主轮询领取。
// [参数] state: 授权会话标识；authID: 账号标识。
// [返回] 会话不存在或已过期时返回 false。
// 最近修改时间：2026-10-18；改动原因：新增，显式成功信号。
func markLoginImported(state, authID string) bool {
	state = strings.TrimSpace(state)
	authID = strings.TrimSpace(authID)
	if state == "" || authID == "" {
		return false
	}
	loginSessionsMu.Lock()
	defer loginSessionsMu.Unlock()
	session, ok := loginSessions[state]
	if !ok || time.Since(session.startedAt) > loginStateTTL {
		delete(loginSessions, state)
		return false
	}
	session.importedAuthID = authID
	session.lastError = ""
	return true
}

// markLoginFailed 记录一次失败说明，供轮询回显。
// [参数] state: 授权会话标识；reason: 失败原因。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，失败原因可见化。
func markLoginFailed(state, reason string) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	loginSessionsMu.Lock()
	defer loginSessionsMu.Unlock()
	if session, ok := loginSessions[state]; ok {
		session.lastError = strings.TrimSpace(reason)
	}
}

// validLoginState 判断授权会话是否仍然有效。
// [参数] state: 授权会话标识。
// [返回] 有效时返回 true。
// 最近修改时间：2026-10-18；改动原因：新增，桥接页与回调的共用校验。
func validLoginState(state string) bool {
	state = strings.TrimSpace(state)
	if state == "" {
		return false
	}
	loginSessionsMu.Lock()
	defer loginSessionsMu.Unlock()
	session, ok := loginSessions[state]
	if !ok {
		return false
	}
	if time.Since(session.startedAt) > loginStateTTL {
		delete(loginSessions, state)
		return false
	}
	return true
}

// findAccountByID 按账号标识查找账号。
// [参数] accounts: 账号列表；authID: 账号标识。
// [返回] 命中的账号；没有时返回 nil。
// 最近修改时间：2026-10-18；改动原因：新增，轮询结果定位。
func findAccountByID(accounts []*StoredAuth, authID string) *StoredAuth {
	for _, sa := range accounts {
		if sa != nil && sa.AuthID == authID {
			return sa
		}
	}
	return nil
}

// pruneExpiredLoginSessionsLocked 清理过期会话，避免 map 无限增长。
// 调用方必须已持有 loginSessionsMu。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，会话回收。
func pruneExpiredLoginSessionsLocked() {
	for state, session := range loginSessions {
		if session == nil || time.Since(session.startedAt) > loginStateTTL {
			delete(loginSessions, state)
		}
	}
}

// authDataForLogin 把账号转成宿主可直接落盘的 AuthData。
//
// ID 必须与宿主文件扫描派生的 ID 同形（相对 auth 目录的文件名，含 .json）：
// 宿主有两条注册路径，不一致会让同一个物理文件被注册成两条账号记录。
//
// metadata 里必须带 refresh_interval_seconds：宿主自动刷新循环的
// nextRefreshCheckAt 在 NextRefreshAfter 为零时会依次回退到
// refresh_interval_seconds 与 ProviderRefreshLead，而扣子不在宿主的
// refreshLeadFactories 注册表里（只内置 codex/claude/antigravity/kimi/xai）。
// 缺这个键时账号永远排不进刷新队列，续期只能靠用户手动点按钮
// （2026-10-18 生产实测：24 小时内 0 条 auto-refresh 日志）。
// [参数] sa: 账号。
// [返回] AuthData。
// 最近修改时间：2026-10-25；改动原因：补顶层 Disabled，宿主只认它判账号状态。
func authDataForLogin(sa *StoredAuth) pluginapi.AuthData {
	storage, _ := json.Marshal(sa)
	fileName := authFileNameOf(sa.AuthID)
	return pluginapi.AuthData{
		Provider: providerName,
		ID:       fileName,
		FileName: fileName,
		Label:    sa.Label,
		// 宿主的 pluginAuthDataToCoreAuth 只用顶层 Disabled 推导 Status，
		// Metadata 里的同名键在该路径上不参与判定。漏设会让「插件写盘禁用」
		// 在宿主侧仍算 active：认证文件页按启用统计、按停用筛选查不到，
		// 宿主调度器也继续把它当作候选（2026-10-25 生产实证）。
		Disabled:    sa.Disabled,
		StorageJSON: storage,
		Metadata: map[string]any{
			"type":                     providerName,
			"label":                    sa.Label,
			"screen_name":              sa.ScreenName,
			"mobile":                   sa.Mobile,
			"refresh_interval_seconds": currentConfig().RenewIntervalSeconds,
			// disabled 与三个低额禁用归属标记必须随 Metadata 回传：宿主在每次
			// 请求结束后都用这份 Metadata 重建物理 auth 文件（白名单语义，未列出
			// 的顶层键一律丢弃）。不回传就等于「插件写禁 → 宿主 persist → 盘上
			// 只剩这 5 个键」，禁用与归属一起消失，巡检再也认不出这个号。
			"disabled":                 sa.Disabled,
			"low_credit_disable":       sa.LowCreditDisable,
			"low_credit_disable_at":    sa.LowCreditDisableAt,
			"low_credit_disable_below": sa.LowCreditDisableBelow,
		},
	}
}

// authFileNameOf 把账号标识规范成宿主 auth 目录下的文件名。
// [参数] authID: 账号标识（可带或不带 .json 后缀）。
// [返回] 形如 coze-<hash>.json 的文件名。
// 最近修改时间：2026-10-18；改动原因：新增，宿主注册 ID 与文件名统一口径。
func authFileNameOf(authID string) string {
	authID = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(authID), ".json"))
	return authID + ".json"
}

// handleParseAuth 响应宿主 auth.parse。
//
// 宿主扫描 auth 目录时用它把 coze-*.json 认领成本插件的账号。
// [参数] raw: 宿主解析请求载荷。
// [返回] 解析响应信封与错误。
// 最近修改时间：2026-10-18；改动原因：新增，账号文件认领。
func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	fileName := strings.TrimSpace(req.FileName)
	if !isCozeAuthFileName(fileName) {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}

	var sa StoredAuth
	if errUnmarshal := json.Unmarshal(req.RawJSON, &sa); errUnmarshal != nil || strings.TrimSpace(sa.CookieHeader) == "" {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if sa.AuthID == "" {
		sa.AuthID = strings.TrimSuffix(fileName, ".json")
	}
	auth := authDataForLogin(&sa)
	auth.ID = fileName
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: auth})
}

// handleRefreshAuth 响应宿主 auth.refresh。
//
// 扣子可以自助续期（Cookie → 新 token → 新 Cookie），因此这里真正执行一轮：
// 续期成功返回新凭据，失败则原样返回旧凭据并把下次刷新推后，避免宿主
// 把账号直接标成刷新失败——续期失败多半是临时网络问题，不该立刻下线账号。
// [参数] raw: 宿主刷新请求载荷。
// [返回] 刷新响应信封与错误。
// 最近修改时间：2026-10-18；改动原因：新增，接入真实续期。
func handleRefreshAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	var sa StoredAuth
	if len(req.StorageJSON) > 0 {
		_ = json.Unmarshal(req.StorageJSON, &sa)
	}
	if strings.TrimSpace(sa.CookieHeader) == "" {
		return nil, fmt.Errorf("账号凭据为空，无法刷新")
	}
	if sa.AuthID == "" {
		sa.AuthID = strings.TrimSuffix(strings.TrimSpace(req.AuthID), ".json")
	}

	res, errRenew := renewSession(&sa)
	if errRenew == nil && res != nil && res.Renewed {
		sa.CookieHeader = res.CookieHeader
		sa.Cookies = res.Cookies
		sa.LastRenewAt = time.Now().Format(time.RFC3339)
		sa.UpdatedAt = sa.LastRenewAt
	}
	// 落盘由宿主完成：StorageJSON 随响应回传后，宿主负责写回 auth 文件；
	// 这里的 sa 来自请求载荷，PhysicalPath 为空（json:"-"），不能自行写盘。

	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth:             authDataForLogin(&sa),
		NextRefreshAfter: time.Now().Add(time.Duration(currentConfig().RenewIntervalSeconds) * time.Second).UTC(),
	})
}

// htmlManagementResponse 把 HTML 文本包装成管理响应。
// [参数] page: 完整 HTML。
// [返回] 管理响应。
// 最近修改时间：2026-10-18；改动原因：新增，HTML 响应构造。
func htmlManagementResponse(page string) pluginapi.ManagementResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: []byte(page)}
}

// jsonManagementResponse 把结果编码成 JSON 管理响应。
// [参数] status: HTTP 状态码；payload: 结果对象。
// [返回] 管理响应。
// 最近修改时间：2026-10-18；改动原因：新增，桥接页提交结果回写。
func jsonManagementResponse(status int, payload map[string]any) pluginapi.ManagementResponse {
	body, _ := json.Marshal(payload)
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	return pluginapi.ManagementResponse{StatusCode: status, Headers: h, Body: body}
}

// htmlEscape 转义 HTML 关键字符。
// [参数] s: 原始字符串。
// [返回] 转义后的字符串。
// 最近修改时间：2026-10-18；改动原因：新增，错误说明渲染。
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;")
	return r.Replace(s)
}
