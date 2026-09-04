# TraeWork Plugin Changelog

## 0.1.44

### Fix — 浏览器授权登录 GetUserInfo 401 时回落回调 URL 的 userInfo（SOLO 客户端同款策略）

0.1.43 生产实测：exchange 已成功换到 token（不再报 HTML 解析错误），但下一步 GetUserInfo 报 `HTTP 401 {"Code":"20310","Message":"The user is not logged in"}`——该路由基于 cookie 会话鉴权，新换的 bearer token 不被认。SOLO 客户端 main.js 取证：`o = r ?? await this.getUserInfo(...)`——**客户端优先用回调 URL 里的 `userInfo` JSON 参数（含 UserID/ScreenName 完整档案），根本不依赖 GetUserInfo**。而 TRAE 授权页的回调 URL 恰好携带 `userInfo={"UserID":"...","ScreenName":"..."}`。

- **`browserlogin.go`**：
  - 新增 `parseBounceUserInfo`：从回调 URL `userInfo` JSON 参数提取 UserID/ScreenName（大小写变体容错）
  - `finishBrowserLogin` 签名加 `bounceUserInfo`：GetUserInfo 失败时回落到回调 userInfo 提供身份（仍先尝试 GetUserInfo 保序）；两者都无才报错
  - callback/submit 双通道把 `q.Get("userInfo")` 传入 settle 链
- 测试：新增 `TestBrowserLoginSubmitUserInfoFallback`（exchange 成功 + GetUserInfo 401 + 回调带 userInfo → 流程必须越过获取用户信息阶段进入导入；GetUserInfo 必须先被尝试一次）
- 涉及文件：`browserlogin.go` / `browserlogin_test.go` / `VERSION` / `main.go` / `CHANGELOG.md`

## 0.1.43

### Fix — 浏览器授权登录 ExchangeToken 打错域（www.trae.cn 返回 SPA HTML）

0.1.42 生产实测：AuthCode 解析链已通（不再报缺 state），但 exchange 报「响应解析失败: invalid character '<'」——`www.trae.cn` 是官网/SPA 域，对 `/trae/api/v3/oauth/ExchangeToken` 返回 HTML 首页（实测 200 text/html 37KB），根本不是 API 域。用户回调 URL 里的 `host=https://api.trae.com.cn` 参数即 API host 提示；实测 `api.trae.cn` 与 `api.trae.com.cn` 双域都返回真 JSON API（400/401 凭据错误响应）。选 `api.trae.cn`：与生产凭据 `"host":"https://api.trae.cn"`、签到默认域 `defaultAPIHost` 同域。

- **`browserlogin.go`**：
  - 新增常量 `browserLoginAuthHost = "https://api.trae.cn"`（API 域）；`browserLoginHost`（www.trae.cn）只用于授权页 URL `auth_url`
  - `browserLoginExchange` / `browserLoginUserInfo` 两处请求切到 `browserLoginAuthHost`
  - `browserLoginUserInfo` 响应解析增强：补 `ResponseMetadata.Error` 上游错误分支 + `result`（camelCase）envelope 回落，与 exchange 解析同级容错
- 涉及文件：`browserlogin.go` / `VERSION` / `main.go` / `CHANGELOG.md`

## 0.1.42

### Fix — 浏览器授权登录适配 TRAE 授权页真实回调形状（authCodeInfo，非标准 OAuth）

生产实测（2026-09-05 00:06，用户真实浏览器登录成功后粘贴回调地址）实锤：TRAE 授权页登录成功后的跳转**不是标准 OAuth 302**——不回传 `code`/`state` query 参数，而是前端 JS 把授权码放进 `authCodeInfo` JSON 参数拼向 redirect_uri。0.1.40 的 submit 只按 `?code=&state=` 形状解析，真实回调必报「回调地址缺少 state 参数」。真实回调形状：

```
http://127.0.0.1:<port>/authorize?isRedirect=true&scope=solo
  &authCodeInfo={"AuthCode":"...","ExpireAt":<ms>,"ExpireDuration":600000}
  &loginTraceID=<uuid>&host=https://api.trae.com.cn&userRegion=cn&userInfo={...UserID...}
```

- **`browserlogin.go`**：
  - 新增 `extractAuthCode`：标准 `?code=` 优先，回落解 `authCodeInfo`（含 `AuthCodeInfo` 大小写变体）JSON 的 `AuthCode` 字段；`ExpireAt`/`ExpireDuration` 不做客户端预检（exchange 失败自然报错）
  - 新增 `newestPendingSession`：TRAE 页面永不回传 state，submit/callback 的会话定位链 = **body.state（面板记住）→ URL state → 最新未处理 pending 会话兜底**（单操作者面板，最新 start 即用户刚登录的会话；已处理/过期会话跳过）
  - `/browser-login/start` 响应新增 `state` 字段（面板提交时带回，主定位键）
  - callback（resource 自动回跳通道）与 submit 双通道同构适配；错误消息更新（缺 code 提示语、无法定位会话提示语）
- **`panel.html`**：`browserLogin()` 记住 start 返回的 `state`（模块级 `browserLoginState`），提交 body 带 `{url, state}`；粘贴卡片注释更新
- 测试：新增 `TestBrowserLoginSubmitTraeAuthCodeInfoShape`（真实回调形状 + body.state/最新兜底/双 pending 取新/无会话报错 4 断言）、`TestBrowserLoginStartReturnsState`、`TestBrowserLoginCallbackTraeShapeFallback`；改写 submit 编排测试缺参用例（无 state 现走兜底，断言缺授权码报错 + 会话存活）
- 附带：并行会话 0.1.41（`767029e`）只含前端带 state 改动（panel.html 被并行 commit 带走），后端解析在本版闭环——0.1.41 若先发布，粘贴提交仍报「缺少 state」，本版为完整修复
- 涉及文件：`browserlogin.go` / `browserlogin_test.go`（`panel.html` 的 browserLogin 段已随 0.1.41 入库）

## 0.1.41

### Fix — 删除确认按钮 busy 状态泄漏导致无法连续删除账号

与 workbuddy-provider 0.14.20 同构修复（逐函数适配）：删除账号成功后确认弹窗按钮停留在「处理中…」禁用态且从未复位，弹窗 DOM 静态复用导致下次打开不可点击。

- 涉及文件：`panel.html`（`confirmDeleteAuth()` 成功分支补 `busy(btn,false)`；`openDeleteModal()` 打开时防御性复位按钮）。

## 0.1.40

### Fix — 浏览器授权登录回调形状适配 TRAE 授权页白名单（面板引导式粘贴闭环）

浏览器实测（2026-09-04，headless + 用户真实浏览器双通道一致）证实 TRAE 授权页对 `auth_callback_url` 的白名单判据是**回环 host AND 路径恰好 `/authorize`**（五组对照：公网域名 https/http 与路径形状均被拒，回环 + 任意端口 + `/authorize` 放行）。0.1.38/0.1.39 把 callback 挂在 `/v0/resource/plugins/<id>/browser-login/callback` 的形状无论本机还是远程宿主都过不了白名单，宿主主端口又没有 `/authorize` 路由（插件无法注册宿主级路径）——唯一零依赖出路是「登录后浏览器落在打不开的回环地址（地址栏保留 `?code=&state=`），用户复制地址栏完整网址回面板粘贴提交」，与宿主本体 `POST /v0/management/oauth/callback`（`redirect_url` 整段粘贴）的手动通道同构。

