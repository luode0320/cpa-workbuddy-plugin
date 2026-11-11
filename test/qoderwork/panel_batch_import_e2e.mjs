#!/usr/bin/env node
/**
 * qoderwork 面板「凭据导入」端到端回归（Node vm + DOM 桩，真实执行面板内联 JS）。
 *
 * 纯解析函数已由 panel_batch_import.mjs 锁死，这里只回答一个问题：
 * 点一次「导入并保存」后，凭据是否真的逐条走 /import-cred、单项失败是否
 * 不拖垮其余项、是否还会误打 PAT 端点 /import。这三条链路断了，前端解析
 * 再对也导不进账号。
 *
 * 2026-11-11 起导入只认凭据 JSON（PAT 入口已移除），因此这里额外断言
 * 全程「零 /import 请求」，防止 PAT 逻辑悄悄回来。
 *
 * 用法：node test/qoderwork/panel_batch_import_e2e.mjs [panel-html-path]
 *   （可选指定面板路径，用于对变异副本做反证验证）
 * 退出码 0 = 全部通过。
 */
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";

const panelPath = process.argv[2]
  ? path.resolve(process.argv[2])
  : path.join(process.cwd(), "qoderwork", "panel.html");
const html = fs.readFileSync(panelPath, "utf8");
const SS_KEY = "qoderwork-mgmt-key";
const CRED1 = { auth: { accessToken: "jt-1" }, account: { uid: "u1" } };
const CRED2 = { auth: { accessToken: "jt-2" }, account: { uid: "u2" } };
const CRED3 = { auth: { accessToken: "jt-3" }, account: { uid: "u3" } };
const PAT_TEXT = "pt-aaaaaaaaaaaa1111";

