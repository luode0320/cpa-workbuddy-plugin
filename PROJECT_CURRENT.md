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
  - `qoderwork-provider`: **0.9.44**（0.9.44 流式中途断流零泄漏换号重试 + 断流信封补 `http_status=502` + 冷却落盘失败可观测与内存兜底；0.9.40 重建上游 messages 时保留工具调用结构修复 agent 多轮往返工具结果失配对；0.9.38 签到节奏恢复每 4 小时六槽 + 移除冗余门控；0.9.37 tools 对齐修复；0.9.35 max_tokens 统一 32768 最低输出底线；0.9.34 上游 110 日调用上限 24 小时持久化冷却 + 路由/换号排除 + 面板解冷）
  - `qoder-ai-provider`: **0.1.24**（0.1.24 流式中途断流零泄漏换号重试 + 断流信封补 `http_status=502`；0.1.20 重建上游 messages 时保留工具调用结构修复 agent 多轮往返工具结果失配对；0.1.18 签到节奏恢复每 4 小时六槽 + 移除冗余门控；0.1.17 tools 对齐修复；0.1.15 max_tokens 统一 32768 最低输出底线；0.1.14 上游 110 日调用上限 24 小时持久化冷却 + 补齐 /clear-cooldown 路由注册）
  - `traework-provider`: **0.2.13**（面板签到时刻文案漂移修正为 00/04/08/12/16/20；0.2.12 定时探活降频至 4 小时 + 失败也刷新节流时间戳；0.2.11 max_tokens 32768 底线 + payload 层同步，探活改全量模型按序测试）
  - `workbuddy-provider`: **0.20.0**（0.20.0 新增连续使用轮换：单账号连续承流满 60 分钟让位、冷却 60 分钟，默认开启，含全冷放开一半防抽干；0.19.0 重新接入成长中心 7 段，修复旧版「任务永远完不成」的两个根因：从未调用 `buddy/first` 领养 + 完全没有 `POST /v2/report` 行为上报；0.18.2 面板单卡刷新补写积分快照；0.18.1 积分三档分级 + 每小时串行扫描 + 低于 100 硬禁用；0.18.0 下线 Global 耗尽自动删号 + 低额红线可配置；0.16.5 流式中途断流零泄漏换号重试 + 断流信封补 `http_status=502`）
  - `workbuddy-ai-provider`: **0.1.16**（0.1.16 授权后自动补注册地等新能力；0.1.15 定时探活降频至 4 小时 + 失败也刷新节流时间戳；0.1.14 max_tokens 32768 底线，探活改全量模型按序测试）
  - `copilot-provider`: **0.1.2**（0.1.2 调度器对齐宿主候选集语义修复越界缺陷，已发布 `0054c28`→`ba471b0`→`bca1264`→`78b4069` 并生产验收：热重载前 5 分钟 50/50 请求告警、热重载后 0/57；0.1.1 宿主 OAuth 弹窗改开插件桥接页展示设备码；0.1.0 首发：Device Flow 登录 + 内部令牌 80% 提前保活 + VS Code Copilot Chat 指纹模拟 + 动态模型发现 + 多账号池换号重试 + 流式/非流式转发 + 额度与面板）
  - `workbuddy-token-usage`: **0.2.8**（0.2.8 让退役实例的 feed 导入协程在连续 3 轮关闭句柄后自行退出；0.2.7 修复宿主配置重载换句柄窗口导致的用量记录丢失；0.2.6 移除四个图表与导出图片、下线 `/stats/trends` 及序列聚合以省算力与响应体积；0.2.5 起请求明细与维度汇总展示原始 provider id）
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-10-17 (GMT+8)

## 活动会话进展摘要

- 当前会话（2026-10-24）：**workbuddy-provider 0.20.0 连续使用轮换已发生产并完成验收**：
  - 用户诉求演进：先尝试 priority 降级（800 请求降 0、次日恢复）→ 代码取证证伪（宿主 `SessionAffinitySelector.Pick` 命中缓存时用 `getAvailableAuthsAcrossPriorities` 跨全部 priority 层校验绑定，priority 对已绑定会话完全无效）→ 改为「单账号持续用 1 小时就进入 1 小时冷却」。
  - **两个用户裁定**：① 计时语义取「连续使用」（有请求就续期，中断超 10 分钟重置）；② 防抽干——冷却前检查是否除本号外全池已冷却，若是先放开一半。
  - **实现**：新增 `usage_rotation.go`（446 行）+ `usage_rotation_test.go`（10 用例）。核心是**轮换状态独立于失败冷却**：`resetAccountFailover` 会在每次成功时清零失败冷却，复用它会让 1 小时让位冷却被第一次成功立刻抹掉。接入 4 个覆盖点——调度候选人过滤、`pickNextAuthForModel` 换号候选、执行入口前置冷冷却拦截（非流式 + 流式）、成功打点（非流式 / 流式同步 / 异步流式三条）。**能在插件入口换号是关键**：宿主的会话亲和命中时不会看 priority，但请求进入插件后由前置拦截主动换号，已绑定会话同样生效。
  - **性能自我修正**：初版 `noteRotationUsage` 每个成功请求都调 `host.auth.list`（跨进程 RPC），改为连续使用达阈值才查池；并把 `markAuthUsed` 改为返回已用时长，省掉一次加锁与整数截断。
  - **配置项**：`rotation_enabled`（**默认 true**）、`rotation_serve_minutes`（60）、`rotation_cooldown_minutes`（60）、`rotation_idle_reset_minutes`（10）。`serve` 或 `cooldown` 填 0 等同关闭。生产未显式写这些键，走代码默认。
  - **验证**：cgo-shim build+vet+test 全绿（新增 `TestRotation_EnabledByDefault`、`TestRotation_NoteUsageSkipsPoolLookupBeforeThreshold`、`TestRotationConfig_ParsesFromConfigYAML`）；**4 项变异精准捕获**（关闭全冷放开 / 忽略 serve 阈值 / 取消中断重置 / 阈值改为恒真）。gofmt 全绿（`usage_config.go` 的格式问题是 HEAD 历史遗留，新增行缩进正确）。
  - **发布链**：`97b4b08`(feat, 9 文件) → CI `40087206136` success（4 分钟，取消冗余 push run `40087200695`）→ 6/6 assets sha256 OK + 二进制内置 `0.20.0` 且含 14 处 `rotation` 符号 → `d8da4c8`(registry) → 远端 6/6 sha+size ALL PASS 且无旧版本残留。
  - **生产部署（热重载零重启）**：`hot reloaded active_version=0.20.0 retired_version=0.19.0`；落盘 `.so` sha256 `df8d066f…0a84f43f` 与本地 zip 逐字节一致，内置版本串 `0.20.0`；accounts / panel 均 200；容器未重启。
  - **生产行为验收**：3 轮流式 `deepseek-v4.1-flash` 请求全部完整返回 `[DONE]`（2482 字节/轮，无截断），插件侧无 error/fail/panic。生产当前 11 个 workbuddy 账号、9 个启用，稳态约 4-5 个可用，池容充裕。
  - **实现踩坑（外部）**：`plugin-store install` 首次与第二次均报 `direct plugin version "0.20.0" not found`，而服务器本机 `curl` raw registry 已能看到 0.20.0 —— 属踩坑 28 的宿主侧 CDN 边缘缓存滞后，等待约 3 分钟后第三次 install 成功。

