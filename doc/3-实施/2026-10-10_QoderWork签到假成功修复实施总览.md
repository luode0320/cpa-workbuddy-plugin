---
schema_version: 1
template_version: implementation-overview-v1
doc_id: IMPL-QODERWORK-CHECKIN-FALSE-SUCCESS-20261010
doc_type: implementation_overview
source_ids: [BUG-QODERWORK-CHECKIN-FALSE-SUCCESS-20261010]
status: accepted
version: v1.0
complexity: L1
current_slice: TASK-006/TASK-007 发布与生产验收完成
updated_at: 2026-10-11 00:15:00
reader_level: business_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
style_regression: required_after_tests
---

# QoderWork 签到假成功修复实施总览

结论：修复 qoderwork-provider 宿主 HTTP 桥状态码解码缺陷（与已发布 qoder-ai 0.1.3 同源），把 404/4xx/5xx 不再当作签到成功，并收紧签到响应归一化；影响 qoderwork 每日签到（面板签到 / 全部签到 / 定时自动签到）与所有依赖 `StatusCode` 判定的链路（额度、计划、探活）；范围：`qoderwork/host_bridge.go`、`qoderwork/billing.go`、新增 `qoderwork/host_bridge_test.go`、版本与文档；非范围：推理链路、模型映射、账号调度与生命周期、宿主 ABI；变化：抽纯函数 `parseHostHTTPDoResult` 兼容 PascalCase 与下划线键，缺 `success` 字段不再盲归一化为成功；完成标准：本地 build/vet/test 全绿 + 反证成立 + 发布链闭环 + 生产账号 u09a5b6ab 行为验收不再假成功。

## 1. 当前计划最终方案的简要说明

- 推荐方案一句话结论：把 qoder-ai 0.1.3 已验证的 `parseHostHTTPDoResult` 移植到 `qoderwork/host_bridge.go`，并收紧 `performCheckinCall`，随后走完整发布链并做生产行为验收。
- 主落点 / 主路径：`qoderwork/host_bridge.go`（状态码解码）、`qoderwork/billing.go`（归一化收紧）、`qoderwork/host_bridge_test.go`（回归）。
- 为什么走这条路线：与 qoder-ai 同源缺陷已有铁证与已发布先例，直接复用同一修复口径最小、风险最低；不改外部接口。

## 2. Agent 对当前问题的理解

- 问题 / 目标：生产账号 `u09a5b6ab` 具备签到资格，但签到后积分不增加，接口/面板可能仍显示「签到成功」。
- 本轮计划范围：qoderwork 宿主桥状态码解码、签到归一化收紧、回归测试、版本与文档、发布与生产验收。
- 明确不在范围：推理链路、模型映射、账号调度与生命周期、宿主 ABI。
- 当前优先闭环：发布链（commit → push → CI → assets → registry → 远端验证）+ 生产部署与行为验收。
- 关键假设 / 待确认点：修好解码后，若上游签到端点返回 4xx/5xx，签到将如实失败而非假成功；真实签到入口是否可用由生产实测判定。
- unresolved_decisions：无未决决策。

## 3. 实施周期与任务拆分

- 周期一：根因修复与本地验证（已完成）
  - `TASK-001` 取证与根因冻结：确认 `hostHTTPDo` 用 `json:"status_code"` 无法匹配宿主 PascalCase 键。
  - `TASK-002` 修复 `host_bridge.go`：抽出 `parseHostHTTPDoResult` 兼容两种键。
  - `TASK-003` 收紧 `billing.go`：`performCheckinCall` 缺 `success` 字段返回失败。
  - `TASK-004` 回归与反证：新增 `host_bridge_test.go`，cgo-shim build/vet/test 全绿。
  - `TASK-005` 版本与文档收口：bump 0.9.26、CHANGELOG、5-tests、6-review。
- 周期二：发布与生产验收
  - `TASK-006` 发布链：commit → push → CI → 下载 assets → registry → 远端验证。
  - `TASK-007` 生产部署与行为验收：plugin-store install 0.9.26，账号 `u09a5b6ab` 签到不再假成功。

## 3.1 最小任务清单

| 任务 ID | 唯一目标 | 状态 |
| --- | --- | --- |
| `TASK-001` | 取证与根因冻结：宿主桥状态码解码错误 | completed |
| `TASK-002` | 修复 host_bridge.go：抽出 parseHostHTTPDoResult | completed |
| `TASK-003` | 收紧 billing.go：performCheckinCall 缺 success 字段返回失败 | completed |
| `TASK-004` | 新增 host_bridge_test.go 回归并完成反证与本地全绿 | completed |
| `TASK-005` | 版本与文档收口：bump 0.9.26、CHANGELOG、5-tests、6-review | completed |
| `TASK-006` | 发布链：commit、push、CI、assets、registry、远端验证 | completed |
| `TASK-007` | 生产部署与账号 u09a5b6ab 行为验收 | completed |

## 4. 真实测试安排

- 测试入口：`python scripts/cgo-shim-build.py qoderwork`（build + vet + test）。
- 覆盖项：PascalCase 主用例、下划线兼容、非法载荷。
- 反证：临时改回 `json:"status_code"`，`TestParseHostHTTPDoResult_HostUntaggedPascalCase` 必失败。
- 通过标准：本地全绿 + 反证成立 + 生产返回真实上游结果。

## 5. 任务完成条件 / 停止条件 / 最大推进边界

- 完成条件：本地全绿 + 反证 + 发布链闭环 + 生产账号行为验收。
- 停止条件：若上游签到端点返回 4xx/5xx，则如实失败，不伪造成功，登记为产品事实。
- 最大推进边界：只改 qoderwork 签到与宿主桥解码，不扩散到推理/调度/宿主。
