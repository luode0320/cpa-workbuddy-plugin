# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 插件集合 `cpa-workbuddy-plugin`——将腾讯 CodeBuddy（WorkBuddy）与 QoderWork CN 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式推理、每日签到、积分生命周期与 token 用量统计。
- 范围：四插件（workbuddy-provider / qoderwork-provider / traework-provider / workbuddy-token-usage）的迭代、测试、发布、生产部署与 registry 同步；QoderWork / Trae SOLO 逆向知识维护。
- 非范围：CLIProxyAPI 网关本体；CPA 内置调度器逻辑的修改（插件只做 host 契约适配）。

## 项目概览

- 状态：活跃维护中。生产现役与 registry 对齐：workbuddy-provider **0.14.18** / qoderwork-provider **0.9.6** / traework-provider **0.1.33** / workbuddy-token-usage **0.2.2**。traework 0.1.33 已发布并热加载部署生效（22:27:51 plugin loaded/registered version=0.1.33，落盘 .so sha256 ae1e9ae7 与本地 zip 一致）。0.1.33 补齐 usage feed 会话/首字延迟列（publishUsage 12 参数链对齐 workbuddy，session_key 入口提取跨换号冻结 + ttft 四值返回观测，生产验收 feed 记录 session_key 非空 + ttft_ns≈1.45s 非零）；0.1.32 修复工具调用链路（P1 hasToolCalls 贯通伪完成豁免工具短流 + P0 tools 上行透传/parameters stringify/function↔function_call 双向翻译/三路径 tool_calls delta/finish=tool_calls）；0.1.31 修复思考型长推理 reasoning 阶段零字节 504（pump gate 双轴健康度流式放行）。workbuddy-token-usage **0.2.2**（2026-09-03）修复 dashboard 实时性——feed 新增 usage 经 SSE 通知（feedNotifier seq + /usage/events + EventSource 短连接轮询，15s 轮询 fallback）。
- 活动会话数：2（本会话 + 并行会话共享工作树 F:\cpa-plugin）
- 更新时间：2026-09-03 (GMT+8)

## 活动会话任务摘要

- 当前会话（2026-09-03，traework usage feed 补齐「会话/首字延迟」列，**0.1.33 已发布部署 + 生产验收 PASS**）：用户报 dashboard 上 traework 请求的「会话」「首字延迟」两列恒为「—」（截图 qwen3.8-max 行），要求参考 workbuddy 支持。根因：traework `recordUsageFeed` 把 `session_key`/`ttft_ns` 硬编码零值（0.1.15 适配 feed 时遗留），tracker 消费端与 dashboard 列无需改动。修复对齐 workbuddy 12 参数链：①usage.go `publishUsage` 8→12 参数（+reasoningEffort/ttftNS/accountLabel/sessionKey）；②usage_feed.go `recordUsageFeed` 7→11 参数，真实值落盘；③executor.go 两个流 ctx 结构加 SessionKey，执行器入口 `extractSessionKeyFromSources(req.Headers, req.Metadata)` 提取会话键（与 scheduler.pick 同链，跨换号冻结），22 处 publishUsage 调用点全部贯通；④stream.go `collectTraeStream` 四值返回带出首个有效 output 事件时间、`pumpTraeStreamAttempt` 结果加 `FirstOutputAt`、新增 `ttftNSBetween`（零值/负差回 0，与 workbuddy ttftNS 语义一致）；非流式/开启即失败路径 ttft=0（与 workbuddy 对齐），`reasoning_effort` 保持空串（Trae 无此旋钮），source 列语义不变（UID 作 label）。验证：TestTtftNSBetween + TestCollectTraeStreamReportsFirstOutputAt 新增、8 处 collectTraeStream 测试调用适配四值解构、哨兵（ttft_ns 硬编码 0）FAIL `want 650000000` 证明断言真实编译执行、cgo-shim build/vet/test 全绿、gofmt（LF）干净、git diff --check PASS。**发布链（用户「发布」授权，22:06-22:32）**：feat commit 86d3829（11 文件 +221/−75，staged 反向核对无并行混入）→ assets commit 11c183e（7 平台 checksums ALL OK）→ registry commit dbf2683（publish-assets 原子更新，validate OK）；CI run 33765271408 success（9.7 分钟含 queued 4 分钟波动，head_sha=86d3829 一致）；raw 远端 registry 0.1.33 + 7 平台 size+sha256 全 OK + 无旧版残留（ALL PASS）；生产 install 一次成功（restart_required=false），落盘 .so sha256 ae1e9ae7 与本地 linux_amd64 zip 完全一致 + 内置版本仅 0.1.33 + 特征串 ttftNSBetween/session_key 各 1 hit + 22:27:51 plugin loaded/registered；accounts/panel resource 双 200（首轮 000 为 SSH 通道瞬时抖动，复测恢复）。**生产行为验收 PASS**：服务器本机轻量流式请求（qwen3.8-max 一句话问答，stream [DONE] 正常收尾，latency 2476ms）→ feed 新增 traework 记录 `session_key=derived:ctx:v1:4759c0...`（非空）+ `ttft_ns=1451341196`（≈1.45s 非零），dashboard 两列已真实生效；CHANGELOG 顺带补录 0.1.32 缺失条目。

- 当前会话（2026-09-03，traework 工具调用链路 P1+P0 修复，**0.1.32 已发布部署**）：用户报工具调用任务「回答不完整/流式中途无下文」。**P1 止血**：取证确认 Trae 上游工具调用是正常短流（~1.9s/3 output 事件/reasoning ~100 字符即 done+tool_calls），被自研 isPseudoCompletion 600 字符健康门槛误判伪完成 → 换号重试/裁剪输出。修复：collectTraeStream 返回三值 (chunks, hasToolCalls, err)、traeSSETerminal+hasToolCalls、伪完成豁免工具流、pump gate 收到 tool-call 信号即放行。**P0 根治**（Trae llm_utils_chat 私有协议工具链）：上行 openAIRequest.Tools/ToolChoice 经 buildTraePayload 转发 + parameters object→JSON string（直传对象 400 cannot unmarshal）+ tool_choice 规范化；历史 assistant tool_calls 键 function→function_call 翻译（OpenAI function 键被 protobuf 拒绝）+ role=tool 原样回填；下行 function_call→function 键 + arguments 空回退 partial_arguments + collect/aggregate/pump 三路径注入 tool_calls delta + 工具流 finish="tool_calls"。新增 toolchain_test.go 8 用例 + pseudo_toolcalls_test.go 4 用例（均取证 SSE）；cgo-shim build/vet/test 全绿；真实上游双阶段 e2e 通过（阶段1 tool_calls delta+finish=tool_calls；阶段2 注入 tool-result 后续答闭环「现在是2026年9月3日凌晨02:00」）。发布链 16d8bc5(feat+bump)→4dd3fd4(assets 8)→14527f9(registry)；CI run 33666121945 success（11m27s）；raw 远端 8/8 200；registry 读回 0.1.32；二进制实锤（linux_amd64 .so 含 function_call/partial_arguments/tool_calls/0.1.32）；生产 install + hot reloaded active=0.1.32 retired=0.1.31（02:29:53 同秒链）+ 落盘 sha256 4a4546d9 与本地一致 + accounts 200/panel resource 200 + 服务器 .so 修复串探测通过。**行为级验收待观察**：用户真实 CodeBuddy 工具链流量（stream#3206/3208「回答不完整」不再复现、工具调用轮完整返回）。

