# Changelog

## 0.5.6 (2026-10-24)

### 修复

- **历史账号无法被宿主识别，导致 coze 模型能列出但请求必然 503**。
  生产实证：`GET /v1/models` 正常返回 5 个 coze 模型，但真实请求报
  `auth_not_found: no auth available (providers=coze-provider, ...)`。
- **根因**：插件凭据写在自己的目录 `~/.antigravity_cockpit/coze_accounts/`，
  宿主只认自己的 spool（`mysqlstore/mysqlstore/auths/`）。两者唯一的同步点
  是 `writeAuthFileDirect` 里 `if auth.PhysicalPath == ""` 分支——
  **只有「首次授权且尚未关联物理路径」时才调 `host.auth.save`**。
  已存在于磁盘的历史账号永远不满足该条件，于是形成死角：
  插件 `/accounts` 能读到、积分能刷新，宿主 spool 里却没有条目。
  生产 9 个账号全部处于该状态；`docker logs` 中 `host.auth.save` **零命中**，
  证明 coze 从未成功注册过任何账号（对照：qoderwork 有 18 个账号在 spool）。

### 新增

- 管理路由 `POST /register-host`：把本地已落盘的账号按宿主命名规则
  （`<auth_id>.json`）原样搬运到宿主 spool，补齐历史欠账。
- **只推未禁用账号**：禁用号推上去会立刻参与路由，与用户意图相反。
- **原样搬运磁盘内容**而不是内存快照：内存里可能持有续期前的旧 Cookie。
- 逐账号留痕（`账号补注册宿主成功/失败`），失败原因可从 `docker logs` 直接追。
- 路径安全：`auth_id` 来自请求，拼成文件名前经 `isSafeCozeAuthFileName`
  命名空间校验与 `isSafeCozeAuthPath` 穿越校验，读取侧同样过闸门。

### 测试

- 新增 3 个用例：非法账号标识被拒（含 `../../etc/passwd`）、
  禁用账号不注册且给出跳过原因、原样读取拒绝路径穿越与非扣子文件。
- `cgo-shim-build.py coze`：build + vet + test 全绿；gofmt 干净。

## 0.5.5 (2026-10-24)

### 新增

- **自动领取每日登录积分（1500 分/天）**。此前插件只调 `credit/balance` 与
  `credit/balance/detail` 两个只读端点，从不触发每日发放，账号池长期 0 分。
  现按积分刷新周期自动领取，面板显示「今日已领/未领」并提供手动补领按钮。

### 关键契约（2026-10-24 生产九账号对照实验实证）

- **每日 1500 分真实存在**：来源 `src=24`「活动积分」，有效期仅 **1 天**，
  当天不领即归零——这是账号池经常 0 分的根因，不是额度被消耗光。
- **唯一触发器是 `GET /api/marketplace/trade/credit/balance/check`**。逐项排除：
  - 只调 `passport/token/beat/v2/`（插件现有续期）→ **不发**
  - 只访问 `GET /home`（模拟网页登录）→ **不发**
  - `coze_space/activity/info` / `commerce/get_user_benefit` /
    `incentive/user_task/list` → **均不发**
  - 不请求（对照组）→ **不发**
  - 只调 `credit/balance/check` → **发 1500 分**（3/3 复现）
- **幂等**：同日连打 3 次 check，当日条目始终 1 条、总额 1500，
  可跟随 5 分钟刷新周期高频调用而不重复发放。
- **响应口径（关键坑，初版实现曾被推翻）**：HTTP 200 + `code=0`，`data` 含
  `pass` / `deny_reason`。**它们描述的是「余额是否够用」，与本次是否发放无关**：
  全 9 账号复核中 b3f25e 与 f18297 返回 `{"pass":false,"deny_reason":1}`，
  但两者当日「每日登录」条目同样到账、`remain` 同为 1500。
  因此**绝不能凭 `pass` 判定领取结果**——真实判据只能是「明细里存在当天、
  且仍有余额的每日登录条目」，即先调 check、再从明细确认。
  `pass` 仅作排查线索保留。

### 实现

- 新增 `coze/daily_credit.go`：领取内核 + 明细确认 + 状态记录 + 跨日判定；
  `coze/daily_credit_manage.go`：面板状态视图与手动补领入口
  （`management.go` 已近 500 行，新链路独立成文件）。
- **必须先领再查**：check 会改变余额，顺序反了本轮拿到的仍是领取前的旧值，
  闸门会把刚领到 1500 分的账号继续判成 0 分并挡回宿主。
  由 `TestRunCreditTickClaimsDailyCreditBeforeFetch` 锁定调用序列。
- 跨日按**北京时间固定 UTC+8** 切日，不用 `time.LoadLocation("Asia/Shanghai")`
  —— 容器镜像不一定装 tzdata，缺库时会静默回退 UTC，把凌晨 8 点前的领取
  算到前一天，导致每天早上误报「今天还没领」。
- 领取失败只记日志不阻断余额刷新：一次网络抖动不该让整轮刷新失败、
  把好号的余额一起丢掉。
- 新增管理路由 `POST /claim-daily`（面板手动补领，领完立即回传最新余额）。
- `/accounts` 新增 `daily_credit` 字段（`claimed_today` / `claimed_at` / `error`）；
  从未尝试过领取时该字段为 `null`，面板显示「未知」而不是「今天没领」。
- 面板卡片积分区新增「今日登录积分已领 / 未领（1500 分 · 当天过期）/ 领取失败」
  三态文案，操作区新增「领今日积分」按钮。

### 测试

- 新增 12 个用例：请求只发一次、`pass=false` 仅记 hint、HTTP/业务码失败记录、
  无账号标识不发请求、**pass=false 但明细有当天余额 → 判已领**、
  零余额条目不判已领、非当日条目不判已领、请求失败时不做确认、跨日重置、
  UTC+8 切日、从未尝试返回 nil；`credits_routing_test.go` 新增 2 个顺序/过滤用例。
