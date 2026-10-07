# 项目记忆

## 核心记忆

### 仓库与发布

- 仓库：`luode0320/cpa-workbuddy-plugin`（原 cpa-plugin，2026-08-22 改名）；物理目录 F:\cpa-plugin
- 多插件架构（5 服务商 + 1 用量统计，2026-10-07 扩展）：workbuddy-provider（腾讯 CodeBuddy CN+Global）、workbuddy-ai-provider（WorkBuddy AI 国际版）、traework-provider（Trae SOLO）、gemini-provider（发布名称 "Gemini Provider"，Google Gemini CLI）、cursor-provider（Cursor，cursor.sh）、workbuddy-token-usage（用量 dashboard 统一查询各服务商 token 使用），历史兼容 qoderwork-provider（QoderWork CN）
- 发布链路（不可跳步）：bump VERSION+main.go → commit → push main → dispatch CI（plugin=xxx version=yyy）→ 下载 8 assets → **git add assets + push（0.9.7 教训）** → publish-assets.py → commit registry + push → 远端验证 raw URL 200
- 发布后清理（每次发布闭环后必做，2026-10-07 固化）：`release-assets/` 受 git 跟踪且被 registry raw URL / 生产 `plugin-store install` 直接引用——**只保留 registry.json 每个插件的当前版本目录**，历史版本用 `scripts/prune-release-assets.py`（`--dry-run` 先看，`--apply` 执行）删除并提交推送；判定只认 registry 的 `id`+`version`，禁用日期/数量启发式；本地未跟踪缓存 `.workbuddy/release-assets/` 直接删。详见项目 skill `project-cpa-workbuddy-plugin-release-asset-prune-rules`（发布 skill Step 13.5）。
- git push 必带：`GIT_TERMINAL_PROMPT=0 GIT_ASKPASS='C:\Users\luode\.github\git-askpass.sh' git -c credential.helper= push https://...`（askpass 用完即删）；tag pattern：`workbuddy-provider-v*` 等
- **仓库默认处于「提交 / 发布已授权」状态（2026-09-30 起）**：`AGENTS.md` / `CLAUDE.md` 的「提交 / 发布授权（默认授权，强制）」段规定——用户不需要每轮显式说「提交」「推送」「发布」，agent 在完成改动并通过全部门禁后可直接 commit → push → CI → assets → registry → 生产 plugin-store 部署；**用户当轮显式边界（如「只提交 git, 不要推送」「先不发布」）绝对优先**；默认授权不免除门禁；本仓库内以该仓库级规则覆盖全局 `git-collaboration-rules` 的「仅当前轮授权」默认语义。
- registry.json：`plugins` 是 list，artifacts 在 `install.artifacts`

### 插件架构事实

- **workbuddy 与 qoderwork 并非完全同构**（2026-08-22 实测 HEAD 基线差异：stream.go 224 行 / main.go 420 行 / scheduler.go 123 行）
  - qoderwork 是旧版：publishUsage 8 参数（无 accountLabel/reasoningEffort）、无 preserve watchdog、无 session_auth（无会话粘性）、SSE 用嵌套解包（outer["body"]）、认证走 `applyCosyHeaders`（COSY 签名，endpointChat 常量）+ qoderEncode 编码体
  - workbuddy 是新版：publishUsage 带 reasoningEffort/accountLabel、有 preserve watchdog、session_auth、`backendHeaders`+`endpointChatFor(sa)`、stripDataPrefix SSE
  - **同步改动只能逐函数适配，不能整体覆盖文件**；accountFailover.go 等纯逻辑文件可整文件同步
