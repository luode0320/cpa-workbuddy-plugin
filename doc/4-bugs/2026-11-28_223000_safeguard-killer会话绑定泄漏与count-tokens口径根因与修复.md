# safeguard-killer 会话粘性绑定泄漏与 count_tokens 口径不一致根因与修复

- 时间: 2026-11-28 22:30
- 插件: safeguard-killer（生产 0.1.11 → 本次 0.1.13）
- 触发方式: 持续监控（bug 巡检 + 未使用功能清理）
- 结论: 2 个真实缺陷 + 2 处冗余/死代码已修；3 项风险已登记未修

## 1. 巡检手段与证据来源

| 手段 | 命令 | 结果 |
| --- | --- | --- |
| 静态死代码扫描 | `go install golang.org/x/tools/cmd/deadcode@latest` → 在 cgo-shim 副本跑 `deadcode -test=false ./...` | 全量符号报 `unreachable`，原因是插件入口是 `//export cliproxyPluginCall` 的 C ABI，不是 `main()`，`deadcode` 的可达性分析对 c-shared 插件不适用，结论作废 |
| 未使用符号扫描 | `go install honnef.co/go/tools/cmd/staticcheck@latest` → 在 cgo-shim 副本跑 `staticcheck -checks all,-ST1000,...` | 命中 1 条真实 U1000（`pruneSessions`）+ 1 条 ST1005；其余 U1000 全部落在被 shim 剥离的 C ABI 符号上，属环境假象 |
| 生产行为取证 | `docker logs cli-proxy-api --since 6h` + 免鉴权 `GET /v0/resource/plugins/safeguard-killer/state` | 6 小时日志无 `panic` / `unknown_method` / `not_supported` / `[error]`；state 19508 字节，60 条决策全为 `served`、无 `failed`，8 条会话绑定 |
| 宿主契约取证 | 读 `F:\CLIProxyAPI\internal\pluginhost\executor_route.go`、`sdk/api/handlers/handlers_routing.go`、`handlers_model_router_test.go` | 确认 `count_tokens` 与对话请求共用 `model.route` 选择链（`plugin_executor_count_tokens` 用例） |

## 2. BUG-1 过期会话粘性绑定无人清理（groupSessions 只增不减）

### 现象与证据

- `staticcheck -checks all` 直接报：`balance.go:43:6: func pruneSessions is unused (U1000)`。
- 全包 grep 确认：`pruneSessions` 只在 `balance.go` 定义处出现一次，`groups.go` / `refusal.go` / `ui.go` 均无调用。
- 当前唯一清理路径是 `groupCandidateOrder`（`refusal.go:60-72`）的懒删除——只有「同一个 `session\x00groupID` 再次被访问」时才会判断过期并 `delete`。

### 根因

0.1.7 精简批次把带定时/配额的旧清理循环删掉后，`pruneSessions` 失去了唯一调用方，但没有随之删除或改挂到新路径上。结果：一次会话一旦不再被访问，它的绑定就永久留在进程 map 内。

每条 `sessionBinding` 含 session 串（UUID 形态）、auth_id、provider、model 与 3 个时间戳，实测约 200 字节；`buildUIState` 又把 map 全量序列化进 `sessions` 字段，而面板每 10 秒轮询 `/v0/resource/plugins/safeguard-killer/state`（生产日志 `22:16-22:20` 可见稳定 10s 节奏的 200 响应）。

### 影响

- 进程内 `groupSessions` 随历史会话数单调增长，直到重启；
- 面板状态体随之膨胀（生产已 19.5 KB），每 10 秒序列化并传输一次；
- 面板 `sessions` 会展示早已失去粘性意义的绑定（`idle_expires_at` 已过的记录仍在列）。

### 修复

1. `balance.go` 新增 `sessionPruneInterval = 5 * time.Minute` 与受 `stateMu` 保护的 `lastSessionPrune`；
2. 新增 `pruneSessionsThrottled(now)`：按间隔节流调用 `pruneSessions`，把全表扫描压到五分钟一次（绑定写入是每请求热路径，逐请求扫描不可接受）；
3. `refusal.go` 的 `bindGroupSession` 在持锁后、写绑定前调用 `pruneSessionsThrottled(now)`，把清理从「读路径懒删除」改为「写入路径周期清理」；
4. `buildUIState` 持锁后先 `pruneSessions(now)`，并跳过 `IdleExpiresAt` 已过的绑定，面板只回仍有粘性意义的会话。

保留 `sessionRetention = 24h` 不变：`buildGroupNotice` 依赖「上一轮由哪个模型服务过」来决定是否提示 `switched from X → Y`，24 小时窗口内的历史绑定仍有意义。

