# Changelog

## 0.1.13 (2026-11-14)

### Fix - 桌面端渠道账号被误判为「签到活动已下线」，12 个账号里 10 个状态错误

- 缺陷(生产实测 2026-11-14 17:47): 0.1.12 上线后 `GET /checkin/status` 对生产 12 个账号只给出 2 个正确状态，其余 10 个（`号10` / `用户358840` / `用户288838x` / `第七` / `用户220908` / `用户766588` / `用户815917` / `用户825285` / `用户305270` / `用户224666`）全部 `state=error` + `message=读取签到活动失败：签到活动已下线或已换季`。
- 根因: 判据顺序错了。`checkinStatusFor` 与 `runCheckinForAccount` 都先查活动详情 `v3/detail`，详情回非 0 就整条失败；但**桌面端渠道账号在网页侧没有活动实例**，其 `v3/detail` 必然回 `{"data":null,"code":2,"message":"campaign not found"}`，而同一账号的 `landing/get` 是正常的 `{"chosen_type":3}`。于是「需桌面端领取」被错报成「活动已下线」，文案与真实原因完全不符，用户会被误导去排查活动是否换季。
- 上游取证: 生产宿主机直打两账号对比——`doubao-adcd31d29873b4c8.json`（欧皇小德子）`v3/detail` → `code=0`（含 tasks 与 example_cases）、`landing/get` → `{"chosen_type":0,"landing_cards":[]}`；`doubao-20fcbcfb49f40321.json`（号10）`v3/detail` → `{"data":null,"code":2,"msg":"","message":"campaign not found"}`、`landing/get` → `{"chosen_type":3,"landing_cards":[{"card_code":"web_pc_download_banner",...,"content":"下载桌面端"}]}`。`landing/get` 才是渠道判据的唯一真相源。
- 修复: `checkin.go` 两处判据顺序统一改为「登录态 → **落地页 `chosen_type` 先行** → 网页渠道才拉详情 → 三态判定 → 额度降级」。桌面端账号在落地页判定处直接返回 `desktop_only`，**完全不请求活动详情、不产生任何签到写请求**；步骤编号同步重排（`checkinStatusFor` 2/3/4/5、`runCheckinForAccount` 2/3/4/5）。
- 测试: 新增 `TestCheckinStatusForDesktopAccountIgnoresDetailError` 锁死本次生产缺陷——桩里 `v3/detail` 回 `code=2 campaign not found`、`landing/get` 回 `chosen_type=3`，要求判 `desktop_only` 且**不请求详情路径**、不请求签到路径；`TestRunCheckinSkipsWhenAlreadySigned` 补显式 `checkinLandingPath` 桩，避免新顺序下靠默认响应蒙对。用例数 13 → 14。
- 哨兵验证: 把落地页分支条件临时改成 `if false && chosenType != checkinChosenWeb` 后 `TestCheckinStatusForDesktopAccountIgnoresDetailError`（`checkin_test.go:203`）与 `TestRunCheckinSkipsDesktopChannel`（`checkin_test.go:230`）双双 FAIL，恢复后全绿——证明新用例真实进编译并守住了顺序。
- 影响范围: 仅 `checkin.go` 两个函数的判据顺序与 `checkin_test.go` 用例、`VERSION` / `main.go` 版本号；路由、面板、定时器、额度递归、`protocol.go` / `models.go` / `completion.go` 零改动。

## 0.1.12 (2026-11-14)

### Feat - 每日签到与活动赠送额度（面板可见 + 手动签到 + 每日两轮定时）

