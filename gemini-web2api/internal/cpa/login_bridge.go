package cpa

import (
	_ "embed"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
)

// loginBridgeHTML 是桥接页模板，占位符由 renderLoginBridge 替换。
//
// 独立存成文件是为了能直接按 HTML 语法审阅与校验，避免模板混在 Go 字符串里。
//
//go:embed login_bridge.html
var loginBridgeHTML string

// 本文件实现 Gemini Web 的「一键授权」登录面：桥接页 + 浏览器扩展回传。
//
// 背景（2026-11-21 取证）：生产容器是无 Chrome 的 Debian 裸镜像，
// chromeauth.FindLocalChromeBinary() 必然返回空，上游「拉起本机 Chrome 无痕窗口」
// 这条路径在生产上完全不可用；而 SAPISID / __Secure-1PSID 是 HttpOnly，
// 页面 JS 读不到。因此登录面改为：宿主 auth.login.start 返回一个桥接页地址，
// 用户在已登录 Google 的浏览器里用扩展（chrome.cookies 能读 HttpOnly）
// 把会话 Cookie 推回插件的资源路由，插件校验后导入账号池。
//
// 为什么提交端点也用 GET：宿主 pluginhost/management.go 的 ServeResourceHTTP
// 只放行 GET，且资源路由不向插件传递请求体；资源路由又是唯一免管理密钥的
// 可浏览器直达通道，所以 Cookie 走 query 传递。

const (
	// loginResourcePrefix 是本插件资源路由前缀。
	loginResourcePrefix = "/v0/resource/plugins/" + PluginID
	// loginBridgePath 是用户可见的桥接页地址。
	loginBridgePath = loginResourcePrefix + "/login"
	// loginSubmitPath 接收扩展/手工粘贴推送的会话 Cookie。
	loginSubmitPath = loginResourcePrefix + "/login/submit"
	// loginPollPath 供桥接页轮询登录结果。
	loginPollPath = loginResourcePrefix + "/login/poll"
	// loginExtensionPath 下载浏览器授权助手扩展 zip。
	loginExtensionPath = loginResourcePrefix + "/login/extension.zip"
	// loginPairPrefix 是扩展识别配对码的前缀。
	loginPairPrefix = "cpa"
)

// loginAction 描述资源路由的一次登录动作结果。
//
// 用结构体而不是 (any, bool, error) 三元组，避免调用处 unpack 出歧义。
type loginAction struct {
	// Handled 为 true 表示该请求已被登录面接管，调用方应直接返回 Response。
	Handled bool
	// Response 是最终写回浏览器的响应。
	Response pluginapi.ManagementResponse
}

// handleLoginResource 处理登录面的全部资源路由。
//
// 必须在静态面板分发之前调用，否则 /v0/resource/plugins/gemini-web2api
// 前缀会把登录页一起吞成面板 HTML。
// [参数] req 是宿主转来的管理请求（资源路由只会有 GET）；path 是去掉 query 的路径。
// [返回] loginAction，Handled 为 false 时表示不是登录面请求。
// 最近修改时间：2026-11-21；改动原因：新增，替代生产不可用的本机 Chrome 登录。
func (p *Plugin) handleLoginResource(req rpcManagementRequest, path string) loginAction {
	switch path {
	case loginBridgePath, loginBridgePath + "/":
		return loginAction{Handled: true, Response: p.renderLoginBridge(req)}
	case loginExtensionPath:
		return loginAction{Handled: true, Response: loginExtensionResponse()}
	case loginSubmitPath:
		return loginAction{Handled: true, Response: p.handleLoginSubmit(req)}
	case loginPollPath:
		return loginAction{Handled: true, Response: p.handleLoginStatus(req)}
	default:
		return loginAction{}
	}
}

// renderLoginBridge 输出桥接页 HTML。
//
// 无 state 或 state 过期时会新建一个会话，让用户从面板菜单直接打开也能用；
// 新建的会话同样进入 p.loginStates，因此宿主 auth.login.poll 的后备分支
// （扫描账号池新增账号）依然能感知到登录完成。
// [参数] req 携带 query（可含 state）与 Headers（用于推导对外 origin）。
// [返回] HTML 管理响应，含配对码、扩展下载地址与轮询地址。
// 最近修改时间：2026-11-21；改动原因：新增。
func (p *Plugin) renderLoginBridge(req rpcManagementRequest) pluginapi.ManagementResponse {
	state := p.ensureLoginState(strings.TrimSpace(req.Query.Get("state")))
	origin := publicOriginFromRequest(req)
	pair := loginPairPrefix + "|" + origin + "|" + state
	page := strings.NewReplacer(
		"{{PAIR}}", htmlEscape(pair),
		"{{STATE}}", htmlEscape(state),
		"{{ORIGIN}}", htmlEscape(origin),
		"{{EXT_URL}}", htmlEscape(loginExtensionPath),
		"{{SUBMIT_URL}}", htmlEscape(loginSubmitPath),
		"{{POLL_URL}}", htmlEscape(loginPollPath),
	).Replace(loginBridgeHTML)
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(page),
	}
}

