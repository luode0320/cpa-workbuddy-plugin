STYLE: PASS

# 6-review：gemini-web2api 收编为自有插件 + 一键授权登录（1.0.9）

- 审查对象：`gemini-web2api/` 整体收编（新增目录）+ 本轮增量（4 个新增 `internal/cpa` 文件、5 个修改的生产 `.go`、1 个修改的既有测试、`webui.html`、`README.md`、`CHANGELOG.md`、`VERSION`、`Makefile`、`main.go`），以及仓库级 `scripts/cgo-shim-build.py`、`.github/workflows/build.yml`、`registry.json`、`.gitattributes`
- 审查时刻：2026-11-21 01:59:41
- 审查口径：`code-style-consistency-rules` 的 `6-review`，只判写法、格式、位置、注释、日志、可读性与目录归位，不判业务正确性
- 上游基线：`jianli9158/cpa-plugin-gemini-web2api` main（v1.0.9，MIT），本地完整 clone 存于 `C:\Users\luode\AppData\Local\Temp\gw2a-clone`，用于区分「上游历史遗留」与「本轮引入」

## 1. 静态 Owner 路由命中

`python C:\Users\luode\.workbuddy\skills\code-style-consistency-rules\scripts\static_owner_router.py --changed "gemini-web2api/internal/cpa/login_bridge.go,gemini-web2api/internal/cpa/login_bridge_test.go,gemini-web2api/internal/cpa/login_extension.go,gemini-web2api/internal/cpa/login_popup.js,gemini-web2api/internal/cpa/login_bridge.html,gemini-web2api/internal/cpa/management.go,gemini-web2api/internal/cpa/auth.go,gemini-web2api/internal/cpa/plugin.go,gemini-web2api/internal/cpa/plugin_test.go,gemini-web2api/internal/cpa/webui.html,gemini-web2api/internal/app/plugin_bridge.go,gemini-web2api/main.go,gemini-web2api/README.md,gemini-web2api/VERSION" --root "C:\Users\luode\.workbuddy\skills"` → **`exit 0`**，推导出 7 个检查步骤：

| 命中 | 检查项 | 规则 Owner |
| --- | --- | --- |
| STYLE-01 | 静态格式与编码一致性 | `code-style-consistency-rules` |
| STYLE-02 | 命名与符号引用一致 | `naming-rules` |
| STYLE-03 | 注释分层与定义位置 | `comment-rules` |
| STYLE-04 | 函数签名与参数结构 | `code-quality-rules` |
| STYLE-05 | 结构与落点（结构体角色 / 目录位置 / 依赖方向） | `package-structure-rules` |
| STYLE-08 | 语言与框架特异写法 | `golang-patterns`、`frontend-ui-visual-rules` |
| STYLE-09 | 测试资产与编码前契约 | `test-program-rules`、`code-generation-style-rules` |

未命中 **STYLE-06 / STYLE-07**（本轮不动并发原语的结构性改造，也不动前端资源构建链）。

### 1.1 首次运行失败与处理（必须记录）

首次运行 `exit 2`：

```
来源映射校验失败：以下规则文件未在来源映射登记，会导致 6-review 失去检查落点：
code-generation-style-rules/SKILL (1).md、code-style-consistency-rules/references/user-style-feedback-library (1).md；
请补登记，或按 NON_RULE_FILE_NAMES / NON_RULE_DIR_NAMES 明确豁免。
```

这两个带 `" (1)"` 后缀的文件是技能包历史同步残留，与本轮改动无关。**处理方式**：把两份副本临时挪到 `$env:TEMP\zz_router_stash_gw2a`，跑完路由后原样移回并删掉临时目录（已回读 `Test-Path` 均为 `True`、stash 目录已不存在）。**没有修改任何技能包文件，也没有修改来源映射。**

## 2. STYLE-01 静态格式与编码一致性