- **5 项变异反证全部 KILLED**：claim 移到 fetch 之后 → 顺序用例 FAIL；
  跨日判定退化为「领过就算今天」→ 跨日用例 FAIL；确认时要求 `passHint` 为真 →
  到账用例 FAIL（这条正是初版错误语义的回归守卫）；请求失败仍做确认 → FAIL；
  确认时忽略 `remaining>0` → FAIL。
- `cgo-shim-build.py coze`：build + vet + test 全绿；gofmt 干净；
  面板两段 `<script>` `new Function()` 语法校验 PASS。

## 0.5.4 (2026-10-21)

### 修复

- **账号池积分全部耗尽时，一次请求仍把整池白打一遍上游**。2026-10-21 生产实证
  （20:11 / 20:14 / 21:12 / 21:13 四个时段）：7 个账号 `remain` 全为 0，一次生图
  请求产生 7 条失败，每条耗时 1.2–3.2 秒、错误都是上游 `1000521 该用户积分已耗尽`。
  闸门在这四个时段一条都没拦（`低于路由红线` 计数为 0）。
  根因是 0.5.2 兜底语义的缺口：`verifyAccountCredits` 只在「池里还有别的可用号」
  时拦截，全池低额时判定为「换号没有目的地」而放行，于是交回宿主逐个白打上游。
  叠加 new-api 的 5 次自动重试，一次生图在客户端刷出 9 条错误。
- **新增「全池确定耗尽」的快速失败通道**。整池可用账号都「确定 0 分」且本号也确定
  低额时，直接返回 400：命中宿主请求级白名单（`clienterror.IsRequestFault` 认
  400/409/413/422）立即终止换号轮转、不冷却账号，且 400 不在 new-api 的自动重试
  区间——两层重试同时止住，零上游消耗。

### 实现

- 快速失败只认 `remain` 恰好为 0：低于红线但仍有残值的号可能跑得完这一轮，
  0.5.2 的「放行让上游自己判」语义不收紧。
- 判据要求「本号与池内每个可用账号的积分数据都新鲜」（`creditDepletedTrustWindow`
  = 15 分钟，比 5 分钟缓存 TTL 宽 3 倍，避免刷新周期边界期把刚拿到的 0 分判成
  「数据不可信」而使快速失败失效）。任一账号积分未知或数据过期就退回放行——
  否则一次刷新停摆会把整池误判成死号、全线拒绝服务。
- 同时校验本号：池快照最多滞后一个刷新周期，快照之后才加入的新账号若积分充足，
  不能因为「快照里可见的号全 0」被判死。
- 快照为空时放行：循环还没跑过第一轮（刚热重载）时没有任何余额事实，
  此时判耗尽会误伤。
- 状态码选 400 而不是 403/503/429：403、503、429 都在宿主的换号白名单
  （`isCredentialRetryRoundStatus` = 403/408/429/500/502/503/504）里，
  等于让宿主继续逐个白打上游。400 既不在该白名单，也不在 new-api 的自动重试
  区间（`401-407,409-428` 恰好跳过 400），是唯一同时止住两层的取值。
- 红线配置为 0（kill switch）时整条判据自动失效：`remain < redline` 恒不成立，
  不需要额外的开关字段。
- 保留 0.5.2 的两条性能红线：闸门全程只读内存快照与积分缓存，
  不在请求路径发宿主 RPC 或上游请求。

### 测试

- 新增 8 个用例：全池 0 分必须 400、健康号存在时仍走 403 换号、空快照放行、
  缓存过期放行（本号过期 / 他号过期两条）、有未知积分账号时不判耗尽、
  残血非零放行、本号健康但快照滞后放行、错误状态码与信封分类、
  以及穿过 `handleExecExecute` 的端到端「零上游消耗」断言。
- 7 项变异反证全部 KILLED：禁用整条快速失败 / 状态码退化为 403 /
  残血号也判耗尽 / 空池也判耗尽 / 池内不校验缓存新鲜度 / 只看池不看本号 /
  忽略 kill switch。其中「池内不校验缓存新鲜度」与「忽略 kill switch」首轮
  **存活**——前者说明缺「本号新鲜但他号过期」的组合用例，补
  `TestVerifyAccountCreditsAllowsWhenPeerCreditCacheStale` 后捕获；
  后者暴露 `redline > 0` 是死代码（`remain < redline` 已隐含闸门开启），已删除。
- `cgo-shim-build.py coze`：build + vet + test 全绿。

## 0.5.3 (2026-10-21)

### 修复

- **生图全部失败但账号积分被烧光**。2026-10-21 生产实证：`doubao-seedream-5-0-*`
  的请求几乎全部返回 502 `上游未返回任何图片`，而账号 7840 积分被耗尽。
  根因是**收尾时序错配**：WS 的 `event_final` 帧在 6-9 秒就到，正文只有
  `[图片](computer:///...)` 占位符，真正的图片附件要十几秒后才落库到
  `message/list` 的 `files`。旧逻辑只在「正文为空」时兜底轮询，生图正文非空
  → 不轮询、不报错，直接把空图片列表交给 `buildImagesResponse` 报「没拿到图」。
  上游其实已经出图并扣了额度。
- **该失败不再触发换号重试**。无状态码的错误会被兜底成 502，宿主据此判账号故障、
  冷却并换号，于是同一笔额度被反复烧（叠加 new-api 的 5 次重试，一次生图请求
  可消耗数倍额度）。现在取不到附件时返回 `imagesUnavailableError`（400），
  命中宿主请求级白名单（400/409/413/422）：不冷却账号、不换号，直接把错误交给调用方。

### 实现

- 生图轮次在对话正常返回后统一由 `awaitImages` 等待晚到的图片附件
  （`pollImagesUntilAvailable`，1 秒间隔、4 分钟上限），文本对话路径不受影响、
  不会为等附件而变慢（由 `wantImages` 标记区分）。
