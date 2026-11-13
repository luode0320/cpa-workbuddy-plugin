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
  12. `doubao-provider`：豆包工作模式（agent_mode=1 PC 客户端管线；DeepSeek V4 / Kimi K3 / GLM 5.3 直连；Cookie 导入 + device_id 派生）。
- 非范围：CLIProxyAPI 核心服务本体；跨项目文件修改。
- 已下线插件：`gemini-provider`（2026-10-11）、`notion-provider`（2026-10-22）。**`doubao-provider` 2026-10-22 下线、2026-11-14 以工作模式 0.1.7 重新上线、0.1.8 面板重写与导入导出已上线（生产已加载）。**

## 项目概况

- 状态：活跃维护中。12 大插件全景（11 Provider + 1 用量统计）。**2026-11-14 doubao-provider 以工作模式管线 0.1.7 重新上线，0.1.8 面板重写与账号导入导出已上线（生产已加载）**。
- **生产主机（2026-11-07 起）= 45.207.222.65（`cpa-server`，端口 18998），254（`cpa-server-new`）已弃用**：用户明示「254 已经放弃，改回 65 为主服务器」，全部域名已解析到 65，254 只剩 mysql+nginx（CPA 已停）。部署/验收一律打 65，管理密钥用插件配置 `cpa_management_key`，账号在 `auth-dir=/CLIProxyAPI/mysqlstore/mysqlstore/auths`（62 个）。
- **2026-11-07 01:27 已在 65 完成全量对齐与启用**：11 个插件全部 `configured/registered/enabled=true` —— workbuddy 0.21.9（20 号）/ coze 0.7.4（23 号）/ qoderwork 0.12.6（2 号）/ workbuddy-ai 0.1.35（6 号）/ token-usage 0.2.8 / traework 0.5.7 / qoder-ai 0.1.35 / zcode 0.3.5 / copilot 0.1.12 / qwenwork 0.2.13 / cursor 0.1.7（首次安装）。install 前 5 个显示 `registered=false` 是「该版本 .so 从未加载」（config 里 enabled 本为 true），install 后日志为 `plugin loaded + plugin registered`，非 hot reload 形态。
- **2026-11-07 19:40 当前生产版本（max_tokens 最小干预批次）**：workbuddy-ai **0.1.37** / qoder-ai **0.1.37** / qoderwork **0.13.5** / traework **0.5.13** / zcode **0.3.7** / qwenwork **0.2.15** —— 上述插件 `configured/registered/enabled/effective_enabled` 全 true，`.so` sha256 与 release zip 逐字节一致。**管理 API 端口是 8317（不是 18998，18998 是 sshd）**。
- **2026-11-07 20:33 workbuddy 已升级 0.21.12 → 0.21.13**（失败禁用与手动启用联动，`hot reloaded active_version=0.21.13 retired 0.21.12`，落盘 sha256 `574f6921…` 与本地 zip 一致）。
- **2026-11-07 21:25 三插件同步升级**：workbuddy-ai **0.1.38** / traework **0.5.14** / qoderwork **0.13.7**（同型缺陷整改），三者 `hot reloaded` 且 `.so` sha256 与 release zip 一致。
- **2026-11-07 22:17 workbuddy 升级 0.21.13 → 0.21.14**（P0 分阶段超时 + P1 HostCallbackID 透传，见下条会话摘要）：`hot reloaded active_version=0.21.14 retired_version=0.21.13`，落盘 sha256 `8433a54c…` 与 release zip 逐字节一致；只有 5 平台资产（缺 `darwin_arm64`）。
- **2026-11-08 14:51 traework 升级 0.5.18 → 0.5.19**（下行 usage 帧缺失修复，`hot reloaded active_version=0.5.19 retired 0.5.18`，落盘 sha256 `27d74d19…` 与 release zip 逐字节一致；容器未重启）。同刻 workbuddy 0.21.15 / qoderwork 0.13.12 由并行会话上线。
- 2026-10-25 历史发布批（已过期，仅存档）：zcode 0.3.1 / qoderwork 0.12.1 / traework 0.5.1 / workbuddy 0.21.2 / cursor 0.1.6 / qoder-ai 0.1.31 / coze 0.6.2 / qwenwork 0.2.9 / copilot 0.1.11 / workbuddy-ai 0.1.30 / token-usage 0.2.8（细节见 git log 与 CHANGELOG）。
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-11-14 (GMT+8)

## 活动会话进展摘要