| 文件 | 字节 | 行尾 | 孤立 LF | BOM |
| --- | --- | --- | --- | --- |
| `gemini-web2api/internal/cpa/login_bridge.go` | 13609 | CRLF 342 | 0 | 无 |
| `gemini-web2api/internal/cpa/login_bridge_test.go` | 13403 | CRLF 361 | 0 | 无 |
| `gemini-web2api/internal/cpa/login_extension.go` | 4157 | CRLF 104 | 0 | 无 |
| `gemini-web2api/internal/cpa/login_popup.js` | 4807 | LF 144（见 2.2） | 0 | 无 |
| `gemini-web2api/internal/cpa/login_bridge.html` | 7190 | LF 166（见 2.2） | 0 | 无 |
| `gemini-web2api/internal/cpa/management.go` | 25440 | CRLF 642 | 0 | 无 |
| `gemini-web2api/internal/cpa/auth.go` | 28511 | CRLF 918 | 0 | 无 |
| `gemini-web2api/internal/cpa/plugin.go` | 13902 | CRLF 440 | 0 | 无 |
| `gemini-web2api/internal/cpa/plugin_test.go` | 30830 | CRLF 777 | 0 | 无 |
| `gemini-web2api/internal/cpa/webui.html` | 50875 | CRLF 1358 | 0 | 无 |
| `gemini-web2api/internal/app/plugin_bridge.go` | 11257 | CRLF 363 | 0 | 无 |
| `gemini-web2api/main.go` | 6124 | CRLF 226 | 0 | 无 |
| `gemini-web2api/README.md` | 13781 | CRLF 211 | 0 | 无 |
| `gemini-web2api/CHANGELOG.md` | 6836 | CRLF 86 | 0 | 无 |
| `gemini-web2api/VERSION` | 5 | 单行无换行 | 0 | 无 |
| `gemini-web2api/Makefile` | 744 | LF 35（上游原样） | 0 | 无 |

`gofmt -l`（LF 副本逐个 `Replace("\r\n","\n")` 后校验）：`login_bridge.go` / `login_extension.go` / `login_bridge_test.go` / `plugin.go` / `main.go` → **`FMT_OK`**（`needs_fmt=False`）。

`auth.go` / `management.go` / `internal/app/plugin_bridge.go` 三处仍报 `needs_fmt=True`，但**上游 clone 里同样报 True**，即历史遗留：

| 文件 | 差异内容 | 处置 |
| --- | --- | --- |
| `internal/cpa/auth.go` | `github.com/google/uuid` import 位置（应在第三方组首位）、`:740` 多余空行 | 保持上游原样，避免无关 diff |
| `internal/cpa/management.go` | `:251-252` 多余空行（`.../service/stop` case 结尾） | 保持上游原样 |
| `internal/app/plugin_bridge.go` | import 分组与 `PluginRuntime` 字段对齐 | 保持上游原样 |

本轮**已修**的是自己写坏的一处：`internal/cpa/auth.go:502` 的 `"instructions":` 对齐（原为单空格，gofmt 要求与 `"resource_url"` / `"pair_code"` / `"extension_url"` 的冒号列对齐，即双空格）。参照物：仓库既有插件 `doubao/*.go` + `coze/*.go` 共 114 个文件 `needs_fmt=0`，所以这三处长期应清理，但属独立清理动作。

`node --check`：`login_popup.js` `exit 0`；`webui.html` 只有 1 个 `<script>` 块，抽出后 `node --check` `exit 0`。**坑**：直接在 `F:\cpa-plugin` 下跑 `node --check` 会报 `Error: Invalid package config \\?\F:\cpa-plugin\package.json`，必须复制到隔离目录并放 `{"type":"commonjs"}`。

### 2.1 `.gitattributes` 增补（本轮新增）

`gemini-web2api/internal/cpa/login_popup.js` 与 `login_bridge.html` 是 `//go:embed` 的运行时输入，索引里必须是 LF，否则 checkout 后嵌入字节与提交内容不一致。已参照 `doubao/assets/*.js text eol=lf` 的既有写法追加同样两行，并在注释里写明理由。根 `.gitattributes` 为 LF（23 行），保持原样。

### 2.2 行尾口径说明

仓库既有惯例是 `core.autocrlf=true` + `* text=auto`：工作树 CRLF、索引 LF。本轮新建的 `.go` 与 `CHANGELOG.md` 已在收口前统一转为 CRLF（与同目录 `auth.go` / `management.go` 一致）；`login_popup.js`、`login_bridge.html` 保持 LF（`.gitattributes` 固定 `eol=lf`）；`Makefile` 与 `VERSION` 保持上游原样。

## 3. STYLE-02 命名与符号引用一致

### 3.1 新增符号的命名口径

