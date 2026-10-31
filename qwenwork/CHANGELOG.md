# QwenWork Plugin Changelog

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
