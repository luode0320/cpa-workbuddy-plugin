# WorkBuddy AI Plugin Changelog

## 0.1.34

### Fix - 兼容宿主主面板 enc::v2:: 密钥格式（修复面板 401）

- 背景: 宿主主面板 `management.html` 于 2026-10-31 自动更新后，把 `cli-proxy-auth` 的密钥混淆格式从 `enc::v1::` 升级为 `enc::v2::`，XOR key 由 `salt|host|userAgent` 改为 `salt|host`。
- 问题: 插件面板 `deobfuscate()` 只识别 `enc::v1::`，遇到 v2 前缀时原样返回密文，`JSON.parse` 失败导致取不到管理密钥。若 sessionStorage 里还残留旧流程写入的密文，面板会持续用错误 Bearer 请求管理接口，停在「management key 无效或缺失（将自动重试）」并无法自愈。
- 修复: 新增 `ENC_PREFIX_V2` 与带版本参数的 `_keyBytes(v2)`；`deobfuscate()` 先按 v2 解、再回退 v1，兼容升级前写入的旧值；新增 `readSessionKey()`，读取 sessionStorage 时若命中 `enc::` 密文前缀即判定为脏值并清除，让 `getKey()` 回落到 localStorage / URL / 手动输入。
- 影响范围: 仅前端 `panel.html`，后端零改动；账号数据、路由与鉴权语义不变。
- 验证: 复刻宿主真实算法构造 v1 / v2 双格式密文，10 个插件面板在「干净 v2」「sessionStorage 残留 v2」「旧版 v1」三种场景下均解出正确密钥；`node new Function` 语法校验通过；`cgo-shim-build.py` build/vet/test 全绿。

## 0.1.33

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

- `cgo-shim-build.py workbuddy-ai` build / vet / test 全绿；新增 `credits_export_zip_test.go` 覆盖文件名生成、同名去重、每账号一文件、单账号失败容错、空账号列表、宿主列表报错六条路径。

## 0.1.32

### Fix - 失败禁用状态改由 CPA 管理接口写入宿主路由真相源

- 缺陷(生产实证 2026-10-28): 失败禁用原本只直写物理 auth 文件的 `disabled` 字段。宿主用 MySQL 存储时物理文件只是镜像，路由真相源在 DB；宿主下一次 persist 会用内存态把镜像覆盖回 `disabled:false`，插件写的归属标记也被白名单裁剪丢弃，导致插件认为已禁用、宿主照样把请求发过来。
- 修复: 新增 `cpa_disable_sync.go`，禁用/恢复改为调用 `PATCH /v0/management/auth-files/status`——与认证文件管理页 toggle 同一条链路（`applyAuthDisabledState` + `authManager.Update` → `persist` → `store.Save`），宿主内存态与 MySQL 同时被改写。
- 兜底: 未配置密钥时该通道整体不启用（行为与 0.1.31 完全一致）；配置了但调用失败时回落原写盘路径，并打 `cpa disable api failed, fallback to disk` 降级日志。
- 配置: 新增 `cpa_management_url`（默认 `http://127.0.0.1:8317`）与 `cpa_management_key`（config_yaml 优先 → env `WB_CPA_MANAGEMENT_KEY`）。密钥只用于请求头，任何日志与回显都不输出其值。
- 边界: 只改造插件自动写入的失败禁用；manual_disable（用户在宿主手动关的号）不由本插件调用该接口。
- 验证: `cgo-shim-build.py workbuddy-ai` build / vet / test 全绿；新增用例锁定「配密钥走 API 且不写盘」「未配密钥写盘零回归」「API 非 2xx 回落写盘」。

## 0.1.31

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

## 0.1.30

### Fix - 失败禁用标记被宿主 persist 抹掉后自动补回

- 背景: 宿主在**每次请求结束后**都会用内存里的 `Auth.Metadata` 重建物理 auth 文件（`MySQLStore.Save` → `auth.Storage.SaveTokenToFile`，白名单语义，未列出的顶层键一律丢弃）。插件直写的禁用归属标记不在该白名单内，于是「插件写禁 → 宿主 persist」之后盘上只剩 `disabled:true`，归属标记消失。标记一丢，探活循环按标记挑候选就再也找不到这个号，账号停在「宿主禁用、插件不认领」的死角。
- 生产实证（workbuddy-provider 0.20.2）: 2026-10-25 三个账号带着 `failover_disable_at` 存活且 DB `disabled=true`（写盘链路本身可用）；另三个在 24 毫秒内被批量重写为 `disabled:false` 且标记全无，DB `updated_at` 分别早于文件 mtime —— 证明最后一笔不经 MySQL，是插件自身周期性落盘写回了已被宿主重建过的脏文档。
- 修复: `persistCounterDelta`（watchdog 周期性落盘）在写盘前调用 `restoreFailoverMarkersIntoDoc` 补齐 `failover_disable` / `_model` / `_at` 三键。补写条件严格限定为「本进程写过禁用」且「盘上确实 `disabled:true`」——前者防止替用户手动禁用或额度耗尽禁用伪造归属，后者防止把已恢复的账号重新标成禁用。`at` 时刻从内存新增的 `failoverDisableAtLocal` 取，保证重建的标记仍是原始禁用时刻。
- 面板: 新增 `failover_unconfirmed` 字段。盘上写着禁用而宿主列表仍 `enabled` 时，徽标显示「写盘未生效·仍在接流量」而不是「10 分钟探活中」。
- 验证: 新增 `failover_marker_restore_test.go` 5 用例；变异验证（去掉本地归属判据 / 断开 `persistCounterDelta` 的补标记调用 / 丢弃原始禁用时刻 / 面板判据忽略宿主状态）全部 KILLED；cgo-shim build+vet+test 全绿；gofmt 干净。
- 涉及文件: `counter.go`、`failover_disable.go`、`panel.go`、新增 `failover_marker_restore_test.go`

