# SafeguardKiller responses 通道破甲失效与空响应误判：三层根因与修复（0.1.8 / 0.1.9 / 0.1.10）

## 1. 问题现象

用户在 `session-fc904680-0a43-4063-8209-d24d982bc3f9`（`F:\xiaoshuo` 工作区，模型 `deepseek-v4-pro`）反复报「失败还在继续，破甲好像没生效」。症状随时间分三个阶段：

| 阶段 | 客户端可见症状 | 服务端证据 |
|---|---|---|
| 0.1.7 及以前 | turn 直接报 `TRANSPORT: upstream stream ended before a completion event; this turn may be incomplete`，连续重试 4 次耗尽 | journal `llm/retry` 累计 **89 条**；CPA 日志 `upstream stream closed before a terminal event (last event: none)` 502 |
| 0.1.8 | 重试消失，但 turn 报 `PI_AI_ERROR: Response incomplete: content_filter`（r=0，不重试） | `state.decisions` 出现 `failed: refusal (content_filter)` 15 次 + `every model failed` |
| 0.1.9 | 客户端收到兜底提示帧 `⚡ [Safeguard Killer] every model in 1 failed: deepseek-v4-pro (empty response (no text or tool calls))` | `state.decisions` 出现 `failed: empty response (no text or tool calls)` |

## 2. 关键事实：客户端走的是 responses 通道

DSH 的 `luode` provider 配 `api: openai-responses`、`baseURL: https://api.luode.vip/v1`。请求体由 `D:\ds\resources\app\dsh\node_modules\@earendil-works\pi-ai\dist\api\openai-responses.js:255-266` 构造：

```js
{ model, input: messages, stream: true, prompt_cache_key, prompt_cache_retention,
  prompt_cache_options, store: false, max_output_tokens? }
```

两个决定性特征：

1. **没有 `messages` 字段**，系统提示装在 `input` 数组的 `{role: "system"|"developer", content: "..."}` 项里（`openai-responses-shared.js:131/142`，`instructionRole` 在非 openai.com/azure 域名下取 `system`）；
2. 该会话的 system 部分是 **9 万字符量级**（`<available_skills>` 56,463 字符 + AGENTS.md 30,347 字符 + AGENTS.md 更新版 29,954 字符），推理通道由 `openai-responses.js:282-301` 强制开启：

```js
params.reasoning = { effort, summary }
params.include = ["reasoning.encrypted_content"]
```

## 3. 三层根因

### 3.1 第一层（0.1.8 修）：破甲净化对 responses 通道空转 + 兜底帧缺终止事件

`F:\cpa-plugin\safeguard-killer\refusal.go` 的 `sanitizeRequest` 只处理 `body["messages"]`，`instructions`/`input`/顶层 `system` 一个都不碰。因为 responses 请求根本没有 `messages`，破甲改写（`claudeCyberGatePrompt` 替换、`<system-reminder>` 剥离）在这条通道上**全程空转**。

同时分组全失败时 `emitExhaustedNoticeStream` 的 `else` 分支只发 chat 帧 + `[DONE]`，不含 `response.completed`。CPA 的 `responsesSSEFramer`（`F:\CLIProxyAPI\sdk\api\handlers\openai\openai_responses_handlers.go:262-269`）只认 `response.completed`/`incomplete`/`failed`/`done`/`error`，收不到就在 `:1062-1078` 返回 502 `upstream stream closed before a terminal event`，客户端随即按 `TRANSPORT` 连续重试 4 次。

**判定方法**：`effectiveFormat(exitFormat, sourceFormat)` 走 `ToLower(TrimSpace(...))`，responses 请求得到 `"openai-response"`（`F:\CLIProxyAPI\sdk\translator\formats.go:5-11` 的 `FormatOpenAIResponse`），既不等于 `"claude"` 也不等于空，直接落进 `else` 的 chat 分支。

### 3.2 第二层（0.1.9 修）：推理通道被误判成空响应，且流式路径的保护从未生效

上游在 `reasoning: {effort: "high"}` + 小 `max_output_tokens` 时的真实事件序列（清空 groups 直连取得）：

```
response.created × 1
response.in_progress × 1
response.output_item.added × 1            # item.type = "reasoning", summary = []
response.reasoning_summary_part.added × 1
response.reasoning_summary_text.delta × 24
response.reasoning_summary_text.done × 1
response.reasoning_summary_part.done × 1
response.output_item.done × 1             # item.summary 带文本
response.incomplete × 1                   # incomplete_details.reason = "max_output_tokens"
```

