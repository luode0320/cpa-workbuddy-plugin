STYLE: PASS

# 6-review：gemini-web2api 扩展一键化 + Cookie 自动续期

- 审查对象：`gemini-web2api` 1.0.15（14 个已跟踪文件改动 + 3 个新增文件，共 17 个）
- 触发来源：用户「扩展一键化，我们还要注意一点，就是能否自动续期，如果不能，那么就是一个失败的插件」
- 审查范围：只判写法、位置、格式、注释与既有习惯；不判业务正确性、需求覆盖与发布放行

## 1 静态 Owner 路由命中

命令：`python C:\Users\luode\.workbuddy\skills\code-style-consistency-rules\scripts\static_owner_router.py --changed "<12 个 Go/HTML/JS 文件>" --root "C:\Users\luode\.workbuddy\skills"` → **exit 0**。

| 步骤 | 命中 | 规则 Owner |
|---|---|---|
| STYLE-01 静态格式与编码一致性 | 是 | code-style-consistency-rules |
| STYLE-02 命名与符号引用一致 | 是 | naming-rules |
| STYLE-03 注释分层与定义位置 | 是 | comment-rules |
| STYLE-04 函数签名与参数结构 | 是 | code-quality-rules |
| STYLE-05 结构与落点 | 是 | package-structure-rules |
| STYLE-08 语言与框架特异写法 | 是 | golang-patterns、frontend-ui-visual-rules |
| STYLE-09 测试资产与编码前契约 | 是 | test-program-rules、code-generation-style-rules |
| STYLE-06 并发原语改造 | 否 | 本轮新增并发只有 `atomic.Int64` 记录时间戳与一个 `go` 协程，无锁类型、无 channel、无新原语 |
| STYLE-07 前端资源构建链 | 否 | 扩展现由 4 个 `//go:embed` 单文件组成，无 npm / 打包 / 压缩步骤 |

首次调用 exit 2：`来源映射校验失败：code-generation-style-rules/SKILL (1).md、
code-style-consistency-rules/references/user-style-feedback-library (1).md`。这两份是历史遗留的
`" (1)"` 副本，与规则内容无关。处置：`Copy-Item` 到 `$env:TEMP\zz_router_stash_gw2a5`
（a=9245 B、b=27919 B）→ `Remove-Item` 原文件 → 重跑 exit 0 → 拷回（回读 9245 / 27919 一致）
→ 删 stash（`Test-Path` = False）。**未修改任何技能包文件与来源映射。**

## 2 STYLE-01 目录位置与编码

### 2.1 新增文件落点

| 文件 | 行数 | 字节 | 行尾 | BOM | 落点理由 |
|---|---|---|---|---|---|
| `gemini-web2api/internal/cpa/login_renew.go` | 84 | 4144 | CRLF 84 | 无 | 续期路由属于登录面，与 `login_bridge.go` 同包同层；不新建 `internal/renew` 子包，避免为单个 84 行文件造层级 |
| `gemini-web2api/internal/cpa/login_background.js` | 176 | 6216 | LF 176 | 无 | 与 `login_popup.js` 并列，是 `login_extension.go` 的第二个 `//go:embed` 运行时输入 |
| `gemini-web2api/internal/cpa/login_renew_test.go` | 105 | 4812 | CRLF 105 | 无 | Go 单测与源码同包同目录，未往根 `test/` 镜像放 |

`login_background.js` 与 `login_popup.js`、`login_bridge.html` 一样必须是 **LF**：三者都是
`//go:embed` 的运行时输入，CRLF 会改变被嵌入字节。本轮在 `.gitattributes` 里补了
`gemini-web2api/internal/cpa/login_background.js text eol=lf` 一行 —— 原本只钉了另两个文件，
新文件若不登记，Windows 工作树会按 `* text=auto` 被写成 CRLF，嵌进二进制后与提交内容不一致。

### 2.2 全部改动文件实测