- 会话（2026-11-14 02:40-03:30）：**doubao 0.1.8：面板重写为 workbuddy 同源形态 + 账号导入导出（生产热重载已生效）**：
  - **用户指令**（m01353）：「不要用户自己填密钥, 参考 workbuddy 面板来设计豆包的面板、导入、导出、账号等功能。」截图停在「加载账号失败：需要管理密钥」。
  - **根因**：`doubao/panel.html` 密钥段只有 `enc::v1::`，缺 `ENC_PREFIX_V2`（宿主新版只拼 host）与 `readSessionKey` 脏值清理，是 12 个插件里唯一落后的面板。
  - **后端**：新建 `doubao/export.go`（276 行）——`handleExportAccounts`（`{version,exported_at,plugin,count,accounts[]}`）、`handleExportAccountsZip`（archive/zip 原样写物理文件字节、无号 404、头 `doubao-credentials-<YYYY-MM-DD>.zip`）、`rawCredentialBytes`、`exportAccountFileName`、`uniqueZipName`、`extractImportPayload`/`cookieFromValue`（深度 4、键序 cookie/cookie_header/cookieHeader/raw/text/json/credential/accounts/auth/account）；`management.go` 路由 8→**10**（:73 `/export`、:74 `/export/zip`），`handleImportCredentials` 兼容 `{cookie,label}`/`{raw}`/`{json}`，成功返回 8 键。
  - **前端**：`panel.html` 整页重写（生产实测 **53382 B**）——三套 CSS token + `window.__dbThemeSync` 父页 MutationObserver、v2+v1 密钥解码、401 累计 3 次才清 key + 60s 冷却、403 `IP banned` 退避 clamp[30s,30min]、筛选四态（全部/可用/已禁用/过期）+ summary + 三列卡片、导入弹窗（粘贴 + 多选 JSON ≤2MB + `expandCredentials` 三形态）、导出 JSON/ZIP、删除二次确认、测试弹窗（`pendingTestAuthIndex` 丢弃过期响应）。豆包无额度/签到能力，卡片不做积分与签到。
  - **本地验证**：`cgo-shim-build.py doubao` 全绿；自建 shim 跑 `go test -v` → **PASS=154 SKIP=13 FAIL=0**；`node --check` 两 script 块 exit=0；DOM 交叉校验 35 id / 20 refs / 16 handlers MISSING=[]；gofmt clean；`scan_test_pollution.py` → POLLUTION: PASS。
  - **文档**：`doc/5-tests/2026-11-14_031515_doubao管理面板重写与账号导入导出验证.md`、`doc/6-review/2026-11-14_031515_doubao管理面板重写与账号导入导出_6-review.md`（`STYLE: PASS`）。
  - **发布链**：提交 `d28391b`（10 文件 +1654/−323）→ CI run 43490690397（`darwin/arm64` cancelled、Release skipped）→ `allow_partial=true` 重派 run 43491137617 → release `doubao-provider-v0.1.8` id=444644251 **6 assets** → registry `d0b8e2a`（16+/16−，`validate-registry.py` → 13 plugin OK）→ 远端 raw `version=0.1.8 artifacts=5`。
  - **生产热重载**：`POST /v0/management/plugin-store/doubao-provider/install?version=0.1.8` → `restart_required:false`；`plugin hot reloaded plugin_id=doubao-provider active_version=0.1.8 retired_version=0.1.7`（03:26:16）；落盘 `.so` **8188384 B** sha256 `f5f61bcb4ba7a84e7e332c74e056932b2c8d005f1f945fdd8e7c76f41b496241` 与本地 zip 内 `.so` **逐字节一致**；容器 `Up 45 hours` **未重启**。
  - **生产验收**：`/v0/resource/plugins/doubao-provider/panel` 200（53382 B）命中 `enc::v2::`/`doubao-mgmt-key`/`__dbThemeSync`/`readSessionKey`/`exportAuthZip`/`openImportModal`/`filterBar`/`authBox` 八标记；`GET /export` 200 `{"accounts":[],"count":0,"exported_at":"2026-11-13T19:26:52Z"}`；`GET /export/zip` 404 `{"error":"没有可导出的账号"}`；`/accounts` 200 空、`/models` 仍 `{"error":"auth_index 不能为空"}`。
  - **遗留**：`/accounts` 仍为 0 —— 宿主只在 provider 有账号时才注册模型，导入真实 Cookie 后才会出现 `doubao/*` 与端到端能力。

- 会话（2026-11-14 01:00-02:35）：**doubao-provider 重新上线 0.1.7：豆包工作模式 agent_mode=1 PC 客户端管线（生产已加载）**：
  - **用户诉求演进**：先问「我们是不是又豆包工作的插件?」→ 追问「GitHub 上有豆包工作的反代项目吗」（调研出唯一完整实现 `yyydsz/doubao2api`）→ 裁定「开始实施」。
  - **架构决策**：插件从 tag `doubao-provider-v0.1.6` 回灌源码（`git checkout doubao-provider-v0.1.6 -- doubao`，45 文件），骨架复刻 copilot；**真正新写的是工作模式**——tag 版只有网页端 `agent_mode=2` 技能链路，无 `general_task_param` / `conversation_init_ext` / `option.ext` 三件套。
  - **协议改造（`doubao/completion.go` +446 行）**：`completionOption` 新增 `ConversationInitOption`/`ConversationInitExt`/`GeneralTaskParam`/`IsOldUser`/`MessageStorageType`/`Ext`；`completionRequest.Ext` 加 `omitempty`（工作模式扩展必须在 `option.ext` 内，顶层留空）；`buildCompletionRequest` 按 `generationMode.WorkMode` 分流 `buildWorkOption` / `buildSkillOption`；`localPermissionsFor` 工作模式 status=1、网页端 3。
  - **客户端指纹（`protocol.go` +237 行）**：新增 `buildClientQuery`（19 键：`aid=582478`、`channel=win`、`client_platform=pc_client`、`web_platform=desktop`、`pc_version=2.30.1`、`runtime_version=3.37.4`、`chromium_version=147.0.7727.149`，`fp=verify_+device_id`、`tea_uuid=device_id`、`web_tab_id` 每轮换新）+ `clientEndpointURL`；`applyUpstreamHeaders(req, workMode)` 工作模式走 `clientUserAgent`（SamanthaDoubao/2.30.1）+ `agw-js-conv: "str, str"` + `last-event-id: undefined` + `x-flow-trace`。**业务端点 `/alice/*`（模型发现、额度、签到）固定走网页端指纹**。
  - **device_id 派生**：`deviceIDFor(sa)` = `sha256(CookieHeader+AuthID)` 前 16 字节逐位 `%10` + 前缀 `73` = **18 位数字串**（上游按 18-19 位判定客户端）；`saveCredentials` 导入即固化 `DeviceID`/`WebID`/`WebTabID` 并落盘。
  - **模型目录**：14 条（4 能力别名 + 10 工作模式模型）。工作模式模型按 `model_item_key` 直连：`deepseek-v4-flash`(person-deepseek-v4-1-flash-261017，300k_230，0.01x)、`deepseek-v4-pro`、`kimi-k3`/`kimi-k3.1-pro`/`kimi-k3.1-pro-fast`、`glm-5.3`/`glm-5.3-flash`、`auto`(9)、`doubao-2.1-lite`(seed-lite-7b)、`doubao-2.1-pro`(5)，后三条走 256k。默认 `deepseek-v4-flash`。
  - **七项配置**：`work_bot_id`/`work_mode_id`(3)/`work_reasoning_effort`(4)/`work_memory_profile`(300k_230)/`work_total_window`(300000)/`work_enable_sandbox`(false)/`work_workspace`；`doubaoRegistration` 的 `ConfigFields` 从 3 条扩到 10 条。
  - **验证**：`cgo-shim-build.py doubao` 三轮全绿；**PASS=147 SKIP=13 FAIL=0**（13 条全是 `DOUBAO_LIVE_TEST=1` 控制的 `*Live*` 用例）；gofmt clean；46 文件 UTF-8 无 BOM + CRLF；新增 `workmode_test.go` 11 用例（契约、三处 model_item_key 一致、技能管线不被吞、权限差异、19 键 query、web_tab_id 轮换、双指纹头、workHomeDir、atoiSafe、device_id 稳定性、JSON 形状）。
  - **测试污染处置**：`scan_test_pollution.py` 首跑 FAIL 13 条（全是 `if os.Getenv("DOUBAO_LIVE_TEST") != "1" {`），根因是脚本只按顶层 `test/` 目录分桶、不识别 Go 的 `*_test.go` 后缀约定；建 `doubao/.test-pollution-allowlist`（5 条键）登记豁免 → PASS。**兄弟插件 workbuddy 240 条 / traework 100 条 / copilot 0 条走同一范式，证明这是仓库主流写法而非污染。**
  - **文档**：`doc/5-tests/2026-11-14_020437_doubao工作模式agent_mode1验证.md`、`doc/6-review/2026-11-14_020437_doubao工作模式agent_mode1_6-review.md`（`STYLE: PASS`，7 步全过）。
  - **发布链**：提交 `193cb87`（52 文件 +12766/−2）→ CI run 43479685482（`darwin/arm64` cancelled）→ `allow_partial=true` 重跑 run 43481801335 → release `doubao-provider-v0.1.7` **5 平台**（linux amd64/arm64、darwin amd64、windows amd64/arm64）→ registry `f2bcab3`（5 artifacts）→ 远端 raw 200 + 5 个资产 HEAD 200 且 Content-Length 与 registry size 一致。
  - **生产安装**：`POST /v0/management/plugin-store/doubao-provider/install?version=0.1.7&source=source-636fa01121af` → `restart_required:false`、`plugin loaded` + `plugin registered`（02:28:32），**容器未重启**（`StartedAt=2026-11-11T22:52:32Z`、`RestartCount=2`）。落盘 `/CLIProxyAPI/plugins/linux/amd64/doubao-provider-v0.1.7.so` sha256 `fe3e42038d62d6cf62f7468829666dcdc4d7a2e8f96df0c229fd2286aecd1f88`（8,038,208 B）**与本地 zip 内 .so 逐字节一致**。
  - **生产验收**：`/v0/management/plugins` 里 `registered/enabled/effective_enabled` 全 true，10 个 `config_fields` 全部下发，`menus` 有 `/v0/resource/plugins/doubao-provider/panel`（菜单名 Doubao）；面板 200；`/accounts` = `{"accounts":[],"count":0}`（未导账号）；`/import {}` → 400 `{"error":"cookie 不能为空"}`；`/models?auth_index=doubao-nonexistent` → `{"error":"账号不存在"}`；`/test-active {}` → `{"error":"auth_index 不能为空"}`；容器内 `/root/.antigravity_cockpit/doubao_accounts` 已自动创建（空）。
  - **当前缺口（真实存在，需真实 Cookie 才能推进）**：`/v1/models` 里没有 `doubao/*` 条目（33 个模型全来自其它 provider），`POST /v1/chat/completions model=doubao/deepseek-v4-flash` → 400 `unknown provider for model`。根因：宿主只在**有账号**时才注册该 provider 的模型（`ModelForAuth` 路径），空账号池 ⇒ 零模型注册。**导入真实豆包 Cookie 后才会出现 `doubao/*` 并打通端到端。**

