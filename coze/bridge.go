package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件渲染授权引导页，并处理脚本回传的令牌。
//
// 页面承担三件事：解释为什么需要手动一步、给出可直接复制的一次性脚本、
// 接收脚本跳转回来的令牌。提交走 resource 路由（免管理密钥）——
// 这是浏览器顶层打开的普通页面，拿不到也不该要求用户填管理密钥
// （与 copilot / doubao 桥接页同一做法）。

// handleLoginBridge 渲染授权引导页。
//
// 无 state 时是「面板独立入口」：页面提供一条与 state 无关的固定书签，
// 用户拖一次即可长期复用，落盘后不通知宿主轮询（回面板刷新即可看到账号）。
// 有 state 时是宿主「添加账号」弹窗入口：除固定书签外，另给一条带 state 的
// 一次性脚本，保证宿主弹窗能拿到结果并自动关闭。
// [参数] state: 授权会话标识（可为空）。
// [返回] 管理响应（HTML）。
// 最近修改时间：2026-11-01；改动原因：新增无 state 的一键授权入口。
func handleLoginBridge(state string) pluginapi.ManagementResponse {
	state = strings.TrimSpace(state)
	if state == "" {
		return loginBridgePage("", "")
	}
	if !validLoginState(state) {
		return loginBridgePage("", "授权会话不存在或已过期（15 分钟有效）。请回到管理页重新发起。")
	}
	return loginBridgePage(state, "")
}

// callbackTokenHeader 是回调页同源回传令牌时使用的请求头。
//
// 令牌不走 query：resource 路由的访问日志会记录完整 URL，令牌会因此落进
// 宿主日志。改走「fragment（浏览器不发给服务端）+ 同源请求头」后，
// 服务端日志里只剩 state。
const callbackTokenHeader = "X-Coze-Token"

// callbackErrorHeader 是回调页同源上报失败原因时使用的请求头。
const callbackErrorHeader = "X-Coze-Error"