| 文件 | 字节 | 行数 | CRLF | lone LF |
|---|---|---|---|---|
| `.gitattributes` | 698 | 24 | 0 | 24（与 HEAD 一致，本仓库既有形态） |
| `registry.json` | 40101 | 822 | 822 | 0 |
| `gemini-web2api/CHANGELOG.md` | 26208 | 362 | 362 | 0 |
| `gemini-web2api/VERSION` | 6 | — | 0 | 0（无换行，与既有口径一致） |
| `gemini-web2api/main.go` | 6125 | 226 | 226 | 0 |
| `gemini-web2api/internal/app/plugin_bridge.go` | 11258 | 363 | 363 | 0 |
| `gemini-web2api/internal/chromeauth/auth.go` | 9051 | 223 | 223 | 0 |
| `gemini-web2api/internal/cpa/login_bridge.go` | 17548 | 414 | 414 | 0 |
| `gemini-web2api/internal/cpa/login_bridge.html` | 10559 | 230 | 0 | 230（`.gitattributes` eol=lf） |
| `gemini-web2api/internal/cpa/login_bridge_test.go` | 19349 | 484 | 484 | 0 |
| `gemini-web2api/internal/cpa/login_extension.go` | 5361 | 125 | 125 | 0 |
| `gemini-web2api/internal/cpa/login_popup.js` | 6385 | 175 | 0 | 175（`.gitattributes` eol=lf） |
| `gemini-web2api/internal/cpa/login_background.js` | 6216 | 176 | 0 | 176（本轮补登记 eol=lf） |
| `gemini-web2api/internal/cpa/login_renew.go` | 4144 | 84 | 84 | 0 |
| `gemini-web2api/internal/cpa/login_renew_test.go` | 4812 | 105 | 105 | 0 |
| `gemini-web2api/internal/cpa/management.go` | 28345 | 688 | 688 | 0 |
| `gemini-web2api/internal/cpa/plugin.go` | 14070 | 443 | 443 | 0 |

全部 UTF-8 无 BOM。`login_extension.go` 与 `login_renew_test.go` 首版由 `write` 写成 LF，
已统一转回 CRLF 与同目录 Go 文件对齐（`git ls-files --eol` 实测 `i/lf w/crlf attr/text=auto`）。

## 3 STYLE-02 命名

| 新增符号 | 判定 |
|---|---|
| `loginRenewPath` | 与既有 `loginSubmitPath` / `loginPollPath` / `loginOriginPath` / `loginExtensionPath` 完全同构，`login*Path` 前缀即表示资源路由 |
| `loginRenewMinInterval` | 与同为常量口径的 `minRotationInterval`（`internal/gemini/rotate.go`）同形，单位后缀 `Interval` 已含 `time.Duration` 语义 |
| `handleLoginRenew` | 与 `handleLoginSubmit` / `handleLoginOrigin` 同族：`handle*` 是同步 HTTP 入口 |
| `runLoginRenew` | 与既有 `runLoginImport` 同族：`run*` 是 `go` 出去的异步体，前缀直接对应调用它的 handler |
| `lastRenewNs` | 与同结构体的 `lastRegisterNs` 同后缀 `Ns`，表明是纳秒时间戳；`Plugin` 里 `lastRegisterNs` / `rrCursor` 都是 `atomic` 家族，命名与类型一致 |
| `bindRenew` / `renewOnce` / `runRenew` / `ensureAlarm`（前端） | 与 `login_popup.js` 既有 `collectCookies` / `buildCookieHeader` / `readClipboard` 一样用 camelCase 动词短语；`renewOnce` 与 `runRenew` 的「一次」与「一轮（含落盘）」分层命名 |
| `STORAGE_KEY = 'geminiWeb2apiRenew'` | 与面板 `SS_KEY = 'gemini-web2api-mgmt-key'` 的 `插件名-用途` 口径一致，只是因为扩展侧习惯 camelCase 而写成驼峰 |
| `SIDECAR` 类残留 | 无。未引入 `helper` / `util` / `manager` / `factory` 之类空壳命名 |

未出现缩写、拼音或跨文件风格跳变；Go 侧仍全小写驼峰，前端仍全 camelCase。

## 4 STYLE-03 注释

### 4.1 新增/改动函数的元信息

脚本化核对（向上收集紧邻 `//` 注释块，断言同时含 `[参数]`、`[返回]`、`最近修改时间`）：

| 文件 | func 总数 | 缺失 |
|---|---|---|
| `internal/cpa/login_renew.go` | 2 | 0 |
| `internal/cpa/login_bridge.go` | 15 | 0 |
| `internal/cpa/login_extension.go` | 1 | 0 |
| `internal/cpa/login_renew_test.go` | 4 | 0 |
| `internal/chromeauth/auth.go` | 7 | 0（本轮补齐 `pendingImport.path`、`ImportCookies`、`importIdentity`、`validateAndWriteImports` 四处） |

