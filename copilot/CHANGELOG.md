# Changelog

## 0.1.12

### Fix - 兼容宿主主面板 enc::v2:: 密钥格式（修复面板 401）

- 背景: 宿主主面板 `management.html` 于 2026-10-31 自动更新后，把 `cli-proxy-auth` 的密钥混淆格式从 `enc::v1::` 升级为 `enc::v2::`，XOR key 由 `salt|host|userAgent` 改为 `salt|host`。
- 问题: 插件面板 `deobfuscate()` 只识别 `enc::v1::`，遇到 v2 前缀时原样返回密文，`JSON.parse` 失败导致取不到管理密钥。若 sessionStorage 里还残留旧流程写入的密文，面板会持续用错误 Bearer 请求管理接口，停在「management key 无效或缺失（将自动重试）」并无法自愈。
- 修复: 新增 `ENC_PREFIX_V2` 与带版本参数的 `_keyBytes(v2)`；`deobfuscate()` 先按 v2 解、再回退 v1，兼容升级前写入的旧值；新增 `readSessionKey()`，读取 sessionStorage 时若命中 `enc::` 密文前缀即判定为脏值并清除，让 `getKey()` 回落到 localStorage / URL / 手动输入。
- 影响范围: 仅前端 `panel.html`，后端零改动；账号数据、路由与鉴权语义不变。
- 验证: 复刻宿主真实算法构造 v1 / v2 双格式密文，10 个插件面板在「干净 v2」「sessionStorage 残留 v2」「旧版 v1」三种场景下均解出正确密钥；`node new Function` 语法校验通过；`cgo-shim-build.py` build/vet/test 全绿。

## 0.1.11

### Fix - 11128 模型级拦截并入账号×模型冷却，移除独立熔断

- 变更要点:
  1. 根因：`classifyUpstreamFailure(400, 11128)` 返回 `failureScopeNone`——11128 从未进入冷却体系。旧实现另建独立熔断状态机（跨 3 账号命中 → 打开 60 秒闸门），但该闸门只拒绝请求、**不改变路由**，宿主仍把请求派给已被拦截的账号×模型，且逐条在本地失败（生产实测 duration 55–160ms），形成「一整片账号全失败」的记录。
  2. 改造：11128 归入 `failureScopeAuthModel`，写入账号×模型冷却，由既有阶梯（60s×次数、上限 10 分钟）与冷却探活复用同一套恢复验证；`model_circuit.go` 由状态机降为记账薄（`noteModelBlockedHit` / `resetModelBlockedState`），独立熔断状态与 `model circuit open` 短路闸门全部删除。
  3. 探活覆盖模型维度：`cooldownProbeTargets` 同时挑账号级与账号×模型级冷却，同一账号多条记录按归一化键去重、只探最近到期那条；探活成功按候选来源精确清除对应维度（模型级成功不清账号级冷却）。
  4. 探活周期标记改用归一化键：生产日志证实同一账号以原始键与别名键成对存在时会被重复探测（间隔约 1 秒），修复后按 `normalizeFailoverKey` 统一标记。
  5. 探活调度参数：提前量 5s → 20s、粒度 5s → 1s。原组合下窗口宽度恰等于 tick 间隔，窗口内只落得进一个 tick，一旦被「上一轮未结束就跳过」吃掉，该冷却周期永不再被探测；现窗口内 20 个机会，并新增 `TestCooldownProbeIntervalFitsWindow` 防回退。

- 涉及文件: `failure_class.go` `model_circuit.go` `failover.go` `cooldown_probe.go` `executor.go` `stream.go` 及对应测试

## 0.1.9 (2026-10-18)

发布链路提速 CYCLE-03 演练版本：验证「代码提交 → dispatch → 下载校验 → publish 单次提交」新流程，无功能改动。

## 0.1.8

免费 / 学生档在上游只开放 auto 选模，插件此前既没有 auto 入口、又会把账号无权调用的模型继续暴露。

**Fix**

