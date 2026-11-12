# Safeguard Killer 从「破甲」退化成「换模型」：同名分组劫持 + 空响应误判根因与修复

结论：**用户要的是破甲能力，不是切换模型能力。** 本次故障的两层缺陷都在这条定位上：第一层是分组 id 与 provider 注册 ID 同名导致请求被劫持进分组、**永远到不了目标模型本体**（破甲链路要处理的请求从未抵达原模型，破甲根本没机会生效）；第二层是空响应判据把「配额截断」和「只有推理内容」误判成模型拒绝，于是滚到分组下一个模型，客户端拿到的回答来自**另一个模型**，正文前缀是 `⚡ [Safeguard Killer] ... switched to ...`。修复后 `deepseek-v4-pro` + `max_tokens=300` 三次实测 **`model=deepseek-v4-pro`、`finish=stop`、无切换、1.8–2.3s**。

- 影响范围：所有在 Safeguard Killer 分组内的思考模型（实测 `hy4free`），以及所有与分组 `id` 同名的 workbuddy 注册模型。
- 非范围：不改上游模型行为、不改 `max_tokens` 透传策略（workbuddy 侧已于 2026-11-07 删除全部自研 max_tokens 控制，见 `workbuddy/model_specs.go:3-24`）、不改真正的拒绝判定。
- 完成标准：目标模型的请求必须真正发给目标模型；模型已完成的工作（截断、推理内容）不被当成拒绝；真正的 `content_filter` / `refusal` / 文本拒绝仍被拦截。

## 问题现象

用户侧观察（原话，m02333）：「wokrbuddy的deepseek-v4-pro模型还是再不停的失败, 重试, 切换模型依旧失败」。

复现（生产 `45.207.222.65:18998`，管理端口 `8317`，`POST /v1/chat/completions`，`max_tokens=300`，连测 3 次）：

| 请求模型 | HTTP | 响应 `model` 字段 | 正文开头 | 耗时 |
|---|---|---|---|---|
| `deepseek-v4-pro` | 200 | **`glm-5.3-flash`** | `⚡ [Safeguard Killer] hy4free (empty response (no text or tool calls)) → switched to glm-5.3-flash` | 7.2–10.7s |
| `DeepSeek-V4-Pro`（大写） | 200 | `deepseek-v4-pro` | 正常直通 | 1.8–2.3s |

同一个模型名，只因大小写不同，一个被降级到别的模型、一个正常。而 new-api 侧 `oneapi.logs` 近 30m 把 `deepseek-v4-pro` **109 次全记 type=2 成功** —— 网关看不到失败，用户侧看到的是「换了个模型回答」。

## 出现环境

- 插件：safeguard-killer 0.1.5（生产），workbuddy-provider 0.21.25。
- 链路：客户端 → new-api → CPA `8317` → Safeguard Killer（`executor.execute` 分组劫持 / `request.intercept_before` 破甲改写）→ workbuddy-provider → 上游。
- 时间：2026-11-12 22:20–22:40。

## 触发条件

1. Safeguard Killer 的某个分组 `id` 与某 provider 的注册模型 ID 同名（本例 `deepseek-v4-pro`）。
2. 客户端请求该模型名（大小写与分组 `id` 一致）。
3. 分组内排到思考模型（本例 `hy4free`），且客户端 `max_tokens` 较小（本例 300）。

三者同时成立即复现。任一条不成立就不复现 —— 大写名绕开分组、`max_tokens` 足够大时思考模型能出正文。

## 期望结果

按用户定调（m02403）：「Safeguard Killer 希望要的是破甲的能力, 而不是切换模型的能力」。具体：

- 请求 `deepseek-v4-pro` 必须真的发给 `deepseek-v4-pro`，破甲改写（系统提示替换、分类器剔除、拒绝污点剥离）作用在**原模型**上。
- 切模型只能作为破甲彻底失败后的兜底，不能因为「模型在思考」就把请求转走。
- 模型产出的推理内容、被配额截断的产出，都属已完成工作，不构成拒绝。

