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

- 状态：活跃维护中。11 大插件全景（10 Provider + 1 用量统计）。**2026-10-22 下线 doubao-provider 与 notion-provider**（用户裁定移除），叠加此前的 Gemini 插件，两者源码与生产残留均已清空。最新发布/就绪版本（2026-10-25 批）：
  - `zcode-provider`: **0.3.1**（0.3.1 修复慢速冷却到期日志重复刷屏；0.3.0 慢速冷却移植 + 非流式 TTFT 采集补齐）
  - `qoderwork-provider`: **0.12.1**（0.12.1 修复到期日志刷屏；0.12.0 慢速冷却移植 + per-attempt TTFT 隔离）
  - `traework-provider`: **0.5.1**（0.5.1 修复到期日志刷屏；0.5.0 慢速冷却移植；0.4.0 失败禁用改 sidecar 三层分离 + 模型级故障豁免 + 同步 workbuddy 七项能力）
  - `workbuddy-provider`: **0.21.2**（0.21.2 修复到期日志刷屏；0.21.1 慢速冷却补判定日志；0.21.0 慢速账号 TTFT 拥堵自动冷却）
  - `cursor-provider`: **0.1.6**；`qoder-ai-provider`: **0.1.31**；`coze-provider`: **0.6.2**；`qwenwork-provider`: **0.2.9**；`copilot-provider`: **0.1.11**；`workbuddy-ai-provider`: **0.1.30**；`workbuddy-token-usage`: **0.2.8**
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-10-25 (GMT+8)

## 活动会话进展摘要

- 当前会话（2026-10-31 凌晨）：**workbuddy 高峰期拥堵识别与失败分类修正（0.21.8，已提交推送，CI 发布中）**：
  - **用户诉求**：「workbuddy客户端有一个模型拥堵的状态, 我们插件现在时看不到的…高峰期拥堵几乎导致无法使用? 分析一下」，随后确认「现在的修复支持提交发布生产了吗?」。
  - **用户三点裁定**：① 参考 `https://github.com/linguo2625469/workbuddy2api-panel` 的处理方式；② 允许生产只读实测但拥堵发生在北京 15–17 点（本轮不在窗口，未取得实证）；③ 只做 `workbuddy-provider`。
  - **结论：不是「看不到状态」，而是拥堵被系统性误判成账号故障，形成雪崩**。四条缺陷：D1 SSE 错误帧只认 `{"error":...}`，顶层业务码帧 `{"code":6004}` 被当正常正文透传；D2 5xx 不在同请求内换号；D3 一次失败即写盘禁用（无门限）；D4 探活标记缺失时永久跳过（2026-10-29 事故机制）。放大链：拥堵 → 按账号级记账 → 一次即禁用 → 池子缩小 + 会话亲和打散 → 压力转移 → 更多拥堵。
  - **修复（10 文件 +679/-51，全部在 `workbuddy/`）**：新增 `failureScopeUpstreamBusy`（6021/`queue.full`：不冷却、不禁用、不换号，只透传 `upstreamBusyHint`）与 `failureScopeModelRateLimit`（6004：只隔离账号×模型，永不禁用账号）；`classifyUpstreamFailure` 判定顺序把两者放在通用限流与 5xx **之前**；`sseErrorFrame` 补顶层业务码帧分支（`topLevelCodeText` 处理 float64 去 `.0`）；5xx 纳入同请求换号（`shouldRotateOnStatus`）；写盘禁用加 `failover_disable_min_count`（默认 3，填 1 恢复旧行为），**门限只作用于瞬时类失败**（`isTransientUpstreamFailure` 判确定性的 401/403/404/405、402、11128 直接禁用）；探活无模型时回落 `failoverStateSnapshotModel` → `deepseek-v4.1-flash`。
  - **实施中的自我修正**：初版门限无差别作用于所有失败，打破 5 个既有测试；审查结论是**设计缺陷而非测试过时**（确定性故障等三次只会让用户多失败两次），故拆出 `recordFailureScopedWithCause` 透传 status/body。另 `TestRunFailoverProbeWithoutFailedModelSkips` 原本断言的正是事故根因机制，已改写为兜底语义并补失败侧用例——该测试在修复前是在固化缺陷。
  - **验证证据**：`python scripts/cgo-shim-build.py workbuddy` build/vet/test 全绿（33.978s / 升版后复跑 34.094s）；**6 项变异测试全部 KILLED**（顶层码帧分支、6021 分支、免罚、门限、门限越界作用于确定性失败、探活兜底）；gofmt 合规（`usage_config.go` 的 `retry_on_4xx` 缩进为既有问题，非本轮引入，按手术式改动原则不动）。
  - **发布链进度**：commit `611e340` → push `7d39588..611e340` → CI dispatch run `41120779119`（`plugin=workbuddy-provider version=0.21.8`）；workbuddy 单测 job 已 success，6 平台构建与 Release 排队中。**注意**：本机从 Git Bash 直接走 `https://...` + `git-askpass.sh` 会失败（该脚本已不存在，现存 `~/.github/ghcred.sh` 也不被接受）；正确通道是远端已配的 SSH（`ssh://git@ssh.github.com:443`），直接 `git push origin main`。`gh` 未安装，CI 触发与状态查询一律走 GitHub API + `~/.github/token`。
  - **边界（如实记录）**：① 拥堵窗口（北京 15–17 点）生产实证未取得，`6004`/`6021` 的真实报文形态依据来自参考仓库 2026-10-15 实案注释，非我方日志；② 下一拥堵窗口需执行 `docker logs cli-proxy-api | grep -E "6004|6021|queue.full"` 补齐；③ 未做面板拥堵状态可视化（用户原始诉求是「高峰期不可用」，本轮只修处置链路）。
  - **产物**：`doc/2-需求/2026-10-31_040033_workbuddy高峰期拥堵识别与失败分类修正.md`、`doc/3-实施/2026-10-31_040033_workbuddy高峰期拥堵识别与失败分类修正_实施总览.md`。

