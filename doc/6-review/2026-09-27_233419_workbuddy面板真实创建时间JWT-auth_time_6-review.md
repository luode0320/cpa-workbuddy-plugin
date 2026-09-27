---
schema_version: 1
template_version: v1
doc_id: STYLE-WB-JWT-AUTHTIME-0.14.42-20260927
doc_type: style_regression
source_ids: [PROD-WB-CREATED-AT-20260927]
status: accepted
version: v1.0
current_slice: workbuddy 0.14.42 面板「创建」时间改用 JWT auth_time 的风格检查
updated_at: 2026-09-27 23:35:03
reader_level: business_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
---

# 6-review：workbuddy 0.14.42 面板「创建」时间改用 JWT `auth_time`

STYLE: PASS

## 检查范围

- 测试证据：`TEST-WB-CGO-SHIM-20260927`（重跑 `python scripts/cgo-shim-build.py workbuddy`：build / vet / test 全绿，go test 11.281s）、`TEST-WB-SENTINEL-20260927`（哨兵 `time.Unix(claims.AuthTime,0)` → `+1`，2 个用例 FAIL，删哨兵后全绿）
- 改动文件：`workbuddy/created_at.go`（新增）、`workbuddy/created_at_test.go`（新增）、`workbuddy/panel.go`（`CreatedAt` 取值 + 字段注释）
- 规则来源：`PROJECT_STYLE.md`、`code-style-consistency-rules` references

## 检查步骤（等价回退）

`python code-style-consistency-rules/scripts/static_owner_router.py --changed ...` 以退出码 2 失败：来源映射覆盖率断言报 `code-quality-rules/references/minimal-solution-ladder.md`、`platform-native-substitutes.md` 未登记。该两个文件位于全局 skill 目录（`D:\\谷歌云盘\\luode-skills`），不在本会话写入边界内，本轮不修改外部资产；按契约「加载失败不得降级手写步骤」保留为外部阻断，改用 `references/style-review-pipeline.md` 的九步口径逐项做等价检查并记录证据。

## 风格证据

- `STYLE-01`（格式与编码）：三文件 UTF-8 无 BOM、无尾随空白；`gofmt -l`（CRLF→LF 临时转换后）输出为空 → PASS。`workbuddy/*.go` 换行 CRLF=37 / LF=37 / MIXED=0，与既有生态一致（`.gitattributes` 为 `* text=auto`），新增两文件为 LF 不构成漂移。
- `STYLE-02`（命名与符号引用）：`parseCreatedAtFromAccessToken` 与同包既有 `parseCountersFromAuthJSON` / `parseDisabledFromAuthJSON` / `parsePreserveFromAuthJSON` / `parseTestFailedFromAuthJSON` 的 `parseXxxFromYyy` 命名完全同构；返回 `(time.Time, bool)` 与本目录 `parseModelsValue` / `parseModelsYAMLBlock` / `parseStored` 的 `(T, bool)` / `(T, error)` 双返回习惯一致 → PASS。
- `STYLE-03`（注释分层与定义位置）：函数注释为唯一完整层且解释「为什么不能用宿主 CreatedAt」（watcher 每次扫描 `time.Now()` 覆写）而非「改了什么」；字段注释保持一句 `// account creation time (RFC3339), from JWT auth_time`；无行内注释平摊 → PASS。
- `STYLE-04`（函数签名与参数结构）：`parseCreatedAtFromAccessToken(accessToken string) (time.Time, bool)` 单参数单行签名，无可选字段平铺 → PASS。
- `STYLE-05`（结构与落点）：新增文件落 `workbuddy/` 包内，与 `test_failed_tag.go` / `preserve.go` 同层；未新增目录、无依赖方向变化（仅用 `encoding/base64`、`encoding/json`、`strings`、`time` 标准库） → PASS。
- `STYLE-06`（公共复用）：`CreatedAt` 采用 3 行内联优先 / 回退结构，直接复用既有 `sa.Auth.AccessToken` 与 `acct.CreatedAt` 字段，未引入新 helper、接口或抽象层 → PASS。
- `STYLE-07`（接口契约与数据访问）：账号列表 JSON 字段 `created_at` 语义由「宿主合成时刻」改为「账号真实创建时刻」，字段名、`omitempty`、RFC3339 格式均未变，对既有面板消费方向后兼容；无新增端点、无 SQL、无时区口径改动（统一 `UTC()` 后 `Format(time.RFC3339)`） → PASS。
- `STYLE-08`（语言特异写法）：Go 侧无 `iota`、无多余 `strings.TrimSpace`；`strings.TrimRight(parts[1], "=")` 属 JWT base64 padding 的必要归一化，非随手包裹 → PASS。
- `STYLE-09`（测试资产）：新增 `workbuddy/created_at_test.go` 落在既有 workbuddy 测试文件群（本仓库 workbuddy / qoderwork 测试与源码同包，`test/` 仅新建了 `test/traework/`），跟随当前文件稳定写法；测试 helper `testJWT` 只在测试包内定义 → PASS。

## 边界声明

- 业务正确性：N/A + 原因：由真实测试与生产行为验收负责 + 证据：`TEST-WB-CGO-SHIM-20260927`、生产 54 账号 `created_at` 53 个不同值（08-20~09-27，与独立 JWT 解析逐条吻合）
- 需求覆盖与发布放行：N/A + 原因：不属于 6-review 职责 + 证据：发布链 cb0a2be → f44eb08 → 6736c90，CI run 36327182344 success
- 外部阻断：全局 skill 来源映射覆盖率漂移（`code-quality-rules/references/minimal-solution-ladder.md`、`platform-native-substitutes.md` 未登记），本会话只读不改，留待该 skill 所属会话补齐