## 0.1.29

### Refactor - 移除失败冷却前置探活，与国际版对齐

- 背景: 0.1.24 从国内版移植「失败即写盘禁用 + 探活解禁」时，一并带上了国内版已于 2026-10-22 删除的冷却探活（`cooldown_probe*.go`），导致国际版同时跑两条恢复通道——内存冷却到期前 20 秒回放一次（1 秒 tick），盘上禁用账号每 10 分钟回放一次。两条都对同一账号发请求、判据不同，且 `failover_disable.go` 的头注释早已写明该机制已移除，注释与实现不一致。
- 删除: `cooldown_probe.go`、`cooldown_probe_test.go`、`cooldown_probe_cycle_test.go`；`main.go` 的 `init()` 不再启动 `cooldownProbeLoop`。恢复通道收敛为唯一一条：10 分钟禁用探活，只回放失败模型，探通才解禁。
- 补齐移植时漏掉的路由判据: `isAccountCoolingDown` 增加「本进程已写盘禁用」这层判据。国内版有、国际版缺失，后果是内存阶梯封顶 10 分钟到期后，插件会把一个盘上仍 `disabled:true` 的账号判为可用——直到禁用探活恢复它为止，这期间路由判据与宿主实际状态不一致。
- 注释对齐: `accountFailover.go`、`failure_class.go`、`model_circuit.go`、`stream.go`、`main.go` 中指向「冷却探活到期前回放」的说明统一改为禁用探活口径。
- 测试: `model_circuit_test.go` 三个冷却探活用例改写为禁用探活版（`TestModelBlockedDualWritesCooldownAndDisable` / `TestProbeSuccessClearsBothCooldownAndDisable` / `TestProbeFailureKeepsAccountDisabled`），并补回国内版已有的 `TestAccountLevelFailureDualWritesCooldownAndDisable`、`TestRepeatedFailuresDoNotRewriteDisk`。
- 变异验证: 临时移除 `isAccountCoolingDown` 中新增的写盘禁用判据 → 相关用例立即 FAIL；还原后全绿，确认该判据确实被测试覆盖。
- 涉及文件: 删除 `cooldown_probe.go` / `cooldown_probe_test.go` / `cooldown_probe_cycle_test.go`；修改 `accountFailover.go`、`failure_class.go`、`main.go`、`model_circuit.go`、`model_circuit_test.go`、`stream.go`、`VERSION`。

## 0.1.28

### Feature - 面板不再自动刷新，默认只展示持久化积分

- 背景: 接 0.1.26/0.1.27 的积分持久化后，面板打开仍会自动触发一轮全量后台刷新（`enterPanel` → `POST /refresh`），并每 10 分钟定时再刷一轮，随后 2s 轮询 `/refresh/status`。账号规模后续会明显变大，这个「打开即对全池打一轮上游」的成本随账号数线性增长，且多数时候用户只是想看一眼余额。
- 改为: 面板打开只渲染后端已有数据（`accountCache` 优先，缺失时用盘上 `credits_snapshot` 兜底），不再发任何刷新请求，也不注册定时刷新器。要看最新积分时点卡片上的「刷新」——该按钮走 `GET /credits?auth_index=`，只刷一个账号。
- 连带清理: 随自动刷新一起失效的死代码整体移除——卡片刷新三态（`data-refresh-state` / `.ref-tag` CSS）、顶部「后台刷新中 N/M」进度、2s 状态轮询、`startBackgroundRefresh` 及其 429 退避，以及仅供静默刷新使用的 `api()` `__silent` 通道。后端 `POST /refresh`、`GET /refresh/status` 与 10 分钟 watchdog 巡检按用户裁定**保留不动**（巡检同时承担定时探活与「测试」标签维护）。
- 修复（移除自动刷新暴露的三处连带缺陷）:
  1. **手动刷新不落盘**: `handleCreditsQuery` 取到新积分后只更新内存，没打持久化标记。自动刷新在时可被后续 flush 顺带写盘，移除后面板唯一的上游取数入口不再写盘 → 下次打开仍显示旧快照。现在取到即 `tagCreditsPersist` + `flushCreditsSnapshots`。
  2. **签到积分不落盘**: 签到新增积分包后同样只更新内存，重启即丢。自动巡检路径补同样的打标与落盘。
  3. **缓存过期 GC 会掏空活账号**: `buildDashboardEx` 原本删除 `fetched` 超过 4×TTL（3 分钟）的条目。该策略依赖「面板一打开就全量刷新补回」，移除后能补数据的只剩 10 分钟巡检，两次巡检之间打开面板会让 plan / 签到态越刷越空。现改为只回收「账号已从宿主列表消失」的残留（抽成 `pruneAccountCacheForLive`）。
