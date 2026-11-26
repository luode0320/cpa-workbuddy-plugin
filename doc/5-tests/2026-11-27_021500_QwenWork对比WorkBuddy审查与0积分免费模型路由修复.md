# 2026-11-27 QwenWork 对比 WorkBuddy 审查与 0 积分免费模型路由修复

## 结论先行

QwenWork 存在一个产品前提错误：代码从立项起就写死「QwenWork 没有免费模型，0 积分账号必须禁用」。本轮用真实账号实测证明这个前提已过时——上游 chat-modes 目录里有免费模型（`minimax-m3.1-flash`，`price_factor=0`），客户端截图也给它标了「免费」角标。本轮已把路由判据从「账号级一刀切」改成「按请求模型的模型级准入」：0 积分账号仍可用免费模型，付费模型才走原来的积分红线。

同时对比 WorkBuddy 做了一轮只读发散审查，发现两个独立的确凿 P1（保活 token 家族、刷新绕过宿主桥），本轮按用户裁定只做 0 积分修复，P1 另列为待裁决项。

## 影响范围

- 受影响：所有 QwenWork 0 积分（含 `quota.exceeded`、`expires`）账号的路由准入与生命周期。
- 用户可见变化：0 积分账号不再被物理写 `disabled:true`；请求免费模型时这些账号可被正常调度；请求付费模型时才被排除。
- 未变：付费模型的积分红线（默认 20）、冷却、慢速让位、session 粘性。

## 环境与方法

- 只读发散：对比 `workbuddy/` 与 `qwenwork/` 的积分、保活、流式、容错链路，两个只读 sidecar 并行。
- 真实探测：用本地导出凭据 `qwenwork-credentials-2026-11-26.json` 直连 `gateway.qwenwork.cn`。
- 本地验证：`python scripts/cgo-shim-build.py qwenwork`（build + vet + test）+ 哨兵反证。

## 一、实测证据

### 1. chat-modes 全量目录（14 个模型）

`GET /api/desktop/chat-modes` 返回 HTTP 200，`schema_version=2`，catalog 14 条。红框里的 4 个模型全部在内：

| key | format | price_factor | enable |
| --- | --- | --- | --- |
| qwen4-max | anthropic/messages | 1.6 | true |
| qwen3.8-max-preview | openai/chat-completions | 1.8 | true |
| qwen3.8-flash | openai/chat-completions | 0.1 | true |
| minimax-m3.1-flash | anthropic/messages | **0** | true |

**免费判据**：`price_factor=0` 且 `enable=true`，唯一命中 `minimax-m3.1-flash`。`show_zero_price_factor` 对全部 14 个模型都是 `true`，不是免费判据。

### 2. 免费端点不存在

MaaS 网关 `GET /v1/models` 与 `/models` 均返回 404。真正的全量模型源仍是 `chat-modes`；此前"换回全量端点"的方向不成立，真实 bug 是解析器按 `format` 前缀过滤掉了 anthropic 格式的模型。

### 3. 0 积分账号仍可服务免费模型

用有积分账号直连 MaaS 网关，`minimax-m3.1-flash` / `qwen4-max` / `glm-5.4` 均 HTTP 200 正常推理。结合 `price_factor=0`，确认 0 积分账号服务免费模型在链路上可行。

### 4. refresh token 家族

导出凭据 `refreshToken` 前缀为 `ory_rt`。直接 `POST /api/v1/deviceToken/refresh` 返回 HTTP 200 并轮换成功，但代码只认 `drt-` 前缀。

## 二、本轮修复（方案 A：模型级准入）

### 改动文件