// handleLoginSubmit 接收会话 Cookie 并异步导入账号池。
//
// 导入包含一次真实的 Gemini 会话校验（最长 30s），因此这里立刻返回
// importing，由桥接页轮询 loginPollPath 取最终结果，避免浏览器请求被
// 代理层超时切断。
// [参数] req 携带 query：state、cookies（Cookie 请求头文本）。
// [返回] JSON 管理响应。
// 最近修改时间：2026-11-21；改动原因：新增。
func (p *Plugin) handleLoginSubmit(req rpcManagementRequest) pluginapi.ManagementResponse {
	state := strings.TrimSpace(req.Query.Get("state"))
	cookies := strings.TrimSpace(req.Query.Get("cookies"))
	if cookies == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "cookies is required"})
	}
	if state == "" || !p.hasLoginState(state) {
		return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "login state is missing or expired, reopen the bridge page"})
	}

	p.markLoginPending(state)
	go p.runLoginImport(state, cookies)
	return jsonManagementResponse(http.StatusAccepted, map[string]any{
		"status": "importing",
		"state":  state,
	})
}

// handleLoginStatus 返回指定 state 的登录进度，供桥接页轮询。
// [参数] req 携带 query：state。
// [返回] JSON 管理响应，status 为 pending/importing/success/error/expired 之一。
// 最近修改时间：2026-11-21；改动原因：新增。
func (p *Plugin) handleLoginStatus(req rpcManagementRequest) pluginapi.ManagementResponse {
	state := strings.TrimSpace(req.Query.Get("state"))
	p.loginStateMu.Lock()
	session := p.loginStates[state]
	payload := map[string]any{"status": "pending"}
	if session == nil {
		payload["status"] = "expired"
	} else {
		switch {
		case session.Done && session.AccountID != "":
			payload["status"] = "success"
			payload["account_id"] = session.AccountID
			payload["email"] = session.Email
		case session.Done && session.ErrorMessage != "":
			payload["status"] = "error"
			payload["message"] = session.ErrorMessage
		case session.Importing:
			payload["status"] = "importing"
		}
	}
	p.loginStateMu.Unlock()
	return jsonManagementResponse(http.StatusOK, payload)
}

// runLoginImport 在后台完成 Cookie 校验、账号导入与宿主同步。
//
// [参数] state 是登录会话标识；cookies 是 Cookie 请求头文本。
// [返回] 无；结果写回 p.loginStates。
// 最近修改时间：2026-11-21；改动原因：新增。
func (p *Plugin) runLoginImport(state, cookies string) {
	defer func() {
		if r := recover(); r != nil {
			p.finishLogin(state, "", "", fmt.Sprintf("import panic: %v", r))
		}
	}()

	rt, err := p.Runtime()
	if err != nil {
		p.finishLogin(state, "", "", err.Error())
		return
	}
	accountID, err := rt.Accounts.ImportCookie(p.ctx, cookies, "", strings.TrimSpace(p.appCfg.ProxyURL), 0)
	if err != nil {
		p.finishLogin(state, "", "", err.Error())
		return
	}

	email := ""
	if client := rt.Pool.Client(accountID); client != nil {
		email = client.Email()
	}
	p.finishLogin(state, accountID, email, "")

	p.loginStateMu.Lock()
	callbackID := ""
	if st := p.loginStates[state]; st != nil {
		callbackID = st.CallbackID
	}
	p.loginStateMu.Unlock()
	p.serviceStopped.Store(false)
	_ = p.SyncAccountIDToHostAuth(callbackID, accountID)
}