- **`browserlogin.go`**：
  - start 的 `auth_callback_url` 改拼 `http://127.0.0.1:<面板 origin 端口>/authorize`（无显式端口回退 8317；新 helper `browserLoginLocalCallback`）——恰好满足白名单双条件，无需任何监听进程；`redirect_origin` 仍校验裸 origin
  - 新增 `POST /browser-login/submit`（management key 鉴权，`mutatingManagementPath` 覆盖）：body `{url}` 接受整段回调地址或裸 query（`code=...&state=...`），解析 code/state/error 后复用既有 state 会话链路（换 token → GetUserInfo → UserID 去重入库 → 结果写回 state 键读后即焚）；授权服务器 `error` 参数短路不触 exchange；重复提交同一 URL 拒绝（code 单次有效，结果只保留一次）
  - callback 与 submit 共享 `settleBrowserLogin`（消费会话 → exchange → 存回 outcome）；`error`/`error_description` query 参数支持；`state` 日志前缀加长度 guard（粘贴路径 state 长度不可信）
  - resource callback 自动回跳路由保留（本地转发器场景仍可自动闭环）
  - 新增测试 seam：`browserLoginExchangeFn`/`browserLoginUserInfoFn`（宿主 HTTP 桥上游注入）
- **`panel.html`**：打开授权页后动态显示粘贴引导卡片（三步说明 + 输入框 + 提交按钮，主题变量自适应）；提交走 `/browser-login/submit` 成功后刷新账号列表；按钮 tooltip 与提示文案同步改为粘贴流程；`?auth_cb=` 自动回跳轮询通道保留
- 测试更新 3 项：start 断言改为回环 + `/authorize` 形状（含无端口 origin 回退 8317）；注册表新增 submit 路由 + mutating 覆盖断言；submit 编排链 6 断言（缺 body/缺 state/未知会话/error 短路不触 exchange/exchange 失败存回/重复提交拒绝）。哨兵验证：临时破坏 callback 形状 → 测试 FAIL → 还原全绿，证明断言真实编译执行
- 验证：cgo-shim build/vet/test 全绿（1.288s）；panel.html 双 script 块 node --check OK；gofmt 干净；git diff --check 干净

## 0.1.39

### Fix — 浏览器授权登录三个生产级缺陷（0.1.38 端到端验证发现）

- **callback 改挂免鉴权 resource 前缀**：0.1.38 把 `GET /browser-login/callback` 注册在 management 前缀下，宿主（CLIProxyAPI v7.2.129 `pluginManagementNoRoute`）对 management 全路由（含 GET）强制 management key 中间件，TRAE 登录页跳回的普通浏览器 GET 无法携带 key，回调在生产必 401。修复：callback 移入 `managementRegistration().Resources`（`/v0/resource/plugins/traework-provider/browser-login/callback`，宿主对该前缀不走鉴权中间件；无 Menu 标签不进管理 UI 菜单），`handleManagement` 的 resPrefix 分支加子路径分发，management 路由表删除该条目
- **授权 URL 补 OAuth `state` 参数**：start 生成的授权 URL 此前未携带 `state`，授权服务器无法按 RFC 6749 原样回传，callback 的 state 查会话必然落空。修复：`q.Set("state", state)`，回传值与 minted 会话键一致
- **面板 `auth_url` 还原 HTML 实体**：宿主 `ServeManagementHTTP` 对 management JSON 响应体强制 `htmlsanitize`（递归 `html.EscapeString`），`&` 全部变成 `&amp;`，浏览器按字面打开会把 `amp;xxx` 当独立参数，授权页参数解析必挂。修复：面板 `browserLogin()` 打开前 `replaceAll("&amp;","&")`（resource 路由响应不转义，callback 回跳页不受影响）
- 测试新增 2 项契约：start 指向 resource 回调 + 携带 state + S256；注册表 callback 仅存在于 Resources 且无 Menu
- 验证：cgo-shim build/vet/test 全绿

## 0.1.38

### Feat — 面板「浏览器授权登录」：免 IDE 完整 OAuth 导入账号

traework 此前仅支持从 storage.json 粘贴/选文件导入凭据（0.1.38 之前 login.go 判定"Trae 无浏览器 OAuth 端点"）。2026-09-04 逆向 TRAE SOLO CN 0.1.62 客户端确认其登录走标准浏览器授权码 + PKCE(S256) 流程，本版在插件侧完整复刻该流程：面板一键发起，浏览器登录 TRAE 后 code 自动回到插件服务器换 token 入库，全程无需安装 IDE。

- `browserlogin.go`（新增）：
  - `POST /browser-login/start`：服务器生成 RFC 7636 PKCE 对（64B verifier → S256 challenge）、一次性 EC P-256 设备密钥（SPKI PEM，对应客户端 DevicePublicKey）、随机设备指纹（machine_id 64hex / device_id 16 位数字），返回 `www.trae.cn/authorization` 授权 URL（参数形状照抄 SOLO 客户端：auth_from=solo、client_id=en1oxy7wnw8j9n、hide_saas_login=true 等）；body `{redirect_origin}` 仅接受裸 origin（开放跳转防护）
  - `GET /browser-login/callback`：TRAE 登录页导航回调（免 Bearer——一次性 state 即凭证），服务端 `POST /trae/api/v3/oauth/ExchangeToken` 换 token（body `{ClientID, AuthCode, CodeVerifier, DeviceInfo, IDEVersion}`，响应 Result 双大小写容错）→ `POST /cloudide/api/v3/trae/GetUserInfo` 取 userId/昵称 → UserID 去重后入库（authFileNameFor 命名，Host 留空走插件默认链路）；凭据只落 auth 文件，回跳 URL 仅带 `?auth_cb=<state>`
  - `POST /browser-login/result`：面板回跳后读一次性结果快照（读后即焚、不含凭据）；过早轮询返回 pending 且不消费会话；会话 TTL 10 分钟
- `management.go`：注册 3 条路由；mutatingManagementPath 补 start/result（面板 Bearer 鉴权）
- `panel.html`：工具栏新增「🔑 浏览器授权登录」按钮（弹窗拦截提示）；`?auth_cb=` 回跳检测（清参数、等 management key 就绪后取结果、成功后刷新账号列表）
- `browserlogin_test.go`（新增）：PKCE RFC 7636 形状、origin 校验（8 非法样本）、设备指纹形状、结果会话读后即焚契约、回跳页三重跳转机制
- 验证：cgo-shim build/vet/test 全绿；panel 内联 JS node 逐块语法通过
- 已知边界：真机端到端待生产验证（解析层已做 PascalCase/camelCase 双容错）；客户端版本常量（2.3.79943 / 0.1.62）上游收紧版本校验时需同步更新

## 0.1.37

### Feat — 解析上游 event:token_usage 真实用量，dashboard Token 列不再依赖估算

traework 此前从不解析上游 token 用量，usage feed 全靠 content 字符估算（InputTokens/ReasoningTokens 恒 0，OutputTokens=chars/4），导致 dashboard 的 trae 模型行输入/输出/思考/总 Token 无真实数据（2026-09-03 截图 qwen3.8-max 行）。2026-09-04 直连取证确认 llm_utils_chat 在 done 前发送 `event:token_usage`，data 为 OpenAI 风格 usage JSON 且 reasoning_tokens 位于顶层。本版三条路径（collect / aggregate / pump）统一接入真实用量，缺失时回退估算，行为与 0.1.36 一致。

- `stream.go`：
  - 新增 `traeUsageCollector`（feed/detail）与 `usageDetailFromTraeMap`：解析 prompt/completion/reasoning/total/cache 计数，数值类型容错（float64/int64/json.Number）；reasoning 顶层缺失时回退 `completion_tokens_details` 子对象（CodeBuddy 形态）；data 带 usage 包装键时自动解包
  - `collectTraeStream` / `aggregateTraeCompletion` 增加 detail 返回值（5 值 / 3 值）；`traeStreamAttemptResult` 新增 Usage 字段；collect/aggregate/pump 三处 scanSSE 回调新增 `case "token_usage"` 分支
  - 新增兜底 helper `usageDetailForAttempt`（流式）/ `usageDetailForCompletion`（非流式）：真实 total>0 原样发布，缺失回退 `estimateUsageFromChunks` / `estimateUsageFromCompletion`
