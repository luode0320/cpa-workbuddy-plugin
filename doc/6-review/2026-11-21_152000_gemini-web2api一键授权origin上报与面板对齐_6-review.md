STYLE: PASS

# 6-review：gemini-web2api 一键授权 origin 上报与面板 workbuddy 对齐

- 审查对象：`gemini-web2api` 1.0.14（12 个已跟踪文件改动 + 2 个新增文件）
- 触发来源：用户指出「面板和 workbuddy 不太一样、那些功能没什么作用」与
  「`http://127.0.0.1:8317/v0/resource/plugins/gemini-web2api/login` 授权打不开，流程没闭环」
- 审查范围：只判写法、位置、格式、注释与既有习惯；不判业务正确性、需求覆盖与发布放行

## 1 静态 Owner 路由命中

命令：`python C:\Users\luode\.workbuddy\skills\code-style-consistency-rules\scripts\static_owner_router.py --changed "<14 个文件>" --root "C:\Users\luode\.workbuddy\skills"` → **exit 0**。

| 步骤 | 命中 | 规则 Owner |
|---|---|---|
| STYLE-01 静态格式与编码一致性 | 是 | code-style-consistency-rules |
| STYLE-02 命名与符号引用一致 | 是 | naming-rules |
| STYLE-03 注释分层与定义位置 | 是 | comment-rules |
| STYLE-04 函数签名与参数结构 | 是 | code-quality-rules |
| STYLE-05 结构与落点 | 是 | package-structure-rules |
| STYLE-08 语言与框架特异写法 | 是 | golang-patterns、frontend-ui-visual-rules |
| STYLE-09 测试资产与编码前契约 | 是 | test-program-rules、code-generation-style-rules |
| STYLE-06 并发原语改造 | 否 | 本轮新增的并发只有 `sync.RWMutex` 读写缓存，与 coze 既有实现同构，无新原语 |
| STYLE-07 前端资源构建链 | 否 | 面板与桥接页都是 `//go:embed` 单文件，无打包链 |

首次调用 exit 2：`来源映射校验失败：code-generation-style-rules/SKILL (1).md、
code-style-consistency-rules/references/user-style-feedback-library (1).md`。这两份是历史遗留的
`" (1)"` 副本，与规则内容无关。处置：`Copy-Item` 到 `$env:TEMP\zz_router_stash_gw2a6`
（a=9245 B、b=27919 B）→ `Remove-Item` 原文件 → 重跑 exit 0 → 拷回（回读 9245 / 27919 一致）
→ 删 stash（`Test-Path` = False）。**未修改任何技能包文件与来源映射。**

## 2 STYLE-01 目录位置与编码

新增的两个 Go 文件都落在业务包内，与被测代码同包/同目录，没有往根目录或 `test/` 镜像里塞：

| 文件 | 行数 | 字节 | 行尾 | BOM |
|---|---|---|---|---|
| `gemini-web2api/internal/cpa/public_origin.go` | 148 | 5215 | CRLF 148 / LF 0 | 无 |
| `gemini-web2api/internal/cpa/public_origin_internal_test.go` | 218 | 9188 | CRLF 218 / LF 0 | 无 |

改动文件的字节与行尾实测：

| 文件 | 字节 | CRLF | lone LF |
|---|---|---|---|
| `gemini-web2api/CHANGELOG.md` | 20575 | 282 | 0 |
| `gemini-web2api/VERSION` | 6 | 0 | 0（无换行，与既有口径一致） |
| `gemini-web2api/main.go` | 6125 | 226 | 0 |
| `gemini-web2api/internal/app/plugin_bridge.go` | 11258 | 363 | 0 |
| `gemini-web2api/internal/cpa/auth.go` | 28816 | 924 | 0 |
| `gemini-web2api/internal/cpa/login_bridge.go` | 17453 | 412 | 0 |
| `gemini-web2api/internal/cpa/login_bridge_test.go` | 18237 | 468 | 0 |
| `gemini-web2api/internal/cpa/management.go` | 28157 | 684 | 0 |
| `gemini-web2api/internal/cpa/plugin_test.go` | 32170 | 799 | 0 |
| `gemini-web2api/internal/cpa/webui.html` | 48356 | 865 | 0 |
| `gemini-web2api/internal/cpa/login_bridge.html` | 8503 | 0 | 191（`.gitattributes` 固定 `eol=lf`） |

