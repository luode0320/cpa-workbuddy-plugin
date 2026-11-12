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


- 会话（2026-11-12 22:05-22:52）：**workbuddy 0.21.26：导入原生兼容第三方 `auth_raw`/`profile_raw` 记录（已发布 + 生产验收通过）**：
  - **用户诉求**：先报「work10.json 导入到 wokrbuddy 失败了」，裁定「改成可导入的给我们手动导入就可以了」拿到转换产物后，进一步要求「把 wokrbuddy 的导入兼容一下这种格式的 json，下次我们就不用手动转换手动导入了」。
  - **根因（非插件 bug）**：第三方导出文件是**顶层数组 + `auth_raw`/`profile_raw` 嵌套 + `access_token` 蛇形命名**，而 `workbuddy/main.go:510 parseStored` 只认「顶层有 `auth` 键的嵌套形态」与「扁平 `accessToken`」，两者都不匹配 ⇒ 逐条 `parse_error: missing accessToken`。`panel.html:1639 expandCredentials` 会把顶层数组展开成 10 条，但每条仍无 `accessToken`。
  - **实现（6 文件 +267/−6）**：① 新建 `workbuddy/auth_thirdparty.go`——`thirdPartyRecord`、`isThirdPartyStored(probe)`（认 `auth_raw`/`profile_raw`/`access_token` 任一键）、`parseThirdPartyStored(raw)`、`epochsToSeconds(v)`（`>1e11` 判毫秒则 `/1000`）；② `main.go:510 parseStored` 改为**三分支分发**（嵌套 `auth` / 第三方 `isThirdPartyStored` / 扁平 `accessToken`），末尾统一校验 accessToken；③ `panel.html:266` 导入提示补第三方形态；④ `VERSION` + `main.go:359 var version` = **0.21.26**、`CHANGELOG.md` 加 0.21.26 节；⑤ 新增 `test/workbuddy/auth_thirdparty_test.go`（5 用例：第三方记录 / 令牌回落 / 嵌套与扁平不受影响 / 缺令牌报错 / 毫秒换算）。
  - **为什么不改 panel 的展开逻辑**：`expandCredentials` 三条既有分支已足够；转换收敛在 `parseStored` 一处，导入、`handleParseAuth` 认领、`handleExecExecute`、导出回读等 10 个调用点全部自动受益。
  - **验证**：`cgo-shim-build.py workbuddy` build/vet/test 全绿（34.078s）；真实数据探针把 `work10.json` 10 条逐条过 `parseStored` 输出 `verified 10/10 credentials`（uid/昵称/`expiresAt=1796910717` 秒级/`domain=www.workbuddy.cn`/`atLen=1539`/`rtLen=1540` 全对）；gofmt 干净；panel 两段脚本 `node --check` exit=0；四个文件 `bom=False`。
  - **发布链**：`fabbed3`（代码 6 文件）→ CI run **43238620141**（`allow_partial=true`）`Release` job success、13 个 Test job 全绿（`windows-arm64` 失败 + `darwin/arm64` cancelled 为踩坑 44 已知形态）→ 4 assets `ALL CHECKSUMS OK` → `publish-assets.py --allow-missing` → registry `adf788c`（4 artifacts）。
  - **生产（热重载零重启）**：`POST /v0/management/plugin-store/workbuddy-provider/install?version=0.21.26` → `restart_required:false`；`hot reloaded active_version=0.21.26 retired_version=0.21.25`；落盘 `.so` 8,466,976 B sha256 **`fca9f1550424b2136f7e58f0a01ef8e0e99f526c98048669620637ee54a70cca`** 与本地 release zip 逐字节一致，`grep -a -c auth_raw` = 3；面板 `/v0/resource/plugins/workbuddy-provider/panel` 200 / 86919 B 含 `auth_raw`；账号池 **30 个**，work10.json 的 10 个 uid **10/10 已在生产**。
  - **生产行为验收（决定性）**：取 `work10.json` 中 uid `399529bf-4251-4f08-b9a8-c0ca053c4b3d` 的**原始未转换记录**直接 POST `/import` → 200 `success:true`；落盘 `workbuddy-399529bf-….json` 3469 B，`auth keys=['accessToken','domain','expiresAt','refreshToken']`、`expiresAt` 为 **int 1796910717（毫秒已 /1000）**、`domain=www.workbuddy.cn`、`atLen/rtLen=1539/1540`、`account.uid` 正确、`note=CN · 积分未知`、**`auth_raw` 残留 False**。
  - **新踩坑**：① `registry.json` raw CDN 这次滞后 **4.5 分钟**（14 次重试全旧版），改用 GitHub Contents API（`/contents/registry.json?ref=main` + base64）才读到 0.21.26；② auth 文件在**容器内** `/root/.cli-proxy-api/`（宿主同名路径不存在），且容器内无 `python3`，需 `docker exec cat > /tmp/x.json` 拉到宿主再解析；③ 生产管理密钥在 `config.yaml` 第 **479** 行 `workbuddy-provider` 段的 `cpa_management_key`（9 字节明文），`remote-management.secret-key` 取不到（`rm_key_len=0`，用它 401）。
  - **遗留**：`darwin_arm64` / `windows_arm64` 平台缺件（同 0.21.25，按 `--allow-missing` 剔除槽位）；`doc/5-tests/` 与 `doc/6-review/` 未为本次改动补验证记录与风格回归记录。