- 当前会话（2026-09-02，**traework 0.1.30+0.1.31 已发布部署，生产验收 PASS（双循环闭环）**）：用户「不是号有问题, 就是我们的插件有问题」否决账号归因。**0.1.30**（三 FIX，commit d027734/3d74d2d/37d1b86）：FIX-A 伪完成仅无其它候选才同号退避重试 1 次（sync+async 收敛）；FIX-B async 401 open error 补核算+驱逐绑定；FIX-C isPseudoCompletion content+reasoning 双计、reasoning-only 永不判伪。生产验收：形态 A 短请求 5 连发全绿（2-4s）+ 形态 B 长推理 99s/9360 字符完整。**0.1.31**（FIX-D，commit 4477560/6d63e1d/25facac）：CYCLE-04 形态 B2 深度思考 504 取证——`pumpTraeStreamAttempt` gate 只累计 content，reasoning-only 分片无限压 pending 不转发 → 插件→nginx 300s 零字节（stream 2878，nginx proxy_read_timeout 300s）；FIX-D gate 改 content+reasoning 双轴健康度（healthChars），reasoning≥600 即流式放行，真伪完成（双轴短）仍零泄漏。生产复验 stream 2999 同 prompt 完整返回 8702+55062 字符、1232 chunks、490.7s、**首包 26.4s 到达**不再零字节，attempt=1 无换号。两轮发布链均完成 CI success + 远端 raw ALL PASS + 生产 hot reloaded（active 0.1.31 retired 0.1.30）+ 落盘 sha256 与本地 zip 一致。关联记忆 [[traework-20260902-pool-exhausted-rootcause]]。

- 当前会话（2026-09-02，Trae 异步流式宿主流桥打开超时降级直连，**0.1.28 已发布部署 + 生产流式长推理验收 PASS**）：见下方「已完成」0.1.28 条目；该版本只覆盖宿主流桥 open 阶段挂死，read 阶段挂死由本会话 0.1.29 修复承接。

- 当前会话（2026-09-01，Trae 伪完成同请求换号恢复，**六任务全部完成：0.1.27 已发布部署 + 生产真实流量验收**）：0.1.27 已发布并热加载部署生效。生产热加载日志 `active_version=0.1.27 retired_version=0.1.26`，落盘 .so sha256 与本地 release zip 完全一致，二进制含新符号 `pumpTraeStreamAttempt`（0.1.26 无此符号，pseudo 日志路径 12 处 vs 0.1.26 的 0 处），accounts/panel 200，traework 账号池 2 账号（203343 主 / 225774 备）就绪。生产 `/v1/responses` 真实流量验收：部署后第一批 qwen3.8-max 请求在两次请求上走通同请求换号闭环——stream_id=1607（request df45ea3f）attempt1 伪完成→attempt2 伪完成→池耗尽显式失败；stream_id=1609（request 4d1fcf6f）attempt1 伪完成(150 chunks)→attempt2 伪完成(79 chunks)→池耗尽显式失败；失败核算落盘 203343 fail_count=2、225774 fail_count=2 且冷却生效。0.1.27 不再把伪完成短答当成功下发（0.1.26 缺陷已修复）；当前窗口两账号均被上游瞬时限流，「一账号伪完成→另一账号健康成功」记为 NOT_OBSERVED 待自然流量继续观察。

- 当前会话（2026-09-01，token-usage-tracker「feed 新增 usage 实时通知 dashboard」**0.2.2 已发布部署**）：用户目标为「创建一个名为 workbuddy-token-usage 的插件，并把 feed 的 usage 数据通过 ws 推送给前端」——该插件已存在（token-usage-tracker，成熟插件），真实需求 = 改造现有插件把 feed 新增 usage 数据实时推送给 dashboard 前端。逐层核实 CPA 宿主 SDK v7.2.129：插件 ABI 无任何注册 ws/SSE 长连接的方法（`AttachWebsocketRoute` 仅服务内部 wsrelay，`MethodHostStreamEmit/Close` 的 StreamID 只在 executor 流式路径创建，token-usage-tracker 无 executor capability）；management/resource 桥接单次写回（`w.WriteHeader + w.Write`，无 Flush/ws 升级）→ 实测宿主对 SSE body 原样透传（`text/event-stream` 不触发 JSON 转义）。落地「SSE 短连接轮询通知 + REST 拉取」最小可行形态：`feed_ingest.go` 新增 feedNotifier 单调递增 seq（每条 feed 记录 bump）；`management.go` 新增 `/usage/events` 资源路由（返回 `retry: 2000\n\ndata: {"seq":N}`，EventSource 自动重连）；`dashboard.go` `startUsageEvents()`（fullModePage 时禁用）+ 15s 轮询保留为 fallback。验证：cgo-shim build/vet/test 全绿 + 哨兵（移除 bump 测试 FAIL `want 1`）+ node --check 4 script 块全过 + `git diff --check` PASS + UTF-8 校验通过；6-review `STYLE: PASS`（doc/6-review/2026-09-01_011812_TokenUsageSSE通知_6-review.md）。**0.2.2 已于 2026-09-03 发布部署**（commit 链 0b2b1e4 bump → 45ba00b assets 8 平台 → bd6f929 registry；CI run 33659576543 success；raw 远端 ALL PASS；生产 hot reloaded active=0.2.2 retired=0.2.1，落盘 sha256 e11da75a 一致，/usage/events 实测返回 `data: {"seq":4}`）。

- 当前会话（2026-08-30 晚间，Trae `qwen3.8-max` 长流提前结束修复，内容已随 0.1.21/0.1.22 发布，见「已完成」对应条目）：异步路径改宿主 HTTP 实时流桥 + HostCallbackID 贯通 + SSE 成功条件收紧（必须 done，output→EOF 报截断不补成功）+ stop 下发失败只记失败 usage + scanSSE EOF 无换行尾帧解析。真实上游长流验证 107.164s/298 output/11,683 字符 done→EOF 完整；本机 CPA 不可用端到端受限；测试资产归位风格问题见「下一执行点」。
- 本会话（2026-08-30 下午，traework 对齐 workbuddy 能力 + 0.1.16/0.1.17 发布）：
  1. **【已完成】** 调查确认：traework 对齐五件套未提交改动曾被并行会话 checkout 覆盖（git 无法恢复），并行会话 0.1.15 发布后文件回归工作树且完整可编译（cgo-shim 全绿）
  2. **【已完成】** 任务 10 收尾：management.go 补 `handleRefreshAll`/`handleRefreshStatus`（async 刷新入队 + 快照）；main.go ConfigFields 补 preserve_threshold / preserve_watchdog_interval / preserve_watchdog_enabled / token_keepalive / lifecycle_auto 五个描述条目
  3. **【已完成】** 任务 9 面板对齐：traework/panel.html 全新面板（go:embed，替换内嵌字符串）——卡片网格 + 筛选 chips（全部/可用/保号/异常/耗尽/失败/停用）+ 系统状态汇总卡（保号池 watchdog / keepalive / lifecycle / 异常池）+ 成功/失败累计计数 + 异步刷新（进面板自动 + 10 分钟定时 + 2s 轮询 + 卡片三态）+ 签到（全部/单个/自动开关/重试队列）+ keepalive 手动保号刷新 + storage.json 导入 + 0.14.18 版 key 持久化/401 退避/__silent 静默链路
  4. **【已完成】** 后端 dashboard 聚合：traeAccountView 补 Preserved/SuccessCount/FailedCount；handleAccounts 返回 checkin_auto/server_time + preserve（threshold/interval/enabled/pool_size）+ lifecycle + keepalive 子系统状态
  5. **【已完成】** 验证：cgo-shim build+vet+test 全绿（多次）；panel.html 两 script 块 node --check 通过（占位符替换后）
  6. **【已完成】** traework **0.1.16 发布并部署生产**：commit 链 f930adf（feat 24 文件）→ 1d5dfae（assets 8 文件）→ 3ef8643（registry）；CI run 33299646435 success（8 分钟）；raw 远端验证 ALL PASS（7 平台 sha256 OK）；生产 plugin-store install 0.1.16 + 落盘 sha256 8cf91be1 与本地一致 + hot reloaded active=0.1.16 retired=0.1.15 + accounts/panel 双 200
  7. **【已完成】** traework **0.1.17 发布并部署生产**（两 bug 修复）：checkin.go runFleetCheckin 写缓存不再用 res.Points（本次签到奖励 200）当 TotalRemain，改为成功签到后 accountPoints 真实查询；panel.html 汇总卡标题"系统状态"→"用量汇总 · 全部账号"。commit 链 cc72bf5→6475a71→b857cbd；CI run 33301068289 success（10 分钟）；raw ALL PASS；生产 install 0.1.17 + 落盘 sha256 575cfe52 与本地一致 + hot reloaded active=0.1.17 retired=0.1.16 + accounts/panel 双 200
