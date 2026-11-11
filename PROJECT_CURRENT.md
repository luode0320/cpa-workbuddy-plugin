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


- 会话（2026-11-11 22:26-22:57）：**qoderwork 导入弹窗改为只认凭据 JSON，删除 PAT 全部逻辑（0.13.16，未发布）**：
  - **用户诉求**：生产面板截图红框指出导入弹窗仍要 `pt-` PAT，「改成只要选择或者输入 JSON 就导入就可以了，不需要 PAT 的这些逻辑」。
  - **前端收敛**：弹窗从「`#importPAT` 单条输入 + 批量区块 + 底部单 PAT 按钮」三层压成「选文件行 + 一个大文本框 + 唯一按钮『导入并保存』」。删 `importAuth` / `runBatchImport` / `parseBatchInput` / `describeBatchItems` / `isPATToken` / `credentialPATToken` / `PAT_RE` / `BATCH_GAP_MS` / `.import-batch` 样式；新增 `parseImportInput` / `describeImportItems` / `runImport`，全部输入统一走 `POST /import-cred` 离线回灌。
  - **后端同步**：删 `handleImportPAT`（67 行）、`POST /import` 路由注册与 `mutatingManagementPath` 条目；`failover_disable_test.go` 白名单审计同步删除 `handleImportPAT` 条目。
  - **保留**：嵌套/平铺凭据、凭据数组、`{accounts:[{credential}]}` 导出包装、JSONL、多文件多选；逐条串行、单项失败不中断、失败明细常驻、部分成功后收敛为仅失败项、批内去重。auth 文件 `personalToken` 字段与 `jrt-` 刷新兜底链路未动。
  - **验证**：解析层 16 条 + E2E 26 条断言全绿；**5 组变异 4 KILLED + 1 等价变异（记明原因）**；`cgo-shim-build.py qoderwork` build/vet/test 全绿；无头 Chrome 本地渲染截图确认弹窗结构符合预期。
  - **文档**：`doc/5-tests/2026-11-11_225600_...验证.md`、`doc/6-review/2026-11-11_225600_..._6-review.md`（STYLE: PASS）、需求主文档追加第 6 节「口径收敛」。
  - **未做**：未提交 git、未发布、未部署生产；`registry.json` 仍是 0.13.15。

- 会话（2026-11-10 21:20-21:50，接续上条）：**决定性发现——生产流量走宿主桥，插件连接池不参与**：
  - **核心洞察**：0.5.40 把连接池扩到 `MaxIdleConns=200 / PerHost=64` 后 TTFT 未见改善。追查调用链发现生产流式请求走 `hostHTTPDoStream` → **宿主 `host.stream.do` RPC**，完全绕过插件的 `streamHTTPClient`。近 15 分钟日志中 `stream bridge / direct fallback / bridge unavailable` 降级关键字 **0 次**，确认生产 **100% 走宿主桥**。
  - **结论**：插件的连接池配置对生产流量**无效**。连接池在宿主进程内，插件无权配置；宿主 `config.yaml` 也无相关可调项。这解释了 0.5.40 扩容后无改善。
  - **宿主桥固有开销**：`bridgedRead` 每读一个分片都要分配 channel + 启动 goroutine + 创建 timer + 一次 cgo RPC。2946 分片的流 = 2946 次这套开销。**但这是流式读取阶段（首字之后），不直接影响 TTFT**。
  - **新增对照开关（0.5.41）**：`stream_direct_forced`（默认 false）—— true 时走插件直连，用插件自己的连接池。运维可做 A/B 对照定位延迟来源。`stream_direct_test.go` 锁定默认值必须为 false（防止误改生产路径）。
  - **累计排除 12 类假设**（新增：宿主桥开销）。唯一剩下的解释是**上游 Trae 的 prefill 时间**，三层证据：① 时段分层（深夜 1.6% vs 晚间 18.1%，workbuddy 全时段 0%）② 同账号 7 秒内 TTFT 跳变 44.8 倍 ③ prefill 占 59.3%。
  - **部署验收**：0.5.41 `hot reloaded 0.5.40→0.5.41`（21:42:32），sha256 `712fd0c0…`。
  - **文档**：诊断文档扩至 20 节（42,447 字节）。

