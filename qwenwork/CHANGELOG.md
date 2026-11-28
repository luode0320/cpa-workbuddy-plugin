# QwenWork Plugin Changelog

## 0.2.29 (2026-11-28)

### Feat - 把「上游在首字前到底做了什么」变成可见事实

- **动机**: 首字延迟排查卡在死结上——插件侧 TTFT 与客户端首帧时间逐条一致（只差 ~0.2s），但**看不出上游在首帧之前做了什么**。生产统计进一步确认：同会话 TTFT 序列的 lag-1 自相关 **-0.018**（无会话粘性/实例亲和 ⇒ 0.2.25 的亲和头假设被证伪），而**相邻慢请求间隔中位 46s、83% ≤120s** ⇒ 上游是**潮汐式排队**。要继续推进，只能先把上游的行为看清楚。
- **两个现成的观测缺口（本轮补上）**：
  1. `hostHTTPDoStream` **本就返回上游响应头**，但 pump 里写的是 `stream, statusCode, _, err := …` ⇒ 响应头被直接丢弃；
  2. SSE 扫描器对不认识的行直接 `continue` ⇒ 上游若在首个内容帧之前发出**排队/路由事件**（社区记载上游存在带 `request_set_id` 的排队事件），我们一直在忽略它。
- **修复**: 新增 `sse_signature.go`——① `upstreamHeaderSummary` 摘出上游响应头白名单（`x-*` / `ratelimit-*` / `retry-after` / `content-type` / `server` / `via`，取值截断 80 字符）；② `sseFrameSignature` / `sseLineSignature` 产出帧的**结构签名**（顶层键名 + `event`/`type`/`status`/`object`/`index`/`error.code` 等枚举型标识）；③ collector 记录首帧前最多 3 帧签名；④ 慢请求日志新增 `upstream=[…]` 字段。
- **隐私红线**: 签名只含**键名**与**枚举型标识**，消息内容、增量文本、token 一律不落日志；响应头只取白名单，凭据类头（authorization / cookie / set-cookie）显式排除。两条都有单测锁定。
- **影响范围**: 仅观测（日志与内存缓冲），不改请求构造、不改路由、不改响应。
- **验证**: `cgo-shim-build.py qwenwork` build/vet/test 全绿；新增 `sse_signature_test.go`（签名含键名与标识 / **内容不泄漏** / 各类 SSE 行形态 / 响应头白名单且无凭据 / prelude 帧数上限）；哨兵反证新增 S26（prelude 不限帧数）、S27（响应头白名单放宽到凭据头）两条。

## 0.2.28 (2026-11-28)

### Feat - 采集「前缀缓存创建量」（区分缓存复用与缓存整体重建）

- **动机**: 只看「缓存命中数」无法区分两种完全不同的状态——**缓存被复用**（首字只付新后缀的 prefill）与**缓存被整体重建**（每次都重付全量 prefill，正是 45~71 万 token 场景下首字 >10s 的机制）。上游在 Anthropic 协议形态下把该数放在 `prompt_tokens_details.cache_creation_input_tokens`（官方模型目录亦记载 `cache_creation_input_tokens` 与 `cache_type: "ephemeral"`）。
- **缺陷**: `usage.Detail.CacheCreationTokens` 一直写进用量 feed，但 `usageDetailFromMap` **从不解析它** ⇒ 该字段**恒为 0**，监控侧完全看不到「缓存是否被重建」。
- **修复**: `usageDetailFromMap` 解析 `prompt_tokens_details.cache_creation_input_tokens` 与顶层 `cache_creation_input_tokens`。
- **影响范围**: 仅用量采集口径；不改请求构造、不改路由。
- **验证**: `cgo-shim-build.py qwenwork` build/vet/test 全绿；新增一条断言（命中数与创建量同时解析）。

## 0.2.27 (2026-11-28)

### Fix - 兜底 MaaS 入口对齐官方客户端 + 补齐设备 UA 头

- **缺陷一（真实 bug）**: `maasDefaultBaseURL` 写的是 `https://maas.xiaoluozi.cn`，而官方桌面客户端的 `DEFAULT_MAAS_BASE_URL` 是 **`https://api9.qwenwork.cn`**（逆向证据：`D:\qwork\QwenWorkCN\2.2.1-26112604\resources\app.asar` → `out/main/main.js`）。两者属**不同 route family**（官方常量表里 `maas.xiaoluozi.cn` 属 legacy/qwenwork 族），社区早已记载该兜底值**实测 401**。⇒ 签发响应一旦缺 `base_url`，本插件必然 401，而官方客户端不会。
- **缺陷二**: 官方出站头白名单（`ALLOWED_HEADERS`）含 `x-mm-device-ua`，主链路也会发；本插件此前不发。补 `x-mm-device-ua`（如实标注自身与运行平台，不冒充官方桌面客户端 UA）。
- **影响范围**: 仅出站请求头与兜底常量；不改变正常路径（签发响应带 `base_url` 时仍以响应为准）。
- **验证**: `cgo-shim-build.py qwenwork` build/vet/test 全绿；新增两条断言（兜底入口与官方一致且不得使用实测 401 的 legacy 入口；`x-mm-device-ua` / `x-mm-device-type` 必须发出）。

### 已知未解（本轮排查结论，登记）

用户口径「输入 45~71 万 token 时首字 30s / 160s 不应该出现，正常都在 10s 以下」。本轮把插件侧能控的差异全部核对完，**剩余部分未能在插件侧定位**，已排除：
- 插件自身对首字的加工：插件侧 TTFT 与客户端首帧时间逐条一致（7.8s vs 8.0s）⇒ 插件只加约 0.2s；
- 上游重试放大：91 次上游尝试 / 82 次客户端请求 = 1.11×；
- 上下文规模 / 账号 / 会话 / 前缀缓存命中四个维度与首字均无相关性；
- 上游入口集群：生产实际外连 163.181.78.145，与官方默认入口同网段 ⇒ **同一组 MaaS 入口**；
- 账号并发超发（traework 那套槽位排队机制）：本插件三个窗口的**单账号在飞并发恒为 1**，机制不成立。

**剩余最可能的差异（需用户决策是否投入）**：官方客户端按模型目录对 `minimax-m3.1-flash` / `qwen4-max` 走 **`anthropic/messages`** 协议，本插件统一走 `openai/chat-completions`（网关两种都收，但内部路径不同）；另外官方必发 `x-qwenwork-app-mode`（运行模式 id，由服务端 profile 下发、动态），本插件不臆造该值。要验证需要「同 body 双协议 A/B」，属新增能力（响应格式需转译），工程量不小。

## 0.2.26 (2026-11-28)

### Fix - 按账号在跑请求数平摊并发（上游槽位排队 → 首字延迟 lottery 的根因）

