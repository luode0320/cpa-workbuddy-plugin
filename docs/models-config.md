# models 配置说明（workbuddy-provider / traework-provider）

> 本文档基于 2026-08-28 两个插件工作树的实际解析代码整理。
> 事实来源：`workbuddy/models.go`、`workbuddy/usage_config.go`、`workbuddy/main.go`、`traework/models.go`、`traework/config.go`、`traework/main.go`。

## 一、配置入口

两个插件的模型列表都在 `config_yaml` 的 **`models`** 键下配置（即面板「模型列表」字段，注册时 `ConfigFields` 中类型为 Array）。

**格式约束（两插件一致）：**

- `models` 的值必须是**整行 JSON 数组**，例如 `models: ["glm-5.2", "kimi-k2.7"]`；
- 不支持多行 YAML 列表（`models:` 换行后逐行 `- xxx` 的写法不生效，会被解析逻辑直接忽略）；
- 每个条目可以是**字符串**（模型 id）或**对象**（结构见下）。

```yaml
# 最小示例：字符串列表
models: ["glm-5.2", "kimi-k2.7"]

# 完整示例：对象列表
[{"context":2000000,"id":"hy4-preview","max_tokens":20000,"name":"Hy4 preview"},{"context":2000000,"id":"hy3","max_tokens":20000,"name":"Hy3"},{"context":2000000,"id":"glm-5.3","max_tokens":20000,"name":"GLM-5.3"},{"context":2000000,"id":"glm-5.3-flash","max_tokens":20000,"name":"GLM-5.3-Flash"},{"context":2000000,"id":"glm-5.2","max_tokens":20000,"name":"GLM-5.2"},{"context":2000000,"id":"glm-5.1","max_tokens":20000,"name":"GLM-5.1"},{"context":2000000,"id":"glm-5v-turbo","max_tokens":20000,"name":"GLM-5v-Turbo"},{"context":2000000,"id":"minimax-m3","max_tokens":20000,"name":"MiniMax-M3"},{"context":2000000,"id":"kimi-k3","max_tokens":20000,"name":"Kimi-K3"},{"context":2000000,"id":"kimi-k2.7-code","max_tokens":20000,"name":"Kimi-K2.7-Code"},{"context":2000000,"id":"kimi-k2.6","max_tokens":20000,"name":"Kimi-K2.6"},{"context":2000000,"id":"deepseek-v4-flash","max_tokens":20000,"name":"Deepseek-V4-Flash"},{"context":2000000,"id":"deepseek-v4-pro","max_tokens":20000,"name":"Deepseek-V4-Pro"},{"context":2000000,"id":"seed-evolving","max_tokens":20000,"name":"Seed-Evolving"},{"context":2000000,"id":"seed-2.1-pro","max_tokens":20000,"name":"Seed-2.1-Pro"},{"context":2000000,"id":"seed-2.1-turbo","max_tokens":20000,"name":"Seed-2.1-Turbo"},{"context":2000000,"id":"qwen3.8-max","max_tokens":20000,"name":"Qwen3.8-Max"},{"context":2000000,"id":"qwen3.7-plus","max_tokens":20000,"name":"Qwen3.7-Plus"}]
```

---

## 二、workbuddy-provider

### 2.1 支持的字段（对象条目）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 模型 ID，为空则该条目丢弃 |
| `name` | string | 否 | 显示名，为空自动用 `id` |
| `alias` | string | 否 | **仅 schema 兼容，无实际效果**（ModelInfo 无对应字段） |
| `context` | int | 否 | 上下文长度，映射为 `ContextLength` |
| `max_tokens` | int | 否 | 最大输出 token 数，映射为 `MaxCompletionTokens` |
| `enabled` | bool | 否 | `false` 时该条目直接跳过；缺省视为启用 |
| `reasoning` | bool | 否 | **仅 schema 兼容，无实际效果** |

### 2.2 语义要点

