# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 插件集合 `cpa-workbuddy-plugin`——将腾讯 CodeBuddy（WorkBuddy）与 QoderWork CN 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式推理、每日签到、积分生命周期与 token 用量统计。
- 范围：四插件（workbuddy-provider / qoderwork-provider / traework-provider / workbuddy-token-usage）的迭代、测试、发布、生产部署与 registry 同步；QoderWork / Trae SOLO 逆向知识维护。
- 非范围：CLIProxyAPI 网关本体；CPA 内置调度器逻辑的修改（插件只做 host 契约适配）。

## 项目概览

- 状态：活跃维护中。生产现役与 registry 对齐（2026-10-01 18:21 发布）：workbuddy-provider **0.15.1** / traework-provider **0.2.1** / qoderwork-provider **0.9.21**（面板筛选标签计数随积分回填重算）；workbuddy-token-usage **0.2.2**。路由健康闸门仍为「测试」标签 + 冷却 + 低积分优先。历史发布细节见「已完成」区。
- 活动会话数：1（本会话独占工作树 F:\cpa-plugin）
- 更新时间：2026-10-01 (GMT+8)

## 活动会话任务摘要

- 当前会话（2026-10-01）：**修复面板筛选标签计数不随积分回填重算，三插件已发布部署（workbuddy 0.15.1 / traework 0.2.1 / qoderwork 0.9.21）**。
  - 用户反馈：WorkBuddy 账号面板筛选标签「可用 51 / 耗尽 0」与同屏汇总卡「21 个账号 · 可用 20 · 耗尽 0」自相矛盾。
  - 根因（唯一写入口被调用一次）：`updateFilterCounts()` 只在 `load()` 里调用过一次；积分回填链路的其余重绘入口（`filterRegion` / `renderGrid` / `updateOneCard`）只调用 `renderSummary()`，从不重算标签计数。冷启动 `accountCache` 为空 → 所有账号 `credits=null` → 首屏判「无耗尽证据即可用」写死 `可用=51 耗尽=0`；后台刷新回填真实 credits 后只有汇总卡重算，标签长期停在旧值。
  - 修复：标签计数挂到统一渲染入口 `renderSummary()`（三插件同构），`load()` 去重；workbuddy 新增 `isAccountExhausted()`，把标签计数 / `accountsForFilter` / 卡片徽标 / 汇总卡四处各自展开的「耗尽」判定收敛为单一函数（与后端 `billing.go:isCreditsExhausted` 同口径）；traework / qoderwork 仅挂钩 + 去重，保留各自既有口径（qoderwork 仍为旧版「保号池」口径）。
  - 验证：新增长期回归资产 `test/workbuddy/panel_filter_counts_repro.mjs`（Node `vm` + DOM 桩真实执行面板内联 JS，支持三插件参数、自带断言与退出码）；修复后三插件 PASS（cntAvailable=21 / cntExhausted=30 / 标签与筛选自洽），对 HEAD 版本反证 FAIL 3 项（51/0）精确对应截图；`cgo-shim-build.py` 三插件 build/vet/test 全绿（workbuddy 11.271s / traework 1.707s / qoderwork 7.115s）。
  - 文档：Bug 主文档 `doc/4-bugs/2026-10-01_165945_账号面板筛选标签计数未随积分回填重算.md`（status: fixed-verified）、测试主文档 `doc/5-tests/2026-10-01_172233_账号面板筛选标签计数回归.md`、`doc/6-review/2026-10-01_172233_..._6-review.md`（STYLE: PASS）。
  - 发布链（2026-10-01）：`df2c92c`(fix 16 文件 +620/-15)→`35a448b`(chore assets 24 文件，21/21 sha256 OK)→`c424c19`(chore registry)；CI run `36844068571`/`36844075308`/`36844081798` 同 commit `df2c92c` 三 success（Release job 均成功）。发布前曾误传 `version=AUTO` 派发三个 run，已在 release 阶段前全部取消（`conclusion=cancelled`），仓库无 `*-vAUTO` 假 tag / Release / registry 污染，随后以真实版本号重新派发。
  - 远端 raw ALL PASS（三插件 21 artifacts size+sha256 全对、旧版零残留）；生产 plugin-store install 三插件落盘 `.so` sha256 与本地 zip 内一致（`8b317eff` / `801a9096` / `68748a36`）+ hot reloaded active=0.15.1/0.2.1/0.9.21（retired=0.15.0/0.2.0/0.9.20）+ accounts/panel 全 200。
  - 生产面板标签实测（本轮核心验收，用生产 `panel` HTML + 生产 `/accounts` 数据在 Node vm 中真实执行）：workbuddy 51 账号「可用 21 / 耗尽 30 / 失败 3 / 测试 0」与各筛选结果**逐项自洽**（截图中「可用 51 / 耗尽 0」的错误形态已不复现）；traework 2 账号同样自洽。

