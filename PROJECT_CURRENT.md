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
- 2026-10-25 历史发布批（已过期，仅存档）：zcode 0.3.1 / qoderwork 0.12.1 / traework 0.5.1 / workbuddy 0.21.2 / cursor 0.1.6 / qoder-ai 0.1.31 / coze 0.6.2 / qwenwork 0.2.9 / copilot 0.1.11 / workbuddy-ai 0.1.30 / token-usage 0.2.8（细节见 git log 与 CHANGELOG）。
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-11-07 (GMT+8)

## 活动会话进展摘要

- 会话（2026-11-07 21:05-22:25）：**workbuddy 首字延迟/超长长尾根因诊断 + P0/P1 修复（0.21.14）**：
  - 诉求：「检查 workbuddy-provider 使用 deepseek-v4.1-flash 有些请求出现首字延迟和生成时间非常非常长的问题，结合 github 其他的项目看看，是不是我们的插件有问题」。
  - **取证**：生产 usage feed 16,944 条样本 —— TTFT p50 **3,479ms** / p90 5,336 / p99 7,993 / **max 150,478ms**；总耗时 p50 5,533 / p99 36,906 / **max 694,166ms**。TTFT>15s 占 0.17%、生成>20s 占 1.78%、总耗时>60s 占 0.27%。**长尾请求 status_code 全为 0（传输级）**。
  - **对照实验（决定性）**：同 prompt 同时段，直连 `copilot.tencent.com` vs 经插件 `127.0.0.1:8317` —— 长输出 267 tok/s vs 162~228 tok/s；大 input（67,637 tokens）2.75~3.79s vs 2.64~3.08s。**同量级，转发链路未引入可观延迟 —— 插件不是常态延迟的原因**。
  - **真因（非插件缺陷）**：生产 95.7% 请求带 `reasoning_effort=max`；长尾记录 `out=17,750~21,943 / reas=14,140~18,227` 而 `in` 仅 70，耗时完全由 reasoning 驱动。对照 `effort=none` 的 lat p50 快 **2.7 倍**。**插件层无法消除（上游无独立思考上限）**。
  - **GitHub 9 家调研**（子代理，实拉源码）：SSE 全部逐行直转（无一聚合）、无一家为拿 usage 等到 `[DONE]` —— 我们这两点与社区一致。**但多家显式设 `ResponseHeaderTimeout=60s` + 禁 h2**（`linguo2625469` 实录「半开 TCP 单次 TTFB 卡 936s」）。
  - **关键反直觉事实**（`dingminhua/dsh-connect-workbuddy` 实测 + 本方同账号复现）：`deepseek-v4.1-flash` **拒绝 `reasoning_effort:off`**（400/11133）；「不发该字段」= 完全不思考，发任何档位 = 思考。**降延迟唯一安全路径是完全不发 `reasoning_effort`，绝不能发 off**。
  - **P0（分阶段超时）**：`sharedHTTPClient` 原只有 `Timeout: 120s`，无 `DialContext`/`TLSHandshakeTimeout`/`ResponseHeaderTimeout`。补 10s / 10s / **60s**，并置空 `TLSNextProto` 真正禁 h2（`ForceAttemptHTTP2=false` 不够）。
  - **P1（HostCallbackID 透传）**：`executorStreamRequest.HostCallbackID` 原有定义**从未使用**；宿主 `rpc_client_stream.go:17-23` 确实下发。沿 `pumpUpstreamStream`/`collectUpstreamStream`/`doExecuteOnce` 三条路径透传；`handleExecExecute` 改用 `executorStreamRequest` 解析（裸 `pluginapi.ExecutorRequest` 无该字段，宿主对 execute 同样下发）。
  - **验证**：cgo-shim build/vet/test 全绿；**变异三轮全部 KILLED**——变异 2 首轮与变异 3 首轮都**未被捕获**，暴露「只测结构体测不到调用点」「纯函数测不到入口接线」两个真实缺口，分别以抽出 `buildHostHTTPStreamWire` + 新增源码级接线断言收口；哨兵法确认新测试真实进编译。
  - **发布**：`8208d85`（代码）→ CI `42380096523`（12 Test 全 success，`darwin/arm64` cancelled；run conclusion=cancelled 但 **Release job=success**）→ `8c3ac5c`（registry 0.21.14，5 artifacts）→ 生产 `plugin-store/.../install` 返回 `restart_required:false` + `hot reloaded`，**未重启容器**。
  - **上线后（28 条）**：TTFT p50 3,413ms / **max 10,601ms**（基线 150,478ms）；lat p50 6,181ms / **max 52,784ms**（基线 694,166ms）。**样本量小、观察窗 20 分钟，属趋势性证据**。
  - **踩坑（新）**：①插件安装路由是 `POST /v0/management/plugin-store/:id/install`，**不是** `/v0/management/plugins/:id/install`（后者 404）；`GET /v0/management/plugins/:id` 同样 404，只有列表 `/plugins` 与 `/plugin-store` 可用。②`download-release-assets.py` 参数顺序是 `<version> [plugin]`，传反会 404。③`git-askpass.sh` 已不存在，改用 SSH 推送（`git@github.com:...`）。
  - **边界（重要）**：`ResponseHeaderTimeout=60s` 在这 28 条样本中**尚未被实际触发**（最长 10.6s），「闸门开启后正确失败并换号」只有单测覆盖、**无生产实证**。生产实际走宿主 `host.http.do_stream`，其 `NewProxyAwareHTTPClient(...,0)` **仍无分阶段超时**，宿主层缺陷未修复（跨项目红线内）。