- 功能: 面板新增「今日签到与免费额度」区块与工具条「今日签到」按钮。每个账号一行：签到状态徽章（今日已签 / 今日未签可签到 / 需桌面端领取 / 登录态过期 / 签到失败）+ 连续天数 + 活动累计额度 + 今日可得额度 + 赠送额度窗口（已用/总量/百分比）。新增管理路由 `GET /checkin/status`（query `auth_index` 可只查单账号）与 `POST /checkin/run`（body `{auth_index?}`，缺省全部账号）。
- 定时: 每天 `10:00` 与 `22:00` 各跑一轮全账号签到（`checkinHours = []int{10, 22}`）。两轮是刻意冗余——上午拿当天额度，晚间兜住上午因上游抖动漏签；重复提交天然幂等（上游回 `idempotent=true`），不会重复发放。
- 契约: 签到 `POST /alice/ug/campaign/landing/checkin/` body `{"scene":"pc_incentive_task_mvp_v3"}`；活动详情 `POST /alice/ug/campaign/v3/detail/` body `{"campaign_group_code":"pc_incentive_task_mvp_v3"}`；落地页 `POST /alice/ug/campaign/landing/get`；额度 `POST /alice/commerce/sale/subscription/quota/summary/` body `{}`。四个端点全部复用既有 `postBusinessJSON`（同一套 19 键网页端 query + 网页端指纹），未新造请求栈。
- 渠道判据（关键）: 落地页响应的 `data.chosen_type` 决定该账号能走哪条入口。`chosen_type=0` 网页签到可成功（回执 `checked_in=true`、`reward_amount=200`）；`chosen_type=3` 被平台改投桌面端（`landing_cards[0].card_code="web_pc_download_banner"`、副标题「前往豆包桌面端即可领取」），任何参数组合都回 `710010702`。生产两账号实测确认，判据可靠。
- 渠道分流的硬约束: 只在 `chosen_type=0` 且今日未签时才提交签到；`chosen_type=3` 只上报「需桌面端领取」，**完全不产生签到写请求**。桌面端形态详情的 `tasks[]` 里根本没有 `daily_signin`（web 形态 7 条含它，客户端形态 5 条不含），说明平台按渠道裁剪活动，不是参数缺失。
- 成功判据: 单个 `code=0` 不算成功。提交后等 2s 回查详情，只有 `daily_checkin.checked_in_today=true` 才判 done，否则如实报「已提交但上游未确认到账」。该口径与 `L0NE-6/DoubaoWork-Daily`、`hiclaw168-163/auto-signin-kit` 两个开源实现一致。
- 风控红线: 不伪造 `biz_params` / `AgwCommonParam` / `a_bogus` 等 SDK 风控字段；不做任务奖励自动领取（`item/reward/claim` 对 `daily_signin` 只回 `biz_error.code=3 领取结果确认中`，不是有效签到路径）；不加后台探活。已签账号完全不发写请求，避免重复裸提交触发上游异常流量判定。
- 额度读法: `quota/summary` 的 `window_limits` 数组位置由上游决定，故按 key 名递归提取，读 `feature_group_name` / `total_amount` / `used_amount` / `used_percent` / `end_time`；数值兼容数字与带千分位/货币符号的字符串。已签账号才有活动赠送额度窗口（生产实测 200/200，到期 `1797237901995`）。
- 代码落点: 新增 `doubao/checkin.go`（接口调用、状态判定、递归额度提取、定时器）与 `doubao/checkin_test.go`（13 个用例）；`doubao/management.go` 登记 2 条路由 + 2 个分发分支；`doubao/main.go` 在 `configurePlugin` 末尾幂等拉起签到定时器；`doubao/panel.html` 新增签到区块与按钮。`protocol.go` / `models.go` / `completion.go` 零改动（`buildBusinessQuery` / `endpointURL` / `clientUserAgent` 全现成复用）。
- 定时器取舍: 与 qoder 系插件一致，故意不提供停止函数——宿主在自身运行时拆卸阶段调用插件 shutdown，此时从 c-shared 运行时触碰 `sync` 原语会让进程 SIGSEGV，故 `ensureCheckinScheduler()` 用 `checkinSchedulerOn` 布尔做幂等，重复 reconfigure 不叠加 goroutine。
- 验证: `python scripts/cgo-shim-build.py doubao` → build / vet / test 全绿（`ok ... 8.2s`）；`panel.html` 两个 `<script>` 块经 `node --check` 语法校验通过。生产两账号真实签到实测：账号1 `chosen_type=0` 拿到幂等回执 `checked_in=true / reward_amount=200`，账号2 `chosen_type=3` 被正确识别为「需桌面端」且未提交。

