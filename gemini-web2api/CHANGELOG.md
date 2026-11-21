# Changelog

本文件记录 gemini-web2api 插件在本仓库（`luode0320/cpa-workbuddy-plugin`）内的自有变更。
上游 `jianli9158/cpa-plugin-gemini-web2api` 与 `Mag1cFall/Gemini-Web2API` 的历史变更见各自仓库。

## 1.0.14 (2026-11-21)

### 修复 - 一键授权的链接不再回落 127.0.0.1

控制台「OAuth 登录」弹窗给出的地址一直是 `http://127.0.0.1:8317/v0/resource/plugins/gemini-web2api/login`，
在用户浏览器里打不开，整条授权链断在第一步。取证结论是**三个环节同时缺信息**，插件自己能补的只有最后一个：

1. 宿主 `internal/pluginhost/management.go` 的 `ServeResourceHTTP` / `ServeManagementHTTP`
   都用 `cloneHeader(r.Header)` 转交请求头，而 Go 标准库把 `Host` 放在 `r.Host` 独立字段、
   不进 `r.Header`，所以插件侧 `Headers.Get("Host")` 恒为空；
2. 生产 nginx 的 `conf.d/proxy.conf` 只设了 `Host` / `X-Real-IP` / `X-Forwarded-For`，
   没有 `X-Forwarded-Host`、没有 `X-Forwarded-Proto`；
3. 宿主 `auth_files_oauth_callback.go` 的 `managementCallbackURL` 硬编码
   `fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, h.cfg.Port, path)`，`StartLogin` 只把这个
   loopback 地址当 `BaseURL` 传给插件，SDK 的 `AuthLoginStartRequest` 里没有任何请求头字段。

处置：真实 origin 改由**浏览器自己上报**。面板页与桥接页一定跑在用户真实域名下，
它们的 `location.origin` 是唯一可靠来源：

- 新增资源路由 `GET /v0/resource/plugins/gemini-web2api/login/origin?origin=<location.origin>`，
  把上报值写进包级缓存（`internal/cpa/public_origin.go`）。
- 面板 `webui.html` 载入时调用 `reportPanelOrigin()`，桥接页载入时调用 `syncOrigin()`；
  桥接页还会就地把配对码从 `cpa|<旧 origin>|<state>` 改写成 `cpa|<真实 origin>|<state>`，
  保证扩展推送时打到的是用户能访问的地址。
- `auth.login.start` 与桥接页渲染统一走新的 `effectiveLoginOrigin`：
  请求头（`X-Forwarded-Host` → `Host`，非回环时）→ 浏览器上报缓存 → 宿主 `BaseURL`。
  请求头前两级都没有主机信息时，`originFromHeaders` 会再试一次 `Referer`——
  控制台 SPA 用同源 fetch 打管理路由时一定带上完整页面地址，从中能还原出
  `https://cpa.luode.vip` 这类真实域名，是这条链上最后一道自动兜底。
- 回环 origin 不允许覆盖已有真实域名，否则容器内自测、健康检查、运维 curl
  一次就会把线上链接改回 `127.0.0.1`。
- 顺带在 `handleManagementHandle` 入口补调 `rememberPublicOrigin(req.Headers)`：
  当前链路拿不到 Host，但只要哪天反代补上 `X-Forwarded-Host`，
  这条兜底通道会自动接住，不需要再改代码。

### 调整 - 面板概览卡按 workbuddy 口径精简

- 标题改为「账号汇总 · 全部账号」，五项指标收敛为
  **可用账号 / 冷却中 / 需重登 / 已停用 / 已发现模型**，
  一条 `可路由占比（可用 / 总数）` 进度条 + `可用 · 冷却 · 需重登 · 停用 · 路由` 明细。
- **移除**概览卡下方整排服务开关与状态位：启动/停止生成服务、重启访客会话、
  停止访客会话、Chrome 可用性 badge。插件随宿主常驻运行，访客会话也会自动重建，
  这些开关在生产上没有实际价值，与 workbuddy 的面板也不一致。
