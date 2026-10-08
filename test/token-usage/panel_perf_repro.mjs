#!/usr/bin/env node
/**
 * Token 用量面板性能回归脚本（Node vm + DOM 桩，真实执行 dashboard.go 内联 JS）。
 *
 * 回归对象：面板打开时的加载行为与刷新机制。
 * 历史缺陷：面板启动即建立 SSE 长连接（/usage/events）并以 15s 定时器整页轮询，
 * 默认时间范围过大（today），服务端在打开面板后持续被读放大拖垮。
 * 期望行为：
 *   1. 启动时不建立 EventSource、不注册 setInterval；
 *   2. 默认时间范围为 last_1_hour（默认只加载最近 1h 数据）；
 *   3. 手动刷新按钮保留（点击后重新拉取数据）。
 *
 * 用法：
 *   node test/token-usage/panel_perf_repro.mjs [dashboard-go-path] [--legacy]
 *   --legacy：对旧版 dashboard.go 做反证验证，断言旧版存在 SSE / 定时器。
 *
 * 退出码：0 = 全部断言通过；1 = 存在断言失败。
 */
import fs from "fs";
import path from "path";
import vm from "vm";
import crypto from "crypto";
import { fileURLToPath } from "url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const legacyMode = process.argv.includes("--legacy");
const argPath = process.argv.slice(2).find((arg) => !arg.startsWith("--"));
const dashboardPath = argPath ? path.resolve(argPath) : path.join(repoRoot, "token-usage-tracker", "usage_stats", "dashboard.go");

// 1) 读取 dashboard.go 并提取内联模板（含指纹打印，防止读错文件）。
const source = fs.readFileSync(dashboardPath, "utf8");
const md5 = crypto.createHash("md5").update(Buffer.from(source, "utf8")).digest("hex");
console.log(`[输入] ${dashboardPath}`);
console.log(`[指纹] md5=${md5} bytes=${Buffer.byteLength(source, "utf8")}`);

const templateStart = source.indexOf("const dashboardHTMLTemplate = `");
if (templateStart < 0) throw new Error("未找到 dashboardHTMLTemplate 模板");
const bodyStart = templateStart + "const dashboardHTMLTemplate = `".length;
const bodyEnd = source.indexOf("</html>", bodyStart);
if (bodyEnd < 0) throw new Error("模板缺少 </html> 结束标记");
let html = source.slice(bodyStart, bodyEnd + "</html>".length);

// 2) 复刻 Go 侧占位符替换中影响执行的两项（其余 FULL_MODE_APIKEY_* 保持注释形态即可）。
let localeDir = path.join(path.dirname(dashboardPath), "locales");
if (!fs.existsSync(localeDir)) {
  // 反证/快照模式：dashboard.go 副本不在原目录时回退到仓库内置 locale。
  localeDir = path.join(repoRoot, "token-usage-tracker", "usage_stats", "locales");
}
const publicLocales = {};
for (const code of ["en", "zh-CN", "zh-TW", "ru"]) {
  const raw = JSON.parse(fs.readFileSync(path.join(localeDir, `${code}.json`), "utf8"));
  const filtered = {};
  for (const [key, value] of Object.entries(raw)) {
    if (key.startsWith("apiKey.") || key === "table.apiKey") continue;
    filtered[key] = value;
  }
  publicLocales[code] = filtered;
}
html = html.replace("/*LOCALE_PLACEHOLDER*/", JSON.stringify(publicLocales));
html = html.replace("/*FULL_MODE_PAGE*/", "false");

// 3) 提取两个 <script> 块并做语法编译校验。
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
if (scripts.length < 2) throw new Error(`预期至少 2 个 script 块，实际 ${scripts.length}`);
scripts.forEach((body, i) => {
  try {
    new vm.Script(body, { filename: `token-usage-dashboard-script-${i}.js` });
  } catch (e) {
    throw new Error(`第 ${i + 1} 个 script 块语法校验失败：${e && e.message ? e.message : e}`);
  }
});
console.log(`[语法] ${scripts.length} 个 script 块编译通过`);

// 4) 构建可观测 DOM / 网络 / 定时器桩。
const eventSources = []; // EventSource 构造记录
const intervalCalls = []; // setInterval 调用记录（参数 ms）
const fetches = []; // fetch 调用记录
const listeners = new Map(); // "id|event" -> [handler]
const rafQueue = [];