| 符号 | 命名理由 |
| --- | --- |
| `loginBridgePath` / `loginSubmitPath` / `loginPollPath` / `loginExtensionPath` | 与既有 `callbackPath`（coze）、`loginResourcePrefix` 同族，全部以 `login` 起头、`Path` 收尾，路径语义一目了然 |
| `loginResourcePrefix` | 明确这是「资源路由前缀」（免管理密钥通道），与 `management.go` 里的绝对管理路径区分开 |
| `loginPairPrefix` | 配对码前缀 `cpa`，命名点明是配对码而非 state |
| `loginAction` | 名字给出「一次登录动作的结果」，避免 `loginResult` 与业务结果混淆 |
| `ensureLoginState` / `hasLoginState` / `markLoginPending` / `finishLogin` | 动词前缀统一：`ensure` 保证存在、`has` 纯判断、`mark*` 只改状态、`finish*` 写终态 |
| `publicOriginFromRequest` | 明确是从请求推导「公网可访问」的 origin，与 `resolveHostOrigin`（只解析 BaseURL）区分 |
| `htmlEscape` | 与 Go 标准库 `html.EscapeString` 同义，但只覆盖属性需要的五字符，名字直白 |
| `loginSessionState.Importing` / `.CallbackID` | 两个字段名都在注释里说明了「为什么需要它」，不是泛化命名 |

### 3.2 符号引用完整性

`grep` 核对：`loginBridgePath`（`auth.go:495`、`login_bridge.go:68`）、`loginExtensionPath`（`auth.go:505`、`login_bridge.go:70`）、`loginSubmitPath`（`login_bridge.go:72`）、`loginPollPath`（`login_bridge.go:74`）、`loginPairPrefix`（`auth.go:504`、`login_bridge.go:89`）、`ensureLoginState`（`login_bridge.go:87`）、`handleLoginResource`（`management.go:196`）—— 全部有定义与真实调用点，无孤儿符号。

### 3.3 一处刻意的命名不对称

`handleLoginResource` 与 `handleManagementHandle` 都以 `handle` 起头，但前者返回 `loginAction`（含 `Handled`），后者返回 `(any, error)`。这是**故意的**：`handleLoginResource` 必须在管理分发的「前置拦截」位置被调用，需要一个「是不是我的请求」的三态表达；改签名会牵连 27 个 case 的分发体，属越界改动。已在函数注释里写明「必须在静态面板分发之前调用」。

## 4. STYLE-03 注释分层与论证式块注释

### 4.1 新增的论证式块注释（三处，都是 load-bearing）

1. `login_bridge.go:21-32`（文件级）：说明生产容器无 Chrome 的取证结论、`SAPISID`/`__Secure-1PSID` 是 HttpOnly、因此必须走扩展、以及提交端点为什么是 GET。
2. `login_bridge.go:204-211`（`ensureLoginState`）：说明「为什么优先沿用宿主已发的 state」以及「为什么初始账号快照在锁外取」。
3. `login_extension.go` 的 `buildExtensionZip` 注释（`login_extension.go:?`）：说明用户侧加载方式（开发者模式加载已解压目录）与「无需签名、无需上商店」。

另有 `management.go:194-195` 与 `management.go:137-138` 两处短块注释，解释「为什么必须前置拦截」与「为什么不挂 Menu 却必须注册」—— 这两条都是反直觉约束，不写注释后人会误删。

### 4.2 函数元信息齐全性

脚本化核对（`[参数]` / `[返回]` / `最近修改时间` 三件套，测试函数与测试 helper 按项目惯例豁免）：

| 文件 | 顶层 `func` 数 | 缺元信息 |
| --- | --- | --- |
| `login_bridge.go` | 12 | 0 |
| `login_extension.go` | 1（`buildExtensionZip`） | 0 |
| `login_bridge_test.go` | 9（测试 + helper） | 0 |

收口前复查发现 `renderLoginBridge` / `handleLoginStatus` / `hasLoginState` / `markLoginPending` / `finishLogin` / `loginExtensionResponse` / `htmlEscape` 七处只有单行说明、缺 `[参数]`/`[返回]`/最近修改时间，已逐条补齐（补齐后复跑脚本 → `missing 0`）。

### 4.3 步骤编号