- 删除因此失去调用方的四个前端函数：`toggleService`、`relaunchGuest`、
  `stopGuest`、`loginWithChrome`；同时下线工具栏的「启动本机 Chrome 无痕登录」按钮
  （生产镜像里没有桌面浏览器，`FindLocalChromeBinary()` 恒为空，该路径永远失败）。
  后端 `/service/start|stop`、`/guest/relaunch|stop`、`/accounts/login-browser`
  路由保持不变，需要时仍可通过管理 API 调用。
- 工具栏「导入 Cookie / StorageState」改名为「粘贴 Cookie 导入」，
  副标题与空状态引导文案同步。

### 测试

- 新增 `internal/cpa/public_origin_internal_test.go`（白盒包）：`splitOrigin`、
  `isLoopbackHost`（含 `::1`、`[::1]:8317` 与裸 IPv6）、`firstHeaderValue`、
  回环不覆盖真实域名、`effectiveLoginOrigin` 三级优先级、origin 上报端点写入缓存。
- `login_bridge_test.go` 增补 `/login/origin` 注册断言、桥接页含 origin 上报地址、
  上报后 `auth.login.start` 返回真实域名，以及面板已下线上述服务开关的反向断言。
- `plugin_test.go` 的登录 URL 断言从写死 `http://127.0.0.1:8317` 改为
  「绝对 URL + 命中桥接页路径」——origin 缓存是包级状态，写死主机名会让用例产生顺序依赖。

## 1.0.13 (2026-11-21)

### 调整 - 面板去掉运行日志，概览卡改为常驻

控制台面板此前底部挂了一块「运行日志」，靠 `setInterval` 每 10 秒打一次 `/logs`。
它有三个问题：① 与控制台其它插件的面板（workbuddy / coze / doubao）视觉不一致；
② 与宿主自己的「日志查看」页面职责重叠；③ 空账号时面板只剩一行居中提示 + 日志块，
大片留白，看起来像没加载出来。

处置：删掉 `#logsCard` 整块与它带的所有派生代码 —— `.logs` / `.log-line` / `.log-time` /
`.log-lvl` / `.log-src` / `.log-msg` 六组样式、`loadLogs()`、`fmtLogTime()`、
全部 `loadLogs()` 调用点、以及 10 秒轮询里的那一行。`/logs` 管理路由**保留**，
仍可通过管理 API 取日志，只是面板不再展示。

顺带清掉 `fmtCST()` —— 它在本面板里从来没有调用点，属于更早遗留的死代码。

### 调整 - 概览卡对齐 workbuddy 的用量汇总卡

- 由「有数据才显示」改为**常驻显示**（去掉了 `style="display:none"` 与 `renderSummary` 里的
  `display="block"`），并在初始化时先用零值渲染一次，避免首屏出现「卡片边框在、里面是空的」。
- 标题改为「运行概览 · 全部账户」，meta 改为「N 个账户 · 就绪 X · 冷却 Y · 需重登 Z · 停用 W」，
  与 workbuddy「用量汇总 · 全部账号 + N 个账号 · 可用 X · 不可用 Y」的口径对齐。
- 新增一条**可路由占比进度条**（就绪 / 总数），配色三档：`>=50%` 绿、`>0` 黄、`==0` 红，
  没有账户时画灰条 —— 与卡片内 `healthHTML` 的账户健康度同口径，不再出现「有进度条但没数据」的歧义。
- 空网格文案由 `.loading`（40px 上下留白 + 纯文字）换成新的 `.empty` 卡片式空状态
  （虚线边框 + 加粗引导词），明确写出「当前用访客会话兜底，仅 gemini-auto 可用」。

### 新增 - 控制台插件图标

`Metadata.Logo` 此前完全没填，宿主不报错、只是静默不显示图标，所以一直没人发现。