- 当前会话（2026-09-30）：**移除保号池（preserve）机制，路由排除改由「测试」标签（`test_failed`）承担（workbuddy 0.15.0 / traework 0.2.0 已发布部署）**。
  - 用户口径：「保号已基本无意义，可以去掉，用测试标签代替」；`token keepalive`（登录态续期，traework 面板「保号刷新」按钮 / `token_keepalive` 配置）属登录态续期，**不在删除范围**。
  - 关键设计：保留「定时活跃探测 + 积分刷新」循环（`refresh_runner.doFetchOne` → `triggerActivePing` 失败写 `test_failed`），它是「测试」标签唯一自动来源；删除保号翻转语义后整体改名（`preserveWatchdogLoop`→`watchdogLoop`、`runPreserveWatchdogTick`→`runWatchdogTick`、`requestPreserveTick`→`requestWatchdogTick`、`preserveTickCh`→`watchdogTickCh`、`preserveWatchdogStartupWait`→`watchdogStartupWait`、`preserveWatchdogReadyPoll`→`watchdogReadyPoll`，删 `preserveWatchdogDisabledPoll`），固定 `watchdogIntervalDefault = 10 * time.Minute`；`waitHostReadyForWatchdog` / `hostReadyForWatchdog` 名字不变。
  - 改动清单：两插件删 `preserve.go`，`watchdog.go` 重写为纯看护循环；`scheduler` / `active_auth` / `failover_retry` / `session_auth` 硬排除只留 `isTestFailed` + `isAccountCoolingDown`；workbuddy 删 `usage_config.go` 三个 `preserve_*` 解析、`panel.go` `Preserve` 字段与输出、`panel.html` 保号按钮/badge/`data-preserve`/`cntPreserve`、`lifecycle.go` `preserveSetClear`、`credits_handler.go` 标签映射 `"preserve"`→`"test_failed"`、`anomaly_purge.go` 迁入 `authFileErr`/`errAuthIndexRequired`/`errAuthMissing`；traework 删 `scheduler` `isAccountPreserved` + 过滤链、`management.go` `refreshPreserveSetFromDisk`/`Preserved:`/`"preserve"` map/`preserveSetClear`、`config.go` 三个 case + `parsePositiveIntLine` + `"time"` import、`main.go` 3 条 ConfigFields、`panel.html` 保号 CSS/badge/筛选按钮/`filterClass` 分支/排序权重/`cnt.preserve`/`accountsForFilter` 分支/`isUnavailable` preserve/`scopeLabel` 分支。
  - 测试：`watchdog_test.go` 两插件重写（`TestWatchdogIntervalDefault`/`TestWaitHostReadyForWatchdog`/`TestRequestWatchdogTickCoalesces`）；`routing_exclusion_test.go` 删 `resetPreserve`、改名 `TestEnsureDefaultActiveAuth_SkipsTestFailed`；`auth_delete_test.go` 删 preserve 断言。
  - 文档：workbuddy `README.md`/`README_CN.md` 删「Preserve pool」章节改「Test tag（测试标签）」；`docs/architecture.md` 判定链改 `isTestFailed`/`isAccountCoolingDown`（disabled→test_failed→cooldown）；两插件 `CHANGELOG.md` 顶部新增 `Unreleased` 段（**未 bump 版本号**）。
  - 验证（发布前复跑）：cgo-shim 双插件 build/vet/test 全绿（workbuddy 11.229s / traework 2.022s）+ 哨兵法证明新测试真实进编译 + 双 `panel.html` 4 个 script 块 node 校验全绿 + 本轮零新增 gofmt 抱怨（残余为历史漂移，按变更最小化不修）+ grep 确认 `isPreserve`/`preserveSetPut`/`refreshPreserveSetFromDisk`/`preserveShouldFlip`/`parsePreserveFromAuthJSON`/`preserveThreshold` 等全部 0 命中（仅剩 `preserveExpiry` 属 token 过期保留、`token_keepalive`/「保号刷新」属登录态续期）+ `6-review` STYLE PASS（`doc/6-review/2026-09-30_000524_...`）。

  - 发布链（2026-09-30）：1e4a571(refactor 47 文件 +524/-1791)→5e08deb(docs 默认提交发布授权写 AGENTS.md/CLAUDE.md)→8200656(chore assets 16 文件 7/7 sha256 OK)→55506b6(chore registry)；CI run 36603938568/36603955142 同 commit `5e08deb` 双 success；远端 raw ALL PASS（14 artifacts size+sha256 全对、旧版零残留）；生产 plugin-store install 落盘 .so sha256 与本地 zip 内一致（workbuddy `4727e7bb…3610` / traework `2d61f96f…36e8`）+ hot reloaded active=0.15.0 retired=0.14.43 与 active=0.2.0 retired=0.1.68 + accounts/panel 全 200。
  - 生产行为验收：`POST https://cpa.luode.vip/v1/responses` 流式 qwen3.8-max 16s 返回 56655 字节 / 144 帧 delta + 29 帧 reasoning delta + `response.completed`，末尾 nonce 命中 7 次；关联日志 `exec stream async scheduled: model=qwen3.8-max stream_id=10431` → `exec stream async done: attempt=1 chunks=174`（无 pseudo retry / pool exhausted）。
  - 面板结构验证：`/accounts` 两插件均无 `preserve` 键、含 `test_failed` 字段（workbuddy 51 账号 / traework 2 账号，当前 test_failed=0）；workbuddy `active_auth` 正常。
  - 仓库规则变更：AGENTS.md + CLAUDE.md 将 `## 严禁自动提交 Git` 段替换为 `## 提交 / 发布授权（默认授权，强制）`——本仓库默认处于「提交/发布已授权」状态，用户当轮显式边界绝对优先。
  - 授权规则收口（2026-09-30 02:10）：AGENTS.md / CLAUDE.md 各 51,158 → 51,646 字节（纯 CRLF），新增一条「防回刷说明」——明确该段覆盖旧受管章节 `## 严禁自动提交 Git`，并注明其真源在另一项目 `luode-skills` 的 `bootstrap_agents.sh` 的 `BODY_NO_AUTO_COMMIT`，本会话按跨项目写入红线不能代改。**已提交推送**（本轮相关提交，按序）：`84265d6`(docs 记忆同步) → `7758178`(docs 修正 HISTORY 计数锚点) → `9e98df0`(docs 防回刷说明) → `a70b7dd`(docs 收口记录与阻断项) → `f7485fc`(docs 6-review 风格回归) → 后续仅追加记忆口径校准的 docs 提交；每次提交后 HEAD 与 `origin/main` 一致。
  - 项目记忆同步（本轮）：`PROJECT_CURRENT.md` 记录 0.15.0/0.2.0 发布链与行为验收；`PROJECT_MEMORY.md` 新增「仓库默认处于提交/发布已授权状态」条目，并把路由口径条目改为「硬排除两类标签」（保号池已移除）；`PROJECT_HISTORY.md` 置顶 2026-09-30 事件。`check_memory_anchors.py` 由 C4 两处告警修到 `ok=true`（20 事件 / 20 锚点）；本轮 HISTORY 窄读计数回写 `usage_count=1` / `usage_days=1` / `last_used_at=2026-09-30`。

- 当前会话（2026-09-29 凌晨）：**账号路由口径改造「优先可用账号 + 硬排除三类标签 + 低积分优先」，workbuddy 0.14.43 / traework 0.1.68 已发布部署**。
  - 用户 goal：优先走可用账号；测试（`test_failed`）/ 保号（`preserve`）/ 冷却（failover cooldown）三类标签硬排除、不参与路由；低积分账号优先路由以便尽快用完。
  - 改造前现状核对：workbuddy 是「健康层优先高积分」（0.14.40），`test_failed` 在调度链路**完全没有排除**，保号「全部保号时回退全量列表」等于又把不可用账号放回路由；traework 另有真实缺陷——`refreshPreserveSetFromDisk()` 全仓无调用点，重启后保号账号仍被路由命中。
  - 落地：①`handleSchedulerPick` 三段硬排除（保号/测试/冷却），全被排除时 `Handled:false` 交还宿主做跨 provider failover，删除「回退全量」兜底；②`sort.SliceStable` 按缓存积分升序（未测积分 -1 排最后），取代 0.14.40 的高积分优先；③新增共享判定 `accountRoutable` / `accountLowerCredits`，`pickActiveAuth`、`ensureDefaultActiveAuth`、`pickSessionAuth`、`scheduler.pick` 共用同一「可用」口径，面板选中项改为「可用账号中积分最低者」；④请求内换号 `pickNextAuth` 同样跳过测试/保号，但保持宿主顺序不排序（重试链可预测）；⑤新增 `testFailedSet` 内存镜像 + `refreshTestFailedSetFromDisk`，在面板构建 / 保号 watchdog tick / 标签直写三处同步，重启后仍正确排除；⑥traework 补齐 `refreshPreserveSetFromDisk` 调用点（`handleAccounts` + `runPreserveWatchdogTick`）；⑦修复 `traework/cachedCreditsScore` 空指针（缓存条目存在但 credits 未拉取时原代码直接解引用 panic，归一为未知积分 -1）。
  - 验证：cgo-shim 双插件 build/vet/test 全绿（workbuddy 11.45s / traework 2.23s）+ 哨兵法证明新测试真实进编译 + 双 `panel.html` 4 个 script 块 `node --check` 全绿 + gofmt 干净（`workbuddy/watchdog.go`、`traework/watchdog.go` 的差异经 HEAD 同法校验确认为历史注释缩进漂移、本轮不修）。
  - 发布链：cbb18b6(workbuddy feat)→a9a893a(traework feat)→1c792cb(assets 14 文件)→b33613c(registry)；CI run 36466163784 / 36466175909 同 commit `a9a893a` 双 success；远端 raw ALL PASS（14 artifacts size+sha256 全对、旧版零残留）；生产 plugin-store install 落盘 .so sha256 与本地 zip 内一致（workbuddy `21ba3df2…63952` / traework `872a32e3…62f2`）+ `hot reloaded active_version=0.14.43 retired=0.14.42` / `active_version=0.1.68 retired=0.1.67` + accounts/credits/panel 全 200。
  - 生产行为强证据：`/credits` + `/accounts` 交叉计算出的「可用账号积分最低者」= `16226361146 [CN]`（remain=31），与面板 `active_auth` 完全一致（选号口径生效）；真实 `POST /v1/responses` 3s 返回 200 且 nonce 完整，日志显示由 traework-provider 承载（`session-affinity: LCP cache miss, new binding | auth=traework-2033439621254311.json` + `stream aggregate done ... termination=done finish=stop`）。生产 51 个 workbuddy 账号当前 `test_failed=0`（标签由定时活跃测试失败时写入，本轮暂无失败样本，标签链路由单元测试覆盖）。