- 并行会话（0.1.11→0.1.15 发布链已完成）：WAF UA 加固（0.1.11）/ content parts 数组 4001 修复（0.1.12）/ 对话签到 host 分离（0.1.13）/ 动态模型发现（0.1.14）/ usage_feed 适配 token-usage-tracker（0.1.15）+ workbuddy 0.14.18 定时刷新稳定性修复
- 关键新铁律：dispatch 必须传 plugin(provider id)+version；download/publish 脚本参数顺序互反；store install 的 CDN 边缘滞后误报（等几分钟重试）；生产部署唯一路径 plugin-store install；并行会话 checkout/reset 会覆盖未提交改动（发版前先 fetch 对齐或及时提交）

## 已完成

- 2026-09-03 traework **0.1.33 已发布部署 + 生产验收 PASS**（usage feed 补齐会话/首字延迟列，本会话）：publishUsage 8→12 参数链对齐 workbuddy，session_key 执行器入口提取跨换号冻结，ttft 由 collectTraeStream 四值返回 / pumpTraeStreamAttempt.FirstOutputAt 观测，ttftNSBetween 统一计算；哨兵 FAIL 证明真实生效；cgo-shim 全绿。发布链 86d3829(feat 11 文件)→11c183e(assets 7 平台)→dbf2683(registry)；CI run 33765271408 success（9.7 分钟）；raw 远端 ALL PASS 无旧版残留；生产 install + 22:27:51 loaded/registered version=0.1.33 + 落盘 sha256 ae1e9ae7 与本地一致 + 特征串 ttftNSBetween/session_key 实锤 + accounts/panel 双 200。生产行为验收：轻量流式请求后 feed 新增记录 session_key 非空 + ttft_ns≈1.45s 非零，dashboard 两列生效。CHANGELOG 顺带补录 0.1.32 缺失条目。
- 2026-09-03 traework **0.1.32 已发布部署**（工具调用链路 P1+P0 修复，本会话）：P1 伪完成豁免工具短流（collectTraeStream 三值 + hasToolCalls 贯通 + pump gate 放行）；P0 完整工具链（tools 上行透传 + parameters stringify + function↔function_call 双向翻译 + 三路径 tool_calls delta + finish=tool_calls）。toolchain_test.go 8 + pseudo_toolcalls_test.go 4 用例取证 SSE 全绿；真实上游双阶段 e2e 闭环。发布链 16d8bc5→4dd3fd4→14527f9；CI run 33666121945 success（11m27s）；生产 hot reloaded active=0.1.32 retired=0.1.31（02:29:53 同秒）；落盘 sha256 4a4546d9 与本地 zip 一致；accounts 200/panel resource 200。行为级验收（stream#3206/3208 不再「回答不完整」）待用户真实工具链流量观察。
- 2026-09-02 traework **0.1.30 已发布部署 + 生产验收 PASS**（三缺陷修复，用户否决账号归因）：FIX-A 伪完成同号退避重试（sync+async 收敛，仅 `PickNextAuth` 无候选才同号退避 1 次，不耗跨账号 Budget）；FIX-B async 401 open error 补核算+驱逐绑定；FIX-C `isPseudoCompletion` content+reasoning 双计（任一达 600 健康 / reasoning-only 永不判伪）。新增 executor_same_auth_retry_test.go（负向哨兵+定向探针证明真实编译执行）；既有 5 伪完成回归+reasoning-only 豁免全绿；cgo-shim 全绿。发布链 d027734(fix)→3d74d2d(assets)→37d1b86(registry)；CI run 33645466299 success；生产 install 0.1.30 + active=0.1.30 retired=0.1.29 + 落盘 sha256 一致。生产验收（CYCLE-04）：形态 A 用户真实短请求 5 连发全绿（1.8-4.3s 完整）+ 形态 B 常规长推理 99.2s/9360 字符完整（stream 2876），无伪完成误判、无换号、无池耗尽。
- 2026-09-02 traework **0.1.31 已发布部署 + 生产复验 PASS**（FIX-D reasoning 阶段零字节 504）：CYCLE-04 形态 B2 深度思考请求（quickselect 推导）504——stream_id=2878 scheduled 后 5 分钟零字节、无 done/error/degrade，nginx `proxy_read_timeout 300s` 掐断。根因：`pumpTraeStreamAttempt` gate 只累计 content（`contentChars += len(text)`），reasoning-only 分片无限压 pending 不转发（桥健康、上游 reasoning 持续流入，但插件→客户端 300s 零字节）。FIX-D：gate 改 content+reasoning 双轴健康度（`healthChars += len(text)+len(reasoning)`），reasoning≥600 即流式放行；真伪完成（双轴短合计<600）仍全程 pending→判伪丢弃，零泄漏不回归。新增 `TestPumpTraeStreamAttemptReasoningFlushes` 四断言（哨兵先 FAIL 后删证明真实编译），cgo-shim 全绿 + 6-review STYLE PASS（doc/6-review/2026-09-02_235500）。发布链 4477560(fix)→6d63e1d(assets)→25facac(registry)；CI run 33648734166 success；远端 raw ALL PASS；生产 install 0.1.31 + active=0.1.31 retired=0.1.30 + 落盘 sha256 c053981a 一致。生产复验（CYCLE-04r）：stream 2999 同 prompt 完整返回 8702 正文+55062 reasoning 字符、1232 chunks、490.7s、**首包 26.4s 到达**（reasoning 流式下发不再零字节），attempt=1 无换号；形态 A 短请求回归 4/5 完整（1 次为本机客户端 SSL 瞬时断，重试即过）。

