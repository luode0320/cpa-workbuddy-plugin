# Changelog

## [Unreleased]

### Fixed
- 使用 schema 6 的原始管理 JSON 响应契约，避免官方 OAuth 授权链接的 `&` 被宿主转成 `&amp;`，导致 `response_type`、`redirect_uri`、`state` 参数缺失；需要支持 schema 6 的 CPA 宿主。

## [0.1.18] - 2026-10-13

### Feat & Fix
- **套餐健康状态路由门控与换号重试判据加固**：
  - `accountRoutable` 校验缓存套餐状态，过期/失效账号硬排除出调度候选集；
  - `shouldRotateOnUpstreamErr` 补齐 `exceeded the quota`、`14018` 与 `额度已用尽` 关键词换号重试。

## [0.1.17] - 2026-10-13

### Feat
- **引入官方规格矩阵自适应 max_tokens 计算**：
  - 新增 `model_specs.go`：维护智谱 GLM 家族（`glm-5.3`, `glm-5.3-flash`, `glm-4-plus` 等）官方规格矩阵；
  - 请求执行体原地优化：在 `mutateModel` 中接入 `relaxMaxTokensInPlace`，客户端未传时补齐官方最大值，推理模型在客户端指定 <= 8192 时放宽至 ReasoningMax（如 GLM-5.3 为 128000），避免长推理思考被短 token 截断；
  - 模型静态目录（`allConfiguredModels`）动态注入 `resolveOfficialModelSpec` 规格，废除旧有静态写死限制；
  - 补充自适应计算与 payload 变异单元测试。

## [0.1.16] - 2026-10-13

### Fixed
- **彻底重构新导入账号官方授权激活开通链路（无需桌面客户端）**：
  - 针对此前调用直接 claim 接口受第三方域名阿里云验证码（`3007 captcha verify failed`）拦截导致的激活无效问题，彻底对齐官方客户端登录机制；
  - 后端新增 `/activate-start` 与 `/activate-poll` 路由：支持指定存量账号发起官方 CLI 授权会话，并在授权完成（ready）时在存量凭据（如 `zcode-d7a0cb4afa8f77c0.json`）中就地更新 Token、AccessToken 与用户信息，保持 AuthID 不变，杜绝重复卡片；
  - 授权激活成功后立即向官方端点拉取最新 Start Plan 额度快照并缓存入库，即刻下发每日 800 万 token；
  - 前端控制面板重构「⚡ 激活开通」与顶部「OAuth 授权登录」交互：弹出官方授权确认窗口并在面板内实时轮询状态，用户一键授权后卡片即刻变绿并显示 8.00M 额度进度条；
  - 单卡「刷新额度」在检测到未激活状态或无生效计划时自动联动引导官方授权激活；
  - 补齐 `/activate-start` 与 `/activate-poll` 完整生命周期单元测试与反证测试。

## [0.1.15] - 2026-10-12

### Added
- **插件自带新账号激活开通每日额度功能（无需客户端登录）**：
  - 新增后端路由 `POST /claim`：调用官方 `POST /api/v1/zcode-plan/billing/claim` 激活开通 `zcode-v3-start-plan`，注入阿里云验证码安全验证头（`X-Aliyun-Captcha-Verify-Param`）与设备指纹；
  - 刷新额度静默激活联动：在后台刷新或拉取额度遇到新导入账号无生效计划时，后端自动先尝试一次静默激活，成功直接返回每日额度；
  - 管理面板卡片支持未激活状态识别与「⚡ 激活开通」一键激活：卡片展示「未激活」Badge，点击激活开通或单卡刷新时自动按需加载阿里云智能无感/滑块验证 SDK，无感验证通过后自动完成激活并即刻回填每日 800 万额度；
  - 新增 2 项套餐激活单元测试（成功激活解析、请求头验证、验证码要求分支响应）。

## [0.1.14] - 2026-10-12