- 会话（2026-11-12 21:09-22:10）：**qoderwork 0.13.17：面板「测试」把上游容量排队（10605 排队帧）与账号故障分开（生产已生效）**：
  - **用户诉求**：贴生产面板截图 + 报错 `活跃测试失败 (模型: Qwen4-Max-Preview): upstream 200: 403: 10605: {"isQueued":true,"modelKey":"qmodel_latest","queueCount":1,"queueType":"p4","retryAfterSeconds":60,"serviceAvailable":true,"subCode":"RESOURCE_LIMIT","waitTime":60}`，问「QoderWork 测试 Qwen4-Max-Preview 失败」。
  - **根因（非账号故障）**：该响应是上游**容量排队帧**（HTTP 200 + 顶层 `code=403` + 内嵌 10605/`isQueued`，`serviceAvailable=true`），不是凭据失效或账号坏。failover 侧早已正确拦截（`accountFailover.go:274` `isQueueFrame(body) → return false`，不计数不冷却；`failover_disable.go` 排队帧不触发禁用），**缺陷只在用户呈现层**：`sendActivePingQoder` 把排队帧当普通错误抛，面板只能显示红色「测试失败」+ 截断 200 字符的原始 JSON 转义串（`&#34;`），用户无法分辨「上游拥堵」还是「账号坏了」。
  - **模型映射确认**：仓库内无 `Qwen4-Max-Preview` 硬编码映射，它走 `lookupUpstreamKey` 运行时映射（上游 `display_name` → 内部代号），上游把该官方名挂在 `qmodel_latest` 代号上，故报错里 `modelKey=qmodel_latest` 属正常。
  - **实现（3 文件 + 1 新测试，+223/−14）**：① `policy.go` 新增 `type queueFrameInfo`（8 字段与上游原帧同名）、`waitSeconds()`（`retryAfterSeconds` 优先、回退 `waitTime`）、`parseQueueFrameInfo(body string) (queueFrameInfo, bool)`（对每个 `{` 起点用 `json.NewDecoder` 试解，规避 `upstream 200: 403: 10605: {...}` 前缀不能整体 Unmarshal）；② `active_ping.go` 新增 `*queueFrameError`（`Error()` 文本**逐字不变**，供历史日志对照）+ `queueFrameNotice`（中文可读提示）+ `buildActivePingFailure`（`errors.As` 命中即返回 `upstream_queued`/`detail`/`retry_after_seconds`/`queue_type`/`queue_count`/`sub_code`/`upstream_model`），`sendActivePingQoder` 的两个错误返回点先判排队；③ `panel.html` 结果渲染改**三态**（成功 ok / 排队 warn / 失败 err）+ 新增 `.r-hd.warn`/`.r-warn` 样式（`.toast.warn` 本就存在）。
  - **键名决策**：用 `upstream_queued` 而非 `queued`——本插件 `credits_handler.go:233` 与 `management.go:255` 的 `queued` 是「后台刷新排队数」，同名会混淆。
  - **不做自动重试**：上游有 110 日调用次数上限（自动探活已于 2026-10-12 移除），且 `defaultActivePingTimeout=20s` 装不下 60s 等待 ⇒ 只做「明确排队中 + 可重试」提示，不引入换号/等待逻辑；failover 语义零改动。
  - **验证**：`cgo-shim-build.py qoderwork` build/vet/test 全绿（14.4s）；新增 7 测试 + 4 项既有排队帧回归全 PASS；**哨兵** `TestZZSentinelQueueFrameCompiled` 证实新测试真进编译（FAIL + `SENTINEL-ALIVE: waitSeconds() = 60`）；**变异**（把 `parseQueueFrameInfo` 命中条件改恒假）触发 shim FAIL；gofmt clean；panel.html `new Function()` `blocks=2 bad=0`；`panel_batch_import.mjs` / `panel_batch_import_e2e.mjs` 既有回归全通过。
  - **文档**：`doc/4-bugs/2026-11-12_214500_QoderWork测试按钮把上游排队当账号失败根因与修复.md`、`doc/3-实施/2026-11-12_214500_QoderWork排队帧与测试失败分离_实施总览.md`、`doc/5-tests/2026-11-12_214500_QoderWork测试按钮排队帧与失败分离验证.md`（含生产验收与发布记录）、`doc/6-review/2026-11-12_215000_QoderWork排队帧与测试失败分离_6-review.md`（`STYLE: PASS`）。
  - **发布**：提交 `7c4527c`（11 文件 +815/−16）→ CI run `43231575197`（`allow_partial=true`）→ Release job success，13 个 `Test (<dir>)` 全绿，失败仅 `windows-arm64 (go-cross)` STEP 5 与 `darwin/arm64` cancelled（踩坑 44 已知形态）→ tag `qoderwork-provider-v0.13.17`（id 443029174，5 assets）→ `ALL CHECKSUMS OK` → registry `0791998`（4 artifacts，`--allow-missing` 剔除 darwin_arm64/windows_arm64）→ 远端 raw registry 已读到 0.13.17。
  - **生产（热重载零重启）**：`plugin-store/qoderwork-provider/install?version=0.13.17` HTTP 200 `restart_required:false`；`hot reloaded active_version=0.13.17 retired_version=0.13.16`；落盘 `.so` sha256 `911c983e…34725d8`（8363872 B）与本地 zip 内 `.so` 逐字节一致；面板 `87771` 字节 sha256 `13965adc…e906705b` 与本地 `panel.html` 完全一致（含 `upstream_queued`、`r-hd.warn`）；账号池 5 个全启用。
  - **生产行为验收（决定性）**：`POST /test-active {"auth_index":"4afdceb9ea8cb1de","model":"Qwen4-Max-Preview"}` → HTTP 200 / 0.323s，返回 `{"error":"上游排队中 (模型: Qwen4-Max-Preview，队列 p4，前方 1 个请求)，约 60 秒后可重试；账号与凭据正常，属上游容量排队而非测试失败","upstream_queued":true,"retry_after_seconds":60,"queue_type":"p4","queue_count":1,"sub_code":"RESOURCE_LIMIT","upstream_model":"qmodel_latest","detail":"upstream 200: 403: 10605: {...}"}`；插件日志 `[qoderwork] manual active ping test queued for model Qwen4-Max-Preview: ...`。改造前同请求返回红色「活跃测试失败」。
  - **同族未同步（需当轮授权）**：`qoder-ai/active_ping.go` 与 `qwenwork/active_ping.go` 结构同构，可逐函数照搬；`workbuddy`/`workbuddy-ai` 的探活只读首包 512 字节即判成功（排队帧会被误判成功），属另一独立问题。本轮按跨项目写入红线未动其它插件目录。
  - **发布脚本踩坑**：`scripts/download-release-assets.py` 的参数顺序是 `<version> [plugin]`，写成 `qoderwork-provider 0.13.17` 会 404。

