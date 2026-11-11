# 测试记录：QoderWork 导入弹窗改为只认凭据 JSON（移除 PAT 逻辑，0.13.16）

- 测试时间：2026-11-11 22:26 - 22:56
- 被测版本：qoderwork-provider 0.13.15（基线）→ 0.13.16
- 测试入口：
  - `node test/qoderwork/panel_batch_import.mjs`
  - `node test/qoderwork/panel_batch_import_e2e.mjs`
  - `python scripts/cgo-shim-build.py qoderwork`
- 关联需求文档：`doc/2-需求/2026-11-07_011000_QoderWork面板批量导入.md`（本轮为其口径收敛）

## 1. 结论

三条测试链路全绿；**5 组变异反证全部 KILLED**；PAT 入口在前端、后端路由、
白名单审计三处均已不存在。导入弹窗从「PAT 单条 + PAT 批量 + 单 PAT 按钮」
三层收敛为「选文件 + 一个文本框 + 一个按钮」，全部输入统一走 `/import-cred` 离线回灌。

## 2. 变更目标

用户在生产面板指出导入弹窗仍要求 `pt-` 开头的 PAT，与「改成只要选择或者输入
JSON 就导入就可以了，不需要 PAT 的这些逻辑」不符。本轮按此收敛，不保留 PAT
兼容路径。

| 维度 | 变更前 | 变更后 |
|---|---|---|
| 弹窗结构 | 上层 `#importPAT` 单条输入 + 下层批量区块 + 底部「导入并保存（单个 PAT）」 | 选文件行 + 单个大文本框 + 底部唯一「导入并保存」 |
| 输入识别 | PAT 列表 / `{pats:[]}` / `{tokens:[]}` / `personalToken` 分流 + 凭据形态 | 仅凭据形态（嵌套 / 平铺 / 数组 / 导出包装 / JSONL） |
| 端点 | PAT → `POST /import`；凭据 → `POST /import-cred` | 全部 → `POST /import-cred` |
| 前端函数 | `importAuth` / `runBatchImport` / `parseBatchInput` / `describeBatchItems` / `isPATToken` / `credentialPATToken` / `PAT_RE` | `runImport` / `parseImportInput` / `describeImportItems` |
| 后端 | `handleImportPAT` + `/import` 路由 + `mutatingManagementPath` 条目 | 全部删除，仅保留 `handleImportCred` |

## 3. 本地构建与测试

```text
[cgo-shim] shim dir: F:\cpa-plugin\cpa-shim-ck8tpmgp\qoderwork
[cgo-shim] running: go build ./... ...
[cgo-shim] OK: go build ./...
[cgo-shim] running: go vet ./... ...
[cgo-shim] OK: go vet ./...
[cgo-shim] running: go test ./... ...
ok  	github.com/luode0320/cpa-workbuddy-plugin/qoderwork	15.041s
[cgo-shim] OK: go test ./...
[cgo-shim] all green (qoderwork)
```

面板脚本（两个 `<script>` 块）：`SYNTAX OK`（node `new Function()` 编译校验）。

```
script blocks: 2
SYNTAX OK
```

## 4. 前端回归：解析层（panel_batch_import.mjs，16 条）

| 用例 | 锁定行为 | 结果 |
|---|---|---|
| 单条嵌套凭据 | `{auth:{accessToken},account:{uid}}` → 1 条 cred | PASS |
| 单条平铺凭据 | `{accessToken,uid}` → 1 条 cred | PASS |
| 裸数组 | 逐元素展开为 2 条 cred | PASS |
| 导出备份包装 | `accounts[].credential` 展开为 2 条 | PASS |
| 导出包装 credential 缺失 | 回退为整条对象 | PASS |
| JSONL | 每行一个对象 → 2 条 | PASS |
| 含 personalToken 的完整凭据 | 仍按 cred 处理，不丢 accessToken | PASS |
| 无关 JSON 对象 | 判 bad，不硬当凭据 | PASS |
| 裸 PAT 文本 | 判 bad（PAT 入口已移除） | PASS |
| 非法 JSON | 逐行判 bad | PASS |
| 空输入 | 返回空数组 | PASS |
| 识别预览文案 | 只统计凭据与失败行，不含 PAT 字样 | PASS |
| DOM 契约 | 10 个导入元素齐全 | PASS |
| PAT 入口已彻底移除 | 无 `#importPAT` / `importAuth` / `parseBatchInput` / `PAT_RE` / `api("/import",` | PASS |
| 导入统一走 /import-cred | 存在 `api("/import-cred",` | PASS |
| script 块语法 | 2 块全部可编译 | PASS |

