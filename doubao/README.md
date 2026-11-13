# doubao-provider

豆包（Doubao）Provider 插件：把豆包网页端账号能力封装为 CPA 的 OpenAI 兼容 Provider。

## 能力

- 工作模式对话（`agent_mode=1`）：模拟豆包 PC 客户端，按 `model_item_key` 直连
  DeepSeek / Kimi / GLM / 豆包等真实上游模型，支持 `reasoning_effort` 推理档位
- 图像生成、视频生成（走网页端 skill 管线）
- 多账号池与阶梯冷却故障转移
- 管理面板：登录态导入、账号管理、模型展示、状态诊断

## 模型

对外模型名统一带 `doubao/` 前缀。

| 模型 | 上游主键 | 上下文 | 说明 |
|---|---|---|---|
| `doubao/default` | 工作模式默认 | 300k | 未指定模型时的行为，等同 `deepseek-v4-flash` |
| `doubao/pro` | 工作模式默认 | 300k | 同上，推理档拉满（4） |
| `doubao/image` | skill 3 | — | 图像生成 |
| `doubao/video` | skill 5 | — | 视频生成 |
| `doubao/auto` | `9` | 256k | 上游自动挑模型，免费额度可用 |
| `doubao/doubao-2.1-lite` | `seed-lite-7b` | 256k | 轻量对话 |
| `doubao/doubao-2.1-pro` | `5` | 256k | 豆包旗舰对话 |
| `doubao/deepseek-v4-flash` | `person-deepseek-v4-1-flash-261017` | 300k | 工作模式默认模型 |
| `doubao/deepseek-v4-pro` | `person-deepseek-v4-pro-ga-260813` | 300k | DeepSeek 高配档 |
| `doubao/kimi-k3` | `person-kimi-k3-261111` | 300k | Kimi 长上下文 |
| `doubao/kimi-k3.1-pro` | `person-kimi-k3-1-pro-261111` | 300k | Kimi 新版旗舰 |
| `doubao/kimi-k3.1-pro-fast` | `person-kimi-k3-1-pro-highspeed-261111` | 300k | Kimi 高速档 |
| `doubao/glm-5.3` | `person-glm-5-3-261111` | 300k | GLM 旗舰档 |
| `doubao/glm-5.3-flash` | `person-glm-5-3-flash-261111` | 300k | GLM 轻量档 |

未知名不做拦截，按工作模式默认模型透传。

## 已知限制

- 上游不返回 token 用量，响应里的 `usage` 是按字符估算（`estimated: true`）
- 豆包客户端协议不支持 Function Calling，`tools` 参数会被忽略
- 工作模式只处理文本消息，附件与图片输入未实现
- 无人值守部署必须保持 `work_enable_sandbox=false`；声明本地沙箱会让请求挂到超时

## 认证方式

**复用用户浏览器登录态**：用户在自有浏览器登录豆包后，从浏览器导出会话 Cookie 并导入本插件。插件**不实现登录流程**（不扫码、不手机号、不内嵌浏览器）。

导出步骤：

1. 在浏览器打开并登录 `https://www.doubao.com`
2. 打开开发者工具 → Application → Cookies → `https://www.doubao.com`
3. 复制完整 Cookie header（至少需含 `flow_cur_user_sec_id` 与 `sessionid`）
4. 粘贴到本插件面板完成导入

## 风险提示

- 本插件走豆包网页端 / PC 客户端协议（非火山方舟开放平台 API），协议可能随上游版本变化而失效
- 上游对请求频率与账号行为有风控策略，请合理控制调用频率
- 账号订阅状态可能影响生图/生视频等高级能力

## 配置项

| 键 | 类型 | 说明 |
|---|---|---|
| `timeout_seconds` | 整数 | 非流式上游请求超时（秒），默认 120 |
| `stream_timeout_seconds` | 整数 | 流式上游请求超时（秒），默认 600 |
| `model_cache_seconds` | 整数 | 模型目录缓存时长（秒），默认 300 |
| `work_bot_id` | 字符串 | 工作模式 `bot_id`，默认 `7338286299411103781`；账号自带的 `bot_id` 优先 |
| `work_mode_id` | 字符串 | 工作模式 `mode_id`，默认 `3`（工作任务） |
| `work_reasoning_effort` | 字符串 | 默认推理档位 0-4，默认 `4` |
| `work_memory_profile` | 字符串 | 默认记忆档位，默认 `300k_230` |
| `work_total_window` | 字符串 | 默认上下文窗口，默认 `300000` |
| `work_enable_sandbox` | 布尔 | 是否声明本地沙箱，默认 `false`；无人值守必须保持 `false` |
| `work_workspace` | 字符串 | 工作目录，默认空（不声明本地目录） |

## 构建与验证

本插件为 c-shared 动态库（`import "C"`）。本地无 CGO 环境时用 shim 验证：

```bash
python scripts/cgo-shim-build.py doubao
```
