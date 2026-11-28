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

- **当前会话（2026-11-28 23:45 - 2026-11-29 02:05）：qwenwork-provider 0.2.31/0.2.32「保留摘要」上下文压缩 —— 把被裁掉的语义还回模型**：
  - **起点**：0.2.30 用纯本地裁剪把上游实收输入钳到 ~153K token（用户原话场景「45~71 万 token、首字 30s/160s」结构性消除，残余首字>10s 13.3% 判定为上游侧）。**但裁剪的代价是被裁历史语义消失 ⇒ 长会话持续「失忆」**。
  - **官方真实做法（逆向证据）**：官方桌面客户端**不是本地裁剪，而是调模型做一次锚定摘要再注回历史**——客户端内嵌 opencode harness（`resources/agent-sdk/components/flash-cli/0.47.0-26112401/runtime/chunk-*.mjs` 的 `@opencode/SessionCompaction`）的 `d1()` 提示词模板、`a9`/`Hie` 锚定 Markdown 结构（Objective / Important Details / Work State / Next Move / Relevant Files）、`<prior-summary>` 滚动合并分支、role=user 的 `<conversation-checkpoint>` 注入形态、官方交替文本序列化（`[User]:`/`[Tool result]:`…）。
  - **本轮实现**：`qwenwork/context_summary.go`（新）——摘要缓存（会话键 → 条目，容量 256）、内容签名判定失效、官方对齐的提示词与注入形态、后台异步摘要（与主请求同会话同票据、共用账号在跑计数）；`context_budget.go` 抽出 `planContextTrim`（摘要需要「裁剪点下标」定位未覆盖区段）；`payload.go` 的 `prepareUpstreamBody` 改为返回 `{Body, Origin, SessionKey, Summary}`（会话键**在裁剪前、基于客户端原文**派生）；`main.go` 两个执行入口接线；`usage_config.go` 新增 `context_summary_enabled` / `context_summary_model` / `context_summary_max_chars`。
  - **关键取舍**：**摘要绝不阻塞首字**——当前请求照旧纯裁剪，摘要后台生成，**下一轮同会话命中并注入 checkpoint**（用一轮语义缺失换首字不变）。
  - **防串味**：摘要记录「被覆盖段的内容签名」，客户端一旦回退/改写历史 ⇒ 旧摘要立即作废（既不注入也不参与滚动合并）。
  - **0.2.32（生产暴露的缺陷）**：0.2.31 上线后同一会话 3 秒内生成**两份**摘要（`covered=1169` 两次）⇒ 根因是 `enqueueContextSummary` **无会话级去重**；代价是每次摘要带整段待摘要历史（约 46 万字符 / 13 万 token）重复烧上游额度，且每个任务占住一个**账号在跑槽位**、与主请求抢同一账号 ⇒ 直接抵消「不阻塞首字」的设计目标。修法：`claimContextSummarySlot` / `releaseContextSummarySlot` 成对管理 `contextSummaryRunning`（同会话同时只允许一个在跑任务；**全局并发上限是限流不是去重**）。
  - **过程中修掉的三个实现缺陷**：① 按**字节**截断会切坏 UTF-8 ⇒ 注入回请求后模型读到乱码 ⇒ 改按 rune 截断；② `context_summary_model` 配置项被读取但未接线 ⇒ 已接入；③ **哨兵脚本三处工程缺陷**：GBK 打印崩溃（`UnicodeEncodeError` 打断整个脚本）/ 异常时源码不还原 / **宿主编译器被 OOM 杀掉（Windows 分页文件仅 26MB ⇒ 提交额度打满）被误判为 NO-HIT** ⇒ 修好并新增 `ENV-FAILED` 判定。
  - **验证**：`cgo-shim-build.py qwenwork` 全绿；新增 `context_summary_test.go`（12 条）+ `context_budget_test.go` 增补「极端预算退化分支下会话键仍基于原文」用例；哨兵扩到 **37 条**、分批跑完 **全部命中**（每批 `restore recheck === all green`）。
  - **生产验证（0.2.31）**：`hot reloaded active_version=0.2.31 retired_version=0.2.30`；容器 `.so` sha256 `9e291be7…70fa5` 与本地 zip **逐字节一致**；14 插件 `STATE_UNCHANGED`；容器 `restarts=0` 未重启；**摘要全链路日志闭环**：`pending_summary=true` → `context summary ready: covered=1169 chars=8960` → 下一轮 `summary_injected=true`。
  - **未闭环**：摘要**质量未做人工评读**（只验机制成立，未验对回答质量的增益 ⇒ 需「同会话开/关摘要」语义 A/B）；`context_summary_model`（用便宜模型做摘要）生产未配置未验证；部署后 TTFT 样本仅 31 条 ⇒ 「无回归」只是方向性结论。
  - **并行会话踩坑（新增）**：并行会话的 `ec1f399` 把本会话未暂存的 0.2.32 源码一并带入（内容完整无误），**提交信息归属错乱** ⇒ commit 后必须 `git show HEAD:<file>` 核对内容归属。