- **现象（0.2.24 生效后实测）**: 同账号 + 同一会话 + 99.8% 前缀缓存命中，首字仍在 **5.9s~23.2s** 之间大幅波动（连续 20 次：7955 / 15280 / 18778 / 7899 / 23184 / 7382 / 6323 / 6655 / 9415 / 7051 / 11382 / 5882 / 6496 / 6924 / 11002 / 7721 / 6917 / 10700 / 8175 / 8343 ms）。**上下文规模、账号、会话、缓存命中四个维度与首字均无相关性**。
- **根因**: 上游按账号分配并行槽位，**超出额度的请求不报错，而是在账号侧静默排队**，表现为首字延迟升高而错误率健康。本插件原先的选号只看「轮询轮到谁」与「会话绑定在谁身上」：① `pickActiveAuth` 是 `activeAuthRR` 单调轮询，与真实负载完全解耦；② `pickSessionAuth` 粘性命中后**无条件**复用绑定号，同一会话的并发子请求因此全部压到一个号上；③ 没有任何地方记录「某个账号正在服务多少个请求」，调度层天然看不到负载。⇒ 与「同一会话的并发子请求压满一个号、超槽位后排队」完全吻合（同仓 `traework` 插件 2026-11-10 已就同一机制落地 0.5.42 修复，见 `doc/4-bugs/2026-11-08_210000_traework首字延迟与生成耗时偏高根因分析.md` 第十九~二十一节）。
- **修复**: 新增 `account_load.go`——在跑请求计数（`accountLoadAcquire` / `accountLoadRelease` / `accountLoadOf`）+ `pickLeastLoaded` + `pickLessLoadedThan` + 单账号在飞软上限 2（与上游默认槽位对齐）；`pickActiveAuth` 改为按真实负载平摊（面板选择只作平级优先，不再无条件直通）；`pickSessionAuth` 粘性命中后在跑数 < 2 才复用，超限且池中存在更闲的账号则改绑并写回绑定表（全池同样忙时保持原绑定，避免来回抖）；流式/非流式/同步收集三条路径都在请求生命周期内记账。
- **安全性依据**: 生产实测快/慢两组的 `cache_ratio` 均为 0.999 且分属不同账号 ⇒ **上游前缀缓存跨账号共享**，把并发摊开不会丢缓存；这是「负载平摊可安全实施」的前提。
- **影响范围**: 仅选号逻辑与请求内记账；不改请求体、不改协议、不改票据与会话键语义。
- **验证**: `cgo-shim-build.py qwenwork` build/vet/test 全绿；新增 `account_load_test.go`（配对自增自减、幂等双释放、空闲优先、平级保序、严格更闲、超限改绑并写回、全池繁忙保持绑定、粘性未超限保持）。

## 0.2.25 (2026-11-28)

### Fix - 亲和类客户端归因头由「每请求随机」改为「会话级恒定」（首字 5.9s~23.2s 波动的根因）

- **现象**: 用户口径「输入 45~71 万 token 时首字 30s、160s 不应该出现，正常都是 10s 以下」。0.2.24 生效后按插件 feed 实测：**同一账号 + 同一会话 + 99.8% 前缀缓存命中**，首字仍在 **5.9s ~ 23.2s** 之间大幅波动（连续 20 次：7955 / 15280 / 18778 / 7899 / 23184 / 7382 / 6323 / 6655 / 9415 / 7051 / 11382 / 5882 / 6496 / 6924 / 11002 / 7721 / 6917 / 10700 / 8175 / 8343 ms）。
- **逐项排除（都不是原因）**: 上下文规模（71 万 token 的 p50 只比小请求慢一点）、账号（换号仅 1/27 且那次最快）、会话（同一会话内波动）、缓存命中（full 99.8% 的组 >10s 率 36%，miss 组只有 1 条）、插件缓冲（插件 TTFT p50=7955ms 与客户端 frt p50=7994ms 逐条一致 ⇒ 插件对首字只加 ~0.2s）。
- **根因**: 波动只能来自「这次请求有没有落到持有该上下文的上游实例」。而插件可控的只有请求头里的**亲和类字段**——官方桌面客户端把 `x-mm-client-request-id` / `x-mm-client-trace-id` / `x-mm-request-set` 做成**每 turn 恒定**（同一 turn 的多次模型调用共用），本插件此前却是**每请求随机 UUID + 随机 trace id**，等于每次请求都换一个「调用身份」；若上游按这些头做实例亲和 / 缓存局部性路由，请求就会在持有该 KV 的实例之间轮转，每次都要把 KV 搬回本地。
- **修复**: 新增 `maasAffinityHeaders(sessionID)`，从**上游会话标识**（票据回填的 `session_id`，同一对话恒定）派生亲和头：`x-mm-client-request-id` = 会话 ID、`x-mm-client-trace-id` = 会话 ID 的 sha256 前 32 位 hex、`x-mm-request-set` = 会话 ID（**本插件此前完全不发**）；另补 `traceparent`（OTel 形式，trace 段会话级恒定、span 段每请求唯一），既保留每请求可区分性又不破坏亲和。无会话标识时退回每请求随机（保持既有行为）。
- **影响范围**: 仅出站请求头；不改请求体、不改路由、不改上游协议语义。
- **验证**: `cgo-shim-build.py qwenwork` build/vet/test 全绿；新增 `affinity_test.go`（会话级恒定 / 不同会话取值不同 / 无会话退回随机 / traceparent 四段且 trace 恒定 span 唯一）；哨兵反证新增 3 条（S17 亲和头退回每请求随机 / S18 request-set 缺失 / S19 traceparent trace 段随机）逐条改坏均真实命中。

## 0.2.24 (2026-11-28)

### Feat - 持续监控补齐：前缀缓存字段提取 + 首字慢埋点 + 票据签发留痕

- 背景: 0.2.23 把首字延迟（TTFT）测准之后，按用户口径「首字大于 10s」复测生产流量（new-api 上游真值，82 请求）：修复后仍有 **39.0%** 的请求首字 >10s。逐项拆解证明**残留部分不在插件侧**：① 插件侧 TTFT 与客户端首帧时间逐条一致（7.8s vs 8.0s）⇒ 插件对首字只加约 0.2s、零缓冲；② 上游尝试放大仅 1.11×（91 次上游尝试 / 82 次客户端请求）⇒ 不是重试风暴；③ 慢/快两组的前缀缓存命中率完全相同（0.996 vs 0.996）⇒ 缓存不是区分因子；④ 与账号、会话间隔、并发度均无相关性。
- 缺陷: 插件只读 `usage.cached_tokens`（顶层），而上游实际把前缀缓存命中数放在 **`usage.prompt_tokens_details.cached_tokens`** ⇒ 插件 feed 与面板的缓存命中**恒为 0**（生产实测上游回报 99.6% 命中，插件侧显示 0），监控口径直接失真，连「首字慢是缓存没命中还是上游慢」都分不出来。
- 修复: `usageDetailFromMap` 补 OpenAI 形态的 `prompt_tokens_details.cached_tokens` / `cache_read_input_tokens`，并保留 Anthropic 顶层 `cache_read_input_tokens` 与顶层 `cached_tokens` 两种回退。
- 新增埋点（只观测、不改行为）：
  - **首字慢留痕**：首字 >10s（与用户口径一致）时按账号节流打一条 `slow first token`，含账号/上游模型/首字/总耗时/输入 token/缓存命中数/缓存命中率/输出 token/思考 token；
  - **票据签发留痕**：MaaS 票据按「账号×上游会话」分片，正常形态是每对话签一次、后续命中缓存；签发/轮换按 60s 节流打一条 `maas ticket minted`，用于识别「退化成每请求重签」这类回归。
- 影响范围: 仅 qwenwork 插件内部；不改变请求构造、不改变路由、不改变任何对外协议。
- 验证: `cgo-shim-build.py qwenwork` build/vet/test 全绿；新增 `observability_test.go`（缓存字段三形态解析、阈值行为、节流行为）；哨兵反证新增 3 条逐条改坏全部真实命中。

## 0.2.23 (2026-11-28)

### Fix - 推理请求「慢 + 排队」：会话身份在上游侧落地 + 慢号让位机制复活 + 排队/模型池忙分类纠正