- 会话（2026-10-29 深夜）：**deepseek-v4.1-flash 16 点后变慢根因诊断（只读排查，未改代码、未发布）**：
  - **用户诉求**：「下午 16 点左右大量会话请求进来后，deepseek-v4.1-flash 推理很慢，分析一下为什么」。
  - **结论：双因叠加，非上游故障、非主机资源、非 CPA 并发**。
  - **主因（变慢起点，早于 16 点）**：主力快号 `1d7eca06`（14:30-15:48 承载该模型 58%-100% 流量，TTFT 中位 1825ms 全池最快）在 **15:48:53** 一条 429（latency 仅 261ms）后触发 `failover disabled account via cpa api`，`failed_count=1`，**至次日 00:01 仍 `disabled=true`、8 小时零探活**。原因：该账号文件顶层无 `failover_disable`/`failover_model` 标记，探活走 `failover probe skipped: no failed model recorded` 分支永久跳过（`workbuddy/failover_disable.go:700-706`）。
  - **次因（二次恶化，16:42 起）**：**16:42:28** 来自 127.0.0.1 的 `PATCH /v8/management/config`（215ms，变更内容全是 `0 -> 0` 空操作）触发 `triggering client reload` → 重建 101 clients → **清空 session-affinity**。亲和命中率：13-15 点 100% → 16 点 77% → **17/18/19/20 点 0%** → 21 点 56% → 22 点 100%，与 TTFT 曲线完全同步。
  - **量化**：账号从 4 个摊到 34 个，同账号请求间隔 4s → 91-305s；gap<5s 时 TTFT 2441ms/66814 tok/s，gap>300s 时 3315ms/54455；9 个重载前后共同账号池化 2408ms → 2971ms（+23%）；反向验证 21 点后亲和恢复，32 个共同账号 3000ms → 2657ms、吞吐 60973 → 75417。
  - **放大因素（非主因）**：输入中位 93k → 198k，回归折算仅解释 1.10x，实测 1.25-1.28x，差额由冷号 prefill 承担。
  - **已排除**：主机资源（load 1.19-1.55 / CPA 0.92% CPU 453MiB / 无 OOM）、CPA 并发（峰值 13 在飞）、网络与固定开销（小输入 TTFT 全天不变）、上游整体拥堵（对照组 kimi-k3.1-pro 16:42 后 TTFT 反而 11903ms → 8633ms）、上游限流（全天失败率 0.0%，仅那一条 429）、慢速冷却误伤（16:20:11 触发 2.9x 后 16:30:17 正常恢复）。
  - **证据边界**：feed 中 workbuddy 缓存字段恒为 0，无法直证上游 prefix cache，「冷号为何慢」是 gap→TTFT 相关性推断；16:00-16:42 已变慢但亲和仍高，该段由快号被禁 + 输入增长解释。
  - **两个待修缺陷**：A 单次 429（频率限制而非额度耗尽）即永久禁用主力号，且标记缺失致探活无权覆盖；B 空操作配置保存也触发全量 client reload 清空亲和。
  - **产物**：`doc/4-bugs/2026-10-29_160000_deepseek-v4.1-flash推理变慢主力快号禁用与会话亲和打散根因诊断.md`；12 个临时分析脚本（本地 + 远端 `/tmp`）均已清理。