- 会话（2026-11-12 01:14-02:18）：**safeguard-killer 0.1.5 发布：成员模型真实名称直接进入智能分组（生产已生效）**：
  - **用户诉求演进**：先问「workbuddy 的 cpa 插件有破甲版本吗」→ 装好后报「Safeguard Killer 不生效」→ 诊断出根因后裁定「改成只要配置模型名称，只要是这个模型的请求就进入这个插件」。
  - **诊断（含一次自我纠错，重要教训）**：初判「三个破甲常量被抹成空操作」**错误**——`claudeCyberGatePrompt`(459 字符)/`Replacement`(323)、`RefusalRetryNudge`(224)/`Replacement`(91)、`ToolNotRun`(82/77)、`ToolInterrupted`(155/136)、`Fable51`(226)/`Fable5`(167) 全部互不相同且非空，替换逻辑完整有效。误判根因是**同一文件被 `read` 工具 / `Get-Content` / `git show` / Python 读出四种不同内容**（工具渲染污染，三次采样 sha256 恒为 `ECAEDC39…`/55421 字节）。**裁决铁律**：字面比对一律用 Python 读原始字节做布尔断言，不信任多层工具转述。
  - **真实根因**：生产分组配置 `id: "1"`（不是模型名），成员是 11 个真实模型名；旧 `resolveTargetGroup` 只认分组 id → 客户端请求真实模型名全部漏过 → 面板「最近决策」恒为 **0**，`fallback_on_refusal` 一次未执行（四个全请求级开关仍在工作：剥污染头 / auto-mode 旁路 / 剥 system-reminder / 禁上游 fallback）。
  - **实现（`groups.go` +56）**：新增 `groupServing`（按成员名反查分组）+ `rotateModels`（链路旋转到请求模型开头、其余环绕）；`resolveTargetGroup` 在 id 未命中后增加成员名分支。语义=请求哪个成员就先用哪个，被拒/失败/低配额才滚下一个。
  - **递归安全性（宿主源码级证据，非推测）**：克隆 `router-for-me/CLIProxyAPI` 确认 `internal/pluginhost/host_callbacks.go:445-446` 把发起插件 id 同时写入 `SkipRouterPluginID`/`SkipInterceptorPluginID`，`sdk/api/handlers/handlers_routing.go:365,380` 据此跳过自身；`handlers_model_router_test.go:542` 有 `"was re-entered despite SkipRouterPluginID"` 断言守护。`executor_route.go:85-92` 注释明确 "without changing the requested model"。
  - **验证**：cgo-shim build/vet/test 全绿；4 个新测试（含 `TestProductionStyleGroupEntersByMemberModel` 用生产同形数据）；哨兵 `TestSentinelGroupsZ` 确认进编译；2 组变异各被 3 条用例捕获；gofmt clean。
  - **发布链**：commit `7c6da39`（6 文件 +156/-6，**路径限定提交**防并行会话污染）→ 本地 push 被 reject 因远端已前进到 `bb77f12`，`git merge-base --is-ancestor` 验证远端已含我的提交（并行会话 push 携带），**无需重推** → CI dispatch `allow_partial=true`，`Release` job success + release 实体 5 资产（`darwin/arm64` cancelled、`windows-arm64` 失败，已知平台缺件）→ 4/4 checksums OK → `publish-assets.py --allow-missing` → commit `e73eeea` → 远端验证 4/4 size+sha ALL PASS、无 0.1.4 残留。
  - **生产部署**：`plugin-store/safeguard-killer/install?version=0.1.5&source=source-636fa01121af`（多源必须带 source，踩坑 60；源 id 从 `GET /v0/management/plugin-store` 现取，勿硬编码）。落盘 `.so` sha256 `ffc83bb4…b2d2ee` 与本地 zip **逐字节一致**；`hot reloaded active_version=0.1.5 retired_version=0.1.4`；容器 Up 自 2026-11-11T14:17Z、restarts=0（未重启）。
  - **生产行为验收（决定性）**：请求成员 `DeepSeek-V4-Pro`（非流式「只回复：收到」）→ 插件接管并链式切换：`DeepSeek-V4-Pro`(11102 上游无此服务) → `Doubao-Seed-2.1-Pro`(model_not_found) → `GLM-5.3`(10605 排队) → `GLM-5.3-Flash`(11102) → **自动切到 `deepseek-v4.1-flash` 成功返回「收到」，finish_reason=stop**；响应含 `⚡ [Safeguard Killer] ` 通知。面板 decisions **0 → 13**。
  - **遗留（用户侧配置）**：分组 11 个成员中 3 个模型名在宿主 `/v1/models`（共 50 个）中不存在——`Doubao-Seed-2.1-Pro`（doubao-provider 已于 2026-10-22 下线）、`hy4`、`qwen3.8-flash`（真实名 `qwen3.8-max`）。这些名在链路里只会白耗一次失败尝试，建议删除或改名。`DeepSeek-V4-Pro` 在列表内但上游报 11102（账号侧无该服务），与插件无关。

