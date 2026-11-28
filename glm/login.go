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

// loginStateTTL 是登录会话有效期。
const loginStateTTL = 15 * time.Minute

// callbackPath 是令牌提交的接收路径（resource 路由，免管理密钥）。
const callbackPath = "/login/callback"

// callbackTokenHeader 是回传 chatglm_refresh_token 的请求头。
const callbackTokenHeader = "X-GLM-RefreshToken"

// callbackErrorHeader 是回传失败原因的请求头。
const callbackErrorHeader = "X-GLM-Error"

// loginSession 是一次待完成的登录会话。
type loginSession struct {
	State     string
	CreatedAt time.Time
	Completed bool
	AuthID    string
	Error     string
}

var loginSessions sync.Map

// publicOrigin 缓存最近一次请求观测到的外部访问 origin。
var (
	publicOriginMu sync.RWMutex
	publicOrigin   string
)

// rememberPublicOrigin 从请求头记录外部 origin。
// [参数] h: 请求头。
// [返回] 无。
// 最近修改时间：2026-11-28 16:50:00；改动原因：新增，授权入口需拼绝对地址。
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
	if publicOrigin == "" || !isLoopbackHost(host) {
		publicOrigin = scheme + "://" + host
	}
	publicOriginMu.Unlock()
}

// currentPublicOrigin 返回最近记录的 origin，未观测到时返回空串。
// [参数] 无。
// [返回] 形如 https://cpa.example.com 的 origin。
// 最近修改时间：2026-11-28 16:50:00；改动原因：新增，绝对 URL 生成。
func currentPublicOrigin() string {
	publicOriginMu.RLock()
	defer publicOriginMu.RUnlock()
	return publicOrigin
}

// firstHeaderValue 取多级代理头里的第一个值。
// [参数] raw: 原始头值。
// [返回] 去空白后的首段。
// 最近修改时间：2026-11-28 16:50:00；改动原因：新增，兼容逗号分隔转发头。
func firstHeaderValue(raw string) string {
	if raw == "" {
		return ""
	}
	if idx := strings.Index(raw, ","); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw)
}

// isLoopbackHost 判断主机名是否为回环 / 通配地址。
// [参数] host: 可能带端口的 Host 值。
// [返回] 属于回环地址时为 true。
// 最近修改时间：2026-11-28 16:50:00；改动原因：新增，防止容器内自测污染外部 origin。
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

// startLoginSession 创建登录会话。
// [参数] 无。
// [返回] 会话 state。
// 最近修改时间：2026-11-28 16:55:00；改动原因：token 授权会话。
func startLoginSession() string {
	state := fmt.Sprintf("glm-%d", time.Now().UnixNano())
	loginSessions.Store(state, &loginSession{State: state, CreatedAt: time.Now()})
	return state
}

// getLoginSession 读取登录会话。
// [参数] state: 会话标识。
// [返回] 会话指针；不存在返回 nil。
// 最近修改时间：2026-11-28 16:55:00；改动原因：token 授权会话读取。
func getLoginSession(state string) *loginSession {
	raw, ok := loginSessions.Load(strings.TrimSpace(state))
	if !ok {
		return nil
	}
	sess, ok := raw.(*loginSession)
	if !ok {
		return nil
	}
	return sess
}

// handleStartLogin 发起登录：返回 token 录入页地址，用户粘贴 chatglm_refresh_token 即完成授权。
// [参数] raw: AuthLoginStartRequest JSON。
// [返回] 登录启动响应、错误。
// 最近修改时间：2026-11-28 16:55:00；改动原因：改为 token 录入授权。
func handleStartLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginStartRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	state := startLoginSession()
	bridgePath := "/v0/resource/plugins/" + providerName + "/login?state=" + url.QueryEscape(state)
	// 宿主弹窗展示的就是这个 URL：必须指向本插件的 token 录入页，不能是上游官网。
	absoluteURL := bridgePath
	if origin := currentPublicOrigin(); origin != "" {
		absoluteURL = origin + bridgePath
	}
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       absoluteURL,
		State:     state,
		ExpiresAt: time.Now().Add(loginStateTTL).UTC(),
		Metadata: map[string]any{
			"logo":       pluginLogoURL,
			"bridge_url": absoluteURL,
			"mode":       "token",
			"prompt":     "在打开的授权页按指引从 chatglm.cn 复制 chatglm_refresh_token 并粘贴提交。",
		},
	})
}

