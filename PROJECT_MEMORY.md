# 项目记忆

## 核心记忆

### 仓库与发布

- 仓库：`luode0320/cpa-workbuddy-plugin`（原 cpa-plugin，2026-08-22 改名）；物理目录 F:\cpa-plugin
- 多插件架构（7 服务商 + 1 用量统计，2026-10-10 扩展）：workbuddy-provider（腾讯 CodeBuddy CN+Global）、qoderwork-provider（QoderWork CN，0.9.x）、workbuddy-ai-provider（WorkBuddy AI 国际版）、qoder-ai-provider（Qoder AI 国际版，qoder.com 每日签到 +100 积分）、traework-provider（Trae SOLO）、gemini-provider（发布名称 "Gemini Provider"，Google Gemini CLI）、cursor-provider（Cursor，cursor.sh）、workbuddy-token-usage（用量 dashboard 统一查询各服务商 token 使用）
- **生产 plugin-store 的自定义源是「整源原子校验」（2026-10-10 实测）**：宿主 `pluginstore.ParseRegistry` 对单个源的一份 `registry.json` 先 normalize 再 ValidateRegistry，**任一条目非法即该源整体解析失败**；`fetchSourcedPlugins` 收到该源 error 就 `continue`，该源**一个插件都不入列**，表现为该源下所有插件 install 都返回 `{"error":"plugin_not_found","message":"plugin not found in registry"}`，并在 `GET /v0/management/plugin-store` 的 `source_errors` 里给出 `plugins[<index>]: <原因>`。本次触发原因：某个新插件登记为 `"install":{"type":"direct","artifacts":[]}`（`direct` 类型要求至少一个 artifact）。**排查口径**：某源插件集体 plugin_not_found 时先读 `source_errors` 定位半成品条目，而不是怀疑自己的发布链路；对端补齐 artifacts 后源自动恢复（宿主每次 install 实时拉 registry，无缓存，无需重启）。**预防**：新插件先建代码与 CI、资产就绪后再一次性提交 registry 条目。
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
- **插件侧无 ws/SSE 长连接通道（宿主 SDK v7.2.129 实测，2026-09-01）**：插件 ABI 无任何注册 ws/SSE 长连接的方法（`AttachWebsocketRoute` 仅服务内部 wsrelay；`MethodHostStreamEmit/Close` 的 StreamID 只在 executor 流式路径创建）；management/resource 桥接单次写回（`w.WriteHeader + w.Write` 无 Flush/ws 升级）。SSE body 原样透传（`text/event-stream` 不触发 JSON 转义）→ 实时推送落地「SSE 短连接轮询通知 + REST 拉取」：`/usage/events` 返回 `retry: 2000\n\ndata: {"seq":N}`，EventSource 自动重连，seq 前进才触发 load()；15s 轮询 fallback。前端 `fullModePage` 禁用 EventSource（无法带 session header）。（2026-10-09 演进：token-usage 面板 0.2.4 已整体移除 SSE 短连接轮询与 15s 轮询，改手动刷新 + 默认最近 1 小时——打开面板持续消耗服务器性能；本条 ABI 结论不变。）详见知识库《插件侧无WebSocket长连接只能SSE短连接轮询》
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

