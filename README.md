# cpa-workbuddy-plugin — CLIProxyAPI 插件集合

[CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) 的 Go 原生插件集合，将主流 AI 服务与私有逆向协议封装为 **OpenAI / Anthropic 兼容 Provider**，供 CPA 网关统一调度。套件支持多账号运维管理、账号故障禁用与探活自愈、40x 同请求换号重试、会话粘性路由、动态模型发现与别名映射、流式长推理稳定性加固（首包即停与 liveBody 实时流）、每日自动签到与积分生命周期管理、额度领取与客户端指纹模拟，以及统一 Token 用量统计与实时看板。

每个插件采用独立模块、独立版本与独立 Release 生命周期管理。本文档重点说明以下七个插件：

| 插件 ID | 源码目录 | 服务商 / 目标服务 | 当前版本 |
|---|---|---|:---:|
| [`workbuddy-provider`](#workbuddy-provider--腾讯-codebuddy-provider) | [`workbuddy/`](workbuddy/) | 腾讯 CodeBuddy（CN + Global） | `v0.17.1` |
| [`workbuddy-ai-provider`](#workbuddy-ai-provider--workbuddy-ai-国际版) | [`workbuddy-ai/`](workbuddy-ai/) | WorkBuddy AI 国际版（workbuddy.ai） | `v0.1.21` |
| [`traework-provider`](#traework-provider--trae-solo-provider) | [`traework/`](traework/) | TRAE SOLO（字节跳动 AI IDE） | `v0.3.0` |
| [`qoderwork-provider`](#qoderwork-provider--qoderwork-cn-provider) | [`qoderwork/`](qoderwork/) | QoderWork CN（qoder.com.cn） | `v0.10.0` |
| [`zcode-provider`](#zcode-provider--zcode-provider) | [`zcode/`](zcode/) | ZCode（智谱 BigModel Coding Plan） | `v0.1.34` |
| [`coze-provider`](#coze-provider--扣子-coze-provider) | [`coze/`](coze/) | 扣子 Coze（对话 + 生图映射） | `v0.5.6` |
| [`workbuddy-token-usage`](#workbuddy-token-usage--用量统计-dashboard) | [`token-usage-tracker/`](token-usage-tracker/) | 统一 Token 用量统计与实时看板 | `v0.2.8` |

> 注：版本以 [registry.json](registry.json)（CPA 插件商店官方可直接安装版本）为准；源码目录内 `VERSION` 可能包含已开发完毕的更高版本。

---

## 插件深度总览

### workbuddy-provider — 腾讯 CodeBuddy Provider

覆盖国内版 `copilot.tencent.com` 与国际版 `workbuddy.ai`（统一基线架构）：

- **OAuth 登录** — 多账号 `workbuddy-<uid>.json` 写入宿主 auth 目录；面板登录轮询按文件路径严格去重。
- **动态模型发现** — 上游 models API 实时拉取 + 5 分钟缓存 + 静态兜底；支持宿主 `oauth-model-alias` 与 `oauth-excluded-models` 治理；兼容宿主 YAML block sequence 与多行 JSON 两种 models 落库形态。
- **执行器** — OpenAI 兼容 chat completions，流式（真 SSE 走 `host.stream.emit`）与非流式；内置 `tool_choice` 归一与 Claude Code 模板清洗。
- **账号故障处置** — 一次上游失败同时写内存阶梯冷却（60s 起、每档 +60s、上限 10 分钟）与盘上 `disabled: true`；后台每 10 分钟串行探活，**只回放当初失败的那一个模型**，探通才写回 `disabled: false`，没有记录失败模型则跳过该账号。写盘后轮询 `host.auth.list` 确认宿主内存已吸收，避免「写了就算禁了」。
- **统一调度内核** — 三类硬排除（`test_failed` 标签、故障禁用、低额）后，存活候选按**高积分优先**排序并平摊负载；全灭时 `Handled: false` defer 交还宿主跨 Provider 容灾；`session_auth` 支持会话粘性绑定。
- **积分生命周期** — 账号耗尽自动禁用、额度回血自动恢复（CN 与 Global 一致，只写盘禁用并保留凭据，不自动删除）；执行器硬积分错误即时触发 reconcile。三档分级：低于硬禁用线 `low_credit_disable_below`（默认 100 点）写盘停用；介于禁用线与保活线 `min_routable_credits`（默认 400 点）之间的打「保活」标，仍可路由但排在正常号之后；两个阈值均**可在配置文件配置修改**（显式 `0` 关闭对应机制）。
- **每日签到** — 每 4 小时（00/04/08/12/16/20）定时自动签到，面板可手动批量签；per-account 互斥锁防止并发冲突。Token 保活使用独立时刻表。
- **计数持久化** — 内存为主 + 跟随定时任务落盘 auth 文件顶层 `success_count` / `failed_count`，容器重启不归零。
- **控制面板** — `/v0/resource/plugins/workbuddy-provider/panel`：积分进度条、CN/Global 筛选、账号删除（二次确认 + 归属校验）、故障禁用与冷却倒计时、选模型测试弹窗。
- **用量 Feed** — 每条请求的 token 消耗以 NDJSON 追加写入共享 feed，供用量插件消费。

---

### traework-provider — TRAE SOLO Provider

基于对字节跳动 TRAE SOLO CN 桌面客户端的逆向封装与工程加固：

- **面板浏览器授权免 IDE 登录** — PKCE S256 + 16 位设备指纹复刻 SOLO 客户端，无需安装桌面 IDE 即可在面板中一键拉起浏览器授权并完成 OAuth 导入。
- **IDE 凭据解析导入** — 支持解析读取本地 IDE `storage.json` 快速同步已有账号（整个文件或单个凭据值均可）。
- **每日签到与防风控** — 每 4 小时（00/04/08/12/16/20）定时 + 面板手动批量签到；采用 SHA-256 确定性派生首位 1~3、末位 1~9 的 16 位纯数字设备号，遇到 9074（「当前参与用户太多」）自动轮换重试，杜绝风控拦截。
- **账号故障处置** — 失败双写（内存阶梯冷却 + 盘上 `disabled: true`）；每 10 分钟串行探活，只回放失败模型，探通才解禁；已删除旧冷却探活通道，恢复路径唯一。
- **长推理流式稳定性** — 宿主流桥 open/read 超时竞速降级直连（30s/90s）；伪完成检测（content + reasoning 双轴健康度）；上游断流兜底收尾（`finish_reason=length`）。
- **实时流与闲置断开** — 实施 `liveBody: resp.Body` 实时直推，遇上游 `event: done` 立即返回哨兵退出大循环并主动切断连接，彻底根除 HTTP Keep-Alive 闲置挂起数十秒的问题。
- **探活提速与模型治理** — `/test-active` 改为流式首包即停 + Keep-Alive TLS 预热，消除思考模型整段思维链等待；支持 `oauth-excluded-models` 模型过滤与会话粘性路由。
- **真实 Token 用量** — 深入解析上游 `event:token_usage`，将真实 token、首字延迟（TTFT）与会话 Key 写入共享 feed。
- **控制面板** — `/v0/resource/plugins/traework-provider/panel`：积分与签到状态、启用/停用、故障禁用徽章与失败筛选口径。

---

### workbuddy-ai-provider — WorkBuddy AI 国际版

针对腾讯 CodeBuddy 独立国际版 `www.workbuddy.ai` 定制的专属 Provider：

- **独立国际版端点** — 聊天走 `/v2/chat/completions`、模型目录走 `/v3/config`，端点硬编码不做多站点切换；凭据统一使用 `workbuddyai-` 前缀，与国内版 `workbuddy-` 账号互不混淆。
- **面板授权登录** — 面板内置 `/login/start` → `/login/poll` 浏览器授权流程，state 有效期 5 分钟、面板每 2 秒轮询；登录成功即消费 state，支持凭据 JSON 导入导出。
- **补注册与 Pro 试用** — CLI 授权的新账号 `areaInfoComplete=false`，插件在落盘前尽力补齐注册地、云账号并领取 `/billing/ide/trial`（250 积分 / 14 天）；已领过时上游返回 14051 记为已领取而非失败，任一步失败都不影响授权落盘。
- **每日签到与 Token 保活** — 签到每 4 小时（00/04/08/12/16/20）触发并叠加 ≤5 分钟随机抖动；Token 保活共用同一六槽时刻表；watchdog 每 10 分钟刷新积分并发一次活跃 ping。
- **调度与故障处置** — 健康候选按**高积分优先**排序；故障为纯内存阶梯冷却（60s 起、每档 +60s、上限 10 分钟），冷却到期前 20 秒开始每秒回放探活，不写盘 `disabled`；支持 `retry_on_4xx`（默认 10）与 1 小时会话粘性。
- **模型发现** — 动态拉取上游模型目录 + 5 分钟缓存与失败负缓存；支持宿主 `oauth-model-alias` 与 `oauth-excluded-models` 治理。
- **控制面板** — `/v0/resource/plugins/workbuddy-ai-provider/panel`：账号列表、积分与签到、批量操作、试用领取、补注册、模型列表与测试探活。
- **用量 Feed** — 每次请求的 token 消耗异步追加至共享 feed。

---

### zcode-provider — ZCode Provider

将智谱 BigModel / Z.ai Coding Plan 封装为 CPA Provider，走 **Anthropic 兼容协议**：

- **桌面客户端指纹模拟（Mimic）** — `internal/mimic/fingerprint.go` 的 `ApplyRequest` 统一注入 20 个请求头：既有 `Content-Type` / `anthropic-version` / `x-api-key` 协议头，也有 `X-ZCode-App-Version`、`X-Device-Mid`、`x-zcode-trace-id`、`x-zcode-agent`、`X-Platform`、`X-Os-Category`、`X-Client-Language` 等指纹头；配置项 `mimic.enabled` 默认开启，关闭时只发 `x-api-key`。
- **双路由与 OAuth 登录** — 按账号套餐分流：`start-plan` 的 JWT 打 `zcode.z.ai/api/v1/zcode-plan/anthropic`，业务令牌打 `api.z.ai/api/anthropic`；支持 Z.ai 官方 OAuth（chat.z.ai / bigmodel.cn）与 Token / API Key 导入，凭据落 `~/.antigravity_cockpit/zcode_accounts/`。
- **额度领取与按模型额度** — `POST /claim` 由 `/billing/preview` 动态解析 planId 并携带阿里云验证头领取额度；额度按模型（entitlement）分别展示进度，数据源主 `/billing/balance`、回退 `/billing/current`。
- **调度与故障处置** — 候选池按**轮询**取号并过滤禁用、`test_failed`、冷却与过期套餐；故障为内存阶梯冷却（60s 起、每档 +60s、上限 10 分钟），冷却前 20 秒每秒回放探活；探活失败才写盘 `test_failed`。
- **模型发现** — 动态拉取上游 `/v1/models`；支持宿主 `oauth-model-alias` 与 `oauth-excluded-models` 治理。
- **控制面板** — `/v0/resource/plugins/zcode-provider/panel`：OAuth 授权、导入导出、批量启停、刷新额度、筛选排序、按模型测试探活、领取额度；主题与宿主 `data-theme` 同步。
- **不做签到** — 本插件不支持每日签到；活跃探活只在额度刷新成功后触发，且同一账号 4 小时内最多一次，无常驻 watchdog。

---

### qoderwork-provider — QoderWork CN Provider

基于对阿里通义灵码衍生国内版 QoderWork 桌面客户端的逆向工程封装：

- **双登录共存** — ① OAuth 设备授权（PKCE，`dt-` 30 天 + `drt-` 1 年自动旋转，登录成功后自动领取一次性 Pro 升级包）② PAT 导入（`pt-`，长期有效兜底），两家族共存于同一 auth 文件。
- **彻底消除假成功漏洞** — 移除了依赖凭据文件创建时间的假导入逻辑，严格基于设备 Token 轮询判定，未在浏览器授权绝不误报成功。
- **COSY 签名推理** — 纯 Go 标准库实现 RSA 包裹 AES 会话密钥 + MD5 请求签名，直连 `gateway.qoder.com.cn` SSE 流式服务。
- **QoderEncoding 协议** — 自定义 Base64 字母表 + 三段重排的请求体编码。
- **多模态与终止加固** — 自定义解析 OpenAI 数组型 Content 多模态报文；统一识别顶层及嵌套 `[DONE]` 信封，即刻 break 退出防止挂死。
- **账号故障处置** — 失败双写（内存阶梯冷却 + 盘上 `disabled: true`）；每 10 分钟串行探活，只回放失败模型，探通才解禁；当日单账号探活失败达 10 次后当日不再探测，本地 0 点恢复。
- **每日 110 日调用上限** — 上游 `Billing daily count exceeded` 走独立 24 小时持久化冷却，排除出路由与换号，仅自然到期解除；面板展示剩余时间并支持人工解冷（`POST /clear-cooldown`）。
- **架构全量对齐** — 账号调度全面接入「硬排除测试标签 + 故障禁用 + 低额红线 + 高积分优先 + 全灭 defer」，面板卡片支持选模型测试探活。

---

### coze-provider — 扣子 Coze Provider

把扣子（Coze）账号接入 CPA，对话与生图统一走扣子 claw 会话通道：

- **同源脚本授权** — 扣子未开放第三方一键授权（`/sign/oauth` 的 `redirect_uri` 硬编码、`/desktop-login` 的协议白名单不可扩展），因此采用一次性同源脚本：用户在 `www.coze.cn` 完成官方登录后执行插件签发的脚本取 `x-tt-token`，插件再以 `passport/token/beat/v2/` 换回 13 条 `Set-Cookie` 落盘。不读浏览器 Cookie 库、不内嵌浏览器、不注册自定义协议。
- **双向无限续期** — `passport/token/beat/v2/` 是双向换发端点（带 Cookie 回 `x-tt-token`，带 `x-tt-token` 回 Cookie），因此会话可持续滚动，不存在快照硬失效。定时续期由插件自建循环负责（默认 4 小时，热重载即跑首轮），不依赖宿主 `auth.refresh` 排期。
- **WebSocket 真流式对话** — 走 `wss://frontier.coze.cn/ws/v2`（`aid=751148` / `fpid=1289` / 子协议 `pbbp2`，业务 `service=33555657`）；WS 必须在 `message/chat` 之前建连，否则丢首帧；`event_delta` 逐帧增量下发，`event_final` 收尾。
- **生图映射（重点）** — 扣子没有独立的图片 API，生图在扣子侧就是一轮对话、结果落在附件而非正文。插件因此声明 `executor_input/output_formats` 含 `openai-image`，把标准 `POST /v1/images/generations` 的请求翻成一条生图提示词打进 claw，再把上游签名直链按 `response_format` 包装成 `{created, data[]}`：
  - **模型类型必须是 `openai-image`** — 宿主图片端点白名单只认该字面量，填 `image` 会被直接 400 拒绝；模型目录里的 `image_generation` 条目在输出时统一映射为该类型。
  - **`n` / `size` 走提示词** — claw 只接受自然语言，`size` 追加「画面尺寸：」段、`n>1` 追加「请生成 N 张不同的图片」，其余字段（`quality` / `style` 等）通道无法表达，不解析也不透传。
  - **晚到附件必须等** — 生图收尾帧 6~9 秒就到且正文只有占位符，真实附件要十几秒到数分钟才落库，因此生图路径独立按 1 秒间隔轮询至 **4 分钟**上限（生产实测成功端到端 24s~3m6s）；取不到图返回 **400** 请求级错误，避免宿主当账号故障换号、把同一笔额度反复烧。
  - **`b64_json` 真实下载** — 扣子只给签名直链，不给 base64；`b64_json` 模式下服务端真实下载后编码，且目标域名限定 `coze.site` 后缀白名单以防 SSRF。
- **积分路由红线** — 扣子不声明 scheduler 能力（宿主只取第一个 scheduler 插件，抢槽会顶掉现有调度内核），改走**执行入口闸门**：后台每 5 分钟刷新积分快照，低于 `min_routable_credits`（默认 100）的账号在执行前直接 403、不打上游；整池确定 0 分时快速失败返回 400，同时止住宿主换号与上游重试。
- **每日登录积分自动领取** — 唯一触发器是 `GET /api/marketplace/trade/credit/balance/check`，每日 1500 分且当天不领即归零；插件按积分刷新周期「先领取再查询」，面板显示今日领取三态并支持手动补领。判据是明细中当天仍有余额的「每日登录」条目，不能凭响应里的 `pass` 判定。
- **失败分类** — 扣子 claw 业务失败统一返回 HTTP 200 + `code`，必须按 `code` 判语义：`1000521`（积分耗尽）映射 **403** 走账号级冷却与换号，其余 4xx 视为请求级故障直接回给调用方、不赔其他账号配额，未分类失败兜底 **502**。
- **控制面板** — `/v0/resource/plugins/coze-provider/panel`：授权登录、账号启停与删除、续期、积分与今日登录积分；「测试」按三层递进探活（会话有效性 → 模型目录权限 → 选中 chat 模型时真跑一次推理），图片/视频模型只做前两层以免空耗配额。

---

### workbuddy-token-usage — 用量统计 Dashboard

记录、统计并可视化跨插件各账户的真实 Token 消耗与费用：

```text
各 Provider 插件（workbuddy / workbuddy-ai / traework / qoderwork / zcode 等）  workbuddy-token-usage 插件
┌───────────────────────────────────────────────┐   NDJSON   ┌─────────────────────────────────────┐
│ 每次推理请求完成                               │ append ▶  │ 轮询读取（增量同步与断点续传）        │
│ publishUsage / usage_feed 汇聚点 ─────────────┼──────────▶│ 导入自身专用数据库                  │
│ 异步追加单行记录至共享 feed 文件                 │  O_APPEND │ Web Dashboard（"Token 用量"）看板   │
└───────────────────────────────────────────────┘           └─────────────────────────────────────┘
```

- **无锁共享 Feed 通道** — 基于 `<CLIProxyAPI root>/data/token-usage-feed.ndjson` 文件追加写入，彻底规避 bbolt 跨进程排它锁冲突；生产端超过 128MB 自动安全轮转截断，消费端按偏移量识别截断并续读。
- **请求明细表** — 完整展示请求时间、使用模型、调用来源、会话 Key、首字延迟（TTFT）、Tokens 明细、费用及**调用服务商（Provider）**，支持多语言导出 CSV。
- **维度汇总表** — 按模型、服务商、账号等维度汇总结算请求量与 token 占比。
- **轻量化架构（v0.2.6+）** — 移除 4 个冗余前端图表、Canvas 导出与趋势聚合计算接口，内嵌体积大幅削减（CSS −20%，JS 代码 −21%），移除高频定时轮询，默认展示最近 1 小时明细并支持手动即时刷新，极大节省宿主算力与网络负载。
- **面板** — `/v0/resource/plugins/token-usage-tracker/usage`，菜单名「Token 用量」。

---

## 核心架构与工程规范

以下规范适用于上述七个插件；个别条目的适用插件已就地标注。

### 1. 统一调度与账号健康隔离

1. **硬排除不可用账号**：带 `test_failed` 标签、处于故障禁用（盘上 `disabled: true`）、或剩余积分低于 `low_credit_disable_below` 硬禁用线的账号，调度器无条件硬排除，绝不分配流量；低于保活线 `min_routable_credits` 的账号打「保活」标，正常档存在时整体让位。
2. **存活账号按积分排序**：workbuddy / workbuddy-ai / traework / qoderwork 按剩余积分降序取号（未测 -1 排最后），并在同一健康阶梯内平摊负载；zcode 采用轮询取号；coze 不声明 scheduler，改由执行入口的积分闸门按 `min_routable_credits`（默认 100）拦截低额账号。
3. **全灭 Defer 跨 Provider 容灾**：当该 Provider 下所有账号均被排除时，执行全灭 defer（`Handled: false`），将请求交还 CPA 宿主调度其他 Provider，严禁死锁在故障 Provider 上。
4. **会话粘性绑定**：开启 `session` 模式时，同一会话的后续请求绑定到同一账号；换号重试前主动解绑失效会话。

### 2. 账号故障处置与探活自愈

- **失败双写（workbuddy / traework / qoderwork）**：一次上游失败同时写内存阶梯冷却（60s 起、线性递增、上限 10 分钟）与盘上 `disabled: true`；冷却只负责写盘生效前的毫秒窗口与写盘失败兜底。
- **纯内存冷却（workbuddy-ai / zcode）**：不做故障写盘，只在内存里阶梯冷却（同样 60s 起、每档 +60s、上限 10 分钟），冷却结束前 20 秒开始每秒回放探活。
- **只探失败模型**：每个账号的每个模型额度彼此独立，在别的模型上探通不能证明失败模型已恢复。workbuddy / traework / qoderwork 后台每 10 分钟串行探活，只回放当初失败的那一个模型；没有记录失败模型则跳过，保持禁用比盲目放回安全。
- **刷新路径保留禁用态**：宿主挑刷新候选不看 `Disabled`，插件若在回传里固定 `disabled: false`，禁用中的号会被一次后台刷新静默解封、完全绕开探活，因此 token 刷新与保活路径必须原样带回 `disabled` 与本机制标记。

### 3. 流式终止与测试探活提速规范

- **SSE 扫描即刻退出**：流式扫描器遇到 `[DONE]` 或 `event: done` 时必须立即 `break` 退出并主动关闭连接，严禁死等 Keep-Alive 闲置连接导致客户端无限转圈。
- **`liveBody` 实时直推**：流式转发必须直连 `resp.Body` 实时推流，严禁全量预缓冲。
- **探活流式首包即停**：面板「测试」按钮与后台活跃探活请求采用 `stream: true, max_tokens: 1`，收到首个有效数据帧即判定成功并异步关闭流，消除思考模型生成整段思维链带来的数秒等待。

### 4. 生产配置与热重载红线

- **插件安装/更新热重载**：生产环境下插件的新增与更新一律调用管理 API `POST /v0/management/plugin-store/<id>/install?version=<ver>`，CPA 自动写入配置并热重载生效（`restart_required: false`）。**非必要绝不重启生产 CPA 容器**，避免引发数十秒冷启动与流量中断。
- **数据库配置表只读不写**：生产 CPA 的配置表 `cli_proxy_api.config_store`（单条 `id='config'`）由管理系统自动维护为多行 YAML；严禁手工读取后进行 JSON 二次序列化写回（二次转义会导致真实换行塌缩为单行，导致 YAML 解析为空配置、端口漂移与 502 故障）。

---

## 仓库结构

```text
cpa-workbuddy-plugin/
├── workbuddy/                  # 腾讯 CodeBuddy Provider（国内版与 Global 版）
├── workbuddy-ai/               # WorkBuddy AI 国际版 Provider（补注册 / 试用 / 独立端点）
├── traework/                   # TRAE SOLO Provider（浏览器授权 / 签到防风控 / 流式加固）
├── qoderwork/                  # QoderWork CN Provider（COSY 签名 / 双登录 / 消除假成功）
├── zcode/                      # ZCode Provider（Anthropic 兼容 / 客户端指纹 Mimic / 额度领取）
├── coze/                       # 扣子 Coze Provider（claw WS 流式 / 生图映射 / 积分红线）
├── token-usage-tracker/        # 统一 Token 用量看板（共享 NDJSON / 明细表 / 维度分析）
├── registry.json               # 插件商店自定义源元数据（schema v2，direct install，包含 sha256 与 size）
├── release-assets/             # 本地资产缓存（.gitignore 忽略；二进制托管在 GitHub Release）
├── assets/                     # 共享资源（官方高清图标 icons/ 等）
├── scripts/                    # 运维与发布脚本
│   ├── cgo-shim-build.py       # 本地跨平台 cgo 验证 shim（Windows 无 C 工具链专用验证工具）
│   ├── download-release-assets.py # 从 GitHub Release 下载资产到本地缓存并校验
│   ├── publish-assets.py       # 扫描 release-assets 并回填 registry.json（URL 指向 Release）
│   └── validate-registry.py    # registry.json 格式与 SHA-256 完整性自检
├── doc/                        # 研发文档库
│   ├── 2-需求/                 # 历史需求与功能迭代文档
│   ├── 3-实施/                 # 实施总览与实施计划文档
│   ├── 4-bugs/                 # 生产故障与缺陷根因诊断文档
│   ├── 5-tests/                # 自动化测试与验证记录
│   └── 6-review/               # 代码风格与回归审查文档
└── .github/workflows/          # GitHub Actions 自动化 CI/CD 构建与 Release 流水线
```

---

## 多平台 Release 规范

每个插件发布时均通过 GitHub Actions 构建包含 6 大目标平台的动态链接库发布包，并附带权威的 `checksums.txt`：

```text
<id>_<version>_linux_amd64.zip      # 包含 <id>.so
<id>_<version>_linux_arm64.zip      # 包含 <id>.so
<id>_<version>_darwin_amd64.zip     # 包含 <id>.dylib
<id>_<version>_darwin_arm64.zip     # 包含 <id>.dylib
<id>_<version>_windows_amd64.zip    # 包含 <id>.dll
<id>_<version>_windows_arm64.zip    # 包含 <id>.dll
checksums.txt                       # 6 平台产物的 SHA-256 校验清单
```

**CI/CD 构建与发布触发模式：**

| 触发条件 | 执行动作 |
|---|---|
| `push` / `pull_request` | 触发全量矩阵编译、单元测试（`go test`）、静态检查（`go vet`）与 `validate-registry.py` 校验（仅验证不发 Release）。 |
| `workflow_dispatch` | 手动在 Actions 界面输入 `plugin=<id>` 与 `version=<ver>`，编译 6 平台构建包并自动创建该插件的独立 GitHub Release。 |

---

## 安装与配置

### 方式一：CPA 插件商店一键安装（推荐）

在 CPA Manager Plus 或 CPA 管理后台的「插件商店」中添加自定义源：

```text
https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/registry.json
```

随后在商店列表中搜索并一键安装所需插件（本仓库当前共 11 个插件，完整清单见 [registry.json](registry.json)）。

> 提示：服务器生产环境可通过管理 API 热重载安装指定版本：
> ```bash
> curl -X POST "http://localhost:8317/v0/management/plugin-store/<plugin-id>/install?version=<version>" \
>      -H "Authorization: Bearer <your-secret-key>"
> ```

### 方式二：手动部署

从 [Releases](https://github.com/luode0320/cpa-workbuddy-plugin/releases) 下载对应平台的 ZIP 压缩包并解压，将编译产物拷贝到 CPA 宿主的 `plugins/` 目录：

```bash
# 以 traework-provider 为例（Linux amd64）
unzip traework-provider_0.3.0_linux_amd64.zip
cp traework-provider.so /path/to/cliproxyapi/plugins/
```

### `config.yaml` 配置示例

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    workbuddy-provider:
      enabled: true
      retry_on_4xx: 3
    workbuddy-ai-provider:
      enabled: true
      retry_on_4xx: 10
    traework-provider:
      enabled: true
      scheduler_mode: "session"
      retry_on_4xx: 3
    qoderwork-provider:
      enabled: true
      retry_on_4xx: 3
    zcode-provider:
      enabled: true
      base_url: "https://api.z.ai/api/anthropic"
      mimic:
        enabled: true
    coze-provider:
      enabled: true
      # 积分路由红线：低于该值的账号不再承载新请求，显式配 0 关闭闸门
      min_routable_credits: 100
      credit_refresh_interval_seconds: 300
    workbuddy-token-usage:
      enabled: true
```

---

## 本地开发与测试

由于各插件依赖 CGO 导出 C ABI 供 CPA 动态加载，在 Windows 或缺乏 C 工具链的环境中直接执行 `go test` 会由于忽略 `import "C"` 文件而报错。

仓库内置了跨平台测试 Shim 工具：

```bash
# 验证指定插件的编译、vet 与单元测试（如 traework）
python scripts/cgo-shim-build.py traework

# 验证其他插件
python scripts/cgo-shim-build.py workbuddy
python scripts/cgo-shim-build.py workbuddy-ai
python scripts/cgo-shim-build.py qoderwork
python scripts/cgo-shim-build.py zcode
python scripts/cgo-shim-build.py coze
python scripts/cgo-shim-build.py token-usage-tracker

# 校验 registry.json 格式与哈希合法性
python scripts/validate-registry.py registry.json
```

---

## 核心文档导航

| 文档路径 | 说明 |
|---|---|
| [KNOWLEDGE.md](KNOWLEDGE.md) | QoderWork CN 完整逆向知识库（凭证体系、COSY 签名、QoderEncoding 算法） |
| [doc/3-实施/](doc/3-实施/) | 各插件核心功能与历史重构实施总览报告 |
| [doc/4-bugs/](doc/4-bugs/) | 生产环境疑难故障诊断（同名模型冲突治理、配置存储转义损坏恢复等） |
| [doc/5-tests/](doc/5-tests/) | 测试设计、验证脚本与自动化执行报告 |
| [doc/6-review/](doc/6-review/) | 提交前 6-review 风格与结构审查记录 |