// handlePollLogin 轮询登录结果，回调页落盘后返回成功。
// [参数] raw: AuthLoginPollRequest JSON。
// [返回] 登录轮询响应、错误。
// 最近修改时间：2026-11-28 16:55:00；改动原因：轮询 token 录入结果。
func handlePollLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("poll: %w", err)
	}
	state := strings.TrimSpace(req.State)
	if state == "" {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "缺少登录会话标识",
		})
	}
	sess := getLoginSession(state)
	if sess == nil {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "登录会话不存在或已过期，请重新发起",
		})
	}
	if time.Since(sess.CreatedAt) > loginStateTTL {
		loginSessions.Delete(state)
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "登录会话已超时（15 分钟），请重新发起",
		})
	}
	if !sess.Completed {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "请在授权页粘贴 chatglm_refresh_token 并提交",
		})
	}

	loginSessions.Delete(state)
	if sess.Error != "" {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: sess.Error,
		})
	}
	sa := findAuthByID(sess.AuthID)
	if sa == nil {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "账号已写入但读取失败，请在面板刷新列表",
		})
	}
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: "授权成功",
		Auth: pluginapi.AuthData{
			Provider:    providerName,
			ID:          sa.AuthID,
			FileName:    sa.AuthID + ".json",
			Label:       sa.Label,
			StorageJSON: mustMarshal(sa),
		},
	})
}

