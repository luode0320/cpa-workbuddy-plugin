# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 原生插件套件 `cpa-workbuddy-plugin`（涵盖 10 大 AI 服务商 Provider + 1 统一用量统计插件，共 12 插件），提供多账号管理、动态模型发现、流式转发、专属现代管理控制台、扫码/Token 登录、保号巡检与统一 token 用量统计分析服务。
- 范围：仓库内 12 大插件体系的代码实现、单元测试、资产构建、发布流水线、registry 元数据同步等：
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
  11. `workbuddy-token-usage`：统一 Token 用量统计与实时看板服务；
  12. `doubao-provider`：豆包工作模式（agent_mode=1 PC 客户端管线）+ 网页链路兼容别名 `doubao/web`（agent_mode=2，供未投放工作模式的账号使用，无法指定具体模型）；DeepSeek V4 / Kimi K3 / GLM 5.3 直连；书签一键授权（采集 `s_v_web_id` 作真实 `fp`）/ Cookie 导入 + device_id 派生；面板测试只下发插件别名目录。
- 非范围：CLIProxyAPI 核心服务本体；跨项目文件修改。
- 已下线插件：`gemini-provider`（2026-10-11）、`notion-provider`（2026-10-22）。**`doubao-provider` 2026-10-22 下线、2026-11-14 以工作模式 0.1.7 重新上线；0.1.8 面板重写与导入导出、0.1.9 书签一键授权、0.1.11 测试模型改别名目录 + conversation_mode 改整数、0.1.19 出站 session 组过滤（后被 0.1.21 撤销）、0.1.20 错误码文案改写、0.1.21 真实设备指纹 `s_v_web_id` 采集、0.1.22 新增网页链路别名 `doubao/web`、0.1.31~0.1.38 多账号会话粘性/拒答态/封面图/msToken 自动续期/空闲保活、0.1.39 吸收 ChinaCPA 视频生成逻辑 —— 均已上线并热重载，生产 `active_version=0.1.39`（2026-11-25）。**

## 项目概况

- **当前会话（2026-11-28 14:20 - 18:00）：traework-provider 0.5.63：定位并修复「推理请求很慢」根因（open 看门狗超时后重发上游请求），已上线**（用户：「有些推理请求很慢, 我们直接用trae的客户端时没有这个问题」→「几乎到了无法使用的地步了」→「也没有排队」）：
  - **现象**：TTFT 锐利集中 33~36s（台阶率 8.97%，`[30000,31000)` 桶恰好 0 命中，残差 p50=4.35s ≈ 一次直连耗时）。
  - **排除项（均实测）**：① 上游慢——绕过插件与 CPA 直测 `llm_utils_chat`（同账号同 body、每次新建连接）650KB p50=11.35s max=40.17s、1500KB max=79.98s、2400KB max=101.22s，**上游有 40~101s 随机长尾但与 body 无单调关系**；② 桥接开销——小 body(24KB) 桥/直连等价(2.10s vs 2.14s)、大 body(1500KB) 桥反而更快(0.42x)；③ 连接池——冷连接反而更快(0.61x/0.24x)；④ session 缓存——`session_id` 复用 1.09x；⑤ 上游槽位排队——台阶样本 **98.4% 在同账号完全空闲时**；⑥ 伪完成重试 0 次；⑦ 账号质量——17 账号 p90 全部 34~37s（全局机制）。
  - **根因**：宿主 `host.http.do_stream` 同步阻塞到上游响应头才返回 `stream_id`（`internal/pluginhost/http_bridge.go` 的 `doHTTP → client.Do`），宿主侧 `ResponseHeaderTimeout` 与 `client.Timeout` **双为 0**，open 可无限等。插件 `hostBridgeOpenTimeout` 是唯一兜底，**旧降级路径超时后放弃宿主侧仍在等待的 open 并重发一次完整上游请求（body 1.2~2.5MB 全量重传）**。生产 8/8 降级事件逐条满足 `ttft ≈ 预算 + 第二次直连耗时`（120000ms + 3547~8053ms）⇒ 同一逻辑请求在上游变两份。**台阶随预算移动**（30s→33~36s；120s→123~126s）是决定性判据。
  - **修复**：0.5.62 看门狗 30s→120s + 补显式降级日志（原完全静默）；0.5.63 区分两类失败——**看门狗超时如实返回错误、不重发**（交 executor 换号），**非超时桥故障仍降级直连**（只发一次不放大），默认预算 120s→180s（高于上游尾部 101s，只兜永久挂起）；新增配置项 `host_bridge_open_timeout_seconds`。
  - **验证**：`cgo-shim-build.py traework` 全绿；新增 `host_bridge_open_timeout_test.go`（4 用例）——该看门狗此前在全部 `*_test.go` **零引用**，是 30s 白等长期无人发现的根因；`test/traework/host_stream_timeout_test.go` 同步锁新语义。**生产按部署时刻 09:42:47Z 严格切分：部署前 open 异常 19 条（全为 `elapsed_ms=1200xx budget_ms=120000`），部署后 0 条**；120s 档 6.4%→3.7%，p90 83.8s→43.9s。
  - **发布链**：commit `3a032fa` → CI run 46130705234（traework 5/6 平台 + Release success）→ `publish-assets.py --allow-missing` → registry `2ba396e` → `POST /v0/management/plugin-store/traework-provider/install?version=0.5.63` → `hot reloaded active_version=0.5.63 retired_version=0.5.62`，容器未重启。
  - **教训**：① **install 路由是 `POST /v0/management/plugin-store/{id}/install`**，`/plugin-store/install?id=` 恒 404；② registry 推送后 raw CDN 有延迟，但宿主 store 可能已更新，带 `?version=` 直接装即可；③ `grep`/`awk` 在 PowerShell 双引号 ssh 里会被本地解析，复杂逻辑一律写脚本传上去执行；④ 直测上游复用连接会因 body 未读净产生 `ResponseNotReady: Request-sent`，测响应头须每次新建连接；⑤ `darwin/arm64` 矩阵常态 cancelled，需 `allow_partial=true` 才让 Release 跑起来。

