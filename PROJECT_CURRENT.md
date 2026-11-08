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

- 状态：活跃维护中。11 大插件全景（10 Provider + 1 用量统计）。**2026-10-22 下线 doubao-provider 与 notion-provider**（用户裁定移除），叠加此前的 Gemini 插件，两者源码与生产残留均已清空。
- **生产主机（2026-11-07 起）= 45.207.222.65（`cpa-server`，端口 18998），254（`cpa-server-new`）已弃用**：用户明示「254 已经放弃，改回 65 为主服务器」，全部域名已解析到 65，254 只剩 mysql+nginx（CPA 已停）。部署/验收一律打 65，管理密钥用插件配置 `cpa_management_key`，账号在 `auth-dir=/CLIProxyAPI/mysqlstore/mysqlstore/auths`（62 个）。
- **2026-11-07 01:27 已在 65 完成全量对齐与启用**：11 个插件全部 `configured/registered/enabled=true` —— workbuddy 0.21.9（20 号）/ coze 0.7.4（23 号）/ qoderwork 0.12.6（2 号）/ workbuddy-ai 0.1.35（6 号）/ token-usage 0.2.8 / traework 0.5.7 / qoder-ai 0.1.35 / zcode 0.3.5 / copilot 0.1.12 / qwenwork 0.2.13 / cursor 0.1.7（首次安装）。install 前 5 个显示 `registered=false` 是「该版本 .so 从未加载」（config 里 enabled 本为 true），install 后日志为 `plugin loaded + plugin registered`，非 hot reload 形态。
- **2026-11-07 19:40 当前生产版本（max_tokens 最小干预批次）**：workbuddy-ai **0.1.37** / qoder-ai **0.1.37** / qoderwork **0.13.5** / traework **0.5.13** / zcode **0.3.7** / qwenwork **0.2.15** —— 上述插件 `configured/registered/enabled/effective_enabled` 全 true，`.so` sha256 与 release zip 逐字节一致。**管理 API 端口是 8317（不是 18998，18998 是 sshd）**。
- **2026-11-07 20:33 workbuddy 已升级 0.21.12 → 0.21.13**（失败禁用与手动启用联动，`hot reloaded active_version=0.21.13 retired 0.21.12`，落盘 sha256 `574f6921…` 与本地 zip 一致）。
- **2026-11-07 21:25 三插件同步升级**：workbuddy-ai **0.1.38** / traework **0.5.14** / qoderwork **0.13.7**（同型缺陷整改），三者 `hot reloaded` 且 `.so` sha256 与 release zip 一致。
- **2026-11-07 22:17 workbuddy 升级 0.21.13 → 0.21.14**（P0 分阶段超时 + P1 HostCallbackID 透传，见下条会话摘要）：`hot reloaded active_version=0.21.14 retired_version=0.21.13`，落盘 sha256 `8433a54c…` 与 release zip 逐字节一致；只有 5 平台资产（缺 `darwin_arm64`）。
- **2026-11-08 14:51 traework 升级 0.5.18 → 0.5.19**（下行 usage 帧缺失修复，`hot reloaded active_version=0.5.19 retired 0.5.18`，落盘 sha256 `27d74d19…` 与 release zip 逐字节一致；容器未重启）。同刻 workbuddy 0.21.15 / qoderwork 0.13.12 由并行会话上线。
- 2026-10-25 历史发布批（已过期，仅存档）：zcode 0.3.1 / qoderwork 0.12.1 / traework 0.5.1 / workbuddy 0.21.2 / cursor 0.1.6 / qoder-ai 0.1.31 / coze 0.6.2 / qwenwork 0.2.9 / copilot 0.1.11 / workbuddy-ai 0.1.30 / token-usage 0.2.8（细节见 git log 与 CHANGELOG）。
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-11-07 (GMT+8)

## 活动会话进展摘要