// handleLoginCallback 接收授权页提交的 refresh_token 并落盘。
// [参数] req: 管理请求（含 Headers 与 Query）。
// [返回] 回调响应。
// 最近修改时间：2026-11-28 16:55:00；改动原因：新增 token 接收与落盘。
func handleLoginCallback(req pluginapi.ManagementRequest) ([]byte, error) {
	refreshToken := strings.TrimSpace(req.Headers.Get(callbackTokenHeader))
	upstreamErr := strings.TrimSpace(req.Headers.Get(callbackErrorHeader))
	state := ""
	if vals := req.Query["state"]; len(vals) > 0 {
		state = strings.TrimSpace(vals[0])
	}
	// 令牌经自定义请求头回传。
	if refreshToken == "" {
		if vals := req.Query["token"]; len(vals) > 0 {
			refreshToken = strings.TrimSpace(vals[0])
		}
	}
	if upstreamErr == "" {
		if vals := req.Query["err"]; len(vals) > 0 {
			upstreamErr = strings.TrimSpace(vals[0])
		}
	}

	html := func(title, detail string, ok bool) []byte {
		color := "#c65746"
		if ok {
			color = "#10b981"
		}
		body := fmt.Sprintf(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><title>%s</title>
<style>body{font:15px/1.7 -apple-system,"Segoe UI","PingFang SC",sans-serif;max-width:560px;margin:80px auto;padding:0 24px;color:#2d2a26}
h1{font-size:20px;color:%s}p{color:#6d6760}</style></head><body><h1>%s</h1><p>%s</p></body></html>`, title, color, title, detail)
		resp := pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  []string{"text/html; charset=utf-8"},
				"Cache-Control": []string{"no-cache, no-store, must-revalidate"},
			},
			Body: []byte(body),
		}
		data, _ := okEnvelope(resp)
		return data
	}

	if upstreamErr != "" {
		if sess := getLoginSession(state); sess != nil {
			sess.Completed = true
			sess.Error = upstreamErr
		}
		return html("授权失败", "原因："+upstreamErr+"。请回到面板重新发起。", false), nil
	}
	if refreshToken == "" {
		return html("未获取到令牌", "没有收到 chatglm_refresh_token。请确认已登录 chatglm.cn 并复制正确的令牌。", false), nil
	}

	sa, err := saveImportedToken(refreshToken, "")
	if err != nil {
		if sess := getLoginSession(state); sess != nil {
			sess.Completed = true
			sess.Error = err.Error()
		}
		return html("令牌校验失败", err.Error(), false), nil
	}
	if sess := getLoginSession(state); sess != nil {
		sess.Completed = true
		sess.AuthID = sa.AuthID
	}
	return html("授权成功", "账号 "+sa.Label+" 已导入，可以关闭本页回到面板。", true), nil
}

// jsStringLiteral 把字符串安全编码为 JS 字面量。
// [参数] s: 原始字符串。
// [返回] 带引号的 JS 字面量。
// 最近修改时间：2026-11-28 16:50:00；改动原因：引导页 state 安全注入。
func jsStringLiteral(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// handleLoginBridge 返回授权引导页（宿主登录弹窗的落地页）。
// chatglm_refresh_token 是 HttpOnly Cookie，网页脚本读不到，因此由用户从开发者工具复制后粘贴提交。
// [参数] req: 管理请求（含 Query state）。
// [返回] 引导页响应。
// 最近修改时间：2026-11-28 16:50:00；改动原因：改为手工复制令牌提交，移除不可行的书签方案。
func handleLoginBridge(req pluginapi.ManagementRequest) ([]byte, error) {
	state := ""
	if vals := req.Query["state"]; len(vals) > 0 {
		state = strings.TrimSpace(vals[0])
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><title>智谱清言授权</title>
<style>body{font:15px/1.8 -apple-system,"Segoe UI","PingFang SC",sans-serif;max-width:680px;margin:52px auto;padding:0 24px;color:#2d2a26}
h1{font-size:21px;margin:0 0 6px}h2{font-size:15px;margin:26px 0 8px}p{color:#6d6760;margin:6px 0}
label{display:block;font-size:12px;color:#6d6760;margin:18px 0 6px}
textarea{width:100%;min-height:80px;background:#fff;color:#2d2a26;border:1px solid #e3e1db;border-radius:8px;padding:9px 11px;font:13px/1.5 ui-monospace,monospace;resize:vertical}
button{background:#4c8dff;color:#fff;border:0;border-radius:8px;padding:9px 18px;cursor:pointer;font-size:13px;margin-top:12px}
button:disabled{opacity:.5}.msg{margin-top:12px;font-size:13px}.ok{color:#10b981}.err{color:#c65746}
code{background:#f0eee8;padding:1px 5px;border-radius:4px;font-size:13px}

.note{background:#f5f4f0;border-radius:9px;padding:12px 14px;font-size:13px;color:#6d6760;margin-top:18px;line-height:1.8}
ol{color:#6d6760;font-size:13px;line-height:1.9;padding-left:20px}</style></head><body>`)
	b.WriteString(`<h1>智谱清言授权</h1><p>本插件使用 <code>chatglm_refresh_token</code> 换取 access_token 调用上游，全程 token 鉴权。</p>`)
	b.WriteString(`<div class="note"><b>为什么需要手工复制：</b>智谱清言把 <code>chatglm_refresh_token</code> 存在 <b>HttpOnly Cookie</b> 里，网页脚本（含书签、扩展）读不到它，因此只能从开发者工具复制一次。插件拿到它之后会自行换取 access_token 调用上游，全程 token 鉴权，无需再手工刷新。</div>`)
	b.WriteString(`<h2>① 登录 chatglm.cn</h2><p>在同一个浏览器里打开并登录 <a href="https://chatglm.cn/" target="_blank" rel="noopener">chatglm.cn</a>，随便发一条消息确保已登录。</p>`)
	b.WriteString(`<h2>② 复制 chatglm_refresh_token</h2><p>按 <b>F12</b> 打开开发者工具 → <b>Application</b>（应用）→ <b>Cookies</b> → <code>https://chatglm.cn</code> → 找到 <code>chatglm_refresh_token</code>，复制它的 Value。</p>`)
	b.WriteString(`<label for="tok">chatglm_refresh_token</label>`)
	b.WriteString(`<textarea id="tok" placeholder="粘贴 chatglm_refresh_token"></textarea>`)
	b.WriteString(`<button id="submitBtn">提交授权</button><div id="msg" class="msg"></div>`)
	b.WriteString(`<div class="note">提交后插件会立即校验：用它换取 access_token，成功即写入账号并完成授权，无需再点其它按钮。</div>`)
	b.WriteString(`<script>
var STATE=__STATE__;
var CALLBACK=location.origin+"/v0/resource/plugins/` + providerName + callbackPath + `"+(STATE?("?state="+encodeURIComponent(STATE)):"");
document.getElementById("submitBtn").addEventListener("click",function(){
  var btn=this,msg=document.getElementById("msg");
  var tok=(document.getElementById("tok").value||"").trim();
  if(!tok){msg.className="msg err";msg.textContent="请先粘贴 chatglm_refresh_token";return}
  btn.disabled=true;msg.className="msg";msg.textContent="校验中…";
  var h={};h["` + callbackTokenHeader + `"]=tok;
  fetch(CALLBACK,{method:"GET",headers:h,credentials:"omit",cache:"no-store"})
    .then(function(r){return r.text().then(function(t){return {ok:r.ok,status:r.status,body:t}})})
    .then(function(res){
      if(!res.ok||!res.body){btn.disabled=false;msg.className="msg err";msg.textContent="回调失败 HTTP "+res.status;return}
      document.open();document.write(res.body);document.close();
    })
    .catch(function(e){btn.disabled=false;msg.className="msg err";msg.textContent="提交失败: "+e});
});
</script>`)
	b.WriteString(`</body></html>`)

	page := strings.ReplaceAll(b.String(), "__STATE__", jsStringLiteral(state))
	resp := pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-cache, no-store, must-revalidate"},
		},
		Body: []byte(page),
	}
	data, _ := okEnvelope(resp)
	return data, nil
}