## 当前证据

### 证据 1：分组里根本没有 `deepseek-v4-pro`

`GET /v0/management/plugins/safeguard-killer/state` 回读：

```text
version: 0.1.5
groups: [{"id":"deepseek-v4-pro","name":"deepseek-v4-pro","strategy":"order",
          "models":["hy4free","glm-5.3-flash","minimax-m3.1-flash"]}]
是否含 deepseek-v4-pro 作为成员: False
```

分组 `id` 叫 `deepseek-v4-pro`，成员却全是别的模型。请求 `deepseek-v4-pro` 命中的是**分组 id**，于是被劫持进这个分组，逐成员换号——**从头到尾没有一次请求真正发给 `deepseek-v4-pro` 本体**。

### 证据 2：劫持与破甲是两条解耦的链路（代码定位）

| 位置 | 职责 | 与分组的关系 |
|---|---|---|
| `safeguard-killer/refusal.go:1401` `handleRequestIntercept` → `:1425` `sanitizeRequest(req.Body, req.Headers, cfg)` | 破甲：改写系统提示、剥离分类器、清拒绝污点头 | **完全解耦**，走 `request.intercept_before` 钩子 |
| `safeguard-killer/groups.go:181-192` `resolveTargetGroup` → `:154-172` `rotateModels` | 分组劫持：命中即把请求指向分组链首模型 | 走 `executor.execute` / `executor.execute_stream` |
| `safeguard-killer/config.go:220-227` `cfg.group(id)` | 按 `group.ID == id` **大小写敏感精确匹配** | 劫持的判定入口 |
| `safeguard-killer/groups.go:137-150` `groupServing` | 按成员名精确匹配（进分组的第二条路） | 同上 |

`sanitizeRequest`（`refusal.go:1252-1352`）的破甲开关：`RewriteCyberSystemPrompt`（`:1258` 替换 `claudeCyberGatePrompt`、删 `claudeFable51SafetyBlurb` / `claudeFable5SafetyBlurb`）、`NeutralizeClassifierStopNudge`（`:1263-1268` 替换 `claudeRefusalRetryNudge` / `claudeToolNotRunClassifier` / `claudeToolInterruptedClassifier`）、`BypassAutoModeClassifier`（`:1275` 删 `safeguards` 字段、`:1286` 过滤 `anthropic_beta`）、`DisableUpstreamServerFallback`（`:1280` 删 `fallbacks`）、`StripRefusalTaintHeaders`（`:1322` 清 `refusalTaintHeaders`）、`cleanMessages`（`:1301`）。

⇒ **清空分组后破甲照旧生效、请求直达目标模型**。这是修复方向成立的架构前提。

### 证据 3：`hy4free` 空响应是思考链烧光配额，不是拒绝

清空分组后直接测 `hy4free`：

- `max_tokens=300` × 10 次：**OK=1、EMPTY=9**。
- `max_tokens=2000` × 3 次：`finish=stop`，`content_len` 41 / 46 / 59，`reasoning_len` **714 / 545 / 919**，`completion_tokens` 412 / 337 / 555。

⇒ 模型在思考，只是 `max_tokens=300` 装不下思考链。上游 served 名是 `hy4-free`（`usage.quota.model='hy4'`），CPA 注册名是 `hy4free`；直接请求 `hy4-free` 返回 400 `unknown provider for model hy4-free`。

### 证据 4：旧空响应判据不识别推理内容与截断

| 位置 | 修复前行为 |
|---|---|
| `safeguard-killer/refusal.go:687-706` `inspectOpenAIMessageContent` | 只读 `choices[].message.content` |
| `safeguard-killer/refusal.go:466-480` `Choices` 结构体 | 只有 `FinishReason` / `Message{Content,Refusal,ToolCalls,FunctionCall}` / `Delta{...}`，**没有 `reasoning_content` 字段** |
| `safeguard-killer/refusal.go:749-763` `checkNonStreamResponse` | `sawEnvelope && visible=="" && !sawTool` → `empty response (no text or tool calls)` |
| `safeguard-killer/refusal.go:810-813` `prefetchStream` | 同判据 |

