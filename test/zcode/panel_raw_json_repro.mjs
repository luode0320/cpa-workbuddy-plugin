#!/usr/bin/env node
// schema 6 管理 JSON 面板回归：URL 保持原样，显示文本在 DOM 边界转义。
// 用法：node test/zcode/panel_raw_json_repro.mjs
import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";

const html = fs.readFileSync(new URL("../../zcode/panel.html", import.meta.url), "utf8");
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m => m[1]);
assert.ok(scripts.length, "必须找到真实面板脚本");
scripts.forEach(script => new vm.Script(script));
const main = scripts.at(-1);
const boot = main.indexOf("/* ---------- boot ---------- */");
assert.ok(boot > 0, "必须找到面板启动边界");

// 仅桩化 DOM 与定时器，不执行启动联网；实际渲染函数来自 panel.html。
const elements = new Map();
const element = id => {
  if (!elements.has(id)) {
    elements.set(id, { textContent: "", innerHTML: "", href: "", classList: { add() {} }, addEventListener() {} });
  }
  return elements.get(id);
};
const context = vm.createContext({
  document: { getElementById: element },
  setInterval() { return 1; },
  clearInterval() {},
});
vm.runInContext(main.slice(0, boot), context);

const unsafe = `"><img src=x onerror=alert(1)><script>alert(2)</script>&'`;
const safe = "&quot;&gt;&lt;img src=x onerror=alert(1)&gt;&lt;script&gt;alert(2)&lt;/script&gt;&amp;&#39;";
assert.equal(context.esc(unsafe), safe);
const account = {
  auth_id: unsafe, label: unsafe, email: unsafe, user_id: unsafe,
  api_key_masked: unsafe, created_at: unsafe, models: [unsafe],
  quota: {
    plan_name: unsafe, total_grant: 1,
    entitlements: [{ show_name: unsafe, period: unsafe, grant_units: 1 }],
  },
};
const card = context.card(account);
assert.ok(card.includes(safe), "原始 JSON 文本应在卡片内被转义");
assert.ok(!card.includes("<img"), "账号或模型文本不能生成图片标签");
assert.ok(!card.includes("<script"), "账号或模型文本不能生成脚本标签");
const failure = context.resultBadge({ ok: false, error: unsafe });
assert.ok(failure.includes(safe), "原始 JSON 错误文本应被转义");
assert.ok(!failure.includes("<img"));
assert.ok(!failure.includes("<script"));

const authorizeURL = "https://chat.z.ai/api/oauth/authorize?client_id=test-client&redirect_uri=https%3A%2F%2Fzcode.z.ai%2Fapi%2Fv1%2Foauth%2Fcli%2Fcallback%2Fzai&state=test-state&response_type=code";
context.openActivateModal(unsafe, unsafe, authorizeURL, "test-state", () => {});
assert.equal(element("activateModalTitle").textContent, unsafe);
assert.equal(element("activateModalHint").textContent, unsafe);
assert.equal(element("activateModalTitle").innerHTML, "");
assert.equal(element("activateModalHint").innerHTML, "");
assert.equal(element("activateExternalLink").href, authorizeURL);
const query = new URL(element("activateExternalLink").href).searchParams;
assert.equal(query.get("client_id"), "test-client");
assert.equal(query.get("redirect_uri"), "https://zcode.z.ai/api/v1/oauth/cli/callback/zai");
assert.equal(query.get("state"), "test-state");
assert.equal(query.get("response_type"), "code");
console.log("PASS: 原始 OAuth URL 参数完整，账号/模型/错误文本安全转义，授权弹窗使用 textContent。");