### Fixed
- **用量日志对齐宿主统一管道与测试探活记账**：重写 `usage_feed.go`，将此前写入孤立路径 `~/.antigravity_cockpit/data/` 的错误路径改为自动探测宿主根目录 `<CLIProxyAPI root>/data/token-usage-feed.ndjson`，并增加 128MB 截断保护；完全对齐 `token-usage-tracker` 消费的嵌套 `tokens` 结构与 `latency_ms`、`ttft_ns`；将账号昵称（如 `ZCode 旅行者2435`）写入 `source` 字段；在面板「测试」按钮（`/test-active`）中接入用量上报，彻底消除“测试成功但在用量面板完全无记录”的假象。
- **修复模型别名反向解析与 GLM 官方大小写归一化**（解决 `GLM-5.3` 可用但 `glm-5.3` 报 `400 unknown provider`）：
  - `main.go` 将 `ExecutorModelScope` 从 `Static` 纠正为 `ExecutorModelScopeOAuth`，使宿主能正确将 `oauth-model-alias`（`GLM-5.3 -> glm-5.3`）绑定至该插件；
  - `models.go` 补齐 `cacheModelAliases` 与 `resolveUpstreamModel`，实现客户端模型别名到上游权威模型的反向还原；
  - 实现 `normalizeOfficialGLMModelID`：对齐 ZCode 官方客户端行为，将任何 `glm-` 开头的小写或变体模型名规范化为上游端点识别的官方大写格式（如 `glm-5.3` -> `GLM-5.3`, `glm-5.3-flash` -> `GLM-5.3-Flash`）；
  - `MethodModelForAuth` 支持自动剥离 `.json` 后缀并从生效套餐的 entitlements 动态提取可用模型。
- **优化流式换号重试与故障退避**：在 `handleExecStream` 中补齐 3 次换号重试循环；将阶梯退避优化为 15 秒固定冷却（对齐 `workbuddy`），换号错误判据纳入上游并发超限（`3009` / `concurrency`），消除单账号并发瞬间打满导致全插件死锁的缺陷。
- **额度刷新稳定性与前端卡顿展示修复**：
  - 修复 `panel.html` 后台轮询收尾时二次调用 `fetchAndPatchQuota` 带有 `&force=1` 重复轰炸上游频控的缺陷，改为直接读取缓存；
  - `quota.go` 增加旧有效快照兜底，在上游瞬时网络波动或拉取失败时保留历史缓存；
  - 账号 OAuth 授权与导入成功后自动调用 `EnqueueOne` 预热额度；
  - `quotaHTML` 优化无数据展示，未获取额度时提示「未获取 (点刷新获取)」，消除永久转圈假死。

## [0.1.13] - 2026-10-12

### Fixed
- **额度查询端点路径改为 `/credits`**：0.1.12 注册的 `GET /quota` 与宿主 CPA 自身的保留额度路由冲突，生产实测被宿主层拦截并返回 `{"error":"auth_index is required"}`，导致面板额度始终拉不到。改用 `/credits`（与 workbuddy 同类端点命名一致），生产热重载后 `/credits`、`POST /refresh`、`GET /refresh/status` 全部返回 200，额度正常展示。

## [0.1.12] - 2026-10-12

### Added
- **账号面板额度可视化（参照 workbuddy 面板重设计）**：此前 zcode 面板只有账号状态，缺少 workbuddy 那样的额度进度条与用量汇总，两者观感差距明显。
  - 新增 `quota.go`：调用 `GET https://zcode.z.ai/api/v1/zcode-plan/billing/current` 解析当前计划的 entitlements（各模型每日授予额度 `grant_units`），带 5 分钟进程内缓存与显式失效。该端点是唯一可靠的额度来源——`billing/balance`（余额/已用）在免费 start-plan 账号上固定返回 `400 code 3001`，故不采用。
  - 新增 `refresh_runner.go`：后台批量额度刷新器（幂等、逐账号进度、串行节流），暴露 `GET /refresh/status` 只读快照供前端轮询。
  - 新增管理端点：`GET /credits`（单账号或全量额度，支持 `force=1` 绕过缓存）、`POST /refresh`（启动后台刷新轮）、`GET /refresh/status`（读取进度）。
    > 路径用 `/credits` 而非 `/quota`：`/quota` 是宿主 CPA 自身的保留额度路由，插件注册该路径会被宿主拦截（实测返回宿主层 `{"error":"auth_index is required"}`），与 workbuddy 的同类端点命名保持一致。
  - `GET /accounts` 附带已缓存额度快照（`quota` 字段），面板首屏即可展示；无缓存时由进入页面自动触发的后台刷新补齐。