// handleLoginCallback 处理脚本回传的令牌。
//
// 脚本在扣子页面取到 x-tt-token 后跳转到本路径，令牌放在 URL fragment 里。
// 首次打开只返回接收页（服务端看不到 fragment），由页面脚本把令牌经
// 同源请求头回传，第二次请求才真正换 Cookie + 落盘。
//
// 安全边界：state 一次性且 15 分钟过期，等于一次性的短时凭证；
// 令牌本身只能兑换一次，且立即消费，不会留在插件内存里。
// [参数] req: 宿主透传的请求（state 在 query，令牌在请求头）。
// [返回] 管理响应（HTML 接收页或 JSON 结果）。
// 最近修改时间：2026-10-18；改动原因：令牌改走 fragment + 请求头，避免落进访问日志。
func handleLoginCallback(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	state := strings.TrimSpace(req.Query.Get("state"))
	// 宽松模式：无 state 时仍允许导入。
	//
	// 面板独立入口与固定书签都不携带 state——书签要能长期复用，不能把一次性的
	// state 烤进书签内容。此时账号身份完全由令牌自身决定（saveFromToken 会用
	// account/info 反查 sec_user_id），不需要会话关联；代价是无法把结果回传
	// 给宿主弹窗，用户回到面板刷新即可看到新账号。
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
			"message": reason + "。请确认已在扣子页面完成登录后重试。",
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
// 最近修改时间：2026-10-18；改动原因：新增，fragment 接收页。
func loginCallbackShell(state, detail string) pluginapi.ManagementResponse {
	var b strings.Builder
	writeBridgeHead(&b, state, "扣子账号授权")
	if detail != "" {
		b.WriteString("<h1>无法继续</h1><div class=\"err\">" + htmlEscape(detail) + "</div>")
		b.WriteString("<p class=\"note\">请回到管理页重新发起授权。</p></div></body></html>")
		return htmlManagementResponse(b.String())
	}
	b.WriteString("<h1>正在完成授权</h1><div id=\"result\" class=\"note\">正在接收令牌…</div>")
	// 回调页通常是新标签页：给一个明确的回跳入口，避免用户停在空白结果页。
	b.WriteString("<a id=\"backPanel\" class=\"bm\" style=\"display:none;margin-top:16px\" href=\"/v0/resource/plugins/" + providerName + "/panel\">打开扣子面板</a>")
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

// loginBridgePage 渲染引导页 HTML。detail 非空时为错误页。
//
// 逐段 WriteString 拼接而非原生字符串：页面里需要写 JS，反引号会截断
// Go 的原始字符串字面量（与 copilot / doubao 桥接页同一处理）。
// [参数] state: 授权会话标识；detail: 错误说明。
// [返回] 管理响应（HTML）。
// 最近修改时间：2026-10-18；改动原因：新增，引导页渲染。
func loginBridgePage(state, detail string) pluginapi.ManagementResponse {
	var b strings.Builder
	writeBridgeHead(&b, state, "扣子账号授权")
	if detail != "" {
		b.WriteString("<h1>无法继续</h1><div class=\"err\">" + htmlEscape(detail) + "</div>")
		b.WriteString("</div></body></html>")
		return htmlManagementResponse(b.String())
	}
	writeBridgeBody(&b, state)
	b.WriteString("</div></body></html>")
	return htmlManagementResponse(b.String())
}

// writeBridgeHead 写出页面头部与样式表。
// [参数] b: 输出缓冲；state: 授权会话标识（写入 body 的 data 属性）；title: 页面标题。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，从渲染函数中拆出。
func writeBridgeHead(b *strings.Builder, state, title string) {
	b.WriteString("<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString("<title>" + htmlEscape(title) + "</title>")
	b.WriteString("<style>")
	b.WriteString("body{font-family:-apple-system,'Segoe UI','Microsoft YaHei',sans-serif;background:#f7f8fb;color:#22262e;margin:0;padding:32px 16px;display:flex;justify-content:center}")
	b.WriteString(".card{background:#fff;border:1px solid #e4e7ee;border-radius:14px;max-width:660px;width:100%;padding:26px 28px}")
	b.WriteString("h1{font-size:19px;margin:0 0 10px}p{line-height:1.7;margin:8px 0;font-size:14px;color:#5f6673}")
	b.WriteString("ol{padding-left:20px;color:#5f6673}ol li{margin:9px 0;font-size:14px;line-height:1.7}")
	b.WriteString("code{background:#f0f2f6;border-radius:4px;padding:2px 6px;font-size:13px}")
	b.WriteString("pre{background:#1e2430;color:#dfe5ef;border-radius:9px;padding:13px 15px;font:12px/1.6 ui-monospace,Consolas,monospace;overflow-x:auto;white-space:pre-wrap;word-break:break-all}")
	b.WriteString("button{margin-top:12px;background:#3b7cff;color:#fff;border:0;border-radius:8px;padding:9px 18px;font-size:14px;cursor:pointer}")
	b.WriteString("button:disabled{opacity:.55;cursor:default}")
	b.WriteString("a.bm{display:inline-block;background:#3b7cff;color:#fff;border-radius:8px;padding:9px 18px;font-size:14px;text-decoration:none;cursor:grab;user-select:none}")
	b.WriteString("details summary{outline:none}")
	b.WriteString(".err{background:#fdf1ef;border:1px solid #e8b9b1;border-radius:8px;padding:10px 14px;margin-top:14px;font-size:14px;color:#a8452f}")
	b.WriteString(".ok{background:#eaf8f2;border:1px solid #a8dcc7;border-radius:8px;padding:10px 14px;margin-top:14px;font-size:14px;color:#0d7a55}")
	b.WriteString(".note{background:#fdf7e6;border:1px solid #edd9a3;border-radius:8px;padding:10px 14px;margin-top:14px;font-size:13px;color:#8a6a10}")
	b.WriteString("</style></head><body data-provider=\"" + htmlEscape(providerName) + "\" data-state=\"" + htmlEscape(state) + "\"><div class=\"card\">")
}

// writeBridgeBody 写出引导文案与一次性脚本。
//
// 脚本内容由 writeAuthScript 生成并写入 <pre>，用户可整段复制到扣子页面的
// DevTools Console 执行，或存为书签。
// [参数] b: 输出缓冲；state: 授权会话标识。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，引导正文与脚本注入。
func writeBridgeBody(b *strings.Builder, state string) {
	bookmarklet := buildBookmarkletScript()
	script := buildAuthScript(state)
	hasState := strings.TrimSpace(state) != ""
	b.WriteString("<h1>授权扣子账号</h1>")
	b.WriteString("<p>扣子没有开放给第三方的一键授权：登录令牌只发给桌面端的 <code>coze-desktop://</code> 协议，插件无法直接接收。下面的书签把「在扣子页面取一次令牌」压缩成一次点击，安装一次即可长期复用。</p>")
	b.WriteString("<p style=\"margin-top:16px\"><b>① 安装授权书签（只做一次）</b><br>把下面的按钮拖到浏览器书签栏（不要直接点击）：</p>")
	b.WriteString("<p><a id=\"bmLink\" class=\"bm\" href=\"#\" draggable=\"true\">扣子一键授权</a></p>")
	b.WriteString("<p style=\"margin-top:14px\"><b>② 打开扣子并登录</b></p>")
	b.WriteString("<button id=\"openCoze\" type=\"button\">打开扣子并登录</button>")
	b.WriteString("<p style=\"margin-top:14px\"><b>③ 在扣子页面点一下书签</b><br>登录完成后，回到扣子标签页，点书签栏里的「扣子一键授权」；页面会自动跳回这里并完成授权。</p>")
	if !hasState {
		b.WriteString("<p class=\"note\" style=\"margin-top:16px\">这是面板独立入口：不依赖宿主弹窗，账号落盘后回到面板点「刷新账号」即可看到。新增账号或更换登录态时，重复上面三步即可。</p>")
	}
	// 兜底通道始终保留：少数环境会用 CSP 拦掉 javascript: 书签，
	// 此时把同一段逻辑粘进 Console 仍可完成授权。
	b.WriteString("<details style=\"margin-top:16px\"><summary style=\"cursor:pointer;color:#3b7cff;font-size:14px\">方式二：一次性脚本（书签被浏览器策略拦截时用）</summary>")
	b.WriteString("<p style=\"margin-top:10px\">复制到扣子页面的 Console 执行：</p>")
	b.WriteString("<pre id=\"authScript\">" + htmlEscape(script) + "</pre>")
	b.WriteString("<button id=\"copyScript\" type=\"button\">复制脚本</button>")
	b.WriteString("<div id=\"copyHint\"></div>")
	b.WriteString("</details>")
	b.WriteString("<p style=\"margin-top:16px\"><b>换浏览器或发给别人？</b><br>本页完整地址可直接复制（含授权参数，15 分钟内有效）：</p>")
	b.WriteString("<pre id=\"bridgeURL\"></pre>")
	b.WriteString("<button id=\"copyURL\" type=\"button\">复制完整地址</button>")
	b.WriteString("<div id=\"copyURLHint\"></div>")
	b.WriteString("<p class=\"note\">书签只做一件事：在扣子页面内调用令牌接口取 <code>x-tt-token</code>，然后把令牌交给本插件。它不读取、也不上传你的 Cookie，内容不含任何密钥，可以长期保留。</p>")
	b.WriteString("<script>")
	b.WriteString("var STATE=document.body.getAttribute(\"data-state\")||\"\";")
	// 书签在扣子页面执行，那里的 location.origin 是 coze.cn；回调地址必须指向
	// 本插件所在域。因此占位符在桥接页（即 CPA 域）加载时替换为当前 origin。
	b.WriteString("var BM=" + jsLiteral(bookmarklet) + ";")
	b.WriteString("var scriptEl=document.getElementById(\"authScript\");")
	b.WriteString("if(scriptEl){scriptEl.textContent=scriptEl.textContent.split(\"__CPA_ORIGIN__\").join(location.origin)}")
	b.WriteString("var bmLink=document.getElementById(\"bmLink\");")
	b.WriteString("if(bmLink){")
	b.WriteString("  bmLink.href=BM.split(\"__CPA_ORIGIN__\").join(location.origin);")
	b.WriteString("  bmLink.addEventListener(\"click\",function(e){e.preventDefault();alert(\"请把这个按钮拖到浏览器书签栏（不要直接点击），然后在扣子页面点击该书签。\")})")
	b.WriteString("}")
	// 完整地址：宿主弹窗里的入口有时只显示相对路径，这里给出可复制的绝对地址。
	b.WriteString("var bridgeEl=document.getElementById(\"bridgeURL\");")
	b.WriteString("if(bridgeEl){bridgeEl.textContent=location.href}")
	b.WriteString("var copyURLBtn=document.getElementById(\"copyURL\");")
	b.WriteString("if(copyURLBtn){copyURLBtn.addEventListener(\"click\",function(){")
	b.WriteString("  var hint=document.getElementById(\"copyURLHint\");")
	b.WriteString("  var done=function(){hint.className=\"ok\";hint.textContent=\"已复制完整地址。\"};")
	b.WriteString("  var fail=function(){hint.className=\"err\";hint.textContent=\"自动复制失败，请手动选中上面的地址复制。\"};")
	b.WriteString("  if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(location.href).then(done).catch(fail)}else{fail()}")
	b.WriteString("})}")
	b.WriteString("var openBtn=document.getElementById(\"openCoze\");")
	b.WriteString("if(openBtn){openBtn.addEventListener(\"click\",function(){window.open(\"https://www.coze.cn\",\"_blank\",\"noopener\")})}")
	b.WriteString("var copyBtn=document.getElementById(\"copyScript\");")
	b.WriteString("if(copyBtn){copyBtn.addEventListener(\"click\",function(){")
	b.WriteString("  var text=document.getElementById(\"authScript\").textContent||\"\";")
	b.WriteString("  var hint=document.getElementById(\"copyHint\");")
	b.WriteString("  var done=function(){hint.className=\"ok\";hint.textContent=\"已复制，请到扣子页面的 Console 粘贴执行。\"};")
	b.WriteString("  var fail=function(){hint.className=\"err\";hint.textContent=\"自动复制失败，请手动选中脚本文本复制。\"};")
	b.WriteString("  if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(text).then(done).catch(fail)}else{fail()}")
	b.WriteString("})}")
	b.WriteString("</script>")
}

// cpaOriginPlaceholder 是脚本里等待替换成插件所在域 origin 的占位符。
//
// 不能直接在服务端拼绝对地址：插件运行在容器内，看不到用户实际访问的域名
// （反向代理 / 端口映射都会改写）。桥接页在浏览器里加载，location.origin
// 就是用户真实可用的地址，替换后才不会把令牌发到 coze.cn。
const cpaOriginPlaceholder = "__CPA_ORIGIN__"

// publicOrigin 缓存最近一次请求观测到的外部访问 origin。
//
// 插件跑在容器里，永远不知道用户实际用的域名（反向代理、端口映射都会改写）；
// 但每个管理 / 资源请求都带着 Host 与 X-Forwarded-* 头，取一次就能拼出
// 用户可复制的绝对地址，供宿主登录弹窗里的入口 URL 使用。
var (
	publicOriginMu sync.RWMutex
	publicOrigin   string
)

// rememberPublicOrigin 从请求头记录外部 origin（取不到 Host 时保持原值）。
// [参数] h: 请求头。
// [返回] 无。
// 最近修改时间：2026-11-01；改动原因：新增，为授权入口拼绝对 URL。
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
	publicOrigin = scheme + "://" + host
	publicOriginMu.Unlock()
}

// currentPublicOrigin 返回最近记录的 origin，未观测到时返回空串。
// [参数] 无。
// [返回] 形如 https://cpa.example.com 的 origin。
// 最近修改时间：2026-11-01；改动原因：新增，绝对 URL 生成。
func currentPublicOrigin() string {
	publicOriginMu.RLock()
	defer publicOriginMu.RUnlock()
	return publicOrigin
}

// firstHeaderValue 取多级代理头里的第一个值。
// [参数] raw: 原始头值。
// [返回] 去空白后的首段；空值时返回空串。
// 最近修改时间：2026-11-01；改动原因：新增，兼容逗号分隔的转发头。
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
// 脚本在扣子页面上下文执行，因此 fetch 天然带上登录 Cookie（credentials
// 必须显式设为 include：跨域或默认策略下不一定自动携带）。
// 取到 x-tt-token 后跳转插件回调页；失败时把上游 message 一并带回，
// 让回调页能显示可读原因而不是笼统的「失败」。
// [参数] state: 授权会话标识。
// [返回] 可粘贴执行的 JS 源码（含待替换的 origin 占位符）。
// 最近修改时间：2026-10-18；改动原因：新增，脚本生成；回调域改为占位符注入。
func buildAuthScript(state string) string {
	callback := "/v0/resource/plugins/" + providerName + callbackPath
	var b strings.Builder
	b.WriteString("(async()=>{\n")
	b.WriteString("  const r=await fetch('/passport/token/beat/v2/',{method:'POST',credentials:'include',")
	b.WriteString("headers:{'sdk-version':'2','Content-Type':'application/json'},body:'{}'});\n")
	b.WriteString("  const j=await r.json().catch(()=>({}));\n")
	b.WriteString("  const t=r.headers.get('x-tt-token');\n")
	b.WriteString("  const cb='")
	b.WriteString(cpaOriginPlaceholder)
	b.WriteString(callback)
	// 无 state 时不拼该参数：面板独立入口没有会话，拼上去会让回调页
	// 多一个空参数，读日志时也难分辨是哪种入口。
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
// 最近修改时间：2026-10-18；改动原因：新增，避免 state 破坏脚本字面量。
func jsStringEscape(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "'", "\\'", "\n", "\\n", "\r", "\\r", "<", "\\x3c")
	return r.Replace(s)
}

// buildBookmarkletScript 生成可长期复用的固定书签脚本。
//
// 与 buildAuthScript 的区别是内容不含 state：拖进书签栏后永久可用，不受
// 15 分钟会话限制，也不需要每次授权前重新复制——这是「一次安装、之后
// 一点即完成」的关键。代价是落盘结果无法回传给宿主弹窗（没有会话可登记），
// 因此只用于面板独立入口与固定书签场景。
//
// 脚本在扣子页面上下文执行，fetch 天然同源；取到 x-tt-token 后带着 fragment
// 跳到本插件回调页，由接收页转成同源请求头提交，令牌不会落进访问日志。
// [参数] 无。
// [返回] javascript: 开头的单行脚本（含待替换的 origin 占位符）。
// 最近修改时间：2026-11-01；改动原因：新增，一次性书签授权。
func buildBookmarkletScript() string {
	callback := "/v0/resource/plugins/" + providerName + callbackPath
	var b strings.Builder
	b.WriteString("javascript:(async()=>{try{")
	b.WriteString("const r=await fetch('/passport/token/beat/v2/',{method:'POST',credentials:'include',")
	b.WriteString("headers:{'sdk-version':'2','Content-Type':'application/json'},body:'{}'});")
	b.WriteString("const j=await r.json().catch(()=>({}));")
	b.WriteString("const t=r.headers.get('x-tt-token');")
	b.WriteString("if(!t){alert('未检测到扣子登录态：'+(((j&&j.data&&j.data.description)||(j&&j.message))||'请先在本页完成登录'));return}")
	b.WriteString("location.href='")
	b.WriteString(cpaOriginPlaceholder)
	b.WriteString(callback)
	b.WriteString("#token='+encodeURIComponent(t)")
	b.WriteString("}catch(e){alert('取令牌失败：'+e)}})()")
	return b.String()
}

// jsLiteral 把脚本编码成可安全嵌入 <script> 的 JSON 字符串字面量。
//
// 直接拼接会被脚本里的引号、反斜杠破坏；JSON 编码保证转义正确，
// 再把 "</" 换成 "<\/"，避免提前闭合 <script> 标签。
// [参数] s: 原始脚本文本。
// [返回] 带引号的 JSON 字面量。
// 最近修改时间：2026-11-01；改动原因：新增，书签脚本安全注入。
func jsLiteral(s string) string {
	raw, errMarshal := json.Marshal(s)
	if errMarshal != nil {
		return "\"\""
	}
	return strings.ReplaceAll(string(raw), "</", "<\\/")
}