- 等待点放在 `runConversation` 收口而不是 `waitForReply` 的分支里：
  `waitForReply` 有多个成功返回分支（收尾有正文、首字超时兜底成功、
  收尾空正文兜底成功），在分支内各自处理必然漏路径。
- 上限取 4 分钟而非最初的 90 秒：生产实测成功生图端到端耗时 24s~3m6s，
  90 秒会把成功场景误判成失败。
- 只在拿到非空附件时 `accept`：collector 的去重键是 `turn|seq|phase`，
  先 accept 一个空附件的 final 会把后续带附件的同一帧判成重复丢掉。
- `newImageExecutorFn` 注入点使真实入口可测（与既有 `runExecutorConversation` 同构）。

### 测试

- 新增 6 个用例：附件晚到仍能取到、附件第 3 轮才出现、取不到时必须是请求级 400、
  对话路径不长等且不轮询、错误类型状态码、以及穿过 `handleImagesExecute` 的端到端。
- 4 项变异反证全部精确 FAIL：禁用等附件分支 / 状态码退化为无类型错误 /
  退化成只查一次 / 工厂指向对话执行器。
- 最后一项首轮**存活**——此前所有用例都直接构造执行器，没有任何用例穿过
  真实入口，正是「纯函数单测通过 ≠ 调用点被覆盖」的典型形态；补端到端用例后捕获。


## 0.5.2 (2026-10-20)

### 新增

- **积分路由红线 `min_routable_credits`，扣子默认 100 分**。剩余积分低于该值的
  账号不再承载新推理请求：宿主选中它时，插件在执行前直接回 `403`，
  宿主按既有账号级故障通道换号并冷却 30 分钟。语义与上游
  `1000521 积分耗尽` 完全一致，只是提前到不打上游，省掉一次往返和一次失败记账。
- 阈值取 100 而不是兄弟插件的 20：扣子单次对话消耗远高于按次计费的套餐类
  产品，20 分只够一两次请求，起不到缓冲作用。该值可按插件配置覆盖，
  显式配 `0` 表示关闭闸门。
- 新增配置项 `credit_refresh_interval_seconds`（默认 300 秒），
  后台定时刷新全池积分，为闸门提供数据。

### 为什么不用 scheduler 能力

- 宿主 `internal/pluginhost/scheduler.go` 的 `schedulerRecord()` 只返回
  **排序后的第一个** scheduler 插件，是全局单例。扣子声明该能力要么永远
  不被调用（优先级低于在任者），要么顶掉现有插件的调度内核，
  连带让 workbuddy 系的高积分优先路由失效。故改走执行入口闸门。
- 插件账号的 auth 文件 `priority` 字段也不会被读取：watcher 对插件账号走
  `PluginAuthParser` 分支，该分支只解析 `disabled`，不解析 `priority`。
  `model.for_auth` 只影响模型注册表，同样无法用于排除账号。

### 实现

- 新增 `coze/policy.go`：红线常量与读写锁、`cachedCreditRemain`、
  `isCreditsBelowRedline`、`hasRoutableAccountExcept`、`verifyAccountCredits`、
  `creditsBelowRedlineError`（带 `StatusCode() 403`）。
- 新增 `coze/credit_loop.go`：后台积分刷新循环（并发 4、等宿主就绪、
  shutdown 停止、reconfigure 立即重排），刷新后发布闸门内存快照。
- **闸门只读内存快照，不发任何宿主 RPC**：`host.auth.list` 是
  「列目录 + 每账号读取」的扇出，放在每个请求的执行入口会把首字时间
  拖慢数毫秒到数十毫秒。快照由积分循环顺带拍平，请求路径零 RPC。
- 兜底语义：只有当池里**还有别的可用账号**时才拦截。全池都低或积分未知时
  放行——把最后一个残血号也挡掉会让用户直接看到无号可用，而实际上游
  可能还能跑完这一次。
- 两个执行入口（`handleExecStream` / `handleExecExecute`）均在
  `parseStoredAuth` 之后、触达上游之前加闸门。
- 面板新增 `low_credits` 字段与「低额保护」黄色徽标，与闸门同源
  （同一份缓存 + 同一个红线），避免面板显示「够用」而实际已被拦截。

### 测试

- 新增 12 个用例：红线默认值、边界（`<` 而非 `<=`）、未知积分放行、
  池有健康号时拦截、全池低额放行、达标放行、禁用/失效号不算换号目标、
  403 状态码、配置解析（缺省保持 100 / 显式覆盖 / 显式 0 关闭）、
  刷新循环写入与失败隔离、刷新后发布快照、入口拦截零上游调用、
  闸门不依赖宿主 RPC。
- 8 项变异反证全部精确 FAIL：红线改回 20、去掉全池兜底、403 改 500、
  删除入口闸门、闸门改回实时 RPC 扫描、删除快照发布、去掉可用性过滤、
  未知积分当 0 处理。
- 其中「未知积分当 0」与「删除快照发布」两项在首轮变异中**存活**，
  据此补强了对应用例才捕获——说明初版用例只覆盖了判定函数，
  没覆盖真正的放行/更新路径。

## 0.5.1 (2026-10-19)

### 修复

- **执行失败不再让坏号反复被选中**。此前执行错误走无状态码信封，
  宿主 `statusCodeFromError` 取到的是 0（不是 5xx），既不冷却该账号，
  也不进入换号重试轮；积分耗尽的号会被后续每个请求反复挑中，
  每次都先白等一轮上游失败。生产实测：同一账号 1.91s 失败后，
  下一个请求仍选中它。
- 积分耗尽（`code=1000521`）现在回传 `403`：命中宿主的换号白名单
  （403/408/429/500/502/503/504），并按 `MarkResult` 的 403 分支
  冷却该账号 30 分钟。之所以不用 402/429：402 只冷却不换号，
  429 走配额退避阶梯、冷却下限仅 10 秒，积分不可能在 10 秒内自愈。
- 其余未分类的上游失败（如「上游未返回任何图片」）兜底回传 `502`，
  同样可换号；请求级 4xx（如 413 内容超长）保持原样，不赔其他账号配额。