- 当前会话（2026-10-24）：**workbuddy-provider 0.19.0 重新接入成长中心，已发生产并完成行为验收**：
  - 用户先给参考项目 `linguo2625469/workbuddy2api-panel`，要求分析「我们之前删掉的成长中心为什么有问题」再重新加上。多轮决策：恢复完整 7 段 / 照参考项目做进度上报 / 默认开启定时自动跑 / 通用上报层（25 个专属动作只读）/ 先探针再实现。
  - **两个根因（旧版 0.16.0 失败真相，此前无人定位）**：① **从未调用 `buddy/first` 领养 Buddy**——未领养时上游对每个 `tasks/accept` 返回 HTTP 200 + code 0，但在 `data.results[i]` 逐条带 `prerequisite not met: first_buddy (no buddy instance found)`；只判 HTTP 码的实现记成「14 accepted」，下一轮待处理集合完全不变，正是历史日志连续 6 轮 `14 accepted, 0 claimed` 的成因。② **完全没有 `POST /v2/report` 行为上报**——`git grep "v2/report" 947a83c^ -- workbuddy/` 无匹配，任务进度由服务端行为事件点亮，不发则 `current` 永不动。
  - **推翻了计划假设**：原怀疑旧版 `codebuddy.cn/v2` 域名有误；实测四个 base 的 `/tasks` 全部 200，域名从来不是问题。
  - **落地**：新增 10 个源文件 + 4 个测试文件（growth.go / _status / _actions / _tasks / _report / _run / _run_segments / _run_streak / _schedule / _handler），全部 ≤200 行；改 checkin.go / management.go / management_test.go / usage_config.go / main.go / panel.html / credits_sweep_test.go。领奖判据改为进度口径 `!claimed && target>0 && current>=target`（`accept_status=="completed"` 会回填后回滚，不可用）。
  - **验证**：cgo-shim build+vet+test 全绿（15 个新用例，3 处变异全部被捕获）；面板两段 `<script>` node --check PASS；真实 CN 账号经 WSL 编译的 c-shared `.so` + ctypes 驱动跑通完整七段（`success:true, buddy_unlocked:true, tasks_progressed:2, errors:null`）。
  - **发布链**：`51f8ee8`(feat, 30 文件) → CI `39991991017` success（4.8 分钟，同时取消 push 触发的冗余 run）→ 6/6 assets sha256 OK → `29ab9a7`(registry) → 远端 6/6 sha+size ALL PASS 且无旧版本残留。
  - **生产验收（热重载零重启）**：`hot reloaded active_version=0.19.0 retired_version=0.18.2`；落盘 `.so` sha256 `dc219e91…1218f46dc`，内置版本串 `0.19.0`、含 `growthRunBuddyAdopt` 符号；单账号实跑 `success:true, buddy_unlocked:true, tasks_accepted:4, tasks_progressed:2, credits_gained:8, energy_balance:8, errors:null`；accounts / panel / growth 三端点均 200；容器 Up 24h 未重启。
  - **配置**：生产未显式设置 `growth_auto`，走代码默认 `true`，与签到共用 4 小时 tick；关闭后仅保留面板手动执行。
  - **受限项**：浏览器点击验证未做（本地无 CPA 实例）；4 个消耗性端点（makeup / redeem / lottery / buddy-open）为遵守探针纪律未单独探测，仅在真实一次运行中按编排触发；真实 Global 账号路径仅单测覆盖（生产 Global 账号数为 0）。
  - 文档：`doc/2-需求/2026-10-24_120000_workbuddy成长中心重新接入需求.md`、`doc/5-tests/…验证.md`、`doc/6-review/…_6-review.md`（STYLE: PASS）。