- `executor.go`：`handleExecExecute`（非流式）、`runTraeSyncStream`（同步流，pseudo/成功分支）、`runTraeAsyncStream`（异步协调 5 处）全部改发 usageDetailFor*；失败路径维持空 Detail 语义不变
- 测试：9 处 collect/aggregate 调用点适配 5/3 返回值；新增 `token_usage_test.go` 8 用例（取证 JSON 顶层/嵌套解析、别名键、usage 包装解包、collect/aggregate 带出、无事件空 Detail、两 helper 兜底语义）
- 验证：cgo-shim build/vet/test 全绿；gofmt 干净（本轮改动文件）

## 0.1.36

### Fix — 纠正 0.1.35 删除方向：恢复「用量汇总」，改删「子系统状态」

0.1.35 误删了面板顶部「用量汇总 · 全部账号」5 卡片与消耗进度条（截图红框指向理解偏差）。本版纠正：**恢复用量汇总区块，删除下方「子系统状态」区块**（保号池 watchdog / keepalive / lifecycle / 异常池 4 卡 + 上次保号刷新行）。账号卡内各自的积分进度条保留。

- `panel.html`：恢复 `renderSummary()` 完整用量汇总渲染（含 `accountsForFilter()` / `creditOf()` 辅助函数、`.summary .pb` CSS）；删除 `renderSubsystem()` 函数与其调用、`.summary-item .d` 死 CSS；同步注释
- 基线核对：与 0.1.34 diff 仅剩「子系统状态」相关删除，用量汇总逻辑与 0.1.34 完全一致
- 验证：node --check 双 script 块 PASS、cgo-shim build/vet/test 全绿

## 0.1.35

> ⚠️ 本版删除方向错误（误删「用量汇总」，应删「子系统状态」），已被 0.1.36 纠正，请勿基于本版面板行为判断。

### Fix — 面板移除「用量汇总」区块（方向错误，已回退）

按用户要求删除面板顶部「用量汇总 · 全部账号」5 卡片与消耗进度条（剩余可用/剩余不可用/已用/额度池/消耗占比）。账号卡内各自的积分进度条保留；「子系统状态」（保号池/keepalive/生命周期/异常池）保留。

- `panel.html`：`renderSummary()` 只渲染子系统状态；删除 `accountsForFilter()` / `creditOf()` 专属辅助函数；删除死 CSS `.summary .pb` 与过时注释
- 验证：node --check 双 script 块 PASS

## 0.1.34

### Feat — 面板功能对齐 workbuddy（B 组 9 项）+ 修复 3 处前后端契约断裂

traework 面板是 0.1.16 从 workbuddy 一次性裁剪的快照，0.14.10~0.14.18 期间 workbuddy 面板的多轮迭代未同步，且存在 3 处复制前端时未同步后端 handler 形态的契约 bug（差异全量清单见知识库《traework面板与workbuddy面板差异清单》）。

**契约修复（前端功能的生效前置）**：

1. `/refresh/status` 去掉 `{"refresh": ...}` 包装层，平铺返回 `globalRefresh.Snapshot()`（management.go）——前端按平铺读 `running/total/done/failed/per_account`，包装导致整个异步刷新轮询链第一次 tick 即静默停止：进度文本、卡片三态、逐卡局部刷新从未运行过。
2. `pollRefreshStatus` 不再丢弃 `/credits` 返回值：新增 `fetchAndPatchCredits`（合并 credits/exhausted 进本地态）+ `updateOneCard`（排序激活时全量重排，否则原地替换卡片）——后台刷新 done 后卡片积分立即更新，不再等整页 load()。
3. `window.__traeThemeSync` 补上启动调用——此前只定义未调用，嵌入 CPA 主面板切换暗色主题时面板不跟随（首屏 sync() 在 IIFE 内已跑，故仅后续变化失效）。

**面板功能对齐（panel.html 重写核心区块）**：

1. 账号卡积分改进度条（`.pb` 系列，与 workbuddy 同构）：可用/已用/占比/额度池/包数/快照时间，>60% 黄 >85% 红；成功/失败/连败/冷却段并入进度条首行。
2. 汇总卡改为真"用量汇总"：剩余(可用)/剩余(不可用)/已用/额度池/消耗占比 5 卡 + 消耗进度条，随筛选 chip 联动（`accountsForFilter`）；保号池/keepalive/lifecycle/异常池子系统卡下移为"子系统状态"行（仅全部视图），消除"用量汇总"名下渲染子系统状态的错位。
3. 签到状态化：后端新增 `checkinDoneToday` 内存记录（auth_index→当日日期，签到成功即写入），`/accounts` 返回 per-account `checkin_today`；前端"已签到"禁用态 + `markCheckinDone` 即时置灰（覆盖 busy 恢复快照防闪回）+ 乐观写本地态。
4. 签到结果细分：`checkinResult` 增加 `Already`；批量 `/checkin` 返回 `summary{success,already,fail,eligible}`（契约同 workbuddy）；results 补 `already/auth_index/nickname` 字段；`checkinResultToast` 区分新签（+积分）/今日已签/失败。
5. 排序三态切换：工具栏"积分 ↕/↑/↓"，激活时按 `credits.total_remain` 排序（未知 = -1 沉到"最少"端），积分变化触发全量重排；关闭时保留原停用>异常>保号>耗尽沉底默认序。
6. 搜索扩为 nickname/label/name/uid 四字段并联动汇总卡；`traeAccountView` 补 `label/name` 字段。
7. 凭据导出：`GET /export`（`{version, exported_at, plugin, count, accounts:[{name, auth_index, uid, nickname, credential}]}`）+ 前端一键下载 `traework-credentials-YYYY-MM-DD.json`；`/export` 列入 `mutatingManagementPath`（含凭据原文，GET 也强制 management key——比 workbuddy 现状更严格）。
8. 冷却倒计时：`cool_until` 从 RFC3339 字符串改为 Unix 秒（对齐 workbuddy 字段名与语义），前端 1s ticker 原地刷新"冷却 Ns"。
9. `api()` 健壮性：无 key 前置拦截、`authFailUntil` 请求前熔断、403 IP-banned 解析上游等待窗口（"Try again in Xm Ys"正则）全局退避（静默请求同样退避）、空/非 JSON/解析失败返回结构化 `{error}`（不 throw SyntaxError）；toast 上限 5 条 + err 6s。

**后端配套**：`traeCredits` 扩展 `total_used/total_size/pack_count`（同一 entitlement 响应的 `quota.credits_limit`/`usage.credits_amount` 解析，旧缓存零值向后兼容）；`accountCredits` 四元组查询替代 `accountPoints`+手拼快照（缓存写入点 4 处升级）；`/credits?auth_index=` 返回补 `credits/exhausted/nickname`，新增 `track=1` 入队后台刷新（`EnqueueOne`，refresh_runner 注释预留的契约补齐）。

## 0.1.33

### Feat — usage feed 补齐会话与首字延迟：dashboard「会话」「首字延迟」列支持 traework 流量

token-usage-tracker dashboard 上 traework 请求的「会话」「首字延迟」两列恒为「—」：traework 的 `recordUsageFeed` 把 `session_key` / `ttft_ns` 硬编码为零值（0.1.15 适配 feed 时的遗留），而 workbuddy 侧早已上报真实值。消费端 token-usage-tracker 的 feed 解析与 dashboard 列无需任何改动（字段一直在 schema 里，只是 traework 恒写空值）。

对齐 workbuddy 的 12 参数 `publishUsage` 链路（`traework/usage.go` + `usage_feed.go` + `executor.go` + `stream.go`）：