| 文件 | 改动 |
| --- | --- |
| qwenwork/free_models.go（新增） | 免费模型注册表、`isFreeModel`、`hasFreeModels`、`accountUsableForModel` |
| qwenwork/free_models_test.go（新增） | 兜底/动态判据、price_factor 解析、0 积分路由准入单测 |
| qwenwork/models.go | `parseChatModesCatalog` 不再按 format 过滤；新增 `parseFreeModelIDs`；`callModelsAPI` 成功后注册免费模型；`wbModels()` 补齐 14 个模型；过时不实注释修正 |
| qwenwork/scheduler.go | pick 阶段按 `accountUsableForModel(c.ID, req.Model)` 判定，cands 标记 Exhausted |
| qwenwork/active_auth.go | `activeAuthCandidate` 加 `Exhausted`，picker 消费该标记；过时不实注释修正 |
| qwenwork/session_auth.go | session 候选过滤沿用 Exhausted 标记 |
| qwenwork/failover_retry.go | `pickNextAuth` 变参 model，按模型判定积分准入 |
| qwenwork/stream.go | 换号调用点传入请求模型；`collectUpstreamStreamOpenAI` 增 `requestedModel` 参数 |
| qwenwork/main.go | 换号调用点传入 `req.Model`；配置描述文案修正 |
| qwenwork/lifecycle.go | 耗尽账号有免费模型时不再物理禁用、已禁用账号恢复启用；过时不实注释修正 |
| qwenwork/policy.go | 过时注释修正 |
| qwenwork/models_config_test.go / qwenwork_contract_test.go / session_auth_test.go | 测试对齐新判据 |

### 验证

- `python scripts/cgo-shim-build.py qwenwork`：build + vet + test **全绿**。
- 哨兵反证：把 `accountUsableForModel` 的免费放行改坏 → `TestAccountUsableForModel` FAIL（exit 1），恢复后重跑全绿。
- gofmt：全部改动文件干净；UTF-8 无 BOM；行尾与原文件一致；零行尾空白。
- 临时探针文件已全部清理。

## 三、审查发现（13 维矩阵摘要）

| 维度 | 结论 |
| --- | --- |
| 安全 | 无新增问题；凭据只用于只读探测，探针不回显 token 原值 |
| 性能 | 无问题 |
| 逻辑正确性 | 发现并修复 0 积分一刀切；另发现 2 个独立 P1（见下） |
| 边界与空值 | `parseFreeModelIDs` 空集/禁用/空 key 已覆盖；无新问题 |
| 并发与竞态 | 无独立发现 |
| 异常与错误处理 | 无独立发现 |
| 数据精度与类型 | price_factor 用 float64 精确判 0，无精度问题 |
| 资源与生命周期 | 临时探针用后即删；无残留 |
| 兼容性 | 付费模型判据完全保留，无回归 |
| 可观测性 | 过时注释与配置文案已修正 |
| 配置与依赖 | 无新增依赖 |
| 业务与用户视角 | 0 积分账号可用免费模型，符合客户端行为 |
| 代码格式与风格 | 风格判据无偏离，gofmt 干净 |

### 待裁决的独立 P1（本轮未修）

- **P1-刷新 token 家族**：`qwenwork/keepalive.go:73`、`oauth.go:381` 只认 `drt-` 前缀，`ory_rt` 前缀 refresh token 直接报 `unsupported token family`。实测 `ory_rt` 可正常刷新（HTTP 200），生产 4 小时保活对当前账号全部失效。建议：增加 `ory_` 家族刷新分支。
- **P1-刷新绕过宿主桥**：`qwenwork/keepalive.go:75` 用 `sharedHTTPClient()` 而非 `hostHTTPDo`，绕过宿主代理策略与请求日志；对照 `workbuddy/keepalive.go:85` 走 hostHTTPDo。建议：改走桥并保留 raw body 供 TOKEN_EXPIRE 分类。
- **P1-模型拦截错误未包装**：`qwenwork/stream.go:43-49` 缺 WorkBuddy 的 `isModelBlockedError` + `requestScopedErrorBody`，模型拦截类 400 会被当凭据故障继续换号。建议：对齐 WorkBuddy 包装成 `invalid_request_error` 让宿主停止换凭据。
- **P1-5xx 不换号**：`qwenwork/accountFailover.go:160-165` 只判 4xx，未纳入 WorkBuddy 的 5xx 换号；上游 5xx 时同请求不换号。建议：对齐 `shouldRotateOnStatus`。

## 未覆盖与前提缺口

- 未做生产热重载与端到端验证：本轮只在本地验证代码逻辑；生产 SSH 与 management key 当前不可用。
- `lifecycle_auto` 默认关闭，本轮对 lifecycle 的修复只在开启该配置时生效；默认路径下 0 积分账号本就不被物理禁用，但调度层的一刀切已修复。
- 免费模型注册表在动态发现成功前用静态兜底 `minimax-m3.1-flash`，与当前上游一致；若上游免费模型变化且动态发现失败，兜底可能滞后。