- 新增 `assets/icons/GeminiWeb.png`（512x512 RGBA 透明底，60696 B），
  源图为 Gemini 官方 favicon（`www.gstatic.com/lamda/images/gemini_favicon_*.png`，256x256），
  LANCZOS 放大到 512 与兄弟插件的 512/1024 口径对齐。
- `internal/cpa/config.go` 的常量块新增 `pluginLogoURL`，指向本仓库 raw 直链；
  `internal/cpa/plugin.go` 的注册体补 `Logo: pluginLogoURL`。
- `registry.json` 的 `gemini-web2api` 条目补 `logo`，键序按兄弟条目排在 `repository` 之后。
- 测试加锁：`TestPluginLifecycleAndCapabilities` 断言注册体里的 `Metadata.Logo`
  必须以 `/assets/icons/GeminiWeb.png` 结尾。这里只断言文件名不写死完整 URL ——
  仓库域名或分支调整时不该连带改测试。哨兵验证：删掉 `Logo:` 那一行后报
  `expected plugin logo ending with /assets/icons/GeminiWeb.png, got ""`。

## 1.0.12 (2026-11-21)

### 修复 - 历史面板路径必须显式注册，否则生产 404

1.0.11 把控制台入口收口到 `/panel` 后，只在 `handleManagementHandle` 的静态分发分支里
保留了 `/accounts` 与 `/webui` 的兼容判断，但**没有把它们登记进 `Resources`**。
宿主 `internal/pluginhost/management.go` 的 `ServeResourceHTTP` 是**按完整路径精确匹配**
`resourceRoutes` 的（`key := managementRouteKey(http.MethodGet, r.URL.Path)`，不做前缀匹配），
未注册的路径直接落到宿主的 404 分支，根本到不了插件的分发函数。

生产实测（1.0.11 install 后）：

```
/v0/resource/plugins/gemini-web2api/panel               -> 200  bytes=49997
/v0/resource/plugins/gemini-web2api/accounts            -> 404  bytes=0     ← 本应 200
/v0/resource/plugins/gemini-web2api/webui               -> 404  bytes=0     ← 本应 200
/v0/resource/plugins/gemini-web2api/login               -> 200  bytes=7394
/v0/resource/plugins/gemini-web2api/login/extension.zip -> 200  bytes=3905
```

处置：把 `legacyPanelResourcePath`（`/accounts`）与 `/webui` 补进 `Resources`，
资源路由从 5 条扩到 7 条，两者都不挂 `Menu`（不进控制台菜单，只保旧链接可用）。

### 修复 - 测试漏检「注册」与「分发」的区别

原 `TestManagementRegistrationIncludesLoginResources` 只断言 `/panel` 与 `/login` 系列已注册，
`TestManagementWebUIPrefixStillServed` 直接调 `management.handle` 绕过宿主路由表，
所以两条历史路径漏注册时测试全绿、生产 404。

现 `TestManagementRegistrationIncludesLoginResources` 的路径集合补上 `/accounts` 与 `/webui`，
并用哨兵验证：删掉 `legacyPanelResourcePath` 那条 `Resources` 后测试立即报
`resource route "/accounts" not registered`。

### 说明

本次仍是**同一次改造的收尾修复**，版本号继续升位到 `1.0.12` 而不是复用 `1.0.11`：
宿主用 `dlopen` 加载插件，glibc 对同一路径会复用已存在的 handle，
同版本覆盖安装（`overwritten=true`）不会重新映射文件，必须换新文件名才能让新镜像生效。

## 1.0.11 (2026-11-21)

### 新增 - 控制台面板收口为单入口并重写为统一视觉风格

