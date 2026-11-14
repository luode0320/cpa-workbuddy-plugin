# doubao 工作模式全模型 503：顶层 `option.conversation_mode` 写字符串被上游 710020202 拒绝的根因与修复

结论：**报错文案长得像 Content-Type 问题，实际是字段类型的「同名不同型」陷阱。** 插件把顶层 `option.conversation_mode` 写成了字符串 `"2"`（`strconv.Itoa(workConversationMode)`），而上游对该键要求 **JSON 整数**；上游以 HTTP 200 + SSE 错误帧 `{"error_code":710020202,"error_msg":"common invalid param"}` 拒绝，插件的 `statusCodeHint` 再把 710020202 翻译成「请确认 Content-Type 为 application/json 且请求体结构完整」，于是用户的排查方向被完全误导到请求头。同一份请求体里 `aggregate_params.conversation_mode` 反而**必须**是字符串 `"2"`，两处同名字段类型相反、不可互换——这是本轮最关键的事实。影响：所有工作模式模型（`deepseek-v4-flash` / `deepseek-v4-pro` / `auto` / `default`）全部不可用；范围：只改该字段类型与赋值 + 对应测试断言；非范围：不改路由、failover、SSE 解析、账号逻辑与面板；完成标准：整数写法下上游完整返回正文，且序列化层有用例把「顶层整数 / aggregate_params 字符串」钉死。

## 问题现象

- 用户侧触发路径：CPAMC 面板把 `doubao/deepseek-v4-pro` 的 OAuth 模型别名设为 `deepseek-pro`，客户端以 `deepseek-pro` 发起对话。
- 报错原文：

  ```json
  {"message":"auth_unavailable: no auth available (providers=doubao-provider, model=deepseek-pro; last upstream error: 上游拒绝了请求参数：请确认 Content-Type 为 application/json 且请求体结构完整)","type":"server_error","param":"","code":"internal_server_error"}
  ```

- 宿主实际下发的是 `model=doubao/deepseek-v4-pro`，`deepseek-pro` 只是对外别名（日志 `alias=deepseek-pro`），所以「不是 pro 特有的问题」这点在现象层完全看不出来。

## 影响范围

- 受影响模型（生产实测全部同错）：`doubao/deepseek-v4-flash`、`doubao/deepseek-v4-pro`、`doubao/default`、`doubao/auto`。
- 受影响账号：同一时刻两个账号各失败一次（`doubao-adcd31d29873b4c8.json` 与 `doubao-c754e5d56ed62480.json`），说明请求体与账号无关。
- 不受影响：网页端链路（`/alice/basic/launch` 拉模型、面板 `/models`、书签授权），它们走 `applyUpstreamHeaders(req, false)` 且不带该字段。
- failover 行为副作用：710020202 不在 `isAccountFailure` / `shouldRotate` 集合内，故不冷却、不换号，只在同一账号上失败一次即 break——这也是日志里「先 a 后 b 各一次」是宿主层重试而非插件换号的原因。

## 出现环境

- 插件：doubao-provider 0.1.9（生产热重载于 15:08:34）。
- 生产：容器 `cli-proxy-api`（host 网络，端口 8317），配置 `/CLIProxyAPI/mysqlstore/mysqlstore/config/config.yaml`。
- 复现时间：2026-11-14 15:47:40 / 15:47:42（用户首次反馈）、15:58:55 / 15:59:06（四模型 A/B 复现）。

## 定位过程