- 会话（2026-11-10 20:43-21:20，接续上条）：**用户明确「首字延迟高是主要问题」→ 验证连接池与 DNS 两个新假设**：
  - **假设 A：连接池容量不足**。实证（近 6h，228 条）：同分钟并发 **1-5 → 慢20s% 9.9%**、6-15 → 10.0%、**16-30 → 26.5%**、30+ → 18.8%，呈正相关。原配置 `MaxIdleConns=20 / MaxIdleConnsPerHost=5`，生产 28 个账号并发时会频繁新建连接（TCP+TLS 握手各一次）。
  - **修复（0.5.40）**：`MaxIdleConns` 20→**200**、`MaxIdleConnsPerHost` 5→**64**（shared 与 stream 两个 client 同步）。新增 `conn_pool_test.go` 断言 ≥28；变异改回 5/20 后测试失败。
  - **部署后验证（90 条样本）**：部署前 n=816 ttft50 9108ms / 慢20s% 20.3%；部署后 n=90 ttft50 10168ms / 慢20s% 24.4%。**未见改善** ⇒ 连接池不是主因，保留该改动（无害且理论上正确）但不作为主要优化方向。
  - **假设 B：DNS 解析慢**。容器 `/etc/resolv.conf` 为 `nameserver 1.1.1.1`；宿主机为 `1.1.1.1 + 8.8.8.8`，**两个都是境外地址**。Go 的 `http.Transport` 会缓存 DNS，仅在新建连接时重解析，影响面有限且属基础设施层，非插件缺陷。
  - **累计已排除 11 类假设**：插件链路、payload 冗余、流式节流、并发排队、prompt cache、账号质量、reasoning、生成速度、max_tokens、首字闸门（已修）、连接池（本轮）。
  - **确认的主因（插件不可干预）**：① Trae 上游晚间容量排队（晚间 18.1% vs 深夜 1.6%，workbuddy 全时段 0%）② 超长 input 的 prefill 成本（占 59.3%，input p50 44.3 万）③ 上游随机突发抖动（同账号 7 秒内跳变 44.8 倍）。
  - **插件侧可干预点已全部落地并验证**：首字闸门 1.2s（实测占 TTFT 2.3%）、伪完成阈值（失败率 26.5%→0%）、done 终止识别（1m26s→4.7s）、内容审核终局、积分耗尽不探活、轮换 serve 5 分钟（已触发）、连接池扩容。
  - **部署验收**：0.5.40 `hot reloaded 0.5.39→0.5.40`（21:13:31），sha256 `b08f55e6…`。
  - **文档**：诊断文档扩至 19 节（39,215 字节），含 11 类假设排除表与连接池/DNS 实证数据。