const stubParent = {
  insertBefore() {},
  appendChild() {},
  replaceChildren() {},
};

function mkEl(id) {
  const el = {
    id,
    textContent: "",
    innerHTML: "",
    value: "",
    checked: false,
    disabled: false,
    hidden: true,
    open: false,
    returnValue: "",
    files: [],
    options: [],
    selectedIndex: -1,
    children: [],
    childNodes: [],
    dataset: {},
    attrs: {},
    style: {
      setProperty() {},
      getPropertyValue() { return ""; },
      removeProperty() {},
    },
    parentNode: stubParent,
    isConnected: true,
    offsetWidth: 900,
    offsetHeight: 330,
    clientWidth: 900,
    clientHeight: 330,
    scrollHeight: 100,
    scrollWidth: 100,
    firstChild: null,
    classList: {
      add() {}, remove() {}, toggle() {}, contains() { return false; },
    },
    addEventListener(event, handler) {
      const key = `${id}|${event}`;
      if (!listeners.has(key)) listeners.set(key, []);
      listeners.get(key).push(handler);
    },
    removeEventListener() {},
    setAttribute(k, v) { el.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(el.attrs, k) ? el.attrs[k] : null; },
    removeAttribute(k) { delete el.attrs[k]; },
    appendChild() {}, insertBefore() {}, replaceChildren() {}, replaceWith() {}, append() {}, remove() {},
    insertAdjacentElement() {}, insertAdjacentHTML() {},
    focus() {}, blur() {}, click() {}, scrollIntoView() {}, dispatchEvent() {},
    showModal() {}, close() {},
    querySelector() { return null; },
    querySelectorAll() { return []; },
    closest() { return null; },
    contains() { return false; },
    getBoundingClientRect() { return { width: 900, height: 330, top: 0, left: 0, right: 900, bottom: 330 }; },
    animate() { return { cancel() {} }; },
  };
  return el;
}

const els = new Map();
const getEl = (id) => {
  if (!els.has(id)) els.set(id, mkEl(id));
  return els.get(id);
};

// 快速范围按钮桩：供 updateQuickRangeState 遍历并写 aria-pressed。
const rangePresets = ["last_1_hour", "last_5_hours", "last_7_days", "last_30_days", "current_month"].map((mode) => {
  const btn = mkEl(`range-preset-${mode}`);
  btn.dataset.rangePreset = mode;
  return btn;
});

const document = {
  title: "",
  getElementById(id) { return getEl(id); },
  querySelector(selector) {
    if (selector === ".request-table") return getEl("__request-table");
    if (selector === ".dimension-table") return getEl("__dimension-table");
    return null;
  },
  querySelectorAll(selector) {
    if (selector === "[data-range-preset]") return rangePresets;
    return [];
  },
  createElement(tag) { return mkEl(tag); },
  createElementNS(ns, tag) { return mkEl(tag); },
  createDocumentFragment() { return mkEl("#fragment"); },
  addEventListener(event, handler) {
    const key = `document|${event}`;
    if (!listeners.has(key)) listeners.set(key, []);
    listeners.get(key).push(handler);
  },
  removeEventListener() {},
  body: mkEl("body"),
  documentElement: mkEl("html"),
};

const buildPayload = (url) => {
  if (url.includes("/preferences")) return {};
  if (url.includes("/stats/initial")) {
    return { models: [], series: [], generated_at: "2026-10-08T12:00:00Z", sources: [], last_used: "" };
  }
  if (url.includes("/stats/trends")) return { model_series: [], bucket_seconds: 3600 };
  if (url.includes("/stats/groups")) return { items: [], total: 0 };
  if (url.includes("/requests")) return { items: [], total: 0 };
  if (url.includes("/prices")) return { prices: {}, revision: 0, sync_settings: { provider_priority: [], ignored_suffixes: [], mappings: [] }, last_sync: null };
  if (url.includes("/costs")) return { summary: { requests: 0, priced_requests: 0, unpriced_requests: 0 }, models: [], series: [], price_book_revision: 0 };
  if (url.includes("/exchange-rate")) return { rate: 7.1, base: "USD", quote: "CNY" };
  return {};
};