- 会话（2026-11-12 00:52-01:45）：**会话压缩阶段异常归因 → content_filter 归入失败分类（workbuddy 0.21.24，未发布）**：
  - **用户提问演进**：先报「session-3304f06f 到压缩就出问题」→ 追问「所以还是超时的问题是吗？」→ 裁定按 traework 0.5.48 的做法把 content_filter 归入失败分类。
  - **归因（含自我纠错）**：初版判定「压缩来得太晚导致超时」**被数据推翻**——该会话 `inputTokens>75 万` 有 **108 次**、`>60 万` **313 次**绝大多数成功；turn 22 step 51-63 连续 13 步在 **795,749~800,068** 输入下全部成功；`idle timeout` 全程仅 **3 次**且每次被 `llm/retry` 当场救回，其中一次发生在 input 仅 **655** 的请求上，与规模无关。
  - **真实主因（C6）**：`Response incomplete: content_filter` **42 次**，占全部 `finish=error` 的 **84%**（42/50），覆盖 **31/37** 个 turn，时间跨度 2026-11-10 23:24 → 11-12 00:55。**42/42 已产出正文**（14 次含 tool-call）——活干完了，末帧被判违规。文案来自上游 `incomplete_details.reason`（`pi-ai/.../openai-responses-shared.js:687-702`）。
  - **压缩本身 3/3 全部成功闭合**（seq 160/173/4326，无 `error` 字段），与失败无因果；`shadowedRange` 倒序是 surface 端点 seq 的合法语义，非缺陷。
  - **实现（4 文件 + 1 新测试）**：① `policy.go` 新增 `isContentModerationFailure`（三层形态 + 精确排除长度截断 + **防误判正常正文**）；② `failure_class.go` 新增**第 0.5 步**归 `failureScopeModelRateLimit`，排在账号级 4xx 之前（traework 0.5.46 未生效的根因）；③ `stream.go` 流式 emit 前记账 + `contentEmitted=true` 禁换号，非流式出口补记；④ `accountFailover.go` 修掉 scope 写死 `AuthModel`（会写盘禁用健康号）。
  - **验证**：`cgo-shim-build.py workbuddy` build/vet/test 全绿；**3 组变异全部 KILLED**——分类改回 None、scope 写死 AuthModel（需桩化 `failoverResolveIndexFn`，否则假通过）、判据放宽为无条件认词（3 条防误判用例 FAIL）。
  - **两个实测陷阱**：① `content_filter` 是 `finish_reason` **正常终止分片**，`sseErrorFrame` 捕获不到，只改分类函数完全无效；② 判据在每个流式分片上运行，正文即用户内容，自由子串会误伤（本仓库正是中文小说场景）。
  - **文档**：`doc/4-bugs/2026-11-12_010000_会话压缩阶段异常根因诊断.md`（10 证据 / 6 项裁决 / 3 Mermaid，含 01:00→01:20 纠错记录）、`doc/3-实施/2026-11-12_013000_workbuddy内容审核归入失败分类实施总览.md`。
  - **待办**：生产发布 + 真机验收（观察 content_filter 冷却记录与模型熔断开闸）。