- 当前会话（2026-09-27 晚）：**workbuddy 0.14.42 面板创建时间改用账号真实创建时间（JWT auth_time）已发布部署**。
  - 用户反馈：面板「创建」时间与「快照」时间仅差 1 分钟，明显不是真实创建时间。
  - 根因（双证据锁定）：面板此前取宿主 `HostAuthFileEntry.CreatedAt`，而宿主 watcher 每次扫描都用 `time.Now()` 重新赋值（`internal/watcher/synthesizer/file.go:114`），因此该值会随积分刷新 / 保号 keepalive / 活跃测试写入漂移到「最近写入时刻」；生产 48 个账号物理 auth JSON 顶层无任何 created_at 字段，文件 mtime/ctime 亦为最近写入。
  - 修复：新增 `workbuddy/created_at.go` 解析 accessToken JWT payload 的 `auth_time`（登录时写定、token 刷新不变，生产 48/48 账号全有值），`panel.go` 优先采用，仅缺字段时回退宿主值；新增 `created_at_test.go` 8 用例（含 base64 padded 段、缺字段、零值/负值、payload 非 JSON、两段式、空串）。
  - 验证：cgo-shim build/vet/test 全绿 + 哨兵（篡改 +1s → 2 用例 FAIL 证明真实进编译）；发布链 cb0a2be(fix)→f44eb08(assets 8 文件, 7/7 sha256 OK)→6736c90(registry)；CI run 36327182344 success；远端 raw ALL PASS（7 artifacts sha256 一致 + 旧版零残留）；生产 install 落盘 sha256 `cc519d97...` 与本地一致 + hot reloaded active=0.14.42 retired=0.14.41 + accounts/panel 双 200；行为验收：`242e1dde` 创建时间由错误的「09-27 22:12」变为真实「08-20 23:11」，51 账号出现 50 个不同时间点；生产流式请求 200 + 104 帧 + 完整正文 + finish_reason 收尾（429 换号后仍成功，failover 正常）。

- 当前会话（2026-09-26）：**WorkBuddy 与 TraeWork 刷新账号自动活跃与面板手动测试按钮功能已实现**。
  - 扩展：在 WorkBuddy 与 TraeWork 面板卡片底部新增「测试」按钮；后端新增 POST /test-active 接口，手动点击绕过 30 分钟防抖立即发起一次真实推理，成功后显示所选模型与响应耗时并更新活跃时间戳，失败展示错误原因。
  - 核心设计：在 `refresh_runner.go` 的 `doFetchOne` 成功刷新积分后触发轻量活跃请求（`user: "hi"`，`max_tokens: 5`），随机挑选账号可用动态模型（workbuddy 兜底 deepseek-v4.1-flash，traework 兜底 claude-3-5-sonnet）。
  - 故障隔离与节流：内置 30 分钟防频繁活跃节流保护（`defaultActivePingInterval = 30 * time.Minute`）；活跃探测失败仅记 warning 日志，绝不阻断或污染 `doFetchOne` 的核心返回值与账号故障降级状态。
  - 验证：通过 `cgo-shim-build.py` 完成 build、vet 以及包含哨兵用例在内的测试，`workbuddy` 与 `traework` 两个插件全绿通过。

- 当前会话（2026-09-14 凌晨，**跨平台 failover 双根因修复，traework 0.1.60 / workbuddy 0.14.31 / qoderwork 0.9.17 已发布部署**）：用户报 trae 全部账号失败后不切 workbuddy 直接失败。生产取证（stream 1253/1298/1326）坐实**两个互补根因**：①并行会话发现并已修复的调度层短路——插件 Scheduler 以 `Handled:true` 返回死账号，阻断宿主跨 provider 兜底（全部耗尽时改 `Handled:false` 延迟给宿主，scheduler/session_auth/active_auth 三处）；②本会话独立发现并修复的错误通道伪装——`streamEmitError`/`emitTraeAsyncError` 把终态错误当 payload 数据帧（`{"error":...}` SSE 事件）发出，宿主 conductor 视为正常流内容、请求以 200 "成功"告终，不轮换凭据/不冷却/不跨平台切换；改走 `host.stream.emit` 信封 `error` 字段（→ `chunk.Err`），traework 另加 `EmitError` 注入点。验证：cgo-shim 三插件全绿（含信封字段断言 + payload 泄漏哨兵 + 既有 5 个异步流测试迁移到错误通道断言）；发布链 40834b0(fix 27 文件)→d44767d(assets 24 文件，21/21 sha256 OK)→bb494d4(registry)；CI 三 run 34768147600/34768143943/34768140125 全 success；远端 raw ALL PASS + 旧版零残留；生产 install 三插件落盘 sha256 与本地一致（db9712be/5fdbf1c8/846b16ee）+ hot reloaded active=0.1.60/0.14.31/0.9.17 + 特征串 emitStreamErrorEnvelope 各 1 次 + accounts/panel 双 200；行为回归 deepseek stream 1741 attempt=1 完整 done、glm 经 workbuddy 正常。**观察项**：trae 全灭→workbuddy 接管的完整链路需真实全灭场景验证（下一次两 trae 账号同时失败时看 conductor 是否切 workbuddy）。qoderwork 的调度器层对齐（Scheduler capability 短路同病）未做，生产无 qoderwork 凭据、低优。沉淀知识库《插件终态错误伪装成成功流会让宿主跨平台failover永不触发》。

