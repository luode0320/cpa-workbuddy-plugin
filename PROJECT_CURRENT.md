# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 插件集合 `cpa-workbuddy-plugin`——将腾讯 CodeBuddy（WorkBuddy）与 QoderWork CN 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式推理、每日签到、积分生命周期与 token 用量统计。
- 范围：四插件（workbuddy-provider / qoderwork-provider / traework-provider / workbuddy-token-usage）的迭代、测试、发布、生产部署与 registry 同步；QoderWork / Trae SOLO 逆向知识维护。
- 非范围：CLIProxyAPI 网关本体；CPA 内置调度器逻辑的修改（插件只做 host 契约适配）。

## 项目概览

- 状态：活跃维护中。生产现役与 registry 对齐（2026-09-27 22:03 发布）：traework-provider **0.1.67** / workbuddy-provider **0.14.42** / qoderwork-provider **0.9.20**（2026-09-14 跨平台 failover 双根因修复已发布部署并 hot reloaded）/ workbuddy-token-usage **0.2.2**。历史发布细节见「已完成」区。
- 活动会话数：2（本会话 + 并行会话共享工作树 F:\cpa-plugin）
- 更新时间：2026-09-27 (GMT+8)

## 活动会话任务摘要

- 当前会话（2026-09-27 晚）：**workbuddy 0.14.42 面板创建时间改用账号真实创建时间（JWT auth_time）已发布部署**。
  - 用户反馈：面板「创建」时间与「快照」时间仅差 1 分钟，明显不是真实创建时间。
  - 根因（双证据锁定）：面板此前取宿主 `HostAuthFileEntry.CreatedAt`，而宿主 watcher 每次扫描都用 `time.Now()` 重新赋值（`internal/watcher/synthesizer/file.go:114`），因此该值会随积分刷新 / 保号 keepalive / 活跃测试写入漂移到「最近写入时刻」；生产 48 个账号物理 auth JSON 顶层无任何 created_at 字段，文件 mtime/ctime 亦为最近写入。
  - 修复：新增 `workbuddy/created_at.go` 解析 accessToken JWT payload 的 `auth_time`（登录时写定、token 刷新不变，生产 48/48 账号全有值），`panel.go` 优先采用，仅缺字段时回退宿主值；新增 `created_at_test.go` 8 用例（含 base64 padded 段、缺字段、零值/负值、payload 非 JSON、两段式、空串）。
  - 验证：cgo-shim build/vet/test 全绿 + 哨兵（篡改 +1s → 2 用例 FAIL 证明真实进编译）；发布链 cb0a2be(fix)→f44eb08(assets 8 文件, 7/7 sha256 OK)→6736c90(registry)；CI run 36327182344 success；远端 raw ALL PASS（7 artifacts sha256 一致 + 旧版零残留）；生产 install 落盘 sha256 `cc519d97...` 与本地一致 + hot reloaded active=0.14.42 retired=0.14.41 + accounts/panel 双 200；行为验收：`242e1dde` 创建时间由错误的「09-27 22:12」变为真实「08-20 23:11」，51 账号出现 50 个不同时间点；生产流式请求 200 + 104 帧 + 完整正文 + finish_reason 收尾（429 换号后仍成功，failover 正常）。

- 当前会话（2026-09-26）：**WorkBuddy 与 TraeWork 刷新账号自动活跃与面板手动测试按钮功能已实现**。
  - 扩展：在 WorkBuddy 与 TraeWork 面板卡片底部新增「测试」按钮；后端新增 POST /test-active 接口，手动点击绕过 30 分钟防抖立即发起一次真实推理，成功后显示所选模型与响应耗时并更新活跃时间戳，失败展示错误原因。
  - 核心设计：在 `refresh_runner.go` 的 `doFetchOne` 成功刷新积分后触发轻量活跃请求（`user: "hi"`，`max_tokens: 5`），随机挑选账号可用动态模型（workbuddy 兜底 deepseek-v4.1-flash，traework 兜底 claude-3-5-sonnet）。
  - 故障隔离与节流：内置 30 分钟防频繁活跃节流保护（`defaultActivePingInterval = 30 * time.Minute`）；活跃探测失败仅记 warning 日志，绝不阻断或污染 `doFetchOne` 的核心返回值与账号故障降级状态。
  - 验证：通过 `cgo-shim-build.py` 完成 build、vet 以及包含哨兵用例在内的测试，`workbuddy` 与 `traework` 两个插件全绿通过。

- 当前会话（2026-09-14 凌晨，**跨平台 failover 双根因修复，traework 0.1.60 / workbuddy 0.14.31 / qoderwork 0.9.17 已发布部署**）：用户报 trae 全部账号失败后不切 workbuddy 直接失败。生产取证（stream 1253/1298/1326）坐实**两个互补根因**：①并行会话发现并已修复的调度层短路——插件 Scheduler 以 `Handled:true` 返回死账号，阻断宿主跨 provider 兜底（全部耗尽时改 `Handled:false` 延迟给宿主，scheduler/session_auth/active_auth 三处）；②本会话独立发现并修复的错误通道伪装——`streamEmitError`/`emitTraeAsyncError` 把终态错误当 payload 数据帧（`{"error":...}` SSE 事件）发出，宿主 conductor 视为正常流内容、请求以 200 "成功"告终，不轮换凭据/不冷却/不跨平台切换；改走 `host.stream.emit` 信封 `error` 字段（→ `chunk.Err`），traework 另加 `EmitError` 注入点。验证：cgo-shim 三插件全绿（含信封字段断言 + payload 泄漏哨兵 + 既有 5 个异步流测试迁移到错误通道断言）；发布链 40834b0(fix 27 文件)→d44767d(assets 24 文件，21/21 sha256 OK)→bb494d4(registry)；CI 三 run 34768147600/34768143943/34768140125 全 success；远端 raw ALL PASS + 旧版零残留；生产 install 三插件落盘 sha256 与本地一致（db9712be/5fdbf1c8/846b16ee）+ hot reloaded active=0.1.60/0.14.31/0.9.17 + 特征串 emitStreamErrorEnvelope 各 1 次 + accounts/panel 双 200；行为回归 deepseek stream 1741 attempt=1 完整 done、glm 经 workbuddy 正常。**观察项**：trae 全灭→workbuddy 接管的完整链路需真实全灭场景验证（下一次两 trae 账号同时失败时看 conductor 是否切 workbuddy）。qoderwork 的调度器层对齐（Scheduler capability 短路同病）未做，生产无 qoderwork 凭据、低优。沉淀知识库《插件终态错误伪装成成功流会让宿主跨平台failover永不触发》。