- 会话（2026-11-07 20:45-21:35）：**三插件同步整改「失败禁用 ↔ 手动启用」同型缺陷（workbuddy-ai 0.1.38 / traework 0.5.14 / qoderwork 0.13.7）**：
  - 诉求：workbuddy 0.21.13 修复后，其余三个插件的 `failover_disable.go` 存在同型缺陷（恢复走 API 不清盘上标记、对账不独立/不清标记），要求一并修。
  - **改动（三插件按各自结构适配，非整文件覆盖）**：
    - **workbuddy-ai**：新增 `isChannelBlockError` → `failureScopeUpstreamBusy` + `noteUpstreamBlockHit`（main/stream 三处调用点）；恢复路径无条件落盘清三键；探活候选去重；对账重写（按 auth_index 去重读盘、只认盘上 `disabled:false`、清标记、仅对 wasOurs 重置计数）+ 独立 2 分钟循环；全池保底；归属丢失自愈。无 sidecar，故只清本地集合。
    - **traework**：同 workbuddy 全套（含 sidecar）；executor.go **六处** 11128 分支统一改走 `noteUpstreamBlockHit`；对账清本地 + sidecar + 盘上标记 + 计数。
    - **qoderwork**：无 11128 链路，故只做恢复落盘清标记、对账判据收紧为「盘上确实 enabled」、清标记 + 计数/探活配额、独立 2 分钟循环；**保留既有池级熔断**（`failoverDisableBudgetExceeded` + 本轮回滚，已覆盖整池清零）。
  - **测试**：新增三个 `failover_sync_test.go`（workbuddy-ai 10 组 / traework 12 组 / qoderwork 5 组）；同步修正 `traework/failover_disable_test.go`（池保底需第三个常驻号）与 `test/traework/failover_marker_restore_test.go`（对账判据需桩出盘上 enabled）。三插件 `cgo-shim-build.py` build/vet/test 全绿。
  - **发布**：`c8b2ae7`（代码）→ 三条 CI run `42375968431`/`42375972582`/`42375977067`（结论均 `cancelled`，但 **Release job 全 success、0 failure**，darwin/arm64 cancelled 属已知取舍）→ assets 各 5/5 checksum OK → `7bb0c38` registry（**只改这三个条目，48 增 48 删，无并行会话污染**）→ 生产 install 三插件全部 200。
  - **生产验证**：`hot reloaded` 三条（workbuddy-ai 0.1.38 / traework 0.5.14 / qoderwork 0.13.7），落盘 `.so` sha256 与本地 zip 逐字节一致（`19616e5e…` / `313d6964…` / `1d4c3525…`）；三插件 `configured/registered/enabled/effective_enabled` 全 true；qoderwork 无 stale 标记、traework 仅 1 个真在禁用的号、workbuddy-ai 归零；无探活风暴。
  - **生产实测请求**：traework `minimax-m3` 200/stop 2.8s、qoderwork `dfmodel` 200/stop 0.7s。
  - **重要发现（非本次改动引入）**：workbuddy-ai 6 个账号上游全部返回 `403 code=11140 "Your account has been restricted due to unusual activity"`，该池自 11-02 起已无成功流量。本次请求走遍 6 个号，**新全池保底只允许禁掉 5 个、保留最后 1 个**（日志 `would empty the pool`），旧版本会把 6 个全禁。**结论：workbuddy-ai 需要补充新账号，代码侧无回归。**

- 会话（2026-11-07 19:56-20:50）：**qoder / trae 生产高强度压测 —— 发现并修复 2 个真实缺陷**：
  - **矩阵**：qoderwork(`dfmodel`) / traework(`qwen3.8-max`) × {显式 2000、显式 100、别名 300、不传、显式 200000} + 并发（3 / 6）。
  - **缺陷一（qoder 别名被丢弃）**：`openAIRequest` 只有 `max_tokens`，新客户端只发 `max_completion_tokens` 时该值被直接丢弃。实测 `max_completion_tokens=300` → 实际生成 **8009 tokens / 7954 字符**；修复后 `comp=334 / chars=299 / finish=length`。
  - **缺陷二（qoder 超大值打穿）**：`max_tokens=200000` 触发上游 `provider_error`；二分确认 `131072` 通过、`200000` 报错 → 新增 `qoderMaxTokensCeiling=131072` 收敛（只拦会报错的畸形大值，131072 内原样透传）。修复后 200000 正常返回。
  - **发现三（traework 模型级，非插件缺陷）**：`qwen3.8-max` **完全忽略 max_tokens** —— `max_tokens=10` 输出完整 100 行（291 字符 / `finish=stop`）；`2000` 与 `100` 输出几乎相同（16983 / 17098 tokens）。插件侧 `buildTraePayload` 单测证明字段正确透传。**此前记忆「trae 上游 max_tokens 只约束正文」基于 `deepseek-v4.1-flash`，不适用于 `qwen3.8-max`。**
  - **上游语义差异（同批实测）**：qoderwork 的 reasoning 计入 max_tokens（`mt=10` → comp=36、正文 9 字符）；traework `qwen3.8-max` 完全不约束；workbuddy 严格按总预算（`mt=100` → out=100 / `finish=length`）。
  - **并发**：qoderwork 3/3 成功（wall 2.4s，comp 47/57/59）；traework 6/6 成功（wall 6.7s，out 56~78）。
  - **发布**：qoder-ai **0.1.38** / qoderwork **0.13.6**，CI 带 `allow_partial` 通过，registry 5 artifacts，生产 `reg=true/eff=true`。
  - **两个副产物坑**：①assets 下载触发 `403 rate limit exceeded`（core 限额充足，403 来自资产通道），改用 `releases/download/<tag>/<asset>` 直链绕过；②下载失败时 `publish-assets.py` 会把插件写成 **0 artifacts**，必须重下后重跑。

- 会话（2026-11-07 19:59-20:40）：**workbuddy 0.21.13 —— 失败禁用与手动启用联动（生产整池被禁事故闭环）**：
  - 诉求：「失败禁用和手动启动有冲突，无法配合联动；刚生产所有账号都被禁用，手动全部启用后失败也没有再次禁用」。
  - **生产取证**：`auths/workbuddy-*.json` 20 个账号中 16 个是 `disabled:false` + `failover_disable:true`（标记 at 时间横跨 10-31 / 11-03 / 11-07 07:28 / 11:49），2 个 `disabled:true` 且无标记；sidecar 只记 2 条。日志 19:49:35-38 同一请求 `05234c3a` 逐个换号撞 19 次 `400 {"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`，账号被逐个写盘禁用 → 整池清零；此后每 10 分钟一轮「probe ok → re-enabled」把同一批 16 个号反复「恢复」。
  - **四个根因**：①11128 被 `isModelBlockedError` 判成账号×模型拦截，`recordFailureScoped` 无条件写盘（无门限），渠道级拒绝被当作账号故障；②恢复走宿主 API 时**不写盘**，`failover_disable` 标记永久残留；③探活候选只要看到标记就认，已启用的号每轮被「恢复」一次，`resetCooldownState` 顺带清零连续失败计数 → 真实失败永远攒不到门限（这才是「失败不再禁用」的机制）；④对账只在 10 分钟探活 tick 里跑且只清内存/sidecar，不清盘上标记，手动启用最长 10 分钟不生效。
  - **修复**：`isChannelBlockError`（11128 + unapproved channel / illegal api invocation）→ `failureScopeUpstreamBusy`（不冷却/不禁用/不换号），三处 11128 调用点统一走 `noteUpstreamBlockHit`；恢复路径无论 API 成败都落盘清三键；探活候选收紧为「盘上 disabled:true 且带标记」或「sidecar 禁用」并按 auth_index 去重；新增 2 分钟独立对账循环（启动即跑一轮）清盘上标记/本地集合/sidecar 并只对确曾归属本机制的号重置计数；新增全池保底（禁用会把可用账号降到 0 时拒绝）；宿主已禁用但无标记时清 sidecar（归属丢失自愈）。
  - **验证与发布**：`cgo-shim-build.py workbuddy` build/vet/test 全绿；新增 `failover_sync_test.go` 9 组用例。`48f9a35` → CI `42372419618`（Test 12 全绿、Release success，darwin/arm64 cancelled 需 `allow_partial=true`）→ assets 5/5 sha OK → `f31bbad` registry 0.21.13 → 生产 `hot reloaded active_version=0.21.13 retired 0.21.12`，落盘 `.so` sha256 `574f6921…` 与本地 zip 逐字节一致。
  - **发布期事故（已修复）**：`publish-assets.py` 之后 `git commit -- registry.json` 把并行会话**未完成**的 qoderwork 0.13.6 / qoder-ai 0.1.38 条目（artifacts 为空数组）一并提交（`f31bbad`），远程 registry 短暂出现两个 0-artifact 条目；已用 `b09f6e7` 复原为 0.13.5 / 0.1.37（各 5 artifacts），`validate-registry.py` OK、远端 raw 校验 ALL PASS。**教训：共享工作树里 `git commit -- registry.json` 会带上他人未提交的 registry 改动，提交前必须先 `git diff registry.json` 全量核对受影响插件。**
  - **生产终态（20:34 实测）**：20 个 workbuddy 账号全部 `disabled=false`、`failover_disabled=None`，盘上 `failover_disable` 标记数 **16 → 0**，sidecar 清空；新版本加载后 12 分钟内 `[workbuddy] failover *` 事件 **0 条**（探活风暴停止）。真实入口 `POST /v1/chat/completions`（deepseek-v4.1-flash）200 / `finish=stop` / 17 tokens。
  - **待观察**：失败禁用路径的生产实证需等自然失败（单测已覆盖门限与渠道级拦截）；探活/对账多实例（热重载遗留 goroutine）仍会各自跑一轮，属幂等冗余。