- **当前会话（2026-11-28 20:00 - 23:45）：qwenwork-provider 首字延迟收口复核 —— 0.2.30 稳态全量样本判定「残余首字>10s」属上游侧**（用户「继续」）：
  - **稳态数据**（插件用量流，0.2.30 生效后 2026-11-28T14:29:48Z~15:12:52Z，n=457）：TTFT p50 **3,136ms** / p90 11,396 / p99 18,698 / max **20,497ms**；**首字>10s 13.3%、>15s 3.9%、>30s 0 条**；input p50 **138,126** / p90 146,182 / max **153,251**（裁剪前 p50 409,518、max 927,650）；cache_ratio p50 0.992；**cache_creation_tokens>0 = 0 条**（前缀缓存从未被整体重建）；状态码 0×434 / 429×21 / 502×2（失败率 5.0%，**无 400** ⇒ 裁剪未破坏 tool 配对）。
  - **预算「真被执行」的核查（本轮新增）**：0.2.30 生效后 **49 分钟（14:29:33Z~15:18:41Z）内 515 次裁剪事件**，其中 440 次与用量流 ±6s 对齐 ⇒ 裁剪后 **440/440 条全部落在 460,000 字符预算内（超预算 0 例）**；稳态窗口内 input>200k 的 6 条全部落在热重载（14:29:32Z）前 30 秒的在途窗口 ⇒ **0.2.30 生效后无一条请求超过预算**（裁剪前 input_chars p50 1,054,762 / max 2,540,814）。
  - **残余 >10s 的四项交叉核查**：① **分段计时**（上游响应头 `X-Envoy-Upstream-Service-Time`）上游模型服务本体占 **94%~97%**、边缘 CDN ~0.4~0.5s、插件 ~0.2s；② **前缀缓存命中与否不是区分因子**（cache_ratio≥0.9 组 >10s 13.3% / p50 2,996ms，<0.9 组 13.4% / p50 3,391ms）⇒ 推翻「裁剪滑窗使头部后前缀失效 ⇒ 首字变慢」的假设，同时解释了 `cached_tokens` 恒为 17,792 / 22,540（= 头部规模）的成因；③ **首字与输入规模脱钩**（61 条慢请求 input 全部 ≤152K、p50 137,586 与全体同阶；`TTFT ≈ 3.09s + 10.20ms × input_K` 在 138K 处只剩 ~1.4s 可干预项）；④ **潮汐成簇**（相邻慢请求间隔中位 26s、p90 91s、93% ≤120s）。
  - **结论**：用户原话场景（45~71 万 token 输入、首字 30s/160s）**结构性消除**（45~71 万 token 924 条→0 条；首字>30s 0 条；max 20.5s）；插件侧 14 项缺陷已全部修复上线；**残余首字>10s 判定为上游侧，插件侧已无可干预空间**（再压预算收益 <1s 且继续丢历史 ⇒ 不动 `context_char_budget`）。
  - **产出**：文档 `doc/5-tests/2026-11-28_234000_qwenwork首字延迟_0.2.30稳态复核.md`；定时巡检任务（每 2 小时）基线与阈值更新（新增「首字>30s 率 >0.5%」「input max >250,000」「cache_creation>0 占比 >5%」三条判据，以及缓存非杠杆等已排除项）。
  - **纪律**：并行会话共享 git index ⇒ commit 前必须 `git diff --cached --name-only` 核对暂存范围，只提交自己产生的文件。

