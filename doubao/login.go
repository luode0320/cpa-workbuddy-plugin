package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件实现宿主的 OAuth 登录入口。
//
// 豆包没有可供第三方接入的 OAuth 授权码流程：登录态是浏览器侧的 HttpOnly
// Cookie（sessionid 等），只能由用户从浏览器导出。因此这里不实现登录，
// 而是把宿主「+ 添加账号」打开的弹窗变成一座桥：引导用户去浏览器登录 →
// 复制 Cookie → 粘回桥接页 → 插件校验并落盘 → 宿主轮询拿到结果。
//
// 之所以必须实现这组方法：插件声明了 auth_provider，宿主会把「+」指向
// /v0/management/<provider>-auth-url，再回调插件的 auth.login.start。
// 未实现时宿主统一报 failed to generate authorization url——这正是修复前
// 用户看到的错误。

// loginStateTTL 是一次登录桥接会话的有效期。
//
// 与宿主侧 OAuth session 的 30 分钟对齐但更短：桥接页只承载一次粘贴动作，
// 忘了关的标签页不该在半小时后还能把凭据塞进来。
const loginStateTTL = 15 * time.Minute

// loginSession 是一次桥接会话。
type loginSession struct {
	// startedAt 是会话创建时间，用于判定会话是否过期。
	startedAt time.Time
	// importedAuthID 是桥接页显式导入成功后登记的账号标识。
	//
	// 这是唯一的成功判据：不能靠「账号集合变化」或「UpdatedAt 变新」推断——
	// 插件在正常请求后会把滚动 Cookie 写回账号文件，那也会刷新 UpdatedAt，
	// 会让「用户还没粘贴」的会话被误判成登录完成。
	importedAuthID string
	// lastError 是最近一次授权尝试的失败原因。
	//
	// 宿主弹窗只认 pending / success / error 三态，失败时若只报 pending，
	// 用户会一直盯着「等待授权」看不到真实原因，因此把上游说明带出来。
	lastError string
}

var (
	loginSessionsMu sync.Mutex
	loginSessions   = make(map[string]*loginSession)
)

// loginBridgeURL 构造桥接页的相对 URL。
//
// 必须是相对路径：宿主 UI 从管理域 origin 用 window.open 打开它，相对路径
// 在本地与远程部署下都能解析到正确的服务地址（与 copilot / traework 同约定）。
// [参数] state: 登录会话标识。
// [返回] 桥接页相对 URL。
// 最近修改时间：2026-10-17；改动原因：新增，宿主 OAuth 弹窗落地页。
func loginBridgeURL(state string) string {
	return "/v0/resource/plugins/" + providerName + "/login?state=" + url.QueryEscape(state)
}

// handleStartLogin 响应宿主 auth.login.start。
//
// 不做任何上游请求：直接登记会话并返回桥接页地址。宿主弹窗打开的就是本页，
// 用户在页内完成 Cookie 粘贴后，宿主通过 auth.login.poll 拿到结果。
// [参数] raw: 宿主登录起点请求载荷。
// [返回] 登录起点响应信封与错误。
// 最近修改时间：2026-10-17；改动原因：新增，修复「failed to generate authorization url」。
func handleStartLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginStartRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	state := randomUUID()

	loginSessionsMu.Lock()
	pruneExpiredLoginSessionsLocked()
	loginSessions[state] = &loginSession{startedAt: time.Now()}
	loginSessionsMu.Unlock()

	bridgeURL := loginBridgeURL(state)
	// 宿主弹窗里显示的是这个 URL：只给相对路径时，有的宿主渲染成不可点击文本，
	// 用户就得自己去拼域名。观测到外部 origin 后拼成绝对地址（取不到时仍用相对路径）。
	absoluteURL := bridgeURL
	if origin := currentPublicOrigin(); origin != "" {
		absoluteURL = origin + bridgeURL
	}
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       absoluteURL,
		State:     state,
		ExpiresAt: time.Now().Add(loginStateTTL).UTC(),
		Metadata: map[string]any{
			"logo":       pluginLogoURL,
			"bridge_url": absoluteURL,
			"prompt":     "在打开的引导页中安装一次授权书签，之后在豆包页面点一下书签即可完成登录。",
		},
	})
}

// handlePollLogin 响应宿主 auth.login.poll。
//
// 成功判据是桥接页显式登记了导入结果（见 markLoginImported）。不能用账号集合
// 变化或 UpdatedAt 推断：插件在正常请求后会把滚动 Cookie 写回账号文件，那同样
// 会刷新 UpdatedAt，会让「用户还没粘贴」的会话被误判成登录完成。
// [参数] raw: 宿主登录轮询请求载荷。
// [返回] 轮询结果信封与错误。
// 最近修改时间：2026-10-17；改动原因：新增，桥接页结果回传。
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
			Message: "登录会话不存在或已过期，请回到管理页重新发起",
		})
	}
	if importedAuthID == "" {
		// 上一次尝试失败过就把原因带出来：宿主弹窗只展示这一行文案，
		// 只报「等待」会让用户以为书签没生效，实际是登录态过期或取令牌被拒。
		pendingMessage := "等待在豆包页面点击授权书签"
		if lastError != "" {
			pendingMessage = "上次尝试失败：" + lastError
		}
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: pendingMessage,
		})
	}

	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return nil, fmt.Errorf("读取账号失败：%w", errList)
	}
	sa := findAccountByID(accounts, importedAuthID)
	if sa == nil {
		// 导入成功后账号文件被删除：报错而不是继续 pending，避免宿主空等到超时。
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "账号已被删除，请重新导入",
		})
	}

	// 结果只消费一次：成功后立即删除会话，避免同一 state 被重复领取。
	loginSessionsMu.Lock()
	delete(loginSessions, state)
	loginSessionsMu.Unlock()
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: "豆包登录态导入成功",
		Auth:    authDataForLogin(sa),
	})
}

