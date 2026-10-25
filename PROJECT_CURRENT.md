# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 原生插件套件 `cpa-workbuddy-plugin`（涵盖 10 大 AI 服务商 Provider + 1 统一用量统计插件，共 11 插件），提供多账号管理、动态模型发现、流式转发、专属现代管理控制台、扫码/Token 登录、保号巡检与统一 token 用量统计分析服务。
- 范围：仓库内 11 大插件体系的代码实现、单元测试、资产构建、发布流水线、registry 元数据同步等：
  1. `workbuddy-provider`：腾讯 CodeBuddy（国内版与 Global 版）；
  2. `workbuddy-ai-provider`：WorkBuddy AI（独立国际版）；
  3. `traework-provider`：Trae SOLO（字节跳动海外/国内 AI IDE 客户端）；
  4. `qoderwork-provider`：QoderWork（阿里通义灵码衍生国内版）；
  5. `qoder-ai-provider`：Qoder AI（国际版 qoder.com，含 UMID 协议支持）；
  6. `cursor-provider`：Cursor（cursor.sh，Connect-RPC 协议与会话连续性）；
  7. `zcode-provider`：ZCode（BigModel Coding Plan，Anthropic 兼容与客户端真实指纹 mimic）；
  8. `copilot-provider`：GitHub Copilot（设备码 OAuth / 内部令牌保活 / VS Code Copilot Chat 指纹模拟）；
  9. `coze-provider`：扣子 Coze（claw 对话 / WS 真流式 / 生图与积分路由红线）；
  10. `qwenwork-provider`：千问办公（Device Flow + PKCE / MaaS 短票据）；
  11. `workbuddy-token-usage`：统一 Token 用量统计与实时看板服务。
- 非范围：CLIProxyAPI 核心服务本体；跨项目文件修改。
- 已下线插件：`gemini-provider`（2026-10-11）、`doubao-provider` 与 `notion-provider`（2026-10-22，源码/测试/图标/registry/CI/专属文档与生产残留全部清空）。

## 项目概况

- 状态：活跃维护中。11 大插件全景（10 Provider + 1 用量统计）。**2026-10-22 下线 doubao-provider 与 notion-provider**（用户裁定移除），叠加此前的 Gemini 插件，两者源码与生产残留均已清空。本轮删除后抽验 workbuddy / coze / token-usage 三插件 cgo-shim build+vet 全绿；未做 11 插件全量回归。最新发布/就绪版本（本轮未发新版）：
  - `zcode-provider`: **0.1.31**（面板额度改为逐模型独立进度条（GLM-5.3 / GLM-5.3-Flash 各一条）并移除「过期」等冗余信息；0.1.30 面板额度改用 `billing/balance` 取真实已用/剩余 + 领取失败文案友好化；0.1.29 修复面板无感验证 captcha 配置缺陷；0.1.28 面板内无感验证领取 Start Plan + OAuth 默认中国区）
  - `cursor-provider`: **0.1.6**（修复 Query 参数反序列化丢失导致真实发现模型被屏蔽、建立点号模型别名归一化根治上游 not_found、测试弹窗支持实时搜索过滤与秒开交互）
  - `qoderwork-provider`: **0.10.1**（0.10.1 模型清单改为全场景并集 + 以上游 `display_name` 为准 + 补齐改名后的请求回译 + 静态兜底对齐可用集合；0.10.0 账号故障改写盘禁用 + 每 10 分钟只探失败模型；0.9.44 流式中途断流零泄漏换号重试 + 断流信封补 `http_status=502`；0.9.40 重建上游 messages 保留工具调用结构；0.9.49 模型对外 ID 改用官方展示名并补齐执行链回译；0.9.48 模型清单按账号分片 + 额度优先）
  - `qoder-ai-provider`: **0.1.24**（0.1.24 流式中途断流零泄漏换号重试 + 断流信封补 `http_status=502`；0.1.20 重建上游 messages 时保留工具调用结构修复 agent 多轮往返工具结果失配对；0.1.18 签到节奏恢复每 4 小时六槽 + 移除冗余门控；0.1.17 tools 对齐修复；0.1.15 max_tokens 统一 32768 最低输出底线；0.1.14 上游 110 日调用上限 24 小时持久化冷却 + 补齐 /clear-cooldown 路由注册）
  - `traework-provider`: **0.4.0**（0.4.0 失败禁用改 sidecar 三层分离（宿主 fsnotify 不监听 spool，写盘不能作真相源）+ 模型级故障豁免（200 的 SSE 错误帧曾被无条件改写 403 导致整池自禁），同步 workbuddy 七项：故障 sidecar / 标记补回 / 外部启用对账 / 轮换冷却 / 积分快照 / 双阈值分级 / 面板徽标；`low_credit_disable_below` 默认 0 关闭；0.2.13 面板签到时刻文案漂移修正为 00/04/08/12/16/20；0.2.12 定时探活降频至 4 小时 + 失败也刷新节流时间戳；0.2.11 max_tokens 32768 底线 + payload 层同步，探活改全量模型按序测试）
  - `workbuddy-provider`: **0.21.1**（0.21.1 慢速冷却补判定日志：进入冷却时记录中位数/基线/倍数/阈值/样本数、防抽干放开单独留痕、到期恢复留痕、配置变更留痕；日志全部移到解锁后避免持锁 I/O；修复 recordSlowSample 先判「冷却中」导致恢复事件永不记录的缺陷。0.21.0 慢速账号 TTFT 拥堵自动冷却）
  - `workbuddy-ai-provider`: **0.1.28**（0.1.28 面板不再自动刷新：打开只渲染内存缓存/盘上积分快照，最新积分由卡片「刷新」显式触发；连带修复手动刷新与签到不落盘、缓存 3 分钟过期 GC 掏空活账号 plan/签到态、默认渲染对无 plan 账号查上游注册态三处缺陷。0.1.27 积分快照并入白名单搬运路径修复备注刷新抹快照；0.1.26 积分快照持久化 `credits_snapshot`；0.1.25 移除耗尽标签与按额度排除；0.1.16 授权后自动补注册地）
  - `copilot-provider`: **0.1.2**（0.1.2 调度器对齐宿主候选集语义修复越界缺陷，已发布 `0054c28`→`ba471b0`→`bca1264`→`78b4069` 并生产验收：热重载前 5 分钟 50/50 请求告警、热重载后 0/57；0.1.1 宿主 OAuth 弹窗改开插件桥接页展示设备码；0.1.0 首发：Device Flow 登录 + 内部令牌 80% 提前保活 + VS Code Copilot Chat 指纹模拟 + 动态模型发现 + 多账号池换号重试 + 流式/非流式转发 + 额度与面板）
  - `workbuddy-token-usage`: **0.2.8**（0.2.8 让退役实例的 feed 导入协程在连续 3 轮关闭句柄后自行退出；0.2.7 修复宿主配置重载换句柄窗口导致的用量记录丢失；0.2.6 移除四个图表与导出图片、下线 `/stats/trends` 及序列聚合以省算力与响应体积；0.2.5 起请求明细与维度汇总展示原始 provider id）
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-10-17 (GMT+8)

## 活动会话进展摘要