- `models.go`：新增 `autoModelID` / `isAutoModelID` / `ensureAutoModel`，所有模型出口（`model.static`、`model.register`、`model.for_auth`、面板 `/models`、面板 `/accounts`、测试弹窗）统一把 `auto` 合成到清单**末尾**并去重。上游 `/models` 不返回 auto（它不是具体模型）；实测（2026-10-17 生产免费账号）该账号省略 model 字段与传 `"auto"` 均返回 400 `model_not_supported`，而具体模型返回 200，说明并非所有免费账号都能走自动选模。因此 auto 只作保留入口置于末尾，避免能直连具体模型的账号把 auto 当首选而踩 400。
- `models.go`：`defaultModels` 收敛为 `["auto"]`。原静态兜底写死 `gpt-4.1` / `gpt-4o` / `claude-sonnet-4` / `o4-mini` / `gemini-2.5-pro`，在上游不可达时会把已下线或当前档位无权调用的模型暴露给宿主；具体模型 ID 一律只由 `/models` 动态获取。
- `models.go`：探测判据由布尔改为三态（usable / unusable / unknown），并修正「探测确定全空就回退候选」的缺陷——免费档上游 `/models` 仍列出大量 chat 条目、探测对每个都返回 `model_not_supported`，旧实现只要探测结果为空就回退候选，导致无权模型被原样暴露（用户点开即 400）。现在只有存在不确定结果（传输错误 / 5xx / 限流）时才保留候选。
- `models.go`：400 / 422 且未命中模型拒绝文案时判为可调用（请求已过模型鉴权、落到参数校验，典型为 `messages must be non-empty`）；此前这类响应落到 unknown 分支。
- `models.go`：`allConfiguredModels` 输出按字典序稳定排序，避免 map 随机迭代顺序让模型注册结果每次不同。
- `executor.go`：`mutateModel` 对 auto 执行 `delete(body, "model")` 而非写入 `"auto"`。上游对 `"model":"auto"` 返回 400 `model_not_supported`，省略该字段交由服务端按账号档位自选。
- `active_ping.go`：`pingUpstreamOnce` 对 auto 同样省略 `model` 字段，与真实推理请求保持同一形态。
- `oauth.go`：刷新令牌时即使模型清单为空也落盘，否则免费档账号上的旧具体模型会一直残留。

**Test**

- 新增 `TestMutateModelAutoOmitsField`（auto 删除字段、大小写与空白容忍）、`TestEnsureAutoModel`（末尾去重、空输入仍产出 auto）、`TestIsAutoModelID`、`TestFetchUpstreamModelsDoesNotPersistAuto`（落盘不含 auto）。
- `TestFetchUpstreamModelsKeepsCandidatesWhenProbeYieldsNothing` 拆分为 `...ClearsWhenProbeSaysUnentitled`（确定全空必须清空）与 `...KeepsCandidatesWhenProbeIsUncertain`（不确定才保留）。
- 哨兵变异 5/5 精确命中；`cgo-shim-build.py copilot` build/vet/test 全绿；真实 `.so` ABI 探针 5/5 PASS。
- 生产实测（2026-10-17）：`auto` 省略 model 与 `"model":"auto"` 均 400，`gpt-4o-mini-2024-07-18` / `gpt-4.1` 均 200，据此确定 auto 置末尾。

## 0.1.6

修复 GitHub 授权成功后长时间停留在「等待认证中」，账号迟迟不落盘的问题。

**Fix**

- `oauth.go`：`pollDeviceLogin` 按 GitHub 设备流的 `interval` 在会话内节流——宿主前端固定 3 秒轮询，此前每次都原样转发上游令牌端点，快于 GitHub 要求的 5 秒间隔，第二次起持续返回 `slow_down` 并把要求间隔阶梯抬到 10/15/20/25/30 秒，导致用户授权完成后仍要等数分钟才拿到 token（生产实测：14:46:16 首次轮询 → 14:54:28 才落盘，约 8 分 12 秒）。现在未到下次允许轮询时间直接返回 pending，不访问上游。
- `oauth.go`：`oauthLoginSession` 新增 `nextPollAt` 与 `mu`；收到 `slow_down` 时调用新增的 `applySlowDown` 采纳上游给出的间隔（未给出时按 `githubSlowDownStepSec` 递增），并立即推后下次轮询时间。此前 `Interval` 字段写入后从未被读取、`githubSlowDownStepSec` 常量从未被引用。
- `oauth.go`：同一会话的轮询改为会话级互斥，避免宿主与插件面板同时轮询时重复消费设备码。

**Test**

- 新增 `TestPollDeviceLoginThrottlesWithinInterval`（窗口内 3 次轮询上游调用 0 次、窗口后 1 次）与 `TestPollDeviceLoginAdoptsSlowDownInterval`（采纳 `interval=30` 并推后 `nextPollAt`）；新增 `expirePollWindow` 辅助，`TestDeviceCodeFlowStartAndPoll` 同步适配。
- 哨兵变异 2/2 精确命中；`cgo-shim-build.py copilot` build/vet/test 全绿。