- 顺带修正: 默认渲染分支不再调用会打上游的 `resolveRegisteredState`，改用新增的纯缓存版 `cachedRegisteredState`；否则账号越多，打开面板的上游请求越多，与本次目标直接冲突。真实注册态探测仍由显式刷新与「补注册」按钮承担。
- 面板文案: 无快照账号不再显示会永久转圈的「加载中…」，改为「暂无积分数据，点「刷新」获取」。注意判据只认「没有快照对象」——三项全 0 是真实零余额（0.1.25 已确认零积分仍可用），仍照常渲染为「可用 0」。
- 测试: 新增 `test/workbuddy-ai/panel_no_auto_refresh_repro.mjs`（Node vm 真实执行面板 JS，断言打开面板零刷新请求、无长周期定时器、单卡刷新仍生效、零余额不被误判为无数据）；新增 `panel_cache_test.go`（渲染路径不触网 + 活账号缓存不被过期清除 + 渲染路径确实调用缓存版）；`credits_persist` 接线审计期望上调为 tag×2 / flush×3。
- 变异验证: ①把 `startBackgroundRefresh` 与 10 分钟定时器加回面板 → 前端回归 6 条断言 FAIL；②删掉手动刷新的打标与落盘 → 接线审计 FAIL（tag 1/2、flush 2/3）；③恢复按 `fetched` 年龄删缓存 → `TestPruneAccountCacheKeepsLiveStaleEntry` FAIL；④把渲染路径换回 `resolveRegisteredState` → 首轮**存活**（纯函数单测覆盖不到调用点），补 `TestDashboardRenderPathUsesCachedRegistration` 后捕获。
- 涉及文件: 修改 `panel.html`、`panel.go`、`credits_handler.go`、`checkin.go`、`credits_persist_test.go`、`main.go`、`VERSION`；新增 `panel_cache_test.go`、`test/workbuddy-ai/panel_no_auto_refresh_repro.mjs`。

## 0.1.27

### Fix - 积分快照在备注刷新时被白名单重建抹掉（0.1.26 的补漏）

- 背景: 0.1.26 把积分写进顶层键 `credits_snapshot` 后，生产实测发现文件里该键会被周期性清除。根因是 `buildAuthFileJSON` 走**白名单重建**——只输出 `type/provider/logo/disabled/note/auth/account` 加 `extra`，任何未显式搬运的顶层键都会消失。`lifecycle.syncAuthNote` 在 4 小时巡检里 routinely 重建文档，于是「刚持久化的积分」最多存活一个巡检周期。
- 修复: 把快照搬运并入既有的 `putFailoverMarkersIntoExtra`，与 failover 三个标记统一走同一条搬运路径。这样所有 `buildAuthFileJSON` 调用点**自动**受保护，不需要每个调用点各记一份键清单——少记一处就是一次静默丢数据。
- 新增 `creditsSnapshotRawFromJSON`：按原始 JSON 值搬运而非反序列化成 `*creditsSummary` 再序列化，避免 `omitempty` 让空包列表在往返中消失。
- 测试: `TestCreditsSnapshotSurvivesWhitelistRebuild` 锁住「重建后快照与 failover 标记都在、数值不漂移」；`TestPutFailoverMarkersNilWhenNothingToCarry` 保住「无可搬运键返回 nil」的既有语义（新登录取依赖它）。变异验证：删掉快照搬运后该用例立即 FAIL。
- 涉及文件: 修改 `failover_disable.go`、`credits_persist_test.go`、`main.go`、`VERSION`。

## 0.1.26

### Feature - 积分快照持久化（照搬计数器的内存优先 + 盘上备份模型）