// markLoginImported 记录桥接页已成功导入的账号，供宿主轮询领取。
// [参数] state: 登录会话标识；authID: 导入成功的账号标识。
// [返回] 会话不存在或已过期时返回 false。
// 最近修改时间：2026-10-17；改动原因：新增，显式成功信号（替代账号集合推断）。
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
	// 成功即清除上一次的失败说明，否则下次轮询仍会把旧错误报给宿主。
	session.lastError = ""
	return true
}

// markLoginFailed 记录一次授权失败的说明，供宿主轮询展示。
//
// 失败不改会话的导入状态：用户可以在 15 分钟内换一个浏览器标签页重试，
// 一次失败就作废会话会让「先误点、再重新登录」的常见路径反复重开弹窗。
// [参数] state: 登录会话标识；reason: 失败原因。
// [返回] 会话不存在或已过期时返回 false。
// 最近修改时间：2026-11-14；改动原因：新增，把上游失败原因回传给宿主弹窗。
func markLoginFailed(state, reason string) bool {
	state = strings.TrimSpace(state)
	if state == "" {
		return false
	}
	loginSessionsMu.Lock()
	defer loginSessionsMu.Unlock()
	session, ok := loginSessions[state]
	if !ok || time.Since(session.startedAt) > loginStateTTL {
		delete(loginSessions, state)
		return false
	}
	session.lastError = strings.TrimSpace(reason)
	return true
}

// validLoginState 判断会话是否存在且未过期。
// [参数] state: 登录会话标识。
// [返回] 存在且未过期时返回 true。
// 最近修改时间：2026-11-14；改动原因：新增，回调入口的会话校验。
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

// loginImportedAuthID 返回会话已登记的账号标识（未登记时为空串）。
// [参数] state: 登录会话标识。
// [返回] 账号标识。
// 最近修改时间：2026-11-14；改动原因：新增，回调重放保护。
func loginImportedAuthID(state string) string {
	state = strings.TrimSpace(state)
	if state == "" {
		return ""
	}
	loginSessionsMu.Lock()
	defer loginSessionsMu.Unlock()
	session, ok := loginSessions[state]
	if !ok || time.Since(session.startedAt) > loginStateTTL {
		return ""
	}
	return session.importedAuthID
}

// adoptPendingLoginSession 把一次无 state 的成功授权登记到最近的待完成会话。
//
// 固定书签不带 state（书签要能长期复用，不能烤进一次性标识），但用户很常见
// 的操作是：从宿主「添加账号」弹窗打开引导页，装上书签后在自己已有的豆包
// 标签页点书签。此时带 state 的会话正等着结果，认领它就能让弹窗自动关闭，
// 而不是让用户对着「等待授权」空等到 15 分钟超时。
// [参数] authID: 刚落盘的账号标识。
// [返回] 认领成功时返回 true。
// 最近修改时间：2026-11-14；改动原因：新增，固定书签结果回传宿主弹窗。
func adoptPendingLoginSession(authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	loginSessionsMu.Lock()
	defer loginSessionsMu.Unlock()
	var targetState string
	var targetStarted time.Time
	for state, session := range loginSessions {
		if session == nil {
			continue
		}
		if time.Since(session.startedAt) > loginStateTTL {
			delete(loginSessions, state)
			continue
		}
		if session.importedAuthID != "" {
			continue
		}
		if targetState == "" || session.startedAt.After(targetStarted) {
			targetState = state
			targetStarted = session.startedAt
		}
	}
	if targetState == "" {
		return false
	}
	loginSessions[targetState].importedAuthID = authID
	loginSessions[targetState].lastError = ""
	return true
}

// findAccountByID 按账号标识查找账号。
// [参数] accounts: 账号列表；authID: 账号标识。
// [返回] 命中的账号；没有时返回 nil。
// 最近修改时间：2026-10-17；改动原因：新增，轮询结果定位。
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
// 最近修改时间：2026-10-17；改动原因：新增，会话回收。
func pruneExpiredLoginSessionsLocked() {
	for state, session := range loginSessions {
		if session == nil || time.Since(session.startedAt) > loginStateTTL {
			delete(loginSessions, state)
		}
	}
}

// authDataForLogin 把账号转成宿主可直接落盘的 AuthData。
//
// StorageJSON 用账号文件的完整 JSON：宿主会原样写回 auth 目录，字段与插件
// 自己落盘的一致（含 device_id 等业务 query 需要的标识）。
//
// ID 必须与宿主文件扫描派生的 ID 同形（相对 auth 目录的文件名，含 .json）：
// 宿主有两条注册路径——host.auth.save 走 authIDForPath（含扩展名），
// 登录结果走 AuthData.ID（原样使用）。两者不一致时同一个物理文件会被注册成
// 两个 auth 记录，管理页与插件面板都会出现两条一模一样的账号。
// [参数] sa: 账号。
// [返回] AuthData。
// 最近修改时间：2026-10-17；改动原因：ID 改为带 .json 的文件名形态，消除重复注册。
func authDataForLogin(sa *StoredAuth) pluginapi.AuthData {
	storage, _ := json.Marshal(sa)
	fileName := authFileNameOf(sa.AuthID)
	return pluginapi.AuthData{
		Provider:    providerName,
		ID:          fileName,
		FileName:    fileName,
		Label:       sa.Label,
		StorageJSON: storage,
		Metadata: map[string]any{
			"type":        providerName,
			"label":       sa.Label,
			"screen_name": sa.ScreenName,
			"mobile":      sa.Mobile,
		},
	}
}

// authFileNameOf 把账号标识规范成宿主 auth 目录下的文件名。
// [参数] authID: 账号标识（可带或不带 .json 后缀）。
// [返回] 形如 doubao-<hash>.json 的文件名。
// 最近修改时间：2026-10-17；改动原因：新增，宿主注册 ID 与文件名统一口径。
func authFileNameOf(authID string) string {
	authID = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(authID), ".json"))
	return authID + ".json"
}