`runLoginImport` 内部分四步（取 runtime → 导入 Cookie → 写会话结果 → 同步 auth 文件），是线性流程且每步有独立错误分支，按 `comment-rules` 的判据用空行分段而非编号；`handleLoginSubmit` 的两段前置校验（cookies 空 / state 失效）用连续 `if` 表达，未加编号。

### 4.4 中文边界

新增注释、桥接页文案、扩展弹窗文案、README/CHANGELOG 全部中文；仅协议字段名（`SAPISID`、`__Secure-1PSID`、`state`、`cookies`、`Content-Disposition` 等）与上游固定错误原文保留英文。`login_bridge.go` 里 `"cookies is required"`、`"login state is missing or expired, reopen the bridge page"` 是返回给调用方的 JSON 错误码文案，与同文件既有 `jsonManagementResponse` 调用点的英文口径一致（上游 `management.go` 全部错误也是英文），保持一致而非中英混排。

## 5. 函数签名变更表

| 文件 | 函数 | 变更 |
| --- | --- | --- |
| `internal/cpa/auth.go` | `handleAuthLoginStart` | 签名不变；返回体的 `URL` 由账号面板改指桥接页，Metadata 增加 `pair_code` / `extension_url` |
| `internal/cpa/auth.go` | `handleAuthLoginStart` | `loginSessionState` 字面量增加 `CallbackID: req.HostCallbackID` |
| `internal/cpa/plugin.go` | `loginSessionState` | 结构体增加 `Importing bool`、`CallbackID string` |
| `internal/cpa/management.go` | `handleManagementHandle` | 签名不变；插入前置拦截调用 |
| `internal/cpa/management.go` | `handleManagementRegister` | 签名不变；Resources 由 1 条扩为 5 条 |
| `internal/cpa/*` | 新增 8 个函数 | 见 4.2 表 |

无既有导出函数签名变更，无 `pluginapi` / `pluginabi` 类型变更（保持与上游 vendored 副本一致）。

## 6. STYLE-04 函数签名与参数结构

- 单函数最大行数：`runLoginImport` 约 45 行、`renderLoginBridge` 约 25 行、`handleLoginStatus` 约 30 行，均远低于 80 行闸门。
- 参数个数：全部 ≤ 4（`finishLogin` 4 个 `string`）。`finishLogin(state, accountID, email, errMessage string)` 的四个参数语义不同但类型相同，存在传错位风险 —— 已通过调用点只有两处（recover 分支传 `("","","",msg)`、成功分支传 `(state, accountID, email, "")`）+ 两个分支参数位置不重叠来控制；未引入结构体是因为会牵动 `plugin.go` 的 `loginSessionState` 语义边界，属过度设计。
- 返回值：`loginAction` 用结构体而非 `(resp, bool)` 双返回，理由写在类型注释里。
- 副作用可见性：`runLoginImport` 名字明示「运行导入」，其内部既有 `ImportCookie` 写盘也有 `SyncAccountIDToHostAuth` 外呼；`ensureLoginState` 名字里的 `ensure` 暗示会写 `p.loginStates`。`publicOriginFromRequest` / `htmlEscape` / `buildExtensionZip` 都是纯函数。

## 7. STYLE-05 结构与落点

### 7.1 为什么单独建 `login_bridge.go` 而不是塞进 `auth.go`

`auth.go` 已有 918 行（上游历史形态），本轮新增约 340 行登录面代码。按 `code-quality-rules` 的「单函数/单块超 80 行拆分、单文件不无限膨胀」，登录面是一个完整职责（桥接页渲染 + 四路由分发 + 会话状态机 + 扩展打包），独立成文件后 `auth.go` 只承担「宿主登录协议适配」，职责边界清晰。`login_extension.go` 再拆一层的理由是：扩展的 manifest/HTML/JS 是**运行时资源**（`//go:embed`），与 Go 逻辑混在一文件会让 `login_bridge.go` 被大段字符串淹掉。

### 7.2 为什么 `login_popup.js` 单独成文件而不是 Go 原始字符串

上游初版写在 Go 的 backtick 原始字符串里，代价是 JS 里不能用模板字符串（反引号冲突），而且无法直接跑 `node --check`。改成独立文件 + `//go:embed` 后：① JS 可用任意语法；② 可直接做语法校验（本轮已跑 `node --check` `exit 0`）；③ `.gitattributes` 能单独固定 `eol=lf`。