- 会话（2026-10-26 晚）：**traework 0.5.3 面板改为纯读积分快照，全链路发布 + 生产热重载验收通过**：
  - **用户诉求**：traework 对齐 workbuddy「进面板纯读快照」；并追问 10 分钟定时器的作用与 workbuddy 是否保留。
  - **答问结论**：① traework 面板的 10 分钟定时器（`panel.html` `PERIODIC_REFRESH_MS`）是「页面开着就每 10 分钟打一轮 `POST /refresh` 全量上游」；② workbuddy 已无此定时器、`startBackgroundRefresh` 变成无调用点的死代码，新鲜度改由**后端 hourly `creditsSweepInterval`** 保证。
  - **关键差异（决定改动安全性）**：traework 后端本来就有 `watchdogIntervalDefault` 10 分钟全量刷新，且不依赖面板是否打开——删前端定时器**不降低任何新鲜度**，只是从「前端一轮 + 后端一轮重叠」变成「只剩后端一轮」。
  - **三处入口必须一起改**（只删前端仍会在首屏对每个无快照账号各打一次上游）：① `panel.html` `enterPanel()` 移除 `startBackgroundRefresh()`；② 删 10 分钟 `setInterval` 及其连带死代码（`startBackgroundRefresh` / `pollRefreshStatus` / `setCardRefreshState` / `finalizeCardState` / `updateRefreshProgress` + `data-refresh-state`/`ref-tag`/`ref-progress` 样式 + `#refreshProgress` 占位元素）；③ `management.go` `handleAccounts` 由「双未命中同步现查」改为 `cr, _ := cachedCredits(f.ID)` 保持未知。
  - **与 workbuddy 的形态差异（刻意不照搬）**：workbuddy 的 `startBackgroundRefresh` / `pollRefreshStatus` 函数体仍留在文件里但零调用点，属死代码；traework 按「本轮产生的未使用代码必须清理」整体删除。
  - **验证**：两段内联 `<script>` node `--check` 通过；`cgo-shim-build.py traework` build/vet/test 全绿；`grep` 复核 `api("/refresh"` 与 `PERIODIC_REFRESH_MS` 零命中、`accountCredits(` 仅剩手动刷新/watchdog/签到/生命周期链路。
  - **发布链**：`7073219`(feat) → push → 首次 dispatch `40341476755` 因 `traework-provider darwin/arm64` 作业被 cancelled 导致 Release skipped（17 成功 / 1 取消）；`rerun-failed-jobs` **不会重跑之前 skipped 的 Release**，故取消重跑并整体重发 `40344671678` → success → 6/6 assets sha256 OK → `a842f2a`(registry) → 远端 registry 0.5.3、6/6 资产 200。
  - **生产部署（热重载零重启，容器 Up 3 days 未重启）**：install 返回 `restart_required:false`；日志 `plugin hot reloaded active_version=0.5.3 retired_version=0.5.2`；落盘 `.so` sha256 `f11cfb26…` 与本地 zip **逐字节一致**。
  - **行为验收**：`/accounts` 200、16 账号与部署前一致；连续 3 次 `/accounts` 全 200 且**零上游 credits/entitlement 调用痕迹**（核心改动生效证据）；快照 `traework-credits.json` mtime `23:06:33` 对应 0.5.3 加载后的 watchdog 首轮刷新，说明后端刷新链仍工作。
  - **边界（如实记录）**：① 版本一致性用例 `TestVersionMatchesRegistration` 硬编码版本号，升版必须同步改 `test/traework/config_fields_test.go`，否则测试红（本轮首跑即因此失败，非代码缺陷）；② macOS runner 偶发 cancelled 会让 Release 跳过，只能整体重 dispatch；③ 面板在快照缺失（新装/首轮未跑）时显示「加载中…」且不再自动回填，需等 watchdog 一轮（≤10 分钟）。

