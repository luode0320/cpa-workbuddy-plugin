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

- 会话（2026-11-07 21:45 起，跨 6 轮）：**qoder/trae 生产高强度压测（详见 doc/4-bugs/2026-11-07_223000_qoder-trae畸形请求打穿整池账号根因诊断与修复.md）**
  - **诉求**：高强度测试 qoder/trae 的 deepseek-flash 找 bug 并修复；同时关注 workbuddy 的 deepseek-v4.1-flash。
  - **共修复 4 个缺陷**（全部已发布 + 生产 hot reload 验收）：
    1. **请求体 role 非法打穿整池**（0.5.16 / 0.13.9）——按上游报错正文匹配的方案（0.5.15/0.13.8）经生产复验**无效**（上游措辞不固定、qoderwork 只回无信息量的 provider_error），改为**发请求前**按 `validOpenAIRoles` 校验，非法 role 直接 400 不发上游。
    2. **traework `max_completion_tokens` 别名被静默丢弃**——新客户端只发别名时值被 json.Unmarshal 丢弃，实测请求 12 token 实际产出 1119 tokens。
    3. **`max_tokens` 畸形值打穿整池**（0.5.17 / 0.13.10）：客户端发 `{"max_tokens":"abc"}` 时 int 强类型让整包 `json.Unmarshal` 失败，宿主当成「账号不行」遍历整池每号失败一次后合成 503。修复：`flexNumber` 容错（数字/数字字符串取整，其余降级 0=未声明）。实测 deepseek-flash 500→**200**（扇出 14→1）、dfmodel 500→**200**（扇出 5→1）。发现路径是分析**真实 feed**：按分钟聚合见 `16:07:50` 同一秒 15 条 `status=0/latency=0`。
    4. **`stream`/`temperature`/`top_p` 畸形值打穿整池**（0.5.18 / 0.13.11）：把第 3 条抽象成模式后系统审计其余强类型标量，实测确认另有 3 个入口——`"stream":"yes"` 扇出 6 号（日志 18 条 `stream of type bool`）、`"temperature":"abc"` 扇出 9 号（14 条）、`"top_p":"abc"` 扇出 2 号（1 条）。修复：`flexBool`（认 true/false/"true"/"false"/"1"/"0"/1/0）与 `flexFloat`（数字与数字字符串取值，自带 `set` 标记区分「显式 0」与「未声明」）。至此该家族 4 个入口全覆盖，其余字段（messages/tools/tool_choice/reasoning_effort）实测扇出 0~1 次无风险。
  - **压测覆盖**：参数矩阵、并发、工具多轮闭环、多轮对话、长上下文、错误路径、reasoning_effort 六档、断连恢复、超长输入 10k-500k、多模态、20 连稳定性、非流式全矩阵、参数边界——修复后**未再发现插件缺陷**。
  - **宿主侧待决（插件外）**：`retry.request-retry=30` 把插件返回的任何 400 当「换账号」，遍历全池后自合成 503；修法在宿主 `config.yaml` 的 `request-scoped-errors` + `action:"stop"`，**未擅自改动**。
  - **workbuddy 侧核实**：子代理报告的 low-credit 拉锯（re-enabled 20 点 203 次→23 点 1 次，拐点对应 20:33 的 0.21.13）、探活回放不可信模型名（6h 命中 0）、11128 扇出全池——均由其他会话修复；workbuddy-ai 的 403/11140 是账号封禁非代码缺陷。
  - **本轮的两次自我纠错**：① 推翻「traework 长列表任务弱化 max_tokens」的错误归因（实为 `kimi-k3` 模型自身忽略该参数，插件已用单测证明 payload 原样传入）；② 承认 feed 中我测试时段的高失败率是**我自己造的**，不是插件问题。


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

- 会话（2026-11-07 14:15 及更早，已沉淀归档）：**qoderwork 16 个 / traework 12 个被删账号全量找回**——恢复源为 `/root/traework-recovery/PRE_RESTORE_auth_store_20261025_134113.sql`（全表 dump，跨 provider 兜底）；决定性判据是**凭据家族**：`dt-/drt-`（OAuth device，drt- 约 1 年）可恢复，`jt-/jrt-`（PAT，jrt- 仅 48h）超期即不可救。写回顺序 auth_store → spool 并做三方数量对齐，还原点三层（表 dump + spool 快照 + 数量基线）。详见项目 skill `project-cpa-workbuddy-plugin-account-data-recovery-rules`。
- 会话（2026-11-07 13:40 及更早）：qoderwork 0.13.2 签到核验 + 修 3 缺陷（`b43d089`→`32df5df`）；0.13.1 修「推理各种失败、断开」（`af216de`→`020313e`→`6c7d253`）；0.13.0 面板批量导入（`dfaecb6`→`8c41737`）；2026-10-31 宿主 management.html 自动升级致 10 插件面板集体 401（`79b581e`+`368b4f3`，根因是 key 混淆从 `enc::v1::` 换成 `enc::v2::`）；2026-10-31 workbuddy 0.21.8 高峰期拥堵识别（`failureScopeUpstreamBusy` / `failureScopeModelRateLimit`）；2026-10-29 deepseek-v4.1-flash 16 点后变慢根因（只读，双因：快号单次 429 后禁用 + 空操作配置保存清空 session-affinity）；2026-10-26 traework 0.5.3 面板改纯读积分快照。细节见 `PROJECT_HISTORY.md` 与 `doc/`。