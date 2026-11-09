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


- 会话（2026-11-10 00:20-00:40，接续上条）：**上游抖动定量证明（同账号 7 秒内 TTFT 跳变 44.8 倍）+ 慢速冷却机制评估**：
  - **抖动定量（20w 输入，783 对同账号相邻请求）**：TTFT 跳变倍数 **p90=6.4x、p99=13.4x、max=44.8x**，≥5x 的对数占 **13.7%**。最极端：`2513ms → 112548ms`（间隔仅 **7 秒**，44.8 倍）。这是上游突发排队的铁证。
  - **时间序列样本（账号 460889140028，3 分钟内）**：`3474 → 8489 → 3754 → 36255 → 4210 → 8901 → 5201 → 4915 → 3814 → 3610 → 8306 → 27506 → 3344 → 25494`，无任何可预测规律。
  - **慢速冷却机制评估**：插件已有 `slow_account.go`（默认开启，2.5 倍阈值 / 15 分钟窗口 / 5 样本 / 3000ms 绝对下限 / 池 ≥10 / P25 基准）。**24h 生产仅触发 1 次**（median=14154ms baseline=4490ms ratio=3.2x），且因防抽干立即释放。原因：判据要求「账号持续慢」，而上游抖动是「随机单次」—— 跳到 112 秒后下一次回落 2.9 秒，中位数被快速拉平。**这是设计边界，不是缺陷**。
  - **超时换号重试收益模拟**：若 TTFT 超 15s 换号（840 样本），11.1% 的请求触发，其中 **82.8% 得到改善**（p50 从 33702ms 降到 18997ms）。但代价是这 11.1% 必然多付 15 秒，且换号后新账号仍是同分布抽样（p50 4s/p90 20s），对原本 20 秒的请求反而变差；同时增加 11% 上游调用量。**决定不实施**。
  - **本轮最终结论**：上游 prefill 存在不可预测的突发排队，是首字延迟长尾的唯一根因。插件侧可优化点已全部落地（首字闸门 1.2s、错误即时化、prefill 原样透传）。若需进一步改善只能减少 input 长度（客户端侧决策）。
  - **文档**：诊断文档扩至 15 节（26,989 字节）。

- 会话（2026-11-10 00:00-00:20，接续上条）：**完整归因收敛——prefill 占 59.3%，生成速度与 workbuddy 仅差 7%**：
  - **流全程耗时（近 3h，配对 1879 条 scheduled→done）**：p50 **9.0s**、p90 **40.0s**、max **558s**；≥10s 占 48.3%、≥20s 占 **26.8%**、≥60s 占 **5.4%**。直接对应「生成时间 100s+」反馈。
  - **贡献拆解（近 12h，n=1991）**：**prefill 占 59.3%**（p50 5628ms / p90 34236ms），生成占 40.7%（p50 2342ms / p90 16918ms）。
  - **生成速度对照（决定性）**：traework **168.1 tok/s** vs workbuddy **181.5 tok/s**，**仅差 7%** ⇒ 生成阶段不是问题。各模型中 `glm-5.3` 的 p10 仅 44.2 tok/s，是长尾生成慢的主要来源。
  - **长耗时流（≥60s，5.5%）构成**：prefill p50 37s + 生成 p50 35s，output p50 1467 token —— 两阶段都大。
  - **新增排除假设 2 类**：① 个别账号质量问题 —— 12 个账号 TTFT p50 都在 4~6s、慢占比 6.9%~27.8%，全池共性；② reasoning 拖慢首字 —— 各 reasoning 档位慢请求占比稳定 14~16%，无相关性。累计已排除 **8 类**假设。
  - **最终归因三层**：① 超长 input 的 prefill 成本（59.3%，input p50 44.3 万、p90 75 万，workbuddy 无 400k+ 负载）② 长输出流生成耗时（40.7%，output p50 1467）③ 上游 prefill 长尾抖动（同账号同 input 下 TTFT 2.8s~52s 随机跳变）。
  - **插件侧已无可压缩空间**：首字闸门封顶 1.2s（实测 p90=1075ms）、错误处理即时化（审核失败 1m26s→4.7s）、prefill 原样透传。
  - **剩余唯一方向（需客户端配合）**：降低上下文上限（80 万→20~30 万，TTFT p50 可从 7.7s 降到 4.5s）或裁剪对话历史。**属产品决策，不是插件缺陷**。
  - **文档**：诊断文档扩至 14 节（22,609 字节）。

