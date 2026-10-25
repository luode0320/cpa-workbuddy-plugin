# Changelog

## 0.2.0

### Breaking - 故障排除从插件内存升级为宿主可见的写盘禁用

**破坏性变更**：内存阶梯冷却（60s × 连续失败次数，上限 10 分钟）与账号×模型冷却状态机已整体下线。升级后不再存在「冷却中」这一状态，取而代之的是盘上 `disabled: true`。

- 变更要点:
  1. 根因：内存冷却只存在插件里，宿主选择器看不见（它只读 `Auth.Disabled` / `Status`）。同一个请求内，宿主仍会把这个已知的坏号发给插件、拿一次失败再换下一个号。
  2. 写盘禁用（`failover_disable.go`）：上游失败即写 `disabled: true` 加三个归属标记（`failover_disable` / `failover_disable_model` / `failover_disable_at`），走宿主自己的路由判据。恢复由 10 分钟探活裁决，且**只回放当初失败的那一个模型**——别的模型探通不能证明失败模型已恢复。
  3. 归属边界：盘上已 `disabled` 但无本机制标记的账号（用户手动禁用、额度耗尽禁用）一律不接管、不解除，避免抢走别人的恢复职责。宿主 persist 抹掉标记时，下一轮探活按「本进程写过禁用 + 盘上确实 disabled」补回。
  4. 额度耗尽（`quota_disable.go`）：`HasUsage && TotalRemain <= 0` 时写 `disabled` 加 `quota_disable` / `quota_disable_at` / `quota_disable_reset` 三标记；恢复需**同时**满足越过重置时刻与探活成功。`HasUsage=false`（只有 `billing/current` 通道）一律不动手——此时剩余量是授予量回退，据此判耗尽会把全部账号误禁。
  5. 额度快照补重置时刻：`zcodeQuotaEntitlement` 新增 `period_start` / `period_end`，快照新增 `earliest_reset`（全部 entitlement 中最小非零 `period_end`）。上游实测 `period_end` = 当日 23:59:59 CST，按自然日重置。
  6. 物理文档归并写入：`writeAuthFileDirect` 从「按结构体重建整篇」改为「读旧文档 → 覆盖结构体已知键 → 写回」。原实现会抹掉结构体未声明的顶层键，包括本机制刚写好的禁用标记——E2E 探针实测复现了「探活成功解除禁用后 `disabled` 又被写回 true」。
  7. 面板：`cooling_down` 字段改名为 `failover_disabled`；新增「额度耗尽」徽章（带北京时间恢复时刻）与筛选标签；汇总行增加额度耗尽计数。
  8. 测试：`cgo-shim` 全绿；19 个新增/改写的变异全部被测试检出；WSL 真实 c-shared `.so` + ctypes 宿主桥 E2E 验证失败禁用、探活恢复、流式终止三条链路（额度链路的端点硬编码不可注入，改由单测覆盖）。
- 涉及文件: `VERSION` `main.go` `failover.go` `failover_disable.go` `quota_disable.go` `model_circuit.go` `failure_class.go` `scheduler.go` `management.go` `panel.html` `authfile.go` `refresh_runner.go` `executor.go` `stream.go` `usage_feed.go` `redact.go` `quota.go` 及对应测试；删除 `cooldown_probe.go` `cooldown_probe_test.go` `cooldown_probe_cycle_test.go` `auth_model_cooldown_test.go` `failover_test.go`

## 0.1.35

### Fix - 保留 OAuth 授权链接原始查询参数

- 变更要点:
  1. 现象：点击面板「激活开通」后，ZCode 官方授权页提示 `response_type`、`redirect_uri`、`state` 三个查询参数缺失。
  2. 根因：插件固定依赖的 v7 SDK 将 `pluginabi.SchemaVersion` 定义为 3，而 CPA 对 schema < 6 的插件管理 JSON 会做 HTML 实体转义，`authorize_url` 的 `&` 变成 `&amp;`，浏览器把参数名解析成 `amp;redirect_uri` 等。
  3. 修复：ZCode 显式声明 `pluginSchemaVersion = 6`，让管理接口返回原始授权 URL；面板继续用 `esc()` / `textContent` 在显示边界处理文本，不依赖宿主转义。
  4. **兼容性**：需要支持 schema 6 的 CPA 宿主（v7.2.155 起）；旧宿主会拒绝加载本插件。
  5. 测试：`main_test` 断言 `reg.SchemaVersion >= 6`（改回 3 时精确失败）；`management_test` 用含百分号编码回调地址的完整授权链接，断言 JSON 往返不改分隔符并覆盖四个参数；`test/zcode/panel_raw_json_repro.mjs` 执行真实面板脚本校验转义与参数保留。