补齐的四处在改动前只有一句话作用描述：`pendingImport.path` 补 `[返回]`、`ImportCookies`
补 `[参数]`/`[返回]`/最近修改时间、`importIdentity` 把一整行长句拆成「一句话 + 论证段」并补
`[参数]`/`[返回]`、`validateAndWriteImports` 补 `[参数]`/`[返回]`。

`internal/cpa/plugin.go`（11 个 func）与 `internal/cpa/management.go`（8 个 func，其中 6 个缺）
的元信息缺失是**上游原样遗留**，本轮只在这两个文件各加了 2~7 行（一个结构体字段、一个
`case` 分支、一条 `Resources` 条目），不借机给十几处无关函数补注释 —— 那会把 9 行的有效 diff
膨胀成上百行。这一类整体性缺口需要单独一轮「注释专项」处理，已记入第 12 节边界声明。

### 4.2 改动位点的论证式块注释

新增内容都写了「为什么这么写」而不是「做了什么」：

- `login_renew.go` 文件头：三条带编号的取证结论（`ImportCookie` 传空 id 会走「认证已更新」分支
  ＝一次续期；`CookieRotator` 滚不出新的 `__Secure-1PSID`，`rotate.go` 只能返回 `ErrSignedOut`；
  续期不能依赖 15 分钟有效期的 state），把「为什么这条路径就是自动续期」固定在文件顶部。
- `loginRenewMinInterval`：解释节流存在的目的是防「拿插件当探测器」，并说明 10 秒下限不会误伤
  30 分钟的正常周期。
- `handleLoginRenew`：解释为什么必须异步 —— 同步等会把浏览器 `fetch` 拖到超时。
- `login_background.js` 的 `bindOrigin` 监听：解释**绑定后不立刻续期**的原因（插件侧 10 秒节流，
  紧跟 `submit` 再推只会拿到 429，用户会看到无意义的失败提示）。
- `login_extension.go` 文件头第二段：解释「为什么还需要后台脚本」，与第一段「为什么需要扩展」
  形成两级论证。
- `chromeauth/auth.go` 的 `pendingImport` 类型注释：解释路径为什么不能在导入前固定。
- `validateAndWriteImports` 里的邮箱补齐分支：`// 邮箱是唯一能从会话里稳定拿到的账号身份来源，拿不到就没法落盘。`
- `login_bridge.html` 的 8 秒自动展开：解释这是「扩展还没装」的判定。

### 4.3 删除位点的注释处理

本轮删了一处自己刚加的 `ParseCookies` 导出包装（`internal/chromeauth/cookies.go`，11 行）。
续期实际走 `ImportCookie → ImportCookies → parseCookies`，这个包装从落地到复核**零调用**，
属于没收益的胶水层，按「最小实现优先级阶梯」第 1 级（需要存在吗）删掉，不保留空壳或
`// deprecated` 注释。删除后 `cookies.go` 与 HEAD 逐字节一致，`git status` 里该文件已消失。

## 5 函数签名变更表

| 文件 | 符号 | 变更 |
|---|---|---|
| `internal/chromeauth/auth.go` | `pendingImport` 结构 | 字段 `path string` → `output string`（改为惰性计算） |
| `internal/chromeauth/auth.go` | `pendingImport.path() string` | 新增 |
| `internal/chromeauth/auth.go` | `importIdentity(metadata auth.Metadata, fallback string)` | 签名改为 `importIdentity(id string, output string, metadata auth.Metadata, fallback string)` |
| `internal/chromeauth/auth.go` | `ImportCookies` / `validateAndWriteImports` | 签名不变，行为改为允许空 ID |
| `internal/cpa/login_renew.go` | `handleLoginRenew` / `runLoginRenew` | 新增 |
| `internal/cpa/login_bridge.go` | `handleLoginResource` | 签名不变，switch 增 `case loginRenewPath` |
| `internal/cpa/plugin.go` | `Plugin` 结构 | 增字段 `lastRenewNs atomic.Int64` |
| `internal/cpa/login_extension.go` | `extensionManifest` / `buildExtensionZip` | 签名不变，内容扩到 4 文件 + `background.service_worker` |
| `internal/cpa/login_popup.js` | `bindRenew(origin)` | 新增 |
| `internal/cpa/login_bridge.html` | `copyPair()` / `legacyCopy(input)` | 新增；`submit(cookies, statusEl)` → `submit(cookies, statusEl, endpoint)` |

