# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 原生插件 `cpa-workbuddy-plugin`，将腾讯 CodeBuddy（WorkBuddy）、Trae SOLO、Google Gemini CLI 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式转发、用量标签与定时保活，打通统一 token 用量统计。
- 范围：仓库内 workbuddy-provider / traework-provider / gemini-provider / workbuddy-token-usage（历史归档 qoderwork-provider）的代码实现、测试、资产打包、发布流水线、registry 同步与 Trae SOLO / QoderWork / Gemini CLI 协议维护。
- 非范围：CLIProxyAPI 宿主本体；CPA 使用的外部逻辑（仅遵守 host 契约交互）。

## 项目概览

- 状态：活跃维护中。4 插件体系（3 供应商 + 1 用量统计），当前新增引入 gemini-provider **0.1.1**（发布名称 "Gemini Provider"，补充 Google 官方彩色图标，打通 token 用量流式统计，已正式发布并热重载部署上线），现有维护 workbuddy-provider **0.15.1** / traework-provider **0.2.1** / qoderwork-provider **0.9.21** / workbuddy-token-usage **0.2.2**。
- 活动会话数：1（本会话独占，工作区 F:\cpa-plugin）。
- 更新时间：2026-10-05 (GMT+8)

## 活动会话任务摘要

- 当前会话（2026-10-05）：**集成 Gemini Provider（发布名称 "Gemini Provider"，gemini-provider 0.1.1），补充 Google 官方彩色图标，打通 Token 用量流式管道，全套 CI 构建发布、Release 资产归档、注册表同步与生产热重载部署全链路闭环**。
  - 用户目标：将开源插件 `https://github.com/router-for-me/cpa-plugin-gemini-cli` 引入为仓库第 4 个插件（3 服务商 + 1 用量统计），发布名称统一指定为 "Gemini Provider"；补充 Google 官方图标；完成发布并热重载部署至生产环境。
  - 核心实施：
    1. 模块迁入与 C ABI 导出（0.1.0）：创建 `gemini/` 目录，将内部 import 路径统一收敛为 `github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/...`，对齐 Go 1.26 与 `CLIProxyAPI/v7 v7.2.129`；编写 `gemini/main.go` 导出标准 C ABI（`cliproxy_plugin_init`、`cliproxyPluginCall`、`cliproxyPluginFree`、`cliproxyPluginShutdown` 等），注册元数据为 Name="Gemini Provider", ID="gemini-provider", Version="0.1.0"；OAuth 客户端凭据混淆以保障安全合规。
    2. Token 用量跨插件管道：编写 `gemini/usage.go` 与 `gemini/usage_feed.go`，解析流式/非流式响应中的 `usageMetadata`，流式响应通过包装 `io.ReadCloser` 计算 TTFT 耗时纳秒并在流关闭时写入共享 `<root>/data/token-usage-feed.ndjson`。
    3. 用量监控插件适配：更新 `token-usage-tracker/usage_stats/auth_identity.go` 的 `displayAuthProvider`，将 `gemini` / `gemini-cli` / `gemini-provider` 统一归一化展示为 `"Gemini"`。
    4. Google 官方图标补充与元数据绑定（0.1.1）：提取 Google 官方标准正方形彩色矢量 PNG（192×192 RGBA 透明底，6,382 字节），分别落盘 `assets/icons/Gemini.png` 与 `assets/icons/Google.png`；更新 `registry.json` 与 `gemini/main.go` 中的 Logo URL 指向 raw.githubusercontent 仓库源；atomic bump 版本为 `0.1.1`，补齐 CHANGELOG.md。
    5. CI 构建与自动化发布：更新 `.github/workflows/build.yml` 新增 `gemini-provider-v*` 触发、构建矩阵与发布任务；派发 GitHub Actions CI（Run ID `37286392309`）40 个 jobs 全部 success；下载 8 个 release assets，7 平台 zip + checksums.txt 双重 SHA256 校验全绿（commit `809100c`）。
    6. 注册表回填与远端校验：执行 `publish-assets.py gemini-provider 0.1.1` 回填 `registry.json` 并推送到 main（commit `8cf6e90`），远端 raw CDN 链接与哈希验证 ALL PASS。
    7. 生产部署与热重载生效：调用生产 CPA 宿主 `plugin-store install` API 安装 0.1.1；生产容器内 `/CLIProxyAPI/plugins/linux/amd64/gemini-provider-v0.1.1.so` 的 SHA256 为 `779c73be648269e0c80f53e61bf71a3ada5e90d5bf46875cc6a609a9ba685936`，与本地 Release zip 产物 100% 绝对一致；生产宿主日志证实热重载成功：`pluginhost: plugin hot reloaded plugin_id=gemini-provider active_version=0.1.1 retired_version=0.1.0`，管理端 plugin-store 图标正常显示。
  - 验证与门禁：
    1. `gemini/main_test.go` 新增 capabilities 与用量提取测试，双向哨兵拦截证明真实进编译；
    2. `token-usage-tracker/feed_ingest_test.go` 新增 `TestFeedIngestGeminiProvider` 真实用量摄取与聚合测试，双向哨兵拦截证明真实进编译；
    3. `python scripts/cgo-shim-build.py <plugin>` 针对全量插件执行 build/vet/test 全绿（gemini 0.238s / token-usage-tracker 0.782s / traework 1.593s / workbuddy 11.101s / qoderwork 7.014s）；
    4. `python scripts/validate-registry.py registry.json` 校验 5 插件配置通过（schema_version=2）；
    5. 远端 raw CDN 链接 HTTP 200（6,382 字节）验证通过；
    6. 落盘测试主文档 `doc/5-tests/2026-10-05_集成GeminiProvider与多插件Token用量测试.md` 与审查文档 `doc/6-review/2026-10-05_集成GeminiProvider与多插件Token用量_6-review.md`（STYLE: PASS）。

