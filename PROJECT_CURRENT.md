# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 原生插件 `cpa-workbuddy-plugin`，将腾讯 CodeBuddy（国内版）、WorkBuddy AI（独立国际版）、Trae SOLO、Google Gemini CLI 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式转发、专属面板、扫码登录、保活巡检，并通过统一 token 用量统计分析。
- 范围：仓库内 workbuddy-provider / workbuddy-ai-provider / traework-provider / gemini-provider / workbuddy-token-usage，历史归档 qoderwork-provider。代码实现、测试、资产构建、发布流水线、registry 同步。
- 非范围：CLIProxyAPI 核心服务本体；跨项目文件修改。

## 项目概览

- 状态：活跃维护中。已新增并成功发布、上线部署独立的 WorkBuddy AI 国际版插件 **0.1.0**（ID: `workbuddy-ai-provider`，名称: "WorkBuddy AI"）。
- 活动工作区：F:\cpa-plugin
- 更新时间：2026-10-06 (GMT+8)

## 活动会话工作摘要

- 当前会话：2026-10-06：**完成独立国际版 WorkBuddy AI 插件（workbuddy-ai-provider 0.1.0）全流程发版与生产部署上线**。
  - 用户目标：支持国际版 WorkBuddy AI 独立插件，端点唯一固定为国际站，提供独立的扫码登录与专属面板，核心保活、测试与切换调度与国内版保持通用；确认发布后全自主推进完整发版与部署。
  - 核心实现与发版成果：
    1. 新建独立模块 `workbuddy-ai/`（ID: `workbuddy-ai-provider`，Name: "WorkBuddy AI"，VERSION: 0.1.0）；
    2. 凭证防碰撞：文件名前缀唯一固定为 `workbuddyai-`，顶层类型为 `workbuddy-ai-provider`；
    3. 端点纯化：基地址固定为 `https://www.workbuddy.ai`，彻底移除国内版混用与 CN 签到；
    4. 双通道扫码：支持 CPA 宿主原生扫码 + 插件自身专属面板（`panel.html`）提供「📱 扫码登录 / 添加账号」弹窗；
    5. 保留核心特性：14天 Pro 体验包一键领取、4小时 access-token 保活刷新（keepalive）、10分钟活跃巡检与定时 ping（watchdog）、低积分优先与会话粘性调度、`retry_on_4xx` 与 NDJSON 共享用量 feed；
    6. 跨插件联动：在 `token-usage-tracker/usage_stats/auth_identity.go` 接入 "WorkBuddy AI" 显示归一化；
    7. GitHub Actions 跨平台 CI 构建成功（Run ID: 37469999135，7 平台架构 build+vet+test+release 全绿）；
    8. 8 个 Release 资产（7 个 zip 包 + checksums.txt）下载自检通过并推送入库；
    9. `registry.json` 原子同步 0.1.0 资产哈希与尺寸，经 `validate-registry.py` 及 GitHub raw CDN 远程验证 ALL PASS；
    10. 生产部署落地：调用生产环境（45.207.222.65:8317）plugin-store install 接口完成热加载，落盘二进制 SHA256 100% 吻合，`accounts` 接口 200，`panel` 资源接口 200，专属面板方案 B 扫码弹窗正常渲染。

<!-- BEGIN RECENT PROJECT SESSIONS -->
## 最近 5 个同项目会话

| 会话 ID | 标题 / 意图 | 状态 | 最后活动时间 | 关键交付物 / 影响 |
|---|---|---|---|---|
| `01a110f3-1891-72c1-914e-7600d0e8b6df` | 独立国际版 WorkBuddy AI 插件落地、发布与生产部署 | completed | 2026-10-06 22:10 | 独立 workbuddy-ai-provider 0.1.0 发版与生产热重载部署，双通道扫码授权，workbuddy.ai 纯化 |
| `01a10add-b83c-7500-bc4c-2c92ff9393ca` | 移植 Gemini Provider 插件与 Token 用量统一统计 | completed | 2026-10-05 19:30 | 移植 gemini-provider 0.1.1，接入 Google 图标，打通 NDJSON feed，CI 全平台流水线 |
| `01a0f678-72a0-7900-9c19-bf43949e829f` | 修复面板筛选标签计数未随积分回填重算缺陷 | completed | 2026-10-01 18:30 | workbuddy 0.15.1 / traework 0.2.1 / qoderwork 0.9.21 面板筛选标签计数刷新修复 |
| `01a0c4f8-1120-7500-b88a-df4598124801` | 移除可疑重复可测试标签中的路由排除 | completed | 2026-09-30 02:40 | workbuddy 0.15.0 / traework 0.2.0 发布，默认提交发布授权机制生效 |
| `01a09d31-4400-7500-9988-cc7722119933` | 账号路由重构：移除优先级+硬排除+客户端粘性 | completed | 2026-09-29 02:30 | workbuddy 0.14.43 / traework 0.1.68 简化路由调度逻辑 |
<!-- END RECENT PROJECT SESSIONS -->