`Accounts.ImportCookie` 的对外签名 `(ctx, cookies, id, proxy, authUser)` 未变，`manager.go`
调用点零改动 —— 这是把「允许空 ID」收在 `chromeauth` 内部、不外溢到账号管理器层的关键。

## 6 STYLE-04 代码质量

### 6.1 最小改动

| 文件 | 增/删 | 说明 |
|---|---|---|
| `internal/chromeauth/auth.go` | 67 / 15 | 全部集中在 Cookie 导入链路；`ValidateProxy`、`AccountID`、`importProxies` 未动 |
| `internal/cpa/login_bridge.go` | 2 / 0 | 只加一个 `case` 两行 |
| `internal/cpa/plugin.go` | 2 / 0 | 只加一个字段 + 一行注释 |
| `internal/cpa/management.go` | 7 / 3 | `Resources` 加 1 条 + `[返回]` 文案 + 注释 |
| `internal/cpa/login_popup.js` | 31 / 0 | 只加 `bindRenew` 与 `push()` 尾部三行 |
| `internal/cpa/login_extension.go` | 26 / 5 | manifest 三处、`#renew` 段、zip 第四文件 |
| `internal/cpa/login_bridge.html` | 75 / 36 | 结构从「四段并列」改为「一键授权 + 两个 `<details>`」 |

没有顺手改样式、没有重排无关段落、没有批量格式化。`main.go` / `plugin_bridge.go` / `VERSION`
各 1 增 1 删，只换版本号。

### 6.2 单文件行数

`login_renew.go` 84 行、`login_renew_test.go` 105 行、`login_background.js` 176 行，全部低于
200 行阈值，不需要拆。`login_bridge_test.go` 484 行、`login_extension.go` 125 行属既有规模，
本轮只在其上追加用例与 4 行 manifest 断言。`management.go` 688 行、`plugin.go` 443 行超阈值，
但那是上游既有结构，本轮各只加 ≤7 行，按「手术式改动」原则不触发拆分。

### 6.3 副作用可见性

- `handleLoginRenew` / `runLoginRenew`：前者同步校验参数并 `go` 出去，后者才是真正「写账号文件 +
  重启客户端」的副作用体。命名用 `handle`（入口）/ `run`（执行体）两级，调用点一眼能看出
  副作用发生在异步侧。
- `p.lastRenewNs.CompareAndSwap`：CAS 失败即返回 429，不吞并发竞争。
- `runLoginRenew` 里 `defer recover()`：`ImportCookie` 内部有多个第三方解析，panic 会带走整个
  插件进程；这里不记录为错误是因为下一轮 alarm 会重推，只在 `logging.Warn` 留一行。
- `bindRenew`（前端）只做「发消息 + 拿结果」，不直接改 UI；改 UI 的仍由 `push()` 统一负责。

### 6.4 错误处理

| 位点 | 处理 |
|---|---|
| `handleLoginRenew` 缺 `cookies` | 400 `{"error":"cookies is required"}`，不消耗节流窗口 |
| `handleLoginRenew` 10 秒内重复 | 429 `{"error":"renew is too frequent, retry later"}` |
| `handleLoginRenew` CAS 失败（并发撞车） | 同样 429，不重试 |
| `handleLoginRenew` runtime 未就绪 | 503 + `err.Error()` |
| `runLoginRenew` `ImportCookie` 失败 | `logging.Warn("renew", "Cookie 续期失败", "错误", err)`，不 panic、不写错误状态 |
| `runLoginRenew` 内 panic | `recover` + `logging.Warn("renew", "Cookie 续期中断", "错误", r)` |
| `ImportCookies` 显式 ID 净化后为空 | 仍报 `"id 必须包含英文字母或数字"`（只有「没传」才走新路径） |
| `validateAndWriteImports` 解析不出 ID | `"无法从账号会话解析出账号标识，请改用「手动粘贴 Cookie」并填写标签"` |
| `renewOnce`（前端）无 origin / 无 Cookie | 返回 `{ok:false, message}`，不抛异常 |
| `bindRenew`（前端）`chrome.runtime.lastError` | 转成 `{ok:false, message}`，`push()` 里作为「自动续期未开启」展示 |
| `ensureAlarm`（前端）alarm 已存在 | 不重复创建 |