- 现象: 经本插件调用 QwenWork 时部分推理请求很慢且排队；同一客户端直连 QwenWork 官方无此现象。生产取证（2026-11-28）: 用量 feed 里 qwenwork 的 TTFT p50=0ms / p90=1ms（同期 traework 5063ms、workbuddy 11796ms）；latency p99=65s、max=104s；new-api 侧 use_time p99=125s、max=260s，8.9% 的请求 ≥40s；CPA 宿主日志出现 `duration=3m3.755s err=upstream 504: stream timeout`；流量 100% 集中在单一账号；容器 24 小时内零条 `slow cooldown entered`（同期 traework 多条）。
- 根因一（会话身份在上游侧没落地）: 本插件的 MaaS 票据按「账号×会话」签发，但出站请求只发 `Authorization` + `Content-Type` + `Accept`，**从不发 `x-session-id`，也不发官方客户端的 `x-mm-*` 客户端归因头**。官方桌面客户端（逆向证据：`D:\qwork\QwenWorkCN\2.2.1-26112604\resources\app.asar` → `out/main/main.js` 的 `buildAuthenticatedUpstreamHeaders` / `buildUpstreamHeaders`）必发 `x-session-id` 与 `x-mm-client-request-source` / `x-mm-device-type` / `x-mm-client-request-id` / `x-mm-client-trace-id`。缺会话绑定 ⇒ 请求在上游侧是无归属流量。修复: `applyMaasHeaders` 补齐上述头部，并显式设置 `User-Agent`；票据侧在签发响应缺 `session_id` 时回填本地派生的 UUID，保证 `x-session-id` 永不为空。
- 根因二（全部对话塌缩成同一个上游会话 + 路由钉死单一账号）: 会话键只取 header/metadata，生产实测恒为 `X-Session-ID: 1`（低熵常量）⇒ ① 上游 MaaS session 被全量对话共用（社区记载该形态即「越聊越慢」）；② 调度侧把全量流量粘在一个账号上，其余账号空转。修复: ① 移植同仓 traework 2026-11-10 的「低熵会话标识剔除」（`minSessionEntropyChars` / `lowEntropySessionID`）；② 新增按请求体派生的上游会话键 `maasSessionKeyFromPayload`（种子优先级 `prompt_cache_key` → `user` → system + 首条 user 消息 sha256，对齐 dsh-qwenwork-connector / dsh-qwenwork-connect / Agent2Providers 三份社区实现），把「路由会话键」与「上游会话键」拆成两套语义，MaaS 票据改按「账号×上游会话」分片。
- 根因三（慢号让位机制整体休眠）: ① `pumpUpstreamStream` 的 TTFT 基准晚于发请求，测的是「响应头到首帧」⇒ 样本恒 0~1ms，永远低于 `slow_min_ttft_ms` 下限；② 异步流入口把客户端别名当上游模型传下去，慢速样本键与调度侧查询键对不上；③ 准入闸门 `slow_min_pool_size` 默认 20 > 生产池 8 ⇒ 机制永不开。修复: TTFT 基准前移到发请求之前；异步流改传解析后的 `upstreamModel`；QwenWork 专属准入下限调整为 6（依据 P25 下标在 N≥6 时 ≥1，基准由「第二慢」而非「最慢」定义）。
- 根因四（模型池忙被当账号故障）: 裸 429（如上游 `All models failed`）此前计入账号阶梯冷却，24 小时内 27 次 minimax 429 足以把健康号逐个推进冷却，流量被迫集中到更少账号，进一步加剧排队。修复: 模型池忙归入排队类——对外 503、不冷却账号、不在同请求内换号（模型池在账号间共享，换号无效）；排队判据补 `serviceAvailable:false` 形态（社区三份实现一致口径）。
- 配套: MaaS 票据缓存增加淘汰（过期清理 + 512 条上限），上游会话键逐对话化后条目基数会涨，不淘汰会撞插件堆 128MiB 软上限；请求体只反序列化一次（`prepareUpstreamBody` 复用解析结果派生会话键）。
- 影响范围: 仅 qwenwork 插件内部；宿主配置、其它插件、协议与面板均不变。
- 验证: `cgo-shim-build.py qwenwork` build/vet/test 全绿；新增 4 个测试文件（会话种子、MaaS 出站头与缓存淘汰、排队/模型池忙分类、TTFT 口径与准入闸门）；哨兵反证 10 条逐条改坏全部真实命中（`scripts/qwenwork_sentinel_c1.py`）；gofmt 零差异、UTF-8 无 BOM、零尾随空白。

## 0.2.22 (2026-11-28)

### Fix - 进入面板不再刷新账号，积分只读持久化快照

- 背景: 0.2.20 已让面板轻载路径读持久化快照，但前端 `enterPanel()` 仍会 `POST /refresh` 触发 `EnqueueAll` 全量刷新，右上角出现「后台刷新完成 8 成功」；后端 preserve watchdog 还会每 10 分钟再全量刷一遍。两者与新增的每小时积分扫描重复，等于进入面板仍打满一轮上游。
- 前端: `enterPanel()` 只做 `load()`（读快照），移除 `startBackgroundRefresh()` 调用及其函数体。
- 后端: `runWatchdogTick()` 去掉全量 `EnqueueAll`，只保留日冷却镜像重建——积分刷新统一由每小时扫描负责。
- 保留的手动入口: 单卡「刷新」走 `GET /credits?auth_index=`，写回快照并即时更新该卡。
- 影响范围: 面板进入零上游请求；上游积分请求频率从「进入面板一次 + 每 10 分钟一次」收敛为「每小时一次」。

## 0.2.21 (2026-11-28)

### Fix - 面板文案与模型级准入实现对齐

- 背景: 0.2.17 起路由准入已改为按请求模型判定（免费模型放行 0 积分账号），0.2.18 起额度耗尽也不再物理停用；但面板仍写着「额度已耗尽，账号已停止参与路由」，与实际行为不符，容易让人以为免费模型也不可用。
- 改动: 面板过时文案与注释统一改为准确表述——0 积分账号的**付费模型**不再路由、**免费模型仍可用**、充值后恢复全部模型。涉及卡片主提示、`0积分` 徽标 title、筛选按钮 title，以及副标题里已失效的「额度耗尽自动停用」。
- 同步修正: `lifecycle_test.go` / `scheduler_test.go` 里「QwenWork 无免费模型」的过时前提注释，避免后续维护被误导。
- 影响范围: 仅文案与注释，路由逻辑零改动；`panel.html` 经 `//go:embed` 编入二进制，故 bump 版本重发。

### Fix - 5xx 纳入同请求换号（对齐 workbuddy）

- 背景: `shouldRotateOnUpstreamErr` 原先对非 200 状态只走 `isAccountLevel4xx`（401/403/404/405/429），5xx 不换号。上游过载通常只影响部分后端，用户因此直接吃到第一次失败，只能等下一个请求靠冷却层换到别的账号。workbuddy 早在 2026-10-31 就把 5xx 纳入同请求换号，qwenwork 一直缺这一档。
- 改动: 新增 `shouldRotateOnStatus`（`accountFailover.go`）——账号级 4xx 之外额外纳入 500-599；`shouldRotateOnUpstreamErr` 改走它。三个执行入口（非流式 `main.go`、流式错误帧 `stream.go`、流式 200 业务错误）自动继承。
- 保持不换号: 0（传输错误）走跨请求冷却；402 硬额度由生命周期写盘禁用接管；400/422 请求形状错误换任何账号都会同样失败，避免白烧重试预算。
- 零泄漏保护不变: 流式路径已输出分片时仍不换号，避免输出重复或拼接错乱。
- 测试: `TestShouldRotateOnUpstreamErr` 表驱动扩到 23 例（含 500/502/503/504/599/600 边界）；新增 `TestShouldRotateOnStatus` 19 例锁定状态码全集；哨兵反证命中（移除 5xx 分支 → 两条测试 FAIL → 还原全绿）。

## 0.2.20 (2026-11-28)

### Feat - 积分持久化与每小时定时扫描（对齐 workbuddy）

- 背景: QwenWork 此前只有 45 秒内存缓存，无任何持久化；冷启动打开面板积分全空，进程重启后数据归零，且面板轻载路径读的就是那份易失缓存。
- 新增 `credits_snapshot.go`: 积分落 sidecar `data/qwenwork-credits.json`，作为跨重启唯一真相源。含内存镜像、磁盘懒加载、键级合并原子落盘（唯一临时名 + rename，防多实例互删）、损坏文件按空快照容错。
- 新增 `credits_sweep.go`: 每小时一轮全量扫描，串行 + 每账号 300ms 间隔摊平上游压力，轮末统一落盘；禁用账号同样刷新（积分恢复是其唯一解禁途径）；首轮复用 `watchdogFirstTickDelay()` 抖动，每轮先过 `retireIfSuperseded()` 实例守卫。
- 面板改读快照: `panel.go` 轻载路径从读内存缓存改为读持久化快照，进页面零上游请求；完整 credits 明细随快照保留，前端 `fetched_at` 与已用/池字段渲染不受影响。
- 手动刷新兜底: `/refresh` 与 `/credits` 仍实时拉上游，`refresh_runner.go` 刷新后同步写快照，避免结果只活在内存。
- 脏数据保护: 单账号失败保留上一轮快照（stale-while-error）；nil 积分不覆盖真实数据。
- 与 workbuddy 的差异: QwenWork 是二值模型（无三档分级），快照条目不带 `Tier`、扫描不做分级检查，落盘合并逻辑保持一致。

