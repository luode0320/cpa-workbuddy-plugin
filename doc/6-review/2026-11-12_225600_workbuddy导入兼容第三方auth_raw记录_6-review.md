# 6-review 风格回归记录 — workbuddy 0.21.26（导入兼容第三方 auth_raw/profile_raw）

- 日期：2026-11-12 22:56
- 审查对象：commit `fabbed3`（6 文件 +267/−6）
- 范围：`workbuddy/auth_thirdparty.go`（新增）、`workbuddy/main.go`（`parseStored`）、`workbuddy/panel.html`（第 266 行提示）、`workbuddy/VERSION`、`workbuddy/main.go:359`、`workbuddy/CHANGELOG.md`、`test/workbuddy/auth_thirdparty_test.go`（新增）

## 检查项

| # | 检查项 | 结论 | 证据 |
|---|---|---|---|
| 1 | 改动范围最小化 | PASS | 6 文件全部服务于「导入兼容第三方形态」这一个目标；无顺手重构、无批量格式化、无无关删改。`parseStored` 只做分支分发改造，既有嵌套/扁平语义逐字保留 |
| 2 | 复用优先，不重复造轮子 | PASS | 令牌取首个非空复用既有 `firstNonEmpty`（`workbuddy/stream.go:1070`）；宽松取值复用既有 `jsonStr` / `jsonI64`（`workbuddy/billing.go:489` / `:471`）；归一化后直接产出既有 `storedAuth`，下游 10 个调用点零改动 |
| 3 | 测试文件不进生产包 | PASS | `workbuddy/auth_thirdparty.go` 内 `func Test` 计数 **0**；新增测试放 `test/workbuddy/`（`package main` 镜像目录，经 `scripts/cgo-shim-build.py:175 copy_mirrored_tests()` 带入 shim 编译），生产构建目录未被污染 |
| 4 | 新增函数注释（comment-rules） | PASS | `auth_thirdparty.go` 内 `[参数]` / `[返回]` / `最近修改时间` 元信息共 **9** 处；函数头三要素齐全，函数体按 `1.` / `1.1` 编号步骤；说明性注释单行只写目的，无论证式多行块 |
| 5 | 注释语言 | PASS | 全部简体中文；只保留字段名、错误串等必要英文标识符（`accessRaw` / `profileRaw` / `parse_error` 等） |
| 6 | 命名 | PASS | `isThirdPartyStored` / `parseThirdPartyStored` / `epochsToSeconds` / `thirdPartyRecord` 语义直白且与本文件既有 `is*` / `parse*` 前缀风格一致 |
| 7 | gofmt | PASS | `auth_thirdparty.go` / `main.go` / `auth_thirdparty_test.go` CRLF→LF 归一后 `gofmt -l` 无输出（exit=0） |
| 8 | go vet / build / test | PASS | `cgo-shim-build.py workbuddy` build / vet / test 全绿（34.078s），版本 bump 后复跑仍全绿 |
| 9 | 边界与错误处理 | PASS | 空输入、非法 JSON、缺 `accessToken` 三条路径都返回既有错误文案（`empty auth storage` / `storage_parse_error: %w` / `parse_error: missing accessToken`），未新增错误语义，未吞异常 |
| 10 | 毫秒/秒换算健壮性 | PASS | `epochsToSeconds` 用 `v > 1e11` 阈值判毫秒，非正值返回 0；`TestEpochsToSeconds` 覆盖秒级、毫秒级、0、负值四类输入 |
| 11 | 日志规范 | PASS | 本轮无新增日志输出（纯解析逻辑）；失败路径沿用既有 `fmt.Errorf` 包装模式 |
| 12 | UI 文案 | PASS | `panel.html:266` 只补一种格式说明，与既有三种形态并列，用语与原句同构；无 HTML 标签误写、无 emoji 噪音 |
| 13 | 编码 | PASS | 四个改动文件 `bom=False`；中文串计数 363 / 377 / 35697 / 0 正常，无乱码 |
| 14 | 目录归位 | PASS | 新生产文件 `workbuddy/auth_thirdparty.go` 与同域 `authfile.go` / `host_auth.go` 平级；新测试进 `test/workbuddy/` 镜像目录，符合测试资产双根落点 |
| 15 | 版本三处一致 | PASS | `workbuddy/VERSION` = `0.21.26`（7 字节、无换行、无 BOM）、`workbuddy/main.go:359 var version = "0.21.26"`、`CHANGELOG.md` 顶部 `## 0.21.26` 三者对齐 |
| 16 | CHANGELOG 结构 | PASS | 沿用既有 0.21.25 节格式：`## <版本>` → `### Feature - <中文标题>` → 背景 / 修复（1./2./3./4.）/ 为什么不改上游或调用方 / 测试，五段齐全 |

## 变异与真实数据反证

- 真实数据探针：`work10.json` 10 条**原始未转换**记录逐条过 `parseStored` → `verified 10/10 credentials`，`expiresAt` 全部落在秒级（`1796910717`~`1796911202`），证明毫秒阈值判断未误伤。
- 回归保护：`TestParseStored_NestedAndFlatUnaffected` 锁定既有嵌套与扁平两形态行为不变，防止新分支抢匹配。
- 否定用例：`TestParseStored_MissingAccessToken` 确认无令牌文档仍走 `parse_error`，未因放宽识别而误接收垃圾数据。

## 结论

**STYLE: PASS** — 改动为手术式最小变更，复用既有工具函数、跟随同域文件写法；注释三要素、编号步骤、UTF-8 编码、目录落点与版本三处一致性全部达标，无需修正。
