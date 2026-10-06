---
schema_version: 1
template_version: v1
doc_id: IMPL-WORKBUDDY-PANEL-QR-LOGIN-20261006
doc_type: implementation_overview
source_ids: [REQ-WORKBUDDY-MULTI-REGION-LOGIN-20261006]
status: in_progress
version: v1.0
current_slice: CYCLE-01 后端登录管理接口扩展
updated_at: 2026-10-06 18:30:00
complexity: medium
baseline_commit: e1e0b7f
reader_level: developer_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
---

# WorkBuddy 面板扫码登录与多区域支持实施总览

结论：在 `workbuddy-provider` 插件管理面板（`panel.html`）与管理服务（`management.go`）中新增多区域（中国国内版 CN 与国际版 Global）扫码/网页登录能力，免去用户手动获取 JSON 再导入的繁琐操作。影响：用户可直接在 WorkBuddy Web 面板内一键启动扫码登录，选择国内版（CodeBuddy）或国际版（WorkBuddy Global），自动完成 OAuth 凭证拉取、域名标注（`Auth.Domain`）、物理凭证文件写入与账号列表热加载。范围：`workbuddy/management.go` 登录启动与轮询 API、`workbuddy/panel.html` 扫码登录模态框与二维码渲染交互、`workbuddy/login_management_test.go` 确定性单元测试、`scripts/cgo-shim-build.py` 门禁验证。非范围：不破坏 CPA 宿主单 Provider 协议、不变更核心调度器（`scheduler.go`）、不修改换号重试与保活巡检既有逻辑。完成标准：本地确定性单测全绿（双向哨兵拦截证明真实进编译）、`cgo-shim-build.py workbuddy` 构建/vet/测试全通过、`panel.html` JS 语法通过 `node --check` 校验。

## 当前计划最终方案简要说明

在 `workbuddy/management.go` 中新增 `/login/start` 与 `/login/poll` 接口，支持指定 `region`（`cn` 或 `global`）向对应官方网关（`copilot.tencent.com` 或 `www.workbuddy.ai`）发起 OAuth 登录态申请与 Token 轮询拉取；在 `workbuddy/panel.html` 操作栏增加「扫码登录」按钮和模态框，提供国内/国际双版本切换、二维码与跳转链接展示，并以 2 秒间隔轮询状态，成功后直写 `workbuddy-<uid>.json` 并即时刷新网格。

## 基本信息与理解

### agent 理解的问题与目标
当前 `workbuddy-provider` 同时支持中国国内版（CN，`codebuddy.cn`）与国际版（Global，`workbuddy.ai`）账号的推理路由、多模型映射、故障转移与用量统计，但原生 OAuth 登录端点写死了国内域名，国际版账号必须依赖手动构造/导出 JSON 文件后再导入，用户使用成本较高。目标是在插件自带的 Web 控制面板提供原生「扫码登录」弹窗，无缝支持国内版与国际版账号的一键授权入库。

### 本轮计划范围与非范围
- **范围**：
  1. 后端新增 `/v0/management/plugins/workbuddy-provider/login/start` 接口，入参 `region`（`cn` 或 `global`），分别调用国内或国际端点；
  2. 后端新增 `/v0/management/plugins/workbuddy-provider/login/poll` 接口，轮询 Token，解析成功后自动构造标准凭证结构（国际版打标 `Domain: www.workbuddy.ai`，国内版打标 `Domain: www.codebuddy.cn`），持久化物理文件并通知宿主；
  3. 前端 `panel.html` 顶部增加「扫码登录」按钮与弹窗，支持版本切换、二维码渲染、官方跳转、倒计时与自动轮询；
  4. 新增 `workbuddy/login_management_test.go` 自动化测试；
  5. 门禁验证与文档收口。
- **不在范围**：
  1. 不修改 CPA 宿主核心 Go 源码（跨项目只读红线）；
  2. 不修改 `scheduler.go` 路由与低积分优先算法；
  3. 不修改既有 `accountFailover.go` 冷却策略。

### 当前优先闭环
优先闭环 `login/start` 与 `login/poll` 核心前后端交互与落盘逻辑，确保无论选择国内还是国际版，授权成功后均能准确落盘并在面板展现正确的 `CN` / `Global` 徽章。