### 验证

`cgo-shim-build.py qwenwork` build/vet/test 全绿 ×3；新增 6 个快照用例（写入读回/重启恢复/磁盘合并/损坏容错/删号清理/nil 保护）；哨兵反证命中（禁用磁盘合并 → 测试 FAIL → 还原全绿）；gofmt 零输出、UTF-8 无 BOM、零行尾空白。

## 0.2.19 (2026-11-27)

### Fix - 插件自限内存上限并错峰启动，抑制容器 OOM

- 背景: 每个 CPA 插件以 `-buildmode=c-shared` 编译、各自链接一份完整 Go runtime，宿主的 `GOMEMLIMIT` 会被每个 runtime 独立读取，形不成整进程总预算；容器内存上限才是唯一硬约束。启动与批量高峰时多个 runtime 同时扩张，整进程会被 OOM 杀掉（生产实测：启动 23 秒内被 OOM，稳态仅 476MB）。
- 改动: 新增 `memory_limit.go`——插件 `init()` 里 `debug.SetMemoryLimit` 给自身堆设独立软上限（默认 128MiB，可用 `CPA_PLUGIN_MEMORY_LIMIT_MB` 覆盖），并提供 `goAfterStartupJitter` / `waitStartupJitter` 两个启动错峰助手（随机 0~3s）。保号 watchdog 错峰启动；积分刷新并发 10→4、保号并发 4→2。
- 语义: 软上限只作用于本插件自己的 runtime，超限只触发本插件 GC，不影响宿主与其它插件；错峰只推迟后台循环的首轮启动，不改变任何周期与业务语义；需要「停止即返回」的循环（doubao 续期、通知类）用 ctx 感知等待，不受睡眠影响。
- 验证: `cgo-shim-build.py` build+vet+test 全绿；新增 `memory_limit_test.go` 覆盖「默认/覆盖/非法值回落」「runtime 实际生效上限」「错峰后确实执行」「取消时立即返回」四类断言。

## 0.2.18 (2026-11-27)

### Fix - 7 条 P1：并发竞态、票据会话隔离、保活家族、归属标记与多实例守卫

- 免费模型注册表并发安全：`freeModelRegistry` 原先是无锁裸 struct，写侧来自 `callModelsAPI`（探活 / 模型查询 / 调度缓存 miss 多线程触发）、读侧在调度与生命周期热路径，构成 data race。改为 `sync.RWMutex`，并新增并发访问回归测试。
- 耗尽不再物理禁用：0.2.17 用 `hasFreeModels()` 决定是否禁用，动态发现一旦返回空集就会把 0 积分账号重新写 `disabled:true`，反向抵消免费模型路由。现改为耗尽一律不物理禁用，准入完全交给调度层的模型级判定。
- refresh token 家族：`keepalive.go` / `oauth.go` 原先只认 `drt-` 前缀，生产账号的 `ory_rt` 前缀被直接判 `unsupported token family`，4 小时保活对全部账号失效。抽出 `isDeviceRefreshFamily` 同时识别 `drt-` 与 `ory_`。
- MaaS 票据会话隔离：缓存键原先只用账号 UID，`session_id` 只在首次签发生效，同账号不同会话复用同一张票，把上游会话绑定折叠成一个。缓存键改为「账号×会话」，`invalidateMaasSession` 改为前缀清除全部会话分片。
- token 轮换后旧票据泄漏：`invalidateMaasSession` 在 `AccessToken` 已被新值覆盖后才计算缓存键，账号缺 UID 时新指纹算出的键与旧键不同，旧票据删不掉。抽出 `invalidateForRefresh` 同时清刷新前快照与刷新后凭证。
- 归属标记丢失：`disableAuth` 走宿主管理 API 成功后直接 return，`exhausted_disable` 标记从未落盘；宿主 API 只写 disabled、不回写插件自定义顶层字段，账号因此变成「无标记禁用」，充值后会被判为用户手动禁用而永不自动恢复。现改为 API 成功后仍落盘标记，落盘失败则回滚宿主禁用。
- 多实例热重载并发刷新：新增 `instance_guard` 模块（标记文件 + 孤儿接管 + 平台进程存活探测），常驻保活循环每轮先过守卫闸门。热重载不卸载旧 .so，旧实例原先会一直跑到进程结束，多实例并发刷新同一 refresh token 时后到者收 401 TOKEN_EXPIRE，健康账号被误标 session-dead。

### 已知未修（登记）

`price_factor` 缺字段时零值被判免费、`markSessionDead` 不写 disabled 导致死号反复 401、`ensureMaasSession` 的 mint/invalidate TOCTOU、credits 负 total 未夹紧、`maasSession.remaining(now)` 忽略 now 参数。

### 验证

`cgo-shim-build.py qwenwork` build/vet/test 全绿 ×3；每个修复均做哨兵反证（改坏对应行 → 测试 FAIL → 还原全绿）；gofmt 零输出、UTF-8 无 BOM、零行尾空白。竞态修复受环境限制（本机无 gcc、WSL 无发行版）无法跑 `-race`，改以加锁路径审查 + 并发压测 + `go vet` 保障。

## 0.2.17 (2026-11-27)

### Fix - 对齐客户端全量模型 + 0 积分账号可用免费模型

- 模型目录：实测 chat-modes 已扩展到 14 个模型，`parseChatModesCatalog` 原先按 `format` 前缀只保留 openai*，把 `qwen4-max` / `minimax-m3.1-flash`（anthropic/messages）等模型全砍掉。MaaS 网关实测把这些模型统一按 chat-completions 转发（qwen4-max / minimax-m3.1-flash 均 HTTP 200 正常推理），故移除 format 过滤，收录全部 enable 模型；静态兜底表同步补齐 14 个。
- 免费模型路由：实测免费判据是 `price_factor=0`（唯一命中 `minimax-m3.1-flash`，`show_zero_price_factor` 对全模型均为 true 不是判据）。原代码写死「QwenWork 无免费模型」，0 积分账号在 scheduler.pick / active_auth / failover 被账号级硬摘除，lifecycle 还会物理写 disabled。改为模型级准入：免费模型放行 0 积分账号，付费模型沿用原积分红线；耗尽账号有免费模型时不再物理禁用。
- 验证：`cgo-shim-build.py qwenwork` build/vet/test 全绿 + 哨兵反证；新增 free_models 单测覆盖兜底/动态判据、price_factor 解析、0 积分路由准入。

## 0.2.16 (2026-11-14)

### Fix - 面板搜索框被浏览器表单历史自动填回上次搜索词

- 缺陷(用户反馈 2026-11-14): 面板刷新后搜索框会自动填入上次的搜索词，账号列表被旧关键字过滤，看起来像账号丢失。根因是浏览器把 `type="search"` 输入框当成可记忆的表单字段，重载时用表单历史恢复 `value`，与 `autocomplete="off"` 无关——浏览器对同名字段的恢复不受该属性约束。
- 修复: `panel.html` 新增 `searchInputNoAutofill()` 自执行函数——加载时把输入框 `name` 改成每次随机的 `q<random>`，浏览器无法用固定字段历史命中它；同时补齐 `autocorrect="off"`/`autocapitalize="off"`/`spellcheck="false"`/`data-form-type="other"`，压掉移动端与密码管理器启发式填充。
- 放行真输入: 用 `pointerdown` / `keydown` 记录「用户确实操作过」，只有未操作过才在 `focus`、`pageshow` 与 0/60/200/600/1200/2500ms 六个短窗口内清空 `value` 并复位 `currentSearch` + 重渲染，避免浏览器在首帧之后才恢复历史值；用户主动输入的搜索词不会被清掉。
- 影响范围: 仅面板前端——搜索输入框属性与上述自执行清理函数，账号列表渲染、筛选语义、签到/导入/导出等全部链路零改动；后端与插件协议无变化。
- 验证: `python scripts/cgo-shim-build.py` build / vet / test 全绿；10 个插件面板同源改动逐一落地（`searchInputNoAutofill` 与 `data-form-type` 各命中 2 处）。

