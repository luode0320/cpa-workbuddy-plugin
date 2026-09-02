---
schema_version: 1
template_version: v1
doc_id: STYLE-TRAE-REASONING-GATE-0.1.31-20260902
doc_type: style_regression
source_ids: [PROD-TRAE-504-STREAM-2878-20260902]
status: accepted
version: v1.0
current_slice: traework 0.1.31 FIX-D reasoning 双轴健康度 gate 改动的风格检查
updated_at: 2026-09-02 23:40:00
reader_level: business_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
---

# 6-review：Trae 思考型长推理 reasoning 阶段零字节 504（0.1.31 FIX-D）

结论：本轮 stream.go / stream_pseudo_test.go 改动通过风格回归。影响：`pumpTraeStreamAttempt` 伪完成保护 gate 的门槛累计从只统计 content 改为统计 content+reasoning 双轴健康度——思考型长推理 reasoning 达到 600 字符即 gate 打开、按序释放已缓冲分片并转实时转发，reasoning 阶段客户端持续收到字节，不再触发 nginx 300s 读超时 504。伪完成保护不回归：真伪完成（content+reasoning 双轴都短，合计 <600）仍全程 pending → done 后判伪 → 丢弃换号，零泄漏语义保留。范围：`traework/stream.go` 与 `test/traework/stream_pseudo_test.go`。非范围：本记录不判断业务正确性、不批准发布、不把本地测试写成生产验收。变化：`contentChars` 局部变量改名为 `healthChars` 并累计 `len(text)+len(reasoning)`；函数头注释、第 1 步内注释更新；测试文件新增 `TestPumpTraeStreamAttemptReasoningFlushes` 四断言块。完成标准：真实 cgo-shim 测试先通过（无条件 t.Fatal 哨兵先 FAIL 后删除证明真实编译），格式、UTF-8、空白、目录、命名、注释、日志、错误处理、复用与临时产物检查均无问题。术语说明：`6-review` 只检查代码写法和资产位置，不判断生产行为。验证状态：真实 cgo-shim 测试已通过，风格检查通过；0.1.31 待发布，生产复验待执行。图片资产决策：N/A，原因：只核对源码与测试资产；证据：无 UI 或视觉改动。

## 文档信息

- 来源取证：2026-09-02 CYCLE-04 生产验收，深度思考请求 `stream_id=2878` scheduled 后 5 分钟零字节 → nginx `proxy_read_timeout 300s` 504（无 degrade 日志证明桥健康、reasoning 持续流入但被 gate 压 pending 不转发）。
- 真实测试：`test/traework/stream_pseudo_test.go` 新增 `TestPumpTraeStreamAttemptReasoningFlushes`（reasoning-only ≥600 按序放行不判伪 / 双轴短真伪完成零下发 / reasoning 达标后 content 实时放行 / reasoning-only 短流豁免释放）。
- 规则来源：`PROJECT_STYLE.md`、`code-style-consistency-rules` 和本轮代码风格契约。

## 检查范围

- `traework/stream.go`：`pumpTraeStreamAttempt` gate 的 `healthChars` 双轴累计（content+reasoning）、gate 打开条件、pending 释放逻辑与中文注释更新。
- `test/traework/stream_pseudo_test.go`：新增 reasoning 放行测试的构造（reasoning-only SSE / 双轴混合 / reasoning-then-content）与断言 helper 复用。
- 发布放行：N/A。原因：不属于 `6-review` 职责。证据：0.1.31 发布与生产复验尚未执行。

## 真实测试前置证据

- cgo-shim：`python scripts/cgo-shim-build.py traework` 的 build、vet、test 全部通过，最新 Go 测试耗时 1.403-1.420 秒。
- 哨兵证据：无条件 `t.Fatal("DIRECTED-FAIL-PROBE-REASONING-FLUSH")` 注入新测试函数体 → FAIL（stream_pseudo_test.go:208 命中），删除后全绿——证明新测试真实进入编译执行。
- 定向修正：首版测试场景 2 误用 reasoning-only 构造（两段都进 reasoning 字段），实测触发 reasoning-only 豁免（不判伪）；改为双轴混合（reasoning 短 + content 短）后符合真伪完成语义，场景 2b 单独锁定 reasoning-only 短流豁免语义。
- 静态门禁：`UTF-8: PASS`、`GOFMT: PASS`（CRLF→LF 临时转换后 gofmt -l 为空）、`git diff --check: PASS`。

## 6-review 结论

STYLE: PASS

## 检查清单

| 类别 | 结果 | 证据 |
| --- | --- | --- |
| 格式与空白 | PASS | gofmt -l 两文件全空（LF 临时转换校验）；无尾随空白 |
| 编码 | PASS | 两文件 UTF-8 无 BOM；`git diff --check` 无空白错误 |
| 目录归位 | PASS | 改动仅在既有 `test/traework/`，不新增源码目录测试 |
| 命名 | PASS | `healthChars` 与既有 `contentChars`/`reasoningChars` 命名风格一致；改动局限在 gate 函数内 |
| 注释 | PASS | 中文元信息说明约束（reasoning 压 pending 的 504 根因、双轴判据、不构成泄漏的理由），不解释"改了什么" |
| 日志 | PASS | 无新增日志——gate 是转发控制，错误出口仍由调用方统一打点，无重复日志 |
| 错误处理 | PASS | 复用既有 emit 错误返回与 scanSSE 错误路径，无重复实现 |
| 复用 | PASS | 测试复用 `chunkDelta`/`traeStreamPumpContext`/`terminationDone` 等既有契约，未引入新抽象 |
| 临时产物 | PASS | cgo-shim 临时目录与探针中间文件已清理 |