// ensureLoginState 校验并返回可用的登录会话标识。
//
// 调用方给了 state 就沿用它（宿主 auth.login.start 已经发过 state，桥接页必须
// 用同一个，否则 auth.login.poll 永远查不到这个会话），只在缺失时新建。
// 初始账号快照在锁外取，避免持 loginStateMu 时进入 Runtime 造成锁序倒置。
// [参数] state 是调用方希望使用的会话标识，可为空。
// [返回] 最终可用的会话标识。
// 最近修改时间：2026-11-21；改动原因：新增，并改为优先沿用调用方 state。
func (p *Plugin) ensureLoginState(state string) string {
	now := time.Now()
	p.loginStateMu.Lock()
	for k, v := range p.loginStates {
		if v == nil || now.Sub(v.CreatedAt) > 15*time.Minute {
			delete(p.loginStates, k)
		}
	}
	if st := p.loginStates[state]; state != "" && st != nil {
		st.CreatedAt = now
		p.loginStateMu.Unlock()
		return state
	}
	p.loginStateMu.Unlock()

	initial := make(map[string]struct{})
	if rt, err := p.Runtime(); err == nil {
		for _, acc := range rt.Pool.Status().Accounts {
			initial[strings.ToLower(strings.TrimSpace(acc.AccountID))] = struct{}{}
		}
	}

	if strings.TrimSpace(state) == "" {
		state = uuid.NewString()
	}
	p.loginStateMu.Lock()
	p.loginStates[state] = &loginSessionState{
		CreatedAt:       now,
		InitialAccounts: initial,
		Provider:        ProviderID,
	}
	p.loginStateMu.Unlock()
	return state
}

// hasLoginState 判断会话是否存在且未过期。
// [参数] state 是登录会话标识。
// [返回] true 表示存在且未超过 15 分钟有效期。
// 最近修改时间：2026-11-21；改动原因：新增。
func (p *Plugin) hasLoginState(state string) bool {
	p.loginStateMu.Lock()
	defer p.loginStateMu.Unlock()
	st := p.loginStates[state]
	return st != nil && time.Since(st.CreatedAt) <= 15*time.Minute
}

// markLoginPending 把会话标记为导入中并清掉旧结果。
// [参数] state 是登录会话标识。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增。
func (p *Plugin) markLoginPending(state string) {
	p.loginStateMu.Lock()
	defer p.loginStateMu.Unlock()
	if st := p.loginStates[state]; st != nil {
		st.Importing = true
		st.Done = false
		st.AccountID = ""
		st.Email = ""
		st.ErrorMessage = ""
	}
}

// finishLogin 写入导入结果并结束导入中状态。
// [参数] state 是登录会话标识；accountID/email 是导入成功的账号；errMessage 非空表示失败原因。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增。
func (p *Plugin) finishLogin(state, accountID, email, errMessage string) {
	p.loginStateMu.Lock()
	defer p.loginStateMu.Unlock()
	if st := p.loginStates[state]; st != nil {
		st.Importing = false
		st.Done = true
		st.AccountID = accountID
		st.Email = email
		st.ErrorMessage = errMessage
	}
}

// publicOriginFromRequest 从请求头还原浏览器可访问的外部 origin。
//
// 资源路由不走管理鉴权，也没有结构化 BaseURL，只能靠 Host 与
// X-Forwarded-Proto 反推；两者都缺时回落到本机默认端口。
// [参数] req 是宿主转来的管理请求。
// [返回] 形如 https://cpa.example.com 的 origin，无尾斜杠。
// 最近修改时间：2026-11-21；改动原因：新增。
func publicOriginFromRequest(req rpcManagementRequest) string {
	host := strings.TrimSpace(req.Headers.Get("Host"))
	if host == "" {
		return resolveHostOrigin("")
	}
	scheme := strings.ToLower(strings.TrimSpace(req.Headers.Get("X-Forwarded-Proto")))
	if idx := strings.IndexByte(scheme, ','); idx >= 0 {
		scheme = strings.TrimSpace(scheme[:idx])
	}
	if scheme != "http" && scheme != "https" {
		scheme = "https"
		if strings.HasPrefix(host, "127.0.0.1") || strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "0.0.0.0") {
			scheme = "http"
		}
	}
	return scheme + "://" + host
}

// loginExtensionResponse 输出授权助手扩展的 zip 下载响应。
// [参数] 无（扩展内容由 buildExtensionZip 打包）。
// [返回] application/zip 管理响应；打包失败时返回 500 JSON。
// 最近修改时间：2026-11-21；改动原因：新增。
func loginExtensionResponse() pluginapi.ManagementResponse {
	payload, err := buildExtensionZip()
	if err != nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":        []string{"application/zip"},
			"Content-Disposition": []string{`attachment; filename="gemini-web2api-auth-helper.zip"`},
			"Cache-Control":       []string{"no-store"},
		},
		Body: payload,
	}
}

// htmlEscape 转义 HTML 文本与属性值。
// [参数] text 是待转义的原始文本。
// [返回] 把 & < > " ' 替换为实体后的安全文本。
// 最近修改时间：2026-11-21；改动原因：新增，用于把 Host 等外部输入拼进桥接页。
func htmlEscape(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return replacer.Replace(text)
}