- 会话（2026-11-14 00:00-00:25）：**Safeguard Killer 0.1.11：面板不再索要管理密钥，状态改走免鉴权 resource 路由**：
  - **用户指令**：「这个不应该让用户填写」（截图红框标出面板右上角管理密钥输入框 + `HTTP 401 ... {"error":"invalid management key"}` 横幅）。
  - **根因**：面板把「读状态」也放在需密钥的 `/v0/management` 下，而密钥唯一来源是 CPA 主面板 `localStorage['cli-proxy-auth']`，主面板 `partialize` 只在勾「记住密码」时才持久化它 ⇒ 没持久化就弹输入框让人手填；空 key 还会累计宿主的 5 次封禁（`maxFailures=5` / `banDuration=30m`，本轮实测被封 `16m11s`）。
  - **依据**：宿主只对 `/v0/management` 校验密钥，`/v0/resource/plugins/<id>/` 走 `pluginResourceNoRoute`（`ServeResourceHTTP` 注释原文 "unauthenticated browser-navigable"）。实测 resource `/ui` 200 / 管理 `/state` 403。
  - **改动**（提交 `082c0dc`，5 文件 +105/−27）：`Resources` 新增 `/state`（Menu 留空不占侧边栏）；`handleManagement` 两支合并（管理路由与 resource 路由同以 `/plugins/safeguard-killer/state` 结尾）；`STATE_API_URL` 改 resource 路径、`doRefresh` 去掉鉴权头与 401/403 分支；删 `#mgmtKey` 输入框与全部回退显示逻辑；`authHeaders()` → `mgmtAuthHeaders()`；保存前判断拿不到密钥就提示去主面板「插件管理 → 编辑配置」。
  - **首版缺陷**：把 `/state` 写成独立 case 与既有 `HasSuffix(mgmtStateRoutePath)` 重叠成**死代码**（变异 1 存活，测试照样全绿）⇒ 已收敛为单支。
  - **验证**：`ui_test.go` 断言 Resources 两条、`/state` 的 Menu 为空、HTML 不含 `mgmtKey`；新增 `TestHandleManagementStateResourcePath`。三组变异全 KILLED（删 resource 声明 / 塞回输入框 / 状态 URL 退回管理路由）。
  - **发布与验收**：registry 提交 `bbb2fef`；CI run 1640 `Release: success`（`darwin/arm64` 仍 cancelled）；`.so` 26,767,392 B sha256 `5102b31c…ac32e1b` 与本地 zip 一致，`hot reloaded active_version=0.1.11 retired_version=0.1.10`（00:19:07）；真机 resource `/state` **200**、`version=0.1.11`、groups 保留、`/ui` 200 且 `mgmtKey=0` 命中、管理 `/state` 无 key 仍 401；`normal max=64` 与 `reasoning max=24` 均 200 且 `decisions` 记 `served by deepseek-v4-pro`、零失败。
  - **环境坑**：`cpa.luode.vip` 与容器 `127.0.0.1` 两条入口的告警计数不同步——本机把域名侧 IP 打到封禁后，容器内 127.0.0.1 也返回 403；改走 `http://45.207.222.65:8317` 直连才 200。

