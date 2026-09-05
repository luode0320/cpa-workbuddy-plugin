# QoderWork Plugin Changelog

## 0.9.9

### Fix — 失败冷却改为固定 15s，不再指数退避（1/3/10 分钟）

账号失败后的冷却窗口从「按连续失败次数指数退避（1/3/10 分钟封顶）」改为**每次失败一律固定 15s**。连续失败计数 count 仍保留并继续驱动异常池冻结（anomaly 阈值默认连续 10 次不变），只是 count 不再拉长冷却时间——路由层更快放行账号参与再次调度，缓解上游限流窗口比分钟级冷却更短时的可用性损失。

- 涉及文件：`accountFailover.go`（删 `failoverTiers` 档位数组，改常量 `failoverCooldown = 15 * time.Second`；`failoverCooldownFor` 固定返回）、`accountFailover_test.go`（断言同步为固定 15s）、`retry_config.go` 注释同步。
- 同构同步自 workbuddy-provider 0.14.21 / traework-provider 0.1.49。

## 0.9.8

### Fix — 面板 API 前缀缺 -provider 后缀导致账号面板全面板 404 空响应

前端硬编码 `const API = "/v0/management/plugins/qoderwork"`（panel.html:259）缺 `-provider` 后缀，与后端 `providerName = "qoderwork-provider"`（main.go:76）注册路由不一致 → 全部面板 API 打到宿主不存在路由 → 404 + 空 body → 旧版裸 `r.json()` 抛浏览器原生 `Unexpected end of JSON input`，账号面板永远空白。认证文件管理页走宿主 API 不受影响，故"授权成功但面板为空"。

- 涉及文件：`panel.html`（前缀补齐为 `/v0/management/plugins/qoderwork-provider`）。
- 同步加固：`api()` 对齐 workbuddy 健壮解析——空 body / 非 JSON / 坏 JSON 一律转结构化中文 error（`{error:...}`），404 空 body 不再抛无定位价值的英文异常。

### Feature — 对齐 workbuddy-provider 0.14.20 面板能力（排序 / 搜索 / 导出 / 备份恢复）

1. **积分排序三态**：工具栏「积分 ↕/↑/↓」循环切换（升序/降序/关闭），按 `credits.total_remain`，未知积分按 -1 沉底；排序激活时单卡更新与整页加载均保持有序。
2. **工具栏搜索**：昵称 / 文件名 / UID 大小写不敏感模糊过滤，与区域筛选联动，汇总卡随筛选刷新。
3. **凭据导出**：`GET /export` 返回全部凭据原始物理 JSON（wrapper：`{version, exported_at, plugin, count, accounts:[{name, auth_index, uid, nickname, credential}]}`），前端一键下载 `qoderwork-credentials-YYYY-MM-DD.json`；`/export` 虽为 GET 但纳入 `mutatingManagementPath`（携带完整凭据，必须要求 management key）。
4. **备份恢复**：`POST /import-cred` 接受单个凭据 JSON（仅 parseStored 结构校验 + uid 非空，不经上游换 token），原样持久化；导入模态新增「从备份恢复」区，`expandCredentials` 支持完整备份文件 / 凭据数组 / 单凭据三种形态，逐项恢复带进度与失败明细。导出 → 恢复完整闭环。

- 不同步项：workbuddy `/trial`（平台特有，qoderwork 已有对应物 `/claim-pro`）；toast 上限与 err 6s（已一致）；`fetchAndPatchCredits`（功能等价，内联于 `pollRefreshStatus`）。
- 测试：cgo-shim build+vet+test 全绿；panel.html JS 语法校验（2 blocks）+ 10 项功能标记自检通过。

## 0.9.7

### Fix — 删除确认按钮 busy 状态泄漏导致无法连续删除账号

与 workbuddy-provider 0.14.20 同构修复（逐函数适配）：删除账号成功后确认弹窗按钮停留在「处理中…」禁用态且从未复位，弹窗 DOM 静态复用导致下次打开不可点击。

- 涉及文件：`panel.html`（`confirmDeleteAuth()` 成功分支补 `busy(btn,false)`；`openDeleteModal()` 打开时防御性复位按钮）。

## 0.9.5

### Feature — 全量对齐 workbuddy-provider（1-9 项功能同步）

与 workbuddy-provider 0.14.13 功能对齐（同步原则：逐函数适配、纯逻辑文件可整文件复制；SSE 嵌套解包 / COSY 签名等架构差异保留 qoderwork 原样）。

1. **登录轮询重复账号修复**：`oauth.go` `handlePollLogin` 三处成功路径改 `toAuthDataOpts` + `ad.ID=""`（对齐 workbuddy 0.14.12，修复同一文件双 key 重复账号）。
2. **models 配置面板化 + ConfigFields 中文化**：`models.go` 新增 `configuredModels` + `parseModelsConfig`，`usage_config.go` configure() 接入 `case "models"`（配置优先链 config > dynamic > static）。
3. **面板 5 卡片 + 异步刷新前端**：用量汇总 5 卡片口径（剩余可用/不可用/已用/额度池/占比）+ 异步节流刷新前端完整对齐。
4. **账号删除**：`POST /delete` 严格校验链 + 二次确认模态框 + `clearDeletedAccountState` 三键清理。
5. **计数持久化**：`counter.go` 内存累计真相源 + 落盘挂 `preserveWatchdogLoop`（启动 `loadCountersFromDisk` + 每次醒来 `flushCounters`，与 workbuddy 对称）。
6. **session_auth 会话粘性**：`schedulerModeSession` 默认 + `pickSessionAuth(extractSessionKey(req), cands)` 分支 + `evictSessionBindingsForAuth` 四接入点（noteAccountFailure 双路径 / freezeAccountForAnomaly 两分支 / clearDeletedAccountState / preserve 进入）。
7. **usage_feed NDJSON 通道**：`usage_feed.go` 新增（`token-usage-feed.ndjson`），`publishUsage` 8→12 参数（+reasoningEffort / ttftNS / accountLabel / sessionKey），11 处调用点全适配，`sseUsageCollector` 加 `firstByteAt` + `ttftNS`。
8. **保号池 + watchdog**：`preserve.go` / `watchdog.go` 新增（`preserveThresholdDefault=50` / 10m tick / enabled=true），面板保号展示（badge / ftag / 过滤 / 汇总统计）。
9. **ConfigFields 全量对齐**：`usage_feed_enabled` / `usage_feed_path` / `preserve_*` 等声明补齐，Description 中文化。

- 测试：`session_auth_test.go` / `usage_feed_test.go` / `watchdog_test.go` / `auth_delete_test.go` 同步 + 补 qoderwork 缺失 helper。
- 验证：cgo-shim build+vet+test 全绿；双 panel.html JS 语法校验通过。
- 未涉及：traework-provider；workbuddy-provider 本版不改动。

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