- **当前会话（2026-11-28 23:15 - ）：热更新内存基线累积根因检查（只查未改码）**（用户「插件热更新会出现内存泄漏的bug问题, 检查一下」，并给出实测：进程 30 个 .so、anon 500MB→1GB、线程 235→400）：
  - **用户判断成立**：不是请求级泄漏，是 Go c-shared 镜像不可卸载导致常驻基线被逐版抬高。代码级证据：`loader_unix.go:188-192`（"image must never be unloaded"）、`close()`（`:293-324`，`hostCtx`/`hostAPI` 注释明说故意泄漏）、`host.go:401-406` + `:999-1004`（retired 裸 append 无上限、retire 时不 shutdown 不删 `hostCallbackEntries`）。
  - **三处修正**：① shadow 是**内容寻址**（`shadow.go:576-581`），同 digest 复用同路径、`dlopen` 返回同一 handle ⇒ **只有版本变更才 +1 image，同版本重装不增**；② quiesce 协议四个插件都没实现，宿主判 unsupported 后 `proceed=true` 但既不暂停也不接管（`host.go:1158-1190`）；③ 孤儿循环并非全部常驻 —— traework/workbuddy 已铺满 `retireIfSuperseded`，**qwenwork 只铺了 2 个，漏 5 个**（`main.go:119`/`main.go:137`→`watchdog.go:100`/`accountFailover.go:62`/`session_auth.go:58`/`slow_account.go:267`，其中 `slow_account` 每 10min 走一次跨进程宿主 RPC 枚举账号）；`glm` 无常驻循环，只承担 image 驻留。
  - **量化（新结论）**：`memory_limit.go` 设计假设是 8 个 runtime × 128MiB ≈ 1GiB（容器 2GiB）；实际 30 image ⇒ **3.84GiB 软上限 >> 2GiB 硬上限**，`CPA_PLUGIN_MEMORY_LIMIT_MB` 这条防线在热更新累积后失效。按用户实测 ≈16.7MB/image，距 2GiB 只剩 12~15 次版本发布。
  - **修法三层**：插件侧补 qwenwork 5 个守卫 + 新增 qwenwork 静态接线测试（镜像 `test/traework/instance_guard_wiring_test.go`，缺这条测试正是漏铺原因）+ 四插件实现 `plugin.quiesce`；宿主侧 quiesce 后对 retired 调 `close()` + retired 上限 1 + 长期改 worker 子进程；运维侧监控 `grep -c cliproxy-plugin /proc/1/maps`（阈值 ≥15）并滚动重启清零。
  - **文档**：`doc/4-bugs/2026-11-28_231500_插件热更新Go-cshared镜像不可卸载致内存基线累积根因与修复方案.md`。
  - **未闭环**：生产实测未做（SSH 公钥未授权，`Permission denied (publickey,password)`，端口 18998）；本轮未改任何代码。

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