`public_origin.go` 首版写成 LF，已转 CRLF 与同目录 Go 文件对齐（转后 `bytes=5215`，构建与测试复跑通过）。
`login_bridge.html` 与 `login_popup.js` 保持 LF —— `.gitattributes` 已把这两个 `//go:embed` 运行时输入
钉成 `text eol=lf`，`git ls-files --eol` 实测 `i/lf w/lf attr/text eol=lf`。

## 3 STYLE-02 命名

| 新增符号 | 判定 |
|---|---|
| `publicOrigin` / `publicOriginMu` | 与 `coze/bridge.go` 的 `publicOrigin` / `publicOriginMu` 同名同义，跨插件一致 |
| `rememberPublicOrigin` | 动词开头、表达副作用（写缓存），与 coze 完全一致 |
| `reportPublicOrigin` | 与 `remember-` 区分：前者来自浏览器主动上报，后者来自请求头被动观测 |
| `currentPublicOrigin` | 读取型命名，不带动词前缀，符合 Go 取值约定 |
| `splitOrigin` | 纯函数，返回 `(scheme, host)`；不叫 `parseOrigin` 是因为它丢弃路径，只取 origin |
| `isLoopbackHost` / `isLoopbackOrigin` | 前者判裸 host，后者判完整 origin，两个粒度分开命名避免误用 |
| `originFromHeaders` / `effectiveLoginOrigin` | 前者纯解析、后者带优先级回落，命名区分「取」与「选」 |
| `loginOriginPath` | 与既有 `loginSubmitPath` / `loginPollPath` / `loginExtensionPath` 同前缀同构 |
| `reportPanelOrigin`（前端） | 与后端 `handleLoginOrigin` 形成「上报 / 接收」配对，前缀 `report` 表示发起方 |

未出现缩写、拼音或不一致风格；后端仍用 `login*` 前缀，前端仍用 camelCase。

## 4 STYLE-03 注释

### 4.1 新增函数的元信息

对三个文件做脚本化核对（向上收集紧邻 `//` 注释块，断言同时含 `[参数]`、`[返回]`、`最近修改时间`）：

| 文件 | 顶层 func | 缺失 |
|---|---|---|
| `internal/cpa/public_origin.go` | 6 | 0 |
| `internal/cpa/public_origin_internal_test.go` | 8 | 0 |
| `internal/cpa/login_bridge.go` | 15 | 0 |

### 4.2 改动位点的论证式块注释

新增/修改的四类位点都写了「为什么这么写」而不是「做了什么」：

- `public_origin.go` 顶部：解释为什么需要包级缓存而不是每次从请求头取 —— 宿主链路拿不到 Host。
- `originFromHeaders` 的 Referer 分支：解释前两级恒为空时 Referer 是最后一道自动兜底。
- `reportPublicOrigin`：解释回环 origin 为什么不覆盖真实域名（容器内自测会把线上链接改回 127.0.0.1）。
- `management.go` 的 `rememberPublicOrigin(req.Headers)` 调用点：解释当前链路拿不到 Host，
  但只要哪天反代补上 `X-Forwarded-Host` 这条通道会自动接住，不需要再改代码。
- `auth.go` 的 `effectiveLoginOrigin(nil, req.BaseURL)`：解释宿主 `managementCallbackURL` 硬编码 loopback，
  直接用 BaseURL 会让弹窗里的链接打不开。
- `webui.html` 的 `reportPanelOrigin` 与 init 顺序注释：解释为什么必须在 `load()` 之前上报。

### 4.3 删除位点的注释处理

被删的 `publicOriginFromRequest`、`toggleService`、`relaunchGuest`、`stopGuest`、`loginWithChrome`
是**整函数删除**，不保留空壳注释；`renderSummary` 里被删掉的六指标段落同理不留 `// removed` 痕迹，
因为 `CHANGELOG.md` 的 1.0.14 小节已完整记录删除理由与后端端点的保留状态。

## 5 函数签名变更表

