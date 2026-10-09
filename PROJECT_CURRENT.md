# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 原生插件 `cpa-workbuddy-plugin`，将腾讯 CodeBuddy（国内版）、WorkBuddy AI（独立国际版）、Trae SOLO、Google Gemini CLI、Cursor（cursor.sh）封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式转发、专属面板、扫码登录、保号巡检，并通过统一 token 用量统计服务。
- 范围：仓库内 workbuddy-provider / workbuddy-ai-provider / traework-provider / gemini-provider / cursor-provider / workbuddy-token-usage（历史归档 qoderwork-provider）的代码实现、测试、资产构建、发布流水线、registry 同步等。
- 非范围：CLIProxyAPI 核心服务本体；跨项目文件修改。

## 项目概况

- 状态：活跃维护中。最新发布版本：qoder-ai-provider **0.1.1**（官方客户端模型对齐、面板测试按钮、自动定时探活与高清图标补齐）/ qoderwork-provider **0.9.23** / workbuddy-provider **0.15.4** / workbuddy-ai-provider **0.1.9** / traework-provider **0.2.4** / workbuddy-token-usage **0.2.4** / cursor-provider **0.1.0** / gemini-provider **0.1.1**。最新发布并部署 qoderwork-provider **0.9.23** / workbuddy-provider **0.15.4** / workbuddy-ai-provider **0.1.9** / traework-provider **0.2.4**（四插件面板「测试」按钮均为「弹出模型选择窗口 → 点选指定模型测试」，发布 + 生产热重载 + 行为验收完成）；同仓已发布 workbuddy-token-usage **0.2.4**（面板性能）、cursor-provider **0.1.0**（Cursor 会话 Token 导入）、gemini-provider **0.1.1**、qoder-ai-provider **0.1.0**（Qoder AI 国际版，每日签到 +100 积分）。
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-10-10 (GMT+8)

## 活动会话进展摘要

- 当前会话（2026-10-10）：**Qoder AI 国际版官方客户端模型对齐、管理面板测试按钮与定时探活、高清图标补齐及 0.1.1 正式发布部署**：
  - 用户反馈与诉求：
    1. 插件模型名称与 Qoder 官方客户端不一致，需核实是否缺少动态获取与真实模型映射；
    2. 缺少管理面板「测试」按钮与指定模型测试能力（对齐 WorkBuddy 的手动点选测试与后台自动定时探活）；
    3. 缺少官方图标（原外部 URL 带空格且 404）。
    4. 指令：“发布 继续”。
  - 核心实施要点：
    1. **模型对齐与动态获取**：从本地 Qoder 官方权威配置（`D:\Qoder\resources\dynamic-text\qoder.json`）中核实 12 个真实模型与上游 key 映射（Cantus ↔ cmodel, Qwen3.8-Max ↔ qmodel_38max, Qwen3.8-Flash ↔ qfmodel, Qwen3.7-Max ↔ qmodel_latest, Qwen3.7-Plus ↔ qmodel, GLM-5.3 ↔ gmodel, GLM-5.3-Flash ↔ gfmodel, Kimi-K3 ↔ kmodel_latest, Kimi-K2.8-Preview ↔ kmodel, DeepSeek-V4-Pro ↔ dmodel, DeepSeek-Flash ↔ dfmodel, MiniMax-M3 ↔ mmodel），上下文基线统一至 1,000,000；在 `body.go` 中实现全量大小写不敏感映射；在 `models.go` 中实现 `defaultModelAliasMap` 与 `authModelsForIndex` 动态获取失败时的优雅兜底。
    2. **测试按钮与自动定时探活**：在 `panel.html` 中补齐卡片「测试」按钮（`data-action="test"`）与模态框 `testModal`，支持手动点选模型实时推理测试；在 `active_ping.go` 与 `refresh_runner.go` 中打通多模型随机自动定时探活（带 30 分钟节流、30 秒熔断与最多 5 个模型轮测）。
    3. **官方高清图标补齐**：从官方应用提取 1024×1024 PNG 至 `assets/icons/QoderAI.png`，更新 `main.go` 的 `pluginLogoURL` 与 `registry.json`，解决 404。
  - 完整闭环发布验证：
    1. 本地 `python scripts/cgo-shim-build.py qoder-ai` 编译、vet 与测试全部通过（all green）。
    2. 严格隔离工作树中无关文件，显式提交 `qoder-ai/` 源码、高清图标并推送至 main（commit 7d7aa20）。
    3. 触发 GitHub Actions 发布流水线（run 37975839198），轮询等待 65 个 jobs 全绿构建完成。
    4. 下载 8 个 release 资产并校验 sha256 全部通过，运行 `publish-assets.py qoder-ai-provider 0.1.1` 更新 `registry.json`，清理历史版本资产并推送（commit 57eff44）。
    5. 远端验证 raw URL 全部 200 OK（图标与 7 平台安装包）。
    6. 生产环境（45.207.222.65）调用 plugin-store 安装 0.1.1 成功，热重载日志确认 `active ping success`；调用 `/models` 成功返回 12+1 个模型列表；调用 `/test-active` 对真实账号测试 `auto`、`Qwen3.8-Max`、`Cantus`、`DeepSeek-V4-Pro`、`GLM-5.3`、`Kimi-K3`、`MiniMax-M3` 均返回 `ok: true` 活跃成功！

