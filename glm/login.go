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

// callbackPath 是书签回传令牌的接收路径（resource 路由，免管理密钥）。
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
// 最近修改时间：2026-11-28 14:45:00；改动原因：新增书签授权会话。
func startLoginSession() string {
	state := fmt.Sprintf("glm-%d", time.Now().UnixNano())
	loginSessions.Store(state, &loginSession{State: state, CreatedAt: time.Now()})
	return state
}

// getLoginSession 读取登录会话。
// [参数] state: 会话标识。
// [返回] 会话指针；不存在返回 nil。
// 最近修改时间：2026-11-28 14:45:00；改动原因：新增书签授权会话读取。
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

// handleStartLogin 发起登录：返回桥接页地址，用户在 chatglm 页面点书签即可自动回调。
// [参数] raw: AuthLoginStartRequest JSON。
// [返回] 登录启动响应、错误。
// 最近修改时间：2026-11-28 14:45:00；改动原因：改为书签自动回调授权。
func handleStartLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginStartRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	state := startLoginSession()
	bridgePath := "/v0/resource/plugins/" + providerName + "/login?state=" + url.QueryEscape(state)
	// 宿主弹窗展示的就是这个 URL：必须指向本插件的书签引导页，不能是上游官网。
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
			"mode":       "bookmark",
			"prompt":     "在打开的引导页安装一次授权书签；登录 chatglm.cn 后点一下书签，页面会自动回调并完成授权。",
		},
	})
}

// handlePollLogin 轮询登录结果，回调页落盘后返回成功。
// [参数] raw: AuthLoginPollRequest JSON。
// [返回] 登录轮询响应、错误。
// 最近修改时间：2026-11-28 14:45:00；改动原因：改为轮询书签回调结果。
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
			Message: "等待在 chatglm.cn 页面点击授权书签",
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