- 会话（2026-11-08 21:15-21:35，接续上条）：**traework 长尾根因收敛（第 5 轮）+ 0.5.25 前向兼容 prompt cache**：
  - **按账号切片**：traework 全部 16 个账号的 TTFT p50 都在 3604~10622ms、slow10% 8%~52%，**没有任何一个账号是快的** —— 排除「个别账号坏」假设，确认是全池共性。
  - **prompt cache 检查**：72h 内三个 provider 的 `cache_read_tokens` / `cache_creation_tokens` 命中率**全部 0%**，而 traework input 中位 **162,340 tokens**。
  - **开源对照**：`connectedgraph/trae2api-web` 的全部 `.go` 文件**不含任何 cache 关键字** → 社区反代同样不处理，佐证 Trae 上游目前不返回缓存信息，延迟无法靠缓存命中改善。
  - **长尾根因已排除项累计 5 类**：① 插件转发链路（同插件 deepseek 0% 慢）；② payload 冗余（无固定 system 模板）；③ 流式读取节流（`time.After` 仅超时守卫）；④ 账号级并发排队（在途数 0/1/2-3/4+ 的慢请求占比 16.5%/34.3%/13.4%/24.4%，无单调关系）；⑤ prompt cache（上游不提供，社区亦不处理）。
  - **修复（0.5.25，前向兼容）**：`usageDetailFromTraeMap` 增补 OpenAI 标准形态 `prompt_tokens_details.cached_tokens` 解析（此前只认 Anthropic 的 `cache_read_input_tokens`），Anthropic 形态仍优先。变异验证 KILLED。
  - **文档精简**：`PROJECT_CURRENT.md` 从 50227 字节精简至 **24960 字节**（保留最近 9 条会话），解除 51200 上限压力。

