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
  - `traework-provider`: **0.2.13**（面板签到时刻文案漂移修正为 00/04/08/12/16/20；0.2.12 定时探活降频至 4 小时 + 失败也刷新节流时间戳；0.2.11 max_tokens 32768 底线 + payload 层同步，探活改全量模型按序测试）
  - `workbuddy-provider`: **0.20.0**（0.20.0 新增连续使用轮换：单账号连续承流满 60 分钟让位、冷却 60 分钟，默认开启，含全冷放开一半防抽干；0.19.0 重新接入成长中心 7 段，修复旧版「任务永远完不成」的两个根因：从未调用 `buddy/first` 领养 + 完全没有 `POST /v2/report` 行为上报；0.18.2 面板单卡刷新补写积分快照；0.18.1 积分三档分级 + 每小时串行扫描 + 低于 100 硬禁用；0.18.0 下线 Global 耗尽自动删号 + 低额红线可配置；0.16.5 流式中途断流零泄漏换号重试 + 断流信封补 `http_status=502`）
  - `workbuddy-ai-provider`: **0.1.28**（0.1.28 面板不再自动刷新：打开只渲染内存缓存/盘上积分快照，最新积分由卡片「刷新」显式触发；连带修复手动刷新与签到不落盘、缓存 3 分钟过期 GC 掏空活账号 plan/签到态、默认渲染对无 plan 账号查上游注册态三处缺陷。0.1.27 积分快照并入白名单搬运路径修复备注刷新抹快照；0.1.26 积分快照持久化 `credits_snapshot`；0.1.25 移除耗尽标签与按额度排除；0.1.16 授权后自动补注册地）
  - `copilot-provider`: **0.1.2**（0.1.2 调度器对齐宿主候选集语义修复越界缺陷，已发布 `0054c28`→`ba471b0`→`bca1264`→`78b4069` 并生产验收：热重载前 5 分钟 50/50 请求告警、热重载后 0/57；0.1.1 宿主 OAuth 弹窗改开插件桥接页展示设备码；0.1.0 首发：Device Flow 登录 + 内部令牌 80% 提前保活 + VS Code Copilot Chat 指纹模拟 + 动态模型发现 + 多账号池换号重试 + 流式/非流式转发 + 额度与面板）
  - `workbuddy-token-usage`: **0.2.8**（0.2.8 让退役实例的 feed 导入协程在连续 3 轮关闭句柄后自行退出；0.2.7 修复宿主配置重载换句柄窗口导致的用量记录丢失；0.2.6 移除四个图表与导出图片、下线 `/stats/trends` 及序列聚合以省算力与响应体积；0.2.5 起请求明细与维度汇总展示原始 provider id）
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-10-17 (GMT+8)

## 活动会话进展摘要

