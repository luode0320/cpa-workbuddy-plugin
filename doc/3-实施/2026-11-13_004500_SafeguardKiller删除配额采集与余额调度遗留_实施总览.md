# Safeguard Killer 删除配额采集与余额调度遗留（0.1.7）实施总览

- 时间：2026-11-13 00:45
- 插件：`safeguard-killer` 0.1.6 → 0.1.7
- 触发来源：用户指令「safeguard-killer 删除一些和破甲无关的功能逻辑」
- 提交：`d4c2146`（15 文件 +132 / −3096）

## 1. 目标与判据

Safeguard Killer 的前身是 `Quota Smart Router`（配额智能路由），改名与扩展破甲能力后，旧的配额采集与余额调度代码仍整块留在插件里。本次目标是**只保留与破甲、分组换模型相关的逻辑**，把配额链整块删净。

判定「与破甲无关」的标准是三条链是否被破甲流程依赖：

| 链 | 组成 | 是否服务破甲 | 处置 |
| --- | --- | --- | --- |
| 破甲链 | `request.intercept_before/after` → `handleRequestIntercept` → `sanitizeRequest`；`response.intercept_stream_chunk` → `handleStreamIntercept`；`request.complete` → `handleStreamCompletion` | 是（本体） | 保留 |
| 分组换模型链 | `model.register` / `model.route` / `executor.execute` / `executor.execute_stream` + `groups.go`；破甲失败后的兜底 | 是（兜底） | 保留 |
| 余额调度链 | `scheduler.pick` → `pick` + `balance.go` + `quota.go` + `types.go` 的配额类型 + 面板凭据页 | 否 | **整块删除** |

## 2. 删除依据（不是审美判断，是三个实际负担）

1. **抢宿主调度权**：插件注册 `Capabilities.Scheduler = true` 后，宿主把凭据选择交给插件，CPA 自己的账号池策略（阶梯冷却、保号池、11102 换号）被绕过。这是 2026-11-12 排查「插件换号遍历」时反复撞到的干扰源。
2. **`management_key` 变成隐性必填**：`skipReason` 首行 `if cfg.ManagementKey == "" { return "", nil }` —— 未配置管理密钥时静默放过，配置后开始按配额跳过成员，同一份配置在两种环境行为不同。
3. **维护面失控**：`quota.go` 1324 行维护 8 个 provider 私有 usage 端点（Claude / Codex / Antigravity / Kimi CN / Kimi AI / xAI weekly / xAI month / 插件自报），上游改版即失效且无法在网关侧兜底。

## 3. 删除清单（按文件）

### 3.1 整文件删除

- `safeguard-killer/quota.go`（1324 行）
- `safeguard-killer/quota_test.go`（482 行）
- `safeguard-killer/balance_test.go`（298 行）

### 3.2 `balance.go` 434 → 71 行

保留：`hostCall = callHost`（宿主回调入口，破甲流式路径与分组执行链共用）、`stateMu`、`decisions`、`metadataCanonicalSessionID`、`metadataDerivedSessionID`、`maxDecisions = 200`、`sessionRetention`、`sessionKey`、`pruneSessions`（只清 `groupSessions`）、`recordDecision`、`buildUIState`（改为从 `cfg.Groups` 推导 `models`，从 `groupSessions` 推导 `sessions`）。

删除：`methodHostAuthList`、`methodHostAuthModels`、`metadataRequestedModel`、`hostCacheTTL`、`minHoursToReset`、`schedulerPickRequest/Options/Candidate/Response`、`hostAuthListResponse`、`hostAuthModelsResponse`、`hostSnapshot`、`hostMu`、`hostCache`、`hostModelsUnsupported`、`sessions`、`isUnsupportedCallback`、`hostState`、`hostCredentialModels`、`authsByID`、`pick`、`touchSession`、`rankCredentials`、`rateCredential`、`rolledWindow`、`governingReset`、`bestRank`、`findRank`、`mergedModels`、`servesKnownModel`、`containsString`。

### 3.3 `types.go` 113 → 35 行

删除 `hostAuth`、`quotaWindow`、`credentialQuota`、`rankEntry`、`uiCredential` 五个类型；`decision` 删 `Ranking`；`uiState` 删 `Credentials` / `ManagementError` / `ModelsError`。

### 3.4 `config.go`

- `pluginConfig` 删 `MinRemaining` / `ManagementKey` / `LegacyManagement`
- 删常量 `groupStrategyQuota`；`effectiveStrategy()` 删 quota 分支；`validateConfig` 的 strategy 校验改为只认 `order` / `random`
- `configurePlugin` 删 legacy management key 迁移段
- `pluginRegistration()` 的 `ConfigFields` 6 → 4（`idle_minutes` / `stream_prefetch_bytes` / `groups` / `features`）；`registrationCapabilities` 删 `Scheduler`