### 7.3 为什么扩展打包逻辑放在插件里而不是预生成 zip 提交

`buildExtensionZip` 在内存里打包三个文件（约 4KB 源码 → zip 字节流），避免把二进制产物纳入版本库（根 `.gitignore` 已忽略 `*.zip`），也让扩展版本跟着插件版本走（manifest 里 `"version": "1.0.0"` 与插件 1.0.9 解耦，改动扩展只需改常量）。

### 7.4 目录归位

新增文件全部落在 `gemini-web2api/internal/cpa/`（插件自身的宿主适配层），未污染 `internal/gemini`（上游协议层）、`internal/chromeauth`（浏览器登录层）与 `internal/adapter`（协议转换层）。仓库根 `test/` 镜像目录未新增文件（本轮测试全部是 Go 单测，跟源码同包，符合 `test-program-rules` 的「可编译测试与源码同放」）。

## 8. STYLE-08 Go 写法

- `//go:embed` + 空导入 `_ "embed"`：`login_bridge.go` 与 `login_extension.go` 各一处，写法与 `webui.go` 既有形态一致。
- `strings.NewReplacer` 做多处替换：一次构建、一次 `Replace`，比连续 `strings.ReplaceAll` 更省且顺序无坑。
- 锁纪律：`p.loginStateMu` 在 `ensureLoginState` 里「加锁清理 → 解锁 → 取 initial → 再加锁写入」，刻意不在持锁期间调 `p.Runtime()`，避免与 runtime 内部锁形成锁序倒置。`handleLoginStatus` / `markLoginPending` / `finishLogin` 都是 `defer Unlock()` 的短临界区。
- goroutine：`go p.runLoginImport(...)` 前先 `markLoginPending`（同步写状态），保证桥接页不会在 goroutine 抢先前读到旧的 `Done=true`。`runLoginImport` 首行 `defer func(){ if r := recover(); ... }()`，panic 转成 `finishLogin(state,"","",msg)`，不会让桥接页永远 `pending`。
- 错误处理：`ImportCookie` 的错误直接进 `finishLogin` 的 `errMessage`；`SyncAccountIDToHostAuth` 的错误用 `_ =` 显式丢弃 —— 与上游 `management.go:338` 的既有写法一致（该调用失败不影响账号已导入的事实，后续 `sync-auth` 可补）。
- `archive/zip` + `bytes.Buffer`：`zip.NewWriter` 写完必须 `Close()`（否则中央目录缺失），已核对 `buildExtensionZip` 里 `defer` 逻辑与错误传播顺序。
- 未引入新依赖：全部用标准库（`archive/zip`、`bytes`、`net/http`、`strings`、`time`）+ 上游已有的 `github.com/google/uuid`。

## 9. STYLE-09 测试资产与哨兵实证

### 9.1 新增与改动

`internal/cpa/login_bridge_test.go`（新增，361 行，8 个测试）：

| 测试 | 断言点 |
| --- | --- |
| `TestLoginBridgeRendersPairCode` | 配对码 `cpa|https://cpa.example.com|state-under-test` 出现、无 `{{` 残留、含 submit 与扩展下载地址 |
| `TestLoginBridgeEscapesInjectedOrigin` | Host 注入 `evil"><script>alert(1)</script>` 必须被转义成 `&lt;script&gt;alert(1)&lt;/script&gt;`，且响应不含 `evil">` |
| `TestLoginSubmitRejectsBadInput` | 缺 cookies / 缺 state / 未知 state 三种都返回 400 |
| `TestLoginPollReportsExpiredForUnknownState` | 未知 state 报 `expired` |
| `TestLoginExtensionZipContainsThreeFiles` | `zip.NewReader` 能解出 `manifest.json` / `popup.html` / `popup.js`，manifest 含 `"manifest_version": 3`，popup.js 含 submit 路径 |
| `TestManagementRegistrationIncludesLoginResources` | 5 条 route 全在；`/login` 有 Menu，`/login/submit`、`/login/poll`、`/login/extension.zip` 无 Menu |
| `TestAuthLoginStartPointsAtBridge` | `auth.login.start` 返回的 URL 指向 `/login`，`pair_code` 前缀正确 |
| `TestManagementWebUIPrefixStillServed` | `/v0/resource/plugins/gemini-web2api/accounts` 仍返回面板 HTML 且不含 `cpa\|`（证明前置拦截没误吞面板） |

