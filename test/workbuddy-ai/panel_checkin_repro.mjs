#!/usr/bin/env node
/**
 * WorkBuddy AI 国际版面板签到回归脚本（Node vm + DOM 桩，真实执行 panel.html 内联 JS）。
 *
 * 回归对象：面板每日签到改造——工具栏「全部签到」入口、自动签到开关、
 * 卡片签到按钮与批量签到置完成动线；同时校验「领取专家加油包」claim 家族已彻底移除。
 * 期望行为：card() 对国际版账号只渲染「签到 / 已签到」按钮；load() 回填 autoToggle
 * 状态；checkinAll() 成功后卡片按钮立即变「已签到」；toggleAuto() 写入 /checkin/config。
 *
 * 用法：
 *   node test/workbuddy-ai/panel_checkin_repro.mjs [panel-html-path]
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
const ENABLED_BODY = JSON.stringify({ enabled: true });

// 国际版账号夹具：region 恒为 global，签到状态由 checkin.today_checked_in 驱动。
const globalAccount = {
  auth_index: "g1",
  auth_id: "acct-g1",
  nickname: "国际版账号",
  region: "global",
  plan: "free",
  disabled: false,
  exhausted: false,
  test_failed: false,
  success: 3,
  failed: 0,
  checkin: { today_checked_in: false },
  credits: {
    total_remain: 100, total_used: 20, total_size: 120, pack_count: 1,
    packages: [{ name: "p" }], fetched_at: "2026-10-08T18:00:00Z",
  },
};

// 载入面板并构建 DOM / 存储 / fetch 桩，返回可观测的监听器、请求记录与卡片按钮。
function loadPanelContext(options) {
  const opts = options || {};
  const checkinAuto = opts.checkinAuto !== false;
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

  const listeners = new Map(); // "元素id:事件" -> [处理函数]
  const fetches = []; // fetch 调用记录
  const session = new Map([[SS_KEY, "TESTKEY"]]); // sessionStorage 桩
  const cards = []; // #grid .card 卡片桩

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
    addEventListener(event, handler) {
      const key = id + ":" + event;
      if (!listeners.has(key)) listeners.set(key, []);
      listeners.get(key).push(handler);
    },
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

  // 卡片桩：checkinAll 的置完成动线依赖 querySelector("button.sec,button.done")。
  const cardButton = mkEl("g1-checkin-btn");
  cardButton.textContent = "签到";
  const cardEl = mkEl("card-g1");
  cardEl.dataset.region = "global";
  cardEl.dataset.auth = "g1";
  cardEl.querySelector = (sel) => (sel.indexOf("button.sec") >= 0 ? cardButton : null);
  cards.push(cardEl);

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
    querySelectorAll(selector) {
      if (selector === "#grid .card") return cards;
      return [];
    },
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
        accounts: [JSON.parse(JSON.stringify(globalAccount))],
        active_auth: "g1",
        active_id: "g1",
        checkin_auto: checkinAuto,
        server_time: "2026-10-08 18:00:00",
      };
    } else if (u.indexOf("/checkin/config") >= 0) {
      const enabled = JSON.parse(String(opt.body || "{}")).enabled === true;
      payload = { checkin_auto: enabled, persistent: false };
    } else if (u.indexOf("/checkin") >= 0) {
      const body = JSON.parse(String(opt.body || "{}"));
      if (body.auth_index) {
        payload = { results: [{ auth_index: body.auth_index, nickname: "国际版账号", success: true, credit: 60 }] };
      } else {
        payload = {
          results: [{ auth_index: "g1", nickname: "国际版账号", success: true, credit: 60 }],
          summary: { total: 1, eligible: 1, success: 1, already: 0, fail: 0, attempted: 1 },
        };
      }
    } else if (u.indexOf("/refresh/status") >= 0) {
      payload = { running: false, total: 0, done: 0, failed: 0, per_account: [] };
    } else if (u.indexOf("/refresh") >= 0) {
      payload = { running: false };
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
    setInterval: () => 0, clearInterval: () => 0,
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
  return { ctx, els: getEl, listeners, fetches, session, cards, cardButton };
}

const failures = [];
function check(label, actual, expected) {
  const ok = String(actual) === String(expected);
  if (!ok) failures.push(label + "：期望 " + expected + "，实际 " + actual);
  console.log("   " + (ok ? "PASS" : "FAIL") + "  " + label + " = " + actual + "（期望 " + expected + "）");
}
// 等待一拍，让面板启动时的异步 load() / 后台轮询完成。
const flush = () => new Promise((r) => setTimeout(r, 40));

(async () => {
  console.log("[回归] 面板 = " + panelPath);
  const html = fs.readFileSync(panelPath, "utf8");

  console.log("场景1 面板结构（签到入口与 claim 家族清理）：");
  check("工具栏「全部签到」绑定 checkinAll", html.indexOf("checkinAll(this)") >= 0, true);
  check("自动签到开关元素存在", html.indexOf("autoToggle") >= 0, true);
  check("开关绑定 toggleAuto", html.indexOf("toggleAuto(this.checked)") >= 0, true);
  check("不再出现 claim 家族代码", html.toLowerCase().indexOf("claim") < 0, true);
  check("不再出现 trial 家族代码", html.toLowerCase().indexOf("trial") < 0, true);
  check("不再出现「领取专家加油包」入口", html.indexOf("领取专家加油包") >= 0, false);
  check("不再出现 skipped_global 残句", html.indexOf("skipped_global") >= 0, false);
  check("不再出现「国际版无需签到」残句", html.indexOf("国际版无需签到") >= 0, false);

  const scenario1 = loadPanelContext({ checkinAuto: true });
  await flush();
  const ctx = scenario1.ctx;
  console.log("场景1 启动自动加载（checkin_auto=true）：");
  check("autoToggle 回填为勾选", scenario1.els("autoToggle").checked, true);

  const cardHTML = ctx.card(JSON.parse(JSON.stringify(globalAccount)));
  check("卡片按钮 data-action=checkin", /data-action="checkin"/.test(cardHTML), true);
  check("卡片按钮文本为「签到」", cardHTML.indexOf(">签到</button>") >= 0, true);
  check("卡片不再出现 claim 按钮", /data-action="claim"/.test(cardHTML), false);
  check("卡片不再出现「领取专家加油包」", cardHTML.indexOf("领取专家加油包") >= 0, false);

  console.log("场景1 批量签到（checkinAll）：");
  await ctx.checkinAll(scenario1.els("toolbarBtn"));
  await flush();
  const batchCalls = scenario1.fetches.filter((f) => f.url.indexOf("/checkin") >= 0 && String(f.options.body || "") === "{}");
  check("批量请求已发出（body 为空对象）", batchCalls.length >= 1, true);
  check("卡片按钮置为已签到", scenario1.cardButton.innerHTML, "已签到");
  check("卡片按钮切 done 样式", scenario1.cardButton.className, "done");

  console.log("场景1 单卡签到（checkin）：");
  await ctx.checkin("g1", scenario1.els("singleBtn"));
  await flush();
  const singleCalls = scenario1.fetches.filter((f) => f.url.indexOf("/checkin") >= 0 && String(f.options.body || "").indexOf("auth_index") >= 0);
  check("单账号请求携带 auth_index", singleCalls.length >= 1, true);
  check("单账号请求体包含 g1", singleCalls.length ? singleCalls[0].options.body.indexOf("g1") >= 0 : false, true);

  console.log("场景1 自动签到开关（toggleAuto）：");
  await ctx.toggleAuto(true);
  await flush();
  const cfgCalls = scenario1.fetches.filter((f) => f.url.indexOf("/checkin/config") >= 0);
  check("开关请求已发出", cfgCalls.length >= 1, true);
  check("开关请求体 enabled=true", cfgCalls.length ? cfgCalls[0].options.body : "", ENABLED_BODY);

  const scenario2 = loadPanelContext({ checkinAuto: false });
  await flush();
  console.log("场景2 关闭态回填（checkin_auto=false）：");
  check("autoToggle 回填为未勾选", scenario2.els("autoToggle").checked, false);

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