- 会话（2026-11-12 01:56-02:20）：**workbuddy 0.21.24 发布上线 + 生产观察**：
  - **发布链**：CI run `43071825610` Release success（4 平台：darwin_amd64/linux_amd64/linux_arm64/windows_amd64；`darwin_arm64` 被取消、`windows_arm64` 构建失败，按 `--allow-missing` 剔除槽位）→ 资产 sha256 自校验 `ALL CHECKSUMS OK` → registry `d3356c8` → 生产 `plugin-store install` `status=installed, restart_required:false` → `hot reloaded active_version=0.21.24 retired_version=0.21.23`，**容器未重启**（Up 4 hours）。
  - **一致性**：生产落盘 `.so` sha256 `e7ac1632bf6665aa…`（8,454,624 B）与本地 zip 内 `.so` **逐字节一致**；`strings` 确认新判据已入二进制（`"content_filter"` / `"content filter"` / `sensitive content` / `max_output_tokens` / `finish_reason: content_filter`）。
  - **观察结论：新分支尚未触发，当前无审核样本**（`content_filter` 命中计数 0，`docker logs` 与容器内 feed 双向确认）。窗口内失败全是 `11102 model service info not found`（HTTP 400，源于**大写模型名** `DeepSeek-V4-Pro`/`GLM-5.3-Flash` 上游不存在）；同窗口小写别名 `deepseek-v4-pro` **217 次全部成功**。`11102` 走 400 → `failureScopeNone`，与本轮改动无交集。
  - **误伤检查全通过**：部署后 workbuddy **0 个账号被失败禁用**；账号池 20 个、**16 个仍可路由**；4 个 disabled 全部是 credits 耗尽/低积分（`exhausted`/`low_credits`），无一为失败禁用。
  - **旧实例干净退出**：`failover reconcile loop stopped` + `probe loop stopped: superseded by newer instance`，无僵尸巡检（对照 2026-11-08 coze 事故）。
  - **观测口径修正（自身错误）**：首轮误判「feed 停止写入」——查的是宿主机路径 `/usr/local/src/cli-proxy-api/data/token-usage-feed.ndjson`（**过期副本**，mtime 停在 11-11 21:40），真实写入在**容器内** `/CLIProxyAPI/data/token-usage-feed.ndjson`（mtime 02:13，2725 行持续增长）。**feed 必须在容器内查**；sidecar 与 feed 是两条独立路径，记账观测不受影响。
  - **新增端到端用例** `workbuddy/content_filter_end_to_end_test.go`（提交 `d8d9eab`）：用生产真实分片形态串起「判据 → 分类 → 冷却落盘」三段 + 3 条反例守卫。**踩坑**：首版直接调用 `recordSSEErrorFailureForModel` 而无守卫，反例立即被记成冷却 —— 那是**测试不忠实**（生产五处调用点全部由 `if isContentModerationFailure(...)` / `if sseErr != ""` / 错误分支包裹），已改为按生产的「先判据、后记账」顺序。
  - **定时观察**：`schedule-bad2541a`，2 小时间隔 × 6 次，判定标准为「出现 content_filter 日志 / sidecar mtime 推进到部署之后 ⇒ 新分支已触发；仍为 0 且无审核流量 ⇒ 报告无样本，不编造结论」。
  - **旁证观察（独立缺陷，不在本轮范围）**：生产磁盘 89%（26G/29G，剩 3.4G），`/apps/mysql8.1/data` 占 12G（`xiaoshuo` 库 8.5G）；大写模型名导致 11102 换号遍历 20 个账号失败 —— 两者均需另立条目跟进。
  - **追加观察（03:12-04:48）**：仍无审核样本（`content_filter` 命中 0）。04:14 出现 1 次失败禁用 `workbuddy-7ee5e596`/`GLM-5.3-Flash`，经日志追踪其 `content_filter` 命中 **0** 次、全部失败为 11102，且该号 `credits.total_remain=0`/`exhausted=True` ⇒ **与本轮改动无关**。04:39 容器优雅重启（`RestartCount=1`、`ExitCode=0`、`OOMKilled=false`，非本轮操作），重启后加载 **0.21.24**，部署成果保留。
  - **失败量归因（定时复核 1/6 量化）**：60m 内 workbuddy 请求 1196 条、`failed=true` **1014（84.8%）**，同期 `11102` **1007** 条；`failed=true` 中大写模型名占 99.8%，`failed=false` **全部小写**且零失败 ⇒ 高失败量是大写模型名问题，与 `content_filter` 分支日志域互斥。
  - **大写模型名溯源进展**：已排除 new-api `channels.models`/`abilities` 全小写、`model_mapping` 空、`routing.force-model-prefix=false`、`oauth-model-alias` 未配置；但 `/v1/models` 对客户端可见 50 个模型**确实含大写名**（`Auto`/`Kimi-K3.1-Pro`/`GLM-5.3`，来自 workbuddy 动态发现上报的真实上游 ID）⇒ 改写发生在 **new-api 出站到 CPA 之间**，组件尚未定位。