- 当前会话（2026-10-25）：**生产 traework-provider 17 个账号误删后从 MySQL binlog 完整恢复，已启用并端到端验收**：
  - **事故**：生产 spool 与 `auth_store` 的 traework 记录均为 0，插件侧无 `traework_accounts` 目录。binlog 定位到 **17 条 DELETE 事件**，三批：10-20 15:34（3）/ 10-21 15:11（4）/ 10-22 12:58~12:59（10）。
  - **恢复依据**：`log_bin=ON` + `binlog_row_image=FULL` + 保留 24h，`binlog.000017` 覆盖 10-16 至今。**恢复前先备份 binlog 本体**（921MB 本地副本），防 24h 轮转清理。
  - **解析**：容器无 `mysqlbinlog`、宿主无 `pip`，改用纯 Python 直解；踩中三个偏移陷阱（事件头 `<IBIIIH>`、table_id 为 6 字节、二进制 JSON 列的偏移基准与 value entry 字段顺序），靠「已知 uid 反查」自证后才产出可信结果。
  - **还原点（用户要求「出错可还原」）**：`PRE_RESTORE_auth_store_20261025_134113.sql`（全表）+ `PRE_RESTORE_spool_20261025_134113.tar.gz`（目录快照）+ `PRE_RESTORE_baseline.txt`（基线 spool_traework=0 / db=0），均在 `/root/traework-recovery/`。
  - **凭据有效性**：17 个全部实测刷新，**16 个成功**（新 token 有效至 11-08）；1 个（`3313287900183084`）返回 `401/20101`，其删除前 note 已是 `Session expired: re-login required`，属删除前既有失效，非本次误删。
  - **写回与验收**：按「auth_store（hex 写 JSON 列）→ spool」顺序写入 17/17；插件原为 `enabled=false`（10 插件中仅 5 个启用），**启用后** `registered/effective_enabled` 均 true，插件 `/accounts` 17 条、宿主 `auth-files` 56→73（+17）。抽样 5 号 `test-active` 4 成功（0.9~2.6s）、1 号报上游 `all models failed`（模型级非账号故障）；真实入口 `/v1/chat/completions` 调 `glm-5.3` 返回 `finish_reason=stop` 正文 `OK`。
  - **边界**：恢复出的 `disabled` 统一为 false，删除前的禁用态未单独保留；解析器与恢复脚本留在 `/root/traework-recovery/`（`recover.py`/`apply.py`），未纳入仓库。

- 当前会话（2026-10-25）：**qoder-ai 0.1.30 修复模型清单漏模型（多场景并集 + 上游 display_name 权威 + 场景别名排除），已提交并触发发布**：
  - 用户诉求：「对 qoder-ai 插件做同样的修复」（承接 qoderwork 0.10.1）。
  - **上游取证（真实国际版账号直连 `openapi.qoder.sh`）**：响应按 11 个场景切分（chat / assistant / quest / qwake / app / inline / qwork / experts / nap / byok_teams / byok_enterprise）；`chat` 仅 12 个 key，全场景并集 21 个。本机官方客户端 QMC 加密目录（`~/.qoder/.models/<uid>/catalog-v6|v7`，HKDF-SHA256(uid)+AES-256-GCM，参考 qoder2api-hub `qoder_sign.qmc_decrypt`）解密后同样证实按场景切分（chat 15 / qwork 6 / inline 4）。
  - **与 qoderwork 的关键差异（不能照搬）**：国际版把档位嵌进场景 id（`quest-auto` / `quest-ultimate` / `qwork-auto` / `qwork-advanced` / `qwork-ultimate` / `experts-auto` / `experts-ultimate` / `nap-auto`，display 分别只是 Auto/Standard/Premium/Advanced），共 8 个伪模型。国内版实测 chat 与并集完全一致（各 11 个），故 qoderwork 直接取全集即可；国际版必须 `isSceneScopedAlias` 按前缀排除。真正漏掉的只有 `performance`（只在 `qwake` 出现）。
  - **命名与回译**：上游 `display_name` 已改名（`kmodel_latest`→`Kimi-K3.1-Pro`、`mmodel`→`MiniMax-M3.1-Flash`）而硬编码表仍是旧名。主条目改用上游 display_name；新增 `upstreamModelNames` 运行时映射供 `cpaToUpstreamKey` / `resolveUpstreamModel` 回译（先到先得，账号级 alias 最高优先）。
  - **静态表**：补入双区客户端目录都有、插件此前完全没收录的 `fast`；静态表**只保留旧名代号**——新名一律交给运行时映射，否则静态表会遮住运行时映射（变异验证发现：硬编码新名会让「移除运行时回译」这个变异存活）。
  - **验证**：新增 `models_scene_union_test.go`（12 用例）；**5 项变异全部 KILLED**（退回只读 chat / 主条目忽略 display_name / 移除场景别名过滤 / 移除运行时回译 / 静态表加回上游已无代号）；cgo-shim build+vet+test 全绿；gofmt 干净。
  - **真实上游验收（改后代码）**：`13 models in 11 scenes`（改造前 12 个且只读 chat）；对外展开 26 条 ID 全部回译通过；`performance` 首次进入清单；两个改名模型由上游 display_name 驱动；静态兜底 36/36 回译通过。
  - **发布链**：`3b9596c`（fix，7 文件）已推 main（`2757f14..3b9596c`）；CI dispatch `qoder-ai-provider 0.1.30` → run `40116130945`。
  - **生产状态**：qoder-ai 当前 `configured=true / registered=false / enabled=false`，且 spool 无 `qoderai-` 前缀账号（生产从未启用该插件），故本轮只做发版与资产校验，无法做生产流量验收。
  - **未闭环**：qoder-ai 启用前的账号导入与真实推理验证；`/v1/models` 中部分模型无 provider 归属的旁证线索（见上一条会话记录）。

