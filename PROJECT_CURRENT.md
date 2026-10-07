# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 原生插件 `cpa-workbuddy-plugin`，将腾讯 CodeBuddy（国内版）、WorkBuddy AI（独立国际版）、Trae SOLO、Google Gemini CLI 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式转发、专属面板、扫码登录、保号巡检，并通过统一 token 用量统计服务。
- 范围：仓库内 workbuddy-provider / workbuddy-ai-provider / traework-provider / gemini-provider / workbuddy-token-usage（历史归档 qoderwork-provider）的代码实现、测试、资产构建、发布流水线、registry 同步等。
- 非范围：CLIProxyAPI 核心服务本体；跨项目文件修改。

## 项目概况

- 状态：活跃维护中。已成功发布并部署 WorkBuddy AI 国际版 **0.1.5** 与 WorkBuddy 国内版 **0.15.2**。
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-10-07 (GMT+8)

## 活动会话进展摘要

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