## 0.1.11 (2026-11-14)

### Fix - 工作模式全模型 503：顶层 `option.conversation_mode` 写成字符串被上游拒绝

- 缺陷(用户反馈 2026-11-14): 通过别名 `deepseek-pro` 请求 `doubao/deepseek-v4-pro` 报 `503 auth_unavailable ... 上游拒绝了请求参数：请确认 Content-Type 为 application/json 且请求体结构完整`。实测不止 pro 别名——`doubao/deepseek-v4-flash`、`doubao/deepseek-v4-pro`、`doubao/default`、`doubao/auto` **四个工作模式模型全部同错**，且同一时刻两个账号各失败一次，说明是请求体被上游参数校验拒绝，不是模型档位或账号问题。
- 根因: 顶层 `option.conversation_mode` 被写成了字符串（`strconv.Itoa` → `"2"`），而上游对该键要求 **JSON 整数**。上游返回的是 HTTP 200 + SSE 错误帧 `{"error_code":710020202,"error_msg":"common invalid param"}`，心跳帧先到、错误随后，故失败耗时仅 130~190ms（正常生成 880ms~1.2s）。**同名不同型陷阱**：`aggregate_params.conversation_mode` 反而必须保持字符串 `"2"`，两处类型不同、不可互换。
- 定位过程: 在宿主机用同账号 Cookie 复刻请求直连 `https://www.doubao.com/chat/completion` 做逐级累加消融（S0→S9）。S0~S6 全 OK，从 S7（同时加入顶层 `conversation_mode="2"`、`message_storage_type=0`、`sse_recv_event_options`+`support_lazy_fetch_stream`）起 ERR；再拆分 S7 三字段，`allbutconv`（去掉 conversation_mode）OK、`conv2int`（写整数 2）OK，最终三变体决定性对比：`"2"` → ERR、`2` → OK、缺键 → OK。
- 修复: `completion.go` 的 `completionOption.ConversationMode` 类型由 `string` 改为 `int`（保留 `omitempty`），赋值由 `strconv.Itoa(workConversationMode)` 改为 `workConversationMode`；`aggregateParams.ConversationMode` 维持 `string` 不动。字段补注释写明该陷阱，避免后续再被"统一类型"改回字符串。
- 验证: `python scripts/cgo-shim-build.py doubao` → build / vet / test 全绿；整数写法下上游完整跑通，事件序列 `SSE_HEARTBEAT,SSE_ACK,FULL_MSG_NOTIFY,STREAM_TIMEOUT_CONTROL,STREAM_MSG_NOTIFY,STREAM_CHUNK,...,SSE_REPLY_END`，`deepseek-v4-pro` 与 `deepseek-v4-flash` 均返回正文（`1+1=?` → `2`），无 error 帧。
- 影响范围: 仅请求体该字段类型与赋值，以及 `workmode_test.go` 对应断言；路由、failover、SSE 解析、面板与账号逻辑零改动。

## 0.1.10 (2026-11-14)

### Fix - 面板搜索框被浏览器表单历史自动填回上次搜索词