- 底层状态码优先：已被标注为 413 等请求级错误不会被外层改写为 5xx，
  避免「一次超长请求冷却整池」的老问题复发。

### 实现

- 新增 `coze/faults.go`：错误分类层（`executionFault` /
  `executionFaultEnvelope` / `withStatus` / `clawBusinessError`）。
- claw 的 `message/chat`、`claw/info`、`session/get_main`、
  `message/list`、`get_frontier_key` 业务失败统一走 `clawBusinessError`，
  按已实证的业务码表分类，未登记的码不猜。
- 修正 `main.go` 中「缺失时宿主一律当 5xx」的错误注释——宿主实际取 0。

### 测试

- 新增 9 个用例：4xx 请求级、配额耗尽 403、未分类兜底 502、
  底层状态码优先、错误链透传、信封字段契约、未知业务码不猜、
  以及 `sendChat` 调用点的端到端回归。
- 4 项变异反证全部精确 FAIL：去掉状态码、还原 `sendChat` 旧写法、
  兜底返回 0、把 403 降级成请求故障。

## 0.5.0 (2026-10-19)

### 新增

- **支持标准图片端点 `POST /v1/images/generations`**。此前 coze 的生图只能通过
  对话内嵌（`/v1/chat/completions` 返回 Markdown 图片），new-api 的图片页
  走标准端点时必然失败。现在该端点可用，返回 OpenAI 规范的
  `{created, data[]}`，支持 `response_format=url` 与 `b64_json`。
- 生图模型对外类型由 `"image"` 改为 `"openai-image"`：CPA 的图片端点白名单
  只认这个类型，填 `"image"` 会被 `rejectUnsupportedImagesModel` 直接 400。
  静态兜底目录同步修正（否则「无账号时 400、有账号时可用」的双轨不一致）。
- executor 声明 `openai-image` 输入/输出格式：宿主按插件声明协商格式，
  缺这个声明会让请求在 1ms 内 500（`does not support input format`）。

### 实现

- 新增 `coze/images.go`：images 请求解析、尺寸/张数翻译成提示词、
  响应包装。复用既有 claw 对话通道，不新增上游路径。
- b64_json 模式真实下载后编码，下载失败即报错——不返回伪造 base64。
- 图片下载限定 `coze.site` 白名单且必须 https：URL 来自上游响应，
  不限制目标等于让上游决定插件去请求哪个地址（SSRF）。
- 入口按 `Format` / `SourceFormat` 分流到 images 路径，
  不进 chat 解析（images 请求没有 messages）。

### 测试

- 新增 13 个用例：请求解析、格式归一化、提示词翻译、url/b64 响应、
  下载失败传播、SSRF 白名单、入口分流、静态兜底类型、对话/视频不回归。
- 5 项变异反证全部精确失败：入口分流、能力映射、SSRF 调用点、
  下载失败传播、响应格式口径。
- 补强两处最初抓不到变异的用例（单函数测试抓不到「调用点被删」）。

### 修复

- 静态兜底目录的生图模型类型此前漏改，导致无账号时图片端点必然 400。

## 0.4.6 (2026-10-19)

### 修复

- **一次超长请求会让整个账号池不可用**。生产实测：`doubao-seed-2-0-lite`
  连续返回 `503 auth_unavailable: no auth available`，宿主日志里
  两个账号各失败一次后全部短路（3–5ms 返回），错误文案都是
  `提交消息失败：数据库错误（code=1000003）`。
  根因是上游 `message/chat` 对 `content` 有 65535 字节硬上限
  （二分实测：65535 成功、65536 失败），越界时返回的是
  `1000003 数据库错误` 而不是参数错误；插件原样上抛后，宿主按 5xx
  判定为账号级故障，把两个账号依次打入冷却，之后所有请求都因
  「无可用账号」被拒。
  现在在同步入口按字节预检，超限直接返回 **413** 请求级错误：
  不再触达上游，宿主也不会冷却账号或换号重试。
  中文按 UTF-8 字节计（一个汉字 3 字节），不按字符数。
- 插件错误信封支持 `http_status`：只有带 4xx 状态码的错误才会被宿主
  归为请求级故障。其余上游失败（如积分耗尽 `1000521`、会话失效
  `700012006`）仍走原有账号级通道，换号与冷却语义不变。

## 0.4.5 (2026-10-19)

### 修复

- **失效账号在面板上仍显示「可用」**。生产实测：三账号中两个已被上游
  判定失效（续期返回「会话过期，请重新登录」、积分端点返回
  `code=700012006`），面板却全部显示「可用」，只有一个能真正积分成功。
  根因是登录态判据只看本地 Cookie 的 `Expires`，而扣子的会话 Cookie
  根本不下发 `Expires`，本地判活永远通过。
  现在新增 `auth_invalid` 标记：续期被上游明确判为会话失效时落盘置位，
  面板据此显示「待重新授权」；续期成功即清除。
  瞬态错误（超时、限流、5xx）不置位，避免误报。
- 失效判定只认上游会话类信号（「会话过期」「登录认证失败」
  `700012006`、`Login verification is invalid`），不按任意错误一刀切。

## 0.4.4 (2026-10-19)

### 修复

- **上游失败被当成「成功但 0 输出」返回**。生产实测：账号积分耗尽时
  上游 `message/chat` 返回 `code=1000521`，插件却仍补一个空正文的
  `finish_reason=length` 终止帧；宿主把「首个 payload」当作流已成功的判据，
  于是整轮请求被记为成功、下游既看不到错误也不计费（new-api 日志
  `Tokens: -`、费用 `$0`，CPA 面板输出 0、耗时仅 6~12ms）。
  现在首帧延迟到真正有内容时再补，一个 payload 都没产出时改走
  `stream.emit` 的 `error` 字段上报，错误文案原样透传给下游；
  正文已泄漏后失败仍用 `length` 收尾，避免作废已渲染内容。