- 会话（2026-11-13 21:57-22:35）：**Safeguard Killer responses 通道三层根因（0.1.8/0.1.9/0.1.10）**：
  - **用户指令**：「session-fc904680 会话 deepseek-v4-pro 还是在出错, 好像破甲还是没有生效」（三轮复报）。
  - **关键前提**：DSH 的 `luode` provider 走 `api: openai-responses`，请求体 `{model, input:[...], stream:true}` **没有 `messages` 字段**，系统提示在 `input` 的 `role=system/developer` 项里；推理通道由 `openai-responses.js:282-301` 强制开启（`reasoning:{effort,summary}` + `include:["reasoning.encrypted_content"]`）。
  - **第一层（0.1.8，提交 `eb677b9`）**：`sanitizeRequest` 只处理 `body["messages"]` ⇒ 破甲在 responses 通道**全程空转**；分组全失败时 `emitExhaustedNoticeStream` 的 `else` 只发 chat 帧，不含 `response.completed` ⇒ CPA `responsesSSEFramer` 报 502 `upstream stream closed before a terminal event` ⇒ 客户端 `TRANSPORT` 重试（该会话累计 89 条 `llm/retry`）。修复：新增 `isResponsesFormat`（认 4 种同义写法）、`buildResponsesExhaustedBody`/`buildResponsesExhaustedSSE`（五帧 `response.completed` 收尾）、`cleanSystemField`；`sanitizeRequest` 补 `input`/`system`/`instructions` 三处；`cleanMessages` 加 `dropWholeSystem` 参数（chat 传 true 整条丢，responses 的 `input` 传 false 只剥片段，否则连用户自己的系统提示一起删）。
  - **第二层（0.1.9，提交 `b1116af`）**：推理通道下上游整段流**一个 `response.output_text.*` 都没有**（只有 `reasoning_summary_text.delta` × 24 + `response.incomplete`/`max_output_tokens`），旧判据只认 chat 形态 ⇒ 判空响应。更深一层：`prefetchStream` 传入的是**带 `data: ` 前缀的 SSE 文本**，`hasTruncatedOrReasoningOutput` 首行 `json.Unmarshal` 必失败 ⇒ **0.1.6 的保护从一开始只覆盖非流式**。修复：拆两层，外层按首字符分流（`{` 走单对象，否则逐行剥 `data:`），内层 `hasTruncatedOrReasoningObject` 四组 responses 判据。
  - **第三层（0.1.10，提交 `6c8d0a0`）**：`inspectPayload` 把 `Delta` 声明成对象指针，而 responses 帧的 `delta` 是**裸字符串** ⇒ `json: cannot unmarshal string into Go struct field ... of type struct {...}` ⇒ **整帧反序列化失败**直接返回全空值，正文永久丢失（原 `response.output_text.delta` 补救分支在整帧失败后根本不执行）。修复：`Delta` 改 `json.RawMessage` 先试字符串再试对象；`sawEnvelope` 判据改 `len(msg.Delta) > 0`；`item` 补 `content[]`、`response` 补 `output[]`。
  - **验证**：三版各加测试（0.1.8 21 用例 / 0.1.9 8→18 + 2 流式端到端 / 0.1.10 7 用例），每版三组变异全 KILLED 且为断言失败；cgo-shim 全绿、gofmt 空输出。
  - **发布与热重载**：0.1.8 `82f12544…b047d8f` / 0.1.9 `fa2b4df9…9acca982` / 0.1.10 `0dbfa6fd…4d79afe5`，三者落盘 sha256 与本地 zip 逐字节一致，均 `restart_required:false`、容器未重启。
  - **真机复测（0.1.10）**：`normal max=64` × 3 全部 200/9 事件/`response.completed`/`sk=False`/正文「收到」；`reasoning max=24` × 2 全部 200/32 事件/`response.incomplete`/`sk=False`；`state.decisions` 15 条全 `served by deepseek-v4-pro`，零失败。
  - **两个坑**：① 清空 groups 会让插件被宿主注销（`model.register` 返回空列表 ⇒ `registered:false`），仅 PUT 写回不够，必须再 install 触发重新注册，探针应改「换成员」；② CI 每次 dispatch 出两个 run，只有跑 build-cross 的那个 `Release: success` 才算发布成功。