- 缺陷(用户反馈 2026-11-14): 面板刷新后搜索框会自动填入上次的搜索词，账号列表被旧关键字过滤，看起来像账号丢失。根因是浏览器把 `type="search"` 输入框当成可记忆的表单字段，重载时用表单历史恢复 `value`，与 `autocomplete="off"` 无关——浏览器对同名字段的恢复不受该属性约束。
- 修复: `panel.html` 新增 `searchInputNoAutofill()` 自执行函数——加载时把输入框 `name` 改成每次随机的 `q<random>`，浏览器无法用固定字段历史命中它；同时补齐 `autocorrect="off"`/`autocapitalize="off"`/`spellcheck="false"`/`data-form-type="other"`，压掉移动端与密码管理器启发式填充。
- 放行真输入: 用 `pointerdown` / `keydown` 记录「用户确实操作过」，只有未操作过才在 `focus`、`pageshow` 与 0/60/200/600/1200/2500ms 六个短窗口内清空 `value` 并复位 `currentSearch` + 重渲染，避免浏览器在首帧之后才恢复历史值；用户主动输入的搜索词不会被清掉。
- 影响范围: 仅面板前端——搜索输入框属性与上述自执行清理函数，账号列表渲染、筛选语义、签到/导入/导出等全部链路零改动；后端与插件协议无变化。
- 验证: `python scripts/cgo-shim-build.py` build / vet / test 全绿；10 个插件面板同源改动逐一落地（`searchInputNoAutofill` 与 `data-form-type` 各命中 2 处）。

### Fix - 面板「测试模型」列表错乱：列出上游内部代号，且点任何代号实际都跑默认模型

- 缺陷(用户反馈 2026-11-14): 面板测试弹窗列的是 `doubao/4tbp`、`doubao/anceba`、`doubao/ances brs` 这类豆包内部代号，与宿主「认证文件管理 → 支持的模型」里的可读别名对不上；更严重的是点任何一个代号，「测试成功」提示里的模型名会回显代号，实际请求却回落到了 `deepseek-v4-flash`，测试结论与所选模型无关。
- 根因一(列表来源错): `handleModelsQuery` 直接把 `/alice/basic/launch` 返回的上游代号当模型列表下发，完全没有并入插件别名目录；宿主那侧走的是 `modelForAuth → catalogueWithUpstream`，别名在前、代号追加在后，所以宿主看着正常。
- 根因二(静默替换): `modeForModel` 对不认识的名字一律回落 `defaultWorkModelID`（正式对话链路需要这个宽松语义，避免客户端传怪名就 400），活跃测试复用了它，于是代号被悄悄换成默认模型，而成功消息只回显客户端传入的名字。
- 修复一: 新增 `testableAliases` / `testableModelIDs` / `testableModelDetails`，测试可选模型固定为插件别名目录（同 `modelForAuth` 同源），只排除 `image` / `video`——它们会真正触发上游生成，既耗额度又要等数十秒，失败原因还与账号不可用难区分。
- 修复二: `/models` 改为先返回别名目录（`source: aliases`，附 `details` 供面板显示可读名与「默认」标记），上游代号只作为 `upstream_models` 附带对照并给 `notice` 说明「代号无法映射到具体模型，故不参与测试」；上游不可达时只降级掉代号，别名目录照旧完整。
- 修复三: 新增 `aliasForTestModel` / `testModeForModel`，测试入口做严格解析——未命中别名直接返回「不支持的测试模型：…（请从模型列表中选择插件别名；上游内部代号无法映射到具体模型）」，不再静默替换；`handleTestActive` 缺省模型改为 `doubao/deepseek-v4-flash`。
- 修复四: 成功消息回显真正打到的上游主键——`活跃成功 (模型: doubao/glm-5.3 → 上游 person-glm-5-3-261111, 耗时: …)`，模型是否真的生效一眼可辨。
- 面板: 测试弹窗每项改为「可读名 + 默认标记 + 模型 id」两行显示（`.mi-name` / `.mi-id` / `.ref-tag`），鼠标悬停展示说明；`details` / `upstream_models` 解析全部对缺失字段做防御。
- 新增回归: `TestHandleModelsQueryKeepsAliasCatalogue`（上游不可达仍给完整别名目录且必含工作模式默认模型）、`TestHandleModelsQueryNeverListsUpstreamCodes`（代号不得进入可点击列表，只出现在 `upstream_models`）、`TestRunActiveTestRejectsUnknownModel`（未知模型名必须报错而非静默成功）。

## 0.1.9

