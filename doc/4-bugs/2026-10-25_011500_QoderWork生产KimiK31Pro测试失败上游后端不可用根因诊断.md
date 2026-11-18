# QoderWork 生产 Kimi-K3.1-Pro 测试失败（provider_error: All backends failed）根因诊断

- 编号：BUG-QODERWORK-KIMI-BACKENDS-FAILED-20261025
- 发现时间：2026-10-25 00:57（北京时间，用户报障附面板截图：`qoderwork-provider` 账号 `ud5cdb65b` 测试 `Kimi-K3.1-Pro` 返回 `upstream 200: provider_error: AI backends failed`）
- 判定时间：2026-10-25 01:15（北京时间）
- 状态：**已定位根因，非插件缺陷，属上游服务侧降级；无代码改动**
- 关联：`doc/4-bugs/2026-10-15_160000_Qoder排队帧被误判账号级故障且跨provider状态码通道缺失根因诊断.md`（同类「上游容量非账号故障」判定链）、`doc/4-bugs/2026-10-20_021500_Qoder模型清单跨账号缓存污染根因诊断.md`（0.10.1 修复的上游模型改名上下文）

## 1. 现象

生产 CPA 面板（`https://cpa.luode.vip/management.html`）对 qoderwork 账号 `ud5cdb65b` 的 `Kimi-K3.1-Pro` 发起「测试」，返回：

```
活跃测试失败 (模型: Kimi-K3.1-Pro): upstream 200: provider_error: AI backends failed
```

要点：HTTP 状态是 **200**，失败信息在响应体内（`provider_error` 前缀由 Qoder 网关写入），并非插件自造文案——全仓库 `grep "AI backends failed"` 零命中。

## 2. 取证与证据链

生产 qoderwork 版本 `0.10.1`（`plugin hot reloaded active_version=0.10.1 retired_version=0.10.0`，2026-10-25 00:31:58），容器 `cli-proxy-api` Up 47 小时未重启。

### 2.1 排除「模型名回译失效」（0.10.1 改动回归）

0.10.1 的主修正是「对外用上游 `display_name`，并新增运行时映射回译」。若回译缺失，客户端按 `Kimi-K3.1-Pro` 请求会被透传上游并静默落到 `auto`（HTTP 200 无报错，但参数与计费全错）。实测对照：

| 请求模型名 | 结果 |
| --- | --- |
| `Kimi-K3.1-Pro`（官方名） | `upstream 200: provider_error: All backends failed` |
| `kmodel_latest`（内部代号） | 同一时刻**同样** `All backends failed` |
| `kimi-k3.1-pro` / `KIMI-K3.1-PRO` / `Kimi-k3.1-Pro` | 与 `Kimi-K3.1-Pro` 完全一致 |
| `Kimi-K3`（旧展示名） | 同样进入 `provider_error` 分支 |

⇒ 官方名与内部代号在**同一秒内同成同败**，错误形态与模型名无关，回译链路正常（生产 `/models?auth_index=bc468df5b3d82f1d` 返回 20 条含 `Kimi-K3.1-Pro` / `kmodel_latest` 成对条目）。

### 2.2 排除「插件侧状态污染（冷却/禁用）」

- 18 个账号 `disabled` 全为 False，`exhausted_count=10`（Free 档耗尽），非本轮成因。
- 账号物理文件（`.../auths/qoderwork-019fd0a6-...cdb65b.json`）顶层 `failover_disable` / `failover_disable_model` / `manual_disable` / `daily_cooling_until` **全部不存在**，说明无写盘禁用。
- 面板 `failover_disabled=true` 是 `isFailoverDisabledLocal` 的内存集合（`panel.go:230`），随进程重启清空；实测该账号在测试中途仍能成功（见 2.3），证明冷却不构成阻断。

### 2.3 决定性证据：同账号同模型秒级抖动

对 `bc468df5b3d82f1d`（`ud5cdb65b`）连续采样 `Kimi-K3.1-Pro`，每 8 秒一次：