- 当前会话（2026-10-23）：**workbuddy-ai 0.1.22 与 zcode 0.1.35 版本就绪并生产落盘（未启用）**：
  - 用户先问「f961f2eb 会话的合并改动是否一起提交发布了」，查清：该会话产出 `121931d`（README 六插件说明，纯文档 +51/−7、父提交我的 `bde747d`），**已提交且已在远端 main**；但它同时合并进来的两处**代码修复从未发布过二进制**。
  - **两处未发布修复**：① `5ab431d`（10-22 23:37，contributor Guichena）workbuddy-ai `backendHeaders` 补四个会话追踪头（`X-Conversation-ID`/`X-Conversation-Request-ID`/`X-Conversation-Message-ID`/`X-Root-Request-ID`，标准库 UUID v4，根请求 ID 与当前关联），修国际版部分账号持续 `400/11128` 渠道校验失败；② `00a7e88`（10-13）zcode 显式声明 `pluginSchemaVersion=6`，避免宿主把 OAuth URL 的 `&` 转成 `&amp;` 致 `response_type`/`redirect_uri`/`state` 丢失，**要求宿主 ≥ v7.2.155**。
  - **发布前生产事实**：10 个插件只有 3 个真正启用（qoderwork / workbuddy / token-usage），workbuddy-ai 与 zcode 均 `registered=false enabled=false`，zcode 连 `configured=false`、磁盘只到 0.1.27（从未装 0.1.34）。用户选「只发版不启用」。
  - **发布链**：`d879fb6`(版本就绪) → 各自 dispatch → workbuddy-ai CI `39812602344` 19/19 success；zcode 首个 run `39812612168` 的 **Release job 卡在 `actions/checkout@v4` 30+ 分钟**（18 个 Test/Build 已全 success）→ 取消重发 `39817830371` 19/19 success → 各 6/6 assets sha256 OK → 产物字节探针（workbuddy-ai `0.1.22`×2 / `0.1.21`×0，zcode `0.1.35`×2 / `0.1.34`×0）→ `de343c6`(registry 双插件) → 远端 12/12 sha+size ALL PASS、无旧版本残留。
  - **生产部署（零容器重启）**：workbuddy-ai `hot reloaded active_version=0.1.22 retired_version=0.1.21`、落盘 sha256 `d4bb6d1b…ba2de5`；zcode `plugin loaded` + `plugin registered`（**证明宿主接受 schema 6**）、落盘 sha256 `eff83df4…cb0644`，两者均与本地 zip 逐字节一致。
  - **踩坑（重要）**：`plugin-store install` 会**自动写 `plugins_enabled:true`**，即「install 必然启用」——与用户「只发版不启用」相悖。已用 `PUT /v0/management/plugins/<id>/config {"enabled":false}` 把两者改回停用；但中途误用 `DELETE /v0/management/plugins/workbuddy-ai-provider`（以为它是"禁用"）把该插件从配置中移除，随后重新 install 恢复。**最终状态**：workbuddy-ai 与 zcode 均 `enabled=false registered=false`，其余 8 个插件状态逐项未变，容器 Up 4 days 未重启；唯一与原始状态的差异是 zcode 的 `configured` 由 `false` 变 `true`（install 必留配置项，属预期）。
  - **遗留待办**：① zcode 启用需宿主 ≥ v7.2.155（本次实测可加载 schema 6，未接流量验证）；② workbuddy-ai 启用前需确认账号迁移完成。

- 当前会话（2026-10-23）：**workbuddy-provider 0.18.0：下线 Global 耗尽自动删号 + 低额红线登记为可配置项**：
  - 用户提出两点：「Global 账号耗尽自动删除凭据」删掉（理由：已有 workbuddy-ai 专做国际版）；「安全缓冲红线默认 400 点」改为「可以在配置文件配置修改」。
  - **第 1 点是行为而非文案**：`lifecycleActionFor` 的 `region=="global" → lifecycleDelete` 会由 `reconcileOneAccount` 物理删号。改为 CN/Global 一视同仁只写盘禁用；同时把恢复分支的 `region == "cn"` 前置条件去掉，否则 Global 一旦被禁用将永久无法自动恢复（这是改行为的隐藏陷阱）。`shouldReenableCN` 更名 `shouldReenable`。
  - **连带清理孤儿**（删号链移除后零调用方）：`lifecycleDelete` 枚举与 String 分支、`applyExhaustedPolicy`、`deleteAuth`、`peerAuthDir`、`hostAuthPersist`、`hostAuthPersistMigrate`、`hostAuthSaveJSON`，以及 `lifecycle.go` 的 `fmt`/`filepath` import。
  - **第 2 点的真实缺口**：`configure()` 早已解析 `min_routable_credits`，但它**没有登记进 `main.go` 的 `ConfigFields`**，管理端根本看不到这个键——配置文件里写有效，界面无处可改。补登记为 `ConfigFieldTypeInteger`（默认 400、显式 0 关闭）。
  - **验证**：cgo-shim build+vet+test 全绿；新增 `TestMinRoutableCredits_RegisteredAsConfigField` 与 `TestNoAutomaticAccountDeletion`；**2 项变异反证均精准 FAIL**（删配置登记 → 前者 FAIL；把 `deleteAuthFileInDir` 重新接回 Global 分支 → 后者 FAIL）；gofmt PASS；panel.html 两块脚本 `node --check` PASS。
  - **发布链**：`67bc26e`(feat) + `bde747d`(README 修正) → CI `39808767311` success → 6/6 assets sha256 OK → `d9c5afd`(registry) + `ad255d1`(描述修正) → 远端 6/6 全 OK 且无旧版本残留。
  - **生产验收（热重载零重启）**：`hot reloaded active_version=0.18.0 retired_version=0.17.1`；落盘 `.so` sha256 `7330d4da…8abff03` 与本地 zip 逐字节一致；`/accounts` 返回 `min_routable_credits=400`、26 个账号在位；Global 账号数为 0（生产早已迁到 workbuddy-ai，正合本次修改目的）；面板 200、插件 `configured/registered/enabled` 全 True；容器 **Up 4 days 未重启**。
  - **踩坑（重要）**：`git commit -- <path>` 提交的是**工作树内容**而非索引内容，第一次提交因此把另一并行会话未提交的六插件 README 段落一并带入；已用后续提交回退该文件，并用备份把工作树内容原样还原（他人的改动仍完整保留在工作树，未提交）。
  - 文档：`workbuddy/CHANGELOG.md` 0.18.0；根 `README.md`、`workbuddy/README.md`、`README_CN.md`、`docs/architecture.md`、面板副标题均改为「不自动删号 + 红线可配置」；`PROJECT_MEMORY.md` 新增生命周期口径条目。

