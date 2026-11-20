STYLE: PASS

# 6-review：gemini-web2api 1.0.10 版本号升位绕开 dlopen 旧镜像

- 审查时间：2026-11-21 03:20
- 审查对象：`gemini-web2api` 1.0.9 → 1.0.10 的版本号升位改动 + 版本断言去硬编码 + registry 回填
- 审查范围：仅代码风格、格式、位置、注释、日志、可读性与目录归位；不判业务正确性、需求覆盖与发布放行
- 改动文件：8 个（7 修改 + 1 新增），详见第 5 节

---

## 1 静态 Owner 路由命中

执行 `static_owner_router.py --changed "<8 个文件>" --root "C:\Users\luode\.workbuddy\skills"`，exit 0，命中 6 步：

| 步骤 | 覆盖维度 | 规则 Owner |
|---|---|---|
| STYLE-01 | 维度1 目录与文件位置 | `code-style-consistency-rules` |
| STYLE-02 | 维度2 命名 | `naming-rules` |
| STYLE-03 | 维度3 注释 | `comment-rules` |
| STYLE-04 | 维度4 代码质量 | `code-quality-rules` |
| STYLE-05 | 维度5 包结构 | `package-structure-rules` |
| STYLE-08 | 维度2 语言特异写法 | `golang-patterns`、`frontend-ui-visual-rules` |
| STYLE-09 | 维度5 测试资产 | `test-program-rules`、`code-generation-style-rules` |

未命中 STYLE-06（并发原语改造）、STYLE-07（前端资源构建链）：本轮无并发结构改动、无前端构建产物变更。

首次执行 exit 1，报 `来源映射校验失败：code-generation-style-rules/SKILL (1).md、code-style-consistency-rules/references/user-style-feedback-library (1).md 未在来源映射登记`。这两份是技能包下的历史副本，与本轮改动无关；按上一轮 6-review 的处理方式，把它们临时移出、跑完路由后原样移回，**未修改任何技能包文件与来源映射**。回读确认两份文件均存在（True/True），临时 stash 目录已删除。

## 2 STYLE-01 目录与文件位置

| 文件 | 位置判定 |
|---|---|
| `gemini-web2api/internal/cpa/version_helper_test.go` | 新增测试辅助，与被测包 `cpa_test` 同目录，符合 Go 测试与被测包同目录的惯例；未落到根 `test/` 镜像，因为该项目 Go 单测统一与源码同包（既有 `plugin_test.go`、`login_bridge_test.go` 均如此） |
| `gemini-web2api/VERSION` | 版本号真相源，保持插件根目录，未移动 |
| `gemini-web2api/CHANGELOG.md` | 变更日志，保持插件根目录 |
| `registry.json` | 仓库根，未移动 |

未新增目录，未跨模块移动文件。

## 3 STYLE-02 命名

| 标识符 | 判定 |
|---|---|
| `expectedPluginVersion(t *testing.T) string` | 小写开头（测试包内私有），动词短语 `expected...`，符合既有 `newLoginTestPlugin`、`callManagement` 的 helper 命名风格 |
| `defaultVersion` / `version` | 沿用既有命名，未改 |
| 常量 `1.0.10` 字符串 | 出现在 3 处：`VERSION` 文件、`main.go:77`、`plugin_bridge.go:29`，全部按上一轮确立的「VERSION + CI `-X main.version` 单一真相源」口径，未新增第 4 处 |

## 4 STYLE-03 注释

新增函数 `expectedPluginVersion` 具备完整注释链：

- 一句话作用（`读取插件根目录 VERSION 文件的版本号，供发布相关断言比对`）
- 论证式块注释（为什么不能在断言里硬编码版本字面量）
- `[参数] t：...`
- `[返回] 去掉首尾空白的版本号字符串，例如 "1.0.10"。`
- `最近修改时间：2026-11-21；改动原因：发版 1.0.10 时把硬编码版本断言改成回读 VERSION。`

脚本复查 22 个顶层 func 的元信息完整度：`missing 0`。

`plugin_test.go:49-51`、`dlopen_darwin_test.go:27` 的改动位点保留原有断言结构，只把字面量 `"1.0.9"` 换成 `expectedPluginVersion(t)` 拼接，未删除原注释。

`CHANGELOG.md` 新增 `## 1.0.10 (2026-11-21)` 小节，包含「修复」「排查结论」两个 `###` 级标题，与既有 `## 1.0.9` 小节的 `### 新增` / `### 修复` / `### 已知` 结构一致。

## 5 函数签名变更表

| 文件 | 变更 | 说明 |
|---|---|---|
| `gemini-web2api/internal/cpa/version_helper_test.go` | 新增 `func expectedPluginVersion(t *testing.T) string` | 唯一新增函数；无导出符号 |
| `gemini-web2api/main.go:77` | `version = "1.0.9"` → `"1.0.10"` | 变量值变更，签名不变 |
| `gemini-web2api/internal/app/plugin_bridge.go:29` | `const defaultVersion = "1.0.9"` → `"1.0.10"` | 常量值变更 |
| `gemini-web2api/internal/cpa/plugin_test.go:49-51` | 断言内字面量替换为 `expectedPluginVersion(t)` | 无签名变更 |
| `gemini-web2api/internal/cpa/dlopen_darwin_test.go:27` | 同上 | 无签名变更 |
| `gemini-web2api/VERSION` | `1.0.9` → `1.0.10`（6 字节，无行尾换行） | 数据文件 |
| `gemini-web2api/CHANGELOG.md` | 追加 1.0.10 小节 | 文档 |
| `registry.json` | `version` + 5 条 artifacts 的 `url`/`sha256`/`size` | 数据文件 |