- 会话（2026-11-07 19:00-19:35）：**traework 0.5.13 —— 模型目录双通道 + 签到 status/冷却 + 版本头对齐真机**：
  - 触发：用户比对 GitHub Trae 开源项目（`JsonLing/trae-cn-provider`、`crush1994/Trae-AutoCheckin`、`bluechonk/trae-credential-reverse-engineering`）后要求「都改」。
  - **模型目录**：`get_detail_param` 由单通道 `solo_work_lite` 改为 `chat_v3` + `solo_agent` 并发拉取，并按固定顺序确定性合并（`chat_v3` 优先；初版按 goroutine 完成顺序合并会让展示名/通道归属随机漂移）。补齐此前完全缺失的过滤：`is_invisible_to_user`、`config_switch=false`、`display_config.is_custom_model`、8 类内部 usage、6 类内部前缀、`-auto` 后缀。合并时记录 `config_name -> 通道`（`traeModelFunctions`），`resolveModelOptions` 聊天选同一通道（错通道上游回 4001），无记录回退 `solo_work_lite`。
  - **签到**：新增 `checkin_credits/status` 预查（`checked_in` → Already、`enable=false` → 未开放），不再对已签账号发无效 claim；新增账号级 9074 冷却（本地 00:00-00:10 早窗 12 分钟、平时 55 分钟，key 优先 uid，账号间不连坐）；`checkinAuthHeaders` 补 `x-app-version-code` / `x-ide-version-code` / `X-User-Region: CN` / `x-device-cpu`。
  - **版本头**：`x-app-version` 默认 `0.1.58 → 1.107.1`；`x-app-version-code` 由「当天日期」改为固定 `20260901`（上游按该数值灰度，实测 20260401 只回 42 条目录、≥20260901 才完整，且必须纯整数）。
  - **发布**：`53fbd7d` → CI `42367639905` Release success（5 平台，darwin/arm64 cancelled）→ `19dd23d`（registry 0.5.13，5 artifacts）→ 生产 `hot reloaded active 0.5.13 / retired 0.5.11`。本版基于并行会话 `aee52c0`（七插件 max_tokens 零干预），CHANGELOG 已注明包含 0.5.12 改造。
  - **生产验证**：模型表 21 → 23；traework `1 → 3`（`deepseek-flash` alias、`Doubao-Seed-Code`、`qwen3.8-flash`）。临时清空 traework 排除项（备份 `/root/cpa-cfg-backup-excl-20261107_192556/excluded.json`）后仍只有 3 个 → **账号级可选集本就 3 个**，原 53 条排除是单通道时代产物，已保留清空。三模型聊天全部 200 且 feed 归因 `provider=traework-provider`，**无 4001**。签到全量 10 账号 **2 秒全部 `already=true`**（仅 status 请求、零 claim）。
  - **风险**：并行会话若再发布 0.5.12，registry 会被回退覆盖；0.5.13 为包含全部改动的最新版本。

- 会话（2026-11-07 18:06-19:10）：**七插件 max_tokens 改为社区共识（减法改造）**：
  - **触发**：用户明示「按 GitHub 众多开源反代项目的方案改造，不要做自己想出来的控制」。实拉 7 家源码取证：tonny0812/workbuddy2api、lovingfish/workbuddy-cliproxy、sixzjd/dsh-workbuddy-connect、dingminhua/dsh-connect-workbuddy、linguo2625469/workbuddy2api-panel、ardeyouxipianyi/workbuddy2api-hub、ithtelab/workbuddy-manager。
  - **社区事实**：①上游对越界 max_tokens 是**静默钳制不报 400**（linguo2625469 实测 16 个 CN 模型 8 个被钳，最狠声称 1M 实际 32K）；②思考与最终回答**共享** output 预算，上游无独立思考上限字段（linguo2625469 `client.go:1283` 独立确认）；③max_tokens **不计入**上下文窗口（sixzjd 实测 70000+64000 通过 100000 窗口）；④GPT 系 <16 报 `code=11133`（两家独立实测 15 拒 16 过）；⑤**七家无一做「抬升显式小值」或「统一地板」**。
  - **改动**：删除四类自研控制 —— `minMaxTokensFloor=32768`、`defaultMaxOutputTokens/defaultReasoningMaxTokens=128000`、推理模式放宽 `<=8192`、广播层无条件抬至 128000。**共 7 个插件**（workbuddy / workbuddy-ai / qoder-ai / qoderwork / traework / zcode / qwenwork）的 `model_specs.go` **逐字节同源**（md5 一致），只保留 `normalizeMaxTokensInPlace`（别名翻译 + GPT<16→16）。请求侧：workbuddy/workbuddy-ai/zcode/qwenwork 走该函数；qoder-ai/qoderwork 显式才写、未传保留 `baseprompt.json` 官方模板的 32768；traework 显式透传、流式未传补 `streamDefaultMaxTokens`。元数据层 `maxOutputTokens()` 改为原样透传，上游不给则留 0（不虚报）。qwenwork/copilot/cursor 中仅 qwenwork 有同类控制，已一并改造；copilot/cursor 本无此机制。
  - **验证**：七插件 `python scripts/cgo-shim-build.py <plugin>` 全绿（build/vet/test），`model_specs.go` md5 唯一；`max_tokens_test.go` 全部重写，覆盖生产事故回归（显式 2000 → 2000）、不放宽（8192 → 8192）、不套地板（100 → 100）、不预截断（200000 → 200000）、别名翻译、畸形值不洗白、GPT 下限。
  - **规则同步**：`skills/project-cpa-workbuddy-plugin-model-specs-and-max-tokens-rules/SKILL.md` 全文重写为「最小干预」，原「反模式三=不干涉」「§3.2 放宽」「§3.3 统一地板」「§5 验收第 1 条」全部推翻。
  - **发布与生产验收（已完成）**：CI 首轮 7 个并行全部 `cancelled`，根因是 `darwin/arm64` 在队列内被取消致 `build-cross != success` → Release 被 skip；**必须带 `allow_partial=true` 才能发布**（run 结论仍显示 cancelled，但 Release job 为 success，判据应看 Release job 与 release 实体，不能看 run conclusion）。带该参数后 7 个插件 Release 全 success。registry 因并行会话提交被覆盖回旧值，已用 `publish-assets.py <plugin> <version> --allow-missing` 重同步（缺 `darwin_arm64` 需显式传参）。生产 `plugin-store install` 全部 200，七插件 `reg=true / eff=true`，落盘 `.so` sha256 与 release zip 逐字节一致。**行为证据**：改造后真实请求 `input=342297`、`reasoning_effort=max`，`output_tokens` 仅 **258**、`reasoning_tokens` 54、耗时 **4.7s**（改造前同场景 12000~22000 tokens / 81~145s）。
  - **并行会话协调**：`traework` 的 0.5.12 被并行会话的 0.5.13 覆盖（其 `53fbd7d` 基于本轮的 `aee52c0`，已含全部 max_tokens 改造），最终 traework 以 0.5.13 上线，无需重发。
  - **待办**：对外广播输出上限仍是上游声称值（多数被钳到 32K），可选引入探测脚本只标注不覆盖。

