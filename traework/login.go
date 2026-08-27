// login.go implements the AuthProvider login surface for traework-provider.
//
// Trae Work SOLO has NO browser OAuth endpoint: credentials live only inside
// the local SOLO client (storage.json, tc-header encrypted) and are imported
// through the host's paste-credential flow (handleParseAuth). We therefore
// keep AuthLoginStart/Poll honest with the host contract:
//
//   - StartLogin returns a VALID non-empty state (v7.2.30 host rejects an
//     empty state with "invalid oauth state", auth_files.go ServePluginAuthURL)
//     plus a data: guide page, so the host flow completes instead of erroring.
//   - PollLogin always reports AuthLoginStatusError with the correct login
//     path, so the UI surfaces guidance instead of spinning forever.
package main

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// loginGuidePage renders as a data: URL when the host starts the OAuth flow
// for this plugin. It exists because Trae SOLO has no OAuth endpoints — the
// only supported way to add an account is pasting the storage.json credential.
const loginGuidePage = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>TraeWork Provider 登录说明</title>
<style>
body{font-family:-apple-system,"Segoe UI","Microsoft YaHei",sans-serif;background:#f5f6f8;color:#1f2328;margin:0;padding:40px 16px;display:flex;justify-content:center}
.card{background:#fff;border-radius:12px;box-shadow:0 2px 12px rgba(0,0,0,.08);max-width:560px;width:100%;padding:32px}
h1{font-size:20px;margin:0 0 12px}
p{line-height:1.7;margin:8px 0;font-size:14px}
ol{padding-left:20px}
ol li{margin:6px 0;font-size:14px;line-height:1.6}
code{background:#f0f1f3;border-radius:4px;padding:2px 6px;font-size:13px;word-break:break-all}
.note{background:#fff7e6;border:1px solid #ffd591;border-radius:8px;padding:10px 14px;margin-top:16px;font-size:13px;color:#874d00}
</style>
</head>
<body>
<div class="card">
<h1>TraeWork Provider 登录说明</h1>
<p>Trae Work SOLO 没有浏览器 OAuth 授权端点，本插件不提供 OAuth 登录。</p>
<p>请改用「添加账号 → 粘贴凭据」的方式导入账号：</p>
<ol>
<li>在 Trae SOLO 客户端中确认已登录（模型可正常对话）</li>
<li>打开客户端数据目录，找到 <code>storage.json</code>（含 <code>credential</code> 字段的认证文件）</li>
<li>复制该文件完整内容，粘贴到插件的「粘贴凭据」输入框并提交</li>
</ol>
<p>支持 <code>tc</code> 加密头与明文 JSON 两种形状，插件会自动解密并保存。</p>
<p class="note">本窗口无需任何操作，直接关闭即可。若宿主仍提示等待登录，请改用「添加账号 → 粘贴凭据」方式添加账号。</p>
</div>
</body>
</html>`

// loginStateTTL bounds the (valid, host-registered) login state even though
// poll never waits on it — the host still validates state expiry semantics.
const loginStateTTL = 10 * time.Minute

// handleStartLogin implements AuthProvider.StartLogin. Trae has no OAuth, so
// instead of returning an empty state (which the host rejects with "invalid
// oauth state"), we mint a valid state and open a data: guide page. Poll then
// reports the correct login path.
func handleStartLogin(raw []byte) ([]byte, error) {
	now := time.Now()
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider: providerName,
		URL:      "data:text/html;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(loginGuidePage)),
		State:    fmt.Sprintf("trw-%d", now.UnixNano()),
		// ExpiresAt keeps the host session TTL bookkeeping honest.
		ExpiresAt: now.Add(loginStateTTL).UTC(),
		Metadata: map[string]any{
			"prompt": "TraeWork 不支持 OAuth 登录：请关闭此窗口，使用「添加账号 → 粘贴凭据」从 Trae SOLO 的 storage.json 导入凭据。",
		},
	})
}

// handlePollLogin implements AuthProvider.LoginPoll. Trae has no server-side
// login state to poll, so every poll reports the explicit error message that
// guides the user to the paste-credential flow.
func handlePollLogin(raw []byte) ([]byte, error) {
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusError,
		Message: "TraeWork 不支持 OAuth 登录：请使用「添加账号 → 粘贴凭据」，从 Trae SOLO 客户端 storage.json 导入凭据（tc 加密头或明文 JSON 均可）。",
	})
}