- **当前会话（2026-11-27 22:00 - 23:55）：doubao-provider 0.1.43：工作模式模型改为自动获取并补齐 6 个缺失模型，已上线**（用户贴客户端模型菜单截图，红框 GLM 5.4 / Qwen 4 Max，问「豆包现在的模型是自动获取的了吗? 我们发现有些模型我们没有」→ 选「先翻我电脑上豆包客户端的本地配置」→ 追加「逆向客户端啊」→「从github其他开源项目中找一下」→「继续」）：
  - **逆向链**：客户端 `D:\Doubao` 全目录（含 335MB `Doubao.dll`、190MB `liblark.dll`）搜 `person-*` 零命中，i18n 只有模型代码表（`new_model_menu_model_GLM54_cn`→GLM 5.4、`QWEN4MAX_cn`→Qwen 4 Max）；前端 JS 定位到消费结构 `menu_item_list[].model_item_key`；GitHub 项目 `niyouqianma/doubaowork2api`、`3294798852/doubao-work-gateway` 揭示菜单端点 `/alice/slot/action_bar_v3/brief_list` + 需 `{language_code, bot_id}`，`bot_id` 来自 `/alice/user/launch` 的 `assistant_bot_id`。
  - **根因**：`/alice/basic/launch` 的 `model_list` 67 条只有内部代号（Aoe 等）且**无 model_item_key**；工作模式请求三处必带 key，故只能靠静态表，新模型不会自动出现。实测 4 账号按正确方式调 `brief_list` 全部返回 130KB 含 key，解析出 **16 个工作模式模型**真值与参数。
  - **修复**：新增 `work_model_discovery.go`（菜单实时发现：`fetchAssistantBotID` → `brief_list` → 解析 `entry_list[].active_switch_conf` 的 `menu_conf_v2.mode_list[].model_list`，过滤 `agent_mode=1`；静态优先、按 `model_item_key` 去重、失败退回静态）；补齐 6 条别名 `glm-5.4`/`qwen3.8-max`/`qwen3.8-flash`/`qwen4-max`/`mimo-v2.6-pro`/`mimo-v2.6-flash`（`300k_230`/`300000`，推理档位 5）；新增 4 单测 + `swapWorkBriefEndpoint`/`swapWorkLaunchEndpoint` 端点覆盖点（测试默认指向不可达地址，不打真实上游）。
  - **验证**：门禁 `cgo-shim-build.py doubao` 全绿 101s；哨兵反证两条有效（去掉 `workAliasID` 中文兜底 → `TestWorkAliasIDFallsBackToKey` FAIL；去掉 `allAliases` 去重 → `TestAllAliasesMergesDynamic` FAIL，补了 19 条）；污染扫描 PASS；6-review STYLE: PASS；gofmt 无差异。
  - **发布链**：commit `3c7921a`（7 文件 +788/−7）→ CI run 46027366191（Test(doubao) success、Release success、5 平台、darwin/arm64 cancelled 常态）→ `download-release-assets.py 0.1.43 doubao-provider` ALL CHECKSUMS OK → `publish-assets.py --allow-missing`（5 artifacts）→ registry `02ef3c7` → 远端 raw 200。
  - **生产热重载已生效**：`POST /v0/management/plugin-store/doubao-provider/install?version=0.1.43`（首次报 `direct plugin version not found` 属 registry CDN 延迟，重试成功）→ `hot reloaded active_version=0.1.43 retired_version=0.1.42`；落盘 `.so` sha256 `5b0bc02f1e113bc9e11e48522622ab6f96bbcda34b487c1dc5c65d7e1a46989d` 与本地 zip 逐字节一致；13 插件 `STATE_UNCHANGED`；容器未重启；生产模型目录 **17 条**（`doubao/video` + 16 工作模型），新增 6 个全部可见。
  - **教训**：① 菜单端点必须带 `bot_id`，空 body 只回 130 字节空列表；② 管理 API 路径是 `/v0/management/plugin-store/{id}/install`；③ registry 推送后 install 可能因 CDN 延迟报 version not found，重试即可；④ 测试直连真实上游会让门禁崩溃，须加端点覆盖点。

- **当前会话（2026-11-26 23:14 - 2026-11-27 03:05）：doubao-provider 0.1.42：重新开放工作模式文本模型（路径 D 史实恢复）并上线**（用户 m01077「我们想把豆包工作模式的模型也开放出来使用」→ m02199「探活并进入实施规划」→「开始实现, 实现完成发布上线」）：
  - **决策链**：m02109「通过接口自动获取模型」→ m02154「我们之前的提交有做过的吧? 后来删除了」触发史实核查，推翻 A/B/C 三选项定路径 D（复用 `/alice/basic/launch` 动态发现 + 从 `5707b5d^` 逐符号恢复 + 零新增依赖，不接 Node CLI）。
  - **探活**：生产出口 IP + 真实 Cookie + 0.1.25 历史代码与 goja 真签名，3 账号 × 3 模型均无 `-1`（历史最大障碍在正确网页协议口径下非必然），但出口 IP 持续风控（710022002×2 / 710022004×1），出字铁证待干净网络补取，不阻断实施。
  - **实现（4 生产 + 3 测试文件）**：`protocol.go` 恢复 7 常量 + `generationMode` 七字段 + `atoiSafe`；`completion.go` 恢复 `buildWorkOption`/`workModeExt` + confirm/WorkMode/skill 三分支分流，`GeneralTaskParam` 改 `any` 多态（视频确认轮填 `*generalTaskParamPayload` 逐字节不变，禁 typed-nil）；`models.go` 恢复 10 别名 + 三层解析 + `allAliases` 合并目录；`main.go` 追加 7 项 `work_*` + ConfigFields + **配置解码链**。恢复 `workmode_test.go` 8 个契约测试、修正 7 处目录旧断言、面板测试断言由拒绝改为接受（`default`/`pro` 仍拒绝）。
  - **验证**：门禁 `cgo-shim-build.py doubao` 全绿（85s）+ 三哨兵反证有效（`atoiSafe` 改坏 → `TestAtoiSafe` FAIL；`model_item_key` 改错 → 契约测试 FAIL；去掉工作模式分支 → `TestModeForModel` FAIL）+ 污染扫描 PASS + 6-review STYLE: PASS + gofmt 无差异。
  - **发布链**：commit `060af8e`（12 文件 +1587/−84）→ CI run 45879125483（Test(doubao) success、darwin/arm64 cancelled 常态、Release success、5 平台）→ `download-release-assets.py 0.1.42 doubao-provider` ALL CHECKSUMS OK → `publish-assets.py --allow-missing`（5 artifacts）→ registry `d0a1146` → 远端 raw 200 / 5 artifacts / 逐 asset HEAD 200。
  - **生产热重载已生效**：`POST /v0/management/plugin-store/doubao-provider/install?version=0.1.42&source=source-636fa01121af` → `hot reloaded active_version=0.1.42 retired_version=0.1.41`；落盘 `.so` sha256 `a18f0c477e7b44d3e4b8ee8bc1d1fa6ba1bfcd2e1737174784e2cf036e0246bb` 与本地 zip 逐字节一致；13 插件启用态 `STATE_UNCHANGED`、容器未重启；生产模型目录已见 `doubao/video` + 10 条工作模式模型（`default` 仍 video）。
  - **教训**：① 管理 API 正确路径 `/v0/management/plugin-store/{id}/install`，`/plugin-store/install?id=` 无响应；② `models` 端点需 `auth_index`；③ 版本号先核 registry 与 `main.go`（0.1.40/0.1.41 已被占用，本轮实为 0.1.42）；④ 宿主内存紧张时 Go `link` 进程残留会导致后续 OOM，需先清理；⑤ 远端是 SSH，用 `git push origin main`。