- 会话（2026-11-10 20:36-21:30）：**DSH 客户端 `@` 文件引用段触发 11128 根因定位与修复（workbuddy 0.21.23）**：
  - **问题**：DSH（DeepSeek-Harness）客户端接入 CN 账号池后每轮第一条消息稳定 `400 code 11128`，既有品牌词改写覆盖不到。
  - **真机消融（真实 CN 账号直连上游，唯一变量是消息体）**：system 含 `FILE_REFERENCE_PROMPT` → 400；**assistant 含同一段 → 400**；**user 含同一段 → 200**；仅把锚点 `Tokens prefixed with @` 换成 `(at)` → 200（整段全换等效）；拆词 `with→by` → 200；身份句连字符化 → 200、前置一行文字 → 200（位置锚定）。
  - **社区结论校正**：讨论帖 v0.1.5 的 `You are a coding agent powered by ...` 与 Web GUI 句式，在当前上游复测（开头/中段/尾部/单句）**全部 200**，故不引入改写。
  - **修复**：`keyword_neutralize.go` 新增 `dshFileReferenceAnchorRe` 与 `scannedFingerprintRoles`（system/developer/assistant），`neutralizeForRole` 按角色分派；user/tool 一字不动；品牌词与 Claude Code 模板规则维持全角色（不回退）。新增开关 `dsh_fingerprint_neutralize`（默认开）。
  - **验证**：cgo-shim build/vet/test 全绿；新增 `dsh_fingerprint_test.go` 8 组用例；CI Release success（5 平台，缺 darwin_arm64）；生产 `install` 热重载（`superseded by newer instance`），落盘 sha256 `af527430…` 与 zip 一致。
  - **生产行为验收（决定性）**：同样的 body，直连上游 400/11128，经生产 `/v1/chat/completions` **200**（system 含 @ 段、assistant 含 @ 段、零宽身份句三组全过），验收窗口 11128 计数 0。
  - **发布踩坑复现**：registry 提交后宿主侧 CDN 边缘滞后，连续 2 次 `direct plugin version "0.21.23" not found`，第 3 次（约 3 分钟）成功 —— 与 0.5.31 的 6 次重试同源，勿误判为未发布。
  - **文档**：`doc/4-bugs/2026-11-10_213000_DSH客户端@文件引用段触发11128根因与修复.md`。

- 会话（2026-11-10 01:00-01:50，接续上条）：**轮换失效最终定位（三层根因）与修复（0.5.38/0.5.39）**：
  - **问题演进**：0.5.36 serve 60→10 分钟仍无让位；0.5.37 加 `rotation approaching`（serve 80% 打点）**一条都没出现**；0.5.38 serve 10→5 分钟仍无日志；**0.5.39 把诊断日志改为每 30 秒输出 `served` 真值后定位成功**。
  - **诊断日志暴露的真相**：`served=0s → 32s → 1m3s → 1m34s → 2m7s → 2m39s → 3m9s → 3m40s` —— **计时正常推进、无重置**。
  - **让位成功触发**：`2026/11/10 01:36:10 rotation: account served 5m0s continuously, cooling down for 10m0s: auth=traework-1958706035295120.json`。
  - **三层根因**：① serve=60 分钟过长（单号可连续 351 请求/29 分钟）② **宿主 selector 自然切换周期 6~7 分钟**（账号 `03c41d2b` 跨度 6.4 分钟、`ffa5a722` 跨度 7.3 分钟就被换走），serve=10 分钟永远等不到 ③ serve=5 分钟后成功触发（5 < 6~7，能在宿主换走前主动让位）。
  - **诊断方法论沉淀（重要）**：排查「机制不生效」时**阈值附近打点无效** —— 无法区分「计时没推进」/「计时被重置」/「判定失败」三种情况。正确做法是**周期性输出核心变量真值**（本案例每 30 秒一条 `served`/`serve`/`enabled`），三个值同屏即可定位。
  - **最终参数**：serve **5 分钟** / cooldown 10 分钟 / idle 3 分钟。
  - **部署验收**：0.5.38 `hot reloaded`（01:25:45）sha256 `01a5cb3e…` 一致；0.5.39 `hot reloaded` 并产出诊断日志。
  - **文档**：诊断文档扩至 18 节（35,545 字节），含问题演进表、诊断日志原文、三层根因链、诊断方法论。