- 背景: 积分原本只活在 `accountCache`（纯内存 `sync.Map`），容器重启或插件热重载后全丢，面板首屏积分列退回「加载中…」，要等一轮上游刷新才恢复。同插件的成功/失败计数早已有 `success_count` / `failed_count` 落盘 + 启动回读，积分缺这一层。
- 落盘: 新增顶层键 `credits_snapshot`，由 `foldCreditsSnapshotIntoDoc` 并入物理 auth JSON，**保留其余所有顶层键**（禁用标记、计数器、备注等）。走 `persistAuthDirect` 而非 `host.auth.save`——后者按宿主类型重建记录，会丢掉它不认识的键。
- 启动回灌: `loadCreditsFromDisk` 在 watchdog 启动时紧跟 `loadCountersFromDisk` 执行，把盘上快照写回 `accountCache`，使面板首屏立刻有数字。`fetched` 取当前时间，避免刚回灌就被 45s TTL 判为过期。
- 落盘节奏: watchdog tick（默认 10 分钟）调用 `flushCreditsSnapshots`；另在 refresh runner 每轮刷新收尾时补一次，让「刚重启就取到的新积分」不必等满一个 tick 才进盘。
- 面板兜底: `/accounts` 在 `fetchCredits=false` 且缓存缺失时直接读盘上快照并顺手回灌缓存，覆盖「刚重启」「缓存被 4×TTL GC 清掉」两种窗口。
- 幂等与新鲜度契约: 积分是**快照**不是累加量，故用 `serial` / `persistedSerial` 对齐而非增量折叠。`ensureCreditsPersistLoaded` 在内存已有更新快照时**保留内存值**，绝不让盘上旧值覆盖实时数据；`seedCreditsCacheFromDisk` 同样只填空缓存、不覆盖已有条目。
- 测试: 新增 `credits_persist_test.go` 共 12 例——解析四形态（缺失/坏 JSON/空账本/零余额有用量）、落盘保留既有键、坏 JSON 容忍、nil 快照不写、serial 递增、回灌不覆盖新值、幂等、读取优先级、round-trip 保留积分包。另加 `TestCreditsPersistWiringIsPresent` 静态审计（扫生产源码、排除定义行与注释），守护五个接缝不被静默删除。变异验证：盘上值覆盖内存、覆盖已有缓存、删掉打标调用三处回退分别被对应用例捕获。
- 涉及文件: 新增 `credits_persist.go`、`credits_persist_test.go`；修改 `cache.go`、`watchdog.go`、`refresh_runner.go`、`panel.go`、`main.go`。

## 0.1.25

### Fix - 移除「耗尽」标签与按额度排除逻辑（零积分同样可用）

- 背景: WorkBuddy AI 账号即使积分为 0 也能正常推理，但插件沿用了国内版的额度语义——面板打「耗尽」红标、调度与换号按剩余额度排除候选、`lifecycle_auto` 把耗尽账号自动禁用。用户实测 `luode0322@gmail.com`（free，已用 100/100）被标「耗尽」且不再承载流量，与实际可用性冲突。
- 路由: `scheduler.go` 候选过滤、`active_auth.go` 的 `accountRoutable` / `ensureDefaultActiveAuth`、`session_auth.go` 会话可用性、`failover_retry.go` 同请求换号候选，全部移除积分判据。零积分账号正常参与路由与会话粘性；积分仅保留为排序依据（高积分优先，未测排最后）。
- 生命周期: 删除 `disableAuth` / `deleteAuth` / `applyExhaustedPolicy` / `lifecycleActionFor` / `shouldReenable` 全套按额度禁用删除逻辑；执行器硬额度错误（402 / 额度文案）只做失败冷却记账，不再触发禁用。新增一次性解禁：盘上带 `exhausted_disable` 标记的历史账号由其自动恢复为 `disabled:false` 并清除标记；`manual_disable` 与无标记禁用保持不动。
- 面板与 API: 移除「耗尽」徽标、「耗尽」过滤器、「低额保护」徽标与统计项，`/accounts` 与 `/credits` 不再下发 `exhausted` / `low_credits`，汇总不再输出 `exhausted_count`。「可用」口径收敛为「未打测试标签」。
- 配置: 移除 `min_routable_credits` 解析（存量配置写该键将被忽略，不再影响任何行为）；`lifecycle_auto` 描述改写为备注维护 + 历史标记解禁。
- 测试: 新增零积分可路由回归（调度保留选中项、全池零积分仍 handled、会话粘性不受额度影响、面板选中不跳过零积分）；`shouldReenableLegacyExhausted` 判据新增 6 组用例（含 manual 优先、无标记不认领）。变异验证：注入「零积分排除」「accountRoutable 判零」「manual 不再优先」三处回退，分别被 3 / 5 / 1 个用例捕获。
- 涉及文件: 修改 `policy.go`、`scheduler.go`、`active_auth.go`、`session_auth.go`、`failover_retry.go`、`lifecycle.go`、`billing.go`、`panel.go`、`panel.html`、`credits_handler.go`、`usage_config.go`、`main.go`、`main_test.go`、`scheduler_test.go`、`session_auth_test.go`、`routing_exclusion_test.go`、`min_routable_credits_test.go`；新增 `lifecycle_legacy_test.go`。

## 0.1.24

### Feature - 对齐国内版：连续使用轮换、失败即写盘禁用 + 探活解禁

