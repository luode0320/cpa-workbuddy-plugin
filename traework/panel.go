// panel.go serves the TraeWork web panel (account credits, manual check-in,
// points refresh, enable/disable, unfreeze, failover status, credential
// import). It is a self-contained HTML page that talks to the management API
// routes registered in management.go.
package main

import (
	"encoding/json"
	"strings"
)

// traeStoragePath is the canonical Windows host path to the Trae SOLO
// globalStorage directory shown in the panel UI. The panel's job is to teach
// the user where their credentials live, not to detect them at runtime — the
// plugin server may run in a Linux container, in which case APPDATA / HOME
// would point somewhere irrelevant. Keep this in sync with the storage layout
// expected by import.go (parseCredentialImport reads the `iCubeAuthInfo://
// icube.cloudide` key from storage.json in this directory).
const traeStoragePath = `C:\Users\luode\AppData\Roaming\TRAE SOLO CN\User\globalStorage`

// servePanel returns the panel HTML for a resource sub-path. Unknown sub-paths
// fall back to the main dashboard. The Trae SOLO credential path is the fixed
// Windows host path (UI hint, not server-detected) — the plugin server may run
// in a Linux container and cannot predict the user's local Trae SOLO install
// location, so the path is hard-coded for clarity rather than injected at
// serve time.
//
// The path is injected in two forms:
//   - __STORAGE_DIR_DISPLAY__ → raw path, rendered inside an HTML <code> hint
//     (backslashes are fine in HTML text nodes).
//   - __STORAGE_DIR_JSON__ → JSON-escaped string literal, assigned to the JS
//     constant TRAE_STORAGE_PATH. JSON escaping doubles the backslashes so the
//     JS string survives parsing (single-quoted C:\Users\... would drop every
//     backslash as an unknown escape sequence).
func servePanel(sub string) []byte {
	_ = sub
	dirJSON, _ := json.Marshal(traeStoragePath)
	out := strings.ReplaceAll(panelHTML, "__STORAGE_DIR_JSON__", string(dirJSON))
	out = strings.ReplaceAll(out, "__STORAGE_DIR_DISPLAY__", traeStoragePath)
	return []byte(out)
}

const panelHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>TraeWork 插件面板</title>
<style>
:root{--bg:#f5f7fa;--card:#fff;--line:#e2e8f0;--text:#1e293b;--muted:#64748b;--ok:#16a34a;--warn:#d97706;--bad:#dc2626;--brand:#0f766e}
*{box-sizing:border-box;margin:0;padding:0}
body{font:14px/1.6 -apple-system,"Segoe UI","Microsoft YaHei",sans-serif;background:var(--bg);color:var(--text);padding:24px}
h1{font-size:20px;margin-bottom:4px}
.sub{color:var(--muted);margin-bottom:16px}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:16px;margin-bottom:16px}
.toolbar{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:16px}
button{border:1px solid var(--line);background:#fff;border-radius:6px;padding:6px 14px;cursor:pointer;font-size:13px}
button:hover{background:#f1f5f9}
button.primary{background:var(--brand);border-color:var(--brand);color:#fff}
button.primary:hover{filter:brightness(1.1)}
table{width:100%;border-collapse:collapse}
th,td{text-align:left;padding:8px 10px;border-bottom:1px solid var(--line);font-size:13px;vertical-align:middle}
th{color:var(--muted);font-weight:500;white-space:nowrap}
.tag{display:inline-block;padding:1px 8px;border-radius:999px;font-size:12px}
.tag.ok{background:#dcfce7;color:var(--ok)}
.tag.bad{background:#fee2e2;color:var(--bad)}
.tag.warn{background:#fef3c7;color:var(--warn)}
.tag.muted{background:#f1f5f9;color:var(--muted)}
.empty{color:var(--muted);text-align:center;padding:24px}
.hint{color:var(--muted);font-size:12px;margin:-8px 0 12px;word-break:break-all}
#msg{color:var(--muted);font-size:12px;min-height:18px}
</style>
</head>
<body>
<h1>TraeWork 插件面板</h1>
<div class="sub">账号额度 · 账号池 · 手动签到 · 积分查询 · 启停账号 · failover 状态</div>

<div id="authBox" class="card" style="display:none;margin-bottom:16px">
  <div style="margin-bottom:8px;color:var(--muted)">需要管理密钥（management key）才能访问账号数据。从 CPA 主面板嵌入时会自动获取，无需输入。错误密钥请勿连点，避免 IP 封禁。</div>
  <div style="display:flex;gap:8px">
    <input id="keyInput" type="password" placeholder="management key" style="flex:1;border:1px solid var(--line);border-radius:6px;padding:6px 10px;font-size:13px">
    <button class="primary" onclick="saveKey()">连接</button>
  </div>
</div>

<div class="toolbar">
  <button class="primary" onclick="fleetCheckin()">全部签到</button>
  <button onclick="refreshCredits()">刷新积分</button>
  <button onclick="toggleAuto()" id="autoBtn">自动签到: …</button>
  <button class="primary" onclick="pickFile()" title="选择 Trae SOLO 客户端导出的 storage.json 文件导入凭据">📁 导入 storage.json</button>
  <button onclick="pickDir()" title="备选入口：从 Trae 凭据目录中挑 storage.json（仅浏览器不支持单文件选择时使用）">选择目录</button>
  <button onclick="copyStorageDir()" title="复制 Trae SOLO 凭据目录路径到剪贴板">复制路径</button>
</div>
<div class="hint" id="storageHint">Trae SOLO 凭据目录（Windows）：<code>__STORAGE_DIR_DISPLAY__</code>。请在资源管理器中打开该目录，将 <code>storage.json</code> 拖入面板或点击「📁 导入 storage.json」选择该文件。</div>
<input type="file" id="fileInput" accept=".json,application/json" style="display:none">
<input type="file" id="dirInput" webkitdirectory multiple style="display:none">

<div class="card">
  <table>
    <thead>
      <tr>
        <th>账号</th><th>剩余积分</th><th>状态</th><th>failover</th><th>操作</th>
      </tr>
    </thead>
    <tbody id="rows"></tbody>
  </table>
  <div class="empty" id="empty" style="display:none">暂无账号 — 在插件配置中添加 traework 凭据后刷新。</div>
</div>
<div id="msg"></div>

<script>
// Management API prefix is the host-stable /v0/management/plugins/<p>, NOT
// derivable from the panel URL (which lives under /v0/resource/plugins/<p>/panel):
// location.pathname would yield the resource prefix and every POST (e.g.
// /import) would 404 with an empty body ("路由未注册"). Matches the
// workbuddy/qoderwork panels which hardcode the same management prefix.
const base = "/v0/management/plugins/traework-provider";

/* ---------- management key acquisition (3 fallbacks, zero config) ----------
   1) CPA main panel localStorage "cli-proxy-auth" (XOR+base64, same-origin
      iframe embed) - same approach as cpa-plugin-key-policy.
   2) ?key= URL param (cleaned from history after read, kept in sessionStorage).
   3) Manual input (sessionStorage only, never persisted).
*/
const ENC_PREFIX = "enc::v1::";
const SECRET_SALT = "cli-proxy-api-webui::secure-storage";
const PANEL_STORE = "cli-proxy-auth";
const SS_KEY = "traework-mgmt-key";

function _enc(t){return new TextEncoder().encode(t)}
function _dec(b){return new TextDecoder().decode(b)}
function _keyBytes(){try{return _enc(SECRET_SALT+"|"+window.location.host+"|"+navigator.userAgent)}catch(e){return _enc(SECRET_SALT)}}
function _xor(d,k){const r=new Uint8Array(d.length);for(let i=0;i<d.length;i++)r[i]=d[i]^k[i%k.length];return r}
function _b64d(s){const bin=atob(s);const b=new Uint8Array(bin.length);for(let i=0;i<bin.length;i++)b[i]=bin.charCodeAt(i);return b}
function deobfuscate(p){if(!p||!p.startsWith(ENC_PREFIX))return p;try{return _dec(_xor(_b64d(p.slice(ENC_PREFIX.length)),_keyBytes()))}catch(e){return p}}
function isEmbedded(){try{return window.self!==window.top}catch(e){return false}}

function readPanelKey(){
  if(!isEmbedded())return null;
  let raw;try{raw=localStorage.getItem(PANEL_STORE)}catch(e){return null}
  if(!raw)return null;
  try{
    const parsed=JSON.parse(deobfuscate(raw));
    const st=(parsed&&parsed.state)||parsed||{};
    return typeof st.managementKey==="string"&&st.managementKey?st.managementKey:null;
  }catch(e){return null}
}
function readUrlKey(){
  const m=new URLSearchParams(window.location.search).get("key");
  if(m){history.replaceState(null,"",window.location.pathname)}
  return m;
}
function getKey(){
  return sessionStorage.getItem(SS_KEY)||readPanelKey()||readUrlKey();
}
function saveKey(){
  const v=document.getElementById("keyInput").value.trim();
  if(!v)return;
  sessionStorage.setItem(SS_KEY,v);
  document.getElementById("authBox").style.display="none";
  enterPanel();
}
function showAuth(){document.getElementById("authBox").style.display="block";const i=document.getElementById("keyInput");i&&i.focus()}
function authHeaders(k){return k?{"Authorization":"Bearer "+k}:{}}
// api() always returns a parsed object, or throws a descriptive Error when
// the body is empty / not JSON. Bare r.json() loses every signal: a fused
// plugin handler or stale identity produces HTTP 200 with an empty body,
// and r.json() throws a DOMException that tells the user nothing. Parsing
// the text ourselves lets us surface the real status code and a short
// body preview so the next panel failure is actionable.
async function api(path, opts){
  opts = opts || {};
  opts.credentials = 'omit';
  opts.headers = Object.assign({}, opts.headers || {}, authHeaders(getKey()));
  let r;
  try { r = await fetch(base + path, opts); }
  catch (e) { throw new Error('网络请求失败：' + (e && e.message || e)); }
  const status = r.status;
  const ctype = r.headers.get('content-type') || '';
  let text = '';
  try { text = await r.text(); } catch (e) { text = ''; }
  // Host rejects plugin management API calls without a valid Bearer key with
  // 401 (often with a JSON body). Throwing BEFORE the parse step prevents the
  // body from being mistaken for data (which previously rendered a fake
  // "暂无账号" empty state and a bare "导入失败").
  if (status === 401) throw new Error('鉴权失败 HTTP 401 — management key 缺失或无效，请刷新页面重新输入，或从 CPA 主面板嵌入打开本页自动获取');
  if (!text) {
    if (status >= 500) throw new Error('服务端错误 HTTP ' + status + '（响应为空）');
    if (status === 404) throw new Error('路由未注册：' + path + '（插件可能未加载，重启宿主）');
    if (status >= 400) throw new Error('鉴权失败 HTTP ' + status + '（响应为空，确认 management_key 配置）');
    // 200 + empty body: plugin handler fused or identity stale on host.
    // Surfacing as an Error makes the panel toast say "重启宿主" instead of
    // the previous crypt DOMException ("Failed to execute 'json'...").
    throw new Error('插件返回空响应 HTTP 200 — 插件可能未加载、已熔断或身份失效，请重启宿主');
  }
  if (!/json/i.test(ctype)) {
    throw new Error('非 JSON 响应（HTTP ' + status + '）：' + text.slice(0, 200));
  }
  try { return JSON.parse(text); }
  catch (e) {
    throw new Error('无法解析 JSON（HTTP ' + status + '）：' + text.slice(0, 200));
  }
}
function tag(cls, text){ return '<span class="tag ' + cls + '">' + text + '</span>'; }
function esc(s){ return String(s == null ? '' : s).replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c])); }
function msg(s){ document.getElementById('msg').textContent = s; }

async function load(){
  const d = await api('/accounts');
  const tbody = document.getElementById('rows');
  tbody.innerHTML = '';
  const accs = d.accounts || [];
  document.getElementById('empty').style.display = accs.length ? 'none' : '';
  for (const a of accs){
    const st = [];
    if (a.active) st.push(tag('ok', '活跃'));
    if (a.disabled) st.push(tag('muted', '已停用'));
    if (a.exhausted) st.push(tag('bad', '额度耗尽'));
    if (a.anomaly) st.push(tag('bad', '异常冻结'));
    if (!st.length) st.push(tag('ok', '正常'));
    const fo = [];
    if (a.cooling_down) fo.push(tag('warn', '冷却中(' + (a.fail_count||0) + '次)'));
    if (a.fail_count && !a.cooling_down) fo.push(tag('warn', '失败' + a.fail_count + '次'));
    if (!fo.length) fo.push(tag('muted', '-'));
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td><b>' + esc(a.nickname || a.uid || a.auth_index) + '</b><div class="sub" style="margin:0">' + esc(a.uid || '') + '</div></td>' +
      '<td><b>' + (a.remain || 0) + '</b></td>' +
      '<td>' + st.join(' ') + '</td>' +
      '<td>' + fo.join(' ') + '</td>' +
      '<td>' +
        '<button onclick="checkin(\'' + esc(a.auth_index) + '\')">签到</button> ' +
        '<button onclick="select(\'' + esc(a.auth_index) + '\')">设为活跃</button> ' +
        '<button onclick="toggleDisabled(\'' + esc(a.auth_index) + '\',' + (!a.disabled) + ')">' + (a.disabled ? '启用' : '停用') + '</button> ' +
        (a.anomaly ? '<button onclick="unfreeze(\'' + esc(a.auth_index) + '\')">解除冻结</button>' : '') +
      '</td>';
    tbody.appendChild(tr);
  }
}

async function checkin(authIndex){
  msg('签到中…');
  const r = await api('/checkin', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({auth_index: authIndex})});
  msg((r.message || (r.ok ? '签到成功' : '签到失败')) + (r.points ? ' +' + r.points + ' 积分' : ''));
  load();
}
async function fleetCheckin(){
  msg('全部账号签到中…');
  const r = await api('/checkin', {method:'POST', headers:{'Content-Type':'application/json'}, body: '{}'});
  msg('完成，成功 ' + (r.checked_in || 0) + ' 个');
  load();
}
async function refreshCredits(){
  msg('刷新积分中…');
  await api('/credits');
  msg('积分已刷新');
  load();
}
async function select(authIndex){
  await api('/select', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({auth_index: authIndex})});
  load();
}
async function toggleDisabled(authIndex, disable){
  await api(disable ? '/disable' : '/enable', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({auth_index: authIndex})});
  load();
}
async function unfreeze(authIndex){
  await api('/unfreeze', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({auth_index: authIndex})});
  load();
}
async function toggleAuto(){
  const cfg = await api('/checkin/config', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({enabled: false})});
  const want = !cfg.enabled;
  await api('/checkin/config', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({enabled: want})});
  renderAuto(want);
}
function renderAuto(on){
  document.getElementById('autoBtn').textContent = '自动签到: ' + (on ? '开' : '关');
}