- 状态：活跃维护中。12 大插件全景（11 Provider + 1 用量统计）。**2026-11-14 doubao-provider 以工作模式管线 0.1.7 重新上线，0.1.8 面板重写与账号导入导出、0.1.9 书签一键授权（替代手工粘贴 Cookie）、0.1.11 面板测试模型改插件别名目录并修 conversation_mode 整数类型均已上线并热重载**。
- **生产主机（2026-11-07 起）= 45.207.222.65（`cpa-server`，端口 18998），254（`cpa-server-new`）已弃用**：用户明示「254 已经放弃，改回 65 为主服务器」，全部域名已解析到 65，254 只剩 mysql+nginx（CPA 已停）。部署/验收一律打 65，管理密钥用插件配置 `cpa_management_key`，账号在 `auth-dir=/CLIProxyAPI/mysqlstore/mysqlstore/auths`（62 个）。
- **2026-11-07 01:27 已在 65 完成全量对齐与启用**：11 个插件全部 `configured/registered/enabled=true` —— workbuddy 0.21.9（20 号）/ coze 0.7.4（23 号）/ qoderwork 0.12.6（2 号）/ workbuddy-ai 0.1.35（6 号）/ token-usage 0.2.8 / traework 0.5.7 / qoder-ai 0.1.35 / zcode 0.3.5 / copilot 0.1.12 / qwenwork 0.2.13 / cursor 0.1.7（首次安装）。install 前 5 个显示 `registered=false` 是「该版本 .so 从未加载」（config 里 enabled 本为 true），install 后日志为 `plugin loaded + plugin registered`，非 hot reload 形态。
- **2026-11-07 19:40 当前生产版本（max_tokens 最小干预批次）**：workbuddy-ai **0.1.37** / qoder-ai **0.1.37** / qoderwork **0.13.5** / traework **0.5.13** / zcode **0.3.7** / qwenwork **0.2.15** —— 上述插件 `configured/registered/enabled/effective_enabled` 全 true，`.so` sha256 与 release zip 逐字节一致。**管理 API 端口是 8317（不是 18998，18998 是 sshd）**。
- **2026-11-07 20:33 workbuddy 已升级 0.21.12 → 0.21.13**（失败禁用与手动启用联动，`hot reloaded active_version=0.21.13 retired 0.21.12`，落盘 sha256 `574f6921…` 与本地 zip 一致）。
- **2026-11-07 21:25 三插件同步升级**：workbuddy-ai **0.1.38** / traework **0.5.14** / qoderwork **0.13.7**（同型缺陷整改），三者 `hot reloaded` 且 `.so` sha256 与 release zip 一致。
- **2026-11-07 22:17 workbuddy 升级 0.21.13 → 0.21.14**（P0 分阶段超时 + P1 HostCallbackID 透传，见下条会话摘要）：`hot reloaded active_version=0.21.14 retired_version=0.21.13`，落盘 sha256 `8433a54c…` 与 release zip 逐字节一致；只有 5 平台资产（缺 `darwin_arm64`）。
- **2026-11-08 14:51 traework 升级 0.5.18 → 0.5.19**（下行 usage 帧缺失修复，`hot reloaded active_version=0.5.19 retired 0.5.18`，落盘 sha256 `27d74d19…` 与 release zip 逐字节一致；容器未重启）。同刻 workbuddy 0.21.15 / qoderwork 0.13.12 由并行会话上线。
- 2026-10-25 历史发布批（已过期，仅存档）：zcode 0.3.1 / qoderwork 0.12.1 / traework 0.5.1 / workbuddy 0.21.2 / cursor 0.1.6 / qoder-ai 0.1.31 / coze 0.6.2 / qwenwork 0.2.9 / copilot 0.1.11 / workbuddy-ai 0.1.30 / token-usage 0.2.8（细节见 git log 与 CHANGELOG）。
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-11-26 (GMT+8)

## 活动会话进展摘要

- **当前会话（2026-11-28 22:23 - 23:05）：safeguard-killer 0.1.13：会话绑定泄漏修复 + 未使用功能移除（本地已提交，未发布）**（用户「Safeguard Killer插件, 持续监控一下bug和不需要没使用的功能移除。」）：
  - **巡检手段**：`deadcode`（结论作废：c-shared 插件入口是 `//export cliproxyPluginCall` 而非 `main()`，全量符号报 unreachable 属假象）→ `staticcheck -checks all`（命中 1 条真 U1000 + 1 条 ST1005）→ 生产取证（`docker logs` 6h 无 panic/unknown_method/not_supported/[error]；`/v0/resource/plugins/safeguard-killer/state` 19508 B、60 条决策全 `served`、8 条会话）→ 宿主契约取证（`internal/pluginhost/executor_route.go`、`sdk/api/handlers/handlers_model_router_test.go` 的 `plugin_executor_count_tokens`）。
  - **BUG-1 过期会话绑定无人清理**：`pruneSessions` 自 0.1.7 精简后零调用（staticcheck U1000 直接命中），过期只在「同 key 再次被访问」时由 `groupCandidateOrder` 懒删 ⇒ 不再被访问的会话 key 永久驻留 map（≈200 B/条），且 `/state` 每 10s 全量返给面板。修法：新增 `sessionPruneInterval=5min` 节流 + `pruneSessionsThrottled`，挂到 `bindGroupSession` 写入路径；`buildUIState` 持锁后先 prune，并只回 `IdleExpiresAt` 未过期的绑定（保留 24h `sessionRetention` 供 `buildGroupNotice` 的「上一轮由谁服务」提示）。
  - **BUG-2 成员模型名 count_tokens 被判未知分组**：宿主对 count_tokens 与对话共用 `model.route`，路由链 `resolveTargetGroup` 认成员模型名、计数链 `cfg.group` 只认分组代号。修为 `resolveTargetGroup(cfg, req.Model, "")`。
  - **未使用功能移除**：`goAfterStartupJitter` / `waitStartupJitter` / `startupJitterMax`（本插件无 scheduler、无定时器，三者零调用，staticcheck U1000）+ 两个只测它们的用例；`withModel` 内部重复 `sanitizeRequest`（且用 `currentConfig()` 覆盖本次请求口径）改为只改 `model` 字段。保留 `init()` 内存软上限与 `CPA_PLUGIN_MEMORY_LIMIT_MB` 覆盖（仍必需）。
  - **验证**：门禁 `cgo-shim-build.py safeguard-killer` build/vet/test 全绿（`PASS=58 FAIL=0`，0.28~0.32s）；gofmt CLEAN（CRLF→LF 临时副本）；**4 条哨兵变异逐条 KILLED**（删清理调用 / 清理空实现 / 去状态过滤 / 退回 `cfg.group`）。
  - **登记未修**：`claudeStreams.states` 无 TTL（只靠 `request.complete` 回调清理）、`handleStreamIntercept` 在 `ChunkIndex=-1` 不 flush 残余缓冲（流被截断时丢内容）、`stream.go:79` ST1005 错误串首字母大写（既有对外文本，非必要不改）。
  - **文档**：`doc/4-bugs/2026-11-28_223000_safeguard-killer会话绑定泄漏与count-tokens口径根因与修复.md`、`doc/5-tests/2026-11-28_224500_safeguard-killer0.1.13会话清理与口径收敛代码级验证.md`、`doc/6-review/2026-11-28_230000_safeguard-killer0.1.13_6-review.md`。
  - **教训**：`Split-Path (Split-Path $shim)` 会落到仓库根 `F:\cpa-plugin`，`Remove-Item -Recurse` 差一步删整仓；删临时目录前必须先断言路径匹配 `\\cpa-shim-[a-z0-9_]+$`。