全仓库 `grep reasoning` 仅 3 处命中（`refusal.go:701` 的数组式 content 分支、`stream_test.go:352`、`:361`），思考模型的推理内容完全不被识别。

## 根因

**两层缺陷叠加，第一层让破甲失效，第二层把「模型在思考」误判成「模型拒绝」。**

第一层，**同名分组劫持**：分组 `id` 被设成与 provider 注册 ID 相同的 `deepseek-v4-pro`，成员里却没有该模型。`cfg.group(requestedModel)` 按 id 精确匹配命中，请求被劫持进分组并按 `strategy: order` 逐成员轮转。破甲改写虽然照常执行，但改写的对象是分组链首的 `hy4free`，不是用户要的 `deepseek-v4-pro` —— **破甲能力被架空**。

第二层，**空响应判据过窄**：`hy4free` 是思考模型，`max_tokens=300` 时推理链吃满配额，`finish_reason=length`、正文空、只有 `reasoning_content`。旧判据只认「有可见文本或有工具调用」，把这种正常产出判成 `empty response (no text or tool calls)`，触发 `fallback_on_refusal` 继续往下滚到 `glm-5.3-flash`。客户端最终拿到的回答来自第三个模型。

两层合起来正是用户感受到的「不停失败、重试、切换模型依旧失败」：不是模型不行，是请求压根没到模型那儿，到了也被当成拒绝。

## 修复方案

### 第一层：清空分组，让请求直达目标模型

- 备份生产配置 `/usr/local/src/cli-proxy-api/config/config.yaml.bak-20261112_2233`（76,835 B）。
- `GET /v0/management/plugins/safeguard-killer/config` → `groups` 置为 `[]` → `PUT` 同端点 → **http=200 `{"status":"ok"}`**；回读 `groups: []`、9 个破甲 feature 全开。

刻意不做的事：**不重建分组、不改分组 id 命名**。分组属于可选执行器劫持层，与破甲解耦；当前用户要的是破甲，分组会再次把请求从目标模型上引开。是否重建、用什么 codename 需单独裁定。

### 第二层：截断与纯推理内容不再被误判空响应

1. `safeguard-killer/refusal.go` 新增 `hasTruncatedOrReasoningOutput(raw []byte) bool`：识别 `choices[].finish_reason == "length"`、`message.reasoning_content` / `message.reasoning`、`delta.reasoning_content` / `delta.reasoning`（后两者 `TrimSpace` 非空才算）。
2. `checkNonStreamResponse` 空判据改为命中即放行：`if sawEnvelope && visible=="" && !sawTool { if !hasTruncatedOrReasoningOutput(trimmedBody) { return fmt.Errorf("empty response (no text or tool calls)") } }`。
3. `prefetchStream` 新增 `sawTruncatedOrReasoning` 累加；`chunk.Done` 的空响应判据加 `&& !sawTruncatedOrReasoning`；预取提前退出条件加 `sawTruncatedOrReasoning` —— 推理内容到达即转发，不把整段思考链缓冲完才吐给客户端。
4. `content_filter`、`refusal` 字段、`detectTextRefusal` 文本模式判据**全部保持不变**。

刻意不做的事：**不抬 `max_tokens`**。`workbuddy/model_specs.go:3-24` 已明确记录 2026-11-07 删除全部自研 max_tokens 控制，抬值属于回退该决策；客户端传 300 就按 300 发，SK 只负责不把这当成拒绝。

## 修复结果