- 历史会话（2026-09-08 晚，**traework 0.1.56 面板 scopeLabel hotfix**）：用户报 TraeWork 面板"加载账号失败，已保留上次数据 / scopeLabel is not defined"。根因：0.1.55 清理 panel.html 异常筛选时 `renderSummary` 内 `const scopeLabel=` 三元链被误删前缀，残留语法合法但语义残缺的孤立表达式（node --check 只查语法抓不到，生产运行时抛 ReferenceError 使 renderSummary 中断、面板整体失败）。修复：程序化重建完整定义（6 筛选分支 + fallback，括号由代码生成并断言平衡）；验证链升级——scopeLabel 运行时 7 分支断言 + **vm + DOM stub 真实执行三插件面板 JS 顶层**（新验证手段，抓未定义标识符）全过。workbuddy/qoderwork 零改动不 bump。发布链见「已完成」0.1.56 条目。教训已沉淀知识库笔记《HTML内嵌JS多级三元删层的括号程序化编辑法》。

- 当前会话（2026-09-08，**三插件移除异常池 + MANUAL-TOGGLE-ONLY 固化，已发布部署**）：用户需求——①去掉"连续 3 次创建失败进异常池"逻辑，统一失败进冷却（固定 15s）；②审计停用路径，强制只有用户手动停用才能停用账号。决策（AskUserQuestion 确认）：三插件同步移除 / 启动时批量清理存量 anomaly:true / 保留连败计数与徽标（仅断开冻结联动）/ 审计确认 + 注释固化。六任务全完成：TASK-01/02 traework 后端+面板；TASK-03 workbuddy 后端+面板（含 usage_config.go anomaly 配置解析块、main.go ConfigFields、scheduler/active_auth/failover_retry/session_auth/management 全链清理）；TASK-04 qoderwork 同构移除（含 counter.go 落盘挂点注释漂移修正 → preserveWatchdogLoop:284、preserve.go 迁入 authFileErr 错误 helper）；每插件新增 anomaly_purge.go + 四态测试（watchdog 启动剥离遗留 anomaly:true 死字段）；TASK-05 停用复扫（traework 唯一写入口 persistDisabledToggle / workbuddy disableAuth auto 路径只透传 / qoderwork 无停用通道）+ 三插件入口固化 MANUAL-TOGGLE-ONLY POLICY 注释；TASK-06 版本 bump 0.1.55 / 0.14.26 / 0.9.14 + 三份 CHANGELOG 条目。验证：cgo-shim 三插件 build/vet/test 全绿 + panel.html node --check 全绿 + grep 零残留。**发布链完成（用户授权"完成后发布"）**：dcdeb95(feat 70 文件 +722/-2210)→fef8df4(assets 24 文件)→48941a2(registry)；CI 三 run 34151835600/34151839572/34151843275 全 success（runner 排队跨两轮轮询窗口，踩坑 45 模式）；远端 raw ALL PASS（21 artifacts sha256 一致 + 旧版零残留）；生产 install 三插件落盘 sha256 与本地一致（661376b6/a2460b39/209ff927）+ hot reloaded active=0.1.55/0.9.14/0.14.26 + accounts/panel 双 200 + accounts 响应零 anomaly/unfreeze 键（行为验证）。踩坑：panel.html scopeLabel 多级三元删层后括号层级必错（traework/workbuddy/qoderwork 三连），手改不可靠 → 用 Python 程序化删除三元层并断言 `count('(')==count(')')` 后写回。

- 历史会话（2026-09-04，浏览器授权登录 **0.1.38 → 0.1.39 → 0.1.40 全链发布部署完成**）：0.1.38 免 IDE OAuth 导入 → 0.1.39 三缺陷修复（&amp; 转义 / callback 免鉴权 resource 前缀 / OAuth state）→ 0.1.40 白名单适配定案。

- 历史会话（2026-09-03，usage feed 补齐「会话/首字延迟」列 **0.1.33**）：已完成发布部署 + 生产验收 PASS，细节见「已完成」区 0.1.33 条目与 CHANGELOG。

- 历史会话（2026-09-03，工具调用链路 P1+P0 修复 **0.1.32**）：已完成发布部署，细节见「已完成」区 0.1.32 条目与 CHANGELOG；行为级验收（stream#3206/3208「回答不完整」不再复现）待用户真实工具链流量观察。

- 历史会话（2026-09-02，**0.1.30+0.1.31**）：伪完成同号退避/401 核算/双轴健康度 reasoning 流式放行，两版均已发布部署 + 生产验收，细节见「已完成」区与 CHANGELOG。

- 当前会话（2026-09-02，Trae 异步流式宿主流桥打开超时降级直连，**0.1.28 已发布部署 + 生产流式长推理验收 PASS**）：见下方「已完成」0.1.28 条目；该版本只覆盖宿主流桥 open 阶段挂死，read 阶段挂死由本会话 0.1.29 修复承接。

- 历史会话（2026-09-01，**0.1.27** 伪完成同请求换号恢复）：六任务完成、已发布部署 + 生产真实流量验收（1607/1609 换号闭环），细节见「已完成」区。

- 历史会话（2026-09-01，token-usage-tracker **0.2.2**）：feed 新增 usage 经 SSE 通知 dashboard（/usage/events seq + 15s 轮询 fallback），已发布部署，细节见 CHANGELOG 与「下一执行点」0.2.2 行。

- 历史会话（2026-08-30~31，traework **0.1.16/0.1.17/0.1.21/0.1.22**）：面板对齐五件套、异步流改宿主流桥实时读取、断流兜底收尾等已完成，细节见「已完成」区与 CHANGELOG。

- 并行会话（0.1.11→0.1.15 发布链已完成）：WAF UA 加固（0.1.11）/ content parts 数组 4001 修复（0.1.12）/ 对话签到 host 分离（0.1.13）/ 动态模型发现（0.1.14）/ usage_feed 适配 token-usage-tracker（0.1.15）+ workbuddy 0.14.18 定时刷新稳定性修复
- 关键新铁律：dispatch 必须传 plugin(provider id)+version；download/publish 脚本参数顺序互反；store install 的 CDN 边缘滞后误报（等几分钟重试）；生产部署唯一路径 plugin-store install；并行会话 checkout/reset 会覆盖未提交改动（发版前先 fetch 对齐或及时提交）

## 已完成

