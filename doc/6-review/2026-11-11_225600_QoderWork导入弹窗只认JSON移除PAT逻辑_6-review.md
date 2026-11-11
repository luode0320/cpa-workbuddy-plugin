# 6-review — QoderWork 导入弹窗改为只认凭据 JSON（移除 PAT 逻辑，0.13.16）

## 结论

STYLE: PASS

## 检查范围

本轮改动 12 个文件：

| 文件 | 改动性质 |
|---|---|
| `qoderwork/panel.html` | 导入弹窗结构收敛（删 `#importPAT` 输入框与批量区块外壳）；`importAuth` / `runBatchImport` / `parseBatchInput` / `describeBatchItems` / `isPATToken` / `credentialPATToken` / `PAT_RE` / `BATCH_GAP_MS` 删除；新增 `parseImportInput` / `describeImportItems` / `runImport`；样式类 `.import-batch` 删除 |
| `qoderwork/management.go` | 删除 `POST /import` 路由注册、handler 分支与 `mutatingManagementPath` 条目；`/import-cred` 描述改写 |
| `qoderwork/credits_handler.go` | 删除 `handleImportPAT`（67 行）；`handleImportCred` 注释合并原两条注释的有效信息 |
| `qoderwork/failover_disable_test.go` | 白名单审计允许表删除已不存在的 `handleImportPAT` 条目 |
| `qoderwork/main.go` | `var version` 0.13.15 → 0.13.16 |
| `qoderwork/VERSION` | 0.13.15 → 0.13.16 |
| `qoderwork/README.md` | 方式二/方式三合并为「凭据导入（JSON，支持批量）」；功能表与首段去掉「PAT 导入」表述 |
| `qoderwork/CHANGELOG.md` | 新增 0.13.16 条目 |
| `test/qoderwork/panel_batch_import.mjs` | 断言重写：去掉 PAT 用例，新增「PAT 文本判 bad」「PAT 入口已彻底移除」 |
| `test/qoderwork/panel_batch_import_e2e.mjs` | 断言重写：新增「只贴 PAT 零请求」「零 /import 请求」；场景改走 `/import-cred` |
| `doc/2-需求/2026-11-07_011000_QoderWork面板批量导入.md` | 追加第 6 节「口径收敛」，标注第 1-5 节 PAT 部分作废 |
| `doc/5-tests/2026-11-11_225600_...验证.md` | **新增**测试主文档 |

## 逐项检查

### 格式与排版

- 6 个非 Go 文件统一 LF + UTF-8 无 BOM（逐字节统计换行符确认：`CRLF=0`）。
- Go 文件走 `cgo-shim` 的 build/vet/test，全部通过；`gofmt -l` 对 `management.go` 报差异，但**该差异在 HEAD 基线就存在**（`StreakDays` 字段行尾对齐与一处空注释行），本轮改动未触碰这两处，按「不为格式化扩大无关 diff」处理，不做顺手修复。
- 缩进：`panel.html` 保持 2 空格与既有 `<div>` 层级；Go 保持 tab。未做批量重排。
- 删除 `.import-batch` 样式类后复查：该类在文件中仅剩 1 处引用（即被删的容器），无孤儿样式规则。

### 命名

- `parseBatchInput` → `parseImportInput`、`describeBatchItems` → `describeImportItems`、`runBatchImport` → `runImport`：随功能收敛改名，去掉已不准确的「batch」语义（现在单条与批量同入口）。
- `batchRunning` → `importRunning`，与新函数名一致；`BATCH_GAP_MS` 删除（该间隔只为 PAT 打上游而设，离线回灌无突发风险）。
- DOM id 保留 `importBatchText` / `batchImportBtn` / `batchPreview` / `batchProgress` / `batchResult`：属既有 id，改名会连带破坏外部用例与浏览器缓存中的脚本引用，且「batch」在此仍指批量场景，语义不冲突。
- Go 侧无新增符号，只做删除；`handleImportCred` 签名未变。

### 注释

- 全部新增/改写注释为简体中文，符合仓库口径。
- 新函数块注释写明「唯一入口：只认凭据 JSON，全部走 /import-cred 离线回灌」与四种输入形态，保留「为什么」而非复述代码。
- `handleImportCred` 注释合并了原 `handleImportPAT` 与 `handleImportCred` 两条注释的有效信息（「面板拆分包装/数组/JSONL 后逐条投递」「原样落盘保留重建会丢的字段」），删除仅对 PAT 成立的「上游换 token」描述。
- 导出函数的注释里 `导入 (parseImportInput expands accounts[].credential)` 同步改名，避免注释指向已不存在的函数。
- 删除处未留「此处曾有 PAT」类历史注释：`CHANGELOG` 已完整记录，代码内保留只会成为噪音。

### 日志

- 本轮未新增/修改任何日志调用。
- `handleImportPAT` 删除后，其失败路径（`jobToken exchange` / `host.auth.save` 错误串）随之消失，属预期：面板不再触发该路径。
- `handleImportCred` 的错误串保持原样（`credential parse:` / `credential has empty uid` / `host.auth.save`），前端失败明细依赖这些文案，测试已断言 `credential has empty uid` 可透出。

### 错误处理

- 前端 `runImport` 保持既有双通道：业务失败（`success:false`）与网络异常（`catch`）都进 `fail[]`，不因收敛而丢一路。E2E 场景 4 与变异 M5 双向锁定。
- 空输入 / 全是无关 JSON / 全是重复项三种拒绝路径各自给出不同 toast 文案，不合并成一句「请重试」。
- 后端 `handleImportCred` 的校验链未改：`cred==nil` → `parseStored` → `uid` 非空 → `host.auth.save`，任一步失败都返回结构化 `{success:false,error}`，前端可原地重试。

### 目录归位与复用

- 前端测试沿用根 `test/` 的源码镜像目录 `test/qoderwork/`，与既有 `panel_test_model_repro.mjs` 同级，未新建目录。
- 测试主文档落 `doc/5-tests/` 扁平 md，6-review 落 `doc/6-review/`，符合 `artifact-storage-rules`。
- 复用既有工具：`esc()` / `busy()` / `toast()` / `hideBatchResult()` / `showBatchResult()` / `clearImportFiles()` 全部原样调用，未新写等价 helper。
- `parseImportInput` 复用了既有的 `isCredentialDoc` / `looksQoderCredential` 两个判定函数（未重写判定逻辑），只删掉 PAT 相关的第三个判定 `credentialPATToken`。

### 测试资产

- 两个 `.mjs` 测试都可通过 `process.argv[2]` 指向变异副本做反证，本轮 5 组变异验证中 4 组 KILLED、1 组判为等价变异并记录原因，未把等价变异伪装成有效反证。
- 测试临时副本落在 `$env:TEMP`，收口已删除；`cpa-shim-*` 构建目录已清理（`Count = 0`）。

### 越界自查

- 未改动任何其他插件的 `panel.html`（`workbuddy` / `traework` / `coze` 等仍各自保留自己的导入实现与 PAT 路径）。
- 未迁移 auth 文件里的 `personalToken` 字段，未改 keepalive 的 `jrt-` 刷新兜底链路 —— 本轮只删「面板导入 PAT」入口。
- `git status` 中新出现的 `doc/2-需求/2026-10-25_025849_new-api经CPA调用Coze视频生成模型.md` 属并行会话产物，不在本轮改动范围，未纳入。

## 结论

当前改动与项目既有写法一致（同一弹窗的 DOM 结构、样式类命名、toast/busy 调用方式、测试的 vm+DOM 桩模式均为跟随既有实现），未引入局部风格跳变。