- 账号授权改为「书签一键授权」：面板新增「豆包一键授权」入口，打开引导页后把授权书签拖进书签栏（只做一次），之后在豆包页面登录完点一下书签，插件即用 x-tt-token 换回完整登录态并落盘。豆包的 sessionid / sid_guard 是 HttpOnly，页面脚本读不到，此前必须让用户自己开开发者工具复制 Cookie。
- 新增令牌兑换链路 tokenbeat.go：凭据换发端点 /passport/token/beat/v2/（与扣子插件同一 passport 网关），令牌只兑换一次且立即消费，插件内存不保留；缺 flow_cur_user_sec_id 时用判据端点返回的 sec_user_id 补齐，再复用既有落盘链路，保证设备标识固化口径与手工导入一致。
- 新增授权回调资源 /login/callback：令牌经 URL fragment（浏览器不回传服务端）加同源请求头递交，不进 query，避免令牌落进宿主访问日志；回传成功后立即 history.replaceState 清掉 fragment。
- 授权会话支持失败原因回传：脚本取不到令牌时把真实原因写进会话，宿主轮询能显示「上次尝试失败：…」，不再只显示「等待中」。
- 授权链路对固定书签（无 state）与面板独立入口（无宿主弹窗）做兼容：认领等待中的会话让弹窗自动关闭，没有会话时提示回面板刷新。
- 手工粘贴 Cookie 降级为兜底通道（details 内），桥接页另一条兜底给出一次性脚本，供浏览器策略拦掉 javascript: 书签时使用。
- 新增回归：tokenbeat_test.go 覆盖令牌兑换成功/业务失败/零 Cookie/空令牌、令牌落盘、书签与脚本生成、公网来源取值优先级、回调页首开/失效 state/严格与宽松双模/重放幂等/脚本报错、回调路由注册与分发。

## 0.1.8

- 管理面板整页重写为与 workbuddy 同源形态：密钥零输入自动获取（`enc::v2::` /
  `enc::v1::` 双格式解码、`sessionStorage` 脏密文自愈清理、`?key=` 取后抹痕），
  401 累计 3 次才清 key 并进入 60 秒冷却，403 命中 IP 封禁时按上游给出的等待窗口
  退避，空响应 / 非 JSON / 坏 JSON 一律降级成 `{error}` 不再抛异常。
- 面板新增账号筛选（全部 / 可用 / 已禁用 / 过期）与汇总卡，排序支持创建倒序、
  账号标识升序、备注名升序；卡片展示备注名、昵称、手机号、脱敏账号标识、
  登录态、创建与更新时间，动作含测试 / 启停 / 删除（删除改为二次确认弹窗）。
- 导入改为弹窗批量导入：粘贴文本框一次可贴多个凭证，另支持多选 `.json` 文件
  （单文件上限 2MB），自动展开裸对象 / 数组 / 导出包装 `accounts[].credential`
  三种形态；逐条串行提交，命中上游限流自动重试 4 次，结束给出逐条结果。
- 新增导出能力两条管理路由：`GET /export` 输出单文件 JSON 汇总（含账号元信息与
  原始凭证），`GET /export/zip` 每账号一个 JSON 打包成 zip；导出内容原样取账号
  物理文件字节，保证「导出 → 导入」闭环不被重新序列化破坏。
- `POST /import` 载荷放宽：除原有 `{cookie, label}` 外，兼容 `{raw}`、`{json}`、
  导出汇总/单账号凭证对象，同一条导入链路可回灌导出文件。
- 新增回归：`export_test.go` 覆盖导入载荷三种形态、导出汇总路由、ZIP 路由与
  文件命名去重、空账号 404、导出路由注册。
## 0.1.7

- 新增工作模式管线（`agent_mode=1`）：按豆包 PC 客户端指纹直连
  `https://www.doubao.com/chat/completion`，请求体组装 `general_task_param` /
  `conversation_init_ext` / `option.ext` 三处工作模式参数，正文与推理内容按
  `STREAM_CHUNK` / `CHUNK_DELTA` 解析。技能管线（生图 / 生视频，
  `agent_mode=2`）保持原样，两条管线由 `generationMode.WorkMode` 分流。