- 会话（2026-11-07 18:05-18:40）：**traework 0.5.11 —— 0.5.10 未真正生效 + 上游 reasoning 不受控的定性**：
  - 诉求：「先修复 trae 的问题」。
  - **根因**：`traework/upstream.go` 的 `buildTraePayload` ①只在 `stream` 分支调用 `computeAdaptiveMaxTokens`，**非流式直接跳过**；②随后**无条件** `applyMaxTokensFloor`，把客户端显式值抬回 32768。0.5.10 只改了 compute 函数本体，被这两处抵消。生产实证：非流式 `max_tokens=2000` 实际透传 32768，`deepseek-v4.1-flash` 生成 **11203 tokens / 72.1s**（同条件 workbuddy 2000/15s）。
  - **0.5.11 修复**：显式值统一原样透传、仅做上限截断；未传时流式补统一上限、非流式保持不设字段（沿用既有语义）；探活改为显式传 `streamDefaultMaxTokens`，不再依赖 floor 抬值。测试断言同步（流式 4096 / 非流式 2000 / 推理 8192 均原样）。
  - **发布**：`261d0da` → CI `42363141090`（Release success，5 平台，darwin/arm64 cancelled）→ `86cc607`（registry 0.5.11，5 artifacts）→ 生产 `hot reloaded active_version=0.5.11 / retired 0.5.10`。
  - **核心定性（改变认知）**：**Trae 上游的 `max_tokens` 只约束正文、完全不约束 reasoning**。实测 `max_tokens=16` → 正文 18（16+2 tokenizer 差）、reasoning 仍 1311；`max_tokens=2000` → 正文约 2000、reasoning 3000~6000、总耗时 38~94s。qoderwork 上游同语义（2000 正文 + 2213 reasoning）。**workbuddy 上游把 reasoning 计入总额度**（精确 2000，其中 2000 全为 reasoning）。因此 traework/qoderwork 的延迟瓶颈是 reasoning 生成，**插件层无法解决**；traework 真正降延迟只能靠客户端缩短 prompt 或上游支持 thinking 开关（payload 无任何 thinking/budget 字段）。
  - **路由真相再更正（重要）**：同名模型不是固定落 workbuddy，宿主按周期在同名 provider 间轮换——同一 key 连续 10 次短请求分布 traework 6 / qoderwork 4 / workbuddy 3；另一窗口 6 次全 qoder、随后 5 次全 traework+workbuddy。**此前所有基于 1~2 次采样的「永远被 workbuddy 抢走」结论均作废**，定向验证必须多次采样。
  - **接口事实**：`/v0/management/plugins/traework-provider/models` 需 `auth_index` 但返回 `{"models":null}`（缓存空）；traework 10 个账号全部 active，非账号故障。

- 会话（2026-11-07 17:42-17:50）：**方案 1 定向隔离引发生产 400，已完整回滚 + 零改动定向验证成功**：
  - 故障：为隔离 qoder/trae，把 `deepseek-v4.1-flash` 加入 `oauth-excluded-models` 的 `workbuddy-provider` + `workbuddy-ai-provider`（`PUT /v8/management/config/oauth/excluded-models`，整表覆盖）。17:42~17:48 期间 `deepseek-flash` 与 `deepseek-v4.1-flash` 全部返回 `400 unknown provider for model deepseek-v4.1-flash`，用户报「现在无法使用了」。
  - **根因**：`/v1/models` 的 `owned_by` 显示该模型 ID **只有 `workbuddy-provider` 注册**（workbuddy 11 个 / workbuddy-ai 3 个 / qoderwork 仅 `dfmodel` / traework 仅 `deepseek-flash` / coze 5 个）。别名把 `deepseek-flash` 改写为 `deepseek-v4.1-flash` 后，路由键就是这个 ID；摘除 workbuddy 系后**没有任何 provider 认领** → 400。方案 1 的前提「摘除后 qoder/trae 能接住」从一开始就不成立。
  - **回滚**：备份目录 `/root/cpa-cfg-backup-20261107_174231`（`config.yaml` + `oauth_excluded_models.json`）；用备份 JSON 原样 PUT 回写，17:47:56 日志确认 `workbuddy-ai-provider 28->27`、`workbuddy-provider 15->14`。验证：GET 与备份**逐字一致**（`config-version:8`）、`config.yaml` 1328 行且非空行多重集零差异（仅行序/空白重排，语义等价）、两个模型真实请求均 `200`、模型表 21 个、三插件版本 0.21.11 / 0.13.4 / 0.5.10 不变。临时脚本 `/root/iso.py` 已删除，备份目录保留。
  - **铁律（新增）**：动 `excluded-models` 前必须先用 `/v1/models` 的 `owned_by` 确认**目标模型 ID 至少还有另一家 provider 能提供**；路由键是 **alias 改写后的名字**，不是插件注册名。仅凭「feed 里见过该 provider + 该模型」不足以推断可承接。
  - **零改动定向验证（qoderwork 成功）**：直接用其专属模型 `dfmodel` 发同 prompt + `max_tokens=2000` + `reasoning_effort=max` → feed `provider=qoderwork-provider`、`model=dfmodel`（未被别名劫持）、`out=1311`（≤2000，放大已消除）、`finish=stop`、耗时 **12.2s**（修复前 7029 tokens / 94s）。证明 0.13.4 的修复在 qoderwork 上真实生效。
  - **traework 与 workbuddy 原生 ID 硬冲突，配置层无解（17:54-17:58 两次实测）**：alias 结构为 `{alias, fork, name}`，是 **`alias → name` 改写**，`name` 必须是插件原生 ID。实测映射：qoderwork `deepseek-flash → DeepSeek-Flash`、traework `deepseek-flash → deepseek-v4.1-flash`、workbuddy `gpt-5.6-luna → deepseek-v4.1-flash`。**traework 的对外名被改写成 workbuddy 的原生 ID，流量必落 workbuddy**；`/v1/models` 的 `owned_by` 展示的是 alias 归属而非原生注册 ID，不能据此判断承接方。两次安全尝试均 `400 unknown provider` 且已自动恢复：①临时解禁 traework 排除项 `deepseek-v4-pro-official`（排除名≠已注册原生 ID，被排除模型根本不注册）；②在 traework 分组新增探测别名 `trae-ds-flash-probe → deepseek-flash`（alias 不能创造原生注册，`deepseek-flash` 本身不是原生 ID）。**结论：定向 traework 只有两条路——摘除 workbuddy（已实测 400，禁止）或改插件代码暴露专属模型 ID（需发版）**。收口：4 模型全 200、模型表 21、探测别名零残留、alias GET 与备份完全一致、config.yaml 仅 alias 字段顺序互换（语义等价）。

