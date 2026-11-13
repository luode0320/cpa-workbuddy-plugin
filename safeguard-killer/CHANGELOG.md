# Changelog

本文件记录 `safeguard-killer` 插件的版本变更。格式按版本号倒序，每条包含背景、改动、验证与涉及文件。

## 0.1.11

### Refactor - 面板不再向用户索要管理密钥，状态接口改走免鉴权的 resource 路由

- 背景: 用户在 CPA 主面板里打开 Safeguard Killer 面板时，看到右上角一个「管理密钥」输入框，以及红色横幅 `HTTP 401: 管理密钥缺失或被拒绝。请在上方输入密钥后点刷新。 {"error":"invalid management key"}`。根因是面板把**读状态**也放在需要密钥的 `/v0/management/...` 下，而密钥只有一个来源——CPA 主面板 `localStorage['cli-proxy-auth']`，且主面板只在用户勾选「记住密码」时才持久化它；没持久化时旧实现就弹输入框让人手填。让使用者手工找密钥、粘贴、点刷新，既不合理也容易因连续失败触发宿主的 5 次封禁（`maxFailures=5`、`banDuration=30m`）。
- 依据: 宿主只对 `/v0/management` 前缀校验管理密钥；`/v0/resource/plugins/<id>/` 走 `pluginResourceNoRoute`，`F:\CLIProxyAPI\internal\pluginhost\management.go` 的 `ServeResourceHTTP` 注释原文即 "dispatches an unauthenticated browser-navigable resource request"。实测 `https://cpa.luode.vip/v0/resource/plugins/safeguard-killer/ui` 200（无鉴权），同域名下 `/v0/management/plugins/safeguard-killer/state` 403。因此状态完全可以走免鉴权通道，面板不需要密钥就能显示。
- 改动: ① `managementRegistrationPayload()` 的 `Resources` 新增 `/state`（`Menu` 留空，避免侧边栏多菜单），与面板 `/ui` 并列挂在无鉴权 resource 路由下；② `handleManagement` 让管理路由与 resource 路由共用同一支（两者都以 `/plugins/safeguard-killer/state` 结尾，无需第二段分支）；③ 面板 `STATE_API_URL` 改为 `/v0/resource/plugins/safeguard-killer/state`，`doRefresh()` 不再带鉴权头、不再处理 401/403；④ 删除 `#mgmtKey` 输入框与全部 `document.getElementById('mgmtKey').style.display = ...` 回退逻辑，`authHeaders()` 收窄为 `mgmtAuthHeaders()`（只返回宿主已持久化密钥的头）；⑤ 保存前先判断能否拿到密钥，拿不到就明确提示去 CPA 主面板「插件管理 → Safeguard Killer → 编辑配置」，或先在主面板勾选「记住密码」；⑥ `detectStoredManagementKey()` 对 `localStorage` 读取与解析全程 try/catch，异常一律返回空串。
- 语义: 读状态永远可用，且永不因缺密钥而报错；**写配置仍需管理密钥**（宿主对 `/v8/management/config/plugins/configs/*` 强制校验，这一点没有变），但面板不再向用户索要——拿不到就引导用户走宿主自己的配置编辑器。
- 验证: `ui_test.go` 同步更新并加强——`TestManagementRegistrationPaths` 断言 `Resources` 恰好两条且 `/state` 的 `Menu` 为空；`TestHandleManagementUI` 改为断言 HTML 引用 `/v0/resource/plugins/safeguard-killer/state` 且**不含 `mgmtKey`**；新增 `TestHandleManagementStateResourcePath` 断言 resource 路由与需要密钥的管理路由返回同一份状态。三组变异全部 KILLED 且失败形态均为断言失败：① 删掉 `/state` resource 声明 → `TestManagementRegistrationPaths` 报 `resources = [{Path:/ui ...}]`；② 把密钥输入框塞回 HTML → `TestHandleManagementUI` 报 `ui html still contains the management key input`；③ 状态 URL 退回管理路由 → `TestHandleManagementUI` 报 `ui html does not reference /v0/resource/plugins/safeguard-killer/state`。变异脚本对每个锚点断言恰好命中 1 次。
- 附带发现: 首版把 `/state` 写成独立 case，与既有的 `strings.HasSuffix(path, mgmtStateRoutePath)` 重叠成死代码——变异 1（删该 case）测试照样全绿。已按最小改动收敛为单支，并在注释里写明两条路由共享后缀的原因。
- 涉及文件: `ui.go`、`ui_test.go`、`VERSION`、`main.go`、`CHANGELOG.md`。

## 0.1.10

### Fix - responses 的裸字符串 delta 不再丢掉正文，SK 不再把正常回答判成空响应