- 生产热重载：`POST /v0/management/plugin-store/safeguard-killer/install?version=0.1.6&source=source-636fa01121af` → **http=200 `{"status":"installed","version":"0.1.6","restart_required":false}`**。
- 日志 `plugin hot reloaded plugin_id=safeguard-killer active_version=0.1.6 retired_version=0.1.5`。
- 落盘 `/usr/local/src/cli-proxy-api/plugins/linux/amd64/safeguard-killer-v0.1.6.so` sha256 `f178ac5acf369f4de4598f3b6ed85816342aeea4d879f67e91fbda95c4c9c3a6`（29,372,928 B）= 本地 release zip 内 `.so` 逐字节一致。
- 容器未重启：`Status=running RestartCount=2 StartedAt=2026-11-11T22:52:32Z`；panic/fatal **0**。

真机验证（修复后）：

| 场景 | 结果 |
|---|---|
| `deepseek-v4-pro` + `max_tokens=300` | **http=200、`model=deepseek-v4-pro`、`finish=stop`、正文「好的」、无 `switched to`** |
| `hy4free` + `max_tokens=300` | **http=200、`served=hy4-free`、`finish=length`、`content_len=0`、`reasoning_len=449`、无 `switched to`** |
| 分组场景 `id=sg-trunc-test`，单成员 `hy4free`，`max_tokens=300` | **http=200、`served=hy4-free`、`finish=stop`、`content_len=3`（「请提问」）、`reasoning_len=218`、`SK切换=False`** |

第三行是关键：修复前这个场景必定降级到 `glm-5.3-flash`，修复后请求停在 `hy4-free` 并正常产出。验证用分组已删除，`groups` 回读为 `[]`。

## 待确认

- 分组是否重建、用什么中性 codename（避免再次与 provider 注册 ID 同名）：需用户裁定。
- 其它 provider 插件（`traework`、`qoderwork` 等）是否有同构的空响应判据需同族同步：本轮只改 `safeguard-killer`。

## 执行附录

### 静态核查位点

- `safeguard-killer/refusal.go:687-706`、`:466-480`、`:749-763`、`:769-820`、`:744`（新增函数插入点）
- `safeguard-killer/config.go:220-227`、`groups.go:137-150`、`:154-172`、`:181-192`
- `workbuddy/model_specs.go:3-24`、`:37`、`:53-77`

### 取证命令（全部只读）

```powershell
# 分组与 feature 状态
ssh cpa-server "curl -s -H 'Authorization: Bearer <KEY>' http://127.0.0.1:8317/v0/management/plugins/safeguard-killer/state"

# 热重载证据
ssh cpa-server "docker logs cli-proxy-api --since 10m 2>&1 | grep -i 'hot reloaded'"
```

### 安全边界

- 取证阶段只读：未改配置、未重启容器、未写数据库。
- 写入阶段：`PUT /v0/management/plugins/safeguard-killer/config` 两次（清空分组、建/删验证用分组），`POST /v0/management/plugin-store/safeguard-killer/install?version=0.1.6&source=source-636fa01121af` 一次；改前已备份 `config.yaml.bak-20261112_2233`（76,835 B）。
- 未做：未重启 `cli-proxy-api` 容器、未写数据库、未改账号池、未改 `max_tokens` 策略。
- 未回显任何凭据原值。

## 追踪附录

- 版本：`safeguard-killer` 0.1.5 → 0.1.6。
- 提交：`2324bb9`（源码 + 测试 + VERSION + main.go + CHANGELOG，5 文件 +186/−6）、`7bf67bf`（registry 4 平台资产）。
- 测试与变异验证：`safeguard-killer/refusal_truncation_test.go` 10 个用例；三组变异全部 KILLED 且失败形态为断言失败。
- 同主题历史缺陷：`doc/4-bugs/2026-11-12_010000_会话压缩阶段异常根因诊断.md` 的「部署后观察：外部会话改写 group 配置（2026-11-12 21:24–21:26）」小节记录了分组配置被外部会话反复改写的事实。