- 背景: 国际版此前只有「冷却」一层，缺少国内版已上线的两项能力：单账号长期承流无让位机制、账号级失败只冷却不落盘（宿主重启后失效）。本次按国内版语义补齐，积分分级体系按产品裁定不同步。
- 连续使用轮换（新增 `usage_rotation.go`）: 单个账号**连续**承流满 `rotation_serve_minutes`（默认 60）后主动让位，进入 `rotation_cooldown_minutes`（默认 60）冷却，后续请求改由其它账号承接。计时取「连续使用」——距上次使用超过 `rotation_idle_reset_minutes`（默认 10）即视为新一轮。因换号发生在插件入口而非依赖宿主 selector，**已绑定会话同样生效**。防抽干：冷却前若除本号外池内已全部冷却，先按冷却结束时间放开一半（向上取整）。
- 轮换接线覆盖: 调度候选过滤、同请求换号候选（`pickNextAuthForModel`）、执行入口前置拦截（非流式与流式各一处）、成功路径打点（非流式 / 同步流式 / 异步流式三条）。
- 失败即写盘禁用（新增 `failover_disable.go`）: 账号级失败除写内存冷却阶梯外，同时把 `disabled:true` + `failover_disable` 标记写入物理 auth 文档；每 10 分钟串行遍历被本机制禁用的账号，回放**失败当时的模型**探活，成功即解禁。只认自己写的标记，manual / exhausted 一律不接管。
- 顺带修复三个既有缺口（迁移测试暴露）:
  1. **后台刷新会静默解封禁用号**：原 `toAuthData(sa)` 固定回 `disabled:false`，禁用中的号被一次 token 刷新绕开探活直接解封。改为沿用盘上禁用状态并搬运标记。
  2. **7 个白名单重建调用点会抹掉 failover 标记**：`buildAuthFileJSON` 不搬运标记会让探活循环认不出被禁用的号（静默遗忘）。已补齐 `credits_handler`、`lifecycle`（4 处）、`management`。
  3. **失败记账未触发写盘**：`recordFailureScoped` 原先只写内存冷却，现拆出 `recordCooldownScoped` 作为纯内存写入，外层补上写盘调用。
- 配置项（4 个，全部可在面板配置）: `rotation_enabled`（默认 true）、`rotation_serve_minutes`（60）、`rotation_cooldown_minutes`（60）、`rotation_idle_reset_minutes`（10）。`serve` 或 `cooldown` 填 0 等同关闭。
- 测试护栏: 新增 `failover_disable_test.go`（含「失败记账必须触发写盘」接线用例，已做变异验证确认能捕获断线）与轮换相关用例。
- 涉及文件: 新增 `usage_rotation.go`、`failover_disable.go`、`failover_disable_test.go`；修改 `main.go`、`scheduler.go`、`stream.go`、`failover_retry.go`、`accountFailover.go`、`lifecycle.go`、`oauth.go`、`management.go`、`credits_handler.go`、`usage_config.go`。

## 0.1.23

### Fix - 客户端标识对齐真实客户端：UA 改为 `WorkBuddy/<版本>`、按日同步版本号、补 X-IDE-Version

- 背景: 插件长期发送 `User-Agent: CLI/2.63.2 WorkBuddy/2.63.2`。从官方 CLI 产物反查确认上游真实 UA 形态是 `${X-IDE-Name}/${版本}`，双产品名前缀的写法在当前 bundle 中已不存在；且 `2.63.2` 停留在 2026-03-17，落后当时最新版 181 个稳定版。
- UA: 改为 `WorkBuddy/<产品版本>`，品牌名与 `X-IDE-Name` 复用同一常量 `ideNameHeaderValue`。
- 版本来源: 官方 COS `releases/latest` 每 24 小时拉取（24h + ≤30min 抖动），npm registry 备用；失败保留当前值，兜底编译期常量 `builtinClientVersion`。
- 安全边界: 只升不降，只接受三段纯数字，拒绝预发布后缀与 `v` 前缀。
- X-IDE-Version: 补上并固定 `0.0.0`（该字段是宿主 IDE 版本语义，独立客户端无宿主 IDE；与 UA 产品版本号是两套语义，有专项不变量测试守护）。
- 配置项: 新增 `client_version`，留空每日自动更新，填写则钉死。
- 覆盖点: `commonHeaders`、`backendHeaders`、模型发现链路三处头部构造。
- 涉及文件: `main.go`、`models.go`、`usage_config.go`、新增 `client_version.go` 与 `client_version_test.go`。

## 0.1.22

### Fix - 补齐会话追踪头，修复国际版部分账号 11128

- 变更要点:
  1. 现象：国际版部分账号聊天返回 `HTTP 400 / code 11128`（request illegal，未通过渠道校验），同一出口与同一模型下只有部分账号持续失败。
  2. 根因：`backendHeaders` 缺少上游渠道校验所要求的会话追踪头，请求被判为非法来源。
  3. 修复：在 `backendHeaders` 统一补齐 `X-Conversation-ID`、`X-Conversation-Request-ID`、`X-Conversation-Message-ID`、`X-Root-Request-ID` 四个头；用标准库生成 UUID v4，根请求 ID 与当前请求 ID 关联。普通聊天、流式、换号重试与主动探活共用该入口，不新增依赖，不改 UA / 模型 / 提示词 / 账号配置。
  4. 边界说明：本修复针对已复现的渠道校验失败，不把所有 11128 归为同一原因（账号级 11128 仍走既有账号×模型冷却）。
  5. 新增回归用例校验四个头存在、UUID v4 格式与跨请求唯一性。