const ctx = {
  console, Date, Math, JSON, Number, String, Object, Array, Boolean, RegExp, Error, Promise,
  isNaN, parseInt, parseFloat, encodeURIComponent, decodeURIComponent,
  TextEncoder, TextDecoder, Intl, Set, Map, WeakMap, WeakSet, AbortController,
  setTimeout, clearTimeout,
  setInterval: (fn, ms) => { intervalCalls.push(ms); return 0; },
  clearInterval: () => {},
  requestAnimationFrame: (fn) => { rafQueue.push(fn); return rafQueue.length; },
  cancelAnimationFrame: () => {},
  Blob: class {},
  URL: { createObjectURL() { return "blob:x"; }, revokeObjectURL() {} },
  URLSearchParams,
  btoa: (s) => Buffer.from(s, "binary").toString("base64"),
  atob: (s) => Buffer.from(s, "base64").toString("binary"),
  CSS: { escape: (s) => String(s) },
  EventSource: class { constructor(url) { eventSources.push(String(url)); } close() {} },
  MutationObserver: class { constructor() {} observe() {} disconnect() {} takeRecords() { return []; } },
  ResizeObserver: class { constructor() {} observe() {} disconnect() {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  matchMedia: (query) => ({ matches: false, media: query, addEventListener() {}, removeListener() {}, addListener() {} }),
  confirm: () => false,
  prompt: () => "",
  addEventListener(event, handler) {
    const key = `window|${event}`;
    if (!listeners.has(key)) listeners.set(key, []);
    listeners.get(key).push(handler);
  },
  removeEventListener() {},
  document,
  navigator: { userAgent: "harness", language: "en", languages: ["en"] },
  history: { replaceState() {} },
  sessionStorage: { getItem() { return null; }, setItem() {}, removeItem() {} },
  localStorage: { getItem() { return null; }, setItem() {}, removeItem() {} },
  location: {
    pathname: "/v0/resource/plugins/token-usage-tracker/usage",
    search: "",
    hash: "",
    href: "https://cpa.luode.dev/v0/resource/plugins/token-usage-tracker/usage",
    assign() {},
    replace() {},
  },
  fetch: async (url, options) => {
    fetches.push({ url: String(url), options: options || {} });
    const payload = buildPayload(String(url));
    return {
      ok: true,
      status: 200,
      statusText: "OK",
      headers: { get: () => "application/json" },
      json: async () => payload,
      text: async () => JSON.stringify(payload),
      blob: async () => new Blob(),
    };
  },
};
ctx.window = ctx;
ctx.self = ctx;
ctx.top = ctx;
ctx.parent = ctx; // window.parent === window：走非嵌入分支
ctx.globalThis = ctx;
vm.createContext(ctx);

// 5) 按页面顺序执行两个 script 块（主题 IIFE + 主逻辑 IIFE）。
scripts.forEach((body, i) => vm.runInContext(body, ctx, { filename: `token-usage-dashboard-script-${i}.js` }));
console.log("[执行] 面板脚本已在 vm 中真实执行");

const flush = async (rounds = 6) => {
  for (let i = 0; i < rounds; i++) await new Promise((r) => setTimeout(r, 30));
  while (rafQueue.length) {
    const fn = rafQueue.shift();
    try { fn(0); } catch (e) { /* rAF 回调容错 */ }
  }
};

const failures = [];
function check(label, actual, expected) {
  const ok = String(actual) === String(expected);
  if (!ok) failures.push(`${label}: 期望 ${expected}，实际 ${actual}`);
  console.log(`   ${ok ? "PASS" : "FAIL"}  ${label} = ${actual}（期望 ${expected}）`);
}
function checkTrue(label, ok, detail) {
  if (!ok) failures.push(`${label}${detail ? "：" + detail : ""}`);
  console.log(`   ${ok ? "PASS" : "FAIL"}  ${label}${detail ? "（" + detail + "）" : ""}`);
}

(async () => {
  if (legacyMode) {
    console.log("[反证] legacy 模式：验证旧版确实存在 SSE 长连接与 15s 定时器");
    await flush();
    checkTrue("旧版建立 EventSource 长连接", eventSources.length >= 1, `构造数=${eventSources.length}`);
    checkTrue("旧版注册 setInterval 定时器", intervalCalls.length >= 1, `调用=${JSON.stringify(intervalCalls)}`);
    if (eventSources.length) checkTrue("SSE 地址指向 /usage/events", eventSources.some((u) => u.includes("/usage/events")), eventSources.join(","));
  } else {
    console.log("[回归] 新版本：启动后不得建立 SSE、不得注册定时器、默认最近 1 小时");
    await flush();

    // 运行级断言 1：无 SSE、无定时器。
    check("EventSource 构造次数", eventSources.length, 0);
    check("setInterval 调用次数", intervalCalls.length, 0);

    // 运行级断言 2：默认时间范围 = last_1_hour，且默认加载窗口为 1 小时。
    check("rangeLabel 文案（en）", getEl("rangeLabel").textContent, "Last hour");
    check("range 隐藏值", getEl("range").value, "last_1_hour");
    const initialCall = fetches.find((f) => f.url.includes("/stats/initial"));
    checkTrue("启动即拉取 stats/initial", Boolean(initialCall), initialCall ? initialCall.url : "未发现请求");
    if (initialCall) {
      const params = new URLSearchParams(initialCall.url.split("?")[1] || "");
      check("请求 range 参数", params.get("range"), "custom");
      const start = new Date(params.get("start") || 0).getTime();
      const end = new Date(params.get("end") || 0).getTime();
      const span = end - start;
      checkTrue("默认加载窗口 ≈ 1 小时", Math.abs(span - 3600 * 1000) <= 5000, `span=${span}ms`);
      checkTrue("结束时间接近当前时刻", Math.abs(Date.now() - end) <= 15000, `age=${Date.now() - end}ms`);
    }

    // 运行级断言 3：快速范围按钮含 last_1_hour 且被标记为选中。
    const preset = rangePresets.find((b) => b.dataset.rangePreset === "last_1_hour");
    check("预设按钮 aria-pressed", preset && preset.attrs["aria-pressed"], "true");

    // 运行级断言 4：手动刷新按钮保留，点击后重新拉取数据。
    const refreshHandlers = listeners.get("refreshButton|click") || [];
    checkTrue("refreshButton 已注册 click", refreshHandlers.length >= 1, `handlers=${refreshHandlers.length}`);
    const before = fetches.filter((f) => f.url.includes("/stats/initial")).length;
    if (refreshHandlers.length) {
      refreshHandlers[0]();
      await flush();
      const after = fetches.filter((f) => f.url.includes("/stats/initial")).length;
      checkTrue("点击刷新后重新拉取 stats/initial", after > before, `before=${before} after=${after}`);
    }

    // 源码级断言 5：模板与后端不再含 SSE / 定时器残留。
    const forbidden = ["EventSource", "usage/events", "startTimer", "startUsageEvents", "refreshTimer", "setInterval", "15000"];
    for (const token of forbidden) {
      checkTrue(`dashboard.go 不含 ${token}`, !source.includes(token), source.includes(token) ? "仍存在" : "已清除");
    }
    const managementPath = path.join(repoRoot, "token-usage-tracker", "management.go");
    const feedPath = path.join(repoRoot, "token-usage-tracker", "feed_ingest.go");
    const managementSrc = fs.readFileSync(managementPath, "utf8");
    const feedSrc = fs.readFileSync(feedPath, "utf8");
    checkTrue("management.go 不含 usage/events", !managementSrc.includes("usage/events"));
    checkTrue("feed_ingest.go 不含 feedNotifier", !feedSrc.includes("feedNotifier"));

    // 源码级断言 6：快速范围按钮与本地化键已加入模板。
    checkTrue("模板含 last_1_hour 预设按钮", html.includes('data-range-preset="last_1_hour"'));
    checkTrue("模板含 range.lastHour 文案键", html.includes("range.lastHour"));
  }

  if (failures.length) {
    console.error(`\n[回归] FAIL（${failures.length} 项）`);
    failures.forEach((f) => console.error("   - " + f));
    process.exit(1);
  }
  console.log(legacyMode ? "\n[反证] PASS 旧版行为差异被正确识别" : "\n[回归] PASS 全部断言通过");
})().catch((e) => {
  console.error("[回归] 脚本异常：" + (e && e.stack ? e.stack : e));
  process.exit(1);
});