- 当前会话（2026-10-25）：**qoderwork 生产 Kimi-K3.1-Pro 测试失败排查：上游后端不可用，非插件缺陷，无代码改动**：
  - 用户报障（附截图）：面板测试账号 `ud5cdb65b` 的 `Kimi-K3.1-Pro` 返回 `upstream 200: provider_error: AI backends failed`。全仓 `grep` 该文案零命中，确认来自上游而非插件。
  - **根因**：Qoder 上游对 `kmodel_latest` 返回 HTTP 200 + `provider_error: All backends failed`（Kimi 后端池全挂）；同期 `serviceAvailable:false` / `subCode:RESOURCE_LIMIT` 证明上游整体资源受限，`Qwen3.8-Flash` / `MiniMax-M3.1-Flash` 表现为排队帧、`GLM-5.3-Flash` 表现为 `112` 权益不足。
  - **四项排除**：① 回译正常——官方名 `Kimi-K3.1-Pro` 与内部代号 `kmodel_latest` 同一秒同成同败；② 账号状态正常——18 号 `disabled=false`，物理 auth 文件无 `failover_disable` / `manual_disable` / `daily_cooling_until`；③ 换号链正常——真实请求轮转 9 个账号全部 `All backends failed` 后如实返回 `auth_unavailable`；④ 非账号无权益——同号 8 秒内曾成功。
  - **插件行为符合设计**：`provider_error` 不含额度关键词、也命中不了 `isQueueFrame`，故不计冷却、不写盘禁用（与物理文件一致）。
  - **独立旁证（未处理）**：`/v1/models` 中 `Kimi-K3.1-Pro` / `DeepSeek-Flash` / `GLM-5.3-Flash` 无 provider 归属，`DeepSeek-Flash` 真实请求报 `unknown provider`，与本次故障无关，待单独排查。
  - **处置**：不改代码、不重启容器、不改配置；等上游恢复后复测 `serviceAvailable` 是否回到 true。文档 `doc/4-bugs/2026-10-25_011500_QoderWork生产KimiK31Pro测试失败上游后端不可用根因诊断.md`；知识库《Qoder上游降级时的四类错误语义判别》。