- 历史会话索引摘要：
  - 2026-10-01：修复面板筛选标签计数不随积分回填重算，三插件已发布部署（workbuddy 0.15.1 / traework 0.2.1 / qoderwork 0.9.21），vm 长期回归测试 PASS。
  - 2026-09-30：移除保号池（preserve）机制，路由排除改由「测试」标签（`test_failed`）承担（workbuddy 0.15.0 / traework 0.2.0 已发布部署）。
  - 2026-09-29：账号路由口径改造「优先可用账号 + 硬排除三类标签 + 低积分优先」，workbuddy 0.14.43 / traework 0.1.68 已发布部署。
  - 2026-09-27：workbuddy 0.14.42 面板创建时间改用账号真实创建时间（JWT auth_time）已发布部署。
  - 2026-09-26：WorkBuddy 与 TraeWork 刷新账号自动活跃与面板手动测试按钮功能已实现。
  - 2026-09-14：跨平台 failover 双根因修复（调度层短路 + 错误通道伪装），traework 0.1.60 / workbuddy 0.14.31 / qoderwork 0.9.17 已发布部署。

## 已完成

- 2026-10-05 **gemini-provider 0.1.1 补充 Google 官方彩色图标、正式发布并成功热重载部署生产（本会话闭环）**：提取 Google 官方标准正方形彩色矢量 PNG（192×192 RGBA 透明底，6,382 字节），分别落盘 `assets/icons/Gemini.png` 与 `assets/icons/Google.png`；更新 `registry.json` 与 `gemini/main.go` 中的 Logo URL 指向 raw.githubusercontent 仓库源；atomic bump 版本为 `0.1.1`，补齐 CHANGELOG.md；本地 `cgo-shim-build.py gemini` 验证 build/vet/test 全绿；代码提交推送至 main（commit `e1e0b7f`）；派发 GitHub Actions CI（Run ID `37286392309`）40 个 jobs 全部 success；下载 8 个 release assets，7 平台 zip + checksums.txt 双重 SHA256 校验全绿（commit `809100c`）；执行 `publish-assets.py` 回填 registry.json 并推送（commit `8cf6e90`），远端 raw CDN 与哈希验证 ALL PASS；生产环境调用 `plugin-store install` 安装，落盘 .so SHA256 `779c73be...` 与本地 release zip 100% 一致，CPA 容器日志证实热重载成功：`pluginhost: plugin hot reloaded plugin_id=gemini-provider active_version=0.1.1 retired_version=0.1.0`，管理端 plugin-store 图标正常显示。
- 2026-10-05 **gemini-provider 0.1.0 正式发布并成功热重载部署生产（本会话闭环）**：将开源插件 `cpa-plugin-gemini-cli` 引入为仓库第 4 个插件（3 服务商 + 1 用量统计），发布名称定为 "Gemini Provider"（ID: `gemini-provider`，版本 `0.1.0`）；实现标准 C ABI 导出、panic recover 保护、NDJSON feed 跨插件用量流式管道与 token-usage-tracker 适配支持；CI 全平台构建全绿，Release assets 校验入库并回填 registry.json；生产环境热重载成功，.so sha256 `ca9a64f4...` 吻合。
- 2026-10-01 **workbuddy-provider 0.15.1 / traework-provider 0.2.1 / qoderwork-provider 0.9.21 面板筛选计数修复发布部署**：标签计数挂到统一渲染入口 `renderSummary()`（三插件同构），`load()` 去重；workbuddy 新增 `isAccountExhausted()` 统一耗尽判定；新增长期 Node vm 回归测试 `test/workbuddy/panel_filter_counts_repro.mjs`；CI 发布与生产热重载部署生效。
- 2026-09-30 **workbuddy-provider 0.15.0 / traework-provider 0.2.0 移除保号池发布部署**：移除保号池机制，路由排除改由「测试」标签（`test_failed`）承担；两插件删除 `preserve.go`，重构 `watchdog.go` 为纯看护循环；CI 发布与生产热重载部署生效。
- 2026-09-29 **workbuddy-provider 0.14.43 / traework-provider 0.1.68 账号路由口径改造发布部署**：账号路由改造为「优先可用账号 + 硬排除三类标签 + 低积分优先」；面板选中项与 `active_auth` 严格自洽；CI 发布与生产热重载部署生效。