// handleParseAuth 响应宿主 auth.parse。
//
// 宿主扫描 auth 目录时用它把 doubao-*.json 认领成本插件的账号。只认文件名
// 前缀且内容含 cookie_header 的文件，其它一律交回宿主按普通文件处理。
// [参数] raw: 宿主解析请求载荷。
// [返回] 解析响应信封与错误。
// 最近修改时间：2026-10-17；改动原因：新增，账号文件认领（缺失会让宿主日志持续报错）。
func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	fileName := strings.TrimSpace(req.FileName)
	if !isDoubaoAuthFileName(fileName) {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}

	var sa StoredAuth
	if errUnmarshal := json.Unmarshal(req.RawJSON, &sa); errUnmarshal != nil || strings.TrimSpace(sa.CookieHeader) == "" {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if sa.AuthID == "" {
		sa.AuthID = strings.TrimSuffix(fileName, ".json")
	}
	// 宿主扫描文件时已按相对路径派生过 ID（含 .json）。这里回传同形 ID，
	// 否则同一次扫描会既登记路径 ID、又登记本函数返回的裸 ID。
	auth := authDataForLogin(&sa)
	auth.ID = fileName
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: auth})
}

// handleRefreshAuth 响应宿主 auth.refresh。
//
// 豆包的会话 Cookie 只能靠用户重新导出，插件无法自行续期：这里原样返回
// 现有凭据并把下次刷新推远，避免宿主把账号标成刷新失败。
// [参数] raw: 宿主刷新请求载荷。
// [返回] 刷新响应信封与错误。
// 最近修改时间：2026-10-17；改动原因：新增，刷新入口（无自助续期能力）。
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
		sa.AuthID = strings.TrimSpace(req.AuthID)
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth:             authDataForLogin(&sa),
		NextRefreshAfter: time.Now().Add(24 * time.Hour).UTC(),
	})
}

// handleLoginBridge 渲染授权引导页。
//
// 无 state 时是「面板独立入口」：页面给一条与 state 无关的固定书签，拖一次
// 即可长期复用，落盘后不通知宿主轮询（回面板刷新即可看到账号）。
// 有 state 时是宿主「添加账号」弹窗入口：除固定书签外，另给一条带 state 的
// 一次性脚本，保证弹窗能拿到结果并自动关闭。
//
// 页面本身走 resource 路由（免管理密钥）：它是浏览器顶层打开的普通页面，
// 拿不到、也不该要求用户填管理密钥（与 coze / copilot 桥接页同一做法）。
// [参数] state: 登录会话标识（可为空）；rawQuery: 原始查询串（兼容资源路由取参差异）。
// [返回] 管理响应（HTML）。
// 最近修改时间：2026-11-14；改动原因：改为书签一键授权，新增无 state 独立入口。
func handleLoginBridge(state, rawQuery string) pluginapi.ManagementResponse {
	state = strings.TrimSpace(state)
	if state == "" && rawQuery != "" {
		if parsed, errParse := url.ParseQuery(strings.TrimPrefix(rawQuery, "?")); errParse == nil {
			state = strings.TrimSpace(parsed.Get("state"))
		}
	}
	if state == "" {
		return loginBridgePage("", "")
	}
	if !validLoginState(state) {
		return loginBridgePage("", "授权会话不存在或已过期（15 分钟有效）。请回到管理页重新发起。")
	}
	return loginBridgePage(state, "")
}

// loginBridgeImportHeader 是桥接页提交 Cookie 时使用的请求头。
//
// resource 路由由宿主强制改写为 GET 且不带请求体，Cookie 只能走请求头；
// 用自定义头而不是 Cookie 头，避免与浏览器自身的 Cookie 语义混淆。
const loginBridgeImportHeader = "X-Doubao-Cookie"

// handleLoginBridgeImport 处理桥接页的登录态提交（resource 路由，免管理密钥）。
//
// 带 state 时：state 在 handleStartLogin 时随机生成、15 分钟过期，等于一次性的
// 短时凭证；导入成功后登记到该会话，宿主轮询领取并自动关闭弹窗。
// 不带 state 时：来自面板独立入口，没有宿主弹窗可回传，改为认领等待中的会话。
// [参数] req: 宿主透传的请求（Cookie 在 X-Doubao-Cookie 头，state 在 query）。
// [返回] JSON 结果信封。
// 最近修改时间：2026-11-14；改动原因：支持面板独立入口（无 state）的手工兜底导入。
func handleLoginBridgeImport(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	state := strings.TrimSpace(req.Query.Get("state"))
	strict := state != ""
	if strict {
		loginSessionsMu.Lock()
		session, ok := loginSessions[state]
		if ok && time.Since(session.startedAt) > loginStateTTL {
			delete(loginSessions, state)
			ok = false
		}
		loginSessionsMu.Unlock()
		if !ok || session == nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "登录会话不存在或已过期，请回到管理页重新发起"})
		}
	}

	cookie := strings.TrimSpace(req.Headers.Get(loginBridgeImportHeader))
	if cookie == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "cookie 不能为空"})
	}

	timeout := time.Duration(currentConfig().TimeoutSeconds) * time.Second
	sa, errSave := saveCredentials(cookie, "", timeout)
	if errSave != nil {
		return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
	}
	// 不带 state 说明来自面板独立入口：没有宿主弹窗能直接回传，改为认领一个
	// 等待中的会话，让用户还开着的那个弹窗也能自动关闭。
	if !strict {
		adopted := adoptPendingLoginSession(sa.AuthID)
		message := "登录态导入成功，账号已保存；回到面板点「刷新账号」即可看到"
		if adopted {
			message = "登录态导入成功，管理页会自动关闭并显示该账号"
		}
		return jsonManagementResponse(http.StatusOK, map[string]any{
			"ok": true, "auth_id": sa.AuthID, "label": sa.Label, "message": message,
		})
	}
	if !markLoginImported(state, sa.AuthID) {
		// 账号已落盘但会话刚好过期：不丢账号，只提示宿主重来。
		return jsonManagementResponse(http.StatusOK, map[string]any{
			"ok": true, "auth_id": sa.AuthID, "label": sa.Label,
			"message": "登录态导入成功，但登录会话已过期；账号已保存，可直接在面板使用",
		})
	}
	return jsonManagementResponse(http.StatusOK, map[string]any{
		"ok": true, "auth_id": sa.AuthID, "label": sa.Label, "message": "登录态导入成功",
	})
}

