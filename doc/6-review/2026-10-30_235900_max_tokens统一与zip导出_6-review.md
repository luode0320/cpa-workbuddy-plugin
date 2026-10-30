# max_tokens 统一与 zip 导出改造 6-review

- 记录时间：2026-10-30 23:59:00
- 检查对象：7 插件（qoder-ai / qoderwork / qwenwork / traework / workbuddy / workbuddy-ai / zcode）的 max_tokens 全局上限统一改造 + 每账号一个 JSON 的 zip 导出改造
- 基线提交：`94eef1e`
- 判定：`STYLE: PASS`（3 处 gofmt 报点经核验为 HEAD 既有历史问题，非本轮引入，按最小改动原则不改）

## 检查范围

按 `static_owner_router.py` 推导的七步流水线执行，覆盖 Owner：`code-style-consistency-rules`、`naming-rules`、`comment-rules`、`code-quality-rules`、`api-contract-rules`、`golang-patterns`、`test-program-rules`。

变更路径（每插件同构，以 workbuddy 为例）：

```
workbuddy/model_specs.go              （重写：删除逐模型规格表，改 4 个全局常量）
workbuddy/max_tokens_test.go          （修改：断言同步到全局常量）
workbuddy/credits_handler.go          （修改：新增 handleExportAuthZip + perAccountFileName）
workbuddy/management.go               （修改：/export/zip 路由注册 + 分发分支 + mutatingManagementPath）
workbuddy/checkin.go                  （修改：新增 hostAuthGetPhysicalFn 测试接缝）
workbuddy/panel.html                  （修改：导出 ZIP 按钮 + exportAuthZip 函数）
workbuddy/credits_export_zip_test.go  （新增：6 个用例）
workbuddy/VERSION                     （修改：0.21.6 → 0.21.7）
workbuddy/main.go                     （修改：var version 同步）
workbuddy/CHANGELOG.md                （修改：新增 0.21.7 章节）
test/traework/config_fields_test.go   （修改：版本一致性断言 0.5.4 → 0.5.5）
```

## 检查步骤与证据

### STYLE-01 静态格式与编码一致性

- 61 个变更文件（Go / HTML / Markdown）全部严格 UTF-8、无 BOM、无乱码替换字符。
- 唯一尾随空白命中为 `qoder-ai/panel.html:445`，该行是仅有缩进空格的空行；`git show HEAD:qoder-ai/panel.html` 相邻位置同样存在 `  $` 形态的缩进空行，属该文件既有排版习惯，非本轮引入。
- gofmt：对本轮全部变更 Go 文件做 LF 副本转换后校验。3 个文件报点（`qoder-ai/management.go`、`qoderwork/management.go`、`workbuddy/management.go`），逐一核验为**同一处历史遗留**：`loadedManagementKey()` 注释块末尾多一行空 `//`。用 `git show HEAD:<file>` 复制为 LF 副本后重跑 `gofmt -l`，三者**均同样报点**，证明 HEAD 已存在，非本轮引入。按「手术式改动：只碰当前目标必需行」原则不改。
- `qoderwork/management.go` 的 gofmt 全文件 diff 是 CRLF 假象（工作区 CRLF、比较基准 LF），去 CRLF 后仅剩上述同一处空注释行差异。
- 证据：`STYLE-01：61 文件 UTF-8 干净；尾随空白 1 处为既有缩进空行；gofmt 3 处报点经 HEAD 交叉验证均为历史遗留 → PASS`

### STYLE-02 命名与符号引用一致

- `handleExportAuthZip` / `handleExportAccountsZip`（zcode）、`perAccountFileName` / `zcodeAccountFileName` 沿用项目既有 `handleXxx` 处理函数族与 camelCase 命名，7 个插件间签名完全一致。
- 测试接缝 `hostAuthGetPhysicalFn` 沿用同项目 `hostAuthListFn` / `hostAuthGetFn`（checkin.go）的 `var xFn = x` 模式，命名后缀一致。
- 全局常量 `minMaxTokensFloor` / `defaultMaxOutputTokens` / `defaultReasoningMaxTokens` / `defaultContextWindow` 沿用 `model_specs.go` 既有的 `default*` 前缀约定。
- 版本双命中：7 个插件的 `VERSION` 与 `main.go` 版本常量（zcode 为 `version` const）全部同值，无第三处硬编码残留（`grep -rn '旧版本号' --include="*_test.go"` 返回空）。
- 证据：`STYLE-02：7 插件函数名/接缝名/常量名完全同构；版本双命中无残留 → PASS`

### STYLE-03 注释分层与定义位置

