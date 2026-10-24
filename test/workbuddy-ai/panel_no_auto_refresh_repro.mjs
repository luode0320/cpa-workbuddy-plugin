#!/usr/bin/env node
/**
 * WorkBuddy AI 国际版面板「不自动刷新」回归脚本（Node vm + DOM 桩，真实执行 panel.html 内联 JS）。
 *
 * 回归对象：用户要求面板打开时只展示持久化积分，不再自动触发全量刷新；
 * 最新积分改由卡片上的「刷新」按钮显式获取。账号规模变大后，打开面板就对
 * 全池打一轮上游既慢又无必要。
 *
 * 期望行为：
 *   1. 面板启动只发 GET /accounts，不发 POST /refresh，也不轮询 /refresh/status；
 *   2. 不注册任何定时器（历史实现每 10 分钟自动刷一轮）；
 *   3. 单卡「刷新」仍可用，命中 GET /credits?auth_index=；
 *   4. 无快照的卡片显示可操作提示，而不是永久「加载中…」。
 *
 * 用法：
 *   node test/workbuddy-ai/panel_no_auto_refresh_repro.mjs [panel-html-path]
 *   （可选指定面板路径，用于对历史版本做反证验证）
 *
 * 退出码：0 = 全部断言通过；1 = 存在断言失败。
 */
import fs from "fs";
import path from "path";
import vm from "vm";
import { fileURLToPath } from "url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const panelPath = process.argv[2]
  ? path.resolve(process.argv[2])
  : path.join(repoRoot, "workbuddy-ai", "panel.html");
const SS_KEY = "workbuddy-mgmt-key";
const CONTENT_TYPE = "application/json";

// 有积分快照的账号：用于断言卡片正常渲染，不触发任何上游刷新。
const accountWithCredits = {
  auth_index: "g1",
  auth_id: "acct-g1",
  nickname: "已持久化账号",
  region: "global",
  plan: "free",
  disabled: false,
  test_failed: false,
  success: 1,
  failed: 0,
  checkin: { today_checked_in: false },
  credits: {
    total_remain: 100, total_used: 20, total_size: 120, pack_count: 1,
    packages: [{ name: "p" }], fetched_at: "2026-10-24T10:00:00Z",
  },
};

// 无积分快照的账号：断言占位文案指向手动刷新，而不是等一个不会再来的后台刷新。
const accountWithoutCredits = {
  auth_index: "g2",
  auth_id: "acct-g2",
  nickname: "无快照账号",
  region: "global",
  plan: "free",
  disabled: false,
  test_failed: false,
  checkin: { today_checked_in: false },
};

