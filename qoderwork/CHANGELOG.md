# QoderWork Plugin Changelog

## 0.9.4

### Feat — 账号面板异步节流刷新（与 workbuddy-provider 0.14.9 对称）

- **后端**（`refresh_runner.go` 新增 + `refresh_runner_test.go`）：`RefreshRunner` 单例，1s/账号节流 + `pending/running/done/failed` 状态机。
- **路由**（`management.go` / `credits_handler.go`）：`POST /refresh` 改异步立即返回、新增 `GET /refresh/status`、`GET /credits?track=1` 走队列。
- **幂等**：`EnqueueAll` / `EnqueueOne` 运行中则忽略，多路触发只跑一轮。
- **前端**（`panel.html`）：`pollRefreshStatus` 2s 轮询 + 卡片三态。
- qoderwork 无 preserve watchdog，该步仅 workbuddy-provider 具备。

## 0.9.1

### Fix — `retry_on_4xx` 同请求切号循环纳入 429（Too Many Requests）

与 workbuddy-provider 0.14.2 对称：0.9.0 设计的同请求切号循环只 cover
账号级 4xx（401/403/404/405），429 / 402 / 5xx / 状态 0 全部强制走
cooldown 跨请求路径。`isAccountLevel4xx` 显式纳入
`http.StatusTooManyRequests`，让上游软限流（按账号/租户维度分配）
也可通过切下一个候选账号在**同一个请求**内恢复。cooldown 阶梯
（1/3/10 分钟）继续作用于失败账号，与 retry 循环并存。

- `accountFailover.go`：`isAccountLevel4xx` 加 `http.StatusTooManyRequests`
  case（与 workbuddy-provider 0.14.2 同源）。
- `retry_config.go`：文件头注释新增 429 说明。
- `main.go` (handleExecExecute) 循环注释更新为"401/403/404/405 或
  429"，与代码同步；行为由 `isAccountLevel4xx` 集中控制，调用点不动。
- `accountFailover_test.go`：`TestIsAccountLevel4xx_Classification` 中
  429 由 false 改为 true（与 workbuddy-provider 对称）。
- 涉及文件：`accountFailover.go` / `accountFailover_test.go` /
  `retry_config.go` / `main.go`。
- 不在范围：429 配额感知（上游共享限流时的快速失败信号）；429 → 切换但
  不计 cooldown 的开关（默认行为下 cooldown 一定累计）。

## 0.9.0

### Feature — 异常池（anomaly pool）：连续失败的账号永久冻结 + 每日刷新

新增"异常池"机制：当账号连续触发账号级 4xx（401/403/404/405）、5xx、
429 软限流、402 硬积分或传输错误达 N 次（默认 10，可通过
`anomaly_pool_threshold` 配置，范围 1-50），自动移入异常池、不再被路由
层选到；面板显示"异常"过滤区和单账号/全量"解除冻结"按钮；每日本地 0 点
自动刷新全池（可通过 `anomaly_refresh_enabled: false` 关闭）。

- 新增 `anomaly.go`：内存 `anomalySet` + 物理 auth JSON 顶层布尔
  `anomaly: true` 双镜像（qoderwork 没有 preserve/watchdog 但保留一致的
  顶层字段语义）；`isAnomaly` / `anomalySetPut` / `anomalySetClear` /
  `persistAnomalyToggle` / `refreshAnomalySetFromDisk` /
  `clearAllAnomalies`；`freezeAccountForAnomaly` 在
  `recordAccountFailure` 内 `count >= threshold` 时异步触发。
- `anomaly.go` 内自带 `writeAnomalyFileDirect`（qoderwork 原本只走
  `host.auth.save`，新增直写 helper 是为了 host rebuild 时不丢顶层字段）。
- `anomaly_config.go`：阈值常量与 `clampAnomalyThreshold` / 解析器。
- 整文件同步 `accountFailover.go`（与 workbuddy-provider 同源，保留
  字节级一致以便后续跨插件发版同步）。
- `scheduler.go` 过滤链：`disabled → anomaly → cooldown`；新增
  `isAccountAnomaly` 函数。
- `failover_retry.go`：`pickNextAuth` 加 `isAccountAnomaly` 跳过。
- `active_auth.go`：`pickActiveAuth` / `ensureDefaultActiveAuth` 加
  `isAccountAnomaly` 跳过。
- `usage_config.go`：`configure()` 仿 retry_on_4xx 的 Seen 模式增加
  `anomaly_pool_threshold` / `anomaly_refresh_enabled` 解析。
- `management.go`：新增 `POST /unfreeze` 端点（与 workbuddy 同款）：
  body 含 `auth_index` 则清单个；空 body 则清全部。
- `main.go` ConfigFields 注册两个新配置键；`version` 0.8.2 → 0.9.0。
- `panel.go` wbAccount 加 `Anomaly bool`；`buildDashboardEx` 加
  `anomaly_pool_size` / `anomaly_pool_threshold` / `anomaly_refresh_enabled`。
- `panel.html`：过滤栏新增"异常"tab；每张卡显示 `.badge.anomaly`；异常
  卡增"解除冻结"按钮；工具栏增"全部解冻"按钮；`updateFilterCounts` /
  `applyCardVisibility` / `accountsForFilter` / `renderSummary` 同步支持。
- `anomalyRefreshLoop`（init 启动）：每分钟检测本地 0 点触发
  `clearAllAnomalies`，`lastDay` 防重入；可通过
  `anomaly_refresh_enabled: false` 关闭。
- 不在范围：自动 watchdog 积分检测解冻；session 粘性（qoderwork 仍用
  retry-only 模型）；跨账号聚合指标。