- 会话（2026-11-13 00:00-00:50）：**Safeguard Killer 0.1.7：删除前身遗留的配额采集与余额调度，只保留破甲与分组**：
  - **用户指令**：「safeguard-killer 删除一些和破甲无关的功能逻辑」。判据=三条链是否被破甲依赖：破甲链（`intercept_before/after`→`sanitizeRequest`、`intercept_stream_chunk`、`request.complete`）与分组换模型链（`model.route`/`executor.execute*`/`groups.go`）保留；余额调度链（`scheduler.pick`→`pick`+`balance.go`+`quota.go`+配额类型+面板凭据页）整块删除。
  - **删除依据（三个实际负担）**：① 注册 `Scheduler` 抢宿主凭据调度，绕过 CPA 账号池策略；② `management_key` 隐性必填，`skipReason` 首行 `if cfg.ManagementKey == "" { return "", nil }` 让同一配置在两种环境行为不同；③ `quota.go` 1324 行维护 8 个 provider 私有 usage 端点，上游改版即失效。
  - **改动**：整删 `quota.go`(1324 行)/`quota_test.go`(482)/`balance_test.go`(298)；`balance.go` 434→71 行（只留 `hostCall`/`stateMu`/`decisions`/`sessionKey`/`pruneSessions`/`recordDecision`/`buildUIState`）；删 `scheduler.pick` 与 `Scheduler` capability、`min_remaining`/`management_key`/`LegacyManagement`、五个配额类型与 `decision.Ranking`、`skipReason` 与 `orderByQuotaWeightedRandom`（`orderByStrategy` 去掉 `cfg`/`now` 参数）、面板凭据 tab + 配额条 + ranking + 三 banner（会话表改 6 列）。
  - **刻意保留**：`legacyNoticePrefix`（剥离历史 assistant 消息旧前缀）、`hostCall`（破甲流式与分组链共用 + 测试替换点）、`idle_minutes`（`bindGroupSession` 算 `IdleExpiresAt`）、`detectStoredManagementKey`（读 CPA 管理密钥调宿主 API，非插件配置项）。
  - **验证**：`cgo-shim-build.py safeguard-killer` build+vet+test 全绿；16 个 `*.go` 归一后 `gofmt -l` 空输出；面板内联 script `node --check` exit=0；DOM id 交叉校验缺失为空（28 个 id）；残留 grep 只剩刻意保留的 `legacyNoticePrefix`；UI 已删符号 19 模式 0 命中。
  - **发布（已完成）**：提交 `d4c2146`（15 文件 **+132/−3096**）→ 文档 `13c57f5` → CI run 1628 的 `Release` job success（run 1627 Release skipped，不能当发布成功）→ tag `safeguard-killer-v0.1.7` 6 资产（5 平台 zip + checksums，新增 `windows_arm64` 槽位）`ALL CHECKSUMS OK` → `.so` 26,742,816 B `bd945859d4497c585ba20cbedb3b5aab6cbc7d9bc8a579f4dc4775c3337f4f94` → registry `16cc5db`（`publish-assets.py --allow-missing` 5 artifacts + 手改描述删 `quota-aware credential balancing`/`Requires management_key`、tags 去 `Scheduler`/`Quota`）。
  - **生产热重载与验收**：`install?version=0.1.7&source=source-636fa01121af` → 200 `restart_required:false`、`hot reloaded active_version=0.1.7 retired_version=0.1.6`；落盘 `.so` sha256 与本地 zip **逐字节一致**；`.so` 内 `scheduler.pick`/`host.auth.list`/`orderByQuotaWeightedRandom`/`skipReason`/`min_remaining`/`groupStrategyQuota` **全部 0 命中**（删除项确实不在二进制里），`hasTruncatedOrReasoningOutput`/`reasoning_content` 仍在；`state.config` 只剩 `features`/`groups`/`idle_minutes`/`stream_prefetch_bytes` 四键。真机：`deepseek-v4-pro`+300 → 200 `finish=stop` 正文「你好呀，见到你真好！」`sk_switch=False`；大写 `DeepSeek-V4-Pro` → 200 归一为小写；流式 14 帧 `finish=stop`；`hy4free`+300 → 200 `finish=length` `reasoning_len=505` **不判空响应不换模型**（0.1.6 截断放行仍在）；`state.decisions` 新增 2 条 `kind=group` `served by deepseek-v4-pro`（分组链仍工作）；面板 `/ui` 200（50,219 B）、`panic|fatal|unknown_method` 10 分钟 0 命中；容器 `RestartCount=2` `StartedAt=2026-11-11T22:52:32Z` **未重启**；磁盘 78%。
  - **当时遗留的缺口**：responses 通道破甲空转与兜底帧缺终止事件，已由 0.1.8 修复（见上条会话）。

- 会话（2026-11-12 23:28-23:50）：**生产磁盘 91% → 77%：/tmp 历史排查产物 + docker 悬空层回收（4.0 GB，零服务中断）**：
  - **回收前**：`/dev/vda1 29G 27G 2.8G 91%`；`/tmp` 852 个文件合计 **2,954.6 MB**；`docker system df` Images 4.672GB 中可回收 **607.5MB**（5 个悬空层）。
  - **第一轮 2,657 MB / 38 项**：`/tmp` 根下历史排查产物全清，Top：`cpa_hist.txt` 688.5M、`q.log` 595.3M、`cpa_53.txt` 399.9M、`cpa_sess.txt` 381.9M、`cpa_glm53.txt` 239.2M、`cpa_gate.txt` 112.4M、`cpa_wb.txt` 80.2M；另清 `cpa-pre/`（11-08 插件包临时下载）、`njs/`+`qjs/`（JS 反混淆）、`auths_probe/`（账号探测副本）、`cpasrc/`（空）。
  - **第二轮 326.4 MB / 17 项**：`cpa24.log` 258.7M、`cpa6h.log` 60.9M、`cpa553b.txt` 6.2M 及其余 14 个残留小文件（`cpa2.log`/`cpa.log`/`g41.txt`/`na.log`/`cpa54.txt`/`qweb.html`/`panel024.html`/`wbfeed.txt`/`ps.json`/`store.json`/`acc.json`/`ps_before.json`/`wb_credits.json`/`cpa553.txt`）。
  - **docker 侧**：`docker image prune -f` 清 5 个悬空层（607.5MB → 42.75MB）；用 `open(r+b).truncate(0)` 保 inode 截断超限 json-file 日志：`cli-proxy-api` 459M→1.83M、`new-api` 135M→0.57M、`nginx` 29M→28.61M（自身已轮转）、`wangge` 5.2M、`xiaoshuo` 1.06M。
  - **轮转未生效根因**：`/etc/docker/daemon.json` 已声明 `log-opts.max-size=50m/max-file=3`，但既有容器创建时 `LogConfig.Config` 为空**未继承**，日志涨到 459M；排查须看 `docker inspect --format '{{.HostConfig.LogConfig}}'`。
  - **安全边界（未碰）**：mysqld 持有的 4 个 `/tmp/ib*` deleted 句柄（`/proc/2455869/fd`）、`systemd-private-*`、`/apps/mysql8.1` 12G、`/var/lib/containerd` 4.5G、在用容器镜像；删除前用 `/proc/*/fd` 全扫确认无进程持有。
  - **结果**：`/dev/vda1 23G 29G 6.8G 77%`（avail 2.70 → **6.8 GiB**），`/tmp` 2.95 GB → **5.4 MB**；9 容器全 Up（`RestartCount=2` **未重启**）、11102/panic/fatal 近 30m 全 0、`safeguard-killer version=0.1.6`、真实请求 `deepseek-v4-pro` → 200 `finish=stop`。

