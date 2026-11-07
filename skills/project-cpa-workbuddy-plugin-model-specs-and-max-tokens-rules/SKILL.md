---
name: project-cpa-workbuddy-plugin-model-specs-and-max-tokens-rules
description: 当开发、接入新 AI 服务商插件（如 workbuddy、workbuddy-ai、qoder-ai、qoderwork、traework、zcode）、新增模型接入，或排查「模型推理卡死 50~60 秒后断开」「长思考模型输出被截断（finish_reason=length 且正文为空）」「客户端写死 max_output_tokens 导致思维链吃满配额」「上游报 400 max_tokens 参数非法」时强制自动触发。本规则的方向是「最小干预」：显式值原样透传，只做别名翻译与 GPT 系下限，坚决禁止插件层抬升、放宽、按统一上限截断或补足输出上限。
---

# cpa-workbuddy-plugin max_tokens 最小干预规范

## 0. 本规则的方向（先读这一条）

**插件层不得替客户端决定输出上限。** 2026-11-07 起，本仓库七个插件
（workbuddy / workbuddy-ai / qoder-ai / qoderwork / traework / zcode / qwenwork）统一
采用 GitHub 上七个 WorkBuddy 反代项目的交集做法：

> 显式传值原样透传；只在「客户端未传」时按各插件自身上游语义决定；
> 只保留两条有硬证据的归一化（别名翻译、GPT 系下限 16）。

历史上本仓库曾自行设计过四类「输出上限控制」，全部已删除，不得恢复：

1. 统一 `minMaxTokensFloor = 32768` 最低输出底线；
2. `defaultMaxOutputTokens` / `defaultReasoningMaxTokens = 128000` 统一上限；
3. 推理模式下把客户端 `<= 8192` 的显式值放宽到 128000；
4. 对外广播的 `MaxCompletionTokens` 无条件抬至 128000。

## 1. 删除依据（改动前必读，防止改回去）

### 1.1 本方生产事故（2026-11-07）

`max_tokens=2000` 被旧逻辑放宽到 128000，上游实际生成 12000~22000 tokens、
耗时 80~145 秒，被调用方判为超时/卡死。输出上限是调用方的显式契约，
插件单方面放大 64 倍属于越权。

同类事故在 traework 非流式路径也发生过：显式 2000 被地板抬回 32768，
实测生成 11203 tokens、耗时 72 秒。

### 1.2 社区实测事实

| 事实 | 来源 | 对旧设计的含义 |
|---|---|---|
| 上游对越界 `max_tokens` 是**静默钳制**，不返回 400 | linguo2625469：16 个 CN 模型中 8 个被钳制，最狠声称 1M 实际 32K | 「防上游 400」这个前提不存在，Safety Clamp 防错了对象 |
| 思考与最终回答**共享** output 预算，上游无独立思考上限字段 | linguo2625469 `internal/upstream/client.go:1283`；ardeyouxipianyi `test_reasoning_tokens_ride_inside_completion` | 抬 `max_tokens` 不是解决正文被思考吃光的杠杆 |
| `max_tokens` **不计入**上下文窗口 | sixzjd README：`prompt≈70000 + max_tokens=64000` 通过 100000 窗口 | context 与 output 是两套独立预算 |
| `/v1/models` 声称的输出上限普遍虚高 | linguo2625469 probe 结论 | 按声称值 clamp 无意义 |
| GPT 系 `max_tokens < 16` 返回 400 `code=11133` | linguo2625469、ardeyouxipianyi 两家独立实测（15 拒、16 过） | 这是唯一需要插件兜的下限 |

### 1.3 七家项目的策略分布

| 项目 | 显式值处理 | 未传处理 | 上限处理 |
|---|---|---|---|
| tonny0812/workbuddy2api | 纯透传 | 不补 | 无 |
| lovingfish/workbuddy-cliproxy | 无相关代码 | — | — |
| dingminhua/dsh-connect-workbuddy | 明确不改 | 不补 | 无 |
| sixzjd/dsh-workbuddy-connect | 别名翻译 | 不补 | 无 |
| linguo2625469/workbuddy2api-panel | 别名翻译 + GPT<16→16 | 不补 | 只抬下限 |
| ardeyouxipianyi/workbuddy2api-hub | 别名翻译 + GPT<16→16 | 按目录补 | 探测值只标注不覆盖 |
| ithtelab/workbuddy-manager | 三字段统一钳 1e6 | 不补 | 防 int32 溢出 |
| **本仓库（改造后）** | 别名翻译 + GPT<16→16 | 按各插件上游语义 | 不预截断 |

**七家无一做「抬升显式小值」或「统一地板」。**

## 2. 核心机制：最小干预三原则

### 2.1 原则一：显式值零干预

客户端传入的 `max_tokens` 一律原样发给上游：

- 不按 `reasoning_effort` 或模型名放宽；
- 不套最低地板；
- 不按统一上限或模型上限预截断（上游静默钳制，预截断只会掩盖真实规格）。

### 2.2 原则二：未传时按各插件自身上游语义