- **当前会话（2026-11-29 00:54 - 02:15）：glm-provider 0.2.12/0.2.13：模型档位改为从历史会话动态发现，补 chat_agent**（用户「模型还是对不上...我们希望的是可以动态获取到准确的模型，未来新增模型不用改插件」）：
  - **`model` 字段是死路**：九组实验（`meta.model` / 顶层 `model` / 真实名 / `glm-4-air` / `glm-5.1` / `totally-bogus-model`）上游全部回报同一内部路由名 `moe_53f`，**传任何值都被忽略**，含不存在的模型名也不报错。
  - **真开关是 `chat_mode`**：实测 `""` / `zero` / `chat_agent` / `deep_research` 改变 `think` / `tool_call_name` / `answer_type` 形态。
  - **发现遗漏档位 `chat_agent`**：`GET /chatglm/backend-api/assistant/conversation/list?assistant_id=...&page=1&page_size=50` 每条会话带 `meta_data.chat_mode`；该账号 46 条历史取值 `chat_agent` / `normal` / `zero`。`chat_agent` 旧代码完全没有（只认 zero 与 deep_research），用户网页用智能体档、插件按默认档发 —— 「对不上」的直接原因。
  - **动态方案**：`discoverChatModes` 扫历史会话（默认 + 自建智能体，各 3 页）收集 `chat_mode` 去重，缓存 10 分钟；`effectiveChatModes` 与基线求并集；**未知取值原样透传**，上游新增档位无需改码。模型名改为 `glm/<chat_mode>`；`resolveAssistantID` 未知名字不再丢弃。
  - **0.2.13 补丁**：历史把默认档记成 `normal`、请求侧传 `""`，二者同档导致目录重复；新增 `normalizeChatMode` 把 `normal` 归一到 `""`。
  - **生产实测**：目录 `glm-4-plus`(标准) / `glm/zero`(思考) / `glm/chat_agent`(智能体) / `glm/deep_research`(深度研究) + 两同档别名；四档端到端均 200。单测 29→34 全 PASS。
  - **部署踩坑**：① `plugin-store install` 因**源缓存**命中旧 registry 报 `direct plugin version not found`，该缓存不在磁盘；② `PUT /plugins/<id>/config` **只卸载不加载**（`metadata` 变 null），**必须带 `"enabled": true` 才触发加载**；③ body 形状 `{"config_yaml":"<yaml>","enabled":true}`；④ 兜底：curl release zip → `python3 zipfile` 解包 → `docker cp` → `PUT config(enabled:true)`。

- **当前会话（2026-11-29 00:30 - 00:55）：glm-provider 0.2.11：模型目录改为接口动态获取 + 档位核实**（用户「好像模型获取得也不对, 我们现在是通过接口动态获取模型的吗?」）：
  - **答**：此前**不是**动态的，`/models` 只回放 `glm/models.go` 的硬编码数组，从不回源。workbuddy 才是「缓存 → `callModelsAPI` 回源 → 落缓存」。
  - **更严重**：`resolveAssistantID` 对非 ID 的模型名一律返回 `defaultAssistantID`，所以 `glm-5.1` / `glm-4.7` / `glm-4-plus` 打到**同一智能体同一 chat_mode**，行为完全一致；只有 `think`(`zero`) 与 `deepresearch`(`deep_research`) 有真实差异 —— 五个名字其实三档。
  - **上游探测**：18 个候选路径只有两条有效 —— `POST /chatglm/backend-api/assistant/list`（用户自建智能体，需 `page`/`page_size`，缺省 400）与 `GET /assistant/info?assistant_id=`。GLM **没有官方模型列表接口**，官方模型由预置智能体承载。
  - **维度实验**：`(if_plus_model, chat_mode)` 六组合实测 —— `if_plus_model` 取 true/false 对响应形态无影响（均 `text`），真正区分档位的只有 `chat_mode`，故不虚构「Plus/普通」两版。
  - **实现**：`modelCatalog` 结构化（DisplayName 如实标注「glm-4-plus 同档」）；新增 `assistants.go`（`listUpstreamAssistants` / `dynamicModelInfos` 追加 `glm/<assistant_id>` / 失败降级带 warning）；`resolveAssistantID` 支持 `glm/<id>` 前缀；`Config` 加 `assistant_list_endpoint`；面板下拉显示 DisplayName、value 用 ID、title 挂档位说明。
  - **验证**：单测 24→29 全 PASS，gofmt 23 文件 PASS，面板 2 script 块 node --check OK；生产 `hot reloaded active=0.2.11`，`/models` 返回 5 条且 DisplayName 已标明同档别名，`warnings` 为空（回源成功，该账号无自建智能体）。
  - **遗留**：官方模型版本由上游按账号决定、插件无法选择；当前账号自建智能体为空，动态链路已通但无真实数据体现。

