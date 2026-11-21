STYLE: PASS

# 6-review：gemini-web2api 面板去日志 + 概览卡对齐 + 插件图标

- 评审对象：gemini-web2api 1.0.13（面板去运行日志、概览卡常驻与进度条、`Metadata.Logo` 与 registry `logo`）
- 变更规模：10 个已跟踪文件 + 1 个新增图标资产，`git diff --stat` = 104 insertions / 72 deletions（不含 registry 与图标）
- 评审口径：只判代码风格、格式、位置、注释、日志、可读性与目录归位；不判业务正确性与发布放行

## 1 静态 Owner 路由命中

命令：
```
python C:\Users\luode\.workbuddy\skills\code-style-consistency-rules\scripts\static_owner_router.py --changed "<12 个文件>" --root "C:\Users\luode\.workbuddy\skills"
```
结果：`exit 0`，命中 STYLE-01 / STYLE-02 / STYLE-03 / STYLE-04 / STYLE-05 / STYLE-08 / STYLE-09 七步。

首次运行为 `exit 2`：
```
来源映射校验失败：以下规则文件未在来源映射登记，会导致 6-review 失去检查落点：code-generation-style-rules/SKILL (1).md、code-style-consistency-rules/references/user-style-feedback-library (1).md；请补登记，或按 NON_RULE_FILE_NAMES / NON_RULE_DIR_NAMES 明确豁免。
```
处置：把两份带 `" (1)"` 后缀的历史副本 `Copy-Item` 到 `$env:TEMP\zz_router_stash_gw2a4`（9245 B / 27919 B）后 `Remove-Item` 原文件（`Move-Item` 会被锁），跑完路由再从 stash 拷回，回读两份大小仍为 9245 / 27919，最后删 stash（`Test-Path` = False）。**未修改任何技能包文件与来源映射。**

未命中：STYLE-06（并发原语改造）、STYLE-07（前端资源构建链）—— 本轮无并发原语变化，面板是 `//go:embed` 单文件，不涉及构建链。

## 2 STYLE-01 目录位置

- 图标资产：`assets/icons/GeminiWeb.png`，与 `Copilot.png` / `Coze.png` / `Doubao.png` 等同目录，符合既有归档位置。
- 面板与 Go 代码：`gemini-web2api/internal/cpa/`，与 `management.go` / `plugin.go` / `login_bridge.go` 同包，未新增目录。
- 文档：`doc/6-review/`，符合 `artifact-storage-rules` 约定的评审产物位置。
- 无临时脚本、无 `.py` / `.sh` / `.bak` 遗留进仓库。

## 3 STYLE-02 命名

| 新增符号 | 命名 | 对齐依据 |
|---|---|---|
| `pluginLogoURL` | 常量，小写包内可见 | `doubao/main.go:62` / `coze/main.go:63` 同名同形 |
| `panelResourcePath` / `legacyPanelResourcePath` | 资源路由常量（上一版引入） | 兄弟插件面板均走 `/panel` |
| `.empty` / `.empty strong` | 面板空状态类 | workbuddy 用 `.loading`，但语义是「加载中」，此处是「空状态」，改用 `.empty` 区分 |
| `renderSummary` 内 `pct` / `barColor` | 局部变量 | 与 `healthHTML` 内 `pct` / `color` 同口径 |

未引入缩写、未新增无意义前缀。

## 4 STYLE-03 注释

### 4.1 新增注释

- `config.go` 的 `pluginLogoURL` 上方三行块注释，说明「为什么托管在本仓库」与「宿主只做 URL 透传，不做本地缓存」。
- `webui.html` 的 `.empty` 上方单行注释，说明「概览卡已给统计，网格只做一句引导」。
- `webui.html` init 处三行注释，说明「先渲染零值概览避免首屏空壳」。
- `plugin_test.go` 新增三行注释，说明「漏填 Logo 宿主静默不显示」与「只校验文件名不写死域名」。

### 4.2 删除注释的处理

删除 `loadLogs` / `fmtLogTime` / `fmtCST` 时，其原有注释一并删除，未留孤儿注释。删除后补回 `/* ---------- actions ---------- */` 分区注释，保持文件分区结构完整。

### 4.3 注释链完整性

`internal/cpa` 包内 22 个顶层函数元信息（一句话 + `[参数]` + `[返回]` + 最近修改时间）复核：缺失 0。本轮未新增顶层函数，`renderSummary` 等为文件内嵌套函数，按工作树既有写法只写一行说明，不写 `[参数]` / `[返回]`。

## 5 函数签名变更表

本轮**无函数签名变更**。`renderSummary(d)` 参数保持不变，仅内部实现重写（`src=d||{}` 做 undefined 兜底）。删除的 `loadLogs()` / `fmtLogTime(s)` / `fmtCST(s)` 均无外部调用方（同文件内的调用点已一并清除）。

## 6 STYLE-04 代码质量

### 6.1 最小改动

- `webui.html` numstat = `+37 / -67`，净减 30 行；未顺手重排未涉及的 CSS 与函数，未改动 `api()` / 密钥三级获取 / `card()` / `healthHTML()` 等无关逻辑。
- `plugin.go` 仅 `+1` 行（`Logo:` 字段），`config.go` 仅 `+5` 行（常量 + 注释）。
- 未引入新依赖、未新增抽象层、未拆文件。

