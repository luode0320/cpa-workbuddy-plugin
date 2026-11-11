#!/usr/bin/env node
/**
 * qoderwork 面板「凭据导入」前端解析逻辑回归。
 *
 * 为什么需要它：导入的全部风险都在「识别错形态」——把导出备份当成单条
 * 凭据只会写进一个账号；JSONL 少认一行就少导一个号；把无关 JSON 硬当凭据
 * 会让后端逐条报 parse_error。这些错误不会让页面崩，只会静默少导，所以
 * 必须用纯函数断言锁死。
 *
 * 2026-11-11 起导入只认凭据 JSON（PAT 入口已移除）：面板不再识别 pt- 文本，
 * 全部凭据走 /import-cred 离线回灌。本文件同时锁住「PAT 逻辑不再回归」。
 *
 * 用法：node test/qoderwork/panel_batch_import.mjs [panel-html-path]
 *   （可选指定面板路径，用于对历史版本或变异副本做反证验证）
 * 退出码 0 = 全部通过。
 */
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";

const panelPath = process.argv[2]
  ? path.resolve(process.argv[2])
  : path.join(process.cwd(), "qoderwork", "panel.html");
const html = fs.readFileSync(panelPath, "utf8");

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log(`PASS ${name}`);
  } catch (err) {
    failures++;
    console.log(`FAIL ${name}: ${err.message}`);
  }
}
function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

/* 按大括号配平抽取一个函数体，跳过字符串与注释里的括号。
   面板里的函数会被 bind 到 DOM 事件，不能整段执行，只能抽纯函数。 */
function extractFunction(src, name) {
  const marker = `function ${name}(`;
  const start = src.indexOf(marker);
  if (start < 0) throw new Error(`未找到函数 ${name}，面板结构可能已变更`);
  let i = src.indexOf("{", start);
  if (i < 0) throw new Error(`函数 ${name} 缺少函数体`);
  let depth = 0;
  for (; i < src.length; i++) {
    const ch = src[i];
    const next = src[i + 1];
    if (ch === '"' || ch === "'" || ch === "`") {
      const quote = ch;
      i++;
      while (i < src.length) {
        if (src[i] === "\\") { i += 2; continue; }
        if (src[i] === quote) break;
        i++;
      }
      continue;
    }
    if (ch === "/" && next === "/") {
      while (i < src.length && src[i] !== "\n") i++;
      continue;
    }
    if (ch === "/" && next === "*") {
      i += 2;
      while (i < src.length && !(src[i] === "*" && src[i + 1] === "/")) i++;
      i++;
      continue;
    }
    if (ch === "{") depth++;
    else if (ch === "}") {
      depth--;
      if (depth === 0) return src.slice(start, i + 1);
    }
  }
  throw new Error(`函数 ${name} 大括号不平衡`);
}

const fnNames = ["isCredentialDoc", "looksQoderCredential", "parseImportInput", "describeImportItems"];
const source = fnNames.map((n) => extractFunction(html, n)).join("\n");
const sandbox = {};
vm.createContext(sandbox);
vm.runInContext(
  source + "\nthis.ns={parseImportInput,describeImportItems,isCredentialDoc};",
  sandbox
);
const { parseImportInput, describeImportItems } = sandbox.ns;

const CRED_A = { auth: { accessToken: "jt-1" }, account: { uid: "u1" } };
const CRED_B = { auth: { accessToken: "jt-2" }, account: { uid: "u2" } };

function kinds(items) {
  return items.map((x) => x.kind).join(",");
}

check("单条嵌套凭据", () => {
  const out = parseImportInput(JSON.stringify(CRED_A));
  assert(out.length === 1, `应识别 1 项，实际 ${out.length}`);
  assert(out[0].kind === "cred", `单条凭据识别为 ${out[0].kind}`);
  assert(out[0].value.auth.accessToken === "jt-1", "凭据内容错误");
});

check("单条平铺凭据（accessToken + uid 平铺）", () => {
  const out = parseImportInput(JSON.stringify({ accessToken: "dt-flat", uid: "flat-uid", nickname: "n" }));
  assert(kinds(out) === "cred" && out[0].value.uid === "flat-uid", "平铺凭据识别失败");
});

check("裸数组：逐元素展开", () => {
  const out = parseImportInput(JSON.stringify([CRED_A, CRED_B]));
  assert(kinds(out) === "cred,cred", `应识别 2 条凭据，实际 ${kinds(out)}`);
  assert(out[0].value.account.uid === "u1", "第一条凭据内容错误");
  assert(out[1].value.account.uid === "u2", "第二条凭据内容错误");
});