- 控制台左侧插件树原本出现两个同名子项（`Gemini Web (Web...` ×2），
  根因是 `/accounts` 与 `/login` 两条 `ResourceRoute` 都挂了 `Menu`。
  现改为**只有一条**挂 Menu 的资源路由，路径统一成 `/panel`
  （与 coze / doubao / workbuddy 等兄弟插件一致），
  其余 `/login`、`/login/extension.zip`、`/login/submit`、`/login/poll`
  照旧注册但 `Menu` 留空，不进控制台菜单。
- 显示名从 `Gemini Web (Web2API)` 改成 **`Gemini Web`**
  （`internal/cpa/config.go` 的 `consoleMenuName`）。
- `/accounts` 与 `/webui` 作为历史面板路径保留兼容，避免已收藏的链接 404。
- `internal/cpa/webui.html` 整体重写为 workbuddy 面板风格：
  三套主题 token（`data-theme` 浅色 / white / dark）、`window.__gwThemeSync`
  镜像父页主题、`.wrap` + `.bar` + `.filter-bar` + `.summary` + 卡片网格、
  右上角 `toasts` 栈、`modal-mask` 弹窗；账户卡从表格行改成卡片，
  带可路由百分比进度条、状态徽章、等级徽章、逐条冷却倒计时。
- 管理密钥获取与 workbuddy 对齐：优先读宿主面板 `localStorage` 的
  `cli-proxy-auth`（`enc::v1::` / `enc::v2::` 解混淆），其次 URL 上的 `?key=`，
  再次 `sessionStorage` 缓存；401 连续三次才清 key 并弹出连接框，
  403 会解析退避窗口，避免一次网络抖动就把面板锁死。
- 原本常驻顶部的「management key 输入框 + 保存密钥」已改成隐藏的 `#authBox`：
  只有在三级取值全部拿不到密钥时才自动弹出，嵌入宿主面板时不占用版面。
- README / README_EN 的面板路径同步改成 `/panel`，并补上控制台显示名。
## 1.0.10 (2026-11-21)

### 修复 - 绕开 glibc 同路径 dlopen 复用旧镜像

- 生产实测：`plugin-store install` 覆盖 `plugins/linux/amd64/gemini-web2api-v1.0.9.so` 后，
  再 install（`overwritten=true`）完全不产生新的 `plugin loaded` 日志，
  `/proc/<pid>/maps` 里该 .so 的 inode 仍是首次加载时的旧 inode 且标记 `(deleted)`。
  宿主的 `loader_unix.go` 用的是 `dlopen(path, RTLD_NOW|RTLD_LOCAL)`，
  glibc 对同一路径会直接复用已有 handle（引用计数 +1），不会重新映射文件，
  因此**同版本覆盖安装永远换不掉内存里的旧镜像**。
- 处置：版本号升到 1.0.10，让 .so 文件名变成 `gemini-web2api-v1.0.10.so`，
  走一次全新路径的 dlopen。这是唯一不重启容器就能换代码的路径。
- 顺带把两处硬编码版本断言的测试改成回读 `VERSION`（新增
  `internal/cpa/version_helper_test.go` 的 `expectedPluginVersion`），
  使版本号只保留 VERSION + CI `-X main.version` 一个真相源。

### 排查结论 - 生产日志 `plugin schema version 10 is not supported` 不属于本插件

- 用生产容器内的 CPython ctypes 直接 `dlopen` 本插件的 .so 调 `plugin.register`，
  四组不同输入（含 `{"schema_version":10}`）都稳定返回 `schema_version: 6`，
  与本仓库 `internal/pluginabi/types.go` 的 `SchemaVersion uint32 = 6` 一致。
- `/proc/1/maps` 显示生产进程同时映射着两个 gemini 系插件：
  `gemini-web2api-v1.0.9.so`（inode 3940，已删）与 `gemini-cli-v1.0.5.so`（inode 4378，已删）。
  后者是磁盘上已不存在、但内存仍映射的孤儿，上报 10 的是它
  （`cpa-plugin-gemini-cli` 的源码 `SchemaVersion = 10`）。
  两者 warn 行在 `docker logs` 里交错成相邻行，极易误判归属。