// ---------------------------------------------------------------------------
// Credential import (Trae SOLO storage.json)
// ---------------------------------------------------------------------------
// TRAE_STORAGE_PATH is the fixed Windows host path of the Trae SOLO
// globalStorage directory (must match traeStoragePath in Go land). It is
// surfaced to the user as a hint and as the payload of the "复制路径" button.
// The value is injected as a JSON string literal (__STORAGE_DIR_JSON__), which
// escapes the backslashes so they survive JS parsing.
const TRAE_STORAGE_PATH = __STORAGE_DIR_JSON__;
let rememberedDir = null;

function copyStorageDir(){
  const done = () => msg('凭据目录已复制：' + TRAE_STORAGE_PATH);
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(TRAE_STORAGE_PATH).then(done).catch(() => fallbackCopy(done));
  } else {
    fallbackCopy(done);
  }
}
function fallbackCopy(done){
  const ta = document.createElement('textarea');
  ta.value = TRAE_STORAGE_PATH;
  document.body.appendChild(ta);
  ta.select();
  try { document.execCommand('copy'); done(); } catch(e){ msg('复制失败，请手动复制：' + TRAE_STORAGE_PATH); }
  ta.remove();
}

const IDB_NAME = 'traework-panel-v1';
function idbGet(key){
  return new Promise((res) => {
    try {
      const rq = indexedDB.open(IDB_NAME, 1);
      rq.onupgradeneeded = () => rq.result.createObjectStore('kv');
      rq.onsuccess = () => {
        const g = rq.result.transaction('kv').objectStore('kv').get(key);
        g.onsuccess = () => res(g.result);
        g.onerror = () => res(null);
      };
      rq.onerror = () => res(null);
    } catch(e){ res(null); }
  });
}
function idbPut(key, val){
  return new Promise((res) => {
    try {
      const rq = indexedDB.open(IDB_NAME, 1);
      rq.onupgradeneeded = () => rq.result.createObjectStore('kv');
      rq.onsuccess = () => {
        const tx = rq.result.transaction('kv', 'readwrite');
        tx.objectStore('kv').put(val, key);
        tx.oncomplete = () => res(true);
        tx.onerror = () => res(false);
      };
      rq.onerror = () => res(false);
    } catch(e){ res(false); }
  });
}