- **当前会话（2026-11-28 23:45 - 2026-11-29 00:25)：glm-provider 0.2.9/0.2.10：面板测试可选模型 + 账号积分额度展示**（用户「测试没有选择模型, 可以参考workbuddy」「账户没有显示积分」）：
  - **积分端点取证**：16 个候选路径只有 `GET /chatglm/user-api/user/info` 命中 200（`/user/credit`、`/user/quota`、`/user/point` 及 `backend-api` 同类路径全 404），积分在 `result.member_info.left_score`（`"199.01"`），额度用 `token_left_percent` / `token_usage_percent`。workbuddy 的积分走它自家 billing API（`/v2/billing/meter/get-user-resource`），GLM 没有同一套，只能照搬「懒加载 + 卡片形态」。
  - **测试选模型**：后端 `handleMgmtTest` 早已支持 `model` 字段，只是面板没传（恒用默认 `glm/glm-5.1`）。卡片加 `.modelsel` 下拉（选项来自 `/models`），`testAccount` 读值随 `/test-active` 提交。
  - **中途 Bug（0.2.9）**：`/credits?auth_id=...` 恒返回 `auth_id required` —— `normalizeMgmtSubPath` 只从 `url.Parse(req.Path)` 取查询串，而宿主可能只下发纯路径、查询放在 `req.Query`；且原代码有 `_ = urlQuery` / `_ = req.Query` 显式忽略。0.2.10 加 `mergeRequestQuery` 合并两源、宿主优先。
  - **验证**：真实账号 `/credits` 返回 `left_score:"199.01" · 余量 99% · 免费用户，登录赠送200积分/天`；`test-active` 带 `glm/glm-4-plus` / `glm/glm-think` 均 `ok:true` 并回显模型；面板 HTML 含 `modelsel` / `data-credits` / `loadCredits` / `loadModels`。单测 19→24 全 PASS，gofmt 21 文件 PASS，面板 2 个 script 块 `node --check` OK。
  - **发布**：0.2.9 → 0.2.10 各一轮 CI，Release 均 success，生产 `hot reloaded active=0.2.10`，容器未重启；四层版本一致 0.2.10。
  - **注意**：积分每次实时回源 `/user/info`、未缓存；账号多时会并发多次请求，量上来需加短 TTL。

- **当前会话（2026-11-28 23:45 - 2026-11-29 00:10）：safeguard-killer 0.1.15：状态契约审计 + 死字段清理 + 用例补强（已发布生产）**（同一目标第 3 轮）：
  - **审计结论**：配置项 11 个全部有生效点且在面板可见可改；CSS 变量 28 定义 / 25 引用，差集 3 个都是「两套主题各定义 + 一处引用」的活变量；JS 51 个函数全部被调用；`data-action` 6 个值与处理分支完全对称；面板按**响应形状**（`output`/`content`/`choices`）而非格式名注入提示，未知形状原样返回属设计。
  - **唯一死字段**：`sessionBinding.Provider` 只被赋常量 `groupProvider`，`ui.go` 全文检索 `provider` 0 次。已删字段 + 两处赋值 + 注释说明为何不放。生产验证：`/state` 的会话字段变为 `session,auth_id,model,bound_at,last_seen,idle_expires_at`。
  - **覆盖率基线**：`go test -coverprofile` 显示 102 函数 / 26 个 0 覆盖 / 语句 64.8%。`configurePlugin`、`validateConfig`（热更新唯一入口与守门）此前完全没测。
  - **补测 7 条**：`config_test.go` 4 条（合法 YAML 落盘含 notice_prefix、空下发回落默认、8 类非法配置逐条被挡**且不污染生效配置**、信封非 JSON 报错）+ `inspect_test.go` 3 条（Claude 内容三形态、Gemini 六安全/四非拒答 finishReason、流中拒答后绑定推进含三种 no-op）。覆盖率 64.8% → 67.2%，0 覆盖 26 → 23（剩下多为 cgo ABI 与 `main`）。
  - **发布**：`eafe557`（代码）→ 内置 token dispatch run `46162188990` → Release success → registry `59bade4` → 宿主可见版本轮询 6 轮（约 6 分钟）到位 → install 200 → `hot reloaded active_version=0.1.15 retired_version=0.1.14`（00:07:45）→ sha256 `7dcfc6f4…2d9b` 一致 → 分组对话 200。门禁 `PASS=68 FAIL=0`。
  - **观察**：darwin/arm64 连续两个版本（0.1.14、0.1.15）都被 cancelled，其余 5 平台正常；`allow_partial=true` 让 Release 照常产出，registry 继续 5 平台。macOS runner 的取消不是偶发一次，后续可按「5 平台为常态」处理。

