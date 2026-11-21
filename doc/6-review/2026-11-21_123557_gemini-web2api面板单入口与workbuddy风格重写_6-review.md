STYLE: PASS

# 6-review：gemini-web2api 1.0.11 面板收口为单入口并重写为 workbuddy 视觉风格

- 审查时间：2026-11-21 12:35
- 审查对象：`gemini-web2api` 1.0.10 → 1.0.11 的控制台面板改造（单菜单 + 改名为 `Gemini Web` + workbuddy 风格重写）
- 审查范围：仅代码风格、格式、位置、注释、日志、可读性与目录归位；不判业务正确性、需求覆盖与发布放行
- 改动文件：12 个（11 修改 + 1 整体重写），详见第 5 节

---

## 1 静态 Owner 路由命中

执行 `static_owner_router.py --changed "<12 个文件>" --root "C:\Users\luode\.workbuddy\skills"`，exit 0，命中 7 步：

| 步骤 | 覆盖维度 | 规则 Owner |
|---|---|---|
| STYLE-01 | 维度1 目录与文件位置 / 维度5 静态风格 | `code-style-consistency-rules` |
| STYLE-02 | 维度2 命名与符号引用 | `naming-rules` |
| STYLE-03 | 维度3 注释分层与定义位置 | `comment-rules` |
| STYLE-04 | 维度4 函数签名与参数结构 | `code-quality-rules` |
| STYLE-05 | 维度1 结构与落点 | `package-structure-rules` |
| STYLE-08 | 维度2 语言与框架特异写法 | `golang-patterns`、`frontend-ui-visual-rules` |
| STYLE-09 | 维度5 测试资产与编码前契约 | `test-program-rules`、`code-generation-style-rules` |

未命中 STYLE-06（并发原语改造）：本轮无并发结构改动，`loginStateMu` / `chromeLogin` 等既有原语未动。
未命中 STYLE-07（前端资源构建链）：面板是 `//go:embed` 直出的单文件 HTML，无构建产物、无打包链变更。

首次执行 `exit 2`，报 `来源映射校验失败：code-generation-style-rules/SKILL (1).md、code-style-consistency-rules/references/user-style-feedback-library (1).md 未在来源映射登记`。这两份是技能包下的历史副本，与本轮改动无关；按前两轮 6-review 的处理方式把它们临时移出、跑完路由后原样移回，**未修改任何技能包文件与来源映射**。回读确认两份文件均存在（True/True），临时 stash 目录已删除。

## 2 STYLE-01 目录与文件位置

| 文件 | 位置判定 |
|---|---|
| `gemini-web2api/internal/cpa/config.go` | 新增 4 个包级常量（`consoleMenuName` / `panelResourcePath` / `legacyPanelResourcePath`），放在既有 `PluginID` / `ProviderID` 常量块内，未新建文件 |
| `gemini-web2api/internal/cpa/management.go` | 路由注册与静态面板分发都在既有文件内，未新建文件 |
| `gemini-web2api/internal/cpa/webui.html` | 面板模板仍在 `internal/cpa/`，由同目录 `webui.go` 的 `//go:embed webui.html` 嵌入，位置未动 |
| `gemini-web2api/internal/cpa/plugin_test.go` | 既有测试文件内改断言，未拆新文件 |
| `gemini-web2api/internal/cpa/login_bridge_test.go` | 既有测试文件内改断言与补注释，未拆新文件 |
| `gemini-web2api/VERSION` / `CHANGELOG.md` / `README.md` / `README_EN.md` | 插件根目录，位置未动 |
| `registry.json` | 仓库根，位置未动 |

未新增目录，未跨模块移动文件，未把代码放到根 `test/` 镜像（本项目 Go 单测统一与被测包同目录）。

## 3 STYLE-02 命名

| 标识符 | 判定 |
|---|---|
| `consoleMenuName = "Gemini Web"` | 常量名语义化前缀 `console`，与既有 `consoleMenuName` 之外无同义重复；值用简短英文产品名，与 `Coze` / `Doubao` / `WorkBuddy` / `ZCode` 一致，去掉原 `(Web2API)` 括号后缀 |
| `panelResourcePath = "/panel"` | 与兄弟 10 个插件统一的面板路径；变量名而非裸字面量，避免 4 处硬编码 |
| `legacyPanelResourcePath = "/accounts"` | `legacy` 前缀明确表达「历史兼容」，避免后续误改 |
| `FilterKeyOf` 相关的 `currentFilter` / `currentSearch` | 与 workbuddy 同名同义，沿用不另起新名 |
| `fmtRemain` / `stateBadgeClass` / `tierClass` / `healthHTML` / `cooldownHTML` | 动词 / 名词短语，动词优先；`HTML` 后缀沿用 workbuddy 的 `healthHTML` 习惯 |
| `window.__gwThemeSync` | `gw` 是 gemini-web 缩写，与 workbuddy 的 `__wbThemeSync` 同构；不用 `__gw2a` 避免与 `gemini2api` 混淆 |