- 当前会话（2026-10-25）：**qoderwork 0.10.1 修复模型清单漏模型（多场景并集 + 上游 display_name 权威），已发生产并验收**：
  - 用户报障（附截图）：对比官方客户端默认模型列表，生产的 qoderwork 模型表少了 `Kimi-K3.1-Pro` 与 `MiniMax-M3.1-Flash`，要求检查模型获取逻辑。
  - **根因两层**：① `callModelsAPI` 只读响应里的 `chat` 场景，而上游按场景切分（11 个场景，各场景模型集合不同：本机客户端目录样本 chat 15 / qwork 13 且不含 mmodel / experts 5）；② 对外模型名取自硬编码表 `internalKeyDisplayNames`，上游 `display_name` 字段虽被解析却从未使用——上游改名不改内部代号（实测 `kmodel_latest`→`Kimi-K3.1-Pro`、`mmodel`→`MiniMax-M3.1-Flash`），表名因此永久滞后。
  - **第三层风险（本次一并堵住）**：只改命名不改回译，客户端按新名请求会被透传给上游并**静默落到 `auto`**（HTTP 200、无报错、参数与计费全错）。新增运行时映射 `upstreamModelNames` 登记「官方名→内部代号」，`cpaToUpstreamKey` / `resolveUpstreamModel` 优先查它（先到先得，后到的旧清单不覆盖新名）。
  - **实现**：`exposeModelIDs` 主条目改用上游 `display_name`（硬编码表降为兜底）；`callModelsAPI` 抽出纯函数 `parseModelsResponse` 遍历全部场景取并集（有序、按代号去重、脏场景跳过）；`wbModels()` 移除 `cmodel`（已下线）与 `qmodel_latest`/`qmodel`/`q37fmodel`/`gm51model`（Free 档 `enable=false`），与生产可用集合对齐（代号映射保留，上游恢复即可用）。
  - **验证**：新增 `models_scene_union_test.go`（12 用例）；**4 项变异全部 KILLED**（主条目忽略 display_name / 退回只读 chat / 移除上游名回译 / 静态表加回已下线模型）；cgo-shim build+vet+test 全绿；gofmt 干净。
  - **真实上游验收**：用生产 spool 凭据直连 `gateway.qoder.com.cn` 跑真实 `callModelsAPI` → `10 models in 11 scenes`，`DisplayName` 确认是 `Kimi-K3.1-Pro` / `MiniMax-M3.1-Flash`，20 条对外 ID 全部回译通过。
  - **发布链**：`5f3cc57`(fix, 8 文件) → CI `40111458460` success → 6/6 assets sha256 OK + 产物 `0.10.1`×2 / `0.10.0`×0 且含 `models in` 新日志串 → `937d1d1`(registry) → 远端 6/6 一致且无旧版本残留。
  - **生产部署（热重载零重启）**：`hot reloaded active_version=0.10.1 retired_version=0.10.0`，`restart_required:false`。生产 `/models?auth_index=` 返回 20 条含两个新名；全局 `/v1/models` 已可见 `Kimi-K3.1-Pro` / `MiniMax-M3.1-Flash`（`owned_by=qoderwork-provider`）。
  - **回译生产实证**：`test-active` 用新名请求，上游排队响应回带 `"modelKey":"mmodel"` / `"modelKey":"qfmodel"` / `"modelKey":"dfmodel"`，证明新名已正确回译成内部代号。当时上游整体排队（`isQueued`），属已知容量状态非账号故障。
  - **未闭环**：`qoder-ai` 存在完全相同的「只读 chat 场景 + display_name 未使用」代码，本轮未改（按插件独立发版，需单独一轮）。文档：`qoderwork/CHANGELOG.md` 0.10.1。

