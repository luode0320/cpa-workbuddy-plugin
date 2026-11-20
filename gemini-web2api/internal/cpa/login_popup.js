'use strict';

// 本文件是「Gemini Web2API 授权助手」扩展的弹窗逻辑，由 login_extension.go
// 通过 go:embed 打进 zip 分发给用户。
//
// 为什么用 GET 而不是 POST（2026-11-21 取证）：宿主
// internal/pluginhost/management.go 的 ServeResourceHTTP 硬性要求 GET
// （非 GET 直接返回 false 让上层 404），且资源路由不向插件传递请求体。
// 资源路由是唯一免管理密钥的通道，所以只能把 Cookie 放进 query；
// 为此只挑会话必需的 Cookie，把 URL 控制在常见代理的单行上限以内。

var SUBMIT_PATH = '/v0/resource/plugins/gemini-web2api/login/submit';

// SESSION_COOKIE_NAMES 是 Gemini Web 会话必需的最小 Cookie 集合。
//
// 依据 internal/chromeauth/cookies.go 的 hasRequiredGoogleCookies
// （SAPISID + __Secure-1PSID）与 rotate.go 的轮换链路，其余统计类
// Cookie 不参与鉴权，全部带上只会撑爆 URL 长度。
var SESSION_COOKIE_NAMES = [
  'SAPISID',
  'APISID',
  'SID',
  'HSID',
  'SSID',
  '__Secure-1PSID',
  '__Secure-1PSIDTS',
  '__Secure-1PSIDRTS',
  '__Secure-1PAPISID',
  '__Secure-3PSID',
  '__Secure-3PSIDTS',
  '__Secure-3PSIDRTS',
  '__Secure-3PAPISID',
  '__Secure-OSID',
  'SIDCC',
  'NID',
  'AEC'
];

function setResult(text, cls) {
  var box = document.getElementById('result');
  box.textContent = text;
  box.className = cls || '';
}

// parsePair 解析桥接页给出的配对码 cpa|origin|state。
function parsePair(raw) {
  var parts = String(raw || '').trim().split('|');
  if (parts.length !== 3 || parts[0] !== 'cpa') {
    throw new Error('配对码格式不对，应为 cpa|origin|state');
  }
  var origin = parts[1].trim().replace(/\/+$/, '');
  if (!/^https?:\/\//i.test(origin)) {
    throw new Error('配对码里的 CPA 地址缺少 http(s):// 前缀');
  }
  var state = parts[2].trim();
  if (!state) {
    throw new Error('配对码缺少 state');
  }
  return { origin: origin, state: state };
}

function collectCookies() {
  return new Promise(function (resolve, reject) {
    chrome.cookies.getAll({ domain: 'google.com' }, function (cookies) {
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
        return;
      }
      resolve(cookies || []);
    });
  });
}

// buildCookieHeader 把 Cookie 列表拼成一行 Cookie 请求头。
//
// 只保留 SESSION_COOKIE_NAMES 里的会话 Cookie。同名 Cookie 在多个子域下会重复，
// 保留首个即可（chrome.cookies 默认优先返回 .google.com 主域值）。
function buildCookieHeader(cookies) {
  var wanted = {};
  for (var w = 0; w < SESSION_COOKIE_NAMES.length; w++) {
    wanted[SESSION_COOKIE_NAMES[w]] = true;
  }
  var seen = {};
  var parts = [];
  for (var i = 0; i < cookies.length; i++) {
    var c = cookies[i];
    if (!c || !c.name || !wanted[c.name]) { continue; }
    if (seen[c.name]) { continue; }
    seen[c.name] = true;
    parts.push(c.name + '=' + c.value);
  }
  return parts.join('; ');
}

function readClipboard() {
  return new Promise(function (resolve) {
    try {
      navigator.clipboard.readText().then(resolve, function () { resolve(''); });
    } catch (e) {
      resolve('');
    }
  });
}

async function push() {
  var btn = document.getElementById('go');
  btn.disabled = true;
  try {
    var pair = parsePair(document.getElementById('pair').value);
    setResult('正在读取 Google Cookie…');
    var cookies = await collectCookies();
    var header = buildCookieHeader(cookies);
    if (!header) {
      throw new Error('没有读到 google.com 的会话 Cookie，请先在浏览器登录 gemini.google.com');
    }
    var url = pair.origin + SUBMIT_PATH
      + '?state=' + encodeURIComponent(pair.state)
      + '&cookies=' + encodeURIComponent(header);
    setResult('正在推送到 ' + pair.origin + ' …');
    var res = await fetch(url, { method: 'GET', cache: 'no-store' });
    var data = {};
    try { data = await res.json(); } catch (e) { data = {}; }
    if (!res.ok || data.error) {
      throw new Error(data.error || ('HTTP ' + res.status));
    }
    setResult('授权成功：' + (data.account_id || data.email || '已导入')
      + '\n回到 CPA 弹窗，登录流程会自动完成。', 'ok');
  } catch (err) {
    setResult('失败：' + (err && err.message ? err.message : err), 'err');
  } finally {
    btn.disabled = false;
  }
}

document.addEventListener('DOMContentLoaded', function () {
  readClipboard().then(function (text) {
    var t = String(text || '').trim();
    if (t.indexOf('cpa|') === 0) {
      document.getElementById('pair').value = t;
      setResult('已从剪贴板读取配对码，点击下方按钮继续。');
    }
  });
  document.getElementById('go').addEventListener('click', push);
});