- 当前会话（2026-10-25）：**workbuddy 慢速账号 TTFT 拥堵自动冷却（0.21.0），已发布生产并在异常排查后恢复全量验收**：
  - **需求起点**：用户从生产用量面板看到同一模型 `deepseek-v4.1-flash` 的 TTFT 在 13s~21s 分层，慢的一批集中在固定账号；这类号每次仍返回 200，既有失败冷却完全抓不到。
  - **关键判读（推翻了直觉方案）**：同一批请求 input 普遍 33 万 tokens，prefill 本身十几秒，**绝对阈值会把整池健康号一次禁光**。判据必须是同模型横向相对对比。
  - **核心实现**：① 修复 per-attempt TTFT 计时污染（重试时 attempt 0 耗时不计入 attempt 1 正常号）；② `slow_account.go` 纯内存状态机：账号×模型粒度、滑动窗口样本、下四分位 P25 相对基线、`ratio_x10`（默认 2.5 倍）慢速判定、10 分钟到期自动恢复、防抽干放开；③ 接入四处路由接入点（`pickNextAuthForModel`、`pumpUpstreamStream` 入口预检、`handleExecExecute` 入口预检、`scheduler.pick`）；④ 6 个 `slow_*` 配置项注册与面板徽标 `慢速让位 · <model>`。
  - **发布链**：`e6e9447`(fix, 11 文件) → CI `40176880918` success → 6/6 assets sha256 OK → `b6bb2eb`(registry) → 远端 6/6 artifacts 200。
  - **生产事故与排查闭环**：发布后尝试通过 `PUT /v0/management/plugins/workbuddy-provider/config` 动态调低阈值时未传 `enabled: true` 和 `store` 结构，导致 CPA 宿主将该插件置为 `enabled: false`，插件在控制台“消失”。
  - **恢复与验证**：通过 `POST /v0/management/plugin-store/workbuddy-provider/install?version=0.21.0` 触发管理端标准热重载重新载入，完整保留 6 个配置项且 `enabled: true` 恢复；生产 37 个账号完整回归，面板 `/v0/resource/plugins/workbuddy-provider/panel` 200（84078 字节），真实端到端 `deepseek-v4.1-flash` 流式请求 200、TTFT 1.31s、总耗时 3.51s，nonce 完整返回，生产完全恢复正常。
  - **生产监控（2026-10-25 20:31~20:49，用户要求）**：确认机制已在真实流量下触发（`/accounts` 出现 `slow_cooling_models:["deepseek-v4.1-flash"]`），但**发现并纠正了一起持续误伤**——
    - **根因**：事故排查期间为测试「能否触发」而临时 PUT 的激进参数（`ratio_x10=12` 即 1.2 倍 / `min_samples=2` / `min_ttft_ms=500` / `window=60min`）被留在了生产，从未恢复。该组合下正常波动即超阈值。
    - **实测证据**：15 分钟窗口 182 条成功请求，池内 TTFT 高度均匀（最快 2.07s / 最慢 2.91s，仅 1.26 倍差），但仍持续有账号被判慢；6 分钟 18 次采样显示冷却主体在 `514****6` 与 `514****9` 之间**反复跳变**（2 样本即判定 + 10 分钟到期轮转到下一个），而非稳定锁定真实慢号。
    - **处置**：`PUT /config` **全量回写**（保留 `enabled` 与 `store`，规避上次的覆盖陷阱）恢复生产默认值：2.5 倍 / 15 分钟窗 / 5 样本 / 10 分钟冷却 / 3000ms 下限，并显式补上 `slow_account_enabled: true`。回写后插件 `enabled=true / effective=true / registered=true`，37 账号完好。
    - **处置后观察**：10 轮 × 20s 采样，冷却稳定锁定在恢复前已进入的 `192****1372` 单号、不再跳变；池内达标账号 TTFT 区间 2.15~2.78s（1.29 倍），低于 2.5 倍阈值，**未再新增误判**。
    - **边界（如实记录）**：慢速冷却模块本身**零日志输出**，生产唯一可观测点是 `/accounts` 的 `slow_cooling_models` 与 `slowCooldownSnapshot`（后者未暴露到 API）。因此「是否判慢、判了谁、为什么判」只能靠外部轮询 + 用 token-usage 逐请求数据反推，无法从日志直接取证；参数变更也不会留下审计痕迹。这是本轮监控暴露的可观测性缺口，未在本轮修复。
  - **可观测性缺口补齐（0.21.1，用户要求「补上冷却模块的判定日志」）**：
    - **判定取证**：进入冷却时打印 `auth / model / median / baseline / ratio / threshold / samples / cooldown / released`，判据三个输入一次记全，可直接区分「账号真慢」与「阈值配错」。
    - **防抽干留痕**：全池被判慢时放开一半的行为单独打印被放开账号清单——该行为与「到期自然恢复」在面板上都表现为不再冷却，成因完全不同。
    - **到期恢复留痕**：冷却过期后账号再次成功时打印 `expired_after` 与本次 TTFT。检测点放在 `recordSlowSample`（本就持写锁、只在真实成功时走到），**没有**放进 `slowCoolingDown`——后者挂在每请求路由热路径，必须保持纯读语义（新增用例 `TestSlowCoolingDownStaysReadOnly` 锁定）。
    - **配置变更留痕**：6 个 `slow_*` 键任一变化时打印生效值，只在真正变化时打印避免重载刷屏。
    - **锁纪律**：三条日志全部移到 `slowMu` 解锁之后；`log.Printf` 会做 I/O，持锁写日志会让所有路由判定排队。
    - **新用例暴露的真实缺陷**：`recordSlowSample` 原先先判「冷却中」再判到期，导致过期后第一条样本被当作仍在冷却而提前返回，恢复事件永远不会被记录——已修正判定顺序。
    - **验证**：新增 `slow_logging_test.go` 6 用例；**5 项变异全部 KILLED**（删判定日志 / 删防抽干日志 / 替换到期日志文案 / 配置日志条件恒假 / `slowCoolingDown` 改写操作）。其中两项首轮被**编译期**拦截（删日志后变量未使用），据此改用可通过编译的语义变异重跑。cgo-shim build+vet+test 全绿，gofmt 干净。
    - **发布链**：`c93d7bd`(fix, 7 文件) → CI `40196544940` success → 6/6 assets sha256 OK → `3a7e07a`(registry) → 远端 6/6 artifacts 200 且 size 全匹配、registry 零 `0.21.0` 残留。
    - **生产部署（热重载零重启）**：`restart_required:false`；`hot reloaded active=0.21.1 retired=0.21.0`；落盘 `.so` sha256 `37c7454a…b57a` 与本地 zip 逐字节一致；二进制内置版本号**仅 `0.21.1`**（无旧版残留）且含 4 条新日志字符串。
    - **生产验收**：插件 `enabled=true / effective=true / path=…v0.21.1.so`；37 账号、慢速冷却 0 个（此前残留已自然到期）；6 个 `slow_*` 配置为生产默认值（2.5x / 15m / 5 样本 / 10m / 3000ms）；面板 200（84078 字节）；真实入口 `deepseek-v4.1-flash` 流式 200、TTFT 1.36s、162 帧、NONCE 完整。
    - **日志链路生产实证**：以可逆方式微调 `slow_cooldown_minutes` 10→11 后立即恢复 10（当前无冷却账号，不影响任何路由决策），生产日志真实打出 `[workbuddy] slow config applied: enabled=true ratio=2.5x window=15m min_samples=5 cooldown=11m min_ttft=3000ms`，证明配置留痕链路在宿主环境下可用。判定/到期/放开三类日志需真实判慢才会触发，本轮生产窗口内无判慢事件，故仅有单测 + 变异覆盖。
    - **边界**：只补日志与可观测性，未改判定算法与阈值；`slowCooldownSnapshot`（剩余冷却时长 + 窗口平均 TTFT）仍未暴露到 API。
  - **侦察结论**：TTFT 采集（`usage.go:266`）、上报（`usage_feed.go`）、面板聚合（`aggregate.go:194`、`api.go:516`）全部已有，**唯独路由层零引用**——缺的只是「观测 → 冷却」这根线。
  - **前置缺陷（阻断项，已修）**：`started` 在 executor 入口只取一次且跨 attempt 复用，`collector` 每轮新建 → 「A 失败耗时 10s → B 首帧 2s」被记成 12s，**A 的失败耗时算到健康号 B 头上**。改为 per-attempt 计时基准（`markAttemptStart`）。非流式路径同样补齐（`doExecuteOnce` 早已算出 `firstByteAt` 却丢弃）。
  - **实现**：新增 `workbuddy/slow_account.go`，与失败冷却/轮换冷却三分独立（三者生命周期不同：失败冷却每次成功清零、轮换冷却需持续到窗口结束、慢速冷却同理且不能跨模型）。四处过滤接入 + 成功路径打点 + 6 配置键登记 + 面板 `slow_cooling_models` 清单。
  - **基准口径经两轮修正（重要）**：初版用「中位数的中位数」，变异验证**存活**——多账号池里慢号占多数时基准被抬到慢号水平，机制会静默失效。改为**下四分位 P25**，同时用 `slow_min_ttft_ms` 绝对下限挡住「单个超快号把基准压到地板」的另一端。两处各有用例锁定。
  - **另外两个设计修正**（均由测试暴露）：① 判慢时清空样本会让面板立刻失去平均延迟、且防抽干再也看不到彼此的冷却状态 → 改为保留样本 + 记录 `cooldownStartedAt`，判定时只统计冷却起点之后的样本，避免「解冷即再冷却」死循环；② 同步路径换号后不回传实际账号 → 新增 `storedAuthID` 并上抛 `usedID`，否则样本会记到被换掉的号上。
  - **验证**：4 个测试文件 20 用例；**5 项变异全部 KILLED**（去绝对下限 / 断调度过滤调用 / 去样本门槛 / 基准退化为最小值 / 基准改回中位数）。其中「断调度过滤」与「基准退化」首轮**存活**，据此补了调度路径用例与基准敏感性用例——这是本轮最有价值的两个发现。cgo-shim build+vet+test 全绿，`panel.html` 双脚本 `node --check` PASS，版本 0.21.0。
  - **未闭环**：未发布、未部署生产；默认阈值（2.5 倍 / 15 分钟 / 5 样本 / 10 分钟冷却 / 3s 下限）未经生产数据校准。上线后需观察 `slow_cooling_models` 是否出现批量增长。
  - **范围边界**：只在 workbuddy 落地，其它 provider 插件未同步；不做写盘禁用（慢是性能问题不是故障）。