- 本插件当时的真实症状是 `registered=false` / `effective_enabled=false`，
  即 `plugin.register` 从未成功过一次，随后每次 `plugin.reconfigure` 因未注册而失败。

## 1.0.9 (2026-11-21)

### 新增 - 收编为第 14 个自有插件

- 上游源码整体 vendor 进 `gemini-web2api/`，module 路径改为
  `github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api`，`go` 指令从 1.27 降到 1.26.0
  （实测 go1.26.5 可编，无 go1.27 专有语法）。
- 布局扁平化：`cmd/gemini-web2api-plugin/main.go` 上移到模块根 `main.go`，与仓库其余插件
  目录惯例一致；删除上游自带的 `.github/`（ci.yml / release.yml 会与自有 `build.yml` 冲突）、
  `.gitignore`（根已覆盖）。
- 版本号改为单一来源：`internal/app/plugin_bridge.go` 提供 `SetVersion` / `Version`，
  `main.go` 的 `var version` 由 CI 的 `-X main.version` 注入，本地构建回落 `VERSION` 文件同值；
  `Makefile` 重写为 `-ldflags="-s -w -X main.version=$(VERSION)" -buildmode=c-shared -o $(OUT_FILE) .`
  （原 `./cmd/gemini-web2api-plugin` 路径已随扁平化失效）。
- 注册元信息指向本仓库：`internal/cpa/plugin.go` 的 `Author` 改为
  `luode0320 (Upstream: jianli9158 / Mag1cFall)`，`GitHubRepository` 改为
  `https://github.com/luode0320/cpa-workbuddy-plugin`。
- CI 与发布面登记：`.github/workflows/build.yml` 八处（tag 映射注释、`on.push.tags`、
  dispatch 的 `plugin` 枚举、test matrix、`SLUG` 映射、tag 前缀条件、tag 解析 shell 分支、
  显示名 case）；`registry.json` 追加第 14 条（6 平台 artifacts，sha256/size 待发布脚本回填）。

### 修复 - schema 版本对齐生产宿主

- 生产 `cli-proxy-api` 容器跑的是 `Commit bd0ed82`（BuiltAt 2026-11-11），其
  `sdk/pluginabi` 为 `ABIVersion=1` / `SchemaVersion=6`；上游 vendored 副本上报 `SchemaVersion=10`，
  宿主 `registerRPCPlugin` 会直接拒绝：`plugin schema version 10 is not supported`。
- 处理：`internal/pluginabi/types.go` 的 `SchemaVersion` 由 10 降为 6。已逐项核对
  上游能力面与宿主 schema-6 契约 —— 上游 `rpcCapabilities` 12 字段是宿主 27 字段的子集；
  上游处理的 27 个 method 全在宿主支持的 61 个之内；上游用到的 `host.*` 里
  `host.auth.models` / `host.affinity.lookup` 零调用点，`host.routing.reset_cooldown`
  唯一调用点显式丢弃错误；上游零处读取宿主传来的 `schema_version`、零处按具名 schema
  常量分支、零处实际使用 schema 7–10 独有能力（`RequestBodyPatcher` / `MultiAuthProviders` /
  `ProviderModels` 等全部为定义未使用）。降级后 `go build` / `go vet` / `go test ./...` 全绿。

### 新增 - 一键授权登录（桥接页 + 浏览器扩展回传）

生产容器是 Debian 12 裸镜像，`/usr/bin` 与 `/usr/local/bin` 里 `chrom|chrome|firefox`
零命中，`chromeauth.FindLocalChromeBinary()` 必然返回空 —— 上游「拉起本机 Chrome 无痕窗口」
这条唯一登录路径在生产上完全不可用，只剩手工粘贴 Cookie。因此新增一条不依赖服务端浏览器的授权通道：

