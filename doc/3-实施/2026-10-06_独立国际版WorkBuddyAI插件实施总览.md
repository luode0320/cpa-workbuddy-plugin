---
schema_version: 1
template_version: v1
doc_id: IMPL-WORKBUDDY-AI-INDEPENDENT-PLUGIN-20261006
doc_type: implementation_overview
source_ids: [REQ-WORKBUDDY-AI-STANDALONE-PLUGIN-20261006]
status: in_progress
version: v1.0
current_slice: CYCLE-01 创建独立插件骨架与端点适配
updated_at: 2026-10-06 18:45:00
complexity: medium
baseline_commit: e1e0b7f
reader_level: developer_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
---

# 独立国际版 WorkBuddy AI 插件实施总览

结论：将国际版 WorkBuddy（`workbuddy.ai`）从原单插件混合体系中完全解耦，新建独立的 `workbuddy-ai-provider` 插件（目录 `workbuddy-ai/`），在 CPA 宿主中提供原生的国际站扫码授权入口、专属的管理面板（`panel.html`）以及与既有体系完全一致的低积分调度、故障退避与跨插件 Token 用量监控。影响：彻底解耦国内版（CodeBuddy CN）与国际版（WorkBuddy Global），CPA 宿主可直接提供原生 `WorkBuddy AI` 登录与面板，免去在单插件内做繁琐的区域特判，底层保号分析、自动测试、自动切换与用量统计 100% 保持通用。范围：创建 `workbuddy-ai/` 全套插件实现、专属管理面板、确定性单元测试、更新 `token-usage-tracker` 归一化展示、更新 CI 与本地构建支持。非范围：不修改 CPA 宿主核心二进制、不影响已上线的国内版 `workbuddy-provider`。完成标准：`cgo-shim-build.py workbuddy-ai` 构建/vet/测试全通过、双向哨兵拦截证明真实进编译、`token-usage-tracker` 单元测试通过。

## 当前计划最终方案简要说明

以生产验证极其成熟的 `workbuddy-provider` 与新独立插件 `gemini-provider` 为蓝本，创建独立的 `workbuddy-ai/` 插件工程。插件 ID 为 `workbuddy-ai-provider`，名称为 `WorkBuddy AI`。所有上游端点彻底纯化为 `https://www.workbuddy.ai`，原生 OAuth 登录（`handleStartLogin`）直连国际站授权端点。移除国内专属的每日签到逻辑，保留专属于国际站的 14 天 Pro 体验包（`/trial`）领取；管理面板定制为专属国际站控制台；用量数据写入共享 NDJSON 并由 `token-usage-tracker` 统一聚合展示。

## 基本信息与理解

### agent 理解的问题与目标
原 `workbuddy-provider` 单插件同时承载国内版与国际版账号，导致宿主原生 OAuth 登录端点写死了国内域名（`copilot.tencent.com`），添加国际版账号必须依赖手动导出/导入。虽然通过在面板加弹窗可以缓解，但更彻底、更符合 CPA 插件哲学的做法是：将国际站作为独立的 Provider（`workbuddy-ai-provider`）。这样宿主原生即可提供「WorkBuddy AI」扫码登录，拥有独立的侧边栏面板菜单，所有路由、冷却、测试和用量功能完全与国内版对齐，实现最高纯粹度与零心智负担。

### 本轮计划范围与非范围
- **范围**：
  1. 纯化还原：确保既有 `workbuddy`（国内版）纯净稳定，撤销多区域试验性改动；
  2. 独立插件源码：创建 `workbuddy-ai/` 目录，实现 C ABI 导出、配置管理、OAuth 原生登录（`https://www.workbuddy.ai/v2/plugin/auth/state?platform=CLI`）、执行器转发、低积分路由调度器、定时活跃 ping 测试、故障转移与 40x 重试、14 天体验包（`/trial`）领取、生命周期与账号管理；
  3. 专属面板：定制 `workbuddy-ai/panel.html` 与 `panel.go`，路由前缀为 `/v0/management/plugins/workbuddy-ai-provider`，菜单展示为 `WorkBuddy AI`，移除签到按钮，突出国际版体验包与积分监控；
  4. 用量管道打通：在 `workbuddy-ai/usage_feed.go` 将 Provider 标为 `WorkBuddy AI`，在 `token-usage-tracker/usage_stats/auth_identity.go` 增加对 `workbuddy-ai` / `workbuddy-ai-provider` 的友好名称归一化；
  5. 自动化测试：编写 `workbuddy-ai/*_test.go`，覆盖元数据契约、OAuth 端点、Trial 领取、调度器与活跃探测；
  6. 构建与流水线：验证 `cgo-shim-build.py workbuddy-ai` 全绿，更新 `.github/workflows/build.yml` 与 `registry.json` 元数据。
- **非范围**：
  1. 跨项目写入红线：不修改父目录 CPA 宿主代码；
  2. 不影响国内版 `workbuddy-provider`、`traework-provider`、`gemini-provider` 的正常运行。

### 当前优先闭环
优先创建 `workbuddy-ai` 插件工程与全部代码，完成本地 CGO_ENABLED=0 shim 构建、vet 与测试通过，确保单元测试具备双向哨兵拦截，证实测试代码真实进入编译运行。

