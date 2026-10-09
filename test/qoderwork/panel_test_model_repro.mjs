#!/usr/bin/env node
/**
 * QoderWork 面板「测试按钮选模型」回归脚本（Node vm + DOM 桩，真实执行 panel.html 内联 JS）。
 *
 * 回归对象：卡片「测试」按钮由无到有，且交互为「先弹出模型小窗口、点选指定
 * 模型再测」。期望行为：点测试只打开弹窗并 GET /models 拉取模型列表；点某个
 * 模型才 POST /test-active 且 body 携带该 model；无模型时提示「暂无可用模型」。
 *
 * 用法：
 *   node test/qoderwork/panel_test_model_repro.mjs [panel-html-path]
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
  : path.join(repoRoot, "qoderwork", "panel.html");
const SS_KEY = "qoderwork-mgmt-key";
const CONTENT_TYPE = "application/json";

const account = {
  auth_index: "a1",
  auth_id: "acct-a1",
  nickname: "QoderWork 账号",
  region: "cn",
  plan: "free",
  disabled: false,
  exhausted: false,
  selected: false,
  preserve: false,
  success: 1,
  failed: 0,
  checkin: { active: true, today_checked_in: false },
  credits: {
    total_remain: 50, total_used: 10, total_size: 60, pack_count: 1,
    packages: [{ name: "p" }], fetched_at: "2026-10-09T02:00:00Z",
  },
};

// 载入面板并构建 DOM / 存储 / fetch 桩，返回可观测的监听器、请求记录与元素访问器。
function loadPanelContext(options) {
  const opts = options || {};
  const modelsPayload = opts.modelsPayload || { models: ["m1", "m2"] };
  const html = fs.readFileSync(panelPath, "utf8");
  const bodies = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
  if (!bodies.length) throw new Error("未在 " + panelPath + " 中解析到内联脚本");
  // 逐个 script 块做语法编译校验（不执行），等价于 node --check。
  bodies.forEach((body, i) => {
    try {
      new vm.Script(body, { filename: "qw-panel-script-" + i + ".js" });
    } catch (e) {
      throw new Error("第 " + (i + 1) + " 个 script 块语法校验失败：" + (e && e.message ? e.message : e));
    }
  });

  const listeners = new Map();
  const fetches = [];
  const session = new Map([[SS_KEY, "TESTKEY"]]);

  const mkEl = (id) => {
    const classes = new Set();
    return {
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
      classList: {
        add(c) { classes.add(c); },
        remove(c) { classes.delete(c); },
        toggle(c) { classes.has(c) ? classes.delete(c) : classes.add(c); },
        contains(c) { return classes.has(c); },
        _has(c) { return classes.has(c); },
      },
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
    };
  };

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
    if (u.indexOf("/models") >= 0) {
      payload = modelsPayload;
    } else if (u.indexOf("/test-active") >= 0) {
      const body = JSON.parse(String(opt.body || "{}"));
      payload = { ok: true, model: body.model, elapsed: "12ms", message: "活跃成功 (模型: " + body.model + ")" };
    } else if (u.indexOf("/accounts") >= 0) {
      payload = { accounts: [JSON.parse(JSON.stringify(account))], active_auth: "a1", server_time: "2026-10-09 10:00:00" };
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
  bodies.forEach((body, i) => vm.runInContext(body, ctx, { filename: "qw-panel-script-" + i + ".js" }));
  return { ctx, els: getEl, listeners, fetches, session };
}

const failures = [];
function check(label, actual, expected) {
  const ok = String(actual) === String(expected);
  if (!ok) failures.push(label + "：期望 " + expected + "，实际 " + actual);
  console.log("   " + (ok ? "PASS" : "FAIL") + "  " + label + " = " + actual + "（期望 " + expected + "）");
}
const flush = () => new Promise((r) => setTimeout(r, 40));

(async () => {
  console.log("[回归] 面板 = " + panelPath);
  const html = fs.readFileSync(panelPath, "utf8");

  console.log("场景1 面板结构（新增测试按钮与模型弹窗）：");
  check("卡片测试按钮绑定 openTestModal", /openTestModal\(/.test(html), true);
  check("测试弹窗 DOM 存在", html.indexOf('id="testModal"') >= 0, true);
  check("模型列表容器存在", html.indexOf('id="testModelList"') >= 0, true);
  check("模型项绑定 runTestModel", html.indexOf("runTestModel(this)") >= 0, true);

  const s1 = loadPanelContext({ modelsPayload: { models: ["m1", "m2"] } });
  await flush();
  const ctx = s1.ctx;

  console.log("场景2 点「测试」只打开弹窗并拉取模型列表：");
  const cardHTML = ctx.card(JSON.parse(JSON.stringify(account)));
  check("卡片测试按钮带 data-action=test", /data-action="test"/.test(cardHTML), true);
  ctx.openTestModal("a1");
  await flush();
  const modelCalls = s1.fetches.filter((f) => f.url.indexOf("/models") >= 0);
  check("已请求 /models", modelCalls.length >= 1, true);
  check("请求携带 auth_index=a1", modelCalls.length ? modelCalls[0].url.indexOf("auth_index=a1") >= 0 : false, true);
  check("弹窗已显示", s1.els("testModal").classList._has("show"), true);
  const listHTML = s1.els("testModelList").innerHTML;
  check("模型列表含 m1", listHTML.indexOf("m1") >= 0, true);
  check("模型列表含 m2", listHTML.indexOf("m2") >= 0, true);
  const beforeTest = s1.fetches.filter((f) => f.url.indexOf("/test-active") >= 0).length;
  check("打开弹窗阶段不发测试请求", beforeTest, 0);

  console.log("场景3 点选指定模型后按该模型测试：");
  await ctx.runTestModel({ dataset: { model: "m2" } });
  await flush();
  const testCalls = s1.fetches.filter((f) => f.url.indexOf("/test-active") >= 0);
  check("已发起 /test-active", testCalls.length >= 1, true);
  const body = testCalls.length ? String(testCalls[0].options.body || "") : "";
  check("测试请求携带指定 model=m2", body.indexOf('"model":"m2"') >= 0, true);
  check("测试请求携带 auth_index=a1", body.indexOf('"auth_index":"a1"') >= 0, true);
  check("弹窗内展示测试成功", s1.els("testResult").innerHTML.indexOf("测试成功") >= 0, true);

  console.log("场景4 无可用模型时给出提示：");
  const s2 = loadPanelContext({ modelsPayload: { models: [] } });
  await flush();
  s2.ctx.openTestModal("a1");
  await flush();
  check("空列表提示「暂无可用模型」", s2.els("testModelList").innerHTML.indexOf("暂无可用模型") >= 0, true);

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
