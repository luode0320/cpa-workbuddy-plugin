# 项目历史事件

> 本文件追加关键历史事件并只保留最近 20 条（按日期倒序、新事件置顶、追加后自动裁剪）；普通启动默认不读取，只有历史追问、当前状态不足或真实卡点时才窄检索。

## 事件

- 2026-09-02：traework-provider **0.1.30 三缺陷修复代码级完成 local 全绿（未发布）**：用户「不是号有问题, 就是我们的插件有问题」否决账号归因（纠偏见 [[traework-prod-account-225774-dead-203343-refresh-mismatch]]）。生产取证（21:26-21:30 stream 2331-2350，0.1.29）失败链=死号 225774 async 401 open error 不核算不驱逐绑定（session 亲和每请求重绑）→ 健康号 203343 被窗口性节流判伪后无同号重试 → pool exhausted；2351 反证 203343 同号 30s 后恢复 18696 tokens。修复：**FIX-A** 伪完成仅当 `PickNextAuth` 无其它候选才对当前账号同号退避重试 1 次（sync+async 收敛一致，`pseudoRetryBudget=1`，不耗跨账号 Budget，有候选仍 A→B 保留既有回归）；**FIX-B** async 401 open error 补 `reconcileAfterExecutorError`+`evictSessionBindingsForAuth`（对照 sync 路径既有核算）；**FIX-C** `isPseudoCompletion` content+reasoning 双计健康度（任一达 600 健康 / content0+reasoning>0 reasoning-only 永不判伪 / 双短才需长输入门槛）。验证：新增 test/traework/executor_same_auth_retry_test.go（async+sync 单号池伪→同号重试→成功 + resetAccountFailover 清零），负向哨兵+定向 Fatal 探针双重证明真实编译执行；既有 5 伪完成回归 + `TestIsPseudoCompletion` reasoning-only 豁免全绿；cgo-shim build/vet/test 全绿、gofmt 干净、改动限 stream.go+executor.go+新测试文件（pump gate reasoning 流式放行属独立优化已回退避免越界）。**未提交未发布**（生产仍 0.1.29；下一步 CYCLE-03 发布 0.1.30 → CYCLE-04 生产真实短请求验收：死号 401 不再拖垮池、伪完成同号退避可恢复、1664/1942/1945 类挂死不复现）。
- 2026-09-02：traework-provider **0.1.29 纠偏——「208-298s 太长」是超长单请求非真实用法，用户真实形态=很多 ~10s 请求；0.1.29 未被用户真实流量验证**（生产日志取证）：用户质疑 208/292/298s 单次推理不可能。生产实测 0.1.29 上 10+ 个正常规模 qwen3.8-max 请求（普通问答 18s；3 连发 10/13/15s；同 session 连续 6 个 3-5s）全部 `attempt=1` 完整 done，无 degrade/pool exhausted/error，其中 2 次上游瞬时 `access denied`（stream_id 2230/2241，HTTP 200 业务错误）→ 同请求自动换号成功（2231/2243），账号级故障换号健康。全量日志 grep degrade/timed out/direct fallback **零命中**——0.1.29 生产从未降级，90s read 超时从未误杀健康流（2139/2146/2154/2201 全部 attempt=1 走桥 done，718/957/867/380 chunks，桥 read 每次 <90s 返回，数据持续到达）。**此前「90s 误杀健康慢首包流+降级」推断不成立**。用户真实痛点在 0.1.27/0.1.28 时代（9/1 23:54-9/2 02:02，公网 183.239.175.194 + token-usage 面板密集测试）：①伪完成池耗尽 1607/1609/1869（双账号 pseudo→`pool exhausted`→HTTP 200 错误分片）；②桥挂死 499——1664（00:29 `/v1/responses` 4m0s）、1942/1945（01:58/02:00 `/v1/chat/completions` 1m40s/1m59s）scheduled 后无后续。0.1.29 部署（02:54）后到 20:42 **用户零真实流量**（02:57-03:09 的 2139/2146/2154 与 0.1.28 的 1850-1857 都是 agent 自发的超长/长请求，非用户）。结论：0.1.29 read 修复尚未经用户真实请求验证；0.1.30 应让用户真实短请求形态验证 read 挂死（1664/1942/1945 类）是否复现，而非继续用超长单请求测耗时。
- 2026-09-02：traework-provider **0.1.29 发布部署 + 生产流式长推理验收 PASS**（异步流式宿主流桥 read 阶段超时降级直连）：用户报 0.1.28 "完全不行"，生产直连复现 qwen3.8-max「分析项目」——插件直接客户端 `hostHTTPDoStreamDirect` 完整流式（327/264 事件），宿主桥 read 阶段在生产无限阻塞（stream_id=1945 scheduled 后 2 分钟零日志 → gin 499）。根因：`hostCall(MethodHostHTTPStreamRead)` 同步 cgo 无超时，阻塞在 host 侧无缓冲 chunk channel；`sharedHTTPClient` 120s 整体超时还会截断长流。修复：host_bridge.go 加 `hostBridgeReadTimeout=90s`（goroutine+select 竞速）超时经 `hostStreamDirectFn` seam 降级插件直连 live 实时流（覆盖 0.1.28 只做的 open 阶段）；新增 `streamHTTPClient()` 无整体超时（长流不被 120s 截断）；`hostHTTPStream` 增 req/bodyBytes 保存降级重开所需。新增 host_bridge_read_timeout_test.go 三用例（桥 read 挂起→降级直连读完整内存 SSE / 健康读不过滤 / 无 req 降级报错），哨兵先 FAIL 后删除证明进编译。cgo-shim 全绿 + 6-review `STYLE: PASS`。发布链 7424cd7(fix)→99f6177(assets 8)→706b85d(registry)；CI success；raw 远端 7 资产 ALL PASS；生产 plugin-store install 0.1.29 + 落盘 sha256 与本地 zip .so 一致 + hot reloaded active=0.1.29 retired=0.1.28。生产验证：3 次流式 qwen3.8-max **agent 自发的超长请求**全部完整——stream_id 2139（账号 e1987432，208.6s，718 chunks）、2146（账号 19ca85be，292.5s，957 chunks）、2154（账号 e1987432，298.4s，867 chunks）均 `attempt=1` 完整 done，正文含 END_NONCE 结尾，无 error/length、无 pseudo retry / pool exhausted / degrade（健康路径直接走桥，降级未触发）。**注意：非用户真实流量，用户真实形态是短请求 ~10s，0.1.29 尚未被用户验证（见顶部纠偏事件）。**
- 2026-09-02：traework-provider **0.1.28 发布部署 + 生产流式长推理验收 PASS**（异步流式宿主流桥打开超时降级直连）：0.1.27 生产直连复现 qwen3.8-max 长推理「积分够却一直失败」——非流式 `/v1/responses` 一次成功（13.3s），带 `StreamID` 异步流式请求 240s 无字节后宿主 499（stream_id=1664 仅 `exec stream async scheduled` 一条日志）。根因：`hostCall`（cgo 同步无超时）在宿主流桥打开阶段永久阻塞协调器 goroutine。修复：`hostBridgeOpenTimeout=30s` 竞速打开，超时/失败降级插件直连 live 实时流（边读边发不缓冲完整 body）；抽出 `hostBridgeAvailableFn`/`hostStreamOpenFn` 注入点；新增 host_stream_timeout_test.go 两用例（哨兵先 FAIL 后删除证明进编译）。cgo-shim 全绿 + 6-review `STYLE: PASS`。发布链 02dc323(fix 6 文件)→b7ae103(assets 8)→a05b252(registry)；CI run 33535588336 success（head=02dc323）；raw 远端 7 资产 ALL PASS；生产 plugin-store install 0.1.28 + 落盘 sha256 8ec5343f 与本地 zip .so 完全一致 + hot reloaded active=0.1.28 retired=0.1.27。生产验收：4 次流式 qwen3.8-max 长推理（stream_id 1850/1853/1856/1857，覆盖两账号 + 同 session 粘性，**agent 自发请求**）全部 `attempt=1` 完整 done，正文含 END_NONCE 结尾，无挂死/499/伪完成/换号；修复前 stream_id=1664 240s 宿主 499 场景闭环（注：1664 是用户 00:29 `/v1/responses` 真实请求；1850-1857 为 01:20-01:32 agent 自发，非用户）。注：本机网络对 GitHub 上行大流量稳定阻断（git push / 5MB 对象均被断），发布经生产服务器 SOCKS 隧道（ssh -D 127.0.0.1:1080）绕过，askpass 脚本用完即删。
- 2026-09-01：traework-provider **0.1.27 发布部署 + 生产真实流量验收完成**（伪完成同请求换号恢复）：长输入 600 字节门槛前缓存，A 短正文 `done` 时零泄漏丢弃并在同一宿主 `StreamID` 内选择 B、复用原 `HostCallbackID`，最终只发送 B 的 finish 和一次 close，全 pseudo 显式池耗尽失败。发布链 dd241fc(fix, 9 文件)→b7516cc(assets 8)→7b6df7a(registry)；CI run 33525997576 success；远端 raw ALL PASS；生产 plugin-store install 0.1.27 + 落盘 sha256 2864da0e 与本地 zip .so 一致 + hot reloaded active=0.1.27 retired=0.1.26 + 二进制含新符号 `pumpTraeStreamAttempt`（0.1.26 无）。生产真实流量证据（23:54-23:57）：stream_id=1607（df45ea3f）与 1609（4d1fcf6f）均走 attempt1→attempt2 双伪完成→池耗尽显式失败，失败核算落盘 203343 fail_count=2、225774 fail_count=2 且冷却生效；0.1.27 不再把伪完成短答当成功下发。「一账号伪完成→另一账号健康成功」NOT_OBSERVED，待自然流量继续观察。
- 2026-09-01：workbuddy-token-usage「feed 新增 usage SSE 实时通知 dashboard」**已改码未提交**：用户目标「创建一个名为 workbuddy-token-usage 的插件并把 feed usage 通过 ws 推给前端」——该插件已存在（token-usage-tracker），真实需求=改造现有插件做 feed→dashboard 实时通知。宿主 SDK v7.2.129 逐层核实：插件 ABI 无任何注册 ws/SSE 长连接的方法（`AttachWebsocketRoute` 仅服务内部 wsrelay；`MethodHostStreamEmit/Close` 的 StreamID 只在 executor 流式路径创建，token-usage-tracker 无 executor capability）；management/resource 桥接单次写回（`w.WriteHeader + w.Write` 无 Flush/ws 升级）→ 实测宿主对 SSE body 原样透传。落地「SSE 短连接轮询通知 + REST 拉取」：`feed_ingest.go` feedNotifier 单调递增 seq（每条 feed 记录 bump）；`management.go` `/usage/events` 路由返回 `retry: 2000\n\ndata: {"seq":N}`（EventSource 自动重连）；`dashboard.go` `startUsageEvents()`（fullModePage 禁用）+ 15s 轮询 fallback。验证：cgo-shim 全绿 + 哨兵（移除 bump FAIL `want 1`）+ node --check 4 script 块 + `git diff --check` PASS + UTF-8 校验 + 6-review `STYLE: PASS`（doc/6-review/2026-09-01_011812_TokenUsageSSE通知_6-review.md）。**未提交未发布**（等用户提交授权）。
- 2026-09-01：**trae-local-verify 项目级 skill 创建**（`skills/project-cpa-workbuddy-plugin-trae-local-verify-rules`）：吸收"本地直连 Trae 上游验证账号推理"经验。覆盖 5 步流程（临时目录→cgo-shim→verify_main.go→运行判定→清理）、解密/header/payload/SSE 复用（decryptCredentialString / buildTraePayload / scanSSE / classify）、5 条踩坑（sharedHTTPClient 120s 截断长流式→必须自定义 client+context 10min、先 reasoning 后正文、storage.json 账号≠生产账号、Windows 直连、SSE output 双格式）。references 含 verify-main-template.md 完整模板 + source-notes.md。quick_validate.py PASS。另沉淀知识库笔记《长流式客户端Timeout会掐断SSE直连》（新账号 uid 2257747741770235 qwen3.8-max 2m37s/595chunk/2.4万字完整 done，生产账号 77tokens 短输出为账号级问题）。
- 2026-08-31：traework-provider **0.1.22 改码未提交**（上游长回答中途断流兜底收尾）：根因=0.1.21 `validate` 收紧把「部分 output 后 EOF 无 done」的上游断流从静默补 stop 改成报 truncated 错误中断 → IDE"生成中途停止"。宿主取证：`http_stream_bridge.read` 无 idle 超时、`DoStream` 无总超时、插件 cgo 桥接 Background ctx 客户端断开不取消上游 → 断流是上游 Trae 长回答中途 EOF 非宿主掐流。修复：`stream.go` `validate`→`classify` 返回 `traeStreamTermination` 三态（Done/OutputEOF/Invalid），仅空响应报 invalid，三条路径对 OutputEOF 统一补 `finish_reason="length"` 收尾；`pumpTraeStream` 断流不清零账号、不记成功、以"不完整"落用量。测试 +3（断流补 length/空响应仍报错/断流不清零账号，哨兵验证进编译）。cgo-shim 全绿，gofmt 干净。VERSION/main.go bump 0.1.22。**未提交未发布**。
- 2026-08-31：traework-provider **0.1.21 发布**（异步流改走宿主流桥实时读取 + 业务成功严格依赖 done 终止）：① `callLLMStream`/`hostHTTPDoStream` 透传 `host_callback_id`，实时读取避免长回答全量缓冲；② `validate` 收紧——部分 `output` 后 EOF 不补成空 stop；③ 最终 stop 下发失败走失败核算；④ `scanSSE` EOF 补齐无换行尾帧。cgo-shim build/vet/test 全绿。提交链 85262aa(fix)→7ad1e4f(assets 8 文件)→4bd1f07(registry)；CI 首 run 33324654919 因无关插件 workbuddy-provider darwin/amd64 checkout 网络瞬时失败拖累 Release job 跳过（Release needs build-cross 无 if:always），重跑 run 33325134505 success；Release `traework-provider-v0.1.21`（8 assets）；raw 远端 ALL PASS（7 平台 size+sha256 全 OK，无旧版本残留）。
- 2026-08-26：token-usage-tracker「进 dashboard 页面概率性中断请求」修复 **已发布**（workbuddy-token-usage 0.2.1）：① `SyncOnRecord` 改 `false` 恢复 store 批量提交（dirty 聚合 + FlushMaxRecords=100 + 5s ticker），写放大降 ~100x；② 新增 `triggerFeedSync()`（容量 1 信号量合并并发触发 + 后台 loop）；③ `serveStatsResource` 读路径改异步触发（写路径保留同步）。cgo-shim build/vet/test 全绿（30s）。提交链 325d6e5(feat)→c582446(chore release assets)→(chore registry)，CI run 32970605823 success，8 assets checksums 全 OK，registry raw 200 含 0.2.1 + 7 artifacts raw URL 全 200。真实页面交互验证（进页面不中断、积压 feed 快速导入）待做。
- 2026-08-26：token-usage-tracker「进 dashboard 页面概率性中断请求」根因定位+修复 **已改码未提交**：根因=读路径每请求同步 `syncUsageFeed()`（feedSyncMu 串行）+ `SyncOnRecord: true` 逐条 bbolt 事务+fsync（写放大）→ feed 积压时 6+ 并发请求排队超 10s 前端超时。修复：① `SyncOnRecord` 改 `false` 恢复 store 批量提交（dirty 聚合 + FlushMaxRecords=100 + 5s ticker）；② 新增 `triggerFeedSync()`（容量 1 信号量合并并发触发 + 后台 loop，feed_ingest.go）；③ `serveStatsResource` 读路径改异步触发（management.go，写路径保留同步）。`cgo-shim-build.py token-usage-tracker` build/vet/test 全绿（30s）。数据丢失窗口（硬崩溃 ≤100 条/1 flush interval）对用量统计可接受。未走发布链路。
- 2026-08-23：账号面板「删除账号」功能 **已发布**（workbuddy 0.14.7）：卡片右上角 `×` 删除图标 + 二次确认模态框（取消不请求 / 确认 POST 后刷新 / 失败 Toast 保留卡片）；后端新增严格删除接口 `POST /delete`（仅收 `auth_index`，重新校验存在性 → `isWorkbuddyAuthFileName` 文件名归属 → `hostAuthGetBundle` 解析 → `phys.AuthIndex` 一致 → 路径非空 → `isSafeWorkbuddyAuthPath` → `deleteAuthFileInDir` 物理删除 → `clearDeletedAccountState` 全维度清理 f.ID/auth_index/UID 三个键）。新增 `clearFailoverStateForAuth` / `clearDeletedAccountState` / `isWorkbuddyAuthFileName` 三个纯函数 + `auth_delete_test.go` 单测；`cgo-shim-build.py workbuddy` build/vet/test 全绿，panel.html 两脚本块 `node --check` 通过。提交链 8003ae6→a6a5527→0cabb46，CI run 32635829837 success，8 assets checksums 全 OK，registry raw 200 含 0.14.7 + 7 assets raw URL 全 200。覆盖边界：`handleDeleteAuth` 完整链路因 `hostCall` 依赖 cgo `hostAPI` 无法在 shim 环境单测，真实页面交互验证待做。
- 2026-08-23：账号面板移除「启用/禁用」手动开关 **已发布**（workbuddy 0.14.8）：用户提"有了保号功能后该开关冗余可删"，本轮把 disabled 在面板上的所有显式化一并清除——前端 panel.html 删 `toggleBtn` 按钮 + `toggleAuth` 函数 + 事件绑定 + 「已禁用」筛选 tab + disabled 徽标 + `disabledN` 计数 + `scopeLabel.disabled` 分支 + `data-disabled` 属性 + `.badge.disabled` CSS + 「可用」过滤与 `accountsForFilter` 的 disabled 判定；后端删 `handleToggleAuth` 函数（credits_handler.go -80 行）+ `management.go` 三处 `/toggle` 接入（注册/分发/mutating path）。cgo-shim build/vet/test 全绿（6.26s），前端两脚本块 `node --check` 通过；grep 全空确认后端无 `handleToggleAuth`/`/toggle` 残留、前端无 `已禁用`/`cntDisabled`/`data-disabled`/`toggleAuth`/`toggleBtn` 残留。保留：`disableAuth/reenableAuth`（lifecycle.go:70/129、keepalive.go:194 自动禁用共享）+ `disabled` 字段持久化链路（认证文件管理开关依赖）+ `disabled_count` 统计字段（panel.go）+ `lazyLoadCredits` 与"全部 lazy 完成"判断的 disabled 过滤（功能性优化）。提交链 81c854b→3ef57d7→5d357bc，CI run 32637348107 success，8 assets checksums 全 OK，registry raw 200 含 0.14.8 + 7 assets raw URL 全 200。真实面板交互验证待做。
- 2026-08-23：账号卡统计位置微调 **已发布**（workbuddy 0.14.6 + qoderwork 0.9.3）：成功、失败、连败、冷却从标题徽标区移动到可用积分首行（`progressHTML(c,a)` 增统计段），`.pb-label` 支持换行，加载中保留「可用积分」，冷却 ticker 只刷新冷却秒数。双面板 4 个脚本块 `node --check` 全部通过，双插件 `cgo-shim-build.py` 的 build/vet/test 全部通过。提交链 195b2ea→a5711ee→f714598，CI 双 run success（32634002586/32634003971），16 assets checksums 全 OK，远端 raw URL 全部 200，registry raw 200 含 0.14.6/0.9.3
- 2026-08-23：账号卡统计位置微调 **已完成未发布**：workbuddy/qoderwork 将成功、失败、连败、冷却从标题徽标区移动到可用积分首行；`.pb-label` 支持换行，加载中保留「可用积分」，冷却 ticker 只刷新冷却秒数。双面板 4 个脚本块 `node --check` 全部通过，双插件 `cgo-shim-build.py` 的 build/vet/test 全部通过；当前仅两个 `panel.html` 有未提交改动。
- 2026-08-23：面板账号卡「成功/失败计数 + 连败/冷却」展示 **已发布**（workbuddy 0.14.5 + qoderwork 0.9.2）：wbAccount 补 Success/Failed（host.auth.list 透传，断点在插件组装丢失非上游缺失）+ FailCount/Cooling/CoolUntil（failoverStateSnapshot 从测试 helper 提升为 dashboard API）；panel.html 徽标区 badge.stat/badge.cooling + 1s 冷却倒计时 ticker；qoderwork 三件套同构适配。提交链 0de225e→1311366→f15bc92→6b20f0b，CI 双 run success（32630699531/32630699252），16 assets checksums 全 OK，远端 raw URL 全部 200
- 2026-08-23：workbuddy-provider v0.14.3 发布（流式切号链 GetBody==nil 断裂修复：`rebuildRequestWithSA` 改 `GetBody()→io.ReadAll→bytes.NewReader` 重建 body，产物 GetBody 恒可用，切号链可走满 retry_on_4xx 预算；0.14.2 复测仍 2 次 429 即中断的真根因，推翻"账号池仅 2 候选"错误假设；提交链 5ae916d→e0ffce0→a03686e→9e04131→06944e7，qoderwork 代码免疫仅修滞后注释，远端 raw URL 200）
- 2026-08-23：workbuddy-provider v0.14.2 + qoderwork-provider v0.9.1 发布（429 纳入同请求切号循环，`isAccountLevel4xx` 增 `StatusTooManyRequests` case；提交链 42b9ac3→4158a59→8a3f18c→d2d9bb9→9efc0d0，远端 raw URL 200 验证通过；0.14.1 面板 tab 补丁已由并行会话先行发布，故 429 修复独立 bump 0.14.2 不重打 tag）
- 2026-08-23：deepseek-vision 第四插件迁移完成（02:23-02:50）后由用户决策**撤销**（02:57）：宿主未配置视觉模型、插件依赖宿主 vision 回调 → 工作区全部回退到 HEAD，三插件基线恢复