- 14 个新增函数（`handleExportAuthZip` ×6、`perAccountFileName` ×6、`handleExportAccountsZip` ×1、`zcodeAccountFileName` ×1）全部具备完整元信息：整体说明 + `[参数]` + `[返回]` + `最近修改时间：2026-10-30；改动原因：…`，脚本核验 `checked 14 missing 0`。
- 函数注释用中文解释 **WHY**：`handleExportAuthZip` 注释明确写出「与 `handleExportAuth` 的区别：那个返回单个汇总 JSON 适合整库备份；本接口面向按账号分发/迁移」，并说明失败项以 `.error.txt` 记录的设计取舍。
- `model_specs.go` 四个常量各自带中文单行作用说明，`lookupOfficialModelSpec` 注释说明「保留结构体是为了让调用方零改动」，解释了为什么不直接删除该函数。
- 前端 `panel.html` 的 `exportAuthZip` 函数头注释说明「GET /export/zip 返回 application/zip 二进制，包内每个账号一个独立 json」，与后端契约对齐。
- 证据：`STYLE-03：14 个新增函数元信息 100% 完整，注释解释 WHY 而非 WHAT → PASS`

### STYLE-04 函数签名与参数结构

- 新增函数签名均为单行，无多行参数列表。
- `perAccountFileName(name, authIndex string, used map[string]int)` 为 3 参数；同目录已有多参数同类型先例（`growthMakeupTarget(streak, heatmap map[string]any, now time.Time)`、`displayNote(sa *storedAuth, cr *creditsSummary, disabled bool)`），参数复杂度未到需要结构体的程度，符合既有尺度。
- `computeAdaptiveMaxTokens(model string, requested int64, isReasoning bool)` 保持既有签名不变，调用方零改动。
- 证据：`STYLE-04：新增签名全部单行；3 参数有同目录先例；既有签名未扩张 → PASS`

### STYLE-07 接口契约与数据访问

- 新增 `GET /export/zip` 与既有 `GET /export` 同族：同 base 前缀、同 GET 方法、同 `Description` 字段写法，仅在路径后缀区分。
- 响应构造沿用项目既有 `pluginapi.ManagementResponse{StatusCode, Headers, Body}`（`makeHTTPResponse`、zcode oauth 等已有先例），未新造响应包装。
- 二进制传递依赖 `ManagementResponse.Body []byte` 经 JSON 通道 base64 无损往返，与项目既有约定一致。
- 错误响应沿用 `mgmtJSONResponse(http.StatusXxx, map[string]any{"error": ...})` 既有形态。
- 路由登记三处齐全：注册列表条目、`handleManagement` 分发分支、`mutatingManagementPath` 白名单（需鉴权的写操作路径表）。
- 证据：`STYLE-07：/export/zip 与 /export 同族；响应/错误构造复用既有模式；路由三处登记齐全 → PASS`

### STYLE-08 语言与框架特异写法

- 常量块使用显式赋值（`minMaxTokensFloor int64 = 32768` 等），未使用 `iota`，符合项目 Go 编码约定。
- 新增代码无 `var (...)` 分组声明；命中的 `workbuddy/checkin.go:30`、`zcode/management.go:20` 均为历史包级 var 块，非本轮新增。
- `strings.TrimSpace` 仅用于外部输入归一化场景（`filepath.Base(name)` 结果判空、`authIndex` 空值判空），与同文件既有 `body.AuthIndex`、`phys.Path` 的处理口径一致，无多余防御。
- zip 写入用标准库 `archive/zip` + `bytes.Buffer`，未引入第三方依赖。
- 证据：`STYLE-08：无 iota、无新增 var() 分组、TrimSpace 仅用于外部输入、只用标准库 → PASS`

### STYLE-09 测试资产与编码前契约

- 新增 7 个 `credits_export_zip_test.go` 全部落在对应插件目录内。经统计，项目既有测试主流也在插件目录内（共 306 个 `_test.go`），`test/` 镜像仅 24 个特例，本选择符合项目主流风格。
- 测试文件命名 `credits_export_zip_test.go` 与所测源码 `credits_handler.go` 语义对应。
- 新增测试已通过 `cgo-shim-build.py` 真实进编译（7/7 PASS），非仅存放不跑。
- `test/traework/config_fields_test.go` 的版本断言同步更新为 0.5.5，与 `VERSION` / `main.go` 三处一致，属发布必需的测试维护。
- 证据：`STYLE-09：7 个测试文件归位插件目录（符合主流分布）；真实进编译；版本断言同步 → PASS`

## 闸门结论

- 全量回归：7 个插件 `cgo-shim-build.py` build / vet / test **全部 PASS**（traework 首轮因版本断言失败，更新断言后复跑 PASS）。
- 前端语法：7 个 `panel.html` 的 2 个 script 块经 `node --check` **全部 PASS**。
- 测试污染扫描：新增生产符号（`handleExportAuthZip` / `perAccountFileName` / `handleExportAccountsZip` / `zcodeAccountFileName`）均被生产代码真实引用，**无「仅被 test 引用」的污染符号**。
- `STYLE: PASS`
