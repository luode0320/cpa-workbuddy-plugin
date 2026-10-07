# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 原生插件 `cpa-workbuddy-plugin`，将腾讯 CodeBuddy（国内版）、WorkBuddy AI（独立国际版）、Trae SOLO、Google Gemini CLI、Cursor（cursor.sh）封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式转发、专属面板、扫码登录、保号巡检，并通过统一 token 用量统计服务。
- 范围：仓库内 workbuddy-provider / workbuddy-ai-provider / traework-provider / gemini-provider / cursor-provider / workbuddy-token-usage（历史归档 qoderwork-provider）的代码实现、测试、资产构建、发布流水线、registry 同步等。
- 非范围：CLIProxyAPI 核心服务本体；跨项目文件修改。

## 项目概况

- 状态：活跃维护中。已成功发布并部署 WorkBuddy AI 国际版 **0.1.6** 与 WorkBuddy 国内版 **0.15.2**；新增第六个插件 cursor-provider **0.1.0**（Cursor 会话 Token 导入，本地验证完成，待发布）。
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-10-07 (GMT+8)

## 活动会话进展摘要

- 当前会话（2026-10-07）：**固化「发布后清理」规则 + release-assets 瘦身**。
  - 用户要求：把「每次发布后都需要清理不需要的垃圾」吸收到项目 skill 与规则中。
  - 落地：新增项目 skill `project-cpa-workbuddy-plugin-release-asset-prune-rules`（保留集=registry 每插件当前版本目录）+ 通用脚本 `scripts/prune-release-assets.py`（--dry-run/--apply，受跟踪目录 git rm、空目录 rmdir）；`AGENTS.md`/`CLAUDE.md`「仓库与发布」小节新增「发布后清理」条；发布 skill 增 Step 13.5 并更新 description。
  - 实操：`release-assets` 3.75 GB → 0.13 GB（删 185 个历史版本目录 / 1469 文件，保留 registry 当前 6 个版本目录），提交 `095f4a4` 推送 `origin/main`；远端当前版本 raw 200、旧版本 404。

- 当前会话（2026-10-07）：**移植 Cursor Provider 插件并新增会话 Token 导入账号能力（cursor-provider 0.1.0）**。
  - 来源：`https://github.com/yobo2u/omsub`（cursor 分支）的 cursor-plugin，按 workbuddy 面板口径全量移植并改造，落在独立插件 `cursor/`（id `cursor-provider`），不依赖 CLIProxyAPI SDK。
  - Token 导入链路：粘贴 `user_<id>::<jwt>`（或 URL 编码 / Cookie 前缀 / 裸 JWT）→ 取 `::` 后段 JWT → `POST https://api2.cursor.sh/oauth/token`（grant_type=refresh_token + client_id + refresh_token）兑换 access_token → `host.auth.save` 落盘为 `cursor-<hash8>.json`（顶层 type=cursor-provider）；按 account_id/email 去重。
  - 管理面板：import / export / delete / enable / disable 五条路由 + 卡片删除/启停、全部启停、导入弹窗、导出、双语 i18n、key 三回退，与 workbuddy 面板一致。
  - 保留移植全量能力：OAuth 轮询登录、executor tool-loop、checkpoint/会话粘性、图片输入、上下文准入。
  - 验证：`python scripts/cgo-shim-build.py cursor` build/vet/test 全绿（含必失败哨兵）；面板单 script 块 node --check 通过；cursor 全量 LF 镜像 gofmt -l 清零；6-review STYLE: PASS。
  - CI/registry：.github/workflows/build.yml 增 cursor-provider-v* tag、dispatch option、test/build 矩阵；registry.json 增 cursor-provider 0.1.0（7 平台 artifacts，sha256/size 占位待 CI 产出后由 publish-assets.py 回填）。
