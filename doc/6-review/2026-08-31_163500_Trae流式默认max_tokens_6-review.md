# 6-review 风格回归：Trae 流式默认 max_tokens

- 日期：2026-08-31 16:35 (GMT+8)
- 改动范围：`traework/upstream.go`（+9）、`traework/upstream_model_test.go`（+26）
- 结论：**STYLE: PASS**

## 检查项

| 检查项 | 结果 | 说明 |
| --- | --- | --- |
| 格式 / 换行 | PASS | gofmt（CRLF→LF 临时转换后）无差异；`git diff --check` PASS，无尾随空白 / 冲突标记 |
| UTF-8 | PASS | 新增常量注释、测试注释均为 UTF-8 中文，无乱码 |
| 命名 | PASS | `streamDefaultMaxTokens` 跟随既有 `maxBodyBytes` 常量命名风格（驼峰 + 语义）；测试函数名 `TestBuildTraePayloadStreamDefaultMaxTokens` 跟随 `TestResolveModelOptions*` 命名习惯 |
| 注释 | PASS | 常量处注释说明"为什么"（上游极小上限导致长任务刚开口就 done）；函数分支注释说明语义边界（仅流式、非流式保持原样）；测试沿用项目 `[参数]/[返回]/最近修改时间` 契约注释 |
| 局部写法 | PASS | `if stream && maxTokens <= 0` 单行分支，贴合既有 `if maxTokens > 0` 写法；未引入外部模板风格 |
| 测试资产归位 | PASS | 追加到既有 `traework/upstream_model_test.go`（`package main` 同包测试），未新建临时目录 / 未漂移 |
| 目录位置 / 依赖方向 | PASS | 改动仅限 `traework/` 包内，无新增依赖 |
| 哨兵 / 临时文件残留 | PASS | 行为哨兵（临时移除分支验证 FAIL）已恢复，无 `哨兵` 残留；临时文件已清理 |
| 改动最小化 | PASS | 仅补默认值分支 + 常量 + 回归测试，未顺手重构 / 改无关代码 |

## 证据

- `cgo-shim build/vet/test` 全绿（`go build` / `go vet` / `go test ./...` 均 OK）
- 行为哨兵：临时移除补默认值分支后 `TestBuildTraePayloadStreamDefaultMaxTokens` FAIL（`stream max_tokens = <nil>, want 20000`），证明测试真实执行且精确覆盖行为
- `gofmt -l` 对 `upstream.go` / `upstream_model_test.go` 均无输出
- `git diff --check` PASS

## 说明

- 本 6-review 只判断风格 / 位置 / 格式 / 可读性 / 目录归位，不判断业务正确性、需求覆盖或发布放行。
- 既有 `traework/*_test.go` 位于源码目录是历史事实（曾标 FIX_REQUIRED），本轮未新增生产测试专用入口，未扩大该既有问题。