- 当前会话（2026-09-08，**三插件移除异常池 + MANUAL-TOGGLE-ONLY 固化，已发布部署**）：用户需求——①去掉"连续 3 次创建失败进异常池"逻辑，统一失败进冷却（固定 15s）；②审计停用路径，强制只有用户手动停用才能停用账号。决策（AskUserQuestion 确认）：三插件同步移除 / 启动时批量清理存量 anomaly:true / 保留连败计数与徽标（仅断开冻结联动）/ 审计确认 + 注释固化。六任务全完成：TASK-01/02 traework 后端+面板；TASK-03 workbuddy 后端+面板（含 usage_config.go anomaly 配置解析块、main.go ConfigFields、scheduler/active_auth/failover_retry/session_auth/management 全链清理）；TASK-04 qoderwork 同构移除（含 counter.go 落盘挂点注释漂移修正 → preserveWatchdogLoop:284、preserve.go 迁入 authFileErr 错误 helper）；每插件新增 anomaly_purge.go + 四态测试（watchdog 启动剥离遗留 anomaly:true 死字段）；TASK-05 停用复扫（traework 唯一写入口 persistDisabledToggle / workbuddy disableAuth auto 路径只透传 / qoderwork 无停用通道）+ 三插件入口固化 MANUAL-TOGGLE-ONLY POLICY 注释；TASK-06 版本 bump 0.1.55 / 0.14.26 / 0.9.14 + 三份 CHANGELOG 条目。验证：cgo-shim 三插件 build/vet/test 全绿 + panel.html node --check 全绿 + grep 零残留。**发布链完成（用户授权"完成后发布"）**：dcdeb95(feat 70 文件 +722/-2210)→fef8df4(assets 24 文件)→48941a2(registry)；CI 三 run 34151835600/34151839572/34151843275 全 success（runner 排队跨两轮轮询窗口，踩坑 45 模式）；远端 raw ALL PASS（21 artifacts sha256 一致 + 旧版零残留）；生产 install 三插件落盘 sha256 与本地一致（661376b6/a2460b39/209ff927）+ hot reloaded active=0.1.55/0.9.14/0.14.26 + accounts/panel 双 200 + accounts 响应零 anomaly/unfreeze 键（行为验证）。踩坑：panel.html scopeLabel 多级三元删层后括号层级必错（traework/workbuddy/qoderwork 三连），手改不可靠 → 用 Python 程序化删除三元层并断言 `count('(')==count(')')` 后写回。

- 当前会话（2026-09-02，Trae 异步流式宿主流桥打开超时降级直连，**0.1.28 已发布部署 + 生产流式长推理验收 PASS**）：见下方「已完成」0.1.28 条目；该版本只覆盖宿主流桥 open 阶段挂死，read 阶段挂死由本会话 0.1.29 修复承接。

- 并行会话（0.1.11→0.1.15 发布链已完成）：WAF UA 加固（0.1.11）/ content parts 数组 4001 修复（0.1.12）/ 对话签到 host 分离（0.1.13）/ 动态模型发现（0.1.14）/ usage_feed 适配 token-usage-tracker（0.1.15）+ workbuddy 0.14.18 定时刷新稳定性修复
- 关键新铁律：dispatch 必须传 plugin(provider id)+version；download/publish 脚本参数顺序互反；store install 的 CDN 边缘滞后误报（等几分钟重试）；生产部署唯一路径 plugin-store install；并行会话 checkout/reset 会覆盖未提交改动（发版前先 fetch 对齐或及时提交）

## 已完成