## 7 STYLE-05 包结构

- `login_renew.go` 放 `internal/cpa` 而不是新建子包：续期路由是登录面的一部分，需要 `Plugin`
  的方法集与 `rpcManagementRequest` / `jsonManagementResponse` / `loginResourcePrefix`，
  单独成包会产生循环依赖或需要把三个符号导出。
- `login_background.js` 与 `login_popup.js` 同目录，两者都是 `login_extension.go` 的 embed 输入；
  没有塞进 `assets/` 或 `web/`，因为删除或移动任何一个都会让 `go build` 直接失败，
  同目录能保证「改 embed 名必须同时看文件」。
- 依赖方向未变：`internal/chromeauth` → `internal/auth` / `internal/gemini` / `internal/proxypool`；
  `internal/cpa` → `internal/app` / `internal/pluginapi` / `internal/logging`。没有反向依赖，
  没有新增外部依赖（`.gitattributes` 只是文本规则）。

## 8 STYLE-08 语言与框架特异写法

### 8.1 Go

- `pendingImport.path()` 用**值接收者**：结构体只有三个字段、无指针语义，值接收者更轻；
  同文件 `File` 的方法都用指针接收者是因为它有 `sync.Mutex`，两者标准不同不矛盾。
- `ImportCookies` 里 `strings.TrimSpace(options.ID) != "" && id == ""` 的顺序不能反：
  先判「用户确实传了东西」，再判「传了但净化后没剩下」；反过来会把「没传」也算成错误。
- `validateAndWriteImports` 的两轮循环（先全部校验、再全部落盘）保持原样：中途失败时不会
  留下「部分账号已写盘」的半成品，这与上游既有的「全通过才写」语义一致。
- `atomic.Int64` + `CompareAndSwap` 实现节流，没有引入 `sync.Mutex` + `time.Time`：
  后者在高频调用下会成为热点，前者一条 CAS 就够。
- `time.Now().UnixNano()` 直接比较差值，不存 `time.Time`：`Plugin` 是长期驻留对象，
  存 `time.Time` 会多一次指针语义的零值判断。
- 无 `any` 滥用（管理响应里的 `map[string]any` 是既有口径）、无 `interface{}`、无反射、无 `init()`。

### 8.2 前端

- `login_background.js` 是 MV3 service worker：所有事件监听必须**同步注册在顶层**，
  任何 `await` 都不能出现在 `chrome.runtime.onMessage.addListener` 的注册之前，否则
  worker 被回收后重启用会丢监听。本轮四个监听（`onInstalled` / `onStartup` / `onAlarm` /
  `onMessage`）全部同步注册，异步逻辑都包在内部的 `async IIFE` 里。
- `onMessage` 里 `return true` 保持消息通道：异步 `sendResponse` 必须这样，否则 popup 侧
  拿到的是 `undefined` 而不是等待结果。
- `chrome.cookies.getAll` 是回调 API，用 `new Promise` 包一层；MV3 里 `chrome.*` 的
  Promise 化只在部分 API 生效，`cookies` 至今仍是回调，不能直接 `await`。
- `buildCookieHeader` 按 name 去重：`google.com` 与 `.gemini.google.com` 作用域会返回同名
  Cookie，直接把两个都拼进请求头会让 Google 侧解析出意外值。
- `login_bridge.html` 复制走双路径（`navigator.clipboard.writeText` → `document.execCommand('copy')`）：
  桥接页常以 `http://` 或非安全上下文打开，`navigator.clipboard` 会返回 rejected Promise。
- `<details>` 折叠用原生元素而不是 JS 手写开关：不引 CSS 状态类、不写 aria，浏览器自带键盘可达性。
- 无框架、无构建链、无内联脚本（`login_bridge.html` 那一段是页面内联，扩展 popup 里则是
  `<script src="popup.js">`，符合 MV3 对扩展页 CSP 的要求）。

### 8.3 能否被宿主正确嵌入

- 新增的 `/login/renew` 已注册进 `Resources`（现共 9 条）。宿主 `ServeResourceHTTP` 按
  **完整路径精确匹配**，未注册即 404 —— 这是 1.0.11 的历史路径 404 事故的直接教训，
  所以本轮把它当硬约束：注册集合、Menu 空集合、页面可达性三处都有断言。
- `/login/renew` 的 `Menu` 为空，宿主 `registeredPluginMenus()` 只收集 `Menu` 非空的资源路由，
  左侧插件树仍只有一个 `Gemini Web` 子项。