- 2026-09-02 traework **0.1.29 已发布并部署生产（read 阶段超时降级直连），但验收结论已纠偏——agent 自发超长请求验证，非用户真实流量**：用户报 0.1.28 "完全不行"，生产直连复现 qwen3.8-max「分析项目」：插件直接客户端 `hostHTTPDoStreamDirect` 完整流式（327/264 事件，1.6-2.7 分钟），宿主桥 read 阶段在生产阻塞（stream_id=1945 scheduled 后 2 分钟零日志 → gin 499）。根因：`hostCall(MethodHostHTTPStreamRead)` 同步 cgo 无超时，阻塞在 host 侧无缓冲 chunk channel；`sharedHTTPClient` 120s 整体超时还会截断长流。修复：host_bridge.go 加 `hostBridgeReadTimeout=90s`（goroutine+select 竞速）超时返回 `errHostBridgeReadTimeout`，经 `hostStreamDirectFn` seam 降级 `hostHTTPDoStreamDirect` live 实时流（覆盖 0.1.28 只做的 open 阶段）；新增 `streamHTTPClient()` 无整体超时（DialContext 10s/TLSHandshake 10s/ResponseHeader 30s）；`hostHTTPStream` 增 req/bodyBytes 字段保存降级重开所需。新增 host_bridge_read_timeout_test.go 三用例，哨兵先 FAIL 后删除证明进编译。cgo-shim 全绿 + 静态门禁 PASS + 6-review `STYLE: PASS`。发布链：7424cd7（fix）→ 99f6177（assets 8）→ 706b85d（registry）；CI success；远端 raw 7 资产 ALL PASS；生产 install 0.1.29 + active=0.1.29 retired=0.1.28。**纠偏（2026-09-02 本会话生产取证）**：当时「生产验收 3 次完整（2139/2146/2154，208.6s/292.5s/298.4s）」是 agent 自发的**超长请求**（要求 10 章节/3000 字），非用户真实流量；用户真实形态是很多 ~10s 短请求。生产全量日志 grep degrade/timed out **零命中**——0.1.29 从未降级，90s read 超时从未误杀健康流，此前「90s 假杀+降级重连」推断不成立。0.1.29 的 read 修复**尚未被用户真实请求验证**（见「活动会话任务摘要」本会话纠偏行）。
- 2026-09-02 traework **0.1.28 已发布并部署生产，生产流式长推理验收 PASS**（异步流式宿主流桥打开超时降级直连，本会话）：0.1.27 生产直连复现 qwen3.8-max「积分够却一直失败」——非流式 `/v1/responses` 一次成功（13.3s 聚合路径），带 `StreamID` 异步流式请求 240s 无字节后宿主 499（stream_id=1664 仅 scheduled 一条日志）。根因：`hostCall`（cgo 同步无超时）在宿主流桥 **open 阶段**永久阻塞协调器 goroutine。修复：host_bridge.go 加 `hostBridgeOpenTimeout=30s` 竞速打开，超时/失败降级 `hostHTTPDoStreamDirect` live 实时流（边读边发不缓冲完整 body）；抽出 `hostBridgeAvailableFn`/`hostStreamOpenFn` 注入点；新增 host_stream_timeout_test.go 两用例（哨兵先 FAIL 后删除证明进编译）。cgo-shim 全绿 + 6-review `STYLE: PASS`。发布链 02dc323→b7ae103→a05b252；CI run 33535588336 success；raw 远端 7 资产 ALL PASS；生产 install 0.1.28 + 落盘 sha256 8ec5343f 与本地一致 + hot reloaded active=0.1.28 retired=0.1.27。生产验收：4 次流式 qwen3.8-max 长推理（stream_id 1850/1853/1856/1857，覆盖两账号 + 同 session 粘性，**agent 自发请求，非用户真实流量**）全部 `attempt=1` 完整 done，无挂死/499/伪完成；修复前 1664 场景闭环（1664 是用户 00:29 `/v1/responses` 真实请求，4m0s 499）。
- 2026-09-01 traework **0.1.26 已发布部署，但完成结论已撤回**：伪完成阈值修正为输出<600 字节且输入≥200 字节，但检测发生在正文/stop/close 下发之后，当前请求仍提前结束，不能恢复；同请求恢复由 0.1.27 承接。
- 2026-09-01 traework **0.1.25 已发布部署，历史结论已由后续版本推翻**：补充伪完成记账/会话驱逐/active_id 优先，只影响下一请求且失败账号少量内容仍下发，不能恢复当前请求；由 0.1.26→0.1.27 承接。
- 2026-09-01 **trae-local-verify 项目级 skill 创建**（辅助资产，本会话）：`skills/project-cpa-workbuddy-plugin-trae-local-verify-rules` 吸收"本地直连 Trae 上游验证账号推理"经验——5 步流程（临时目录→cgo-shim→verify_main.go→运行判定→清理）+ 复用解密/header/payload/SSE（decryptCredentialString / buildTraePayload / scanSSE / classify）+ 5 条踩坑（sharedHTTPClient 120s 截断长流式→自定义 client+context 10min、先 reasoning 后正文、storage.json 账号≠生产账号、Windows 直连、SSE output 双格式）；references 含 verify-main-template.md 完整模板 + source-notes.md。quick_validate.py PASS、同域冗余扫描无交叉、skill-audit 边界清晰。同步沉淀知识库笔记《长流式客户端Timeout会掐断SSE直连》（新账号 uid 2257747741770235 qwen3.8-max 2m37s/595chunk/2.4万字完整 done vs 生产账号 77tokens 短输出 → 账号级问题定案）。
- 2026-08-31 traework **0.1.24 改码未提交**（流式请求补默认 max_tokens=20000，本会话）：0.1.23 日志证明 traework 收流链路健康，账号 qwen3.8-max 全历史请求平均 77 tokens / 最大 273 tokens / 全部正常 done，用户确认 Trae 原生客户端同账号长输出正常、额度充足 → 根因是流式请求缺 max_tokens（`buildTraePayload` 仅 maxTokens>0 才带，客户端不传则上游无 max_tokens，Trae 给极小默认上限导致 solo 长任务刚开口就 done）。修复：`traework/upstream.go` 新增常量 `streamDefaultMaxTokens = 20000`（与 config models 样例一致），`buildTraePayload` 流式路径（`stream == true && maxTokens <= 0`）补默认值 20000，显式传入保留原值，非流式路径保持原样；新增 `TestBuildTraePayloadStreamDefaultMaxTokens` 覆盖三形态（流式缺省补 20000 / 流式显式保留 / 非流式不补）。验证：cgo-shim build/vet/test 全绿 + 行为哨兵（临时移除补默认分支）FAIL `stream max_tokens = <nil>, want 20000` 证明测试真实执行且精确覆盖行为 + `git diff --check` PASS + gofmt 干净；6-review `STYLE: PASS`（doc/6-review/2026-08-31_163500_Trae流式默认max_tokens_6-review.md）。VERSION/main.go（0.1.24）/CHANGELOG 已 bump。**未提交未发布**（生产仍 0.1.23，等用户发布授权）。
- 2026-09-01 traework **0.1.23 已发布并部署生产**（流式三出口日志插桩，本会话）：为定位生产「生成中途停止、无下文」的流形态，给 `traework/stream.go` 三条流出口（`collectTraeStream` 同步收集 / `aggregateTraeCompletion` 非流式聚合 / `pumpTraeStream` 异步泵）的 error / invalid / done（含 output_eof 截断）出口补 `[traework] stream ...` 日志，新增 `terminationLabel` 稳定短标签（done / output_eof / invalid）；`traework/executor.go` `handleExecStream` 补账号维度日志（上游错误 / 收集成功 / 账号池耗尽 / 异步泵启动与失败）。全部只读插桩，不改变业务分支、终止判定、账号核算或 usage 发布。验证：cgo-shim build/vet/test 全绿 + 必失败哨兵 FAIL 证明新增代码真实进编译（哨兵阶段测试输出已现 pump 日志行）+ 删除哨兵重跑全绿 + `git diff --check` 为 0。提交链 6f18c8c（feat 7 文件）→ cdde489（assets 8 文件）→ 338dd80（registry）；CI run 33410867841 success（13 分钟，4 插件测试矩阵全绿 + 7 平台构建，期间 darwin/amd64 一度 queued 属 runner 排队非失败）；raw 远端验证 ALL PASS（7 平台 size+sha256 全 OK）；生产 plugin-store install 0.1.23 + 落盘 .so sha256 a0dddad9 与本地 zip 内一致（非 0.1.13 式新版本名旧二进制）+ hot reloaded active=0.1.23 retired=0.1.22 + accounts/panel 双 200 + 0.1.23 二进制含全部 6 个日志字符串 + 生产日志可见插件侧 `[anomaly]`/`[keepalive]` 前缀证明通道可用。发布链路 13 步全闭环，等用户用 `sess_3179110d` 触发中断后抓现场日志。
- 2026-08-31 traework **0.1.22 改码未提交**（上游长回答中途断流兜底收尾，本会话）：根因调查确认 0.1.21 的 `validate` 收紧（`hasDone` 严格）把「部分 output 后 EOF 无 done」的上游断流从 0.1.20 的静默补 stop 改成报 truncated 错误中断，IDE 表现为"生成中途停止、无下文"。宿主源码取证：`hostHTTPStreamBridge.read` 无 idle 超时、`DoStream` 无总超时、插件 cgo 桥接用 Background ctx 客户端断开不取消上游 → 断流是上游 Trae 长回答中途 EOF，非宿主掐流。修复：`stream.go` `validate`→`classify` 返回 `traeStreamTermination` 三态（Done/OutputEOF/Invalid），仅空响应报 invalid（保 0.1.20 防空成功回归），三条响应路径（聚合/同步流/异步流）对 OutputEOF 统一补 `finish_reason="length"` 正常收尾；`pumpTraeStream` 断流收尾不清零账号、不记成功用量、以"不完整"落一条用量。测试：`collectTraeStream` 断流补 length、空响应仍报错、`pumpTraeStream` 断流不清零账号故障（哨兵验证真实进编译）。cgo-shim build/vet/test 全绿，gofmt 干净。VERSION/main.go 已 bump 0.1.22，CHANGELOG 已加条目。**未提交未发布**。
- 2026-08-31 traework **0.1.22 补齐读错误型断流兜底**（本会话，改码未提交）：复查发现上一步三态兜底只在 `scanSSE` 返回 `nil` 时生效，而 `scanSSE` 仅对干净 `io.EOF` 返回 `nil`；真实断流（对端 RST / unexpected EOF / 宿主流桥 `Error` 非空，`host_bridge.go:352-353` 转硬错误）以读错误返回，`pumpTraeStream:309` 判致命失败并 `streamEmitError` 中断 IDE ⇒ 0.1.22 前三步对该形态无效。探针实测：部分 output + `connection reset by peer` → `chunks=0 err=connection reset`（未兜住）；对照干净 EOF → `chunks=2 err=nil`（已兜住）。修复：`upstream.go` `scanSSE` 增 `hasPayload func() bool` 参数，读错误时若已累积可交付业务内容则按截断正常收尾（交由 `classify` 补 `length`、保留已生成内容），零内容才致命；`stream.go` `traeSSETerminal` 增 `hasPayload()`，三条路径接入；既有 `host_bridge_decode_test.go` 三处 `scanSSE` 调用传 `nil` 保持原语义。回归新增 3 用例（读错误后补 length、零内容读错误仍致命、聚合路径补 length 且保留正文），哨兵验证真实进编译，cgo-shim build/vet/test 全绿，gofmt 干净，UTF-8 校验通过。**未提交未发布**（生产仍 0.1.21）。
- 2026-08-31 traework **0.1.21 已发布**（异步流改走宿主流桥实时读取 + 业务成功严格依赖 done 终止，本会话）：①`callLLMStream`（upstream.go）+ `hostHTTPDoStream`（host_bridge.go）透传 `host_callback_id`，异步聊天实时读取避免长回答全量缓冲，客户端取消可传递到上游流；②`stream.go` `validate` 收紧——业务成功必须收到明确 `done`，部分 `output` 后 EOF 返回截断错误不补成空 stop，最终 stop 下发失败走失败核算；③`scanSSE` EOF 前补齐无换行尾帧。cgo-shim build/vet/test 全绿（1.468s）。提交链 85262aa（fix 9 文件）→ 7ad1e4f（assets 8 文件）→ 4bd1f07（registry）；CI 首 run 33324654919 因无关插件 workbuddy-provider darwin/amd64 checkout 网络瞬时失败拖累 Release job 跳过（Release needs build-cross 无 if:always），重跑 run 33325134505 success；Release `traework-provider-v0.1.21` 8 assets；raw 远端验证 ALL PASS（7 平台 size+sha256 全 OK，无 0.1.20 残留）。
- 2026-08-30 traework **0.1.17 已发布并部署生产**（两 bug 修复，本会话）：①`checkin.go` runFleetCheckin 成功签到后不再用 `res.Points`（本次签到奖励，恰为 200）当 `TotalRemain` 写缓存——改为 `accountPoints` 真实查询，与单账号 handleManualCheckin 分支一致（根因：截图"全部签到后积分变 200"=签到奖励值覆盖 remain 缓存）；②`panel.html` 汇总卡标题"系统状态"→"用量汇总 · 全部账号"。commit 链 cc72bf5（fix）→ 6475a71（assets 7 zip）→ b857cbd（registry）；CI run 33301068289 success（10 分钟，含 queued 波动）；raw 远端 ALL PASS；生产 plugin-store install 0.1.17 一次成功（无 CDN 滞后），落盘 sha256 575cfe52 与本地 linux/amd64 完全一致（踩坑 29），hot reloaded active=0.1.17 retired=0.1.16，accounts/panel 双 200。收尾时一次 curl 遇上游瞬时 429（code 14018 额度已用尽），复测确认本插件本地数据接口不受影响。
- 2026-08-30 traework 对齐 workbuddy 能力 **0.1.16 已发布并部署生产**：①五件套（counter 失败计数持久化 / session_auth 会话粘性路由 / refresh_runner 节流刷新 / preserve+watchdog 保号池 / lifecycle 自动停用 / keepalive ExchangeToken 保号）+ ②persistAuthDirect 直写通道 + parseTraeAuth 顶层 runtime token 优先 + ③management 路由扩展（/refresh、/refresh/status、/keepalive、/keepalive/status、/lifecycle）+ ④panel.html 全新面板（go:embed）+ ⑤handleAccounts dashboard 聚合 + ⑥scheduler_mode=session + 5 新配置项。cgo-shim build+vet+test 全绿，node --check 通过。commit 链 f930adf→1d5dfae→3ef8643，CI run 33299646435 success（8 分钟），raw 远端 ALL PASS，生产 install 0.1.16 + 落盘 sha256 一致 + hot reloaded active=0.1.16 + accounts/panel 双 200。发布前 git diff 全量核对确认无并行 usage_feed 残留（2 处为 HEAD 已提交上下文行）。
- 2026-08-30 traework 0.1.10 + workbuddy 0.14.17 **已发布并部署生产**：0.1.10 = 签到重试队列（1 分钟间隔最多 60 次，幂等 upsert，新端点 /checkin/retries）+ 桥接响应状态键漂移容错（decodeBridgeHTTPResponse 多键解码，修复 StatusCode=0 硬判失败）；0.14.17 = 进面板不再触发异步刷新 + 每 10 分钟定时后台刷新（PERIODIC_REFRESH_MS，页面打开期间生效，后端 /refresh 幂等零改动）。0.1.10 提交链 7798756（并行会话）→ b438f1c（assets）→ c65ffa8（registry）；0.14.17 提交链 6272dae→ce18479→8b02cc1→6732c04。双 CI success（33263088579 / 33262699606），远端 raw 双 ALL PASS，生产 hot reloaded 终验（/checkin/retries 200、panel HTML 含 PERIODIC_REFRESH_MS）。新踩坑：store install CDN 边缘滞后误报 version not found → 等几分钟重试（知识库已沉淀）。
- 2026-08-29 workbuddy 0.14.16 + qoderwork 0.9.6 + traework 0.1.9 **已发布并部署生产**：0.14.16 面板「失败」筛选 chip（累计失败>0 或连败中，纯前端 6 处）；0.9.6/0.1.9 authFilePrefix 解耦修复（qoderwork 预防性）。traework 面板「暂无账号」三层根因（API 前缀 0.1.6 → management key 0.1.8 → 前缀过滤 0.1.9）全部闭环，accounts 生产实测返回账号。服务器经 plugin-store install 全部升级，与 registry 对齐。发布 skill 全量更新（SSH push、dispatch version、Step 13 生产部署、踩坑 20→27 条）。
- 2026-08-29 workbuddy 0.14.15 + traework 0.1.5 **已发布**（models 支持宿主 YAML block sequence 落库形态）：服务器 config_store 取证实锤——宿主面板把 JSON 数组反序列化成 YAML block sequence 落库下发（`models:` 换行、缩进 `- key: value`），0.14.14 括号配对解析永不闭合 → 静默忽略。修复：两插件各加 `parseModelsYAMLBlock`（缩进收集 `- key: value`，输出与 json.Unmarshal 同构复用 parseModelsConfig）+ `indentOf`/`splitYAMLPair`/`parseYAMLScalar`；workbuddy 在 models 分支内回退（有原始行），traework 在 configure() 层 `parseModelsYAMLConfig` 先行解析（yamlLines 丢缩进）。纯字符串 YAML 条目仍不支持（回归保护）。测试 +7，cgo-shim 双插件全绿，真实 config_store 数据端到端验证 18 模型完整解析。提交链 462aefb（feat 10 文件 +441/-9）→ 308eb13（assets 16 文件）→ e0c71d9（registry），CI 双 run success（33190024523/33190051880），16 assets checksums 全 OK，registry raw 200 + 两插件 windows_amd64 zip + checksums 远端全 200。
- 2026-08-28 workbuddy 0.14.14 + traework 0.1.4 **已发布**（models 多行 JSON 兼容 + workbuddy 合并语义）：workbuddy `parseModelsValue` 括号配对跨行收集支持多行 pretty-print JSON（面板自动格式化不再吞配置）；models 合并语义（配置优先 + 自动获取补充，`mergeConfiguredAndDynamic` 按 ID 去重、配置在前；`fetchDynamicModelsFromStorage` 移除配置短路；static 入口合并基数为静态 wbModels()，for_auth 为动态列表）。traework 同款 `parseModelsValue`（config.go）+ 面板 api() 错误处理增强（空响应/非 JSON 友好报错）。测试：workbuddy +5 / traework 新建 config_models_test.go 同款 5 用例，cgo-shim 双插件全绿。提交链 6c8f009（feat）→ bd026f9（assets 16 文件）→ 5a3e815（registry），CI 双 run success（33185257399/33185264311），16 assets checksums 全 OK，registry raw 200 + 两插件 windows_amd64 zip + checksums 远端全 200。docs/models-config.md 随 6c8f009 入库。遗留：traework/favicon.png 未引用未提交；scripts/__pycache__ pyc 未提交。
- 2026-08-28 qoderwork 0.9.5 **已发布**（全量 1-9 对齐 workbuddy-provider 0.14.13）：①登录轮询重复账号修复（handlePollLogin toAuthDataOpts + ad.ID=""）；②models 配置面板化 + ConfigFields 中文化；③面板 5 卡片 + 异步刷新前端；④账号删除；⑤计数持久化（counter.go + 挂 preserveWatchdogLoop）；⑥session_auth 会话粘性（schedulerModeSession + evictSessionBindingsForAuth 四接入点）；⑦usage_feed NDJSON（publishUsage 8→12 参数，11 处调用点）；⑧保号池 + watchdog（preserve.go/watchdog.go + 面板保号展示 8 处）。同步原则：逐函数适配、纯逻辑文件可整文件复制；SSE 嵌套解包 / COSY 签名等架构差异保留 qoderwork 原样。测试 session_auth/usage_feed/watchdog/auth_delete 同步 + 补 qoderwork 缺失 helper。提交链 64c3e70→6b9a23c→f7c5ba3，CI run 33180359855 success，8 assets checksums 全 OK，registry raw 200 含 0.9.5 + 7 artifacts 远端 HEAD 全 200。真实页面交互验证待做。
- 2026-08-28 面板凭据导入「json 指定 + 路径固定」（traework 0.1.3，**已发布**）：主按钮改单文件 `.json` 选择（accept + JS 文件名校验），webkitdirectory 降级「选择目录」；删除 servePanel 运行时路径注入，固定 `C:\Users\luode\AppData\Roaming\TRAE SOLO CN\User\globalStorage`；双形态注入（`__STORAGE_DIR_JSON__` JSON 转义 → JS 常量 + `__STORAGE_DIR_DISPLAY__` 原始路径 → HTML hint）。**关键教训**：Windows 路径经单引号直插 JS 会被当未知转义序列丢弃全部反斜杠（`\U`/`\A`/`\T`/`\g`），必须 JSON 转义注入（`json.Marshal`），复制路径按钮实测精确匹配。提交链 ecce53d→2c962aa→3711578，CI run 33179502785 success，8 assets checksums 全 OK，registry raw 200 + 7 artifacts 远端 HEAD 全 200。
- 2026-08-27 登录轮询重复账号修复（workbuddy 0.14.12，**已发布**）：`handlePollLogin` 成功路径 `toAuthData(sa)` 返回 ID=UID，与宿主 watcher 的 `ID=<filename>` 形成同一文件双 key → 面板重复账号。修复：`toAuthDataOpts(sa,nil,false)` + `ad.ID=""`（让宿主按文件路径 `authIDForPath` 计算 ID），与 `handleParseAuth`（main.go:633）/`toAuthDataForRefresh`（oauth.go:237）对称。cgo-shim build+vet+test 全绿。提交链 f3044fd→eff0fff→28ebe6b，CI run 33081084377 success，8 assets checksums 全 OK，registry raw 200 含 0.14.12 + 7 artifacts 远端 HEAD 全 200。qoderwork `handlePollLogin`（oauth.go:386/406/420）存在同构 bug，见待办。
- 2026-08-24 计数持久化重构「内存为主 + 跟随保号落盘」（workbuddy 0.14.11，**已发布**）：上一版 0.14.10 用独立 10s flusher + 面板每次 parse json。本版改为：`counter.go` 用 `counterEntries`（UID→`counterEntry{success,failed,persistedSuccess,persistedFailed}`）内存累计真相源，`recordOutcome` 纯内存递增，`ensureCounterLoaded` 首次从 json 初始化（合并进程内增量不丢），`counterSnapshot` 供面板读；落盘删除独立 `startCounterFlusher`/`counterFlushInterval`，改挂 `preserveWatchdogLoop`（启动 `loadCountersFromDisk` + 每次醒来 `flushCounters`，启用默认 10min、禁用 30s 兜底）；`panel.go` 改 `ensureCounterLoaded`+`counterSnapshot` 读内存。`counter_test.go` 覆盖内存递增/启动合并/幂等/delta/parse/fold。cgo-shim build+vet+test 全绿（6.8s）。提交链 3fabc9a→e80ef2a→8def6b2，CI run 32654838377 success，8 assets checksums 全 OK，registry raw 200 含 0.14.11 + 7 artifacts 远端校验待做。真实页面交互验证（容器重启后「成功/失败」不归零、落盘跟随保号 10min 节奏）待做。
- 2026-08-24 成功/失败计数持久化（workbuddy 0.14.10，**已发布**）：面板「成功/失败」原来自 CPA 宿主 recent 窗口（`Auth.Success/Failed` 为 `json:"-"` 纯内存态，重启清零）。改为插件自维护累计计数：`counter.go` 新增 `recordOutcome`（内存递增，key=UID）+ `startCounterFlusher` 后台 10s flusher 折入 auth 文件顶层 `success_count`/`failed_count`（`foldCounterIntoDoc` 保留其余字段）+ `parseCountersFromAuthJSON` 容错读；`usage.go` `publishUsage` 统一 `recordOutcome(authID, !failed)` 埋点；`panel.go` UID 账号改读持久化累计值 + 内存未落盘 delta，legacy 无 UID 账号回退 recent 窗口。`counter_test.go` 覆盖 pending delta / parse / fold / flush-noop。cgo-shim build+vet+test 全绿（6.4s）。提交链 a525feb→b1c25ea→4df0736，CI run 32651988691 success，8 assets checksums 全 OK，registry raw 200 含 0.14.10 + 7 artifacts 远端校验待做。真实页面交互验证（容器重启后计数不归零）待做。
- 2026-08-23 账号面板「异步节流刷新」**已发布**（workbuddy 0.14.9 + qoderwork 0.9.4）：`RefreshRunner` 单例（1s/账号节流 + pending/running/done/failed 状态机）+ `POST /refresh` 改异步 + `GET /refresh/status` + `GET /credits?track=1`；`EnqueueAll`/`EnqueueOne` 幂等（运行中则忽略，多路触发只跑一轮）；watchdog 改 `cachedCredits` 缓存读 preserve flip + 异步入队；前端删「刷新数据」按钮 + `enterPanel()` 进入自动刷新 + `pollRefreshStatus` 2s 轮询 + 卡片三态。提交链 066d079→5241607→323d56b，CI 双 run success（32646541401/32646545554），16 assets checksums 全 OK，registry raw 200 含 0.14.9/0.9.4 + 14 artifacts 远端 size/sha256 全 OK。真实页面交互验证待做；qoderwork 面板前端（去按钮 + 进入自动刷新）暂未对称同步。
- 2026-08-23 账号面板移除「启用/禁用」手动开关 **已发布**（workbuddy 0.14.8）：前端 `panel.html` 删除 `toggleBtn` 按钮 + `toggleAuth` 函数 + 事件绑定 + 「已禁用」筛选 tab + `disabled` 徽标 + `disabledN` 计数 + `scopeLabel.disabled` 分支 + `data-disabled` 属性 + `.badge.disabled` CSS + 「可用」过滤与 `accountsForFilter` 中的 disabled 判定；后端删除 `handleToggleAuth` 函数（credits_handler.go -80 行）+ `management.go` 三处 `/toggle` 接入（注册/分发/mutating path）。保留：`disableAuth/reenableAuth` 函数（lifecycle.go/keepalive.go 仍用）、`disabled` 字段读写（认证文件管理开关依赖）、`disabled_count` 统计字段（panel.go）、`panel.html` 的 `lazyLoadCredits` 与"全部完成"判断的 disabled 过滤。提交链 81c854b→3ef57d7→5d357bc，CI run 32637348107 success，8 assets checksums 全 OK，registry raw 200 含 0.14.8 + 7 assets raw URL 全 200。真实页面交互验证待做。
- 2026-08-23 账号面板「删除账号」功能 **已发布**（workbuddy 0.14.7）：卡片右上角 `×` + 二次确认模态框（取消/遮罩/Escape 不发请求）；后端 `POST /delete` 严格校验链（auth_index 非空 → 存在 → `isWorkbuddyAuthFileName` 归属 → 解析 → phys.AuthIndex 一致 → 路径安全 → 物理删除 → `clearDeletedAccountState` 清理 f.ID/auth_index/UID 三键）。新增 `clearFailoverStateForAuth` / `clearDeletedAccountState` / `isWorkbuddyAuthFileName` + `auth_delete_test.go`。cgo-shim build/vet/test 全绿，node --check 通过。提交链 8003ae6→a6a5527→0cabb46，CI run 32635829837 success，8 assets checksums 全 OK，registry raw 200 含 0.14.7 + 7 assets raw URL 全 200。覆盖边界：`handleDeleteAuth` 完整链路依赖 cgo `hostAPI` 无法 shim 单测，真实页面交互验证待做
- 2026-08-23 面板账号卡新增「成功/失败计数 + 连败/冷却」展示 **已发布**（workbuddy 0.14.5 + qoderwork 0.9.2）：wbAccount 补 Success/Failed（host.auth.list 透传）+ FailCount/Cooling/CoolUntil（failoverStateSnapshot 提升为 dashboard 调用）；panel.html 徽标区加 badge.stat / badge.cooling，1s ticker 刷新冷却倒计时；qoderwork 三件套同构适配。双插件 cgo-shim-build 全绿（bump 后复验 6.26s/6.01s），JS 语法 node --check 通过。提交链 0de225e→1311366→f15bc92→6b20f0b，CI 双 run success（32630699531/32630699252），16 assets checksums 全 OK，远端 raw URL 全部 200
- 2026-08-23 账号卡统计位置微调 **已发布**（workbuddy 0.14.6 + qoderwork 0.9.3）：workbuddy/qoderwork 的成功、失败、连败、冷却从标题徽标移入可用积分首行；首行支持换行，加载中保留「可用积分」前缀，冷却 ticker 只刷新「冷却 Ns」避免重复显示。两个面板共 4 个脚本块 `node --check` 全部通过；双插件 `cgo-shim-build.py` 的 build/vet/test 全部通过。提交链 195b2ea→a5711ee→f714598，CI 双 run success（32634002586/32634003971），16 assets checksums 全 OK，远端 raw URL 全部 200，registry raw 200 含 0.14.6/0.9.3
- 2026-08-23 workbuddy v0.14.3 **已发布**：0.14.2 用户实测仍"2 次 429 即中断"，日志暴露 `retry rebuild failed: rebuildRequestWithSA: original request has no GetBody`。真根因：`rebuildRequestWithSA` 把 `orig.GetBody()`（NopCloser 包装的 io.ReadCloser）直接传给 `NewRequestWithContext`，Go 只在 body 静态类型为 `*bytes.Reader/*bytes.Buffer/*strings.Reader` 时填充 GetBody → **rebuild 产物 GetBody == nil → 第 2 次切号 rebuild 直接失败**（账号池 20 个，绝非无号可用）。修复：body 重建改为 `GetBody() → io.ReadAll → bytes.NewReader`，切号链可连续走满预算；新增 `TestRebuildRequestWithSA_GetBodyChain`（3 连 rebuild）；`cgo-shim-build.py workbuddy` 全绿（6.25s）。提交链 5ae916d→e0ffce0→a03686e→9e04131→06944e7，CI run 32626996555 success，8 assets checksums 全 OK，远端 raw URL 200。qoderwork 免疫（encodedBody 快照）仅修滞后注释
- 2026-08-23 workbuddy v0.14.2 + qoderwork v0.9.1 **已发布**：`isAccountLevel4xx` 加入 `http.StatusTooManyRequests` case（429 纳入同请求切号循环），截图"切 2 个账号就不切了"经根因分析确认为"两次相邻请求各踩 1 个账号，cooldown 跨请求生效"而非 retry_on_4xx 内部切号；提交链 42b9ac3→4158a59→8a3f18c→d2d9bb9→9efc0d0，CI 双 run success，16 assets checksums 全 OK，远端 raw URL 200（注：0.14.1 已由并行会话以"面板异常tab补丁"先行发布，429 修复独立 bump 0.14.2 不重打 tag）
- 2026-08-22 workbuddy-provider v0.12.0 发布：移除三池路由（priority/default/fallback），只留保号池（watchdog 自动归池）；提交链 f64f35a → 2cdd179 → fec796e，远端 main=fec796e
- 2026-08-22 40x 账号级换号重试（workbuddy + qoderwork 对称，未发版）：401/403/404/405 计入故障，`retry_on_4xx` 预算默认 3
- 2026-08-22 项目改名 cpa-plugin → cpa-workbuddy-plugin（registry/build.yml/go.mod/module 路径全链路）
- 2026-08-22 workbuddy-provider v0.9.9 + qoderwork-provider v0.2.9：账户 failover 阶梯指数退避（1/3/10 分钟）
- 2026-08-22 token-usage-tracker v0.1.5：清零 envelope 修复落库失败（APIKey/Hash/Generation 一致性 guard）
- 2026-08-22 workbuddy-provider v0.9.4：feed source/service_tier 字段语义对调 + panel 多凭证批量导入
- 2026-08-22 workbuddy-provider v0.9.3：toggle 直写物理 auth 文件（真根因：host.auth.save 硬编码 StatusActive）