- **当前会话（2026-11-28 21:58 - 22:20）：glm-provider 0.2.6：版本三轨统一 + 移除 glm-web 旧目录（生产已热重载）**（用户「全部实现, 发布生产」）：
  - **起点纠偏**：探查发现插件早已实现（`glm/` 18 个 Go 文件、`cgo-shim` 全绿），真实问题是**四层版本错配**：`main.go`=0.2.5 / `VERSION`=0.2.2 / registry=0.2.2（指向 v0.2.2）/ 生产实跑 `glm-provider-v0.2.5.so`；且 `glm-web/` 22 文件删除未提交、`CHANGELOG` 只到 0.2.0。故计划改为「收口式」而非重写。
  - **改动（零逻辑）**：`git rm --cached glm-web` 22 文件；`VERSION` 0.2.2→0.2.6；`main.go:85` 0.2.5→0.2.6；`CHANGELOG` 补 `0.2.1`~`0.2.6` 六节。门禁 `cgo-shim-build.py glm` 全绿（15 测试）、面板两 `<script>` `node --check` OK、gofmt 18 文件 PASS。
  - **发布链**：commit `796cbef`（25 = D22 + M3）→ CI dispatch run `46152719667`（Test(glm) success、5 平台 success、darwin/arm64 cancelled 常态、**Release job success**）→ `ALL CHECKSUMS OK` → `publish-assets --allow-missing`（5 artifacts）→ registry `fb7a3df`。
  - **生产**：`install?version=0.2.6&source=source-636fa01121af` → `restart_required:false` → **一次 install 即 `hot reloaded active_version=0.2.6 retired_version=0.2.5`**；落盘 `.so` sha256 `dcff315d…` 与本地 zip 逐字节一致；`/panel` 200（26029 B）、`/login` 200、`/accounts` 200、`/models` 200（5 个 `glm/*`）。
  - **启用态**：`glm-provider` 四字段前后全 `true`（STATE_UNCHANGED）；`safeguard-killer` 于 22:17:00 被**外部并发会话** `PUT /v8/management/config/...` 修改，非本轮链路，未擅自还原。
  - **遗留**：TASK-007 真实 `chatglm_refresh_token` 端到端验证阻塞，待用户 OAuth 登录后提供凭据。
  - **教训**：① `release job` 判定含 `workflow_dispatch && version != ''`，**不依赖 tag**，故 workflow `tags` 列表缺 `glm-provider-v*` 不影响 dispatch 发版（推 tag 触发仍不可用，待补）；② 生产插件实际路径是宿主机 `/usr/local/src/cli-proxy-api/plugins/...`，容器内 `/CLIProxyAPI/plugins/...` 是挂载点；③ `models`/`accounts` 在 `/v0/management/plugins/<id>/` 下，`/v0/resource/plugins/<id>/` 只有 `panel`/`login`；④ 生产启用态快照会受并行会话干扰，判 STATE_UNCHANGED 须按影响范围裁定。

- 会话（2026-11-28 18:17 - 18:38）：**glm-provider 0.2.4：面板自动解密宿主管理密钥，不再要求手填（生产已热重载）**：
  - **用户反馈**：面板弹出「需要管理密钥」输入框，参考 workbuddy 不该让用户填。
  - **根因**：宿主主面板把管理密钥**加密**后写入 `localStorage["cli-proxy-auth"]`：`XOR(salt|host[|ua]) + base64`，带 `enc::v1::`（salt|host|UA）/ `enc::v2::`（salt|host）前缀。此前只做 `JSON.parse` 明文，解不出来 → 弹框。
  - **修复**：完整移植 workbuddy 的 `_keyBytes/_xor/_b64d/deobfuscate/readPanelKey/readUrlKey/persistKey/readSessionKey/getKey`；`SECRET_SALT="cli-proxy-api-webui::secure-storage"`、`PANEL_STORE="cli-proxy-auth"`。另放宽两处：① `readPanelKey` 不限定 `isEmbedded`（同源独立标签页也读 localStorage，并回退 `window.top.localStorage`）；② `api()` 无 key 时**先尝试请求**，仅首次 401 且无 key 才弹框。
  - **验证**：本地 Node 闭环测试 v1/v2 加密→解密往返均 PASS；生产面板已含 `deobfuscate`×3、`enc::v2::`×2、`managementKey`×4。
  - **发布**：commit `a7c3d9c` → CI run `46135146523`（Release success）→ 资产 ALL CHECKSUMS OK → registry `39d8e7a` → 生产 install → **`hot reloaded active_version=0.2.4 retired_version=0.2.3`**；落盘 `.so` sha256 `00b279a9…6ac3` 与 release zip 一致；容器未重启。