JS 里刻意避开模板字符串，全部用 `'…'+x+'…'` 拼接，与 gofmt 无关但与 `//go:embed` 原始字符串的编辑安全相关（反引号会截断 Go raw string），已在文件头注释记录。

## 4 STYLE-03 注释

### 4.1 新增注释元信息

本轮给 `management.go` 两个顶层 func 补齐完整元信息（此前缺）：

- `handleManagementRegister`：一句话作用 + 论证式块注释（为什么只有 `/panel` 挂 Menu）+ `[参数]` + `[返回]`×2（`rpcManagementRegistrationResponse` 与 `error`）+ `最近修改时间`。
- `handleManagementHandle`：一句话作用 + 论证式块注释（分发顺序固定为 登录面 → 静态面板 → 管理 API，以及为什么登录面必须先判）+ `[参数]` + `[返回]` + `最近修改时间`。

改动位点均保留原有行内注释，未删未改语义。`config.go` 的 4 个新常量各带 2 行中文说明，解释「为什么叫这个名字 / 为什么要这级兼容」。

### 4.2 测试注释

`TestManagementRegistrationIncludesLoginResources` 与 `TestManagementWebUIPrefixStillServed` 都补齐了「一句话作用 + 论证式块注释 + `[参数]` + `[返回]` + `最近修改时间`」五项；后者原本只有函数体没有头部注释，本轮补上。

### 4.3 面板内注释

`webui.html` 用 `/* ---------- actions ---------- */`、`/* ---------- modals ---------- */`、`/* ---------- init ---------- */` 三段分区，与 workbuddy 的 `/* ---- xxx ---- */` 分区习惯一致。`openLoginBridge` 上方保留 4 行块注释说明「桥接页走资源路由、不需要管理密钥、所以用相对路径」，`api()` 内对 401 累计 3 次、403 退避窗口、非 JSON 响应各自留一行中文注释解释动因。

脚本复查 `internal/cpa` 包全部顶层 func 的元信息完整度：`missing 0`（测试函数 8 个 + 生产函数 22 个）。

## 5 函数签名变更表

| 文件 | 变更 | 说明 |
|---|---|---|
| `gemini-web2api/internal/cpa/config.go` | 新增 `consoleMenuName` / `panelResourcePath` / `legacyPanelResourcePath` 三个包级常量 | 无函数签名变化 |
| `gemini-web2api/internal/cpa/management.go` | `handleManagementRegister` / `handleManagementHandle` 签名不变，仅函数体与注释变化 | 资源路由 5 条改 1 条挂 Menu；静态分发条件加 `/panel` |
| `gemini-web2api/internal/cpa/webui.html` | 整体重写（2172 diff 行），无 Go 符号变化 | 1358 行 → 932 行 |
| `gemini-web2api/internal/cpa/plugin_test.go` | 断言字符串与请求路径变化，测试函数签名不变 | `/accounts` → `/panel`，文案 `启动本机 Chrome 无痕登录` → `Gemini Web 面板` |
| `gemini-web2api/internal/cpa/login_bridge_test.go` | 同上 | 新增「恰好 1 条 Menu 且必须是 /panel」遍历断言 |
| `gemini-web2api/main.go:77` | `version = "1.0.10"` → `"1.0.11"` | 变量值变更，签名不变 |
| `gemini-web2api/internal/app/plugin_bridge.go:29` | `const defaultVersion = "1.0.10"` → `"1.0.11"` | 常量值变更 |
| `registry.json` | `version` 与 5 条 artifacts url 换到 `-v1.0.11`，`sha256` 清空、`size` 置 0 | 待 CI 产物回填 |

无导出符号删除，无包边界变化。

## 6 STYLE-04 代码质量

### 6.1 最小改动

- `config.go` 只往既有 `const` 块追加 3 个常量 + 注释，13 行新增，未重排既有常量。
- `management.go` 改动集中在 `Resources` 切片与静态分发条件两处，20 行 diff；16 条管理路由与全部 switch 分支未动。
- `webui.html` 是需求驱动的整体重写（用户要求「参考 workbuddy」），不属于无关重构：可见的 2172 行 diff 中，约 1315 行是旧面板模板删除、922 行是新模板。
- `main.go` / `plugin_bridge.go` 各 1 行版本号。
- `registry.json` 16 增 16 删，全部围绕版本与 artifacts url。
- 未顺手改无关代码，未批量格式化。