- 会话（2026-11-09 23:45-00:05，接续上条）：**专项分析「20w 输入 / 500 输出 / 首字 20s+」——瓶颈 100% 在 prefill，workbuddy 无 400k+ 负载**：
  - **该画像实测（近 24h，input 150k-250k 且 output ≤1000，n=605）**：TTFT p50 4035ms、**p90 24226ms**、**max 113341ms**，TTFT≥20s 占比 **10.6%**（64/605）。
  - **关键判读**：慢样本的 `latency - ttft` 仅 0.9~4.9 秒（如 113341ms TTFT 但生成仅 4871ms），说明**生成阶段完全正常，瓶颈 100% 在 prefill**。
  - **TTFT 随 input 单调递增**：0-50k → 1385ms；200k-300k → 4501ms；500k+ → 7736ms（p90 16404ms）。prefill 是线性成本。
  - **与 workbuddy 的决定性对照**：同规模输入下 traework 只慢 **1.36~1.38 倍**（100k-200k: 3660 vs 2693ms；200k-400k: 4950 vs 3581ms），但 **workbuddy 完全没有 400k+ 的请求**，而 traework 有 **6928 条（占比 58%）**。⇒ 用户感受到的「trae 慢很多」主因是 **traework 承载了 workbuddy 完全没有的超长上下文负载**，而非插件链路慢。
  - **input 分布（n=12022）**：p50 **443218**、p90 **750291**、max 815669，**无超过 100 万的请求**（客户端已按上限截断到 ~80 万）。
  - **已排除的优化路径**：① prompt caching —— 24h/12023 条样本的 `cached_tokens`/`cache_read_tokens`/`cache_creation_tokens` **全部为 0**，上游不提供；② 插件侧截断 —— `buildTraePayload` 原样透传，主动截断会破坏对话完整性；③ 首字闸门 —— 已封顶 1200ms（实测 p90=1075ms）；④ 并发排队 —— 已实证无关。
  - **剩余唯一可行方向**：缩短 input 长度，需客户端（DSH）侧配合把上下文上限从 ~80 万降到 20~30 万，可让 TTFT p50 从 7.7s 降到 4.5s。**属产品决策，不是插件缺陷**。
  - **文档**：诊断文档扩至 13 节（18,395 字节）。

- 会话（2026-11-09 23:25-23:45，接续上条）：**缺陷十「内容审核失败客户端仍收 200」（traework 0.5.34）**：
  - **修复效果确认（0.5.31 生效）**：`glm-5.3-flashx` 审核失败耗时从 **1m26s/1m34s 降到 4.7s/2.2s**，因 0.5.31 让插件在 done 时立即识别审核原因并终止，不再等完整生成。
  - **新缺陷十**：但客户端仍收到 **HTTP 200**（`gin_logger` 记录 `200 | 10.02s | POST /v1/responses`），无法区分「正常完成」与「被审核截断」。根因：流式错误信封 `marshalStreamErrorEnvelope` 只带 `error` 字段、**不带 `http_status`**，宿主 `statusCodeFromError` 得 0，客户端按成功收尾。
  - **修复（0.5.34）**：新增 `isContentModerationFailure`（认 `sensitive content` 与 `content error finish reason`），`envelopeHTTPStatusForError` 据此返回 **400**。宿主 `IsRequestFault(400)` 判定为请求级故障，既不冷却凭据也不跨凭据轮转 —— 这类失败与账号健康度无关，同一段对话换号必然重演。
  - **验证**：新增 `content_moderation_test.go` 两组用例（7 条识别边界含配额/渠道/500 反例、状态码映射）；变异移除该分支后 `TestEnvelopeHTTPStatusForContentModeration` 失败。
  - **部署验收**：`hot reloaded 0.5.33→0.5.34`（23:39:32），落盘 `.so` sha256 `31dd3b16…` 与 release zip 逐字节一致。
  - **闸门数据持续稳定**：54 条样本 `gate_ms` p50=0 / p90=1075ms，全部被 1200ms 上限压住。

- 会话（2026-11-09 23:05-23:25，接续上条）：**闸门延迟实测验收 + 观测点修正（traework 0.5.33）**：
  - **路径核实（重要修正）**：生产流量**全部走 `exec stream async`**（2h 内 1289 次），该路径直接调用 `pumpTraeStreamAttempt`，因此 0.5.32 的闸门修复**对生产有效**；但 0.5.32 把观测日志加在 `pumpTraeStream` 包装函数里（生产不经过它），导致日志一条都没有。0.5.33 把观测点移到 `executor.go` 的 async 循环内。
  - **闸门机制实证**：生产 2h async done 样本 1289 条，`chunks` p50 仅 **34**（p10=8 / p90=222），即典型输出约 136 字符，**远达不到 600 门槛** —— 闸门在多数请求里全程不开，分片一直留在 pending 直到 done 才统一释放。这是首字被拖后的直接机制。
  - **修复效果实测（0.5.33 部署后 8 分钟，16 条样本）**：`gate_ms` **p50=0 / p90=1015 / max=1026**，全部被 1200ms 上限压住。`gate_ms=0` 是字符数够快立即打开；`gate_ms≈1015` 正是上限生效强制放行。修复前该值等于「攒满 600 字符所需时间」，可能达数十秒。
  - **5 个目标模型现状（近 12h，失败率均 <1.2%）**：deepseek-v4.1-flash n=512 fail 1.2% ttft50 5006ms；glm-5.3-flashx n=394 fail 0.8% ttft50 5387ms；minimax-m3.1-flash n=323 fail 0.9% ttft50 6161ms；glm-5.3 n=207 fail 0.5% ttft50 5571ms；DeepSeek-V4-Pro-Official n=75 fail 0.0% ttft50 6391ms。
  - **部署验收**：`hot reloaded 0.5.32→0.5.33`（23:16:53），落盘 `.so` sha256 `922b145b…` 与 release zip 逐字节一致。

