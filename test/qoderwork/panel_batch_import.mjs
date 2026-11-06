#!/usr/bin/env node
/**
 * qoderwork 面板「批量导入」前端解析逻辑回归。
 *
 * 为什么需要它：批量导入的全部风险都在「识别错形态」——把导出备份当成单条
 * 凭据只会写进一个账号；把带 accessToken 的完整凭据当成 PAT 会白白打一次
 * 上游换 token，还可能因 PAT 缺失而失败；JSONL 少认一行就少导一个号。
 * 这些错误不会让页面报错，只会静默少导，所以必须用纯函数断言锁死。
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

const patLine = html.match(/const PAT_RE=\/\^pt-\[A-Za-z0-9_-\]\+\$\/;/);
assert(patLine, "未找到 PAT_RE 常量定义");

const fnNames = [
  "isPATToken",
  "isCredentialDoc",
  "credentialPATToken",
  "looksQoderCredential",
  "parseBatchInput",
  "describeBatchItems",
];
const source = [patLine[0], ...fnNames.map((n) => extractFunction(html, n))].join("\n");
const sandbox = {};
vm.createContext(sandbox);
vm.runInContext(
  source +
    "\nthis.ns={parseBatchInput,describeBatchItems,isPATToken,credentialPATToken};",
  sandbox
);
const { parseBatchInput, describeBatchItems, isPATToken } = sandbox.ns;

const PAT1 = "pt-aaaaaaaaaaaa1111";
const PAT2 = "pt-bbbbbbbbbbbb2222";
const PAT3 = "pt-cccccccccccc3333";

function kinds(items) {
  return items.map((x) => x.kind).join(",");
}

check("单条 PAT 文本", () => {
  const out = parseBatchInput(PAT1);
  assert(out.length === 1, `应识别 1 项，实际 ${out.length}`);
  assert(out[0].kind === "pat" && out[0].value === PAT1, "单 PAT 识别错误");
});

check("多行 PAT（每行一个）", () => {
  const out = parseBatchInput(`${PAT1}\n${PAT2}\r\n${PAT3}\n`);
  assert(kinds(out) === "pat,pat,pat", `应识别 3 个 PAT，实际 ${kinds(out)}`);
  assert(out[2].value === PAT3, "第三个 PAT 内容错误");
});

check("一行内逗号/空格分隔多个 PAT", () => {
  const out = parseBatchInput(`${PAT1}, ${PAT2}  ${PAT3}`);
  assert(kinds(out) === "pat,pat,pat", `应识别 3 个 PAT，实际 ${kinds(out)}`);
});

check("裸数组：凭据对象逐元素展开", () => {
  const doc = [
    { auth: { accessToken: "jt-1" }, account: { uid: "u1" } },
    { auth: { accessToken: "jt-2" }, account: { uid: "u2" } },
  ];
  const out = parseBatchInput(JSON.stringify(doc));
  assert(kinds(out) === "cred,cred", `应识别 2 条凭据，实际 ${kinds(out)}`);
  assert(out[0].value.account.uid === "u1", "第一条凭据内容错误");
});

check("裸数组：pt- 字符串元素按 PAT 处理", () => {
  const out = parseBatchInput(JSON.stringify([PAT1, PAT2]));
  assert(kinds(out) === "pat,pat", `应识别 2 个 PAT，实际 ${kinds(out)}`);
});

check("数组混排：凭据与 PAT 各自分流", () => {
  const doc = [{ auth: { accessToken: "jt-9" }, account: { uid: "u9" } }, PAT1];
  const out = parseBatchInput(JSON.stringify(doc));
  assert(kinds(out) === "cred,pat", `分流错误：${kinds(out)}`);
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
  const out = parseBatchInput(JSON.stringify(doc));
  assert(kinds(out) === "cred,cred", `应展开 2 条凭据，实际 ${kinds(out)}`);
  assert(out[1].value.account.uid === "ub", "第二条凭据内容错误");
});

check("导出包装：credential 缺失时整条回退", () => {
  const doc = { accounts: [{ auth: { accessToken: "jt-x" }, account: { uid: "ux" } }] };
  const out = parseBatchInput(JSON.stringify(doc));
  assert(kinds(out) === "cred" && out[0].value.account.uid === "ux", "应回退为整条对象");
});

check("单条嵌套凭据", () => {
  const out = parseBatchInput(JSON.stringify({ auth: { accessToken: "jt-solo" }, account: { uid: "solo" } }));
  assert(kinds(out) === "cred" && out[0].value.auth.accessToken === "jt-solo", "单条凭据识别失败");
});

check("单条 flat 凭据（accessToken + uid 平铺）", () => {
  const out = parseBatchInput(JSON.stringify({ accessToken: "dt-flat", uid: "flat-uid", nickname: "n" }));
  assert(kinds(out) === "cred" && out[0].value.uid === "flat-uid", "flat 凭据识别失败");
});

check("JSONL：每行一个凭据对象", () => {
  const text =
    JSON.stringify({ auth: { accessToken: "jt-1" }, account: { uid: "u1" } }) +
    "\n" +
    JSON.stringify({ auth: { accessToken: "jt-2" }, account: { uid: "u2" } }) +
    "\n";
  const out = parseBatchInput(text);
  assert(kinds(out) === "cred,cred", `JSONL 应识别 2 条，实际 ${kinds(out)}`);
});

check("JSONL + PAT 混排", () => {
  const text = JSON.stringify({ auth: { accessToken: "jt-1" }, account: { uid: "u1" } }) + "\n" + PAT2;
  const out = parseBatchInput(text);
  assert(kinds(out) === "cred,pat", `混排识别错误：${kinds(out)}`);
});

check("PAT 容器：{pats:[...]} / {tokens:[...]}", () => {
  const a = parseBatchInput(JSON.stringify({ pats: [PAT1, PAT2] }));
  assert(kinds(a) === "pat,pat", `pats 容器识别错误：${kinds(a)}`);
  const b = parseBatchInput(JSON.stringify({ tokens: [PAT3] }));
  assert(kinds(b) === "pat", `tokens 容器识别错误：${kinds(b)}`);
});

check("PAT 对象：personalToken 字段走 PAT 路径", () => {
  const out = parseBatchInput(JSON.stringify({ personalToken: PAT1 }));
  assert(kinds(out) === "pat" && out[0].value === PAT1, "personalToken 应识别为 PAT");
});

check("关键回归：含 personalToken 的完整凭据仍按凭据处理", () => {
  // 若判定顺序反过来，这里会被当成 PAT，丢掉已有的 accessToken 会话令牌。
  const doc = { auth: { accessToken: "jt-live", personalToken: PAT1 }, account: { uid: "u-keep" } };
  const out = parseBatchInput(JSON.stringify([doc]));
  assert(kinds(out) === "cred", `完整凭据被误判为 ${kinds(out)}`);
  assert(out[0].value.auth.accessToken === "jt-live", "凭据内容被篡改");
});

check("无关 JSON 对象：标记为无法识别而不是硬当凭据", () => {
  const out = parseBatchInput(JSON.stringify({ foo: 1 }));
  assert(kinds(out) === "bad", `无关对象应为 bad，实际 ${kinds(out)}`);
});

check("非法 JSON 文本：逐行标记为无法识别", () => {
  const out = parseBatchInput("{not json");
  assert(out.length === 1 && out[0].kind === "bad", "非法输入应得到 1 条 bad");
});

check("空输入：返回空数组", () => {
  assert(parseBatchInput("   \n  ").length === 0, "空输入不应产生条目");
  assert(parseBatchInput(undefined).length === 0, "undefined 不应产生条目");
});

check("PAT 判定不接受非 pt- 前缀", () => {
  assert(!isPATToken("jt-xxxx"), "jt- 不应被当成 PAT");
  assert(!isPATToken("ptx-xxxx"), "ptx- 不应被当成 PAT");
  assert(isPATToken(" " + PAT1 + " "), "带空白的 PAT 应被接受");
});

check("识别预览文案包含三类计数", () => {
  const items = [
    { kind: "pat", value: PAT1 },
    { kind: "cred", value: {} },
    { kind: "bad", value: "x" },
  ];
  const text = describeBatchItems(items);
  assert(/1 个 PAT/.test(text), `缺少 PAT 计数：${text}`);
  assert(/1 个凭据/.test(text), `缺少凭据计数：${text}`);
  assert(/1 行无法识别/.test(text), `缺少失败计数：${text}`);
});

check("DOM 契约：批量导入元素齐全", () => {
  for (const id of [
    "importModal",
    "importPAT",
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

check("旧的单段备份恢复入口已移除", () => {
  assert(!html.includes("function importCred("), "旧 importCred 未删除");
  assert(!html.includes('id="importCredText"'), "旧 importCredText 未删除");
  assert(!html.includes("function expandCredentials("), "旧 expandCredentials 未删除");
});

check("批量导入按类型分流到 /import 与 /import-cred", () => {
  assert(html.includes('api("/import",'), "PAT 未调用 /import");
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