- 背景: 0.1.9 修好了「仅推理内容」的误判，但普通请求（带 system-reminder + `max_output_tokens=64`）仍然 3/3 命中兜底，客户端拿到的正文是 `⚡ [Safeguard Killer] every model in 1 failed: deepseek-v4-pro (empty response (no text or tool calls))`。把分组临时指向 `glm-5.3` 让 `deepseek-v4-pro` 直通上游后取原始流，上游完全正常：`response.output_text.delta` 的 `delta` 是**裸字符串** `"收到"`、`response.output_item.done` 的 `item.content[0]` 是 `{"type":"output_text","text":"收到"}`、末帧 `response.completed`。Go 实测复现了失败点：`inspectPayload` 把 `Delta` 声明成对象指针，遇到字符串报 `json: cannot unmarshal string into Go struct field ... of type struct {...}`，**整帧反序列化失败**直接返回全空值，正文就此丢失；原 `refusal.go` 里那段 `if msg.Type == "response.output_text.delta"` 的补救分支在整帧 Unmarshal 已失败之后根本不会被执行。
- 改动: `inspectPayload` 的 `Delta` 由对象指针改为 `json.RawMessage`，取到后先试字符串形态写正文，失败再按对象形态解 `text_delta` / `input_json_delta` / `refusal` / `stop_reason`；`sawEnvelope` 判据由 `msg.Delta != nil` 改为 `len(msg.Delta) > 0`；删除上面那段已多余的 `response.output_text.delta` 补救分支；`item` 结构补 `content[]`，`response` 结构补 `output[]`，分别提取 `output_text`/`text` 正文，覆盖「正文只出现在 `output_item.done`」或「正文只在终止帧 `response.completed` 里」两种上游形态。
- 语义: 破甲优先、兜底其次。这三种形态都是上游正常产出，不该被当成空响应。`content_filter`、`refusal` 字段、文本拒绝模式与真空白判据一律不变。
- 验证: 新增 `refusal_payload_test.go`（3 个测试函数 / 7 个用例）——`TestInspectPayload_ResponsesStringDelta` 覆盖裸字符串 delta、对象 `text_delta`、`input_json_delta` 记工具调用、对象 delta 的 `refusal` 判拒绝、`output_item.done` 的 `item.content` 带正文；`TestCheckNonStreamResponse_ResponsesStringDeltaPasses` 覆盖终止帧独占正文；`TestPrefetchStream_ResponsesStringDeltaPasses` 覆盖流式字符串 delta。三组变异全部 KILLED 且失败形态均为断言失败：① 不再识别字符串 delta → `refusal_payload_test.go:60: visible = "", want 包含 "收到"` + `:109 prefetchStream() = empty response ...`；② 去掉 `item.content` 正文提取 → `output_item.done 的 item.content 带正文` FAIL；③ 去掉 `response.output` 正文提取 → `TestCheckNonStreamResponse_ResponsesStringDeltaPasses` FAIL。变异脚本对每个锚点断言恰好命中 1 次。
- 涉及文件: `refusal.go`、`refusal_payload_test.go`、`VERSION`、`main.go`、`CHANGELOG.md`。

## 0.1.9

### Fix - 「仅推理内容」的响应不再被误判成空响应，破甲与分组不再被截断思考误伤

- 背景: 0.1.8 补齐了 responses 通道的净化与终止帧，但客户端那条链路的失败并未消失——症状从「502 + TRANSPORT 重试」变成「客户端收到 `⚡ [Safeguard Killer] every model in 1 failed: ...` 兜底提示」。生产 `state.decisions` 在 02:14:09 / 02:16:01 / 02:16:19 / 02:21:07 连续记下 4 条 `failed: empty response (no text or tool calls)`。直连上游复现：带 `reasoning: {effort: "high", summary: "auto"}` 且 `max_output_tokens` 偏小时，上游整段流里**一个 `response.output_text.*` 事件都没有**，只有 `response.reasoning_summary_text.delta` × 24、`reasoning` 类型的 `output_item.added|done`，末帧 `response.incomplete` 的 `incomplete_details.reason = "max_output_tokens"`——活干完了，只是全在思考链里。而 `hasTruncatedOrReasoningOutput` 只解析 chat 形态的 `choices[].finish_reason` 与 `reasoning_content`，对这套 responses 事件一个都不认，于是正文为空 + `sawEnvelope=true` 直接落进空响应判据。
- 背景（更深一层）: 即使把上面的形态补全，流式路径仍然不生效——`prefetchStream` 传给该函数的 `chunk.Payload` 是**带 `data: ` 前缀的 SSE 文本**，函数内直接 `json.Unmarshal` 必然失败并返回 `false`。也就是说 0.1.6 引入的这个保护从一开始就只覆盖非流式请求。这一点由本轮新增的 `TestPrefetchStream_ResponsesReasoningOnlyPasses` 抓出（首版实现下该用例稳定 FAIL 于 `empty response (no text or tool calls)`）。
- 改动: `hasTruncatedOrReasoningOutput` 拆成两层——外层先看首字符，`{` 开头按单个 JSON 对象处理，否则按 SSE 逐行剥 `data:` 前缀后逐条判断；内层 `hasTruncatedOrReasoningObject` 在原有 chat 判据之外新增四组 responses 判据：① `response.reasoning_summary_text.delta` / `response.reasoning_text.delta` 且 `delta` 非空；② 对应的 `.done` 事件且 `text` 非空；③ `item`/`output[]` 中 `type=="reasoning"` 且 `summary` 或 `content` 非空；④ `incomplete_details.reason == "max_output_tokens"`（顶层与 `response` 内两种位置，语义等同 chat 的 `finish_reason=="length"`）。`content_filter` 不在放行范围，仍判 `refusal (content_filter)`。
- 语义: 仍是「破甲优先、兜底其次」，只是把「模型把配额烧在推理链上」正式列为合法产出。空响应判据本身没有放松——真正没有任何产出的流依然判 `empty response`。
- 验证: `refusal_truncation_test.go` 由 8 个用例扩到 18 个（新增 responses 推理增量/完成帧/推理 item 起始帧与完成帧/顶层 `max_output_tokens`/`content_filter` 不算截断/非流式 output 含推理项/非流式空输出等），并新增两个流式端到端用例 `TestPrefetchStream_ResponsesReasoningOnlyPasses`（推理流放行且提前返回）与 `TestPrefetchStream_ResponsesTrulyEmptyStillFails`（真空流仍报错）。三组变异全部 KILLED 且失败形态均为断言失败：① 去掉 responses 推理增量识别 → 3 个 `TestHasTruncatedOrReasoningOutput` 子用例 FAIL；② 去掉 SSE 逐行解析、退回整段 `Unmarshal` → `TestPrefetchStream_ResponsesReasoningOnlyPasses` FAIL 于 `empty response (no text or tool calls)`；③ 去掉 `max_output_tokens` 识别 → `responses 顶层 max_output_tokens 截断` 与 `responses 配额截断放行` FAIL。变异脚本对每个锚点断言恰好命中 1 次。
- 涉及文件: `refusal.go`、`refusal_truncation_test.go`、`VERSION`、`main.go`、`CHANGELOG.md`。