- 会话（2026-11-07 17:25）：**长推理 80~145 秒卡死根因修复（workbuddy 0.21.11 / qoderwork 0.13.4 / traework 0.5.10）**：
  - 诉求：高强度测试 qoderwork / traework 的 `deepseek-v4.1-flash` 推理请求，要求不测到 workbuddy；随后明确判定标准「首字延迟和生成时间过长的都是 bug」。
  - **压测基线（修复前）**：同 prompt、`max_tokens=2000`、`reasoning_effort=max` 下，traework 生成 9908 tokens / 72s、qoderwork 7029 tokens / 94s。**对照实验推翻了「只有这两个插件慢」的前提**：直连 workbuddy 同名模型生成 12030~21943 tokens / 81~145s，比另两个更慢。
  - **根因**：三插件 `model_specs.go` 的 `computeAdaptiveMaxTokens` 在思考模式下把客户端 `<=8192` 的 `max_tokens` 一律放宽到 `defaultReasoningMaxTokens`（128000），2000 被放大 64 倍，模型因此可无限推理。`applyMaxTokensFloor` 还会把非推理档的显式小值抬到 32768。
  - **修复**：显式传入 `max_tokens` 时原样尊重、仅做上限截断；只有未传（`<=0`）才补足统一上限。三插件同源同步修改，测试断言同步（推理档 8192/2000 保留、非推理档 8192 保留、`relaxMaxTokensInPlace` 对显式值不再改写 payload）。
  - **发布**：`5c1f4c6`（代码）→ CI `42358546508`/`42358554570`/`42358566325`（三条 run 均 `cancelled` 但 Release `success`，仅缺 `darwin/arm64`）→ `41d680f`（registry）。生产热重载三插件到 `active_version` 0.21.11 / 0.13.4 / 0.5.10。
  - **修复效果（workbuddy 实测）**：`out=2000` 精确卡上限（修复前 12030~21943），单请求耗时 **13.8~15.2s**（修复前 81~145s），token 降 90%、耗时降 89%。
  - **隔离结论（17:47 修正，原「摘除 workbuddy 该模型」建议已作废）**：`oauth-model-alias` 把 `deepseek-flash` 改写成 `deepseek-v4.1-flash` 后仍按模型名匹配所有提供方，workbuddy 优先级更高会抢走流量（响应体 `"model":"deepseek-v4.1-flash"`、feed `provider=workbuddy-provider`）。`/v1/models` 的 `owned_by` 实测证明 **`deepseek-v4.1-flash` 只有 `workbuddy-provider` 提供**，qoderwork 仅注册 `dfmodel`、traework 仅注册 `deepseek-flash`，二者都不含该 ID —— 摘除 workbuddy 系必然 `unknown provider`，**方案 1 不可行且已造成 5 分钟生产 400**（详见下条会话）。正确做法：直接用 provider 专属模型名（`dfmodel` 可零改动定向测 qoderwork）；traework 需先处理 alias 劫持。
  - **附带的第二处差异**：`traework/usage_feed.go:159` 明确记录 Trae 上游不支持 `reasoning_effort`，该参数从不透传；`max_tokens` 是其唯一推理深度收口，被放大后等于完全失控。

- 会话（2026-11-07 16:00）：**慢速冷却池准入闸门 20→10 发布（qoderwork 0.13.3 / traework 0.5.9）**：
  - 诉求：「qoder 和 trae 的插件，慢速冷却的『池大于 20』阈值改成 10」。
  - 改动 4 处：两插件 `slow_account.go` 的 `defaultSlowMinPoolSize 20 → 10`（注释补改动原因）、`main.go` 的 ConfigField 描述「默认 20」→「默认 10」；另同步 `slow_account_test.go` 里 `TestSlowAccount_PoolSizeGateBlocksJudgement` 的硬编码 20/21（此断言漏在第一轮 grep 之外，靠 cgo-shim 失败才暴露，改为 10/11）。
  - 依据：生产 `config.yaml` **未显式配置** `slow_min_pool_size`，故改默认值即生效；当前池规模 qoderwork 18 / traework 12，均大于 10。
  - 发布：`6992ba2`（代码）→ CI `42349569872` / `42349577549` → `f29ac91`（registry）。**首轮 CI 两条 run 均 `cancelled`**：`darwin/arm64` 在 macOS runner 池排队超时被系统取消（`runner=` 为空），导致 Release `skipped`；带 `allow_partial=true` 重新 dispatch 后 Release `success`，产物 5 平台（缺 darwin/arm64），`publish-assets.py --allow-missing` 正常发布。
  - 生产：热重载 `active_version=0.13.3`（retired 0.13.2）/ `0.5.9`（retired 0.5.8）；落盘 `.so` sha256 `451a3bf6…` / `1c998739…` 与本地 release zip 内二进制逐一相同；`config_fields` 元数据两者均显示「默认 10」。
  - **重要澄清（本次踩坑）**：热重载后 `/v1/models` 从 25 降到 19、qoderwork 全部对外模型（`QoDuck`/`lmodel`/`jmodel`/`mmodel`/`kmodel_latest`/`Kimi-K3.1-Pro`）消失，一度误判为「宿主热重载跳过 `callRegister`」，为此重启了容器（15:42，已获用户当轮授权）。**重启未恢复，真相是配置**——管理 API 审计日志显示 `14:53:32 oauth-excluded-models[qoderwork-provider]: updated (13 -> 20 entries)`（同批 `traework-provider: 52 -> 54`），即**并行会话在 14:53 通过 `PUT /v8/management/config/oauth/excluded-models` 主动收紧了模型暴露**。排除清单现有 19 条，含 qoduck/lmodel/mmodel/jmodel/kmodel_latest/kimi-k3.1-pro 等。**结论：账号恢复与本次发布均无问题（探活 `ok=True`），qoderwork 当前对外无可用模型是配置策略所致，是否解除排除待用户决定。**
  - 生产现状：6 插件 enabled+registered（coze 0.7.4 / qoderwork 0.13.3 / traework 0.5.9 / workbuddy-ai 0.1.36 / workbuddy 0.21.10 / token-usage 0.2.8），账号 qoderwork 18 / traework 12 / workbuddy 21 / coze 23，模型 19 个；真实入口 `glm-5.3`、`kimi-k3.1-pro` 均 `finish_reason=stop`。
  - **可迁移教训**：模型从 `/v1/models` 消失时，**先查 `excluded-models` 与管理 API 审计日志（`config_reload.go` 的 `updated (A -> B entries)` 行）**，再怀疑热重载/注册链路。本次为错误假设复了一次容器重启，成本不必要。