- 涉及文件: `main.go` `main_test.go` `management_test.go` `README.md` `VERSION`

## 0.1.34

### Fix - 11128 模型级拦截并入账号×模型冷却，移除独立熔断

- 变更要点:
  1. 根因：`classifyUpstreamFailure(400, 11128)` 返回 `failureScopeNone`——11128 从未进入冷却体系。旧实现另建独立熔断状态机（跨 3 账号命中 → 打开 60 秒闸门），但该闸门只拒绝请求、**不改变路由**，宿主仍把请求派给已被拦截的账号×模型，且逐条在本地失败（生产实测 duration 55–160ms），形成「一整片账号全失败」的记录。
  2. 改造：11128 归入 `failureScopeAuthModel`，写入账号×模型冷却，由既有阶梯（60s×次数、上限 10 分钟）与冷却探活复用同一套恢复验证；`model_circuit.go` 由状态机降为记账薄（`noteModelBlockedHit` / `resetModelBlockedState`），独立熔断状态与 `model circuit open` 短路闸门全部删除。
  3. 探活覆盖模型维度：`cooldownProbeTargets` 同时挑账号级与账号×模型级冷却，同一账号多条记录按归一化键去重、只探最近到期那条；探活成功按候选来源精确清除对应维度（模型级成功不清账号级冷却）。
  4. 探活周期标记改用归一化键：生产日志证实同一账号以原始键与别名键成对存在时会被重复探测（间隔约 1 秒），修复后按 `normalizeFailoverKey` 统一标记。
  5. 探活调度参数：提前量 5s → 20s、粒度 5s → 1s。原组合下窗口宽度恰等于 tick 间隔，窗口内只落得进一个 tick，一旦被「上一轮未结束就跳过」吃掉，该冷却周期永不再被探测；现窗口内 20 个机会，并新增 `TestCooldownProbeIntervalFitsWindow` 防回退。

- 涉及文件: `failure_class.go` `model_circuit.go` `failover.go` `cooldown_probe.go` `executor.go` `stream.go` 及对应测试

## [0.1.32] - 2026-10-17

### Fixed
- **账号故障冷却由固定 15 秒改为 30s 递增阶梯、上限 10 分钟**：
  - 现状：`failoverCooldown` 对所有失败一律返回固定 15 秒，无法区分「偶发单次抖动」与「账号持续不可用」；持续失败的账号每 15 秒被重新拉回路由、再次失败，形成「快速轮转但始终打不通」的抖动，既浪费配额也放大上游压力。
  - 改造：`failover.go` 以 `failoverCooldownBase = 30s` / `failoverCooldownMax = 10min` / `failoverCooldownLadderCap = 20` 三常量替换原固定常量，新增 `failoverCooldownFor(count)` 按连续失败次数线性递增并在 10 分钟封顶；`noteAccountFailure` 改用该函数计算冷却窗口。偶发单次失败只冷 30 秒，持续失败逐步退到 10 分钟，一次成功即清零回落。阶梯时长按「先比较后相乘」计算，避免计数极大时整数溢出。
  - 验证：新增 `TestFailoverCooldownFor_Ladder`（含封顶档位断言）与 `TestNoteAccountFailure_LadderCooldown`；`cgo-shim-build.py zcode` build/vet/test 全绿。

## [0.1.31] - 2026-10-15

### Changed
- **面板额度改为逐模型独立进度条，并移除冗余信息**：
  - 现状：0.1.30 引入真实用量后，一个账号的多个模型（GLM-5.3 / GLM-5.3-Flash 等）仍共用**一条**汇总进度条，无法看出各模型各自的消耗；卡片还展示「过期」等与额度判断无关的行。
  - 改造：`quotaHTML` 改为按 `entitlements[]` **逐模型渲染独立进度条**——每个模型一条，标签显示「剩 X / Y」并按「剩余/授予」着色（≤30% 黄、≤10% 红）；回退通道（无真实用量）的模型单独展示「授予 X」并以半透明满格条区分，不再混入汇总口径。
  - 精简：卡片移除「过期」行（额度判断以进度条为准）；进度条下方 meta 收敛为「已用 / 剩余」合计 + 快照时间。
  - 回归：`test/zcode/panel_quota_render_repro.mjs` 同步更新——新增逐模型进度条数量、逐模型剩余/授予文案、耗尽模型转红、卡片不展示过期行等断言；并修正两处**既有失配**（0.1.28 默认授权站点已改中国区、0.1.30 汇总文案已改「今日授予合计」，脚本此前仍断言旧值）。