### 验证

`sessions_test.go` 三条用例 + 三条哨兵变异（见 `doc/5-tests/2026-11-28_224500_safeguard-killer0.1.13会话清理与口径收敛代码级验证.md`）。

## 3. BUG-2 成员模型名请求 count_tokens 被判为未知分组

### 现象与证据

- `routeGroup` / `executeGroup` / `executeGroupStream` 都走 `resolveTargetGroup`，因此「用分组成员模型名直接进入分组」是被支持并已测的行为（`groups_test.go: TestRouteGroupClaimsRealModelNames`）。
- `countGroupTokens` 却写成 `cfg.group(req.Model)`，只认分组代号。
- 宿主侧：`sdk/api/handlers/handlers_routing.go:routeModel` 同时服务于对话与 `ExecuteCountWithAuthManager`；`handlers_model_router_test.go:896 plugin_executor_count_tokens` 用例明确覆盖「模型路由命中插件后由插件 executor 执行 count_tokens」。

### 根因

两条链路对「什么算本插件的模型」判定口径不一致：路由链用 `resolveTargetGroup`（含成员名、含 `claude-opus-4-8` 兜底），计数链用 `cfg.group`（只认代号）。

### 影响

客户端拿成员模型名请求 `/v1/messages/count_tokens` 时，宿主路由命中本插件，插件返回 `model group X is not configured`，计数失败；对话请求同样的模型名却正常。Claude Code 一类客户端会用 count_tokens 估算上下文预算，失败表现为上下文统计不可用。

### 修复

`countGroupTokens` 改用 `resolveTargetGroup(cfg, req.Model, "")`，与路由链同口径。

### 验证

新增 `TestCountGroupTokensAcceptsMemberModelName`；哨兵变异（退回 `cfg.group`）KILLED。

## 4. 未使用功能清理

### U-1 `goAfterStartupJitter` / `waitStartupJitter` / `startupJitterMax`（本包零使用）

0.1.12 的 OOM 治理给 13 个插件统一补了 `memory_limit.go`，其中两个助手是给「有后台循环的插件」做启动错峰的。safeguard-killer 没有 scheduler、没有定时器、没有任何后台循环，三者零调用（`staticcheck` U1000 命中）。已删除两个助手、`startupJitterMax`、因此失效的 `context` / `math/rand` / `time` 导入，以及两个只测这两个助手的用例。

保留：`init()` 的 `debug.SetMemoryLimit` 与 `CPA_PLUGIN_MEMORY_LIMIT_MB` 覆盖。这部分仍然必需——每个插件链接独立 Go runtime，宿主注入的 `GOMEMLIMIT` 只是各 runtime 的独立软上限，容器内存上限才是硬约束。

### U-2 `withModel` 内部重复净化

`executeGroup` / `executeGroupStream` 已用本次请求的 `cfg` 调过 `sanitizeRequest`，`withModel` 又对同一份 body 净化一次，而且第二次用的是 `currentConfig()`：配置热更新期间会用新配置覆盖本次请求口径。已改为只改 `model` 字段，Gemini 这类「model 只在路径里、body 无 model 字段」的协议继续原样透传。

## 5. 登记未修项（本轮不动，留待持续监控）

| 编号 | 现象 | 判定 | 为什么不本轮修 |
| --- | --- | --- | --- |
| R-1 | `claudeStreams.states` 只在 `request.complete` 回调里 `delete`，无 TTL 兜底 | 宿主正常路径会回调 complete；只有宿主异常/回调丢失才泄漏 | 加 TTL 需要新时间源与锁内扫描，收益取决于宿主回调可靠性，先观察生产 |
| R-2 | `handleStreamIntercept` 在 `ChunkIndex == -1` 时直接把 body 透传，不 flush 残余缓冲 | 正常流由 `message_stop` 触发 `flush`；只有流被截断时丢弃缓冲内容 | 需要宿主流结束语义的确证样本，当前生产 6 小时无相关报错 |
| R-3 | `stream.go:79` ST1005 错误串首字母大写 | 纯风格，`Claude stream normalization requires a request ID` 是既有对外错误文本 | 改文本会动错误消息，非必要不动 |

## 6. 结论

- 生产当前运行 0.1.11，本地 0.1.13 已完成代码级验证并提交；发布前 0.1.11 仍可用（无 panic/无失败决策）。
- 本轮没发现新的路由/拦截类功能缺陷；巡检结论是「主链路健康，问题集中在状态生命周期与两条链路的口径一致性」。