- 当前会话（2026-10-10）：**qoderwork-provider 测试按钮改造为「先选模型再测试」，并发布部署 0.9.23**。
  - 用户诉求：qoderwork-provider 的测试也改造一下（对齐 workbuddy / workbuddy-ai / traework 三插件），改造完成提交并发布部署。
  - 后端：qoderwork/models.go 新增 authModelsForIndex(authIndex) + handleModelsQuery(req)（cachedDynamicModels → callModelsAPI，AccessToken 非空前置判断）；management.go 新增只读 GET /models?auth_index= 路由（未进 mutatingManagementPath）与 /test-active 分支；active_ping.go 新增 pickRandomQoderModels / pickRandomQoderModel / sendActivePingQoder / handleTestActive / handleTestActiveWithAuth(sa, authIndex, model)，指定模型时透传、为空时随机（兼容旧面板），兜底模型 "auto"。
  - 前端：qoderwork/panel.html 新增测试弹窗 testModal + 6 个 JS 函数（pendingTestAuthIndex / openTestModal / closeTestModal / onTestMaskClick / loadTestModels / runTestModel）、卡片按钮 data-action="test"、7 条 CSS（.import-result / .r-* / .model-list / .model-item），Escape 处理补 testModal。
  - 本地验证：cgo-shim build/vet/test 全绿（ok 7.249s）+ 必失败哨兵确证真实进编译（sentinel expects a fake model, got "auto"）；Node vm 面板回归 17 项 PASS + 旧版反证 5 项 FAIL（TypeError: ctx.openTestModal is not a function）；6-review STYLE: PASS。
  - 发布闭环：commit 7df7f9a（13 文件，+1115 行）→ push main → CI run 37964833825 success（head=7df7f9a）→ 资产 commit 7f18e2a（8 文件，7/7 sha256 OK）→ registry 0.9.23 回填（随 30dfe9b 推送，7 artifacts sha256+size 与 checksums 全等）→ 远端 raw 7/7 200 且 size 一致；validate-registry.py 8 插件 schema_version=2 通过。
  - 生产部署与验收：plugin-store install 0.9.23 → installed；落盘 .so sha256 bb55d2df…3977 与本地 release zip 内 .so 完全一致；热重载 active_version=0.9.23 retired_version=0.9.22；行为验收 GET /models?auth_index= 返回 {"models":["auto"]}、POST /test-active 指定 model=auto 成功（421ms，回显该 model）、无 model 兼容随机路径成功（622ms）、无 auth_index 返回 auth_index is required、panel / accounts 200、面板资源含 testModal / openTestModal / loadTestModels / runTestModel / data-action="test" 全命中。
  - **本轮关键踩坑（共享源原子性）**：部署期间生产自定义源被另一并行会话的半成品条目（qoder-ai-provider，`type: direct` + 空 artifacts）整体校验失败，`source_errors: plugins[7]: direct install requires at least one artifact`，导致该源下**全部**自定义插件 install 返回 plugin_not_found；根因是宿主 ParseRegistry 对「单源一份 registry」原子校验，任一条目非法即整源不入列。待对端补齐 artifacts 后源自动恢复，无需重启宿主、无需重发本插件 registry。已沉淀知识库《插件商店源级 registry 校验原子失败会让整源插件集体 plugin_not_found》。
  - 发布后清理：prune-release-assets dry-run 确认 8 插件保留集完整（含 qoderwork-provider-0.9.23），待删 0。