- 当前会话（2026-10-24）：**coze 每日登录积分自动领取（0.5.5）+ 历史账号补注册宿主（0.5.6），均已发生产并验收**：
  - 用户诉求：「扣子插件不能每日签到领积分，看看有什么办法」→ 裁定方案 A（插件内置），并要求「把号都导入到生产」→ 明确为「启用插件让 9 个号回归路由」。
  - **上游契约（九账号对照实验实证）**：每日 1500 分「每日登录」活动积分真实存在，**有效期仅 1 天、当天不领即归零**——这是账号池长期 0 分的根因。唯一触发器是 `GET /api/marketplace/trade/credit/balance/check`：只调 token beat 续期不发、只访问 `/home` 不发、`activity/info` 与 `incentive/user_task/list` 也不发；只打 check 则 3/3 复现 0→1500，同日连打只发一条（幂等）。
  - **关键坑（初版实现曾因此做错并被推翻）**：check 响应的 `pass`/`deny_reason` 描述的是「**余额是否够用**」，与本次是否发放无关——实测 `pass=false` 的 b3f25e / f18297 同样到账 1500。到账判据只能是「明细里存在当天且仍有余额的每日登录条目」，即先 check 再查明细。`display_status`：2=当天可用、3=未来到期、4=已过期。
  - **0.5.5 实现**：新增 `daily_credit.go`（领取内核+明细确认+跨日判定）、`daily_credit_manage.go`（面板视图与手动补领）；领取必须**先于**余额查询（否则闸门读到旧余额继续判 0 分）；跨日按固定 UTC+8（不依赖容器 tzdata）；失败只记日志不阻断刷新；禁用号不领；面板加今日领取三态与「领今日积分」按钮。
  - **第二个缺陷（0.5.6）**：启用后模型能列出但请求必 503 `auth_not_found`。根因是注册只有 `writeAuthFileDirect` 里 `if auth.PhysicalPath == ""`（仅首次授权）一个入口，历史账号永不满足 → 插件能读能刷积分、宿主 spool 一条都没有（`host.auth.save` 日志零命中，对照 qoderwork 有 18 条）。新增 `POST /register-host` 把已落盘账号原样搬运进宿主 spool（只推未禁用、原样读磁盘、逐账号留痕、路径双闸门）。
  - **验证**：新增 15 个用例；**8 项变异全部 KILLED**（claim 后置 / 跨日退化 / 确认要求 passHint / 失败仍确认 / 忽略 remaining>0 等），其中「确认要求 passHint」正是初版错误语义的回归守卫。cgo-shim build+vet+test 全绿；gofmt 干净；面板两段脚本语法 PASS。
  - **发布链**：0.5.5 `1b4990b` → CI `40104373276` success → `37db359`(registry)；0.5.6 `c46ec70` → CI `40106095504` success（首轮失败于 **traework** 的 TempDir 清理 flaky，与 coze 无关，重跑后全绿）→ `dad3e94`(registry)。两轮均 6/6 assets sha256 OK、产物版本串与旧版归零核对通过。
  - **生产验收（全程热重载零重启，容器 Up 46h）**：0.5.5 后 9/9 账号 `remain=1500`、`claimed_today=true`；`/claim-daily` 返回 `claimed:true, remain:1500`。0.5.6 补注册 `success:9/failed:0`，宿主 spool coze 文件 0→9、`auth-files` coze 条目 0→9，**端到端推理 `coze/doubao-seed-2-0-lite-260215` 返回 HTTP 200「你好呀」**。
  - 文档：`doc/2-需求/2026-10-24_220000_Coze每日登录积分自动领取需求.md`、`doc/5-tests/2026-10-24_220000_Coze每日登录积分自动领取验证.md`。

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
  - 查清 `f961f2eb` 会话产出 `121931d`（README 六插件说明，纯文档）已提交；另有两处未发布修复 `5ab431d`（workbuddy-ai `backendHeaders` 补四个会话追踪头）。
  - 发布前生产事实：10 个插件只有 3 个真正启用（qoderwork / workbuddy / token-usage），workbuddy-ai 与 zcode 均 registered 但未启用。
  - 发布链：`d879fb6` → workbuddy-ai CI `39812602344` 19/19 success；zcode 首个 run 失败后修正重跑通过。生产部署零容器重启，两插件均 `hot reloaded` 生效，落盘 `.so` 哈希与本地一致。
  - 边界：两插件仍处禁用状态，本次仅完成「版本就绪 + 落盘」，不代表能力已上线。