- 2026-09-08 **traework 0.1.56 面板 scopeLabel hotfix（本会话）**：用户报面板"加载账号失败 / scopeLabel is not defined"。根因：0.1.55 清理 panel.html 时 `renderSummary` 的 `const scopeLabel=` 三元链被误删前缀（残留语法合法的孤立表达式，node --check 抓不到，运行时 ReferenceError 中断 renderSummary）。修复：程序化重建完整定义（all/available/preserve/exhausted/failed/disabled 6 分支 + fallback，括号由代码生成断言平衡）。验证升级：scopeLabel 运行时 7 分支断言 + vm+DOM stub 真实执行三插件面板 JS 顶层全过（新验证手段）；cgo-shim traework 全绿；dcdeb95 diff 逐行复盘确认无其他残行。workbuddy/qoderwork 零改动不 bump。发布链 f8ac3af(fix 6 文件 +18/-6)→646a407(assets 8 文件)→d0ac6eb(registry)；CI run 34233803018 success（8m9s）；远端 raw ALL PASS（7 artifacts sha256 一致 + 旧版零残留）；生产 install status=installed restart_required=false + 落盘 sha256 与本地一致（1e3b4af7）+ hot reloaded active=0.1.56 retired=0.1.55 + accounts/panel 双 200 + 生产面板 HTML grep `const scopeLabel` 命中 1 次（定义真实存在）。
- 2026-09-08 **三插件移除异常池发布部署（traework 0.1.55 / workbuddy 0.14.26 / qoderwork 0.9.14，已发布）**：删除三插件 anomaly.go/anomaly_config.go（连败冻结、异常集合、每日复活、/unfreeze 路由、anomaly_pool_threshold/anomaly_refresh_enabled 配置与解析）；accountFailover 冻结判定删除、失败只进 15s 冷却；scheduler/active_auth/failover_retry/session_auth anomaly 过滤层与谓词全清；panel.html 异常徽标/解冻按钮/异常筛选/计数与汇总口径全清；新增 anomaly_purge.go（watchdog 启动剥离遗留 anomaly:true，幂等不盲写）+ 四态测试；qoderwork counter.go 挂点注释漂移修正 + preserve.go 迁入 authFileErr helper。停用复扫：traework 唯一写入口 persistDisabledToggle（仅手动 toggle）/ workbuddy auto 路径只透传 disabled / qoderwork 无停用通道；三插件入口固化 MANUAL-TOGGLE-ONLY POLICY 注释。发布链 dcdeb95→fef8df4→48941a2；CI 三 run success；远端 raw ALL PASS（21 artifacts sha256 一致）；生产 install 落盘 sha256 一致 + hot reloaded active=0.1.55/0.9.14/0.14.26 + accounts/panel 双 200 + 行为验证零 anomaly 键。踩坑 50：发布前 VERSION 文件漏 bump（main.go 已改）→ push 前 `cat */VERSION` 与 main.go 两两核对。
- 2026-09-08 **三插件每 4 小时调度批量发布部署（traework 0.1.54 / workbuddy 0.14.25 / qoderwork 0.9.13，历史会话）**：自动签到从每日两班（09:00/21:00）改每 4 小时六班（00/04/08/12/16/20 本地时间），token 保活从每日 22:00 单次改与签到同节奏，缩小 Keycloak 离线会话失效窗口；注释/ConfigFields 描述/panel schedule/测试用例（workbuddy nextCheckinTime 三用例、traework keepalive 窗口用例）同步。动机背景：analysis/traework-0.1.50-keepalive-2200-acceptance-20260906.md 记录 22:00 每日保活的生产行为不一致回归。顺带修复 workbuddy/qoderwork checkin.go 注释 tab 缩进错乱；随批提交 SKILL.md 踩坑 47/48。验证：cgo-shim 三插件全绿。发布链 d43caea→9a424f5→a30a7e6；CI 三 run success（并行排队 25+ 分钟跨两个轮询窗口，踩坑 45 再次验证）；远端 raw ALL PASS；生产 install 三插件落盘 sha256 一致（c1c6a931/c70b4d4b/07ca3f70）+ hot reloaded 全部生效（qoderwork 本轮无踩坑 48 现象）+ accounts/panel 双 200 + 生产 dashboard schedule 已是新班表。
- 2026-09-05 traework **0.1.42 已发布部署**（浏览器授权登录适配 TRAE 授权页真实回调形状，本会话）：用户真机实测 0.1.40 实锤根因——TRAE 授权页登录成功后的跳转**不是标准 OAuth 302**，不回传 code/state，授权码在 `authCodeInfo={"AuthCode",...,"ExpireDuration":600000}` JSON 参数里（参数集 isRedirect/scope/authCodeInfo/loginTraceID/host/userRegion/userInfo）。0.1.40 submit 只按 `?code=&state=` 解析必报「缺少 state」。修复：`extractAuthCode`（?code= 优先、authCodeInfo JSON 回落）+ 会话定位链（start 响应新增 state 字段 → 面板 browserLoginState 带回 body.state → URL state → 最新 pending 会话兜底 newestPendingSession）+ callback/submit 双通道同构。验证：cgo-shim 全绿（1.310s）+ 哨兵（破坏 authCodeInfo 解析 → 两个新测试 FAIL → 还原全绿）+ panel JS OK + gofmt 干净。发布链 98b81e0(fix 5 文件 +313/-43)→a60038d(assets)→e0b1850(registry)；CI run 33897217089 success（12m10s）；远端 raw ALL PASS（7 平台 sha256 一致，0.1.37~0.1.41 零残留）；生产 install 落盘 sha256 `c5150057` 与本地一致 + loaded/registered 0.1.42 热重载 + restart_required=false；生产冒烟：authCodeInfo 形态 + 假 state → 「授权会话不存在或已过期」、无 state 无会话 → 「无法定位授权会话」（均为 0.1.42 新语义，证明解析与定位链生效）。闭环标志：用户重测真实登录导入。
- 2026-09-04 traework **0.1.39 已发布部署**（浏览器授权登录三缺陷修复，本会话）：生产端到端验证 0.1.38 暴露三缺陷——BUG-A 宿主 `ServeManagementHTTP` 对 management JSON 响应强制 `htmlsanitize`（`&`→`&amp;`，授权页参数解析必挂）→ 面板 `browserLogin()` 打开前 `replaceAll("&amp;","&")` 兜底（resource 路由响应不转义）；BUG-B callback 注册在 management 前缀被宿主 management key 中间件拦截（`pluginManagementNoRoute` 对含 GET 全路由强制鉴权）→ 移入 `Resources` 挂免鉴权 `/v0/resource/plugins/<id>/browser-login/callback`（无 Menu 不进管理 UI 菜单）+ resPrefix 分支子路径分发；BUG-C 授权 URL 缺 OAuth `state` 参数（授权服务器无法回传、callback 查会话必落空）→ `q.Set("state", state)`。新增 2 契约测试（start 指向 resource 回调+state+S256；注册表 callback 仅在 Resources 且无 Menu）。发布链 bf6ba87(fix 7 文件)→CI dispatch(traework-provider/0.1.39)。验证：cgo-shim 全绿。
- 2026-09-04 traework **0.1.38 已发布部署 + 生产部署验证 PASS（端到端复验移交 0.1.39）**（浏览器授权登录：免 IDE 完整 OAuth 导入，本会话）：逆向 TRAE SOLO CN 0.1.62 `main.js` 确认浏览器授权码 + PKCE(S256) 流程（授权 URL 参数形状、ExchangeToken v3、GetUserInfo v3、refresh 设备无关、ClientID 双值 Bb()）。`browserlogin.go` 三路由（start: PKCE 对+EC P-256 设备密钥+随机指纹+origin 校验；callback: 换 token→取用户→UserID 去重入库→回跳页三重跳转；result: 读后即焚不含凭据，pending 不消费）+ `panel.html` 工具栏按钮 + `?auth_cb=` 回跳轮询。修复自审缺陷 2 处（callback outcome 未存回 map / result 过早轮询误删 pending 会话）。发布链 b5f105c(feat 11 文件)→babd598(assets 7 平台)→ea57fee(registry)；CI run 33788701204 success（约 11 分钟）；远端 7 平台 sha256 ALL PASS；生产 install 落盘哈希一致 f98950e4… + hot reloaded active=0.1.38 retired=0.1.37 + restart_required=false + panel 200 含按钮 + start 无 key 401 + accounts 200 + 流式回归 stream_id=1294 attempt=1 chunks=12 健康。生产复验暴露三缺陷 → 由 0.1.39 修复。
- 2026-09-03 traework **0.1.33 已发布部署 + 生产验收 PASS**（usage feed 补齐会话/首字延迟列，本会话）：publishUsage 8→12 参数链对齐 workbuddy，session_key 执行器入口提取跨换号冻结，ttft 由 collectTraeStream 四值返回 / pumpTraeStreamAttempt.FirstOutputAt 观测，ttftNSBetween 统一计算；哨兵 FAIL 证明真实生效；cgo-shim 全绿。发布链 86d3829(feat 11 文件)→11c183e(assets 7 平台)→dbf2683(registry)；CI run 33765271408 success（9.7 分钟）；raw 远端 ALL PASS 无旧版残留；生产 install + 22:27:51 loaded/registered version=0.1.33 + 落盘 sha256 ae1e9ae7 与本地一致 + 特征串 ttftNSBetween/session_key 实锤 + accounts/panel 双 200。生产行为验收：轻量流式请求后 feed 新增记录 session_key 非空 + ttft_ns≈1.45s 非零，dashboard 两列生效。CHANGELOG 顺带补录 0.1.32 缺失条目。
- 2026-09-03 traework **0.1.32 已发布部署**（工具调用链路 P1+P0 修复，本会话）：P1 伪完成豁免工具短流（collectTraeStream 三值 + hasToolCalls 贯通 + pump gate 放行）；P0 完整工具链（tools 上行透传 + parameters stringify + function↔function_call 双向翻译 + 三路径 tool_calls delta + finish=tool_calls）。toolchain_test.go 8 + pseudo_toolcalls_test.go 4 用例取证 SSE 全绿；真实上游双阶段 e2e 闭环。发布链 16d8bc5→4dd3fd4→14527f9；CI run 33666121945 success（11m27s）；生产 hot reloaded active=0.1.32 retired=0.1.31（02:29:53 同秒）；落盘 sha256 4a4546d9 与本地 zip 一致；accounts 200/panel resource 200。行为级验收（stream#3206/3208 不再「回答不完整」）待用户真实工具链流量观察。
- 2026-09-02 traework **0.1.30 已发布部署 + 生产验收 PASS**（三缺陷修复，用户否决账号归因）：FIX-A 伪完成同号退避重试（sync+async 收敛，仅 `PickNextAuth` 无候选才同号退避 1 次，不耗跨账号 Budget）；FIX-B async 401 open error 补核算+驱逐绑定；FIX-C `isPseudoCompletion` content+reasoning 双计（任一达 600 健康 / reasoning-only 永不判伪）。新增 executor_same_auth_retry_test.go（负向哨兵+定向探针证明真实编译执行）；既有 5 伪完成回归+reasoning-only 豁免全绿；cgo-shim 全绿。发布链 d027734(fix)→3d74d2d(assets)→37d1b86(registry)；CI run 33645466299 success；生产 install 0.1.30 + active=0.1.30 retired=0.1.29 + 落盘 sha256 一致。生产验收（CYCLE-04）：形态 A 用户真实短请求 5 连发全绿（1.8-4.3s 完整）+ 形态 B 常规长推理 99.2s/9360 字符完整（stream 2876），无伪完成误判、无换号、无池耗尽。
- 2026-09-02 traework **0.1.31 已发布部署 + 生产复验 PASS**（FIX-D reasoning 阶段零字节 504）：CYCLE-04 形态 B2 深度思考请求（quickselect 推导）504——stream_id=2878 scheduled 后 5 分钟零字节、无 done/error/degrade，nginx `proxy_read_timeout 300s` 掐断。根因：`pumpTraeStreamAttempt` gate 只累计 content（`contentChars += len(text)`），reasoning-only 分片无限压 pending 不转发（桥健康、上游 reasoning 持续流入，但插件→客户端 300s 零字节）。FIX-D：gate 改 content+reasoning 双轴健康度（`healthChars += len(text)+len(reasoning)`），reasoning≥600 即流式放行；真伪完成（双轴短合计<600）仍全程 pending→判伪丢弃，零泄漏不回归。新增 `TestPumpTraeStreamAttemptReasoningFlushes` 四断言（哨兵先 FAIL 后删证明真实编译），cgo-shim 全绿 + 6-review STYLE PASS（doc/6-review/2026-09-02_235500）。发布链 4477560(fix)→6d63e1d(assets)→25facac(registry)；CI run 33648734166 success；远端 raw ALL PASS；生产 install 0.1.31 + active=0.1.31 retired=0.1.30 + 落盘 sha256 c053981a 一致。生产复验（CYCLE-04r）：stream 2999 同 prompt 完整返回 8702 正文+55062 reasoning 字符、1232 chunks、490.7s、**首包 26.4s 到达**（reasoning 流式下发不再零字节），attempt=1 无换号；形态 A 短请求回归 4/5 完整（1 次为本机客户端 SSL 瞬时断，重试即过）。