- **收尾但无正文/图片时不再返回成功**。`waitForReply` 在
  `event_final` 到达且兜底轮询也拿不到内容时返回错误，
  不再让空响应当成功响应流到下游。

## 0.4.3 (2026-10-19)

### 诊断

- 补齐空正文路径的日志：`isFinished` 已收尾但正文为空时记录
  `message_id` 与兜底轮询失败原因。0.4.2 只覆盖 `runStream` 返回错误，
  而实测空正文走的是「正常返回但正文为空」，因此需要两条新日志才能定位。

## 0.4.2 (2026-10-19)

### 诊断

- 对话执行失败时输出一条 warn 日志（模型、prompt 长度、已收正文长度、
  收尾原因、错误原文）。空正文 + `length` 收尾在下游表现为「成功但 0 输出」，
  没有任何错误信息，只能靠这条日志定位。

## 0.4.1 (2026-10-19)

### 修复

- **下游用量全部记零**。生产面板显示 coze 请求「输入 0 / TPS 0.00」，
  new-api 通用日志里该模型 `Tokens` 恒为 `-`、费用 `$0`，无法计费。
  根因是插件从未在响应里输出 `usage` 字段：宿主只从 chunk/响应体的
  `usage` 取 token 数，扣子上游又没有 token 计数端点、WS 帧里也不带用量，
  于是整条链路拿到的都是零值。
  现在流式在**终止帧**、非流式在响应体输出 OpenAI 规范的 usage，
  按字符特征估算（CJK ≈ 1 字 1 token，拉丁 ≈ 4 字符 1 token），
  并带 `estimated: true` 便于区分估算值与上游实测值。

### 测试

- 新增 `TestBuildUsageEstimatesTokens`（估算值与 total 自洽）、
  `TestUsageOnlyInTerminalChunk`（中间帧不带 usage、终止帧必须带）、
  `TestBuildCompletionCarriesUsage`（非流式响应带非零 usage）。
  分别变异移除分片与非流式两条挂载路径，两个用例精确失败。

## 0.4.0 (2026-10-19)

### 新增

- **对话内生图能力打通**。此前生图回复只能拿到 `[图片](computer:///...)` 占位
  文本，图片本身被整条丢弃。现在图片会在正文之后以 Markdown 图片语法下发，
  流式收尾补一帧、非流式拼入响应体，下游客户端可直接预览。
  契约按 2026-10-18 真实账号实测确定：图片地址在消息的**顶层 `files` 数组**
  （`file_url` 为 `static.coze.site` 签名直链，有效期 30 天），不在
  `content_v2` 的内容分片里；生图约 18~25 秒原子出现，无中间态可流。
- `clawFile` / `clawMessage.Files` 解析附件；`frontierRawMessage.Files`
  收取 WS 帧顶层附件（`content_v2` 的 image/file 分片作为兼容路径保留）。
- 兜底轮询改用 `findReplyMessageFor` 取整条消息，避免只看内层 payload 漏图。

### 修复

- **非流式正文重复**。`execute` 原先把 `runStream` 的 emit 增量与返回值各算一次，
  响应体里出现两份正文。抽出共用执行体 `runConversation`，
  正文只取返回值，图片由调用方决定交付方式。
- 图片去重：同一地址在 `event_delta` 与 `event_final`（或 `files` 与
  `content_v2` 分片）重复出现时只下发一张。

### 测试

- 新增 12 个用例覆盖附件解析、顶层 `files` 取址、跨来源去重、
  空 url 守卫、收尾下发、非流式去重、兜底轮询取图。
- 夹具 `replyFrameWithImages` 改为真实上游形态（图片在顶层 `files`）：
  早期版本把它写成 `content_v2` 的 image 分片，导致「WS 取不到图」
  的单测完全不可见；变异回旧实现后 3 个用例精确失败。
- 新增 `live_image_test.go` / `live_download_test.go` / `live_seed_test.go`
  真实链路探针，仅在设置 `COZE_LIVE_COOKIE` 时执行，签名参数全部脱敏。

## 0.3.6 (2026-10-18)

### 修复

- **`/v1/responses` 与 new-api 转发请求必然失败**。生产日志大量出现
  `503 auth_unavailable ... last upstream error: 请求缺少 messages`。
  根因：`executorRequestBody` 优先读 `OriginalRequest`（客户端原始请求体），
  而宿主**只对 `Payload` 做协议翻译**。走 Responses 入口时原始体只有 `input`
  字段，没有 `messages`，插件在入口直接报错；该错误还会让宿主把账号标记为
  不可用，连带后续正常的 chat / completions 请求一起 503。
  修复为优先读 `Payload`，仅在为空时回退 `OriginalRequest`，与兄弟插件
  doubao 的取值策略一致。

### 测试

- `TestExecutorRequestBodyPreference` 改为锁定新优先级（Payload 优先）。
- 新增 `TestExecutorResponsesEntryParsesTranslatedPayload`：模拟 Responses
  入口（原始体只有 `input`、Payload 含 `messages`），验证能正确折叠成 prompt。
  变异回旧实现后两个用例精确复现生产报错文案「请求缺少 messages」。

## 0.3.5 (2026-10-18)

### 变更

- **面板「测试模型」升级为真实推理探活**。此前只校验登录态与模型可见性，
  测不出对话链路故障——本轮 0.3.0→0.3.4 的流式 500、空正文、多轮空回复
  都属于「认证层全绿但推理不通」的类型，面板却一直显示测试通过。
  现在 chat 模型会真的发一次极小推理（提示词「只回复两个字：可用」），
  走与用户请求相同的 executor 链路，能捕获只有对话链路才暴露的问题。
- 图片/视频模型仍只做前两层校验：它们不适用对话链路，且单次生成要数十秒。
- 探活结果的 `scope` 字段随之扩展为 `auth+models+inference`，
  面板文案同步改为反映真实覆盖范围。

### 修复

- **删除过期提示**：面板弹窗里「当前为认证层探活……对话链路将在后续周期
  接入」是 CYCLE-01 之前的说明，对话链路早已上线，该文案已不成立。