// callbackPath 是授权脚本回传令牌的接收路径（resource 路由，免管理密钥）。
//
// 与桥接页分开：桥接页负责「装书签」，本路径负责「收令牌」。共用一条路径时
// 首次打开和回传要挤在同一个处理里，页面里就没有地方放安装说明了。
const callbackPath = "/login/callback"

// callbackTokenHeader 是回调页同源回传令牌时使用的请求头。
//
// 令牌不走 query：resource 路由的访问日志会记录完整 URL，令牌会因此落进宿主
// 日志。改走「fragment（浏览器不发给服务端）+ 同源请求头」后，服务端日志里
// 只剩 state。
const callbackTokenHeader = "X-Doubao-Token"

// callbackErrorHeader 是回调页同源上报失败原因时使用的请求头。
const callbackErrorHeader = "X-Doubao-Error"

// handleLoginCallback 处理授权脚本回传的令牌。
//
// 脚本在豆包页面取到 x-tt-token 后跳到本路径，令牌放在 URL fragment 里。
// 首次打开只返回接收页（服务端看不到 fragment），由页面脚本把令牌经同源
// 请求头回传，第二次请求才真正换 Cookie + 落盘。
//
// 安全边界：state 一次性且 15 分钟过期，等于一次性的短时凭证；令牌本身只能
// 兑换一次且立即消费，不留在插件内存里。
// [参数] req: 宿主透传的请求（state 在 query，令牌在请求头）。
// [返回] 管理响应（HTML 接收页或 JSON 结果）。
// 最近修改时间：2026-11-14；改动原因：新增，书签授权的令牌接收与落盘。
func handleLoginCallback(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	state := strings.TrimSpace(req.Query.Get("state"))
	// 宽松模式：无 state 时仍允许导入。
	//
	// 面板独立入口与固定书签都不携带 state——书签要能长期复用，不能把一次性
	// 的 state 烤进书签内容。此时账号身份完全由令牌自身决定（saveFromToken 会
	// 用判据端点反查 sec_user_id），不需要会话关联；代价是无法把结果回传给
	// 宿主弹窗，用户回到面板刷新即可看到新账号。
	strict := state != ""
	if strict && !validLoginState(state) {
		return loginCallbackShell("", "授权会话不存在或已过期（15 分钟有效）。请回到管理页重新发起。")
	}

	token := strings.TrimSpace(req.Headers.Get(callbackTokenHeader))
	upstreamErr := strings.TrimSpace(req.Headers.Get(callbackErrorHeader))

	// 首次打开：服务端拿不到 fragment，先返回接收页。
	if token == "" && upstreamErr == "" {
		return loginCallbackShell(state, "")
	}

	// 重放保护：会话已导入成功时直接回成功，不再二次兑换令牌。
	// 回调页可能被刷新或从浏览历史重新打开，而 x-tt-token 只能兑换一次。
	// 宽松模式没有会话可查，但 saveFromToken 对同一账号是幂等覆盖，重放无害。
	if strict && loginImportedAuthID(state) != "" {
		return jsonManagementResponse(http.StatusOK, map[string]any{
			"ok":      true,
			"message": "该授权已完成，请回到管理页查看账号。",
		})
	}

	if token == "" {
		reason := "脚本未取到令牌"
		if upstreamErr != "" {
			reason = "脚本报告：" + upstreamErr
		}
		if strict {
			markLoginFailed(state, reason)
		}
		return jsonManagementResponse(http.StatusOK, map[string]any{
			"ok":      false,
			"message": reason + "。请确认已在豆包页面完成登录后重试。",
		})
	}

	sa, errSave := saveFromToken(token, "")
	if errSave != nil {
		if strict {
			markLoginFailed(state, errSave.Error())
		}
		return jsonManagementResponse(http.StatusOK, map[string]any{
			"ok":      false,
			"message": errSave.Error(),
		})
	}

	if !strict {
		// 固定书签不带 state，但宿主「添加账号」弹窗可能正等结果：
		// 认领最近一个未完成会话，让它自动关闭，而不是空等到 15 分钟超时。
		adopted := adoptPendingLoginSession(sa.AuthID)
		message := "授权成功，账号已保存；回到面板点「刷新账号」即可看到。"
		if adopted {
			message = "授权成功，管理页会自动关闭并显示该账号。"
		}
		return jsonManagementResponse(http.StatusOK, map[string]any{
			"ok":      true,
			"label":   sa.Label,
			"message": message,
		})
	}

	if !markLoginImported(state, sa.AuthID) {
		// 账号已落盘但会话刚好过期：不丢账号，只提示重来。
		return jsonManagementResponse(http.StatusOK, map[string]any{
			"ok":      true,
			"label":   sa.Label,
			"message": "账号已保存，但授权会话已过期；可直接在面板使用该账号。",
		})
	}
	return jsonManagementResponse(http.StatusOK, map[string]any{
		"ok":      true,
		"label":   sa.Label,
		"message": "授权成功，正在通知管理页…",
	})
}