- 当前会话（2026-10-07）：**定位新授权账号离奇消失根因，彻底移除自动物理删除机制，发布并部署 WorkBuddy AI 0.1.6**。
  - 用户反馈：刚刚成功授权了一个号，出现在了面板中，但很短的时间过后账号消失了；
  - 核心排查与根因：
    1. 13:52:07 账号授权成功创建（workbuddyai-3d749a7f-ec76-4a55-a316-37d86c9ff47c.json）；
    2. 13:52:09 有并发请求调用该账号，上游接口返回 upstream 429: Credits exhausted（code: 14018），命中 isHardCreditError；
    3. 旧逻辑中将 Global 账号生命周期硬编码为 lifecycleDelete，触发 reconcileOneAccount 后二次调用 fetchUserResource 确认 0 额度，随即执行 deleteAuth 物理删除了磁盘文件；
  - 架构与生命周期对齐修复 (v0.1.6)：
    1. 彻底将 lifecycleActionFor 返回值调整为 lifecycleDisable（仅软禁用，绝不物理删除账号），与国内版完全对齐；
    2. 放开通用恢复通道：去除 cn 专属判定，当 disabled 账号在后续充值或刷新中检测到 remain > 0 时自动重新激活（lifecycleReenable）；
    3. 修正专属管理面板前端 region 默认 fallback 为 global；
    4. 单测补充断言并通过必失败哨兵编译验证；版本 bump 至 0.1.6，GitHub Actions 48 个构建任务全绿；
    5. 8 个 Release 资产已下载入库并推送到 GitHub，registry.json 0.1.6 校验全绿并推送，生产服务器已成功热重载加载 0.1.6。


- 当前会话（2026-10-07）：**定位模型未展示根因、支持双重 agent 结构与 default 映射，并全链路发布部署 WorkBuddy AI 0.1.5**。
  - 用户反馈：截图反馈“该凭证暂无可用模型 / 该认证凭证可能尚未被服务器加载或没有绑定任何模型”。
  - 核心排查与根因收敛：
    1. **截图时段与旧版本原因**：用户截图时间为今日凌晨 03:21:35，当时线上运行为旧版 0.1.3，其错误请求内网 `/console/enterprises/personal/models` 返回 500 导致动态发现失败；
    2. **凭证禁用状态注销机制**：截图显示该凭证右侧处于【未启用】（灰色禁用）状态。CPA 宿主核心（`service_models.go`）设计规定，禁用凭证立即从 `ModelRegistry` 注销，故点击模型弹窗返回空；
    3. **上游真实协议结构增强兼容 (v0.1.5)**：`https://www.workbuddy.ai/v3/config` 结构增加 `data.agent.agents` 与 `data.agents` 双重提取兼容，增强 `default` / `default-model` 至 `auto` 映射，并补齐带前缀的可观测动态发现日志；
    4. **单测与全架构 CI**：单测覆盖嵌套 agent 结构及 auto 别名继承，全绿通过；版本 bump 至 0.1.5，CI（Run ID: 37575092982）48 个任务构建全绿；
    5. **资产同步与生产热更新**：下载 8 个 Release 资产入库推送到 main，同步更新 `registry.json` 并通过校验，在生产服务器完成 0.1.5 热重载加载生效。

<!-- BEGIN RECENT PROJECT SESSIONS -->
## 最近 5 个同项目会话