1. **会话列**：执行器入口（`handleExecExecute` / `handleExecStream`）用与 `scheduler.pick` 同一优先链的 `extractSessionKeyFromSources(req.Headers, req.Metadata)` 提取会话亲和键，经 `traeSyncStreamContext` / `traeAsyncStreamContext` / `traeStreamPumpContext` 泵入全部 22 处 `publishUsage` 调用点，跨换号尝试冻结（req 不随 attempt 变化），写入 feed 的 `session_key` 列。
2. **首字延迟列**：`collectTraeStream` 改四值返回带出首个有效 output 事件到达时间；`pumpTraeStreamAttempt` 结果结构新增 `FirstOutputAt`；新增 `ttftNSBetween`（零值/负差返回 0，与 workbuddy `sseUsageCollector.ttftNS` 语义一致），流式路径按真实观测写 `ttft_ns`；非流式与开启即失败路径写 0（与 workbuddy 行为对齐）。
3. `reasoning_effort` 保持空串（Trae 上游无此旋钮），参数链对齐 workbuddy 保持 feed schema 一致；`source` 列语义不变（traework 传账号 UID 作 label）。

验证：新增 `TestTtftNSBetween`（4 断言）+ `TestCollectTraeStreamReportsFirstOutputAt`（观测点非零/空流零值）+ `TestRecordUsageFeedAppendsNDJSON` 扩展 session_key/ttft_ns 落盘断言；既有 8 处 `collectTraeStream` 测试调用适配四值解构。cgo-shim build/vet/test 全绿。

## 0.1.32

### Fix — 工具调用链路 P1+P0：伪完成豁免工具短流 + Trae 私有协议完整工具链转换

P1 止血：`collectTraeStream` 三值返回带出 `hasToolCalls`，含结构化 tool_calls 的短流永不判伪完成（上游取证：正常工具调用流 ~1.9s / 3 output 事件即 done+tool_calls，被 600 字符健康门槛误判换号/裁剪）；pump gate 收到 tool-call 信号即放行。P0 根治：上行 `tools`/`tool_choice` 透传（parameters object→JSON string、tool_choice 规范化）、历史 assistant tool_calls 键 function→function_call 翻译、role=tool 原样回填、下行 function_call→function 键 + arguments 空回退 partial_arguments、collect/aggregate/pump 三路径注入 tool_calls delta、工具流 finish="tool_calls"。新增 toolchain_test.go 8 用例 + pseudo_toolcalls_test.go 4 用例；真实上游双阶段 e2e 闭环。

## 0.1.31

### Fix — 思考型长推理 reasoning 阶段零字节 504：pump gate 纳入 reasoning 双轴健康度流式放行

生产验收 0.1.30（2026-09-02，CYCLE-04）短请求与常规长推理全绿后，深度思考请求（quickselect 算法推导，prompt 要求「深入思考后完整作答」）触发 **504**：`stream_id=2878` scheduled 后 5 分钟零字节、无 done/error/degrade，nginx `proxy_read_timeout 300s` 掐断。根因：`pumpTraeStreamAttempt` 的伪完成保护 gate **只累计 content**（`contentChars += len(text)`），思考型模型 reasoning 阶段的 reasoning-only 分片被无限压入 pending 缓存、永不转发——上游 reasoning 持续流入宿主桥（无 90s 降级，证明桥健康），但插件→nginx→客户端方向 300s 无字节 → 504。0.1.30 的 `isPseudoCompletion` 已把 reasoning 长流豁免为健康（reasoning ≥600 不判伪），gate 却不放行 reasoning，判据与转发放行不一致。

修复（`traework/stream.go`）：

4. **FIX-D · pump gate 双轴健康度**：`pumpTraeStreamAttempt` 门槛累计从 `contentChars += len(text)` 改为 `healthChars += len(text) + len(reasoning)`——reasoning 达到 600 字符（健康思考轨迹）即 gate 打开、按序释放已缓冲分片并转实时转发，reasoning 阶段客户端持续收到字节，不再触发 300s 读超时。伪完成保护不回归：真伪完成是 content+reasoning **双轴都短**（合计 <600），gate 不打开 → 全程 pending → done 后 `isPseudoCompletion` 判伪 → 丢弃 + 换号，零泄漏语义保留。reasoning-only 短流（content==0）仍走既有豁免不判伪。

验证：新增 `TestPumpTraeStreamAttemptReasoningFlushes`（reasoning-only ≥600 按序放行不判伪 / 双轴短真伪完成零下发 / reasoning 达标后 content 实时放行 / reasoning-only 短流豁免释放），哨兵先 FAIL 后删除证明真实编译。cgo-shim build+vet+test 全绿 + gofmt（LF 转换）CLEAN + UTF-8 OK。

## 0.1.30

### Fix — 池耗尽根因三缺陷修复：async 401 核算驱逐 + 伪完成单号池同号退避 + reasoning 纳入伪完成判据

生产取证（2026-09-02，0.1.29 部署后 21:26-21:30，stream_id 2331-2350）确认用户真实失败链 = **插件自身缺陷**而非账号需重导（用户明确「不是号有问题, 就是我们的插件有问题」）：死账号反复 `open error status=401`（async 路径不核算不驱逐绑定 → session 亲和每请求重绑死号）→ 拖累健康号被窗口性节流判伪 → 无同号退避重试 → `pool exhausted`。stream 2351 决定性反证：同一健康账号 30s 前被判伪，同号 30s 后恢复 18696 tokens 完整长推理。

修复（`traework/stream.go` + `traework/executor.go`）：

1. **FIX-B · async 401 核算 + 驱逐绑定**：`runTraeAsyncStream` 的 `open error` 账号级 4xx 分支补 `reconcileAfterExecutorError` + `evictSessionBindingsForAuth`（原来只 publishUsage，死号永不进冷却/异常 → 每请求被 host session 亲和重复选中）。同步路径既有核算，异步路径补平。
2. **FIX-A · 伪完成同号退避重试**：sync/async 协调器收敛一致——伪完成先核算失败 + 驱逐绑定，`PickNextAuth` **仅当池中已无其它候选**（单号池或他号全冷却）时对当前账号同号退避重试一次（`pseudoRetryBudget=1`，不消耗跨账号 Budget；生产实证 2351：同号窗口性节流约 30s 自愈，直接 pool exhausted 让单号池请求必败）。有其它候选仍 A→B 切换，既有契约不回归。
3. **FIX-C · reasoning 纳入伪完成判据**：`isPseudoCompletion` 从只数 content 改为 content+reasoning 双计健康度——任一达 600 字符即健康（思考型模型长 reasoning + 短正文不再误判）；content 0 + reasoning>0（reasoning-only 纯思考流）永不判伪；双零/双短且长输入才判伪。

验证：新增 `test/traework/executor_same_auth_retry_test.go` 两用例（async+sync 单号池伪完成→同号退避→成功，成功后 `resetAccountFailover` 清零断言）；负向哨兵 + 定向 `t.Fatal` 探针双重证明新测试真实编译执行；既有 5 个伪完成回归（A→B 切换 / 池耗尽显式失败 / 同请求不重复选号 / reasoning-only 豁免）全绿。cgo-shim build/vet/test 全绿，gofmt 干净，UTF-8 通过。生产行为验收由发布后真实流量承接。

## 0.1.29

### Fix — 异步流式宿主流桥读取挂起：read 超时保护 + 降级插件直连，覆盖打开阶段未覆盖的卡死窗口