- 当前会话（2026-10-25）：**traework 向 workbuddy 同步能力并修掉两个真实缺陷，发布 0.4.0 并部署生产**：
  - **两个真实缺陷**：① 写盘禁用可能名义生效——宿主 fsnotify 不监听 mysqlstore spool（qoderwork 0.11.0 生产实证 48h 零 watcher 事件而 mtime 持续变化），「写盘 → 宿主毫秒级感知」前提不成立；改为插件自有 sidecar `data/traework-failover.json` 作为路由判据权威来源。② 模型级故障被记成账号级——HTTP 200 的 SSE 错误帧被无条件改写成 403，`All backends failed` 类后端池级故障走进账号级判据导致整池自禁（与 qoderwork 0.11.0 事故同机理）；在记账唯一漏斗与换号判据两处同时豁免，硬额度（402/额度标记）优先不被吞。
  - **同步的七项能力**：故障 sidecar 跨重启存活、宿主 persist 抹标记后补回、外部启用对账、轮换冷却（状态独立于失败复位，四处成功打点）、积分快照持久化 `data/traework-credits.json`（与故障 sidecar 分文件，避免高频替换冲掉低频状态）、积分双阈值分级、面板轮换/积分档位徽标、11 个配置键登记。
  - **关键裁定（放宽而非收紧）**：`low_credit_disable_below` 默认 **0（关闭）**，刻意不同于 workbuddy 的 100——直接照搬会让现网 remain 20–99 的账号在首轮扫描被批量禁用；保活线 `min_routable_credits` 从「硬排除」降级为「排序降级」。
  - **验证**：10 条变异全部被测试检出（含「调度层过滤行没接上」的经典存活形态；`TestLowCreditDisableBelowZeroKeepsLegacyBehavior` 锁定 remain=50 仍可路由）；cgo-shim build+vet+test 全绿；panel.html 两个 script 块 `node --check` 通过。
  - **发布链**：`37f1d17`(fix, 35 文件) → CI `40172655622` success → 6/6 assets sha256 OK → `4e02b36`(registry) → 远端 6/6 artifacts 正确。
  - **生产部署（热重载零重启）**：`restart_required:false`；`hot reloaded active=0.4.0 retired=0.3.0`；`.so` sha256 `0c953f74…ed33dee` 与本地 CI 产物逐字节一致。
  - **生产验收**：`/accounts` 与 `/panel` 双 200；16 账号 `normal:14 / preserve:2 / disabled:0`——**禁用线默认 0 的红线在生产坐实**（remain=0 的号未被误禁）；`traework-credits.json` version:1、4716 字节、明细含 total_used/total_size/pack_count；配置字段 25 条全部注册（11 个新键在内）；真实请求 `kimi-k3.1-pro` HTTP 200 耗时 3.9s。
  - **未闭环项（如实记录）**：① 轮换需真实长流量（1 小时）才能观察到让位，本轮未跑满；② 模型级豁免的正文形态 grep 近 5 小时生产日志无样本，标记表按后端池语义最小集落地（`RESOURCE_LIMIT`/`10605` 等 Qoder 专属文案刻意未加，避免吞掉真实账号故障）。

- 当前会话（2026-10-25）：**coze 低额自动禁用未同步宿主状态修复（0.6.2），已发生产并验收**：
  - **根因**：宿主 `pluginAuthDataToCoreAuth` **只用 `AuthData` 顶层 `Disabled` 推导 `Status`**，Metadata 里的 `"disabled"` 在该路径上不参与判定。coze 的 `authDataForLogin` 只补了 Metadata、漏设顶层字段 → 插件写盘禁用对宿主不可见。**全项目 7 个插件只有 coze 漏了这一行**。0.6.1 的「Metadata 回传」只覆盖文件重建路径（`synthesizer/file.go:107`），漏掉状态判定路径。
  - **影响面**：真实流量**无影响**（`publishCreditPool` 按 `!acc.Disabled` 排除低额号，6 小时日志「转由宿主换号」0 次、「已自动禁用」24 次）；受损的是宿主可见性与调度候选池口径。
  - **修复**：`authDataForLogin` 补 `Disabled: sa.Disabled`，一处覆盖 `auth.parse` / 登录成功 / `auth.refresh` 三个调用点。新增 1 用例 + 2 处补顶层断言，变异验证 KILLED，cgo-shim 全绿。
  - **发布链**：`0e8620a`(fix, 6 文件) → CI `40170584547` success → 6/6 assets sha256 OK → `2adc70c`(registry) → 远端 6/6 artifacts 200。
  - **生产部署（热重载零重启）**：`restart_required:false`；`.so` sha256 `81a51f97…e28229` 与本地一致；`hot reloaded active=0.6.2 retired=0.6.1`。
  - **生产验收**：宿主 `auth-files` 从「14 全 active」变为 **10 active / 4 disabled**（`status=disabled`，ID 与插件侧逐一对应）；插件 `/accounts` `normal:10 / disabled:4`、`auto_disabled` 4 条；`test-active` 上游 `ok:true`。
  - **文档**：`doc/4-bugs/2026-10-25_144500_coze低额自动禁用未同步宿主认证文件状态根因诊断.md`（含三层生产铁证与 workbuddy 对照）。