- 账号指纹改为客户端形态：请求 query 用 19 键 PC 客户端参数集（`aid=582478`、
  `channel=win`、`client_platform=pc_client`、`web_platform=desktop`、
  `chromium_version=147.0.7727.149` 等），每次请求轮换 `web_tab_id`；请求头补
  `agw-js-conv: str, str` / `last-event-id: undefined` / `x-flow-trace`。
  业务端点（`/alice/*`）与模型发现仍走原网页端指纹，互不影响。
- `device_id` 由 Cookie 摘要派生并固化：导入账号时按 `sha256(CookieHeader+AuthID)`
  取前 16 字节逐位取模拼成 18 位数字串（前缀 `73`），同账号重复导入得到同一标识，
  避免被上游按多设备登录归因。`web_id` 缺失时同源补齐。
- 模型目录扩为两层：能力别名（default / pro / image / video）+ 工作模式模型
  （auto / doubao-2.1-lite / doubao-2.1-pro / deepseek-v4-flash / deepseek-v4-pro /
  kimi-k3 / kimi-k3.1-pro / kimi-k3.1-pro-fast / glm-5.3 / glm-5.3-flash）。每条
  工作模式模型锁定 `model_item_key` + `memory_profile` + `total_window_size`。
- 新增七项配置：`work_bot_id` / `work_mode_id` / `work_reasoning_effort` /
  `work_memory_profile` / `work_total_window` / `work_enable_sandbox` /
  `work_workspace`，均带默认值，缺省键保留原值不被热更新冲空。
- 新增回归：`workmode_test.go` 11 项，覆盖工作模式请求体契约、`model_item_key`
  三处一致、顶层 `ext` 必须为空、技能管线不被工作模式污染、客户端 query 形态与
  `web_tab_id` 轮换、客户端请求头指纹、`device_id` 派生稳定性。

## 0.1.5

- 修复导入一个账号后面板出现两条完全相同记录。宿主有两条注册路径：`host.auth.save`
  用文件相对路径派生 ID（带 `.json`），登录结果直接用 `AuthData.ID`。插件回传的 ID
  不带后缀，同一个物理文件因此被登记成 `doubao-xxx` 与 `doubao-xxx.json` 两条记录。
  现统一为宿主文件形态（`authFileNameOf`），`auth.parse` 也回传同名 ID；账号列表
  另按 `AuthID` 去重，兜住旧版本已写进宿主的历史重复项。
- 模型清单改为动态发现：新增 `model_discovery.go`，从 `/alice/basic/launch` 的
  `model_list` 拉取账号真实模型（实测 67 条 / 26 个去重代号），与 4 个能力别名合并
  后按账号返回。上游不可达时回落到能力别名并在面板显示原因，不会让目录变空。
  缓存按账号隔离、10 分钟 TTL，面板提供「刷新模型清单」强制刷新。
- 上游风控提示改为可操作文案：`710022004` 由 `shark_admin/verify/semantic_reasoning`
  下发，同一 Cookie 换出口 IP、换设备标识、换深度思考参数都照样触发，而账号信息端点
  仍返回 `success`——这不是网络抖动，等几分钟通常无效，只有换账号或等风控窗口解除。
- `doubao/pro`（深度思考）的说明如实标注：当前与 `default` 发出的上游请求相同，
  `need_deep_think` / `model_item_key` 的正确取值尚未验证（风控期无法做参数消融），
  深度思考尚未真正生效。
- 新增回归：`TestAuthDataIDMatchesHostFileName`、`TestListAllAuthFilesDedupesByAuthID`、
  `TestDiscoverUpstreamModels`（含同名去重）、`TestCachedUpstreamModelsReusesPerAccount`
  （缓存按账号隔离）、`TestModelForAuthMergesUpstreamAndAliases`、
  `TestHandleModelsQueryFallsBackToAliases`；面板回归补回落提示与动态清单两阶段。