- 当前会话（2026-10-22）：**workbuddy-provider 0.17.1：低额红线默认 20 → 400 并已发生产**：
  - 用户要求「低额改成 400，并且核验一下是否生效」。**改动**：`policy.go` `defaultMinRoutableCredits` 20→400；面板低额判定 / 徽标文案 / 统计分段三处硬编码 20 改为消费 `/accounts` 新增的 `min_routable_credits` 字段；测试改用相对红线的 `lowCredits()` / `healthyCredits()` 构造数据，新增默认值锚定与 399/400 分界用例（锁死严格 `<`）。
  - **验证**：cgo-shim build+vet+test 全绿；三组变异——阈值回 20（2 用例 FAIL）、`<` 改 `<=`（边界用例 FAIL）均被捕获；第三组「删面板下发字段」首轮**存活**（`buildDashboardEx` 无 host seam，补测需改生产代码，按规则不补），改为前端 `minRoutableCredits=0` 时只依赖后端 `low_credits` 标记降级。
  - **发布链**：`44979a0`(fix 9 文件) → CI `39800921287` success（cancel 冗余 push run `39800886763`）→ 6/6 assets sha256 OK → `c96d4c1`(registry 0.17.1) → 远端 raw ALL PASS 且无 0.17.0 残留。
  - **生产验收（热重载零重启）**：`hot reloaded active_version=0.17.1 retired_version=0.17.0`；落盘 `.so` sha256 `913a1092…c74842` 与本地 zip 逐字节一致；`/accounts` 返回 `min_routable_credits=400`；刷新后 26 号中 21 个有积分数据，`remain=372` 判 `low_credits=true`（旧红线 20 下不会触发）、`remain=590` 仍可路由。按用户选择未在生产配置显式写该键，走代码默认。
  - **待办**：其余插件（workbuddy-ai / traework / qoderwork / qwenwork / qoder-ai）红线仍为 20；如需统一到 400 需各自发版。

- 当前会话（2026-10-22）：**下线 doubao-provider 与 notion-provider（13 → 11 插件）**：
  - 用户裁定「这两个插件可以从我们项目删除了」，并选择「仓库删 + 生产卸载」与「doc 历史文档一并删除」。
  - **仓库侧删除面**：源码 `doubao/`（45）、`notion/`（39）、测试镜像 `test/doubao`（8）、`test/notion`（5）、`assets/icons/Doubao.png` + `Notion.png`、`doc/` 专属文档 27 份（需求 2 / 实施 2 / bug 1 / 测试 10 / 6-review 12）；`registry.json` 13 → 11、`.github/workflows/build.yml` 清理 8 处接入点、`.gitignore` 移除指向已删目录的 `doubao/.local_cookie.txt`；本地 `release-assets/` 中两个插件约 250MB 缓存与遗留 `doubao/.local_cookie.txt` 一并清除。
  - **生产侧关键发现**：两个插件**早已**是 `configured:false / registered:false / enabled:false`——生效配置 `config.yaml` 的 8 个插件键里本就没有它们（历史 `DELETE /v8/management/plugins/<id>` 的后果），生产只剩 14 个 `.so`（doubao 3 + notion 11）与 2 个账号凭据目录占盘，`/accounts` 均 404。故本轮「卸载」实为清理磁盘残留：备份 `/root/plugin-removal-backup-20261022_223520` 后删除全部 `.so` 与 `doubao_accounts` / `notion_accounts`。
  - **验证**：workbuddy / coze / token-usage 三插件 cgo-shim build+vet 全绿；11 个 CI 矩阵目录与 registry 条目一一对应且全部存在；全仓无 `doubao-provider` / `notion-provider` 硬引用残留（剩余命中全是 `doubao-seed-*` 模型名与历史文档中的对比引用）；生产插件列表 12 → 10 项、doubao/notion 完全消失且其余插件状态逐项未变，管理面 `plugins=200` / `config=200`、外网 `/v1/models=401`（正常鉴权），容器 `Up 3 days` 未重启。
  - **提交**：`34f6aa9`（129 文件 = 126 删 + 3 改），已推送 main。
  - **遗留待办**：① CI run `39796599752` 长时间 queued，未在本轮等到完成，需回查确认 11 插件测试矩阵全绿；② `README.md` 与 `项目设计.md` 的插件数表述仍是「9 个」（本轮之前就已滞后于实际 13 个），本轮未顺手改写，如需对齐当前 11 个应单独处理。

- 当前会话（2026-10-21）：**coze 0.5.4：全池积分耗尽时快速失败，止血上游白打与错误风暴**：
  - 用户报障（附截图）：生成一张图刷出 9 条 `Failed to generate image`（20:11 / 21:12 各一批）。**与 0.5.3 的晚到附件问题无关**，是另一条独立缺陷。
  - **根因（生产日志实证）**：0.5.2 闸门兜底语义是「全池低额则放行」，本意别把最后一个残血号挡掉；但全池都是 0 分时「放行」等于把请求交回宿主逐个白打上游。四个时段的 request_id **每个都轮满 7 个号**、每条 1.2–3.2 秒才拿到上游 `1000521 该用户积分已耗尽`，闸门在这些时段**零拦截**（`低于路由红线` 计数为 0）；叠加 new-api `RetryTimes=5` 且自动重试码 `401-407,409-428,430-503` 恰好跳过 400，一次出图放大成 9 条错误。
  - **修复**：新增「整池确定 0 分」快速失败通道，返回 **400**（`creditsPoolDepletedError`）——命中宿主请求级白名单 `clienterror.IsRequestFault`（400/409/413/422）立即终止换号轮转且不冷却账号，又不在 new-api 重试区间，两层重试一起止住、零上游消耗。判据只认 `remain` **恰好为 0**（残血非零仍按原语义放行让上游自己判），并要求本号与池内每个可用号的积分数据都在 `creditDepletedTrustWindow`（15 分钟，故意宽于 5 分钟缓存 TTL，免得刷新边界期把刚拿到的 0 分判成不可信而使快速失败失效）内新鲜；任一账号未知/过期、或快照为空（刚热重载未跑首轮）一律退回放行；红线配 0 时两条判据一并失效，无需额外开关。
  - **机制实证（对照实验）**：用既有 413 分支（同走同步信封、同属请求级白名单）验证——客户端发 70000 字节 → `HTTP=413 41ms`，宿主侧该 request_id **只有 1 条执行日志**、未轮转到第二个号，413 原样透传调用方。⇒「同步信封带请求级 4xx」确实既终止轮转又到达客户端。**400 分支自身上线时尚未在生产触发过**（它要求整池确定 0 分，验收时池里有 2 个健康号），不得声称已生产实证。
  - **验证**：cgo-shim build+vet+test 全绿；新增 9 用例（全池 0 分必须 400、有健康号仍走 403、快照为空放行、本号过期放行、他号过期放行、未知积分不判耗尽、残血非零放行、本号健康但快照滞后放行、穿过 `handleExecExecute` 的零上游断言）；**7 项变异反证全部 KILLED**，其中「池内不校验缓存新鲜度」与「忽略 kill switch」首轮**存活**——前者补 `TestVerifyAccountCreditsAllowsWhenPeerCreditCacheStale` 后捕获，后者暴露 `redline > 0` 是死代码（`remain < redline` 已隐含闸门开启），已删。
  - **发布链**：提交 `2310041` + registry `320249d` → CI run `39622568217` **21 job 全 success**（本次 darwin/arm64 未被排队拖死）→ 6/6 assets `ALL CHECKSUMS OK` → 产物内嵌 `0.5.4`×2、`0.5.3` 归零、新文案「账号池积分已耗尽」命中 → 远端 6/6 assets 200 且字节数一致 → 生产 `plugin-store install` 热重载（`active_version=0.5.4 retired_version=0.5.3`，**零容器重启**）→ 落盘 `.so` sha256 `32217406a64be9e754eb1f1fb2d97f7d694a3e9fb36ff088bd3d13c02d69e4c5` 与本地逐字节一致。
  - **生产回归（request_id 159372c8，23:15）**：0 分号 3ms 被 403 拦截 → 换健康号 → **30.3 秒 HTTP 200 出图**，确认未破坏 0.5.3 的晚到附件路径与既有换号语义。
  - **遗留待办（仍未闭合）**：① token-usage-tracker 把管理密钥当 `source` 打印到日志（`source=Ld@588588`），属凭据回显违规，需在该插件侧修复；② coze 的 `min_routable_credits` 未写入生产配置，走默认 100。
  - 文档：`coze/CHANGELOG.md` 0.5.4 章节；路由口径同步见 `PROJECT_MEMORY.md`。