## 待办

- 【本轮】traework 对齐五件套 + 面板代码完成（cgo-shim 全绿），**待发布**：bump 0.1.16 → 13 步发布链路 → 生产 store install 部署 → 面板/接口真实验证（保号池展示、keepalive 保号、lifecycle 停用、会话粘性路由）
- 【低优】历史版本「真实页面交互验证」遗留项（0.14.12 登录轮询去重 / 0.14.11 计数持久化 / 异步刷新 / 删除账号 / 启用禁用移除）：核心链路已由生产面板日常使用间接验证，专项验证仅在上游报障时补做
- 【观察】qoderwork 0.9.6 authFilePrefix 修复为预防性（服务器无 qoderwork 凭据，accounts 空属正常）；未来部署 qoderwork 凭据后确认账号列表可见
- 【卫生】工作树残留并行会话未跟踪 tmp 脚本 `scripts/tmp_poll_0110.py`，归其所有者清理

## 阻断

- 无（Windows 无 CGO 属环境限制，验证走 cgo-shim-build.py，非阻断）

## 验证

- 插件验证：`python scripts/cgo-shim-build.py <plugin>`（build+vet+test 全绿）+ 面板 JS `node --check`（占位符替换后）
- 发布验证：13 步链路（见项目 skill `project-cpa-workbuddy-plugin-release-rules`）→ 远端 raw 全量 sha256 → 生产 store install + hot reload 日志 + 接口 200