// loginCallbackShell 渲染令牌接收页。
//
// 页面读 location.hash 里的令牌，经同源请求头回传后再展示结果；
// 回传成功后立即清掉地址栏里的 fragment，避免令牌留在浏览历史里。
// [参数] state: 授权会话标识；detail: 非空时为错误说明（不执行回传）。
// [返回] 管理响应（HTML）。
// 最近修改时间：2026-11-14；改动原因：新增，fragment 接收页。
func loginCallbackShell(state, detail string) pluginapi.ManagementResponse {
	var b strings.Builder
	writeBridgeHead(&b, state, "豆包账号授权")
	if detail != "" {
		b.WriteString("<h1>无法继续</h1><div class=\"err\">" + htmlEscape(detail) + "</div>")
		b.WriteString("<p class=\"note\">请回到管理页重新发起授权。</p></div></body></html>")
		return htmlManagementResponse(b.String())
	}
	b.WriteString("<h1>正在完成授权</h1><div id=\"result\" class=\"note\">正在接收令牌…</div>")
	// 回调页通常是新标签页：给一个明确的回跳入口，避免用户停在结果页。
	b.WriteString("<a id=\"backPanel\" class=\"bm\" style=\"display:none;margin-top:16px\" href=\"/v0/resource/plugins/" + providerName + "/panel\">打开豆包面板</a>")
	b.WriteString("<script>")
	b.WriteString("var STATE=document.body.getAttribute(\"data-state\")||\"\";")
	// 宽松模式（面板 / 固定书签）没有 state：不带该参数，服务端按无会话导入处理。
	b.WriteString("var IMPORT=\"/v0/resource/plugins/" + providerName + callbackPath + "\"+(STATE?\"?state=\"+encodeURIComponent(STATE):\"\");")
	b.WriteString("var p=new URLSearchParams(location.hash.replace(/^#/,\"\"));")
	b.WriteString("var token=p.get(\"token\")||\"\";var err=p.get(\"err\")||\"\";")
	b.WriteString("var box=document.getElementById(\"result\");")
	b.WriteString("function show(text,cls){box.className=cls;box.textContent=text}")
	b.WriteString("if(!token&&!err){show(\"未收到授权令牌，请回到管理页重新发起授权。\",\"err\")}else{")
	b.WriteString("  var headers={};")
	b.WriteString("  if(token){headers[\"" + callbackTokenHeader + "\"]=token}else{headers[\"" + callbackErrorHeader + "\"]=err}")
	b.WriteString("  fetch(IMPORT,{method:\"GET\",cache:\"no-store\",headers:headers})")
	b.WriteString("    .then(function(res){return res.json()})")
	b.WriteString("    .then(function(d){")
	b.WriteString("      history.replaceState(null,\"\",location.pathname+location.search);")
	b.WriteString("      if(d&&d.ok){document.querySelector(\"h1\").textContent=\"授权成功\";show((d.message||\"\")+(d.label?\"　账号：\"+d.label:\"\"),\"ok\");var bk=document.getElementById(\"backPanel\");if(bk)bk.style.display=\"inline-block\"}")
	b.WriteString("      else{document.querySelector(\"h1\").textContent=\"授权失败\";show((d&&d.message)||\"未知错误\",\"err\")}")
	b.WriteString("    })")
	b.WriteString("    .catch(function(e){show(\"提交失败：\"+(e&&e.message?e.message:String(e)),\"err\")})")
	b.WriteString("}")
	b.WriteString("</script>")
	b.WriteString("</div></body></html>")
	return htmlManagementResponse(b.String())
}

// loginBridgePage 渲染授权引导页 HTML。detail 非空时为错误页。
//
// 逐段 WriteString 拼接而非原生字符串：页面里需要写 JS，反引号会截断 Go 的
// 原始字符串字面量（与 copilot / coze 桥接页同一处理）。
// [参数] state: 登录会话标识；detail: 错误说明。
// [返回] 管理响应（HTML）。
// 最近修改时间：2026-11-14；改动原因：改为书签一键授权的引导正文。
func loginBridgePage(state, detail string) pluginapi.ManagementResponse {
	var b strings.Builder
	writeBridgeHead(&b, state, "豆包账号授权")
	if detail != "" {
		b.WriteString("<h1>无法继续</h1><div class=\"err\">" + htmlEscape(detail) + "</div>")
		b.WriteString("</div></body></html>")
		return htmlManagementResponse(b.String())
	}
	writeBridgeBody(&b, state)
	writeBridgeScript(&b)
	b.WriteString("</div></body></html>")
	return htmlManagementResponse(b.String())
}