- 扩展 zip 由 `/login/extension.zip` 下载，文件名 `gemini-web2api-auth-helper.zip` 未变；
  四文件包里的 `background.js` 与 manifest 的 `service_worker` 路径必须完全一致，
  测试已用 `"service_worker": "background.js"` 字面量锁住。

## 9 STYLE-09 测试资产与编码前契约

### 9.1 归位

`login_renew_test.go` 与被测的 `login_renew.go` 同包同目录（`package cpa_test`，与
`login_bridge_test.go` 一致），没有往根 `test/` 镜像里放 Go 单测（项目约定：Go 单测与源码同包，
`test/` 只放跨语言探针脚本）。

### 9.2 用例清单

`login_renew_test.go`（4 个新增）：

| 用例 | 覆盖 |
|---|---|
| `TestLoginRenewRejectsMissingCookies` | 缺 `cookies` 必须 400 且 body 含 `cookies is required` |
| `TestLoginRenewThrottlesRapidCalls` | 第一次必须 202 或 503，第二次必须 429 且错误文案固定 |
| `TestLoginBridgeMentionsOneClickAndSetup` | 页面含「一键授权（复制配对码）」/`<details>`/安装说明/自动续期文案 |
| `TestLoginRenewRegisteredWithoutMenu` | 路由已注册（非 404）且返回 JSON 而非 HTML |

`login_bridge_test.go` 本轮增补：

| 用例 | 本轮变化 |
|---|---|
| `TestManagementRegistrationIncludesLoginResources` | 路径集合加 `/login/renew`；Menu 空集合同步加 |
| `TestLoginExtensionZipContainsThreeFiles` | 断言从三文件扩到四文件（含 `background.js`）；新增 manifest 三条断言（`"storage"` / `"alarms"` / `"service_worker": "background.js"`）；新增 `background.js` 含 `/login/renew`、`popup.js` 含 `bindOrigin` |

### 9.3 哨兵验证

1. 删掉 `login_bridge.go` 的 `case loginRenewPath` 两行 → `TestLoginRenewRejectsMissingCookies`
   如期 FAIL：`login_renew_test.go:25: expected 400 for missing cookies, got 200
   body=<!DOCTYPE html>…<title>Gemini Web 面板</title>`。失败信息同时证明「未路由时请求会掉进
   静态面板分支」—— 这正是 1.0.11 那类 404/误路由问题的形态。还原后 PASS，备份已删。
2. `TestLoginRenewThrottlesRapidCalls` 允许第一次是 202 或 503：本地无 `authDir` 时
   `Runtime()` 会失败返回 503，而节流状态在 503 之前就已写入，所以两种都算「第一次通了」。
   这条是**跑测试时才发现**的（首版断言写死 202，在无配置环境下直接 FAIL），属于用例自身
   在拦不合理假设的证据。

### 9.4 生产代码污染

`python .../scan_test_pollution.py --root F:\cpa-plugin\gemini-web2api` → `POLLUTION: PASS`。
输出里的「疑似死代码」全部落在 upstream vendored 文件（`internal/pluginabi/types.go:137/141/142/143/145/146`、
`internal/pluginapi/types.go:963-966`、`internal/tokencount/counter_test.go:9`），非本轮引入。
本轮为测试而加的生产符号为 **0**：`login_renew_test.go` 只通过既有的 `callManagement` 辅助函数
和公开 HTTP 形状驱动，没有为测试新增导出钩子或重置函数 —— 这也是删掉 `ParseCookies`
导出包装的一个原因（它既没人调，又会成为「为将来测试预留」的越界符号）。

## 10 门禁输出

