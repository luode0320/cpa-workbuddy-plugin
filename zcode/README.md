# ZCode Provider (CLIProxyAPI Plugin)

`zcode-provider` 是 CLIProxyAPI (CPA) 的企业级动态库插件，用于将 BigModel Coding Plan 的 Anthropic 协议接入 CPA，并全面对齐 `workbuddy-provider` 的多账号运维管理能力。

## 核心特性

1. **ZCode 客户端指纹注入（Mimic）**：自动注入 ZCode 桌面客户端全量 Headers 指纹（`x-api-key`、`http-referer`、`User-Agent`、`x-zcode-app-version`、`x-zcode-agent`、`x-query-id`、`x-session-id`、`x-zcode-trace-id` 等）。
2. **多账号独立存储与管理**：凭据安全保存在 `~/.antigravity_cockpit/zcode_accounts/zcode-<hash8>.json`，支持通过 Web 面板或管理 API 批量导入、导出、启用、禁用与删除。
3. **专属管理控制台（Web Panel）**：挂载于 `/v0/resource/plugins/zcode-provider/panel`，支持深浅色主题切换、指标统计、账号卡片运维。
4. **选模型测试探活**：面板支持弹出模型选择模态框，按指定模型向 `/test-active` 发起真实对话探活，测量响应延迟并自动标记故障状态。
5. **智能调度与故障转移**：支持多账号轮询调度、429/5xx 阶梯退避冷却（1/3/10 分钟）、401/403/404 同请求自动换号重试、测试失败（`test_failed`）硬隔离。
6. **流式与非流式转发**：实现 `MethodExecutorExecute` 与 `MethodExecutorExecuteStream`，通过 `host.stream.emit` 将上游 SSE 实时推送给客户端。
7. **Token 用量统计**：自动采集输入/输出 Token 与请求耗时，追加写入统一 `token-usage-feed.ndjson`，联动 `workbuddy-token-usage` 看板。

## 配置示例

```yaml
plugins:
  enabled: true
  configs:
    zcode-provider:
      enabled: true
      base_url: "https://open.bigmodel.cn/api/anthropic"
      dynamic_models: true
      model_cache_seconds: 300
      timeout_seconds: 300
      models:
        - "GLM-5.3"
        - "GLM-5.3-Flash"
        - "GLM-4-Plus"
        - "GLM-4-Air"
        - "GLM-4-Flash"
      model_map: {}
      mimic:
        enabled: true
        app_version: "3.14.4"
        platform: "win32-x64"
        os_category: "windows"
        os_version: "10.0.19045"
        language: "zh-CN"
        timezone: "Asia/Shanghai"
        release_channel: "production"
        title: "Z Code@electron"
```

## 本地验证

```bash
python scripts/cgo-shim-build.py zcode
```