// 载入面板并构建 DOM / 存储 / fetch 桩，返回请求记录与定时器登记表。
function loadPanelContext(accounts) {
  const html = fs.readFileSync(panelPath, "utf8");
  const bodies = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
  if (!bodies.length) throw new Error("未在 " + panelPath + " 中解析到内联脚本");
  // 逐个 script 块做语法编译校验（不执行），等价于 node --check。
  bodies.forEach((body, i) => {
    try {
      new vm.Script(body, { filename: "wbai-panel-script-" + i + ".js" });
    } catch (e) {
      throw new Error("第 " + (i + 1) + " 个 script 块语法校验失败：" + (e && e.message ? e.message : e));
    }
  });

  const fetches = []; // fetch 调用记录
  const timers = []; // setInterval 登记，用于断言「没有自动刷新定时器」
  const session = new Map([[SS_KEY, "TESTKEY"]]);

  const mkEl = (id) => ({
    id,
    textContent: "",
    innerHTML: "",
    value: "",
    checked: false,
    disabled: false,
    className: "",
    dataset: {},
    style: {},
    children: [],
    firstChild: { remove() {} },
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    addEventListener() {},
    removeEventListener() {},
    focus() {},
    remove() {},
    appendChild() {},
    replaceChildren() {},
    setAttribute() {},
    getAttribute() { return null; },
    removeAttribute() {},
    querySelector(sel) { return sel === ".t" || sel === ".d" ? mkEl(id + sel) : null; },
    querySelectorAll() { return []; },
  });

  const els = new Map();
  const getEl = (id) => {
    if (!els.has(id)) els.set(id, mkEl(id));
    return els.get(id);
  };
  const document = {
    title: "",
    getElementById(id) { return getEl(id); },
    querySelector(selector) {
      if (selector.startsWith("#")) return getEl(selector.slice(1));
      return null;
    },
    querySelectorAll() { return []; },
    createElement(tag) { return mkEl(tag); },
    addEventListener() {},
    removeEventListener() {},
    body: { appendChild() {}, removeChild() {} },
    documentElement: mkEl("html"),
  };

  const fetchStub = async (url, options) => {
    const u = String(url);
    const opt = options || {};
    fetches.push({ url: u, options: opt });
    let payload = {};
    if (u.indexOf("/accounts") >= 0) {
      payload = {
        accounts: JSON.parse(JSON.stringify(accounts)),
        active_auth: "acct-g1",
        checkin_auto: false,
        server_time: "2026-10-24 18:00:00",
      };
    } else if (u.indexOf("/credits") >= 0) {
      payload = {
        accounts: [{
          auth_index: "g1",
          nickname: "已持久化账号",
          credits: { total_remain: 77, total_used: 43, total_size: 120 },
        }],
      };
    }
    return {
      status: 200,
      statusText: "OK",
      ok: true,
      headers: { get: () => CONTENT_TYPE },
      text: async () => JSON.stringify(payload),
      json: async () => payload,
    };
  };

  const ctx = {
    console, Date, Math, JSON, Number, String, Object, Array, Boolean, RegExp, Error, Promise,
    isNaN, parseInt, parseFloat, encodeURIComponent, decodeURIComponent,
    TextEncoder, TextDecoder, setTimeout, clearTimeout,
    setInterval: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    clearInterval: () => {},
    requestAnimationFrame: (f) => f(),
    Blob: class {},
    URL: { createObjectURL() { return "blob:x"; }, revokeObjectURL() {} },
    URLSearchParams,
    btoa: (s) => Buffer.from(s, "binary").toString("base64"),
    atob: (s) => Buffer.from(s, "base64").toString("binary"),
    CSS: { escape: (s) => String(s) },
    document,
    navigator: { userAgent: "harness", language: "zh-CN", languages: ["zh-CN"] },
    history: { replaceState() {} },
    sessionStorage: {
      getItem(key) { return session.has(key) ? session.get(key) : null; },
      setItem(key, value) { session.set(key, String(value)); },
      removeItem(key) { session.delete(key); },
    },
    localStorage: { getItem() { return null; }, setItem() {} },
    location: { host: "cpa.luode.dev", search: "", href: "https://cpa.luode.dev/panel" },
    matchMedia: () => ({ matches: false, addEventListener() {}, addListener() {} }),
    fetch: fetchStub,
  };
  ctx.window = ctx;
  ctx.self = ctx;
  ctx.top = ctx;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  // 两个 script 块都按页面顺序执行：块 1 定义主题同步入口，块 2 为主逻辑。
  bodies.forEach((body, i) => vm.runInContext(body, ctx, { filename: "wbai-panel-script-" + i + ".js" }));
  return { ctx, els: getEl, fetches, timers };
}

const failures = [];
function check(label, actual, expected) {
  const ok = String(actual) === String(expected);
  if (!ok) failures.push(label + "：期望 " + expected + "，实际 " + actual);
  console.log("   " + (ok ? "PASS" : "FAIL") + "  " + label + " = " + actual + "（期望 " + expected + "）");
}
// 等待几拍，足以让启动时的异步 load() 与（历史实现里的）后台刷新发出。
const settle = () => new Promise((r) => setTimeout(r, 120));