## 0.1.5

账号故障冷却由固定 15 秒改为 30s 递增阶梯、上限 10 分钟。

**Fix**

- `failover.go`：`failoverCooldown` 常量（固定 15 秒）替换为 `failoverCooldownBase = 30s` / `failoverCooldownMax = 10min` / `failoverCooldownLadderCap = 20`，新增 `failoverCooldownFor(count)` 按连续失败次数线性递增并在 10 分钟封顶。此前固定窗口让持续失败的账号每 15 秒被重新拉回路由、再次失败，形成「快速轮转但始终打不通」的抖动；现偶发单次失败只冷 30 秒，持续失败逐步退到 10 分钟，一次成功即清零回落。
- `failover.go`：`noteAccountFailure` 改用 `failoverCooldownFor(entry.failCount)` 计算冷却窗口；阶梯时长按「先比较后相乘」计算，避免计数极大时整数溢出。

**Test**

- 新增 `TestFailoverCooldownFor_Ladder`（含封顶档位断言）与 `TestNoteAccountFailure_LadderCooldown`。
- `cgo-shim-build.py copilot` build/vet/test 全绿。

## 0.1.4

修复模型清单暴露账号无权模型的问题：面板与宿主此前显示 25 个模型，但用户逐个测试几乎全部 400，实际只有 10 个可用。

**Fix**

- `models.go`：新增零消耗可用性探测。上游 `/models` 只返回「账号可见」而非「账号可调用」的模型全集，且响应中无任何静态字段能区分可调用性（实测生产账号：25 条候选仅 10 条真正可走 `/chat/completions`，其余报 400 `model_not_supported`）。现按账号逐模型探测，只保留可调用模型。
- `models.go`：探测手法为零配额消耗——向 `/chat/completions` 发送仅含 `{"model": id}` 的请求（故意省略 messages）：可调用模型返回 400 `messages must be non-empty`（已过模型鉴权），无权模型返回 400 `model_not_supported`（模型鉴权阶段即拒，不计费）。实测探测前后 `quota_snapshots` 完全一致。
- `models.go`：探测并发上限 6（实测 25 个模型耗时 3.38s）；单个探测遇传输错误 / 5xx 时保守保留该模型，避免上游抖动误删可用模型；探测整体无结果时回退到 `/models` 过滤清单（宁多勿空），不因探测故障清空模型出口。
- `models.go`：动态发现后同步刷新账号落盘 `models` 字段，避免 `modelCache` 过期后面板卡片回落到历史兜底清单，与测试弹窗不一致。
- `models.go`：`gpt-5.4-nano` 类仅在 `/responses` 可用的模型会因探测失败被剔除——本插件只转发 `/chat/completions`，不可路由的模型不应暴露。

**Test**

- 新增 6 个用例：探测判据区分 entitlement、不确定结果保守保留、批量过滤、探测无结果时的降级保留、并发执行验证、`fetchUpstreamModels` 端到端过滤。
- 哨兵反证 4/4：禁用探测过滤、探测判据写反、移除降级保留、探测改串行，均被对应用例精确捕获。
- 真实 `.so` ABI 探针 5/5 PASS。

## 0.1.3

修复模型自动获取不正确（面板与宿主长期只显示 5 个兜底模型，上游实际可用 25 个），并为面板补齐「测试模型」功能。

**Fix**

- `models.go`：`authModelsForIndex` 优先级反转为「上游动态发现 > 账号落盘缓存 > 配置 models > 静态兜底」。原先账号文件的 `models` 字段非空即返回，导致 `fetchUpstreamModels` 永不执行、动态发现被完全旁路。
- `models.go`：`fetchUpstreamModels` 按上游契约过滤——只保留 `capabilities.type=chat` 且 `policy.state != disabled` 的条目，并剔除 `copilot-search-*` / `exec-agent-*` / `trajectory-compaction` 等内部子代理工具项（vendor=Fireworks 且 family 命中前缀）。实测生产 57 条 → 25 条可用对话模型。注意 `model_picker_enabled` 全为 false，不可用作判据。
- `models.go`：`allConfiguredModels` 改为动态优先，不再无条件并入兜底清单，避免上游已下线模型污染宿主全局模型目录与跨 provider 路由选模。
- `models.go`：落地此前形同虚设的 `dynamic_models` 配置开关（原字段被解析但全仓无读取点），显式 `false` 时跳过上游请求。
- `authfile.go`：`saveAccount` / `saveTokenAccount` 落盘不再写入兜底清单，避免旧清单遮蔽动态发现。
- `oauth.go`：`handleRefreshAuth` 在刷新令牌时顺带刷新模型清单，使存量账号的失真 `models` 字段能被自动纠正。
- `management.go`：面板 `/accounts` 的模型列表改用 `authModelsForIndex`（动态优先），不再直读账号落盘字段。