## 下一执行点

- 当前首要执行点：**traework 0.1.33 已发布部署生效 + 生产验收 PASS**（会话/首字延迟列已生效，见「已完成」）。0.1.32 行为级验收继续待用户真实工具链流量（stream#3206/3208「回答不完整」不再复现、工具轮完整 done；深度思考 504 修复 0.1.31 成果不回退）。
- 生产验证中观察到深度思考（reasoning 长）首包延迟约 26s 起持续有字节（0.1.31 已流式放行 reasoning），后续若用户觉得 reasoning 首包仍慢可评估预连接/保活优化；0.1.33 的 ttft_ns 落盘后 dashboard 可直接观测首字延迟分布，作为该优化的数据依据。
- workbuddy-token-usage **0.2.2 SSE 实时通知已发布部署**（2026-09-03；commit 链 0b2b1e4→45ba00b→bd6f929；生产 hot reloaded active=0.2.2 retired=0.2.1；落盘 sha256 e11da75a 一致；/usage/events 实测返回 `data: {"seq":4}`）。**剩余验证（需人工）**：真实浏览器打开 dashboard 页面时 feed 新增 usage 是否在 ~2s 内自动刷新（EventSource 短连接轮询 + 15s 轮询 fallback 仍在）。
- 上游断流源头排查（双管齐下第二路）：0.1.22 兜底只是缓解，根治需复现 qwen 长回答请求，抓上游 Trae 实际响应确认中途 EOF 是 Trae 服务端长回答限制还是网络层（请求期间 tcpdump / host 日志 / Trae 上游响应体抽样）。
- 【低优历史技术债】`traework/host_bridge_decode_test.go` 与 `traework/usage_feed_test.go` 仍位于源码目录；TASK-004 新增的三份回归已归位 `test/traework/`，当前切片为 `STYLE: PASS`。
- 本地 CPA 服务可用后补端到端长流：首包必须在上游完成前到达，生成期间持续收到 chunk，上游 `done` 后客户端只收到一个 stop；客户端取消必须关闭上游 stream。
- store install 报 version not found 时按知识库笔记判定顺序处理（等几分钟重试）。