- 当前会话（2026-10-25）：**慢速账号冷却跨五插件移植完成并批量发生产**：
  - **用户裁定**：traework / qoderwork / workbuddy-ai / qoder-ai / zcode 五个插件都做；其中 workbuddy-ai（2 号）与 qoder-ai（0 号）**只落文档计划、暂不实现**——池子 <10 时 P25 基线不成立，会制造噪音；zcode 选「完整改造与其他插件对齐」；发布方式选「全部改完后批量发布」。
  - **两个前置硬前提（来自实施前的盘点）**：① **per-attempt TTFT 计时隔离**是阻断项——五个插件的 `ttftNS(started)` 都用请求级时间戳跨 attempt 复用，「A 失败 10s → B 首帧 2s」会被记成 12s，把 A 的失败耗时算到健康号头上，直接吃这个样本比不加机制更糟；② **账号池 ≥10**，否则 P25 退化成较小值。故顺序固定为「先补计时基准 + 用例锁定，再接状态机」。
  - **各插件接入点差异（不能整体复制文件）**：traework 四处（前期预检 + 异步流 + 同步 collect + `pickNextAuthForModel`）；qoderwork 用 `pickNextAuth` 变参 `model ...string` 避开 10 处调用点改动；zcode 架构差异最大，单点 `accountRoutable` 同时覆盖三条路径，另补非流式 TTFT 采集（`executeOnceTimed` + `firstByteReader`，空读不算首字节）。
  - **实现与验证**：三插件各补 per-attempt 用例（traework 4 / qoderwork 3 / zcode 3）；`slow_pipeline_test.go` 覆盖「打点去判定」链式语义——该变异首轮**存活**，因为异步成功路径的 `noteSlowObservation` 挂在宿主 RPC 链上无法单测，补链式用例后才 KILLED（覆盖边界已如实记录）。
  - **发布链**：`13fb652`(feat, 48 文件) → 三 CI success → 6/6×3 assets sha256 OK → `3abc5eb`(registry) → 远端 6/6×3 200 → 三插件热重载部署，sha256 逐字节一致。
  - **本轮发现的真实缺陷（生产实证）**：`recordSlowSample` 检测到冷却到期后只打日志、**不清零 `cooldownUntil`**，导致「已过期」在后续每条成功请求上持续成立——生产同一账号 **1 分钟内刷 8 条** `slow cooldown expired`。修复为判定过期后立即清零，并补 `TestSlowCooldownExpiryLogsOnce`；「去掉清零」变异在四插件全部 KILLED。**教训**：我先前在该处注释里写「每条冷却只会打一次」是错的，注释不能替代用例。
  - **第二批发布（携带上述修复）**：三插件已发版本产物构建于修复之前，同号重发会与既有 tag 冲突，故升 patch——traework 0.5.1 / qoderwork 0.12.1 / zcode 0.3.1，与 workbuddy 0.21.2 一并批量发布。`494bf80`(fix) + `a820790`(版本升级) → 四 CI success → 6/6×4 assets sha256 OK → `06f10bd`(registry) → 远端 registry 四插件版本正确、零旧版残留、linux_amd64 资产 4/4 返回 200。
  - **生产部署（热重载零重启，容器 Up 2 days 未重启）**：四次 install 均 `restart_required:false`；四条 `hot reloaded`（`0.21.2←0.21.1` / `0.5.1←0.5.0` / `0.12.1←0.12.0` / `0.3.1←0.3.0`）；四个落盘 `.so` sha256 与本地 zip **逐字节一致**（`b110fb36…` / `bec2a75c…` / `4a6230ff…` / `aff4ef5b…`）。
  - **生产验收**：四插件 `enabled=true`、`/accounts` 全部 200；账号规模 37 / 16 / 18 / 1 与移植前盘点一致（部署未影响账号数据）；无 panic / 加载失败日志；近 4 分钟无 `slow cooldown` 日志——池内延迟均匀时本就不该判慢，符合预期。
  - **边界（如实记录）**：① 慢速判定的**正向触发**未在本轮生产窗口内复现（需真实慢号），四插件的判定/放开/到期三类日志目前只有单测 + 变异覆盖，workbuddy 侧历史上有过真实触发记录；② workbuddy-ai 与 qoder-ai 仅有文档计划，代码未动；③ zcode 生产池仅 1 个账号，P25 基线在该池不成立，机制实质性不会生效——待池子扩充后再观察。