## 待办

- 无（本轮所有用户目标与技术门禁全部闭环达成）。

## 阻断

- 无（全部发布部署门禁已过，生产环境已热重载运行最新 0.1.1 版本）。

## 验证

- 本地编译验证：`python scripts/cgo-shim-build.py gemini`（build/vet/test 全绿，0.238s）。
- 注册表验证：`python scripts/validate-registry.py registry.json`（5 插件 schema_version=2 全部通过）。
- CI 构建验证：GitHub Actions Run ID `37286392309`（40 jobs 全部 success）。
- 资产校验：`python scripts/download-release-assets.py 0.1.1 gemini-provider`（7 平台 zip + checksums.txt 双重 SHA256 校验通过，ALL CHECKSUMS OK）。
- 远端 raw CDN 验证：`https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/assets/icons/Gemini.png` 返回 HTTP 200，大小 6,382 字节。
- 生产环境部署验证：`POST http://127.0.0.1:8317/v0/management/plugin-store/gemini-provider/install?version=0.1.1` 返回 status="installed", restart_required=false。
- 生产容器二进制校验：`/CLIProxyAPI/plugins/linux/amd64/gemini-provider-v0.1.1.so` SHA256 为 `779c73be648269e0c80f53e61bf71a3ada5e90d5bf46875cc6a609a9ba685936`，与本地 zip 产物 100% 一致。
- 生产热重载日志证据：
  `pluginhost: plugin loaded plugin_id=gemini-provider version=0.1.1 path=plugins/linux/amd64/gemini-provider-v0.1.1.so`
  `pluginhost: plugin registered plugin_id=gemini-provider plugin_name=Gemini Provider version=0.1.1`
  `pluginhost: plugin hot reloaded plugin_id=gemini-provider active_version=0.1.1 retired_version=0.1.0`

## 下一执行点

- 向用户进行最终任务收口交付与状态汇报。