- 2026-09-08 **traework 0.1.56 面板 scopeLabel hotfix（本会话）**：用户报面板"加载账号失败 / scopeLabel is not defined"。根因：0.1.55 清理 panel.html 时 `renderSummary` 的 `const scopeLabel=` 三元链被误删前缀（残留语法合法的孤立表达式，node --check 抓不到，运行时 ReferenceError 中断 renderSummary）。修复：程序化重建完整定义（all/available/preserve/exhausted/failed/disabled 6 分支 + fallback，括号由代码生成断言平衡）。验证升级：scopeLabel 运行时 7 分支断言 + vm+DOM stub 真实执行三插件面板 JS 顶层全过（新验证手段）；cgo-shim traework 全绿；dcdeb95 diff 逐行复盘确认无其他残行。workbuddy/qoderwork 零改动不 bump。发布链 f8ac3af(fix 6 文件 +18/-6)→646a407(assets 8 文件)→d0ac6eb(registry)；CI run 34233803018 success（8m9s）；远端 raw ALL PASS（7 artifacts sha256 一致 + 旧版零残留）；生产 install status=installed restart_required=false + 落盘 sha256 与本地一致（1e3b4af7）+ hot reloaded active=0.1.56 retired=0.1.55 + accounts/panel 双 200 + 生产面板 HTML grep `const scopeLabel` 命中 1 次（定义真实存在）。
- 2026-09-08 **三插件移除异常池发布部署（traework 0.1.55 / workbuddy 0.14.26 / qoderwork 0.9.14，已发布）**：删除三插件 anomaly.go/anomaly_config.go（连败冻结、异常集合、每日复活、/unfreeze 路由、anomaly_pool_threshold/anomaly_refresh_enabled 配置与解析）；accountFailover 冻结判定删除、失败只进 15s 冷却；scheduler/active_auth/failover_retry/session_auth anomaly 过滤层与谓词全清；panel.html 异常徽标/解冻按钮/异常筛选/计数与汇总口径全清；新增 anomaly_purge.go（watchdog 启动剥离遗留 anomaly:true，幂等不盲写）+ 四态测试；qoderwork counter.go 挂点注释漂移修正 + preserve.go 迁入 authFileErr helper。停用复扫：traework 唯一写入口 persistDisabledToggle（仅手动 toggle）/ workbuddy auto 路径只透传 disabled / qoderwork 无停用通道；三插件入口固化 MANUAL-TOGGLE-ONLY POLICY 注释。发布链 dcdeb95→fef8df4→48941a2；CI 三 run success；远端 raw ALL PASS（21 artifacts sha256 一致）；生产 install 落盘 sha256 一致 + hot reloaded active=0.1.55/0.9.14/0.14.26 + accounts/panel 双 200 + 行为验证零 anomaly 键。踩坑 50：发布前 VERSION 文件漏 bump（main.go 已改）→ push 前 `cat */VERSION` 与 main.go 两两核对。
- 2026-09-08 **三插件每 4 小时调度批量发布部署（traework 0.1.54 / workbuddy 0.14.25 / qoderwork 0.9.13，历史会话）**：自动签到从每日两班（09:00/21:00）改每 4 小时六班（00/04/08/12/16/20 本地时间），token 保活从每日 22:00 单次改与签到同节奏，缩小 Keycloak 离线会话失效窗口；注释/ConfigFields 描述/panel schedule/测试用例（workbuddy nextCheckinTime 三用例、traework keepalive 窗口用例）同步。动机背景：analysis/traework-0.1.50-keepalive-2200-acceptance-20260906.md 记录 22:00 每日保活的生产行为不一致回归。顺带修复 workbuddy/qoderwork checkin.go 注释 tab 缩进错乱；随批提交 SKILL.md 踩坑 47/48。验证：cgo-shim 三插件全绿。发布链 d43caea→9a424f5→a30a7e6；CI 三 run success（并行排队 25+ 分钟跨两个轮询窗口，踩坑 45 再次验证）；远端 raw ALL PASS；生产 install 三插件落盘 sha256 一致（c1c6a931/c70b4d4b/07ca3f70）+ hot reloaded 全部生效（qoderwork 本轮无踩坑 48 现象）+ accounts/panel 双 200 + 生产 dashboard schedule 已是新班表。
- 2026-09-05 traework **0.1.42 已发布部署**（浏览器授权登录适配 TRAE 授权页真实回调形状，本会话）：用户真机实测 0.1.40 实锤根因——TRAE 授权页登录成功后的跳转**不是标准 OAuth 302**，不回传 code/state，授权码在 `authCodeInfo={"AuthCode",...,"ExpireDuration":600000}` JSON 参数里（参数集 isRedirect/scope/authCodeInfo/loginTraceID/host/userRegion/userInfo）。0.1.40 submit 只按 `?code=&state=` 解析必报「缺少 state」。修复：`extractAuthCode`（?code= 优先、authCodeInfo JSON 回落）+ 会话定位链（start 响应新增 state 字段 → 面板 browserLoginState 带回 body.state → URL state → 最新 pending 会话兜底 newestPendingSession）+ callback/submit 双通道同构。验证：cgo-shim 全绿（1.310s）+ 哨兵（破坏 authCodeInfo 解析 → 两个新测试 FAIL → 还原全绿）+ panel JS OK + gofmt 干净。发布链 98b81e0(fix 5 文件 +313/-43)→a60038d(assets)→e0b1850(registry)；CI run 33897217089 success（12m10s）；远端 raw ALL PASS（7 平台 sha256 一致，0.1.37~0.1.41 零残留）；生产 install 落盘 sha256 `c5150057` 与本地一致 + loaded/registered 0.1.42 热重载 + restart_required=false；生产冒烟：authCodeInfo 形态 + 假 state → 「授权会话不存在或已过期」、无 state 无会话 → 「无法定位授权会话」（均为 0.1.42 新语义，证明解析与定位链生效）。闭环标志：用户重测真实登录导入。
- 2026-09-04 traework **0.1.39 已发布部署**（浏览器授权登录三缺陷修复，本会话）：生产端到端验证 0.1.38 暴露三缺陷——BUG-A 宿主 `ServeManagementHTTP` 对 management JSON 响应强制 `htmlsanitize`（`&`→`&amp;`，授权页参数解析必挂）→ 面板 `browserLogin()` 打开前 `replaceAll("&amp;","&")` 兜底（resource 路由响应不转义）；BUG-B callback 注册在 management 前缀被宿主 management key 中间件拦截（`pluginManagementNoRoute` 对含 GET 全路由强制鉴权）→ 移入 `Resources` 挂免鉴权 `/v0/resource/plugins/<id>/browser-login/callback`（无 Menu 不进管理 UI 菜单）+ resPrefix 分支子路径分发；BUG-C 授权 URL 缺 OAuth `state` 参数（授权服务器无法回传、callback 查会话必落空）→ `q.Set("state", state)`。新增 2 契约测试（start 指向 resource 回调+state+S256；注册表 callback 仅在 Resources 且无 Menu）。发布链 bf6ba87(fix 7 文件)→CI dispatch(traework-provider/0.1.39)。验证：cgo-shim 全绿。
- 2026-09-04 traework **0.1.38 已发布部署 + 生产部署验证 PASS（端到端复验移交 0.1.39）**（浏览器授权登录：免 IDE 完整 OAuth 导入，本会话）：逆向 TRAE SOLO CN 0.1.62 `main.js` 确认浏览器授权码 + PKCE(S256) 流程（授权 URL 参数形状、ExchangeToken v3、GetUserInfo v3、refresh 设备无关、ClientID 双值 Bb()）。`browserlogin.go` 三路由（start: PKCE 对+EC P-256 设备密钥+随机指纹+origin 校验；callback: 换 token→取用户→UserID 去重入库→回跳页三重跳转；result: 读后即焚不含凭据，pending 不消费）+ `panel.html` 工具栏按钮 + `?auth_cb=` 回跳轮询。修复自审缺陷 2 处（callback outcome 未存回 map / result 过早轮询误删 pending 会话）。发布链 b5f105c(feat 11 文件)→babd598(assets 7 平台)→ea57fee(registry)；CI run 33788701204 success（约 11 分钟）；远端 7 平台 sha256 ALL PASS；生产 install 落盘哈希一致 f98950e4… + hot reloaded active=0.1.38 retired=0.1.37 + restart_required=false + panel 200 含按钮 + start 无 key 401 + accounts 200 + 流式回归 stream_id=1294 attempt=1 chunks=12 健康。生产复验暴露三缺陷 → 由 0.1.39 修复。
- 2026-09-03 traework **0.1.33 已发布部署 + 生产验收 PASS**（usage feed 补齐会话/首字延迟列，本会话）：publishUsage 8→12 参数链对齐 workbuddy，session_key 执行器入口提取跨换号冻结，ttft 由 collectTraeStream 四值返回 / pumpTraeStreamAttempt.FirstOutputAt 观测，ttftNSBetween 统一计算；哨兵 FAIL 证明真实生效；cgo-shim 全绿。发布链 86d3829(feat 11 文件)→11c183e(assets 7 平台)→dbf2683(registry)；CI run 33765271408 success（9.7 分钟）；raw 远端 ALL PASS 无旧版残留；生产 install + 22:27:51 loaded/registered version=0.1.33 + 落盘 sha256 ae1e9ae7 与本地一致 + 特征串 ttftNSBetween/session_key 实锤 + accounts/panel 双 200。生产行为验收：轻量流式请求后 feed 新增记录 session_key 非空 + ttft_ns≈1.45s 非零，dashboard 两列生效。CHANGELOG 顺带补录 0.1.32 缺失条目。
- 2026-09-03 traework **0.1.32 已发布部署**（工具调用链路 P1+P0 修复，本会话）：P1 伪完成豁免工具短流（collectTraeStream 三值 + hasToolCalls 贯通 + pump gate 放行）；P0 完整工具链（tools 上行透传 + parameters stringify + function↔function_call 双向翻译 + 三路径 tool_calls delta + finish=tool_calls）。toolchain_test.go 8 + pseudo_toolcalls_test.go 4 用例取证 SSE 全绿；真实上游双阶段 e2e 闭环。发布链 16d8bc5→4dd3fd4→14527f9；CI run 33666121945 success（11m27s）；生产 hot reloaded active=0.1.32 retired=0.1.31（02:29:53 同秒）；落盘 sha256 4a4546d9 与本地 zip 一致；accounts 200/panel resource 200。行为级验收（stream#3206/3208 不再「回答不完整」）待用户真实工具链流量观察。
- 2026-09-02 traework **0.1.30 已发布部署 + 生产验收 PASS**（三缺陷修复，用户否决账号归因）：FIX-A 伪完成同号退避重试（sync+async 收敛，仅 `PickNextAuth` 无候选才同号退避 1 次，不耗跨账号 Budget）；FIX-B async 401 open error 补核算+驱逐绑定；FIX-C `isPseudoCompletion` content+reasoning 双计（任一达 600 健康 / reasoning-only 永不判伪）。新增 executor_same_auth_retry_test.go（负向哨兵+定向探针证明真实编译执行）；既有 5 伪完成回归+reasoning-only 豁免全绿；cgo-shim 全绿。发布链 d027734(fix)→3d74d2d(assets)→37d1b86(registry)；CI run 33645466299 success；生产 install 0.1.30 + active=0.1.30 retired=0.1.29 + 落盘 sha256 一致。生产验收（CYCLE-04）：形态 A 用户真实短请求 5 连发全绿（1.8-4.3s 完整）+ 形态 B 常规长推理 99.2s/9360 字符完整（stream 2876），无伪完成误判、无换号、无池耗尽。
- 2026-09-02 traework **0.1.31 已发布部署 + 生产复验 PASS**（FIX-D reasoning 阶段零字节 504）：CYCLE-04 形态 B2 深度思考请求（quickselect 推导）504——stream_id=2878 scheduled 后 5 分钟零字节、无 done/error/degrade，nginx `proxy_read_timeout 300s` 掐断。根因：`pumpTraeStreamAttempt` gate 只累计 content（`contentChars += len(text)`），reasoning-only 分片无限压 pending 不转发（桥健康、上游 reasoning 持续流入，但插件→客户端 300s 零字节）。FIX-D：gate 改 content+reasoning 双轴健康度（`healthChars += len(text)+len(reasoning)`），reasoning≥600 即流式放行；真伪完成（双轴短合计<600）仍全程 pending→判伪丢弃，零泄漏不回归。新增 `TestPumpTraeStreamAttemptReasoningFlushes` 四断言（哨兵先 FAIL 后删证明真实编译），cgo-shim 全绿 + 6-review STYLE PASS（doc/6-review/2026-09-02_235500）。发布链 4477560(fix)→6d63e1d(assets)→25facac(registry)；CI run 33648734166 success；远端 raw ALL PASS；生产 install 0.1.31 + active=0.1.31 retired=0.1.30 + 落盘 sha256 c053981a 一致。生产复验（CYCLE-04r）：stream 2999 同 prompt 完整返回 8702 正文+55062 reasoning 字符、1232 chunks、490.7s、**首包 26.4s 到达**（reasoning 流式下发不再零字节），attempt=1 无换号；形态 A 短请求回归 4/5 完整（1 次为本机客户端 SSL 瞬时断，重试即过）。