- 2026-08-22：workbuddy-provider v0.12.0 发布，移除三池路由只留保号池（提交链 f64f35a→2cdd179→fec796e）
- 2026-08-22：40x 账号级换号重试（401/403/404/405，retry_on_4xx 预算默认 3），workbuddy+qoderwork 对称，未发版
- 2026-08-22：项目改名 cpa-plugin → cpa-workbuddy-plugin，registry/build.yml/go.mod 全链路同步
## 计数锚点区

> 本区由 `memory-usage-tracking-rules` 收口闸门维护：HISTORY 仅窄读计入，会话启动不读不计；被裁剪事件的锚点随事件一起删除（不保留 retired）；本区计数仅作主题热度弱信号。锚点 key 用事件 `- YYYY-MM-DD：` 后的核心主题短语（约前 12 字符，可前缀匹配）。

```yaml
version: 1
anchors:
  - title: "traework 0.1.27 发布部署+生产验收完成"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "workbuddy-token-usage「feed 新增 usage SSE 实时通知 dashboard」"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "trae-local-verify 项目级 skill 创建"
    usage_count: 1
    usage_days: 1
    last_used_at: 2026-09-01
    absorbed_to: null
  - title: "traework-provider **0.1.22 改码未提交**"
    usage_count: 1
    usage_days: 1
    last_used_at: 2026-09-01
    absorbed_to: null
  - title: "traework-provider **0.1.21 发布**"
    usage_count: 1
    usage_days: 1
    last_used_at: 2026-09-01
    absorbed_to: null
  - title: "token-usage-tracker「进 dashboard 页面概率性中断请求」修复"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "token-usage-tracker「进 dashboard 页面概率性中断请求」根因定位+修复"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "账号面板「删除账号」功能"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "账号面板移除「启用/禁用」手动开关"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "账号卡统计位置微调 **已发布**"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "账号卡统计位置微调 **已完成未发布**"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "面板账号卡「成功/失败计数 + 连败/冷却」展示"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "workbuddy-provider v0.14.3 发布"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "workbuddy-provider v0.14.2 + qoderwork-provider v0.9.1 发布"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "deepseek-vision 第四插件迁移完成"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "workbuddy-provider v0.12.0 发布"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "40x 账号级换号重试"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "项目改名 cpa-plugin → cpa-workbuddy-plugin"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
  - title: "token-usage-tracker v0.1.5 清零 envelope 修复落库失败"
    usage_count: 0
    usage_days: 0
    last_used_at: null
    absorbed_to: null
```
