# coze-provider

CLIProxyAPI（CPA）插件：把扣子（Coze）账号接入 CPA，提供**授权登录**、
**会话续期**与**对话转发**能力。

> 当前版本 0.3.0：CYCLE-00（认证）与 CYCLE-01（对话链路）已打通。
> 插件声明 `model_provider` + `executor`，对话走 WebSocket 真流式。

## 对话链路

```text
OpenAI 请求
  → claw/info({})            → claw_id
  → session/get_main         → 固定主会话 session_id
  → get_frontier_key         → access_key + device_id
  → 建立 frontier WebSocket  （必须先于下一步，否则丢首帧）
  → message/chat             → 用户消息 message_id（异步，无回复内容）
  → WS 收 reply 帧
       event_delta → 增量 → chat.completion.chunk
       event_final → 收尾
```

### 关键约束（实现必读）

- **WS 连接参数**：`wss://frontier.coze.cn/ws/v2`，`aid=751148`、`fpid=1289`、
  `xsack=0`、子协议 `pbbp2`。
  ⚠️ `/api/fullstack_chux/v1/frontier/connection-params` 返回的
  `aid=578287` / `message_service_id=33555616` 是**另一条通道**，
  用错只能收到 `hi` 心跳、收不到任何业务帧。
- **业务 service 编号是 `33555657`**，不是 `33555616`。
- **模型不由请求决定**：`message/chat` 不接受模型参数，实际模型由扣子侧
  配置（页面右下角选择器）控制。请求里的 `model` 只用于响应回显。
- **多轮上下文有损**：claw 只接受单条文本，OpenAI 的 system / 历史轮次会被
  折叠成「角色: 内容」逐行拼接，角色边界不作为独立字段传给上游。
- **增量必须逐帧下发**：上游 `content.message_data.reply.content` 是 delta，
  可累加；合并多帧下发会让客户端「卡一下然后蹦出一大段」。
- **去重键是 `turn_id + seq_num + phase`**：同一轮的 `event_start` 与首个
  `event_delta` 共享同一个 `id`，只按 id 去重会丢掉真正的首个增量。
- **收尾判据是 `phase == event_final`**：没有 `is_finish` 字段。

## 为什么不是一键授权

扣子没有开放给第三方的一键授权入口（2026-10-17 桌面端 1.1.45 源码取证）：

| 入口 | 阻断原因 |
|---|---|
| `/sign/oauth` | `redirect_uri` 硬编码为 `www.coze.cn/auth/callback`，站外地址被丢弃 |
| `/desktop-login` 的 `redirect` | 登录令牌只流向 `redirect_uri`，`redirect` 不承载令牌 |
| `/desktop-login` 的 `redirect_uri` | 协议白名单硬编码 `coze-desktop:` / `-beta:` / `-dev:` |

插件因此采用「同源授权」：用户在扣子网页完成官方登录后，由**一段在扣子页面
同源执行的脚本**取 `x-tt-token`，再由插件换取会话。**不读浏览器 Cookie 库、
不内嵌浏览器、不注册自定义协议。**

这段脚本有两种形态，二者共用同一个回调页：

| 形态 | 是否带 state | 有效期 | 适用入口 |
|---|---|---|---|
| **固定书签** | 不带 | 长期可用 | 面板「登录扣子」（推荐） |
| 一次性脚本 | 带 state | 15 分钟 | 宿主「添加账号」弹窗、书签被 CSP 拦截时 |

固定书签是 0.7.2 新增：因为内容不含 state，拖进书签栏后**永久可用**，
之后任何一次扣子登录完成，点一下书签即可完成授权（或把新登录态覆盖回旧账号）。

## 授权流程

```text
方式 A：固定书签（面板「登录扣子」）
  → 面板新开 /v0/resource/plugins/coze-provider/login（无 state）
  → 用户把页面上的按钮拖到书签栏（只做一次）
  → 点「打开扣子并登录」完成官方登录
  → 在扣子页面点一下该书签
  → 书签在扣子同源取 x-tt-token，带 fragment 跳回插件回调页
  → 插件换 Cookie + 校验 sec_user_id + 落盘（不通知宿主弹窗，回面板刷新可见）

方式 B：一次性脚本（宿主「添加账号」弹窗）
  → 插件签发 state（15 分钟有效）并返回可复制脚本
  → 用户在 https://www.coze.cn 完成官方登录
  → 在 Console 粘贴脚本执行
  → 脚本在扣子同源上下文取 x-tt-token，跳转插件回调页（带 state）
  → 插件换 Cookie + 校验 + 落盘 + 登记会话，宿主弹窗自动关闭

公共末段：POST /passport/token/beat/v2/（x-tt-token）→ 13 条 Set-Cookie
        → GET /passport/account/info/v2/ → sec_user_id 非空 → 落盘
```

两种形态的令牌都走 URL fragment + 同源请求头提交，不落访问日志。

## 续期

`passport/token/beat/v2/` 是**双向**换发端点：

| 调用方式 | 响应 |
|---|---|
| 带会话 Cookie | `x-tt-token` 响应头（Set-Cookie 0 条） |
| 带 `x-tt-token` | `Set-Cookie`（13 条） |

