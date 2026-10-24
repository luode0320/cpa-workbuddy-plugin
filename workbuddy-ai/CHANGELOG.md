# WorkBuddy AI Plugin Changelog

## 0.1.24

### Feature - 对齐国内版：连续使用轮换、失败即写盘禁用 + 探活解禁

- 背景: 国际版此前只有「冷却」一层，缺少国内版已上线的两项能力：单账号长期承流无让位机制、账号级失败只冷却不落盘（宿主重启后失效）。本次按国内版语义补齐，积分分级体系按产品裁定不同步。
- 连续使用轮换（新增 `usage_rotation.go`）: 单个账号**连续**承流满 `rotation_serve_minutes`（默认 60）后主动让位，进入 `rotation_cooldown_minutes`（默认 60）冷却，后续请求改由其它账号承接。计时取「连续使用」——距上次使用超过 `rotation_idle_reset_minutes`（默认 10）即视为新一轮。因换号发生在插件入口而非依赖宿主 selector，**已绑定会话同样生效**。防抽干：冷却前若除本号外池内已全部冷却，先按冷却结束时间放开一半（向上取整）。
- 轮换接线覆盖: 调度候选过滤、同请求换号候选（`pickNextAuthForModel`）、执行入口前置拦截（非流式与流式各一处）、成功路径打点（非流式 / 同步流式 / 异步流式三条）。
- 失败即写盘禁用（新增 `failover_disable.go`）: 账号级失败除写内存冷却阶梯外，同时把 `disabled:true` + `failover_disable` 标记写入物理 auth 文档；每 10 分钟串行遍历被本机制禁用的账号，回放**失败当时的模型**探活，成功即解禁。只认自己写的标记，manual / exhausted 一律不接管。
- 顺带修复三个既有缺口（迁移测试暴露）:
  1. **后台刷新会静默解封禁用号**：原 `toAuthData(sa)` 固定回 `disabled:false`，禁用中的号被一次 token 刷新绕开探活直接解封。改为沿用盘上禁用状态并搬运标记。
  2. **7 个白名单重建调用点会抹掉 failover 标记**：`buildAuthFileJSON` 不搬运标记会让探活循环认不出被禁用的号（静默遗忘）。已补齐 `credits_handler`、`lifecycle`（4 处）、`management`。
  3. **失败记账未触发写盘**：`recordFailureScoped` 原先只写内存冷却，现拆出 `recordCooldownScoped` 作为纯内存写入，外层补上写盘调用。
- 配置项（4 个，全部可在面板配置）: `rotation_enabled`（默认 true）、`rotation_serve_minutes`（60）、`rotation_cooldown_minutes`（60）、`rotation_idle_reset_minutes`（10）。`serve` 或 `cooldown` 填 0 等同关闭。
- 测试护栏: 新增 `failover_disable_test.go`（含「失败记账必须触发写盘」接线用例，已做变异验证确认能捕获断线）与轮换相关用例。
- 涉及文件: 新增 `usage_rotation.go`、`failover_disable.go`、`failover_disable_test.go`；修改 `main.go`、`scheduler.go`、`stream.go`、`failover_retry.go`、`accountFailover.go`、`lifecycle.go`、`oauth.go`、`management.go`、`credits_handler.go`、`usage_config.go`。

## 0.1.23

### Fix - 客户端标识对齐真实客户端：UA 改为 `WorkBuddy/<版本>`、按日同步版本号、补 X-IDE-Version

- 背景: 插件长期发送 `User-Agent: CLI/2.63.2 WorkBuddy/2.63.2`。从官方 CLI 产物反查确认上游真实 UA 形态是 `${X-IDE-Name}/${版本}`，双产品名前缀的写法在当前 bundle 中已不存在；且 `2.63.2` 停留在 2026-03-17，落后当时最新版 181 个稳定版。
- UA: 改为 `WorkBuddy/<产品版本>`，品牌名与 `X-IDE-Name` 复用同一常量 `ideNameHeaderValue`。
- 版本来源: 官方 COS `releases/latest` 每 24 小时拉取（24h + ≤30min 抖动），npm registry 备用；失败保留当前值，兜底编译期常量 `builtinClientVersion`。
- 安全边界: 只升不降，只接受三段纯数字，拒绝预发布后缀与 `v` 前缀。
- X-IDE-Version: 补上并固定 `0.0.0`（该字段是宿主 IDE 版本语义，独立客户端无宿主 IDE；与 UA 产品版本号是两套语义，有专项不变量测试守护）。
- 配置项: 新增 `client_version`，留空每日自动更新，填写则钉死。
- 覆盖点: `commonHeaders`、`backendHeaders`、模型发现链路三处头部构造。
- 涉及文件: `main.go`、`models.go`、`usage_config.go`、新增 `client_version.go` 与 `client_version_test.go`。

