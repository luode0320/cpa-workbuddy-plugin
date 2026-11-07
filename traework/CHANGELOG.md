# TraeWork Plugin Changelog


## 0.5.16

### Fix - 请求体 role 前置校验（畸形请求打穿整池的根因修复）

- 背景: 0.5.15 采用「按上游报错正文匹配请求形状错误」的方案上线后，生产复验**未通过** —— 一次
  `role=wizard` 请求仍然逐个禁用 9 个账号，随后 5/5 次正常请求全部 503 auth_unavailable。
- 根因（0.5.15 方案为何无效）: 上游对同一个非法 role 存在**至少两种报错文案**：
  - `tool call failed: invalid_parameter_error:wizard is not one of ['system', ...]`（早期实测）
  - `tool call failed: The parameter \`messages.role\` specified in the request are not valid:
    invalid value: \`wizard\`, supported values are: \`system\`, ...`（本次复验实测）

  正文匹配只能覆盖已知文案，属于追着上游措辞打补丁，永远穷不尽。
- 修复: 改为**在发请求之前**按 `validOpenAIRoles` 校验消息 role，非法请求直接以
  `invalid request: messages[N].role "X" is not a valid role` 返回 400，根本不发往上游。
  合法角色表: `system` / `user` / `assistant` / `tool` / `developer` / `function`。
  校验点覆盖流式与非流式两个入口（`handleExecExecute` / `handleExecStream`）。
- 保留 0.5.15 的正文判据作为第二道防线（覆盖 role 之外的请求形状错误），但它不再是主防线。
- 测试: 新增 `TestValidateOpenAIMessageRoles`（6 组：正常/工具/非法 role/空 role/role 非字符串/空列表）。
  `cgo-shim-build.py traework` build/vet/test 全绿。

### Docs - 附：kimi-k3 对 max_tokens 无反应的定性（无代码改动）
- 背景：2026-11-08 生产压测发现 traework 的 `kimi-k3` 对 `max_tokens` 完全无反应。
- 实测（同一 prompt「请详细介绍一下人工智能的发展历史」）：
  | max_tokens | 正文 | output_tokens | finish_reason |
  |---|---|---|---|
  | 5 | 8522 字符 | 6023 | stop |
  | 20 | 13017 字符 | 8743 | stop |
  | 50 | 11431 字符 | 6791 | stop |
  | 5000 | 1091 字符 | 678 | stop |
- 插件侧已排除：新增 `TestBuildTraePayload_KimiK3IncludesMaxTokens`（mt=5/20/50/5000 四组），
  断言 `buildTraePayload` 产出的 `max_tokens` 原样存在且未被改写、`config_name` 同时在位；
  `grep kimi|kmodel traework/*.go` 零命中，无模型级特判。
- 对照：同批 `deepseek-flash` / `qwen3.8-flash` 在短任务下正常受控（54 / 20 字符），
  说明是**模型级差异**而非整个上游忽略。
- 处置：属上游模型行为，插件层**不做人为截断**（会破坏正文完整性，且无法区分
  「模型自己停」与「被截断」）。客户端若需硬约束请改用遵守 max_tokens 的模型。

## 0.5.15

### Fix - 畸形请求不再逐个禁用整池账号 + max_completion_tokens 别名识别

- 背景（2026-11-07 生产高强度压测实测）: 发一次 `role=wizard` 的请求，上游返回 HTTP 200 +
  `tool call failed: invalid_parameter_error:wizard is not one of [...]`。旧逻辑把它按账号级
  失败记账并逐个写盘禁用，撞到第 9 个账号才因换号预算耗尽停下，随后**正常请求全部返回 503
  auth_unavailable**。kimi-k3 与 DeepSeek-V4-Pro-Official 各自被打穿一遍；qoderwork 侧同型
  （一次畸形请求打穿 8 个号，基线恢复后 5/5 次正常请求仍失败）。
- 根因: 失败分类只按 HTTP 状态码判账号级（400/422 才认作请求形状问题），而 Trae 把客户端
  请求体错误包在 **HTTP 200 的 `event:error` 帧**里，状态码判据完全命中不到。
- 修复内容:
  - 新增 `requestShapeFailureMarkers` 与 `isRequestShapeErrorBody(body)`：按正文识别
    `is not one of` / `invalid_parameter` / `invalid request` / `cannot unmarshal` /
    `missing required` / `tool call failed` 等客户端请求体形状错误。
  - `classifyUpstreamFailure` 在模型级故障判定之后、所有账号级判据之前新增豁免分支，
    **严格限定 HTTP 200**：status==0（传输层失败，body 是插件本地错误文本）、5xx、401/403/
    404/405、402 一律照旧记账 —— 既有 `TestClassifyUpstreamFailureMatchesLegacyCountGate`
    逐状态锁定了该等价性，放宽会漏掉真正的坏账号。
  - `openAIRequest` 补 `max_completion_tokens` 字段，新增 `effectiveMaxTokens` 统一取值
    （显式 max_tokens 优先，其次别名，非正值视为未传），流式与非流式两处 `buildTraePayload`
    调用统一改走它。此前只解析 `max_tokens`，DSH / Codex 系新客户端只发别名时该值被
    `json.Unmarshal` 静默丢弃，等价于客户端从未声明上限（实测请求 12 token 实际产出
    1091 字符 / 1119 tokens）。