### 关键假设与冻结约定
1. 国际版 OAuth 端点与国内版完全同构：`POST https://www.workbuddy.ai/v2/plugin/auth/state?platform=CLI` 返回 `code: 0`、`data: {state, authUrl}`，真实探活已证实可用；
2. 登录会话生命周期采用 5 分钟 TTL，独立 Cookie Jar 防并发串扰；
3. 物理文件命名保持规范：`workbuddy-<uid>.json`。

## 现状与落点代码目录树

```text
workbuddy/
├── management.go               # [修改] 注册 /login/start 与 /login/poll 路由及处理逻辑
├── panel.html                  # [修改] 新增扫码登录按钮、弹窗模态框、二维码渲染与轮询逻辑
├── login_management_test.go    # [新增] 覆盖 login/start 与 login/poll 契约的单元测试
doc/3-实施/
└── 2026-10-06_WorkBuddy面板扫码登录与多区域支持实施总览.md # [新增] 本实施计划文档
```

## 实施周期总览

| 周期 | 任务范围 | 核心目标 | 收口条件 |
| :--- | :--- | :--- | :--- |
| **CYCLE-01** | TASK-001 | 后端接口扩展：`/login/start` 与 `/login/poll` | 接口可按 region 生成对应官方 state/authUrl，poll 成功时写入对应 domain 凭证 |
| **CYCLE-02** | TASK-002 | 前端面板交互：`panel.html` 扫码登录弹窗与二维码 | 面板提供国内/国际双版本切换，支持扫码/跳转与 2s 轮询自动刷新 |
| **CYCLE-03** | TASK-003 | 确定性测试与 CGO-shim 编译验证 | 单测全绿，`cgo-shim-build.py workbuddy` build/vet/test 全绿，双向哨兵通过 |
| **CYCLE-04** | TASK-004 | 6-review 风格审查与文档收口 | 代码风格一致性校验通过，更新 PROJECT_CURRENT.md |

### 流程与时序图

```mermaid
flowchart TD
  subgraph PanelUI [管理面板前端 panel.html]
    A[点击「扫码登录」按钮] --> B[弹出选择弹窗: CN 或 Global]
    B --> C[调用 POST /login/start {region}]
    C --> D[渲染二维码 + 官方授权按钮]
    D --> E[以 2s 间隔发起 GET /login/poll]
  end

  subgraph PluginBackend [插件后台 management.go]
    C -->|region=cn| F1[POST copilot.tencent.com/v2/plugin/auth/state]
    C -->|region=global| F2[POST www.workbuddy.ai/v2/plugin/auth/state]
    F1 --> G[返回 state 与 auth_url]
    F2 --> G
    E --> H{检查授权状态}
    H -->|等待中| I[返回 status: pending]
    H -->|已完成| J[提取 Token / Account / 标记 Domain]
    J --> K[直写物理文件 workbuddy-uid.json]
    K --> L[返回 status: success + 账号信息]
  end

  I --> E
  L --> M[关闭弹窗 + Toast提示 + 自动重载网格]
```

## 最小任务清单（垂直切片）

### TASK-001：后端登录管理接口扩展（CYCLE-01）
- **落点文件**：`workbuddy/management.go`
- **实施要点**：
  1. 在 `wbManagementEndpoints` 声明中注册 `/login/start` 与 `/login/poll`；
  2. 扩展 `handleManagement` 路由匹配分支；
  3. 实现 `handleManagementLoginStart`：
     - 解析请求体 `{"region": "cn"|"global"}`，默认 `cn`；
     - 确定上游地址（`upstreamBaseCN` 或 `upstreamBaseGlobal`）；
     - 复用 `newLoginClient()` 发起请求获取 `state` 和 `authUrl`；
     - 存入 `loginStates`，并记录所属的 `region` 与基地址；
     - 返回 `{ "code": 0, "state": "...", "auth_url": "...", "region": "..." }`；
  4. 实现 `handleManagementLoginPoll`：
     - 查询 `loginStates`；
     - 向上游轮询 `/v2/plugin/auth/token`；
     - 获取 token 后，请求 `/v2/plugin/login/account` 提取账号信息；
     - 组装 `storedAuth`，若 region 为 global 则设置 `Auth.Domain = "www.workbuddy.ai"`，若为 cn 则设置 `Auth.Domain = "www.codebuddy.cn"`；
     - 调用 `writeAuthFileDirect` 保存至账号目录，同步通知宿主；
     - 返回 `{ "status": "success", "nickname": "...", "uid": "...", "region": "..." }`。
