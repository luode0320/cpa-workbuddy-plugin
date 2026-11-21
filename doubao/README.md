# doubao-provider

豆包（Doubao）Provider 插件：把豆包网页端账号能力封装为 CPA 的 OpenAI 兼容 Provider。

本插件只做**视频生成**一件事。图片生成已于 0.1.30 按 `CHG-001` 删除；工作模式对话、
技能文本对话、每日签到、额度查询、令牌用量上报都不在范围内（2026-11-17 起）。

## 能力

- 视频生成（`doubao/video` → 上游 skill 5）：异步产物，**两段式**。首轮返回
  参数确认与 `conversation_id`，调用方带上该 id 再发一次才会真正开始生成；成品由
  确认轮之后的 `/im/chain/single` 消息链回灌，不推给本次连接
- 多账号池与阶梯冷却故障转移
- 管理面板：密钥零输入（自动从 CPA 主面板读取，兜底 URL 参数与手工输入）、
  书签一键授权 / 批量导入凭证（粘贴 / 多选 JSON 文件）、导出账号（单文件 JSON / ZIP）、
  账号筛选排序汇总、启停 / 删除 / 单账号能力探活（只判首轮通路是否建立，详见下文
  「面板与密钥」）

## 模型

对外模型名统一带 `doubao/` 前缀，只有一条：

| 模型 | 上游 skill | 说明 |
|---|---|---|
| `doubao/video` | 5 | 视频生成。首轮给参数确认，带 `conversation_id` 再发一次才出片 |

`doubao/image` 已删除：请求它会得到 400 `model_not_found`（`unknown provider for model doubao/image`，由宿主目录先行拦截）。未知名一律报错，不再回落到
任何默认模型：静默回落会让调用方以为在测生视频，实际打到了别的东西上。豆包上游
另有一套内部代号（`4tbp`、`anceba` 等），插件没有代号到模型的映射表，因此代号只
作为排障信息附带。

## 视频参数

`doubao/video` 支持三个可选平铺字段，与续接点同层：

| 字段 | 类型 | 说明 |
|---|---|---|
| `video_duration` | 整数 | 目标时长（秒），合法区间 1~60，缺省 10 |
| `video_resolution` | 字符串 | 目标清晰度，缺省 `720p` |
| `video_ratio` | 字符串 | 画面比例，缺省 `auto` |

三个字段只在视频模式下生效。传了非缺省值时，首轮提示词会追加
一段「视频参数要求：时长 N 秒、清晰度 X、画面比例 Y」，确认轮 `chat_ability` 的
`ability_param` 用同一套值提交 —— 两处取自同一个 `videoParams`，不会出现「上游
对用户承诺 5 秒、实际提交 10 秒」的脱钩（0.1.27 前的缺陷）。不传时与旧版逐字节一致。

越界不报错，按缺省处理：`video_duration` 超出 1~60、清晰度或比例为空白串时回落默认档，
避免一个笔误让整次生成失败。

响应里视频条目的 `media[].duration` 是**产物真实时长**（上游给的浮点秒，如 `10.08`），
与请求的目标时长不是一回事：上游可能取整或改写。

## 接入方式

插件挂在 CPA 宿主上对外提供 OpenAI 兼容接口，调用方只需要 baseURL 与 API Key 两样东西。

| 项 | 值 |
|---|---|
| Base URL | `https://cpa.luode.vip/v1` |
| API Key | CPA 宿主 `config.yaml` 的 `access.api-keys` 列表里的任一条，以 `Authorization: Bearer <key>` 发送 |
| 端点 | `POST /v1/chat/completions`，请求体与 OpenAI 完全一致 |

`GET /v1/models` 要同一个 Key：不带或带错都返回 HTTP 401。目录里 `owned_by=doubao-provider`
的条目中只有 `doubao/video` 是可用的能力别名，其余是上游内部代号原样透传，不要拿它们发请求。

### 两段式调用

视频必须发两次：第一次只拿续接点，第二次才真的出片。

