# TraeWork Plugin Changelog

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