因此会话可无限滚动续期，不存在 Cookie 快照硬失效。续期有两个触发源：

- **插件内定时器**：默认每 4 小时（`renew_interval_seconds`）遍历所有账号
  （**含已禁用账号**）执行一轮续期并落盘，插件热重载后立即先跑一轮；
- **面板「续期」按钮**：对单个账号立即执行一轮。

禁用账号同样续期：被低额机制禁用的账号要靠「次日签到」恢复，而签到必须
用有效 Cookie；禁用一个号就停止续期会让它在等待期内失效、永久无法自愈。
禁用号续期不产生流量——它们不参与路由，只是维持凭据可用。

宿主 `auth.refresh` 也会走同一套续期内核，但扣子不声明 scheduler 能力，
宿主自动刷新循环对本插件只重排不执行（`handleDueAuth` 的 `exec == nil`
分支），因此定时续期由插件自己负责。

### 换发判据：看会话主项，不看下发条数

`renewSession` 判定「这一轮到底换发成功没有」的唯一依据是**会话主 Cookie
（`sessionid` / `sessionid_ss` / `sid_tt` / `sid_guard`）的值是否变化**，
下发条数不算数。

原因（2026-11-21 生产复盘）：死会话打 `token/beat` 依然会下发 9 条埋点
Cookie（`ttwid` / `s_v_web_id` / `biz_trace_id` / `Hm_lvt_*` 等），一条
会话主项都没有。旧实现只判 `len(Set-Cookie) > 0`，会把这种「有下发、无换发」
记成成功并刷新 `last_renew_at` —— 时间戳一路前进，会话其实早已失效，面板
看不出任何异常。

另外，新 Cookie **叠加**在账号既有 Cookie 之上，不整份替换：扣子把凭据拆在
多个同义名上（`uid_tt` / `uid_tt_ss` / `session_tlb_tag` 各出现两次），
整份替换会丢掉没被本轮重新下发的那些项。

### 两级换发：裸 HTTP 失败后降级到真实浏览器

同一份 Cookie，Go 进程直连 `token/beat/v2/` 会被判 `error_code:401` /
`session_expired`，而**真实页面内同源 fetch 同一端点却能换发**——差别不在
Cookie，在客户端特征（TLS 指纹 / UA / 埋点 Cookie / referer / 浏览器侧前置
会话状态）。因此续期做成两级：

1. **裸 HTTP**（默认路径）：`renewSession`，无进程开销；
2. **真实浏览器**（`browser_keep_enabled` 开启时）：CDP 起一个独立 profile
   的浏览器，注入账号 Cookie，打开 `www.coze.cn`，在页面上下文内执行同源
   fetch 完成换发，再从浏览器 Cookie 罐读回结果。

降级触发条件有三条，缺一不可：

- 裸 HTTP 报错且错误文案命中会话过期（网络抖动 / 限流 / 5xx **不**降级，
  起浏览器只会白跑一次 90 秒超时）；
- 裸 HTTP 没报错但没换到会话主项（埋点 Cookie 不算换发）；
- 浏览器保活开关已开启。

两级都失败时返回的是**包装错误**，裸 HTTP 的原始原因在前（`isSessionExpiredError`
仍能命中），浏览器侧原因在后。浏览器保活**救不回已被上游吊销的会话**：服务端
换锁之后任何客户端特征都换不出新 Cookie，只能重新授权。它的价值是把**活号**
的寿命从「几天」拉到「数月」——桌面端闲置约一个月即被吊销，持续保活则不会。

### 失败退避：4h → 8h → 24h → 48h

连续失败（含「未换发」）的账号进入阶梯退避，封顶 48 小时：即使账号长期失效，
每天仍有一次自愈机会。成功一次即清零。

退避前实测：12 个账号里 5 个已失效，8 天累计约 240 次必然失败的上游请求，
全是白耗配额与风控额度，还把真正的告警淹掉。退避表只放内存（热重载清零后
以 4 小时重新起步），是节流手段不是业务事实。

### 日志与告警

每轮结果分三档记录，不再混写：真换发打 info「定时续期完成」；上游报错打
warn「定时续期失败」；上游正常响应但没换到会话主项打 warn「定时续期未换发」。
旧实现两种走向都打「定时续期完成」，会话死了很久也看不出来。

`renew_alert_webhook` 配置后，账号 `auth_invalid` 的 false→true（失效）与
true→false（恢复）各推一条 JSON，同一账号连续判定只推第一条；正文里手机号
中间四位遮蔽，不携带 Cookie 原值。

## 积分巡检与低额自动禁用

插件每小时跑一轮全量巡检（`credit_refresh_interval_seconds`，默认 3600 秒），
一轮里按固定顺序完成四件事：

1. 领取当日登录积分（`credit/balance/check`，幂等）；
2. 查询余额与即将过期明细；
3. 写内存缓存 + 落盘快照（`<CLIProxyAPI root>/data/coze-credits.json`）；
4. 分级：剩余积分低于红线 → 写盘禁用；积分回升 + 本机制标记 → 自动解禁。