- 当前会话（2026-10-20）：**coze 0.5.3：修复「生图全部失败但积分被烧光」**：
  - 用户报障（附 new-api 日志 + CPA 用量面板截图）：`coze/doubao-seedream-5-0-260128` 请求几乎全部 502/403 失败，账号 7840 积分却耗尽。
  - **根因（收尾时序错配）**：WS 的 `event_final` 帧 6–9 秒就到达，正文只有 `[图片](computer:///...)` 占位符（非空），真实图片附件要晚十几秒到几分钟才落库 `message/list` 的 `files`。旧逻辑只在「正文为空」时兜底轮询 → 生图路径不轮询、不报错 → 空 images → `buildImagesResponse` 报「上游未返回任何图片」。**上游已出图并扣额度**。
  - **放大链（额度成倍烧）**：该错误无状态码 → 兜底 502 → 宿主判账号故障、冷却并换号；new-api `AutomaticRetryStatusCodes` 含 502 且 `RetryTimes=5` → 同一笔额度反复烧。两层重试同时叠加。
  - **修复**：① `runConversation` 收口统一调 `awaitImages`（不用 `waitForReply` 的分支，因为它有三个成功返回分支，分支内处理必漏路径）；② `pollImagesUntilAvailable` 1 秒间隔、**4 分钟**上限（生产实测成功生图端到端 24s~3m6s，90 秒会误判成功为失败）；③ 只在附件非空时 `accept`（去重键 `turn|seq|phase`，先 accept 空附件会把后续带附件同帧判重丢弃）；④ 取不到时返回 `imagesUnavailableError`（**400**）——命中宿主请求级白名单（400/409/413/422）不冷却不换号，且不在 new-api 自动重试区间，两层重试同时止住；⑤ `wantImages` 标记区分生图/对话，文本路径不为等附件变慢。
  - **超时链路核实**：nginx `proxy_read_timeout 600s` > 4 分钟上限；宿主无额外插件 RPC 超时；生产有 3m6s 成功实测。
  - **验证**：cgo-shim build+vet+test 全绿；新增 6 用例（晚到附件、第 3 轮才出现、取不到须 400、对话路径不长等、错误状态码、穿过真实入口的端到端）；**5 项变异反证全 FAIL**（禁用等附件 / 状态码退化 / 只查一次 / 工厂指向对话执行器 / `awaitImages` 直接放行）。其中「工厂指向对话执行器」首轮**存活**——所有用例都直接构造执行器、无一条穿过 `handleImagesExecute`，是「纯函数单测通过 ≠ 调用点被覆盖」的典型形态，补端到端用例 + 生产侧注入点后才捕获。
  - **发布链**：提交 `5535ec3` → CI run `39464339820` 首轮 darwin/arm64 job 排队超时被取消（其余 19 job 全 success）→ `rerun-failed-jobs` 重跑。
  - 文档：`coze/CHANGELOG.md` 0.5.3 章节。

- 当前会话（2026-10-20）：**coze 0.5.2 发布生产：新增积分路由红线（默认 100 分）**：
  - 用户需求：「低积分跳过路由，但 coze 积分消耗速度不同，改成 100」。**方案裁定**：宿主 `schedulerRecord()` 只返回排序后第一个 scheduler 插件（全局单例，在任者 copilot-provider），coze 抢槽会顶掉 workbuddy 调度内核，且插件 auth 的 `priority` 不被读取 ⇒ 放弃声明 Scheduler，改**执行入口闸门**。
  - **实现**：`coze/policy.go`（红线 100 + 缓存判定 + 403 `creditsBelowRedlineError`）、`coze/credit_loop.go`（后台刷新 300s、并发 4、发布 `creditPoolSnapshot`）、两个执行入口在 `parseStoredAuth` 后加闸门、面板 `low_credits` 徽标；配置项 `min_routable_credits`（`*int64`，缺省保持 100、显式 0 关闭）。闸门**只读内存快照**，禁止在请求路径调 `host.auth.list`（由 `TestVerifyAccountCreditsUsesMemorySnapshotOnly` 守护）。
  - **验证**：cgo-shim 全绿；新增 12 用例；8 项变异全部精确 FAIL（其中「删快照发布」与「未知积分当 0」首轮存活，补强用例后捕获）。**发布链**：`b46969c` + registry `b07d209` → CI `39452488433` success → 6/6 assets OK → 生产热重载 `active_version=0.5.2`（零重启）→ `.so` sha256 `1b13157e…3663e` 与 zip 一致。
  - **生产验收（request_id 86564019）**：5 个号中 4 个 0 分、1 个 437 分；0 分号 2–10ms 内 403 拦截零上游消耗，换健康号后连续 3 次 200；面板 `low_credits` 正确标出 4 个 0 分号。
  - 文档：`coze/CHANGELOG.md` 0.5.2 章节。