- **当前会话（2026-11-28 23:20 - 23:40）：safeguard-killer 0.1.14：流式加固（本地已提交，已发布生产）**（用户「Safeguard Killer插件, 持续监控一下bug和不需要没使用的功能移除。」第 2 轮）：
  - **巡检手段**：配置项使用面逐字段反查（`pluginConfig`/`featureConfig` 全部有生产读取点，**无死配置**）；生产决策分布（108 决策 / 114 attempt **全部 served**，无 fallback）；`staticcheck -checks all`（除 C ABI 假象只剩 `stream.go:79 ST1005`）；`executeGroup`→`prefetchStream`→`forwardStream`→`emitExhaustedNoticeStream` 逐段读。
  - **排除的假缺陷**：① `prefetchStream` 每条错误路径都已关源流（`refusal.go:875/880/889/898/905/911`），`continue` 不泄漏；② `request.intercept_before/after` 共用 handler 是宿主语义（`rpc_client.go:719/728` 都映射到 `InterceptRequestAfterAuth`）；③ `ChunkIndex == -1` 是 `StreamChunkHeaderInitIndex`（仅响应头初始化），不是流结束，早退正确。
  - **BUG-4（P1，未触发但代价最高）**：`forwardStream` / `emitExhaustedNoticeStream` 由 `go` 启动且直接经 cgo 写客户端流；全包 `recover()` 出现 0 次。插件跑在宿主进程内，goroutine panic 会终止整个 CPA 进程（宿主 fuse 只包「宿主→插件」方向）。修：两处各加 `defer recover()`，命中时 `closeStream` 目标流并释放源上游流。
  - **BUG-5（P2）**：`normalizeClaudeStream` 缺 `requestID` 时返回错误 → 整条已生成的流失败。修为原样透传（不建状态、不重编号），顺带消掉唯一真告警 ST1005。
  - **加固（P2）**：`claudeStreamState` 加 `created`，`maxClaudeStreamStates=512` 满时 `evictClaudeStreamLocked` 淘汰最早一条，兜住宿主漏发 `request.complete` 的无界泄漏。
  - **验证**：门禁 `PASS=61 FAIL=0`；gofmt CLEAN；3 条哨兵（recover 改空操作 / 缺 ID 改回报错 / 删淘汰调用）**全部 KILLED**；`staticcheck` 非 C ABI 告警 0。
  - **发布**：commit `e91a242` → 临时 dispatch 工作流（内置 `GITHUB_TOKEN`，`allow_partial=true`）触发 run `46159257774` → **Release success**（5 平台，darwin_arm64 cancelled）→ 资产 `ALL CHECKSUMS OK` → registry `1c6e7bc` → 生产 install 200 → **`hot reloaded active_version=0.1.14 retired_version=0.1.13`**（23:35:44）→ 落盘 `.so` sha256 `cd258a82…0591` 与本地 zip 一致 → `count_tokens` 成员名回归 200。
  - **新教训（重要）**：宿主 install 前会 `FetchRegistry`（每次实拉，无本地缓存），但 raw.githubusercontent 的 CDN 有约 5 分钟缓存。刚 push registry 就 install 会命中 `plugin_manifest_invalid: direct plugin version "X" not found`（8-30ms 快速失败，无网络请求特征）。**判据**：`GET /v0/management/plugin-store` 看宿主实际拉到的 `safeguard-killer` 版本；等它变成目标版本再 install，不要盲重试。
  - **教训**：`Split-Path (Split-Path $shim)` 会落到仓库根 `F:\cpa-plugin`，`Remove-Item -Recurse` 差一步删整仓；删临时目录前必须先断言路径匹配 `\\cpa-shim-[a-z0-9_]+$`。