// pickFile is the primary entry point: open a single-file picker filtered to
// .json / application/json so the user cannot accidentally pick an unrelated
// file. The accept attribute is a hint, not a filter, so we also validate
// the chosen filename on the change handler.
function pickFile(){
  document.getElementById('fileInput').click();
}

document.getElementById('fileInput').addEventListener('change', async (ev) => {
  const f = (ev.target.files || [])[0];
  if (!f) return;
  if (!/\.json$/i.test(f.name) && f.type !== 'application/json') {
    msg('请选择 .json 文件（Trae SOLO 导出的 storage.json）');
    ev.target.value = '';
    return;
  }
  await doImport(f);
  ev.target.value = '';
});

async function pickDir(){
  // Primary path: File System Access API (Chrome/Edge). The directory handle
  // is remembered in IndexedDB, so the second click onward opens directly on
  // the previously chosen Trae globalStorage directory.
  if (window.showDirectoryPicker) {
    try {
      if (!rememberedDir) rememberedDir = await idbGet('storage-dir');
      if (rememberedDir) {
        try {
          if ((await rememberedDir.queryPermission({mode:'read'})) !== 'granted') {
            await rememberedDir.requestPermission({mode:'read'});
          }
        } catch(e){}
      }
      const opts = { id: 'trae-storage', mode: 'read' };
      if (rememberedDir) opts.startIn = rememberedDir;
      const handle = await window.showDirectoryPicker(opts);
      rememberedDir = handle;
      await idbPut('storage-dir', handle);
      const fh = await findStorageJson(handle);
      if (!fh) { msg('所选目录下未找到 storage.json，请确认选择的是 Trae 凭据目录'); return; }
      await doImport(await fh.getFile());
      return;
    } catch(e){ if (e && e.name === 'AbortError') return; }
  }
  // Fallback: webkitdirectory input (still lets the user pick the folder).
  document.getElementById('dirInput').click();
}