## 0.1.8

### Fix - responses 通道的破甲与兜底帧补齐，修掉「破甲未生效 + 客户端反复重试」

- 背景: DSH 客户端的 `luode` provider 配的是 `api: openai-responses`，请求体形如 `{model, input: [...], stream: true}`——系统提示装在 `input` 数组的 `role=system`/`developer` 项里，**没有 `messages` 字段**。而 `sanitizeRequest` 的 JSON 层只处理 `body["messages"]`，`instructions`/`input`/顶层 `system` 一个都不碰，于是破甲改写（`claudeCyberGatePrompt` 替换、`<system-reminder>` 剥离）在这条通道上全程空转。更糟的是分组全失败时 `emitExhaustedNoticeStream` 的 `else` 分支只发 chat 帧 + `[DONE]`，不含 `response.completed`；CPA 的 `responsesSSEFramer` 只认 `response.completed`/`incomplete`/`failed` 这类终止事件，收不到就返回 502 `upstream stream closed before a terminal event`，客户端随即按 `TRANSPORT` 连续重试 4 次。实测某会话（`session-fc904680`）累计 73 次 `llm/retry`，服务端 `state.decisions` 同步出现 15 条 `failed: refusal (content_filter)` + `every model failed`。
- 改动（破甲覆盖 responses/Anthropic 通道）: 新增 `cleanSystemField`，同时处理顶层 `system` 的字符串与 text 块数组两种形状；`sanitizeRequest` 在 `messages` 之外新增 `input`、`system`、`instructions` 三处净化分支；`cleanMessages` 增加 `dropWholeSystem` 参数——chat/Claude 通道传 `true`（那里的 system 消息整条都是限制文案，整条丢弃更彻底），responses 的 `input` 传 `false`（`input` 里的 system 项常与正常指令混排，只能剥片段，整条删会把用户自己的系统提示一起删掉）。
- 改动（responses 兜底帧）: 新增 `isResponsesFormat`（认 `openai-response`/`openai-responses`/`openai_responses`/`responses` 四种同义写法并忽略大小写空白）、`buildResponsesExhaustedBody`（非流式，产出合法 `object=response` 结构）、`buildResponsesExhaustedSSE`（流式，发 `response.created` → `output_item.added` → `output_text.delta` → `output_item.done` → `response.completed` 五帧，终止事件收尾）；`buildExhaustedNonStreamBody` 与 `emitExhaustedNoticeStream` 在 claude 判断之前先分流 responses；`injectNonStreamNotice` 增加 responses 分支（提示写进 `output[].content[]` 的 `output_text`）；`injectStreamNotice` 对 responses 直接原样返回——`response.*` 事件序列的 `output_index` 必须与流内一致，硬插 chat delta 帧会污染事件流。
- 语义: 破甲优先、兜底其次。破甲现在能真正作用到 responses 与 Anthropic 通道的系统提示；分组全失败时客户端至少拿到结构合法、带终止事件的兜底响应，不再陷入 `TRANSPORT` 重试。
- 验证: 新增 `refusal_responses_test.go`（7 个测试函数 / 21 个用例）覆盖格式识别、流式兜底帧的终止事件与末帧位置、非流式兜底体的 `object=response` 结构、`input` 与 `developer` 角色的系统项净化、顶层 `system` 两种形状、开关关闭时不动请求体、responses 非流式提示注入、responses 流不被插帧。三组变异全部 KILLED 且失败形态为断言失败：① `isResponsesFormat` 退化为永不命中；② `input` 的 `dropWholeSystem` 改回 `true`；③ 删掉 `injectStreamNotice` 的 responses 短路。变异脚本对每个锚点断言恰好命中 1 次，命中数不是 1 直接退出（防「锚点出现两次只替换了第一处」的假通过）。
- 涉及文件: `refusal.go`、`refusal_responses_test.go`、`VERSION`、`main.go`、`CHANGELOG.md`。