- 当前会话（2026-10-25）：**生产 traework-provider 17 个账号误删后从 MySQL binlog 完整恢复，已启用并端到端验收**：
  - binlog 定位到 17 条 DELETE（10-20/10-21/10-22 三批）；恢复前先备份 binlog 本体（921MB）防 24h 轮转；容器无 `mysqlbinlog` 改用纯 Python 直解（踩事件头/table_id/二进制 JSON 偏移三个陷阱，靠已知 uid 反查自证）。
  - 还原点三层（表 dump + spool 快照 + 数量基线）在 `/root/traework-recovery/`；17 个凭据实测刷新 **16 成功**（1 个删除前即 `Session expired`，非本次误删）；按「auth_store → spool」写回 17/17，启用后 `/accounts` 17 条、宿主 `auth-files` 56→73。
  - **完整方法论已沉淀为项目 skill** `project-cpa-workbuddy-plugin-account-data-recovery-rules`；边界：恢复出的 `disabled` 统一 false，删除前禁用态未保留。

- 当前会话（2026-10-25，已压缩）：**zcode 功能同步 P0 发布 0.2.0 并部署生产**：三项裁定（只做 P0 / 引入写盘禁用并移除内存冷却 / 额度耗尽落禁用 + 重置后探活恢复）；三处流式缺陷修复（`[DONE]` 与 `event: done` 立即停读、识别 HTTP 200 承载的错误帧、报文脱敏）；10 分钟探活只回放失败模型。**E2E 抓到的真实缺陷**：`writeAuthFileDirect` 按结构体整篇重建会抹掉宿主字段、且探活成功后把刚写的 `disabled:false` 覆盖回内存旧值 → 改为「读旧文档 → 覆盖已知键 → 写回」并补回归守卫。12 项变异全部 KILLED；生产热重载零重启，但**池内 0 个 zcode 账号**，额度链路真实 E2E 未做。完整细节见 git log 与 doc/3-实施。

- 当前会话（2026-10-25）：**同一宿主 persist 抹除缺口同步到四个插件，已发生产并验收**：
  - **背景**：workbuddy-provider 0.20.2 修复「宿主每次请求结束用内存 Metadata 重建 auth 文件、抹掉插件直写归属字段」后，用户要求同步到 workbuddy-ai / qoderwork / qoder-ai / coze。
  - **侦察结论（四插件三种形态，不能照搬）**：workbuddy-ai 与 qoderwork 与 workbuddy 完全同构（`failover_disable` 三键 + `counter.go` 周期性落盘）；qoder-ai 用的是 `daily_cooling_until`（24h 冷却截止时间，非 disabled 标记）；coze 的标记在 `StoredAuth` 结构体内、且**没有计数器落盘通道**，且其 `auth.parse` 的 Metadata 原先连 `disabled` 都没有（只有 5 个键），宿主一次 persist 会把整文件压成那 5 个键。
  - **修复**：workbuddy-ai 0.1.30 / qoderwork 0.11.1 —— `persistCounterDelta` 落盘前补齐三键，条件限定「本进程写过禁用」+「盘上确实 disabled」，`at` 取内存原始时刻；qoder-ai 0.1.31 —— 同路径补齐 `daily_cooling_until`，只补仍在冷却期内的账号（避免把该恢复的号重新冻结）；coze 0.6.1 —— 正解是让 `auth.parse` 的 Metadata 带上 `disabled` + 三个低额禁用归属键，宿主重建时原样写回，形成闭环。三插件面板同步新增 `failover_unconfirmed`。
  - **验证**：新增 4 个测试文件共 13 用例；变异验证全部 KILLED —— workbuddy-ai/qoderwork（去归属判据、断开调用点）、qoder-ai（断开补字段调用、去掉「字段已存在」判据）、coze（`disabled` 写死 false，被 2 条用例同时捕获）。五插件 cgo-shim 全绿；gofmt 干净（修正了本轮引入的多余空行与字段对齐）。
  - **发布链**：`cfe116b`(fix, 27 文件) → 四 CI 并行全部 success（`40132792599`/`40132787644`/`40132783125`/`40132778231`）→ 四组 6/6 assets sha256 OK + 二进制内置新版本号且含新符号 → `6e9a1dd`(registry) → 远端四插件 version + 24/24 artifacts ALL PASS。
  - **生产部署（热重载零重启）**：四次 install 均 `restart_required:false`；四个落盘 `.so` sha256 与本地 zip 逐字节一致（`0471956f…`/`92434314…`/`12019342…`/`bb748fea…`）；`hot reloaded` 四条全部 `active=新版 retired=旧版`。
  - **生产验收**：四插件 `/accounts` 与 `/panel` 全部 200（workbuddy-ai 2 号 / qoderwork 18 号 / qoder-ai 0 号 / coze 14 号）；三插件当前无禁用账号，`failover_unconfirmed` 计数 0（无分叉）；真实入口流式 `deepseek-v4.1-flash` 1.8s、含 nonce、无断流。
  - **边界（如实记录）**：本轮与 workbuddy 0.20.2 一样只做「补字段 + 暴露分叉」，宿主 persist 与插件写盘的竞态窗口未消除；彻底消除需改走 `PATCH /v0/management/auth-files/status` 宿主禁用通道。coze 走的是 Metadata 回传路线，理论上比补标记更彻底（宿主重建时自带正确值），但同样未消除并发写入竞态。

- 当前会话（2026-10-25）：**workbuddy-provider 0.20.2 失败禁用标记保底补回，已发生产并验收**：
  - **根因**：宿主在每个请求结束后 `m.persist(ctx, auth)`（`conductor_cooldown.go:994/1085`），用内存 `Auth.Metadata` 重建 auth 文件；该 Metadata 只含插件 `auth.parse` 白名单键，本机制 `failover_disable`/`_model`/`_at` 三键不在其中，于是「插件写禁 → 宿主 persist」后盘上只剩 `disabled:true`。标记一丢，10 分钟探活按标记挑候选就再也找不到该号。
  - **生产铁证**：3 个账号（2ca400e3/5e6050ba/242e1dde）带 `failover_disable_at` 存活且 DB `disabled=true`（证明写盘链路本身可用）；另 3 个（700fd592/59667798/7b251fe4）在 03:33:55.74~.765 的 24ms 内被批量重写为 `disabled:false` 且标记全无，DB updated_at 分别停在 03:28:23/03:31:08/03:33:52 —— 文件 mtime 晚于 DB，证明最后一笔不经 MySQL，是插件自身周期性落盘写回了已被宿主重建过的脏文档（写入者 = `flushCounters`）。
  - **修复**：`persistCounterDelta` 落盘前调 `restoreFailoverMarkersIntoDoc` 补齐三键；条件严格限定「本进程写过禁用」+「盘上确实 disabled」，防止替手动/耗尽禁用伪造归属、防止把已恢复账号重新标禁用；`at` 从内存 `failoverDisableAtLocal` 取原始时刻。
  - **面板**：新增 `failover_unconfirmed`，盘上禁用而宿主仍 enabled 时显示「写盘未生效·仍在接流量」，这是 6 vs 3 显示分叉的直接来源。
  - **验证**：新增 `failover_marker_restore_test.go` 6 用例；**5 项变异全部 KILLED**（去本地归属判据/去 disabled 判据/断开 persistCounterDelta 补标记调用/丢弃原始时刻/面板判据忽略宿主态）。其中「断开调用」首轮存活 → 暴露纯函数测试不覆盖调用点，据此给 `counter.go` 补宿主注入点并加 `TestPersistCounterDelta_RestoresMarkersOnDisk`。cgo-shim 全绿、gofmt 干净、panel.html 双脚本语法通过。
  - **发布链**：`94a0dcc`(fix, 8 文件) → CI `40130552239` success → 6/6 assets sha256 OK + 二进制内置 0.20.2×2 / 0.20.1×0 且含 `failover_unconfirmed`×3 + `restoreFailoverMarkers`×2 → `8eca911`(registry) → 远端 6/6 sha+size ALL PASS、零旧版残留。
  - **生产部署（热重载零重启）**：install 返回 `restart_required:false`；落盘 `.so` sha256 `c7904d88…4c6bda` 与本地 zip 逐字节一致；`hot reloaded active_version=0.20.2 retired_version=0.20.1`。
  - **生产验收**：`/accounts` 200（13 账号，`failover_disabled=3` / `disabled=3` / `failover_unconfirmed=0`，三者对齐证明当前 3 个禁用号写盘均被宿主正常吸收、无分叉）；`/panel` 200（83636 字节，含「写盘未生效」文案）；真实入口流式 `deepseek-v4.1-flash` 返回 200、1382 字符、6 章节 + nonce 完整、4.2s 无断流。
  - **边界（如实记录）**：本轮只做「补标记 + 暴露分叉」，宿主 persist 与插件写盘的竞态窗口未消除（补标记只保证最后一笔不丢）；彻底消除需改走 `PATCH /v0/management/auth-files/status` 宿主禁用通道，属后续独立改动。新版本运行窗口内无 failover 事件（无账号失败），故 `NOT confirmed` 归零只能证明「无失败」，不能单独证明修复在生产正向路径生效。