0.1.28 修复了宿主流桥**打开阶段**的永久阻塞，但生产直连复现 `qwen3.8-max` 长推理仍失败：`stream_id=1945` scheduled 后 2 分钟无任何 open/pump/伪完成/耗尽日志，客户端 `499`。根因：`hostHTTPStream.Read()` 里的 `hostCall(MethodHostHTTPStreamRead)` 同样是**同步 cgo 调用（无超时）**，阻塞在宿主侧无缓冲 chunk 通道上等上游数据；长推理期间宿主侧上游数据迟迟不到，read 永久悬挂——0.1.28 只保护了 open，read 阶段仍是裸奔。

修复（`traework/host_bridge.go` + `traework/main.go`）：

1. **宿主流桥读取超时保护**：新增 `hostBridgeReadTimeout = 90s`，把 `MethodHostHTTPStreamRead` RPC 放进独立 goroutine 并用 `select` 竞争超时；超时返回 `errHostBridgeReadTimeout`，不再永久阻塞。90s 宽于健康流正常 chunk 间隔，同时兜住卡死窗口。
2. **read 超时降级插件直连**：`hostHTTPStream` 打开时保存原始请求 `req`/`bodyBytes`；bridged read 超时后在同一 attempt 内重开上游为**直接实时流**（live 模式），后续 Read 全部从直连体读取，请求完成而不是悬挂。
3. **流式直连客户端无整体超时**：新增 `streamHTTPClient`（无 `Timeout`，仅 dial/TLS/响应头超时）替代 120s 的 `sharedHTTPClient`——否则 read 超时降级直连后，长推理在第 120s 被整体超时掐断，等于从「挂起」变「截断」。
4. **可测化**：抽出 `hostStreamReadFn` / `hostStreamDirectFn` 注入点，`hostBridgeReadTimeout` 改为 var 便于测试缩小驱动降级路径。

验证：cgo shim build、vet、test 全绿；新增 `traework/host_bridge_read_timeout_test.go` 三个用例——「桥 read 挂起→超时降级直连读完整 SSE」「桥 read 正常→透传不降级」「缺 req→超时返回明确错误不 panic」；唯一失败哨兵先使 cgo-shim FAIL、删除后全绿；UTF-8、gofmt、`git diff --check` PASS。生产流式长推理验收由发布后直连承接。

## 0.1.28

### Fix — 异步流式宿主流桥打开挂起：超时保护 + 降级插件直连，修复长推理卡死/499

0.1.27 发布部署后，生产直连复现 `qwen3.8-max` 长推理「一直失败」：非流式请求一次成功（13.3s 返回完整正文，走聚合路径），但**带 `StreamID` 的异步流式请求 240s 无任何字节后超时**。生产日志仅一条 `exec stream async scheduled: ... stream_id=1664`，之后无 open/pump/伪完成/耗尽日志、无失败核算；宿主在 4m0s 返回 `499`。根因：异步协调器第一次 `deps.Open` 调 `callLLMStream → hostHTTPDoStream → hostCall(MethodHostHTTPDoStream)`，`hostCall` 是**同步 cgo 调用（`C.wb_call_host`，无超时）**，宿主流桥在长推理场景打开上游后不返回，协调器 goroutine 永久悬挂，客户端收不到任何内容，feed 记「失败（HTTP 200）0/12 tokens」。

修复（`traework/host_bridge.go`）：

1. **宿主流桥打开超时保护**：新增 `hostBridgeOpenTimeout = 30s`，把 `MethodHostHTTPDoStream` RPC 放进独立 goroutine 并用 `select` 竞争超时；超时返回 `errHostBridgeOpenTimeout`，不再永久阻塞。
2. **降级插件直连**：`hostHTTPDoStream` 超时或桥调用失败时回退 `hostHTTPDoStreamDirect`——用插件自有 `http.Client`（120s Timeout）直接请求上游，并把响应体升级为**实时流（live 模式）**边读边发，而非旧版一次性缓冲完整 body；长推理仍能流式输出，只是首包经插件直连而非宿主桥。
3. **可测化**：抽出 `hostBridgeAvailableFn` 与 `hostStreamOpenFn` 注入点，便于单测模拟桥挂起。
4. 超时后迟到的宿主侧流会被丢弃（宿主侧遗留一个未消费流，优于客户端 4 分钟卡死）。

验证：cgo shim build、vet、test 全绿；新增 `test/traework/host_stream_timeout_test.go` 覆盖「桥打开超时→降级直连读完整 SSE」与「直连流边读边发、首包不等待完整 body」；唯一失败哨兵先使 cgo-shim FAIL、删除后全绿；UTF-8、gofmt、`git diff --check` PASS。生产流式长推理验收由发布后直连承接。

## 0.1.27

### Fix — 伪完成同请求换号恢复：A 短答零泄漏丢弃，原 StreamID 内切换健康账号 B 继续生成

0.1.26 能在服务端识别伪完成，但检测发生在正文、`stop` 与 `close` 已下发之后，客户端仍会看到短输出并以成功收尾；账号失败与会话驱逐只影响**下一请求**。生产现象证明需要把恢复提前到**首次 emit 之前**，并在同一逻辑请求内完成。

修复（`traework/stream.go` + `traework/executor.go`）：

1. **单次上游尝试解析器（`pumpTraeStreamAttempt`）**：长输入（输入字符 ≥ 200）在前 600 字节正文健康门槛前全量缓冲 content + reasoning 标准 chunk，不 emit、不 finish、不 close；达到 600 字节立即按序释放并实时透传；门槛前收到显式 `done` 且正文 1..599 字节 → 判定伪完成，丢弃该 attempt 全部 pending，返回结构化结果且**零 finish/close**。
2. **同步路径同请求换号**：`handleExecStream` 同步分支在既有账号重试循环内复用 `pickNextAuth`，A 伪完成 → forced failure 记账 + 驱逐会话绑定 + 更新 `curSA/curAuthID/authUID` 继续尝试 B；池耗尽返回 `upstream account pool exhausted after N attempt(s)`。
3. **异步协调器（`runTraeAsyncStream`）**：保持客户端固定 `StreamID` 与 OpenAI request ID，复用原 `HostCallbackID`；每次上游 attempt 独立 open → pump → 立即 close；伪完成时只关闭 A 上游句柄，保持宿主流打开并在同一逻辑请求内选 B；只有最终健康账号能发送正文、唯一 `stop` + 一次下游 `close`。`triedAuthIDs` 阻止 A→B→A 回跳。
4. **失败边界**：所有账号伪完成 → 显式失败且不补成功终止；output EOF 唯一 `length` 收尾并记失败；读取/发送错误与客户端取消不当作伪完成重试，立即关闭当前上游并结束。

验证：cgo shim build、vet、test 全绿（最新 1.433s）；新增三份根镜像回归（`test/traework/stream_pseudo_test.go`、`executor_pseudo_retry_test.go`、`async_stream_failover_test.go`）覆盖 599/600 字节边界、同步 A→B 与池耗尽、异步同 StreamID 零泄漏恢复；两轮唯一失败哨兵先使同一 cgo-shim 入口失败、删除后全绿，证明测试真实进入编译；`git diff --check`、UTF-8、gofmt、污染门禁 PASS；6-review `STYLE: PASS`。生产 `/v1/responses` 验收由部署后真实验证承接。

## 0.1.26

### Fix — 伪完成检测升级「输出字符 + 输入长度」双重判据，修复 120-token 伪完成漏检

生产 0.1.25 部署后用户反馈 qwen3.8-max 长推理仍被提前结束。全量 feed + 容器日志定位到用户 01:49（UTC 17:49:43）在健康账号 `2257747741770235` 上拿到 **120 tokens / 5.9s** 的伪完成，而同账号前后请求均为 10803~11390 tokens 长输出——账号本身健康，是上游**瞬时限流**返回「HTTP 200 + done + 极少正文」。