## 0.1.7

### Refactor - 删除插件前身遗留的配额采集与余额调度，只保留破甲与分组

- 背景: 插件由 `Quota Smart Router` 演进而来（`legacyNoticePrefix = "⚡ [Quota Smart Router] "` 是其残留），迁移到 `Safeguard Killer` 后仍拖着整套「读取各 provider 配额 → 按 burn rate 排序凭据 → `scheduler.pick` 抢占宿主调度」的链路。这条链路与破甲零耦合：破甲走 `request.intercept_before` → `sanitizeRequest`，分组走 `model.route` / `executor.execute`，两者都不依赖配额。留着它带来三个实际负担：① 插件注册了 `Scheduler` 能力就会接管宿主的凭据调度，把宿主的账号池策略挤掉；② `management_key` 成为必填项，未配置时 `skipReason` 静默返回空、分组行为随配置漂移；③ `quota.go` 1300+ 行维护着 8 个 provider 的私有 usage 端点，随上游改版持续失效。
- 改动（整块删除）: 删 `quota.go` 全文件（provider usage 端点、`ensureQuotaFresh`/`quotaFor`/`fetchCredentialQuota` 六个 `fetch*Quota`、`ensureFallbackModels`/`fallbackModels`、`managementStatus`/`cpaManagementCall`）；`balance.go` 由 434 行重写为 80 行，只保留 `hostCall` 测试替换点、`stateMu`/`decisions` 环形缓冲、`sessionKey`、`pruneSessions`（只清 `groupSessions`）、`recordDecision`、`buildUIState`（模型列表改由配置里的分组 id 与成员推导，会话取自 `groupSessions`）；删 `pick`/`rankCredentials`/`rateCredential`/`rolledWindow`/`governingReset`/`bestRank`/`findRank`/`touchSession`/`authsByID`/`hostCredentialModels`/`hostState`/`hostCache`/`hostMu`/`hostModelsUnsupported`/`mergedModels`/`servesKnownModel`/`containsString`/`isUnsupportedCallback` 及其全部 DTO。
- 改动（类型与配置）: `types.go` 删 `hostAuth`/`quotaWindow`/`credentialQuota`/`rankEntry`/`uiCredential` 五个类型、`decision.Ranking`、`uiState.Credentials|ManagementError|ModelsError`；`config.go` 删 `groupStrategyQuota` 常量、`pluginConfig.MinRemaining|ManagementKey|LegacyManagement`、`defaultPluginConfig` 的 `min_remaining` 默认值、`configurePlugin` 的 legacy management key 迁移段、`validateConfig` 的 min_remaining 校验与 quota 策略分支（报错文案改 `want order or random`）、`pluginRegistration` 的 `management_key`/`min_remaining` 两个 ConfigFields 与 `Capabilities.Scheduler`；`main.go` 删 `methodSchedulerPick` 常量与 `case methodSchedulerPick:` 分支、不再使用的 `time` 导入。
- 改动（分组链）: `groups.go` 删 `skipReason` 整个函数与两条执行路径上的调用（原本配额低就跳过模型，现在只在拒答/报错时才前进）；`refusal.go` 删 `orderByQuotaWeightedRandom`（Efraimidis–Spirakis 加权随机）与 `orderByStrategy` 的 quota 分支、不再使用的 `math` 与 `sort` 导入。
- 改动（管理面板）: `ui.go` 删「凭据」tab 与其表格、`renderWindow`/`renderCredentials`/`renderRanking`/`num`/`pct`/`tierOf`/`fracToPct`/`credName`/`credNameByID`/`details`/`openDetails` 及其 toggle 监听；状态栏去掉「凭据」胶囊，横幅由 5 个减为 2 个（错误/保存），设置表单去掉「管理密钥」与「最低剩余比例」两个字段，会话表由 7 列改 6 列（会话/命中模型/分组/绑定时间/最后访问/空闲缓存到期），决策表去掉 ranking 分支；翻译函数 `zhExcluded`/`zhMgmtError`/`zhStatus`/`STATUS_TEXT`/`zhHostError`/`zhModelsError` 一并删除，`REASON_TEXT` 由 7 条减为 2 条、`zhKind` 只保留分组路由；`--quota-medium-color`、`.quota-bar`/`.quota-fill`/`.windows`/`.win-*`/`.rank` 等配额专用 CSS 清除。
- 保留（刻意不动）: `legacyNoticePrefix = "⚡ [Quota Smart Router] "` 保留——多轮会话里历史 assistant 消息可能仍带旧前缀，`cleanMessages` 需要它才能剥干净；`hostCall` 保留——破甲流式路径与分组执行链都靠它回调宿主；`idle_minutes` 保留——分组会话粘性靠它算 `IdleExpiresAt`；`detectStoredManagementKey` 保留——那是读 CPA 自己的管理密钥用于面板调管理 API，不是插件的 `management_key` 配置项。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿；`groups_test.go` 用新写的 `stubHost`/`noOtherHost` 替代已删除的 `balance_test.go` 辅助函数，`TestExecuteGroupFallsThroughFailuresAndLowQuota` 改名 `TestExecuteGroupFallsThroughFailures` 并去掉配额窗口注入，`TestGroupRoutingStrategiesRandomAndQuotaSticky` 改名 `TestGroupRoutingStrategiesRandomAndOrderSticky` 改用 `random` 策略；提取 `uiHTML` 内嵌 `<script>`（26,728 字节）跑 `node --check` 退出码 0；全包 `quota|Quota|credential|Credential|balance|Balance|scheduler|Scheduler|management_key|min_remaining|host\.auth|rankEntry|hostState|mergedModels` 残留扫描只剩 `refusal.go:21` 的 legacy 前缀常量与 `ui.go` 的 `detectStoredManagementKey`（均为刻意保留）。
- 删除文件: `quota.go`、`quota_test.go`、`balance_test.go`。
- 涉及文件: `safeguard-killer/balance.go`、`safeguard-killer/config.go`、`safeguard-killer/groups.go`、`safeguard-killer/main.go`、`safeguard-killer/refusal.go`、`safeguard-killer/types.go`、`safeguard-killer/ui.go`、`safeguard-killer/groups_test.go`、`safeguard-killer/ui_test.go`、`safeguard-killer/README.md`、`safeguard-killer/VERSION`、`safeguard-killer/CHANGELOG.md`。

