'use strict';

// 本文件是「Gemini Web2API 授权助手」扩展的后台脚本，由 login_extension.go
// 通过 go:embed 打进 zip 分发给用户。
//
// 职责只有一件事：按固定周期把最新的 Google 会话 Cookie 回推给插件，让插件
// 侧的账号在会话失效后能自动救回（对应插件资源路由 /login/renew）。
//
// 为什么续期必须由扩展发起（2026-11-21 取证）：Gemini Web 的
// __Secure-1PSIDTS 由插件内的 CookieRotator 每 600s 自动滚动，但会话被 Google
// 判定为失效时该轮换只能返回 ErrSignedOut —— 服务端没有任何可等待的对象能换出
// 新的 __Secure-1PSID，且该 Cookie 是 HttpOnly，只有 chrome.cookies 读得到。
// 因此「用户浏览器里的活会话」是唯一可靠的新鲜来源，只能由扩展主动回推。
//
// 为什么回推不需要配对码：配对码里的 state 是登录会话标识，只在桥接页打开后
// 15 分钟内有效；续期是长期行为，用户不会为它重开桥接页。插件侧的 /login/renew
// 因此只校验 Cookie 本身，靠邮箱把 Cookie 落到对应账号上。

var RENEW_PATH = '/v0/resource/plugins/gemini-web2api/login/renew';

// RENEW_ALARM 是续期定时器名称，STORAGE_KEY 保存插件 origin 与开关。
var RENEW_ALARM = 'gemini-web2api-renew';
var STORAGE_KEY = 'geminiWeb2apiRenew';

// RENEW_PERIOD_MINUTES 是续期间隔。
//
// 30 分钟：既短于 Gemini Web 会话在无活动时的失效窗口，又不会把上游
// ListAccounts / 首页请求打成探测流量。Chrome 对 alarms 的最小周期限制是
// 1 分钟（打包扩展），30 分钟远高于该下限。
var RENEW_PERIOD_MINUTES = 30;

// SESSION_COOKIE_NAMES 与 popup.js 保持同一份白名单，改动时两处必须同步。
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

function readState() {
  return new Promise(function (resolve) {
    chrome.storage.local.get(STORAGE_KEY, function (items) {
      resolve(items && items[STORAGE_KEY] ? items[STORAGE_KEY] : {});
    });
  });
}

function writeState(state) {
  return new Promise(function (resolve) {
    var patch = {};
    patch[STORAGE_KEY] = state;
    chrome.storage.local.set(patch, function () { resolve(); });
  });
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

// buildCookieHeader 把 Cookie 列表拼成一行 Cookie 请求头，只保留会话 Cookie。
function buildCookieHeader(cookies) {
  var wanted = {};
  for (var w = 0; w < SESSION_COOKIE_NAMES.length; w++) {
    wanted[SESSION_COOKIE_NAMES[w]] = true;
  }
  var seen = {};
  var parts = [];
  for (var i = 0; i < cookies.length; i++) {
    var c = cookies[i];
    if (!c || !c.name || !wanted[c.name] || seen[c.name]) { continue; }
    seen[c.name] = true;
    parts.push(c.name + '=' + c.value);
  }
  return parts.join('; ');
}

// renewOnce 执行一轮续期，返回值只用于写入状态给弹窗展示。
async function renewOnce() {
  var state = await readState();
  if (!state.origin) {
    return { ok: false, message: '尚未绑定插件地址，请先在桥接页完成一次授权' };
  }
  var cookies = await collectCookies();
  var header = buildCookieHeader(cookies);
  if (!header) {
    return { ok: false, message: '浏览器里没有 google.com 会话 Cookie，请先登录 gemini.google.com' };
  }
  var url = state.origin + RENEW_PATH + '?cookies=' + encodeURIComponent(header);
  var res = await fetch(url, { method: 'GET', cache: 'no-store' });
  var data = {};
  try { data = await res.json(); } catch (e) { data = {}; }
  if (!res.ok || data.error) {
    return { ok: false, message: data.error || ('HTTP ' + res.status) };
  }
  return { ok: true, message: '已回推会话 Cookie' };
}

// runRenew 执行续期并把结果写回状态，供弹窗显示上次结果。
async function runRenew() {
  var result;
  try {
    result = await renewOnce();
  } catch (err) {
    result = { ok: false, message: String(err && err.message ? err.message : err) };
  }
  var state = await readState();
  state.lastRunAt = Date.now();
  state.lastOk = !!result.ok;
  state.lastMessage = result.message;
  await writeState(state);
  return result;
}

// ensureAlarm 保证续期定时器存在且周期正确；Chrome 重启后 alarm 仍在，
// 但插件升级或用户手动清除时需要补建。
function ensureAlarm() {
  chrome.alarms.get(RENEW_ALARM, function (alarm) {
    if (!alarm) {
      chrome.alarms.create(RENEW_ALARM, { periodInMinutes: RENEW_PERIOD_MINUTES, delayInMinutes: 1 });
    }
  });
}

chrome.runtime.onInstalled.addListener(function () {
  ensureAlarm();
});

chrome.runtime.onStartup.addListener(function () {
  ensureAlarm();
});

chrome.alarms.onAlarm.addListener(function (alarm) {
  if (alarm && alarm.name === RENEW_ALARM) {
    runRenew();
  }
});

// 弹窗在授权成功后调它：绑定插件地址并开启周期性续期。
//
// 注意这里不立刻续期：插件侧 /login/renew 有 10 秒节流，紧接着刚完成的
// /login/submit 再推一次只会拿到 429，用户会看到一条无意义的失败提示。
// 首个 alarm 在 1 分钟后触发，那时节流窗口早已过去，第一次回推即可成功。
chrome.runtime.onMessage.addListener(function (message, sender, sendResponse) {
  if (!message || message.type !== 'bindOrigin') {
    return false;
  }
  (async function () {
    var state = await readState();
    state.origin = String(message.origin || '').replace(/\/+$/, '');
    await writeState(state);
    ensureAlarm();
    sendResponse({ ok: true, message: '自动续期已开启，1 分钟后开始每 30 分钟回推一次' });
  })();
  return true;
});