- **面板功能对齐 workbuddy**：
  - 账号卡新增每日额度进度条（按 entitlement 展示模型名 + 授予额度 + 周期）与计划名徽章。
  - 新增「额度汇总」卡（每日额度合计、额度已获取/未知、可用/禁用/测试/冷却计数）与消耗占比进度条。
  - 新增「刷新额度」全局按钮与单卡「刷新额度」按钮，配合后台刷新三态（pending/running/done/failed）与顶部「额度刷新中 N/M」进度。
  - 排序新增「额度 ↑ / ↓」，搜索与筛选计数联动额度汇总。
  - `api()` 对齐 workbuddy 的健壮语义：`__silent` 静默请求不参与鉴权熔断、401/403 阈值清 key、空/非 JSON 响应结构化降级。
- 新增 6 项额度回归测试（解析、缺 JWT、缓存命中与 force 绕过、缓存过期、刷新器快照与幂等、全量查询容错）并经受必失败哨兵反证。

## [0.1.11] - 2026-10-12

### Fixed
- **修复测试模型报 `405 + code 3012 request has been blocked due to unusual activity`**（刚登录的 OAuth 账号探活/调用全部被上游风控拦截，表现为"刚登录就不可用"）：
  根因是请求头 `X-ZCode-App-Version` 取了具体版本号（`3.14.4`）。线上实测（`zcode.z.ai/api/v1/zcode-plan/anthropic/v1/messages`，同一账号 JWT、同一 IP 交错复测 8 次）确认：该头取具体版本号（`3.14.4` / `3.15.1` / `3.13.0` …）一律被上游 WAF 拦为 `405 + code 3012`；取非版本号字面量（`unknown` / `cli` / `electron`）稳定放行 `200`。
  桌面端 `buildZCodeSourceHeadersFromContext` 亦以 `??"unknown"` 兜底，说明 `unknown` 才是上游认可的合法取值。修复后 `AppVersion` 默认改为 `unknown`，模型调用、模型发现、探活一并生效。
- 新增回归守卫测试 `TestFingerprint_AppVersionMustNotBeConcreteRelease`，锁定 `X-ZCode-App-Version` 不得回退为语义化版本号（临时改回 `3.14.4` 会立即 FAIL 并提示 3012 风控）。
- **补齐宿主 `oauth-excluded-models` 过滤**（`filterExcludedModels`）：宿主只在 `model.for_auth` 的按账号路径替插件过滤，`model.static` 返回的列表会被原样登记进全局模型目录并参与路由选模，因此插件必须在出口自行过滤——此前 zcode 是 5 个 OAuth 插件里唯一缺口。现于 `MethodModelStatic` 与 `MethodModelForAuth` 两个活出口应用（`MethodModelRegister` 为死代码：宿主仅在声明 `model_registrar` 能力时才调用它，故不改）。未配置排除项时原样返回入参，零行为变化。新增 4 项用例覆盖命中/缺省/键大小写漂移/入参不被就地压缩。

## [0.1.10] - 2026-10-12

### Fixed
- **修复测试模型报 `401 令牌已过期或验证不正确`**（OAuth 账号刚登录即失败）：此前插件把 OAuth 令牌直接作为 `x-api-key` 发往 `open.bigmodel.cn/api/anthropic`，与官方实际链路不符，必然 401。
- **按账号类型路由到两条官方链路**（新增 `upstream_target.go`，2026-10-12 线上实测 + 桌面端逆向确认）：
  - **start-plan（免费版）**：以 `zcodeJwtToken` 作 `Authorization: Bearer` 访问 `https://zcode.z.ai/api/v1/zcode-plan/anthropic`（桌面端 `resolveCurrent` 对 `planKind==="start-plan"` 即直接返回 `zcodeJwtToken`）；
  - **付费 Coding Plan**：先调 `POST https://api.z.ai/api/auth/z/login {"token": <zai access_token>}` 换取业务令牌，再以 `x-api-key` 访问 `https://api.z.ai/api/anthropic`（对应官方 `zcode-builtin.json` 的 `zai-api` 模板 `access.type=zhipu-coding-plan-api-key`）。