| 文件 | 符号 | 变更 |
|---|---|---|
| `internal/cpa/login_bridge.go` | `publicOriginFromRequest(req rpcManagementRequest) string` | **删除**（被 `effectiveLoginOrigin` 取代） |
| `internal/cpa/login_bridge.go` | `effectiveLoginOrigin(h http.Header, fallbackBaseURL string) string` | 新增 |
| `internal/cpa/login_bridge.go` | `originFromHeaders(h http.Header) string` | 新增 |
| `internal/cpa/login_bridge.go` | `isLoopbackOrigin(origin string) bool` | 新增 |
| `internal/cpa/login_bridge.go` | `handleLoginOrigin(req rpcManagementRequest) pluginapi.ManagementResponse` | 新增 |
| `internal/cpa/public_origin.go` | `rememberPublicOrigin` / `reportPublicOrigin` / `currentPublicOrigin` / `splitOrigin` / `isLoopbackHost` / `firstHeaderValue` | 新增（6 个） |
| `internal/cpa/webui.html` | `toggleService` / `relaunchGuest` / `stopGuest` / `loginWithChrome` | **删除**（4 个前端函数） |
| `internal/cpa/webui.html` | `reportPanelOrigin` | 新增 |
| `internal/cpa/webui.html` | `renderSummary(d)` | 签名不变，函数体改为 workbuddy 口径 |

后端 16 条管理路由与 `pluginabi` 的方法集合零变更，`AuthLoginStartResponse` 结构零变更
（只是在 `URL` / `Metadata` 里换成真实域名）。

## 6 STYLE-04 代码质量

### 6.1 最小改动

- `webui.html` 净减 37 行（34 增 / 71 删），删除项全部指向用户明确否定「这些功能没什么作用」的服务开关；
  没有顺手改样式、没有重排无关段落。
- `auth.go` 只改 7 增 1 删，集中在 `handleAuthLoginStart` 尾部 origin 计算，其余 900 多行未动。
- `management.go` 15 增 4 删：`Resources` 加 1 条 + 注释、`handleManagementHandle` 加 1 处
  `rememberPublicOrigin` 调用 + 注释、`[返回]` 文案同步。
- `plugin_test.go` 7 增 1 删：只把写死主机名的断言换成「绝对 URL + 路径后缀」。
- 新增文件都是纯新增，不侵入既有文件。

### 6.2 单文件行数

`public_origin.go` 148 行、`public_origin_internal_test.go` 218 行，均在 200 行上下；
`public_origin_internal_test.go` 略超 200 是因为 7 个用例都带完整元信息注释。这属于测试文件的
正常规模，不拆 —— 拆开会产生两个只差一个主题的测试文件，检索成本反而更高。

### 6.3 副作用可见性

`rememberPublicOrigin` / `reportPublicOrigin` 都是「写包级缓存」的副作用函数，命名用动词
`remember` / `report` 明示副作用；`currentPublicOrigin` / `originFromHeaders` / `splitOrigin`
是纯读取/纯解析，命名不带动词。`reportPanelOrigin`（前端）同理只用 `fetch` 不做 UI 改动，
失败只 `catch` 不弹 toast —— 上报失败不该打扰用户主流程。

### 6.4 错误处理

| 位点 | 处理 |
|---|---|
| `handleLoginOrigin` 缺 `origin` 参数 | 400 + `{"error":"origin is required"}`，不写入缓存 |
| `handleLoginOrigin` origin 非绝对 URL | 400 + `{"error":"origin must be an absolute http(s) URL"}`，不写入缓存 |
| `reportPublicOrigin` 收到回环 origin 且已有真实域名 | 静默忽略，保留真实域名（不是错误，是保护） |
| `reportPanelOrigin`（前端）上报失败 | `.catch` 空实现，只留一行注释说明不影响主流程 |
| 桥接页 `syncOrigin()` 上报失败 | `.catch` 空实现，页面已用本地 `location.origin` 就地改写过配对码 |
| `originFromHeaders` 无任何主机线索 | 返回空字符串，由 `effectiveLoginOrigin` 继续回落，不 panic 不猜默认值 |

## 7 STYLE-05 包结构

- `public_origin.go` 放 `internal/cpa`（而不是新建 `internal/origin` 子包）：origin 缓存只服务于
  登录面，与 `login_bridge.go` 强耦合，单独成包会制造无收益的层级。这是「最小实现优先级阶梯」
  第 2 级（复用现有包）而不是第 6 级（自建结构）。
- `public_origin_internal_test.go` 用**白盒包 `package cpa`**：需要直接读写 `publicOrigin` /
  `publicOriginMu`，避免为测试往生产代码里加导出的重置函数。同目录已存在
  `login_bridge_test.go`（`package cpa_test` 黑盒），两者并存不冲突 —— Go 允许同目录内
  `cpa` 与 `cpa_test` 两个测试包。
- 依赖方向未变：`internal/cpa` → `internal/pluginapi`，没有反向依赖，没有新增外部依赖。