- 插件是 c-shared（import "C"），Windows 无 gcc 时 main.go 被工具链忽略（undefined: storedAuth 是环境假象），验证一律走 `python scripts/cgo-shim-build.py <plugin>`
- **插件侧无 ws/SSE 长连接通道（宿主 SDK v7.2.129 实测，2026-09-01）**：插件 ABI 无任何注册 ws/SSE 长连接的方法（`AttachWebsocketRoute` 仅服务内部 wsrelay；`MethodHostStreamEmit/Close` 的 StreamID 只在 executor 流式路径创建）；management/resource 桥接单次写回（`w.WriteHeader + w.Write` 无 Flush/ws 升级）。SSE body 原样透传（`text/event-stream` 不触发 JSON 转义）→ 实时推送落地「SSE 短连接轮询通知 + REST 拉取」：`/usage/events` 返回 `retry: 2000\n\ndata: {"seq":N}`，EventSource 自动重连，seq 前进才触发 load()；15s 轮询 fallback。前端 `fullModePage` 禁用 EventSource（无法带 session header）。详见知识库《插件侧无WebSocket长连接只能SSE短连接轮询》
- 磁盘写路径：host.auth.save 会丢未知顶层字段 → 直写物理 auth 文件（writeAuthFileDirect + fsnotify）；auth 目录 `~/.antigravity_cockpit/<plugin>_accounts/`
- **cursor-provider 架构事实（2026-10-07 移植）**：来源 yobo2u/omsub cursor 分支的 cursor-plugin，落在仓库 `cursor/`，不依赖 CLIProxyAPI SDK（仅 testify + protobuf）；provider id `cursor-provider`，凭据文件名前缀 `cursor-`（`cursor-<hash8>.json`，顶层 `type=cursor-provider`）。Token 导入：会话 JWT 作为 refresh_token 走 `POST https://api2.cursor.sh/oauth/token`（body `grant_type=refresh_token` + `client_id=KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB` + `refresh_token`；**不返回 refresh_token，须保留原 JWT 段继续刷新**）；旧端点 `/auth/exchange_user_api_key`（401）与 `/auth/exchange`（404）已失效。管理路由前缀 `/v0/management/plugins/cursor-provider/*`（import/export/delete/enable/disable）；禁用标记走直写物理文件顶层 `disabled`（同 workbuddy，host.auth.save 会丢未知顶层字段）。
- **账号路由口径 = 「硬排除两类标签 + 低积分优先」（2026-09-30 起移除保号池）**：`scheduler.pick` 只让「可用」账号承载流量——「测试」`test_failed` 与「冷却」failover cooldown 两类**无条件硬排除且无任何回退**（旧版「全部保号时回退全量列表」已删除；候选全被排除即 `Handled:false` 交还宿主做跨 provider failover）。存活候选按缓存剩余积分**升序**排序（未测积分 -1 排最后），意图是先把临近耗尽的账号用完，再由定时活跃探测失败打「测试」标签。统一判定：`workbuddy/active_auth.go` 与 `traework/active_auth.go` 各导出 `accountRoutable(authID)`（`!cooling && !testFailed`）与 `accountLowerCredits(left,right)`，被 `pickActiveAuth` / `ensureDefaultActiveAuth` / `pickSessionAuth` / `scheduler.pick` 共用；面板选中项 = 「可用账号中积分最低者」，与调度口径一致。请求内换号 `pickNextAuth` 同样跳过测试账号，但**保持宿主顺序不排序**（同一请求重试链必须可预测）。`test_failed` 内存镜像 `testFailedSet` + `refreshTestFailedSetFromDisk` 在面板构建 / 账号 watchdog tick / 标签直写三处同步，重启后仍正确排除。**保号池（`preserve`）已于 2026-09-30 移除，其原职责由 `test_failed` 标签承担。**
- **traework `cachedCreditsScore` 空指针曾致 panic（2026-09-29 修复；保号池已于 2026-09-30 移除）**：缓存条目存在但 credits 尚未拉取时原代码直接解引用 `entry.credits.TotalRemain` 会 panic，现归一为「未知积分 (-1), not exhausted」。workbuddy 侧对应函数本就有 `entry.credits == nil` 守卫，无需改。
- config_yaml 经 host RPC 传输时 []byte 走 base64；测试必须 `json.Marshal(map{"config_yaml": []byte(yaml)})`
- **账号「创建时间」只能取自 JWT `auth_time`，不能取宿主 `HostAuthFileEntry.CreatedAt`（2026-09-27，workbuddy 0.14.42）**：宿主 watcher 每次扫描都执行 `auth.CreatedAt = time.Now()`（`internal/watcher/synthesizer/file.go:114`，`dispatcher.go:320` 传 `Now: time.Now()`），所以宿主侧 CreatedAt 会随积分刷新 / 保号 keepalive / 活跃测试写入漂移到「最近写入时刻」；生产 48 个 workbuddy auth JSON 顶层无 created_at 字段，文件 mtime/ctime 同样不可用。唯一可靠源是 accessToken JWT payload 的 `auth_time`（登录写定、token 刷新不变，生产 48/48 账号全有值，分布 08-20~09-27）；`iat` 会随刷新集中漂移（实测 19 个账号同秒），不可当创建时间。解析 helper：`workbuddy/created_at.go` 的 `parseCreatedAtFromAccessToken`（base64url 解 payload 第 2 段，不验签——本地已持有 token，非安全边界）。

### 关键设计决策

