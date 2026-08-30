# TraeWork Plugin Changelog

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
