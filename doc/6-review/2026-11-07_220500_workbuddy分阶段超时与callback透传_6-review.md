# 6-review — workbuddy 上游分阶段超时与 HostCallbackID 透传（0.21.14）

## 结论

STYLE: PASS

## 检查范围

本轮改动 6 个文件（5 改 + 1 新增）：

| 文件 | 改动性质 |
|---|---|
| `workbuddy/host_bridge.go` | 补分阶段超时常量 + transport 字段 + wire 结构 + 抽出 `buildHostHTTPStreamWire` 纯函数 + `hostStreamOpenFn` 接缝 + `hostHTTPDoStream` 签名 |
| `workbuddy/main.go` | 两个入口透传 `req.HostCallbackID`；`handleExecExecute` 改用 `executorStreamRequest` 解析 |
| `workbuddy/stream.go` | `pumpUpstreamStream` / `collectUpstreamStream` / `collectUpstreamStreamOnce` / `doExecuteOnce` 四个签名补参并下传 |
| `workbuddy/active_ping.go` | 探活调用点显式传空串 |
| `workbuddy/VERSION` | 0.21.13 → 0.21.14 |
| `workbuddy/host_bridge_timeout_test.go` | 新增，6 组回归用例 |

## 逐项检查

### 格式与排版

- `gofmt -l` 对全部 5 个 Go 文件返回空（CRLF→LF 临时转换后校验，符合仓库「历史 tracked 文件 CRLF」的既有口径）。
- 缩进为 tab，空行分组沿用文件既有节奏；新增 const 块与文件内既有 `const (...)` 写法一致。
- UTF-8：所有新增中文注释与测试文件均为 UTF-8，无乱码，无尾随空白。

### 命名

- 常量 `upstreamDialTimeout` / `upstreamTLSHandshakeTimeout` / `upstreamResponseHeaderTimeout` 遵循文件内 `upstreamBaseCN` 等既有前缀习惯。
- 函数 `buildHostHTTPStreamWire` 遵循同文件 `parseHostHTTPDoResult`、`hostBridgeUnwrap` 的动词+对象命名。
- 变量接缝 `hostStreamOpenFn` 与同构插件 `traework/host_bridge.go:364` 的 `hostStreamOpenFn` 同名同语义，跨插件保持一致。

### 注释

- 全部新增注释为中文，符合仓库规则与 `PROJECT_STYLE.md` 的「修改函数使用中文元信息」条目。
- 被修改函数均带 `[参数]` / `[返回]` / `[最近修改时间]` 元信息：`buildHostHTTPStreamWire`（新增，含 `[参数]`/`[返回]`/时间/原因）、`hostHTTPDoStream`（时间/原因）、`handleExecExecute`（新增元信息块）。
- 常量逐条带单行中文说明，说明的是「挡什么」而非复述字段名。
- 未引入论证式长块；关键决策（60s 取值依据、禁 h2 的原因）保留简短依据说明，符合「注释要交代为什么」。

### 日志

- 本轮未新增日志调用。既有 `log.Printf("[workbuddy] ...")` 前缀风格未被改动。

### 错误处理

- 分阶段超时触发的错误走既有 `hostHTTPDoStream` 返回路径：`err != nil` → `hostHTTPDoStreamDirect` 兜底，或向上返回 `http_error`，与改动前语义一致，未新增分支或吞异常。
- `handleExecExecute` 改用 `executorStreamRequest` 解析：该结构体内嵌 `pluginapi.ExecutorRequest`，字段集合是超集，解析行为不变，无兼容性风险。

### 复用与依赖

- 未引入新第三方依赖；`crypto/tls`、`net` 为标准库。
- 复用同文件既有 `mustJSON`，未新写序列化 helper。
- `hostStreamOpenFn` 接缝沿用 traework 已验证过的模式，未另造抽象。

### 目录归位

- 新增测试 `workbuddy/host_bridge_timeout_test.go` 与生产文件同目录同包（包内白盒测试），符合仓库既有 `*_test.go` 归位习惯（如 `host_bridge_test.go`、`failover_sync_test.go`）。
- 未在根 `test/` 下新增镜像：本仓库 `test/<plugin>/` 用于跨包验证，包内白盒用例历来与源码同目录，本轮跟随既有习惯。

### 局部风格一致性

- 测试文件沿用同目录 `host_bridge_test.go` 的写法：表驱动不明显时用逐条断言 + `t.Fatalf` 带「实际值」。
- 源码级接线断言并非本轮独创：`test/traework/credits_sweep_test.go:122` 已有读源码核对先例。
- 未做任何批量格式化或无关重排；`git diff --stat` 显示 4 个 Go 文件共 +109/−24，全部可追溯到本轮目标。

### 测试资产

- 新增测试文件为长期保留资产，无临时脚本、无 `_tmp|_scratch|_copy` 命名。
- 变异验证期间的临时哨兵用例已移除，`Select-String` 反查确认 `SENTINEL` / `TestSentinelMustFail` 零残留。
- 远端临时脚本（`/tmp/wbstat*.py` 等 18 个）与本机临时文件已在诊断阶段清理完毕。

## 负向边界声明

本 review 只判断风格、格式、位置、注释、日志与可读性，**不构成**：

- 业务正确性判断（分阶段超时是否真的收敛了长尾 → 由真实运行验证回答）；
- 需求覆盖结论；
- 发布放行或最终验收。
