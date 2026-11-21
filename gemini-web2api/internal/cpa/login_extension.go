package cpa

import (
	"archive/zip"
	"bytes"
	_ "embed"
)

// extensionPopupJS 是扩展弹窗脚本，独立存成 login_popup.js 便于 node --check
// 直接做语法校验，也避免 JS 里的反引号与 Go 原始字符串定界符冲突。
//
//go:embed login_popup.js
var extensionPopupJS string

// extensionBackgroundJS 是扩展后台脚本，负责定时回推会话 Cookie 完成续期。
//
//go:embed login_background.js
var extensionBackgroundJS string

// 本文件保存「浏览器授权助手」扩展的清单、弹窗骨架与打包逻辑。
//
// 为什么需要扩展（2026-11-21 取证）：Gemini Web 的 StreamGenerate 只认
// __Secure-1PSID + SAPISID 这套 Google 会话 Cookie，两者都是 HttpOnly，
// 页面内 JS 通过 document.cookie 读不到。同时生产容器是 Debian 12 裸镜像，
// 没有 Chrome，插件进程也没有桌面会话，无法像桌面版那样拉起本机浏览器。
// 因此唯一能做到「在用户自己的浏览器里取到 HttpOnly Cookie 并回传」
// 的载体就是扩展：chrome.cookies API 能读 HttpOnly，且跑在用户已登录的
// 浏览器里。插件侧只负责收 Cookie 并落盘。
//
// 为什么还要后台脚本（2026-11-21 追加）：Gemini Web 会话会在数小时后被
// Google 判定失效，插件内的 CookieRotator 只能滚 __Secure-1PSIDTS，换不出
// 新的 __Secure-1PSID。用户浏览器里的活会话是唯一新鲜来源，因此扩展必须
// 常驻并按周期回推，这就是「自动续期」的落地位置。

// extensionManifest 是扩展清单，MV3 权限：cookies / clipboardRead /
// storage（保存插件地址与上次续期结果）/ alarms（定时续期）。
//
// 没有 content_scripts：续期只需要 chrome.cookies，不需要注入站点页面，
// 也就不需要把扩展暴露给 gemini.google.com 的页面上下文。
const extensionManifest = `{
  "manifest_version": 3,
  "name": "Gemini Web2API 授权助手",
  "version": "1.1.0",
  "description": "从已登录的浏览器读取 Google Gemini Web 会话 Cookie 并推送到 CLIProxyAPI 插件，随后定时续期。",
  "permissions": ["cookies", "clipboardRead", "storage", "alarms"],
  "host_permissions": ["<all_urls>"],
  "background": {
    "service_worker": "background.js"
  },
  "action": {
    "default_title": "推送到 Gemini Web2API",
    "default_popup": "popup.html"
  }
}
`

// extensionPopupHTML 是扩展弹窗页面骨架。
//
// 逻辑全部放在 popup.js：MV3 的内容安全策略禁止内联脚本。
const extensionPopupHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<style>
  body { width: 360px; margin: 0; padding: 14px; font: 13px/1.5 -apple-system, "Segoe UI", sans-serif; color: #1f2328; }
  h1 { font-size: 14px; margin: 0 0 10px; }
  label { display: block; margin: 10px 0 4px; font-weight: 600; }
  input, button { box-sizing: border-box; width: 100%; font: inherit; }
  input { padding: 6px 8px; border: 1px solid #d0d7de; border-radius: 6px; }
  button { margin-top: 10px; padding: 8px; border: 0; border-radius: 6px; background: #1f883d; color: #fff; font-weight: 600; cursor: pointer; }
  button:disabled { background: #94d3a2; cursor: default; }
  #result { margin-top: 10px; padding: 8px; border-radius: 6px; background: #f6f8fa; white-space: pre-wrap; word-break: break-all; }
  .ok { background: #dafbe1 !important; }
  .err { background: #ffebe9 !important; }
  .hint { margin-top: 8px; color: #656d76; font-size: 12px; }
  #renew { margin-top: 12px; padding-top: 10px; border-top: 1px solid #d0d7de; color: #656d76; font-size: 12px; }
</style>
</head>
<body>
<h1>Gemini Web2API 授权助手</h1>
<label for="pair">配对码（桥接页会自动复制到剪贴板）</label>
<input id="pair" placeholder="cpa|https://你的CPA域名|state" autocomplete="off">
<button id="go">读取 Cookie 并推送</button>
<div id="result">点击上方按钮开始。</div>
<div class="hint">请在已登录 gemini.google.com 的浏览器窗口中运行本扩展。</div>
<div id="renew">自动续期：授权成功后由后台每 30 分钟回推一次会话 Cookie。</div>
<script src="popup.js"></script>
</body>
</html>
`

// buildExtensionZip 生成可分发的扩展压缩包。
//
// 用户下载后解压，在 chrome://extensions 打开开发者模式「加载已解压的扩展程序」
// 即可；无需签名，也无需上传到任何商店。
// [参数] 无。
// [返回] zip 字节流与错误。
// 最近修改时间：2026-11-21；改动原因：新增，桥接页一键授权载体；
// 追加 background.js 承载定时续期。
func buildExtensionZip() ([]byte, error) {
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	files := []struct {
		name    string
		content string
	}{
		{"manifest.json", extensionManifest},
		{"popup.html", extensionPopupHTML},
		{"popup.js", extensionPopupJS},
		{"background.js", extensionBackgroundJS},
	}
	for _, item := range files {
		entry, err := writer.Create(item.name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(item.content)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