**一个 `response.output_text.*` 事件都没有**——活干完了，只是全在思考链里。而 `hasTruncatedOrReasoningOutput` 只认 chat 形态的 `choices[].finish_reason == "length"` 与 `reasoning_content`，对这套 responses 事件一个都不认，于是「正文为空 + `sawEnvelope = true`」直接落进空响应判据。

更深一层：`prefetchStream` 传给该函数的 `chunk.Payload` 是**带 `data: ` 前缀的 SSE 文本**，函数首行 `json.Unmarshal` 必然失败并返回 `false`。**0.1.6 引入的截断/推理放行从一开始只覆盖非流式请求，流式路径从未生效。**

### 3.3 第三层（0.1.10 修）：裸字符串 delta 让整帧反序列化失败，正文全丢

把分组临时指向 `glm-5.3` 让 `deepseek-v4-pro` 直通上游，取原始流——上游完全正常：

```
response.output_text.delta → delta 是裸字符串 "收到"
response.output_item.done  → item.content[0] = {"type":"output_text","text":"收到"}
response.completed         → status = "completed"
```

而 `inspectPayload` 把 `delta` 声明成对象指针：

```go
Delta *struct {
    Type        string `json:"type"`
    Text        string `json:"text"`
    PartialJSON string `json:"partial_json"`
    ...
} `json:"delta"`
```

Go 实测（最小复现）证明这会**整帧反序列化失败**：

```
input={"type":"response.output_text.delta","delta":"收到"}
  err=json: cannot unmarshal string into Go struct field probe.delta of type struct {...}
```

`inspectPayload` 走到 `if errUnmarshal != nil { return "", "", false, false }` 返回全空值 ⇒ 正文永久丢失 ⇒ 下游判空响应。

原 `refusal.go` 里那段 `if msg.Type == "response.output_text.delta" { var deltaEvt struct{ Delta string }; json.Unmarshal(raw, &deltaEvt) }` 的补救分支，**在整帧 Unmarshal 已失败之后根本不会被执行**（外层已提前 return）。

## 4. 修复

### 4.1 0.1.8（提交 `eb677b9`）

- 新增 `isResponsesFormat`（认 `openai-response`/`openai-responses`/`openai_responses`/`responses` 四种写法，忽略大小写空白）
- 新增 `buildResponsesExhaustedBody` / `buildResponsesExhaustedSSE`：后者依次发 `response.created` → `output_item.added` → `output_text.delta` → `output_item.done` → `response.completed`，终止事件收尾
- 新增 `cleanSystemField`：处理顶层 `system` 的字符串与 text 块数组两形态
- `sanitizeRequest` 新增 `input`、`system`、`instructions` 三处净化分支
- `cleanMessages` 增加 `dropWholeSystem` 参数：chat/Claude 传 `true`（整条 system 都是限制文案），responses 的 `input` 传 `false`（system 项常与正常指令混排，整条删会连带删掉用户自己的系统提示）
- `injectStreamNotice` 对 responses 直接原样返回——`response.*` 的 `output_index` 必须与流内一致，硬插 chat delta 帧会污染事件流

### 4.2 0.1.9（提交 `b1116af`）

`hasTruncatedOrReasoningOutput` 拆两层：

- 外层按首字符分流：`{` 开头走单对象，否则按 `\n` 逐行剥 `data:` 前缀
- 内层 `hasTruncatedOrReasoningObject` 四组判据：chat 截断/推理字段、`response.reasoning_summary_text.delta`（`delta` 非空）与 `.done`（`text` 非空）、`item`/`output[]` 的 `type == "reasoning"` 且 summary/content 非空、`incomplete_details.reason == "max_output_tokens"`（顶层与 `response.` 内两处）

`content_filter` 不在放行范围，仍判 `refusal (content_filter)`。

### 4.3 0.1.10（提交 `6c8d0a0`）

- `Delta` 由对象指针改为 `json.RawMessage`，取到后先试字符串形态写正文，失败再按对象形态解 `text_delta` / `input_json_delta` / `refusal` / `stop_reason`
- `sawEnvelope` 判据由 `msg.Delta != nil` 改为 `len(msg.Delta) > 0`
- 删除已多余的 `response.output_text.delta` 补救分支
- `item` 结构补 `content[]`、`response` 结构补 `output[]`，覆盖「正文只出现在 `output_item.done`」与「正文只在终止帧 `response.completed` 里」两种上游形态