### 关键假设与冻结约定
1. 插件 ID：`workbuddy-ai-provider`，展示名：`WorkBuddy AI`，ProviderName 契约：`workbuddy-ai-provider`；
2. 凭证文件命名规范：`workbuddy-ai-<uid>.json`，凭证顶层 `type: "workbuddy-ai-provider"`，避免与国内版凭证混淆；
3. 国际站端点基地址唯一固定为 `https://www.workbuddy.ai`；
4. 国际站不支持每日签到，仅保留 `/trial`（14 天 Pro 体验包）；
5. 调度器、冷却、`retry_on_4xx`、`test_failed`、`active_ping` 逻辑与国内版 100% 同构。

## 现状与落点代码目录树

```text
workbuddy-ai/
├── main.go                     # C ABI 导出、插件注册（WorkBuddy AI）、方法分发
├── oauth.go                    # 国际站 OAuth 原生登录启动、Token 轮询、凭证保存
├── models.go                   # 国际站动态模型拉取与映射
├── stream.go                   # 流式与非流式 chat/completions 转发
├── scheduler.go                # 低积分优先与会话粘性调度器
├── active_auth.go              # 活跃账号选路、测试/冷却状态过滤
├── active_ping.go              # 定时 active_ping 活跃度探测
├── accountFailover.go          # 阶梯故障退避（429/402/5xx）
├── failover_retry.go           # 40x 换号重试逻辑（retry_on_4xx）
├── test_failed_tag.go          # test_failed 标签内存与磁盘同步
├── billing.go                  # 国际站积分查询与 /trial 体验包领取
├── credits_handler.go          # 体验包与凭证导入/导出处理
├── management.go               # 管理路由（/accounts, /trial, /test-active, /select 等）
├── panel.go                    # 国际站 Web 面板数据聚合
├── panel.html                  # 国际站专属 Web 管理面板
├── authfile.go                 # 凭证读写与存储格式转换
├── created_at.go               # JWT auth_time 解析
├── lifecycle.go                # 额度耗尽与账号生命周期
├── watchdog.go                 # 保活与自动活跃巡检
├── usage_feed.go               # Token 用量追加写入共享 NDJSON
├── VERSION                     # 0.1.0
├── go.mod                      # 依赖声明（对齐 Go 1.26 与 CLIProxyAPI v7.2.129）
├── go.sum                      # 依赖锁文件
└── *_test.go                   # 确定性单元测试套件
token-usage-tracker/
└── usage_stats/
    └── auth_identity.go        # 扩展支持 WorkBuddy AI 供应商名称归一化
scripts/
└── cgo-shim-build.py           # 验证支持新插件目录
registry.json                   # 登记 workbuddy-ai-provider
.github/workflows/
└── build.yml                   # CI 构建流水线接入
doc/3-实施/
└── 2026-10-06_独立国际版WorkBuddyAI插件实施总览.md
```

## 依赖图与任务拆分

```text
[Task 1: 环境纯化与项目准备]
        │
        ▼
[Task 2: workbuddy-ai 核心逻辑与端点纯化实现]
        │
        ▼
[Task 3: workbuddy-ai 专属面板与管理路由]
        │
        ▼
[Task 4: token-usage-tracker 适配与用量管道]
        │
        ▼
[Task 5: 单元测试套件编写与双向哨兵验证]
        │
        ▼
[Task 6: 本地构建验证、流水线配置与工程收口]
```

### 最小闭环任务列表

1. **Task 1: 环境纯化与项目准备**
   - 确认既有 `workbuddy` 仓库处于纯净状态；
   - 建立 `workbuddy-ai/` 目录与基础配置（`VERSION`、`go.mod`、`go.sum`）。

2. **Task 2: workbuddy-ai 核心逻辑与端点纯化实现**
   - 实现 `main.go`，注册 `WorkBuddy AI`（`workbuddy-ai-provider`）；
   - 实现 `oauth.go`，原生直连 `https://www.workbuddy.ai/v2/plugin/auth/state?platform=CLI`；
   - 迁移并纯化 `stream.go`, `scheduler.go`, `active_auth.go`, `active_ping.go`, `accountFailover.go`, `failover_retry.go`, `billing.go`, `authfile.go`, `lifecycle.go`, `watchdog.go`，移除所有国内腾讯特定逻辑与每日签到特判。

3. **Task 3: workbuddy-ai 专属面板与管理路由**
   - 编写 `management.go`，管理路径挂载在 `/v0/management/plugins/workbuddy-ai-provider`；
   - 编写 `panel.go` 与 `panel.html`，展示「WorkBuddy AI 账号管理 (国际站)」，保留 14 天 Pro 体验包领取与测试操作。

4. **Task 4: token-usage-tracker 适配与用量管道**
   - 更新 `usage_feed.go` 上报 `WorkBuddy AI`；
   - 更新 `token-usage-tracker/usage_stats/auth_identity.go` 归一化。

5. **Task 5: 单元测试套件编写与双向哨兵验证**
   - 编写 `workbuddy-ai/main_test.go` 与关键功能测试；
   - 双向哨兵拦截验证真实编译。

6. **Task 6: 本地构建验证、流水线配置与工程收口**
   - 运行 `cgo-shim-build.py workbuddy-ai` 验证全绿；
   - 更新 `registry.json` 与 `.github/workflows/build.yml`。