### 6.2 单文件行数

`webui.html` 重写后 **932 行 / 50929 字节**，比改前的 1358 行 / 50875 字节减少 426 行。workbuddy 的 `panel.html` 是 1712 行 / 90067 字节，本面板体量在其 55% 左右，属于同一量级的单文件模板。

`internal/cpa/management.go` 663 行、`config.go` 303 行、`plugin_test.go` 779 行、`login_bridge_test.go` 388 行，均属上游既有规模，本轮未显著抬升（`management.go` 从 646 行到 663 行，`login_bridge_test.go` 从 375 行到 388 行）。**65 个超 200 行 Go 文件的拆分豁免仍待用户裁定**，本轮不扩大范围。

### 6.3 副作用可见性

`management.go` 的注册函数是纯数据构造，无副作用；分发函数是命令与查询混合的唯一入口（宿主 ABI 决定的形态），每个 case 的副作用（`p.serviceStopped.Store` / `rt.Accounts.RelaunchGuest` / `p.SyncAccountIDToHostAuth`）都在分支内显式可见，未藏进 helper。

面板 JS 侧把「读」与「写」分开命名：`load()` / `loadLogs()` 只读，`toggleAccount` / `clearCooldowns` / `relaunchGuest` / `stopGuest` / `syncAuthFiles` / `importCookies` / `saveAccountProxy` 等动词开头且各自 `await load()` 回读，符合「读后写后必须回读」的既有习惯。

### 6.4 错误处理

`api()` 对 401 / 403 / 空响应 / 非 JSON / JSON 解析失败五类都转成 `{error:...}` 返回，**不让 `SyntaxError` 冒泡**；`load()` 在 `d.error` 时保留上一次网格并只 toast，避免一次抖动把面板清空；`loadLogs()` 抛错静默（`/* 日志轮询失败保持静默，下一次自动重试 */`）。

## 7 STYLE-05 包结构

未新增包、未新增目录、未改变依赖方向。`internal/cpa` 仍只依赖 `internal/{app,auth,balancer,chromeauth,config,logging,pluginabi,pluginapi}` 与标准库 + `uuid` + `yaml.v3`，本轮只多用了 `strings` / `net/url` 等已在用的标准库。

`webui.go` 保持 4 行 `//go:embed` 壳，模板本体留在 `webui.html`，符合「模板与 Go 代码分离」的既有权衡。

## 8 STYLE-08 语言与框架特异写法

### 8.1 Go

- 常量块放在 `const (...)` 内并用空行分组 + 中文注释，与 `config.go` 既有 `PluginID` / `ProviderID` 风格一致。
- `strings.HasPrefix(path, loginResourcePrefix)` 复用既有 `login_bridge.go` 定义的常量，未重复字面量。
- 新增的遍历断言用 `for ... range` + `t.Fatalf`，未引入 `testify` 等新依赖。

### 8.2 前端

- 三套主题 token 全部用 CSS 自定义属性，`[data-theme="white"]` 与 `[data-theme="dark"]` 各自覆盖同名变量，不写 `!important`。
- `color-scheme` 在每个 `:root` 块声明，与 workbuddy 一致，保证原生控件（滚动条、输入框）跟随主题。
- 响应式用 `@media (max-width:1180px)` 两列、`(max-width:760px)` 一列，与 workbuddy 的三列基准一致。
- 无内联 `style` 大面积硬编码：仅 `h2` 的 flex 容器、卡片底部的 `.bar` 间距等 3 处行内样式，其余全走 class。
- 所有插入 DOM 的用户数据都经 `esc()` 转义，未出现字符串拼接直插未转义值。
- 可访问性：`.card-del` 带 `aria-label="删除账号"`，密码输入用 `type="password"`，弹窗遮罩点击判定 `e.target===e.currentTarget`。

### 8.3 已核实的「面板能被宿主正确嵌入」

`ServeResourceHTTP` 按 `managementRouteKey(http.MethodGet, r.URL.Path)` **精确匹配**已注册的资源路由（`internal/pluginhost/management.go:293`），所以 `/panel` 必须显式注册——本轮已注册；未注册的深层路径不会命中。同时 `:290` 只放行 GET，故面板内的所有写操作都走 `/v0/management/gemini-web2api/*`（带 Bearer key），不走资源路由。