## 0.2.15

### Fix - 删除自研 max_tokens 控制，改为「显式值零干预」（对齐社区共识）

- 背景: 实拉 GitHub 七家 WorkBuddy 反代项目（tonny0812/workbuddy2api、lovingfish/workbuddy-cliproxy、sixzjd/dsh-workbuddy-connect、dingminhua/dsh-connect-workbuddy、linguo2625469/workbuddy2api-panel、ardeyouxipianyi/workbuddy2api-hub、ithtelab/workbuddy-manager）源码核对，**无一做「抬升客户端显式 max_tokens」或「统一输出地板」**；叠加本方 2026-11-07 生产事故（`max_tokens=2000` 被放宽到 128000，实际生成 12000~22000 tokens / 80~145 秒被判超时）。
- 删除的四类控制: ①`minMaxTokensFloor=32768` 统一最低输出底线；②`defaultMaxOutputTokens` / `defaultReasoningMaxTokens=128000` 统一上限；③推理模式下把客户端 `<=8192` 放宽到 128000；④模型元数据 `MaxCompletionTokens` 无条件抬至 128000。
- 保留的两条归一化: `max_completion_tokens` → `max_tokens` 别名翻译（显式 `max_tokens` 优先，别名一律删除、非正整数不翻译）、GPT 系 `max_tokens < 16` 抬至 16（上游 `code=11133`，两家独立实测 15 拒 / 16 过）。
- 元数据: `maxOutputTokens()` 改为原样透传上游值，上游未提供时留 0（不虚报、不猜值）。
- 未传语义: 客户端不传时不写 `max_tokens`；上游 catalog 只提供 `max_input_tokens`，输出上限如实留 0。
- 验证: `python scripts/cgo-shim-build.py qwenwork` 的 build / vet / test 全绿；`max_tokens_test.go` 重写，覆盖生产事故回归（显式 2000 → 2000）、不放宽（8192 → 8192）、不套地板（100 → 100）、不预截断（200000 → 200000）、别名翻译、畸形值不洗白、GPT 下限。
- 涉及文件: `model_specs.go`（重写为最小干预）、请求侧（`payload.go` / `body.go` / `upstream.go` / `executor.go` 之一）、`models.go`（元数据侧）、`max_tokens_test.go`。

## 0.2.14

### Feat - 非推理输出上限由 64000 抬升至 128000

- 变更: `defaultMaxOutputTokens` 由 64000 抬升至 128000，与 `defaultReasoningMaxTokens` 齐平；`minMaxTokensFloor` 保持 32768、`defaultContextWindow` 保持 1048576 不变。
- 动机: 模型元数据广播层（`models.go` 的 `maxOutputTokens()`）自 2026-10-30 起对全部模型已宣称 128000，而请求体自适应层在非推理模式仍只发 64000，形成「元数据 128k、实际 64k」的自相矛盾，长总结与上下文压缩场景被提前截断。
- 影响: 客户端未传 `max_tokens` 时，非推理请求的补足值由 64000 变为 128000；传入超过 128000 的值截断至 128000；传入低于 32768 的值仍抬升至 32768 底线；推理模式行为不变。
- 验证: `python scripts/cgo-shim-build.py qwenwork` 的 build/vet/test 全绿；`max_tokens_test.go` 全部期望值同步更新。

## 0.2.13

### Fix - 兼容宿主主面板 enc::v2:: 密钥格式（修复面板 401）

- 背景: 宿主主面板 `management.html` 于 2026-10-31 自动更新后，把 `cli-proxy-auth` 的密钥混淆格式从 `enc::v1::` 升级为 `enc::v2::`，XOR key 由 `salt|host|userAgent` 改为 `salt|host`。
- 问题: 插件面板 `deobfuscate()` 只识别 `enc::v1::`，遇到 v2 前缀时原样返回密文，`JSON.parse` 失败导致取不到管理密钥。若 sessionStorage 里还残留旧流程写入的密文，面板会持续用错误 Bearer 请求管理接口，停在「management key 无效或缺失（将自动重试）」并无法自愈。
- 修复: 新增 `ENC_PREFIX_V2` 与带版本参数的 `_keyBytes(v2)`；`deobfuscate()` 先按 v2 解、再回退 v1，兼容升级前写入的旧值；新增 `readSessionKey()`，读取 sessionStorage 时若命中 `enc::` 密文前缀即判定为脏值并清除，让 `getKey()` 回落到 localStorage / URL / 手动输入。
- 影响范围: 仅前端 `panel.html`，后端零改动；账号数据、路由与鉴权语义不变。
- 验证: 复刻宿主真实算法构造 v1 / v2 双格式密文，10 个插件面板在「干净 v2」「sessionStorage 残留 v2」「旧版 v1」三种场景下均解出正确密钥；`node new Function` 语法校验通过；`cgo-shim-build.py` build/vet/test 全绿。

## 0.2.12

### Feat - 输出上限统一为推理 128k / 非推理 64k

- 变更: 删除逐模型官方规格表，改为四个全局常量（`minMaxTokensFloor=32768`、`defaultMaxOutputTokens=64000`、`defaultReasoningMaxTokens=128000`、`defaultContextWindow=1048576`）。所有模型一律按「推理模式 128000 / 非推理模式 64000」自适应，动态发现（上游 /models 返回但未收录）的模型同样适用。
- 自适应规则: 客户端未传 max_tokens（<=0）取目标上限；推理模式且客户端传 <=8192 时放宽到 128000，避免思维链吃满配额提前截断；客户端传值超过目标上限时安全截断，防止上游 400；其余保留客户端原值但受 32768 底线保护。
- 影响: 净删除约 340 行逐模型规格代码；`lookupOfficialModelSpec` 保留签名，恒返回可用规格，调用方零改动。

### Feat - 新增每账号一个 JSON 的 zip 导出

- 新增 `GET /export/zip` 管理接口：把每个账号的原始凭证打成 zip，包内每个账号一个独立 JSON 文件，便于按账号分发或迁移。
- 与既有 `/export` 的区别：`/export` 返回单个汇总 JSON（accounts[] 数组），适合整库备份；`/export/zip` 解压后直接得到一组 `<name>.json`，可逐个拖进导入弹窗，也可直接放进 auth 目录。
- 容错: 单账号读取失败不影响其余账号，失败项以 `<name>.error.txt` 记录原因；无账号时返回 404 JSON 错误。
- 文件名: 取宿主 auth 文件名 basename，为空回退 auth_index，再回退固定名；同名冲突追加 `-2`、`-3` 序号，避免 zip 内条目覆盖。
- 面板: 账号工具栏新增「导出 ZIP」按钮，走原生 `fetch` + `blob()` 下载。

### 验证

- `cgo-shim-build.py qwenwork` build / vet / test 全绿；新增 `credits_export_zip_test.go` 覆盖文件名生成、同名去重、每账号一文件、单账号失败容错、空账号列表、宿主列表报错六条路径。

## 0.2.11

### Fix - 额度耗尽禁用改由 CPA 管理接口写入宿主路由真相源