check("导出备份包装：取 accounts[].credential", () => {
  const doc = {
    version: 1,
    plugin: "qoderwork-provider",
    accounts: [
      { auth_index: "a1", credential: { auth: { accessToken: "jt-a" }, account: { uid: "ua" } } },
      { auth_index: "a2", credential: { auth: { accessToken: "jt-b" }, account: { uid: "ub" } } },
    ],
  };
  const out = parseImportInput(JSON.stringify(doc));
  assert(kinds(out) === "cred,cred", `应展开 2 条凭据，实际 ${kinds(out)}`);
  assert(out[1].value.account.uid === "ub", "第二条凭据内容错误");
});

check("导出包装：credential 缺失时整条回退", () => {
  const doc = { accounts: [{ auth: { accessToken: "jt-x" }, account: { uid: "ux" } }] };
  const out = parseImportInput(JSON.stringify(doc));
  assert(kinds(out) === "cred" && out[0].value.account.uid === "ux", "应回退为整条对象");
});

check("JSONL：每行一个凭据对象", () => {
  const text = JSON.stringify(CRED_A) + "\n" + JSON.stringify(CRED_B) + "\n";
  const out = parseImportInput(text);
  assert(kinds(out) === "cred,cred", `JSONL 应识别 2 条，实际 ${kinds(out)}`);
});

check("含 personalToken 的完整凭据仍按凭据处理", () => {
  const doc = { auth: { accessToken: "jt-live", personalToken: "pt-aaaaaaaaaaaa1111" }, account: { uid: "u-keep" } };
  const out = parseImportInput(JSON.stringify([doc]));
  assert(kinds(out) === "cred", `完整凭据被误判为 ${kinds(out)}`);
  assert(out[0].value.auth.accessToken === "jt-live", "凭据内容被篡改");
});

check("无关 JSON 对象：标记为无法识别而不是硬当凭据", () => {
  const out = parseImportInput(JSON.stringify({ foo: 1 }));
  assert(kinds(out) === "bad", `无关对象应为 bad，实际 ${kinds(out)}`);
});

check("PAT 文本不再是合法输入（PAT 入口已移除）", () => {
  const out = parseImportInput("pt-aaaaaaaaaaaa1111");
  assert(out.length === 1 && out[0].kind === "bad", "裸 PAT 文本应被判为无法识别");
});

check("非法 JSON 文本：逐行标记为无法识别", () => {
  const out = parseImportInput("{not json");
  assert(out.length === 1 && out[0].kind === "bad", "非法输入应得到 1 条 bad");
});

check("空输入：返回空数组", () => {
  assert(parseImportInput("   \n  ").length === 0, "空输入不应产生条目");
  assert(parseImportInput(undefined).length === 0, "undefined 不应产生条目");
});

check("识别预览只统计凭据与失败行", () => {
  const items = [{ kind: "cred", value: {} }, { kind: "bad", value: "x" }];
  const text = describeImportItems(items);
  assert(/1 个凭据/.test(text), `缺少凭据计数：${text}`);
  assert(/1 行无法识别/.test(text), `缺少失败计数：${text}`);
  assert(!/PAT/.test(text), `文案不应再出现 PAT：${text}`);
});

check("DOM 契约：导入弹窗元素齐全", () => {
  for (const id of [
    "importModal",
    "importFiles",
    "importFileList",
    "importFileInfo",
    "clearImportFiles",
    "importBatchText",
    "batchPreview",
    "batchProgress",
    "batchResult",
    "batchImportBtn",
  ]) {
    assert(html.includes(`id="${id}"`), `缺少元素 #${id}`);
  }
});

check("PAT 入口已彻底移除", () => {
  assert(!html.includes('id="importPAT"'), "旧的 #importPAT 输入框未删除");
  assert(!html.includes("function importAuth("), "旧的 importAuth 未删除");
  assert(!html.includes("parseBatchInput"), "旧 parseBatchInput 未删除");
  assert(!html.includes("PAT_RE"), "旧 PAT_RE 常量未删除");
  assert(!html.includes('api("/import",'), "面板仍会调用 PAT 导入端点 /import");
});

check("导入统一走 /import-cred", () => {
  assert(html.includes('api("/import-cred",'), "凭据未调用 /import-cred");
});

check("面板全部 script 块语法有效", () => {
  const blocks = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
  assert(blocks.length >= 2, `script 块数量异常：${blocks.length}`);
  blocks.forEach((code, i) => {
    try {
      new vm.Script(code);
    } catch (err) {
      throw new Error(`第 ${i + 1} 个 script 块语法错误：${err.message}`);
    }
  });
});

if (failures) {
  console.log(`\n${failures} 项失败`);
  process.exit(1);
}
console.log("\n全部通过");