根因：0.1.25 的 `isPseudoCompletion` 用**裸字符阈值 120**（注释本意是覆盖 4~129 token，但实现成了字符），120 token 中文输出 ≈ 480 字符远超阈值 → 漏检 → 伪完成被 `resetAccountFailover` 当成功清零 → 账号永不换号 → 同会话继续粘在被瞬时限流的账号上。

修复（`stream.go` + `executor.go`）：

1. **双重判据**：输出字符 < 600（等价 150 token 估算）**且** 输入字符 ≥ 200（长任务）才判伪完成。覆盖当时已知的 15/120 token 检测样本，同时通过输入长度保护正常短答（「你好」≈5 token）不被误判；后续生产反证表明，该版本的检测发生在正文与终止已下发之后，不代表当前请求已经恢复。
2. **输入长度穿入**：`handleExecStream` 用新增 `estimateInputChars` 从归一化消息统计输入字符数，同步 collect 路径直接传入；异步 pump 路径经 `traeStreamPumpContext.InputChars` 传入 `isPseudoCompletion`。
3. 用字符数而非估算 token，避免短输出 `chars/4` 取整归零导致漏判。

验证：cgo shim build、vet、test 全绿；`TestIsPseudoCompletion` 扩充覆盖（59 字符伪完成 / 479 字符 120-token 伪完成 / 短输入短答不误判 / 860 字符健康长输出不误判），新增 `TestEstimateInputChars`（单/多段文本统计）；行为哨兵（临时把 `isPseudoCompletion` 改恒 false）FAIL 证明测试真实执行；`git diff --check` PASS；6-review `STYLE: PASS`。

## 0.1.25

### Fix — 伪完成检测换号 + 会话分配优先面板选中账号，修复 qwen3.8-max「短输出伪完成」不换号

生产 0.1.23 实测（正式路径发真实请求 + 全量 feed 分析）确认两件事：

1. **伪完成是账号级问题且绕过 failover**：账号 `2033439621254311` 的 `qwen3.8-max` 全历史几乎都是「HTTP 200 + done + 极少输出」（1~129 token，平均 77），而被静默限流/标记；健康账号 `2257747741770235` 曾有 215 token / 7.9 分钟真实长输出。伪完成在协议层是合法成功（`termination=done`），`resetAccountFailover` 将其清零，账号永不进入 cooldown/anomaly → **永不换号**。
2. **切换机制对 session 模式形同虚设**：host 把传入插件的候选账号按 auth ID 字典序排序（`traework-203343... < traework-225774...`），而插件 `pickSessionAuth` 新会话无绑定时取 `usable[0]` 且完全不看面板选中账号（active_id）——所以即使把 active_id 切到 225774，新会话仍恒定选 203343。

修复（两处叠加，补齐检测与下一请求换号）：

1. **伪完成检测（方向 2）**：`stream.go` 新增 `isPseudoCompletion`——`done` 收尾但正文（不含 reasoning）字符 < 120（≈30 token）判伪完成；异步 `pumpTraeStream` 与同步 `executor.go` collect 路径命中后**不计成功清零**，改走 `noteForcedAccountFailure`（新增强制记账，跳过 `isAccountFailure` 判定，因为伪完成 status=200 无任何失败标记）计账号失败 + `evictSessionBindingsForAuth` 驱逐会话绑定，让下一次请求切到健康账号。已生成的少量内容仍正常下发给客户端，不打断用户。
2. **会话分配优先面板选中账号（方向 1）**：`session_auth.go` 的 `pickSessionAuth` 新分配分支优先选中 `getActiveAuthID()`（面板 active_id），再退化为「无绑定优先 → round-robin」。面板切号从此真正生效，运营可手动把流量切到健康账号。

验证：cgo shim build、vet、test 全绿；新增 `TestPickSessionAuth_FreshAssignmentPrefersActiveID`（active_id 被优先选中）与 `TestIsPseudoCompletion`（短正文判真、长正文判假、纯 reasoning 不误判），行为哨兵（临时移除优先分支/阈值分支）FAIL 证明测试真实执行；`git diff --check` PASS；6-review `STYLE: PASS`。后续反证：伪完成少量内容仍会先下发，账号失败与会话驱逐只能影响下一请求；同请求零泄漏恢复由后续版本承接。

## 0.1.24

### Fix — 流式请求补默认 max_tokens=20000，solo 长任务不再"刚开口就 done"

生产 0.1.23 日志证明 traework 收流链路健康（termination=done、22 chunks、宿主 200、feed failed:false），但账号 `qwen3.8-max` 全历史请求平均仅 77 tokens、最大 273 tokens，全部正常 done —— 上游 Trae 对**无 `max_tokens`** 的 solo 请求给极小默认上限，长任务（如分析项目、应持续数分钟的推理）刚输出一小段就自行结束。用户确认 Trae 原生客户端用同账号跑 `qwen3.8-max` 长输出正常、额度充足，根因锁定在插件链路缺 `max_tokens` 缺省。

1. `traework/upstream.go` 新增常量 `streamDefaultMaxTokens = 20000`（与 config models 样例一致）。
2. `buildTraePayload` 流式路径（`stream == true`）在客户端未传 `max_tokens`（`maxTokens <= 0`）时补默认值 20000；显式传入时保留原值；**非流式路径保持原样**（不补默认）。

验证：cgo shim build、vet、test 全绿；新增 `TestBuildTraePayloadStreamDefaultMaxTokens` 覆盖三种形态（流式缺省补 20000 / 流式显式保留 / 非流式不补），行为哨兵（临时移除补默认分支）FAIL `stream max_tokens = <nil>, want 20000` 证明测试真实执行且精确覆盖行为；`git diff --check` PASS；6-review `STYLE: PASS`。

## 0.1.23

### Added — 流式响应三出口补现场日志插桩，便于定位生产「生成中途停止」形态

0.1.21/0.1.22 反复修复"生成中途停止、无下文"，但流路径零日志，生产无法确认一次中断到底是上游未发 `done`（上游断流）、收到 `done` 后丢失（宿主掐流）、还是空响应。本轮为三条流出口补可检索日志，供生产 `docker logs` 按 `request_id` / `stream_id` 拉全链路判断流形态。

1. `traework/stream.go` 新增 `terminationLabel`，把终结类别映射为稳定短标签 `done` / `output_eof` / `invalid`。
2. `collectTraeStream`（同步收集）在 error / invalid / done 出口各落一条 `[traework] stream collect ...` 日志，含 `request_id` / `model` / `status` / `termination` / `chunks` / `finish` / `elapsed_ms`。
3. `aggregateTraeCompletion`（非流式聚合）同理，含 `chars` 统计。
4. `pumpTraeStream`（异步泵）在 error / finish emit error / truncated（output_eof）/ done 出口各落一条 `[traework] stream pump ...` 日志，含 `stream_id` 关联宿主流。
5. `traework/executor.go` 的 `handleExecStream` 在同步收集成功、上游错误、账号池耗尽、异步泵启动与启动失败各补账号维度日志。

全部为只读插桩，不改变业务分支、终止判定、账号核算或 usage 发布行为。

验证：cgo shim build、vet、test 全绿；临时必失败哨兵测试 FAIL 证明新增代码真实进编译后删除重跑全绿；`git diff --check` 退出码 0。真实日志形态待发布后由用户触发一次中断会话，从生产 `docker logs cli-proxy-api` 抓取核对。

## 0.1.22

### Fix — 上游长回答中途断流兜底收尾，不再中断报错

0.1.21 收紧流协议后，上游 Trae 长回答在生成中途 EOF（有部分 `output` 但未收到 `done`）时会被当作 `truncated SSE response` 致命错误处理，插件中断流并向 IDE 下发 error，用户表现为"生成中途停止、无下文"。

本次修复区分三种终结类别：业务完整（有 `done`）、上游中途断流（有部分 `output` 但 EOF 无 `done`）、空响应（无 `output` 无 `done`）：

