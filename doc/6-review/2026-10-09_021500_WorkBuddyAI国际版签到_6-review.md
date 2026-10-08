---
schema_version: 1
template_version: 1
doc_id: "STYLE-WBAI-CHECKIN-20261008"
doc_type: "style_regression"
source_ids: ["REQ-WBAI-CHECKIN-20261008"]
status: "accepted"
version: "v1.2"
current_slice: "TASK-003"
updated_at: "2026-10-09 03:52:00"
reader_level: business_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
---

# 6-review 风格回归：WorkBuddy AI 国际版签到改造（数据面 + 控制面 + 面板）

结论：本轮把 WorkBuddy AI 国际版签到改造（数据面、控制面与面板三层）完成风格回归，格式、命名、注释、结构、端点契约、测试资产归位与前端写法全部通过；影响：后续维护者可以按国际版口径理解签到状态解析、领取调用、缓存合并、控制面调度与面板交互链路；范围：三个任务的 workbuddy-ai 后端文件、面板文件与回归脚本，外加 1 份实施文档；非范围：业务正确性、需求覆盖、统计口径与发布放行；变化：记录三轮检查流水线、真实测试前置证据与结论；完成标准：STYLE 为 PASS 且格式门禁全部通过；术语说明：风格回归是对代码写法和位置的检查，cgo-shim 是在无 C 工具链的 Windows 上以 CGO_ENABLED=0 复现插件 build/vet/test 的本地验证脚本；验证状态：三轮真实测试与格式门禁均已通过。

## 文档信息

| 字段 | 内容 |
| --- | --- |
| 关联任务 | `TASK-001` 后端签到数据链路；`TASK-002` 后端控制面与调度；`TASK-003` 面板签到改造与 vm 回归 |
| 关联真实测试 | `TEST-001`、`TEST-002`、`TEST-003` |
| 检查时点 | 真实测试通过后 |
| 检查流水线 | `code-style-consistency-rules/scripts/static_owner_router.py` 以 `luode-skills` 为根推导：TASK-001 的 7 个变更路径命中 STYLE-01、02、03、04、08、09 六步（不触发 05/06/07）；TASK-002 的 10 个路径（含数据面复检）扩展命中 STYLE-07；TASK-003 的 2 个变更路径（面板与回归脚本）推导命中 STYLE-01、02、03、04、08、09 六步（不触发 05/06/07） |

## 检查范围

- 检查格式、换行、UTF-8、尾随空白、命名、局部写法、目录位置、依赖方向、测试资产归位、注释、日志、可读性与公共工具复用。
- 检查文件：`workbuddy-ai/checkin.go`（新增）、`workbuddy-ai/checkin_test.go`（新增）、`workbuddy-ai/billing.go`、`workbuddy-ai/cache.go`、`workbuddy-ai/panel.go`、`workbuddy-ai/lifecycle.go`、`workbuddy-ai/refresh_runner.go`。
  - TASK-002 扩展检查文件：`workbuddy-ai/keepalive.go`、`workbuddy-ai/management.go`、`workbuddy-ai/credits_handler.go`。
  - TASK-003 扩展检查文件：`workbuddy-ai/panel.html`、`test/workbuddy-ai/panel_checkin_repro.mjs`。
- 范围外说明：不判断业务正确性、需求覆盖、测试覆盖率或发布放行。

## 真实测试前置证据

- `TEST-001`：`python scripts/cgo-shim-build.py workbuddy-ai` build + vet + test 全绿（`ok ... 0.685s`），证据 `EVIDENCE-001`。
- `TEST-001` 哨兵确证：临时加入必失败 `TestSentinelMustFailIfCompiled` 后 shim 输出 FAIL（证明新增测试真实进入编译），删除哨兵并恢复文件（md5 一致）后重跑全绿，证据 `EVIDENCE-002`。
- 静态门禁：`gofmt -l`（LF 镜像）清零、`git diff --check` exit=0、7 文件无 BOM、UTF-8 合法、无尾随空白，证据 `EVIDENCE-003`。
 - `TEST-002`：`python scripts/cgo-shim-build.py workbuddy-ai` build + vet + test 全绿（`ok ... 0.720s`），证据 `EVIDENCE-005`。
 - `TEST-002` 聚焦运行：`go test -run TestNextCheckinTime -count=1 -v` 在 shim 镜像内 PASS（新增调度合并测试真实执行），证据 `EVIDENCE-005`。
 - `TEST-002` 哨兵确证：临时必失败哨兵 → shim FAIL（`sentinel-shim-exit=1`）→ 恢复 md5 一致（`37e26f7f...`）→ 重跑全绿，证据 `EVIDENCE-006`。
 - TASK-002 静态门禁：`gofmt -l`（LF 镜像）5 文件清零、`git diff --check` exit=0、5 文件无 BOM、UTF-8 合法、无尾随空白，证据 `EVIDENCE-007`。
 - `TEST-003`：`node test/workbuddy-ai/panel_checkin_repro.mjs` 21 项断言全 PASS、退出码 0（vm 真实执行 panel.html 两个 script 块），证据 `EVIDENCE-008`。
 - `TEST-003` 反证：对 HEAD 基线版本（`git show HEAD:workbuddy-ai/panel.html`，md5 `e41ebe5a74355b089756ef3c4c6ac988`）运行同一脚本，13 项断言 FAIL、退出码 1，证明断言集对旧缺陷真实敏感，证据 `EVIDENCE-008`。
 - TASK-003 静态门禁：`git diff --check` exit=0、panel.html 与 mjs 均无 BOM、UTF-8 合法、无尾随空白、`node --check` 通过，证据 `EVIDENCE-009`。