- **当前会话（2026-11-28 22:54 - 23:40）：glm-provider 0.2.7/0.2.8：修复授权 40004 与提交白屏，真实账号端到端跑通**（用户贴令牌与白屏截图「提交了一个token, 但是没有成功回调」）：
  - **两个串联故障**：① 授权页提交后整页白屏；② 令牌校验失败 `refresh status 400: 40004 bad request`。
  - **根因 1（白屏）**：表单以 `POST` 打 `/v0/resource/plugins/glm-provider/login/callback`，而 resource 路由只注册 GET → 404 空响应体 → 前端 `document.open();document.write("")` 把整页覆盖成空文档。**白屏不是没渲染，是被空体覆盖**。
  - **根因 2（40004）**：`X-Device-Id` 发的是随机串，上游要求它等于令牌 JWT 载荷里的 `device_id`。六组单变量探针定死：随机→`400/40004`、载荷值→`200` 成功；去签名头→`400/40011`（签名是另一条独立必需项）。
  - **根因 3（对话同源）**：修完刷新后对话仍 `40004`，`prepareStreamRequest` 同样写 `refreshHeaders("", sign, newNonce())`。C1/C2 探针复现同一对比 → `device_id` 一致性是**整站级**要求。
  - **修复**：新增 `deviceIDFromToken`（0.2.8 由 `deviceIDFromRefreshToken` 泛化）从 JWT 载荷取 `device_id`，刷新与对话链路共用；解析失败在发请求前给可行动中文错误。授权页改 `GET` + `X-GLM-RefreshToken` 头（同源无预检、令牌不进 URL 与历史），并加 `!res.ok||!res.body` 兜底避免清空页面。
  - **测试**：夹具改 JWT 形 `fakeToken(deviceID, kind)`；新增 `TestDeviceIDFromToken`（四边界）、`TestRequestTokenRejectsMissingDeviceID`、`TestPrepareStreamRequestUsesTokenDeviceID`、`TestPrepareStreamRequestRejectsTokenWithoutDeviceID`，并在 `TestRequestTokenMock` 断言 `X-Device-Id` 来自载荷。单测 15→19 全 PASS，gofmt 18 文件 PASS。
  - **发布与部署**：0.2.7（刷新+白屏）与 0.2.8（对话）各走一轮 CI（`46157623948` / `46158978629`，Release 均 success、`ALL CHECKSUMS OK`、5 平台）→ registry → 生产 `hot reloaded active=0.2.7` 再 `0.2.8`，容器未重启。
  - **真实端到端（TASK-007 关闭）**：授权「账号 glm Ji4HOg 已导入」→ `/accounts` 1 个账号 `disabled=false/test_failed=false` → 非流式「你好你好你好」→ 流式 `reasoning_content`+`content` 分帧正常 → `glm-think`/`glm-5.1` 均 200。
  - **教训**：①「提交后白屏」第一嫌疑是回调响应为空，不是 HTML 错；curl 用 GET 能拿到完整页面会误判「接口通」，**方法本身**必须核对；② 伪装头里凡能与实物对上的（如 `X-Device-Id`）都不能伪造，要从凭据派生，造不出就查协议；③ 同类校验常被多端点共享，修完一处必须扫全部调用方（`grep X-Device-Id` 只 1 处，但 `refreshHeaders` 有 2 个调用点都传了随机值）。