### 3.5 `main.go`

删 `methodSchedulerPick` 常量与 `case methodSchedulerPick:` 分支；删 import `"time"`。

### 3.6 `groups.go`

删 `skipReason` 整函数与 `executeGroup` / `executeGroupStream` 两处调用块。

### 3.7 `refusal.go`

删 `orderByQuotaWeightedRandom`（Efraimidis-Spirakis 加权随机）；`orderByStrategy` 删 quota 分支，签名由 `orderByStrategy(cfg pluginConfig, models []string, strategy string, now time.Time)` 简化为 `orderByStrategy(models []string, strategy string)`；删 import `"math"` / `"sort"`。

### 3.8 `ui.go`（面板）

删第 5 个「凭据」tab 与 `pane-credentials` 整块；status-bar 删「凭据」pill；删 `hostErrorBanner` / `modelsWarnBanner` / `mgmtErrorBanner` 三个 banner；设置页删「管理密钥」「最低剩余比例」两个字段；删 `renderCredentials` / `renderWindow` / `renderRanking` / `renderSessions`（按 6 列重写）/ `num` / `pct` / `tierOf` / `fracToPct` / `credName` / `credNameByID` / `details` / `openDetails`；删 `zhExcluded` / `zhMgmtError` / `STATUS_TEXT` / `zhStatus` / `zhHostError` / `zhModelsError`；`STRATEGY_HINTS` 与下拉删 quota；CSS 删 `.quota-bar` / `.quota-fill` / `.windows` / `.rank` 与 `--quota-medium-color`。

## 4. 刻意保留（四条例外，理由必须留在代码里）

| 保留项 | 理由 |
| --- | --- |
| `legacyNoticePrefix = "⚡ [Quota Smart Router] "` | 多轮会话历史里的 assistant 消息可能仍带旧前缀，`cleanMessages` 需要它来剥离，否则旧前缀会被当成正文回传上游 |
| `hostCall` / `callHost` | 破甲流式路径（`prefetchStream` / `forwardStream`）与分组执行链共用同一入口，也是单元测试的替换点 |
| `idle_minutes` | `bindGroupSession` 用它计算 `IdleExpiresAt`，是分组粘性缓存的唯一时间源 |
| `detectStoredManagementKey`（ui.go） | 读的是 CPA 自己的管理密钥，用于调宿主管理 API，与已删的插件 `management_key` 是两个东西 |

## 5. 验证

| 项 | 命令 | 结果 |
| --- | --- | --- |
| 构建 / 静态检查 / 单元测试 | `python scripts/cgo-shim-build.py safeguard-killer` | all green（build OK、vet OK、test ok 0.351–0.553 s） |
| 格式 | 16 个 `*.go` CRLF→LF 归一后 `gofmt -l` | 空输出，exit 0 |
| 面板 JS 语法 | 提取内联 `<script>`（28,067 B，唯一 1 块）跑 `node --check` | exit 0 |
| 面板 DOM 完整性 | 正则提取 `getElementById` 目标与 HTML 声明的 id 交叉比对 | 缺失列表为空（28 个 id 齐全） |
| 残留符号 | `grep quota|management_key|min_remaining|groupStrategyQuota|scheduler.pick|host.auth|rankEntry|hostState|mergedModels|orderByQuota|skipReason` | 仅剩 `refusal.go:21` 的 `legacyNoticePrefix`（刻意保留） |
| UI 已删符号 | `credCount|tabCred|renderCredentials|renderWindow|renderRanking|pane-credentials|hostErrorBanner|modelsWarnBanner|mgmtErrorBanner|zhExcluded|zhMgmtError|zhStatus|STATUS_TEXT|zhHostError|zhModelsError|tierOf|fracToPct|credName|openDetails` | 0 命中 |

## 6. 未被本次改动触及的既有缺口

`refusal.go` 的 `emitExhaustedNoticeStream` 与 `buildExhaustedNonStreamBody` 只认 `claude` 与「其它（chat）」两种格式，而 responses 请求经 `effectiveFormat` 得到的是 `"openai-response"`，会落进 chat 分支。下游 `responsesSSEFramer` 只认 `response.completed` / `response.incomplete` 等终止事件，chat 帧不含终止事件即报 `upstream stream closed before a terminal event`（502），客户端侧表现为 `TRANSPORT` 重试。

该缺口在本次瘦身之前就存在，与配额链无关，单独留作后续修复项。