- 缺陷(生产实证 2026-10-28): 额度耗尽自动禁用原本只直写物理 auth 文件的 `disabled` 字段。宿主用 MySQL 存储时物理文件只是镜像，路由真相源在 DB；宿主下一次 persist 会把镜像覆盖回 `disabled:false`，插件写的 `exhausted_disable` 标记也被白名单裁剪丢弃。
- 修复: 新增 `cpa_disable_sync.go`，耗尽禁用与积分恢复改为调用 `PATCH /v0/management/auth-files/status`——与认证文件管理页 toggle 同一条链路，宿主内存态与 MySQL 同时被改写。
- 边界: 只有「本机制新写入的禁用」才调用该接口（内部 `ownsDisable` 门控）；manual_disable 与未知写入方的禁用一律不调，避免把用户意图改写成插件可恢复的禁用。
- 兜底: 未配置密钥时该通道整体不启用（行为与 0.2.10 完全一致）；配置了但调用失败时回落原写盘路径，并打 `cpa disable api failed, fallback to disk` 降级日志。
- 配置: 新增 `cpa_management_url`（默认 `http://127.0.0.1:8317`）与 `cpa_management_key`（config_yaml 优先 → env `WB_CPA_MANAGEMENT_KEY`）。密钥只用于请求头，任何日志与回显都不输出其值。
- 验证: `cgo-shim-build.py qwenwork` build / vet / test 全绿；新增用例锁定通道契约（方法/路径/Bearer 头/请求体）、未配密钥报错、非 2xx 报错、默认 URL 与末尾斜杠裁除、密钥解析优先级。

## 0.2.10

### Feat - 慢速账号 TTFT 拥堵自动冷却（账号 × 模型级）

- 背景: 同模型下首字延迟显著高于同池水平的账号，在该模型上让出流量。这类账号每次都能返回 200，既有失败冷却完全抓不到。与 workbuddy / traework / qoderwork / zcode 同源实现。
- 判据: 同模型、窗口内样本达标的账号，其 TTFT 中位数达到「全池基准 × 倍数阈值」且高于绝对下限时判慢。基准取各账号中位数的**下四分位（P25）**——用中位数会在「慢号占多数」的池子里被抬到慢号水平、一个也判不出来。
- 状态: 独立 `slow_account.go`，与失败冷却三者互不干扰；**只走内存、不写盘禁用**（慢是性能问题不是故障），到期自动恢复。
- 可观测性: 四类日志——判定取证、防抽干放开清单、到期恢复、配置变更生效值；全部在解锁后写，避免持锁 I/O 阻塞路由判定。

### Fix - per-attempt TTFT 计时隔离（阻断项）

- 缺陷: `ttftNS(started)` 用**请求级**时间戳，跨重试 attempt 复用。账号 A 失败耗时 10s 后换到 B、B 首帧 2s，被记成 **12s**——A 的失败耗时算到健康号 B 头上。慢速判据直接吃这个样本会误禁健康账号。
- 修复: collector 新增 `attemptStarted` 与 `markAttemptStart`；异步流每个 attempt 起点调用，同步 collect 路径在循环内重设（collector 由调用方跨 attempt 复用，不重设就会累积）。
- 验证: 新增 `ttft_attempt_test.go` 3 用例；变异「退回请求级基准」KILLED。

### Fix - 非流式路径 TTFT 恒为 0

- 缺陷: 非流式 `publishUsage(..., 0)` 恒传 TTFT=0，慢速冷却完全没有样本可判——纯非流式流量永远达不到样本门槛，机制对它们等同不存在。
- 修复: 非流式请求函数返回 per-attempt TTFT；聚合函数新增 `firstByteAt *time.Time` 出参，在**首个可解析分片**处打点（不是读完正文的时刻）。三处成功路径（非流式 / 同步流 / 异步流）统一调用 `noteSlowObservation` 打点。
- 验证: 非流式成功分支的 TTFT 不再恒 0；cgo-shim build+vet+test 全绿。

### 配置

- 新增 7 个 `slow_*` 配置键并登记进 `ConfigFields`：`slow_account_enabled` / `slow_ttft_ratio_x10` / `slow_window_minutes` / `slow_min_samples` / `slow_cooldown_minutes` / `slow_min_ttft_ms` / `slow_min_pool_size`。
- 默认值：开启 / 2.5 倍 / 15 分钟窗 / 5 样本 / 10 分钟冷却 / 3000ms 下限 / 账号池 > 20。
- 七个键共同决定判定结果，任一出现即整套写入，避免「阈值改了、窗口还是旧值」的中间态；全缺时保持当前值，防止无关 reconfigure 冲掉显式关闭。


### Feat - 账号池准入闸门（默认 > 20）

- 背景: 慢速判据是各账号中位数的下四分位（P25）。池子太小时该统计量退化成「取较小值」——两个账号里任一次网络抖动就会让其中一个被判慢、两号互相顶替；池内单个毛刺账号即可拉低基准。2026-10-25 生产实证：激进参数下池内 TTFT 仅 1.26 倍差却持续判慢，冷却主体在两号之间反复跳变。
- 闸门: 账号总数（**启用 + 停用**）须严格大于 `slow_min_pool_size`（默认 20）才触发判定。计数含停用账号——停用多是临时的（额度耗尽、探活中），池子真实规模应按归属算；否则一次批量临时禁用就会让机制忽开忽关。
- 位置: 闸门放在**打点之前**而非只在判定里。账号数不足时连样本都不记——池子长大后若拿着欠账的陈旧样本立刻判一轮，等于用不成立的历史数据误判。
- 取数: 账号枚举走宿主 RPC（跨进程），绝不能放每请求热路径。改为独立 ticker 每 10 分钟探测一次，判定入口只读内存缓存。探测失败保留上一次已知值（RPC 偶发失败不该让闸门忽开忽关）；首轮立即探测，不留盲区。
- 未探测状态: 缓存零值一律视为不达标（保守关闭）。宁可晚一个刷新周期生效，也不能在未知规模下用退化基准误判。
- 配置: 新增 `slow_min_pool_size` 配置项与面板字段。
- 验证: 新增 4 个用例（≤20 不判 / 21 放行 / 闸门在打点前 / 未探测保守关闭 / 阈值可配置）；变异「闸门恒放行」「闸门只拦判定不拦打点」「> 改 >=」全部 KILLED。

## 0.2.8

### Fix - 流式 usage 帧被吞致下游全部记 $0；刷新路径补生命周期对账

- 变更要点:
  1. **0 计费根因**：上游把计费 usage 挂在 `choices[0].delta` 为 `{}`、且没有 `finish_reason` 的终止 chunk 上（`{"choices":[{"delta":{},"index":0}],"usage":{...}}`）。`cleanChunkJSON` 的「全空 delta 整帧丢弃」规则命中该帧，把它当噪声丢掉，于是下游（new-api 等）收到的流里完全没有 token 数，逐条记 `$0`。
  2. 直连上游实测确认上游确有该帧（`usage_frames=1`），而 CPA 输出流里最后一帧是 `finish_reason:stop` 后直接 `[DONE]`，`stream_options.include_usage:true` 也拿不到——证明是插件侧丢弃，不是上游不给。
  3. 修复：`delta` 为空且无 `finish_reason` 时，若 chunk 顶层存在 `usage` 则保留整帧（仍清理空 `tool_calls`/`function_call` 噪声），否则维持丢弃。异步 `streamEmit` 与同步 `collectUpstreamStreamOpenAI` 两条路径共用该函数，一处修复双路生效。
  4. **刷新路径缺生命周期对账**：`doFetchOne` 只刷新积分缓存，不触发 `reconcileOneAccount`，导致开启 `lifecycle_auto` 后耗尽账号仍不会被停用（只有面板 force 加载与保活时刻才跑对账）。现于刷新完成后立即对账（非 force，复用刚写入的缓存，不产生第二次上游请求）。