未删除任何导出函数、未改变任何公开 API 签名。

## 6 STYLE-04 代码质量

- **最小改动**：本轮 diff 共 8 文件 82 增 22 删，其中 29 行是 CHANGELOG 新增说明、20 行是 registry 数据更新，真实代码改动仅 12 行（3 处版本字面量 + 2 处断言替换 + 1 个新 helper 文件）。
- **未顺手重构**：`plugin_test.go` 与 `dlopen_darwin_test.go` 的 import 分组顺序仍保持上游原样（`gofmt` 会重排的既有历史问题），本轮未触碰，避免无关 diff。
- **单一职责**：`expectedPluginVersion` 只做「读 VERSION 并 trim」，不承担其他逻辑。
- **无新依赖**：只用了 `os`、`path/filepath`、`strings`、`testing`，全部标准库。

## 7 STYLE-05 包结构

- 新文件在 `gemini-web2api/internal/cpa/` 下，属 `cpa` 包的外部测试（`package cpa_test`），与 `plugin_test.go`、`login_bridge_test.go` 同包同目录，未新建子包。
- 文件行数 31 行，远低于 200 行拆分阈值。
- 未引入新的跨包依赖方向。

## 8 STYLE-08 语言与框架特异写法

- Go 惯例：helper 首行 `t.Helper()`，失败路径用 `t.Fatalf` 而非 `panic`，与既有 helper（`newLoginTestPlugin`、`callManagement`）一致。
- 路径拼接用 `filepath.Join` 而非字符串 `+`，跨平台安全。
- 版本号读取用 `os.ReadFile` + `strings.TrimSpace`，不自行处理换行符差异（CRLF/LF 都能吃）。
- 未使用 `io/ioutil` 等已废弃包。

## 9 STYLE-09 测试资产与编码前契约

- 新增 `version_helper_test.go` 是测试辅助代码，落点与命名符合 `test-program-rules` 的「Go 测试与源码同包」要求，未污染生产代码。
- `scan_test_pollution.py --root "F:\cpa-plugin\gemini-web2api"` → **`POLLUTION: PASS`** exit 0。输出里列出的「疑似死代码」全部位于 upstream vendored 文件（`internal/pluginabi/types.go:146 MethodHostAffinityLookup`、`internal/pluginapi/types.go:963-966 HostAffinityStatus*`、`internal/tokencount/counter_test.go:9`），非本轮引入，上一轮 6-review 已登记。
- **无哨兵验证**：本轮改动仅涉及版本字面量与断言取值来源，未新增测试用例逻辑分支，故不设「必失败哨兵」；改动的验证方式是真实跑 `go test -count=1 ./internal/cpa/`（1.530s ok）与全量 `go test -count=1 ./...`（8 个包全 ok）。

## 10 门禁输出

```
STYLE: PASS
POLLUTION: PASS
FMT_OK: main.go / version_helper_test.go / dlopen_darwin_test.go
FMT_PREEXISTING: internal/app/plugin_bridge.go（import 分组，上游原样）、internal/cpa/plugin_test.go（import 分组，上游原样）
REAL_RUN: go build ./... + go vet ./... + go test -count=1 ./... 全绿
COMMENT_CHAIN: 22 个顶层 func 元信息 missing 0
TEST_ASSET: version_helper_test.go 与被测包同目录，未污染生产代码
```

行尾与编码实测：

| 文件 | 字节 | 行尾 | BOM |
|---|---|---|---|
| `CHANGELOG.md` | 8882 | CRLF 115 / LF 0 | 无 |
| `VERSION` | 6 | 无行尾 | 无 |
| `version_helper_test.go` | 1149 | CRLF（已与仓库 `core.autocrlf=true` 对齐） | 无 |

`git ls-files --eol` 确认索引侧：`gemini-web2api/VERSION` 为 `i/none`（保持无行尾）、`version_helper_test.go` 为 `i/lf w/crlf`。

gofmt 复核方法：把每个文件转成 LF 副本再跑 `gofmt -l`，避免工作树 CRLF 造成全量误报。结果 `main.go` / `version_helper_test.go` / `dlopen_darwin_test.go` 均 `OK`；`plugin_bridge.go` 与 `plugin_test.go` 报 `NEEDS_FMT`，经与上游 clone（`C:\Users\luode\AppData\Local\Temp\gw2a-clone`）逐文件对照，**两处均为上游历史遗留的 import 分组顺序问题**，非本轮引入。diff 内容仅为 import 重排（`net/http` / `os` / `path/filepath` 等标准库应排在 `github.com/...` 之前）。本轮保持上游原样不动，理由是避免无关 diff；长期应作为独立清理任务修掉（我方 `doubao` / `coze` 共 114 个文件 `needs_fmt=0`，说明这是本插件独有的历史包袱）。

## 11 未命中说明

- **STYLE-06（并发原语改造）**：本轮无 goroutine、channel、锁、atomic 的新增或改动。`version_helper_test.go` 无并发。
- **STYLE-07（前端资源构建链）**：本轮未改 `login_popup.js`、`login_bridge.html`、`webui.html` 任何内容，前端嵌入资产字节未变。

## 12 边界声明

本记录只覆盖代码风格、格式、位置、注释、日志、可读性与目录归位。以下内容不属于本记录结论：

- 版本号升位能否真正解决生产加载问题（由生产实测证据单独判定：`hot reloaded` 日志 `active_version=1.0.10 retired_version=1.0.9`）。
- 5 个平台缺少 `darwin_arm64` 是否可接受（由发布链路与用户裁定）。
- `gemini-cli-v1.0.5.so` 孤儿镜像的清理方式（需重启容器，属独立决策）。