1. **首轮**只发提示词。上游回一段参数确认文本（这一轮不产生产物），响应**顶层**带
   `conversation_id` / `section_id` / `message_index` 三个续接点。
2. **确认轮**把这三个值原样带回，`content` 填「确认，开始生成」。这一次才开始生成并
   阻塞等待成品，成功时响应**顶层** `media` 数组给出视频直链。

三个续接点与三个视频参数（`video_duration` / `video_resolution` / `video_ratio`）都是
**请求体顶层平铺字段**，不在 `metadata` 里。官方 openai SDK 用 `extra_body` 传即可：SDK
会把它们透传到顶层，响应侧的续接点也能从 `model_dump()` 顶层读回。

**不带续接点的第二次请求不会报错，但会另起一个全新会话**，上游把「确认，开始生成」当成
新需求，自己编一套参数重来一遍，永远拿不到上一轮那个成品。这是最容易踩的坑。

### 首轮就拒答：先确认账号当前还能不能出片

正常首轮回的是「参数确认」（模型 / 时长 / 比例）。如果首轮回的是下面这类文本，说明上游
模型这一次根本没有调用视频技能，**确认轮一定拿不到成品**，不要去发第二次：

- 「很抱歉，我当前没办法直接生成并输出视频文件」
- 「当前这个对话窗口没有视频渲染执行入口，我无法在这里直接提交生成」
- 「我目前不能直接生成5秒…短视频文件，你可以把提示词拿到支持文生视频的工具里生成」

这类回复**照样带 `conversation_id`**，形态上与正常首轮无法区分；照发确认轮会白等满 6 分钟
（默认 6 分钟，配置项 `video_poll_timeout_seconds`）再返回「等待视频超时」。当前插件不解析首轮正文，只能由
调用方自己看：首轮正文里没出现你要的时长/比例，就不要发确认轮。

面板的「测试模型」对 `doubao/video` 返回 `ok:true` 只代表**会话建起来了**（首轮 ACK），
不保证真能出片，两者不要混为一谈。

### curl

```bash
# 首轮：只拿续接点，不产生产物
curl -s https://cpa.luode.vip/v1/chat/completions \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao/video",
    "messages": [{"role":"user","content":"生成一段 5 秒的海浪拍打礁石短视频"}],
    "stream": false,
    "video_duration": 5, "video_resolution": "720p", "video_ratio": "16:9"
  }'

# 确认轮：把上一轮的三个续接点带回来，这一步会阻塞等成品
curl -s https://cpa.luode.vip/v1/chat/completions \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao/video",
    "messages": [{"role":"user","content":"确认，开始生成"}],
    "stream": false,
    "conversation_id": "<首轮的 conversation_id>",
    "section_id": "<首轮的 section_id>",
    "last_message_index": <首轮的 message_index>,
    "video_duration": 5, "video_resolution": "720p", "video_ratio": "16:9"
  }'
```

确认轮成功时响应顶层 `media` 形如：

```json
{"media": [{"type": "video", "url": "https://v26-vdl.doubao.com/...", "duration": 5.057, "width": 384, "height": 216}]}
```

`duration` 是产物真实时长，`width` / `height` 是上游给的**预览尺度**，不是成品分辨率
（成品实测 1280×720）；需要真实分辨率请下载后用 `ffprobe` 读。没有成品直链的占位块不会
出现在 `media` 里 —— 插件只把带 URL 的成品交出去，封面图不再冒充视频地址。

### Python（官方 openai SDK）