- 会话（2026-11-12 08:12-21:20）：**大写模型名 11102 根因定位 + 两层修复（workbuddy 0.21.25）**：
  - **根因定位（推翻「new-api 改写」假设）**：大写名**不来自 new-api**——`oneapi.channels` 全表 `models` 字段全小写、`model_mapping` 全空、`oneapi.abilities` 精确查 `DeepSeek-V4-Pro` 返回 0 行、`oneapi.logs` 近 30m 模型名全小写。真实来源是**客户端/上游调用方直发大写名**（日志中 `model=` 与 `alias=` 相同，插件未改写）。
  - **第一层根因（配置错配）**：`safeguard-killer` group `"1"` 的 `models` 成员名与各 provider 的注册 ID **大小写不一致**。`safeguard-killer/groups.go:137-150` `groupServing` 用 `strings.TrimSpace(model) == trimmed` **精确大小写敏感匹配**；`:189-192` 命中即把该模型劫持进 group 并用 `rotateModels`（`:154-172`）轮转到链首。铁证 trace `1485bf92`：`qoderwork-provider model=DeepSeek-Flash` 5 个账号 `403:10605` 全失败 → 按 order 滑到 `workbuddy-provider model=DeepSeek-V4-Pro` 遍历 6+ 账号全 11102。A/B 直打 `http://127.0.0.1:8317/v1/chat/completions`（管理 key `cpa_management_key`）：`deepseek-v4-pro`→**200**、`DeepSeek-V4-Pro`→**11102**（响应被 Safeguard Killer 包装）、`Totally-Unknown-Model-XYZ`→`unknown provider for model ...`（证明是被明确认领而非未知名兜底）。
  - **第一层修复（2026-11-12 20:34，用户裁定「改 group 配置为唯一别名」）**：走 `PUT /v0/management/plugins/safeguard-killer/config`，group `"1"` 的 `models` 11 项→8 项全小写 workbuddy 注册 ID：`deepseek-v4-pro`/`glm-5.3`/`glm-5.3-flash`/`kimi-k3.1-pro`/`deepseek-v4.1-flash`/`minimax-m3.1-flash`/`mimo-v2.6-pro`/`hy4free`（移除任何 provider 都不存在的 `Doubao-Seed-2.1-Pro`/`hy4`/`qwen3.8-flash`；`hy4` 真实注册名是 `hy4free`）。改前备份 `/usr/local/src/cli-proxy-api/config/config.yaml.bak-20261112_202935`（68,892 B）。**8 个成员逐个 3 次实测全部 OK=3 FAIL=0**；但大写名**仍 11102** ⇒ group 只解决「被劫持」这一条路径，客户端直发大写名的问题依旧。
  - **第二层根因（插件大小写短路）**：CPA 的 provider 路由对模型名**大小写不敏感**，`DeepSeek-V4-Pro` 会被正常认领到 workbuddy；但插件两处按「忽略大小写」判等 ⇒ 大写名原样透传上游（上游 CodeBuddy 模型 ID **大小写敏感**）→ `11102 model service info not found`（HTTP 400）→ 插件按账号级故障逐个换号 → **一次拼写问题放大成整池遍历**（60m 内 1196 请求 / 1014 失败 84.8%，其中 11102 占 1007）。
    - `workbuddy/payload.go` `rewriteModelInPlace` / `rewriteModelInBody`：`strings.EqualFold(strings.TrimSpace(cur), upstreamModel)` 判为「已相等」直接跳过重写 → 改为**逐字节精确相等**。
    - `workbuddy/models.go` `resolveUpstreamModel`：alias 表按小写索引，查不到时直接 `return m` 不做归一 → 三条返回路径全部套 `canonicalUpstreamModelID`。
  - **第二层实现（workbuddy 0.21.25）**：① `models.go` 新增归一表 `upstreamModelIDCache`（`strings.ToLower(id)`→注册原样 ID），`refreshUpstreamModelIDCache()` 从配置列表 + 动态发现列表**整体重建**，`setConfiguredModels`/`clearConfiguredModels`/`storeDynamicModels` 在**释放各自写锁之后**刷新（持锁调用自死锁）；② 新增 `canonicalUpstreamModelID`：查表折回注册大小写，**未命中只去空白原样返回，绝不猜大小写**（表为空时保持透传语义）；③ `payload.go` 两处改精确比较。新增 `model_case_normalize_test.go`（10 个测试函数，覆盖 7 种大小写变体 / 表未命中透传 / 空表保大小写 / alias 目标归一 / 4 种 body 重写形态 / 2 个缓存刷新入口）。
  - **实施中修掉的自身缺陷**：`canonicalUpstreamModelID` 初版在表未命中时写成 `return canonicalUpstreamModelID(m)`（**自我递归**），被变异验证的 `fatal error: stack overflow`（栈帧重复数百次于 `models.go:600`）暴露 → 改为 `return m`。
  - **变异脚本陷阱**：`replace(old, new, 1)` 只改第一个匹配，锚点 `return canonicalUpstreamModelID(m)\n}` 在文件中出现两次 ⇒ 变异落在自递归行、fallback 路径从未被改 → **变异存活假通过**；`restore` 还会把修复还原成自递归。最终脚本改为**校验锚点恰好命中 1 次**并把上下文带进锚点。
  - **验证**：`cgo-shim-build.py workbuddy` build/vet/test 全绿（33.8s）；**2 组变异均 KILLED**且失败形态是断言 FAIL（变异1 → `TestRewriteModelInPlace_RewritesCaseOnlyDifference`+`TestPrepareUpstreamBody_NormalizesUppercaseModel`；变异2 → `TestResolveUpstreamModel_NormalizesCase`+`TestPrepareUpstreamBody_NormalizesUppercaseModel`）；gofmt 干净（CRLF→LF 归一后空输出）；注释链 [参数]/[返回]/最近修改时间 齐全。
  - **提交/发布**：代码提交 `8eae84b`（6 文件 +366/−11）→ push main 成功；CI run `1614`（id 43223135085）因 `windows-arm64 (go-cross)` 的 "Build cross shared library" 失败 + `darwin/arm64` cancelled 导致 **Release skipped**，改用 `allow_partial=true` 重跑 run `1615`（id 43225613666）。
  - **文档**：`doc/4-bugs/2026-11-12_010000_会话压缩阶段异常根因诊断.md` 的「独立缺陷：大写模型名导致 11102 换号遍历」整节重写为两层根因 + 两层修复 + 变异验证 + 脚本陷阱。
  - **部署后外部配置漂移（21:24–21:26，非本会话）**：0.21.25 上线后 4 分钟，生产 `safeguard-killer` group 被并行会话经 7 次 `PUT /v8/management/config/plugins/configs/safeguard-killer` 改成单 group `"id": "deepseek-v4-pro"`（`models: hy4free/glm-5.3-flash/minimax-m3.1-flash/deepseek-v4.1-flash`）。因 group id 与 workbuddy 注册 ID 同名且 `groupServing` 大小写敏感 ⇒ **小写名开始被劫持进 group、大写名反而绕开**（与 0.21.25 前的方向正好相反）。未造成 11102 回归；本轮仅记录事实、不改该配置。收口建议：group id 应改成不与任何 provider 注册 ID 同名的中性 codename（如 `wb-mix`）。已写入诊断文档「部署后观察：外部会话改写 group 配置」小节（提交 `496e59a`）。
  - **知识库沉淀**：新建 `D:\谷歌云盘\知识库\20-Knowledge\工程实践\忽略大小写的相等判断会短路规范化重写.md`（`id: case-insensitive-equality-short-circuits-canonical-rewrite`，active），记录四段放大链 + 两层正解 + 可迁移定位手法 + 自身两个假通过坑；同轮在 `30-MOCs/工程实践/插件体系与扩展架构.md` 补两条入链（新笔记 + 历史孤岛 `工具输出污染时用Python读原始字节裁决`），`knowledge_index.py check` 由 `exit=5`（孤岛 1）转为 **`ok:true` / `unmounted_count:0` / `exit=0`**，`audit_vault_knowledge.py` 显示新笔记未进入冲突或孤岛候选。