1. `traework/stream.go` 将 `validate` 改为 `classify`，返回 `traeStreamTermination` 终结类别；仅空响应才报 `invalid SSE response`（保留 0.1.20 防空成功回归），部分 `output` 后 EOF 归为可兜底的中途断流。
2. 三条响应路径（同步非流式聚合、同步流式、异步流式）对中途断流统一补 `finish_reason="length"` 正常收尾，保留已生成内容，不再中断或报错。
3. `pumpTraeStream` 断流收尾时不清零账号故障、不记成功用量，以"不完整"标记落一条用量记录供面板识别；上游显式 `event:error`、下发失败或空响应仍走原失败出口。
4. **补齐读错误型断流兜底**：上一步的三态兜底只在 `scanSSE` 返回 `nil` 时生效，而 `scanSSE` 仅对干净 `io.EOF` 返回 `nil`；真实上游断流多表现为读错误（对端 RST / unexpected EOF / 宿主流桥 `Error` 非空），此时 `scanSSE` 原样返回 err，`pumpTraeStream` 判为致命失败并向 IDE 下发 error，0.1.22 前三步对该形态无效。`traework/upstream.go` 的 `scanSSE` 新增 `hasPayload` 判定参数：读错误时只要已累积可交付业务内容就按截断正常收尾（交由 `classify` 补 `length`、保留已生成内容），零内容才让读错误致命；`stream.go` 的 `traeSSETerminal` 新增 `hasPayload()`，三条响应路径接入该判定。

验证：cgo shim build、vet、test 全绿；新增 `collectTraeStream` 断流补 length、空响应仍报错、`pumpTraeStream` 断流不清零账号故障等回归用例；本次另增 `collectTraeStream` 读错误后补 length、零内容读错误仍致命、`aggregateTraeCompletion` 读错误补 length 并保留正文三个用例（含真实进编译哨兵验证）。

## 0.1.21

### Fix — 异步流改走宿主流桥实时读取，业务成功严格依赖 done 终止

修复 Trae 异步流式请求把完整上游响应体全量缓冲后才下发，长回答期间客户端长时间收不到分片；并收紧流协议判定，部分 `output` 后未收到 `done` 即结束的不完整响应不再被补成正常 stop。

1. `traework/upstream.go` 新增 `callLLMStream`，异步聊天改走宿主流桥实时读取，并透传 CPA 异步执行的 `host_callback_id`，客户端取消可传递到上游流。
2. `traework/host_bridge.go` 的 `rpcHostHTTPRequestWire` 新增外层 `host_callback_id` 字段，异步请求保留长生命周期 callback context；`hostHTTPDoStream` 增加 `hostCallbackID` 参数。
3. `traework/stream.go` 收紧 `validate`：业务成功必须收到明确 `done`；部分 `output` 后 EOF 返回 `truncated SSE response`，不再补成空 stop 伪成功；最终 stop 下发失败时走失败核算，不再复位账号或记成功用量。
4. `traework/stream.go` 的 `scanSSE` 在 EOF 前补齐无换行尾帧，避免最后一个 `done` 或 `output` 因缺少换行被丢弃；孤立 `event` 残片不伪造业务事件。
5. `traework/executor.go` 异步路径统一关闭上游流句柄，非预期 panic 转成可见失败，避免穿透插件运行时。

验证：cgo shim build、vet、test 全绿；新增 `host_callback_id` 透传、SSE 跨分片重组、无换行尾帧解析等回归用例通过。真实 Trae/Qwen 上游请求尚未在本地自动化环境发起。

## 0.1.20

### Fix — 拒绝 HTTP 200 异常响应形成空成功

修复 Trae 上游在 HTTP 200 下返回普通 JSON、HTML、未知 SSE 事件、无换行尾帧或空 `output` 时，插件可能将无有效业务事件的响应收束为空 completion 或空 stop 分片的问题。

1. `traework/stream.go` 新增 `traeSSETerminal`，仅在出现可转换的 output 或明确 done 时允许成功结束。
2. 同步非流式和同步流式在无有效终止时返回 `invalid SSE response`，不再生成空成功响应。
3. 异步流式改走失败出口，不再成功复位账号状态或写入成功用量。
4. 保持既有 `event:error` 解析、账号失败分类及异步流不跨账号重放策略不变。

验证：cgo shim build、vet、test 全绿；代码格式、差异检查与生产代码测试污染扫描通过。真实 Trae/Qwen 上游请求尚未在本地自动化环境发起。

## 0.1.19

### Fix — 异步流式请求补齐 Token 用量统计

修复 Trae 异步流式请求虽然能够正常返回响应，但请求结束后没有写入共享 usage feed，导致 `workbuddy-token-usage` 无法在 Token 用量统计面板展示记录的问题。

1. `handleExecStream` 向后台 `pumpTraeStream` 传递完整用量上下文，包括客户端模型别名、Trae 上游模型、账号 UID、HTTP 状态和请求开始时间。
2. `pumpTraeStream` 累计已经发送的标准流式分片，在异步流成功结束时统一发布一次用量。
3. SSE 扫描失败或分片转换失败时，统一发布一次失败用量；保留原有错误通知、账号故障恢复、`streamClose` 和 stop chunk 行为。
4. 继续使用 `estimateUsageFromChunks` 估算输出与总 Token，并保留 `alias=qwen-max-latest`、`model=qwen3.8-max` 的统计维度。

验证：cgo-shim build、vet、test 全绿；异步流回归测试确认成功请求只写入一条 `traework-provider` usage feed 记录。

## 0.1.18

### Feat — 面板删除账号 + 修复 trae 账户模型请求异常（SSE 业务错误换号）

用户实测：走 trae 插件账户的 doubao / deepseek 模型明显出问题，而 workbuddy 插件正常。
根因：Trae 上游 `llm_utils_chat` 把业务失败（4011 限流 / 14018 额度用尽）藏在 **HTTP 200 + SSE `event:error`** 里返回，而 traework 的换号重试只看 HTTP 状态码层——SSE 层账号级错误不触发换号，坏账号被反复命中；workbuddy 上游失败在 HTTP 层且流式路径有换号循环，所以不受影响。

1. **【P0】SSE 业务错误纳入账号换号**（`traework/executor.go` / `stream.go`）：
   - 非流式：`aggregateTraeCompletion` 的 SSE 错误若判定为账号级（`isAccountFailure`，覆盖 4011 / 14018 等）→ 同请求 `pickNextAuth` 换号重试。
   - 流式同步收集：重构为与 workbuddy 同款换号循环（HTTP 4xx 与 SSE 业务错误均换号）。
   - 流式异步 pump：`pumpTraeStream` 增加 authID 参数，SSE 错误计入 failover 冷却（原缺口：pump 内 SSE 错误不记冷却，坏账号不会被隔离）。已 emit 的部分 chunk 无法换号，靠冷却保证下一次请求绕开。
   - 业务码分类（`accountFailover_test.go` 新增 5 用例）：4011 / 14018（含 json 形态）→ 账号级；4001 / 4023 → 保守不换号（参数/模型名问题换号无益）。

2. **【Feat】面板删除账号**（对齐 workbuddy 0.14.7）：
   - 后端 `POST /delete`（`traework/management.go` `handleDeleteAuth`）：严格校验链（auth_index 非空 → host 列表存在 → `isTraeworkAuthFileName` 归属 → `hostAuthGetBundle` → 物理索引一致 → `isSafeAuthPath` → `deleteAuthFileInDir` 物理删除 → `clearDeletedAccountState` 清理 6 类内存态）。
   - 前端（`traework/panel.html`）：账号卡右上角 `×` + 二次确认模态框（取消 / 遮罩 / Escape 均不发请求），确认后 `POST /delete` 并刷新列表。
   - 新增 `isTraeworkAuthFileName`（`authfile.go`）+ `clearDeletedAccountState` / `clearFailoverStateForAuth` + `auth_delete_test.go` 4 用例。