## 8 STYLE-08 语言与框架特异写法

### 8.1 Go

- `sync.RWMutex` 保护 `publicOrigin`，读用 `RLock`、写用 `Lock`，临界区只有一次赋值或一次读取，
  无嵌套锁、无锁内调用外部函数。
- `splitOrigin` 用 `net/url.Parse` 而不是手写字符串切割（手写会踩 `//host/path`、`user:pass@host`
  等边界），解析失败按「非法」处理。
- `firstHeaderValue` 对逗号分隔的多值头取首段并 `TrimSpace`，与 coze 同实现。
- `isLoopbackHost` 处理三类输入：带方括号的 IPv6（`[::1]:8317`）、只有一个冒号的 host:port、
  裸 IPv6（`::1`）。首版对裸 `::1` 会 `LastIndex(":")` 截成 `::` 导致误判，已修正 ——
  这条是**跑测试才发现**的，属于哨兵价值的直接证据。
- 无 `any` 滥用、无 `interface{}`、无反射、无 `init()`。

### 8.2 前端

- `webui.html` 与 `login_bridge.html` 都只用原生 `fetch` + 原生 DOM，无框架、无构建链。
- `reportPanelOrigin` 用 `try/catch` 包住 `window.location.origin` 读取（跨域 iframe 场景会抛），
  并对 `origin === "null"` 做显式排除（`file://` 或沙箱 iframe 会给字面量 `"null"`）。
- 桥接页 `syncOrigin()` 在 `copyBtn` 绑定之前就执行，保证用户点「复制」时配对码已是真实域名。

### 8.3 能否被宿主正确嵌入

- 面板路径 `/panel` 与桥接页 `/login` 都注册在 `Resources` 里；新增的 `/login/origin` 也已注册。
  宿主 `ServeResourceHTTP` 按**完整路径精确匹配**，未注册即 404 —— 1.0.11 的历史路径 404 事故
  就是这么来的，本轮把它作为硬约束逐条核对。
- `Resources` 现在 8 条，其中只有 `/panel` 挂 `Menu`，其余 7 条 `Menu` 为空。宿主
  `registeredPluginMenus()` 只收集 `Menu` 非空的资源路由，所以左侧插件树仍只有一个
  `Gemini Web` 子项。

## 9 STYLE-09 测试资产与编码前契约

### 9.1 归位

`public_origin_internal_test.go` 与被测的 `public_origin.go` 同包同目录，没有往根 `test/`
镜像里放 Go 单测（项目约定：Go 单测与源码同包，`test/` 只放跨语言探针脚本）。

### 9.2 用例清单

`public_origin_internal_test.go`（7 个）：

| 用例 | 覆盖 |
|---|---|
| `TestSplitOrigin` | 只接受 http/https、丢弃路径与 query、非法输入回空 |
| `TestIsLoopbackHost` | `127.0.0.1` / `localhost` / `::1` / `[::1]:8317` / `0.0.0.0` / 公网域名 |
| `TestFirstHeaderValue` | 逗号取首段、两端空格、单值、空串 |
| `TestReportPublicOriginIgnoresLoopback` | 回环不覆盖真实域名；缓存为空时接受回环 |
| `TestEffectiveLoginOriginPriority` | 转发头 > 缓存 > 宿主 BaseURL 三级；回环头回落缓存 |
| `TestOriginFromHeadersRefererFallback` | 只有 Referer 时采用；转发头优先于 Referer；相对 Referer 回空；全空回空 |
| `TestHandleLoginOriginWritesCache` | 缺参 400 / 相对 origin 400 / 合法 200 且缓存写入 |

`login_bridge_test.go` 本轮新增/增补（共 10 个用例，含 2 个新增）：

| 用例 | 本轮变化 |
|---|---|
| `TestLoginBridgeRendersPairCode` | 增补「页面含 `/login/origin`」断言 |
| `TestManagementRegistrationIncludesLoginResources` | 路径集合加入 `/panel`、`/accounts`、`/webui`、`/login/origin`；Menu 空集合同步 |
| `TestLoginOriginEndpointRecordsOrigin` | 新增：缺参/相对 origin 各 400，合法 200 且回带 origin，随后 `auth.login.start` 返回真实域名 |
| `TestPanelReportsOrigin` | 新增：面板含 `login/origin` 上报；反向断言 `toggleService`/`relaunchGuest`/`stopGuest`/`loginWithChrome`/`chromeBtn` 已下线；标题为「账号汇总 · 全部账号」 |