## 5. 前端回归：端到端（panel_batch_import_e2e.mjs，26 条）

Node `vm` + DOM 桩真实执行 `panel.html` 内联脚本，断言「点一次导入后端实际收到什么」。

| 场景 | 断言要点 | 结果 |
|---|---|---|
| 1 凭据数组 | 2 次 `/import-cred`、各带对应 uid、**零 `/import`**、成功关弹窗清输入复位按钮 | PASS |
| 2 导出备份包装 | 展开为 2 次 `/import-cred`、零 `/import` | PASS |
| 3 JSONL | 2 次 `/import-cred`、零 `/import` | PASS |
| 4 单项失败 | 3 项全尝试、业务失败与网络异常都进明细、弹窗保留、进度清空、重试文本只留 u2/u3 | PASS |
| 5 空输入 | 零请求 | PASS |
| 6 只贴 PAT 文本 | 零 `/import`、零 `/import-cred`、不写结果面板 | PASS |
| 7 批内去重 | `[u1,u1,u2]` → 只发 2 次 | PASS |

## 6. 变异反证（5 组，全部 KILLED）

| 变异 | 手法 | 结果 | 命中断言 |
|---|---|---|---|
| M1 端点回退 | `api("/import-cred"` → `api("/import"` | KILLED | 「PAT 入口已彻底移除」「导入统一走 /import-cred」 |
| M2 凭据首判失效 | `if(isCredentialDoc(node))` → `if(false)` | 等价变异 | `looksQoderCredential` 兜底仍命中，属预期；不作为有效反证 |
| M3 失败项回填失效 | 重试回填改为空串 | KILLED | 「重试文本保留业务失败项」「重试文本保留异常项」 |
| M4 导出包装展开失效 | `if(Array.isArray(node.accounts))` → `if(false)` | KILLED | 「导出备份包装：取 accounts[].credential」「导出包装：credential 缺失时整条回退」 |
| M5 吞掉单项网络异常 | catch 分支改为 `void 0` | KILLED | 「请求异常也被记为失败」「重试文本保留异常项」 |

M2 记录为等价变异：`isCredentialDoc` 提前返回被短路后，同函数末尾的
`looksQoderCredential(node)` 分支仍会把带 `auth` 的对象判成 cred，行为不变。
真正会改变行为的是把两者同时短路，已由 M4 覆盖同类结构性变异。

## 7. 后端契约核对

| 项 | 核对方式 | 结果 |
|---|---|---|
| `POST /import` 路由已移除 | `grep '"\/import"' *.go` | 0 命中 |
| `handleImportPAT` 已移除 | `grep handleImportPAT *.go` | 0 命中 |
| `mutatingManagementPath` 无 `/import` | 人工核对 switch 列表 | 仅剩 `/import-cred` |
| 白名单审计允许表 | `failover_disable_test.go` 删除 `handleImportPAT` 条目 | go test 通过（该审计会因残留条目之外的原因失败，故必须同步） |
| 未使用的 import | `credits_handler.go` 仍用 `pluginabi`/`filepath`/`time`/`fmt`/`json`/`strings` | go build 通过，无 unused 报错 |

## 8. 遗留与边界

- 面板缓存的 HTML 由 `go:embed panel.html` 编进 `.so`，需重新构建发布才生效；
  本轮未做生产部署。
- `PersonalToken`（`pt-`）字段仍保留在 auth 文件与 keepalive 逻辑中（历史文件
  兼容与 `jrt-` 刷新失败兜底），本轮只移除「面板导入 PAT」这条入口，不动存量
  数据与刷新链路。
- 版本三轨已同步到 0.13.16：`qoderwork/VERSION`、`main.go var version`；
  `registry.json` 待发布流程同步（本轮未推送）。