- 新增 `business_token.go`：业务令牌换取与 30 分钟进程内缓存、显式失效接口；上游成功判定兼容 `code=200` 与 `code=0`。
- `defaultBaseURL` 对齐官方模板：`open.bigmodel.cn/api/anthropic` → `api.z.ai/api/anthropic`；用户显式配置 `base_url` 时仍尊重配置。
- 模型请求、模型发现、探活统一经 `resolveUpstreamTargets` 解析端点与凭证，静态 API Key 账号不受影响。
- 新增 4 项业务令牌回归测试并经受必失败哨兵反证。

## [0.1.8] - 2026-10-12

### Fixed
- **彻底修复 OAuth 授权失败（`Redirect URI not registered for this client`）**：经 `zcode.z.ai` 线上接口实测 + ZCode 3.15.1 桌面端 `app.asar` 逆向确认，`chat.z.ai` 对 `client_id` 执行 redirect_uri 严格白名单，仅接受服务端注册的 `https://zcode.z.ai/api/v1/oauth/cli/callback/zai`；此前照抄桌面端 deep-link 或改用 CPA 本地回调（`localhost:1455` / `127.0.0.1:8317`）均被拒绝。
- **重构为官方 CLI 轮询链路**：`handleStartLogin` 改为调用 `POST /api/v1/oauth/cli/init` 获取服务端签发的 `authorize_url`（回调已注册）；`handlePollLogin` 改为轮询 `GET /api/v1/oauth/cli/poll/<flow_id>`，`status=ready` 时一次性取得 `token`（ZCode JWT）、`zai.access_token` 与用户档案并落盘。
- 移除本地回调桥接与 `redirect_uri` 手工回传路径，桥接页改为引导跳转 + 结果查询；新增 3 项回归测试锁定「授权地址必须使用官方注册回调」与 init/poll 失败分支。

## [0.1.7] - 2026-10-11

### Fixed
- 修复 OAuth 授权重定向地址：将授权 URL 中的 `redirect_uri` 由桌面客户端地址（`zcode.z.ai/app/oauth/login?redirect=zcode://oauth/callback`）调整为 CPA 宿主标准回调地址（`http://localhost:1455/auth/callback` 或 `req.BaseURL`），彻底消除登录完成后调起本地 ZCode 客户端的问题，实现直接回调至 CPA 接收凭据。
- 令牌交换兼容容错：`exchangeZaiOAuthCode` 优先使用 CPA 回调地址，同时保留备用桌面客户端地址重试兜底，并支持自适应从完整回调 URL 中解析回调基地址。

## [0.1.6] - 2026-10-11

### Fixed
- 修复 OAuth 授权重定向地址：将授权 URL 中的 `redirect_uri` 由桌面客户端地址（`zcode.z.ai/app/oauth/login?redirect=zcode://oauth/callback`）调整为 CPA 宿主标准回调地址（`http://localhost:1455/auth/callback` 或 `req.BaseURL`），彻底消除登录完成后调起本地 ZCode 客户端的问题，实现直接回调至 CPA 接收凭据。

### Performance
- 测试探活（`/test-active`）改为流式首包即停：`active_ping.go` 的 `handleTestActiveWithAuth` 改为流式请求（`stream=true, max_tokens=1`），读取首个 512 字节数据块并校验无错误后即刻返回并异步关闭底层流，消除 `GLM-5.3` 等思考模型的完整生成等待。

## [0.1.5] - 2026-10-11

### Changed
- 重构 ZCode 账号授权为官方真实 Z.ai OAuth 授权链路：
  - 授权直达端点：对接官方 `chat.z.ai/auth`，下发真实 `client_id=client_P8X5CMWmlaRO9gyO-KSqtg` 与带版本及协议的 `redirect_uri=https://zcode.z.ai/app/oauth/login?redirect=zcode%3A%2F%2Foauth%2Fcallback&app_version=3.15.1`，生成 32 字符 hex `state`；
  - 令牌交换接口：通过 `https://zcode.z.ai/api/v1/oauth/token`（`provider=zai`）将授权码交换为正式 `zcodeJwtToken`、`access_token` 及用户档案数据；
  - 凭据存储升级：`StoredAuth` 扩展 `token`、`access_token`、`user_id`、`username`、`email`、`avatar_url`、`expires_at`，保持与已有 APIKey 的完全向下兼容；
  - 桥接页面升级：`/oauth-bridge` 改为 Z.ai 官方登录授权引导与回调地址（含 code 与 state）解析提交；
  - 宿主回调自动捕获：`handlePollLogin` 支持自动读取宿主 `.oauth-zcode-provider-<state>.oauth` 载荷文件并兑换落盘；
  - 管理面板升级：`panel.html` 增加「OAuth 授权登录」按钮，支持账号用户名/邮箱/用户ID展示与凭据导入。

