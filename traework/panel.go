// panel.go serves the TraeWork web panel (account credits, manual check-in,
// points refresh, enable/disable, unfreeze, failover status). It is a
// self-contained HTML page that talks to the management API routes
// registered in management.go.
package main

// servePanel returns the panel HTML for a resource sub-path. Unknown sub-paths
// fall back to the main dashboard.
func servePanel(sub string) []byte {
	_ = sub
	return []byte(panelHTML)
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
#msg{color:var(--muted);font-size:12px;min-height:18px}
</style>
</head>
<body>
<h1>TraeWork 插件面板</h1>
<div class="sub">账号额度 · 账号池 · 手动签到 · 积分查询 · 启停账号 · failover 状态</div>

<div class="toolbar">
  <button class="primary" onclick="fleetCheckin()">全部签到</button>
  <button onclick="refreshCredits()">刷新积分</button>
  <button onclick="toggleAuto()" id="autoBtn">自动签到: …</button>
</div>

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
const base = location.pathname.replace(/\/panel.*$/, '');
async function api(path, opts){
  const r = await fetch(base + path, opts);
  return r.json();
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
(async function(){
  const cfg = await api('/checkin/config', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({enabled: true})});
  renderAuto(cfg.enabled);
  await load();
})();
</script>
</body>
</html>
`