- 会话（2026-11-28 15:54 - 17:52）：**qwenwork-provider 0.2.23：推理「慢 + 排队」根因定位与修复，生产已热重载生效（用户「QwenWork 持续监控一下，有些推理请求很慢…直接用 QwenWork 的客户端时没有这个问题的，也没有排队…优先参考 github 找 QwenWork 的反代项目…」）**：
  - **三路证据交叉收敛**：① 生产取证（feed 1812 行 + new-api 1156 条 + CPA gin/json.log + 生产 config.yaml）；② GitHub QwenWork 反代社区口径（modelmux / dsh-qwenwork-connector / dsh-qwenwork-connect / Agent2Providers / Buddy2api / magpie-qwenwork-affiliate / cpa-plugin-qwork-oauth，**已排除 Qoder 系**）；③ **官方桌面客户端逆向**（真实安装 `D:\qwork\QwenWorkCN\2.2.1-26112604\resources\app.asar` → `out/main/main.js`；`D:\qwenwork` 是空目录）。
  - **官方客户端对照（最关键）**：MaaS 出站必发 `x-session-id` + `x-mm-client-request-source` / `x-mm-device-type` / `x-mm-client-request-id` / `x-mm-client-trace-id`；票据按「账号×会话」缓存、`ttl/2` 刷新；**不做本地串行化排队**；`bodyIdleTimeoutMs=0`（长流不被掐）。**我方此前只发 Authorization + Content-Type + Accept** —— 请求在上游侧是无归属流量，这是与官方客户端唯一完全对称的差异。
  - **根因（4 项）**：R1 出站缺会话绑定/归因头 + 全部对话塌缩成同一上游 MaaS session + 路由钉死单一账号（生产会话键恒为 `X-Session-ID: 1`）；R2 慢号让位机制三处同时失效（TTFT 基准晚于发请求 ⇒ 恒 0~1ms；异步流传客户端别名当上游模型；准入闸门 20 > 生产池 8 ⇒ 永久休眠）；R3 `host_callback_id` 未透传 ⇒ 客户端断连后上游请求不被取消；R4 裸 429「模型池忙」被当账号故障冷却。
  - **修复（11 个生产文件 + 5 个测试文件）**：① `applyMaasHeaders` 补 `x-session-id` + `x-mm-*` + `User-Agent`，签发响应缺 `session_id` 时回填本地派生 UUID；② 会话身份拆两套——路由会话键**移植 traework 2026-11-10 的低熵剔除**，上游 MaaS 会话键**按请求体逐对话派生**（`prompt_cache_key`→`user`→system+首条 user 消息 sha256，对齐社区三份实现），票据改按「账号×上游会话」分片并加淘汰（512 上限）；③ TTFT 基准前移 + 异步流改传解析后 upstreamModel + QwenWork 专属准入下限 6（P25 下标 ≥1）；④ 模型池忙归 503 排队类（不冷却账号、不同请求换号）+ 排队判据补 `serviceAvailable:false`；⑤ `host_callback_id` 全链路透传 + 共享客户端补 Dial/TLS/ResponseHeader 三段超时 + 禁 h2。
  - **验证**：门禁 `cgo-shim-build.py qwenwork` 全绿；新增 5 测试文件 33 条断言；**哨兵反证 13 条逐条改坏全部真实命中**（`scripts/qwenwork_sentinel_c1.py` → SENTINEL_ALL_OK）；格式检查无 BOM/无尾随空白/gofmt 仅命中存量文件；6-review `STYLE: PASS`。
  - **发布**：`311514c`（25 文件 +2004/−78）→ CI run `46128751959`（Release success、Test(qwenwork) success、5 平台，darwin/arm64 cancelled 常态）→ 资产 ALL CHECKSUMS OK → registry `3108b94`（5 artifacts）→ 生产 install → 落盘 `.so` sha256 `f05f44cb…87a0` 与本地 zip 逐字节一致 → **`hot reloaded active_version=0.2.23 retired_version=0.2.22`**（17:42:47）；14 插件 `STATE_UNCHANGED`；容器未重启（started 08:15:48Z restarts=0）；`/accounts` `/panel` 200。测试记录 `doc/5-tests/2026-11-28_175000_...md`。
  - **生产回归（0.2.23 生效后实时流量）**：TTFT **p50=8291ms / p90=11721ms**（修复前 p50=0ms）；端到端 **p50=9988ms / p90=19931ms / max=28889ms**（修复前 p99=65.4s、new-api 侧 p99=125s max=260s）；承流账号 **1 个 → 8 个**；会话键由恒定 `header:1` 变为逐会话 `clientreq:session-<uuid>`；`slow pool size updated: now=8`（闸门放行，慢号让位机制复活）。
  - **部署期三次踩坑（可复用）**：① `plugin-store install` 只收 **POST**，curl 默认 GET 会静默 404（空响应）；② **整源原子校验**——并行会话 `glm-provider` 0.2.3 已 bump 版本未回填 artifacts ⇒ 整个源被拒（与 2026-10-10 同型），轮询等待其回填即可恢复；③ 首个 install 只落盘未热重载——**宿主异步 config reload 被折叠**（并行会话同刻装 traework），**再触发一次 install** 即触发完整 reload 轮。
  - **未闭环（登记）**：宿主 `retry.request-retry: 100` / `max-retry-credentials: 100` 会把任何插件错误放大成最多 100 轮 × 100 凭据的重试风暴（属宿主全局配置，社区建议用 `request-scoped-errors` 把 503 排队类声明为终态，**需用户决策**）；上游 429 原始报文尚未逐条留证；首帧静默上限仍由宿主 `host.http.stream_read` 决定。
  - **qwenwork-provider 0.2.24（持续监控补齐，已上线）**：用户把判据收紧为「**首字大于 10s 的**」，按该口径用 **new-api 上游真值**（`frt` + `cache_tokens`）复测：修复后仍 **39.0%**（32/82）超 10s（修复前近 2h 34.7%）。**逐项排除后认定残留在上游侧**：① 插件 TTFT p50=7775ms vs 客户端 frt p50=7994ms ⇒ 插件对首字只加 ~0.2s、零缓冲；② 上游尝试放大 1.11×；③ 慢/快两组前缀缓存命中率完全相同（0.996 vs 0.996）；④ 账号/会话间隔/并发三维度均无相关性。残留是上游在 580~630K token 上下文上的固定处理/排队开销（与上下文规模不同阶：456K 与 278K 的 frt p50 同为 ~6s），官方客户端的办公场景上下文远小于此，**不构成同量对比**。
  - **qwenwork-provider 0.2.25 / 0.2.26 / 0.2.27（把「首字>10s」追到最后一层，已全部上线）**：
    - **0.2.25 亲和类归因头改会话级恒定**：官方把 `x-mm-client-request-id`/`x-mm-client-trace-id`/`x-mm-request-set` 做成**每 turn 恒定**，我方此前每请求随机。A/B（n=54 vs 66）`>10s` 31.5%→48.5%，**但两窗口时段不同（晚高峰）⇒ 时段混淆，假设未被证实**；保留理由是对齐官方协议语义、消除真实偏差。
    - **0.2.26 按账号在跑请求数平摊并发**（移植同仓 `traework` 0.5.42 已验证的修法）：新增 `account_load.go`（在跑计数 + `pickLeastLoaded` + `pickLessLoadedThan` + 软上限 2）；`pickActiveAuth` 改按负载平摊；`pickSessionAuth` 粘性命中后在跑数 <2 才复用、超限改绑到更闲的账号并写回绑定表；三条请求路径在生命周期内记账、换号迁移计数。**安全前置**：快/慢两组 `cache_ratio` 均 0.999 且分属不同账号 ⇒ 上游前缀缓存跨账号共享，摊开不丢缓存。**A/B（n=34）`>10s` 38.2%，无单调改善；且三个窗口的单账号在飞并发恒为 1 ⇒ 槽位排队机制在我们的流量上不成立**（那是 13–25 并发/号的场景），0.2.26 对当前流量是 no-op（对突发仍正确、无害）。
    - **0.2.27 兜底入口对齐官方 + 补 device-ua**：`maasDefaultBaseURL` 原写 `https://maas.xiaoluozi.cn`（官方常量表里属 **legacy/qwenwork** route family，社区实测该兜底值 **401**），官方是 `https://api9.qwenwork.cn` ⇒ 签发响应缺 `base_url` 时我方必然 401、官方不会，已修；另补 `x-mm-device-ua`（官方出站白名单 `ALLOWED_HEADERS` 含该字段）。
    - **九项排除链（最终）**：① 插件对首字只加 ~0.2s（插件 TTFT 与客户端 frt 逐条一致）；② 上游尝试放大 1.11×；③ 上下文规模非同阶且**同规模双峰**（713k 请求同时出现 7.9/8.1/31.8/188s）；④ 各账号 >10s 率 30%/29%/18% 无离群号；⑤ 同会话内首字 5.9–23.2s 跳变；⑥ 慢/快组 cache_ratio 均 0.996；⑦ 生产实际外连 163.181.78.145 与 `api9.qwenwork.cn` 同网段 ⇒ **同一组 MaaS 入口**；⑧ 单账号在飞并发恒为 1 ⇒ 槽位排队机制不成立；⑨ **官方对 `minimax-m3.1-flash`/`qwen4-max` 走 `anthropic/messages`，我方统一 `chat/completions`** ⇒ **剩余最可能差异，需「同 body 双协议 A/B」，属新增能力，待用户决策**。
    - **发布与验收**：CI `46137849468` / `46141764468` / `46144611189`（Release success）；生产 `hot reloaded active_version=0.2.25/0.2.26/0.2.27`；三次 `.so` sha256 与本地 release zip 逐字节一致；14 插件 `STATE_UNCHANGED`；容器未重启。哨兵反证累计 **24 条**（S17~S24）逐条改坏全部真实命中。
    - **教训**：① 「官方客户端没这个问题」⇒ **逆向官方客户端的出站实现**（Electron 解 `app.asar` → `out/main/main.js`）比读社区代码更直接，能发现协议形态/兜底入口/白名单头这类隐藏差异；② **A/B 必须同时段**，跨时段会得出相反结论；③ **负结果也要留档**（并发恒为 1 直接推翻了槽位排队假设）；④ 兜底常量也是协议契约。
  - **qwenwork-provider 0.2.28（采集前缀缓存创建量，已上线）**：`usage.Detail.CacheCreationTokens` 一直写进用量 feed，但 `usageDetailFromMap` **从不解析** ⇒ 恒为 0，监控侧看不到「缓存是被复用还是被整体重建」。修复后 `cache_creation_tokens` 真实落盘（0.2.28 生效后 n=33 实测全为 0 ⇒ **缓存从未被重建**，「缓存重建导致每次全量 prefill」被证伪）。CI `46146376088`（Release success）；生产 `hot reloaded active_version=0.2.28 retired_version=0.2.27`（20:56:20）；`.so` sha256 `903c1f80…8956` 与本地 release zip 逐字节一致；`STATE_UNCHANGED`、容器未重启；哨兵新增 S25 真实命中。
  - **最终定量结论（n=380，0.2.23 起 TTFT 可信）**：**`TTFT(ms) = 3090 + 10.20 × input(K)` ⇒ 10s 临界上下文 ≈ 677K token**（45万→7.7s / 60万→9.2s / 71万→10.3s / 700K-900K 桶 p50=8.6s、>10s 率 38.8%）。⇒ **用户「45~71 万 token 正常 <10s」的预期与实测高度吻合**；**判定「首字>10s 是否异常」必须先按输入规模标定基线**，否则会把正常的规模成本误报成故障。残留的 ~30% 尾部（同会话同账号、缓存命中下仍 6~20s 波动）来自**上游排队/实例调度**，插件侧贡献 ~0.2s。
  - **收口判定**：插件侧 12 项缺陷全部修复并上线（会话身份/归因头/慢号让位/排队分类/断连取消/缓存口径/缓存创建量/兜底入口/亲和头会话恒定/负载平摊/device-ua/观测埋点）；**首字 >10s 的主体责任在上游**（大上下文固有处理成本 + 上游排队波动）。剩余可干预点仅「官方对 anthropic-format 模型走 `anthropic/messages`、我方统一 `chat/completions`」这一项，需新增响应转译能力，**属新增功能，未擅自实施**。
  - **0.2.24 的三个交付**：① **修复前缀缓存字段提取**（上游把命中数放在 `usage.prompt_tokens_details.cached_tokens`，插件原先只读顶层 `cached_tokens` ⇒ feed/面板缓存命中恒为 0、监控口径失真）；② **首字慢埋点**（首字 >10s 按账号节流打 `slow first token`，含账号/模型/首字/总耗时/输入 token/缓存命中/命中率/输出与思考 token）；③ **票据签发留痕**（按 60s 节流打 `maas ticket minted`，识别「退化成每请求重签」）。哨兵反证扩到 **16 条**（新增 S14 缓存字段退回顶层 / S15 首字慢阈值放大 / S16 票据日志失去节流），逐条改坏均真实命中。发布：`b61c13f` → CI run `46134653443`（Release success、Test(qwenwork) success、5 平台）→ 资产 ALL CHECKSUMS OK → registry `2a13000` → 生产 install → 落盘 `.so` sha256 `3b56e4ec…d83c` 与本地 zip 逐字节一致 → **`hot reloaded active_version=0.2.24 retired_version=0.2.23`**（18:43:37）；14 插件 `STATE_UNCHANGED`；容器未重启。**生产验证**：`maas ticket minted: account=1c5a4499… session=da2215f7…`（按会话签票、非每请求）；`slow first token: … ttft=15280ms total=15.387s input=717480 cached=716207 cache_ratio=0.998`；feed 的 `cached_tokens` 真实落盘（ratio 0.997）。
  - **教训**：①「客户端没这个问题的」最有价值的对照组是**官方客户端自己的实现**——逆向 `app.asar` 拿到出站头全集后差异一眼可见，比读社区代码更直接；② **TTFT 恒 0 一定是测量口径 bug**，不是「上游快」；③ 会话键必须**逐对话隔离**（社区明文禁止用账号 uid 当 session_id），否则上游排队 + 前缀缓存串味一起发生；④ 仓内已有正确实现时**移植**（traework 低熵剔除、workbuddy callback 透传与分阶段超时）优于重造。

