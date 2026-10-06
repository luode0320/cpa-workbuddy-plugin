# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 原生插件 `cpa-workbuddy-plugin`，将腾讯 CodeBuddy（国内版）、WorkBuddy AI（独立国际版）、Trae SOLO、Google Gemini CLI 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式转发、专属面板、扫码登录、保活巡检，并通过统一 token 用量统计分析。
- 范围：仓库内 workbuddy-provider / workbuddy-ai-provider / traework-provider / gemini-provider / workbuddy-token-usage，历史归档 qoderwork-provider。代码实现、测试、资产构建、发布流水线、registry 同步。
- 非范围：CLIProxyAPI 核心服务本体；跨项目文件修改。

## 项目概览

- 状态：活跃维护中。已成功发布并生产部署 WorkBuddy AI 国际版 **0.1.1** 与 WorkBuddy 国内版 **0.15.2**。
- 活动工作区：F:\cpa-plugin
- 更新时间：2026-10-07 (GMT+8)

## 活动会话工作摘要

- 当前会话：2026-10-07：**WorkBuddy AI 0.1.1 与 WorkBuddy 国内版 0.15.2 全流程发版与生产热重载部署上线**。
  - 用户目标：解决 OAuth 国际版账号授权后面板白屏、获取不到模型的问题；清理国内版面板冗余文案与 Global 标签；全流程自主发版并部署上线。
  - 核心实现与发版成果：
    1. **国际版模型兜底（workbuddy-ai-provider 0.1.1）**：在 `models.go` 补齐 17 个静态模型兜底（`auto`, `deepseek-v4.1-flash`, `deepseek-v4-pro`, `glm-5.3`, `glm-5.3-flash`, `glm-5.2`, `glm-5.1`, `glm-5v-turbo`, `kimi-k3-1`, `kimi-k2.8-preview`, `kimi-k2.7`, `kimi-k2.6`, `minimax-m3`, `hy4-preview`, `hy3`, `hy3-x`, `space-bunny`），在上游控制台动态接口偶发 500 时 100% 确保 CPA 宿主能识别完整模型列表；
    2. **国际版面板容错**：在 `workbuddy-ai/panel.html` 增加 `autoToggle` 空指针安全保护，彻底根除国际版缺少该元素抛错导致的白屏中断；
    3. **国内版面板精简（workbuddy-provider 0.15.2）**：彻底移除“全部领取”Global 体验包按钮、筛选栏中冗余的 CN/Global 标签、文案简化为“自动签到”，分区域统计在无 Global 账号时不冗余展示；
    4. **CI 跨平台流水线验证**：两插件版本（0.1.1 与 0.15.2）经 GitHub Actions CI（Run ID: 37485980329 & 37485990761）7 平台全量构建成功并生成 Release 资产；
    5. **资产入库与 Registry 原子同步**：下载 16 个 Release 资产校验通过入库，`registry.json` 同步资产哈希与尺寸，经 `validate-registry.py` 验证通过，推送至 GitHub main 分支并通过 raw CDN 200 验证；
    6. **生产环境热重载落地与在线验证**：通过生产环境（45.207.222.65:8317）管理接口完成热加载，落盘 `.so` 哈希完全匹配；真实调用管理接口验证：CPA 宿主成功挂载全部 17 个模型；国际版面板与国内版面板均返回 HTTP 200，账号信息正常展示且无脚本抛错。

<!-- BEGIN RECENT PROJECT SESSIONS -->
## 最近 5 个同项目会话

| 会话 ID | 标题 / 意图 | 状态 | 最后活动时间 | 关键交付物 / 影响 |
|---|---|---|---|---|
| `01a110f3-1891-72c1-914e-7600d0e8b6df` | 修复国际版模型兜底与白屏、清理国内版冗余标签、发版与生产部署 | completed | 2026-10-07 00:15 | workbuddy-ai 0.1.1 静态模型兜底与白屏修复、workbuddy 0.15.2 面板精简，全流程发布与生产热重载上线 |
| `01a10add-b83c-7500-bc4c-2c92ff9393ca` | 移植 Gemini Provider 插件与 Token 用量统一统计 | completed | 2026-10-05 19:30 | 移植 gemini-provider 0.1.1，接入 Google 图标，打通 NDJSON feed，CI 全平台流水线 |
| `01a0f678-72a0-7900-9c19-bf43949e829f` | 修复面板筛选标签计数未随积分回填重算缺陷 | completed | 2026-10-01 18:30 | workbuddy 0.15.1 / traework 0.2.1 / qoderwork 0.9.21 面板筛选标签计数刷新修复 |
| `01a0c4f8-1120-7500-b88a-df4598124801` | 移除可疑重复可测试标签中的路由排除 | completed | 2026-09-30 02:40 | workbuddy 0.15.0 / traework 0.2.0 发布，默认提交发布授权机制生效 |
| `01a09d31-4400-7500-9988-cc7722119933` | 账号路由重构：移除优先级+硬排除+客户端粘性 | completed | 2026-09-29 02:30 | workbuddy 0.14.43 / traework 0.1.68 简化路由调度逻辑 |
<!-- END RECENT PROJECT SESSIONS -->