- 2026-10-10: traework-provider 0.2.5 发布部署完成——修复签到 `x-device-id` 连字符拼接（`<base>-<uid>`）与 `randomDeviceID` 尾零填充/号段越界（如「用户04878311608」的 `9670064000000000`）触发的上游 9074（「当前参与用户太多，请稍后再试」）风控拦截，改为 SHA-256 确定性派生首位 1~3、末位 1~9 的 16 位纯数字设备标识 + 遇 9074/9095 自动轮换新 16 位设备号重试 + `autoCheckinLoop` 绝对时间对齐。发布链 `54f490a` → CI run `38059677609` success → `6291b2a`（8 资产）→ `2ffcdc9`（registry）→ `74dfe92`（prune 0.2.4）；生产热重载 `active_version=0.2.5`、落盘 `.so` SHA-256 `f24fe5f4…` 与本地一致；目标账号「用户04878311608」签到成功（积分 130→230，包数 2→3），全量 7 账号 `/checkin` 7/7 `ok:true`。
- 2026-10-10: qoder-ai-provider 0.1.3 修复宿主 HTTP 桥状态码解码错误（签到假成功根因）——插件用 `json:"status_code"` 解码宿主 `host.http.do` 响应，而宿主 v7.2.x 序列化 `pluginapi.HTTPResponse` 未加 json tag，实际键名是 PascalCase `{"StatusCode":404,...}`；下划线标签既不匹配键名也不构成大小写不敏感匹配，`StatusCode` 恒为 0，`if resp.StatusCode >= 400` 永不触发，404/4xx/5xx 全被当作 200 成功。抽出纯函数 `parseHostHTTPDoResult` 按真实线协议解码并兼容下划线变体；`performCheckinCall` 收紧为「缺 success 字段即返回失败」，去掉盲默认成功。新增 `host_bridge_test.go` 三用例，反证（临时改回 `status_code`）必失败。**关键产品事实：国际版 `openapi.qoder.sh` 无 `/sash/api/v1/me/daily-check-in/{status,claim}` 端点（404 NotFound），官方桌面客户端无签到代码，奖励体系是 `campaigns`（三账号均 `claimable:false, campaigns:[]`）→ 修好解码后签到必失败，真实签到入口待产品确认（GAP-001，禁止伪造成功）。** 同源缺陷：`qoder-ai` 与 `qoderwork` 的 `host_bridge.go` 逐字节相同，qoderwork 待修；workbuddy 0.14.30（1f26e0c）为先例。
- 2026-10-10: qoder-ai-provider 0.1.2 / qoderwork-provider 0.9.25 content 多形态解析根因修复——两插件 `body.go` 的 `openAIMessage.Content` 原为强类型 `string`，客户端发 OpenAI 多模态部件数组 content 时 `json.Unmarshal` 直接失败，executor 返回 `payload parse: json: cannot unmarshal array into ... content of type string` 并对外 503；面板「测试」按钮走 `sendActivePingQoder` 直接构造结构体绕过解析，故测试通过而真实推理必失败。修复：`openAIMessage` 增加自定义 `UnmarshalJSON` + `decodeOpenAIContent`（字符串原样 / 数组提取 text·input_text 拼接 / null 归一空串），`Content` 字段类型与下游签名零改动；两插件逐字同构。**关键教训：面板「测试」按钮只证明上游连通性，不覆盖客户端 payload 解析路径，测试通过不等于推理可用。** qoderwork 首次定版 0.9.24 与并行会话已发布版本撞车（`cafc4cf`），改发 0.9.25。
- 2026-10-10: qoderwork-provider 0.9.23 发布完成——面板卡片「测试」按钮对齐三插件口径，从「随机挑模型直接测」改为「点测试 → 弹出该账号支持的模型小窗口 → 点选指定模型再测」；后端新增 authModelsForIndex + handleModelsQuery + 只读 GET /models?auth_index= + handleTestActiveWithAuth(sa, authIndex, model)（空 model 兼容随机）；前端新增 testModal 弹窗与 6 个 JS 函数。发布链 7df7f9a → CI run 37964833825 success → 7f18e2a（8 资产 7/7 sha256 OK）→ registry 0.9.23 回填；生产 install hot reloaded active=0.9.23 retired=0.9.22，落盘 .so sha256 bb55d2df… 与本地 zip 一致；行为验收 /models 返回 {"models":["auto"]}、/test-active 指定 model=auto 成功 421ms（621ms 兼容路径）。同轮定位「共享源半成品条目拖垮整源」根因（见「仓库与发布」条）。
- 2026-10-09: workbuddy-provider 0.15.4 / workbuddy-ai-provider 0.1.9 / traework-provider 0.2.3 发布完成——三插件面板卡片「测试」按钮从「随机挑模型直接测」改为「点测试 → 弹出该账号支持的模型小窗口 → 点选指定模型再测」。后端三插件 models.go 新增 authModelsForIndex + handleModelsQuery，management.go 新增只读 GET /models?auth_index=，active_ping.go 的 handleTestActiveWithAuth(sa, authIndex, model) 支持指定模型（model 空则保持随机，兼容旧面板）。前端三插件 panel.html 新增测试弹窗（openTestModal / loadTestModels / runTestModel）。发布链 ce6b4e2（32 文件）→ CI 三 run success（37940758486 / 37940778484 / 37940786472）→ fe5742d（24 资产）→ 207e22c（registry）→ ada831f（prune 旧版 0.15.3 / 0.1.8 / 0.2.2）；远端 49/49 当前资产 200、旧版 404；生产 plugin-store install 三插件 installed，落盘 .so sha256 与本地 zip 一致（9c3b85b6… / dd77aab4… / 2d671e16…），热重载 active_version 命中目标版本；行为验收 /models 返回真实模型（17 / 27 / 大量）、缺 auth_index 报错、/test-active 指定 deepseek-v4.1-flash 成功
- 2026-10-09: workbuddy-ai-provider 0.1.8 发布完成——国际版签到改造（面板「领取专家加油包」失效入口替换为每日签到 + 自动签到开关 + 批量签到，前端对齐 CN 面板口径）；发布链 583bb05 → b4e3a3e（8 资产）→ 45f6ce5（registry）；CI run 37836464993 全 57 jobs success；生产 plugin-store 热重载 active_version=0.1.8 retired_version=0.1.7，落盘 .so sha256 baf416d3… 与本地 zip 一致；生产 /checkin 按契约透传上游结果（code=10001 签到活动未开启），四路对照 + CN 域反证 + banner 12302 判定上游活动离线（GAP-001 环境性阻断，禁止伪造成功）
- 2026-10-09: workbuddy-token-usage 0.2.4 发布完成——移除面板 SSE 短连接轮询与 15s 定时轮询，改手动刷新 + 默认最近 1 小时；补插件图标（assets/icons/TokenTracker.png）。发布链 aab549c → 37de5e4（8 资产）→ cd8b642（registry）→ 63a9879（prune 0.2.3）；CI run 37812435637 全 57 jobs success；生产 plugin-store install 热重载 active_version=0.2.4 retired_version=0.2.3，落盘 .so sha256 8ce19240… 与本地 zip 一致；生产面板 /usage 200（EventSource=0 / setInterval=0 / last_1_hour×7）
- 2026-10-07: cursor-provider 0.1.0 发布完成——发布链 44d2bb4 → a17c2aa（8 资产）→ 92f569c（registry 回填），CI run 37636155616 全 57 jobs success，生产 plugin-store install 热重载成功，端到端验收通过（token 导入、去重、流式/非流式推理、export 接口）；高级模型上游 resource_exhausted 为 Cursor 服务端账号配额限制
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