- 会话（2026-11-28 17:21 - 17:40）：**glm-provider 0.2.3：面板按 workbuddy 规格重构（生产已热重载）**：
  - **用户反馈**：面板太简陋，「也没有参考 workbuddy 的面板来实现，也要重构一下」。
  - **重构内容（`glm/panel.html` 100 行 → 503 行 / 25KB）**，完整复刻 workbuddy 面板骨架：
    - **主题令牌**：light/dark 两套 CSS 变量（对齐 CPA 主面板），嵌入 iframe 时同步父级 `data-theme`。
    - **管理密钥获取**：嵌入自动读 `cli-proxy-auth` / URL `?key=` 参数（读后从历史清除）/ 手工输入（仅 sessionStorage）；401 累计 3 次才清 key 并退避 60s，403 IP 封禁识别后统一退避——避免连点触发封禁。
    - **工具栏**：添加账号 / 导出凭证 / 刷新列表。
    - **筛选栏**：全部 / 可用 / 已停用 / 测试失败，带实时计数。
    - **汇总卡**：账号总数、可用、已停用、测试失败四项指标格。
    - **账号卡片网格**（3 列响应式）：标签 + 状态徽章 + AuthID + 令牌掩码 + Access 过期时间 + 用户 ID + 测试/启停/删除按钮。
    - **模态框**：添加账号（含 chatglm.cn 取 token 三步指引 + textarea + 备注）、删除二次确认；Esc/遮罩关闭。
    - **toast 通知栈**：右上滑入，ok/err/warn 三态。
  - **发布**：commit `f6e1ef2` → CI run `46130513004`（Release success）→ 资产 ALL CHECKSUMS OK → registry `a1b94fe` → 生产 install → **`hot reloaded active_version=0.2.3 retired_version=0.2.2`**；落盘 `.so` sha256 `e6884556…ee3b` 与 release zip 一致；容器未重启。
  - **验收**：面板 25330 字节，含标题「智谱清言 账号面板」、filter-bar、summaryBox、toasts、management key 框、导入并校验按钮；两 script 块 node --check 通过。