### 6.2 单文件行数

`webui.html` 902 行超过 200 行阈值。豁免理由：它是 `//go:embed` 的单页 HTML（含内联 `<style>` 与 `<script>`），拆成多文件会破坏 `embed` 单文件契约与部署产物一致性；兄弟插件 `workbuddy/panel.html`（1712 行）、`coze/panel.html`、`doubao/panel.html` 同理。Go 文件本轮无行数增长超阈。

### 6.3 副作用可见性

`renderSummary` 是纯渲染函数（只写 DOM），无副作用命名问题。`load()` 是 query（读 `/status`），`toggleService` / `relaunchGuest` / `importCookies` 等是 command，命名已区分。

### 6.4 错误处理

`api()` 的空响应 / 非 JSON / bad JSON 三分支仍返回 `{error:...}`，不冒泡 `SyntaxError`；本轮未触碰。删除 `loadLogs` 后，10 秒轮询只剩 `load()`，少了一条静默失败通道，反而降低噪声。

## 7 STYLE-05 包结构

未新增包、未新增目录、未改变依赖方向。`pluginLogoURL` 放在 `internal/cpa/config.go` 的既有 const 块内，与 `PluginID` / `consoleMenuName` / `panelResourcePath` 同处，不引入新的常量文件。

## 8 STYLE-08 语言与框架特异写法

### 8.1 Go

- 常量在 const 块内对齐，未使用 `var`。
- `plugin_test.go` 用匿名结构体解码 JSON 而非字符串拼接断言：避免依赖字段顺序，也避免外部测试包访问不到小写常量 `pluginLogoURL`（首次写成字符串拼接时报 `undefined: pluginLogoURL`）。

### 8.2 前端

- `webui.html` 两个 `<script>` 块分别 `node --check` 通过（block0 主题同步 / block1 主逻辑 26544 chars）。
- 新增 CSS 复用既有 token（`var(--card)` / `var(--border)` / `var(--radius-md)` / `var(--mut)` / `var(--fg)`），无硬编码色值。
- 进度条复用既有 `.pb` / `.pb-label` / `.pb-track` / `.pb-bar` / `.pb-meta`，与卡片 `healthHTML` 完全同构。

### 8.3 面板能被宿主正确嵌入

- `summaryBox` 去掉 `style="display:none"` 后由 JS 在首屏 `renderSummary(lastStatus)` 预渲染，避免「卡片边框有、内容没有」。
- 无外部字体、无外链资源，嵌入 iframe 与 standalone 两种形态一致。
- 主题同步 `window.__gwThemeSync` 未改动。

## 9 STYLE-09 测试资产

### 9.1 归位

`plugin_test.go` 与被测包同目录（`internal/cpa`），未进根 `test/` 镜像目录，符合 Go 测试路径约束。

### 9.2 改动内容

新增 14 行，锁死 `Metadata.Logo` 必须以 `/assets/icons/GeminiWeb.png` 结尾。

### 9.3 哨兵验证

临时删掉 `plugin.go` 的 `Logo: pluginLogoURL,` 行后运行：
```
--- FAIL: TestPluginLifecycleAndCapabilities
    plugin_test.go:67: expected plugin logo ending with /assets/icons/GeminiWeb.png, got ""
```
恢复后 PASS，证明新断言真实进编译并生效。

## 10 门禁输出

```
STYLE: PASS
POLLUTION: PASS
FMT_OK: main.go / internal/cpa/config.go / internal/cpa/plugin.go
FMT_PREEXISTING: internal/cpa/management.go（switch 内多一个空行）/ internal/cpa/plugin_test.go（import 分组）/ internal/app/plugin_bridge.go（import 分组 + 字段对齐）—— 三处上游原样，非本轮引入
REAL_RUN: go build ./... + go vet ./... + go test -count=1 ./... 全绿（adapter 0.476s / balancer 1.681s / cpa 2.224s / gemini 0.392s / mcp 0.408s / proxydial 1.080s / requestdb 0.509s / tokencount 1.229s）
SHIM: python scripts/cgo-shim-build.py gemini-web2api -> all green
COMMENT_CHAIN: internal/cpa 22 个顶层 func 元信息 missing 0
TEST_ASSET: plugin_test.go 与被测包同目录，未污染生产代码
EOL: 10 个改动文件 CRLF / lone_lf 0 / 无 BOM；VERSION 无换行；GeminiWeb.png 为二进制
```

## 11 未命中说明

- STYLE-06 并发原语改造：本轮只删轮询里的一行 `loadLogs()`，未改 `setInterval` 结构、未新增 goroutine / channel / mutex。
- STYLE-07 前端资源构建链：面板是 `//go:embed` 单文件，无打包器、无 npm 依赖、无构建产物。

## 12 边界声明

- 本评审只覆盖写法、位置、格式、注释与既有习惯一致性。
- 不覆盖：面板在生产宿主的实际渲染效果、图标 URL 的 CDN 可达性、`/logs` 管理路由删除后是否有外部调用方依赖（该路由在 `management.go` 中保留，仅面板不再渲染）。
- 待用户裁定：`/accounts` 与 `/webui` 两条历史面板路径是否长期保留（当前已注册为兼容路由）。