- 会话（2026-11-12 22:20-23:12）：**Safeguard Killer 从「破甲」退化成「换模型」：同名分组劫持 + 空响应误判（0.1.6 已上线）**：
  - **用户诉求与定调**：报「wokrbuddy 的 deepseek-v4-pro 还是不停失败、重试、切换模型依旧失败」，随后定调「Safeguard Killer 希望要的是破甲的能力, 而不是切换模型的能力」——破甲是目的，切模型只能作兜底。
  - **复现（3/3 稳定）**：小写 `deepseek-v4-pro` + `max_tokens=300` → http=200 但响应 `model` 变成 `glm-5.3-flash`，正文前缀 `⚡ [Safeguard Killer] hy4free (empty response (no text or tool calls)) → switched to glm-5.3-flash`，耗时 7.2–10.7s；大写名反而直通。new-api 把 109 次全记 type=2 成功，用户侧只看到「换了模型」。
  - **根因第一层（破甲被架空）**：分组 `id` 被改成与 workbuddy 注册 ID 同名的 `deepseek-v4-pro`，成员却是 `['hy4free','glm-5.3-flash','minimax-m3.1-flash']`，**没有 deepseek-v4-pro**。`safeguard-killer/config.go:220-227` 按 id 精确匹配、`groups.go:181-192` 命中即劫持并 `rotateModels` ⇒ 请求被逐成员轮转，**从未发给目标模型本体**，破甲改写落在链首思考模型上。架构前提：破甲走 `refusal.go:1401 handleRequestIntercept` → `:1425 sanitizeRequest`，**与分组完全解耦**，故清空分组后破甲照旧生效。
  - **根因第二层（思考被判拒绝）**：`hy4free` 思考链吃满 `max_tokens=300` 配额（`finish=length`、正文空），旧判据 `refusal.go:749-763` / `:810-813` 只认可见文本或 tool call，且 `Choices`（`:466-480`）**无 `reasoning_content` 字段** ⇒ 判成 `empty response (no text or tool calls)` → 触发 `fallback_on_refusal` 换模型。量化：300 下 10 次 **OK=1 EMPTY=9**；2000 下 `reasoning_len` 714/545/919。
  - **修复**：① 生产 `groups=[]`（备份 `config.yaml.bak-20261112_2233`，76,835 B），刻意不重建分组；② `refusal.go` 新增 `hasTruncatedOrReasoningOutput`（认 `finish_reason=length`、`message/delta` 的 `reasoning_content`/`reasoning`），`checkNonStreamResponse` 与 `prefetchStream` 空判据命中即放行、预取遇推理内容即转发；`content_filter`/`refusal`/`detectTextRefusal` 判据不变；刻意**不抬 max_tokens**（`workbuddy/model_specs.go:3-24` 记录 2026-11-07 已删除全部自研 max_tokens 控制）。
  - **验证**：`refusal_truncation_test.go` 10 用例 + 哨兵确认进编译 + **3 组变异全 KILLED 且形态为断言失败**；`cgo-shim-build.py safeguard-killer` 全绿。发布 `2324bb9`（5 文件 +186/−6）→ CI run `43241690267`（`allow_partial=true`）4 平台 `ALL CHECKSUMS OK` → registry `7bf67bf` → 热重载 `active_version=0.1.6 retired_version=0.1.5`、落盘 `.so` sha256 `f178ac5acf369f4de4598f3b6ed85816342aeea4d879f67e91fbda95c4c9c3a6`（29,372,928 B）与 release zip 一致、容器未重启。
  - **生产验收（决定性）**：`deepseek-v4-pro`+300 → 200 `model=deepseek-v4-pro` `finish=stop` 无切换；`hy4free`+300 → 200 `finish=length` `reasoning_len=449` 无切换；分组场景 `id=sg-trunc-test`（单成员 hy4free）+300 → 200 `finish=stop` 正文「请提问」**SK切换=False**（修复前必降级到 glm-5.3-flash）。验证分组已删除，`groups` 回读 `[]`。
  - **新踩坑**：① dispatch CI **前必须先 commit+push**，否则构建远端 HEAD、产物不含本地修复；② `plugin-store install` 多源同 id 返回 409 `plugin_store_source_required`，须带 `&source=<id>`（源 id 现取勿硬编码）；③ ssh 用 `cpa-server` 别名（root / 18998 / IdentityFile 已配），`luode@IP` 会 Permission denied。
  - **文档**：`doc/4-bugs/2026-11-12_230000_SafeguardKiller分组劫持破甲退化为换模型根因与修复.md`（172 行），提交 `c7f0c54`。**遗留**：分组是否重建与中性 codename 待裁定；其它 provider 插件是否有同构空响应判据待同族同步。