## 5. 验证

### 5.1 单元测试

| 版本 | 新增测试 | 用例数 |
|---|---|---|
| 0.1.8 | `refusal_responses_test.go`（7 函数） | 21 |
| 0.1.9 | `refusal_truncation_test.go` 扩充（8 → 18）+ 2 个流式端到端 | 20 |
| 0.1.10 | `refusal_payload_test.go`（3 函数） | 7 |

每版均跑三组变异，全部 KILLED 且失败形态为断言失败：

- 0.1.8：`isResponsesFormat` 退化为永不命中 / `input` 的 `dropWholeSystem` 改回 `true` / 删掉 `injectStreamNotice` 的 responses 短路
- 0.1.9：去掉 responses 推理增量识别 / 去掉 SSE 逐行解析退回整段 `Unmarshal` / 去掉 `max_output_tokens` 识别
- 0.1.10：不再识别字符串 delta / 去掉 `item.content` 正文提取 / 去掉 `response.output` 正文提取

变异脚本对每个锚点断言 `text.count(old) == 1`，防空锚点造成的假通过。

### 5.2 生产热重载

| 版本 | 落盘 `.so` 字节 | sha256 | 日志 |
|---|---|---|---|
| 0.1.8 | 26,759,200 | `82f1254473ecff156714026660c9aa0ff99e49a978c677a005febd562b047d8f` | 02:13:35 `hot reloaded active_version=0.1.8 retired_version=0.1.7` |
| 0.1.9 | 26,767,392 | `fa2b4df93c9fb97d741d0a01b8fb6c4a64f065e53d5bcafb499f87d99acca982` | 22:09:23 `hot reloaded active_version=0.1.9 retired_version=0.1.8` |
| 0.1.10 | 26,767,392 | `0dbfa6fd22e22868035541d2cbb7095815ad98e2011da5356c735eec4d79afe5` | 22:32:33 `hot reloaded active_version=0.1.10 retired_version=0.1.9` |

三者落盘 sha256 与本地 release zip 内 `.so` 逐字节一致，容器均未重启。

### 5.3 真机复测（0.1.10 上线后，`POST /v1/responses` + `stream: true`）

| 场景 | 结果 |
|---|---|
| `normal max=64` × 3 | 全部 `http=200`、9 事件、`term=['response.completed']`、`sk=False`、正文「收到」 |
| `reasoning max=24` × 2 | 全部 `http=200`、32 事件、`term=['response.incomplete']`、`sk=False` |

`state.decisions` 15 条**全部** `served by deepseek-v4-pro` / `['served']`，零失败。容器 `Up 40 hours`（未重启），`panic|fatal|unknown_method` 10 分钟 0 命中，磁盘 79%。

## 6. 过程中踩到的两个坑

### 6.1 清空 groups 会让插件被宿主注销

一次探针 `PUT /v8/management/config/plugins/configs/safeguard-killer` 把 `groups` 置空后，`state` 开始 404，`/v0/management/plugins` 显示 `registered:false`、`enabled:false`。原因：`groups` 为空 ⇒ 插件 `model.register` 返回空列表 ⇒ 宿主注销该插件，**破甲链根本没在跑**。

恢复方式：先 `PUT` 写回 groups，再 `POST /v0/management/plugin-store/.../install` 重装触发重新注册。**仅 PUT 写回不够，必须 reinstall。** 后续探针改用「把 group 成员换成别的模型」而非清空 groups。

### 6.2 CI 的 `Release: skipped` 不算发布成功

每次 dispatch 会产生两个 run，一个只有 `Test` 任务（`Release: skipped`），另一个跑 build-cross 矩阵。只有后者的 `Release: success` 才是有效产物。顶层 `conclusion=cancelled` 通常是 `darwin/arm64` 排队被取消拖累，不影响 Release job 本身。

## 7. 尚未解决

`content_filter` 是**上游服务端审核**，累计 15 次命中 `deepseek-v4-pro`。破甲提示词注入改不动服务端审核，只能靠分组多成员兜底。生产 group 当前仍是单成员 `[{"id":"1","name":"1","strategy":"order","models":["deepseek-v4-pro"]}]`，命中即 `every model failed`。若要兜底，需给分组补成员（codename 必须避开 provider 注册 ID，如 `sg-fast`，否则会被 `groupServing` 的精确匹配劫持）。
