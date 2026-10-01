#!/usr/bin/env node
/**
 * 面板筛选标签计数回归脚本（Node vm + DOM 桩，真实执行 panel.html 内联 JS）。
 *
 * 回归对象：账号面板顶部筛选标签（全部 / 可用 / 耗尽 …）的计数。
 * 历史缺陷：`updateFilterCounts` 只在 `load()` 里调用一次；积分回填链路的其余
 * 重绘入口只重算汇总卡，导致标签长期停在首屏「冷缓存 = 全部可用」的旧值。
 *
 * 用法：
 *   node test/workbuddy/panel_filter_counts_repro.mjs            # 默认 workbuddy
 *   node test/workbuddy/panel_filter_counts_repro.mjs traework
 *   node test/workbuddy/panel_filter_counts_repro.mjs qoderwork
 *
 * 退出码：0 = 全部断言通过；1 = 存在断言失败。
 */
import fs from "fs";
import path from "path";
import vm from "vm";
import { fileURLToPath } from "url";

const plugin = (process.argv[2] || "workbuddy").trim();
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const panelPath = path.join(repoRoot, plugin, "panel.html");

// 夹具：51 个账号 = 30 耗尽 + 1 积分未知 + 20 可用。
const N = 51;
const EXHAUSTED = 30;
const UNKNOWN = 1;

function loadPanelContext() {
  const html = fs.readFileSync(panelPath, "utf8");
  const bodies = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
  // 面板固定两个 script 块，主逻辑在第二个。
  const main = bodies[bodies.length - 1];
  if (!main) throw new Error(`未在 ${panelPath} 中解析到内联脚本`);
  // 逐个 script 块做语法编译校验（不执行），等价于 node --check。
  bodies.forEach((body, i) => {
    try {
      new vm.Script(body, { filename: `${plugin}-panel-script-${i}.js` });
    } catch (e) {
      throw new Error(`第 ${i + 1} 个 script 块语法校验失败：${e && e.message ? e.message : e}`);
    }
  });
  console.log(`[语法] ${bodies.length} 个 script 块编译通过`);

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
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    addEventListener() {},
    removeEventListener() {},
    focus() {},
    remove() {},
    replaceWith() {},
    appendChild() {},
    insertBefore() {},
    querySelector() { return null; },
    querySelectorAll() { return []; },
    setAttribute() {},
    getAttribute() { return null; },
  });

  const els = new Map();
  const document = {
    getElementById(id) {
      if (!els.has(id)) els.set(id, mkEl(id));
      return els.get(id);
    },
    querySelector() { return null; },
    querySelectorAll() { return []; },
    createElement(tag) {
      const e = mkEl(tag);
      e.firstChild = { remove() {}, classList: { add() {}, remove() {} } };
      return e;
    },
    addEventListener() {},
    removeEventListener() {},
    body: { appendChild() {}, removeChild() {} },
    documentElement: mkEl("html"),
  };

  let payload;
  const ctx = {
    console,
    Date, Math, JSON, Number, String, Object, Array, Boolean, RegExp, Error, Promise,
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
    navigator: { userAgent: "harness" },
    history: { replaceState() {} },
    sessionStorage: { getItem() { return "KEY"; }, setItem() {}, removeItem() {} },
    localStorage: { getItem() { return null; }, setItem() {} },
    location: { host: "cpa.luode.dev", search: "", href: "https://cpa.luode.dev/panel" },
    fetch: async (url) => {
      const body = String(url).includes("/accounts") ? payload : { error: "n/a" };
      return {
        status: 200,
        statusText: "OK",
        headers: { get: () => "application/json" },
        text: async () => JSON.stringify(body),
      };
    },
  };
  ctx.window = ctx;
  ctx.self = ctx;
  ctx.top = ctx;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  vm.runInContext(main, ctx, { filename: `${plugin}-panel-main.js` });
  return { ctx, document, setPayload: (p) => { payload = p; } };
}

const mkAccounts = (withCredits) =>
  Array.from({ length: N }, (_, i) => {
    const ex = i < EXHAUSTED;
    const unknown = i >= EXHAUSTED && i < EXHAUSTED + UNKNOWN;
    return {
      auth_index: "idx-" + i,
      auth_id: "acct-" + i,
      region: "cn",
      plan: "free",
      disabled: false,
      // 后端回填后才带真实 exhausted 标记；冷缓存里一律为 false。
      exhausted: withCredits ? ex : false,
      test_failed: false,
      preserve: false,
      success: 10,
      failed: i < 5 ? 3 : 0,
      credits: (withCredits && !unknown)
        ? {
            total_remain: ex ? 0 : 100,
            total_used: ex ? 900 : 100,
            total_size: ex ? 900 : 200,
            pack_count: 1,
            packages: [{ name: "p" }],
            fetched_at: "2026-10-01T07:56:34Z",
          }
        : null,
    };
  });

const failures = [];
function check(label, actual, expected) {
  // 三个面板对 textContent 的赋值类型不统一（有的赋 number、有的赋 String），
  // 这里统一按字符串比较，只校验显示出来的计数是否正确。
  const ok = String(actual) === String(expected);
  if (!ok) failures.push(`${label}: 期望 ${expected}，实际 ${actual}`);
  console.log(`   ${ok ? "PASS" : "FAIL"}  ${label} = ${actual}（期望 ${expected}）`);
}

const { ctx, document, setPayload } = loadPanelContext();
const el = (id) => document.getElementById(id);

(async () => {
  console.log(`[回归] 面板 = ${plugin}/panel.html`);

  setPayload({
    accounts: mkAccounts(false),
    active_auth: "acct-0",
    active_id: "acct-0",
    checkin_auto: true,
    server_time: "2026-10-01 15:56:29",
  });
  await ctx.load();
  console.log("阶段1 冷缓存 load() 之后：");
  check("cntAll", el("cntAll").textContent, String(N));

  // 真实路径：后台刷新把 /credits 逐个回填进 lastAccounts，随后用户点「可用」标签。
  ctx.__filled = mkAccounts(true);
  vm.runInContext(
    "lastAccounts.forEach((a,i)=>{a.credits=__filled[i].credits;a.exhausted=__filled[i].exhausted;});",
    ctx,
  );
  ctx.filterRegion("available", null);
  console.log("阶段2 积分回填后点击「可用」标签（= 缺陷截图那一刻）：");
  check("cntAvailable", el("cntAvailable").textContent, String(N - EXHAUSTED));
  check("cntExhausted", el("cntExhausted").textContent, String(EXHAUSTED));
  check("cntAll", el("cntAll").textContent, String(N));

  // 自洽性：标签计数必须等于同口径筛选结果，不能与可见卡片数打架。
  if (typeof ctx.accountsForFilter === "function") {
    const scoped = ctx.accountsForFilter(vm.runInContext("lastAccounts", ctx));
    check("标签可用 与 筛选结果一致", Number(el("cntAvailable").textContent), scoped.length);
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