## 0.1.22

### Fix - 补齐会话追踪头，修复国际版部分账号 11128

- 变更要点:
  1. 现象：国际版部分账号聊天返回 `HTTP 400 / code 11128`（request illegal，未通过渠道校验），同一出口与同一模型下只有部分账号持续失败。
  2. 根因：`backendHeaders` 缺少上游渠道校验所要求的会话追踪头，请求被判为非法来源。
  3. 修复：在 `backendHeaders` 统一补齐 `X-Conversation-ID`、`X-Conversation-Request-ID`、`X-Conversation-Message-ID`、`X-Root-Request-ID` 四个头；用标准库生成 UUID v4，根请求 ID 与当前请求 ID 关联。普通聊天、流式、换号重试与主动探活共用该入口，不新增依赖，不改 UA / 模型 / 提示词 / 账号配置。
  4. 边界说明：本修复针对已复现的渠道校验失败，不把所有 11128 归为同一原因（账号级 11128 仍走既有账号×模型冷却）。
  5. 新增回归用例校验四个头存在、UUID v4 格式与跨请求唯一性。
- 涉及文件: `main.go` `conversation_headers_test.go` `VERSION`

## 0.1.21

### Fix - 11128 模型级拦截并入账号×模型冷却，移除独立熔断

- 变更要点:
  1. 根因：`classifyUpstreamFailure(400, 11128)` 返回 `failureScopeNone`——11128 从未进入冷却体系。旧实现另建独立熔断状态机（跨 3 账号命中 → 打开 60 秒闸门），但该闸门只拒绝请求、**不改变路由**，宿主仍把请求派给已被拦截的账号×模型，且逐条在本地失败（生产实测 duration 55–160ms），形成「一整片账号全失败」的记录。
  2. 改造：11128 归入 `failureScopeAuthModel`，写入账号×模型冷却，由既有阶梯（60s×次数、上限 10 分钟）与冷却探活复用同一套恢复验证；`model_circuit.go` 由状态机降为记账薄（`noteModelBlockedHit` / `resetModelBlockedState`），独立熔断状态与 `model circuit open` 短路闸门全部删除。
  3. 探活覆盖模型维度：`cooldownProbeTargets` 同时挑账号级与账号×模型级冷却，同一账号多条记录按归一化键去重、只探最近到期那条；探活成功按候选来源精确清除对应维度（模型级成功不清账号级冷却）。
  4. 探活周期标记改用归一化键：生产日志证实同一账号以原始键与别名键成对存在时会被重复探测（间隔约 1 秒），修复后按 `normalizeFailoverKey` 统一标记。
  5. 探活调度参数：提前量 5s → 20s、粒度 5s → 1s。原组合下窗口宽度恰等于 tick 间隔，窗口内只落得进一个 tick，一旦被「上一轮未结束就跳过」吃掉，该冷却周期永不再被探测；现窗口内 20 个机会，并新增 `TestCooldownProbeIntervalFitsWindow` 防回退。

- 涉及文件: `failure_class.go` `model_circuit.go` `accountFailover.go` `cooldown_probe.go` `main.go` `stream.go` 及对应测试

## 0.1.19

### Fix - 正文已泄漏的断流改为 length 收尾（new-api 可恢复计费）