document.getElementById('dirInput').addEventListener('change', async (ev) => {
  const files = Array.from(ev.target.files || []);
  const f = files.find(x => x.name === 'storage.json') || files[0];
  if (f) await doImport(f);
  ev.target.value = '';
});

async function findStorageJson(dirHandle){
  try { return await dirHandle.getFileHandle('storage.json'); } catch(e){}
  try {
    for await (const [, h] of dirHandle.entries()) {
      if (h.kind === 'file' && h.name === 'storage.json') return h;
    }
  } catch(e){}
  return null;
}

async function doImport(file){
  try {
    const text = await file.text();
    if (!text || !text.trim()) { msg('文件内容为空'); return; }
    msg('导入中…');
    const r = await api('/import', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({filename: file.name || 'storage.json', content: text})});
    msg(r.message || r.error || (r.ok ? '导入成功' : '导入失败（响应无错误详情）'));
    load();
  } catch(e){ msg('导入失败：' + (e && e.message || e)); }
}

// Drag & drop anywhere on the panel: dropping storage.json (or any .json
// credential) triggers the same import path without any navigation.
document.addEventListener('dragover', (e) => e.preventDefault());
document.addEventListener('drop', async (e) => {
  e.preventDefault();
  const files = Array.from((e.dataTransfer && e.dataTransfer.files) || []);
  const f = files.find(x => x.name === 'storage.json') || files[0];
  if (f) await doImport(f);
});

function enterPanel(){
  (async function(){
    try {
      const cfg = await api('/checkin/config', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({enabled: true})});
      renderAuto(cfg && cfg.enabled);
    } catch (e) {
      msg('面板初始化失败：' + (e && e.message || e));
      return;
    }
    try { await load(); }
    catch (e) { msg('账号列表加载失败：' + (e && e.message || e)); }
  })();
}
if(getKey()){
  enterPanel();
}else{
  showAuth();
  msg('未检测到 management key — 请手动输入，或从 CPA 主面板嵌入打开本页自动获取');
}
</script>
</body>
</html>
`