<!-- BEGIN TASK PLAN PROJECTION -->
```json
{
  "version": 4,
  "registry_schema": "task_plan_projection_registry",
  "registry_updated_at": "2026-10-05T19:30:00.000000Z",
  "projections": [
    {
      "projection_id": "SESSION/gemini-provider-integration-20261005",
      "session_id": "01a10add-b83c-7500-bc4c-2c92ff9393ca",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "completed",
      "plan_key": "FEAT/INTEGRATE-GEMINI-PROVIDER-20261005",
      "source_document": "doc/5-tests/2026-10-05_集成GeminiProvider与多插件Token用量测试.md",
      "plan_fingerprint": "cpa-gemini-provider-011-token-usage-and-icon",
      "updated_at": "2026-10-05T19:30:00.000000Z",
      "steps": [
        {
          "id": "GP-01",
          "step": "[GP-01] 迁入开源 cpa-plugin-gemini-cli 并收敛模块与 import 路径",
          "status": "completed"
        },
        {
          "id": "GP-02",
          "step": "[GP-02] 实现 main.go C ABI 导出与元数据暴露 (Gemini Provider 0.1.0)",
          "status": "completed"
        },
        {
          "id": "GP-03",
          "step": "[GP-03] 打通跨插件 Token 用量追加写入 NDJSON feed",
          "status": "completed"
        },
        {
          "id": "GP-04",
          "step": "[GP-04] token-usage-tracker 适配支持 Gemini 归一化展示与测试",
          "status": "completed"
        },
        {
          "id": "GP-05",
          "step": "[GP-05] 更新 CI build.yml 与 registry.json (schema v2)",
          "status": "completed"
        },
        {
          "id": "GP-06",
          "step": "[GP-06] cgo-shim 全绿回归测试与哨兵防假验证",
          "status": "completed"
        },
        {
          "id": "GP-07",
          "step": "[GP-07] 落盘 doc/5-tests 与 doc/6-review 文档",
          "status": "completed"
        },
        {
          "id": "GP-08",
          "step": "[GP-08] 知识库沉淀与项目记忆四件套同步",
          "status": "completed"
        },
        {
          "id": "GP-09",
          "step": "[GP-09] CI 发布与 7 平台 Release 资产下载及哈希校验 (0.1.0)",
          "status": "completed"
        },
        {
          "id": "GP-10",
          "step": "[GP-10] registry.json 更新、远端验证与生产环境热重载部署 (0.1.0)",
          "status": "completed"
        },
        {
          "id": "GP-11",
          "step": "[GP-11] 补充 Google 官方彩色图标资产并更新 Logo 元数据与版本 bump 至 0.1.1",
          "status": "completed"
        },
        {
          "id": "GP-12",
          "step": "[GP-12] 派发 CI 构建、下载校验 0.1.1 资产、更新 registry.json 并完成生产热重载上线",
          "status": "completed"
        }
      ]
    },
    {
      "projection_id": "SESSION/71be5ed10371f684a3d1498a024babb2101371a98a9c270886e5549365bcb789",
      "session_id": "01a0f678-72a0-7900-9c19-bf43949e829f",
      "projection_origin": "persisted",
      "synthesis_mode": "none",
      "state": "inactive",
      "plan_key": "BUG/PANEL-FILTER-COUNTS-STALE-20261001",
      "source_document": "doc/4-bugs/2026-10-01_165945_账号面板筛选标签计数未随积分回填重算.md",
      "plan_fingerprint": "a042e130613a3d7427b41883d0ff93c59fd582af0be6a0b32398ceaf3f2a1a5b",
      "updated_at": "2026-10-01T10:29:40.061158Z",
      "steps": [
        {
          "id": "TF-01",
          "step": "[TF-01] 定位缺陷：筛选标签计数未随积分回填重算",
          "status": "completed"
        },
        {
          "id": "TF-02",
          "step": "[TF-02] 统一耗尽判定 isAccountExhausted 并挂在 renderSummary",
          "status": "completed"
        },
        {
          "id": "TF-03",
          "step": "[TF-03] 复写脚本收为长期测试资产 + 三面板回归",
          "status": "completed"
        },
        {
          "id": "TF-04",
          "step": "[TF-04] cgo-shim 三插件 build/vet/test 全绿",
          "status": "completed"
        },
        {
          "id": "TF-05",
          "step": "[TF-05] 落盘 doc/5-tests 与 doc/6-review",
          "status": "completed"
        },
        {
          "id": "TF-06",
          "step": "[TF-06] 项目记忆计数锚点回写 + 知识库沉淀",
          "status": "completed"
        },
        {
          "id": "TF-07",
          "step": "[TF-07] 提交推送 + CI 发布 + 生产热重载验证 + 面板标签实测",
          "status": "completed"
        }
      ]
    }
  ]
}
```
<!-- END TASK PLAN PROJECTION -->

<!-- BEGIN RECENT PROJECT SESSIONS -->
## 最近 5 个同项目会话

| 会话 ID | 标题 / 主题 | 状态 | 最近活动时间 | 关键交付物 / 结论 |
|---|---|---|---|---|
| `01a10add-b83c-7500-bc4c-2c92ff9393ca` | 集成 Gemini Provider 与多插件 Token 用量统一统计 | completed | 2026-10-05 19:30 | 引入 gemini-provider 0.1.1 并补充 Google 图标，打通 NDJSON feed，CI 发布并生产热重载 |
| `01a0f678-72a0-7900-9c19-bf43949e829f` | 修复面板筛选标签计数未随积分回填重算 | completed | 2026-10-01 18:30 | workbuddy 0.15.1 / traework 0.2.1 / qoderwork 0.9.21 面板筛选自洽并上线 |
| `01a0c4f8-1120-7500-b88a-df4598124801` | 移除保号池改由测试标签承担路由排除 | completed | 2026-09-30 02:40 | workbuddy 0.15.0 / traework 0.2.0 发布部署，默认提交发布授权规则入库 |
| `01a09d31-4400-7500-9988-cc7722119933` | 账号路由改造：可用优先+硬排除+低积分优先 | completed | 2026-09-29 02:30 | workbuddy 0.14.43 / traework 0.1.68 发布部署，生产路由口径自洽 |
| `01a05231-8890-7500-ab12-ee9911223344` | workbuddy 面板改用账号真实创建时间 JWT auth_time | completed | 2026-09-27 23:35 | workbuddy 0.14.42 发布部署，解决面板创建时间随刷新漂移问题 |
<!-- END RECENT PROJECT SESSIONS -->