// writeBridgeHead 写出页面头部与样式表。
// [参数] b: 输出缓冲；state: 登录会话标识（写入 body 的 data 属性）；title: 页面标题。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增 title 参数与书签样式，供两种页面共用。
func writeBridgeHead(b *strings.Builder, state, title string) {
	b.WriteString("<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString("<title>" + htmlEscape(title) + "</title>")
	b.WriteString("<style>")
	b.WriteString("body{font-family:-apple-system,'Segoe UI','Microsoft YaHei',sans-serif;background:#faf9f5;color:#2d2a26;margin:0;padding:32px 16px;display:flex;justify-content:center}")
	b.WriteString(".card{background:#fff;border:1px solid #e3e1db;border-radius:14px;max-width:660px;width:100%;padding:26px 28px}")
	b.WriteString("h1{font-size:19px;margin:0 0 10px}p{line-height:1.7;margin:8px 0;font-size:14px;color:#6d6760}")
	b.WriteString("ol{padding-left:20px;color:#6d6760}ol li{margin:9px 0;font-size:14px;line-height:1.7}")
	b.WriteString("code{background:#f0eee8;border-radius:4px;padding:2px 6px;font-size:13px}")
	b.WriteString("pre{background:#26241f;color:#ede9e3;border-radius:9px;padding:13px 15px;font:12px/1.6 ui-monospace,Consolas,monospace;overflow-x:auto;white-space:pre-wrap;word-break:break-all}")
	b.WriteString("textarea{width:100%;box-sizing:border-box;min-height:96px;border:1px solid #e3e1db;border-radius:8px;padding:9px 11px;font:12px/1.5 ui-monospace,Consolas,monospace;outline:none;resize:vertical}")
	b.WriteString("textarea:focus{border-color:#4c8dff;box-shadow:0 0 0 3px rgba(76,141,255,.18)}")
	b.WriteString("button{margin-top:12px;background:#4c8dff;color:#fff;border:0;border-radius:8px;padding:9px 18px;font-size:14px;cursor:pointer}")
	b.WriteString("button:disabled{opacity:.55;cursor:default}")
	b.WriteString("a.bm{display:inline-block;background:#4c8dff;color:#fff;border-radius:8px;padding:9px 18px;font-size:14px;text-decoration:none;cursor:grab;user-select:none}")
	b.WriteString("details{margin-top:16px}details summary{cursor:pointer;color:#4c8dff;font-size:14px;outline:none}")
	b.WriteString(".err{background:#fdf1ef;border:1px solid #e8b9b1;border-radius:8px;padding:10px 14px;margin-top:14px;font-size:14px;color:#a8452f}")
	b.WriteString(".ok{background:#eaf8f2;border:1px solid #a8dcc7;border-radius:8px;padding:10px 14px;margin-top:14px;font-size:14px;color:#0d7a55}")
	b.WriteString(".note{background:#fdf7e6;border:1px solid #edd9a3;border-radius:8px;padding:10px 14px;margin-top:14px;font-size:13px;color:#8a6a10}")
	b.WriteString("</style></head><body data-provider=\"" + htmlEscape(providerName) + "\" data-state=\"" + htmlEscape(state) + "\"><div class=\"card\">")
}

// writeBridgeBody 写出授权引导正文。
//
// 三条通道并存，主通道是书签：豆包的关键会话 Cookie（sessionid / sid_guard）
// 是 HttpOnly，页面 JS 读不到，用户手工去开发者工具里复制既麻烦又常漏字段；
// 书签把「在同一页面取一次令牌」压缩成一次点击，装一次即可长期复用。
// 保留后两条兜底：少数环境会用 CSP 拦掉 javascript: 书签，此时粘脚本或
// 手工粘 Cookie 仍能完成授权。
// [参数] b: 输出缓冲；state: 授权会话标识（为空表示面板独立入口）。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：改为书签一键授权，保留手工粘贴兜底。
func writeBridgeBody(b *strings.Builder, state string) {
	script := buildAuthScript(state)
	hasState := strings.TrimSpace(state) != ""
	b.WriteString("<h1>授权豆包账号</h1>")
	b.WriteString("<p>豆包没有开放给第三方的 OAuth：登录态是浏览器里的 HttpOnly Cookie，页面脚本读不到。下面的书签替你在豆包页面取一次性令牌，由插件换成完整登录态——装一次即可长期复用。</p>")
	b.WriteString("<p style=\"margin-top:16px\"><b>① 安装授权书签（只做一次）</b><br>把下面的按钮拖到浏览器书签栏（不要直接点击）：</p>")
	b.WriteString("<p><a id=\"bmLink\" class=\"bm\" href=\"#\" draggable=\"true\">豆包一键授权</a></p>")
	b.WriteString("<p style=\"margin-top:14px\"><b>② 打开豆包并登录</b></p>")
	b.WriteString("<button id=\"openDoubao\" type=\"button\">打开豆包并登录</button>")
	b.WriteString("<p style=\"margin-top:14px\"><b>③ 在豆包页面点一下书签</b><br>登录完成后，回到豆包标签页，点书签栏里的「豆包一键授权」；页面会自动跳回这里并完成授权。</p>")
	if !hasState {
		b.WriteString("<p class=\"note\" style=\"margin-top:16px\">这是面板独立入口：不依赖宿主弹窗，账号落盘后回到面板点「刷新账号」即可看到。新增账号或更换登录态时，重复上面三步即可。</p>")
	}
	b.WriteString("<details><summary>方式二：一次性脚本（书签被浏览器策略拦截时用）</summary>")
	b.WriteString("<p style=\"margin-top:10px\">复制到豆包页面的 Console 执行：</p>")
	b.WriteString("<pre id=\"authScript\">" + htmlEscape(script) + "</pre>")
	b.WriteString("<button id=\"copyScript\" type=\"button\">复制脚本</button>")
	b.WriteString("<div id=\"copyHint\"></div>")
	b.WriteString("</details>")
	b.WriteString("<details><summary>方式三：手动粘贴 Cookie（前两者都不可用时用）</summary>")
	b.WriteString("<p style=\"margin-top:10px\">开发者工具 → Application → Cookies → <code>https://www.doubao.com</code>，复制完整 Cookie header 后粘贴到这里：</p>")
	b.WriteString("<textarea id=\"cookie\" placeholder=\"flow_cur_user_sec_id=...; sessionid=...; ttwid=...\"></textarea>")
	b.WriteString("<button id=\"submit\" type=\"button\">导入并完成</button>")
	b.WriteString("<div id=\"result\"></div>")
	b.WriteString("</details>")
	b.WriteString("<p style=\"margin-top:16px\"><b>换浏览器或发给别人？</b><br>本页完整地址可直接复制（含授权参数，15 分钟内有效）：</p>")
	b.WriteString("<pre id=\"bridgeURL\"></pre>")
	b.WriteString("<button id=\"copyURL\" type=\"button\">复制完整地址</button>")
	b.WriteString("<div id=\"copyURLHint\"></div>")
	b.WriteString("<p class=\"note\">书签只做一件事：在豆包页面内调用令牌接口取 <code>x-tt-token</code>，然后把令牌交给本插件。它不读取、也不上传你的 Cookie，内容不含任何密钥，可以长期保留。</p>")
}

// writeBridgeScript 写出引导页脚本。
//
// 书签内容由服务端生成后经 jsLiteral 编码注入：脚本里带引号、反斜杠和 </，
// 直接拼接会被打断。占位符 __CPA_ORIGIN__ 在页面加载时替换成 location.origin——
// 书签要在豆包页面执行，那里的 origin 是 doubao.com，回调必须指向插件所在域。
// [参数] b: 输出缓冲。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增书签 href 注入与三条通道的交互。
func writeBridgeScript(b *strings.Builder) {
	b.WriteString("<script>")
	b.WriteString("var STATE=document.body.getAttribute(\"data-state\")||\"\";")
	b.WriteString("var PROVIDER=document.body.getAttribute(\"data-provider\")||\"\";")
	b.WriteString("var BM=" + jsLiteral(buildBookmarkletScript()) + ";")
	b.WriteString("var scriptEl=document.getElementById(\"authScript\");")
	b.WriteString("if(scriptEl){scriptEl.textContent=scriptEl.textContent.split(\"" + cpaOriginPlaceholder + "\").join(location.origin)}")
	b.WriteString("var bmLink=document.getElementById(\"bmLink\");")
	b.WriteString("if(bmLink){")
	b.WriteString("  bmLink.href=BM.split(\"" + cpaOriginPlaceholder + "\").join(location.origin);")
	b.WriteString("  bmLink.addEventListener(\"click\",function(e){e.preventDefault();alert(\"请把这个按钮拖到浏览器书签栏（不要直接点击），然后在豆包页面点击该书签。\")})")
	b.WriteString("}")
	b.WriteString("var bridgeEl=document.getElementById(\"bridgeURL\");")
	b.WriteString("if(bridgeEl){bridgeEl.textContent=location.href}")
	b.WriteString("function bindCopy(btnId,hintId,textGetter,okText){")
	b.WriteString("  var btn=document.getElementById(btnId);if(!btn){return}")
	b.WriteString("  btn.addEventListener(\"click\",function(){")
	b.WriteString("    var hint=document.getElementById(hintId);")
	b.WriteString("    var done=function(){hint.className=\"ok\";hint.textContent=okText};")
	b.WriteString("    var fail=function(){hint.className=\"err\";hint.textContent=\"自动复制失败，请手动选中上面的文本复制。\"};")
	b.WriteString("    var text=textGetter();")
	b.WriteString("    if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(text).then(done).catch(fail)}else{fail()}")
	b.WriteString("  })")
	b.WriteString("}")
	b.WriteString("bindCopy(\"copyURL\",\"copyURLHint\",function(){return location.href},\"已复制完整地址。\");")
	b.WriteString("bindCopy(\"copyScript\",\"copyHint\",function(){var el=document.getElementById(\"authScript\");return el?el.textContent:\"\"},\"已复制，请到豆包页面的 Console 粘贴执行。\");")
	b.WriteString("var openBtn=document.getElementById(\"openDoubao\");")
	b.WriteString("if(openBtn){openBtn.addEventListener(\"click\",function(){window.open(\"https://www.doubao.com\",\"_blank\",\"noopener\")})}")
	b.WriteString("var submitBtn=document.getElementById(\"submit\");")
	b.WriteString("if(submitBtn){submitBtn.addEventListener(\"click\",function(){")
	b.WriteString("  var btn=this;var cookieEl=document.getElementById(\"cookie\");var cookie=cookieEl?cookieEl.value.trim():\"\";")
	b.WriteString("  var box=document.getElementById(\"result\");")
	b.WriteString("  function show(text,cls){box.className=cls;box.textContent=text}")
	b.WriteString("  if(!cookie){show(\"请先粘贴 Cookie\",\"err\");return}")
	b.WriteString("  var IMPORT=\"/v0/resource/plugins/\"+PROVIDER+\"/login/import\"+(STATE?\"?state=\"+encodeURIComponent(STATE):\"\");")
	b.WriteString("  btn.disabled=true;show(\"正在校验登录态…\",\"\");")
	b.WriteString("  fetch(IMPORT,{method:\"GET\",cache:\"no-store\",headers:{\"X-Doubao-Cookie\":cookie}})")
	b.WriteString("    .then(function(res){return res.json().then(function(d){return {status:res.status,data:d}}).catch(function(){return {status:res.status,data:{error:\"响应解析失败\"}}})})")
	b.WriteString("    .then(function(r){")
	b.WriteString("      if(r.data&&r.data.ok){show(\"导入成功，正在通知管理页…\",\"ok\");btn.textContent=\"已导入\";return}")
	b.WriteString("      show(\"导入失败：\"+((r.data&&r.data.error)||\"未知错误\"),\"err\");")
	b.WriteString("    })")
	b.WriteString("    .catch(function(e){show(\"导入失败：\"+(e&&e.message?e.message:String(e)),\"err\")})")
	b.WriteString("    .then(function(){btn.disabled=false})")
	b.WriteString("})}")
	b.WriteString("</script>")
}

// cpaOriginPlaceholder 是脚本里等待替换成插件所在域 origin 的占位符。
//
// 不能直接在服务端拼绝对地址：插件运行在容器内，看不到用户实际访问的域名
// （反向代理 / 端口映射都会改写）。引导页在浏览器里加载，location.origin
// 就是用户真实可用的地址，替换后才不会把令牌发到 doubao.com。
const cpaOriginPlaceholder = "__CPA_ORIGIN__"

// publicOrigin 缓存最近一次请求观测到的外部访问 origin。
//
// 插件跑在容器里，永远不知道用户实际用的域名；但每个管理 / 资源请求都带着
// Host 与 X-Forwarded-* 头，取一次就能拼出用户可复制的绝对地址，供宿主登录
// 弹窗里的入口 URL 使用。
var (
	publicOriginMu sync.RWMutex
	publicOrigin   string
)

// rememberPublicOrigin 从请求头记录外部 origin（取不到 Host 时保持原值）。
// [参数] h: 请求头。
// [返回] 无。
// 最近修改时间：2026-11-14；改动原因：新增，为授权入口拼绝对 URL。
func rememberPublicOrigin(h http.Header) {
	if h == nil {
		return
	}
	host := firstHeaderValue(h.Get("X-Forwarded-Host"))
	if host == "" {
		host = firstHeaderValue(h.Get("Host"))
	}
	if host == "" {
		return
	}
	scheme := firstHeaderValue(h.Get("X-Forwarded-Proto"))
	if scheme == "" {
		scheme = "https"
	}
	publicOriginMu.Lock()
	// 回环地址（容器内自测、健康检查、本地 curl）不能覆盖用户真实访问的域名，
	// 否则宿主弹窗里的授权入口会变成 https://127.0.0.1:8317/...；
	// 只有在还没有任何观测值时，才允许用它兜底。
	if publicOrigin == "" || !isLoopbackHost(host) {
		publicOrigin = scheme + "://" + host
	}
	publicOriginMu.Unlock()
}

// isLoopbackHost 判断主机名是否为回环 / 通配地址。
// [参数] host: 可能带端口的 Host 值。
// [返回] 属于回环地址时为 true。
// 最近修改时间：2026-11-14；改动原因：新增，防止容器内自测污染外部 origin。
func isLoopbackHost(host string) bool {
	name := host
	if idx := strings.LastIndex(name, ":"); idx > 0 {
		name = name[:idx]
	}
	name = strings.Trim(name, "[]")
	switch name {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return true
	}
	return false
}

// currentPublicOrigin 返回最近记录的 origin，未观测到时返回空串。
// [参数] 无。
// [返回] 形如 https://cpa.example.com 的 origin。
// 最近修改时间：2026-11-14；改动原因：新增，绝对 URL 生成。
func currentPublicOrigin() string {
	publicOriginMu.RLock()
	defer publicOriginMu.RUnlock()
	return publicOrigin
}

// firstHeaderValue 取多级代理头里的第一个值。
// [参数] raw: 原始头值。
// [返回] 去空白后的首段；空值时返回空串。
// 最近修改时间：2026-11-14；改动原因：新增，兼容逗号分隔的转发头。
func firstHeaderValue(raw string) string {
	if raw == "" {
		return ""
	}
	if idx := strings.Index(raw, ","); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw)
}