- 当前会话（2026-10-19）：**coze 0.4.6 发布生产：64KB 内容超限不再拖垮账号池**：`message/chat` 的 content 有 65535 字节硬上限，越界返回 `1000003 数据库错误`（无状态码）→ 宿主按 5xx 判账号级故障、依次冷却两号 → 单次超长请求放大成整池 503。修复：两个入口按 UTF-8 字节预检返回 **413** 请求级错误；`envelopeError` 补 `HTTPStatus`/`Retryable` 与 `requestFaultEnvelope`，宿主识别为请求级故障即跳过冷却与换号。验证 6 用例 + 3 次变异精确捕获；生产实测 70016 字节请求 **0.03s 返回 413**（修复前 5.8s/500）。**踩坑**：生产生效配置是容器内 `/CLIProxyAPI/config.yaml`（明文 secret-key），不是 MySQL spool 那份（bcrypt 哈希，用它认证必 401）。
- 当前会话（2026-10-19）：**定时任务并发治理三连（均未发布生产）**：① 6 插件 4h 槽位改走 `next*FireTime(now)` + `schedulerJitterMax=5m` 可注入抖动；traework `checkinRetryLoop` 原无界 goroutine → 收敛为 `runCheckinRetryRound()` + 并发 4；② 六个 watchdog 首轮加抖动（插在 `waitHostReadyForWatchdog` 之后，等待不可被 tick 打断，否则重新同刻对齐）；③ 只读复查发现唯一无上限扇出是 coze `/accounts`（建议补 `sem=4`，未修）。验证：全插件 cgo-shim 全绿、三处变异精确捕获。
- 当前会话（2026-10-19）：**账号故障冷却基数 30s → 60s（未发布生产）**：
  - 用户需求：「统一的 30s 冷却递增改成 60s 开始递增 60s」，裁定为 **60s、120s、180s…线性递增、上限仍 10 分钟**。
  - **实现**：各插件 `failoverCooldownBase` 30s→60s（已下线插件当时也在范围），`LadderCap` 由 `max/base` 自动从 20 档变 10 档；公式不变（仍 `base × count`），账号×模型双层冷却共用同一函数。
  - **验证**：当轮 10 插件 cgo-shim build+vet+test 全绿；gofmt 干净；`POLLUTION: PASS`；变异验证（workbuddy 基数改回 30s）→ 4 个用例精确 FAIL 并打印 `= 30s, want 1m0s`，还原后全绿。
  - **并发盘点（只读）**：积分刷新走 `refreshConcurrency=10` worker 池（6 插件）；签到/保活 sem=4；coze 续期并发 4；zcode 单 worker + 300ms；traework 自动签到与 qwenwork 对账串行。全仓无跨插件共享限流，4h 槽位整点对齐致整机峰值约 70–80 个上游请求同刻在场。
  - 文档：`doc/3-实施/2026-10-19_213000_账号冷却基数30s改60s实施总览.md`。
- 当前会话（2026-10-19）：**Qoder 系模型发现风暴与 OOM 修复 P0/P1/P2 本地实施（按约束未发布生产）**：
  - 放大器链：`callModelsAPI` 签名 body 与 GET 实际 body 不一致 → `Signature invalid` 恒 403（累计 5449 条、从未成功）→ 失败不写任何缓存 → 宿主每次 auth 重写触发 `model.for_auth` 逐账号全量重扫（实测 8409 条 N+1）。
  - P0 改 GET 真实携带与签名同源的 `qoderEncode("{}")`（方向由外部可工作实现 qoder2api-hub 裁定）；P1 补 `failedAt` 负缓存 5min 切断风暴；P2 在 `pickNextAuth` 单点收口 50ms 退避 + 错误体 `io.LimitReader` 截断。复核追加修复「先开窗再回退致回退成死路径」。
  - **P0/P1/P2 逐项细节、9 个新测试文件与变异复验见 `doc/3-实施/2026-10-19_133000_Qoder系模型发现风暴与OOM修复P0P1P2实施总览.md`；生产发布与验收由后续会话完成（见 git log 与 `PROJECT_MEMORY.md`）。**
- 前序会话（2026-10-18）：**coze 0.3.0 发布生产并连修四处缺陷（0.3.1~0.3.4），后续 0.3.5/0.3.6/0.4.0/0.4.1 同会话收口**：四个根因（流式 channel 塞 `Chunks` 致序列化失败 → `host.stream.emit`；WS 缺 Cookie 只推心跳 → 补 `cookieHeader`；final 是累积全文致正文重复 → final 只留 Snapshot；固定 300ms 猜就绪 → `WaitReady` + 按 `reply_to_message_id` 精确配对）。另 0.3.6 修 `/v1/responses` 必然 503（`executorRequestBody` 改 Payload 优先）、0.4.1 修下游用量全记零。均热重载零重启验收。**细节见 `coze/CHANGELOG.md` 0.3.0~0.4.1 各章节**。未闭合待办：new-api 渠道 4「混合」的 `models` 需手工追加 coze 模型名。
- 前序会话（2026-10-18）：**coze TASK-004 对话链路实现完成（v0.3.0），真流式落地**：新增 `frontier_frame.go`（12 字段 protobuf）、`frontier.go`（WS 客户端 + 心跳 + 退避重连）、`claw.go`、`convert.go`、`executor.go`（`turn_id+seq_num+phase` 去重）。实现中修两缺陷：心跳协程在断开后空等 10s（改为读循环结束即取消）、增量被合并成大块（改 `pending` 队列逐帧 emit）。cgo-shim 全绿、46 用例、3 次变异精确捕获。

