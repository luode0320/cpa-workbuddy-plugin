# 项目代码风格

## 核心风格

### Trae SSE 必须以明确完成事件收口
- 别名: SSE 终止契约, event done
- 类型: 流式协议风格
- 示例: `if t.hasDone { return nil }`
- 说明: HTTP 2xx 和已产生 output 都不能替代 Trae `event:done`；部分输出后 EOF 按截断失败处理。
- 来源: `traework/stream.go` 的 `traeSSETerminal.validate`
- 适用范围: TraeWork 同步聚合、同步流式和异步流式响应
- 更新时间: 2026-08-30
- 状态: 启用

### 异步宿主流桥透传 callback context
- 别名: HostCallbackID 透传, 宿主流生命周期
- 类型: 异步与并发风格
- 示例: `hostHTTPDoStream(req, hostCallbackID)`
- 说明: 异步 executor 使用 CPA 宿主 HTTP 流桥时，将请求的 `HostCallbackID` 放入外层 wire，使客户端取消可以沿 callback context 传播到上游；流结束时显式关闭句柄。
- 来源: `traework/executor.go`、`traework/host_bridge.go`、`traework/upstream.go`
- 适用范围: TraeWork 长生命周期异步 HTTP 流
- 更新时间: 2026-08-30
- 状态: 启用

## 命名与注释

### 修改函数使用中文元信息和就近步骤编号
- 别名: 函数注释元信息, 编号步骤注释
- 类型: 注释风格
- 示例: `// [参数] ...`、`// [返回] ...`、`// 最近修改时间：yyyy-MM-dd HH:mm:ss；改动原因：...`
- 说明: 修改函数签名、流程、分支、返回值或关键调用时同步更新中文元信息；超过 5 行的流程块使用就近 `1.`、`2.` 编号说明意图和原因。
- 来源: `traework/executor.go`、`traework/host_bridge.go`、`traework/host_bridge_decode_test.go`
- 适用范围: 本仓库 Go 生产代码和测试代码的本轮改动位点
- 更新时间: 2026-08-30
- 状态: 启用

## 变更记录

- 2026-08-30：基于 Trae `qwen3.8-max` 长流修复，建立项目专属流式终止、宿主 callback 生命周期和中文注释样例。

## 计数锚点区

```yaml
version: 1
anchors:
  - title: Trae SSE 必须以明确完成事件收口
    usage_count: 1
    usage_days: 1
    last_used_at: 2026-09-01
    absorbed_to: null
  - title: 异步宿主流桥透传 callback context
    usage_count: 1
    usage_days: 1
    last_used_at: 2026-09-01
    absorbed_to: null
  - title: 修改函数使用中文元信息和就近步骤编号
    usage_count: 1
    usage_days: 1
    last_used_at: 2026-09-01
    absorbed_to: null
```