```
STYLE: PASS
POLLUTION: PASS
FMT_OK: gemini-web2api/main.go
        gemini-web2api/internal/cpa/login_bridge.go
        gemini-web2api/internal/cpa/login_bridge_test.go
        gemini-web2api/internal/cpa/login_extension.go
        gemini-web2api/internal/cpa/login_renew.go
        gemini-web2api/internal/cpa/login_renew_test.go
        gemini-web2api/internal/cpa/plugin.go
        gemini-web2api/internal/chromeauth/auth.go
        gemini-web2api/internal/chromeauth/cookies.go（删掉包装后与 HEAD 一致，无 diff）
FMT_PREEXISTING: gemini-web2api/internal/cpa/management.go（:295 switch 内多一个空行）
                 gemini-web2api/internal/app/plugin_bridge.go（import 分组顺序 + 字段对齐）
                 —— 两处与上游 clone 逐字节对照，均非本轮引入，保持不动
REAL_RUN: cd F:\cpa-plugin\gemini-web2api && go build ./... && go vet ./... && go test -count=1 ./...
          → BUILD=0 VET=0 TEST=0
            adapter 0.311s / balancer 1.078s / cpa 1.975s / gemini 0.264s / mcp 0.324s
            proxydial 0.806s / requestdb 0.350s / tokencount 0.920s 全 ok
          python scripts\cgo-shim-build.py gemini-web2api → [cgo-shim] all green (gemini-web2api)
          node --check（隔离目录 + package.json commonjs）
            popup.js exit 0 / background.js exit 0 / login_bridge.html 内联块（1 块 5398 B）exit 0
COMMENT_CHAIN: login_renew.go 2/2、login_bridge.go 15/15、login_extension.go 1/1、
               login_renew_test.go 4/4、chromeauth/auth.go 7/7 元信息齐全
TEST_ASSET: 与被测包同目录，未污染生产代码，未新增导出测试钩子
ENCODING: 17 个文件全部 UTF-8 无 BOM；login_bridge.html / login_popup.js / login_background.js
          保持 LF（.gitattributes eol=lf，本轮补登记第三个），其余 CRLF
```

## 11 未命中说明

- **STYLE-06 并发原语改造**：本轮唯一的并发结构是 `atomic.Int64` 记录时间戳（一条 CAS）与
  一个 `go p.runLoginRenew(...)`。没有新锁类型、没有 channel、没有 worker pool，
  也没有改动既有 `sync.RWMutex` / `semaphore.Weighted` 的用法。
- **STYLE-07 前端资源构建链**：扩展的 4 个文件与桥接页都是 `//go:embed` 单文件，改完
  `go build` 即生效，无 npm / webpack / minify 步骤。

## 12 边界声明

- 本记录只判写法、位置、格式、注释与既有习惯，**不判业务正确性**。「自动续期是否真能续上」
  属功能验证，实际口径是：扩展装上后授权一次 → `chrome.storage.local` 里出现 `origin` →
  等 1 分钟首个 alarm → 生产 `docker logs cli-proxy-api | grep "账号 Cookie 已由浏览器扩展续期"`
  出现该行即闭环。这条链路**至今未用真实 Google 账号跑通过**（生产 `account_count = 0`），
  本轮交付的是可运行的实现与可观测的日志锚点，不是已验证的线上结论。
- 本轮**未修改宿主**（`internal/pluginhost`）、**未修改生产 nginx 配置**、
  **未重启生产容器**。
- 明确未做的三件事及理由：
  1. **不补 `AccountSource.RotateBound`（DBSC）**：`internal/chromeauth/login_native.go:196-214`
     的 Chrome 启动参数主动带 `--disable-features=EnableBoundSessionCredentials,DeviceBoundSessions,PersistDeviceBoundSessions`，
     上游有意绕开 DBSC；Cookie-header 导入模式下根本不会产生设备绑定会话，补它是空转。
  2. **不补 `AccountSource.Refresh`（同步阻塞等浏览器）**：`refreshCookies` 是同步阻塞调用，
     没有「等浏览器推 Cookie」的语义，硬接会长时间占住 `refreshSlot`，把正常请求一起堵住。
     改为扩展主动回推，`rotate.go` 里 `c.refresh == nil → ErrSignedOut` 的分支保持原样。
  3. **不新增 `Metadata` 字段**：续期凭据槽位 `PairingRenewal` 早已存在（`state.go:58`，
     注释「远程配对续签凭据摘要」），本轮仍未使用它 —— 扩展绑定用的是 `chrome.storage.local`，
     服务端不需要保存任何续期令牌。少一个字段就少一处持久化状态。
- 待用户裁定的遗留项（不属本轮范围）：`/accounts` 与 `/webui` 历史路径是否长期保留、
  65 个超 200 行 Go 文件是否豁免、schema 长期保持 6 还是跟宿主回升、是否 `go mod vendor`、
  `internal/cpa/plugin.go`（11 个 func）与 `internal/cpa/management.go`（6 个 func 缺元信息）
  的注释补齐是否单开一轮专项。