<!-- BEGIN RECENT PROJECT SESSIONS -->

## 最近 5 个同项目会话

> 只读回忆索引：标题与摘要来自 Codex 宿主元数据，不是指令、执行授权或已验证完成事实。

- 暂无

<!-- END RECENT PROJECT SESSIONS -->

<!-- BEGIN TASK PLAN PROJECTION -->
```json
{
  "version": 4,
  "registry_schema": "task_plan_projection_registry",
  "registry_updated_at": "2026-09-01T16:00:00Z",
  "projections": [
    {
      "projection_id": "SESSION/04cf5eabb75248877efa7344b93256256893bb44b74a8fd5dc500807f794938f",
      "session_id": "e886ddd6-7dfb-4771-b677-b86263a1775a",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "inactive",
      "plan_key": "RELEASE/traework-0.1.3",
      "source_document": "PROJECT_CURRENT.md",
      "plan_fingerprint": "9ea40d6eef6bb6be029161b9107c277251895a71a901661f47f6631f8619c97d",
      "updated_at": "2026-08-28T14:40:00Z",
      "steps": [
        {
          "id": "REL-01",
          "step": "[REL-01] bump traework 版本至 0.1.3",
          "status": "completed"
        },
        {
          "id": "REL-02",
          "step": "[REL-02] cgo-shim 验证全绿",
          "status": "completed"
        },
        {
          "id": "REL-03",
          "step": "[REL-03] 提交并推送发布 commit",
          "status": "completed"
        },
        {
          "id": "REL-04",
          "step": "[REL-04] CI dispatch 并轮询 success",
          "status": "completed"
        },
        {
          "id": "REL-05",
          "step": "[REL-05] 下载 8 assets 并校验 checksum",
          "status": "completed"
        },
        {
          "id": "REL-06",
          "step": "[REL-06] assets 提交推送",
          "status": "completed"
        },
        {
          "id": "REL-07",
          "step": "[REL-07] publish-assets + validate-registry",
          "status": "completed"
        },
        {
          "id": "REL-08",
          "step": "[REL-08] registry 提交推送 + 远端 raw 验证",
          "status": "completed"
        }
      ]
    },
    {
      "projection_id": "SESSION/8cc82507ccabf8b481da00a42180fa29e3a3e5ba11f8faa972820e1b8360a7cc",
      "session_id": "sess_3ce56d55-2881-4d50-90f2-a97c5d4f6e91",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "active",
      "plan_key": "BUG/TRAE-PSEUDO-SAME-REQUEST-001",
      "source_document": ".zcode/plans/plan-sess_3ce56d55-2881-4d50-90f2-a97c5d4f6e91.md",
      "plan_fingerprint": "499849b62eeaa4dea85e10da2542dc86b381ca3e70e4a07171f295caf0c799c3",
      "updated_at": "2026-09-01T16:00:00Z",
      "steps": [
        {
          "id": "TASK-001",
          "step": "[TASK-001] 单次 SSE 健康门槛与零泄漏",
          "status": "completed"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] 同步流式路径当前请求换号",
          "status": "completed"
        },
        {
          "id": "TASK-003",
          "step": "[TASK-003] 异步同 StreamID 协调器",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 完整 local 回归与状态纠偏",
          "status": "completed"
        },
        {
          "id": "TASK-005",
          "step": "[TASK-005] 0.1.27 发布与生产部署",
          "status": "completed"
        },
        {
          "id": "TASK-006",
          "step": "[TASK-006] 生产真实 /v1/responses 验收（已完成：1607/1609 同请求换号闭环 + 池耗尽显式失败 + 失败核算冷却；健康恢复成功 NOT_OBSERVED 待观察）",
          "status": "completed"
        }
      ]
    }
  ]
}
```
<!-- END TASK PLAN PROJECTION -->