## 0.1.6

### Fix - 配额截断与纯推理内容不再被误判为空响应而触发换模型

- 背景: 分组内包含思考模型（如 `hy4free`）时，客户端传较小的 `max_tokens`（实测 300），推理链会吃满整个输出配额，`finish_reason=length`、正文为空、只有 `reasoning_content`。旧实现的空响应判据只认「无可见文本、无工具调用」，把这种正常产出当成模型拒绝，于是滚到分组下一个模型，客户端拿到的回答来自另一个模型，正文前缀是 `⚡ [Safeguard Killer] ... switched to ...`。这使破甲能力退化成模型切换能力。
- 改动: `refusal.go` 新增 `hasTruncatedOrReasoningOutput`，识别 `finish_reason=length`、`message.reasoning_content` / `message.reasoning`、`delta.reasoning_content` / `delta.reasoning`；`checkNonStreamResponse` 与 `prefetchStream` 的空响应判据改为在该识别命中时放行。
- 语义: 破甲优先，换模型只是兜底。推理内容与配额截断都属模型已完成工作，不构成拒绝；真正的 `content_filter`、`refusal` 字段、文本拒绝模式仍按原判据拦截。
- 流式行为: `prefetchStream` 在检测到推理内容时提前结束预取并开始转发，避免整段思考链被缓冲后才吐给客户端。
- 验证: `refusal_truncation_test.go` 覆盖截断、非流式推理内容、驼峰与短名两种字段、流式 delta、空推理内容、非 JSON、以及 `content_filter` 仍被拒绝共 10 个用例；三组变异（删除 `length` 判据、空响应判据回退为无条件报错、删除推理内容判据）全部 KILLED 且失败形态为断言失败。
- 涉及文件: `refusal.go`、`refusal_truncation_test.go`、`VERSION`、`main.go`。

## 0.1.5

### Feat - 用成员模型的真实名称直接进入智能分组，无需客户端改配置

- 背景: 用户配置了一个分组 `id: "1"`、成员是 11 个真实模型名（`DeepSeek-V4-Pro` / `GLM-5.3` / `Doubao-Seed-2.1-Pro` 等），但面板「最近决策」恒为 0 —— 客户端请求的是真实模型名，而旧实现只把分组的 `id`（`"1"`）当作入口，成员名一律不认领。结果是该分组从未被执行器接管，`fallback_on_refusal`（拒答自动换模型）这条最关键的破甲能力一次都没跑过，只有全请求级的四个开关在工作。
- 改动: `groups.go` 新增 `pluginConfig.groupServing`（按成员模型名反查所属分组）与 `rotateModels`（把链路旋转到被请求的模型开头、其余按配置顺序环绕）；`resolveTargetGroup` 在 `id` 未命中后增加成员名命中分支，命中即返回旋转后的成员链。
- 语义: 请求某个成员模型时，先按客户端要求的那个模型执行（保持客户端预期与 prompt 缓存），只有它拒答/失败/配额耗尽才滚到分组里的下一个。分组 `id` 入口与 Claude Code `claude-opus-4-8` 兜底入口的行为保持不变。
- 不引入递归: 分组内部经 `host.model.execute` 下发成员真实模型名，宿主 `modelExecutionRequestFromPlugin` 会把发起插件 id 同时写入 `SkipRouterPluginID`，`applyModelRouter` 据此跳过该插件自身（宿主侧有 `was re-entered despite SkipRouterPluginID` 断言守护），因此不会二次接管。
- 面板: 「智能分组」说明文案补充「也可以用成员模型的真实名称直接进入」。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿；新增 `TestRouteGroupClaimsRealModelNames`、`TestResolveTargetGroupRotatesToRequestedModel`、`TestRotateModels`、`TestProductionStyleGroupEntersByMemberModel`（按生产配置同形数据构造）；哨兵测试 `TestSentinelGroupsZ` 确认新测试真实进入编译链路后删除；两组变异（禁用 `groupServing` 命中、`rotateModels` 恒返回原序）各被 3 条用例捕获，还原后复跑全绿。
- 涉及文件: `safeguard-killer/groups.go`、`safeguard-killer/groups_test.go`、`safeguard-killer/ui.go`、`safeguard-killer/VERSION`、`safeguard-killer/main.go`、`safeguard-killer/CHANGELOG.md`。