- 会话（2026-11-08 20:55-21:15，接续上条）：**traework 0.5.24 — 第四类未识别业务码 1005 + 信封状态码缺失**：
  - **发布 0.5.23**：含并行会话的浏览器登录桥接页 + 本轮超时闸门 30s→120s；`hot reloaded active 0.5.23 retired 0.5.22`，sha256 `f5d5c547…` 与 zip 一致。
  - **实例守卫生产实证**：20:41:29 日志 `[traework] failover reconcile loop stopped: superseded by newer instance` 出现 —— 0.5.22 旧实例在 0.5.23 抢占所有权后自行退出。
  - **缺陷七（1005 套餐限制）**：`kimi-k3.1-pro` 全部 25 个样本返回 `{"code":1005,"message":"","extra":"{\"plan\":5}"}`，无判据 + traework `envelopeError` **同样缺 `http_status` 字段**（此前只修了 workbuddy），单请求换遍 **12 个账号**（8e169c66 / 9887bf54）。
  - **修复（0.5.24）**：新增 `isPlanRestrictedFailure` 归入 `isModelLevelFailure`；`envelopeError` 补 `HTTPStatus`，新增 `envelopeHTTPStatusForError`（请求形状/模型级 → 400）。变异两轮全部 KILLED。生产 `hot reloaded active 0.5.24`，sha256 `d655ef1b…` 一致。
  - **首字延迟深挖（同模型公平对比）**：72h 内同模型 `deepseek-v4.1-flash`，workbuddy n=12734 ttft50=2820/p90=4958/慢10%=0.3%；traework n=779 ttft50=3490/**p90=26137**/**慢10%=17.6%**。控制 input 100k-200k：workbuddy p50 2524/p90 3375，traework p50 3234/**p90 25720**。⇒ 平均只慢 28%，**长尾慢 7.6 倍**。
  - **并发排队假设被否定**：用「请求开始时刻的同账号在途数」重算（排除反向因果）——在途 0/1/2-3/4+ 的慢请求占比为 16.5%/34.3%/13.4%/24.4%，无单调关系。
  - **部署后终态（21:03 起）**：上游失败 **0**、1005 **0**、11128 扇出 **0**、112 **0**、param invalid **0**；探活失败由 12 次/分钟降至 **2 次**（实例守卫逐次热重载持续收敛）。
  - **文档**：诊断文档扩至 10 节，新增第四类业务码对照表（10605 / 112 / 11128 / 1005 的载体与处置）。
- 会话（2026-11-08 15:29-21:00）：**coze 积分红线默认 200 → 10（0.7.5）+ 修掉热重载僵尸实例写盘缺陷（0.7.6），已发布生产**：
  - 诉求：用户报「coze 现在的积分每天是 60 签到积分」，要求「低于 10 才禁用」。
  - **第一层根因**：`coze/policy.go` 的 `defaultMinRoutableCredits` 仍是 200，而扣子签到额度已降为 **60 分**（生产实证 `remain=60` / `daily_credit.amount=60`）——红线远高于单日额度，刚领完签到的号被整批误禁再靠次日签到解禁。
  - **第二层根因（真缺陷）**：**宿主热重载只把旧实例标记 retired，不调它的 `cliproxyPluginShutdown`** → 0.7.4 僵尸巡检在 19:02:52 用旧红线 **200** 把 0.7.5 刚解禁的 23 个号整批重禁，引发 19:02:55-19:02:58 连续 4 个 503。判定证据 = 写盘内容 `low_credit_disable_below=200`（只存在于旧二进制）。
  - **0.7.5 改动**：默认值 200→10；配置描述/README/注释同步；面板签到徽标与按钮去掉 1500 硬编码，改读服务端 `daily_credit.amount`；用例边界 199/200/201 → 9/10/11。
  - **0.7.6 改动**：新增 `coze/cpa_active_guard.go`（写盘前问宿主「当前活跃的 .so 是哪个」，版本号不是自己就放弃整轮写盘；宿主查不到时按活跃处理）；`credit_sweep.go` 接入 `canWrite`，退役实例跳过分级写盘/快照落盘/闸门发布，仍照常刷新积分与内存缓存。
  - **生产事故处置**：23 个账号在 auth_store 里 `disabled=true, below=200`（11-05 遗留）+ 19:02 旧实例重写盘，三方不一致。按项目 skill 处置：建还原点 → **卸载插件触发宿主 shutdown 清僵尸** → 重装 → 用 **JSON 字面量直接 UPDATE auth_store**（`CAST(X'<hex>' AS JSON)` 会二次编码成 `"base64:type15:..."` 字符串，踩坑）→ 清 7 个残留 `low_credit_disable` 标记（否则下次巡检会把人工禁用号误解禁）→ 人工禁用的 8 个一律不动。
  - **验证**：3 组变异全部 KILLED；生产三层一致（redline=10 / low_credits=0 / disabled=8 全为人工禁用 / DB below200=0）；落盘 `.so` sha256 `382a60c1…ee2397c7` 与 release zip 逐字节一致；真实入口 200 + `finish_reason=stop` + 正文「收到」；**重装同一版本模拟热重载后状态完全未变**，防线在生产实际生效。
  - **发布**：`eee6065`(0.7.5) → `452f5e7`(registry) → `f235291`(文档) → `38d8e96`(0.7.6) → `b0f2ddc`(registry)；两次均 5 平台（darwin/arm64 CI job 被取消，registry 已移除该槽位）。**容器未重启**。
  - **新增生产事实**：扣子每日签到额度 = **60 分**（历史 1500 分口径作废）。
  - 遗留：红线 10 的业务适配性需生产观察禁用频次再定；darwin/arm64 平台待补发。


- 会话（2026-11-08 20:35-20:55，接续上条）：**traework 0.5.23 发布 + 首字延迟决定性证据 + 实例守卫生产实证**：
  - **发布**：traework 0.5.23（含并行会话的浏览器登录桥接页 + 本轮的超时闸门 30s→120s）。CI run 42480619699 Release success → 资产 5/5 checksum OK → registry 0.5.23（远端 raw 已生效）→ 生产 `hot reloaded active_version=0.5.23`，落盘 sha256 `f5d5c547…` 与 release zip 逐字节一致。
  - **决定性证据（首字延迟不是插件 bug）**：同一 traework 插件、同一条宿主桥、同一组超时配置，仅模型不同 —— `deepseek-v4.1-flash` TTFT p50 **1297ms / 0.0% 慢请求**（n=23），`minimax-m3.1-flash` 4245ms / 22.6%（n=515），`Doubao-Seed-2.1-Pro` 7742ms / 27.1%（n=145），跨插件对照 workbuddy `deepseek-v4.1-flash` 1008ms / 0.0%。⇒ 转发链路无问题，慢的是特定上游模型服务；排除了「payload 冗余」「流式读取节流」「宿主桥瓶颈」三类假设。
  - **实例守卫生产实证**：20:41:29 日志 `[traework] failover reconcile loop stopped: superseded by newer instance` 出现 1 次 —— 0.5.22 旧实例在 0.5.23 抢占所有权后自行退出，机制在真实热重载下成立。
  - **部署后综合观测（20:40 起 15 分钟）**：11128 宿主扇出 **0**、112 **0**、param invalid **0**、上游失败 **0**；探活失败由 11 次/分钟降至 **5.2 次/分钟**（其余为 0.5.21 及更早的无守卫实例，彻底清理需容器重启，属红线待确认）。
  - **文档**：`doc/4-bugs/2026-11-08_210000_traework首字延迟与生成耗时偏高根因分析.md` 扩至 9 节，含阈值占比表、prefill/生成阶段拆分、同插件跨模型对照、实例守卫实证。

- 会话（2026-11-08 20:35-21:10，接续上条）：**traework 首字延迟根因分析 + 两个超时缺口（qoderwork 0.13.15 已发布 / traework 超时随 0.5.23）**：
  - **观测结论**：12h feed 中 traework `minimax-m3.1-flash` TTFT p50 5578ms / p90 26580ms / p99 55150ms，`Doubao-Seed-2.1-Pro` p50 6875ms / p90 30332ms；对照 workbuddy `deepseek-v4.1-flash` p50 2464ms / p90 3185ms。
  - **归因（三项证据）**：① 大 input prefill 是主因（大 input 归一化 29.3 vs 22.4 ms/1k tokens，仅差 31%）；② 长尾异常（100k-200k 区间 p50 3433ms 但 p90 16349ms，workbuddy 同区间 p90 仅 3240ms）；③ 慢请求（TTFT>15s，占 14.6%）input 中位 36 万 tokens、集中在 17-19 点高峰、集中在随后因配额耗尽被禁用的账号。
  - **判定：不是插件缺陷**。慢速账号机制以全池中位数为基准，全池同时变慢时基准同步抬高，未误判是正确行为。
  - **参考开源实现**：`connectedgraph/trae2api-web`（Go，50 stars）用 `ResponseHeaderTimeout: 120s`，且流式客户端**不设** `http.Client.Timeout`（注释：否则超长 SSE 会被切断），其单测显式断言 `StreamHTTP.Timeout == 0`。
  - **据此修复**：qoderwork `sharedHTTPClient` 补 10s/10s/60s + 禁 h2（0.13.15 已发布部署）；traework `sharedHTTPClient` 补 10s/10s/**120s** + 禁 h2，流式 `ResponseHeaderTimeout` 30s→**120s**（已提交 `4ec9805`，随并行会话的 0.5.23 发布）。
  - **文档**：`doc/4-bugs/2026-11-08_210000_traework首字延迟与生成耗时偏高根因分析.md`。
  - **并行会话协调**：traework 0.5.23 与 `browserlogin.go` 由并行会话推进，本轮只提交 `host_bridge.go` + `upstream_timeout_test.go`，未动 VERSION/CHANGELOG。


- 会话（2026-11-08 19:58-20:35，接续上条）：**继续观测 → 探活放大与 HTTP 超时缺口（traework 0.5.22 / qoderwork 0.13.15）**：
  - **观测**：45 分钟窗口 11128 宿主扇出 34 条**全部在 19:43 部署前**（部署后 0）；112 与 param invalid 均为 0。feed 部署后仅 traework 107 行（失败 3 条，status=0、latency 61~73s）。
  - **缺陷四（qoderwork 缺分阶段超时）**：`sharedHTTPClient` 只有 `Timeout: 120s`，缺拨号/TLS 握手/响应头三道前置闸门；workbuddy 0.21.14 已按同口径补齐。修复后 Transport 补 10s/10s/60s 并置空 `TLSNextProto` 真正禁 h2。
  - **缺陷五（traework 探活被放大 10 倍）**：探活周期 10 分钟，但单账号 90 分钟被探活 **96 次**（11 账号共 806 次失败）。根因是宿主热重载只把旧 `.so` 记入 retired、**不调 `plugin_shutdown`**，`for range ticker.C` 无退出条件，实例随热重载累积。修复：新增 `instance_guard.go`（`$TMPDIR` 所有权标记 + 每轮 tick 自检）。
  - **验证**：两插件 cgo-shim 全绿；变异验证 3 轮全部 KILLED（其中一轮因日志淹没 FAIL 行误判，改用定向 `go test -run` 复核）。生产 20:17 热重载两插件，sha256 与 release zip 逐字节一致。
  - **效果**：部署后 12 分钟探活失败 **11 次/分钟 → 0**（禁用账号的陈旧 `failover_disable` 标记已被对账清空，探活目标归零）。
  - **遗留**：① 实例守卫只对本版本之后的热重载生效（旧版本无自检代码），彻底清理需容器重启（红线，待用户确认）；② workbuddy 11128 与 traework 实例守卫的 `superseded` 日志均缺生产实证。
- 会话（2026-11-08 13:32-20:00）：**生产流量持续观测 + 三个跨凭据扇出缺陷（workbuddy 0.21.15→0.21.18 / qoderwork 0.13.12-0.13.14 / traework 0.5.20）**：
  - **观测**：24h feed 8433 行，qoderwork 失败率 **47.26%**（354/749，其中 294 条 HTTP 200 零 token）、workbuddy 2.90%、traework 6.53%。
  - **缺陷一（qoderwork 112 无权益）**：上游 upstream 200: 112: {"pricingUrl":...} 全无判据，宿主逐个换凭据。修复：isEntitlementRequiredFailure + 归入 isModelLevelFailure + 换号短路 + 信封 400。生产 112 已归零。
  - **缺陷二（workbuddy 11128 渠道拦截）**：单请求扇出 **17 个账号**、3h 内 62 请求 583 次失败。三轮定位后才触及根因：envelopeError 结构体**根本没有 http_status 字段**，0.21.15/0.21.16 加的状态码是死代码。0.21.18 补字段后生效。
  - **缺陷三（traework 模型名大小写）**：官方展示名 MiniMax-M3.1-Flash → 上游 param is invalid → 整池轮询。修复：traeCanonicalModelNames 精确回填 + param is invalid 归入请求形状。修复后展示名与内部代号均返回 200。
  - **方法论沉淀**：宿主流式 emit 通道（rpcStreamEmitRequest）**没有 http_status**，流式错误只能靠「错误文本本身是合法 JSON 且 error.code 命中 clienterror.requestFaultCodes」触发 IsRequestFault，据此新增 requestScopedErrorBody。
  - **验证**：三插件 cgo-shim build/vet/test 全绿；变异验证 6 轮全部 KILLED（其中一轮暴露冗余分支，已补断言加固）。
  - **生产终态**：workbuddy 0.21.18 / qoderwork 0.13.14 / traework 0.5.21 全部 installed+enabled+eff=true，sha256 与 release zip 逐字节一致，均走热重载未重启容器。
- 会话（2026-11-08 19:00-19:20，接续上条）：**同族缺陷扩散修复（workbuddy 0.21.17 / workbuddy-ai 0.1.39 / qoderwork 0.13.14 / qoder-ai 0.1.39）**：
  - **触发**：traework 修复后长窗口复核（1468 条 / 1410 成功 / **0 条成功却零用量**）干净，但 new-api 仍有零星「上游没有返回计费信息」。归因发现**同一份 `cleanChunkJSON` 在四个兄弟插件里仍是修复前版本**（qwenwork 2026-10-18 已修，其余漏改），会把「空 delta + 顶层 usage」的终止计费帧整帧丢弃。当日 feed 佐证：workbuddy 有 6 条成功但零用量（05:35–05:48Z，均为 deepseek-v4.1-flash）。
  - **修复**：该分支内先查 `obj["usage"]`，存在则 `continue` 保留整帧（空 function_call / tool_calls 仍清理），否则维持原丢弃语义。
  - **验证**：workbuddy / workbuddy-ai 在既有 `cleanchunk_test.go` 追加 2 用例（**首轮误覆盖了原文件，已 git checkout 恢复后改为追加**）；qoderwork / qoder-ai 新建 4 用例。四插件变异反证全部 KILLED；cgo-shim build/vet/test 全绿。
  - **发布**：`3782b64`（代码）→ 四条 CI 全部 **Release job success**（darwin/arm64 均 cancelled）→ 各 5/5 checksums OK → `9ec91f4`（registry）→ 生产四条 install 全部 installed + 四个 `hot reloaded`，落盘 `.so` sha256 与 release zip 逐一逐字节一致，**容器未重启**（Up 28h）。
  - **生产验收**：部署后真实请求 3 次响应均含 1 个非 null usage 帧；`oneapi.logs` 新记录 5/9、5/9、5/17 全非零；部署后窗口 20 条记录**零用量 = 0**。
  - **提交瑕疵（如实记录）**：`git add workbuddy workbuddy-ai qoderwork qoder-ai`（目录粒度）把并行会话未提交的 `qoderwork/policy.go`（3 行 marker 追加）带进了 `3782b64`；纯追加、随同一次 cgo-shim 全绿、功能无害，未改写历史。**教训：共享工作树里 `git add <目录>` 会带上他人未提交改动，应按文件粒度暂存。**

- 会话（2026-11-08 15:29-19:05）：**coze 积分红线默认 200 → 10（coze-provider 0.7.5，已发布生产）**：
  - 诉求：用户报「coze 现在的积分每天是 60 签到积分」，要求把「低额自动禁用」改成低于 10 才禁用。
  - 根因：`coze/policy.go` 的 `defaultMinRoutableCredits` 仍是 2026-10-25 定的 **200**，而扣子每日签到额度已降为 **60 分**——红线远高于单日额度，「刚领完签到、正常消耗」的号会在下一轮巡检被整批误禁再靠次日签到解禁。
  - 改动：默认值 200 → 10；配置字段描述 / README 配置表 / 红线段落 / 注释同步；**面板签到徽标与按钮去掉 1500 硬编码**，改读服务端 `daily_credit.amount`；`daily_credit.go` 注释改为「额度随官方活动变化，不得当固定常量」。共 9 文件（+91/−74）。
  - 测试：边界 199/200/201 → 9/10/11，低额夹具压到 10 以下，恢复用例改 60 分口径，用例改名 `TestMinRoutableCreditsDefaultIs10`；**变异反证**（改 11）触发 4 条 FAIL；cgo-shim build/vet/test 全绿、gofmt 干净、面板 2 段脚本语法 PASS。
  - 发布：`eee6065`（代码）→ CI `42460399359` 首轮 darwin/arm64 取消致 Release skipped → `allow_partial=true` 重跑 `42460686443` Release success（5 平台）→ `452f5e7`（registry 0.7.5）→ 资产 URL 200、raw registry 与本地一致 → 生产 install `restart_required=false`，落盘 `.so` sha256 `74546d74…4b87068` 与 release zip 逐字节一致，**容器未重启**。
  - 生产验收：`/v0/management/plugins` coze `registered/enabled/effective_enabled` 全 true、配置字段描述已是「默认 10 分」；`/accounts` 返回 `redline=10 / disabled=0 / low_credits=0 / sweep.refreshed=23 / re_enabled=23`；宿主 `/v0/management/auth-files` 23 个 coze 文件 `disabled=false / status=active`。
  - **新增生产事实**：扣子每日签到额度确认为 **60 分**（账号余额 `remain=60`、`daily_credit.amount=60`、明细 `{"title":"每日登录","remaining":60}`）。历史记录里的 1500 分口径已作废。
  - 遗留：darwin/arm64 平台缺件（registry 已移除该槽位）；红线 10 的业务适配性需生产观察禁用频次再定。

- 会话（2026-11-08 13:24-15:36）：**traework 0.5.19 —— 下行帧从不携带 usage 致 new-api Tokens 全为 "-" 零计费**（详见 `doc/4-bugs/2026-11-08_135204_traework下行从不携带usage致new-api全量Tokens为空零计费根因诊断.md`）：
  - 诉求：new-api 使用日志里 `doubao-seed-2.1-pro` 的 Tokens 列全是 `-`，问是否出错；随后授权「执行并经过完整测试后再上线」。
  - **定性**：请求本身全部成功（首字 14~39s 是思考型模型正常行为），是**计费载体缺席**。`doubao-seed-2.1-pro` 的真实承接方是 **traework-provider**（`upstream.go` 的 `traeConfigNameAliases` 映射到 `Doubao-Seed-2.1-Pro`），不是已下线的 doubao 插件。
  - **双点根因**：①`chunkDelta` / `toolCallDeltaChunk` / `completionAggregate` 三个构造函数只拼 `id/object/created/model/choices`，**从不写 `usage`**，四条下行路径的终止帧全走它们；②上游 `token_usage` 事件真实用量只喂内部 `traeUsageCollector` 写 NDJSON feed 给自家 dashboard，**从不转下行**。决定性事实：宿主 `pluginhost` 对插件 executor 只透传 payload、**没有 usage 观测器**（`ObserveOpenAIStream` 只挂在 openai_compat / kimi / codex 原生 executor），插件不自挂就等于用量彻底丢失。
  - **修复**：抽出 `deltaChunk` 骨架 + `chunkFinishWithUsage`（终止帧唯一出口）；新增 `usageToOpenAI`（输出 `prompt_tokens`/`completion_tokens`/`total_tokens`，cached 落 `prompt_tokens_details.cached_tokens`、reasoning 落 `completion_tokens_details.reasoning_tokens`——**宿主不读顶层 `reasoning_tokens`**，全零返回 nil）；四条路径接入（collect / pump / async 协调器 / 非流式聚合新增 `usageObj` 参数）；新增 `normalizeTraeDetail`（任一桶非零即真实并补 total）与 `tokensFromChars`（短输出保底 1 token，否则估算 0 等于不挂 usage）。`chunkDelta` 恒定不挂 usage（中间帧带 usage 会被客户端按重复计费累加）。**collect/aggregate 返回契约保持不变**（首轮改成返回兜底值导致既有用例 `TestCollectTraeStreamWithoutTokenUsageEmptyDetail` 失败，随即恢复）。
  - **验证**：新增 12 个用例（`usage_emit_test.go` / `usage_emit_async_test.go`），**6 组变异全部 KILLED**（其中异步路径首轮 SURVIVED，补异步用例后 KILLED）；`cgo-shim-build.py traework` build/vet/test 全绿，gofmt 全 FMT_OK。
  - **发布**：`563d8bd`（代码）→ CI `42453870678`（Release success，`darwin/arm64` 的 macos-14 runner 连续三次被取消，与 0.5.18 同状态）→ `d303204`（registry 0.5.19，5 artifacts）→ 生产 install `restart_required=false` + `hot reloaded`，**未重启容器**。
  - **生产验收**：直连 CPA 流式响应 380,644 字节，**usage 帧数 = 1 且在终止帧**（`prompt=53 / completion=1854 / reasoning=1491 / total=1907`）；经 new-api 实测 `oneapi.logs` 流式 53/843/quota 87、非流式 55/560/quota 59 **均非零**，与 feed 同刻记录数值同源。**零值率：修复前 57 条中 40 条零值（70.2%）→ 修复后 0%**。
  - **新踩坑**：①管理密钥在 `config.yaml` 是 bcrypt，明文取插件段 `cpa_management_key`（9 字节），须用服务器自带 `bcrypt.checkpw` 离线验证后再用，错 key 会按源 IP 封 30 分钟；②`nginx cpa.conf` 的 `upstream cpa_pool` 里 `155.103.157.254:8317` 已下线（connection refused），weight=3 的轮询会白白失败——不影响 usage 修复但会拖慢请求；③`download-release-assets.py` 参数是 `<version> <plugin>`（与 publish 相反）。
  - **遗留**：修复前 40 条零值记录无法回溯；`darwin_arm64` 产物缺失（runner 取消，与 0.5.18 同）。