- 会话（2026-11-07 14:45）：**traework 12 个被删账号找回（账号数据恢复 + 插件启用，未改代码）**：
  - 诉求：「tear 的好像也有，看看能不能还原」。`traework` 池恢复前仅 1 个账号且**插件处于 `enabled=false / registered=false`**（`/accounts` 返回空）。
  - **恢复源**：`/root/traework-recovery/restored/`（10-25 误删恢复时产出的 17 个账号文件）+ `refreshed_tokens.json`（10-25 的 16 个刷新结果）。**10-25 的全表 dump 里 traework=0**，因为那是「恢复 traework 之前」建的还原点——按前缀查会得出「无源可恢复」的错误结论。
  - **刷新实测**：对 16 个逐个重放 `POST api.trae.cn/cloudide/api/v3/trae/oauth/ExchangeToken`（ClientID 用 `en1oxy7wnw8j9n`，不是代码常量 `ono9krqynydwx5`）→ **12 成功 / 4 失败（401/20101，真死）**。失败 uid：`1712389365702043`、`1941087193676826`、`2046641643717561`、`2750333044403947`。
  - **关键新事实（推翻旧假设）**：traework 的 `refreshToken` 每次都返回新值（16/16 全部不同），但**旧值在有效期内可重复使用**——同一 RT 连续两次调用均返回 200。因此「轮换 ≠ 一次性作废」，不能因怕消费而放弃实测；正确做法是每成功一个立即落盘。`note` 的 "Session expired" 不能作判据（6 个带此 note 的账号里 3 个照样刷新成功）。
  - **写回与验收**：还原点 `PRE2_auth_store_20261107_143807.sql`（220KB）+ `PRE2_spool_*.tar.gz`（59KB）+ 基线 `traework=1/1`；写 auth_store 12 行 + 原子写 spool 12 个 → `auth_store=13 / spool=13`；启用插件后 `enabled=true / registered=true`，`/accounts` 加载 **12** 条（旧的 `3313287900183084` 未加载：spool 文件是 `str` 而非 dict，且 note 为 session expired）。
  - **探活**：12 个全量 `test-active` → **9 通过 / 3 上游业务态失败**（`1114256688551036` quota exceeded、`3841050312977305` 与 `4491956683805020` all models failed），**0 个凭据失效**。
  - **踩坑（新）**：`/v1/models` 看不到任何 traework 模型、真实入口报 `unknown provider for model` —— 根因是 `config.yaml` 的 `excluded-models.traework-provider` 列了 **52 个**模型名，而插件动态拉到 **44 个**，其中 **43 个被排除**，只剩 `kimi-k3.1-pro`。用该模型真实入口 `finish_reason=stop`、正文「正常」、119 tokens，证明链路通。**excluded-models 待用户决策是否清理**（属配置策略变更，未擅自改）。
  - 产物：`/root/traework-recovery/restored_v2/`（12 个 600）、`restored_tokens_v2.json`（12 条新凭据）、`restore_traework.sql`。

- 会话（2026-11-07 14:15）：**qoderwork 16 个被删账号全量找回（账号数据恢复，未改代码）**：
  - 诉求：「qoder 之前有很多账号，后来被删了，254、65 都有删除，可以找回吗」。
  - **结论：16 个账号完整找回，上游 18/18 存活**。恢复后 qoderwork 池 2 → 18，全部 `active / disabled=false / exhausted=false`。
  - **唯一恢复源**：`/root/traework-recovery/PRE_RESTORE_auth_store_20261025_134113.sql`（md5 `b0320129…`，10-25 为恢复 traework 误删而建的全表 dump）——它是全表备份，顺带保住了另外 5 个 provider 的历史账号。
  - **已排除的源**：11-06 23:21 迁移备份与 11-07 01:30 的 254 最终归档均 qoderwork=0（证明删除早于 11-06 23:21）；254 binlog 仅存 000036~000061（最早 11-06 03:43，24h 窗口）检索不到 qoderwork；`auth-backup-20260908` 只有 4 traework + 1 workbuddy。
  - **决定性判据（新增事实）**：能否救回取决于**凭据家族**——`dt-/drt-`（OAuth device，drt- ~1 年、rotating）可恢复；`jt-/jrt-`（PAT，jrt- 仅 48h）超 48 小时即不可救。本次 18 个全为 `dt-/drt-`，`accessToken` 有效期到 2026-11-23，故 13 天后仍 18/18 上游 200 且返回 uid 与本地逐一匹配。
  - **写回与验收**：先建三层还原点（`PRE_RESTORE_auth_store_20261107_141226.sql` 206KB / spool tar 56KB / 基线 `0`）；写 auth_store 16 行（`CAST(X'…' AS JSON)`）+ 原子写 spool 16 个 → 宿主 14:12:48 自动加载；**发现并修掉存储层不一致**：在役 2 个账号原本只存在于 spool、不在 auth_store（qoderwork 由插件直写物理文件），已用**当前 spool 最新内容**（非 dump 旧 token）补写，避免重启回滚旧凭据。最终三方对齐 `auth_store=18 / spool=18 / /accounts=18`。
  - **行为验收**：抽样 3 个新恢复账号 `/test-active` 全 `ok=true`；真实入口 `POST /v1/chat/completions`（model `QoDuck`）`finish_reason=stop`、正文「正常」、credits 0.36 正常结算。
  - 产物：`/root/qoderwork-recovery/`（`restored/` 16 个凭据文件 600、`manifest.json`、还原点、`backfill_2.sql`）。知识库：本次「凭据家族决定可恢复窗口」「全表 dump 是跨 provider 兜底源」两条已补进项目 skill。

- 会话（2026-11-07 13:40）：**qoderwork 0.13.2 签到核验 + 修 3 缺陷**（已发布）：
  - 核验：**链路正常**。上游探测：`01a1595b` 有 `CLAIM_BENEFIT+CREDITS 100`、窗口 11-07 10:00→11-08 09:59、`CLAIMED`（12:03 已领）；`01a15e9a` 只有 `VIEW_DETAILS`、无签到活动（上游按账号下发）。
  - 缺陷：`StreakDays` 硬编码 1（编造「连续 1 天」）、success 分支不返回该字段（toast「连续 ? 天」）、签到零日志。
  - 修复：streak 恒 0、toast 改「+N 积分」、补 `[qoderwork] checkin` 日志（skip already/no_campaign、ok credit=+N、auto-* 失败）。
  - 验证：`checkin_streak_test.go`（3 形态 + 积分来源 + 过期窗口）、2 变异 KILLED、cgo-shim 全绿、`b43d089`→`32df5df`、生产 hot reloaded 0.13.2 且两账号日志正确。