```python
from openai import OpenAI

client = OpenAI(base_url="https://cpa.luode.vip/v1", api_key=os.environ["CPA_API_KEY"])
PARAMS = {"video_duration": 5, "video_resolution": "720p", "video_ratio": "16:9"}

r1 = client.chat.completions.create(
    model="doubao/video",
    messages=[{"role": "user", "content": "生成一段 5 秒的海浪拍打礁石短视频"}],
    extra_body=PARAMS,
)
d1 = r1.model_dump()

r2 = client.chat.completions.create(
    model="doubao/video",
    messages=[{"role": "user", "content": "确认，开始生成"}],
    extra_body={
        **PARAMS,
        "conversation_id": d1["conversation_id"],
        "section_id": d1["section_id"],
        "last_message_index": d1["message_index"],
    },
)
media = r2.model_dump().get("media") or []
print(media[0]["url"])
```

确认轮的 `choices[0].message.content` 是 **Object[]**（多段 text），不是字符串；首轮才是
字符串。要按类型分别处理，否则会拿到 `TypeError`。

### 流式

`stream: true` 时续接点**只在终止帧**：前面所有 `data:` 帧的 `conversation_id` 都是空串，
只有 `finish_reason` 为 `stop` 的那一帧带完整三个值，之后跟一个 `data: [DONE]`。客户端
必须在终止帧读，读早了拿到的是空字符串。

## 多账号与会话粘性

视频是两段式的，上游只认**建立会话的那个账号**。插件在首轮成功后把
`conversation_id → auth_id` 记进内存表（TTL 30 分钟），后续带续接点的请求会被
钉回同一个账号，不受宿主选号轮转影响。

由此带来两条行为：

- **续接请求不换号**。确认轮失败时直接返回错误，不会拿别的账号重试 —— 换号后
  `conversation_id` 在新账号上不存在，重试注定失败，还会把那笔额度记到健康账号头上。
- **绑定账号消失即报错**。绑定账号被删除或停用后，续接请求返回「会话 … 绑定的账号
  … 已不存在或已停用：请重新发起首轮，不要续接旧会话」，不会悄悄换号。

插件热重载会清空这张表。热重载后仍拿旧 `conversation_id` 续接时，请求退回「沿用
当前账号」的行为（不报错），但多数情况下拿不到成品，重新发起首轮更省时间。

## 已知限制

- 上游不返回 token 用量，响应里的 `usage` 是按字符估算（`estimated: true`）
- 豆包客户端协议不支持 Function Calling，`tools` 参数会被忽略
- 视频生成要等 1~5 分钟，产物地址有 `url_expire`，过期后需要重新生成
- 视频模型代号固定为 `seedance_v2.0_mini`，插件不让调用方指定（换模型属能力变更，
  不是参数）
- 视频「确认」轮依赖会话续接（`conversation_id` + `last_section_id` +
  `last_message_index`），丢掉 id 就只能重新从参数确认开始
- 生视频消耗账号额度；账号未投放视频能力时上游会回明确错误码
- 确认轮等待分两段，两个时长都是**视频专属配置项**，与对话超时互不影响：
  `video_poll_timeout_seconds`（默认 360）是等产物的总时长，`video_creation_wait_seconds`
  （默认 120）是等上游落下第一个创作块的上限。后者是「上游拒答也会返回续接点」的补救：
  没开始渲染时会在 120 秒内报「上游没有开始渲染」，而不是干等满 6 分钟。
  上游出片时长波动很大：2026-11-20 实测同一账号 02:24 那次 130 秒就出片，02:33 起连续 4 次
  等到 6 分钟仍未产出、回「等待视频超时（6m0s）」。超时不代表请求写错，可用同一个
  `conversation_id` 再发一次确认轮
- 上游对视频有最低 4 秒时长限制：传更短的值不会报错，上游会自行抬到 4 秒并在确认文本里说明
- 面板「测试」只能判到「首轮会话建起来了」：首轮本来就不产生产物，能否真出片
  要带续接点再发确认轮才知道。上游还会先建会话再回一段拒答文本，这两种态在流里无法
  区分，所以成功文案写的是「首轮通路正常」而不是「活跃成功」
- `msToken` 靠业务请求的响应头自动续期（见下方「风控令牌续期」）。它**没有固定有效期**：
  上游只做形态/长度粗判。真正的失效表现是 `710022002`，此时需要用户重新点一次授权书签