- `internal/cpa/login_bridge.go`（新增）：`handleLoginResource` 接管四个资源路由
  ——`/v0/resource/plugins/gemini-web2api/login`（桥接页）、`/login/extension.zip`（扩展下载）、
  `/login/submit`（收 Cookie）、`/login/poll`（轮询进度）；`ensureLoginState` 优先沿用宿主
  `auth.login.start` 已发的 state（缺失才新建），`runLoginImport` 走 `Accounts.ImportCookie`
  做一次真实 Gemini 校验后 `finishLogin`，并回填 `CallbackID` 以 `SyncAccountIDToHostAuth`
  同步 auth 文件。
- `internal/cpa/login_bridge.html`（新增）：三步引导页（复制配对码 / 下载并加载扩展 / 等待结果），
  另带手动粘贴 Cookie 输入框。占位符由 `strings.NewReplacer` 注入且全部经 `htmlEscape`
  （Host 是可伪造输入，已加用例断言注入 `<script>` 被转义）。
- `internal/cpa/login_extension.go` + `login_popup.js`（新增）：MV3 授权助手扩展，`archive/zip`
  在内存里打包 `manifest.json` / `popup.html` / `popup.js`，用 `chrome.cookies` 读取
  `SAPISID` / `__Secure-1PSID` 等 HttpOnly 会话 Cookie（页面 JS 的 `document.cookie` 读不到），
  按配对码 `cpa|origin|state` 回传。
- `internal/cpa/management.go`：登录面分发**必须前置于静态面板**，否则
  `/v0/resource/plugins/gemini-web2api` 前缀会把登录页一起吞成面板 HTML；
  `handleManagementRegister` 的 Resources 由 1 条扩为 5 条（`/login` 挂 Menu，
  `/login/submit`、`/login/poll`、`/login/extension.zip` 不挂 Menu —— 不进控制台菜单，
  但必须注册，否则宿主 `ServeResourceHTTP` 查不到路由会直接 404）。
- `internal/cpa/auth.go`：`handleAuthLoginStart` 的返回 `URL` 由账号面板改指桥接页，
  Metadata 增加 `pair_code` / `extension_url`；`loginSessionState` 记录 `CallbackID`。
- `internal/cpa/webui.html`：登录区改为三列，新增方式一「一键授权（浏览器扩展回传）」
  并标 `recommended`；方式二「本机 Chrome」补注「服务器部署时没有桌面浏览器，请改用方式一或方式三」。

### 设计取舍 - 为什么提交端点是 GET

宿主 `internal/pluginhost/management.go` 的 `ServeResourceHTTP` 开头即
`if !strings.EqualFold(r.Method, http.MethodGet) { return false }`，且资源路由不向插件传递
请求体；而资源路由是唯一免管理密钥、可浏览器直达的通道。因此 Cookie 走 query 传递，
扩展与桥接页都用 `GET`。导入含一次真实 Gemini 校验（最长 30s），故 `/login/submit`
立刻返回 `202 importing`，由 `/login/poll` 取最终结果，避免浏览器请求被代理层超时切断。

### 已知

- 上游 OAuth 续签链路是死代码：`auth.SetOAuthAndBound` / `auth.SetBoundSession` /
  `AccountSource.Refresh` 均零调用点，`gemini/rotate.go` 的「会话失效 → refreshCookies」分支
  永远走不到，会话失效即报 `ErrSignedOut`。本轮未接活，一键授权仍是「一次性导入 Cookie」，
  长期保活依赖上游 `RotateCookies` 定期轮换。
- 上游 155 个 Go 文件里 65 个超过 200 行（`internal/pluginapi/types.go` 1768 行、
  `internal/balancer/balancer.go` 1237 行等），属原样 vendor 的历史形态，本轮未拆分。
- `auth.go` / `management.go` / `internal/app/plugin_bridge.go` 三处 `gofmt -l` 仍报差异
  （import 顺序、多余空行），上游 clone 同样如此，非本轮引入；保持原样以避免无关 diff。
