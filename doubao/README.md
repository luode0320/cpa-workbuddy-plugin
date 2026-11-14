# doubao-provider

豆包（Doubao）Provider 插件：把豆包网页端账号能力封装为 CPA 的 OpenAI 兼容 Provider。

## 能力

- 工作模式对话（`agent_mode=1`）：模拟豆包 PC 客户端，按 `model_item_key` 直连
  DeepSeek / Kimi / GLM / 豆包等真实上游模型，支持 `reasoning_effort` 推理档位
- 图像生成、视频生成（走网页端 skill 管线）
- 多账号池与阶梯冷却故障转移
- 管理面板：密钥零输入（自动从 CPA 主面板读取，兜底 URL 参数与手工输入）、
  书签一键授权 / 批量导入凭证（粘贴 / 多选 JSON 文件）、导出账号（单文件 JSON / ZIP）、
  账号筛选排序汇总、启停 / 删除 / 单账号模型探活

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

未知名不做拦截，按工作模式默认模型透传；但面板「测试」按钮走严格解析，
只接受上表中的别名（生图/生视频除外），未命中直接报错而不是悄悄换成默认模型。
豆包上游另有一套内部代号（`4tbp`、`anceba` 等），插件没有代号到模型的映射表，
因此代号只作为排障信息附带，不作为可测试项。

## 已知限制

- 上游不返回 token 用量，响应里的 `usage` 是按字符估算（`estimated: true`）
- 豆包客户端协议不支持 Function Calling，`tools` 参数会被忽略
- 工作模式只处理文本消息，附件与图片输入未实现
- 无人值守部署必须保持 `work_enable_sandbox=false`；声明本地沙箱会让请求挂到超时

## 认证方式

**复用用户浏览器登录态**：用户在自有浏览器登录豆包后，由授权书签把一次性令牌交给插件，
插件换回完整会话 Cookie 并落盘。插件**不实现登录流程**（不扫码、不手机号、不内嵌浏览器），
**也不读浏览器 Cookie 库**——豆包的 `sessionid` / `sid_guard` 是 HttpOnly，页面脚本读不到。

一键授权步骤：

1. 打开插件面板 → 点「豆包一键授权」，会打开授权引导页
2. 把页面上的「豆包一键授权」按钮**拖到浏览器书签栏**（只做一次，别直接点击）
3. 点引导页的「打开豆包并登录」，在豆包完成登录
4. 回到豆包标签页，点一下书签栏里的「豆包一键授权」，页面自动跳回并完成授权

书签只做一件事：在豆包页面内调令牌端点取 `x-tt-token`，不读取也不上传 Cookie，
内容不含任何密钥，可以长期保留。引导页另给两条兜底：一次性脚本（浏览器策略拦掉
`javascript:` 书签时用）与手动粘贴 Cookie。

批量导入：面板「导入凭证」支持粘贴单份 Cookie、粘贴多份凭证，或一次性选中多个
`.json` 文件（单对象 / 数组 / 导出汇总文件三种形态都可直接回灌）

面板的账号管理支持：筛选（全部 / 可用 / 已禁用 / 过期）、排序（创建时间 /
账号标识 / 备注名）、单账号模型探活、启用停用、删除、导出（单文件 JSON 或
每账号一个 JSON 的 ZIP）。导出的文件可直接被同一面板的导入功能回灌。

## 面板与密钥

面板嵌在 CPA 主面板内打开时会自动从宿主 `cli-proxy-auth` 读取管理密钥，
无需手工填写；独立打开（或父页上下文不可用）时按 `?key=xxx` 参数兜底，
两者都拿不到才显示手工输入框。密钥只写入当前标签页的 `sessionStorage`，
不落盘、不写入 URL 历史。

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