- 会话（2026-11-28 16:46 - 17:12）：**glm-provider 0.2.2：授权入口指向插件页 + 去掉书签改纯 token 授权（生产已热重载）**：
  - **用户反馈**：①宿主「添加账号」弹窗里 GLM 的授权入口显示 `https://chatglm.cn/`，点开只是官网，登录后无反应；②质疑「我们是 token 授权，为什么也要桥接页」。
  - **根因 1**：`handleStartLogin` 的 `URL` 填了上游官网 `https://chatglm.cn/`，应填**本插件落地页绝对地址**。补 `rememberPublicOrigin`/`currentPublicOrigin`（从 `X-Forwarded-Host`/`Host` + `X-Forwarded-Proto` 观测外部 origin，回环地址不覆盖），`management` 入口每次请求记录，`URL` = origin + `/v0/resource/plugins/glm-provider/login?state=...`。
  - **根因 2（设计纠偏）**：桥接页不是第二套鉴权，而是**宿主登录弹窗的落地页**——宿主发起登录必须拿到一个可打开的 URL（不给会报 `failed to generate authorization url`），而 `chatglm_refresh_token` 存在浏览器 LocalStorage，宿主读不到，所以必须有页面承接用户粘贴。据此删掉书签方案（`buildBookmarkletScript`/`htmlEscapeAttr` 全删），`/login` 改为纯 token 录入页（步骤说明 + textarea + 提交按钮，提交走 `/login/callback` 落盘后展示结果）；面板「一键授权」改回「添加账号」直贴 token。
  - **0.2.2 发布**：commit `14835b8` → CI run `46128248012`（Release success）→ 资产 ALL CHECKSUMS OK → registry `e0acff5` → 生产 install → **`hot reloaded active_version=0.2.2 retired_version=0.2.1`**；落盘 `.so` sha256 `773a7e9c…4326` 与 release zip 逐字节一致；容器未重启。
  - **验收**：`/login` 页面含 `id="tok"` 与「提交授权」、无 `bmLink`；面板含「添加账号」「导入并校验」、无「一键授权」。
  - **教训**：① `AuthLoginStartResponse.URL` 必须是插件自己的落地页，不能填上游官网；② 插件跑在容器里不知道外部域名，必须从请求头观测 origin 拼绝对地址；③ token 类 provider 的「桥接页」本质是宿主弹窗的 token 录入落地页，不是额外授权体系。
- 会话（2026-11-28 16:21 - 16:40）：**glm-provider 0.2.1：重启后加载成功 + 管理接口修复 + 旧 id 清理（生产已上线）**：
  - **用户手动重启后**：`glm-provider` 0.2.0 成功 `plugin loaded + plugin registered`，面板/授权页均 200，白屏已修复（面板返回真实 HTML，含「一键授权」入口与书签兜底）。
  - **发现新缺陷**：`/accounts`、`/models` 等管理接口返回**空响应**——根因是所有 management 响应也必须包成 `pluginapi.ManagementResponse`（同面板），而原实现用了裸 `okEnvelope(map)`。新增 `jsonResponse()` helper 并批量替换 18 处，修复。
  - **0.2.1 发布**：commit `cd1d04b` → CI run `46125821167`（Release success）→ 资产 ALL CHECKSUMS OK → registry commit `0057899`（5 artifacts）→ 生产 install → **`plugin hot reloaded active_version=0.2.1 retired_version=0.2.0`**；落盘 `.so` sha256 `8cc682dc…d556` 与 release zip 逐字节一致；容器未重启（rc=0）。
  - **验收**：`/accounts` 返回 `{"accounts":null}`（无账号属正常）、`/models` 返回 5 个 `glm/*` 模型、panel=200、login=200。
  - **旧 id 清理（用户确认删除）**：先备份到 `/root/plugin_cleanup_20261128163711`（config.json + 7.6MB .so），再 `DELETE /v0/management/plugins/glm-web-provider` → `configured_removed:true, file_deleted:true`；旧面板页现 404；最终 14 插件，`glm-provider` 全 true，其余状态不变。
  - **教训**：CPA 插件所有 management/resource 响应都必须用 `pluginapi.ManagementResponse{StatusCode, Headers, Body []byte}` 包装；裸 `okEnvelope(map)` 会返回空体（面板白屏、接口空响应的同一根因）。