## [0.1.30] - 2026-10-15

### Fixed
- **面板额度永远显示满额（绿色进度条恒 100%），已用额度从不刷新**：
  - 现象：生产多个账号额度已被消耗，但面板始终显示「合计 8.00M」满格，进度条不随真实用量变化。
  - 根因：额度数据源 `GET /api/v1/zcode-plan/billing/current` **只返回授予量 `grant_units`，没有任何已用/剩余字段**；面板 `quotaHTML` 还硬编码 `width:100%`，导致「显示的是授予量、进度条恒满」。真实用量在 `GET /api/v1/zcode-plan/billing/balance?app_version=<v>`（客户端 `fetchZaiStartPlanBalanceEnvelope` 的通道），返回 `data.balances[]` 含 `total_units` / `used_units` / `remaining_units` / `available_units`。该端点**强制校验设备三头** `X-Platform` / `X-Device-Mid` / `X-ZCode-App-Version`，缺任一上游返回 `400 code 3001 parameter error`——这也是此前误判「balance 不可用、只能读 current」的原因。
  - 修复：`fetchAccountQuota` 改为**主走 balance**（携带设备三头 + `app_version`）取真实已用/剩余，失败时回退 current（仅授予量）；快照新增 `total_used` / `total_remain` / `has_usage` / `source` 与逐 entitlement 的 `used_units` / `remaining_units` / `available_units` / `has_usage`。面板进度条改为按「剩余/授予」渲染（剩余 ≤30% 转黄、≤10% 转红），卡片与汇总区同时展示「已用/剩余」；无用量通道时明确标注「每日授予额度（暂未取到用量）」并以半透明满格条区分，避免再被误读为满额。
  - 验证：生产真实账号实测 balance 返回真实用量（`zcode-d7a0cb4afa8f77c0` 的 GLM-5.3 `used_units=19` / `remaining_units=2999981`），修复后面板据此渲染；新增回归守卫 `TestFetchAccountQuota_ParsesBalanceUsage`（哨兵变异禁用用量解析后精确 FAIL）。

### Changed
- **「领取失败 no permission」翻译为友好中文提示**：
  - 根因：上游 `billing/preview` 当前对所有账号返回 `plans:[]`（未下发可领取计划），此时 claim 走 fallback planId 被上游业务层拒为英文 `no permission`；这属于**上游活动态**而非插件缺陷。
  - 修复：新增 `friendlyClaimError()` 把 `no permission` / `1003 already exists` / `1001 target not found` 翻译为可读中文（「当前上游未开放 Start Plan 领取…」等），未知错误保留原文便于排障；面板对「未开放」类提示降级为 warn 级别，不再一律显示为红色失败。

## [0.1.29] - 2026-10-15

### Fixed
- **面板无感验证未传 captcha 配置，导致产出的 `captchaVerifyParam` 在 captcha 层即被拒（领取始终提示需要安全验证）**：
  - 根因：`runTracelessVerification` 既未在 `initAliyunCaptcha` 入参传 `prefix` / `region`，又只设了错误的全局变量名 `window.AliyunRequestVerificationConfig`——阿里云 SDK 实际读取的是 `window.AliyunCaptchaConfig`，且**仅在脚本加载期读取一次**。结果 SDK 把验证请求打到 `undefined.captcha-open.aliyuncs.com`，产出 76 字符、不含 `securityToken` 的无效参数，带该参数重调 `/claim` 仍返回 `captcha_required`。
  - 修复：在 `initAliyunCaptcha` 入参显式传 `prefix` / `region`（与脚本加载时机无关的权威通道），并在加载 SDK 前预设 `window.AliyunCaptchaConfig` 作为双保险。
  - 验证：真实浏览器 A/B/C/D/E 五组受控实验锁定差异；修复后参数由 76 字符（无 `securityToken`）变为 280 字符（含 `securityToken`），生产 `/claim` 上游响应由 `captcha_required`（captcha 层）转为业务层响应，证明 captcha 校验已通过。