## 9 STYLE-09 测试资产与编码前契约

### 9.1 测试资产归位

无新增测试文件；`plugin_test.go` 与 `login_bridge_test.go` 与生产代码同包（`cpa_test` 外部测试包），符合本项目 Go 单测惯例，不进根 `test/` 镜像。

### 9.2 测试改动内容

`TestManagementRegistrationIncludesLoginResources`：

- 路径集合从 `{/accounts,/login,/login/extension.zip,/login/submit,/login/poll}` 改成 `{/panel,…}`。
- **新增**遍历统计：挂 Menu 的路由数必须恰好为 1、路径必须是 `/panel`、Menu 必须是 `"Gemini Web"`。
- 逐条断言 `/login` 系列 4 条的 Menu 为空。

`TestManagementWebUIPrefixStillServed`：

- 请求路径从单条 `/accounts` 改成 4 条（`/panel`、`/accounts`、`/webui`、`/`），逐条断言返回面板 HTML、不含 `cpa|`（不串到桥接页）、含标题 `Gemini Web 面板`。

### 9.3 哨兵验证

为确认新断言真的进编译链路，临时把 `consoleMenuName` 改成 `"Gemini Web2API"`，运行 `go test ./internal/cpa/ -run TestManagementRegistrationIncludesLoginResources` → **FAIL**：

```
login_bridge_test.go:288: unexpected console menu name: got "Gemini Web2API" want "Gemini Web"
```

改回 `"Gemini Web"` 后重跑 PASS。哨兵已撤销，工作树无残留。

## 10 门禁输出

```
STYLE: PASS
POLLUTION: PASS
FMT_OK: gemini-web2api/main.go / internal/cpa/config.go / internal/cpa/login_bridge_test.go
FMT_PREEXISTING: gemini-web2api/internal/cpa/management.go（switch 内多一个空行）
                gemini-web2api/internal/cpa/plugin_test.go（import 分组顺序）
                gemini-web2api/internal/app/plugin_bridge.go（import 分组 + 字段对齐）
REAL_RUN: go build ./...  exit 0
          go vet ./...    exit 0
          go test -count=1 ./...  全绿（adapter 0.376s / balancer 1.083s / cpa 1.817s /
                                   gemini 0.289s / mcp 0.347s / proxydial 0.865s /
                                   requestdb 0.380s / tokencount 0.961s）
          python scripts\cgo-shim-build.py gemini-web2api  → all green
          node --check（webui.html 两个 <script> 块，隔离目录 + {"type":"commonjs"}）exit 0
COMMENT_CHAIN: internal/cpa 顶层 func 元信息 missing 0
TEST_ASSET: 未新增测试文件；既有测试与被测包同目录，未污染生产代码
```

### 10.1 `FMT_PREEXISTING` 三处说明

三处 gofmt 差异都**不是本轮引入**：`management.go` 的 switch 内空行与 `plugin_test.go` / `plugin_bridge.go` 的 import 分组顺序在上游 clone（`C:\Users\luode\AppData\Local\Temp\gw2a-clone`）里同样存在。本轮只在 `management.go` 顶部新增了元信息注释，未触碰那处空行；`plugin_test.go` 只改断言字符串。保持上游原样，避免无谓 diff。

### 10.2 编码与行尾

12 个改动文件全部 **UTF-8 无 BOM**。工作树行尾：`VERSION` 无换行（6 字节 `1.0.11`）；`webui.html` 等 10 个文本文件全 CRLF（与 `core.autocrlf=true` + `* text=auto` 的工作树形态一致）；`registry.json` 保持 LF（索引 `i/lf w/lf`）。

`webui.html` 写入时是 LF，已按仓库惯例转回 CRLF（932 行 CRLF / 932 行 LF 计数，无孤立 LF）。`login_popup.js` 与 `login_bridge.html` 在 `.gitattributes` 里显式 `text eol=lf`（`//go:embed` 运行时输入），本轮未触碰，`git ls-files --eol` 复核仍为 `i/lf w/lf attr/text eol=lf`。

## 11 未命中说明

- **STYLE-06 并发原语改造**：本轮未新增或改造 goroutine、锁、channel、atomic。面板 10 秒轮询用 `setInterval` 且带 `openModalCount>0` 暂停判据，避免与用户输入竞争重绘；Go 侧未动 `loginStateMu` / `chromeLogin` / `serviceStopped`。
- **STYLE-07 前端资源构建链**：面板是 `//go:embed` 直出的单文件 HTML + 内联 CSS/JS，无 npm、无打包、无构建产物，因此无构建链规则可命中。