## 0.1.4

### Fix - 宿主缺少 host.auth.models 回调时降级运行，不再整体失效并输出红色致命横幅

- 背景: 生产宿主 commit 7417cfb（BuiltAt 2026-10-15）早于上游 2026-10-21 才引入的 `host.auth.models` 回调，插件查询各凭据模型列表时得到 `unsupported host callback host.auth.models`。旧实现把该失败当成致命错误直接上报，导致整份 host snapshot 作废：面板红横幅「路由组件读取宿主凭据列表失败」、凭据与模型列表全空。又因 `skipReason` 用 `containsString(snapshot.models[auth.ID], model)` 过滤，模型列表为空时会把所有凭据全部跳过，模型维度的分组路由实际不可用。
- 改动: `balance.go` 的 `hostSnapshot` 新增 `modelsErr` 承载降级原因，`hostState` 只在 `host.auth.list` 失败时返回错误，模型列表失败改为返回空表继续；新增 `hostModelsUnsupported` 闩（首次发现宿主不支持即记住，避免每个 15s 缓存窗口重复打回调）、`hostCredentialModels()`、`mergedModels()`（宿主优先、管理 API 兜底补空缺）、`servesKnownModel()`（模型列表为空的凭据视为可服务任意模型）。
- 改动: `quota.go` 新增 `ensureFallbackModels` / `fallbackModels` / `credentialModelsCall`，用既有的 `GET /v0/management/auth-files/models?name=` 异步（10 分钟 TTL、4 并发、单飞）补齐模型列表；空结果不覆盖旧表，避免把「该 provider 由代码静态定义模型」误判成「不服务任何模型」。
- 改动: `groups.go` 的 `skipReason` 与 `refusal.go` 的 `orderByQuotaWeightedRandom` 改用 `mergedModels` + `servesKnownModel`；`types.go` 的 `uiState` 新增 `models_error`；`ui.go` 新增黄色 warn 横幅「模型列表来源降级」（`zhModelsError`），删掉 `zhHostError` 里把该情形说成致命的旧分支，凭据表在模型列表未知时标注「按可服务任意模型处理」。
- 不动的部分: `host.auth.list` 失败仍是致命错误并显示红色横幅；后端 `skipped: ` / `failed: ` / `%s has %.0f%% left` 等被测试断言的字符串、`Menu: "Safeguard Killer"`、两个接口常量、默认提示前缀全部保持原样。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿；新增三个用例并单独 `-v` 复跑通过——`TestHostStateDegradesWithoutHostAuthModels`（models 回调报 unsupported 时 hostState 不报错、记录 modelsErr、闩置位）、`TestSkipReasonTreatsUnknownModelListAsUsable`（模型未知时不被跳过）、`TestBuildUIStateUsesFallbackModelLists`（管理 API 兜底列表进入 credentials 与 models，`state.error` 为空、`models_error` 有值）；重新提取 `uiHTML` 内嵌 `<script>`（36905 字节）跑 `node --check` 退出码 0。
- 涉及文件: `safeguard-killer/balance.go`、`safeguard-killer/quota.go`、`safeguard-killer/groups.go`、`safeguard-killer/refusal.go`、`safeguard-killer/types.go`、`safeguard-killer/ui.go`、`safeguard-killer/balance_test.go`、`safeguard-killer/VERSION`、`safeguard-killer/main.go`、`safeguard-killer/CHANGELOG.md`。

## 0.1.3

### Fix - 补齐 0.1.2 遗漏的三处英文残留（凭据状态、宿主回调错误、配额窗口重置前缀）