## [0.1.28] - 2026-10-14

### Added
- **面板内直接领取 Start Plan 每日额度（阿里云无感验证），无需再打开 ZCode 桌面客户端**：
  - 背景：免费 Start Plan 的每日 800 万额度需经官方 `POST /api/v1/zcode-plan/billing/claim` 领取，该接口强制校验阿里云人机验证头；此前插件端只能显示「未领取」并引导用户去桌面客户端手动领取。
  - 逆向依据（`app.asar` 3.15.1）：官方客户端用阿里云**无感验证**（`AliyunCaptcha.js` → `initAliyunCaptcha`（必须同时传 `element` 与 `button`）→ `inst.startTracelessVerification()`）产出 `captchaVerifyParam`，随请求头 `X-Aliyun-Captcha-Verify-Param` 提交；`planId` 由 `getManualClaimPlanPreviews()` 从 `/billing/preview` **动态解析**（带批次后缀，实测 `zcode-v3-start-plan-0817`）。
  - 面板新增「🎁 领取额度」：先无参数调 `/claim` 探明验证码需求 → 浏览器内跑无感验证产出参数 → 带参数重调 `/claim` 完成领取；验证参数为一次性凭证，每次领取重新执行无感验证。
  - 兜底保留原「📖 领取指引」弹窗（SDK 不可达或上游活动下线时仍可走官方客户端路径）。

### Fixed
- **claim 请求 `plan_id` 硬编码无后缀值导致上游返回 `code 1001 target not found`**：
  - 根因：`defaultStartPlanID` 原为 `zcode-v3-start-plan`，而官方 preview 下发的是带批次后缀的 `zcode-v3-start-plan-0817`。
  - 修复：新增 `fetchClaimablePlanID()` 从官方 `/billing/preview` 动态解析 planId，解析失败时回退修正后的默认值 `zcode-v3-start-plan-0817`。
- **claim / preview 请求缺失 `X-Device-Mid` 设备指纹头导致上游返回 `code 3001 parameter error`**：
  - 修复：新增 `zcodeDeviceMid()`（进程内稳定 UUID v4）并注入两个端点的请求头。

### Changed
- **OAuth 授权站点默认改为中国区（bigmodel）**：
  - 面板下拉框默认选中「中国区（bigmodel.cn）」并置于首位，回退分支同步调整。
  - 宿主入口 `resolveOAuthProvider` / `providerFromMetadata` 的空值/缺失默认值由 `zai` 改为 `bigmodel`；显式传 `zai` 仍按国际站处理。
  - 测试 `TestResolveOAuthProvider` / `TestProviderFromMetadata` 断言同步更新，新增 `zai` 显式传参用例；哨兵变异精确 FAIL。

## [0.1.27] - 2026-10-14

### Fixed
- **bigmodel（中国区）账号推理稳定 401「令牌已过期或验证不正确」，根因是推理端点误用 `open.bigmodel.cn`**：
  - 现象：CYCLE-3 真实 bigmodel 账号授权成功、额度可查（800 万/日）、模型清单可拉，但推理 3/3 稳定 401。
  - 根因：0.1.25 为 bigmodel 选择的 `open.bigmodel.cn/api/anthropic` **不接受 ZCode OAuth JWT**——实测该端点对 bigmodel 与 zai **两类账号的 JWT 都返回 401**；同一 bigmodel JWT 打 `zcode.z.ai/api/v1/zcode-plan/anthropic` 则**鉴权通过**（同一 IP 对照：无效 JWT → 401、有效 JWT → 405/3012 或 429/3009，证明鉴权先于风控生效）。
  - 客户端逆向佐证（`app.asar` 3.15.1）：`open.bigmodel.cn` 仅服务 `bigmodelIndividualCodingPlan`（**付费个人版**）；`bigmodelStartPlan`（**免费版**，即 OAuth 授权得到的形态）走默认端点解析，且 `resolveCurrent` 中 start-plan **不分 family**，统一使用 `zcodeJwtToken`。
  - 修复：`resolveUpstreamTargets` 移除 bigmodel 专用端点分支（删除 `bigmodelCodingPlanBaseURL` 常量），start-plan 形态下中国站与国际站**共用** `zcode.z.ai/api/v1/zcode-plan/anthropic`。
  - `provider_family` 字段保留（面板归属展示用），不再影响 start-plan 端点选择。
  - 测试：`TestResolveUpstreamTargets_ByProviderFamily` 断言改为「bigmodel 走 zcode.z.ai 且不得含 open.bigmodel.cn」；哨兵变异（改回 open.bigmodel.cn）精确 FAIL。

