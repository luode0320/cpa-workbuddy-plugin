# 项目当前状态

## 目标与范围

- 目标：维护 CLIProxyAPI (CPA) 的 Go 原生插件 `cpa-workbuddy-plugin`，将腾讯 CodeBuddy（国内版）、WorkBuddy AI（独立国际版）、Trae SOLO、Google Gemini CLI 封装为 OpenAI 兼容 provider，提供多账号管理、动态模型、流式转发、专属面板、扫码登录、保号巡检，并通过统一 token 用量统计服务。
- 范围：仓库内 workbuddy-provider / workbuddy-ai-provider / traework-provider / gemini-provider / workbuddy-token-usage（历史归档 qoderwork-provider）的代码实现、测试、资产构建、发布流水线、registry 同步等。
- 非范围：CLIProxyAPI 核心服务本体；跨项目文件修改。

## 项目概况

- 状态：活跃维护中。已成功发布并部署 WorkBuddy AI 国际版 **0.1.3** 与 WorkBuddy 国内版 **0.15.2**。
- 活动工作区：F:\cpa-plugin
- 当前时间：2026-10-07 (GMT+8)

## 活动会话进展摘要

- 当前会话（2026-10-07）：**WorkBuddy AI 0.1.3 彻底清空写死模型、完全依赖动态发现并完成全链路发布与生产热部署**。
  - 用户反馈：弹窗中展示的 GPT 及多系列模型系写死模型，明确要求坚决遵循此前规范，彻底去除写死模型，完全由动态获取。
  - 核心实施与发布成果：
    1. **彻底清空写死模型**：在 `workbuddy-ai/models.go` 中将 `wbModels()` 清空并返回 `nil`，坚决对齐国内版规范。
    2. **标准优先级链**：`resolveModels()` 严格按“动态发现 > config_yaml 覆盖 > 静态兜底(nil)”解析，动态与配置均为空时返回 `[]`，绝不向用户伪造硬编码模型。
    3. **双模式兼容解析**：`callModelsAPI()` / `parseModelsAPIResponse()` 兼容 agents 过滤模式与 models 列表模式，并兼容驼峰与蛇形字段，严格过滤 offline/disabled 模型。
    4. **单测全绿**：`workbuddy-ai/models_test.go` 增加 `TestWBModelsReturnsNil`、`TestResolveModelsPriority` 等全量单测，`cgo-shim-build.py` 验证全绿。
    5. **CI 多架构构建**：版本升级至 `0.1.3`，GitHub Actions CI（Run ID: 37513461781）48 个构建任务全部成功并完成 Release 打包。
    6. **资产同步与 Registry 发布**：下载全部 8 个 release 资产并通过 SHA256 校验入库；更新 `registry.json`（0.1.3）并通过校验推送至 main 分支。
    7. **生产热更新验证**：通过 SSH 在生产服务器（45.207.222.65:8317）完成 `workbuddy-ai-provider 0.1.3` 容器内安装，成功加载热重载日志，账号接口返回模型与动态发现契约完全对齐。

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