## 12 边界声明

本次审查只覆盖代码风格、格式、位置、注释、日志、可读性与目录归位七个静态维度，**不判断**：面板视觉是否符合用户审美、`/panel` 是否为最终路径、1.0.11 是否应当发布、生产部署后左侧插件树是否真的只剩一个子项（这需要 install 后实测）。

生产验证与发布放行属于发布链路，与本审查正交。

本轮新增 1 条待用户裁定的口径：**面板路径从 `/accounts` 迁到 `/panel` 后，旧的 `/accounts` 是否需要长期保留**。当前实现保留为历史兼容（`legacyPanelResourcePath`），若确认无外部收藏可后续删除。

## 13 补审：1.0.12 历史面板路径补注册

### 13.1 事实

1.0.11 install 到生产后实测发现历史路径返回 404：

```
/v0/resource/plugins/gemini-web2api/panel               -> 200  bytes=49997
/v0/resource/plugins/gemini-web2api/accounts            -> 404  bytes=0
/v0/resource/plugins/gemini-web2api/webui               -> 404  bytes=0
/v0/resource/plugins/gemini-web2api/login               -> 200  bytes=7394
/v0/resource/plugins/gemini-web2api/login/extension.zip -> 200  bytes=3905
```

原因：`internal/pluginhost/management.go` 的 `ServeResourceHTTP` 用
`managementRouteKey(http.MethodGet, r.URL.Path)` **按完整路径精确匹配** `resourceRoutes`，
未注册的路径直接落到宿主 404，压根进不到 `handleManagementHandle` 的静态分发分支。
1.0.11 只改了分发条件却没把 `/accounts` 与 `/webui` 登记进 `Resources`。

### 13.2 处置（STYLE-01 / STYLE-04 维度）

- `management.go` 的 `Resources` 从 5 条扩到 7 条，补 `legacyPanelResourcePath` 与 `/webui`，两者 `Menu` 留空。
  与既有 `/login` 系列同样处理，保持「只有 `/panel` 挂菜单」的口径不变。
- `handleManagementRegister` 的 `[返回]` 注释同步从「5 条资源路由」改为「7 条资源路由」，并补一行改动原因。
- `login_bridge_test.go` 的 `TestManagementRegistrationIncludesLoginResources` 路径集合补 `/accounts` 与 `/webui`，
  Menu 断言集合同步扩充；并加注释说明「宿主精确匹配，只留分发分支而不注册必须 404」。

### 13.3 哨兵验证（复现本次缺陷）

删掉 `Resources` 里 `legacyPanelResourcePath` 那一条后重跑，测试立刻失败：

```
--- FAIL: TestManagementRegistrationIncludesLoginResources (0.09s)
    login_bridge_test.go:272: resource route "/accounts" not registered,
        got [/login/submit /login/poll /panel /webui /login /login/extension.zip]
```

恢复后 PASS。哨兵已撤销，工作树无残留。

### 13.4 版本处置

本次仍属同一次改造的收尾，但版本号升到 `1.0.12` 而不是复用 `1.0.11`：
宿主 `dlopen` 对同一路径会复用已存在的 handle，同版本覆盖安装（`overwritten=true`）不重新映射文件，
必须换新文件名才能让新镜像生效。这条约束已在 1.0.10 那次事故里实证过。

### 13.5 门禁（1.0.12 复跑）

```
STYLE: PASS
POLLUTION: PASS
FMT_OK: gemini-web2api/internal/cpa/login_bridge_test.go
FMT_PREEXISTING: gemini-web2api/internal/cpa/management.go（switch 内多一个空行，上游原样）
REAL_RUN: go build ./...  exit 0
          go vet ./...    exit 0
          go test -count=1 ./...  全绿（adapter 0.376s / balancer 1.083s / cpa 1.817s /
                                   gemini 0.289s / mcp 0.347s / proxydial 0.865s /
                                   requestdb 0.380s / tokencount 0.961s）
          python scripts\cgo-shim-build.py gemini-web2api  → all green
SENTINEL: 删 legacyPanelResourcePath 的 Resources → FAIL；恢复 → PASS
COMMENT_CHAIN: 改动位点（Resources 注释、handleManagementRegister 元信息）已同步
```

### 13.6 编码与行尾

`management.go` 与 `login_bridge_test.go` 均 UTF-8 无 BOM、工作树 CRLF、gofmt 仅剩上游原样的 switch 空行。
`VERSION` 6 字节 `1.0.12` 无换行；`CHANGELOG.md` CRLF 179 行。