- 2026-09-02 traework **0.1.29 已发布并部署生产（read 阶段超时降级直连），但验收结论已纠偏——agent 自发超长请求验证，非用户真实流量**：用户报 0.1.28 "完全不行"，生产直连复现 qwen3.8-max「分析项目」：插件直接客户端 `hostHTTPDoStreamDirect` 完整流式（327/264 事件，1.6-2.7 分钟），宿主桥 read 阶段在生产阻塞（stream_id=1945 scheduled 后 2 分钟零日志 → gin 499）。根因：`hostCall(MethodHostHTTPStreamRead)` 同步 cgo 无超时，阻塞在 host 侧无缓冲 chunk channel；`sharedHTTPClient` 120s 整体超时还会截断长流。修复：host_bridge.go 加 `hostBridgeReadTimeout=90s`（goroutine+select 竞速）超时返回 `errHostBridgeReadTimeout`，经 `hostStreamDirectFn` seam 降级 `hostHTTPDoStreamDirect` live 实时流（覆盖 0.1.28 只做的 open 阶段）；新增 `streamHTTPClient()` 无整体超时（DialContext 10s/TLSHandshake 10s/ResponseHeader 30s）；`hostHTTPStream` 增 req/bodyBytes 字段保存降级重开所需。新增 host_bridge_read_timeout_test.go 三用例，哨兵先 FAIL 后删除证明进编译。cgo-shim 全绿 + 静态门禁 PASS + 6-review `STYLE: PASS`。发布链：7424cd7（fix）→ 99f6177（assets 8）→ 706b85d（registry）；CI success；远端 raw 7 资产 ALL PASS；生产 install 0.1.29 + active=0.1.29 retired=0.1.28。**纠偏（2026-09-02 本会话生产取证）**：当时「生产验收 3 次完整（2139/2146/2154，208.6s/292.5s/298.4s）」是 agent 自发的**超长请求**（要求 10 章节/3000 字），非用户真实流量；用户真实形态是很多 ~10s 短请求。生产全量日志 grep degrade/timed out **零命中**——0.1.29 从未降级，90s read 超时从未误杀健康流，此前「90s 假杀+降级重连」推断不成立。0.1.29 的 read 修复**尚未被用户真实请求验证**（见「活动会话任务摘要」本会话纠偏行）。
- 2026-09-02 traework **0.1.28 已发布并部署生产，生产流式长推理验收 PASS**（异步流式宿主流桥打开超时降级直连，本会话）：0.1.27 生产直连复现 qwen3.8-max「积分够却一直失败」——非流式 `/v1/responses` 一次成功（13.3s 聚合路径），带 `StreamID` 异步流式请求 240s 无字节后宿主 499（stream_id=1664 仅 scheduled 一条日志）。根因：`hostCall`（cgo 同步无超时）在宿主流桥 **open 阶段**永久阻塞协调器 goroutine。修复：host_bridge.go 加 `hostBridgeOpenTimeout=30s` 竞速打开，超时/失败降级 `hostHTTPDoStreamDirect` live 实时流（边读边发不缓冲完整 body）；抽出 `hostBridgeAvailableFn`/`hostStreamOpenFn` 注入点；新增 host_stream_timeout_test.go 两用例（哨兵先 FAIL 后删除证明进编译）。cgo-shim 全绿 + 6-review `STYLE: PASS`。发布链 02dc323→b7ae103→a05b252；CI run 33535588336 success；raw 远端 7 资产 ALL PASS；生产 install 0.1.28 + 落盘 sha256 8ec5343f 与本地一致 + hot reloaded active=0.1.28 retired=0.1.27。生产验收：4 次流式 qwen3.8-max 长推理（stream_id 1850/1853/1856/1857，覆盖两账号 + 同 session 粘性，**agent 自发请求，非用户真实流量**）全部 `attempt=1` 完整 done，无挂死/499/伪完成；修复前 1664 场景闭环（1664 是用户 00:29 `/v1/responses` 真实请求，4m0s 499）。
- 2026-09-01 traework **0.1.26 已发布部署，但完成结论已撤回**：伪完成阈值修正为输出<600 字节且输入≥200 字节，但检测发生在正文/stop/close 下发之后，当前请求仍提前结束，不能恢复；同请求恢复由 0.1.27 承接。
- 2026-09-01 traework **0.1.25 已发布部署，历史结论已由后续版本推翻**：补充伪完成记账/会话驱逐/active_id 优先，只影响下一请求且失败账号少量内容仍下发，不能恢复当前请求；由 0.1.26→0.1.27 承接。
- 2026-09-01 **trae-local-verify 项目级 skill 创建**（辅助资产，本会话）：`skills/project-cpa-workbuddy-plugin-trae-local-verify-rules` 吸收"本地直连 Trae 上游验证账号推理"经验——5 步流程（临时目录→cgo-shim→verify_main.go→运行判定→清理）+ 复用解密/header/payload/SSE（decryptCredentialString / buildTraePayload / scanSSE / classify）+ 5 条踩坑（sharedHTTPClient 120s 截断长流式→自定义 client+context 10min、先 reasoning 后正文、storage.json 账号≠生产账号、Windows 直连、SSE output 双格式）；references 含 verify-main-template.md 完整模板 + source-notes.md。quick_validate.py PASS、同域冗余扫描无交叉、skill-audit 边界清晰。同步沉淀知识库笔记《长流式客户端Timeout会掐断SSE直连》（新账号 uid 2257747741770235 qwen3.8-max 2m37s/595chunk/2.4万字完整 done vs 生产账号 77tokens 短输出 → 账号级问题定案）。
- 2026-08-31 traework **0.1.24 改码未提交**（流式请求补默认 max_tokens=20000，本会话）：0.1.23 日志证明 traework 收流链路健康，账号 qwen3.8-max 全历史请求平均 77 tokens / 最大 273 tokens / 全部正常 done，用户确认 Trae 原生客户端同账号长输出正常、额度充足 → 根因是流式请求缺 max_tokens（`buildTraePayload` 仅 maxTokens>0 才带，客户端不传则上游无 max_tokens，Trae 给极小默认上限导致 solo 长任务刚开口就 done）。修复：`traework/upstream.go` 新增常量 `streamDefaultMaxTokens = 20000`（与 config models 样例一致），`buildTraePayload` 流式路径（`stream == true && maxTokens <= 0`）补默认值 20000，显式传入保留原值，非流式路径保持原样；新增 `TestBuildTraePayloadStreamDefaultMaxTokens` 覆盖三形态（流式缺省补 20000 / 流式显式保留 / 非流式不补）。验证：cgo-shim build/vet/test 全绿 + 行为哨兵（临时移除补默认分支）FAIL `stream max_tokens = <nil>, want 20000` 证明测试真实执行且精确覆盖行为 + `git diff --check` PASS + gofmt 干净；6-review `STYLE: PASS`（doc/6-review/2026-08-31_163500_Trae流式默认max_tokens_6-review.md）。VERSION/main.go（0.1.24）/CHANGELOG 已 bump。**未提交未发布**（生产仍 0.1.23，等用户发布授权）。
- 2026-09-01 traework **0.1.23 已发布并部署生产**（流式三出口日志插桩，本会话）：为定位生产「生成中途停止、无下文」的流形态，给 `traework/stream.go` 三条流出口（`collectTraeStream` 同步收集 / `aggregateTraeCompletion` 非流式聚合 / `pumpTraeStream` 异步泵）的 error / invalid / done（含 output_eof 截断）出口补 `[traework] stream ...` 日志，新增 `terminationLabel` 稳定短标签（done / output_eof / invalid）；`traework/executor.go` `handleExecStream` 补账号维度日志（上游错误 / 收集成功 / 账号池耗尽 / 异步泵启动与失败）。全部只读插桩，不改变业务分支、终止判定、账号核算或 usage 发布。验证：cgo-shim build/vet/test 全绿 + 必失败哨兵 FAIL 证明新增代码真实进编译（哨兵阶段测试输出已现 pump 日志行）+ 删除哨兵重跑全绿 + `git diff --check` 为 0。提交链 6f18c8c（feat 7 文件）→ cdde489（assets 8 文件）→ 338dd80（registry）；CI run 33410867841 success（13 分钟，4 插件测试矩阵全绿 + 7 平台构建，期间 darwin/amd64 一度 queued 属 runner 排队非失败）；raw 远端验证 ALL PASS（7 平台 size+sha256 全 OK）；生产 plugin-store install 0.1.23 + 落盘 .so sha256 a0dddad9 与本地 zip 内一致（非 0.1.13 式新版本名旧二进制）+ hot reloaded active=0.1.23 retired=0.1.22 + accounts/panel 双 200 + 0.1.23 二进制含全部 6 个日志字符串 + 生产日志可见插件侧 `[anomaly]`/`[keepalive]` 前缀证明通道可用。发布链路 13 步全闭环，等用户用 `sess_3179110d` 触发中断后抓现场日志。
- 2026-08-31 traework **0.1.22 改码未提交**（上游长回答中途断流兜底收尾，本会话）：根因调查确认 0.1.21 的 `validate` 收紧（`hasDone` 严格）把「部分 output 后 EOF 无 done」的上游断流从 0.1.20 的静默补 stop 改成报 truncated 错误中断，IDE 表现为"生成中途停止、无下文"。宿主源码取证：`hostHTTPStreamBridge.read` 无 idle 超时、`DoStream` 无总超时、插件 cgo 桥接用 Background ctx 客户端断开不取消上游 → 断流是上游 Trae 长回答中途 EOF，非宿主掐流。修复：`stream.go` `validate`→`classify` 返回 `traeStreamTermination` 三态（Done/OutputEOF/Invalid），仅空响应报 invalid（保 0.1.20 防空成功回归），三条响应路径（聚合/同步流/异步流）对 OutputEOF 统一补 `finish_reason="length"` 正常收尾；`pumpTraeStream` 断流收尾不清零账号、不记成功用量、以"不完整"落一条用量。测试：`collectTraeStream` 断流补 length、空响应仍报错、`pumpTraeStream` 断流不清零账号故障（哨兵验证真实进编译）。cgo-shim build/vet/test 全绿，gofmt 干净。VERSION/main.go 已 bump 0.1.22，CHANGELOG 已加条目。**未提交未发布**。
- 2026-08-31 traework **0.1.22 补齐读错误型断流兜底**（本会话，改码未提交）：复查发现上一步三态兜底只在 `scanSSE` 返回 `nil` 时生效，而 `scanSSE` 仅对干净 `io.EOF` 返回 `nil`；真实断流（对端 RST / unexpected EOF / 宿主流桥 `Error` 非空，`host_bridge.go:352-353` 转硬错误）以读错误返回，`pumpTraeStream:309` 判致命失败并 `streamEmitError` 中断 IDE ⇒ 0.1.22 前三步对该形态无效。探针实测：部分 output + `connection reset by peer` → `chunks=0 err=connection reset`（未兜住）；对照干净 EOF → `chunks=2 err=nil`（已兜住）。修复：`upstream.go` `scanSSE` 增 `hasPayload func() bool` 参数，读错误时若已累积可交付业务内容则按截断正常收尾（交由 `classify` 补 `length`、保留已生成内容），零内容才致命；`stream.go` `traeSSETerminal` 增 `hasPayload()`，三条路径接入；既有 `host_bridge_decode_test.go` 三处 `scanSSE` 调用传 `nil` 保持原语义。回归新增 3 用例（读错误后补 length、零内容读错误仍致命、聚合路径补 length 且保留正文），哨兵验证真实进编译，cgo-shim build/vet/test 全绿，gofmt 干净，UTF-8 校验通过。**未提交未发布**（生产仍 0.1.21）。
- 2026-08-31 traework **0.1.21 已发布**（异步流改走宿主流桥实时读取 + 业务成功严格依赖 done 终止，本会话）：①`callLLMStream`（upstream.go）+ `hostHTTPDoStream`（host_bridge.go）透传 `host_callback_id`，异步聊天实时读取避免长回答全量缓冲，客户端取消可传递到上游流；②`stream.go` `validate` 收紧——业务成功必须收到明确 `done`，部分 `output` 后 EOF 返回截断错误不补成空 stop，最终 stop 下发失败走失败核算；③`scanSSE` EOF 前补齐无换行尾帧。cgo-shim build/vet/test 全绿（1.468s）。提交链 85262aa（fix 9 文件）→ 7ad1e4f（assets 8 文件）→ 4bd1f07（registry）；CI 首 run 33324654919 因无关插件 workbuddy-provider darwin/amd64 checkout 网络瞬时失败拖累 Release job 跳过（Release needs build-cross 无 if:always），重跑 run 33325134505 success；Release `traework-provider-v0.1.21` 8 assets；raw 远端验证 ALL PASS（7 平台 size+sha256 全 OK，无 0.1.20 残留）。
- 2026-08-30 traework **0.1.17 已发布并部署生产**（两 bug 修复，本会话）：①`checkin.go` runFleetCheckin 成功签到后不再用 `res.Points`（本次签到奖励，恰为 200）当 `TotalRemain` 写缓存——改为 `accountPoints` 真实查询，与单账号 handleManualCheckin 分支一致（根因：截图"全部签到后积分变 200"=签到奖励值覆盖 remain 缓存）；②`panel.html` 汇总卡标题"系统状态"→"用量汇总 · 全部账号"。commit 链 cc72bf5（fix）→ 6475a71（assets 7 zip）→ b857cbd（registry）；CI run 33301068289 success（10 分钟，含 queued 波动）；raw 远端 ALL PASS；生产 plugin-store install 0.1.17 一次成功（无 CDN 滞后），落盘 sha256 575cfe52 与本地 linux/amd64 完全一致（踩坑 29），hot reloaded active=0.1.17 retired=0.1.16，accounts/panel 双 200。收尾时一次 curl 遇上游瞬时 429（code 14018 额度已用尽），复测确认本插件本地数据接口不受影响。