## 0.1.4

- 登录桥接页不再要求管理密钥。0.1.3 的桥接页让浏览器直接请求
  `/v0/management/plugins/doubao-provider/import`，而 management 路由由宿主强制
  鉴权，用户必须再填一次管理密钥才能完成导入——与 traework 等插件的授权体验不一致。
  现改为提交到本插件自己的 resource 路由 `/login/import`（宿主对 resource 路由
  GET 免密钥放行），Cookie 放 `X-Doubao-Cookie` 请求头。安全性由 state 保证：
  state 在发起登录时随机生成、15 分钟过期，等于一次性短时凭证。
- 移除桥接页的密钥输入框、localStorage 密钥读取与 401 清 key 逻辑。
- `/import` 管理路由不再承担桥接页回传职责，删除已无调用方的 `login_state` 分支。
- 新增回归：`TestLoginBridgeImportWithoutManagementKey`（无 Authorization 也能导入
  且轮询拿到 success）、`TestLoginBridgeImportRejectsBadInput`、
  `TestLoginBridgeImportRouteIsDispatched`（路由被删/改 404 时用例必须失败）。

## 0.1.3

- 修复登录桥接页的假成功：0.1.2 用「账号集合变化 / UpdatedAt 变新」判定登录完成，
  但插件每次正常请求后会把滚动 Cookie 写回账号文件，那同样刷新 UpdatedAt。
  生产实测复现：用户尚未粘贴 Cookie，只要期间有任意请求经过，宿主轮询就会
  返回 `status: ok` 并显示认证成功。
  现改为**只认桥接页的显式导入信号**：`/import` 携带 `login_state`，导入成功后
  插件把账号标识登记到该会话，轮询只在该登记存在时返回 success。
  账号已落盘但会话过期时仍保留账号并提示重来，不丢用户已导入的凭据。
- 新增回归：`TestPollLoginIgnoresRefreshedAccounts`（账号被刷新仍须 pending）、
  桥接页断言「提交携带 login_state」。

## 0.1.2

- 账号卡片补齐「测试模型」（对齐 workbuddy 面板）：
  卡片新增「测试」按钮 → 弹窗选择模型 → 发起一次真实推理。
  测试走流式首帧即停（收到首个有效输出立即返回，不等全文），
  并拦截 HTTP 200 下的 `STREAM_ERROR` 帧（额度耗尽/限流不会被误判为活跃）；
  上游不回包由 20 秒上下文兜住。模型列表只含对话类别名——生图/生视频
  会真正触发上游生成，既耗额度又要等数十秒，且失败原因与账号不可用难区分。
- 修复宿主 OAuth 登录入口报 `failed to generate authorization url`：
  插件声明了 `auth_provider`，却只实现了 `auth.identifier`，宿主的
  `/v0/management/<provider>-auth-url` 调用 `auth.login.start` 拿到
  `unknown_method` 后统一回该错误。现补齐 `auth.login.start` /
  `auth.login.poll` / `auth.parse` / `auth.refresh`，并把宿主弹窗接到
  「浏览器 Cookie 导入」桥接页：豆包的登录态是 HttpOnly Cookie，只能由用户
  从浏览器导出，插件不实现一键授权（与既定认证路线一致）。
  登录成功的判据是「账号集合出现新成员」或「已有账号 UpdatedAt 晚于会话开始」，
  不使用宿主文件时间戳——插件刷新存量账号也会重置它，会造成未授权即成功。
- 新增回归：
  - Go 单测 20 个（首帧即停、错误帧拦截、空回复、超时兜底、登录起点/轮询/
    历史账号不误判、账号文件认领、刷新、方法分发、帧判定语义）；
  - 面板 `test/doubao/panel_test_model_repro.mjs`（19 断言）；
  - 桥接页 `test/doubao/login_bridge_repro.mjs`（18 断言）。

## 0.1.1