## [0.1.4] - 2026-10-11

### Fixed
- 修复桥接授权页丢失 `state` 参数：宿主将 URL path 与 query 分开传递（Path 不含 query、参数在 Query 字段），此前误用从 path 解析出的 query 覆盖宿主传入的 `req.Query`，导致管理后台「OAuth 登录」下发的授权链接打开后显示「缺少 state 参数」、无法完成绑定。
- 新增宿主调用契约回归测试，复现「Path 不含 query + 参数在 Query」的真实调用形状，防止再次回归。

## [0.1.3] - 2026-10-11

### Added
- 实现管理后台「OAuth 登录」桥接授权机制：支持 `StartLogin`、桥接授权输入页（`/oauth-bridge`）、`PollLogin` 轮询保存与 `RefreshAuth`，用户可在 CPA 管理后台直接点击 ZCode 发起授权绑定并自动保存账号。
- 恢复 `Capabilities.AuthProvider = true`，管理后台「OAuth 登录」卡片恢复展示且点击可正常交互完成授权。

## [0.1.2] - 2026-10-11

### Fixed
- 修正 capabilities 注册配置：将 AuthProvider 设为 false，消除在 CPA 宿主管理后台「OAuth 登录」中误暴露出 zcode-provider 卡片并在点击时报 `failed to generate authorization url` 的缺陷。
- 为 `MethodAuthLoginStart` 补充明确的拒绝提示处理（API Key 凭据请在专属面板导入管理），避免未注册方法异常。

## [0.1.1] - 2026-10-11

### Changed
- 管理面板按 WorkBuddy 面板设计系统整体重写，视觉与交互风格统一：
  - 主题令牌（`--bg/--card/--acc/--ok/--warn/--err` 等）与 WorkBuddy 面板同源，支持 `data-theme=white|dark` 与嵌入时镜像 CPA 父页主题（早期主题同步脚本 + MutationObserver）。
  - 布局改为「标题 + 副标题 + 工具栏 + 筛选标签 + 用量汇总卡 + 三列账号卡网格」。
  - 新增账号概览汇总卡（总账号数 / 可用 / 禁用 / 测试失败 / 冷却中 / 模型条目）与筛选标签实时计数。
  - 账号卡改为徽章式状态（可用 / 禁用 / 测试 / 冷却）、脱敏 Key、创建与更新时间、模型标签、卡片内操作按钮。
  - 新增右上角滑入式 toast 通知栈（成功 / 警告 / 失败三态）与统一 busy spinner 按钮态。
  - 模态框（导入 / 删除 / 测试）改为标准 modal 结构（header / body / footer + 遮罩点击关闭），删除走二次确认。
  - 新增工具栏搜索（备注 / Key / ID 模糊匹配）与排序三态（创建倒序 → 名称升序 → 名称降序）。
  - 测试模态框改为「先列出账号模型 → 点选模型即测」，结果内联展示并同步刷新账号状态。
- 管理路由统一由 `normalizeMgmtSubPath` 解析（面板 `/panel`、`/status`、`/v0/resource/...` 与 `/v0/management/...` 前缀全部兼容），移除冗余的 `isPanelPath`。
- 新增 `panel_test.go` 面板渲染与路由契约回归测试。

## [0.1.0] - 2026-10-11

### Added
- 初始版本：移植并重构 `cpa-plugin-zcode` 为 `zcode-provider` 独立插件。
- ZCode 桌面客户端指纹（`internal/mimic`）Header 注入。
- 多账号管理体系（`authfile.go`、`management.go`）：导入、导出、删除、启停与全部启停。
- 专属管理面板（`panel.html`）：支持深浅色主题、指标概览、选模型连通性测试弹窗。
- 多账号轮询调度器（`scheduler.go`）与 40x 换号 / 429 阶梯退避故障转移（`failover.go`）。
- Anthropic 协议非流式与流式推理执行器（`executor.go`、`stream.go`）。
- Token 用量统计上报（`usage_feed.go`）。