- **优先级链：config > 动态发现 > 静态默认**。配置了 `models` 后，`model.for_auth` 直接返回配置列表，不再调用上游 `/console/enterprises/personal/models` 动态发现接口，也不再走 `wbModels()` 静态默认。
- **`models: []` 显式清空**：表示“停止覆盖”，恢复动态发现 / 静态默认路径。
- **全非法条目保持现状**：如果列表里所有条目都解析失败（缺 id、格式错），当前生效的覆盖列表不会被清空——一次坏编辑不会静默毁掉正在工作的配置。
- 字符串条目等价于 `{"id": "<s>", "name": "<s>"}`，`context`/`max_tokens` 为 0。

### 2.3 默认静态列表（未配置时兜底，动态发现失败也会回退到这里）

| 模型 ID | 显示名 | context |
| --- | --- | --- |
| `glm-5.2` | GLM-5.2 | 1,000,000 |
| `glm-5.1` | GLM-5.1 | 131,072 |
| `glm-5v-turbo` | GLM-5V Turbo | 131,072 |
| `kimi-k2.7` | Kimi K2.7 | 262,144 |
| `minimax-m3` | MiniMax M3 | 204,800 |
| `hy3` | Hy3 | 262,144 |
| `hy3-preview` | Hy3 Preview | 262,144 |
| `hy3-preview-agent` | Hy3 Preview Agent | 262,144 |
| `deepseek-v4-pro` | DeepSeek V4 Pro | 1,000,000 |
| `deepseek-v4-flash` | DeepSeek V4 Flash | 1,000,000 |

### 2.4 示例

```yaml
# 配置两个模型：最终列表 = 这两个（配置优先）+ 自动获取/默认中未配置的模型（补充）
models: ["glm-5.2", "deepseek-v4-flash"]

# 对象形式：定制显示名与上下文长度（同 ID 时覆盖自动获取版本）
models: [{"id": "glm-5.2", "name": "旗舰模型", "context": 1000000, "max_tokens": 8192}]

# 混合形式 + 禁用某个条目
models: ["kimi-k2.7", {"id": "hy3-preview", "name": "Hy3 预览", "enabled": false}]

# 显式清空覆盖，恢复动态发现 / 静态默认（纯自动获取，无合并）
models: []
```

### 2.5 合并语义（配置优先 + 自动获取补充）

最终返回给客户端的模型列表 = **配置条目（在前，优先）+ 自动获取列表中未配置的模型（追加在后）**：

- **同 ID**：保留配置条目（name / context / max_tokens 以配置为准），自动获取版本被丢弃；
- **配置没有、自动获取有的**：追加在配置条目之后；
- 自动获取来源：`model.for_auth` 用上游动态发现（失败回退静态默认），`model.static` 用静态默认列表（该请求无 storageJSON，无法动态拉取）；
- 去重键为模型 ID（大小写敏感）；合并不修改输入列表（安全共享缓存底层数组）。

示例：配置 `["glm-5.3"]`，自动获取有 `[glm-5.2, glm-5.3, hy4-preview]` → 结果 `[glm-5.3, glm-5.2, hy4-preview]`（glm-5.3 用配置版本，glm-5.2 / hy4-preview 补充保留）。

> 注意：合并是唯一语义，**无法**通过配置"只显示配置里的模型、隐藏自动获取的其他模型"。若要隐藏某模型，可配合宿主 `oauth-excluded-models` 过滤。

---

## 三、traework-provider

### 3.1 支持的字段（对象条目）

traework 的对象条目直接反序列化进 SDK 的 `pluginapi.ModelInfo`（v7.2.30），**只认 SDK 原生字段**：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 模型 ID，为空则该条目丢弃 |
| `name` | string | 否 | 显示名，为空自动用 `id` |
| `description` | string | 否 | 模型描述 |

> ⚠️ **重要差异**：`context`、`max_tokens`、`enabled`、`alias`、`reasoning` 这些字段在 traework **不生效**——SDK `ModelInfo` 没有对应 JSON 字段，解析时被静默忽略。其中 `enabled: false` 不会跳过条目，写进去会原样出现在模型列表里。