- 涉及文件: `main.go` `conversation_headers_test.go` `VERSION`

## 0.1.21

### Fix - 11128 模型级拦截并入账号×模型冷却，移除独立熔断

- 变更要点:
  1. 根因：`classifyUpstreamFailure(400, 11128)` 返回 `failureScopeNone`——11128 从未进入冷却体系。旧实现另建独立熔断状态机（跨 3 账号命中 → 打开 60 秒闸门），但该闸门只拒绝请求、**不改变路由**，宿主仍把请求派给已被拦截的账号×模型，且逐条在本地失败（生产实测 duration 55–160ms），形成「一整片账号全失败」的记录。
  2. 改造：11128 归入 `failureScopeAuthModel`，写入账号×模型冷却，由既有阶梯（60s×次数、上限 10 分钟）与冷却探活复用同一套恢复验证；`model_circuit.go` 由状态机降为记账薄（`noteModelBlockedHit` / `resetModelBlockedState`），独立熔断状态与 `model circuit open` 短路闸门全部删除。
  3. 探活覆盖模型维度：`cooldownProbeTargets` 同时挑账号级与账号×模型级冷却，同一账号多条记录按归一化键去重、只探最近到期那条；探活成功按候选来源精确清除对应维度（模型级成功不清账号级冷却）。
  4. 探活周期标记改用归一化键：生产日志证实同一账号以原始键与别名键成对存在时会被重复探测（间隔约 1 秒），修复后按 `normalizeFailoverKey` 统一标记。
  5. 探活调度参数：提前量 5s → 20s、粒度 5s → 1s。原组合下窗口宽度恰等于 tick 间隔，窗口内只落得进一个 tick，一旦被「上一轮未结束就跳过」吃掉，该冷却周期永不再被探测；现窗口内 20 个机会，并新增 `TestCooldownProbeIntervalFitsWindow` 防回退。

- 涉及文件: `failure_class.go` `model_circuit.go` `accountFailover.go` `cooldown_probe.go` `main.go` `stream.go` 及对应测试

## 0.1.19

### Fix - 正文已泄漏的断流改为 length 收尾（new-api 可恢复计费）

- 变更要点:
  1. 根因：0.1.18 只解决了「仅思考分片时换号」，但**正文已发出后断流**仍走 `streamEmitError` 发 `event: error`。宿主 `forwardResponsesStream` 对非 Codex 客户端只输出 `event: error`（`writeTerminalError`），而 new-api 的 `OaiResponsesStreamHandler` 只从 `response.completed / incomplete / done / failed / cancelled` 提取 usage；`event: error` 不在其列 → 走正文估算兜底，但正文为空时 `CompletionTokens=0` → 打出「上游没有返回计费信息，无法扣费」并 `$0`。
  2. 收尾策略改为合成终止分片：正文已泄漏且无法换号时，不再发 error，改为 emit 一个 `finish_reason=length` 的空 delta 分片并正常关流。宿主补 `[DONE]` 后，转译层 `incompleteByFinishReason` 把 `length` 映射为 `response.incomplete`（含 `incomplete_details.reason=max_output_tokens`），该事件属终态（`responsesSSETerminalEvent`）不会触发 502 兜底。
  3. 计费恢复链路：new-api 收到 `response.incomplete` 后从终态事件提 usage；若无 usage 则回退到 `response.output_text.delta` 累计正文估算（`relay_responses.go:146`），正文已泄漏场景可正常出 token。
  4. 语义边界：**只有正文已泄漏才合成 length**；零泄漏或仅思考分片仍走换号，换号失败仍透传 error（保持可观测性，不把真实失败伪装成成功截断）。
  5. 同步路径（`StreamID == ""`）不改：宿主 `rpcPluginAdapter.ExecuteStream` 恒填 `StreamID`（`rpc_client_stream.go:16`），该分支仅为防御性 fallback；其调用方在 `err == nil` 时把请求记为成功，若在此合成终止分片会让截断在 dashboard 上伪装成成功。
- 验证: 新增 `TestBuildStreamTerminalChunk`（校验 object/model/finish_reason=length/空 delta/不被判为内容泄漏）与 `TestBuildStreamTerminalChunkFraming`（SSE 前缀差异）；两次变异反证精确命中（`finish_reason` 改 `stop` → 1 例 FAIL；`delta` 改非空 → 1 例 FAIL）；cgo-shim build/vet/test 全绿。
- 已知代价: 客户端会看到「成功但内容被截断」的 incomplete 状态，而非显式错误；这是保留已生成内容的必然取舍，日志侧仍有 `publishUsage(failed=true)` 记录可追溯。
- 依据: 用户裁定「traework 式收尾作为正文已泄漏分支的补充」；宿主与 new-api 链路已逐行核验（`responsesSSETerminalEvent`、`buildResponsesCompletedEvent`、`relay_responses.go:91-153`）。
- 涉及文件: workbuddy-ai/policy.go、workbuddy-ai/stream.go、workbuddy-ai/stream_read_error_failover_test.go、workbuddy-ai/main.go、workbuddy-ai/VERSION