```
01:13:15 ok（1.104s）
01:13:23 provider_error: Error in upstream response
01:13:32 provider_error: All backends failed
01:13:40 provider_error: All backends failed
01:13:48 provider_error: All backends failed
01:13:57 provider_error: All backends failed
```

同一账号、同一模型、间隔 8 秒内既成功又失败，且错误文案在 `Error in upstream response` 与 `All backends failed` 之间漂移。**账号级状态（额度/令牌/冷却）不可能在 8 秒内反复变化**，故排除账号侧。

### 2.4 跨账号对照：模型权益与后端可用性双重因素

对 6 个未禁用账号测同一 `Kimi-K3.1-Pro`：

| 账号 | 结果 | 判读 |
| --- | --- | --- |
| `ud5cdb65b` | 成功 1.169s | 有权益 + 后端此刻可用 |
| `u475788f0` | 成功 543ms | 同上 |
| `u806974a3` | 成功 1.82s | 同上 |
| `udbee4bb0` | `upstream 200: 112: {"pricingUrl":"https://qoder.com.cn/pricing?client=qoder"}` | **该账号无此模型权益**（引导购买） |
| `ue878e781` | `upstream 200: 112: {...pricing...}` | 同上 |
| `u7d5058ff` | `provider_error: Error in upstream response` | 有权益但上游后端抖动 |

⇒ 至少存在两类独立上游响应：`112 + pricingUrl`（账号无权益，属预期）与 `provider_error`（上游模型后端不可用，属服务降级）。

### 2.5 上游处于整体容量受限（非 Kimi 专属）

同一时间窗真实推理（`POST /v1/chat/completions`）对照：

| 模型 | 上游返回 |
| --- | --- |
| `Kimi-K3.1-Pro` | `upstream 200: provider_error: All backends failed` |
| `Qwen3.8-Flash` | `upstream 200: 403: 10605: {"isQueued":true,"queueCount":0,"queueType":"p3","serviceAvailable":false,"subCode":"RESOURCE_LIMIT","waitTime":30}` |
| `MiniMax-M3.1-Flash` | 同上 `RESOURCE_LIMIT`（`modelKey":"mmodel"`） |
| `GLM-5.3-Flash` | `upstream 200: 112: {...pricingUrl...}` |

关键字段 `serviceAvailable:false` + `subCode:RESOURCE_LIMIT`（历史 `2026-10-15` 诊断中同字段为 `serviceAvailable:true` 才是「账号健康、仅排队」）⇒ 上游当前为**整体资源受限**，Kimi 的 `All backends failed` 是同一降级在不同模型上的表现形态（后端池全挂，队列都排不进去）。

### 2.6 插件行为正确性：轮转与记账均正常

- 单次真实请求 `b469cc61` 的宿主导出显示：连续 9 个账号全部 `All backends failed`（每个 125–201ms），最终返回 `auth_unavailable: no auth available (providers=qoderwork-provider, model=Kimi-K3.1-Pro; last upstream error: ...)`。**换号链工作正常**，失败源于所有账号同时命中同一上游故障。
- `provider_error` 文本不含任何 `hardCreditMarkers`（`policy.go:50`）、命中不了 `isQueueFrame`（`policy.go:333`，需 `isQueued`/`queueCount`/`10605`），故 `isAccountFailure(200, body)` 返回 false ⇒ 该帧**不参与账号级记账**、不写盘禁用（与物理文件核对一致），只在末次失败时按历史路径记一次 usage `failed=true`。这是符合设计的行为，不是缺陷。
- 唯一可优化点（非本次故障成因）：`provider_error` 属服务端后端不可用，当前既不算排队帧也不带 503，宿主侧不会跨 provider 轮转，最终对外呈现为 `internal_server_error` 而非 503。是否把它纳入「上游网关类失败」通道需单独评估，属体验优化而非正确性问题。

### 2.7 全局模型表缺项为另一独立现象