- 当前会话（2026-10-25）：**qoderwork 0.11.0 失败禁用链路重构已发生产并完成验收（含管理密钥落 .ssh 元信息）**：
  - 用户裁定「直接把有问题的去掉、重新实现」，本轮完成三层分离重构：故障分类 → 插件自有 sidecar 状态 → 路由排除（`scheduler.pick` + `pickNextAuth`），并以熔断器兜住分类错误。
  - **发布链**：`e7cc87f`(refactor, 25 文件) + `ac3cd2e`(registry) → CI `40124679285` success → 6/6 assets sha256 OK → 远端 registry ALL PASS 且零旧版残留。
  - **管理密钥已落 `.ssh` 元信息**（用户授权明文持久化）：`C:\Users\luode\.ssh\servers.json` 新增 `managementKey` + `managementKeyNote` 字段，标注为 CPA 生产管理密钥明文；本轮及后续取值全部经文件读取，未在任何回复/输出中回显。生产 `config.yaml` 中该键已被宿主启动时 bcrypt 哈希（不可逆），明文仅此一处留存。
  - **生产部署（热重载零重启）**：`POST plugin-store/qoderwork-provider/install?version=0.11.0` → `200 {"status":"installed","restart_required":false}`；`hot reloaded active_version=0.11.0 retired_version=0.10.1`；落盘 `.so` sha256 `d87abf7c…d3ce`（8134976 字节）与本地 release zip 逐字节一致；容器未重启。
  - **接口验收**：`/accounts` 200（18 账号）、`/panel` 200（75812 字节，含三类新徽标钩子）；`/v1/models` 返回 20 条 qoderwork 模型。
  - **排队帧零误判实证**：验收窗口内上游整体 `RESOURCE_LIMIT` 排队，18 个账号被逐个打到 `403:10605 isQueued`，随后核对 `disabled=0 / cooling=0 / failover_disabled=0 / rotation_cooling=0`，`data/qoderwork-failover.json` **未生成**（无任何禁用记录），`failover disable` 日志 qoderwork 侧 0 条。**口径务必准确**：排队帧不判账号故障是 0.10.1 时代已有的红线，本轮验证的是重构没有破坏它；本轮新增的模型级故障分类（`All backends failed` 那条路径）在该窗口内上游未复现，因此只有单测 + 变异覆盖，**生产正向证据仍待补**。
  - **积分快照侧车实证**：`GET /credits` 全量拉取 18/18 成功 → `data/qoderwork-credits.json` 12200 字节落盘，v1 schema、中文正常、18 个账号条目齐全；侧车目录可写性单独验证通过。
  - **成功链路**：`GLM-5.3` / `DeepSeek-V4-Pro` / `Kimi-K2.8-Preview` 三模型流式均返回真实 `reasoning_content` 帧（非空完成）。
  - **未在本轮闭环（如实记录）**：① 成功链路因上游随后整体排队，未能稳定跑满「完整流式到 `[DONE]` + 轮换 `承流` 计时可见」的端到端样本；② 账号级故障触发侧车 `failover-failover.json` 写禁用的正向路径未在生产复现（需真实账号级错误，当前上游只有排队帧）；两项均有本地 cgo-shim 单测 + 变异验证覆盖，生产侧待下个窗口补证。
  - **旁证（非本轮回归）**：部署后 3 条 `upstream served model "auto"` 警告，与部署前 02:12 的同类警告同形，属上游排队降级期的既有现象，非 0.11.0 引入。

- 更早会话（2026-10-25，已压缩）：**qoderwork 失败禁用两层根因诊断（已被 0.11.0 重构取代，结论仍有效）**：① 宿主以 `auth_store` 为真相源、spool 为单向投影，插件搬标记唯一通道是 `AuthData.Metadata` → `handleParseAuth`，而 DB 回写 spool 不走该回调；② 真根因是 watcher 未监听——容器 `auth-dir` 下 qoderwork 文件 0 个、18 个全在 `mysqlstore/mysqlstore/auths/`，改 note / `os.Rename` / 删除重建三项实测零事件，48h 零事件而 spool mtime 持续在变。该结论直接促成 0.11.0 三层分离重构，也是 traework 0.4.0 采用 sidecar 的依据。

- 更早会话（2026-10-25，已压缩）：**qoder-ai 0.1.30 模型清单修复的上游取证**：真实国际版账号直连证实响应按 11 场景切分（chat 仅 12 个 key、并集 21 个）；QMC 加密目录解密同样证实按场景切分。国际版把档位嵌进场景 id 形成 8 个伪模型必须按前缀排除；真正漏掉的只有 `performance`。静态表只保留旧名代号，新名交给运行时映射（否则会遮住映射，变异存活）。

- 更早会话（2026-10-25，已压缩）：**qoderwork 生产 Kimi-K3.1-Pro 测试失败排查：上游后端不可用，非插件缺陷**。Qoder 上游返回 HTTP 200 + `All backends failed`（Kimi 后端池全挂），同期 `serviceAvailable:false` / `subCode:RESOURCE_LIMIT`；四项排除（回译 / 账号状态 / 换号链 / 权益）均正常。**该案例正是 traework 0.4.0 模型级豁免的动机来源之一**——同类文案在 traework 会被判账号级并整池自禁。

- 更早会话（2026-10-24 ~ 2026-10-25，已压缩）：**qoderwork 0.10.1 / qoder-ai 0.1.30 模型清单漏模型修复**（多场景并集 + 上游 `display_name` 权威 + 运行时回译映射，均已发生产）；**coze 0.5.5/0.5.6 每日登录积分自动领取 + 历史账号补注册宿主**（唯一触发器 `GET .../credit/balance/check`，补注册修复请求必 503）；**workbuddy 0.20.0 连续使用轮换**（满 60 分钟让位、冷却 60 分钟、中断 10 分钟重计时、全冷放开一半）。完整细节见 git log 与 PROJECT_HISTORY.md。