- 2026-09-02 traework **0.1.29 已发布并部署生产（read 阶段超时降级直连），但验收结论已纠偏——agent 自发超长请求验证，非用户真实流量**：用户报 0.1.28 "完全不行"，生产直连复现 qwen3.8-max「分析项目」：插件直接客户端 `hostHTTPDoStreamDirect` 完整流式（327/264 事件，1.6-2.7 分钟），宿主桥 read 阶段在生产阻塞（stream_id=1945 scheduled 后 2 分钟零日志 → gin 499）。根因：`hostCall(MethodHostHTTPStreamRead)` 同步 cgo 无超时，阻塞在 host 侧无缓冲 chunk channel；`sharedHTTPClient` 120s 整体超时还会截断长流。修复：host_bridge.go 加 `hostBridgeReadTimeout=90s`（goroutine+select 竞速）超时返回 `errHostBridgeReadTimeout`，经 `hostStreamDirectFn` seam 降级 `hostHTTPDoStreamDirect` live 实时流（覆盖 0.1.28 只做的 open 阶段）；新增 `streamHTTPClient()` 无整体超时（DialContext 10s/TLSHandshake 10s/ResponseHeader 30s）；`hostHTTPStream` 增 req/bodyBytes 字段保存降级重开所需。新增 host_bridge_read_timeout_test.go 三用例，哨兵先 FAIL 后删除证明进编译。cgo-shim 全绿 + 静态门禁 PASS + 6-review `STYLE: PASS`。发布链：7424cd7（fix）→ 99f6177（assets 8）→ 706b85d（registry）；CI success；远端 raw 7 资产 ALL PASS；生产 install 0.1.29 + active=0.1.29 retired=0.1.28。**纠偏（2026-09-02 本会话生产取证）**：当时「生产验收 3 次完整（2139/2146/2154，208.6s/292.5s/298.4s）」是 agent 自发的**超长请求**（要求 10 章节/3000 字），非用户真实流量；用户真实形态是很多 ~10s 短请求。生产全量日志 grep degrade/timed out **零命中**——0.1.29 从未降级，90s read 超时从未误杀健康流，此前「90s 假杀+降级重连」推断不成立。0.1.29 的 read 修复**尚未被用户真实请求验证**（见「活动会话任务摘要」本会话纠偏行）。
- 2026-09-02 traework **0.1.28 已发布并部署生产，生产流式长推理验收 PASS**（异步流式宿主流桥打开超时降级直连，本会话）：0.1.27 生产直连复现 qwen3.8-max「积分够却一直失败」——非流式 `/v1/responses` 一次成功（13.3s 聚合路径），带 `StreamID` 异步流式请求 240s 无字节后宿主 499（stream_id=1664 仅 scheduled 一条日志）。根因：`hostCall`（cgo 同步无超时）在宿主流桥 **open 阶段**永久阻塞协调器 goroutine。修复：host_bridge.go 加 `hostBridgeOpenTimeout=30s` 竞速打开，超时/失败降级 `hostHTTPDoStreamDirect` live 实时流（边读边发不缓冲完整 body）；抽出 `hostBridgeAvailableFn`/`hostStreamOpenFn` 注入点；新增 host_stream_timeout_test.go 两用例（哨兵先 FAIL 后删除证明进编译）。cgo-shim 全绿 + 6-review `STYLE: PASS`。发布链 02dc323→b7ae103→a05b252；CI run 33535588336 success；raw 远端 7 资产 ALL PASS；生产 install 0.1.28 + 落盘 sha256 8ec5343f 与本地一致 + hot reloaded active=0.1.28 retired=0.1.27。生产验收：4 次流式 qwen3.8-max 长推理（stream_id 1850/1853/1856/1857，覆盖两账号 + 同 session 粘性，**agent 自发请求，非用户真实流量**）全部 `attempt=1` 完整 done，无挂死/499/伪完成；修复前 1664 场景闭环（1664 是用户 00:29 `/v1/responses` 真实请求，4m0s 499）。
- 2026-09-01 traework **0.1.26 已发布部署，但完成结论已撤回**：伪完成阈值修正为输出<600 字节且输入≥200 字节，但检测发生在正文/stop/close 下发之后，当前请求仍提前结束，不能恢复；同请求恢复由 0.1.27 承接。
- 2026-09-01 traework **0.1.25 已发布部署，历史结论已由后续版本推翻**：补充伪完成记账/会话驱逐/active_id 优先，只影响下一请求且失败账号少量内容仍下发，不能恢复当前请求；由 0.1.26→0.1.27 承接。
- 2026-09-01 **trae-local-verify 项目级 skill 创建**（辅助资产，本会话）：`skills/project-cpa-workbuddy-plugin-trae-local-verify-rules` 吸收"本地直连 Trae 上游验证账号推理"经验——5 步流程（临时目录→cgo-shim→verify_main.go→运行判定→清理）+ 复用解密/header/payload/SSE（decryptCredentialString / buildTraePayload / scanSSE / classify）+ 5 条踩坑（sharedHTTPClient 120s 截断长流式→自定义 client+context 10min、先 reasoning 后正文、storage.json 账号≠生产账号、Windows 直连、SSE output 双格式）；references 含 verify-main-template.md 完整模板 + source-notes.md。quick_validate.py PASS、同域冗余扫描无交叉、skill-audit 边界清晰。同步沉淀知识库笔记《长流式客户端Timeout会掐断SSE直连》（新账号 uid 2257747741770235 qwen3.8-max 2m37s/595chunk/2.4万字完整 done vs 生产账号 77tokens 短输出 → 账号级问题定案）。

