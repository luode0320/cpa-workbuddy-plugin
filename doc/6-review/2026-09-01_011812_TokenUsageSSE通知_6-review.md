# 6-review 风格回归：dashboard SSE 通知（usage/events）

- 日期：2026-09-01 01:18 (GMT+8)
- 改动范围：`token-usage-tracker/feed_ingest.go`（+31）、`token-usage-tracker/management.go`（+24）、`token-usage-tracker/feed_ingest_test.go`（+79）、`token-usage-tracker/usage_stats/dashboard.go`（+2）
- 结论：**STYLE: PASS**

## 背景（宿主约束导致的方案形态）

CPA 宿主 v7.2.129 的 pluginhost management/resource 桥接是一次性写回（`WriteHeader` + 单次 `Write(body)`，无增量 Flush、无 ws 升级），插件侧无任何长连接通道。因此「feed→前端推送」落为 **SSE 短连接通知**：新增 `/usage/events` 资源路由，每次返回当前 feed 序列号（`retry: 2000\n\ndata: {"seq":N}\n\n`），前端 EventSource 订阅并在序列前进时触发 `load()` 刷新。行为实测过宿主 sanitize 对 `text/event-stream` 原样透传、桥接单次写回 47 字节完整（sdkcopy 临时测试，已清理）。

## 检查项

| 检查项 | 结果 | 说明 |
| --- | --- | --- |
| 格式 / 换行 | PASS | 新增代码区 gofmt-clean（CRLF→LF 临时转换后过滤新符号无差异）；`git diff --check -- token-usage-tracker/` PASS；既有历史 gofmt 差异（trackerConfig 对齐 / management.go 导入顺序 / usageRecordJSON 对齐）为本轮之前就存在，未顺手批量格式化 |
| UTF-8 | PASS | 新增注释与测试均为 UTF-8 中文，无乱码（4 文件逐一校验） |
| 命名 | PASS | `feedNotifierMu/feedNotifierSeq` 跟随既有 `feedSyncMu/feedOffset` 命名风格；`serveUsageEvents` 跟随 `serveStatsResource` 动词短语；测试 `TestFeedNotifierSSE` 跟随 `TestFeedIngestEndToEnd` 习惯 |
| 注释 | PASS | notifier 注释说明"为什么"（宿主单次写回→SSE 短连接→EventSource 重连）；`serveUsageEvents` 注释说明宿主约束；测试沿用项目 `[参数]` 契约注释风格 |
| 局部写法 | PASS | `feedNotifierBump/Latest` 与既有 `saveFeedOffset/loadFeedOffset` 的锁+读写模式一致；`serveUsageEvents` 构造 `ManagementResponse` 的方式贴合 `mgmtHTMLResponse/mgmtJSONResponse` |
| 测试资产归位 | PASS | 追加到既有 `token-usage-tracker/feed_ingest_test.go`（`package main` 同包测试），未新建临时目录 |
| 目录位置 / 依赖方向 | PASS | 改动仅限 `token-usage-tracker/` 包内，未新增第三方依赖（仅用标准库 `fmt`） |
| 哨兵 / 临时文件残留 | PASS | 行为哨兵（临时移除 `feedNotifierBump()`）已恢复；`dump_html_test.go`、`tmp_dashboard_*.html/js`、`tmp_ws_probe/`（含 sdkcopy 探测副本）均已清理；无残留 |
| 改动最小化 | PASS | 仅新增 notifier + events 路由 + 前端订阅 + 回归测试，未顺手重构 / 改无关代码 |

## 证据

- `cgo-shim build/vet/test` 全绿（`go build` / `go vet` / `go test ./...` 均 OK，多次）
- 行为哨兵：临时移除 `feedNotifierBump()` 后 `TestFeedNotifierSSE` FAIL（`feedNotifierLatest() = 0 after one line, want 1`），证明测试真实执行且精确覆盖行为
- 前端 JS：`DashboardHTML` + `fullDashboardHTML` 两个变体的 4 个 `<script>` 块 `node --check` 全部 exit 0（含新增 `startUsageEvents`）
- `git diff --check -- token-usage-tracker/` PASS，无尾随空白 / 冲突标记
- 4 个改动文件 UTF-8 校验全部通过

## 说明

- 本 6-review 只判断风格 / 位置 / 格式 / 可读性 / 目录归位，不判断业务正确性、需求覆盖或发布放行。
- SSE 通知是宿主无长连接能力约束下的最小可行形态（「SSE 通知 + REST 拉取」），不是真正的 WebSocket 推送；真 ws 需宿主开放插件侧长连接通道，属宿主能力扩展，非本插件可单方面实现。
- 前端 15 秒 `setInterval` 轮询保留为兜底（EventSource 失败 / full-mode 页面不启用 SSE 时仍能刷新），SSE 仅作为实时性增强。