`GET /v1/models` 共 56 条，`owned_by=qoderwork-provider` 17 条，其中 `Kimi-K3.1-Pro`、`DeepSeek-Flash`、`GLM-5.3-Flash` **无任何 provider 归属**（`owners=[]`），而 `DeepSeek-V4-Pro` / `Qwen3.8-Max` 正常。直接请求 `DeepSeek-Flash` 返回 `unknown provider for model DeepSeek-Flash`。

- 该现象与本次 Kimi 故障**无关**（`Kimi-K3.1-Pro` 在真实请求中能正常路由到 qoderwork 并拿到上游响应，证明名字已注册）。
- 成因指向 `model.static`（无账号上下文）按「缓存中模型最多的一份」聚合（`widestCachedDynamicModels`），生产日志显示部分账号发现仅 2 个模型、部分 10 个，全局表按最大份生成，与各账号实际权益不一致属既有设计取舍；`DeepSeek-Flash` 未入表需单独排查，本轮不展开。

## 3. 根因结论

| 层次 | 结论 |
| --- | --- |
| **直接原因** | Qoder 上游网关对 `kmodel_latest` 返回 HTTP 200 + `{"provider_error":"All backends failed","error":"Error in upstream response"}`，即 Kimi 模型后端池全部不可用。 |
| **加重因素** | 同一时间窗上游整体 `serviceAvailable:false` / `RESOURCE_LIMIT`，Kimi 只是最早暴露为 `All backends failed` 的模型；`Qwen` / `MiniMax` 表现为排队帧，`GLM-5.3-Flash` 表现为 `112` 权益不足。 |
| **非成因（已排除）** | ① 0.10.1 模型名回译失效（官方名与内部代号同成同败）；② 账号额度耗尽 / 冷却 / 写盘禁用（18 号 `disabled=false`、物理文件无 `failover_disable`）；③ 插件换号链缺陷（9 号如实轮转，最终 `auth_unavailable`）；④ 该账号无 Kimi 权益（同号 8 秒内曾成功）。 |
| **插件责任** | 无。插件对 `provider_error` 的归类（不记账、不禁用、不上报 503）与既有设计一致；唯一遗留是 `provider_error` 未纳入网关类状态码通道，属可选的体验优化项。 |

## 4. 处置与建议

1. **本轮不做代码改动**：故障源在上游，改插件不能恢复；对 `provider_error` 强行打 503 只会把一次上游故障放大成跨 provider 轮转风暴，风险大于收益。
2. **观察窗口**：建议在上游恢复后复测同一组模型（`Kimi-K3.1-Pro` / `Qwen3.8-Flash` / `MiniMax-M3.1-Flash`），确认 `serviceAvailable` 回到 true；若 24 小时后仍为 `All backends failed`，再按「上游长期下线 Kimi 后端」评估是否从 `wbModels()` 与对外清单中摘除该条目。
3. **面板使用建议**：上游容量受限期间，面板「测试」失败率高属预期，不能作为账号健康判据；判据应看 `serviceAvailable` 字段与 `provider_error` 文案区分（前者账号侧，后者服务侧）。
4. **后续可选**：`112 + pricingUrl` 表示账号无该模型权益，可在面板测试结果中单独标注「无权益」而非「测试失败」，减少误判（需单独立项）。

## 5. 证据留痕

- 生产版本：`qoderwork-provider 0.10.1`（热重载 2026-10-25 00:31:58），容器 Up 47h。
- 探针命令：`POST /v0/management/plugins/qoderwork-provider/test-active`（`auth_index` + `model`）、`GET .../models?auth_index=`、`POST /v1/chat/completions`。
- 宿主日志：`docker logs --since 2m cli-proxy-api | grep Kimi-K3` → 9 条 `upstream execution failed ... provider_error: All backends failed`，随后 `auth_unavailable`。
- 全程只读操作，未修改生产配置、未重启容器、未改数据库表。
