---
schema_version: 1
doc_id: TEST-GEMINI-PROVIDER-INTEGRATION-20261005
doc_type: test
source_ids: [GOAL-INTEGRATE-GEMINI-CLI]
status: accepted
version: v1.0
current_slice: 集成 Gemini Provider 插件与多插件 Token 用量统一统计
updated_at: 2026-10-05 18:40:00
reader_level: business_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
---

# 集成 Gemini Provider 与多插件 Token 用量测试

结论：已成功将开源插件 `cpa-plugin-gemini-cli` 引入本仓库，发布名称为 "Gemini Provider"（ID: `gemini-provider`，版本: `0.1.0`）。插件完成标准 C ABI 导出、生命周期管理以及共享 NDJSON token 用量追加通道；`token-usage-tracker` 统一用量插件已完成适配，能够正确解析识别 `gemini` / `gemini-cli` / `gemini-provider` 并统一归一化展示为 `"Gemini"`。所有单测、端到端 Feed 摄取测试、CGO-shim 隔离构建验证及 CI 注册表校验全部通过。

## 文档信息

- 来源：用户需求将开源插件 `https://github.com/router-for-me/cpa-plugin-gemini-cli` 加入到当前项目，形成包含服务商插件与 Token 用量监控的统一工程，发布名称指定为 "Gemini Provider"。
- 实施记录：
  1. 新增 `gemini/` 插件目录，收敛 internal 模块路径为 `github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/...`；
  2. 创建 `gemini/main.go` 导出标准 C ABI（`cliproxy_plugin_init`、`cliproxyPluginCall`、`cliproxyPluginFree`、`cliproxyPluginShutdown` 等）；
  3. 编写 `gemini/usage_feed.go` 与 `gemini/usage.go`，将流式/非流式请求的 usageMetadata 提取为标准结构并写入共享 `<root>/data/token-usage-feed.ndjson`；
  4. 修改 `token-usage-tracker/usage_stats/auth_identity.go`，在 `displayAuthProvider` 中支持 Gemini Provider 归一化展示；
  5. 修改 `.github/workflows/build.yml` 支持 `gemini-provider` CI 编译、交叉编译与 Release；
  6. 更新 `registry.json`，补充 `gemini-provider` 7 平台 artifacts 注册表条目。
- 真实验证：
  1. `gemini/main_test.go`：覆盖 capabilities 暴露、usageMetadata 提取解析与流式统计；
  2. `token-usage-tracker/feed_ingest_test.go`：新增 `TestFeedIngestGeminiProvider`，断言从 feed 读取 gemini 记录并正确归一化展示与聚合；
  3. 注入必失败哨兵用例证实新测试真实参与编译和执行；
  4. `python scripts/cgo-shim-build.py <plugin>` 针对 5 个插件全量执行 build、vet、test 全部通过；
  5. `python scripts/validate-registry.py registry.json` 校验 5 插件配置合法有效。
- 性能验证：N/A，用量采集采用异步追加或带缓冲写入，NDJSON 增量解析不阻塞主推理通道。

## 验证策略

1. **C ABI 与宿主协议兼容性**：验证 `gemini/main.go` 能够正确响应 `cliproxy_plugin_init` 与 `plugin.get_info`，正确返回插件名称 `"Gemini Provider"` 及元数据。
2. **Token 用量提取与跨插件 Feed 协同**：
   - 模拟 Gemini 上游响应中的 `usageMetadata`（`promptTokenCount`、`candidatesTokenCount`、`totalTokenCount`）；
   - 验证流式首包纳秒计时与最终用量结算；
   - 验证追加写入的 NDJSON 格式被 `token-usage-tracker` 的 `IngestFeed` 读取后，能够正确归一化为 Provider `"Gemini"` 并计入聚合统计。
3. **防假测试与哨兵编译拦截**：
   - 在 `gemini/main_test.go` 与 `token-usage-tracker/feed_ingest_test.go` 中临时注入 `t.Fatal("sentinel failure")`，确认执行 `cgo-shim-build.py` 时产生真实 FAIL 输出，之后恢复正常代码。
4. **回归矩阵**：
   - 全量验证仓库内全部 5 个插件（`gemini`、`token-usage-tracker`、`traework`、`workbuddy`、`qoderwork`）构建、vet 与测试。

## 回归范围与证据