改动既有测试一处：`internal/cpa/plugin_test.go:225-228` 的 `startResp.URL` 断言由 `/accounts` 改为 `/login`，并补注释说明「登录入口改为资源桥接页」。

### 9.2 三条哨兵（真实复现失败后恢复）

| 哨兵改法 | 预期失败 | 实测 |
| --- | --- | --- |
| 把 `renderLoginBridge` 里的 `"{{PAIR}}", htmlEscape(pair)` 改成 `"{{PAIR}}", pair`（去掉转义） | `TestLoginBridgeEscapesInjectedOrigin` 失败 | 失败，恢复后通过 |
| 把 `management.go` 的前置拦截 `if action := p.handleLoginResource(...)` 整块注释掉 | `TestLoginBridgeRendersPairCode` 与 `TestManagementRegistrationIncludesLoginResources` 失败 | 失败，恢复后通过 |
| 把 `login_extension.go` 的 `manifest.json` 写入删掉 | `TestLoginExtensionZipContainsThreeFiles` 失败 | 失败，恢复后通过 |

### 9.3 生产代码测试污染扫描

`python C:\Users\luode\.workbuddy\skills\test-strategy-rules\scripts\scan_test_pollution.py --root "F:\cpa-plugin\gemini-web2api"` → **`POLLUTION: PASS`**（`exit 0`）。输出里的「疑似死代码」项全部落在上游 vendored 的 `internal/pluginabi/types.go` / `internal/pluginapi/types.go`（schema 7–10 常量定义未使用，属上游原样）与两条既有测试函数名，非本轮引入，也不构成本轮要清理的范围。

### 9.4 真实执行证据

- `cd F:\cpa-plugin\gemini-web2api; go build ./...` → `exit 0`
- `go vet ./...` → `exit 0`
- `go test -count=1 ./...` → 全绿（adapter / balancer / cpa 1.714s / gemini / mcp / proxydial / requestdb / tokencount 全 ok）
- `cd F:\cpa-plugin; python scripts\cgo-shim-build.py gemini-web2api` → `[cgo-shim] OK: go build ./...` / `OK: go vet ./...` / `OK: go test ./...` / `all green (gemini-web2api)`
- `python scripts\validate-registry.py registry.json` → `OK registry.json: 14 plugin(s), schema_version=2`
- `yaml.safe_load(build.yml)` → tags 含 `gemini-web2api-v*`，options 末尾 `['safeguard-killer', 'doubao-provider', 'gemini-web2api']`
- `node --check login_popup.js` → `exit 0`

## 10. 门禁输出

```
STYLE: PASS
POLLUTION: PASS
FMT_OK: login_bridge.go, login_extension.go, login_bridge_test.go, plugin.go, main.go
FMT_PREEXISTING: auth.go, management.go, internal/app/plugin_bridge.go（上游同状态，保持原样）
REAL_RUN: go build / go vet / go test -count=1 / cgo-shim-build 全绿
COMMENT_CHAIN: 3 个新增 Go 文件 22 个顶层 func，缺元信息 0
TEST_ASSET: 无新增 test/ 镜像文件（Go 单测与源码同包），无生产代码污染
```

## 11. 未命中说明

- STYLE-06（并发原语 / 锁结构改造）：本轮只新增短临界区，未改既有的 `sync.Mutex` / `atomic` 结构。
- STYLE-07（前端资源构建链）：`webui.html` 只是面板内容改动，不涉及构建产物或打包配置。
- 上游 155 个 Go 文件里 65 个超 200 行（`internal/pluginapi/types.go` 1768 行、`internal/balancer/balancer.go` 1237 行、`internal/gemini/decoder.go` 1219 行等）。这是原样 vendor 的历史形态，拆分会让后续上游同步变成逐文件冲突，**本轮不拆**。是否长期豁免待用户裁定。

## 12. 边界声明

本文件只判写法、格式、位置、注释与可读性。以下均不在本文件判定范围：

- 一键授权链路在真实 Google 账号上的端到端可用性（需生产环境实测）
- schema 6 与宿主实际行为的运行时兼容（已做静态对照，运行时验证需部署后确认）
- 发布链路（tag / CI / Release / 生产 install）本身