- 当前会话（2026-10-23）：**workbuddy-ai 0.1.22 与 zcode 0.1.35 版本就绪并生产落盘（未启用）**：
  - 查清 `f961f2eb` 会话产出 `121931d`（README 六插件说明，纯文档）已提交；另有两处未发布修复 `5ab431d`（workbuddy-ai `backendHeaders` 补四个会话追踪头）。
  - 发布前生产事实：10 个插件只有 3 个真正启用（qoderwork / workbuddy / token-usage），workbuddy-ai 与 zcode 均 `registered` 但未启用。
  - 发布链：`d879fb6` → workbuddy-ai CI `39812602344` 19/19 success；zcode 首个 run 失败后修正重跑通过。
  - 生产部署零容器重启：两插件均 `hot reloaded` 生效，落盘 `.so` 哈希与本地一致；因插件未启用，未做流量验收。
  - 边界：两插件仍处禁用状态，本次仅完成「版本就绪 + 落盘」，不代表能力已上线。

- 更早会话（2026-10-18 ~ 2026-10-23，已按 51200 字节上限压缩，完整记录见 git log 与 PROJECT_HISTORY.md）：
  - **workbuddy 0.17.1 / 0.18.0**：低额红线 20→400（面板三处硬编码改为消费 `min_routable_credits`，399/400 分界用例锁死严格 `<`）；下线 Global 耗尽自动删号改为 CN/Global 一视同仁写盘禁用（同时去掉恢复分支的 `region=="cn"` 前置，否则 Global 禁后永久不恢复）；`min_routable_credits` 补登记进 `ConfigFields`，否则配置写了界面看不到。
  - **workbuddy-ai 0.1.22 / zcode 0.1.35 版本就绪未启用**：补齐 `5ab431d`（四会话追踪头修国际版 400/11128）与 `00a7e88`（schema 6 防 OAuth URL `&` 转义）两处从未发过二进制的修复。踩坑：`plugin-store install` 必然写 `plugins_enabled:true`，与「只发版不启用」相悖，须再 `PUT config {"enabled":false}`。
  - **下线 doubao / notion 两插件（13 → 11）**：源码、测试镜像、图标、专属文档、registry、CI 矩阵、生产磁盘残留全部清空；两者此前早已 `configured/registered/enabled` 全 false。
  - **coze 0.4.6 → 0.5.4 系列**：64KB content 超限返回 413；积分路由红线走执行入口闸门（非 scheduler）；0.5.3 生图统一 `awaitImages` + 4 分钟上限 + 缺图 400；0.5.4 整池确定 0 分快速失败 400（判据只认 `remain==0` 且要求池内每号积分在 15 分钟可信窗内）。
  - **定时任务并发治理**：6 插件 4h 槽位改 `next*FireTime` + 5m 可注入抖动；traework 签到重试收敛并发 4；六个 watchdog 首轮加抖动；唯一无界扇出是 coze `/accounts`。
  - **账号故障冷却基数 30s → 60s**（线性递增、上限 10 分钟、cap 10 档）。
  - **Qoder 系模型发现风暴与 OOM 修复（0.9.47 / 0.1.27）**：GET 真携带 `qoderEncode("{}")` 修复恒 403 Signature invalid；补负缓存 5min；`pickNextAuth` 50ms 退避 + 错误体截断。0.9.48 / 0.1.28 缓存按账号分片 + 额度优先；0.9.49 / 0.1.29 对外 ID 改官方展示名 + 执行链回译。
  - **其余**：qwenwork 立项（Device Flow + PKCE → MaaS 短票据 TTL 2h）、发布矩阵下线 freebsd-amd64（7 → 6 平台）；coze 0.2.0~0.3.6 模型动态发现 + 面板重做 + WS 真流式；五插件签到统一每 4 小时；zcode 中国区与额度领取（0.1.22→0.1.31）；copilot 立项与 0.1.0/0.1.1（宿主 OAuth 弹窗改桥接页）；Kiro 裁定不立项；max_tokens 官方规格自适应 + 32768 底线；Qoder 110 日上限 24h 持久化冷却；排队帧不判账号故障（403:10605 isQueued）；删除 Gemini 插件；Token 用量面板移除 4 图表。

<!-- BEGIN RECENT PROJECT SESSIONS -->
## 最近 5 个同项目会话