## 0.1.18

### Fix - 断流分级换号（补齐 0.16.5 同源修复）+ 断流不再误伤账号冷却

- 变更要点:
  1. 根因：本插件此前**完全没有断流换号路径**——`pumpUpstreamStream` 的 `scanner.Err()` 分支只记录失败并透传错误，同步路径同样直接返回；连零泄漏场景也不会换号。同时错误信封无 `http_status`，宿主无法据此跨 provider 轮转。
  2. 补齐断流识别与分级换号：新增 `isStreamReadError` / `shouldRotateOnStreamReadError` / `streamChunkHasContent` / `streamChunksHaveContent`，异步与同步两条路径均在「正文零泄漏」时换号续试。
  3. 细化泄漏语义：仅思考分片已下发时打 `reasoning only, no content` 标记并允许换号；正文已发出时打 `after partial output`，抑制换号与状态码。避免 qwen-3.8-flash 在 `reasoning_effort=max` 下思考阶段断流被误判为不可重试。
  4. 信封补 502：新增 `envelopeHTTPStatusForError`，断流时携带 `http_status=502`，让宿主识别为网关失败并可跨 provider 轮转。
  5. 断流不再对账号记账：断流是上游网络/服务抖动，与账号凭据健康度无关，移除 `noteAccountFailure(curAuthID, 0, ...)`，避免健康账号被阶梯冷却。
- 验证: 新增 `TestIsStreamReadError`（8 例）、`TestShouldRotateOnStreamReadError`（10 例，含「仅思考分片仍可换号」「正文已泄漏不换号」）、`TestStreamChunkHasContent`（11 例）；变异反证（守卫退回 `emitted`）精确命中 1 例；cgo-shim build/vet/test 全绿。
- 已知代价: 思考阶段换号会让客户端看到两段思考摘要（宿主 ABI 无清空已发内容通道）。

## 0.1.17

### Fix - 账号故障冷却改为 30s 递增阶梯、上限 10 分钟

- 变更要点:
  1. 根因：`failoverCooldownFor` 此前对所有失败一律返回固定 15 秒，无法区分「偶发单次抖动」与「账号持续不可用」。连续失败时账号每 15 秒就被重新拉回路由、再次失败，形成「快速轮转但始终打不通」的抖动，既浪费配额也放大上游压力。
  2. 冷却窗口改为随连续失败次数线性递增：`30s × 连续失败次数`，在 10 分钟封顶（`failoverCooldownLadderCap = 10min / 30s = 20` 档）。偶发单次失败只冷 30 秒，持续失败的账号逐步退到 10 分钟，一次成功即清零回落。
  3. 软失败与硬失败共用同一计数阶梯：429 无额度标记、软限流文案、零字节断流三类瞬时限流形态，此前只做固定冷却、不推进连续失败计数；现统一计入阶梯，与硬失败口径一致。
  4. 阶梯时长按「先比较后相乘」计算，避免计数极大时整数溢出。
  5. 回归用例补强：`TestFailoverCooldownFor_Ladder`（含封顶档位）、`TestRecordAccountFailure_LadderCooldown`、`TestRecordAccountFailure_LadderCapsAtMax`。
- 边界说明: 业务类 4xx（400 等）仍不计入阶梯。
- 涉及文件: workbuddy-ai/accountFailover.go、workbuddy-ai/accountFailover_test.go、workbuddy-ai/policy.go、workbuddy-ai/stream.go、workbuddy-ai/keepalive.go、workbuddy-ai/retry_config.go、workbuddy-ai/main.go、workbuddy-ai/VERSION

## 0.1.16

### Feat - 国际版 OAuth 授权后自动补注册地，修复新账号无可用额度

- 变更要点:
  1. 修复根因：插件以 `platform=CLI` 调 `auth/state`，官方登录页据此主动跳过注册地选择页，新账号以 `areaInfoComplete=false` 落盘，云账号未注册、无额度池（面板表现为 plan 为空、可用积分 0）。
  2. 新增 `registration.go` 补注册链路：查 `areaInfoComplete` → 按出口 IP 推导注册地（`CN` 映射为 `HK`）→ 取国家完整条目 → 提交注册地 → 注册云账号 → 尽力领试用包，全流程 Bearer 鉴权，无需官方客户端或浏览器会话。
  3. 授权钩子接入：`handlePollLogin` 在返回凭证前调用 `ensureAccountRegistered`，已注册账号零额外调用直接跳过；补注册失败只记录状态、不阻断授权落盘（授权本身已成功）。
  4. 面板状态透出：`wbAccount` 新增 `registered` 字段，无 plan 时才探测注册态并回写缓存；新增「未注册」徽标与「补注册」按钮，对应管理路由 `POST /register`。
  5. 新增 `registration_test.go` 六个用例（嵌套 JSON 解包、CN→HK 映射、已注册跳过、完整链路、提交失败、UID 缺失），并完成 CN→HK 哨兵反证。
