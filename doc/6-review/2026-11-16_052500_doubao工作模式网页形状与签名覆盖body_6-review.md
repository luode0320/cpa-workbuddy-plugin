STYLE: PASS

# 0.1.23 风格回归：`-1` 真因修正、签名覆盖请求体、删除 `doubao/web`、测试提示词轮换

本轮改动 14 个文件（13 改 1 新增），`git diff --stat doubao/` = 539 insertions(+) / 596 deletions(-)。

静态 Owner 路由命中的检查项：STYLE-01 / STYLE-02 / STYLE-03 / STYLE-04 / STYLE-05 / STYLE-08 / STYLE-09；**STYLE-06 与 STYLE-07 未命中**（无对应改动类型），下方逐项给出结论与未命中原因。

## STYLE-01 静态格式与编码一致性

Owner：`code-style-consistency-rules`。

- `gofmt -l`（先转 LF 临时副本再校验，避开仓库既有 CRLF 检出形态造成的假阳性）→ `FMT_OK`，无未格式化文件。本轮唯一需要 `gofmt -w` 的是 `completion.go`：新增 22 个字段后结构体列宽变化，gofmt 必须重排，属必要变更；重排后已还原为 CRLF（45189 B）。
- 编码体检（10 个改动文件全部）：`bom=False` / `ctrl=0`。`completion.go` 45189 B、`protocol.go` 19402 B、`sign.go` 11408 B、`models.go` 23383 B、`active_ping.go` 12443 B、`active_ping_prompt_test.go` 1154 B、`workmode_test.go` 26056 B、`models_test.go` 8479 B、`sign_test.go` 4821 B、`active_ping_test.go` 15644 B。
- `CHANGELOG.md` 64592 B / 545 处 CRLF / 0 处孤立 LF / `bom=False`，与仓库其余 tracked 文件检出形态一致。
- `VERSION` 6 B、无尾换行（`hex=30-2E-31-2E-32-33` 即 `0.1.23`），与 `git show HEAD:doubao/VERSION` 完全同形——保留原文件的「无尾换行」这一非标准形态，不顺手加换行。

## STYLE-02 命名与符号引用一致

Owner：`naming-rules`。

- 新增字段名沿用上游 JSON 名的 Go 化写法，与文件内既有 `SSERecvEventOptions` / `NeedDeepThink` / `IsAudio` 的缩写全大写口径一致：`ConnectorInfoList`（`connector_info_list`）、`EditQueryID`（`edit_query_id`）、`RegenQueryID`（`regen_query_id`）、`RelatedDeletedMessage`（`related_deleted_message_ids`）、`RecoveryOption` / `recoveryOption`（`recovery_option`）、`ReqCreateTimeSec`（`req_create_time_sec`）、`AppendSSEEventScene`（`append_sse_event_scene`）、`LastMessageIndex`（`last_message_index`）。
- `testPingText`（常量）→ `testPingPrompts`（切片）+ `testPingPrompt()`（取值函数）：单数常量变复数容器时同步改名，容器与取值函数用同词根区分。
- 删除符号后无悬挂引用：`testPingText` 全仓已无引用；`WebPipeline` / `doubao/web` 只剩测试里的负向断言与 `protocol.go` 的结论修正注释（这是刻意保留，用来防止旧别名回流）。

## STYLE-03 注释分层与定义位置

Owner：`comment-rules`。

- 五个主文件函数注释审计（只读脚本扫每个 `func ` 前的连续注释块）：`completion.go` / `sign.go` / `protocol.go` / `active_ping.go` / `models.go` 全部 `missing: none`，即每个函数都有注释块、都含 `[参数]` 与 `最近修改时间`。
- 新增字段组用小节注释说明「为什么不写 omitempty」，并把成因（2026-11-16 逐项消融结论）写进注释，符合文件内「决策 + 时间 + 依据」的既有写法，不是只写「这些字段是默认值」这种复述型注释。
- 步骤编号延续文件习惯：`buildSkillOption` 现为 1/2/3/4 四步、`buildWorkOption` 现为 1/2/3/4 四步，新增步骤接续编号、未重排既有步骤，符合 `comment-step-numbering-gate`。
- `recoveryOption` 类型定义紧跟 `completionOption` 之后，与 `conversationInitOption` / `conversationInitExt` / `agentTaskParam` / `generalTaskParam` 的「被引用类型紧邻主类型」顺序一致。
- 清理了一处悬挂文档引用：注释原写「详见 `webDefaultOptionKeys`」，而该符号从未定义，改为「（2026-11-16 逐项消融确认）」。

## STYLE-04 函数签名与参数结构

Owner：`code-quality-rules`（含 minimal-change 边界）。