- 背景: 0.1.2 已把面板主体汉化，但用真实 DTO 形态驱动渲染函数做残留扫描时仍发现三处英文：配额窗口行的 `resets` 前缀、凭据状态徽标直接输出宿主返回的英文 status（`unavailable` 等）、宿主回调错误横幅直接输出 `host callback host.auth.models: unsupported ...` 原文。
- 改动: `safeguard-killer/ui.go` 的 `renderWindow` 把 `resets ` 前缀改为 `重置 `；新增 `STATUS_TEXT` 映射表与 `zhStatus(text)`，把宿主凭据状态值（active/ready/disabled/unavailable/expired/invalid/error/pending/refreshing/cooling）翻成中文，未命中的状态原样透出以便排查；新增 `zhHostError(text)`，对 `host.auth.models` / `host.auth.list` 未实现的回调错误给出中文成因说明并附原文，其余错误原样透出。
- 不动的部分: 后端 `hostState` / `buildUIState` 的取值与判据、宿主原始错误串、`ui_test.go` 断言依赖的 `Menu`、路由路径与两个接口常量；`resets` 只在展示层替换，不涉及任何后端字段名。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿；重新提取 `uiHTML` 内嵌 `<script>`（36905 字节）跑 `node --check` 退出码 0；用 vm + DOM stub 以真实 `uiState` DTO 形态（`credentials[].auth` / `has_quota` / `quota.windows[].remaining`、`decisions[].ranking` / `attempts`）驱动 `render` / `renderGroups` / `renderCredentials` / `renderSessions` / `renderDecisions`，输出的 7614 字节 HTML 中纯英文文本节点仅剩 15 项，全部是凭据名、供应商名、模型 ID、配额来源标识与 `uarr`/`darr`/`times` 三个 HTML 实体符号，无遗漏的可译文案。
- 涉及文件: `safeguard-killer/ui.go`、`safeguard-killer/VERSION`、`safeguard-killer/main.go`、`safeguard-killer/CHANGELOG.md`。

## 0.1.2

### Feat - 管理面板全量汉化（简体中文）