### 测试

- 新增 `TestRunActiveTest_InferenceSucceeds`（chat 模型走推理并成功）、
  `TestRunActiveTest_InferenceFails`（认证与模型都正常但推理失败须判失败）、
  `TestRunActiveTest_SkipsInferenceForImageModel`（图片模型不发推理）。
  变异移除推理分支后前两个用例精确失败。
- 推理探活抽成可注入变量 `inferenceProbe`，单测用假实现避免真实网络。

## 0.3.4 (2026-10-18)

### 修复

- **多轮对话返回空正文**。生产实测：单条 user 正常（65 字），带
  `assistant` 历史的多轮请求正文 0 字、`finish_reason=stop`。
- **根因**：`runStream` 用固定 `300ms` 猜 WS 是否连上再提交消息。
  网络慢时 chat 抢在连接之前发出；快速回复（多轮场景实测约 2 秒结束）
  的 `event_delta` 在连接就绪前就推完了，只收到重放的 `event_final`，
  而 final 的 delta 已被 0.3.3 清空 → 正文为空。
  WS 抓帧佐证：同一 prompt 上游确实推了 start / delta(28 字) / final 三帧。
- **修复**：
  - 新增 `frontierClient.WaitReady`，握手成功即关闭 `ready` 通道，
    调用方等真实就绪信号（上限为握手超时 10s），不再靠猜。
  - `waitForReply` 增加兜底：已收尾但正文为空时，按本轮 `message_id`
    去 `message/list` 取落库正文补齐。
  - `fallbackPoll` 改为按 `reply_to_message_id` 精确配对；原实现取
    「最新一条 reply」，可能把上一轮回复当成本轮结果。

### 测试

- 新增 `TestWaitReadySignalsAfterHandshake`（握手后立即就绪）与
  `TestEmptyReplyFallsBackToPolling`（只发 final 时靠轮询补回正文）。
  变异移除空正文兜底后，后者精确失败并在断言里复现空正文。

## 0.3.3 (2026-10-18)

### 修复

- **正文重复一遍：`event_final` 的累积全文被当成增量追加**。0.3.2 恢复真流式
  后，生产实测 39 个正文帧、正文 1156 字，其中末帧 578 字**恰好等于前文
  拼接**——同一段答案完整输出了两遍。
- **根因**：`content.message_data.reply.content` 在 `event_delta` 帧里是增量，
  但在 `event_final` 帧里装的是**累积全文**。`parseFrontierMessage` 对所有
  reply 帧一视同仁地取 content 当 delta，收尾帧就把全文又追加了一次。
- **修复**：解析完 `content_v2` 拿到 `phase` 后，`event_final` 帧清空 `Delta`，
  只保留 `Snapshot` 供校对。

### 测试

- 新增 `TestParseFinalFrameDoesNotDuplicate`（final 帧 delta 必须为空）与
  `TestParseDeltaFrameKeepsDelta`（普通增量帧仍保留 delta）。
  变异移除清空逻辑后前者精确失败。

## 0.3.2 (2026-10-18)

### 修复

- **WS 握手缺会话 Cookie，只能收到心跳**。0.3.1 修好序列化后，生产流式
  虽能返回完整正文，但耗时 92 秒且正文在最后一帧一次性到达——首帧之后
  91 秒没有任何增量，正好卡在首字超时（90s）上，实际走的是轮询兜底。
- **根因**：frontier 网关对「无 Cookie 的握手」照样返回 `101 Switching
  Protocols`，但只推 `hi` 心跳，一帧业务数据都不给。生产日志里
  `frontier 已连接` 到 `连接中断` 相隔 91 秒、中间零业务帧，与此吻合。
- **对照实验（同一生产账号，仅差一个 Cookie 头）**：
  - 不带 Cookie：6 帧，全部是 `hi`；
  - 带 Cookie：25 帧，其中 21 条二进制业务帧，在 0.34s–4.25s 陆续到达。
- **修复**：`frontierClient` 增加 `cookieHeader`，`runOnce` 握手时写入
  `Cookie` 请求头；`runStream` 从账号取 `sa.CookieHeader` 传入。

### 测试

- 新增 `TestFrontierHandshakeCarriesCookie`：假服务端记录握手 Cookie 并断言。
  变异删除该请求头后用例精确失败。

## 0.3.1 (2026-10-18)

### 修复

- **流式请求 500：宿主 RPC 无法序列化 channel**。0.3.0 的
  `handleExecStream` 把 `Chunks` 填成 `chan pluginapi.ExecutorStreamChunk`
  直接返回，而宿主 `rpcExecutorStreamResponse.Chunks` 是 slice，RPC 桥在
  JSON 序列化阶段就失败，客户端拿到
  `{"error":{"message":"json: unsupported type: <-chan pluginapi.ExecutorStreamChunk"}}`，
  生产实测 0 字节正文、0 个 chunk。
  改为宿主提供的异步路径：带 `stream_id` 时立即返回响应头，由后台协程用
  `host.stream.emit` 逐帧推送、`host.stream.close` 收尾；无 `stream_id`
  时退化为同步收集，一次性返回 slice 形态的完整 chunk 列表。
- **响应头补 `X-Accel-Buffering: no`**。缺这一项时反向代理会攒够缓冲才下发，
  真流式在客户端表现为「长时间无输出后一次性蹦出全文」。

### 测试

- 新增 `TestStreamResponseSerializable`：锁定「流式响应必须可 JSON 序列化」
  契约。变异回 channel 形态后该用例精确复现生产报错文案。
- 新增 `TestStreamHeadersCarryNoBufferHint`：锁定 `text/event-stream` 与
  `X-Accel-Buffering: no`。

## 0.3.0 (2026-10-18)

### 新增