- 会话（2026-11-07 02:00）：**qoderwork 0.13.1 修复「推理各种失败、断开」**（已发布 + 生产热重载验收）：
  - 用户报「`1846555387` 推理失败、断开」。该数字在账号 / CPA 日志 / new-api 全表 / 仓库**均无命中**（需澄清来源）；两账号 `/test-active` 659/570ms 成功 → **账号健康，问题在失败处置**。
  - **根因**：① `recordAccountFailure` 无条件写盘禁用 → 一次 5xx/断流/限流就摘号，池仅 2 个号等于砍半；② 流式 `sseErr`、非流式 `completionErr`、collect 三处把 HTTP 200 错误帧 remap 成 **403** → 软限流被当凭据故障，首败即禁。
  - **修复**：新增 `failover_disable_min_count`（默认 3；瞬时失败连续 3 次才禁，401/403/404/405·402·110 日限额首败即禁）；新增 `noteUpstreamErrFrameForModel`（200 错误帧按瞬时记账、额度升 402）；`recordCooldownState` 返回计数供门限判定。排队帧（10605/isQueued）与模型级故障仍不罚号。
  - 验证：新增 `failover_disable_threshold_test.go`（11 瞬时 + 7 确定性 + 端到端门限 + 红线 + 不伪 403）；**3 变异全 KILLED**；cgo-shim 全绿。
  - 发布：`af216de`→`020313e`(CI)→`6c7d253`(registry 0.13.1)；生产 hot reloaded 0.13.1、sha 一致、accounts/panel 200、两账号冒烟成功、配置项已注册。**`darwin/arm64` 缺资产**（`macos-15`/`macos-14` 均 `runner_id=0` 被取消，macOS 配额问题非代码），registry 5 平台；补齐无需新版本号：runner 恢复后重新 dispatch 同版本 → Release `--clobber` → 重跑 `publish-assets`。边界：冷却窗口仍在（两号同时冷却时交宿主换道），根因来自代码路径对比、无拥堵窗口实证。

- 当前会话（2026-11-07 01:50）：**非推理输出上限 64k → 128k（7 插件发布 + 生产热重载验收通过）**：
  - **用户诉求**：「默认的最大输出统一提高，现在好像非推理是 64k?」，裁定非推理 64000 → 128000，覆盖 7 个统一体系插件。
  - **根因层面的关键**：不是单纯放宽。`models.go` 的 `maxOutputTokens()` 自 2026-10-30 起对全部模型恒返回 128000，而 `computeAdaptiveMaxTokens` 非推理分支只补足 64000，形成「元数据 128k、请求层 64k」的既有矛盾。
  - **改动**：7 份 `model_specs.go` 的 `defaultMaxOutputTokens` 64000→128000；`minMaxTokensFloor` 32768 与 `defaultReasoningMaxTokens` 128000 不变；6 个 `max_tokens_test.go` 期望值同步，traework 新增 `TestUnifiedOutputCap`；7 插件 cgo-shim 全绿。
  - **发布阻塞与解法**：macOS 托管 runner 在 UTC 17:00 前后长期排不到机器（darwin job `steps=0` 被 cancelled → Release skipped）。新增 `allow_partial` 输入（`0d83256`）：Release 只要求 build/build-cross 未被 skipped，完备性校验降级为 WARN。**降级事故（已修复）**：批处理按旧版发布把 registry `qoderwork` 写成 0.12.7，已重新 publish 恢复。
  - **最终版本与验收**：workbuddy 0.21.10 / workbuddy-ai 0.1.36 / qoder-ai 0.1.36 / qoderwork 0.13.0 / traework 0.5.8 / zcode 0.3.6 / qwenwork 0.2.14（`59a8fee`+`0d83256`+`cf54224`）；6 个 install 全 200、7 个 `hot reloaded`、落盘 sha 一致、11 插件 `version==installed_version`、`/accounts`+`/panel` 全 200；`glm-5.3` 不传 `max_tokens` 返 200 + `finish_reason: stop`。
  - **已知边界**：workbuddy-ai 4/6（缺 darwin 两平台），其余 5 个 5/6（缺 `darwin_arm64`）；刻意未补发——重发同版本会重建 Release 并可能改资产哈希。

- 会话（2026-11-07 01:10）：**qoderwork 0.13.0 面板批量导入**：旧弹窗只能单 PAT。改为多文件多选（.json/.jsonl/.txt ≤2MB）+ 统一识别（PAT 多行、单凭据、数组、`accounts[].credential`、`{pats:[]}`、JSONL、混排）；**判定先凭据后 PAT**（否则含 personalToken 的凭据被当 PAT）；PAT→`/import`、凭据→`/import-cred`；串行 +150ms、批内去重、失败收敛为「仅失败项」。验证：两个 .mjs（50 断言）+ 5 变异 KILLED + cgo-shim 全绿 + CI `42260811851`；`dfaecb6`→`8c41737`，生产 hot reloaded、accounts/panel 200。边界：未在生产实跑导入。

- 当前会话（2026-10-31 下午）：**宿主 management.html 自动升级导致 10 插件面板集体 401（已修复并批量发生产，热重载验收通过）**：
  - **用户诉求**：贴出生产截图（WorkBuddy 面板「management key 无效或缺失（将自动重试）」，计数器全 0，侧栏「认证文件 44」）问「这是什么问题?」，随后「顺便把其他 10 个同实现的插件一起改掉」。
  - **根因（生产实证）**：宿主 `management.html` 由 `managementasset` 每 3 小时自动拉取，**2026-10-31 05:45:30** 那次升级把 key 混淆从 `enc::v1::`（XOR key = `salt|host|userAgent`）换成 `enc::v2::`（`salt|host`，去掉 UA）；插件 `deobfuscate()` 只认 v1 → 原样返回密文 → `JSON.parse` 失败 → `readPanelKey()` 返回 null。**症状被第二层缺陷放大**：旧版插件曾把未解密密文写进 sessionStorage，`getKey()` 又优先读它，于是面板持续用错误 Bearer 打服务端停在 401（宿主日志 14:51:08–14:52:59 五次 401），不会自愈。
  - **已排除**：账号数据丢失（`/accounts` 200 且 10 个账号）、宿主 management key 问题（无 key 401 / 正确 key 200）、example-api-key safe mode（生产 `api-keys` 无 `your-api-key-*`）、插件后端鉴权（`checkManagementAuth` 只拦 mutating 路径）。
  - **修复（10 文件 +20/-3 各一份）**：`ENC_PREFIX_V2` 常量 + `_keyBytes(v2)` 双派生（v2 不拼 UA、v1 保留 UA 兼容旧落盘值）+ `deobfuscate()` 双前缀分支 + 新增 `readSessionKey()` 丢弃任何 `enc::` 前缀的遗留密文。覆盖 workbuddy / workbuddy-ai / copilot / coze / qoder-ai / qoderwork / qwenwork / traework / zcode 的 `panel.html` 与 `cursor/internal/plugin/assets/management.html`。
  - **验证**：10 面板 v1/v2/明文三组解码向量 + 3 组 E2E（clean-v2 / session 残留 v2 / clean-v1）全绿；`cgo-shim-build.py` 10 插件全绿；10 个 CI run 全 success（`41181309134` 等）；**生产热重载 9 个 `hot reloaded` + cursor 首次安装 `loaded+registered`（`overwritten=false`，无 retired 版本属正常）**；10 个落盘 `.so` sha256 与本地 release zip 逐一一致；`/accounts` 与 `/panel` 全 200（workbuddy 10 账号、coze 17 账号）；浏览器侧对 10 个生产面板跑完整断言（v1/v2/明文/损坏值安全/遗留密文清理/明文 session 保留）全部通过。
  - **版本**：workbuddy 0.21.9 / qoderwork 0.12.5 / traework 0.5.6 / workbuddy-ai 0.1.34 / cursor 0.1.7 / qoder-ai 0.1.35 / zcode 0.3.4 / copilot 0.1.12 / qwenwork 0.2.13 / coze 0.7.1；commit `79b581e`（代码）+ `368b4f3`（registry）。
  - **待观察**：宿主下次自动升级若再改格式，同类症状会复发；排查入口固定为「`docker cp` 出生产 `management.html` 比对其 `enc::v` 前缀与 `yl()` 派生函数」。project memory 已沉淀 `project-host-key-obfuscation-versioning.md`。