| 插件 | 未传时的行为 | 依据 |
|---|---|---|
| workbuddy / workbuddy-ai / zcode / qwenwork | 不写该字段 | 上游有自己的默认值 |
| qoder-ai / qoderwork | 不覆盖模板，保留 `baseprompt.json` 的 32768 | 该值来自官方客户端模板，非自研魔数 |
| traework（流式） | 补 `streamDefaultMaxTokens` | trae 上游对未传会给极小上限，长任务刚开口就 done（独立实测） |
| traework（非流式） | 不写该字段 | 沿用既有语义 |

### 2.3 原则三：只保留两条有硬证据的归一化

1. **别名翻译**：`max_completion_tokens` → `max_tokens`。
   - 上游只认 `max_tokens`，别名透传会被忽略后回落上游默认（实测 32000）；
   - 显式 `max_tokens` 优先，别名无论是否翻译一律删除；
   - 非正整数（0 / null / 负数 / 字符串 / 小数）不翻译，不把畸形值洗成合法值。
2. **GPT 系下限**：`gpt-` 前缀模型且 `< 16` 时抬至 16。
   - 两家独立实测：15 拒、16 过，`code=11133 model_param_invalid`；
   - 非 GPT 模型无此限制，不得套用。

## 3. 插件实施规范

### 3.1 策略层（`model_specs.go`，六插件逐字节同源）

```go
const defaultContextWindow int64 = 1048576 // 仅用于 ContextLength 元信息兜底
const gptMinMaxTokens int64 = 16

func normalizeMaxTokensInPlace(obj map[string]any) bool {
	changed := false
	// 1. 别名翻译：显式 max_tokens 优先，别名一律删除
	if alias, has := obj["max_completion_tokens"]; has {
		delete(obj, "max_completion_tokens")
		changed = true
		if _, explicit := obj["max_tokens"]; !explicit {
			if v, ok := positiveIntValue(alias); ok {
				obj["max_tokens"] = v
			}
		}
	}
	// 2. GPT 系下限
	if v, ok := positiveIntValue(obj["max_tokens"]); ok && v < gptMinMaxTokens {
		model, _ := obj["model"].(string)
		if strings.Contains(strings.ToLower(model), "gpt-") {
			obj["max_tokens"] = gptMinMaxTokens
			changed = true
		}
	}
	return changed
}
```

### 3.2 请求体层

- **workbuddy / workbuddy-ai / zcode / qwenwork**：`prepareUpstreamBody`（或 `mutateModel`）调用 `normalizeMaxTokensInPlace`。
- **qoder-ai / qoderwork**：`body.go` 里 `if req != nil && req.MaxTokens > 0 { params["max_tokens"] = int64(req.MaxTokens) }`，未传不覆盖模板。
- **traework**：`upstream.go` 里 `if maxTokens > 0 { payload["max_tokens"] = maxTokens } else if stream { payload["max_tokens"] = int(streamDefaultMaxTokens) }`。

### 3.3 元数据层（`models.go`）

- 上游给了 `maxOutputTokens` → 原样广播；
- 上游没给 → 留 0（未知），**不猜值、不虚报**；
- 配置来源的 `max_tokens` → 原样透传，不过地板；
- qoder 系上游只提供 `max_input_tokens`，输出上限如实留空。

### 3.4 探活负载

固定 `max_tokens = gptMinMaxTokens`（16）：探活只需首帧，同时避开 gpt- 系列
的 11133 下限。禁止再用「写 1 靠地板抬升」的写法。

## 4. 验收标准

收口前必须在 `max_tokens_test.go` 覆盖以下断言：

1. **生产事故回归**：推理模式 + 显式 2000 → 输出 2000（不得变成 128000/32768）；
2. **保守值不放宽**：推理模式 + 显式 8192 → 输出 8192；
3. **不套地板**：显式 100 → 输出 100；
4. **不预截断**：显式 200000 → 输出 200000；
5. **未传语义**：按第 2.2 节各插件自己的约定断言（不写 / 模板值 / trae 缺省值）；
6. **别名翻译**：仅传别名 → 译为 `max_tokens`；两者都有 → 显式优先、别名删除；
7. **畸形值不洗白**：别名为 0 / 负数 / 字符串 / 小数 → 不写 `max_tokens`；
8. **GPT 下限**：`gpt-` 模型传 5 → 16；非 GPT 模型传 5 → 5。

另需保证：

- 七个插件 `model_specs.go` 的 md5 完全一致（逐字节同源）；
- 全仓库不再出现 `applyMaxTokensFloor` / `minMaxTokensFloor` /
  `defaultMaxOutputTokens` / `defaultReasoningMaxTokens` /
  `computeAdaptiveMaxTokens` / `lookupOfficialModelSpec` 的**代码引用**
  （注释中说明历史可以保留）。

## 5. 已知遗留（不阻塞，待决策）

对外广播的输出上限仍是上游声称值，而社区实测多数模型被静默钳制在 32K。
可选做法：照 linguo2625469 引入探测脚本，测出真实钳制值后**只标注不覆盖**
（`output_clamp` / `max_output_tokens_clamped`）。当前按「不虚报也不额外猜」
处理，即原样透传上游值。
