# 2026-11-29_013000 glm-provider 模型档位动态发现与 chat_agent 补齐

## 一、问题

用户指出：「模型还是对不上, 说明我们取得有问题? 我们不希望写死, 我们希望的是可以动态获取到准确的模型, 因为未来也可能新增模型, 如果是写死的模型, 后续还是需要再改插件。」

0.2.11 虽然接入了 `assistant/list`，但档位清单仍是代码里的常量（`knownChatModes` 只认 `""` / `zero` / `deep_research`），本质仍是写死。

## 二、为什么「对不上」

### 2.1 `model` 字段被上游完全忽略

对 `assistant/stream` 注入 `model` 字段做九组实验（`meta_data.model`、顶层 `model`、真实名、`glm-4-air`、`glm-4-flash`、`glm-5.1`、甚至 `totally-bogus-model`）：

| 变体 | HTTP | 上游回报的 `model` |
|---|---|---|
| baseline（不传） | 200 | `moe_53f` |
| `meta.model=glm-4-plus` | 200 | `moe_53f` |
| `meta.model=glm-4` | 200 | `moe_53f` |
| `meta.model=glm-4-air` | 200 | `moe_53f` |
| `meta.model=glm-4-flash` | 200 | `moe_53f` |
| `meta.model=glm-5.1` | 200 | `moe_53f` |
| `meta.model=totally-bogus-model` | 200 | `moe_53f` |
| `top.model=glm-4-plus` | 200 | `moe_53f` |
| `top.model=totally-bogus-model` | 200 | `moe_53f` |

结论：`moe_53f` 是服务端内部路由名，**客户端无法通过任何 `model` 字段选择模型**。传不存在的模型名也不报错，直接被忽略。

### 2.2 真正的档位开关是 `chat_mode`

同一实验里 `chat_mode` 确实改变响应形态：

| `chat_mode` | `think` 段 | `tool_call_name` | `answer_type` |
|---|---|---|---|
| `""` | 有 | 有 | 无 |
| `zero` | 有 | 有 | 无 |
| `chat_agent` | 有 | 有 | 无 |
| `deep_research` | 无 | 无 | 有 |

### 2.3 遗漏的档位：`chat_agent`

关键取证来自**会话历史**。`GET /chatglm/backend-api/assistant/conversation/list?assistant_id=...&page=1&page_size=50` 返回的每条会话都带 `meta_data`：

```json
{"app_site":"agentmore","if_plus_model":true,"is_old_model":false,
 "subscribe_id":"deep_research","title_modified":true}
```

统计该账号 46 条历史会话：

| 字段 | 实测取值 |
|---|---|
| `chat_mode` | `chat_agent`、`normal`、`zero` |
| `subscribe_id` | `deep_research` |
| `if_plus_model` | `true` |

**`chat_agent` 是旧代码完全没有的档位**（`resolveAssistantID` 只认 `zero` 与 `deep_research`，其余一律降级成 `""`）。用户网页上用的是「智能体」档，插件却按默认档发，这就是「模型对不上」的直接原因。

## 三、实现：从历史会话动态发现档位

### 3.1 核心思路

GLM 没有模型列表接口（0.2.11 已穷举 40+ 候选路径全 404），但**历史会话记录了上游实际用过的每一个 `chat_mode`**。网页端一旦启用新档位，历史里必然出现该取值 —— 于是「扫描历史」等价于「枚举当前可用档位」，且天然自更新。

### 3.2 `discoverChatModes`

```go
func discoverChatModes(accessToken string, cfg Config) ([]string, error)
```

- 扫描范围：默认智能体 + 账号自建智能体（`discoveryAssistantIDs`）。
- 每个智能体翻最多 3 页（`page_size=50`），收集 `meta_data.chat_mode` 去重。
- 结果缓存 10 分钟（`modeDiscoveryCache`），避免每次开面板都扫历史。

### 3.3 基线与发现求并集

`effectiveChatModes(discovered)` 把内置基线 `["", "zero", "chat_agent", "deep_research"]` 与发现结果合并：基线在前保证目录稳定，发现的新档位追加在后。**未知档位不会被丢弃**。

### 3.4 模型名改为档位直映射

```go
func chatModeModelID(mode string) string {
	if mode == "" { return "glm/glm-4-plus" }   // 默认档沿用兼容名
	return "glm/" + mode                         // glm/chat_agent、glm/zero、glm/deep_research
}
```

`resolveAssistantID` 同步改为：先剥离 `glm/` 前缀 → 裸 24+ 位 ID 当 `assistant_id` → 历史别名映射到默认档 → **其余一律当作 `chat_mode` 原样透传**。这样上游出现 `glm/glm_next_gen` 时，请求会带 `chat_mode=glm_next_gen` 发出去，而不是被静默降级成默认档。

### 3.5 别名降级

`glm-5.1` / `glm-4.7` 从「真实档位」降为 `legacyAliasModels` 纯兼容别名，不再与真实档位混排误导；`baselineModelInfos` 作为无账号或回源失败时的兜底。

## 四、验证

### 4.1 真实发现结果

对真实账号扫描 46 条历史会话：

```
发现 chat_mode: ['chat_agent', 'normal', 'zero']
发现 subscribe_id: ['deep_research']
```

`chat_agent` 成功被发现 —— 这是旧代码缺失的档位。

### 4.2 代码门禁

```
python scripts/cgo-shim-build.py glm → build / vet / test 全绿
```

单测 29 → **32** 全 PASS；`gofmt` 23 个 Go 文件 PASS。

测试重写为动态语义：

| 测试 | 作用 |
|---|---|
| `TestDiscoverChatModesFromHistory` | 从历史发现档位，含未知档位 `brand_new_mode`，且去重保序 |
| `TestEffectiveChatModesUnionsBaselineAndDiscovered` | 基线与发现求并集，基线保持在前 |
| `TestDynamicModelInfosIncludesDiscoveredModes` | **新档位 `glm_next_gen` 自动进入目录**（用户核心诉求） |
| `TestChatModeModelIDRoundTrip` | 档位 → 模型名 → 档位无损往返 |
| `TestResolveAssistantIDMapsModes` | `chat_agent`、未来未知档位均原样透传，别名映射到默认档 |
| `TestDynamicModelInfosAppendsAssistants` | 自建智能体追加、空 ID 与重复项跳过 |
| `TestDynamicModelInfosFallsBackOnError` | 上游 500 时降级基线并告警 |

## 五、结论

模型目录不再依赖任何写死的档位清单：档位从历史会话动态发现，未知档位自动纳入并原样透传。**上游未来新增模型时，插件无需改代码、无需发版**，面板下次刷新（缓存 10 分钟）即可看到。

四层版本一致：代码 = `VERSION` = registry = 生产 `0.2.12`。

## 六、说明

1. `moe_53f` 是服务端内部路由名，客户端无法选择，也无法据此反推模型版本；`model` 字段不要再用。
2. `subscribe_id` 在请求里不被回显，无法作为选择器；它只出现在会话记录里，当前值恒为 `deep_research`。
3. 档位发现的准确度依赖账号有历史会话。全新账号历史为空时只能拿到基线四项；用几次网页端后即自动补齐。
4. 若上游改为在别处暴露档位（例如新接口），只需替换 `discoverChatModes` 的实现，其余链路不动。