## [0.1.26] - 2026-10-14

### Fixed
- **非法 provider 在 HTTP 入口被静默回退为 `zai`，校验形同虚设**：
  - 现象：`POST /activate-start` 传入 `provider=zhipu` 等非法值时未报错，而是返回国际站 `chat.z.ai` 授权地址（违反 AC-006「未知 provider 返回明确错误，不静默回退」）。
  - 根因：入口层先调用 `normalizeProviderFamily()`（语义为「非法值回退 zai」，本用于存量数据读取）再做校验，非法值被提前改写成 `zai`，下游 `isValidOAuthProvider()` 永远拿不到原始值。
  - 修复：新增 `resolveOAuthProvider(raw)` 统一入口解析——**先校验、后归一化**，空值仍默认 `zai`（向后兼容）；`handleActivateStart` 非法值返回 **400**，`providerFromMetadata` 改为原样透传原始取值，`startAccountOAuth` 改走统一解析。
  - 新增用例：`TestResolveOAuthProvider`、`TestHandleActivateStart_RejectsUnknownProvider`（HTTP 层 400 且不触达上游）、`TestHandleActivateStart_AcceptsBigmodelProvider`；`TestProviderFromMetadata` 增补非法值透传断言。哨兵反证 3 例精确 FAIL。
  - 影响面：面板下拉仅 `zai` / `bigmodel` 两项，用户路径不可达；缺陷影响直接调管理 API 的集成方。

## [0.1.25] - 2026-10-14

### Added
- **支持 bigmodel（中国区）OAuth 授权，与国际站 `zai` 双 provider 共存**：
  - OAuth 授权链路 `provider` 参数化：`startAccountOAuth` / `zaiCLIInit` / `settleZaiPollResult` / `saveZaiAccount` 全链接受 `provider`（`zai` / `bigmodel`），未知取值返回明确错误，不静默回退。
  - `StoredAuth` 新增 `provider_family` 字段（缺省视为 `zai`，向后兼容）；`normalizeProviderFamily` / `providerFamilyOf` 统一归一化。
  - 上游端点按 family 分支：`resolveUpstreamTargets` 在 bigmodel 下走 `open.bigmodel.cn/api/anthropic`；业务令牌端点 `businessLoginEndpointFor` 按 family 隔离并分键缓存。
  - 面板工具栏新增 provider 下拉（国际站 / 中国区，默认 `zai`），`openOAuthBridge()` 透传选中值；账号卡片新增归属徽标 `providerBadge(a)`，后端 `accountSummary` 透出 `provider_family`。
  - 新增 `provider_family_test.go`（8 例）、`upstream_target_test.go`（6 例）与 `TestHandleListAccounts_ExposesProviderFamily`。
- 修正面板回归脚本阶段 1「无额度卡片」断言长期失配（实现已改为「未领取」提示，断言仍停在旧措辞）。

## [0.1.24] - 2026-10-14

### Changed
- **定时探活降频至 4 小时并修正失败账号节流缺陷**：
  - `defaultActivePingInterval` 由 30 分钟放宽至 4 小时：真实推理与探活共享账号调用配额，探活只服务 `test_failed` 标签的健康判定，30 分钟一轮过于频繁。
  - `recordActivePing` 由「仅成功时刷新」改为「无论成败都刷新」。此前失败账号不计时，会在每个 watchdog tick（10 分钟）被反复全量重测，反而比健康账号更频繁地消耗账号配额。
  - 新增 `TestDoActivePingFailureStillThrottles` 回归用例：断言失败后立即再次调用被节流拦截、不再发起模型请求。

## [0.1.23] - 2026-10-14

### Fixed
- **推理路径改用无整体超时的 HTTP 客户端，根治长思考输出被掐断**：
  - 现象：glm-5.3 推理偶发失败，生产日志 `err=context deadline exceeded (Client.Timeout ... while reading body)`、`duration=14.216s`，同账号同分钟前序请求 10.8s / 5.6s / 6.2s 均成功。
  - 根因：`modelHTTPClient` 的 15 秒整体超时（`http.Client.Timeout` 覆盖连接 + 重定向 + 读取响应体全流程）被流式 / 非流式 / 探活三处推理路径复用，思考链稍长即在读响应体时被强制中断。
  - 修复：新增 `inferenceHTTPClient`——不设整体超时，仅按操作 deadline（Dial 10s / TLS 握手 10s / 响应头等待 90s），替换三处推理调用；`modelHTTPClient` 保留 15 秒，仅服务模型列表、令牌换取、额度查询等元数据短请求。
  - 新增 `inference_client_test.go`：守「推理客户端整体超时不得回归」与「慢响应体须完整读完」两条契约。