- 会话（2026-11-12 22:05-22:52）：**workbuddy 0.21.26：导入原生兼容第三方 `auth_raw`/`profile_raw` 记录（已发布 + 生产验收通过）**：
  - **用户诉求**：先报「work10.json 导入到 wokrbuddy 失败了」，裁定「改成可导入的给我们手动导入就可以了」，拿到转换产物后进一步要求「把 wokrbuddy 的导入兼容一下这种格式的 json，下次我们就不用手动转换手动导入了」。
  - **根因（非插件 bug）**：第三方导出是**顶层数组 + `auth_raw`/`profile_raw` 嵌套 + `access_token` 蛇形命名**，而 `workbuddy/main.go:510 parseStored` 只认「顶层有 `auth` 键的嵌套形态」与「扁平 `accessToken`」⇒ 逐条 `parse_error: missing accessToken`。
  - **实现（6 文件 +267/−6）**：新建 `workbuddy/auth_thirdparty.go`（`thirdPartyRecord`、`isThirdPartyStored` 认 `auth_raw`/`profile_raw`/`access_token` 任一键、`parseThirdPartyStored`、`epochsToSeconds` 用 `>1e11` 判毫秒）；`parseStored` 改**三分支分发**（嵌套 / 第三方 / 扁平），末尾统一校验 accessToken；`panel.html:266` 导入提示补第三方形态；`VERSION`+`main.go:359` = 0.21.26；新增 `test/workbuddy/auth_thirdparty_test.go` 5 用例。**不动 panel 的 `expandCredentials`**——转换收敛在 `parseStored` 一处，导入、认领、执行、导出回读等 10 个调用点自动受益。
  - **验证**：cgo-shim 全绿（34.078s）；真实 `work10.json` 10 条逐条过 `parseStored` 输出 `verified 10/10`（`expiresAt=1796910717` 秒级、`domain=www.workbuddy.cn`、at/rtLen 1539/1540 全对）；gofmt 干净、panel `node --check` exit=0。
  - **发布与生产**：`fabbed3` → CI `43238620141` Release success（4 assets `ALL CHECKSUMS OK`）→ registry `adf788c`。热重载 `active_version=0.21.26`，落盘 `.so` 8,466,976 B sha256 `fca9f155…70cca` 与 zip 一致，`grep -a -c auth_raw`=3；面板 200/86919 B 含 `auth_raw`；账号池 30 个、work10 的 10 个 uid **10/10 已在生产**。
  - **生产行为验收（决定性）**：取 uid `399529bf-4251-4f08-b9a8-c0ca053c4b3d` 的**原始未转换记录**直接 POST `/import` → 200 `success:true`；落盘 `auth keys=['accessToken','domain','expiresAt','refreshToken']`、`expiresAt` 为 **int 1796910717**、`auth_raw` 残留 False。
  - **新踩坑**：① `registry.json` raw CDN 滞后 **4.5 分钟**，改用 GitHub Contents API（`/contents/registry.json?ref=main` + base64）才读到；② auth 文件在**容器内** `/root/.cli-proxy-api/`，容器内无 `python3`，需 `docker exec cat > /tmp/x.json` 拉到宿主再解析；③ 生产管理密钥在 `config.yaml` 第 **479** 行 `workbuddy-provider` 段的 `cpa_management_key`（9 字节明文），`remote-management.secret-key` 取不到（`rm_key_len=0`，用它 401）。
  - **遗留**：`darwin_arm64`/`windows_arm64` 缺件（按 `--allow-missing` 剔除槽位）；`doc/5-tests/` 与 `doc/6-review/` 未补验证与风格回归记录。

- 会话（2026-11-12 21:09-22:10）：**qoderwork 0.13.17：面板「测试」把上游容量排队（10605 排队帧）与账号故障分开（生产已生效）**：
  - **用户诉求**：贴生产面板截图 + 报错 `活跃测试失败 (模型: Qwen4-Max-Preview): upstream 200: 403: 10605: {"isQueued":true,"modelKey":"qmodel_latest","queueCount":1,"queueType":"p4","retryAfterSeconds":60,"serviceAvailable":true,...}`。
  - **根因（非账号故障）**：该响应是上游**容量排队帧**（HTTP 200 + 顶层 `code=403` + 内嵌 10605/`isQueued`，`serviceAvailable=true`）。failover 侧早已正确拦截（`accountFailover.go:274` 排队帧 `return false` 不计数不冷却；`failover_disable.go` 不触发禁用），**缺陷只在用户呈现层**：`sendActivePingQoder` 把排队帧当普通错误抛，面板只显示红色「测试失败」+ 截断 200 字符的转义串，无法分辨「上游拥堵」与「账号坏了」。`Qwen4-Max-Preview` 走 `lookupUpstreamKey` 运行时映射到 `qmodel_latest`，无硬编码映射，属正常。
  - **实现（3 文件 + 1 新测试，+223/−14）**：`policy.go` 新增 `queueFrameInfo`（8 字段与上游同名）、`waitSeconds()`（`retryAfterSeconds` 优先、回退 `waitTime`）、`parseQueueFrameInfo`（对每个 `{` 起点用 `json.NewDecoder` 试解，规避前缀不能整体 Unmarshal）；`active_ping.go` 新增 `*queueFrameError`（`Error()` 文本逐字不变供历史日志对照）+ `queueFrameNotice` + `buildActivePingFailure`；`panel.html` 结果渲染改**三态**（ok / warn / err）。**键名用 `upstream_queued` 不用 `queued`**——后者在本插件 `credits_handler.go:233`/`management.go:255` 是「后台刷新排队数」。**不做自动重试**（上游有 110 日调用次数上限，且 `defaultActivePingTimeout=20s` 装不下 60s 等待）。
  - **验证**：cgo-shim 全绿（14.4s）；新增 7 测试 + 4 项既有排队帧回归 PASS；哨兵 `TestZZSentinelQueueFrameCompiled` 证实进编译；变异（`parseQueueFrameInfo` 改恒假）触发 FAIL；panel `new Function()` `blocks=2 bad=0`。
  - **发布与生产**：`7c4527c`（11 文件 +815/−16）→ CI `43231575197` Release success → tag `qoderwork-provider-v0.13.17`（5 assets）→ registry `0791998`；热重载 `0.13.17`，`.so` sha256 `911c983e…34725d8`（8,363,872 B）与 zip 一致，面板 87,771 B 逐字节一致，容器未重启。
  - **生产行为验收（决定性）**：`POST /test-active` 打 `Qwen4-Max-Preview` → 200 / 0.323s，返回 `upstream_queued:true` + `retry_after_seconds:60` +「账号与凭据正常，属上游容量排队而非测试失败」；改造前同请求是红色「活跃测试失败」。
  - **文档**：`doc/4-bugs/2026-11-12_214500_…根因与修复.md` + `doc/3-实施/…_实施总览.md` + `doc/5-tests/…验证.md` + `doc/6-review/…_6-review.md`（`STYLE: PASS`）。
  - **同族未同步（需当轮授权）**：`qoder-ai/active_ping.go` 与 `qwenwork/active_ping.go` 结构同构可照搬；`workbuddy`/`workbuddy-ai` 探活只读首包 512 字节即判成功（排队帧会被误判成功），属另一独立问题。

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
  - **已收口**：0.21.24 于 11-12 01:56 热重载上线，六轮定时观察 `content_filter` 全程 0（生产无审核样本），新分支未经真实数据触发但零误伤。