- 当前会话（2026-10-26）：**慢速冷却加账号池准入闸门（>20）+ 三插件补齐机制，七插件批量发生产**：
  - **用户诉求**：「慢速冷却改成只有账号大于 20（启用+停用）才触发，这样就不会因为账号不够出现问题；把所有慢速冷却机制都加上这个逻辑，把其他插件也补上慢速冷却机制并账号大于 20 才触发」。
  - **为什么必须要闸门**：判据基准是各账号中位数的**下四分位（P25）**。池子太小时该统计量退化成「取较小值」——两个账号里任一次抖动就会让其中一个被判慢、两号互相顶替；池内单个毛刺账号即可拉低基准。2026-10-25 生产实证：激进参数下池内 TTFT 仅 1.26 倍差却持续判慢、冷却主体在两号间反复跳变。
  - **三个实现约束（关键）**：① 闸门放在**打点之前**（`noteSlowObservation` 入口）而非只在判定里——否则账号不足时样本照记，池子长大后拿陈旧样本立刻误判一轮；② 账号枚举走宿主 RPC（跨进程），**绝不能放每请求热路径**——改为独立 ticker 每 10 分钟刷新内存缓存，判定入口只读变量；③ 缓存零值（从未探测成功）**一律视为不达标**（保守关闭），探测失败保留上一次已知值。
  - **计数含停用账号**：停用多是临时的（额度耗尽、探活中），池子真实规模应按归属算；只数可路由号会让一次批量临时禁用把机制忽开忽关。
  - **实现范围**：workbuddy / traework / qoderwork / zcode 加闸门（zcode 新增 `countHostAuthFiles()` 只数文件名、不逐号拉凭据，避免一次刷新变 N 次 RPC）；workbuddy-ai / qoder-ai / qwenwork 补 per-attempt TTFT 隔离 + 非流式 TTFT 采集 + 完整状态机 + 路由过滤 + 7 配置键。
  - **修掉的真实缺陷（非流式 TTFT 恒为 0）**：三者非流式 `publishUsage(..., 0)` 恒传 0，慢速冷却按样本门槛判定，纯非流式流量永远达不到门槛——机制对它们**等同不存在**。改为非流式函数返回 TTFT、聚合函数加 `firstByteAt *time.Time` 出参，打点在**首个可解析分片**（不是读完正文）。
  - **用户裁定（coze/copilot/cursor）**：「三者都不做」。coze 无 TTFT 采集也无 `accountRoutable`；copilot 无 TTFT 采集；cursor 架构不同且内部 TTFT 未上抛。补齐前置条件的工作量远大于移植状态机本身，且生产均未启用或无池子。
  - **验证**：七插件 cgo-shim build+vet+test 全绿；变异「闸门恒放行」「只拦判定不拦打点」「> 改 >=」「per-attempt 退回请求级基准」全部 KILLED。
  - **发布链**：`ca3b4f5`(feat, 63 文件) → 七 CI（qoder-ai darwin/amd64 首轮因 `proxy.golang.org` DNS 超时失败，重跑 success）→ 7×6/6 assets sha256 OK → `d7276a5`(registry) → 远端 registry 七插件版本正确、linux_amd64 资产 7/7 返回 200。
  - **生产部署（热重载零重启）**：七次 install 均 `restart_required:false`；六条 `hot reloaded` + qwenwork 首次 `loaded/registered`（此前未启用，无 retired 版本）；七个落盘 `.so` sha256 与本地 zip **逐字节一致**。
  - **生产验收**：七插件 `/accounts` 全 200，账号数 37/16/18/1/2/0/0 与部署前一致；**闸门实证**——`[workbuddy] slow pool size updated: prev=0 now=37`（放行）、`[qoderwork] … now=18`、`[traework] … now=16`（休眠），正是设计意图：机制在位、按池子规模自动上下线。
  - **边界**：慢速判定的正向触发仍需真实慢号；qoder-ai / qwenwork 池为空，机制虽在但不会生效。