## [0.1.22] - 2026-10-14

### Fixed
- **纠正「激活开通」错误承诺，改为如实的 Start Plan 领取指引**：
  - 根因实证：官方每日 800 万 Start Plan 并非授权登录时自动下发，而是由 ZCode 客户端内的服务端活动弹窗引导用户**手动领取**（`POST /api/v1/zcode-plan/billing/claim`），且官方对该接口强制校验阿里云人机验证头（`X-Aliyun-Captcha-Verify-Param`，服务端下发 `captcha.enabled=true`，sceneId `11xygtvd`）——无头、伪造验证头、换 token 一律 `400 3007 captcha verify failed`；网页端与插件端均无领取入口。
  - 账号卡片未领取态文案由「未激活」改为「未领取」，按钮由「⚡ 激活开通」改为「📖 领取指引」，点击弹出官方领取步骤说明（下载客户端 → 同账号登录 → 客户端内领取 → 回面板刷新额度）。
  - 移除失效的「点击按钮即下发额度」交互与其对应文案；OAuth 授权登录弹窗、成功提示统一改为「授权登录成功」语义，明确授权只完成账号绑定、额度需在客户端领取。
  - 后台 `handleActivatePoll` 成功文案由「授权激活成功」改为「授权登录成功」，并同步函数注释。

## [0.1.21] - 2026-10-13

### Added
- **新增定时探活（全量模型按序测试）**：
  - 引入 30 分钟节流（`shouldActivePing` / `recordActivePing` / `resetActivePingTimes`）与 20s 单请求超时，在额度刷新成功后对账号执行一轮探活；
  - 按该账号全部已配置模型顺序依次探活，**任一模型成功即判定健康并立即终止本轮**，仅当全部失败才写入 `test_failed` 标签；
- **max_tokens 统一 32768 最低输出底线**：
  - 新增 `minMaxTokensFloor=32768` 常量与 `applyMaxTokensFloor` 纯函数，`computeAdaptiveMaxTokens` 四个出口全部经其收敛；
  - `models.go` 的 `MaxCompletionTokens` 广播一并保底；探活负载 `max_tokens` 同样抬升至底线。

### Changed
- **官方规格矩阵全扩**：`model_specs.go` 规格表由 14 条扩至全通用家族（覆盖 GLM 4.5/4.6/4.7 全系、Claude、GPT、Gemini、DeepSeek、Qwen 等），匹配逻辑改为最长前缀优先并支持 `vendor/` 前缀剥离。

## [0.1.20] - 2026-10-13

### Added
- **激活弹窗新增官方授权地址复制区**：
  - 在「⚡ 激活开通」引导弹窗中展示完整可复制的官方授权地址（只读输入框 + 「复制链接」按钮）；
  - 当浏览器拦截弹窗或需要跨窗口 / 跨设备打开时，可直接复制该地址到其它窗口完成授权，插件后台轮询仍会自动捕获结果并落盘额度；
  - 复制优先使用 `navigator.clipboard`，在非安全上下文回退到 `execCommand('copy')` 兜底。

## [0.1.19] - 2026-10-13

### Fixed
- **修复官方授权激活链接 XML 实体转义缺陷（彻底解决弹窗报 422 缺少 query 参数）**：
  - 根因锁定：上游 `/api/v1/oauth/cli/init` 返回的 `authorize_url` 包含 HTML 转义实体 `&amp;`，浏览器解析为 `amp;redirect_uri` 等错误参数名，导致 `chat.z.ai` 判定必填字段 `response_type`、`redirect_uri`、`state` 缺失抛出 422；
  - 后端在 `zaiCLIInit` 与 `startAccountOAuth` 中增加 `html.UnescapeString` 与 `&amp;` 强力清理，并在前端控制面板二次做防御性反转义；
  - 授权窗口能够秒级正常加载官方授权页，浏览器已登录时点击一次即可自动完成闭环并回调，无感更新 800 万额度。

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