// handleLoginCallback 接收书签回传的 refresh_token 并落盘。
// [参数] req: 管理请求（含 Headers 与 Query）。
// [返回] 回调响应。
// 最近修改时间：2026-11-28 14:45:00；改动原因：新增书签令牌接收与落盘。
func handleLoginCallback(req pluginapi.ManagementRequest) ([]byte, error) {
	refreshToken := strings.TrimSpace(req.Headers.Get(callbackTokenHeader))
	upstreamErr := strings.TrimSpace(req.Headers.Get(callbackErrorHeader))
	state := ""
	if vals := req.Query["state"]; len(vals) > 0 {
		state = strings.TrimSpace(vals[0])
	}
	// 书签用 location.href 跳转，令牌经 query 回传；页面内 fetch 用 header。
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
		return html("未获取到令牌", "书签没有回传 chatglm_refresh_token。请确认已登录 chatglm.cn 后重新点击书签。", false), nil
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

// cpaOriginPlaceholder 是书签脚本里等待替换成插件所在域 origin 的占位符。
// 书签要在 chatglm.cn 页面执行，回调必须指向 CPA 域；引导页与 CPA 同源，
// 因此在引导页 JS 里用 location.origin 替换后再写进书签 href。
const cpaOriginPlaceholder = "__CPA_ORIGIN__"

// buildBookmarkletScript 生成可在 chatglm.cn 页面一键取令牌的书签脚本。
// 读取 localStorage 的 chatglm_refresh_token（Cookie 兜底），跳转插件回调页完成落盘。
// [参数] state: 登录会话标识；空串表示长期可复用的固定书签。
// [返回] javascript: 开头的单行脚本（含待替换的 origin 占位符）。
// 最近修改时间：2026-11-28 18:55:00；改动原因：恢复书签一键授权。
func buildBookmarkletScript(state string) string {
	callback := "/v0/resource/plugins/" + providerName + callbackPath
	var b strings.Builder
	b.WriteString("javascript:(function(){")
	b.WriteString("var S=" + jsStringLiteral(state) + ";")
	b.WriteString("var CB='" + cpaOriginPlaceholder + callback + "'+(S?('?state='+encodeURIComponent(S)):'?');")
	b.WriteString("var t='';")
	b.WriteString("try{var v=localStorage.getItem('chatglm_refresh_token');")
	b.WriteString("if(v){try{var o=JSON.parse(v);t=(o&&o.value)?o.value:v}catch(e){t=v}}}catch(e){}")
	b.WriteString("if(!t){try{var m=document.cookie.match(/(?:^|;\\s*)chatglm_refresh_token=([^;]*)/);t=m?decodeURIComponent(m[1]):''}catch(e){}}")
	b.WriteString("if(!t){location.href=CB+'&err='+encodeURIComponent('未找到 chatglm_refresh_token，请先登录');return}")
	b.WriteString("location.href=CB+'&token='+encodeURIComponent(t);")
	b.WriteString("})();")
	return b.String()
}

// jsStringLiteral 把字符串安全编码为 JS 字面量。
// [参数] s: 原始字符串。
// [返回] 带引号的 JS 字面量。
// 最近修改时间：2026-11-28 14:45:00；改动原因：脚本注入安全编码。
func jsStringLiteral(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// htmlEscapeAttr 转义 HTML 属性值。
// [参数] s: 原始字符串。
// [返回] 转义后字符串。
// 最近修改时间：2026-11-28 18:55:00；改动原因：书签 href 安全注入。
func htmlEscapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// handleLoginBridge 返回授权引导页（宿主登录弹窗的落地页）。
// 主入口是书签一键授权：装一次书签，之后在 chatglm.cn 页面点一下即自动回调完成授权。
// 保留手工粘贴 chatglm_refresh_token 作为兜底（书签被浏览器策略拦截时用）。
// [参数] req: 管理请求（含 Query state）。
// [返回] 引导页响应。
// 最近修改时间：2026-11-28 18:55:00；改动原因：恢复书签一键授权为主入口。
func handleLoginBridge(req pluginapi.ManagementRequest) ([]byte, error) {
	state := ""
	if vals := req.Query["state"]; len(vals) > 0 {
		state = strings.TrimSpace(vals[0])
	}
	bookmark := buildBookmarkletScript(state)

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><title>智谱清言授权</title>
<style>body{font:15px/1.8 -apple-system,"Segoe UI","PingFang SC",sans-serif;max-width:680px;margin:52px auto;padding:0 24px;color:#2d2a26}
h1{font-size:21px;margin:0 0 6px}h2{font-size:15px;margin:26px 0 8px}p{color:#6d6760;margin:6px 0}
label{display:block;font-size:12px;color:#6d6760;margin:18px 0 6px}
textarea{width:100%;min-height:80px;background:#fff;color:#2d2a26;border:1px solid #e3e1db;border-radius:8px;padding:9px 11px;font:13px/1.5 ui-monospace,monospace;resize:vertical}
button{background:#4c8dff;color:#fff;border:0;border-radius:8px;padding:9px 18px;cursor:pointer;font-size:13px;margin-top:12px}
button:disabled{opacity:.5}.msg{margin-top:12px;font-size:13px}.ok{color:#10b981}.err{color:#c65746}
code{background:#f0eee8;padding:1px 5px;border-radius:4px;font-size:13px}
.bm{display:inline-block;background:#4c8dff;color:#fff;text-decoration:none;padding:10px 20px;border-radius:9px;font-weight:600;cursor:grab}
.note{background:#f5f4f0;border-radius:9px;padding:12px 14px;font-size:13px;color:#6d6760;margin-top:18px;line-height:1.8}
ol{color:#6d6760;font-size:13px;line-height:1.9;padding-left:20px}</style></head><body>`)
	b.WriteString(`<h1>智谱清言授权</h1><p>本插件使用 <code>chatglm_refresh_token</code> 换取 access_token 调用上游，全程 token 鉴权。</p>`)
	b.WriteString(`<h2>① 安装授权书签（只做一次）</h2><p>把下面按钮拖到浏览器书签栏（<b>不要直接点击</b>）：</p>`)
	b.WriteString(`<p><a class="bm" id="bmLink" href="` + htmlEscapeAttr(bookmark) + `">智谱清言一键授权</a></p>`)
	b.WriteString(`<h2>② 登录 chatglm.cn</h2><p>在同一个浏览器里打开并登录 <a href="https://chatglm.cn/" target="_blank" rel="noopener">chatglm.cn</a>。</p>`)
	b.WriteString(`<h2>③ 点一下书签</h2><p>回到 chatglm.cn 标签页，点书签栏里的「智谱清言一键授权」，页面会自动跳回这里并完成授权。</p>`)
	b.WriteString(`<div class="note">书签只做一件事：在 chatglm.cn 页面内读取 <code>chatglm_refresh_token</code> 并交给本插件，不读取其它数据，不含任何密钥，可长期保留。</div>`)
	b.WriteString(`<h2>书签被拦截？手工粘贴</h2><p>F12 → Application → Local Storage → 复制 <code>chatglm_refresh_token</code>，粘贴到下方提交。</p>`)
	b.WriteString(`<label for="tok">chatglm_refresh_token</label>`)
	b.WriteString(`<textarea id="tok" placeholder="粘贴 chatglm_refresh_token"></textarea>`)
	b.WriteString(`<button id="submitBtn">提交授权</button><div id="msg" class="msg"></div>`)
	b.WriteString(`<script>
var STATE=__STATE__;
var CALLBACK=location.origin+"/v0/resource/plugins/` + providerName + callbackPath + `"+(STATE?("?state="+encodeURIComponent(STATE)):"");
(function(){
  var el=document.getElementById("bmLink");
  if(el){el.href=el.getAttribute("href").split("` + cpaOriginPlaceholder + `").join(location.origin);}
})();
document.getElementById("submitBtn").addEventListener("click",function(){
  var btn=this,msg=document.getElementById("msg");
  var tok=(document.getElementById("tok").value||"").trim();
  if(!tok){msg.className="msg err";msg.textContent="请先粘贴 chatglm_refresh_token";return}
  btn.disabled=true;msg.className="msg";msg.textContent="校验中…";
  var h={};h["` + callbackTokenHeader + `"]=tok;
  fetch(CALLBACK,{method:"POST",headers:h,credentials:"omit"})
    .then(function(r){return r.text()})
    .then(function(t){document.open();document.write(t);document.close()})
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