## 待办

- 【已完成 ✅】traework **0.1.37 已发布部署 + 生产验收 PASS**（2026-09-04，并行会话 token_usage 接入，本会话代发）：解析上游 `event:token_usage` 真实用量，dashboard 输入/输出/思考/总 Token 列不再依赖估算（诊断见知识库《traework用量全靠估算不解析上游usage.md》）。改动：stream.go（traeUsageCollector + usageDetailFromTraeMap + collect/aggregate 返回值扩 5/3 值 + traeStreamAttemptResult.Usage + usageDetailForAttempt/ForCompletion 兜底 helper）；executor.go（非流式 handleExecExecute + 同步 runTraeSyncStream + 异步 runTraeAsyncStream 5 处全部改发真实 usage，失败路径空 Detail 不变）；测试 9 处调用适配 + 新增 token_usage_test.go 8 用例；cgo-shim build/vet/test 全绿（本会话代发前复核）。提交链 7f0068a feat → 98bcb66 assets（7 平台 ALL CHECKSUMS OK）→ 58f886b registry；CI run 33783473893 success（12m48s）；远端验证 ALL PASS（0.1.33~0.1.36 零残留）；生产 install：落盘 .so sha256 `1314d84f...016364` 与本地一致、内置版本 0.1.37、特征串 traeUsageCollector×1/usageDetailFromTraeMap×1、hot reloaded active_version=0.1.37 retired=0.1.36、accounts/panel 200。验证遗留：dashboard 四列真实值需发新 trae 请求观测（并行会话闭环项）。
- 【已完成 ✅】traework 面板删除方向纠正 **0.1.36 已发布部署 + 生产验收 PASS**（2026-09-04 凌晨）：0.1.35 误删「用量汇总」方向错误（红框实指「子系统状态」），本版纠正：**恢复用量汇总 5 卡 + 进度条，改删「子系统状态」4 卡**（保号池 watchdog/keepalive/lifecycle/异常池 + 上次保号刷新）。改动：恢复 `renderSummary()` 完整用量汇总渲染（含 `accountsForFilter()`/`creditOf()`、`.summary .pb` CSS）；删除 `renderSubsystem()` 与其调用、`.summary-item .d` 死 CSS；基线核对 0.1.34 diff 仅剩子系统状态删除。提交链 dd1d491 fix → ff57346 assets（7 平台 ALL CHECKSUMS OK）→ e951946 registry；CI run 33781254643 success（12m26s）；远端验证 ALL PASS（0.1.32~0.1.35 零残留）；生产 install：落盘 .so sha256 `366116c6...e73d1ca` 与本地一致、内置版本 0.1.36、二进制「用量汇总」3 次/「子系统状态」0 次/renderSubsystem 0 次、hot reloaded active_version=0.1.36 retired=0.1.35、accounts/panel 200、生产面板 HTML 实测（54936 字节）「用量汇总」3 次出现、「子系统状态」0 次。教训：截图红框指认须先确认区块归属再动手，0.1.35 误删已记入 CHANGELOG 警示。
- 【已完成 ✅】traework 面板移除「用量汇总」区块 **0.1.35（方向错误，已被 0.1.36 纠正并回退，2026-09-04 凌晨）**：误删顶部「用量汇总 · 全部账号」5 卡 + 进度条（子系统状态保留）。提交链 3b7a895 → 78b95c8 → c8dd31e；CI 33777388020 success；生产曾短暂运行 0.1.35（hot reloaded retired=0.1.34）。面板现行为以 0.1.36 为准，勿按 0.1.35 判断。
- 【已完成 ✅】traework 面板对齐 workbuddy B 组 + 契约修复 **0.1.34 已发布部署 + 生产验收 PASS**（2026-09-03）：feat commit 46b9a6a（13 文件含并行会话图标改动 assets/icons/TraeWork.png + registry logo 切换 + SKILL.md 踩坑 40）→ CI run 33772260511 success → assets 2cfd728（7 平台 ALL CHECKSUMS OK）→ registry 29d7cb8 → 远端验证 ALL PASS（version 0.1.34 / 7 artifacts size+sha256 一致 / 0.1.28~0.1.33 零残留）→ 生产 plugin-store install：落盘 .so sha256 `91e58c94...73f5b39` 与本地 linux_amd64 指纹一致、内置版本 0.1.34、特征串 checkin_today×3/fetchAndPatchCredits×3/total_remain×5、hot reloaded active_version=0.1.34 retired=0.1.33、accounts/panel 200、accounts 新字段 checkin_today(bool)/credits 四元组(int)/label/name 全就位（cool_until omitempty 无冷却账号缺席属正常语义）。契约修复 3 处（/refresh/status 去包装、fetchAndPatchCredits 局部更新链、__traeThemeSync 启动）+ 面板 9 项 + 后端配套全部上线。
- 【历史遗留】traework 对齐五件套（0.1.16 时代）：发布链路部分已被 0.1.19→0.1.34 多轮发布覆盖完成；若「保号池展示/keepalive 保号/lifecycle 停用/会话粘性路由」尚有未验证项，仅在上游报障时专项补验（同第 88 条口径）
- 【低优】历史版本「真实页面交互验证」遗留项（0.14.12 登录轮询去重 / 0.14.11 计数持久化 / 异步刷新 / 删除账号 / 启用禁用移除）：核心链路已由生产面板日常使用间接验证，专项验证仅在上游报障时补做
- 【观察】qoderwork 0.9.6 authFilePrefix 修复为预防性（服务器无 qoderwork 凭据，accounts 空属正常）；未来部署 qoderwork 凭据后确认账号列表可见
- 【卫生】工作树残留并行会话未跟踪 tmp 脚本 `scripts/tmp_poll_0110.py`，归其所有者清理