## 6-review 结论

- STYLE: PASS
- 完成标准：格式、命名、注释、结构、测试资产归位与语言写法六类检查全部通过，无 FIX_REQUIRED 项。

## 检查清单

| 编号 | 检查项 | 结果 | 证据 |
| --- | --- | --- | --- |
| STYLE-01 | 静态格式与编码一致性（UTF-8/BOM/尾随空白/换行） | PASS | 7 个文件全部合法 UTF-8、无 BOM、无尾随空白；`gofmt -l`（LF 镜像）清零；`git diff --check` exit=0；`lifecycle.go` 物理 CRLF 为 HEAD 既有历史形态，numstat 与 `--ignore-cr-at-eol` 逐行一致，无行尾伪变更，证据 `EVIDENCE-003`；TASK-002 复检：`keepalive.go`/`management.go`/`credits_handler.go`/`checkin.go`/`checkin_test.go` 五文件同样零 BOM、零尾随空白，`gofmt -l` 清零、`git diff --check` exit=0，`credits_handler.go` 的 `file` 类别标记（C source）与 HEAD 基线一致非本轮引入，证据 `EVIDENCE-007`；TASK-003 复检：`panel.html` 与回归脚本均 UTF-8 无 BOM、零尾随空白，`git diff --check` exit=0；`panel.html` 保持历史 CRLF 形态（CRLF 归一化提示为历史形态假象），mjs 为 LF 且 `node --check` 通过，证据 `EVIDENCE-009` |
| STYLE-02 | 命名与符号引用一致 | PASS | 新增 `checkinSummary`、`fetchCheckinStatus`、`performCheckinCall`、`checkinErrorResult`、`jsonBool`/`jsonI64`/`jsonStr`、`runAutoCheckin`、`processAutoCheckinAccount`、`handleManualCheckin`、`checkinOneAccount`、`mergeCheckinCache`、`billingBase`/`setBillingBase`、`hostAuthListFn`/`hostAuthGetFn` 全部与 CN 参考逐函数同构命名；`accountDetailCall.ci` 跟随同结构 `cr` 缩写口径；三个 `cachedAccountDetails` 调用点（panel.go:108、lifecycle.go:447、refresh_runner.go:303）全部同步适配新签名，无残留旧调用；TASK-002 新增 `nextCheckinTime`、`handleCheckinConfig` 与 CN 参考同名；`schedulerLoop` 签名保持 `stop <-chan struct{}` 不变，`nextKeepaliveTime` 保留为 CN 同款未删函数（CN schedulerLoop 改用 nextCheckinTime 后同样保留），无同义不同名；TASK-003 面板新增「全部签到」按钮（`onclick=checkinAll`，定义 :1181）、`autoToggle` 开关元素与 `toggleAuto`（定义 :1221）、`markCheckinDone`（定义 :406，调用 :1033/:1213）全部定义与使用成对；`card()` 动作区统一 `data-action=checkin`；全文 `claim` 与 `trial` 家族 0 命中，无残留引用 |
| STYLE-03 | 注释分层与定义位置 | PASS | 注释语言按「当前文件稳定写法 > 默认中文」裁决：`billing.go` 新增函数跟随文件内英文注释写法（HEAD 基线仅 2 行中文且均为业务字符串，评审中已把误加的中文注释改回英文）；`cache.go`/`panel.go`/`refresh_runner.go` 沿用各自文件既有英文/零注释口径；`checkin.go`/`checkin_test.go` 为新建文件，采用模块内中文元信息先例（active_auth.go、test_failed_tag.go）；`checkin.go` 新增函数体内无编号步骤注释，与 CN 参考 `workbuddy/checkin.go`（全历史 0 处编号）及 workbuddy-ai 模块 80+ 文件主流写法（编号注释仅存在于 payload.go、models_test.go 两个历史特例文件）一致，按手术式改动不引入局部风格跳变，证据 `EVIDENCE-004`；TASK-002 按同口径裁决：`checkin.go::nextCheckinTime` 补中文 `[参数]`/`[返回]`/最近修改时间元信息（新建函数走新建文件先例），`checkin_test.go::TestNextCheckinTime` 走测试框架签名豁免（只写验证什么/防什么），`keepalive.go::schedulerLoop` 改动点跟随 CN `workbuddy/checkin.go:65-67` 与同文件 `shouldRunKeepaliveNow` 既有英文三行说明口径，`management.go`/`credits_handler.go` 新增路由与函数跟随「同族管理文件英文/零注释」口径（`handleCheckinConfig` 英文一句话函数说明），未把函数头格式平摊到字段或行内，证据 `EVIDENCE-007`；TASK-003 注释口径按「当前文件稳定写法优先」裁决：`workbuddy-ai/panel.html` 与 CN 参考 `workbuddy/panel.html` 全历史均无 `[参数]`/`[返回]` 元信息与编号步骤注释（grep 计数均为 0），改动跟随面板内既有无元信息口径，不引入跨面板风格跳变 |
| STYLE-04 | 函数签名与参数结构 | PASS | `cachedAccountDetails` 由 3 返回值扩为 4 返回值（插入 `ci *checkinSummary`），与 CN 参考同签名完全一致；`billingCall`、`fetchCheckinStatus`、`performCheckinCall`、`checkinOneAccount` 等保持单行签名，无多行参数列表、无可选字段平铺；测试辅助函数 `withFakeHostCall(t, files, docs)` 为 3 参数收敛形态；TASK-002 新增 `nextCheckinTime(now time.Time) time.Time` 与 `handleCheckinConfig(req pluginapi.ManagementRequest)` 均保持单参数单返回值形态；TASK-003 未改动函数签名，`checkinAll(btn)`、`toggleAuto(on)`、`checkin(idx,btn)` 均保持面板既有参数形态，无新增参数或可选字段平铺 |
| STYLE-07 | 接口契约与数据访问 | PASS | 新增 `POST /checkin`、`POST /checkin/config` 两条管理路由与 CN 参考逐字同构：注册表描述、switch 分支（`handleManualCheckin`/`handleCheckinConfig`）、`mutatingManagementPath` 清单三处同步齐全；`handleCheckinConfig` 请求体 `enabled` 用 `*bool` 区分「未传」与「显式 false」，响应 `{checkin_auto, persistent}` 与 panel.html:1279 既有消费端字段一致；无 SQL/日志/时区改动，端点语义与响应形状无契约漂移，证据 `EVIDENCE-007` |
| STYLE-08 | 语言与框架特异写法 | PASS | `sem := make(chan struct{}, 4)` 并发扇出与 `buildDashboardEx`、`keepalive.go:255` 既有模式一致；`sync.Map` 缓存读写沿用同文件既有 `accountCache` 口径；`atomic` 用于测试计数避免数据竞争；`billingBase` 采用包级 `var` + `setBillingBase` 测试接缝，与 CN 参考同构；无新增第三方依赖；TASK-003 前端写法沿用面板既有模式：事件绑定走 `onclick`/`onchange` 内联属性、请求走既有 `api()` helper、按钮态走 `markCheckinDone` 既有 `className` 口径，无新增框架或依赖 |
| STYLE-09 | 测试资产与编码前契约 | PASS | `checkin_test.go` 为插件包内白盒单测，与 `workbuddy-ai/` 既有 5 个测试文件同落点同命名（`*_test.go`）；全部走 httptest 本地桩 + `hostAuthListFn`/`hostAuthGetFn` 内存桩，不触碰真实上游；测试辅助 `withFakeHostCall`/`testAuthDoc`/`cleanupTestAccount` 与 `traework/keepalive_test.go` 的接缝恢复先例同构；11 个测试函数全部具备中文「验证什么」注释，3 个辅助函数具备完整元信息，本轮编码前按 `code-generation-style-rules` 跟随局部既有写法形成契约；TASK-002 新增 `TestNextCheckinTime` 仍落在同一白盒测试文件，含中文「验证什么/防什么（GAP-004）」注释，经过聚焦运行与哨兵确证真实进编译；TASK-003 新增 `test/workbuddy-ai/panel_checkin_repro.mjs` 落点与命名对齐 `test/workbuddy/panel_filter_counts_repro.mjs`、`test/cursor/panel_auto_entry_repro.mjs` 先例，含中文头注释（回归对象/期望行为/用法/退出码），21 项断言真实执行面板内联 JS（vm），反证旧版 13 项 FAIL、退出码 1，证据 `EVIDENCE-008` |