**Feat**

- 新增面板「测试模型」功能：账号卡片「测试」按钮 → 模型选择弹窗 → 点选模型发起一次真实推理并就地展示结果。后端新增 `POST /test-active`（`active_ping.go`）。
- 探活契约对齐既有插件成熟策略：流式请求 `stream=true, max_tokens=1`，收到首个有效输出帧即返回（TTFT 级），后台异步关流；拦截 HTTP 200 下的 `event: error` 与 JSON `error` 字段，不盲判首包成功。

**Test**

- `models_discovery_test.go` 新增 5 例：真实对话模型保留（含 policy 缺省）、不可用条目剔除（embeddings/completion/disabled/三类内部工具项）、内部工具项判定必须同时满足 vendor 条件、流式错误帧拦截（含正常帧与 `[DONE]` 不误报）、错误文本截断边界。
- 哨兵反证 3 组：关闭 policy 过滤 → `disabled policy` 用例精确 FAIL；关闭内部工具项过滤 → 3 个工具项用例精确 FAIL；关闭 `event: error` 分支 → 错误帧用例精确 FAIL；恢复后全绿。
- 真实 `.so` ABI 探针 5/5 PASS（`test/copilot/models_and_test_probe.py`）：注册表含 `/test-active`、`model.for_auth` 与 `model.static` 出口非空、`POST /test-active` 入参校验、`GET /models` 可达。
- 生产真实数据验证：上游 `/models` 57 条经过滤判据得 25 条可用对话模型；真实凭据流式探活 `status=200`、首包 1313ms、非错误帧。

**涉及文件**：`copilot/`（`models.go`、`authfile.go`、`management.go`、`oauth.go`、`active_ping.go`、`models_discovery_test.go`、`panel.html`、`VERSION`、`main.go`、`CHANGELOG.md`、`test/copilot/models_and_test_probe.py`）。

## 0.1.2

修复插件调度器越界返回自有账号 ID 导致宿主判 `unknown auth id` 的问题：`handleSchedulerPick` 原先忽略宿主下发的候选集，直接返回自有账号池的 `AuthID`（`copilot-<hash8>`，不带 `.json`），而宿主候选 ID 带 `.json` 后缀，逐字校验失败后回落内建选择器，插件调度意图被完全忽略（3 小时窗口累计 1165 条告警）。现改为只在宿主候选集内取号并原样返回候选 ID。

**Fix**

- `scheduler.go`：`handleSchedulerPick` 改为按 `req.Candidates` 过滤（本 provider、未禁用、不在冷却期），在候选集内轮转取号并原样返回 `candidate.ID`；非本 provider 候选或无可用候选时返回 `Handled:false` 交由宿主内建调度器处理。新增 `candidateDisabled` 对齐宿主候选禁用语义（`Status=disabled` 与 `Metadata.disabled`）。

**Test**

- `scheduler_test.go` 新增 6 例：候选 ID 逐字返回（回归判据）、外部 provider 让位、禁用跳过与全禁用让位、冷却跳过与全冷却让位、空候选让位、候选集内轮转。
- 真实 `.so` ABI 探针 5/5 PASS（WSL gcc 构建 + ctypes 宿主桥驱动完整生产路径，覆盖单测无法触达的 ABI 边界与 JSON 字段名契约）。
- 哨兵反证：注入 `strings.TrimSuffix(candidates[idx].ID, ".json")` 后 4 例精确 FAIL，恢复后全绿。

**涉及文件**：`copilot/`（`scheduler.go`、`scheduler_test.go`、`VERSION`、`main.go`）。

## 0.1.1

