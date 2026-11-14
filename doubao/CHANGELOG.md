# Changelog

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