| 会话 ID | 标题 / 意图 | 状态 | 活跃时间 | 关键改动 / 影响 |
|---|---|---|---|---|
| `01a11b9b-eb7d-7830-afed-7c8dd64c6629` | WorkBuddy AI 国际版签到改造实施 | completed | 2026-10-09 05:46 | 面板「领取专家加油包」失效入口替换为国际版每日签到，新增自动签到开关与批量签到；0.1.8 发布部署验收通过，上游活动离线登记为 GAP-001 |
| `01a11ba7-3193-7ab0-9618-11075ecaba63` | Token 用量面板性能与图标修复 | completed | 2026-10-09 03:06 | token-usage 0.2.4 移除面板 SSE 短连接轮询与 15s 定时轮询、改手动刷新并补插件图标；发布部署验收通过 |
| `01a115b7-7f04-7b81-8e96-cb269a144fa4` | Cursor 插件移植与 Token 导入实施 | completed | 2026-10-07 23:41 | 新增第六个插件 cursor-provider 0.1.0，支持会话 Token 导入账号；全链路发布、生产热重载与端到端验收均通过 |
| `01a11624-7389-7fe2-ac2b-82eb433267bd` | release-assets 3.75 GB 清理旧发布缓存 | completed | 2026-10-07 19:34 | release-assets 从 3.75 GB 清理至 0.13 GB；发布后清理规则固化为项目 skill 与 prune 脚本 |
| `01a110f3-1891-72c1-914e-7600d0e8b6df` | WorkBuddy AI 彻底清空写死模型与 0.1.3 全链路发布部署 | completed | 2026-10-07 03:20 | workbuddy-ai 彻底剔除硬编码模型，完全动态拉取；0.1.3 发布并生产部署热重载验证通过 |
<!-- END RECENT PROJECT SESSIONS -->
<!-- BEGIN TASK PLAN PROJECTION -->
```json
{
  "version": 5,
  "registry_schema": "task_plan_projection_registry",
  "registry_updated_at": "2026-10-25T07:01:22.732382Z",
  "projections": [
    {
      "projection_id": "SESSION/30608b3616fa2311bfb720a2776c09ac96b2738601ec3752b03a252c810cdb70",
      "session_id": "01a115da-13cb-7f90-ac94-25c133fdc31e",
      "projection_origin": "synthesized",
      "synthesis_mode": "exact",
      "state": "active",
      "plan_key": "IMPL-CURSOR-PROVIDER-PORT-20261007",
      "source_document": "doc/3-实施/2026-10-07_Cursor插件移植与Token导入实施总览.md",
      "plan_fingerprint": "9eea12fd114fb3e34cb60dd2a60eceb2eacd227a7e9a481c5b3f73c954a2a3b6",
      "updated_at": "2026-10-07T12:25:35Z",
      "steps": [
        {
          "id": "TASK-001",
          "step": "[TASK-001] 移植源码并统一标识",
          "status": "completed"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] 令牌解析与导入",
          "status": "completed"
        },
        {
          "id": "TASK-003",
          "step": "[TASK-003] 导出删除启停接口",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 面板与本地回归收口",
          "status": "completed"
        },
        {
          "id": "TASK-005",
          "step": "[TASK-005] 发布与部署",
          "status": "in_progress"
        }
      ]
    },
    {
      "projection_id": "SESSION/df86ce8afa18a9515072ffdfb460ee7e85402285d28ea505ed37f9133e10c0e8",
      "session_id": "01a115b7-7f04-7b81-8e96-cb269a144fa4",
      "projection_origin": "synthesized",
      "synthesis_mode": "exact",
      "state": "active",
      "plan_key": "IMPL-CURSOR-PANEL-AUTOENTRY-20261008",
      "source_document": "doc/3-实施/2026-10-08_Cursor面板免密直入与workbuddy对齐实施总览.md",
      "plan_fingerprint": "16acf9f349058ff187e5a3a7fb9bcc2f856cc447c47c98da76c8206319350112",
      "updated_at": "2026-10-07T17:15:57Z",
      "steps": [
        {
          "id": "TASK-001",
          "step": "[TASK-001] 面板免密直入改造：密钥区默认隐藏、启动自动加载、移除订阅额度说明区块",
          "status": "in_progress"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] 测试与本地回归：management_test 断言更新、面板自动进入回归脚本、cgo-shim-build 全绿",
          "status": "pending"
        },
        {
          "id": "TASK-003",
          "step": "[TASK-003] 文档与版本收口：DESIGN/CHANGELOG/VERSION 0.1.1、6-review 风格回归",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 发布与生产部署验证：commit/push/CI/资产入库/registry/热重载验收",
          "status": "completed"
        }
      ]
    },
    {
      "projection_id": "SESSION/94dd522698a5cf6e0897452ab54a3907615603273c390609e7694e7501d16106",
      "session_id": "88d9095a-1953-4563-8fa7-28bc30f9b129",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "inactive",
      "plan_key": "DIAG/WORKBUDDY-ROUTING-BALANCE",
      "source_document": "PROJECT_CURRENT.md",
      "plan_fingerprint": "a2faaec8f1ff284eb6cfdd6066e0be9632cf1863dea7cff7b96f078b01c72367",
      "updated_at": "2026-10-23T02:00:00Z",
      "steps": [
        {
          "id": "DIAG-01",
          "step": "[DIAG-01] 统计最近 1 小时生产日志中各 workbuddy 账号的请求量分布，确认不均衡程度",
          "status": "completed"
        },
        {
          "id": "DIAG-02",
          "step": "[DIAG-02] 判定请求实际走的是插件 scheduler.pick 还是宿主内置 session-affinity 选择器",
          "status": "completed"
        },
        {
          "id": "DIAG-03",
          "step": "[DIAG-03] 统计各账号唯一会话数与粘性时长，验证「少量会话高粘性导致集中」假设",
          "status": "completed"
        },
        {
          "id": "DIAG-04",
          "step": "[DIAG-04] 核对生产实际在任的 scheduler 插件，确认 workbuddy 路由逻辑是否被宿主绕过",
          "status": "completed"
        },
        {
          "id": "DIAG-05",
          "step": "[DIAG-05] 输出根因结论与可选修复方向，交用户裁定",
          "status": "completed"
        }
      ]
    },
    {
      "projection_id": "SESSION/6ca9986cdc78bbad2f4d5aade087730561b431aaa0987d9882cc2d1577078be9",
      "session_id": "6dbfcd84-d790-4a42-91df-8ead95031d6e",
      "projection_origin": "synthesized",
      "synthesis_mode": "fallback",
      "state": "active",
      "plan_key": "SYNTH-FALLBACK/20261024T164321Z",
      "source_document": "",
      "plan_fingerprint": "9b56b693aa310c501f3474e3a893700bd5397d43439963cba4351e1f2c456a27",
      "updated_at": "2026-10-24T16:43:21.205445Z",
      "steps": [
        {
          "id": "RECOVERY-01",
          "step": "[RECOVERY-01] 核对“对 qoder-ai 插件做与 qoderwork 0.10.1 相同的模型发现修复”的目标与验收条件",
          "status": "in_progress"
        },
        {
          "id": "RECOVERY-02",
          "step": "[RECOVERY-02] 确认“对 qoder-ai 插件做与 qoderwork 0.10.1 相同的模型发现修复”的来源与当前会话归属",
          "status": "pending"
        },
        {
          "id": "RECOVERY-03",
          "step": "[RECOVERY-03] 对照“对 qoder-ai 插件做与 qoderwork 0.10.1 相同的模型发现修复”核实已完成与待处理事项",
          "status": "pending"
        },
        {
          "id": "RECOVERY-04",
          "step": "[RECOVERY-04] 核对“对 qoder-ai 插件做与 qoderwork 0.10.1 相同的模型发现修复”的当前进度和中断位置",
          "status": "pending"
        },
        {
          "id": "RECOVERY-05",
          "step": "[RECOVERY-05] 确认继续“对 qoder-ai 插件做与 qoderwork 0.10.1 相同的模型发现修复”所需的权限和安全边界",
          "status": "pending"
        },
        {
          "id": "RECOVERY-06",
          "step": "[RECOVERY-06] 按已确认的下一项继续处理“对 qoder-ai 插件做与 qoderwork 0.10.1 相同的模型发现修复”",
          "status": "pending"
        },
        {
          "id": "RECOVERY-07",
          "step": "[RECOVERY-07] 对照“对 qoder-ai 插件做与 qoderwork 0.10.1 相同的模型发现修复”的验收条件验证恢复结果",
          "status": "pending"
        },
        {
          "id": "RECOVERY-08",
          "step": "[RECOVERY-08] 更新“对 qoder-ai 插件做与 qoderwork 0.10.1 相同的模型发现修复”的进度证据并完成收口",
          "status": "pending"
        }
      ]
    },
    {
      "projection_id": "SESSION/782414aa0d40fe5708401abc892566d7d70d4151ee67a90159bbfa4441ddd533",
      "session_id": "65b03a5b-6668-4651-986a-e9e82b82f85f",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "inactive",
      "plan_key": "PLAN/zcode-p0-failover-disable",
      "source_document": "doc/3-实施/2026-10-25_133753_zcode功能同步P0与写盘禁用_实施总览.md",
      "plan_fingerprint": "7339ff9e834290d1aad6ebdc9ef9ec8e183f5ab0d1db4492d620a9e7be9c2d73",
      "updated_at": "2026-10-25T06:17:21.727397Z",
      "steps": [
        {
          "id": "TASK-003",
          "step": "[TASK-003] 为 zcode 新增错误文本脱敏：移植 Bearer/JWT/token 正则，接入流式错误帧与用量上报出口，让上游报文不再泄露凭据",
          "status": "completed"
        },
        {
          "id": "TASK-001",
          "step": "[TASK-001] 修 zcode 流式终止：识别 [DONE] 与 event: done 后立即停止读取上游并关闭流，不再死等空闲连接",
          "status": "completed"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] 让 zcode 识别 HTTP 200 承载的错误帧，命中即按失败收尾并按失败记账，不再当成功清掉失败状态",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 为 zcode 新增物理 auth 文档读写原语：读盘为 map、改键后整体写回，保证未知顶层键原样保留",
          "status": "completed"
        },
        {
          "id": "TASK-005",
          "step": "[TASK-005] 让 zcode 失败即写盘禁用：写 disabled 与三个归属标记，宿主随即停止路由该账号",
          "status": "completed"
        },
        {
          "id": "TASK-006",
          "step": "[TASK-006] 为 zcode 加 10 分钟探活恢复与标记保底：只探失败模型，探通解禁，标记被宿主抹掉则下一轮补回",
          "status": "completed"
        },
        {
          "id": "TASK-007",
          "step": "[TASK-007] 删除 zcode 的 1 秒冷却前探活循环，避免与新的 10 分钟探活重复打上游",
          "status": "completed"
        },
        {
          "id": "TASK-008",
          "step": "[TASK-008] 下线 zcode 内存冷却状态机，路由判据改为读写盘禁用，失败分类保留为唯一判据",
          "status": "completed"
        },
        {
          "id": "TASK-009",
          "step": "[TASK-009] 让 zcode 额度快照带上每档 period_end 与账号级最早重置时刻，先用真实 balance 响应验证该字段存在",
          "status": "completed"
        },
        {
          "id": "TASK-010",
          "step": "[TASK-010] 让 zcode 额度耗尽写盘禁用，并在越过重置时刻且探活成功后才解禁",
          "status": "completed"
        },
        {
          "id": "TASK-011",
          "step": "[TASK-011] zcode 收口发布：本地 E2E 验证四条链路、版本升到 0.2.0、补 CHANGELOG、6-review 风格回归",
          "status": "completed"
        }
      ]
    }
  ],
  "quarantined_projections": [
    {
      "reason_code": "invalid_projection",
      "affected_session_id": "eccae029-046a-47a1-a6f9-f417e5d06b29",
      "source_version": 5,
      "source_index": 6,
      "raw_entry": {
        "projection_id": "SESSION/eccae029046a47a1a6f9f417e5d06b29queueframefailover000000000000000000",
        "session_id": "eccae029-046a-47a1-a6f9-f417e5d06b29",
        "projection_origin": "synthesized",
        "synthesis_mode": "exact",
        "state": "active",
        "plan_key": "FIX-QODER-QUEUE-FRAME-CLASSIFY-20261015",
        "source_document": "doc/4-bugs/2026-10-15_160000_Qoder排队帧被误判账号级故障且跨provider状态码通道缺失根因诊断.md",
        "plan_fingerprint": "queue-frame-classify-and-status-channel-20261015",
        "updated_at": "2026-10-15T09:40:00Z",
        "steps": [
          {
            "id": "TASK-001",
            "step": "[TASK-001] Bug 单落盘：排队帧误判账号级 + 跨 provider 通道缺失",
            "status": "completed"
          },
          {
            "id": "TASK-002",
            "step": "[TASK-002] 第1步：排队帧不再误判为账号级故障",
            "status": "completed"
          },
          {
            "id": "TASK-003",
            "step": "[TASK-003] 第2步：envelopeError 补 http_status 打通宿主状态码通道",
            "status": "completed"
          },
          {
            "id": "TASK-004",
            "step": "[TASK-004] 宿主侧改动计划：异步流错误通道透传状态码",
            "status": "completed"
          },
          {
            "id": "TASK-005",
            "step": "[TASK-005] 真实测试：单测 + 必失败哨兵 + cgo-shim 全绿",
            "status": "completed"
          },
          {
            "id": "TASK-006",
            "step": "[TASK-006] 6-review + 发布 + 生产热重载验收",
            "status": "completed"
          }
        ]
      }
    },
    {
      "reason_code": "invalid_projection",
      "affected_session_id": "bbc80e05-e600-4495-bdc2-9fd5e1bfc7cf",
      "source_version": 5,
      "source_index": 5,
      "raw_entry": {
        "projection_id": "SESSION/dd4640b0e3990effb3a66aee0da5c3fe25288b12610cbe5e75c08c80d2a34b14",
        "session_id": "bbc80e05-e600-4495-bdc2-9fd5e1bfc7cf",
        "projection_origin": "persisted",
        "synthesis_mode": "none",
        "state": "active",
        "plan_key": "IMPL-QWENWORK-MULTI-ACCOUNT-20261018",
        "source_document": "doc/3-实施/2026-10-17_234000_QwenWorkProvider插件立项_实施总览.md",
        "plan_fingerprint": "21081b87f6808d09b806eb30cc74aedcc99e3c61226a4fa090740970209e032f",
        "updated_at": "2026-10-17T18:06:46Z",
        "steps": [
          {
            "id": "TASK-001",
            "step": "[TASK-001] 侦察 qwenwork 多账号/调度/冷却现状与 CYCLE-02 差距",
            "status": "completed"
          },
          {
            "id": "TASK-002",
            "step": "[TASK-002] 账号池与账号落盘对齐（含令牌刷新折叠式直写）",
            "status": "completed"
          },
          {
            "id": "TASK-003",
            "step": "[TASK-003] 调度三模式 + 会话粘性对齐（含 off 解析修复）",
            "status": "completed"
          },
          {
            "id": "TASK-004",
            "step": "[TASK-004] 冷却阶梯 + 换号重试对齐（账号身份 key 统一为宿主 auth.ID）",
            "status": "completed"
          },
          {
            "id": "TASK-005",
            "step": "[TASK-005] 双账号故障切换验证 + 文档与风格收口",
            "status": "completed"
          }
        ]
      }
    },
    {
      "reason_code": "invalid_projection",
      "affected_session_id": "b98da698-d42c-4920-afdf-34f3bc242d7c",
      "source_version": 5,
      "source_index": 6,
      "raw_entry": {
        "projection_id": "SESSION/b104d3bd7fe586700ffcf91a64d1b13f225f91728325744aad9b76fccf1540cb",
        "session_id": "b98da698-d42c-4920-afdf-34f3bc242d7c",
        "projection_origin": "persisted",
        "synthesis_mode": "none",
        "state": "active",
        "plan_key": "DIAG-PROJECT-DISK-USAGE-20261018",
        "source_document": "doc/4-bugs/2026-10-18_172500_项目磁盘占用过大根因诊断.md",
        "plan_fingerprint": "41eebc847b50e161b00543a1659f14721dff70011ae0728f14474bf54e37d36c",
        "updated_at": "2026-10-18T09:12:12Z",
        "steps": [
          {
            "id": "TASK-001",
            "step": "[TASK-001] 统计工作树各目录与 .git 的真实磁盘占用",
            "status": "completed"
          },
          {
            "id": "TASK-002",
            "step": "[TASK-002] 定位大体积来源：release-assets、Git 对象、缓存、构建产物",
            "status": "completed"
          },
          {
            "id": "TASK-003",
            "step": "[TASK-003] 归纳根因与安全清理建议，落盘诊断文档",
            "status": "completed"
          }
        ]
      }
    },
    {
      "reason_code": "invalid_projection",
      "affected_session_id": "8852ed0a-cb93-4902-afa3-c5e3ca0d52c0",
      "source_version": 5,
      "source_index": 4,
      "raw_entry": {
        "projection_id": "SESSION/99f9638121426a8b2925987898247cd6fc2392819869f89217807ed333c5afda",
        "session_id": "8852ed0a-cb93-4902-afa3-c5e3ca0d52c0",
        "projection_origin": "synthesized",
        "synthesis_mode": "exact",
        "state": "inactive",
        "plan_key": "IMPL-QODERWORK-FAILOVER-REFACTOR-20261025",
        "source_document": "doc/3-实施/2026-10-25_030000_QoderWork失败禁用链路重构实施总览.md",
        "plan_fingerprint": "ed6b6a2f7e5b00ff7c8171c82c29f44dd2663def8345b732e85344f0a3bbad80",
        "updated_at": "2026-10-25T04:00:00Z",
        "steps": [
          {
            "id": "TASK-001",
            "step": "[TASK-001] 管理密钥写入 .ssh 元信息（servers.json managementKey）",
            "status": "completed"
          },
          {
            "id": "TASK-002",
            "step": "[TASK-002] plugin-store install 部署 qoderwork 0.11.0 并校验落盘 .so sha256 + hot reloaded 日志",
            "status": "completed"
          },
          {
            "id": "TASK-003",
            "step": "[TASK-003] 生产 accounts / panel 接口双 200 与面板新字段验收",
            "status": "completed"
          },
          {
            "id": "TASK-004",
            "step": "[TASK-004] 生产真实入口流式行为验收 + 侧车文件生成核对",
            "status": "completed"
          }
        ]
      }
    }
  ]
}
```
<!-- END TASK PLAN PROJECTION -->