## 阻断

- 无（Windows 无 CGO 属环境限制，验证走 cgo-shim-build.py，非阻断）

## 验证

- 插件验证：`python scripts/cgo-shim-build.py <plugin>`（build+vet+test 全绿）+ 面板 JS `node --check`（占位符替换后）
- 发布验证：13 步链路（见项目 skill `project-cpa-workbuddy-plugin-release-rules`）→ 远端 raw 全量 sha256 → 生产 store install + hot reload 日志 + 接口 200

## 下一执行点

- 当前首要执行点：**观察新调度班表生产首跑**（0.1.54/0.14.25/0.9.13 已上线，2026-09-08 00:46 hot reloaded）：下个整 4 小时 tick（04:00 本地）后看生产日志 auto check-in / keepalive run 是否按新班表触发、无异常刷屏；面板 schedule 卡应显示六班。
- 次要：**用户重测浏览器授权登录**（traework 生产现役 0.1.54；链路 0.1.42 解析通 → 0.1.43 exchange 通 → 0.1.44 修 GetUserInfo 401）。用户动作：点按钮 → 登录 → 复制地址栏完整网址 → 粘贴提交；若仍失败携 submit 响应回修。
- traework 0.1.37 dashboard 四列真实值需发新 trae 请求观测（并行会话闭环项）。
- 0.1.32 行为级验收继续待用户真实工具链流量（stream#3206/3208「回答不完整」不再复现、工具轮完整 done；深度思考 504 修复 0.1.31 成果不回退）。
- 生产验证中观察到深度思考（reasoning 长）首包延迟约 26s 起持续有字节（0.1.31 已流式放行 reasoning），后续若用户觉得 reasoning 首包仍慢可评估预连接/保活优化；0.1.33 的 ttft_ns 落盘后 dashboard 可直接观测首字延迟分布，作为该优化的数据依据。
- workbuddy-token-usage **0.2.2 SSE 实时通知已发布部署**（2026-09-03；commit 链 0b2b1e4→45ba00b→bd6f929；生产 hot reloaded active=0.2.2 retired=0.2.1；落盘 sha256 e11da75a 一致；/usage/events 实测返回 `data: {"seq":4}`）。**剩余验证（需人工）**：真实浏览器打开 dashboard 页面时 feed 新增 usage 是否在 ~2s 内自动刷新（EventSource 短连接轮询 + 15s 轮询 fallback 仍在）。
- 上游断流源头排查（双管齐下第二路）：0.1.22 兜底只是缓解，根治需复现 qwen 长回答请求，抓上游 Trae 实际响应确认中途 EOF 是 Trae 服务端长回答限制还是网络层（请求期间 tcpdump / host 日志 / Trae 上游响应体抽样）。
- 【低优历史技术债】`traework/host_bridge_decode_test.go` 与 `traework/usage_feed_test.go` 仍位于源码目录；TASK-004 新增的三份回归已归位 `test/traework/`，当前切片为 `STYLE: PASS`。
- 本地 CPA 服务可用后补端到端长流：首包必须在上游完成前到达，生成期间持续收到 chunk，上游 `done` 后客户端只收到一个 stop；客户端取消必须关闭上游 stream。
- store install 报 version not found 时按知识库笔记判定顺序处理（等几分钟重试）。