- 会话（2026-11-28 14:33 - 15:58）：**glm-provider 0.2.0：改名 + 修面板白屏 + 书签一键授权（已发版，生产待重启加载）**：
  - **用户反馈**：①面板白屏，且名称要改成 `glm-provider`（不要 glm-web-provider）；②授权不完整，登录后应自动回调。
  - **改名**：`glm-web-provider`→`glm-provider`、目录 `glm-web/`→`glm/`、模型前缀 `glm-web/`→`glm/`、go.mod module、CI options/matrix/PLUGINS map/tag/display、registry 条目全部同步；registry 已无旧 id 残留。
  - **白屏根因与修复**：面板必须返回 `pluginapi.ManagementResponse`（StatusCode/Headers/Body），原实现返回自定义 map → 宿主拿不到 HTML。新增 `glm/panel.go` 用 `embed.FS` + `ManagementResponse`，`management.go` 改调 `handlePanelPage()`。
  - **书签一键授权（新增 `glm/login.go`）**：`/login` 引导页（拖书签）+ `/login/callback` 接收 `X-GLM-RefreshToken` 头并落盘（免管理密钥）+ 登录会话 state 与 15 分钟 TTL + 轮询返回成功/失败；书签在 chatglm.cn 页面读 `chatglm_refresh_token`（localStorage 优先，Cookie 兜底）后同源 fetch 回插件，页面自动跳转结果页；保留手工粘贴兜底。`normalizeMgmtSubPath` 增加 `/v0/resource/plugins/<id>` 前缀兼容。
  - **门禁**：`cgo-shim-build.py glm` build+vet+test 全绿；面板两 script 块 node --check OK；gofmt PASS。
  - **发布链**：commit `83a8111`（26 文件，含旧目录删除）→ CI run `46118422228`（head 83a8111，Release success）→ `download-release-assets.py 0.2.0 glm-provider` ALL CHECKSUMS OK → `publish-assets.py glm-provider 0.2.0 --allow-missing`（5 artifacts）→ registry commit `f3120e0` → 远端 raw 0.2.0/5 artifacts。
  - **生产部署状态（⚠️ 未完成加载）**：首次 install 遇 registry CDN 旧缓存报 `direct install requires at least one artifact`（502），等 60s 重试成功；`glm-provider` configured=true、`.so` 已落盘 `/CLIProxyAPI/plugins/linux/amd64/glm-provider-v0.2.0.so`（sha256 `0475d771…6f6d53`），但 **registered=false、面板/授权页 404**。
  - **根因**：宿主仅在**启动时**扫描插件目录，热重载只对已加载插件生效；新插件 id 首次安装必须重启才能加载。已探测 `/plugins/reload`、`/plugin-store/reload`、`/plugins/rescan` 等端点均 404，无免重启通道；`PUT /plugins/<id>/config` 同内容不触发加载（符合项目既有结论）。
  - **用户裁定（15:57）**：选择「先不重启，保留现状」——重启留待后续维护窗口。旧 `glm-web-provider` 0.1.0 仍在运行（configured/registered/enabled 全 true），暂未清理。
  - **恢复步骤（重启后即可用）**：重启 `cli-proxy-api` → 日志确认 `plugin loaded + plugin registered plugin_id=glm-provider` → 验证 `GET /v0/resource/plugins/glm-provider/panel` 200 且非白屏 → `GET .../login` 200 → 面板或引导页导入 refresh_token → 真实对话验证。清理旧 id：确认新版可用后 `DELETE /v0/management/plugins/glm-web-provider`（会同时删配置与落盘 .so）。
- 会话（2026-11-28 04:15 - 05:32）：**glm-web-provider 0.1.0 全量实现并上线生产（用户「安装计划全部实现，然后上线生产」）**：
  - **实现（全新 `glm-web/` 插件，15 Go 文件 + panel.html + go.mod/sum + VERSION + Makefile + CHANGELOG）**：token 型 `StoredAuth` 落盘（保留未知顶层键）、`generateSign`/`requestToken`/`acquireToken`（md5 签名 + 3600s 缓存 + 并发单飞）、`buildStreamPayload`/`mergeMessages`/`parseGLMFrame`/`pumpGLMStream`（GLM `parts[].content[]` 全量快照增量去重、text/think 分离、[DONE] 立即收尾）、非流式 `convertNonStream`、多账号 scheduler + 故障记账、面板 token 导入（`glm-web/` 模型前缀，`const API` 对齐 id）、NDJSON 用量上报。ABI 骨架复用 zcode。上游 refresh/stream/info/delete 端点全部可配置覆盖。
  - **门禁**：`python scripts/cgo-shim-build.py glm-web` build+vet+test 全绿（15 测试）；哨兵反证三项有效（签名算法/ SSE 增量/别名前缀，改坏→FAIL→还原全绿）；面板两 script 块 node --check OK；gofmt 15 文件 PASS；污染扫描 glm-web 零命中（11 条均为存量插件）；6-review `STYLE: PASS`。文档：`doc/5-tests/2026-11-28_050000_...`、`doc/6-review/2026-11-28_051000_...`。
  - **CI workflow 扩展**：`build.yml` 注册 `glm-web-provider`（dispatch options + test matrix + PLUGINS map + release tag 条件 + display name）。
  - **发布链**：VERSION/main.go=0.1.0 → commit `5b09c1d`（25 文件）→ push → dispatch `plugin=glm-web-provider version=0.1.0 allow_partial=true` → run `46068933609`（head 5b09c1d，Release=success，run conclusion=cancelled 属 darwin/arm64 常态）→ release `461076121`（6 资产）→ `download-release-assets.py 0.1.0 glm-web-provider` ALL CHECKSUMS OK → `publish-assets.py glm-web-provider 0.1.0 --allow-missing`（5 artifacts）→ registry commit `dced117` push → 远端 raw **ALL PASS**（5 artifacts sha256/size 全 OK）。一次发布两次推送，未混入并行改动。
  - **生产部署（45.207.222.65:8317）**：管理密钥 `cpa_management_key` 经 bcrypt 离线验明（不回显）；install 前取 13 插件快照；`POST /plugin-store/glm-web-provider/install?version=0.1.0&source=source-636fa01121af` → `restart_required:false plugins_enabled:true` → 日志 `plugin loaded + plugin registered version=0.1.0`（新插件首次安装形态）；落盘 .so sha256 `29146ed...12e5b` 与 release zip 内 .so 逐字节一致；面板 200；除新增 glm-web 外 13 插件启用态逐字段不变；容器未重启（started 2026-11-27T17:50 早于 install 05:28）。
  - **模型目录说明**：`/v1/models` 当前 glm-web 模型为 0，与 zcode/qoder-ai/cursor 等无账号 provider 一致——宿主按已配置账号动态激活模型，用户上线后 OAuth 登录账号即出现，非缺陷。
  - **TASK-008 待办（用户上线后）**：用户提供 OAuth 登录产物 `chatglm_refresh_token` → 面板导入 → 真实验证 H-01（刷新拿到 access_token）/H-02（SSE 对话出字 + [DONE]）/H-04（SIGN_SECRET 有效）/H-05（getUserInfo 识别账号非访客）；失败按 GAP 回写并最小修正复验。

> 更早的会话明细（2026-11-20 及之前：store-auth PAT、10 插件面板、doubao 0.1.8/0.1.9/0.1.11/0.1.19~0.1.22 等）已归档至 git 提交历史与 doc/ 对应文档，本文件只保留最近 7 条会话摘要以控制体积。