## 问题与修复

- 修复项 1（格式）：`checkin_test.go` 末尾多余空行导致 `gofmt` 差异，已删除并复验 `gofmt -l` 清零。
- 修复项 2（注释裁决）：`billing.go` 新增函数上误加的 2 行中文注释与文件内英文稳定写法冲突，评审中改回英文；`checkin.go` 补齐 `runAutoCheckin` 的 `[参数]`/`[返回]`/最近修改时间，4 处「最近修改时间」冒号格式统一；`checkin_test.go` 补齐 11 个测试函数「验证什么」注释与 3 个辅助函数元信息。
 - 修复项 3（格式，TASK-002）：首轮补丁在 `management.go` 与 `checkin_test.go` 引入 6 行多余前导空格（` ^I`），`gofmt`（LF 镜像）与 `cat -A` 复核后逐行修正，最终 5 文件清零。
 - 修复项 4（断言强度，TASK-002）：初版 `TestNextCheckinTime` 仅断言 `shouldRunKeepaliveNow`，无法证明「合并取最早」不丢保活时段（两时段表恰好同构）；改为临时替换 `keepaliveHours` 为 `{1, 13}` 后断言 00:30 的下一触发为 01:00，覆盖 GAP-004 声明，测试后恢复原值。
- 待复核裁决（记录理由）：`checkin.go` 方法体未补编号步骤注释。裁决依据：CN 参考 `workbuddy/checkin.go` 全历史 0 处编号注释；workbuddy-ai 模块 80+ 文件中编号注释仅存在于 `payload.go`、`models_test.go` 两个历史特例，本轮新增函数与 CN 参考逐函数同构，补编号会造成跨文件风格跳变；按手术式改动原则与 `code-generation-style-rules` 的「当前文件稳定写法优先」口径，不强制补编号。
- 待复核裁决（TASK-003，注释口径）：panel.html 的新增/修改函数与按钮未补 `[参数]`/`[返回]` 元信息与编号步骤注释。裁决依据：`workbuddy-ai/panel.html` 与 CN 参考 `workbuddy/panel.html` 全历史均无此类元信息（grep 计数均为 0），面板 JS 与 Go 模块分属不同风格域，按「当前文件稳定写法优先」与手术式改动原则跟随面板内既有口径，不引入风格跳变。
- 历史遗留说明：`policy.go` 的 `iota` 与 `cache.go` 函数内 `var` 块均为 HEAD 既有写法（blame 479e0be，2026-10-06），非本轮引入，按手术式改动不顺手修复。
- 图片资产决策：N/A + 原因：本风格回归记录不需要图片资产 + 证据：格式与归位结论由命令证据覆盖。

