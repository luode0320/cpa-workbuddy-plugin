# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 插件集合 `cpa-workbuddy-plugin`——将腾讯 CodeBuddy（WorkBuddy）与 QoderWork CN 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式推理、每日签到、积分生命周期与 token 用量统计。
- 范围：三插件（workbuddy-provider / qoderwork-provider / workbuddy-token-usage）的迭代、测试、发布与 registry 同步；QoderWork 逆向知识维护。
- 非范围：CLIProxyAPI 网关本体；CPA 内置调度器逻辑的修改（插件只做 host 契约适配）。

## 项目概览

- 状态：活跃维护中（最近发布 2026-08-27 workbuddy 0.14.12 登录轮询重复账号修复）
- 活动会话数：1
- 更新时间：2026-08-27 22:20 (GMT+8)

## 活动会话任务摘要

- 本会话：进入 2026-08-23 第三轮
  1. **【已完成】** WorkBuddy 账号面板「账号删除」功能（workbuddy 0.14.7，已发布）：卡片右上角 `×` + 二次确认模态框
  2. **【已完成】** 移除 WorkBuddy 面板「启用/禁用」手动开关（workbuddy 0.14.8，已发布）：保留后端 disabled 字段链路
  3. **【已发布】** WorkBuddy + QoderWork 账号面板「异步节流刷新」（workbuddy 0.14.9 + qoderwork 0.9.4，2026-08-23 发布）：
     - 后端：新增 `refresh_runner.go`（RefreshRunner 单例 + 1s/账号节流 + pending/running/done/failed 状态机）+ `refresh_runner_test.go`，双插件对称
     - 路由：`POST /refresh` 改异步（EnqueueAll source=panel 立即返回）+ `GET /refresh/status`（Snapshot）+ `GET /credits?track=1` 入队
     - watchdog：`runPreserveWatchdogTick` 改 `cachedCredits` 缓存读 preserve flip + `EnqueueAll(source=watchdog)` 立即返回
     - 前端：双 panel.html 删 `lazyLoadCredits` 自动触发 + `pollRefreshStatus` 2s 轮询 + 卡片 `data-refresh-state` 三态 CSS
     - **第二轮修订（本轮）**：① `EnqueueAll`/`EnqueueOne` 改「运行中则忽略」幂等（`idx < len(batch)` → 返回 0/false，不再替换/追加批次），单测 `EnqueueAllReplacesBatch`→`EnqueueAllIdempotent` + 新增 `EnqueueOneIdempotent`；② WorkBuddy 前端删除顶部「刷新数据」按钮，改 `enterPanel()`（`await load()` 渲染缓存 → `startBackgroundRefresh()` 静默 `POST /refresh` + 轮询），init/saveKey 走 enterPanel，软刷新只 `load()`，完成收尾去 toast（前端无感知）；③ qoderwork 面板前端暂不动（后端幂等已同步）
     - 验证：双插件 cgo-shim build+vet+test 全绿 + 双 panel.html node --check 通过
     - 设计/实施文档：`analysis/workbuddy-async-refresh-design.md`（第 0 节第二轮修订）/ `workbuddy-async-refresh-implementation-plan.md`

## 已完成

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

- 【待办】qoderwork `handlePollLogin` 同构 bug（2026-08-27 发现，用户已确认本轮只发 workbuddy）：qoderwork/oauth.go:386/406/420 三处成功路径仍直接 `toAuthData(sa)`，未做 `ad.ID=""`（其 parse/refresh 路径已修），存在与 workbuddy 0.14.12 修复前相同的重复账号风险。待用户确认后对称修复（3 处）+ cgo-shim 验证 + 发布 qoderwork 0.9.5。
- 【本轮】workbuddy 0.14.12 登录轮询重复账号修复已发布（提交链 f3044fd→eff0fff→28ebe6b，CI run 33081084377）。真实页面交互验证待做（CPA 宿主走一遍 OAuth 登录轮询，确认面板不再出现重复账号）。
- 【本轮】workbuddy 0.14.11 计数持久化重构已发布（提交链 3fabc9a→e80ef2a→8def6b2，CI run 32654838377）。真实页面交互验证待做（容器重启后访问面板确认「成功/失败」为重启前累计值而非 0 起跳，且落盘跟随保号 10min 节奏）。
- 【本轮】workbuddy 0.14.10 成功/失败计数持久化已发布（提交链 a525feb→b1c25ea→4df0736）。真实页面交互验证待做（容器重启后访问面板确认「成功/失败」为重启前累计值而非 0 起跳）。
- 账号面板「异步节流刷新」真实页面交互验证待做（CPA 宿主打开面板 → 直接渲染缓存 → 后台逐卡渐进 done，cgo-shim 无法覆盖这条 UI 链路）
- 账号删除功能真实页面交互验证待做（在 CPA 宿主面板点 `×` 走一遍确认/取消/失败路径，确认卡片刷新与 Toast）
- 账号面板移除「启用/禁用」功能真实页面交互验证待做（已发布 0.14.8，确认面板卡片底部不再出现「禁用/启用」按钮，但「刷新」「已签到/领取」「解除冻结」（如有）「×删除」仍正常）
- 待确认 40x 换号重试的发版节奏（0.14.2 已含 429 切号，retry_on_4xx 预算是否上调待用户拍板）

## 阻断

- 无（Windows 无 CGO 属环境限制，验证走 cgo-shim-build.py，非阻断）

## 验证

- 插件验证：`python scripts/cgo-shim-build.py <plugin>`（build+vet+test 全绿）
- 发布验证：下载 8 assets → sha256sum -c → publish-assets.py → validate-registry.py → 远端 raw URL 200

## 下一执行点

- 【本轮】workbuddy 0.14.11 计数持久化重构已发布。下一步：容器重启后做页面交互验证（「成功/失败」跨重启不归零、且落盘跟随保号 10min 节奏）；qoderwork 面板前端是否对称同步待用户确认。
- 待确认 40x 换号重试的发版节奏（0.14.2 已含 429 切号，retry_on_4xx 预算是否上调待用户拍板）

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
  "registry_updated_at": "1970-01-01T00:00:00Z",
  "projections": []
}
```
<!-- END TASK PLAN PROJECTION -->