- 更早会话（2026-10-25）：**workbuddy 慢速账号 TTFT 拥堵自动冷却从设计到生产验收（0.21.0 → 0.21.1）**：
  - **需求起点**：生产用量面板显示同一模型 `deepseek-v4.1-flash` 的 TTFT 在 13s~21s 分层，慢的一批集中在固定账号；这类号每次仍返回 200，既有失败冷却完全抓不到。
  - **关键判读（推翻直觉方案）**：同批请求 input 普遍 33 万 tokens，prefill 本身十几秒，**绝对阈值会把整池健康号一次禁光**，判据必须是同模型横向相对对比。
  - **基准口径两轮修正**：初版「中位数的中位数」变异**存活**——慢号占多数时基准被抬到慢号水平、机制静默失效；改为**下四分位 P25**，并用 `slow_min_ttft_ms` 绝对下限挡住「单个超快号把基准压到地板」。
  - **实现**：`slow_account.go` 纯内存状态机（账号×模型粒度、滑动窗口、P25 相对基线、2.5 倍阈值、10 分钟到期自动恢复、防抽干放开一半），与失败冷却/轮换冷却三分独立；四处路由接入点；6 个 `slow_*` 配置键 + 面板 `slow_cooling_models`。
  - **生产误伤与纠正**：事故排查期间为测试「能否触发」临时 PUT 的激进参数（1.2 倍 / 2 样本 / 500ms 下限 / 60 分钟窗）**留在了生产**，导致池内 TTFT 仅 1.26 倍差却持续判慢、冷却主体在两号间反复跳变。全量回写默认值后稳定锁定单号、不再新增误判。**教训**：临时调参必须有回写清单。
  - **可观测性补齐（0.21.1）**：四类日志——判定取证（中位数/基线/倍数/阈值/样本数）、防抽干放开清单、到期恢复、配置变更生效值；全部移到 `slowMu` 解锁后（`log.Printf` 做 I/O，持锁会阻塞路由判定）；`slowCoolingDown` 保持纯读，到期检测放 `recordSlowSample`（新用例 `TestSlowCoolingDownStaysReadOnly` 锁定）。
  - **另一处真实缺陷**：`recordSlowSample` 原先先判「冷却中」再判到期，导致过期后第一条样本被当作仍在冷却提前返回，恢复事件永不记录——已修正判定顺序。
  - **发布与验收**：0.21.0 `e6e9447` + 0.21.1 `c93d7bd` 两次发布均热重载零重启、sha256 逐字节一致；生产 `slow config applied` 日志实证可用。
  - **边界**：只做纯内存冷却，不做写盘禁用——慢是性能问题不是故障，写盘禁用要求探活恢复，对慢号过重。

- 更早会话（2026-10-25）：**traework 0.4.0 能力同步与两处真实缺陷修复**：
  - **两个真实缺陷**：① 写盘禁用可能名义生效——宿主 fsnotify 不监听 mysqlstore spool（qoderwork 0.11.0 生产实证 48h 零事件而 mtime 持续变化），改插件自有 sidecar `data/traework-failover.json` 作路由判据权威源；② 模型级故障被记成账号级——HTTP 200 的 SSE 错误帧被无条件改写成 403，`All backends failed` 类池级故障走进账号级判据导致整池自禁（与 qoderwork 0.11.0 事故同机理），在记账漏斗与换号判据两处同时豁免。
  - **同步七项**：故障 sidecar 跨重启存活、persist 抹标记后补回、外部启用对账、轮换冷却、积分快照持久化、双阈值分级、面板徽标；11 个配置键登记。
  - **关键裁定（放宽而非收紧）**：`low_credit_disable_below` 默认 **0（关闭）**，刻意不同于 workbuddy 的 100——照搬会让现网 remain 20–99 的账号首轮被批量禁用。
  - **验证与发布**：10 条变异全部检出（含「调度层过滤行没接上」的经典存活形态）；`37f1d17` → CI → `4e02b36`(registry) → 热重载零重启；生产 16 账号 `normal:14 / preserve:2 / disabled:0`，禁用线默认 0 的红线在生产坐实。
  - **未闭环**：轮换需 1 小时真实长流量才能观察，本轮未跑满；模型级豁免的正文形态近 5 小时生产日志无样本。