- 续期发生在业务请求之后，因此「token 已经失效到发不出任何请求」这一态仍需人工介入；
  插件无法凭空生成一个上游认可的 token（`mssdk.bytedance.com` 的换 token 端点要设备侧
  加密串 `strData`，算不出来）

## 风控令牌续期

`/chat/completion` 除了 Cookie 还要求两个风控参数：`msToken`（长效）与 `a_bogus`
（每次现算）。`a_bogus` 由内嵌的官方 `bdms-sdk` 在 goja 里现算，插件不必操心；
`msToken` 的续期链路是：

1. 首次由授权书签从页面 `localStorage.xmst` 取回，随授权一起落盘；
2. 之后**每次业务请求**都会从上游响应头拿到一个新的 `x-ms-token`，插件立即写回账号文件。

因此只要账号还在正常出字，`msToken` 就会一直跟着刷新，不需要用户再点书签。
上游对 token 只做形态/长度粗判（实测：全随机值回 `710022002`，空值、改一个字符重复粘贴
都放行，截断一半才判伪造），所以插件落盘前只做字符集校验，不写死长度或有效期。

### 空闲期保活

上面那条链路有个前提：得有业务请求。整段时间没有调用时，响应头就不会来，`msToken`
也就停在原地（实测空闲 4 小时 43 分，账号文件里的 token 一个字节没变）。为此插件内置了
一个保活定时器：

- 每隔 `keepalive_interval_seconds`（默认 1800 秒，即 30 分钟）向
  `/samantha/user/preference/get` 发一次只读偏好查询，从响应头接管新的 `x-ms-token` 并写回账号文件；
- 选这个端点的原因：它不需要 `a_bogus` 签名、不消耗对话额度、不改账号状态，每次响应都带 token；
- 默认开启，想完全静默的实例写 `keepalive_enabled: false` 关掉；
- 首轮不立刻跑，等第一个 tick；热重载会重建定时器，但不会立刻对上游发请求；
- 保活失败只记日志，不进 failover 冷却账本——它不属于任何一次请求的失败；
- 改配置立刻生效：保活协程每 30 秒自己读一次宿主的 `config.yaml`，发现
  `keepalive_enabled` / `keepalive_interval_seconds` 变了就地重建定时器（宿主改配置不会回调插件，
  见下方「配置生效时机」）。

保活只覆盖「空闲期也能换 token」。如果 token 已经失效到连一次请求都发不出去，
仍然需要用户重新点一次授权书签。

## 配置生效时机

宿主的 `PUT /v0/management/plugins/doubao-provider/config` **不会**让插件重新解析配置：写入后宿主
走的是 `configReloadHook` 分支，而生产注入的那个 hook 只触发 watcher 重载，不会调
`pluginHost.ApplyConfig`，插件收不到 `MethodPluginReconfigure`。因此：

- `keepalive_enabled` / `keepalive_interval_seconds`：**立刻生效**。插件保活协程每 30 秒巡检一次
  宿主配置文件并自行调整，不需要任何额外动作。
- 其余五项（`timeout_seconds` / `stream_timeout_seconds` / `model_cache_seconds` /
  `video_poll_timeout_seconds` / `video_creation_wait_seconds`）：改完要重装插件（
  `plugin-store install`，会触发一次真实加载）才会生效。

**注意一条单向性**：把 `keepalive_enabled` 改成 `false` 之后，保活协程会自行退出，而退出是终态——
再改回 `true` **不会**让它重新起来（协程只在插件被重新加载时创建）。只想临时停一会儿的话，
把 `keepalive_interval_seconds` 改大（比如 86400）比关掉更安全；确实关过又想恢复，重装一次插件即可。

## 认证方式

**复用用户浏览器登录态**：用户在自有浏览器登录豆包后，由授权书签把一次性令牌交给插件，
插件换回完整会话 Cookie 并落盘。插件**不实现登录流程**（不扫码、不手机号、不内嵌浏览器），
**也不读浏览器 Cookie 库**——豆包的 `sessionid` / `sid_guard` 是 HttpOnly，页面脚本读不到。