- 变更要点:
  1. 根因：0.1.18 只解决了「仅思考分片时换号」，但**正文已发出后断流**仍走 `streamEmitError` 发 `event: error`。宿主 `forwardResponsesStream` 对非 Codex 客户端只输出 `event: error`（`writeTerminalError`），而 new-api 的 `OaiResponsesStreamHandler` 只从 `response.completed / incomplete / done / failed / cancelled` 提取 usage；`event: error` 不在其列 → 走正文估算兜底，但正文为空时 `CompletionTokens=0` → 打出「上游没有返回计费信息，无法扣费」并 `$0`。
  2. 收尾策略改为合成终止分片：正文已泄漏且无法换号时，不再发 error，改为 emit 一个 `finish_reason=length` 的空 delta 分片并正常关流。宿主补 `[DONE]` 后，转译层 `incompleteByFinishReason` 把 `length` 映射为 `response.incomplete`（含 `incomplete_details.reason=max_output_tokens`），该事件属终态（`responsesSSETerminalEvent`）不会触发 502 兜底。
  3. 计费恢复链路：new-api 收到 `response.incomplete` 后从终态事件提 usage；若无 usage 则回退到 `response.output_text.delta` 累计正文估算（`relay_responses.go:146`），正文已泄漏场景可正常出 token。
  4. 语义边界：**只有正文已泄漏才合成 length**；零泄漏或仅思考分片仍走换号，换号失败仍透传 error（保持可观测性，不把真实失败伪装成成功截断）。
  5. 同步路径（`StreamID == ""`）不改：宿主 `rpcPluginAdapter.ExecuteStream` 恒填 `StreamID`（`rpc_client_stream.go:16`），该分支仅为防御性 fallback；其调用方在 `err == nil` 时把请求记为成功，若在此合成终止分片会让截断在 dashboard 上伪装成成功。
- 验证: 新增 `TestBuildStreamTerminalChunk`（校验 object/model/finish_reason=length/空 delta/不被判为内容泄漏）与 `TestBuildStreamTerminalChunkFraming`（SSE 前缀差异）；两次变异反证精确命中（`finish_reason` 改 `stop` → 1 例 FAIL；`delta` 改非空 → 1 例 FAIL）；cgo-shim build/vet/test 全绿。
- 已知代价: 客户端会看到「成功但内容被截断」的 incomplete 状态，而非显式错误；这是保留已生成内容的必然取舍，日志侧仍有 `publishUsage(failed=true)` 记录可追溯。
- 依据: 用户裁定「traework 式收尾作为正文已泄漏分支的补充」；宿主与 new-api 链路已逐行核验（`responsesSSETerminalEvent`、`buildResponsesCompletedEvent`、`relay_responses.go:91-153`）。
- 涉及文件: workbuddy-ai/policy.go、workbuddy-ai/stream.go、workbuddy-ai/stream_read_error_failover_test.go、workbuddy-ai/main.go、workbuddy-ai/VERSION

## 0.1.18

### Fix - 断流分级换号（补齐 0.16.5 同源修复）+ 断流不再误伤账号冷却

- 变更要点:
  1. 根因：本插件此前**完全没有断流换号路径**——`pumpUpstreamStream` 的 `scanner.Err()` 分支只记录失败并透传错误，同步路径同样直接返回；连零泄漏场景也不会换号。同时错误信封无 `http_status`，宿主无法据此跨 provider 轮转。
  2. 补齐断流识别与分级换号：新增 `isStreamReadError` / `shouldRotateOnStreamReadError` / `streamChunkHasContent` / `streamChunksHaveContent`，异步与同步两条路径均在「正文零泄漏」时换号续试。
  3. 细化泄漏语义：仅思考分片已下发时打 `reasoning only, no content` 标记并允许换号；正文已发出时打 `after partial output`，抑制换号与状态码。避免 qwen-3.8-flash 在 `reasoning_effort=max` 下思考阶段断流被误判为不可重试。
  4. 信封补 502：新增 `envelopeHTTPStatusForError`，断流时携带 `http_status=502`，让宿主识别为网关失败并可跨 provider 轮转。
  5. 断流不再对账号记账：断流是上游网络/服务抖动，与账号凭据健康度无关，移除 `noteAccountFailure(curAuthID, 0, ...)`，避免健康账号被阶梯冷却。
- 验证: 新增 `TestIsStreamReadError`（8 例）、`TestShouldRotateOnStreamReadError`（10 例，含「仅思考分片仍可换号」「正文已泄漏不换号」）、`TestStreamChunkHasContent`（11 例）；变异反证（守卫退回 `emitted`）精确命中 1 例；cgo-shim build/vet/test 全绿。
- 已知代价: 思考阶段换号会让客户端看到两段思考摘要（宿主 ABI 无清空已发内容通道）。