- 三处签名变更都是**追加尾参**，不重排既有参数：`sign(rawURL, msToken, body string)`、`signChatCompletionURL(sa, rawURL, body []byte)`、`completionURL(sa, workMode, body []byte)`。`body` 作为最后一个参数符合「输入在前、上下文在后」的既有顺序（原 `sa` 已在首位）。
- `body` 用 `[]byte` 而非 `string`：调用方 `streamCompletion` 手里就是 `raw []byte`，传 `[]byte` 免一次转换；只在进签名器时 `string(body)` 转一次，转换点集中在 `signChatCompletionURL` 内部。
- 最小改动边界：未顺手重构 `buildSkillOption` 的固定取值（`ModelItemKey:"0"` / `ReasoningEffort:"3"` / `ModeID:"1"`）、未动 `skillExt` 的 6 键、未动 `aggregateParams` 三处 `model_item_key` 一致性、未动 `statusCodeHint` 其余 9 个分支。
- 删除的符号全部是形状改造后不再被引用的本轮相关符号（`clientUserAgent` / `doubaoClientAID` / `buildClientQuery` / `clientEndpointURL` / `workDeviceName` / `workHomeDir` / `clientOption` / `taskInput` / `agentsMD`），不是历史死代码清理；删除后 `go build` 无未使用导入（`protocol.go` 的 `"os"` 随 `workDeviceName` 一并删除）。
- 未做过度抽象：22 个默认值字段的赋值放在两个 option 构造函数的「步骤 3」里，没有抽公共 helper——两条管线的字段来源不同（技能管线由 `skillExt` 负责、工作模式由 `workModeExt` 负责），抽 helper 必须传管线标志，属为未来扩展提前抽象。

## STYLE-05 结构与落点（结构体角色 / 目录位置 / 依赖方向）

Owner：`package-structure-rules`。

- 结构体角色分层正确：`recoveryOption` 是纯请求体载荷类型，紧邻 `completionOption`；`completionOption` 仍是请求体组装类型，未混入业务逻辑；`generalTaskParam` / `agentTaskParam` 保持「请求体嵌套块」角色。
- 依赖方向未变：`completion.go` 依赖 `signChatCompletionURL`（同包 `sign.go`），未出现反向依赖；`active_ping.go` 新增 `"sync/atomic"` 是标准库，未引入新依赖。
- 目录零变动：未新增目录、未移动文件。新增测试文件 `doubao/active_ping_prompt_test.go` 与既有 `doubao/active_ping_test.go` 同目录同前缀，命名 `<被测文件>_<主题>_test.go` 一致。

## STYLE-06 未命中

本轮无目录结构调整、无模块边界变更、无跨层依赖新增，路由未命中。目录归位需求由 STYLE-05 覆盖。

## STYLE-07 未命中

本轮无前端组件、无页面结构、无样式资源改动，路由未命中。

## STYLE-08 语言与框架特异写法

Owner：`golang-patterns`。

- 用 `*int64` 表达「字段存在但值为 null」：`LastMessageIndex *int64` + `var lastIndex *int64; if lastMessageIndex > 0 { lastIndex = &lastMessageIndex }`。这是 Go 里表达 JSON `null` 的标准做法，无法用 `omitempty` 或 `0` 替代——上游要的是显式的 `"last_message_index": null`，`0` 会被当成「第 0 条消息」。
- `[]any{}` 与 `map[string]any{}` 的空值区分：`connector_info_list` / `edit_query_id` / `regen_query_id` 必须序列化成 `[]`，`related_deleted_message_ids` 必须序列化成 `{}`。用 `[]any{}` 与 `map[string]any{}` 而不是字符串 `"[]"`——第一版用字符串直接换来 `710020202 common invalid param`。
- 原子计数轮换：`atomic.AddUint64(&testPingCounter, 1)` 而非 `time.Now().UnixNano() % n`。后者在 Windows 上失效——时钟精度约 15.6 ms，纳秒低位在连续调用里不变，`TestTestPingPromptRotates` 报「连续 32 次只出现 1 条」。
- 无 goroutine、无 channel、无 `defer` 新增，本轮未触碰并发路径。

## STYLE-09 测试资产与编码前契约

Owner：`test-program-rules`、`code-generation-style-rules`。

- 生产代码零测试专用分支：`completion.go` / `sign.go` / `protocol.go` / `models.go` / `active_ping.go` 内无 `if testing`、无以 `_test` 结尾的导出符号、无测试桩。唯一测试钩子 `completionEndpointOverride`（`swapCompletionEndpoint`）是既有机制，本轮未改其语义。
- 新测试随被测文件同目录（`doubao/*_test.go`），符合本仓库 c-shared 插件的既有测试落点，未引入根 `test/` 镜像目录——该仓库所有插件 UT 都在插件目录内，属稳定局部约定。
- 三个新用例断言的是**可观察行为**而非实现细节：`TestWorkOptionCarriesWebDefaults` 断序列化后的 JSON 键是否存在（而非字段是否被赋值）、`TestSignCoversRequestBody` 断两次签名的 `a_bogus` 值不同、`TestTestPingPromptRotates` 断连续 32 次的覆盖集合。改实现不改行为时用例不会误红。
- 测试探针（`zz_*.go` / `zz_*.js` / `zz_*.py`）全部留在临时目录 `F:\cpa-plugin\cpa-shim-manual`，收口前整目录删除，未进入工作树。

## 结论

`STYLE: PASS`。命中项全部通过，未命中项已说明原因；本轮唯一格式化动作（`completion.go` 的 `gofmt -w`）由新增字段的列宽变化直接触发，属必要变更。