- **当前会话（2026-11-28 22:23 - 23:05）：safeguard-killer 0.1.13：会话绑定泄漏修复 + 未使用功能移除（本地已提交，未发布）**（用户「Safeguard Killer插件, 持续监控一下bug和不需要没使用的功能移除。」）：
  - **巡检手段**：`deadcode`（结论作废：c-shared 插件入口是 `//export cliproxyPluginCall` 而非 `main()`，全量符号报 unreachable 属假象）→ `staticcheck -checks all`（命中 1 条真 U1000 + 1 条 ST1005）→ 生产取证（`docker logs` 6h 无 panic/unknown_method/not_supported/[error]；`/v0/resource/plugins/safeguard-killer/state` 19508 B、60 条决策全 `served`、8 条会话）→ 宿主契约取证（`internal/pluginhost/executor_route.go`、`sdk/api/handlers/handlers_model_router_test.go` 的 `plugin_executor_count_tokens`）。
  - **BUG-1 过期会话绑定无人清理**：`pruneSessions` 自 0.1.7 精简后零调用（staticcheck U1000 直接命中），过期只在「同 key 再次被访问」时由 `groupCandidateOrder` 懒删 ⇒ 不再被访问的会话 key 永久驻留 map（≈200 B/条），且 `/state` 每 10s 全量返给面板。修法：新增 `sessionPruneInterval=5min` 节流 + `pruneSessionsThrottled`，挂到 `bindGroupSession` 写入路径；`buildUIState` 持锁后先 prune，并只回 `IdleExpiresAt` 未过期的绑定（保留 24h `sessionRetention` 供 `buildGroupNotice` 的「上一轮由谁服务」提示）。
  - **BUG-2 成员模型名 count_tokens 被判未知分组**：宿主对 count_tokens 与对话共用 `model.route`，路由链 `resolveTargetGroup` 认成员模型名、计数链 `cfg.group` 只认分组代号。修为 `resolveTargetGroup(cfg, req.Model, "")`。
  - **未使用功能移除**：`goAfterStartupJitter` / `waitStartupJitter` / `startupJitterMax`（本插件无 scheduler、无定时器，三者零调用，staticcheck U1000）+ 两个只测它们的用例；`withModel` 内部重复 `sanitizeRequest`（且用 `currentConfig()` 覆盖本次请求口径）改为只改 `model` 字段。保留 `init()` 内存软上限与 `CPA_PLUGIN_MEMORY_LIMIT_MB` 覆盖（仍必需）。
  - **验证**：门禁 `cgo-shim-build.py safeguard-killer` build/vet/test 全绿（`PASS=58 FAIL=0`，0.28~0.32s）；gofmt CLEAN（CRLF→LF 临时副本）；**4 条哨兵变异逐条 KILLED**（删清理调用 / 清理空实现 / 去状态过滤 / 退回 `cfg.group`）。
  - **登记未修**：`claudeStreams.states` 无 TTL（只靠 `request.complete` 回调清理）、`handleStreamIntercept` 在 `ChunkIndex=-1` 不 flush 残余缓冲（流被截断时丢内容）、`stream.go:79` ST1005 错误串首字母大写（既有对外文本，非必要不改）。
  - **文档**：`doc/4-bugs/2026-11-28_223000_safeguard-killer会话绑定泄漏与count-tokens口径根因与修复.md`、`doc/5-tests/2026-11-28_224500_safeguard-killer0.1.13会话清理与口径收敛代码级验证.md`、`doc/6-review/2026-11-28_230000_safeguard-killer0.1.13_6-review.md`。
  - **发布（已完成）**：commit `82f4399` → tag `safeguard-killer-v0.1.13` 触发 CI `46156063595` 但 **Release skipped**（darwin/arm64 cancelled，tag 路径无 allow_partial）→ 追加一次性 dispatch 工作流（仓库内置 `GITHUB_TOKEN`）触发 `46156785282`（allow_partial=true）→ **Release success**（5 平台，缺 darwin_arm64）→ 资产 `ALL CHECKSUMS OK` → `publish-assets --allow-missing` → registry commit `de6741a` → 生产 install 200 / `restart_required:false` → **`hot reloaded active_version=0.1.13 retired_version=0.1.11`**（23:04:26）→ 落盘 `.so` sha256 `043fa063…57ffd` 与本地 zip 逐字节一致 → 临时工作流删除 commit `23dd7d8`。
  - **生产行为验收**：① `count_tokens` 用成员模型名 `deepseek-v4.1-flash` → `200 {"input_tokens":2}`（0.1.11 会报 `model group … is not configured`）；② `state` 由 **58163 B**（200 决策 + 9 条会话含过期项）降到 2076 B，5 分钟后 16680 B（55 决策 + 3 条会话全未过期），23:09:42 过期那条已从 `sessions` 消失；③ 6 分钟日志无 panic/unknown_method/[error]，容器未重启。
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

> 更早的会话明细（2026-11-20 及之前：store-auth PAT、10 插件面板、doubao 0.1.8/0.1.9/0.1.11/0.1.19~0.1.22 等）已归档至 git 提交历史与 doc/ 对应文档，本文件只保留最近 7 条会话摘要以控制体积。