- 更早会话（2026-10-25）：**coze 低额自动禁用未同步宿主状态修复（0.6.2）**：宿主 `pluginAuthDataToCoreAuth` **只用 `AuthData` 顶层 `Disabled` 推导 `Status`**，coze 的 `authDataForLogin` 只补了 Metadata、漏顶层字段 → 插件写盘禁用对宿主不可见（全项目 7 插件只有 coze 漏这一行）。修复后宿主 `auth-files` 从 14 全 active 变 10 active / 4 disabled。真实流量无影响（`publishCreditPool` 按 `!acc.Disabled` 排除低额号）。

- 更早会话（2026-10-25）：**生产 traework-provider 17 个账号误删后从 MySQL binlog 完整恢复**：binlog 定位到 17 条 DELETE（10-20/10-21/10-22 三批）；恢复前先备份 binlog 本体（921MB）防 24h 轮转；容器无 `mysqlbinlog` 改用纯 Python 直解；17 个凭据刷新 16 成功（1 个删除前即 Session expired）；按「auth_store → spool」写回 17/17，启用后 `/accounts` 17 条。完整方法论已沉淀为项目 skill `project-cpa-workbuddy-plugin-account-data-recovery-rules`。

- 更早会话（2026-10-25）：**zcode 0.2.0 功能同步**（只做 P0 / 引入写盘禁用并移除内存冷却 / 额度耗尽落禁用 + 重置后探活恢复）；三处流式缺陷修复；E2E 抓到 `writeAuthFileDirect` 按结构体整篇重建会抹掉宿主字段、且探活成功后把刚写的 `disabled:false` 覆盖回内存旧值 → 改为「读旧文档 → 覆盖已知键 → 写回」。

- 更早会话（2026-10-25）：**宿主 persist 抹除缺口同步四插件**（workbuddy-ai / qoderwork / qoder-ai / coze，三种形态不能照搬）：workbuddy-ai 与 qoderwork 同构补三键；qoder-ai 用 `daily_cooling_until` 且只补仍在冷却期内的号；coze 正解是让 `auth.parse` 的 Metadata 带上 `disabled` + 三个低额归属键形成闭环。五插件 cgo-shim 全绿、变异全部 KILLED、热重载零重启。

- 更早会话（2026-10-25）：**workbuddy-provider 0.20.2 失败禁用标记保底补回**：宿主每请求结束用内存 `Auth.Metadata` 重建 auth 文件，`failover_disable` 三键不在白名单 → 标记丢失导致 10 分钟探活挑不到候选。修复为 `persistCounterDelta` 落盘前补齐三键（条件严格限定「本进程写过禁用」+「盘上确实 disabled」）；新增 `failover_unconfirmed` 面板徽标暴露分叉。

- 更早会话（2026-10-25）：**qoderwork 0.11.0 失败禁用链路三层分离重构**（故障分类 → 插件自有 sidecar → 路由排除，熔断器兜住分类错误）。管理密钥已落 `.ssh/servers.json` 元信息（用户授权明文持久化，取值全部经文件读取、未在任何回复中回显）。排队帧零误判实证：18 号被逐个打到 `403:10605 isQueued` 后 `disabled=0 / cooling=0 / failover_disabled=0`，sidecar 未生成。