- 会话（2026-11-09 22:45-23:10，接续上条）：**缺陷九「首字闸门无时间上限致长任务首帧被拖后」（traework 0.5.32）**：
  - **用户核心诉求**：traework 首字延迟高。本轮定位到插件侧一个此前未发现的可干预点。
  - **根因**：`pump` 路径的首字闸门按**字符数**攒够 `pseudoCompletionMaxChars(600)` 才把缓冲分片下发给客户端。0.5.29 把伪完成阈值从 200 提到 20000 后，`gateOpen := ctx.InputChars < 20000` 使 **99.1%** 的真实请求（input ≥ 5000 token）全部走闸门。
  - **量化证据**：闸门组 TTFT p50 **5630ms**（n=1103）vs 直通组 **1866ms**（n=10），相差 **3 倍**。对照 workbuddy：**完全没有首字闸门**（grep `gateOpen|pending` 零命中），其 TTFT p50 仅 2526ms。
  - **修复（0.5.32）**：新增 `pseudoGateMaxHold = 1200ms`，闸门改为**双条件** —— 攒够健康字符 **或** 持有超上限，任一满足即放行。伪完成防护让位于首字体感：「已生成内容被回滚」可接受，「长时间零输出」不可接受。
  - **可观测性**：`traeStreamAttemptResult` 新增 `GateOpenedAt`，成功路径输出 `stream pump gate delay: gate_ms=… ttft_ms=…`，可直接量出闸门给用户感知首字带来的额外延迟。
  - **验证**：新增 `gate_hold_test.go` 三组用例（上限取值合理性 500ms~2s 区间、持有超时放行、未超时仍按字符判定且伪完成零泄漏）；变异把 `heldTooLong` 置 false 后 `TestPumpGateOpensOnHoldTimeout` 失败。
  - **部署验收**：`hot reloaded 0.5.31→0.5.32`（23:01:03），落盘 `.so` sha256 `6382f250…` 与 release zip 逐字节一致。部署后 12 分钟错误指标全零；闸门延迟样本因夜间低谷暂无真实请求，下轮补采。

- 会话（2026-11-09 22:10-22:40，接续上条）：**缺陷八「done 事件终止原因被丢弃致内容审核失败无标记」（traework 0.5.31）**：
  - **现象**：`glm-5.3-flashx` 两次请求耗时 **1m26s / 1m34s**、生成 **2946 个分片**后才报 `upstream 200: sensitive content error finish reason: sensitive`，且客户端收到 **HTTP 200**（被截断但无错误标记）。
  - **根因**：上游把终止原因放在 `done` 事件的 **data** 里，而三条扫描路径（collect / aggregate / pump）的 `case "done"` 分支都只调 `terminal.recordDone()` 后立即返回，**完全不解析 payload** —— 审核失败被当业务完整收尾。
  - **修复（0.5.31）**：新增 `doneTerminalError`，在 `recordDone()` 之前解析 done 的 data；命中 `sensitive` / `content error` 时返回 `upstream <status>: <原因>` 错误。三条路径统一接入（pump 路径用 `ctx.StatusCode`）。判据方向为「宁可放过」——只认内容审核关键词，其余未知 payload 按正常完成处理。
  - **验证**：新增 `done_terminal_test.go` 两组用例（11 条识别边界含 7 种正常完成形态、脱敏截断）；变异使 `doneTerminalError` 恒返回空串后 4 条用例失败。
  - **部署验收**：`hot reloaded 0.5.30→0.5.31`（22:35:50），落盘 `.so` sha256 `fc538b6b…` 与 release zip 逐字节一致。部署后 10 分钟：上游失败 **0**、内容审核 0、探活失败 0、11128 0、换号耗尽 0、伪完成重试 0。
  - **发布踩坑（复现第 2 次）**：CDN 缓存导致 `plugin-store install` 连续 6 次返回旧版本（`{"status":"installed"}` 但无版本号）；等待约 5 分钟缓存过期后第 7 次拿到 0.5.31。**教训：install 返回不带目标版本号即视为未生效，需等待后重试，不能凭 status=installed 判定成功。**
  - **本轮失败率基线（近 6h）**：workbuddy 0.26%（2/757）、traework 1.39%（7/505），其中 traework 剩余失败为本次修复的内容审核 4 条 + 零星 504。