- 验证: `cleanchunk_test.go` 新增 5 个用例（usage 终止帧保留、带空壳噪声仍保留、纯空帧仍丢弃、finish_reason 帧保留、role 首帧保留）；cgo-shim build/vet/test 全绿；**变异反证**：把 usage 分支改回无条件丢弃 → `TestCleanChunkJSONKeepsUsageOnlyFrame` 与 `TestCleanChunkJSONKeepsUsageWithNoiseFields` 精确 FAIL，还原后全绿。
- 首字延迟说明: 生产 feed 实测 104s TTFT 对应输入 **388,677 tokens**（同一会话逐轮累积，每轮 +300~5000），属上游预填充耗时，非插件开销；同会话输入涨到 31 万后单轮 TTFT 稳定在 7~12s。
- 涉及文件: qwenwork/stream.go、qwenwork/refresh_runner.go、qwenwork/cleanchunk_test.go、qwenwork/VERSION、qwenwork/CHANGELOG.md、qwenwork/main.go

## 0.2.7

### Fix - 额度耗尽账号自动禁用并对齐面板展示（推翻「0 积分仍可用免费模型」前提）

- 变更要点:
  1. 用户反馈面板第一个账号「可用积分」永久卡在「加载中…」，且没有任何 0 积分耗尽表现。生产实证：该账号上游 `account-context` 返回 `quota.exceeded=true`、`remaining=0`，但钱包三项全为 0，导致快照 `total_remain/total_used/total_size` 全 0、无 packages ⇒ 被判为「数据未知」而非「耗尽」。
  2. **关键前提被实测推翻**：原实现按「0 积分仍可用免费模型」设计（lifecycle/active_auth/policy/面板文案四处）。生产用该账号签发 MaaS 会话后实测 `qwen3.8-max-preview` / `qwen3.8-flash` / `qwen3.8-plus` / `glm-5.2` **全部 429 `balance_insufficient`** —— QwenWork 没有免费模型。
  3. 修复一（信号层）：`creditsSummary` 新增 `Exceeded` 字段，`buildCreditsSummary` 把上游 `quota.exceeded` 写入快照；`isCreditsExhausted` 将其作为权威判据（有余额时仍以余额优先，防陈旧标记）。
  4. 修复二（路由层）：新增 `isCreditsLowOrExhausted`，调度过滤、`cachedCreditsScore`、`accountRoutable`、换号候选四处统一改用该判定，耗尽账号不再承接流量。
  5. 修复三（生命周期）：新增 `disableAuth`，耗尽时写 `disabled:true` + `exhausted_disable:true`（可自动恢复）；已禁用文件保持原状，`manual_disable` 与无标记禁用永不被自动改动。`reconcileOneAccount` 在启用态判定耗尽即禁用，禁用态仅当积分恢复为正数才重新启用。
  6. 修复四（面板）：新增 `isAccountExhausted` 统一判定并纳入 `exceeded`；耗尽时进度条显示「已耗尽 · 余0」而非无限转圈；可用/0积分筛选、计数、汇总卡与文案全部改为「耗尽不可用」口径。
- 验证: cgo-shim build/vet/test 全绿；新增 `billing_exceeded_test.go`（3 用例）覆盖 `exceeded` 映射、正常账号不误判、钱包失败回落；`lifecycle_test.go` 改为 `TestExhaustedCreditsAreNotRoutable` + `TestExceededFlagMarksExhausted`；调度与会话用例改为「耗尽必须换号/让位」。**变异反证三组**：① 恢复旧「0 积分可路由」→ 2 用例 FAIL；② `accountRoutable` 失效 → 2 用例 FAIL；③ 解析层不写 `exceeded` → 接线用例 FAIL（首次变异未捕获，据此抽出 `buildCreditsSummary` 纯函数补测后命中）。
- 已知边界: `lifecycle_auto` 默认关闭，写 disabled 的自动禁用需开启该开关；路由排除与面板展示**无条件生效**，不依赖开关。
- 涉及文件: qwenwork/management.go、billing.go、policy.go、lifecycle.go、scheduler.go、active_auth.go、failover_retry.go、panel.go、credits_handler.go、panel.html、main.go、lifecycle_test.go、scheduler_test.go、session_auth_test.go、billing_exceeded_test.go、VERSION、CHANGELOG.md

## 0.2.6

### Fix - MaaS session_id 必须是合法 UUID（执行器路径 422 的真正原因）

- 变更要点:
  1. 0.2.5 修好 body.model 后，执行器路径改报 `maas mint: http 422 value is not a valid UUID`。生产日志定位：插件把调度侧会话键（形如 `x-client-request-id:abc`、`derived:conv-123`）原样当作上游 `session_id` 发送，而上游强校验 UUID。
  2. 面板「测试」传空 sessionID 故正常，执行器路径必带会话键故必失败——又是一次「面板成功、网关失败」的路径差异。
  3. 修复：新增 `maasSessionID`，按会话键做 SHA-256 确定性派生并格式化为 v4 UUID（版本位 4、变体位 8/9/a/b）；同一会话恒得同一 UUID（保持会话粘性），空键返回空串交由上游分配。
  4. `mintPayload` 独立成函数，便于测试直接断言「实际发往上游的 payload 里 session_id 是 UUID」。
- 验证: 新增 `maas_session_id_test.go`（4 个用例）覆盖 UUID 格式、确定性、空键；**变异反证**：把 `mintPayload` 退回直接透传会话键 → `TestMintPayload_SessionIDIsUUID` 精确 FAIL；还原后全绿。
- 已知边界: 首版测试只断言 `maasSessionID` 本身，变异（跳过派生）未被捕获；据此把 payload 组装抽成 `mintPayload` 并补断言，才让变异命中。
- 涉及文件: qwenwork/maas.go、qwenwork/maas_session_id_test.go、qwenwork/VERSION、qwenwork/CHANGELOG.md

## 0.2.5

### Fix - 透传请求体的 model 改写为上游真实 ID（别名 400 的真正根因）

- 变更要点:
  1. 0.2.3（执行器反解）与 0.2.4（作用域改 both）上线后**别名请求仍 400**。生产日志给出决定性证据：`provider=qwenwork-provider model=qwen3.8-max-preview alias=qwen-3.8-max`——**宿主早已把 `ExecutorRequest.Model` 反解为真实 ID**，传给上游的却是 `req.Payload`（客户端原文，其中 `model` 仍是别名）。
  2. 真正根因：插件透传路径 `prepareUpstreamBody(bodyRaw)` 原样转发客户端请求体，从未改写其中的 `model` 字段；`upstreamModel` 仅用于用量统计，未回写 body。
  3. 修复：`prepareUpstreamBody` 增加 `upstreamModel` 参数，在强制 `stream=true` 与 `max_tokens` 自适应之外，把 body 的 `model` 改写为已解析的上游 ID；空值时保留原值。同步与流式两条路径都传入 `upstreamModel`。
  4. 0.2.3/0.2.4 的改动保留：执行器反解（兼容 auth 属性别名通道）与 `ExecutorModelScopeBoth`（让 `StaticModels`/`ModelsForAuth` 能填充别名缓存）本身正确且必要，只是不足以覆盖「body 未改写」这条真正的失效路径。
- 验证: 新增 `TestPrepareUpstreamBody_RewritesModelToUpstreamID`（断言别名被改写为真实 ID、空上游名保留原值）；**变异反证**：删除 model 重写 → 该用例精确 FAIL；还原后 cgo-shim build/vet/test 全绿。
- 涉及文件: qwenwork/payload.go、qwenwork/main.go、qwenwork/max_tokens_test.go、qwenwork/VERSION、qwenwork/CHANGELOG.md

## 0.2.4

### Fix - 模型作用域改为 both，让宿主注册期填充 oauth-model-alias 缓存

- 变更要点:
  1. 0.2.3 补了执行器别名反解，但**生产实测仍 400**。根因在更上层：插件声明 `ExecutorModelScope: OAuth`，而宿主 `executorScopeAllowsStaticModels`（`internal/pluginhost/adapters.go:60`）只对 `static`/`both` 返回 true ⇒ 宿主**从不调用** `StaticModels` ⇒ `cacheModelAliases` 从未执行 ⇒ 别名缓存恒空 ⇒ `resolveUpstreamModel` 查不到任何映射。
  2. 改为 `ExecutorModelScopeBoth`：既让注册期 `StaticModels` 被调用以填充别名缓存，又保留 OAuth 账号级模型发现能力。
  3. 补 `TestRegistration_ExecutorModelScopeAllowsStaticModels` 锁定该声明；变异回 `OAuth` 时该用例精确 FAIL。