## 待办

- 【已完成 ✅】traework **0.1.37 已发布部署 + 生产验收 PASS**（2026-09-04，并行会话 token_usage 接入，本会话代发）：解析上游 `event:token_usage` 真实用量，dashboard 输入/输出/思考/总 Token 列不再依赖估算（诊断见知识库《traework用量全靠估算不解析上游usage.md》）。改动：stream.go（traeUsageCollector + usageDetailFromTraeMap + collect/aggregate 返回值扩 5/3 值 + traeStreamAttemptResult.Usage + usageDetailForAttempt/ForCompletion 兜底 helper）；executor.go（非流式 handleExecExecute + 同步 runTraeSyncStream + 异步 runTraeAsyncStream 5 处全部改发真实 usage，失败路径空 Detail 不变）；测试 9 处调用适配 + 新增 token_usage_test.go 8 用例；cgo-shim build/vet/test 全绿（本会话代发前复核）。提交链 7f0068a feat → 98bcb66 assets（7 平台 ALL CHECKSUMS OK）→ 58f886b registry；CI run 33783473893 success（12m48s）；远端验证 ALL PASS（0.1.33~0.1.36 零残留）；生产 install：落盘 .so sha256 `1314d84f...016364` 与本地一致、内置版本 0.1.37、特征串 traeUsageCollector×1/usageDetailFromTraeMap×1、hot reloaded active_version=0.1.37 retired=0.1.36、accounts/panel 200。验证遗留：dashboard 四列真实值需发新 trae 请求观测（并行会话闭环项）。
- 【已完成 ✅】traework 面板删除方向纠正 **0.1.36 已发布部署 + 生产验收 PASS**（2026-09-04 凌晨）：0.1.35 误删「用量汇总」方向错误（红框实指「子系统状态」），本版纠正：**恢复用量汇总 5 卡 + 进度条，改删「子系统状态」4 卡**（保号池 watchdog/keepalive/lifecycle/异常池 + 上次保号刷新）。改动：恢复 `renderSummary()` 完整用量汇总渲染（含 `accountsForFilter()`/`creditOf()`、`.summary .pb` CSS）；删除 `renderSubsystem()` 与其调用、`.summary-item .d` 死 CSS；基线核对 0.1.34 diff 仅剩子系统状态删除。提交链 dd1d491 fix → ff57346 assets（7 平台 ALL CHECKSUMS OK）→ e951946 registry；CI run 33781254643 success（12m26s）；远端验证 ALL PASS（0.1.32~0.1.35 零残留）；生产 install：落盘 .so sha256 `366116c6...e73d1ca` 与本地一致、内置版本 0.1.36、二进制「用量汇总」3 次/「子系统状态」0 次/renderSubsystem 0 次、hot reloaded active_version=0.1.36 retired=0.1.35、accounts/panel 200、生产面板 HTML 实测（54936 字节）「用量汇总」3 次出现、「子系统状态」0 次。教训：截图红框指认须先确认区块归属再动手，0.1.35 误删已记入 CHANGELOG 警示。
- 【已完成 ✅】traework 面板移除「用量汇总」区块 **0.1.35（方向错误，已被 0.1.36 纠正并回退，2026-09-04 凌晨）**：误删顶部「用量汇总 · 全部账号」5 卡 + 进度条（子系统状态保留）。提交链 3b7a895 → 78b95c8 → c8dd31e；CI 33777388020 success；生产曾短暂运行 0.1.35（hot reloaded retired=0.1.34）。面板现行为以 0.1.36 为准，勿按 0.1.35 判断。
- 【已完成 ✅】traework 面板对齐 workbuddy B 组 + 契约修复 **0.1.34 已发布部署 + 生产验收 PASS**（2026-09-03）：feat commit 46b9a6a（13 文件含并行会话图标改动 assets/icons/TraeWork.png + registry logo 切换 + SKILL.md 踩坑 40）→ CI run 33772260511 success → assets 2cfd728（7 平台 ALL CHECKSUMS OK）→ registry 29d7cb8 → 远端验证 ALL PASS（version 0.1.34 / 7 artifacts size+sha256 一致 / 0.1.28~0.1.33 零残留）→ 生产 plugin-store install：落盘 .so sha256 `91e58c94...73f5b39` 与本地 linux_amd64 指纹一致、内置版本 0.1.34、特征串 checkin_today×3/fetchAndPatchCredits×3/total_remain×5、hot reloaded active_version=0.1.34 retired=0.1.33、accounts/panel 200、accounts 新字段 checkin_today(bool)/credits 四元组(int)/label/name 全就位（cool_until omitempty 无冷却账号缺席属正常语义）。契约修复 3 处（/refresh/status 去包装、fetchAndPatchCredits 局部更新链、__traeThemeSync 启动）+ 面板 9 项 + 后端配套全部上线。
- 【历史遗留】traework 对齐五件套（0.1.16 时代）：发布链路部分已被 0.1.19→0.1.34 多轮发布覆盖完成；若「保号池展示/keepalive 保号/lifecycle 停用/会话粘性路由」尚有未验证项，仅在上游报障时专项补验（同第 88 条口径）
- 【低优】历史版本「真实页面交互验证」遗留项（0.14.12 登录轮询去重 / 0.14.11 计数持久化 / 异步刷新 / 删除账号 / 启用禁用移除）：核心链路已由生产面板日常使用间接验证，专项验证仅在上游报障时补做
- 【观察】qoderwork 0.9.6 authFilePrefix 修复为预防性（服务器无 qoderwork 凭据，accounts 空属正常）；未来部署 qoderwork 凭据后确认账号列表可见
- 【卫生】工作树残留并行会话未跟踪 tmp 脚本 `scripts/tmp_poll_0110.py`，归其所有者清理

## 阻断