## 执行附录

- 编码与换行核对：逐文件检查 BOM、CRLF/LF 计数与 UTF-8 合法性；`lifecycle.go` 以 `--ignore-cr-at-eol` 复核，无行尾伪变更。
- 格式核对：`git diff --check` exit=0；`gofmt`（LF 归一化镜像）TASK-001 7 文件 + TASK-002 5 文件清零。
- 哨兵核对：临时必失败测试 → shim FAIL → 删除恢复 → md5 一致（TASK-002 轮 md5 `37e26f7f...`）→ 重跑全绿。
- TASK-002 聚焦核对：`go test -run TestNextCheckinTime -count=1 -v` shim 镜像内 PASS。
- TASK-003 面板核对：vm 回归 21 项断言 PASS（退出码 0）、反证 13 项 FAIL（退出码 1）；`git diff --check` exit=0；panel.html 无 BOM、mjs 无尾随空白、`node --check` 通过。
- 命令与证据见上表 `EVIDENCE-*`。

## 追踪附录

 - 稳定 ID：`TASK-001`、`TASK-002`、`TASK-003`、`TEST-001`、`TEST-002`、`TEST-003`、`EVIDENCE-001` 至 `EVIDENCE-009`。
- 来源：`REQ-WBAI-CHECKIN-20261008`；实施总览 `doc/3-实施/2026-10-08_WorkBuddyAI国际版签到改造实施总览.md`。
