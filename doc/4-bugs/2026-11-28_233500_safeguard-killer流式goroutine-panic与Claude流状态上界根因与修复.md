# safeguard-killer 流式 goroutine 的 panic 隔离与 Claude 流状态上界

- 发现时间: 2026-11-28 23:20
- 诊断轮次: 第 2 轮巡检（0.1.13 上线后）
- 影响版本: 0.1.13 及以前（0.1.14 修复）
- 严重级别: P1（潜在全局不可用，生产未触发）

## 巡检方法与结论

第 2 轮巡检按「配置项使用面 → 静态检查全量 → 主执行路径逐段读」推进：

| 手段 | 结论 |
| --- | --- |
| 配置项使用面 | `pluginConfig` / `featureConfig` 每个字段都能找到生产代码读取点（`NoticePrefix` 经 `effectiveNoticePrefix`、`executorFormats` 用于 registration），**无死配置** |
| 生产决策分布 | 108 条决策、114 个 attempt **全部 served**，无 fallback、无失败、无 `model_group_exhausted` |
| `staticcheck -checks all`（shim） | 只剩 `stream.go:79 ST1005` 一条真告警，其余全是 cgo 剥离后的 C ABI 假象 |
| 主执行路径 | `executeGroup` / `executeGroupStream` / `prefetchStream` / `forwardStream` / `emitExhaustedNoticeStream` 逐段读 |

排除项（读代码后确认不是缺陷，登记备查）：

- `prefetchStream` 的**每条错误路径都关闭了源上游流**（`refusal.go:875/880/889/898/905/911`），因此 `executeGroupStream` 在 prefetch 失败时 `continue` 到下一个模型**不会**泄漏流句柄——这一点与最初的怀疑相反。
- `request.intercept_before` 与 `request.intercept_after` 共用同一个 handler 是**宿主语义**：宿主 `rpc_client.go:719/728` 把两个 method 都映射到同一个 `InterceptRequestAfterAuth` 回调（测试桩 `test_helpers_test.go:81/94/102` 同样处理），插件无法也不需要区分阶段。
- `handleStreamIntercept` 在 `ChunkIndex == -1` 时早退是**正确的**：`-1` 是 `pluginapi.StreamChunkHeaderInitIndex`，表示「仅响应头的初始化调用」，不是流结束。

## 缺陷 1：流式转发 goroutine 的 panic 没有边界（P1）

### 证据

`groups.go:336` 与 `refusal.go:342` 都用 `go` 启动直接写客户端流的 goroutine：

```go
go forwardStream(resp.StreamID, req.StreamID, req.HostCallbackID, prefetched, done, shiftIndices, stampSafeguards, cfg.Features.FallbackOnRefusal, session, group, model)
go emitExhaustedNoticeStream(req.StreamID, req.HostCallbackID, effectiveFormat(req.Format, req.SourceFormat), group.ID, notice)
```

全包 `recover()` 出现次数：**0**（`grep -c 'recover()' *.go` 无命中）。

### 为什么是 P1

c-shared 插件没有进程隔离：插件 `.so` 被加载在宿主 CPA 进程内。宿主对**自己的调用链**有 fuse/recover（`internal/pluginhost/adapters_interceptors.go:85` `h.fusePlugin`），但那是包在「宿主调插件」方向上的；插件自己 `go` 出去、再由它回调宿主的 goroutine **不在任何 recover 范围内**。一旦 `forwardStream` 里 panic（JSON 解码异常形态、map 索引、切片越界、宿主返回结构变化），整个 CPA 进程终止——同一容器内十几个插件与全部在途请求一起失效，且无法自动化恢复。

生产 0.1.13 运行 40 分钟日志无 `panic`（0 次），属**未触发但代价最高**的一类缺陷。

### 修复

两个 goroutine 顶部各加一个 `defer recover()`：

```go
defer func() {
    if recovered := recover(); recovered != nil {
        closeStream(target, callbackID, fmt.Sprintf("stream forward panic: %v", recovered))
        _, _ = hostCall(methodHostModelStreamClose, hostStreamRef{StreamID: source, HostCallbackID: callbackID})
    }
}()
```

`forwardStream` 同时释放两条流（客户端目标流 + 源上游流），`emitExhaustedNoticeStream` 只关目标流。客户端因此拿到明确错误而不是一个半开连接。

## 缺陷 2：Claude 流缺 request ID 时整条流失败（P2）

### 证据

```go
func normalizeClaudeStream(requestID string, body []byte) ([]byte, error) {
    if requestID == "" {
        return nil, fmt.Errorf("Claude stream normalization requires a request ID")
    }
```

`handleStreamIntercept` 把这个 error 直接抛给宿主，宿主把该 chunk 判为失败——客户端这一轮的**全部输出**都拿不到。而实际缺的只是一个「跨 chunk 状态表的键」，放弃事件重编号（把多个 text 块合并、重排 index）并不影响内容完整性。

### 修复

缺 `requestID` 时原样透传 body、不建状态：

```go
if requestID == "" {
    return body, nil
}
```

顺带消掉了 `staticcheck` 唯一一条真实告警（ST1005 错误串首字母大写）。

## 缺陷 3：Claude 流状态表没有上界（P2）

### 证据

`claudeStreams.states` 的唯一清理入口是 `cleanupClaudeStream`（由 `request.complete` 回调触发）。宿主正常路径一定回调，但一旦库外层路径漏发（例如宿主在归并阶段提前返回、或插件 emit 失败后宿主放弃该请求），state 永久驻留，且每条 state 持有 `pending` / `frame` 缓冲与整段 `text` Builder。

### 修复

`claudeStreamState` 增加 `created` 字段，状态表达到 `maxClaudeStreamStates = 512` 时淘汰创建最早的一条（`evictClaudeStreamLocked`，调用方已持锁）。正常路径仍是 `request.complete` 精确清理，上界只兜漏网情况。

## 验证

- 门禁：`python scripts/cgo-shim-build.py safeguard-killer` → build / vet / test 全绿（`PASS=61 FAIL=0`）
- 哨兵反证 3 条全部 KILLED：recover 主体改空操作 → `TestForwardStreamRecoversFromPanic` 失败；缺 ID 改回返回错误 → `TestNormalizeClaudeStreamWithoutRequestIDPassesThrough` 失败；删掉淘汰调用 → `TestClaudeStreamStatesEvictedAtCapacity` 失败
- gofmt：`gofmt -l` 对全部 `.go` 无输出
- `staticcheck -checks all`：非 C ABI 告警 0

## 涉及文件

| 文件 | 改动 |
| --- | --- |
| `safeguard-killer/groups.go` | `forwardStream` 加 panic 兜底 |
| `safeguard-killer/refusal.go` | `emitExhaustedNoticeStream` 加 panic 兜底 |
| `safeguard-killer/stream.go` | 缺 requestID 透传；`created` 字段 + `evictClaudeStreamLocked` + `maxClaudeStreamStates` |
| `safeguard-killer/stream_test.go` | 新增 2 条用例 + `resetClaudeStreams` |
| `safeguard-killer/groups_test.go` | 新增 `TestForwardStreamRecoversFromPanic` |