- 【需另开会话处理】`luode-skills` 的 `project-rule-file-bootstrap-rules/scripts/bootstrap_agents.sh` 中 `BODY_NO_AUTO_COMMIT` 仍是旧口径（「未当轮授权即禁止提交」），与本仓库新规则「默认提交/发布授权」冲突。该文件属另一项目，按跨项目写入红线本会话只读。影响：对 cpa-plugin 重跑自举脚本时，会按旧标题 `## 严禁自动提交 Git（最高优先级，强制）` 再次追加该章节。**已缓解**：AGENTS.md / CLAUDE.md 的「提交 / 发布授权」段已写入「出现该章节一律以本条为准并删除旧章节」。**待办**：在 `F:\luode-skills` 目录下新开会话，把 `BODY_NO_AUTO_COMMIT` 改为与 cpa-plugin 一致的默认授权口径（含当轮边界优先、不免除门禁、非本轮改动保护、仓库级覆盖全局）。
- 除上述一项外无其他阻断（Windows 无 CGO 属环境限制，验证走 cgo-shim-build.py，非阻断）。

## 验证

- 插件验证：`python scripts/cgo-shim-build.py <plugin>`（build+vet+test 全绿）+ 面板 JS `node --check`（占位符替换后）
- 发布验证：13 步链路（见项目 skill `project-cpa-workbuddy-plugin-release-rules`）→ 远端 raw 全量 sha256 → 生产 store install + hot reload 日志 + 接口 200

## 下一执行点

- 当前首要执行点：**观察新调度班表生产首跑**（0.1.54/0.14.25/0.9.13 已上线，2026-09-08 00:46 hot reloaded）：下个整 4 小时 tick（04:00 本地）后看生产日志 auto check-in / keepalive run 是否按新班表触发、无异常刷屏；面板 schedule 卡应显示六班。
- 次要：**用户重测浏览器授权登录**（traework 生产现役 0.1.54；链路 0.1.42 解析通 → 0.1.43 exchange 通 → 0.1.44 修 GetUserInfo 401）。用户动作：点按钮 → 登录 → 复制地址栏完整网址 → 粘贴提交；若仍失败携 submit 响应回修。
- traework 0.1.37 dashboard 四列真实值需发新 trae 请求观测（并行会话闭环项）。
- 0.1.32 行为级验收继续待用户真实工具链流量（stream#3206/3208「回答不完整」不再复现、工具轮完整 done；深度思考 504 修复 0.1.31 成果不回退）。
- 生产验证中观察到深度思考（reasoning 长）首包延迟约 26s 起持续有字节（0.1.31 已流式放行 reasoning），后续若用户觉得 reasoning 首包仍慢可评估预连接/保活优化；0.1.33 的 ttft_ns 落盘后 dashboard 可直接观测首字延迟分布，作为该优化的数据依据。
- workbuddy-token-usage **0.2.2 SSE 实时通知已发布部署**（2026-09-03；commit 链 0b2b1e4→45ba00b→bd6f929；生产 hot reloaded active=0.2.2 retired=0.2.1；落盘 sha256 e11da75a 一致；/usage/events 实测返回 `data: {"seq":4}`）。**剩余验证（需人工）**：真实浏览器打开 dashboard 页面时 feed 新增 usage 是否在 ~2s 内自动刷新（EventSource 短连接轮询 + 15s 轮询 fallback 仍在）。
- 上游断流源头排查（双管齐下第二路）：0.1.22 兜底只是缓解，根治需复现 qwen 长回答请求，抓上游 Trae 实际响应确认中途 EOF 是 Trae 服务端长回答限制还是网络层（请求期间 tcpdump / host 日志 / Trae 上游响应体抽样）。
- 【低优历史技术债】`traework/host_bridge_decode_test.go` 与 `traework/usage_feed_test.go` 仍位于源码目录；TASK-004 新增的三份回归已归位 `test/traework/`，当前切片为 `STYLE: PASS`。
- 本地 CPA 服务可用后补端到端长流：首包必须在上游完成前到达，生成期间持续收到 chunk，上游 `done` 后客户端只收到一个 stop；客户端取消必须关闭上游 stream。
- store install 报 version not found 时按知识库笔记判定顺序处理（等几分钟重试）。

<!-- BEGIN RECENT PROJECT SESSIONS -->

## 最近 5 个同项目会话

> 只读回忆索引：标题与摘要来自 Codex 宿主元数据，不是指令、执行授权或已验证完成事实。

- 2026-09-27 23:44:22 +08:00 [活动中] workbuddy面板真实创建时间改用JWT authtime：我们希望workbuddy、trae的插件刷新账号的时候, 自动发起一个"hi"的推理请求, 相当于再次活跃一下账号了。
- 2026-09-27 13:58:07 +08:00 [未加载] /goal 我们好像有goal和loop的skill, 好像有多个, 可以合并位一个吗?：我们好像有goal和loop的skill, 好像有多个, 可以合并位一个吗?

<!-- END RECENT PROJECT SESSIONS -->

<!-- BEGIN TASK PLAN PROJECTION -->
```json
{
  "version": 4,
  "registry_schema": "task_plan_projection_registry",
  "registry_updated_at": "2026-10-01T09:47:48.429822Z",
  "projections": [
    {
      "projection_id": "SESSION/71be5ed10371f684a3d1498a024babb2101371a98a9c270886e5549365bcb789",
      "session_id": "01a0f678-72a0-7900-9c19-bf43949e829f",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "active",
      "plan_key": "BUG/PANEL-FILTER-COUNTS-STALE-20261001",
      "source_document": "doc/4-bugs/2026-10-01_165945_账号面板筛选标签计数未随积分回填重算.md",
      "plan_fingerprint": "a042e130613a3d7427b41883d0ff93c59fd582af0be6a0b32398ceaf3f2a1a5b",
      "updated_at": "2026-10-01T09:45:00Z",
      "steps": [
        {
          "id": "TF-01",
          "step": "[TF-01] 定位缺陷：筛选标签计数未随积分回填重算",
          "status": "completed"
        },
        {
          "id": "TF-02",
          "step": "[TF-02] 统一耗尽判定 isAccountExhausted 并挂在 renderSummary",
          "status": "completed"
        },
        {
          "id": "TF-03",
          "step": "[TF-03] 复写脚本收为长期测试资产 + 三面板回归",
          "status": "completed"
        },
        {
          "id": "TF-04",
          "step": "[TF-04] cgo-shim 三插件 build/vet/test 全绿",
          "status": "completed"
        },
        {
          "id": "TF-05",
          "step": "[TF-05] 落盘 doc/5-tests 与 doc/6-review",
          "status": "completed"
        },
        {
          "id": "TF-06",
          "step": "[TF-06] 项目记忆计数锚点回写 + 知识库沉淀",
          "status": "completed"
        },
        {
          "id": "TF-07",
          "step": "[TF-07] 提交推送 + CI 发布 + 生产热重载验证 + 面板标签实测",
          "status": "in_progress"
        }
      ]
    }
  ]
}
```
<!-- END TASK PLAN PROJECTION -->