## 0.1.17

### Fix - 账号故障冷却改为 30s 递增阶梯、上限 10 分钟

- 变更要点:
  1. 根因：`failoverCooldownFor` 此前对所有失败一律返回固定 15 秒，无法区分「偶发单次抖动」与「账号持续不可用」。连续失败时账号每 15 秒就被重新拉回路由、再次失败，形成「快速轮转但始终打不通」的抖动，既浪费配额也放大上游压力。
  2. 冷却窗口改为随连续失败次数线性递增：`30s × 连续失败次数`，在 10 分钟封顶（`failoverCooldownLadderCap = 10min / 30s = 20` 档）。偶发单次失败只冷 30 秒，持续失败的账号逐步退到 10 分钟，一次成功即清零回落。
  3. 软失败与硬失败共用同一计数阶梯：429 无额度标记、软限流文案、零字节断流三类瞬时限流形态，此前只做固定冷却、不推进连续失败计数；现统一计入阶梯，与硬失败口径一致。
  4. 阶梯时长按「先比较后相乘」计算，避免计数极大时整数溢出。
  5. 回归用例补强：`TestFailoverCooldownFor_Ladder`（含封顶档位）、`TestRecordAccountFailure_LadderCooldown`、`TestRecordAccountFailure_LadderCapsAtMax`。
- 边界说明: 业务类 4xx（400 等）仍不计入阶梯。
- 涉及文件: workbuddy-ai/accountFailover.go、workbuddy-ai/accountFailover_test.go、workbuddy-ai/policy.go、workbuddy-ai/stream.go、workbuddy-ai/keepalive.go、workbuddy-ai/retry_config.go、workbuddy-ai/main.go、workbuddy-ai/VERSION

## 0.1.16

### Feat - 国际版 OAuth 授权后自动补注册地，修复新账号无可用额度

- 变更要点:
  1. 修复根因：插件以 `platform=CLI` 调 `auth/state`，官方登录页据此主动跳过注册地选择页，新账号以 `areaInfoComplete=false` 落盘，云账号未注册、无额度池（面板表现为 plan 为空、可用积分 0）。
  2. 新增 `registration.go` 补注册链路：查 `areaInfoComplete` → 按出口 IP 推导注册地（`CN` 映射为 `HK`）→ 取国家完整条目 → 提交注册地 → 注册云账号 → 尽力领试用包，全流程 Bearer 鉴权，无需官方客户端或浏览器会话。
  3. 授权钩子接入：`handlePollLogin` 在返回凭证前调用 `ensureAccountRegistered`，已注册账号零额外调用直接跳过；补注册失败只记录状态、不阻断授权落盘（授权本身已成功）。
  4. 面板状态透出：`wbAccount` 新增 `registered` 字段，无 plan 时才探测注册态并回写缓存；新增「未注册」徽标与「补注册」按钮，对应管理路由 `POST /register`。
  5. 新增 `registration_test.go` 六个用例（嵌套 JSON 解包、CN→HK 映射、已注册跳过、完整链路、提交失败、UID 缺失），并完成 CN→HK 哨兵反证。
- 涉及文件: workbuddy-ai/registration.go、workbuddy-ai/registration_test.go、workbuddy-ai/oauth.go、workbuddy-ai/management.go、workbuddy-ai/credits_handler.go、workbuddy-ai/panel.go、workbuddy-ai/panel.html、workbuddy-ai/cache.go

## 0.1.15

### Chore - 定时探活降频至 4 小时并修正失败账号节流缺陷

- 变更要点:
  1. `defaultActivePingInterval` 由 30 分钟放宽至 4 小时：真实推理与探活共享账号调用配额，探活只服务 `test_failed` 标签的健康判定，30 分钟一轮过于频繁。
  2. 修正失败账号节流缺陷：`recordActivePing` 由「仅成功时刷新」改为「无论成败都刷新」。此前失败账号不计时，会在每个 watchdog tick（10 分钟）被反复全量重测，反而比健康账号更频繁地消耗账号配额。
  3. 新增 `TestDoActivePingFailureStillThrottles` 回归用例：断言失败后立即再次调用被节流拦截、不再发起模型请求。