- 更早会话（2026-10-25，已压缩）：**qoderwork 失败禁用两层根因诊断**（已被 0.11.0 重构取代，结论仍有效）：① 宿主以 `auth_store` 为真相源、spool 为单向投影，插件搬标记唯一通道是 `AuthData.Metadata`；② 真根因是 watcher 未监听——改 note / `os.Rename` / 删除重建三项实测零事件。该结论直接促成 0.11.0 三层分离与 traework 0.4.0 的 sidecar 方案。

- 更早会话（2026-10-25，已压缩）：**qoder-ai 0.1.30 模型清单修复的上游取证**：真实国际版账号直连证实响应按 11 场景切分（chat 仅 12 个 key、并集 21 个）；国际版把档位嵌进场景 id 形成 8 个伪模型必须按前缀排除；真正漏掉的只有 `performance`。

- 更早会话（2026-10-25，已压缩）：**qoderwork 生产 Kimi-K3.1-Pro 测试失败排查：上游后端不可用，非插件缺陷**。Qoder 上游返回 HTTP 200 + `All backends failed`（Kimi 后端池全挂），同期 `serviceAvailable:false` / `subCode:RESOURCE_LIMIT`；四项排除均正常。**该案例正是 traework 0.4.0 模型级豁免的动机来源之一**。

- 更早会话（2026-10-24 ~ 2026-10-25，已压缩）：**qoderwork 0.10.1 / qoder-ai 0.1.30 模型清单漏模型修复**（多场景并集 + 上游 `display_name` 权威 + 运行时回译映射）；**coze 0.5.5/0.5.6 每日登录积分自动领取 + 历史账号补注册宿主**（唯一触发器 `GET .../credit/balance/check`）；**workbuddy 0.20.0 连续使用轮换**（满 60 分钟让位、冷却 60 分钟、中断 10 分钟重计时、全冷放开一半）。完整细节见 git log 与 PROJECT_HISTORY.md。

- 更早会话（2026-10-18 ~ 2026-10-23，已按 51200 字节上限压缩，完整记录见 git log 与 PROJECT_HISTORY.md）：
  - **workbuddy 0.17.1 / 0.18.0**：低额红线 20→400（399/400 分界用例锁死严格 `<`）；下线 Global 耗尽自动删号改为 CN/Global 一视同仁写盘禁用；`min_routable_credits` 补登记进 `ConfigFields`。
  - **workbuddy-ai 0.1.22 / zcode 0.1.35 版本就绪未启用**：补齐 `5ab431d`（四会话追踪头修国际版 400/11128）与 `00a7e88`（schema 6 防 OAuth URL `&` 转义）。踩坑：`plugin-store install` 必然写 `plugins_enabled:true`，与「只发版不启用」相悖，须再 `PUT config {"enabled":false}`。
  - **下线 doubao / notion 两插件（13 → 11）**：源码、测试镜像、图标、专属文档、registry、CI 矩阵、生产磁盘残留全部清空。
  - **coze 0.4.6 → 0.5.4 系列**：64KB content 超限返回 413；积分路由红线走执行入口闸门（非 scheduler）；0.5.3 生图统一 `awaitImages` + 4 分钟上限；0.5.4 整池确定 0 分快速失败 400。
  - **定时任务并发治理**：6 插件 4h 槽位改 `next*FireTime` + 5m 可注入抖动；traework 签到重试收敛并发 4；六个 watchdog 首轮加抖动。
  - **账号故障冷却基数 30s → 60s**（线性递增、上限 10 分钟、cap 10 档）。
  - **Qoder 系模型发现风暴与 OOM 修复**：GET 真携带 `qoderEncode("{}")` 修复恒 403；补负缓存 5min；`pickNextAuth` 50ms 退避 + 错误体截断；缓存按账号分片 + 额度优先；对外 ID 改官方展示名 + 执行链回译。
  - **其余**：qwenwork 立项；发布矩阵下线 freebsd-amd64（7 → 6 平台）；coze 模型动态发现 + 面板重做 + WS 真流式；五插件签到统一每 4 小时；zcode 中国区与额度领取；copilot 立项；Kiro 裁定不立项；max_tokens 官方规格自适应 + 32768 底线；Qoder 110 日上限 24h 持久化冷却；排队帧不判账号故障；删除 Gemini 插件；Token 用量面板移除 4 图表。

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