- 涉及文件: workbuddy-ai/registration.go、workbuddy-ai/registration_test.go、workbuddy-ai/oauth.go、workbuddy-ai/management.go、workbuddy-ai/credits_handler.go、workbuddy-ai/panel.go、workbuddy-ai/panel.html、workbuddy-ai/cache.go

## 0.1.15

### Chore - 定时探活降频至 4 小时并修正失败账号节流缺陷

- 变更要点:
  1. `defaultActivePingInterval` 由 30 分钟放宽至 4 小时：真实推理与探活共享账号调用配额，探活只服务 `test_failed` 标签的健康判定，30 分钟一轮过于频繁。
  2. 修正失败账号节流缺陷：`recordActivePing` 由「仅成功时刷新」改为「无论成败都刷新」。此前失败账号不计时，会在每个 watchdog tick（10 分钟）被反复全量重测，反而比健康账号更频繁地消耗账号配额。
  3. 新增 `TestDoActivePingFailureStillThrottles` 回归用例：断言失败后立即再次调用被节流拦截、不再发起模型请求。
- 涉及文件: workbuddy-ai/active_ping.go、workbuddy-ai/active_ping_test.go

## 0.1.14

### Feat - max_tokens 统一 32768 最低输出底线 + 定时探活改全量模型按序测试

- 变更要点:
  1. 设立 `minMaxTokensFloor=32768` 统一最低输出底线：规格表兜底 `ReasoningMax` 由 8192 对齐至 32768；`computeAdaptiveMaxTokens` 四个出口全部经 `applyMaxTokensFloor` 收敛，杜绝任何路径回落到 32768 以下的过小输出上限。
  2. 广播层同步：`models.go` 的静态模型列表与 `maxOutputTokens()` 输出的 `MaxCompletionTokens` 一并保底。
  3. 定时探活改为全量模型按序测试：废除「随机取 ≤5 个」与整轮预算熔断，`doActivePing` 取该账号全部候选模型依次测试；任一成功即判定健康并立即终止本轮（清 `test_failed`），仅当全部失败才打标签。
  4. 探活负载 `max_tokens` 经 `applyMaxTokensFloor` 抬升。
  5. 新增 `TestDoActivePingAllModelsBeyondLegacyCap` 回归用例。
- 涉及文件: workbuddy-ai/model_specs.go、workbuddy-ai/models.go、workbuddy-ai/active_ping.go、workbuddy-ai/*_test.go

## 0.1.13

### Fix - 修复官方规格自适应 max_tokens 未随 0.1.12 二进制发布（内嵌 vcs.revision 取证）

- 变更要点:
  1. 根因：0.1.12 发布 tag (`workbuddy-ai-provider-v0.1.12`) 指向 commit `5f12b41`，早于自适应引擎提交；构建产物内嵌 `vcs.revision=5f12b41`，二进制符号探针中 `computeAdaptiveMaxTokens` / `model_specs.go` 全部 MISS——线上运行的是旧的写死 128k 实现。
  2. 本版重新打包发布，使 `model_specs.go` 官方规格矩阵与 `computeAdaptiveMaxTokens` 真正进入线上二进制：未传补齐官方最大值，推理模型在 <= 8192 时放宽至 ReasoningMax，超限截断。
  3. 验证方式：构建产物内嵌 `vcs.revision` 必须指向本版提交，且二进制符号探针须命中 `computeAdaptiveMaxTokens`。

## 0.1.12

### Feat & Fix - 设立安全缓冲额度红线 (minRoutableCredits=20)、健康平摊轮询与官方规格自适应 max_tokens

- 变更要点:
  1. 设立安全缓冲额度红线 (`min_routable_credits`，默认 20，可配置)：剩余额度 `< 20` 的账号视为低额保护态，不再分配新推理流量。
  2. 调度策略逆转：废除残血优先（Low-Credit-First）升序排序，在 `scheduler.pick` 初筛中硬排除低额与耗尽账号，存活健康候选平摊轮询（Round-Robin）。
  3. 请求内换号链过滤：`pickNextAuth` 跳过 `remain < min_routable_credits` 账号；`hardCreditMarkers` 补齐 `exceeded the quota` 与 `14018` 触发换号重试。
  4. 按模型官方规格自适应计算 `max_tokens`：引入 `model_specs.go`，未传补齐官方最大值，推理模型在 <= 8192 时放宽至 ReasoningMax，超限截断，根治推理模型 token 耗尽问题。
  5. 面板状态与视觉对齐：新增 `low_credits` 字段与「低额保护」黄色徽标展示。

## 0.1.11

### Feat - 补齐 checkin_auto 配置声明与全套路由与基础设施回归单测

- 变更要点:
  1. 补齐 `main.go` 中 `ConfigFields` 的 `checkin_auto` 配置字段声明，打通宿主管理界面对国际版自动签到调度器的可视化配置。
  2. 全面补充 11 套核心单测资产（涵盖会话粘性路由、低积分优先调度、全灭 defer、计数持久化、Token 保活、测试标签与看门狗）。
- 涉及文件: workbuddy-ai/main.go、workbuddy-ai/VERSION、workbuddy-ai/*_test.go