- 涉及文件: workbuddy-ai/active_ping.go、workbuddy-ai/active_ping_test.go

## 0.1.14

### Feat - max_tokens 统一 32768 最低输出底线 + 定时探活改全量模型按序测试

- 变更要点:
  1. 设立 `minMaxTokensFloor=32768` 统一最低输出底线：规格表兜底 `ReasoningMax` 由 8192 对齐至 32768；`computeAdaptiveMaxTokens` 四个出口全部经 `applyMaxTokensFloor` 收敛，杜绝任何路径回落到 32768 以下的过小输出上限。
  2. 广播层同步：`models.go` 的静态模型列表与 `maxOutputTokens()` 输出的 `MaxCompletionTokens` 一并保底。
  3. 定时探活改为全量模型按序测试：废除「随机取 ≤5 个」与整轮预算熔断，`doActivePing` 取该账号全部候选模型依次测试；任一成功即判定健康并立即终止本轮（清 `test_failed`），仅当全部失败才打标签。
  4. 探活负载 `max_tokens` 经 `applyMaxTokensFloor` 抬升。
  5. 新增 `TestDoActivePingAllModelsBeyondLegacyCap` 回归用例。
- 涉及文件: workbuddy-ai/model_specs.go、workbuddy-ai/models.go、workbuddy-ai/active_ping.go、workbuddy-ai/*_test.go

## 0.1.13

### Fix - 修复官方规格自适应 max_tokens 未随 0.1.12 二进制发布（内嵌 vcs.revision 取证）

- 变更要点:
  1. 根因：0.1.12 发布 tag (`workbuddy-ai-provider-v0.1.12`) 指向 commit `5f12b41`，早于自适应引擎提交；构建产物内嵌 `vcs.revision=5f12b41`，二进制符号探针中 `computeAdaptiveMaxTokens` / `model_specs.go` 全部 MISS——线上运行的是旧的写死 128k 实现。
  2. 本版重新打包发布，使 `model_specs.go` 官方规格矩阵与 `computeAdaptiveMaxTokens` 真正进入线上二进制：未传补齐官方最大值，推理模型在 <= 8192 时放宽至 ReasoningMax，超限截断。
  3. 验证方式：构建产物内嵌 `vcs.revision` 必须指向本版提交，且二进制符号探针须命中 `computeAdaptiveMaxTokens`。

## 0.1.12

### Feat & Fix - 设立安全缓冲额度红线 (minRoutableCredits=20)、健康平摊轮询与官方规格自适应 max_tokens

- 变更要点:
  1. 设立安全缓冲额度红线 (`min_routable_credits`，默认 20，可配置)：剩余额度 `< 20` 的账号视为低额保护态，不再分配新推理流量。
  2. 调度策略逆转：废除残血优先（Low-Credit-First）升序排序，在 `scheduler.pick` 初筛中硬排除低额与耗尽账号，存活健康候选平摊轮询（Round-Robin）。
  3. 请求内换号链过滤：`pickNextAuth` 跳过 `remain < min_routable_credits` 账号；`hardCreditMarkers` 补齐 `exceeded the quota` 与 `14018` 触发换号重试。
  4. 按模型官方规格自适应计算 `max_tokens`：引入 `model_specs.go`，未传补齐官方最大值，推理模型在 <= 8192 时放宽至 ReasoningMax，超限截断，根治推理模型 token 耗尽问题。
  5. 面板状态与视觉对齐：新增 `low_credits` 字段与「低额保护」黄色徽标展示。

## 0.1.11

### Feat - 补齐 checkin_auto 配置声明与全套路由与基础设施回归单测

- 变更要点:
  1. 补齐 `main.go` 中 `ConfigFields` 的 `checkin_auto` 配置字段声明，打通宿主管理界面对国际版自动签到调度器的可视化配置。
  2. 全面补充 11 套核心单测资产（涵盖会话粘性路由、低积分优先调度、全灭 defer、计数持久化、Token 保活、测试标签与看门狗）。
- 涉及文件: workbuddy-ai/main.go、workbuddy-ai/VERSION、workbuddy-ai/*_test.go