- 前序会话（2026-10-18，已压缩）：**coze CYCLE-01 协议取证：抓到真流式帧，TASK-004 阻断解除**：浏览器内给 `WebSocket.prototype.send` 打补丁捕获真实 WS 实例，抓到 100 帧业务数据。关键实测：`aid=751148`/`fpid=1289`/`xsack=0`/`pbbp2`（`connection-params` 的 `aid=578287`/`service=33555616` 是另一条通道，用错只收心跳）；claw 业务 `service=33555657`；阶段机 `event_start`→`event_delta`→`event_final`；`content.message_data.reply.content` 是 delta、`content_v2.session.final` 是累积快照；去重键 `turn_id+seq_num+phase`。见 `doc/5-tests/2026-10-18_164500_Coze对话链路协议补全.md` §4.6。
- 前序会话（2026-10-18）：**coze-provider 0.2.2 定时续期改由插件自建定时器执行**：宿主 `refresh_interval_seconds` 只决定排期，`auto_refresh_loop.handleDueAuth` 在 `exec == nil` 时仅空转重排、不调用 `auth.refresh`；coze 声明 `Executor:false` 故宿主排期对本插件永不产生续期。新增 `coze/renew_loop.go`（默认 4h、并发 4、热重载即跑首轮）。顺带修真实缺陷：`applyRenewal` 原防覆盖判据比对对象永远是文件自身，外部重新授权的新凭据会被旧快照静默覆盖；改为读取时锚定 `StoredAuth.LoadedDigest`。已发布并生产热重载验收。
- 前序会话（2026-10-18）：**coze-provider 0.2.0 模型动态发现 + 面板重做**：声明 `model_provider` 并实现 `model.static`/`model.for_auth`（`/v1/model/list` 的 `model_type` 必须是字符串枚举，多模态目录才有完整视频族，生产实测 16 模型）；面板移除授权区、按 workbuddy 风格重做、加 `/models` 与 `/test-active`、时间统一 `Asia/Shanghai`。已发布 0.2.0 并生产热重载验收（`active_version=0.2.0`、`/test-active` 可见 16 模型）。未验证：真实推理链路（属 CYCLE-01）。
- 会话（2026-10-18，已压缩）：**qwenwork CYCLE-02 多账号调度：修复账号身份 key 失配（P0）**：执行器用 `auth.accessToken` 当账号身份，导致候选、冷却、会话绑定错位；改为账号维度稳定 key 并补回归；发布 0.2.x 生产热重载零重启。
- 当前会话（2026-10-18）：**qwenwork-provider 0.1.0 立项实现并全链路发布**：Device Flow + PKCE（`gateway.qwenwork.cn`）换宿主 JWT → `POST /api/desktop-sessions`（**201 非 200**）签发 `sk-mm-v1.*` 短票据（TTL 2h）→ 裸 Bearer 打 `{base_url}/v1/chat/completions`；MaaS 无 `/v1/models`，目录在 `GET /api/desktop/chat-modes`。推理走**请求体透传**（仅强制 `stream=true` + `max_tokens` 自适应），管理路由收敛 13 条。发布链 `a9585c7` → CI `39050879075` → `91e050e` → `14bc392`(6 平台) → `3568d37`(registry)；图标改用仓库内 `assets/icons/QwenWork.png`（原外链 404）。
- 当前会话（2026-10-18）：**发布矩阵下线 freebsd-amd64，平台数 7 → 6**：根因 `go-cross/cgo-actions@v1` 硬编码下载 FreeBSD `14.3-RELEASE/base.txz`，该版本已从官方源下架（404），曾致全部 10 插件 freebsd 构建集体失败并连带 Release job skipped；直接下线该平台根治。改动 `build.yml`（删 12 条矩阵项与 wget 补丁）、`publish-assets.py`（删 `freebsd_amd64` 与逃生门）、`registry.json`（10 插件各 7→6）。CI run `39052278051` 84 success / 1 skipped / 0 failure。见 `doc/6-review/2026-10-18_010000_发布矩阵下线freebsd平台_6-review.md`。
- 当前会话（2026-10-17）：**qoder 三插件流式断流换号与冷却落盘修复（CYCLE-01 + CYCLE-02 全部闭环）**：根因三层（断流分支无换号路径 + 断流信封无状态码 + 24h 冷却 92 次触发零落盘）。qoderwork / workbuddy / qoder-ai 新增 `isStreamReadError` + `shouldRotateOnStreamReadError`，信封补 `http_status=502`。已发布 qoderwork 0.9.44 / workbuddy 0.16.5 / qoder-ai 0.1.24，热重载零重启。**未实证项与验收边界见 `doc/4-bugs/2026-10-17_202500_qoderwork流式中途断流不换号且24h冷却未落盘根因诊断.md`。**
- 会话（2026-10-15，已压缩）：**跨插件同步第二轮复检（只读审计）**：md5 对比 + 机制分布 grep + 版本矩阵复核 9 插件，原批次 A~D 全部闭环。

- 会话（2026-10-15，已压缩）：**qoder 双插件重建上游 messages 保留工具调用结构（qoderwork 0.9.40 / qoder-ai 0.1.20）**：根因是 baseprompt 的 agent 上下文 + 插件重建 messages 丢掉 `tool_calls`/`tool_call_id`；补三字段并同步自定义 `UnmarshalJSON`（陷阱：加字段必须同步解析否则恒空）。**判别力边界**：只修结构丢失，探索型任务仍不收敛。

- 会话（2026-10-15，已压缩）：**五插件签到节奏统一为每 4 小时**：qoderwork/qoder-ai 由 `[10,22]` 恢复为 `[0,4,8,12,16,20]` 并移除失效门控；修正面板与文档错标的时段文案；四插件发布生产、零重启，远端 28/28 ALL PASS。