### 3.2 语义要点

- **无动态发现**：Trae Work SOLO 接受任意模型名（未知名字原样透传为 `config_name`），列表仅用于客户端选择器展示。
- **`models: []` 不生效**：空列表解析后条目数为 0，保持现状（等价于“不改动”），**无法**像 workbuddy 那样用空数组清空回默认。
- 全非法条目同样保持现状。
- 未配置时使用内置默认列表（见下）。

### 3.3 默认列表（未配置时）

| 模型 ID | 说明 |
| --- | --- |
| `glm-5.2` | Trae Work 默认模型 (GLM-5.2) |
| `glm-4.7` | GLM-4.7 |
| `deepseek-v4` | DeepSeek V4 |
| `deepseek-v4-flash` | DeepSeek V4 Flash |
| `qwen-max-latest` | Qwen Max |
| `doubao-1.5-pro-32k-250428` | Doubao 1.5 Pro |

### 3.4 示例

```yaml
# 仅字符串列表（最常用）
models: ["glm-5.2", "deepseek-v4-flash", "qwen-max-latest"]

# 对象形式（仅 id / name / description 生效）
models: [{"id": "glm-5.2", "name": "Trae 默认模型", "description": "GLM-5.2"}]
```

---

## 四、两插件差异速查

| 对比项 | workbuddy-provider | traework-provider |
| --- | --- | --- |
| 字符串条目 | ✅ | ✅ |
| 对象条目 `id`/`name` | ✅ | ✅ |
| `description` | ❌（无此字段） | ✅ |
| `context` / `max_tokens` | ✅ 生效 | ❌ 被忽略 |
| `enabled: false` 跳过条目 | ✅ | ❌ 被忽略 |
| `alias` / `reasoning` | 接受但无效果 | ❌ 被忽略 |
| `models: []` 清空回默认 | ✅ | ❌ 不生效（保持现状） |
| 配置与自动获取的关系 | **合并**（配置优先，自动获取补充，见 2.5） | 配置覆盖内置默认（无自动获取层） |
| 全非法条目 | 保持现状 | 保持现状 |
| 并发保护 | `configuredModelsMu`（RWMutex） | 无锁（既有行为） |

---

## 五、注意事项

1. **`models:` 值支持三种形态（2026-08-29 起全兼容）**：
   - **单行 JSON**（推荐）：`models: [{"id": "glm-5.2", "context": 2000000}]`；
   - **多行 pretty-print JSON**：面板/编辑器自动格式化出的 `models: [` 换行、逐对象一行的形态——插件按括号配对跨行收集后整体解析；
   - **YAML block sequence**：宿主管理面板把 JSON 数组序列化回 YAML 的形态（`models:` 换行、逐行 `- context: 2000000` / `id: ...`）——**实测 cli-proxy-api 的 config_store 落库形态就是这样**（2026-08-29 服务器取证确认），插件按缩进收集条目解析。纯字符串条目列表（`- hy4-preview`）不支持，保持旧值。
2. **修改生效**：配置通过插件 `configure()` 在配置加载时解析，修改后需重新加载配置（面板保存 / 宿主 reload）。
3. **workbuddy 是"合并"不是"替换"（2026-08-28 起）**：配置了非空 `models` 后，最终列表 = **配置条目（在前，优先）+ 自动获取列表（动态发现 / 静态默认）中未配置的模型（补充在后）**。同 ID 时以配置的字段为准，自动获取的版本被丢弃。例如：配置了 `glm-5.3`，自动获取也有 `glm-5.3` → 只用配置版本；配置没有但自动获取有的 `hy4-preview` → 保留追加。想要"只显示配置的模型"目前做不到（合并是唯一语义），可用 `models: []` 显式清空回到纯自动获取。
4. **traework 的 `enabled` 是陷阱**：面板字段说明虽写了 `{id, name, ...}`，但实际 `enabled: false` 不生效，条目仍会展示；要隐藏模型请直接不要写进列表。