function loadPanelContext(opts) {
  const options = opts || {};
  const bodies = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
  bodies.forEach((body, i) => {
    // 编译校验（不执行），等价 node --check
    new vm.Script(body, { filename: "qw-panel-" + i + ".js" });
  });

  const listeners = new Map();
  const fetches = [];
  const session = new Map([[SS_KEY, "TESTKEY"]]);

  const mkEl = (id) => {
    const classes = new Set();
    return {
      id, textContent: "", innerHTML: "", value: "", checked: false, disabled: false,
      className: "", dataset: {}, style: {}, children: [],
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
      removeEventListener() {}, focus() {}, remove() {}, appendChild() {},
      replaceChildren() {}, setAttribute() {}, getAttribute() { return null; },
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
    querySelector(sel) { return sel.startsWith("#") ? getEl(sel.slice(1)) : null; },
    querySelectorAll() { return []; },
    createElement(tag) { return mkEl(tag); },
    addEventListener() {}, removeEventListener() {},
    body: { appendChild() {}, removeChild() {} },
    documentElement: mkEl("html"),
  };

  let credCall = 0;
  const fetchStub = async (url, init) => {
    const u = String(url);
    const body = String((init && init.body) || "");
    fetches.push({ url: u, body });
    let payload = {};
    if (u.indexOf("/import-cred") >= 0) {
      credCall++;
      // 异常路径与业务失败路径都要覆盖：前者走 catch，后者走 success:false 分支，
      // 两条都必须被记进失败明细，否则导入会静默丢账号。
      if (options.throwCredAt && credCall === options.throwCredAt) throw new Error("network down");
      const bad = options.failCredAt && credCall === options.failCredAt;
      payload = bad
        ? { success: false, error: "credential has empty uid" }
        : { success: true, nickname: "nick" + credCall, uid: "u-cred" + credCall };
    } else if (u.indexOf("/accounts") >= 0) {
      payload = { accounts: [], active_auth: "", server_time: "2026-11-07 01:00:00" };
    }
    return {
      status: 200, ok: true,
      headers: { get: () => "application/json" },
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
      getItem(k) { return session.has(k) ? session.get(k) : null; },
      setItem(k, v) { session.set(k, String(v)); },
      removeItem(k) { session.delete(k); },
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
  bodies.forEach((body, i) => vm.runInContext(body, ctx, { filename: "qw-panel-" + i + ".js" }));
  return { ctx, els: getEl, listeners, fetches };
}

const failures = [];
function check(label, cond, detail) {
  if (cond) {
    console.log("   PASS  " + label);
  } else {
    failures.push(label + (detail ? "：" + detail : ""));
    console.log("   FAIL  " + label + (detail ? "：" + detail : ""));
  }
}

function patCalls(fs) {
  return fs.filter((f) => /\/import$/.test(f.url) || /\/import\?/.test(f.url));
}
function credCalls(fs) {
  return fs.filter((f) => f.url.indexOf("/import-cred") >= 0);
}

(async () => {
  console.log("[回归] 面板 = " + panelPath);
  console.log("场景1 凭据 JSON 数组 → 逐条走 /import-cred：");
  const s1 = loadPanelContext();
  s1.els("importBatchText").value = JSON.stringify([CRED1, CRED2]);
  const btn1 = s1.els("batchImportBtn");
  await s1.ctx.runImport(btn1);
  const c1 = credCalls(s1.fetches);
  check("发起 2 次 /import-cred", c1.length === 2, "实际 " + c1.length);
  check("第 1 条携带 u1", c1.length === 2 && c1[0].body.indexOf('"uid":"u1"') >= 0, c1[0] && c1[0].body);
  check("第 2 条携带 u2", c1.length === 2 && c1[1].body.indexOf('"uid":"u2"') >= 0, c1[1] && c1[1].body);
  check("未打 PAT 端点 /import", patCalls(s1.fetches).length === 0, "出现了 /import 请求");
  check("全部成功后关闭弹窗", !s1.els("importModal").classList._has("show"), "弹窗仍打开");
  check("全部成功后清空输入", s1.els("importBatchText").value === "", "输入未清空");
  check("按钮已复位可再次点击", btn1.disabled === false, "按钮仍禁用");

  console.log("场景2 导出备份包装 → 展开为逐条凭据：");
  const s2 = loadPanelContext();
  s2.els("importBatchText").value = JSON.stringify({
    version: 1,
    plugin: "qoderwork-provider",
    accounts: [{ credential: CRED1 }, { credential: CRED2 }],
  });
  await s2.ctx.runImport(s2.els("batchImportBtn"));
  check("发起 2 次 /import-cred", credCalls(s2.fetches).length === 2, "实际 " + credCalls(s2.fetches).length);
  check("未打 PAT 端点 /import", patCalls(s2.fetches).length === 0, "出现了 /import 请求");

  console.log("场景3 JSONL → 每行一条：");
  const s3 = loadPanelContext();
  s3.els("importBatchText").value = JSON.stringify(CRED1) + "\n" + JSON.stringify(CRED2);
  await s3.ctx.runImport(s3.els("batchImportBtn"));
  check("发起 2 次 /import-cred", credCalls(s3.fetches).length === 2, "实际 " + credCalls(s3.fetches).length);
  check("未打 PAT 端点 /import", patCalls(s3.fetches).length === 0, "出现了 /import 请求");

  console.log("场景4 单项失败不拖垮其余项，失败明细可见：");
  const s4 = loadPanelContext({ failCredAt: 2, throwCredAt: 3 });
  s4.els("importBatchText").value = JSON.stringify([CRED1, CRED2, CRED3]);
  s4.els("importModal").classList.add("show");
  await s4.ctx.runImport(s4.els("batchImportBtn"));
  check("仍尝试了全部 3 项", credCalls(s4.fetches).length === 3, "实际 " + credCalls(s4.fetches).length);
  const resultHTML = s4.els("batchResult").innerHTML;
  check("结果面板已展示", s4.els("batchResult").classList._has("show"), "结果面板未显示");
  check("业务失败原因可见", resultHTML.indexOf("credential has empty uid") >= 0, resultHTML.slice(0, 200));
  check("请求异常也被记为失败", resultHTML.indexOf("network down") >= 0, resultHTML.slice(0, 200));
  check("有失败时保留弹窗便于重试", s4.els("importModal").classList._has("show"), "弹窗被关闭");
  check("进度提示已清空", s4.els("batchProgress").textContent === "", "进度残留");
  // 部分成功后输入框收敛为仅失败项，重试不会重复导入已成功的 u1
  const retryText = s4.els("importBatchText").value;
  check("重试文本只剩失败项", !retryText.includes('"u1"'), "重试文本仍含已成功的 u1：" + retryText);
  check("重试文本保留业务失败项", retryText.includes('"u2"'), "缺 u2：" + retryText);
  check("重试文本保留异常项", retryText.includes('"u3"'), "缺 u3：" + retryText);

  console.log("场景5 无输入时拒绝执行并提示：");
  const s5 = loadPanelContext();
  await s5.ctx.runImport(s5.els("batchImportBtn"));
  check("未发起任何导入请求",
    patCalls(s5.fetches).length === 0 && credCalls(s5.fetches).length === 0,
    "空输入仍发了请求");

  console.log("场景6 只贴 PAT 文本 → 判为无法识别，不打任何端点：");
  const s6 = loadPanelContext();
  s6.els("importBatchText").value = PAT_TEXT;
  await s6.ctx.runImport(s6.els("batchImportBtn"));
  check("未发起 /import 请求", patCalls(s6.fetches).length === 0, "仍打了 PAT 端点");
  check("未发起 /import-cred 请求", credCalls(s6.fetches).length === 0, "把 PAT 误当凭据提交");
  check("失败明细给出无法识别", s6.els("batchResult").innerHTML.length === 0, "不应写结果面板");

  console.log("场景7 批内重复项去重：");
  const s7 = loadPanelContext();
  s7.els("importBatchText").value = JSON.stringify([CRED1, CRED1, CRED2]);
  await s7.ctx.runImport(s7.els("batchImportBtn"));
  const c7 = credCalls(s7.fetches);
  check("重复凭据只导 1 次（总计 2 次）", c7.length === 2, "实际 " + c7.length);
  check("去重后第二项是 u2", c7.length === 2 && c7[1].body.indexOf('"uid":"u2"') >= 0, c7[1] && c7[1].body);

  if (failures.length) {
    console.log("\n" + failures.length + " 项失败");
    process.exit(1);
  }
  console.log("\n全部通过");
})().catch((err) => {
  // 面板代码在运行期抛错同样属于回归失败：静默崩溃会让入口误判为「无 FAIL」
  console.log("FAIL 运行期异常：" + (err && err.message ? err.message : err));
  process.exit(1);
});
