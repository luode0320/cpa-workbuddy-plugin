#!/usr/bin/env node
/**
 * coze 面板「导出备份 / 导入备份」前端逻辑回归。
 *
 * 为什么需要它：这两个入口的关键风险不在后端（已由 coze/backup_test.go 覆盖），
 * 而在前端展开与串联——备份文件有三种合法形态（导出包装 / 裸数组 / 单条），
 * 展开错一种就会出现「文件选对了却提示无法解析」或「只导入了第一个账号」。
 * 同时锁住按钮与弹窗的 DOM 契约，避免改动 HTML 后事件绑定静默失效。
 *
 * 用法：node test/coze/panel_backup.mjs
 * 退出码 0 = 全部通过。
 */
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";

const panelPath = path.join(process.cwd(), "coze", "panel.html");
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

// 从面板 HTML 里抽出 expandCredentials（纯函数，无 DOM 依赖）单独执行。
const fnMatch = html.match(/function expandCredentials\(text\)\{[\s\S]*?\n\}/);
assert(fnMatch, "未找到 expandCredentials，面板结构可能已变更");
const sandbox = {};
vm.createContext(sandbox);
vm.runInContext(fnMatch[0] + "\nthis.expandCredentials = expandCredentials;", sandbox);
const expand = sandbox.expandCredentials;

check("导出包装：按 accounts[].credential 展开", () => {
  const doc = {
    version: 1,
    plugin: "coze-provider",
    accounts: [
      { auth_id: "coze-a", credential: { cookie_header: "sessionid=a" } },
      { auth_id: "coze-b", credential: { cookie_header: "sessionid=b" } },
    ],
  };
  const out = expand(JSON.stringify(doc));
  assert(Array.isArray(out) && out.length === 2, `应展开 2 条，实际 ${out && out.length}`);
  assert(out[0].cookie_header === "sessionid=a", "第一条凭据内容错误");
});

check("导出包装：credential 缺失时整条回退", () => {
  const doc = { accounts: [{ cookie_header: "sessionid=raw" }] };
  const out = expand(JSON.stringify(doc));
  assert(out.length === 1 && out[0].cookie_header === "sessionid=raw", "应回退为整条对象");
});

check("裸数组：逐元素展开", () => {
  const out = expand(JSON.stringify([{ cookie_header: "a" }, { cookie_header: "b" }]));
  assert(out.length === 2, `应展开 2 条，实际 ${out && out.length}`);
});

check("单条凭据：作为一条展开", () => {
  const out = expand(JSON.stringify({ cookie_header: "solo" }));
  assert(out.length === 1 && out[0].cookie_header === "solo", "单条凭据展开失败");
});

check("非法 JSON：返回 null 触发报错分支", () => {
  assert(expand("{not json") === null, "非法 JSON 应返回 null");
});

check("空对象数组：返回空数组而非 null", () => {
  const out = expand(JSON.stringify({ accounts: [] }));
  assert(Array.isArray(out) && out.length === 0, "空 accounts 应得到空数组");
});

check("按钮与弹窗 DOM 契约齐全", () => {
  for (const id of [
    "btnExport",
    "btnImport",
    "importModal",
    "importModalClose",
    "importCancel",
    "importConfirm",
    "importRaw",
    "importFiles",
    "importProgress",
    "importResult",
  ]) {
    assert(html.includes(`id="${id}"`), `缺少元素 #${id}`);
  }
});

check("事件绑定覆盖新增入口", () => {
  for (const frag of [
    'getElementById("btnExport").addEventListener',
    'getElementById("btnImport").addEventListener',
    'getElementById("importConfirm").addEventListener',
    'getElementById("importModal").addEventListener',
  ]) {
    assert(html.includes(frag), `缺少绑定：${frag}`);
  }
});

check("导出/导入请求路径与后端路由一致", () => {
  assert(html.includes('api("/export")'), "导出未调用 /export");
  assert(html.includes('api("/import-cred"'), "导入未调用 /import-cred");
});

check("样式类 file-row 已定义", () => {
  assert(/\.file-row\s*\{/.test(html), "缺少 .file-row 样式定义");
});

check("面板两个 script 块语法有效", () => {
  const blocks = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
  assert(blocks.length >= 2, `script 块数量异常：${blocks.length}`);
  blocks.forEach((code, i) => {
    try {
      new Function(code);
    } catch (err) {
      throw new Error(`第 ${i} 个 script 块语法错误：${err.message}`);
    }
  });
});

console.log(failures === 0 ? "\n全部通过" : `\n${failures} 项失败`);
process.exit(failures === 0 ? 0 : 1);