- 数据库/配置一律逻辑引用（无物理外键）
- failover：1/3/10 分钟阶梯退避，429/402/5xx/传输错误计入，业务 400 不计
- 40x 换号重试（2026-08-22）：401/403/404/405 计入账号级故障，`retry_on_4xx` 预算默认 3（0-5），**缺省键保持当前值**（kill switch 安全），400 直通不重试
- 路由：0.12.0 起移除三池只留保号池；**2026-09-30 起保号池也已移除**，路由健康闸门只保留「测试标签 `test_failed` + failover cooldown」硬排除 + 低积分优先；存量 `pool`/`priority`/`preserve` 字段忽略式读取不清理
- 版本三轨（qoderwork）：main.go 0.8.2 / VERSION 0.4.1 / registry 0.2.x 历史双轨，发版以 registry 为准
- 跨插件数据通道：NDJSON 文件 feed（token-usage-feed.ndjson，超 128MB 截断），不用共享 bbolt（排它锁冲突）；2026-10-05 扩展支持 gemini-provider（写入 usageMetadata 与 TTFT 耗时），token-usage-tracker 统一将 gemini/gemini-cli/gemini-provider 归一化展示为 "Gemini"
- **面板「成功/失败」计数是 CPA 宿主的 recent 窗口计数，纯内存态不落盘**（2026-08-23 根因确认）：`CLIProxyAPI v7 sdk/cliproxy/auth/types.go` 里 `Auth.Success int64 json:"-"` / `Auth.Failed int64 json:"-"` / `recentRequests json:"-"`，序列化写 auth 文件时被显式跳过 → 容器重启必然清零，与挂载无关（deploy-server.yml 的 auths 目录其实挂了 `-v "${AUTH_DIR}":/root/.cli-proxy-api`，但字段本就不写盘）。workbuddy 插件只透传 `host.auth.list` 的 `HostAuthFileEntry.Success/Failed`（panel.go 注释「persisted by the host」），自己不维护。窗口约 10min×20 桶≈200 分钟，是滚动健康度指标而非全量历史累计
- **方案 B 落地（workbuddy 0.14.10，2026-08-23）**：插件自维护累计计数并持久化到 auth 文件顶层 `success_count`/`failed_count`（**字段名刻意避开宿主的 `success`/`failed`**，避免与 HostAuthFileEntry recent 窗口形成双源歧义）。`counter.go`：`recordOutcome(uid, success)` 内存递增（key=UID，与调度/failover/preserve/anomaly 同键）→ `startCounterFlusher` 后台 10s flusher `flushCounters` 把增量经 `foldCounterIntoDoc`（保留其余顶层字段）折入物理文件 → `persistAuthDirect` 直写（非 host.auth.save）。埋点在 `publishUsage` 统一 `recordOutcome(authID, !failed)`（每请求恰好一次，authID 即 UID）。panel 读取：UID 账号用 `parseCountersFromAuthJSON(phys.JSON)` + `counterPendingDelta` 合并，legacy 无 UID 账号回退 recent 窗口
- **计数持久化重构「内存为主 + 跟随保号落盘」（workbuddy 0.14.11，2026-08-24）**：0.14.10 的 10s 独立 flusher + 面板每次 parse json 改为——`counter.go` 用 `counterEntries`（UID→`counterEntry{success,failed,persistedSuccess,persistedFailed}`）作内存累计真相源：`recordOutcome` 纯内存递增、`ensureCounterLoaded` 首次从 json 初始化（合并进程内增量不丢）、`counterSnapshot` 供面板读（不每次 parse json）；落盘删除独立 flusher，改挂 `watchdogLoop`（启动 `loadCountersFromDisk` 恢复历史 + 每次醒来 `flushCounters`，启用默认 10min、禁用 30s 兜底），`flushCounters` 算 `total-persisted` 增量折入后回写 persisted、失败保留重试。json 语义=兜底持久化（最多丢一个 tick 增量，可接受），内存=运行期唯一真相源

## 变更记录

- 2026-10-07: 新增「发布后清理」规则（AGENTS.md/CLAUDE.md 发布小节 + 发布 skill Step 13.5）与项目 skill `project-cpa-workbuddy-plugin-release-asset-prune-rules` + `scripts/prune-release-assets.py`；`release-assets` 3.75 GB → 0.13 GB
- 2026-08-23: 由 `project-rule-file-bootstrap-rules` 的 `memory-bootstrap` 初始化双区骨架；核心记忆由项目分析沉淀
- 2026-07-03: 模板骨架初始化（模板原始记录）

## 机器索引区

```yaml
version: 1
entities: []
relations: []
evidence: []
contexts: []
lifecycle:
  active: []
  deprecated: []
  stale: []
  conflicted: []
  retired: []
retrieval_hints:
  aliases: {}
  scopes: {}
  sources: {}
extensions:
  external_refs: []
  retrieval_provider: ""
  vector_doc_id: ""
  graph_node_id: ""
usage_tracking:
  schema_version: 1
  counted_files:
    - PROJECT_MEMORY.md
    - PROJECT_STYLE.md
    - PROJECT_HISTORY.md
  policy_ref: memory-usage-tracking-rules/references/usage-tracking-policy.md
```