- 会话（2026-10-31 凌晨）：**workbuddy 高峰期拥堵识别与失败分类修正（0.21.8，已提交推送，CI 发布中）**：
  - 诉求：「workbuddy 客户端有模型拥堵状态，插件看不到，高峰期几乎无法使用」；裁定：参考 `linguo2625469/workbuddy2api-panel`、允许生产只读实测、只做 `workbuddy-provider`。
  - **结论：不是「看不到状态」，而是拥堵被系统性误判成账号故障形成雪崩**。四缺陷：D1 SSE 只认 `{"error":...}`，顶层 `{"code":6004}` 帧被当正文透传；D2 5xx 不换号；D3 一次失败即写盘禁用；D4 探活标记缺失永久跳过。放大链：拥堵 → 账号级记账 → 一次即禁用 → 池缩 + 亲和打散 → 更多拥堵。
  - **修复（10 文件 +679/-51）**：新增 `failureScopeUpstreamBusy`（6021/`queue.full`：不冷却不禁用不换号）与 `failureScopeModelRateLimit`（6004：只隔离账号×模型），判定顺序放在通用限流与 5xx 之前；`sseErrorFrame` 补顶层业务码帧；5xx 纳入同请求换号；写盘禁用加 `failover_disable_min_count`（默认 3，仅作用于瞬时类失败，确定性 401/403/404/405、402、11128 直接禁用）；探活无模型时回落 `failoverStateSnapshotModel`。
  - 自查修正：门限初版无差别生效打破 5 个既有测试，判定为设计缺陷而非测试过时，拆 `recordFailureScopedWithCause`；`TestRunFailoverProbeWithoutFailedModelSkips` 原本固化事故根因机制，已改写为兜底语义。
  - 验证：cgo-shim 全绿；**6 项变异全部 KILLED**；发布链 `611e340` → CI `41120779119`（`plugin=workbuddy-provider version=0.21.8`）。**踩坑**：Git Bash 直连 HTTPS + askpass 不可用，正确通道是 SSH（`ssh://git@ssh.github.com:443`）；`gh` 未装，API 操作走 `~/.github/token`。
  - 边界：拥堵窗口（京 15–17 点）未取得生产实证，`6004`/`6021` 报文形态依据参考仓库实案注释；未做面板拥堵可视化。产物见 `doc/2-需求` 与 `doc/3-实施` 的同名文档。

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

- 更早会话（2026-10-25）：**慢速账号冷却跨五插件移植完成并批量发生产**：
  - **用户裁定**：traework / qoderwork / zcode 三插件实现；workbuddy-ai（2 号）与 qoder-ai（0 号）只落文档计划——池子 <10 时 P25 基线不成立；zcode 选「完整改造与其他插件对齐」；批量发布。
  - **两个前置硬前提**：① **per-attempt TTFT 计时隔离**是阻断项（请求级时间戳跨 attempt 复用会把 A 的失败耗时记到健康号头上）；② **账号池 ≥10**，否则 P25 退化成较小值。故顺序固定为「先补计时基准 + 用例锁定，再接状态机」。
  - **各插件接入点差异（不能整体复制文件）**：traework 四处；qoderwork 用 `pickNextAuth` 变参 `model ...string` 避开 10 处调用点；zcode 单点 `accountRoutable` 覆盖三条路径 + 补非流式 TTFT 采集。
  - **发布链**：`13fb652`(feat, 48 文件) → 三 CI success → `3abc5eb`(registry) → 三插件热重载部署，sha256 逐字节一致。中途发现 `recordSlowSample` 到期后不清零 `cooldownUntil` 导致日志刷屏，修复后升 patch 二批发布（traework 0.5.1 / qoderwork 0.12.1 / zcode 0.3.1）→ `494bf80` + `a820790` → `06f10bd`(registry)。**教训**：注释不能替代用例。
  - **边界**：慢速判定的正向触发未在生产窗口复现（只有单测 + 变异覆盖）；zcode 生产池仅 1 个账号，P25 基线不成立，机制实质不生效。

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

- 更早会话（2026-10-18 ~ 2026-10-23，完整记录见 git log 与 PROJECT_HISTORY.md）：
  - workbuddy 0.17.1/0.18.0：低额红线 20→400（严格 `<` 由 399/400 用例锁死）；Global 耗尽不再自动删号，改与 CN 一致写盘禁用；`min_routable_credits` 登记 ConfigFields。
  - workbuddy-ai 0.1.22 / zcode 0.1.35：补四会话追踪头（修国际版 400/11128）与 schema 6 防 OAuth URL `&` 转义。踩坑：`plugin-store install` 必写 `plugins_enabled:true`，只发版不启用须再 `PUT config {"enabled":false}`。
  - 下线 doubao/notion（13→11 插件）：源码、测试、图标、文档、registry、CI 矩阵、生产残留全清。
  - coze 0.4.6→0.5.4：64KB content 返 413；积分红线走执行入口闸门；生图 `awaitImages` + 4 分钟上限；整池 0 分快速失败 400。
  - 基础设施：6 插件 4h 槽位改 `next*FireTime` + 5m 抖动；账号故障冷却基数 30s→60s；Qoder 系修 GET 必带 `qoderEncode("{}")`（此前恒 403）、补负缓存 5min、`pickNextAuth` 退避、缓存按账号分片；其余含 qwenwork/copilot 立项、发布矩阵去 freebsd、max_tokens 自适应 32768 底线、排队帧不判账号故障、删除 Gemini。

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