- 验证: cgo-shim build/vet/test 全绿；变异反证精确命中；生产别名请求由 400 → 200（见下）。
- 涉及文件: qwenwork/main.go、qwenwork/alias_resolution_test.go、qwenwork/VERSION、qwenwork/CHANGELOG.md

## 0.2.3

### Fix - 执行器补齐 oauth-model-alias 反解（别名请求不再 400）

- 变更要点:
  1. 根因：宿主对 `oauth-model-alias` 只做**路由匹配**，传给执行器的 `req.Model` 仍是客户端原始别名；插件执行器此前只做 `stripProviderPrefix` 就直接透传，而上游只认真实模型 ID ⇒ 经 new-api 走别名的请求稳定返回 `400 invalid_request`（`param: "None"`）。面板「测试」按钮直接指定真实 ID，故同账号同模型测试成功——两条路径的差异掩盖了该缺陷。
  2. 生产实证（2026-10-18 04:13）：`qwen3.8-max-preview` 直连 200；三条别名 `qwen-3.8-max` / `qwen-3.8` / `qwen3.8-max` 全部 400。面板 `test-active` 对照同样复现（真实 ID 成功、别名失败），排除账号与网络因素。
  3. 修复：新增 `upstreamModelFor(model, attrs)`，固定「剥 provider 前缀 → 反解别名 → 模型键归一」三步；同步路径 `handleExecExecute` 与流式路径 `handleExecStream` 统一调用，消除两处各自拼装。
  4. 别名来源双通道：auth 属性 `model_alias` / `model-alias` / `oauth-model-alias` 优先，其次宿主级 `oauth-model-alias` 缓存（`cacheModelAliases` 在 `model.static` / `model.for_auth` 时由宿主传入的 `HostConfigSummary` 填充）。
- 验证: 新增 `alias_resolution_test.go`（4 个用例、9 个子用例）覆盖宿主缓存通道、auth 属性优先级、`cacheModelAliases` 过滤规则（空别名/同名/空 name 跳过）、channel 名大小写不敏感，以及执行器入口 `upstreamModelFor` 的完整链路（含 `qwenwork/` 前缀与大小写形态）；**变异反证**：把 `upstreamModelFor` 退回「只剥前缀」→ 6 个子用例精确 FAIL；还原后 cgo-shim build/vet/test 全绿。
- 已知边界: 本版修复了执行器接线，但宿主级别名缓存因作用域声明问题仍为空，生产验证未通过——由 0.2.4 补上作用域修复后才真正生效。
- 涉及文件: qwenwork/main.go、qwenwork/alias_resolution_test.go、qwenwork/VERSION、qwenwork/CHANGELOG.md

## 0.2.2

### Fix - 正文已泄漏的断流改为 length 收尾（new-api 可恢复计费）

- 变更要点:
  1. 根因：0.2.1 只解决了「仅思考分片时换号」，但**正文已发出后断流**仍走 `streamEmitError` 发 `event: error`。宿主 `forwardResponsesStream` 对非 Codex 客户端只输出 `event: error`（`writeTerminalError`），而 new-api 的 `OaiResponsesStreamHandler` 只从 `response.completed / incomplete / done / failed / cancelled` 提取 usage；`event: error` 不在其列 → 走正文估算兜底，但正文为空时 `CompletionTokens=0` → 打出「上游没有返回计费信息，无法扣费」并 `$0`。
  2. 收尾策略改为合成终止分片：正文已泄漏且无法换号时，不再发 error，改为 emit 一个 `finish_reason=length` 的空 delta 分片并正常关流。宿主补 `[DONE]` 后，转译层 `incompleteByFinishReason` 把 `length` 映射为 `response.incomplete`（含 `incomplete_details.reason=max_output_tokens`），该事件属终态（`responsesSSETerminalEvent`）不会触发 502 兜底。
  3. 计费恢复链路：new-api 收到 `response.incomplete` 后从终态事件提 usage；若无 usage 则回退到 `response.output_text.delta` 累计正文估算（`relay_responses.go:146`），正文已泄漏场景可正常出 token。
  4. 语义边界：**只有正文已泄漏才合成 length**；零泄漏或仅思考分片仍走换号，换号失败仍透传 error（保持可观测性，不把真实失败伪装成成功截断）。
  5. 同步路径（`StreamID == ""`）不改：宿主 `rpcPluginAdapter.ExecuteStream` 恒填 `StreamID`（`rpc_client_stream.go:16`），该分支仅为防御性 fallback；其调用方在 `err == nil` 时把请求记为成功，若在此合成终止分片会让截断在 dashboard 上伪装成成功。
- 验证: 新增 `TestBuildStreamTerminalChunk`（校验 object/model/finish_reason=length/空 delta/不被判为内容泄漏）与 `TestBuildStreamTerminalChunkFraming`（SSE 前缀差异）；两次变异反证精确命中（`finish_reason` 改 `stop` → 1 例 FAIL；`delta` 改非空 → 1 例 FAIL）；cgo-shim build/vet/test 全绿。
- 已知代价: 客户端会看到「成功但内容被截断」的 incomplete 状态，而非显式错误；这是保留已生成内容的必然取舍，日志侧仍有 `publishUsage(failed=true)` 记录可追溯。
- 依据: 用户裁定「traework 式收尾作为正文已泄漏分支的补充」；宿主与 new-api 链路已逐行核验（`responsesSSETerminalEvent`、`buildResponsesCompletedEvent`、`relay_responses.go:91-153`）。
- 涉及文件: qwenwork/policy.go、qwenwork/stream.go、qwenwork/stream_read_error_failover_test.go、qwenwork/main.go、qwenwork/VERSION

## 0.2.1

### Fix - 思考阶段断流允许换号 + 断流不再误伤账号冷却

- 变更要点:
  1. 根因：0.2.0 的零泄漏守卫以 `emitted`（是否发出任意分片）判定，把「仅思考分片已下发、正文尚未开始」也视为已泄漏。生产会话 `5fa71566` 的 `qwen-3.8-flash` 请求（new-api ID `202610171844095432303748268d9d6vrvyri6H`）在 `reasoning_effort=max` 下思考 33.99s 后 `connection reset by peer`，正文未开始却因 `emitted=true` 拒绝换号，整条请求失败且 new-api 零计费。
  2. 细化泄漏语义：新增 `streamChunkHasContent` 判定分片是否承载正文/工具调用/refusal；异步与同步两条断流路径改以 `contentEmitted` 作为换号守卫。**仅思考分片允许换号重放，正文已发出仍一律不换号**（保持原有不变量）。
  3. 错误文本标记分级：正文已泄漏仍打 `after partial output`（抑制换号与信封状态码）；仅思考泄漏改打 `reasoning only, no content`，不再抑制 502 信封，宿主可据此跨 provider 轮转。
  4. 断流不再对账号记账：断流是上游网络/服务抖动，与账号凭据健康度无关。移除 `noteAccountFailure(curAuthID, 0, ...)`，避免健康账号被阶梯冷却后无法承载流量。
- 验证: `TestShouldRotateOnStreamReadError` 扩至 10 例（新增「仅思考分片仍可换号」「正文已泄漏不换号」）；新增 `TestStreamChunkHasContent`（11 例）；cgo-shim build/vet/test 全绿。
- 已知代价: 思考阶段换号会让客户端看到两段思考摘要（宿主 ABI 无清空已发内容通道）。

## 0.2.0

- 多账号调度：账号身份 key 统一为宿主 auth.ID，修复克隆遗留的 token 当身份导致冷却永不生效（详见 `doc/3-实施/2026-10-17_234000_QwenWorkProvider插件立项_实施总览.md` § CYCLE-02）。

## 0.1.0

- 首发：Device Flow + PKCE 登录、MaaS desktop-sessions 签发、OpenAI 兼容推理透传、管理面板与用量流。