3. **验证**：cgo-shim build + vet + test 全绿；panel.html 占位符替换后两个 script 块 `node --check` 全 PASS。

## 0.1.17

### Fix — 全部签到后积分被本次奖励覆盖 + 面板"系统状态"标题错位

两个 bug 都源于 0.1.16 版本对齐五件套面板时遗漏的回归，合并发布即可覆盖生产解决。

1. **【P0】全部签到后积分变 200 而非账户总积分**（`traework/checkin.go` `runFleetCheckin`）：
   - 现象：面板点"全部签到"后，每个账号卡片上的"积分"显示为本次签到奖励值（恰好是 200），而不是账户的真实剩余积分。
   - 根因：成功签到后写缓存时把 `res.Points`（**本次签到的奖励积分**）当成了 `TotalRemain`（**账户总剩余积分**）写入 `cacheCredits`，覆盖了真实 remain。
   - 修复：改为成功签到后调用 `accountPoints` 真实查询积分并写入缓存（与单账号 `handleManualCheckin` 分支保持一致），不再使用 `res.Points`。
   - 影响：刷新触发"全部签到"路径，缓存被该 bug 污染为奖励值后只能等待生命周期自动刷新或手动点"刷新"才能恢复；面板一直显示错误。

2. **面板"系统状态"标题错位**（`traework/panel.html`）：
   - 现象：汇总卡顶部标题是"系统状态"，但汇总项（保号池 watchdog / keepalive / lifecycle / 异常池）实际上分别是各账号层用量与子系统开关的汇总，应叫"用量汇总 · 全部账号"。
   - 修复：标题文案改为"用量汇总 · 全部账号"（行为不变）。

## 0.1.16

### Feature — 对齐 workbuddy 五件套 + 完整面板（保号 / 异常 / 失败计数 / 会话粘性 / 生命周期）

traework 此前仅有 failover 阶梯冷却 + anomaly 冻结 + CPAMP 用量上报，面板为旧版表格。本次按 workbuddy 架构全量对齐，补齐五件套后端能力并重写面板，使 traework 与 workbuddy / qoderwork 功能面一致。

1. **失败计数持久化（`counter.go`）**：`publishUsage` 埋 `recordOutcome` 累计成功/失败，随 watchdog tick 折叠为顶层 `success_count` / `failed_count` 落盘（经 `persistAuthDirect` 直写，宿主不认识的字段不丢失）。
2. **会话级粘性路由（`session_auth.go`）**：`scheduler_mode=session` 时同一会话钉同一账号 1 小时，账号失效（冷却/异常/保号/耗尽）自动驱逐会话绑定换号。
3. **保号池 + 看护循环（`preserve.go` + `watchdog.go`）**：积分低于 `preserve_threshold`（默认 50）的账号进入保号池，路由时优先排除、仅在无其它可用账号时兜底；watchdog 每 10 分钟刷新积分快照并更新保号池归属（防全保号锁死：全保号时保留全列表回退）。
4. **节流刷新（`refresh_runner.go`）**：异步刷新队列，1 账号/秒节流，面板进页自动刷新 + 每 10 分钟定时刷新。
5. **生命周期自动停用（`lifecycle.go`）**：`lifecycle_auto` 开启时账号积分耗尽（remain<=0）自动禁用（不自动复活，需面板手动启用或重新导入），禁用时驱逐会话绑定。
6. **每日 token 保号（`keepalive.go`）**：本地时间 22:00 通过 Trae ExchangeToken 端点自动续期 access token（写顶层 token/refreshToken/expiredAt）；刷新令牌失效的账号自动标记禁用待重新导入；`parseTraeAuth` 改为顶层 runtime 字段优先于 credential blob，避免刷新结果被静态凭据覆盖。
7. **直写通道（`authfile.go` `writeAuthFileDirect`）**：temp+rename 原子直写，供 preserve / counter / lifecycle / keepalive 共用（`host.auth.save` 会丢弃宿主不认识的顶层字段）。
8. **完整面板（`panel.html` + `panel.go` go:embed）**：卡片网格 + 筛选 chips（全部/可用/保号/异常/耗尽/失败/停用）+ 系统状态汇总（watchdog 阈值/间隔/开关/池大小、keepalive 开关/上次运行、lifecycle 开关、异常池大小）+ 账号卡（昵称/UID/积分/成功/失败/连败/冷却/保号/异常/禁用/活跃徽标 + 签到/设为活跃/启停/解冻/刷新）+ 异步刷新（2s 轮询三态）+ keepalive 手动保号 + storage.json 导入。
9. **dashboard 聚合（`management.go` / `active_auth.go`）**：`/accounts` 单次拉取返回 preserve / lifecycle / keepalive 子系统状态 + `checkin_auto` / `server_time`；新增 `/refresh`、`/refresh/status`、`/keepalive`、`/keepalive/status`、`/lifecycle` 路由。
10. **配置项**：`scheduler_mode` 支持 `session`；新增 `token_keepalive` / `lifecycle_auto` / `preserve_threshold` / `preserve_watchdog_interval` / `preserve_watchdog_enabled`。

- 测试：`counter_test.go` / `session_auth_test.go` / `watchdog_test.go` / `keepalive_test.go` 新增，移植 workbuddy 用例并适配 traework。
- 验证：cgo-shim build+vet+test 全绿；panel.html 两 script 块 node --check 通过。
- 未涉及：workbuddy-provider；qoderwork-provider 本版不改动。

## 0.1.15

### Feature — 共享 usage feed 写入（适配 token-usage-tracker）

traework 的 token 用量此前只走 CPAMP HTTP 上报，不写共享 NDJSON feed，导致独立的 `token-usage-tracker` 插件完全收不到 traework 数据（宿主 UsagePlugin 广播对插件 executor 恒为空，tracker 的 `handleUsage` 也收不到）。本次按 workbuddy 的 `usage_feed.go` 模式补齐，使三插件（workbuddy / qoderwork / traework）用量均可被 tracker 可视化。

1. **新增 `usage_feed.go`**：解析 `usage_feed_enabled` / `usage_feed_path`（默认 `<CLIProxyAPI 根目录>/data/token-usage-feed.ndjson`，与 workbuddy / tracker 默认一致）；`recordUsageFeed` 写行与 workbuddy feed 同构（`provider=traework-provider` / `executor_type=traework` / `auth_type=oauth` / `source=authUID`；`session_key` / `reasoning_effort` / `ttft_ns` 写零值保持 schema 自描述）；O_APPEND 逐行追加 + 128MB 轮转。
2. **`publishUsage` 接入**：goroutine 中 `recordUsageFeed(...)` 与 `forwardUsageToCPAMP(...)` 并列（feed 独立于 CPAMP 配置，未配 CPAMP 也能统计）。
3. **`handleUsage` 不写 feed**（与 workbuddy 一致）：避免与 tracker 自身 UsagePlugin 广播重复计数。
4. **配置接线**：`config.go` `configure()` 在 `cfgMu.Lock()` 之外调 `configureUsageFeed`（自带 `usageFeedMu`，防锁序死锁）；`main.go` ConfigFields 注册 `usage_feed_enabled` / `usage_feed_path`。
5. **tracker 侧文档**：`token-usage-tracker/README.md` 数据链路更新为 workbuddy + traework 双写入方。

- 测试：`usage_feed_test.go` 新增（配置解析 / NDJSON 追加与字段断言 / 128MB 轮转）。
- 验证：cgo-shim build+vet+test 全绿；tracker `decodeFeedLine` 字段逐一核对匹配，无 provider 硬编码。
- 未涉及：workbuddy-provider；qoderwork-provider 本版不改动。