**顺序不可调换**：签到必须先于余额查询。反了会让刚领到当日签到积分的账号被
当日旧余额判成 0 分而误禁用一小时。

**积分红线默认 10 分**（2026-11-08 由 200 下调）：扣子每日签到额度已改为 60 分，
红线必须显著低于单日额度，否则「刚领完签到、正常消耗」的号会被整批误禁。

**禁用与恢复的归属规则**：

- 只有带 `low_credit_disable` 标记的账号才会被自动解禁；
- 用户手动禁用/启用的账号永不被巡检改动，且人工操作会清掉该标记
  （否则签到到账会违背用户意愿自动解禁）；
- 判定在账号文件锁内重读磁盘完成，扫描期间的人工操作不会被覆盖。

**恢复判据是纯积分**（余额查询成功且 `remain >= 红线`），不做推理探活：
扣子查余额本身就要有效 Cookie，已隐含登录态有效；推理探活会真实消耗积分，
每小时对被禁号烧一次与被禁目的矛盾。

**面板打开零上游**：`/accounts` 只读落盘快照，不再逐号并发打上游。
需要最新数字时用单卡「刷新积分」（`force=1`，会同步写回快照并重跑分级）
或「立即巡检」（`POST /refresh-all`）。

**全池耗尽快速失败的采信窗口为 90 分钟**：必须显著宽于巡检周期，
否则每轮中后段缓存会被判成「不可信」，快速失败静默失效、请求回去白打上游。

## 配置

| 键 | 默认 | 说明 |
|---|---|---|
| `timeout_seconds` | 30 | 上游请求超时（秒） |
| `renew_interval_seconds` | 14400 | 定时续期间隔（秒），默认 4 小时 |
| `credit_refresh_interval_seconds` | 3600 | 积分巡检间隔（秒），承担签到、落盘与分级 |
| `min_routable_credits` | 10 | 积分红线：低于此值不路由且自动禁用；填 `0` 关闭整个机制 |
| `renew_alert_webhook` | 空 | 账号失效/恢复的通知地址；空则只写日志 |
| `browser_keep_enabled` | false | 裸 HTTP 换发失败时是否降级到真实浏览器 |
| `browser_keep_chrome_path` | 空 | 浏览器可执行文件路径；空则按 `COZE_CHROME_PATH` / `CHROME_PATH` / `BROWSER_PATH` 与常见安装位置依次探测 |
| `browser_keep_headless` | true | 保活浏览器是否无头；遇到滑动验证须置 `false` 转可见窗口 |
| `browser_keep_profile_dir` | 空 | 浏览器 profile 根目录；空则用 `~/.antigravity_cockpit/coze_browser_profiles`，每个账号一个子目录 |

浏览器保活**默认关闭**：它要起真实浏览器进程（单实例约 200MB 内存加磁盘 profile），
多数精简 Linux 容器根本没装 Chrome。开局前先用面板「浏览器保活」按钮或
`GET /browser-status` 确认 `available` 为真——报的是可执行文件探测结果，不是开关状态。

## 管理接口

前缀 `/v0/management/plugins/coze-provider`：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/accounts` | 账号列表（读落盘快照，零上游请求） |
| POST | `/refresh-all` | 立即触发一轮全量巡检（异步） |
| GET | `/credits` | 单账号积分（`force=1` 跳过缓存并落盘） |
| POST | `/claim-daily` | 手动补领当日登录积分 |
| POST | `/login-start` | 生成一次性授权会话与脚本 |
| POST | `/renew` | 手动续期（裸 HTTP，不触发浏览器降级） |
| POST | `/browser-revive` | 用真实浏览器为指定账号换发一次（`body: {auth_id}`），裸 HTTP 被判失效时用 |
| GET | `/browser-status` | 浏览器保活开关、浏览器可执行文件探测结果与正在保活的账号 |
| POST | `/delete` / `/enable` / `/disable` | 账号管理 |
| GET | `/export` | 导出全部账号凭据为单个 JSON 备份（含 Cookie 原文） |
| POST | `/import-cred` | 从备份恢复单条账号（`body: {cred}`） |

免密钥资源页：`/v0/resource/plugins/coze-provider/panel`、`/login`、
`/login/callback`。

- `GET /login`：**不带 state** 为面板一键授权页（固定书签）；带 state 为宿主弹窗引导页。
- `GET /login/callback`：带 state 时回传宿主弹窗；不带 state 时为面板宽松导入（账号身份由令牌自身决定）。

## 本地验证

```bash
python scripts/cgo-shim-build.py coze
```

## 协议依据

- 对话链路协议（含真实帧 schema）：`doc/5-tests/2026-10-18_164500_Coze对话链路协议补全.md`
- 端到端实测：`doc/5-tests/2026-10-17_235500_Coze授权链路端到端验证.md`
- 授权入口裁定：`doc/5-tests/2026-10-17_234500_CozeDesktopLogin授权入口复验.md`
- 实施总览：`doc/3-实施/2026-10-17_230000_CozeProvider插件立项_实施总览.md`