// buildAuthScript 生成一次性同源授权脚本。
//
// 脚本在豆包页面上下文执行，因此 fetch 天然带上登录 Cookie（credentials 必须
// 显式设为 include：默认策略下不一定自动携带）。取到 x-tt-token 后跳转插件
// 回调页；失败时把上游 message 一并带回，让回调页能显示可读原因。
// [参数] state: 授权会话标识。
// [返回] 可粘贴执行的 JS 源码（含待替换的 origin 占位符）。
// 最近修改时间：2026-11-14；改动原因：新增，脚本生成；回调域改占位符注入。
func buildAuthScript(state string) string {
	callback := "/v0/resource/plugins/" + providerName + callbackPath
	var b strings.Builder
	b.WriteString("(async()=>{\n")
	b.WriteString("  const r=await fetch('" + tokenBeatPath + "',{method:'POST',credentials:'include',")
	b.WriteString("headers:{'sdk-version':'" + tokenBeatSDKVersion + "','Content-Type':'" + tokenBeatContentType + "'},body:'{}'});\n")
	b.WriteString("  const j=await r.json().catch(()=>({}));\n")
	b.WriteString("  const t=r.headers.get('x-tt-token');\n")
	b.WriteString("  const cb='")
	b.WriteString(cpaOriginPlaceholder)
	b.WriteString(callback)
	// 无 state 时不拼该参数：面板独立入口没有会话，拼上去会让回调页多一个
	// 空参数，读日志时也难分辨是哪种入口。
	if strings.TrimSpace(state) != "" {
		b.WriteString("?state=")
		b.WriteString(jsStringEscape(state))
	}
	b.WriteString("';\n")
	b.WriteString("  if(!t){location.href=cb+'#err='+encodeURIComponent((j&&j.data&&j.data.description)||(j&&j.message)||'no_token');return;}\n")
	b.WriteString("  location.href=cb+'#token='+encodeURIComponent(t);\n")
	b.WriteString("})()")
	return b.String()
}