- 会话（2026-11-10 00:20-01:00，接续上条）：**用户新反馈「轮询没生效，一直用一个号」→ 缺陷十一/十二**：
  - **缺陷十一（0.5.35）积分耗尽账号仍被探活**：23:20 生产实测 14 个已配额耗尽账号在 2 分钟内被探活各 2~3 次，全部返回 `exceeded the quota`。根因 `doFetchOne` 末尾无条件调用 `triggerActivePing`。修复：抽出 `shouldActivePingForCredits`（`cr != nil && cr.TotalRemain > 0`）。
  - **缺陷十二（0.5.36）轮换参数过长致「一直用一个号」**：24h 生产实证 —— 轮换让位**仅 4 次**、每次都是连续使用满 `1h0m0s` 才触发；最长连续段 `3735516826058092` **351 请求 / 29 分钟**全压一个号；主号占比 **21%~35%**（理想 1/13≈7.7%）。根因 `defaultRotationServeMinutes = 60`。修复：serve/cooldown **60→10 分钟**、idle **10→3 分钟**（idle 必须显著小于 serve，否则「中断重算」与「已服务满」判据冲突）。
  - **修复验证（关键）**：0.5.36 部署（00:40:39）后，每 5 分钟窗口的活跃账号数从 **1 个**（00:22）提升到 **6 个**（00:44~00:50）；近 30 分钟 **11 个不同 auth_hash** 在用（33.2%/31.7%/16.3%/9.7%...）。
  - **宿主选号路径澄清**：插件 `scheduler` 已注册（`Scheduler: true`），但生产日志无 scheduler 调用记录 —— 宿主选号不走插件 scheduler。轮换冷却通过 `scheduler.go`/`active_auth.go` 的过滤点生效，实际分散由宿主自身 selector + 插件禁用/冷却状态共同决定。
  - **新增测试**：`active_ping_exhausted_test.go`（5 条边界）、`rotation_defaults_test.go`（锁定 serve=10/cooldown=10/idle=3 及相互关系 serve≤15、cooldown≤2×serve、idle<serve）；同步修正 `usage_rotation_test.go`（改引用常量）与 `panel_rotation_badge_test.go`（时间线匹配 idle=3）。两处变异验证均被捕获。
  - **部署验收**：0.5.35 `hot reloaded 0.5.34→0.5.35`；0.5.36 `hot reloaded 0.5.35→0.5.36`（00:40:39），落盘 `.so` sha256 `14d83c49…` 与 release zip 逐字节一致。
  - **文档**：诊断文档扩至 17 节（33,328 字节）。

- 会话（2026-11-10 00:00-00:20，接续上条）：**时段分层证明 Trae 上游晚间容量排队（决定性证据）**：
  - **20w 输入分时段 TTFT**：深夜 02:00 慢请求 **0.0%**、05:00 **1.6%**、03:00 2.6%；晚间 21:00 **32.3%**、22:00 **20.8%**、23:00 12.0%。同一 input 量级，时段差异达 20 倍。
  - **全池分时段对照（决定性）**：traework 20:00 慢请求 **18.1%**、21:00 12.9%、22:00 17.7%、23:00 14.5%；而 **workbuddy 在所有时段（含 20:00 同刻）慢请求均为 0.0%**。这排除了「客户端负载/时段因素」（同时段 workbuddy 完全正常）与「本机资源竞争」（两插件共用同进程与网络出口）⇒ **唯一解释是 Trae 上游自身容量排队**。
  - **max_tokens 默认值假设排除**：`streamDefaultMaxTokens=128000`（客户端未传时补），实测 6h/2222 条 output_tokens：p50 382、p90 1645、p99 7680、max 16000，**≥100000 占比 0.00%**，该默认值不构成 prefill 负担。
  - **payload 冗余假设排除**：`toTraeMessages` 只做格式转换（字符串→parts 数组）不改变内容长度；`buildTraePayload` 只加标量字段，不注入固定 system prompt。
  - **最终归因四层**：① Trae 上游晚间容量排队（晚间 18.1% vs 深夜 1.6%）② 超长 input 的 prefill 成本（占 59.3%）③ 上游随机突发抖动（同账号 7 秒内跳变 44.8x）④ 长输出流生成耗时（占 40.7%）。**四层全部无法由插件干预**。
  - **可操作建议（不涉及插件改动）**：① 错峰使用（深夜慢请求 1.6% vs 晚间 18%）② 降低上下文上限 80 万→20~30 万 ③ 对长任务设合理预期（20 万 input 的 TTFT 合理区间为深夜 3~6 秒、晚间高峰 30 秒）。
  - **文档**：诊断文档扩至 16 节（30,690 字节）。
  - **宿主重启影响**：本轮开始时宿主重启导致一次工具调用结果未知，核实工作树干净、无未提交改动、后台任务已清空后，重跑只读分析脚本恢复。

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