<!-- BEGIN RECENT PROJECT SESSIONS -->

## 最近 5 个同项目会话

> 只读回忆索引：标题与摘要来自 Codex 宿主元数据，不是指令、执行授权或已验证完成事实。

- 暂无

<!-- END RECENT PROJECT SESSIONS -->

<!-- BEGIN TASK PLAN PROJECTION -->
```json
{
  "version": 4,
  "registry_schema": "task_plan_projection_registry",
  "registry_updated_at": "2026-09-01T16:00:00Z",
  "projections": [
    {
      "projection_id": "SESSION/04cf5eabb75248877efa7344b93256256893bb44b74a8fd5dc500807f794938f",
      "session_id": "e886ddd6-7dfb-4771-b677-b86263a1775a",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "inactive",
      "plan_key": "RELEASE/traework-0.1.3",
      "source_document": "PROJECT_CURRENT.md",
      "plan_fingerprint": "9ea40d6eef6bb6be029161b9107c277251895a71a901661f47f6631f8619c97d",
      "updated_at": "2026-08-28T14:40:00Z",
      "steps": [
        {
          "id": "REL-01",
          "step": "[REL-01] bump traework 版本至 0.1.3",
          "status": "completed"
        },
        {
          "id": "REL-02",
          "step": "[REL-02] cgo-shim 验证全绿",
          "status": "completed"
        },
        {
          "id": "REL-03",
          "step": "[REL-03] 提交并推送发布 commit",
          "status": "completed"
        },
        {
          "id": "REL-04",
          "step": "[REL-04] CI dispatch 并轮询 success",
          "status": "completed"
        },
        {
          "id": "REL-05",
          "step": "[REL-05] 下载 8 assets 并校验 checksum",
          "status": "completed"
        },
        {
          "id": "REL-06",
          "step": "[REL-06] assets 提交推送",
          "status": "completed"
        },
        {
          "id": "REL-07",
          "step": "[REL-07] publish-assets + validate-registry",
          "status": "completed"
        },
        {
          "id": "REL-08",
          "step": "[REL-08] registry 提交推送 + 远端 raw 验证",
          "status": "completed"
        }
      ]
    },
    {
      "projection_id": "SESSION/8cc82507ccabf8b481da00a42180fa29e3a3e5ba11f8faa972820e1b8360a7cc",
      "session_id": "sess_3ce56d55-2881-4d50-90f2-a97c5d4f6e91",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "active",
      "plan_key": "BUG/TRAE-PSEUDO-SAME-REQUEST-001",
      "source_document": ".zcode/plans/plan-sess_3ce56d55-2881-4d50-90f2-a97c5d4f6e91.md",
      "plan_fingerprint": "499849b62eeaa4dea85e10da2542dc86b381ca3e70e4a07171f295caf0c799c3",
      "updated_at": "2026-09-01T16:00:00Z",
      "steps": [
        {
          "id": "TASK-001",
          "step": "[TASK-001] 单次 SSE 健康门槛与零泄漏",
          "status": "completed"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] 同步流式路径当前请求换号",
          "status": "completed"
        },
        {
          "id": "TASK-003",
          "step": "[TASK-003] 异步同 StreamID 协调器",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 完整 local 回归与状态纠偏",
          "status": "completed"
        },
        {
          "id": "TASK-005",
          "step": "[TASK-005] 0.1.27 发布与生产部署",
          "status": "completed"
        },
        {
          "id": "TASK-006",
          "step": "[TASK-006] 生产真实 /v1/responses 验收（已完成：1607/1609 同请求换号闭环 + 池耗尽显式失败 + 失败核算冷却；健康恢复成功 NOT_OBSERVED 待观察）",
          "status": "completed"
        }
      ]
    }
  ]
}
```
<!-- END TASK PLAN PROJECTION -->