- 会话（2026-11-12 01:56-02:20）：**workbuddy 0.21.24 发布上线 + 生产观察**：
  - **发布链**：CI run `43071825610` Release success（4 平台：darwin_amd64/linux_amd64/linux_arm64/windows_amd64；`darwin_arm64` 被取消、`windows_arm64` 构建失败，按 `--allow-missing` 剔除槽位）→ 资产 sha256 自校验 `ALL CHECKSUMS OK` → registry `d3356c8` → 生产 `plugin-store install` `status=installed, restart_required:false` → `hot reloaded active_version=0.21.24 retired_version=0.21.23`，**容器未重启**（Up 4 hours）。
  - **一致性**：生产落盘 `.so` sha256 `e7ac1632bf6665aa…`（8,454,624 B）与本地 zip 内 `.so` **逐字节一致**；`strings` 确认新判据已入二进制（`"content_filter"` / `"content filter"` / `sensitive content` / `max_output_tokens` / `finish_reason: content_filter`）。
  - **观察结论：无审核样本**（`content_filter` 命中 0，`docker logs` 与容器内 feed 双向确认）。窗口内失败全是 `11102`（大写模型名，走 HTTP 400 → `failureScopeNone`，与本轮改动无交集）；小写别名 `deepseek-v4-pro` **217 次全成功**。
  - **误伤检查全通过**：部署后 workbuddy **0 个账号被失败禁用**；账号池 20 个、**16 个仍可路由**；4 个 disabled 全部是 credits 耗尽/低积分（`exhausted`/`low_credits`），无一为失败禁用。
  - **旧实例干净退出**：`failover reconcile loop stopped` + `probe loop stopped: superseded by newer instance`，无僵尸巡检（对照 2026-11-08 coze 事故）。
  - **观测口径修正（自身错误）**：首轮误判「feed 停止写入」，因查的是宿主机上的**过期副本**；真实写入在**容器内** `/CLIProxyAPI/data/token-usage-feed.ndjson`。**feed 必须在容器内查**；sidecar 与 feed 是两条独立路径，记账观测不受影响。
  - **新增端到端用例** `workbuddy/content_filter_end_to_end_test.go`（提交 `d8d9eab`）：用生产真实分片形态串起「判据 → 分类 → 冷却落盘」+ 3 条反例守卫。**踩坑**：首版无守卫直接调 `recordSSEErrorFailureForModel`，反例立即被记成冷却（测试不忠实，生产五处调用点都有 `if isContentModerationFailure(...)` 包裹），已改为「先判据、后记账」。
  - **定时观察**：`schedule-bad2541a`，2 小时间隔 × 6 次，判定标准为「出现 content_filter 日志 / sidecar mtime 推进到部署之后 ⇒ 新分支已触发；仍为 0 且无审核流量 ⇒ 报告无样本，不编造结论」。
  - **追加观察（03:12-04:48）**：仍无审核样本。04:14 一次失败禁用（`workbuddy-7ee5e596`/`GLM-5.3-Flash`）经追踪 `content_filter` 命中 0、失败全为 11102、该号 `credits.total_remain=0`/`exhausted=True` ⇒ 与本轮改动无关。04:39 容器优雅重启（`ExitCode=0`、`OOMKilled=false`，非本轮操作）后仍加载 0.21.24。

- 会话（2026-11-12 08:12-21:20）：**大写模型名 11102 根因定位 + 两层修复（workbuddy 0.21.25）**：
  - **根因（推翻「new-api 改写」假设）**：大写名不来自 new-api（`oneapi.channels.models` 全小写、`model_mapping` 空、`oneapi.abilities` 查大写返回 0 行、日志 `model=` 与 `alias=` 相同）。两层：① `safeguard-killer` group 成员名与 provider 注册 ID **大小写不一致**，而 `safeguard-killer/groups.go:137-150` `groupServing` 用 `strings.TrimSpace(model) == trimmed` **大小写敏感**精确匹配，命中即经 `rotateModels`（`:154-172`）劫持进 group；② CPA 路由对模型名**大小写不敏感**，但插件 `workbuddy/payload.go` 的 `rewriteModelInPlace`/`rewriteModelInBody` 用 `strings.EqualFold` 判等跳过重写、`workbuddy/models.go` 的 `resolveUpstreamModel` 查不到 alias 时直接 `return m` ⇒ 大写名原样透传大小写敏感的上游 → `11102 model service info not found`（HTTP 400）→ 按账号级故障逐个换号，**一次拼写问题放大成整池遍历**（60m 内 1196 请求 / 1014 失败 84.8%，11102 占 1007）。
  - **修复**：① group `"1"` 成员改 8 项全小写 workbuddy 注册 ID（移除三不存在的 `Doubao-Seed-2.1-Pro`/`hy4`/`qwen3.8-flash`，`hy4` 真名 `hy4free`），备份 `config.yaml.bak-20261112_202935`；② `models.go` 新增归一表 `upstreamModelIDCache`（`ToLower(id)`→注册原样 ID）+ `canonicalUpstreamModelID`（**未命中只去空白原样返回，绝不猜大小写**），`resolveUpstreamModel` 三条返回路径全套，缓存刷新在**释放写锁之后**（持锁调用自死锁）；③ `payload.go` 两处改**逐字节精确相等**。新增 `model_case_normalize_test.go`（10 函数）。
  - **两个假通过陷阱（重要）**：① 归一表未命中分支初版写成 `return canonicalUpstreamModelID(m)`（**自我递归**），被变异验证的 `fatal error: stack overflow`（`models.go:600`）暴露 → 改 `return m`；② 变异脚本 `replace(old,new,1)` 只改首个匹配，锚点出现两次 ⇒ 变异落在自递归行、fallback 从未被改 → **变异存活假通过**且 `restore` 把修复还原成自递归；脚本最终改为**校验锚点恰好命中 1 次**。
  - **验证与发布**：`cgo-shim-build.py workbuddy` 全绿；**2 组变异均 KILLED** 且失败形态为断言 FAIL；代码 `8eae84b`（6 文件 +366/−11）；CI run 1614 `Release skipped`，`allow_partial=true` 重跑 run 1615 才 release。文档见 `doc/4-bugs/2026-11-12_010000_会话压缩阶段异常根因诊断.md`（含「部署后观察：外部会话改写 group 配置」小节）。知识库沉淀 `20-Knowledge/工程实践/忽略大小写的相等判断会短路规范化重写.md`。