- 背景: 生产面板 `https://cpa.luode.vip/management.html#/plugin-pages/safeguard-killer/0` 全英文，用户反馈「英文的看不懂」，需要简体中文界面。
- 改动: `safeguard-killer/ui.go` 的 `uiHTML` 常量内所有面向用户的可见文案改为简体中文——页面标题与描述、状态胶囊（版本/凭据/分组/会话/最近更新）、工具栏（刷新/放弃/保存/未保存改动）、5 个标签页、两张设置卡片（Router & Quota Settings 4 个字段的标签与说明、Safeguards & Client Adaptations 8 个开关标题与说明、客户端提示前缀）、智能分组编辑器（代号/可见名/路由策略/上移下移移除/添加模型/删除分组/三条策略提示）、校验错误、保存横幅、相对时间与倒计时、配额窗口、凭据表、会话表、决策表与排序表表头、错误横幅、页面异常提示。
- 新增展示层翻译函数（只翻译渲染，不改后端取值与判断）: `REASON_TEXT` 映射表 + `zhReason` / `zhAttempt` / `zhExcluded` / `zhKind` / `zhMgmtError`，把后端返回的英文 reason、attempt result、排除原因、决策类型、配额采集错误在渲染时翻成中文。
- 不动的部分（刻意保留原样）: 后端日志与判据字段（`served` / `skipped: ` / `failed: ` / "%s has %.0f%% left"）以及测试断言依赖的字符串；注册菜单名 `Menu: "Safeguard Killer"` 与路由路径；`STATE_API_URL` / `CONFIG_API_URL` 两个接口常量；技术标识与技术名词（Claude Code / OpenAI/Codex / Gemini / Fable/Mythos / x-cc-* 头名 / safeguards / not_flagged 等）；默认提示前缀 `⚡ [Safeguard Killer] `；`managementRegistrationPayload` 的 Route/Resource Description 一并改为中文说明，但 `Menu` 保持英文（`ui_test.go:15` 断言）。
- 顺带修复: 汉化过程中 `showError('网络错误，请求 ' + STATE_API_URL + ' 失败', String(err))` 曾漏掉一个闭合引号导致内嵌 JS 语法错误，已在 `ui.go` 修正。
- 验证: 提取 `uiHTML` 内嵌 `<script>`（35923 字节）跑 `node --check` 退出码 0；确认 `uiHTML` 内反引号仅首尾各 1 个，Go raw string 未被破坏；`python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿（`ok github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`）；HTML 段残留英文仅技术名词。
- 涉及文件: `safeguard-killer/ui.go`、`safeguard-killer/VERSION`、`safeguard-killer/main.go`、`safeguard-killer/CHANGELOG.md`。

## 0.1.1

### Fix - 把插件声明的 schema 版本从 9 降到宿主实际支持的 6，修复生产注册失败

- 背景: 0.1.0 vendor 进来的 `schemaVersion` 沿用了上游的 9（上游对着 latest 主线开发）。生产宿主 `cli-proxy-api` 的 commit 是 `7417cfb1bbfb794417764f8cd974a637a0779ddd`，其 `sdk/pluginabi.SchemaVersion = 6`，`internal/pluginhost/rpc_client.go:56-68` 的 `registerRPCPlugin` 在拿到注册包后执行 `if resp.SchemaVersion > pluginabi.SchemaVersion { return ... fmt.Errorf("plugin schema version %d is not supported", resp.SchemaVersion) }` —— 声明 9 会被直接拒绝，插件整体注册不上。
- 改动: `safeguard-killer/main.go` 的 `schemaVersion` 常量由 `9` 改为 `6`，并在常量上方补注释说明「必须 <= 宿主 sdk/pluginabi.SchemaVersion」的原因，避免后续再被上游版本号带偏。
- 相容性取证: 全仓库 grep `Registrations` / `omit_body` / `patch_body` / `body_patcher` / `request_body` / `HistoryChunks` 命中数均为 0 —— 插件没有用到 v7（拦截器省略 body）、v8（原始 body + 字节区间 patch）、v9（ModelRegistrar 多 provider Registrations）、v10（多 OAuth 标识）任何新特性。注册包只用 `Metadata` + `Capabilities`，拦截器只用 `Headers` / `Body` / `ClearHeaders` / `Terminate` / `StatusCode` / `ResponseHeaders` / `ResponseBody`，流式拦截只用 `Body` / `DropChunk`，模型注册只产出单 provider `ModelRegistrationResponse{Provider, Models}` —— 全部落在 v6 契约内。且 `configurePlugin` 不按 `schema_version` 分支，宿主传 6 或 9 行为完全一致，降版不改变任何业务逻辑。
- 附带收益: v6 起宿主对 management 响应不再做 HTML 实体转义（`ManagementResponse` 的注释明确 `On schema_version >= 6, JSON bodies are returned without HTML entity escaping`），面板与 `/v0/management/plugins/safeguard-killer/*` 的 URL 类字段可以直接使用，无需前端反转义。
- 验证: 新增 `safeguard-killer/config_test.go` 的 `TestPluginRegistrationSchemaVersionWithinHostContract` —— 断言注册包 schema 与常量一致且不超过宿主上限 6；用临时哨兵 Test 确认该文件真实进入编译链路（shim 输出 FAIL）后删除哨兵。`python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿（`ok github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`）。
- 涉及文件: `safeguard-killer/main.go`、`safeguard-killer/config_test.go`、`safeguard-killer/VERSION`、`safeguard-killer/CHANGELOG.md`。

## 0.1.0

### Feat - 首次从上游 vendor 并纳入本仓库发布链路

- 背景: 需要一套能在本仓库自行构建、自行发布、可长期维护的「拒答救援 + Smart Groups 路由」能力。上游 `esauvisky/CLIProxyAPI-Safeguard-Killer-Plugin` v0.5.10 已具备完整能力（拒答检测与透明换模型、Claude Code 拒答污染头剥离、auto-mode 分类器旁路、配额加权凭证平衡、5 分钟 prompt-cache 粘性），因此原样 vendor 进本仓库，只改归属与版本号，不改业务逻辑。
- 归属改造: `pluginName` 保持 `Safeguard Killer`；`pluginAuthor` 改为 `luode0320`；`pluginRepo` / `pluginLogo` 指向本仓库；`pluginVersion` 由常量改为 `var pluginVersion = version`，版本号交由 CI 以 `-ldflags -X main.version` 注入，与本仓库其余插件一致。plugin id 仍为 `safeguard-killer`，因此管理路由 `/v0/management/plugins/safeguard-killer/state` 与 `CONFIG_API_URL` 无需改名。
- 模块改造: `go.mod` module 改为 `github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`，`go 1.26` → `go 1.26.0` 对齐仓库其余插件；依赖不变（`github.com/tiktoken-go/tokenizer v0.8.1`、`gopkg.in/yaml.v3 v3.0.1`、间接 `github.com/dlclark/regexp2/v2 v2.5.1`）。插件不依赖 CLIProxyAPI SDK，纯 ABI v1，因此与本仓库任何插件并存且不受宿主 SDK 版本约束。
- 发布链路: `build.yml` 增加 tag 模式 `safeguard-killer-v*`、dispatch choice、test matrix、`PLUGINS` 字典、release job 的 tag 判定与 `tag → plugin` 分支、`DISPLAY` 显示名；`registry.json` 增加 6 平台（darwin amd64/arm64、linux amd64/arm64、windows amd64/arm64）direct 安装条目。
- 本地验证工具修复: `scripts/cgo-shim-build.py` 原先只会替换标准 ABI 符号，遇到本插件的三个 C 静态封装（`store_host_api` / `call_host_api` / `free_host_buffer`）会编译失败。已补三个 shim 桩并把 `C.free_host_buffer` 的替换规则排到 `C.free` 之前（前缀规则会先把 `C.free_host_buffer` 吃成 `cgoFree_host_buffer`）。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿（`ok github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`）；`python scripts/validate-registry.py registry.json` 返回 `OK registry.json: 12 plugin(s), schema_version=2`。
- 涉及文件: `safeguard-killer/`（16 个 `.go` 含 7 个 `_test.go`、`go.mod`、`go.sum`、`LICENSE`、`Makefile`、`README.md`、`VERSION`、`tests/abi_test.c`）、`assets/icons/SafeguardKiller.png`、`.github/workflows/build.yml`、`registry.json`、`scripts/cgo-shim-build.py`。