- 测试: 新增 `request_shape_test.go`（10 组正文形态 + 分类层接线断言 + 401 反证）与
  `max_tokens_test.go` 的两组别名用例（取值优先级 + 真实 JSON 反序列化路径）。
  变异验证：停用 `isRequestShapeErrorBody` 判定后 3 个用例 KILLED；删除别名分支后
  3 个用例 KILLED。`cgo-shim-build.py traework` build/vet/test 全绿。

## 0.5.14

### Fix - 失败禁用与手动启用联动（与 workbuddy 0.21.13 同批整改）

- 背景: workbuddy 侧 2026-11-07 生产事故（一次渠道级 11128 把 19 个账号逐个写盘禁用；手动启用后失败不再禁用）在本插件存在同型缺陷，按同一口径整改。
- 修复内容:
  - 新增 `isChannelBlockError`（11128 + `unapproved channel` / `Illegal API invocation`）→ `failureScopeUpstreamBusy`：不记冷却、不写盘禁用、不换号；普通 11128 仍按账号×模型拦截处理。executor.go 六处 11128 分支统一改走 `noteUpstreamBlockHit`。
  - 恢复路径无论宿主 API 成败都落盘清理 `failover_disable*` 三键；此前只在 API 失败时写盘，`disabled:false + 旧标记` 会让探活反复「恢复」并清零连续失败计数。
  - 探活候选判据收紧为「盘上 `disabled:true` 且带本机制标记」或「sidecar 记禁用」，并按 `auth_index` 去重。
  - 对账逻辑重写：按 `auth_index` 去重读盘、只认盘上确实 `disabled:false` 的号，清本地集合 + sidecar + 盘上陈旧标记，且只对确曾归属本机制的号重置连续失败计数；新增独立 2 分钟对账循环（启动即跑一轮）。
  - 新增全池保底：禁用会把池内可用账号降到 0 时拒绝写盘（单账号池、宿主列表不可用时不阻断）。
  - 宿主已禁用但盘上无本机制标记（归属丢失）时清掉插件侧 sidecar 与本地记录。
- 验证: `python scripts/cgo-shim-build.py traework` build / vet / test 全绿；新增 `failover_sync_test.go` 12 组用例。
- 涉及文件: `failure_class.go`、`model_circuit.go`、`executor.go`、`watchdog.go`、`failover_disable.go`、`failover_disable_test.go`、`failover_sync_test.go`、`test/traework/failover_marker_restore_test.go`。

## 0.5.13

### Feat - 模型目录双通道 + 目录过滤、签到 status 预查与 9074 冷却、版本头对齐真机

- 模型目录: `get_detail_param` 由单通道 `solo_work_lite` 改为 `chat_v3` + `solo_agent` 并发拉取，按 `chat_v3` 优先顺序确定性合并；补齐此前完全缺失的过滤（`is_invisible_to_user`、`config_switch=false`、`display_config.is_custom_model`、内部 usage 标签、内部前缀、`-auto` 后缀），避免内部模型混入目录。
- 通道归属: 目录合并时记录每个模型的上架通道（`traeModelFunctions`），`resolveModelOptions` 在聊天时选用同一通道——上游按「配置 × 通道」判定上架，拿未上架通道的模型去问会回 4001；无记录时仍回退 `solo_work_lite`。
- 签到: 新增 `checkin_credits/status` 预查，已签或 `enable=false` 直接跳过，不再对已签账号发无效 claim；9074 后按账号记冷却（本地 00:00-00:10 早窗 12 分钟、平时 55 分钟），冷却期内零请求，账号间不连坐；请求头补齐 `x-app-version-code` / `x-ide-version-code` / `X-User-Region: CN` / `x-device-cpu`。
- 版本头: `x-app-version` 默认 `0.1.58` → `1.107.1`；`x-app-version-code` 由「当天日期」改为固定 `20260901`——上游按该数值做灰度（实测 20260401 只回 42 条目录、≥20260901 才回完整目录），且必须为纯整数，日期值会与版本号不自洽。
- 测试: `models_dynamic_test.go` 覆盖双通道合并、过滤与通道映射；`upstream_model_test.go` 新增通道优先用例并隔离全局映射；`test/traework/checkin_device_rotation_test.go` 区分 status 与 claim 的请求序列。
- 依据: GitHub `JsonLing/trae-cn-provider`（chat_v3 + solo_agent 双查与多层过滤）、`crush1994/Trae-AutoCheckin`（status 预查、9074 换号与冷却）、`bluechonk/trae-credential-reverse-engineering`（UG checkin 端点与设备头）。
- 说明: 本版包含 0.5.12 的「显式 max_tokens 零干预」改造（基于 `aee52c0`）。

## 0.5.12