一键授权步骤：

1. 打开插件面板 → 点「豆包一键授权」，会打开授权引导页
2. 把页面上的「豆包一键授权」按钮**拖到浏览器书签栏**（只做一次，别直接点击）
3. 点引导页的「打开豆包并登录」，在豆包完成登录
4. 回到豆包标签页，点一下书签栏里的「豆包一键授权」，页面自动跳回并完成授权

书签只做一件事：在豆包页面内调令牌端点取 `x-tt-token`，不读取也不上传 Cookie，
内容不含任何密钥，可以长期保留。引导页另给两条兜底：一次性脚本（浏览器策略拦掉
`javascript:` 书签时用）与手动粘贴 Cookie。

批量导入：面板「导入凭证」支持粘贴单份 Cookie、粘贴多份凭证，或一次性选中多个
`.json` 文件（单对象 / 数组 / 导出汇总文件三种形态都可直接回灌）

面板的账号管理支持：筛选（全部 / 可用 / 已禁用 / 过期）、排序（创建时间 /
账号标识 / 备注名）、单账号模型探活、启用停用、删除、导出（单文件 JSON 或
每账号一个 JSON 的 ZIP）。导出的文件可直接被同一面板的导入功能回灌。

## 面板与密钥

面板嵌在 CPA 主面板内打开时会自动从宿主 `cli-proxy-auth` 读取管理密钥，
无需手工填写；独立打开（或父页上下文不可用）时按 `?key=xxx` 参数兜底，
两者都拿不到才显示手工输入框。密钥只写入当前标签页的 `sessionStorage`，
不落盘、不写入 URL 历史。

## 风险提示

- 本插件走豆包网页端协议（非火山方舟开放平台 API），协议可能随上游版本变化而失效
- 上游对请求频率与账号行为有风控策略，请合理控制调用频率
- 账号订阅状态与活动投放可能影响生视频能力

## 配置项

| 键 | 类型 | 说明 |
|---|---|---|
| `timeout_seconds` | 整数 | 非流式上游请求超时（秒），默认 120 |
| `stream_timeout_seconds` | 整数 | 流式上游请求超时（秒），默认 600 |
| `model_cache_seconds` | 整数 | 模型目录缓存时长（秒），默认 300 |
| `video_poll_timeout_seconds` | 整数 | 等视频产物的总时长（秒），默认 360；只作用于视频回灌 |
| `video_creation_wait_seconds` | 整数 | 等上游落下第一个创作块的上限（秒），默认 120；只作用于视频回灌 |
| `keepalive_enabled` | 布尔 | 是否在空闲期定时刷新 `msToken`，默认 `true` |
| `keepalive_interval_seconds` | 整数 | 保活间隔（秒），默认 1800；只在 `keepalive_enabled` 为真时生效 |

七项都在插件注册元数据里声明过（`ConfigFields`），所以管理端能列出名称、类型与描述。
**但当前没有可视化编辑表单**：改值仍需 `PUT /v0/management/plugins/doubao-provider/config`
的 `config_yaml`，例如：

```bash
curl -X PUT https://cpa.luode.vip/v0/management/plugins/doubao-provider/config \
  -H "Authorization: Bearer YOUR_KEY" -H "Content-Type: application/json" \
  -d '{"enabled":true,"priority":20,"config_yaml":"keepalive_interval_seconds: 900\\n"}'
```

`config_yaml` 写空串表示全部回到默认值（保活开启、间隔 1800 秒）。

早期版本的 `work_*` 七项配置已删除；存量 `config.yaml` 里留着这批键不会报错，
yaml 会宽松忽略。

## 构建与验证

本插件为 c-shared 动态库（`import "C"`）。本地无 CGO 环境时用 shim 验证：

```bash
python scripts/cgo-shim-build.py doubao
```