修复宿主管理页「登录」弹窗不显示设备码的问题：宿主 `ServePluginAuthURL` 只回传 `{status, url, state}`，插件返回的 `Metadata.user_code` 被写进宿主 session store 而不回传前端，导致用户在弹窗里看不到设备码、无法完成授权。现将登录启动返回的 `URL` 指向插件自有 resource 前缀的桥接页，宿主弹窗打开的即该页，页面内直接展示设备码与授权入口。

**Fix**

- `oauth.go`：`handleStartLogin` 返回的 `URL` 由 GitHub 授权地址改为桥接页相对路径 `/v0/resource/plugins/copilot-provider/login-bridge?state=<state>`；`Metadata` 新增 `bridge_url`，`prompt` 改为指引用户在弹窗页面内完成授权。
- `management.go`：`Resources` 注册 `/login-bridge`（无 `Menu`，不进管理菜单）；`handleManagement` 在 resource 前缀下分发该路径到桥接页。

**Feat**

- `bridge.go`：新增设备码桥接页渲染（`loginBridgeURL` / `handleLoginBridge` / `loginBridgePage` / `htmlEscape`）。页面展示大号可选中设备码、三步授权指引与 GitHub 授权页入口；仅做展示，不驱动轮询、不消费登录会话，授权结果轮询与凭据落盘仍由宿主 `get-auth-status` → `PollLogin` 链路负责（桥接页若抢先调用 `pollDeviceLogin`，会在成功时消费掉会话导致宿主拿不到凭据）。

**Test**

- `bridge_test.go` 新增 3 例：桥接页渲染设备码与授权地址且 HTML 完整未截断、缺 state / 未知 state / 超时 state 的守卫与超时会话清理、resource 路由分发到桥接页。
- `main_test.go` 的 `TestDeviceCodeFlowStartAndPoll` 补充断言：启动响应的 `URL` 必须是桥接页相对路径前缀，且 `Metadata.bridge_url` 与之一致。
- 单测总数 30 例（`main_test.go` 17 + `management_test.go` 10 + `bridge_test.go` 3）。

**涉及文件**：`copilot/`（`bridge.go`、`bridge_test.go`、`oauth.go`、`management.go`、`main.go`、`VERSION`、`main_test.go`）。

## 0.1.0

首发版本：把 GitHub Copilot 订阅额度封装为 CPA 原生 Provider，覆盖设备码登录、令牌保活、模型发现、多账号池与管理面板。

**Feat**

- 新增 `copilot-provider` 插件骨架：C ABI 入口、注册元数据与能力声明（model provider / auth provider / executor / scheduler / management）。
- 实现 GitHub OAuth Device Flow 登录：设备码申请、授权轮询、账号落盘（`copilot-<hash8>.json`）。
- 实现 Copilot 内部令牌交换与保活：`GET /copilot_internal/v2/token`，按 `refresh_in` / `expires_at` 的 80% 提前重取。
- 实现客户端指纹注入（`upstream.go`）与上游主机名按账号类型推导。
- 实现模型发现（`/models` + 5 分钟缓存）、`filterExcludedModels` 排除治理与模型别名解析。
- 实现非流式 / 流式转发执行器，流式遇 `[DONE]` 立即终止。
- 实现多账号调度、故障转移（429/5xx 换号重试 + 15 秒冷却）与配额查询（`/copilot_internal/user`）。
- 实现管理面板（账号卡片、配额进度条、设备码登录、Token 导入、启停与删除）。
- 持久化上游 `endpoints.api` 权威推理端点，端点解析优先级为配置 `base_url` > 账号端点 > 账号类型推导（含 `*.githubcopilot.com` 域校验）。
- 额度快照补 5 分钟 TTL 过期判定，账号删除时同步失效额度缓存。

**Test**

- 单测 27 例（`main_test.go` 17 + `management_test.go` 10），含设备码全链路桩测试、端点优先级与负例校验、调度轮询、额度 TTL 与删除清缓存。
- 面板 DOM 回归脚本 `test/copilot/panel_render_repro.mjs`；宿主 ABI 桥探针 `test/copilot/host_bridge_probe.py`。

**涉及文件**：`copilot/`（`main.go`、`authfile.go`、`token.go`、`oauth.go`、`upstream.go`、`quota.go`、`management.go`、`scheduler.go`、`executor.go`、`stream.go`、`models.go`、`panel.go`、`panel.html`、`util.go`、`failover.go`、`VERSION`、`go.mod`、`main_test.go`、`management_test.go`）、`assets/icons/Copilot.png`、`registry.json`、`.github/workflows/build.yml`、`README.md`。