- **验证点**：路由能准确分发，入参错误时返回 400，未完成时返回 pending。
- **任务完成条件**：通过单元测试验证请求流与状态转换。
- **任务停止条件**：接口协议不一致或写入冲突。
- **最大推进边界**：仅在 `workbuddy/management.go` 内部扩展，不影响其他路由。

### TASK-002：前端面板扫码登录弹窗与交互实现（CYCLE-02）
- **落点文件**：`workbuddy/panel.html`
- **实施要点**：
  1. 在头部操作区 `<div class="actions">` 中增加「扫码登录」按钮；
  2. 增加 `#loginModal` 模态弹窗：
     - 标签切换栏：`[🇨🇳 中国国内版]` 与 `[🌐 国际版 (Global)]`；
     - 二维码渲染容器（支持内嵌轻量纯 JS/SVG 二维码生成或清晰二维码展示）与链接卡片；
     - 「在浏览器打开登录页」跳转按钮与刷新按钮；
     - 状态指示器与倒计时提示；
  3. 编写 `showLoginModal()`、`startLoginSession(region)`、`pollLoginSession(state)`、`closeLoginModal()` 等交互函数；
  4. 轮询成功后，自动关闭弹窗并调用 `load(true)` 刷新账号列表，触发 `toast("登录成功", "ok")`。
- **验证点**：使用 `node --check` 提取脚本块进行语法检查，确保无语法错误和作用域泄露。
- **任务完成条件**：页面能正确拉起弹窗、切换区域、触发轮询与关闭。

### TASK-003：单元测试编写与 CGO-shim 编译验证（CYCLE-03）
- **落点文件**：`workbuddy/login_management_test.go`
- **实施要点**：
  1. 编写 `TestManagementLoginStart_RegionDispatch`：测试传入 cn 和 global 时分别派发到对应端点；
  2. 编写 `TestManagementLoginPoll_PendingAndSuccess`：Mock 上游返回，验证 token 换取与 `Domain` 准确写入；
  3. 执行必失败哨兵测试，证实单测被纳入执行；
  4. 恢复测试并执行 `python scripts/cgo-shim-build.py workbuddy`。
- **通过标准**：build、vet、test 全部通过，耗时正常。

### TASK-004：6-review 风格核对与状态文档收口（CYCLE-04）
- **落点文件**：`doc/6-review/...`、`PROJECT_CURRENT.md`
- **实施要点**：
  1. 检查代码注释、中文编码、无死代码、无格式污染；
  2. 生成 6-review 文档；
  3. 更新 `PROJECT_CURRENT.md` 已完成与当前状态。
- **通过标准**：STYLE: PASS，工程文档与实际代码绝对自洽。

## 真实测试安排

- **测试入口**：`python scripts/cgo-shim-build.py workbuddy`（执行 `go test -v -run TestManagementLogin`）。
- **依赖环境**：Windows 本地 Python + Go 1.26 环境。
- **样本/数据来源**：Mock HTTP Server 模拟 upstreamBaseCN 和 upstreamBaseGlobal 的 `auth/state`、`auth/token`、`login/account` 接口返回。
- **通过标准**：
  1. `TestManagementLoginStart_RegionDispatch` PASS；
  2. `TestManagementLoginPoll_PendingAndSuccess` PASS；
  3. 双向哨兵机制验证（插入 `t.Fatal("sentinel")` 确保确实执行该测试）；
  4. 全量单测 0 失败，无 panic。

## 任务完成与停止边界

- **完成条件**：
  1. 前后端代码全部落盘；
  2. 新增测试双向哨兵验证通过，`cgo-shim-build.py workbuddy` 全绿；
  3. `panel.html` 语法校验通过；
  4. 6-review 审查通过；
  5. 状态同步至 `PROJECT_CURRENT.md`。
- **停止/结束条件**：
  1. 遇到不可恢复的 ABI 破坏；
  2. 官方接口结构发生破坏性变更。
- **最大推进边界**：严格限制在 `workbuddy/` 目录及文档，严禁触碰其他插件及外部项目。
