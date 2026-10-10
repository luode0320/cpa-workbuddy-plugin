#!/usr/bin/env node
/**
 * Cursor 管理面板免密直入回归脚本（Node vm + DOM 桩，真实执行 management.html 内联 JS）。
 *
 * 回归对象：面板启动时的管理密钥获取与进入分支。
 * 期望行为：已有密钥（父页同源存储 / URL 参数 / 会话存储）时免密直入自动加载状态，
 * 密钥输入区保持隐藏；无密钥时才显示输入区；手动连接保存到当前标签页会话后直接加载；
 * 密钥失效（401/403）时重新显示输入区允许修正。
 *
 * 用法：
 *   node test/cursor/panel_auto_entry_repro.mjs [panel-html-path]
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
  : path.join(repoRoot, "cursor", "internal", "plugin", "assets", "management.html");
const SS_KEY = "cursor-mgmt-key";

// 载入面板并构建 DOM / 存储 / fetch 桩，返回可观测的监听器与请求记录。
function loadPanelContext({ storedKey = null, respond } = {}) {
  const html = fs.readFileSync(panelPath, "utf8");
  const bodies = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
  // 面板当前为两个 script 块（主题同步 + 主逻辑），历史版本可能只有一个；按页面顺序全部执行。
  if (!bodies.length) throw new Error(`未在 ${panelPath} 中解析到内联脚本`);
  // 逐个 script 块做语法编译校验（不执行），等价于 node --check。
  bodies.forEach((body, i) => {
    try {
      new vm.Script(body, { filename: `cursor-panel-script-${i}.js` });
    } catch (e) {
      throw new Error(`第 ${i + 1} 个 script 块语法校验失败：${e && e.message ? e.message : e}`);
    }
  });

  const listeners = new Map(); // "元素id:事件" -> [处理函数]
  const fetches = []; // fetch 调用记录
  const session = new Map(); // sessionStorage 桩
  if (storedKey) session.set(SS_KEY, storedKey);

  const mkEl = (id) => ({
    id,
    textContent: "",
    innerHTML: "",
    value: "",
    checked: false,
    disabled: false,
    dataset: {},
    style: {},
    firstChild: null,
    __focused: false,
    lang: "",
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    addEventListener(event, handler) {
      const key = `${id}:${event}`;
      if (!listeners.has(key)) listeners.set(key, []);
      listeners.get(key).push(handler);
    },
    removeEventListener() {},
    focus() { this.__focused = true; },
    remove() {},
    append() {},
    appendChild() {},
    replaceChildren() {},
    setAttribute() {},
    getAttribute() { return null; },
    removeAttribute() {},
    querySelector() { return null; },
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
      if (selector === 'meta[name="description"]') return getEl("meta-description");
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

  const ctx = {
    console, Date, Math, JSON, Number, String, Object, Array, Boolean, RegExp, Error, Promise,
    isNaN, parseInt, parseFloat, encodeURIComponent, decodeURIComponent,
    TextEncoder, TextDecoder, setTimeout, clearTimeout,
    setInterval: () => 0, clearInterval: () => 0,
    requestAnimationFrame: (f) => f(),
    Blob: class {}, URL: { createObjectURL() { return "blob:x"; }, revokeObjectURL() {} },
    URLSearchParams,
    btoa: (s) => Buffer.from(s, "binary").toString("base64"),
    atob: (s) => Buffer.from(s, "base64").toString("binary"),
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
    fetch: async (url, options) => {
      fetches.push({ url: String(url), options: options || {} });
      const result = respond ? respond(String(url)) : { status: 200, payload: { accounts: [] } };
      return {
        status: result.status,
        statusText: "OK",
        ok: result.status >= 200 && result.status < 300,
        headers: { get: () => "application/json" },
        json: async () => result.payload,
      };
    },
  };
  ctx.window = ctx;
  ctx.self = ctx;
  ctx.top = ctx;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  // 两个 script 块都按页面顺序执行：块 1 定义主题同步入口，块 2 为主逻辑。
  bodies.forEach((body, i) => vm.runInContext(body, ctx, { filename: `cursor-panel-script-${i}.js` }));
  return { ctx, els: getEl, listeners, fetches, session };
}

const failures = [];
function check(label, actual, expected) {
  const ok = String(actual) === String(expected);
  if (!ok) failures.push(`${label}: 期望 ${expected}，实际 ${actual}`);
  console.log(`   ${ok ? "PASS" : "FAIL"}  ${label} = ${actual}（期望 ${expected}）`);
}

// 输入区可见性口径：display 被清空为 "" 即显示，否则（none/未触碰）视为隐藏。
const displayState = (el) => (el.style.display === "" ? "显示" : "隐藏");
const flush = () => new Promise((r) => setTimeout(r, 30));

(async () => {
  console.log("[回归] 面板 = cursor/internal/plugin/assets/management.html");

  // 场景 1：会话已有密钥（等价嵌入主面板自动获取）→ 免密直入并自动加载状态。
  {
    const { els, fetches } = loadPanelContext({ storedKey: "KEY" });
    await flush();
    const authBox = els("authBox");
    const keyInput = els("management-key");
    console.log("场景1 有密钥启动：");
    const statusCalls = fetches.filter((f) => f.url.includes("cursor-provider/status"));
    check("状态请求已自动发出", statusCalls.length, 1);
    check("状态请求携带 Authorization", statusCalls[0] && statusCalls[0].options.headers && statusCalls[0].options.headers.Authorization, "Bearer KEY");
    check("输入区保持隐藏", displayState(authBox), "隐藏");
    check("输入框未被聚焦", keyInput.__focused, false);
    check("加载按钮恢复可用（流程完成）", els("load").disabled, false);
  }

  // 场景 2：无密钥启动 → 显示输入区；手动连接（Enter / 点击）后保存并直接加载。
  {
    const { els, listeners, fetches, session } = loadPanelContext();
    await flush();
    const authBox = els("authBox");
    const keyInput = els("management-key");
    console.log("场景2 无密钥启动：");
    check("输入区已显示", displayState(authBox), "显示");
    check("输入框获得焦点", keyInput.__focused, true);
    check("未发出状态请求", fetches.filter((f) => f.url.includes("/status")).length, 0);

    console.log("场景2 手动输入并按 Enter 连接：");
    keyInput.value = "MANUAL";
    const enterHandler = listeners.get("management-key:keydown")?.[0];
    check("Enter 绑定已注册", typeof enterHandler, "function");
    enterHandler({ key: "Enter" });
    await flush();
    check("会话存储已保存密钥", session.get(SS_KEY), "MANUAL");
    check("输入区已隐藏", displayState(authBox), "隐藏");
    const first = fetches.filter((f) => f.url.includes("cursor-provider/status"));
    check("状态请求携带手动密钥", first[0] && first[0].options.headers.Authorization, "Bearer MANUAL");

    console.log("场景2 手动输入并点击「加载状态」连接：");
    keyInput.value = "MANUAL2";
    const clickHandler = listeners.get("load:click")?.[0];
    check("点击绑定已注册", typeof clickHandler, "function");
    clickHandler();
    await flush();
    check("会话存储已更新密钥", session.get(SS_KEY), "MANUAL2");
    const second = fetches.filter((f) => f.url.includes("cursor-provider/status"));
    check("状态请求携带新密钥", second[1] && second[1].options.headers.Authorization, "Bearer MANUAL2");
  }

  // 场景 3：密钥失效（401）→ 重新显示输入区，允许修正后重试。
  {
    const { els } = loadPanelContext({
      storedKey: "BAD",
      respond: () => ({ status: 401, payload: { error: "unauthorized" } }),
    });
    await flush();
    const authBox = els("authBox");
    const keyInput = els("management-key");
    console.log("场景3 密钥失效（401）：");
    check("输入区重新显示", displayState(authBox), "显示");
    check("输入框获得焦点", keyInput.__focused, true);
  }

  if (failures.length) {
    console.error(`\n[回归] FAIL（${failures.length} 项）`);
    failures.forEach((f) => console.error("   - " + f));
    process.exit(1);
  }
  console.log("\n[回归] PASS 全部断言通过");
})().catch((e) => {
  console.error("[回归] 脚本异常：" + (e && e.stack ? e.stack : e));
  process.exit(1);
});