- 当前会话（2026-10-10）：**独立国际版 Qoder AI 插件全套代码实现、验证全绿与生态接入（qoder-ai-provider 0.1.0）**。
  - 用户诉求：对标 workbuddy-ai-provider 独立国际版架构，构建独立的 Qoder AI 国际版插件，支持国际站（qoder.com）每日签到给 100 积分功能与多账号管理。
  - 调研与端点实测（2026-10-10）：
    1. 域名与网关确认：qoder.ai 308 重定向至 https://qoder.com；国际站 OpenAPI 统一基地址为 https://openapi.qoder.sh；
    2. 关键业务端点在线实测：/sash/api/v1/me/daily-check-in/status 与 /claim、/api/v2/quota/usage、/api/v2/user/plan、/api/v1/deviceToken/* 均通过 curl 真实响应证实路由 100% 存活；
    3. 推理与模型同网关：openapi.qoder.sh 同步挂载 /algo/api/v2/model/list 与 /algo/api/v2/service/pro/sse/agent_chat_generation，完全复用 COSY 签名 + QoderEncoding + SSE 转发链路。
  - 代码实现（qoder-ai/）：
    1. 独立工程骨架：插件 ID `qoder-ai-provider`，名称 `Qoder AI`，凭据文件规范 `qoderai-<uid>.json`（单账号兜底 `qoderai.json`），独立存储目录 `qoderai_accounts`；
    2. 核心功能闭环：OAuth 设备授权流（PKCE + nonce + 官方 Client ID）、PAT 导入、Token 自动保活；每日签到（每次 100 积分）+ 4 小时后台定时轮询 + 单账号并发防重锁 + 批量签到；动态模型拉取与静态 fallback；指定模型探活测试（authModelsForIndex + handleTestActiveWithAuth）；
    3. 专属管理控制台：定制 qoder-ai/panel.html，管理路径挂载在 /v0/management/plugins/qoder-ai-provider，两段内嵌 script 通过 Node 语法验证；
  - 生态基础设施集成：
    1. 用量归一化：token-usage-tracker/usage_stats/auth_identity.go 增加 Qoder AI 供应商映射；
    2. CI/CD 流水线：.github/workflows/build.yml 增加 qoder-ai-provider-v* 触发器、dispatch 选项及 7 平台构建矩阵；
    3. 插件注册表：registry.json 登记 qoder-ai-provider 0.1.0 元数据并通过 validate-registry.py 校验；
  - 验证与门禁全绿：
    1. cgo-shim-build.py qoder-ai：build / vet / test 全绿；
    2. 哨兵失败拦截反证：qoder_ai_endpoints_test.go 临时插入 SENTINEL_FAILURE 证实真实进入编译测试，恢复后复测全绿；
    3. 跨插件防破坏验证：qoderwork、workbuddy-ai、token-usage-tracker 跑 cgo-shim-build 全绿；
    4. 6-review：STYLE: PASS，实施总览已归档落盘。
  - 发布闭环与生产热部署（2026-10-10）：
    1. 代码提交：commit 30dfe9b（71 文件）推送到 main；
    2. CI 流水线：run 37966644012（head=30dfe9b）全 65 jobs success（含 8 插件测试与 7 平台构建矩阵）；
    3. Release 资产：下载 8 资产（7 zip + checksums）校验 ALL OK，commit a2e5252 推送 main；
    4. 注册表回填：publish-assets.py 同步 7 平台 artifacts sha256 与 size，commit d124fa0 推送 main；
    5. 远端验证：raw.githubusercontent.com 7 平台资产 size 与 sha256 ALL PASS；
    6. 生产热重载部署：plugin-store install qoder-ai-provider 0.1.0 成功 installed；生产落盘 .so sha256（6363aacc8a72cf304a450e6cc1430a0b5f716615906e764eb95f443278c411a7）与本地 zip 100% 精确一致；
    7. 生产接口验收：/v0/management/plugins/qoder-ai-provider/accounts 200，/v0/resource/plugins/qoder-ai-provider/panel 200，日志确认挂载热重载生效；
    8. 发布后清理：prune-release-assets dry-run 确认 8 插件版本保留集完整，无历史冗余资产。

- 当前会话（2026-10-09）：**三插件测试按钮改为弹出模型选择窗口按指定模型测试（workbuddy 0.15.4 / workbuddy-ai 0.1.9 / traework 0.2.3）**。
  - 用户诉求：三插件面板卡片「测试」按钮原本随机挑一个模型直接测，改为点击后弹出该账号支持的模型小窗口，点选指定模型再测。
  - 后端：三插件 models.go 新增 authModelsForIndex + handleModelsQuery；active_ping.go 的 handleTestActiveWithAuth(sa, authIndex, model) 支持指定模型（model 为空保持随机，兼容旧面板）；management.go 新增只读 GET /models?auth_index= 路由。
  - 前端：三插件 panel.html 新增测试弹窗（openTestModal / loadTestModels / runTestModel），testActive 改为先弹窗选模型；无模型时提示「暂无可用模型」并允许关闭。
  - 本地验证：cgo-shim 三插件 build/vet/test 全绿 + 必失败哨兵确证真实进编译；三面板 Node vm 回归各 17 项 PASS + 旧版反证 FAIL；6-review STYLE: PASS。
  - 发布闭环：commit ce6b4e2（32 文件）→ CI 三 run success（37940758486 / 37940778484 / 37940786472，head=ce6b4e2）→ 资产 commit fe5742d（24 文件，三目录 7/7 sha256 OK）→ registry 回填 commit 207e22c → prune 旧版 commit ada831f；远端 49/49 当前资产 200、旧版（0.15.3 / 0.1.8 / 0.2.2）404。
  - 生产部署：三插件 plugin-store install 全部 installed；落盘 .so sha256 与本地 zip 完全一致（workbuddy 0.15.4 9c3b85b6… / workbuddy-ai 0.1.9 dd77aab4… / traework 0.2.3 2d671e16…）；热重载 active_version 命中目标版本；panel / accounts 三插件全 200。
  - 生产行为验收：/models 三插件返回真实模型（17 / 27 / 大量）；缺 auth_index 返回 auth_index is required；/test-active 指定 deepseek-v4.1-flash → traework 成功（1.625s，回显该 model），workbuddy 因额度 429 但错误文案确认使用指定模型。

- 当前会话（2026-10-07）：**固化「发布后清理」规则 + release-assets 瘦身**。
  - 用户要求：把「每次发布后都需要清理不需要的垃圾」吸收到项目 skill 与规则中。
  - 落地：新增项目 skill `project-cpa-workbuddy-plugin-release-asset-prune-rules`（保留集=registry 每插件当前版本目录）+ 通用脚本 `scripts/prune-release-assets.py`（--dry-run/--apply，受跟踪目录 git rm、空目录 rmdir）；`AGENTS.md`/`CLAUDE.md`「仓库与发布」小节新增「发布后清理」条；发布 skill 增 Step 13.5 并更新 description。
  - 实操：`release-assets` 3.75 GB → 0.13 GB（删 185 个历史版本目录 / 1469 文件，保留 registry 当前 6 个版本目录），提交 `095f4a4` 推送 `origin/main`；远端当前版本 raw 200、旧版本 404。

- 当前会话（2026-10-07）：**移植 Cursor Provider 插件并新增会话 Token 导入账号能力（cursor-provider 0.1.0）**。
  - 来源：`https://github.com/yobo2u/omsub`（cursor 分支）的 cursor-plugin，按 workbuddy 面板口径全量移植并改造，落在独立插件 `cursor/`（id `cursor-provider`），不依赖 CLIProxyAPI SDK。
  - Token 导入链路：粘贴 `user_<id>::<jwt>`（或 URL 编码 / Cookie 前缀 / 裸 JWT）→ 取 `::` 后段 JWT → `POST https://api2.cursor.sh/oauth/token`（grant_type=refresh_token + client_id + refresh_token）兑换 access_token → `host.auth.save` 落盘为 `cursor-<hash8>.json`（顶层 type=cursor-provider）；按 account_id/email 去重。
  - 管理面板：import / export / delete / enable / disable 五条路由 + 卡片删除/启停、全部启停、导入弹窗、导出、双语 i18n、key 三回退，与 workbuddy 面板一致。
  - 保留移植全量能力：OAuth 轮询登录、executor tool-loop、checkpoint/会话粘性、图片输入、上下文准入。
  - 验证：`python scripts/cgo-shim-build.py cursor` build/vet/test 全绿（含必失败哨兵）；面板单 script 块 node --check 通过；cursor 全量 LF 镜像 gofmt -l 清零；6-review STYLE: PASS。
  - CI/registry：.github/workflows/build.yml 增 cursor-provider-v* tag、dispatch option、test/build 矩阵；registry.json 增 cursor-provider 0.1.0（7 平台 artifacts）。
  - **发布闭环（已完成）**：commit 44d2bb4 → push；CI run 37636155616 全 57 jobs success；下载 8 资产（7 zip + checksums）并校验 ALL OK，commit a17c2aa；publish-assets 回填 registry（commit 92f569c）；远端 raw 7 资产 size+sha256 ALL PASS；生产 plugin-store install 0.1.0，落盘 .so sha256 与本地 zip 100% 一致，容器日志 plugin loaded + plugin registered 热重载成功；plugin-store 状态 installed/registered/enabled/effective_enabled 全 true。
  - **生产端到端验收（已完成）**：真实 token 导入 → oauth/token 兑换 → 落盘 cursor-6c2463e563a13c02.json → 面板可见（246 模型）→ 重复导入正确去重；推理验收 cursor/default 非流式 3 次 + 流式（1..5 完整 + finish_reason:stop + [DONE]）+ 多轮全部成功；export 接口 200。高级模型（gpt-5.3-codex / composer-2.5 / claude-4-sonnet / gemini-3.x 等）返回上游 resource_exhausted（429，约 280ms 快速拒绝），属 Cursor 服务端对该账号订阅的配额判定，插件按真实 429 语义透传，非移植缺陷。
  - 发布后清理：release-assets prune dry-run 确认 7 保留集完整、无待删；本地缓存 .workbuddy/release-assets 已删。
- 当前会话（2026-10-07）：**定位新授权账号离奇消失根因，彻底移除自动物理删除机制，发布并部署 WorkBuddy AI 0.1.6**。
  - 用户反馈：刚刚成功授权了一个号，出现在了面板中，但很短的时间过后账号消失了；
  - 核心排查与根因：
    1. 13:52:07 账号授权成功创建（workbuddyai-3d749a7f-ec76-4a55-a316-37d86c9ff47c.json）；
    2. 13:52:09 有并发请求调用该账号，上游接口返回 upstream 429: Credits exhausted（code: 14018），命中 isHardCreditError；
    3. 旧逻辑中将 Global 账号生命周期硬编码为 lifecycleDelete，触发 reconcileOneAccount 后二次调用 fetchUserResource 确认 0 额度，随即执行 deleteAuth 物理删除了磁盘文件；
  - 架构与生命周期对齐修复 (v0.1.6)：
    1. 彻底将 lifecycleActionFor 返回值调整为 lifecycleDisable（仅软禁用，绝不物理删除账号），与国内版完全对齐；
    2. 放开通用恢复通道：去除 cn 专属判定，当 disabled 账号在后续充值或刷新中检测到 remain > 0 时自动重新激活（lifecycleReenable）；
    3. 修正专属管理面板前端 region 默认 fallback 为 global；
    4. 单测补充断言并通过必失败哨兵编译验证；版本 bump 至 0.1.6，GitHub Actions 48 个构建任务全绿；
    5. 8 个 Release 资产已下载入库并推送到 GitHub，registry.json 0.1.6 校验全绿并推送，生产服务器已成功热重载加载 0.1.6。


- 当前会话（2026-10-07）：**定位模型未展示根因、支持双重 agent 结构与 default 映射，并全链路发布部署 WorkBuddy AI 0.1.5**。
  - 用户反馈：截图反馈“该凭证暂无可用模型 / 该认证凭证可能尚未被服务器加载或没有绑定任何模型”。
  - 核心排查与根因收敛：
    1. **截图时段与旧版本原因**：用户截图时间为今日凌晨 03:21:35，当时线上运行为旧版 0.1.3，其错误请求内网 `/console/enterprises/personal/models` 返回 500 导致动态发现失败；
    2. **凭证禁用状态注销机制**：截图显示该凭证右侧处于【未启用】（灰色禁用）状态。CPA 宿主核心（`service_models.go`）设计规定，禁用凭证立即从 `ModelRegistry` 注销，故点击模型弹窗返回空；
    3. **上游真实协议结构增强兼容 (v0.1.5)**：`https://www.workbuddy.ai/v3/config` 结构增加 `data.agent.agents` 与 `data.agents` 双重提取兼容，增强 `default` / `default-model` 至 `auto` 映射，并补齐带前缀的可观测动态发现日志；
    4. **单测与全架构 CI**：单测覆盖嵌套 agent 结构及 auto 别名继承，全绿通过；版本 bump 至 0.1.5，CI（Run ID: 37575092982）48 个任务构建全绿；
    5. **资产同步与生产热更新**：下载 8 个 Release 资产入库推送到 main，同步更新 `registry.json` 并通过校验，在生产服务器完成 0.1.5 热重载加载生效。

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
  "version": 4,
  "registry_schema": "task_plan_projection_registry",
  "registry_updated_at": "2026-10-08T20:50:04.091751Z",
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
      "projection_id": "SESSION/20e8ec46dba59302295da066dda5bf5a2a66b4529a4bae4bb9cdc4698f67c8d1",
      "session_id": "01a11ba7-3193-7ab0-9618-11075ecaba63",
      "projection_origin": "synthesized",
      "synthesis_mode": "exact",
      "state": "inactive",
      "plan_key": "IMPL-TOKEN-USAGE-PERF-ICON-20261008",
      "source_document": "doc/3-实施/2026-10-08_Token用量面板性能优化与插件图标修复实施总览.md",
      "plan_fingerprint": "e41673af031195fb8992a957a1a20efa15ce3eac1bf8bc3dee3e5f2c8de406cc",
      "updated_at": "2026-10-08T17:23:47.766782Z",
      "steps": [
        {
          "id": "TASK-001",
          "step": "[TASK-001] 后端移除 SSE 通道（路由、函数、通知序列、测试同步）",
          "status": "completed"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] 面板前端改造（去自动刷新、新增 last_1_hour、本地化）",
          "status": "completed"
        },
        {
          "id": "TASK-003",
          "step": "[TASK-003] 图标资产与注册（新图标入库、Logo URL、registry logo）",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 版本与文档收口（VERSION/main.go bump、6-review、测试文档）",
          "status": "completed"
        },
        {
          "id": "TASK-005",
          "step": "[TASK-005] 发布与生产部署验证",
          "status": "completed"
        }
      ]
    },
    {
      "projection_id": "SESSION/6d9b35f8481be4fb66fbe631557cd5e155caef128cb55e64f7b4d110746dca96",
      "session_id": "01a11b9b-eb7d-7830-afed-7c8dd64c6629",
      "projection_origin": "synthesized",
      "synthesis_mode": "exact",
      "state": "inactive",
      "plan_key": "IMPL-WBAI-CHECKIN-20261008",
      "source_document": "doc/3-实施/2026-10-08_WorkBuddyAI国际版签到改造实施总览.md",
      "plan_fingerprint": "6cd82fd431385b7bf05b43a41d7600cfc93bd4126cc463516ef891fb986687b5",
      "updated_at": "2026-10-08T20:50:04.091579Z",
      "steps": [
        {
          "id": "TASK-001",
          "step": "[TASK-001] 后端签到数据链路：新增 checkin.go，扩展缓存 3 路与面板回填",
          "status": "completed"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] 后端控制面与调度：/checkin 路由、配置开关、调度器合并",
          "status": "completed"
        },
        {
          "id": "TASK-003",
          "step": "[TASK-003] 面板签到改造与 vm 回归脚本",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 文档与版本收口：bump 0.1.8、6-review",
          "status": "completed"
        },
        {
          "id": "TASK-005",
          "step": "[TASK-005] 发布与生产部署验收",
          "status": "completed"
        }
      ]
    }
  ]
}
```
<!-- END TASK PLAN PROJECTION -->