- **对话链路打通（CYCLE-01 / TASK-004）：声明 executor 能力，支持真流式**。
  此前插件只暴露模型目录、不声明 executor，推理请求不会路由进来。
  现在完整链路为：`claw/info` 取 `claw_id` → `session/get_main` 取主会话 →
  `get_frontier_key` 取 WS 凭证 → 建立 frontier WebSocket →
  `message/chat` 异步提交 → 从 WS 收增量 → 转 `chat.completion.chunk`。
- **`frontier_frame.go`：protobuf 帧编解码**。上游帧是 12 字段 protobuf
  （`SeqID`/`LogID`/`service`/`method`/`headers`/`payloadEncoding`/
  `payloadType`/`payload`/`LogIDNew`/`serverTiming`/`msgID`/`frameType`），
  没有 `.proto` 可用，字段号由客户端 SDK 反编译并用真实帧校准。
  解码按 tag 分派并跳过未知字段，上游加字段不会让链路当天断掉。
- **`frontier.go`：WebSocket 客户端**。连接参数 `aid=751148`、`fpid=1289`、
  `xsack=0`、子协议 `pbbp2`；10 秒文本心跳 `hi`；读超时兜底；
  指数退避重连（1s 起、上限 30s，连上即重置）。
- **`claw.go`：claw HTTP 协议层**。四个端点统一错误判定
  （HTTP 200 + `code` 表达业务结果，只看状态码会把失败判成成功）。
- **`convert.go` / `executor.go`：OpenAI 协议转换与执行器**。
  多轮 messages 折叠成单条文本；增量逐帧下发（不合并）；按
  `turn_id + seq_num + phase` 去重；`event_final` 收尾；
  首字超时降级到 `message/list` 轮询兜底。

### 修复

- **重连延迟**：初版心跳协程只在 ticker 触发时检查状态，服务端断开后
  要空等一个完整心跳周期（10 秒）才重连。改为读循环结束时立即取消
  心跳协程，重连不再有额外延迟。
- **增量被合并**：初版按「累积正文差值」下发，两帧到达间隔小于轮询
  间隔时会被合并成一大块，客户端表现为「卡一下然后蹦出一大段」。
  改为维护待下发队列，每个上游帧独立 emit 一次。

### 说明

- **模型选择不由插件控制**：`message/chat` 不接受模型参数，实际模型由
  扣子侧配置决定（页面右下角选择器）。请求里的 `model` 只用于响应回显。
- **多轮上下文是有损的**：claw 只接受单条文本，OpenAI 的 system / 历史
  轮次会被折叠成「角色: 内容」逐行拼接，角色边界不作为独立字段传给上游。
- **`message/list` 只作兜底**：它返回已落库的完整消息（实测约 4 秒延迟），
  没有增量语义；只在 WS 首字超时（90 秒）时启用。

## 0.2.2 (2026-10-18)

### 修复

- **定时续期此前只是「排进队列」，并没有真正执行**：0.2.1 补的
  `refresh_interval_seconds` 只能让宿主把账号排进自动刷新队列，而
  `auto_refresh_loop.handleDueAuth` 在 `exec == nil` 时只做
  `l.upsert(authID, now.Add(interval))` 空转重排、不调用 `auth.refresh`；
  宿主 `RegisterExecutors` 只注册声明了 executor 能力的插件，扣子不声明
  executor，因此宿主的 4 小时排期对本插件永远不产生续期。
  现改为**插件内自建续期定时器**（`renew_loop.go`）：默认每 4 小时遍历所有
  启用账号执行 `renewSession` + `applyRenewal` 落盘；插件热重载后立即先跑
  一轮；`reconfigure` 改动间隔会立即重排，无需重启；`Shutdown` 时关闭停止
  信号，旧实例循环立刻退出。
- 续期失败只记日志，不改账号启用状态——临时网络问题不该把账号下线，
  否则它会在下一轮彻底失去自愈机会。
- 续期动作经宿主 `host.log` 留痕，`docker logs` 可直接看到每轮账号数、
  成功数与失败原因；此前后台续期没有任何日志出口，无法判断是否真的发生。

### 说明

- 单轮并发上限 4 个账号，避免账号多时一轮拖到几分钟，也避免触发上游风控。
- 缺少物理路径的账号会被跳过并记一条 warn：续期结果无处落盘，继续跑只会
  白耗上游请求，这种情况通常意味着宿主账号发现链路异常。

## 0.2.1 (2026-10-18)

### 新增

- **积分余额与过期提醒**：账号卡展示可用积分、额度池、消耗进度条与即将过期
  明细；汇总卡新增「可用积分」「即将过期」两格。数据来自
  `GET /api/marketplace/trade/credit/balance`（余额）与
  `.../balance/detail?account_type=2&display_status=2`（逐笔过期明细）。
  实测生产账号：总额 3000 / 可用 3000 / 即将过期 1500，与官网订阅页一致。
- **面板「刷新积分」按钮**：`GET /credits?auth_id=&force=1` 跳过 5 分钟缓存
  强制查询，只重绘该卡片。

### 修复

- **续期此前不是自动的**：宿主自动刷新循环的 `nextRefreshCheckAt` 在
  `NextRefreshAfter` 为零时会回退到 `refresh_interval_seconds` 与
  `ProviderRefreshLead`，而扣子不在宿主内置的 refresh lead 注册表里
  （只有 codex / claude / antigravity / kimi / xai）。插件从未下发该字段，
  导致账号永远排不进刷新队列——生产实测 24 小时内 0 条 auto-refresh 日志，
  三次续期全部来自手动点击。现于登录/刷新下发的 `AuthData.Metadata` 补
  `refresh_interval_seconds`。
- 默认续期巡检间隔由 6 小时改为 **4 小时**，与其它插件一致。

### 说明

- 积分查询失败时面板显示「—」与错误原因，不会把未知渲染成 0。
- 明细接口失败只影响过期提示，余额照常展示。

## 0.2.0 (2026-10-18)

### 新增