- 更早会话（2026-10-21，已按 51200 字节上限压缩，完整记录见 git log）：
  - **coze 0.5.4 全池积分耗尽快速失败**：0.5.2 闸门「全池低额则放行」在整池 0 分时等于把请求交回宿主逐个白打上游（四个时段每个 request_id 轮满 7 个号）。新增「整池确定 0 分」通道返回 400（命中宿主请求级白名单，立即终止换号且不冷却），判据只认 `remain==0` 且要求本号与池内每号积分数据都在 15 分钟可信窗内。生产用 413 分支做同链路对照实证；400 分支自身上线时未在生产触发过。遗留待办：token-usage-tracker 把管理密钥当 source 打印到日志需修；coze 的 `min_routable_credits` 未写入生产配置，走默认 100。

- 更早会话（2026-10-14 ~ 2026-10-20，已按 51200 字节上限压缩，完整记录见 git log 与 PROJECT_HISTORY.md）：
  - **coze 系列 0.4.6 → 0.5.4**：64KB content 超限返回 413（防整池 503）；0.5.2 积分路由红线（执行入口闸门，非 scheduler）；0.5.3 生图收口统一 `awaitImages` + 4 分钟上限 + 缺图返回 400（止血额度被烧）；0.5.4 整池确定 0 分快速失败 400（零上游消耗）。
  - **定时任务并发治理**：6 插件 4h 槽位改 `next*FireTime` + 5m 可注入抖动；traework 签到重试收敛并发 4；六个 watchdog 首轮加抖动；唯一无界扇出是 coze `/accounts`。
  - **账号故障冷却基数 30s → 60s**：线性递增、上限 10 分钟、cap 10 档。
  - **Qoder 系模型发现风暴与 OOM 修复（0.9.47 / 0.1.27）**：GET 真携带 `qoderEncode("{}")` 修复恒 403 Signature invalid；补负缓存 5min；`pickNextAuth` 50ms 退避 + 错误体截断。0.9.48 / 0.1.28 缓存按账号分片 + 额度优先排序。0.9.49 / 0.1.29 对外 ID 改官方展示名 + 执行链回译。
  - **qwenwork-provider 立项与多账号**：0.1.0 Device Flow + PKCE → MaaS 短票据（TTL 2h）；CYCLE-02 修账号身份 key 失配；发布矩阵下线 freebsd-amd64（平台 7 → 6）。
  - **coze 0.2.0~0.3.6**：模型动态发现 + 面板重做；定时续期改插件自建定时器；真流式落地（WS 心跳/退避重连、`turn_id+seq_num+phase` 去重、`WaitReady` 精确配对）；修 `/v1/responses` 503（Payload 优先）。
  - **五插件签到节奏统一每 4 小时**；**zcode 中国区与额度领取（0.1.22→0.1.31）**、**copilot-provider 立项与 0.1.0/0.1.1**（宿主 OAuth 弹窗改桥接页）、**Kiro 裁定不立项**；**max_tokens 官方规格自适应 + 32768 底线**；**Qoder 110 日上限 24h 持久化冷却**；**排队帧不判账号故障**（403:10605 isQueued）；**删除 Gemini 插件**；**Token 用量面板移除 4 图表**。

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
  "registry_updated_at": "2026-10-25T06:17:48.595464Z",
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
      "state": "active",
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
          "status": "in_progress"
        },
        {
          "id": "TASK-008",
          "step": "[TASK-008] 下线 zcode 内存冷却状态机，路由判据改为读写盘禁用，失败分类保留为唯一判据",
          "status": "pending"
        },
        {
          "id": "TASK-009",
          "step": "[TASK-009] 让 zcode 额度快照带上每档 period_end 与账号级最早重置时刻，先用真实 balance 响应验证该字段存在",
          "status": "pending"
        },
        {
          "id": "TASK-010",
          "step": "[TASK-010] 让 zcode 额度耗尽写盘禁用，并在越过重置时刻且探活成功后才解禁",
          "status": "pending"
        },
        {
          "id": "TASK-011",
          "step": "[TASK-011] zcode 收口发布：本地 E2E 验证四条链路、版本升到 0.2.0、补 CHANGELOG、6-review 风格回归",
          "status": "pending"
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