- 面板补齐 management key 鉴权（修复生产「导入失败：missing management key」）：
  宿主对 `/v0/management/*` 统一鉴权，不带 `Authorization` 时返回
  401 `{"error":"missing management key"}`。面板此前直接裸 fetch，
  导致导入、刷新账号、启停、删除全部被宿主拦断。
  现按既有 6 个插件的同构实现补齐三段式取 key：① CPA 主面板 iframe 内
  从 `localStorage["cli-proxy-auth"]` 解密（XOR+base64）；② URL `?key=`
  读取后从地址栏剔除并落 `sessionStorage`；③ 面板内手动填入。另补
  401/403 时清 key、重新弹框并给出可读提示，不再回显「未知错误」。
- 新增 `test/doubao/panel_auth_repro.mjs`（16 项 DOM 断言，覆盖三条兜底路径与 401 处理）

## 0.1.0

- 插件骨架与宿主注册：`doubao-provider` 能力声明、管理路由、内嵌面板页
- 登录态导入与有效性校验：面板粘贴浏览器 Cookie → 判据端点
  （`accounts.doubao.com/passport/account/info/v2/`）确认登录态 → 原子落盘
  账号文件；垃圾凭据明确拒绝，不落盘
- 账号管理：面板列出/启用/禁用/删除账号，登录态状态上报（`auth_state`）
- Cookie Jar 写回：捕获上游滚动下发的 `Set-Cookie`（如 `gd_random`）并持久化，
  写入前比对文件身份防止覆盖外部新导入的凭据
- 对话链路（`/chat/completion`）：按 `content_block` + `mode_id:"1"` 契约构造请求体，
  解析 SSE 多类增量事件（`STREAM_MSG_NOTIFY` / `STREAM_CHUNK` / `CHUNK_DELTA`），
  识别流内 `STREAM_ERROR` 并转成可操作中文提示；**关键修正**：`client_meta.local_conversation_id`
  为必填项，缺失或空串会被上游以 `710020202 common invalid param` 拒绝
- 模型目录：声明 4 个稳定语义别名（`doubao/default`、`doubao/pro`、`doubao/image`、
  `doubao/video`），`image`/`video` 携带 `skill` 3/5 激活生成路径；未知名原样透传；
  出口自行响应宿主 `oauth-excluded-models`
- executor 接入：非流式返回完整 OpenAI 响应，流式逐帧转 `chat.completion.chunk`
  （首帧带 `role`，末帧 `finish_reason=stop`），深度思考走 `reasoning_content`
- 生图生视频：解析 `CREATION` 块（`block_type=2074`），按 `id` 合并同一资源的
  多帧上报（首帧只有占位尺寸、后帧才带 `key` 与直链）；图片优先取 `image_ori`，
  再调 `/creativity/resource/get_without_watermark` 换无水印全尺寸直链（失败时
  回退流内缩略图，不失败整个请求）；回复转成 OpenAI 多模态 `image_url` + `media` 扩展
  - **实测修正**：`skill`（3/5）不是生成路径的必要条件——上游按提示词意图决定是否
    绘制，不带 `skill` 的「画一只猫」同样产出图片，带 `skill=3` 的闲聊仍只回文本
    （上游回显 `input_skill.skill_id="0"`）。注入保留为语义声明
- 多账号池与故障转移：账号启停/删除直写物理文件；429 / 402 / 401 / 403 / 5xx /
  传输错误按阶梯冷却（30s × 连续失败次数，封顶 10 分钟），业务 400 不计入（换号
  无意义且会误伤健康账号）；同请求内遇 429/额度耗尽/登录态失效立即换号并排除刚失败
  的账号，一次成功即清零；`scheduler.pick` 只在宿主候选集内取号，无可用候选时返回
  `Handled:false` 让位宿主跨 provider 轮换
- Cookie Jar 防覆盖判定改用 SHA-256 内容摘要：Windows 下文件索引可能复用、等长
  JSON 可能落在同一时间戳刻度，`os.SameFile + ModTime + Size` 三项同时失效会让旧
  快照覆盖用户刚重新导入的凭据