(async () => {
  console.log("[回归] 面板 = " + panelPath);
  const html = fs.readFileSync(panelPath, "utf8");

  console.log("场景1 源码层：自动刷新入口已移除：");
  check("不再定义 startBackgroundRefresh", /function\s+startBackgroundRefresh/.test(html), false);
  check("不再有 10 分钟定时刷新常量", html.indexOf("PERIODIC_REFRESH_MS") >= 0, false);
  check("不再轮询 /refresh/status", html.indexOf("/refresh/status") >= 0, false);
  check("不再 POST /refresh", /["'`]\/refresh["'`]/.test(html), false);
  check("已移除后台刷新进度占位元素", html.indexOf("refreshProgress") >= 0, false);
  check("已移除卡片刷新三态样式", html.indexOf("ref-tag") >= 0, false);

  console.log("场景2 运行时：打开面板不触发任何刷新：");
  const panel = loadPanelContext([accountWithCredits, accountWithoutCredits]);
  await settle();
  const refreshCalls = panel.fetches.filter((f) => /\/refresh/.test(f.url));
  check("启动后没有任何 /refresh 或 /refresh/status 请求", refreshCalls.length, 0);
  // 面板仍保留 1s 的冷却倒计时定时器（纯前端文案，不发请求），
  // 因此这里锁的是「没有长周期刷新型定时器」，而不是「没有任何定时器」。
  const refreshTimers = panel.timers.filter((t) => Number(t.ms) >= 60 * 1000);
  check("没有长周期自动刷新定时器", refreshTimers.length, 0);
  // 即便把现存定时器全部手动触发一次，也不得产生刷新请求。
  for (const t of panel.timers) { try { t.fn(); } catch (_) {} }
  await settle();
  check("定时器回调不产生刷新请求", panel.fetches.filter((f) => /\/refresh/.test(f.url)).length, 0);
  const accountCalls = panel.fetches.filter((f) => f.url.indexOf("/accounts") >= 0);
  check("启动后只拉一次 /accounts", accountCalls.length, 1);

  console.log("场景3 有快照的卡片直接展示持久化数值：");
  const withCredits = panel.ctx.card(JSON.parse(JSON.stringify(accountWithCredits)));
  check("卡片展示持久化剩余积分", withCredits.indexOf("可用 100") >= 0, true);
  check("卡片展示快照时间", withCredits.indexOf("快照") >= 0, true);
  check("卡片带单卡刷新按钮", /data-action="refresh"/.test(withCredits), true);

  console.log("场景4 无快照的卡片给出可操作提示：");
  const noCredits = panel.ctx.card(JSON.parse(JSON.stringify(accountWithoutCredits)));
  check("无快照时提示点刷新获取", noCredits.indexOf("暂无积分数据，点「刷新」获取") >= 0, true);
  check("无快照时不再显示「加载中…」", noCredits.indexOf("加载中…") >= 0, false);

  console.log("场景4b 真实零余额仍照常展示数值（零积分可用，不得误判为无数据）：");
  const zeroAccount = JSON.parse(JSON.stringify(accountWithCredits));
  zeroAccount.auth_index = "g3";
  zeroAccount.credits = { total_remain: 0, total_used: 100, total_size: 100 };
  const zeroCard = panel.ctx.card(zeroAccount);
  check("零余额展示「可用 0」", zeroCard.indexOf("可用 0") >= 0, true);
  check("零余额不显示无数据占位", zeroCard.indexOf("暂无积分数据") >= 0, false);

  console.log("场景5 单卡「刷新」仍走 /credits 并更新卡片：");
  await panel.ctx.refreshCredits("g1", panel.els("refreshBtn"));
  await settle();
  const creditCalls = panel.fetches.filter((f) => f.url.indexOf("/credits") >= 0);
  check("单卡刷新命中 /credits?auth_index=g1", creditCalls.length >= 1 && creditCalls[0].url.indexOf("auth_index=g1") >= 0, true);

  if (failures.length) {
    console.error("\n[回归] FAIL（" + failures.length + " 项）");
    failures.forEach((f) => console.error("   - " + f));
    process.exit(1);
  }
  console.log("\n[回归] PASS 全部断言通过");
  process.exit(0);
})().catch((e) => {
  console.error("[回归] 脚本异常：" + (e && e.stack ? e.stack : e));
  process.exit(1);
});