// jsStringEscape 转义要嵌入单引号 JS 字符串的文本。
// [参数] s: 原始文本。
// [返回] 转义后的文本。
// 最近修改时间：2026-11-14；改动原因：新增，避免 state 破坏脚本字面量。
func jsStringEscape(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "'", "\\'", "\n", "\\n", "\r", "\\r", "<", "\\x3c")
	return r.Replace(s)
}

// buildBookmarkletScript 生成可长期复用的固定书签脚本。
//
// 与 buildAuthScript 的区别是内容不含 state：拖进书签栏后永久可用，不受
// 15 分钟会话限制，也不需要每次授权前重新复制——这是「一次安装、之后
// 一点即完成」的关键。代价是落盘结果无法直接回传给宿主弹窗（没有会话可
// 登记），因此由 adoptPendingLoginSession 在服务端认领最近的待完成会话。
// [参数] 无。
// [返回] javascript: 开头的单行脚本（含待替换的 origin 占位符）。
// 最近修改时间：2026-11-14；改动原因：新增，一次性书签授权。
func buildBookmarkletScript() string {
	callback := "/v0/resource/plugins/" + providerName + callbackPath
	var b strings.Builder
	b.WriteString("javascript:(async()=>{try{")
	b.WriteString("const r=await fetch('" + tokenBeatPath + "',{method:'POST',credentials:'include',")
	b.WriteString("headers:{'sdk-version':'" + tokenBeatSDKVersion + "','Content-Type':'" + tokenBeatContentType + "'},body:'{}'});")
	b.WriteString("const j=await r.json().catch(()=>({}));")
	b.WriteString("const t=r.headers.get('x-tt-token');")
	b.WriteString("if(!t){alert('未检测到豆包登录态：'+(((j&&j.data&&j.data.description)||(j&&j.message))||'请先在本页完成登录'));return}")
	b.WriteString("location.href='")
	b.WriteString(cpaOriginPlaceholder)
	b.WriteString(callback)
	b.WriteString("#token='+encodeURIComponent(t)")
	b.WriteString("}catch(e){alert('取令牌失败：'+e)}})()")
	return b.String()
}

// jsLiteral 把脚本编码成可安全嵌入 <script> 的 JSON 字符串字面量。
//
// 直接拼接会被脚本里的引号、反斜杠破坏；JSON 编码保证转义正确，再把 "</"
// 替换成 "<\/"，避免提前闭合 <script> 标签。
// [参数] s: 原始脚本文本。
// [返回] 带引号的 JSON 字面量。
// 最近修改时间：2026-11-14；改动原因：新增，书签脚本安全注入。
func jsLiteral(s string) string {
	raw, errMarshal := json.Marshal(s)
	if errMarshal != nil {
		return "\"\""
	}
	return strings.ReplaceAll(string(raw), "</", "<\\/")
}

// htmlManagementResponse 把 HTML 文本包装成管理响应。
// [参数] page: 完整 HTML。
// [返回] 管理响应。
// 最近修改时间：2026-10-17；改动原因：新增，HTML 响应构造。
func htmlManagementResponse(page string) pluginapi.ManagementResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: []byte(page)}
}

// jsonManagementResponse 把结果编码成 JSON 管理响应。
// [参数] status: HTTP 状态码；payload: 结果对象。
// [返回] 管理响应。
// 最近修改时间：2026-10-17；改动原因：新增，桥接页提交结果回写。
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
// 最近修改时间：2026-10-17；改动原因：新增，错误说明渲染。
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;")
	return r.Replace(s)
}