- 会话（2026-10-14/15，已压缩）：**zcode-provider 中国区支持与额度领取链路（0.1.22→0.1.31）**：bigmodel OAuth 按 provider/域名/回调路径参数化复用 CLI 轮询链路；面板内无感验证领取额度（`captchaVerifyParam` 需带 `prefix`/`region`，修复后含 `securityToken`）；额度展示改走 `billing/balance` 取真实已用/剩余，面板改逐模型独立进度条。**未覆盖**：上游对全部账号 `billing/preview` 返回 `plans:[]`，完整激活未实证（上游活动态）。

- 会话（2026-10-14，已压缩）：**四插件定时探活降频至 4 小时并修失败账号节流缺陷**：`recordActivePing` 原仅成功时刷新时间戳，失败账号不计时 → 每个 tick 被反复重测；改为「无论成败都先刷新」。

- 会话（2026-10-14/15，已压缩）：**zcode-provider 0.1.22 领取指引**：改为如实 Start Plan 指引（`guideModal`）；根因：额度领取须官方客户端手动完成（claim 强制阿里云人机验证头，无头一律 `400 3007`）。

- 历史会话（2026-10-15，已压缩）：**copilot-provider 立项与 0.1.0/0.1.1 两轮发布**。要点留存：宿主 `ServePluginAuthURL`（`auth_files_oauth_callback.go:218`）不把插件 `Metadata` 回传前端 → 0.1.1 改用**桥接页**方案（`/v0/resource/plugins/copilot-provider/login-bridge?state=…`，resource 路由是插件唯一免鉴权 GET 面），桥接页**只展示不轮询**（轮询成功即 read-once 删除会话，桥接页自行轮询会偷走凭据）。两轮均热重载零重启、7/7 artifacts ALL PASS。**Kiro 裁定不立项**：FAQ 明文禁第三方 harness、免费档仅 50 credits/月、有终局封禁实证。文档：`doc/3-实施/2026-10-16_010000_copilot宿主OAuth弹窗设备码桥接页实施总览.md`、`doc/3-实施/2026-10-15_Copilot与Kiro插件立项实施总览.md`、`doc/3-实施/2026-10-15_235000_Kiro可行性评估报告.md`。

- 会话（2026-10-13，更早历史已压缩，详见 git log 与 PROJECT_HISTORY.md）：
  - **六插件 max_tokens 统一 32768 底线**：`applyMaxTokensFloor` 四出口收敛；zcode 规格矩阵 14→64 条；四插件探活改「全量按序 any-success」。
  - **Qoder 110 日上限 24h 持久化冷却**：`isDailyCountExceeded` 精确识别 110；`daily_cooling_until` 落盘 + 内存镜像 + watchdog 重建；路由/换号/粘性绝对排除，`resetAccountFailover` 刻意保留；新增 `POST /clear-cooldown` 与面板徽标。
  - **max_tokens 官方规格自适应**：Qoder 客户端写死 `max_output_tokens=8192` 致思维链吃满配额、正文被 `finish_reason: length` 截断；内置规格表，推理模式放宽至官方上限、超限 clamp。
  - **ZCode 0.1.14/0.1.15**：修 `usage_feed.go` 孤立路径与缺 `tokens` 嵌套、`ExecutorModelScopeStatic` 误声明致别名未绑定（改 `ExecutorModelScopeOAuth`）；claim 需 `X-Aliyun-Captcha-Verify-Param` + `sgp`（后被 0.1.28 无感验证取代）。
  - **qoder 排队帧未识别致 60s 挂死（0.1.9 / 0.9.30）**：`parseQoderSSEChunk` 增第 4 类顶层 `code` 形态 + `unwrapNestedMessage` 逐层展开；哨兵反证移除后精确复现 10.00s 挂死。
  - **删除 Gemini 插件**（体积 14.4MB、强依赖 GCP 结算、无面板与容灾）；**ZCode OAuth 重构为官方 CLI 轮询**（0.1.5→0.1.8，deep-link 唤起本地客户端）；**Qoder 403 首包假成功与流式诊断**（生产 nginx `proxy_buffering off` 后外网 1.35s）；**deepseek-v4.1-flash 推理断开根因**（同名裸模型多服务商认领 + 会话亲和钉死）；**Token 用量面板移除 4 图表**（0.2.6~0.2.8）。

- （更早的 13 条会话进展摘要已按 51200 字节上限裁剪，历史见 git log 与 PROJECT_HISTORY.md）

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
  "registry_updated_at": "2026-10-22T17:50:15.790173Z",
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
          "status": "pending"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 发布与生产部署验证：commit/push/CI/资产入库/registry/热重载验收",
          "status": "pending"
        }
      ]
    },
    {
      "projection_id": "SESSION/4c4dab02fd732c0d17940754bc3095a8a25815699011645d49b517efc9cac1bb",
      "session_id": "75082e35-bf34-49e3-8ec9-d8c884098190",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "inactive",
      "plan_key": "FIX-QODER-STREAM-READ-FAILOVER-20261017",
      "source_document": "doc/3-实施/2026-10-17_203500_qoder三插件流式断流换号与冷却落盘修复实施总览.md",
      "plan_fingerprint": "f9fba8f7e10ccf53f10814cc188a002c7c92165a7c6c4561909339e3c52a3b70",
      "updated_at": "2026-10-17T13:26:54Z",
      "steps": [
        {
          "id": "TASK-001",
          "step": "[TASK-001] qoderwork 零泄漏断流换号：断流分支接入换号决策与重建请求",
          "status": "completed"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] qoderwork 断流错误信封补 http_status=502",
          "status": "completed"
        },
        {
          "id": "TASK-003",
          "step": "[TASK-003] qoderwork 冷却落盘失败补日志并保留内存冷却兜底",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] workbuddy 断流换号并新增信封状态码通道",
          "status": "completed"
        },
        {
          "id": "TASK-005",
          "step": "[TASK-005] qoder-ai 断流换号与信封状态码扩展",
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
    }
  ]
}
```
<!-- END TASK PLAN PROJECTION -->