| 会话 ID | 标题 / 意图 | 状态 | 活跃时间 | 关键改动 / 影响 |
|---|---|---|---|---|
| `01a110f3-1891-72c1-914e-7600d0e8b6df` | WorkBuddy AI 彻底清空写死模型与 0.1.3 全链路发布部署 | completed | 2026-10-07 03:20 | workbuddy-ai 彻底剔除硬编码模型，完全动态拉取；0.1.3 发布并生产部署热重载验证通过 |
| `01a10add-b83c-7500-bc4c-2c92ff9393ca` | 移植 Gemini Provider 并接入 Token 用量统一统计 | completed | 2026-10-05 19:30 | 移植 gemini-provider 0.1.1，引入 Google 图标，打通 NDJSON feed，CI 全平台流水线 |
| `01a0f678-72a0-7900-9c19-bf43949e829f` | 修复三插件筛选标签计数未随积分回填重算缺陷 | completed | 2026-10-01 18:30 | workbuddy 0.15.1 / traework 0.2.1 / qoderwork 0.9.21 派生筛选标签计数重绘修复 |
| `01a0c4f8-1120-7500-b88a-df4598124801` | 移除不可用重复测试标签与全选路径重构 | completed | 2026-09-30 02:40 | workbuddy 0.15.0 / traework 0.2.0 仓库级默认提交发布授权长效生效 |
| `01a09d31-4400-7500-9988-cc7722119933` | 账号路由重构：移除优先级+硬排除+客户端粘性 | completed | 2026-09-29 02:30 | workbuddy 0.14.43 / traework 0.1.68 单路由池与保号池逻辑 |
<!-- END RECENT PROJECT SESSIONS -->
<!-- BEGIN TASK PLAN PROJECTION -->
```json
{
  "version": 4,
  "registry_schema": "task_plan_projection_registry",
  "registry_updated_at": "2026-10-07T14:03:47.571953Z",
  "projections": [
    {
      "projection_id": "SESSION/30608b3616fa2311bfb720a2776c09ac96b2738601ec3752b03a252c810cdb70",
      "session_id": "01a115da-13cb-7f90-ac94-25c133fdc31e",
      "projection_origin": "synthesized",
      "synthesis_mode": "exact",
      "state": "active",
      "plan_key": "IMPL-CURSOR-PROVIDER-PORT-20261007",
      "source_document": "doc/3-实施/2026-10-07_Cursor插件移植与Token导入实施总览.md",
      "plan_fingerprint": "9eea12fd114fb3e34cb60dd2a60eceb2eacd227a7e9a481c5b3f73c954a2a3b6",
      "updated_at": "2026-10-07T12:25:35Z",
      "steps": [
        {
          "id": "TASK-001",
          "step": "[TASK-001] 移植源码并统一标识",
          "status": "completed"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] 令牌解析与导入",
          "status": "completed"
        },
        {
          "id": "TASK-003",
          "step": "[TASK-003] 导出删除启停接口",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 面板与本地回归收口",
          "status": "completed"
        },
        {
          "id": "TASK-005",
          "step": "[TASK-005] 发布与部署",
          "status": "in_progress"
        }
      ]
    },
    {
      "projection_id": "SESSION/b7bb272cea839ad00154909ba5c6f5976dc459430c297f68c2845b27fa9853bc",
      "session_id": "01a115b7-7f04-7b81-8e96-cb269a144fa4",
      "projection_origin": "synthesized",
      "synthesis_mode": "exact",
      "state": "active",
      "plan_key": "IMPL-CURSOR-PROVIDER-PORT-20261007",
      "source_document": "doc/3-实施/2026-10-07_Cursor插件移植与Token导入实施总览.md",
      "plan_fingerprint": "9eea12fd114fb3e34cb60dd2a60eceb2eacd227a7e9a481c5b3f73c954a2a3b6",
      "updated_at": "2026-10-07T14:03:46.758376Z",
      "steps": [
        {
          "id": "TASK-001",
          "step": "[TASK-001] 移植源码并统一标识",
          "status": "completed"
        },
        {
          "id": "TASK-002",
          "step": "[TASK-002] 令牌解析与导入",
          "status": "completed"
        },
        {
          "id": "TASK-003",
          "step": "[TASK-003] 导出删除启停接口",
          "status": "completed"
        },
        {
          "id": "TASK-004",
          "step": "[TASK-004] 面板与本地回归收口",
          "status": "completed"
        },
        {
          "id": "TASK-005",
          "step": "[TASK-005] 发布与部署",
          "status": "in_progress"
        }
      ]
    }
  ]
}
```
<!-- END TASK PLAN PROJECTION -->