1. **排除 Content-Type**：`protocol.go:43-48` 已确认 `/chat/completion` 只接受纯 `application/json`（带 charset 的是 `/im/*`），`streamCompletion` 实际设的就是纯值，与报错文案不符 → 说明 710020202 不是 HTTP 错误而是业务码，文案只是插件的本地翻译。
2. **复刻上游直连**：在宿主机用生产账号 Cookie 写探针复刻插件请求（19 键 client query、`Content-Type: application/json`、`Accept: text/event-stream`、客户端 UA、`agw-js-conv`、`last-event-id: undefined`、`x-flow-trace`）。首轮即复现：`HTTP 200` → `SSE_HEARTBEAT` → `STREAM_ERROR {"error_code":710020202,"error_msg":"common invalid param",...}`。**心跳先到**证明 URL / query / Cookie / header 全部通过，拒绝点落在请求体反序列化。
3. **逐级累加消融（S0→S9）**：以「极简 body」（无 `general_task_param`、无 `conversation_init_ext`、`agent_mode=0`）为基线，逐级累加工作模式字段。S0~S6 全 OK，**S7 起 ERR**；S7 一次加入三个字段：顶层 `conversation_mode="2"`、`message_storage_type=0`、`sse_recv_event_options` + `support_lazy_fetch_stream`。
4. **拆分 S7**：`allbutconv`（只加后两个字段，不加 conversation_mode）→ OK；`conv2int`（conversation_mode 写整数 2）→ OK → 嫌疑锁定到 `conversation_mode` 的类型。
5. **决定性三变体对比**：`"2"` → ERR 710020202；`2` → OK；删键 → OK。根因定论。
6. **对照外部参考实现**：`yyydsz/doubao2api`（main）的 `internal/doubao/doubao.go` 第 184 行就是 `"conversation_mode": modeInt(o.WorkMode)`（整数），第 216 行 `aggregate_params` 内才是 `itoa(...)`（字符串），与实测完全一致；插件在 0.1.7 按其重写时把顶层这处写成了 `strconv.Itoa`，属迁移时的类型误抄。
7. **已排除的其它嫌疑**：`last_section_id` / `last_message_index` 有无（两变体都错）、技能模式 `agent_mode=2`（也错）、`auto` 模型档位（也错）、query `aid=582478` vs `497858`（极简两者都 OK、工作模式两者都 ERR）、`message_storage_type` 与 `support_lazy_fetch_stream`（去掉后仍 OK）。

## 根因

`doubao/completion.go` 的 `completionOption.ConversationMode` 声明为 `string`，`buildWorkOption` 用 `strconv.Itoa(workConversationMode)` 赋值，序列化出 `"conversation_mode":"2"`；上游对该路径要求整数，收到字符串即判 `common invalid param`。

同一份请求体里 `aggregateParams.ConversationMode` 是另一个类型：

| 位置 | JSON 路径 | 要求的类型 | 正确写法 |
|---|---|---|---|
| 顶层 option | `option.conversation_mode` | **整数** | `2` |
| 聚合参数 | `option.aggregate_params.conversation_mode` | **字符串** | `"2"` |

## 修复

- `doubao/completion.go:123-127`：`ConversationMode string` → `ConversationMode int`（保留 `json:"conversation_mode,omitempty"`），字段注释写明「顶层整数、aggregate_params 字符串、写错回 HTTP 200 + SSE 错误帧」，把陷阱钉在定义处。
- `doubao/completion.go:451`：`ConversationMode: strconv.Itoa(workConversationMode)` → `ConversationMode: workConversationMode`。
- `doubao/completion.go:473` 保持不变：`AggregateParams.ConversationMode: strconv.Itoa(workConversationMode)`。
- `doubao/workmode_test.go`：结构体层断言改为比较整数；`TestWorkOptionJSONShape` 追加序列化层断言——`option["conversation_mode"]` 必须是 `float64` 且等于 `workConversationMode`，`option["aggregate_params"].(map[string]any)["conversation_mode"]` 必须是 `string` 且等于 `strconv.Itoa(workConversationMode)`。只断结构体不能防住 tag 写错或类型回退，必须看序列化结果。

## 验证

- `python scripts/cgo-shim-build.py doubao` → `go build` / `go vet` / `go test` 全绿（`ok github.com/luode0320/cpa-workbuddy-plugin/doubao 2.007s`）。
- 哨兵验证：把断言临时改成 `!= workConversationMode+97`，shim 输出 `--- FAIL: TestBuildWorkOptionContract`（`workmode_test.go:47`）与 `--- FAIL: TestWorkOptionJSONShape`（`workmode_test.go:370`），证明新断言真实进编译并生效；恢复后重新全绿。
- 上游实测：整数写法下事件序列 `SSE_HEARTBEAT,SSE_ACK,FULL_MSG_NOTIFY,STREAM_TIMEOUT_CONTROL,STREAM_MSG_NOTIFY,STREAM_CHUNK,...,SSE_REPLY_END`，`deepseek-v4-pro` 与 `deepseek-v4-flash` 都返回正文（`1+1=?` → `2`），无 error 帧。
- 测试证据：`doc/5-tests/2026-11-14_162000_doubao工作模式710020202上游消融定位验证.md`

## 经验

- 上游业务码常以 HTTP 200 + SSE 错误帧返回，`statusCodeHint` 的本地翻译会误导排查方向；排障第一步应是拿原始帧，而不是相信翻译文案。
- 「同名不同型」是跨层协议迁移的高发缺陷：迁移参考实现时，逐字段核对类型比整体照抄更可靠。
- 只断结构体字段的测试挡不住序列化层退化；协议字段必须断言 JSON 输出类型。