### 9.3 哨兵验证

1. 把 `handleLoginResource` 的 `case loginOriginPath` 整行注释掉 →
   `--- FAIL: TestLoginOriginEndpointRecordsOrigin` /
   `login_bridge_test.go: expected 400 for missing origin, got 200` → 恢复后 PASS。
2. `isLoopbackHost` 首版对裸 `::1` 误判为「非回环」，`TestIsLoopbackHost` 直接 FAIL，
   修正实现后 PASS —— 说明该用例真的在拦回归，不是摆设。
3. `TestEffectiveLoginOriginPriority` 在 origin 缓存未做测试隔离时曾跨用例串味，
   为此新增 `resetPublicOriginForTest()` 并在每个相关用例里 `t.Cleanup` 复位。

### 9.4 生产代码污染

`python .../scan_test_pollution.py --root F:\cpa-plugin\gemini-web2api` → `POLLUTION: PASS`。
输出里的「疑似死代码」全部落在 upstream vendored 的文件
（`internal/pluginabi/types.go:137/141/142/143/145/146`、`internal/pluginapi/types.go:963-966`、
`internal/tokencount/counter_test.go:9`），非本轮引入，也无本轮为测试方便而加的生产导出符号。

## 10 门禁输出

```
STYLE: PASS
POLLUTION: PASS
FMT_OK: gemini-web2api/main.go
        gemini-web2api/internal/cpa/login_bridge.go
        gemini-web2api/internal/cpa/login_bridge_test.go
        gemini-web2api/internal/cpa/public_origin.go
        gemini-web2api/internal/cpa/public_origin_internal_test.go
FMT_PREEXISTING: gemini-web2api/internal/cpa/auth.go（import 分组顺序 + :744 多一个空行）
                 gemini-web2api/internal/cpa/management.go（:291 switch 内多一个空行）
                 gemini-web2api/internal/cpa/plugin_test.go（import 分组顺序）
                 gemini-web2api/internal/app/plugin_bridge.go（import 分组顺序 + 字段对齐）
                 —— 四处与上游 clone 逐字节对照，均非本轮引入，保持不动
REAL_RUN: cd F:\cpa-plugin\gemini-web2api && go build ./... && go vet ./... && go test -count=1 ./...
          → 全绿（adapter / balancer / cpa / gemini / mcp / proxydial / requestdb / tokencount 均 ok）
          python scripts\cgo-shim-build.py gemini-web2api → all green
          node --check（login_bridge.html 1 块 + webui.html 2 块，隔离目录 + commonjs）→ 3/3 exit 0
COMMENT_CHAIN: public_origin.go 6/6、public_origin_internal_test.go 8/8、login_bridge.go 15/15 元信息齐全
TEST_ASSET: 与被测包同目录，未污染生产代码，未新增导出测试钩子
ENCODING: 14 个文件全部 UTF-8 无 BOM；login_bridge.html 保持 LF（.gitattributes eol=lf），其余 CRLF
```

## 11 未命中说明

- **STYLE-06 并发原语改造**：本轮唯一的并发结构是 `sync.RWMutex` 读写一个字符串，与
  `coze/bridge.go` 的同名实现完全同构，没有新原语、没有 goroutine、没有 channel。
- **STYLE-07 前端资源构建链**：`webui.html` 与 `login_bridge.html` 都是 `//go:embed` 单文件，
  没有 npm / 打包 / 压缩步骤，改完即生效。

## 12 边界声明

- 本记录只判写法、位置、格式、注释与既有习惯，**不判业务正确性**。「授权链接是否真的能在
  用户浏览器打开」属于功能验证，实际口径是：生产 `install 1.0.14` 后由面板与桥接页分别上报
  `location.origin`，再调 `auth.login.start` 观察返回的 `URL` 是否落在真实域名上。
- 本轮**未修改宿主**（`internal/pluginhost`）与**未修改生产 nginx 配置**。宿主
  `cloneHeader` 不带 `Host` 是上游行为，nginx 缺 `X-Forwarded-Host` 是部署配置；
  两者都属外部系统，插件侧只做「能不依赖它们就不依赖」的兜底。
- 待用户裁定的遗留项（不属本轮范围）：`/accounts` 与 `/webui` 历史路径是否长期保留、
  65 个超 200 行 Go 文件是否豁免、schema 长期保持 6 还是跟宿主回升、是否 `go mod vendor`。