- **模型动态发现**：声明 `model_provider` 能力并实现 `model.static` /
  `model.for_auth`。目录来自两个上游端点——`POST /v1/model/list`（按
  `model_type` 逐类拉取 `llm` / `image_generation` / `video_generation` /
  `tts` / `asr` / `embedding`）与
  `POST /api/coze_claw/resource/get_multimodal_model_catalog`（生视频完整族
  只在这里）。实测单账号 9 个模型：对话 3 / 图像 2 / 视频 1 / 嵌入 1 / 语音 2。
  管理台 `auth-files` 的「支持的模型」弹窗从此不再显示「该凭证暂无可用模型」。
- **面板「测试模型」**：新增 `GET /models`（按账号的模型下拉数据源，失败时
  回落内置清单并在 `notice` 说明原因）与 `POST /test-active`（认证层探活：
  校验登录态 + 模型可见性 + 账号归属）。真实推理链路需 `claw_id` /
  `session_id`，属 CYCLE-01。
- **面板按 workbuddy 风格重做**：账号卡（状态徽章 / 身份字段 / 操作按钮组）、
  顶部筛选标签与计数、账号汇总卡、搜索、全部启用/禁用、删除二次确认、
  右上角滑入通知、跟随宿主主题（含暗色）。
- 账号时间统一按 `Asia/Shanghai` 展示为 `YYYY-MM-DD HH:mm:ss`。

### 变更

- 面板移除「授权扣子账号」引导区：授权统一走宿主「添加账号」弹窗（OAuth
  桥接页），面板只承担账号运维。
- `model.static` 不再在宿主启动期发起网络请求，只返回内置种子目录；
  真实目录由 `model.for_auth` 按账号、按需提供——模型注册在宿主认证加载
  路径中同步执行，启动期打上游会拖慢宿主。
- 模型发现改为并发拉取各类型端点，单请求 8 秒超时；缓存按账号分桶，
  多账号不再互相覆盖。

## 0.1.3 (2026-10-18)

### 修复

- **授权脚本转义的真正根因在宿主侧**：宿主回写 `ManagementResponse.Body`
  时会再做一次带 HTML 转义的 JSON 序列化。实测证据：`/renew` 的错误文案
  把入参 `x<y>z'w` 回显成 `x&lt;y&gt;z&#39;w`——插件侧关闭自己的转义无效。
  改为**以 base64 下发脚本**（base64 不含 HTML 特殊字符），面板端
  `atob` + `TextDecoder` 解码还原；响应带 `encoding: "base64"` 标识。
- 0.1.1 / 0.1.2 两次修复均无效（分别关信封层与 payload 层转义），
  本版是根因修复。

## 0.1.2 (2026-10-18)

### 修复

- **面板授权脚本仍被 HTML 实体化**（0.1.1 修复不完整）：0.1.1 只改了
  `okEnvelope`，但 `handleLoginStartFromPanel` 在更早一层用 `json.Marshal`
  生成 payload，转义发生在那一步，信封层再关转义也无济于事。改为
  `marshalNoHTMLEscape`。生产复验：0.1.1 的 `login-start` 仍含 26 处
  `&#39;` / 2 处 `&gt;` / 0 处 `=>`。
- 补 `TestLoginStartFromPanel_ScriptNotEscaped` 直接打真实入口（此前只有
  信封级用例，覆盖不到这条路径）。

## 0.1.1 (2026-10-18)

### 修复

- **授权脚本被 HTML 实体化**：`okEnvelope` 原先用 `json.Marshal`，它默认开启
  HTML 转义，把脚本里的 `=>`、`'`、`&&` 变成 `=&gt;`、`&#39;`、`&amp;&amp;`，
  面板复制到的脚本无法执行。改用 `json.Encoder` + `SetEscapeHTML(false)`。
  生产实测证据：`login-start` 响应 774 字节中含 26 处 `&#39;`、2 处 `&gt;`、
  0 处 `=>`。

## 0.1.0 (2026-10-18)

CYCLE-00 首发：扣子（Coze）授权登录链路。

### 新增

- **授权登录**：面板生成一次性同源授权脚本，用户在扣子页面执行后把
  `x-tt-token` 交给插件；插件用该令牌调 `passport/token/beat/v2/` 换取会话
  Cookie 并落盘。全程不读浏览器 Cookie 库、不内嵌浏览器、不注册自定义协议。
  令牌经 URL fragment + 同源请求头两段式回传：fragment 不发给服务端，
  因此不会落进宿主的访问日志。
- **登录态判据**：`passport/account/info/v2/` 的 `message=success` 且
  `sec_user_id` 非空双条件判定；判据不通过不落盘。
- **Cookie 持久化**：按 `(name, domain, path)` 三元组存储，保留同名分域
  Cookie（真实端点下发的 13 条中 `uid_tt` / `uid_tt_ss` / `session_tlb_tag`
  各出现两次）。
- **滚动续期**：`passport/token/beat/v2/` 是双向换发端点——带 Cookie 换
  `x-tt-token`，带 token 换新 `Set-Cookie`；插件据此实现
  「Cookie → 新 token → 新 Cookie」循环，无需用户反复授权。
- **账号管理**：列表、删除、启用、禁用、手动续期；面板展示授权时间、
  最近续期时间与登录态状态。
- **宿主集成**：`auth.parse` / `auth.login.start` / `auth.login.poll` /
  `auth.refresh` 全链路实现，宿主「添加账号」弹窗可正常拉起。

### 已知限制

- 只实现认证链路（CYCLE-00）：**未声明 executor / model_provider**，
  对话能力（CYCLE-01）尚未实现。
- 授权需用户在扣子页面手动执行一次脚本（因官方 `redirect_uri` 协议白名单
  只认 `coze-desktop:` 系列，插件无法作为 OAuth 回调接收方）。
- 续期巡检尚未接入后台定时器，当前由宿主 `auth.refresh` 与面板手动触发。

### 实测依据

- 真实账号端到端验证：令牌 355 字符 → 13 条 Set-Cookie → `sec_user_id`
  76 字符 → 续期闭环成立。见
  `doc/5-tests/2026-10-17_235500_Coze授权链路端到端验证.md`。
