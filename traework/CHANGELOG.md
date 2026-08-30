# TraeWork Plugin Changelog

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