- 会话（2026-11-11 22:26-22:57）：**qoderwork 导入弹窗改为只认凭据 JSON，删除 PAT 全部逻辑（0.13.16，未发布）**：
  - **用户诉求**：生产面板截图红框指出导入弹窗仍要 `pt-` PAT，「改成只要选择或者输入 JSON 就导入就可以了，不需要 PAT 的这些逻辑」。
  - **前端收敛**：弹窗从「`#importPAT` 单条输入 + 批量区块 + 底部单 PAT 按钮」三层压成「选文件行 + 一个大文本框 + 唯一按钮『导入并保存』」。删 `importAuth` / `runBatchImport` / `parseBatchInput` / `describeBatchItems` / `isPATToken` / `credentialPATToken` / `PAT_RE` / `BATCH_GAP_MS` / `.import-batch` 样式；新增 `parseImportInput` / `describeImportItems` / `runImport`，全部输入统一走 `POST /import-cred` 离线回灌。
  - **后端同步**：删 `handleImportPAT`（67 行）、`POST /import` 路由注册与 `mutatingManagementPath` 条目；`failover_disable_test.go` 白名单审计同步删除 `handleImportPAT` 条目。
  - **保留**：嵌套/平铺凭据、凭据数组、`{accounts:[{credential}]}` 导出包装、JSONL、多文件多选；逐条串行、单项失败不中断、失败明细常驻、部分成功后收敛为仅失败项、批内去重。auth 文件 `personalToken` 字段与 `jrt-` 刷新兜底链路未动。
  - **验证**：解析层 16 条 + E2E 26 条断言全绿；**5 组变异 4 KILLED + 1 等价变异（记明原因）**；`cgo-shim-build.py qoderwork` build/vet/test 全绿；无头 Chrome 本地渲染截图确认弹窗结构符合预期。
  - **文档**：`doc/5-tests/2026-11-11_225600_...验证.md`、`doc/6-review/2026-11-11_225600_..._6-review.md`（STYLE: PASS）、需求主文档追加第 6 节「口径收敛」。
  - **发布（2026-11-12 00:10 完成）**：提交 `f3d50db`（代码 16 文件）+ `0f32552` / `4b01f7c`（registry）。生产 `plugin-store install` 热重载 `active_version=0.13.16 retired_version=0.13.15`，落盘 `.so` sha256 `edfb78d5…`，面板与本地 `panel.html` 逐字节一致（`80305b3f…`，87257 字节）；`POST /import` 生产实测 404、`POST /import-cred` 200 且返回结构化校验错误；真实入口 `deepseek-v4-pro` 流式 94 chunks / `[DONE]` / `finish_reason=stop` / usage 115 tokens。**平台缺件**：`darwin_arm64` 与 `windows_arm64` 未发布——CI 侧 `snap install zig` 连续 5 次 `unable to contact snap store`（GitHub 基础设施故障，与本轮代码无关），按 `--allow-missing` 发 4 平台。

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