| 优先级 | 验证目标 | 判据 | 结果 |
| --- | --- | --- | --- |
| P0 | Gemini 插件 C ABI 导出与元数据暴露 | `cliproxy_plugin_init` 返回 JSON 包含 `"name":"Gemini Provider"`、`"id":"gemini-provider"` | PASS（`TestGeminiPlugin_Capabilities`） |
| P0 | Gemini 用量元数据提取 | `extractGeminiUsage` 正确解析 input=12, output=34, total=46 | PASS（`TestGeminiPlugin_ExtractGeminiUsage`） |
| P0 | Token 用量跨插件端到端摄取 | `token-usage-tracker` 摄取 gemini feed 记录后展示为 `"Gemini"` | PASS（`TestFeedIngestGeminiProvider`） |
| P0 | 哨兵防伪验证 | 注入错误断言时必须触发真实失败 | PASS（哨兵测试已验证通过） |
| P0 | CGO 隔离构建验证 | 5 插件 `cgo-shim-build.py` 全绿通过 | PASS（全绿） |
| P0 | 插件注册表 Schema v2 校验 | `validate-registry.py` 返回 5 plugin(s) OK | PASS（`OK registry.json: 5 plugin(s)`） |

### 真实执行命令与测试输出

1. `python scripts/cgo-shim-build.py gemini`
```text
[cgo-shim] shim dir: F:\cpa-plugin\cpa-shim-ss6orftk\gemini
[cgo-shim] running: go build ./... ...
[cgo-shim] OK: go build ./...
[cgo-shim] running: go vet ./... ...
[cgo-shim] OK: go vet ./...
[cgo-shim] running: go test ./... ...
ok  	github.com/luode0320/cpa-workbuddy-plugin/gemini	0.142s
ok  	github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/auth	0.674s
ok  	github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/compat	0.379s
ok  	github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/executor	0.643s
?   	github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/fingerprint	[no test files]
ok  	github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/models	0.420s
ok  	github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/plugin	0.170s
ok  	github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/thinking	0.443s
ok  	github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/translator	0.154s
[cgo-shim] OK: go test ./...
[cgo-shim] all green (gemini)
```

2. `python scripts/cgo-shim-build.py token-usage-tracker`
```text
[cgo-shim] shim dir: F:\cpa-plugin\cpa-shim-qelgrxf2\token-usage-tracker
[cgo-shim] running: go build ./... ...
[cgo-shim] OK: go build ./...
[cgo-shim] running: go vet ./... ...
[cgo-shim] OK: go vet ./...
[cgo-shim] running: go test ./... ...
ok  	github.com/luode0320/cpa-workbuddy-plugin/token-usage-tracker	0.782s
ok  	github.com/luode0320/cpa-workbuddy-plugin/token-usage-tracker/usage_stats	0.766s
[cgo-shim] OK: go test ./...
[cgo-shim] all green (token-usage-tracker)
```

3. `python scripts/validate-registry.py registry.json`
```text
OK registry.json: 5 plugin(s), schema_version=2
```

4. 全量回归测试：
- `traework`: `cgo-shim-build.py traework` -> all green (1.593s)
- `workbuddy`: `cgo-shim-build.py workbuddy` -> all green (11.101s)
- `qoderwork`: `cgo-shim-build.py qoderwork` -> all green (7.014s)

## 静态门禁检查

- `UTF8_BOM: PASS`（所有改动与新增文件均无 UTF-8 BOM）
- `EOL: PASS`（遵循既有文件换行约定）
- `TRAILING_WS: PASS`（代码无新增多余尾随空白）
- `COMPILATION: PASS`（全部模块编译通过且 `go vet` 无警报）

## 结论与放行判定

新增的 `gemini-provider` 插件已完整融入本项目的多插件架构与发布体系。Token 用量统计模块无缝纳管了 Gemini 的请求用量数据，CI 流水线和插件注册表已齐备。经全量编译构建与单测回归，确认系统行为正常，符合交付标准，准予放行提交。

## 附录

- 插件版本：`gemini/VERSION` = `0.1.0`
- 注册表版本：`registry.json` 中 `gemini-provider` 版本 `0.1.0`，包含 7 架构 artifacts 下载配置
- 共享管道文件路径约定：`<CLIProxyAPI root>/data/token-usage-feed.ndjson`
