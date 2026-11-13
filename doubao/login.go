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
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       bridgeURL,
		State:     state,
		ExpiresAt: time.Now().Add(loginStateTTL).UTC(),
		Metadata: map[string]any{
			"logo":       pluginLogoURL,
			"bridge_url": bridgeURL,
			"prompt":     "在打开的引导页中粘贴豆包浏览器的 Cookie，导入成功后此窗口会自动完成。",
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
	var importedAuthID string
	if ok {
		importedAuthID = session.importedAuthID
	}
	loginSessionsMu.Unlock()

	if !ok {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "登录会话不存在或已过期，请回到管理页重新发起",
		})
	}
	if importedAuthID == "" {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "等待在引导页中粘贴 Cookie",
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

// handleLoginBridge 渲染桥接页。
//
// 页面只做两件事：说明为什么没有一键授权，以及把用户粘贴的 Cookie 交给插件
// 自己消费。提交走 resource 路由（/login/import）而不是 management 路由：
// resource 路由由宿主以 GET 免密钥放行，桥接页是浏览器顶层打开的普通页面，
// 拿不到、也不该要求用户填管理密钥（与 traework 桥接页同一做法）。
// [参数] state: 登录会话标识；rawQuery: 原始查询串（兼容资源路由的取参差异）。
// [返回] 管理响应（HTML）。
// 最近修改时间：2026-10-17；改动原因：提交改走免密钥的 resource 路由。
func handleLoginBridge(state, rawQuery string) pluginapi.ManagementResponse {
	state = strings.TrimSpace(state)
	if state == "" {
		return loginBridgePage("", "缺少 state 参数：请从管理页的「添加账号」重新发起。")
	}

	loginSessionsMu.Lock()
	session, ok := loginSessions[state]
	if ok && time.Since(session.startedAt) > loginStateTTL {
		delete(loginSessions, state)
		ok = false
	}
	loginSessionsMu.Unlock()
	if !ok {
		return loginBridgePage("", "登录会话不存在或已过期（15 分钟有效）。请回到管理页重新点击「添加账号」。")
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
// 只有持有有效 state 的桥接页才能调用：state 在 handleStartLogin 时随机生成、
// 15 分钟过期，等于一次性的短时凭证。导入成功后登记到该会话，宿主轮询领取。
// [参数] req: 宿主透传的请求（Cookie 在 X-Doubao-Cookie 头，state 在 query）。
// [返回] JSON 结果信封。
// 最近修改时间：2026-10-17；改动原因：新增，桥接页免密钥提交入口。
func handleLoginBridgeImport(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	state := strings.TrimSpace(req.Query.Get("state"))
	if state == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "缺少登录会话标识，请回到管理页重新发起"})
	}

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

	cookie := strings.TrimSpace(req.Headers.Get(loginBridgeImportHeader))
	if cookie == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "cookie 不能为空"})
	}

	timeout := time.Duration(currentConfig().TimeoutSeconds) * time.Second
	sa, errSave := saveCredentials(cookie, "", timeout)
	if errSave != nil {
		return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
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

// loginBridgePage 渲染桥接页 HTML。detail 非空时为错误页。
//
// 逐段 WriteString 拼接而非原生字符串：页面里需要写 JS，反引号会截断 Go 的
// 原始字符串字面量（与 copilot 桥接页同一处理）。
// [参数] state: 登录会话标识；detail: 错误说明。
// [返回] 管理响应（HTML）。
// 最近修改时间：2026-10-17；改动原因：新增，桥接页渲染。
func loginBridgePage(state, detail string) pluginapi.ManagementResponse {
	var b strings.Builder
	writeBridgeHead(&b, state)
	if detail != "" {
		b.WriteString("<h1>无法继续</h1><div class=\"err\">" + htmlEscape(detail) + "</div>")
		b.WriteString("</div></body></html>")
		return htmlManagementResponse(b.String())
	}
	writeBridgeBody(&b)
	writeBridgeScript(&b)
	b.WriteString("</div></body></html>")
	return htmlManagementResponse(b.String())
}

// writeBridgeHead 写出页面头部与样式表。
// [参数] b: 输出缓冲；state: 登录会话标识（写入 body 的 data 属性）。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，从桥接页渲染中拆出。
func writeBridgeHead(b *strings.Builder, state string) {
	b.WriteString("<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString("<title>豆包账号登录</title>")
	b.WriteString("<style>")
	b.WriteString("body{font-family:-apple-system,'Segoe UI','Microsoft YaHei',sans-serif;background:#faf9f5;color:#2d2a26;margin:0;padding:32px 16px;display:flex;justify-content:center}")
	b.WriteString(".card{background:#fff;border:1px solid #e3e1db;border-radius:14px;max-width:600px;width:100%;padding:26px 28px}")
	b.WriteString("h1{font-size:19px;margin:0 0 10px}p{line-height:1.7;margin:8px 0;font-size:14px;color:#6d6760}")
	b.WriteString("ol{padding-left:20px;color:#6d6760}ol li{margin:8px 0;font-size:14px;line-height:1.7}")
	b.WriteString("code{background:#f0eee8;border-radius:4px;padding:2px 6px;font-size:13px}")
	b.WriteString("textarea{width:100%;box-sizing:border-box;min-height:96px;border:1px solid #e3e1db;border-radius:8px;padding:9px 11px;font:12px/1.5 ui-monospace,Consolas,monospace;outline:none;resize:vertical}")
	b.WriteString("textarea:focus{border-color:#4c8dff;box-shadow:0 0 0 3px rgba(76,141,255,.18)}")
	b.WriteString("input{width:100%;box-sizing:border-box;border:1px solid #e3e1db;border-radius:8px;padding:8px 11px;font-size:13px;outline:none;margin-top:8px}")
	b.WriteString("button{margin-top:12px;background:#4c8dff;color:#fff;border:0;border-radius:8px;padding:9px 18px;font-size:14px;cursor:pointer}")
	b.WriteString("button:disabled{opacity:.55;cursor:default}")
	b.WriteString(".err{background:#fdf1ef;border:1px solid #e8b9b1;border-radius:8px;padding:10px 14px;margin-top:14px;font-size:14px;color:#a8452f}")
	b.WriteString(".ok{background:#eaf8f2;border:1px solid #a8dcc7;border-radius:8px;padding:10px 14px;margin-top:14px;font-size:14px;color:#0d7a55}")
	b.WriteString(".note{background:#fdf7e6;border:1px solid #edd9a3;border-radius:8px;padding:10px 14px;margin-top:14px;font-size:13px;color:#8a6a10}")
	b.WriteString("</style></head><body data-provider=\"" + htmlEscape(providerName) + "\" data-state=\"" + htmlEscape(state) + "\"><div class=\"card\">")
}

// writeBridgeBody 写出引导文案与表单控件。
// [参数] b: 输出缓冲。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：移除管理密钥输入框（改走免密钥 resource 路由）。
func writeBridgeBody(b *strings.Builder) {
	b.WriteString("<h1>绑定豆包账号</h1>")
	b.WriteString("<p>豆包没有开放给第三方的一键授权：登录态是浏览器里的 HttpOnly Cookie，只能由你从浏览器导出。请按下面三步操作。</p>")
	b.WriteString("<ol>")
	b.WriteString("<li>在浏览器打开并登录 <code>https://www.doubao.com</code></li>")
	b.WriteString("<li>开发者工具 → Application → Cookies → <code>https://www.doubao.com</code>，复制完整 Cookie header</li>")
	b.WriteString("<li>粘贴到下方文本框并点「导入并完成」</li>")
	b.WriteString("</ol>")
	b.WriteString("<textarea id=\"cookie\" placeholder=\"flow_cur_user_sec_id=...; sessionid=...; ttwid=...\"></textarea>")
	b.WriteString("<button id=\"submit\">导入并完成</button>")
	b.WriteString("<div id=\"result\"></div>")
	b.WriteString("<div class=\"note\">导入成功后本页会自动提示，回到管理页即可看到新账号。</div>")
}

// writeBridgeScript 写出桥接页脚本。
//
// 脚本保持纯静态：provider 与 state 从 body 的 data 属性读取，避免把 Go 变量
// 拼进 JS 字面量——拼接点既难测，也容易引入转义缺陷。
// 提交走 resource 路由（免管理密钥），Cookie 放自定义请求头，因为宿主把
// resource 请求统一改写为 GET 且不转发请求体。
// [参数] b: 输出缓冲。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：改走免密钥 resource 路由提交。
func writeBridgeScript(b *strings.Builder) {
	b.WriteString("<script>")
	b.WriteString("var PROVIDER=document.body.getAttribute(\"data-provider\")||\"\";")
	b.WriteString("var STATE=document.body.getAttribute(\"data-state\")||\"\";")
	b.WriteString("var IMPORT=\"/v0/resource/plugins/\"+PROVIDER+\"/login/import?state=\"+encodeURIComponent(STATE);")
	b.WriteString("function show(text,cls){var r=document.getElementById(\"result\");r.className=cls;r.textContent=text;}")
	b.WriteString("document.getElementById(\"submit\").addEventListener(\"click\",function(){")
	b.WriteString("  var btn=this;var cookie=document.getElementById(\"cookie\").value.trim();")
	b.WriteString("  if(!cookie){show(\"请先粘贴 Cookie\",\"err\");return}")
	b.WriteString("  btn.disabled=true;show(\"正在校验登录态…\",\"\");")
	b.WriteString("  fetch(IMPORT,{method:\"GET\",cache:\"no-store\",headers:{\"X-Doubao-Cookie\":cookie}})")
	b.WriteString("    .then(function(res){return res.json().then(function(d){return {status:res.status,data:d}}).catch(function(){return {status:res.status,data:{error:\"响应解析失败\"}}})})")
	b.WriteString("    .then(function(r){")
	b.WriteString("      if(r.data&&r.data.ok){show(\"导入成功，正在通知管理页…\",\"ok\");document.getElementById(\"submit\").textContent=\"已导入\";return}")
	b.WriteString("      show(\"导入失败：\"+((r.data&&r.data.error)||\"未知错误\"),\"err\");")
	b.WriteString("    })")
	b.WriteString("    .catch(function(e){show(\"导入失败：\"+(e&&e.message?e.message:String(e)),\"err\")})")
	b.WriteString("    .then(function(){btn.disabled=false})")
	b.WriteString("});")
	b.WriteString("</script>")
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
