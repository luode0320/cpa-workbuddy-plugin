# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 插件集合 `cpa-workbuddy-plugin`——将腾讯 CodeBuddy（WorkBuddy）与 QoderWork CN 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式推理、每日签到、积分生命周期与 token 用量统计。
- 范围：三插件（workbuddy-provider / qoderwork-provider / workbuddy-token-usage）的迭代、测试、发布与 registry 同步；QoderWork 逆向知识维护。
- 非范围：CLIProxyAPI 网关本体；CPA 内置调度器逻辑的修改（插件只做 host 契约适配）。

## 项目概览

- 状态：活跃维护中（最近发布 2026-08-23 workbuddy 0.14.6 + qoderwork 0.9.3）
- 活动会话数：1
- 更新时间：2026-08-23 18:07 (GMT+8)

## 活动会话任务摘要

- 本会话：将已发布的账号统计从标题徽标区移动到可用积分首行（workbuddy + qoderwork 双插件同步），已随 0.14.6 + 0.9.3 发布完成

## 已完成

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

- 规则文件与四件套自举结果待提交（AGENTS.md / CLAUDE.md / .gitattributes / .editorconfig / PROJECT_*.md 为新增文件，未 git add）

## 阻断

- 无（Windows 无 CGO 属环境限制，验证走 cgo-shim-build.py，非阻断）

## 验证

- 插件验证：`python scripts/cgo-shim-build.py <plugin>`（build+vet+test 全绿）
- 发布验证：下载 8 assets → sha256sum -c → publish-assets.py → validate-registry.py → 远端 raw URL 200

## 下一执行点

- 本轮积分首行布局调整已完成并发布（workbuddy 0.14.6 + qoderwork 0.9.3，远端 main=f714598）
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
