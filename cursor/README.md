# Cursor for CPA/CLIProxyAPI

这是一个独立的 CLIProxyAPI 原生动态库插件，把用户本人授权的 Cursor 订阅接入 OpenAI 兼容的 `/v1/chat/completions` 接口。插件直接安装到 CLIProxyAPI 的插件目录，不要求部署或运行 opencodex 服务，也不修改 CPA Manager Plus 或 CLIProxyAPI 源码。

> [!IMPORTANT]
> 本项目是非官方社区插件，与 Cursor、Anysphere、CLIProxyAPI、CPA Manager Plus 或 opencodex 无隶属或授权关系。使用前请完整阅读[免责声明](DISCLAIMER.md)，并自行确认符合适用法律、服务条款及订阅限制。

## 当前源码版本

本仓库（`luode0320/cpa-workbuddy-plugin`）内的移植版本为 **0.1.0**，插件 id 为 `cursor-provider`，移植自 [yobo2u/omsub](https://github.com/yobo2u/omsub/tree/cursor) 的 cursor-plugin（上游源码版本 0.6.6，保留其上下文准入、会话排队与 Blob 预算、输出截断和模型发现可用性保护），并新增**会话 Token 导入**与**账号导入 / 导出 / 删除 / 启用禁用**管理能力。安装包见本仓库 [GitHub Release](https://github.com/luode0320/cpa-workbuddy-plugin/releases)；安装前请校验同一 Release 的 `checksums.txt`。

## 已验证宿主环境

- CLIProxyAPI `v7.3.17`，提交 `9bdde54`
- CPA Manager Plus `v1.14.0`
- Linux amd64
- Cursor OAuth、动态模型发现、非流式、SSE 流式和错误路径
- 插件自有 Cursor 管理页、受认证管理 API、模型禁用与本地估算用量

## 安装

### CLIProxyAPI 插件商店

官方插件商店收录后，可在 CPA Manager Plus / CLIProxyAPI 插件商店中选择
 `cursor` 安装。商店会从最新的 `v<version>` GitHub Release 下载当前平台 ZIP，
 并用同一 Release 中的 `checksums.txt` 校验文件。

本仓库通过自定义插件源 `registry.json` 提供 7 平台构建（darwin/linux/freebsd/windows × amd64/arm64），插件 id 为 `cursor-provider`：

```text
cursor-provider_0.1.0_linux_amd64.zip
checksums.txt
```

### 手动安装

解压发布包，然后把 `--plugins-dir` 指向 CLIProxyAPI 配置中的 `plugins.dir`：

```sh
unzip cursor-provider_0.1.0_linux_amd64.zip
sudo ./install.sh --plugins-dir /opt/cpa-manager-plus/cliproxyapi/plugins
```

在 CPA Manager Plus 插件页面启用 `cursor-provider`，或确保 CLIProxyAPI 配置包含：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cursor-provider:
      enabled: true
      priority: 1
```

重启 CLIProxyAPI 后，可在 CPA Manager Plus 的认证页面发起 Cursor OAuth（浏览器登录和授权必须由 Cursor 账号本人完成），也可直接在“Cursor 管理”面板中**导入会话 Token**。凭据由 CLIProxyAPI 的认证目录保存为 `cursor-<hash8>.json`，插件不会把 token 写入自身配置。

## Cursor 管理

插件启用后会通过 CLIProxyAPI 的原生插件资源机制注册“Cursor 管理”菜单，不需要修改 CPA Manager Plus 页面。管理页提供：

- Cursor OAuth 账户状态和实时可用模型；
 - **导入 Token**：粘贴 Cursor 会话 Token（形如 `user_<id>::<jwt>`、URL 编码形态或 `WorkosCursorSessionToken=...` Cookie，一行一个），插件用标准 OAuth 令牌端点兑换后落盘；同一账号身份自动去重；
 - **导出凭证 / 删除账号 / 启用禁用**：导出全部账号凭据为 JSON 备份、二次确认删除账号、按账号或全部启用/禁用；
- 每个账户的模型禁用选择器，以及“保存设置 / Save settings”和需确认保存的“全部禁用 / Disable all”操作；
- 插件进程启动后的本地估算 Token 与请求计数；
- 明确的订阅额度不可用状态。

管理页完整支持中文和英文。首次打开时优先使用已保存的语言偏好，否则跟随浏览器语言，非中文环境默认英文；页面右上角可随时切换“中文 / English”。切换会同步更新静态文案、账户状态、用量指标、模型控制、操作提示、页面标题及无障碍语言标记。插件只保存语言偏好；管理密钥仅保留在当前标签页会话中，面板启动时检测到可用密钥会直接加载状态（免密直入），未检测到才显示输入区。

模型禁用规则保存在对应 Cursor OAuth 认证 JSON 的 `disabled_models` 字段中。插件会同时在模型发现和请求执行阶段应用规则，刷新 OAuth token 时也会保留规则。

账户列表只显示仍有物理文件的 Cursor OAuth 凭据，不显示 CLIProxyAPI 的 `runtime_only` 投影或文件删除后短暂残留的 `source: memory` 运行时记录；如果旧数据中多个条目解析到同一个 Cursor `account_id` 或邮箱，也只显示一个逻辑账户。

文件加载时，运行时账号 ID 由宿主按实际凭据路径生成；新 OAuth 登录使用包含 `.json` 的文件名作为 ID，令牌刷新沿用宿主已有 ID 和文件名。这避免保存模型设置时 `host.auth.save` 为同一文件另建账号。升级此身份修复后需重启 CLIProxyAPI，以清除旧进程残留的重复记录；不要通过删除账号来清理共用同一文件的条目。

插件浏览器资源按 CLIProxyAPI 设计是未认证的静态入口，因此页面不会直接暴露账户数据；读取状态、导入/导出/删除账号或保存规则时都需要 CLIProxyAPI 管理密钥。管理密钥按“主面板同源存储 → URL `?key=` → 手动输入”三级自动获取：前两者读取后写入当前标签页 `sessionStorage`，手动输入也只保存在当前标签页会话，均不写入长期浏览器存储。面板启动时若任一来源已有密钥即直接加载状态（免密直入）；无密钥时才显示输入区，密钥失效（401/403）时会重新显示输入区允许修正。

## 调用

模型名使用 `cursor/` 前缀，例如：

```sh
curl https://your-cpa.example/v1/chat/completions \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"cursor/default","messages":[{"role":"user","content":"hello"}],"stream":false}'
```

流式调用把 `stream` 改为 `true`，返回标准 SSE 数据帧和 `[DONE]`。

## 升级和卸载

再次运行新版本 `install.sh` 即可升级；旧二进制会备份到插件目录的 `.cursor-backups/`。

```sh
sudo ./uninstall.sh --plugins-dir /opt/cpa-manager-plus/cliproxyapi/plugins
```

卸载脚本不会直接删除二进制，而是移动到 `.cursor-uninstalled/`。随后在 CPA Manager Plus 中禁用 `cursor` 并重启 CLIProxyAPI。OAuth 凭据不会自动删除，避免误删账号数据。

## 能力与边界

- 0.6.1 的可选工具循环防护、检查点命中优化和增量摘要说明见[行为与验收说明](docs/cursor-tool-loop-and-cache.md)。硬停止默认关闭，仅对账户认证 JSON 中 `tool_loop_guard_tools` 精确列出的工具启用；本版本不会自动修改账户策略，也不保证消除所有重复操作。
- 0.6.3 的工具交接、结构化超时和宿主配套要求见[流式错误边界](docs/cursor-stream-failures.md)。不支持结构化错误的宿主仍可读取错误字符串，但不会自动获得修正后的冷却与重试分类；不能仅升级插件就视为问题全部解除。
- v0.5.10 的修复和升级说明见[发布说明](docs/releases/v0.5.10.md)。原生 grep/read/shell 请求会收到完整的不可执行协议回复，由客户端已公开的工具完成实际操作；网关不会执行这些原生文件读取或命令。
- 工具结果续轮会明确标记已完成的结果，帮助 Agent 继续回答，避免重复前言或再次发出已完成的工具调用。已有 OpenCode 会话中保存的重复内容不会被自动修改。
- 支持 OpenAI `chat-completions` 文本消息、非流式和 SSE 流式响应。`max_tokens` / `max_completion_tokens` 参与本地入口预算，但尚未作为 Cursor 上游输出硬限制执行；`stream_options` 等未实现字段仍不应当作已生效能力。
- 未发布的上下文防护修改在插件模型接口及管理 status JSON 中分别提供原生非 MAX 容量和 1,000,000 策略上限；未知原生容量不伪造。入口使用保守字节预算，输入 JSON 约 0.98 MB 即可能拒绝，不能等同于“允许精确 1M 原生 token”。普通执行、流式、计数和完整历史续传均受检查，详见[上下文与客户端策略](docs/cursor-context-limits.md)。
- 支持标准 function tools、`tool_choice`、多工具调用、assistant `tool_calls` 历史和 tool 结果续轮；工具目录通过 Cursor 原生 `mcp_tools` 注册，结果转换为 OpenAI 兼容 `tool_calls`。
- 支持 Cursor 在工具调用前重新查询 MCP 工具目录：仅返回当前请求已提供的工具定义和参数 schema，不启动 MCP 服务、不读取网关文件，也不代替客户端执行工具。
- 支持 `image_url` / `input_image` 内联 data URL，以及 `file` / `input_file` 的图片或 UTF-8 文本附件；图片和文件通过 Cursor 原生 `selected_context` 发送，tool 结果中的图片也能随续轮送达。
- 支持 Cursor Agent 内置图片生成工具：无头调用按 Cursor CLI 行为自动批准生成请求，成功结果以 CLIProxyAPI 兼容的 `message.images` / `delta.images` 内联 data URL 返回；模型元数据声明文本和图片输入输出能力。上游证据、协议与验收边界见[图片能力说明](docs/cursor-image-capabilities.md)。
- `message.images` / `delta.images` 是 CLIProxyAPI 的 Chat Completions 扩展而不是 OpenAI 标准字段；调用方必须显式解析它。当前 OpenBitFun/BitFun 的 OpenAI 流适配器会忽略该字段，因此插件能返回图片不等于现有 BitFun UI 已能显示图片，客户端仍需单独适配。
- 为避免服务端请求伪造，远程图片 URL 不由插件下载；调用方应传内联 data URL。仅有 `file_id` 而没有 `file_data` 的附件无法由独立插件解析。
- v0.5.9 会在 OpenAI 兼容输出边界规范化 Cursor 工具调用 ID：保持调用与结果引用一致，禁止控制字符，最大 64 字节，并在规范化冲突时生成稳定无碰撞 ID；已有合法 ID 不会被改写。
- 真正为空且不含工具调用的 assistant 历史会被过滤，成功但没有文本、图片或工具调用的 Cursor 响应会明确失败；合法的 image-only 和 tool-only assistant 响应保持不变。
- 会话检查点仅在插件进程内保存，并按账户、模型和会话严格隔离。仅追加式线性历史会尝试续传；分支、编辑、压缩、过期、重启或状态异常时会安全回退为完整重放；checkpoint 续传在尚未暴露文本、工具调用、工具结果且未回应交互请求时，如收到无输出的干净 EndStream、Connect `internal` / `failed_precondition` 错误或无有效进展的传输超时，会丢弃旧 checkpoint 并仅安全重试一次完整重放。
- 无交互 UI 的代理不能代替用户批准 Cursor 原生联网、模式切换或提问。Web Search、Exa Search/Fetch、Switch Mode、Ask Question 会收到带原始关联 ID 的明确拒绝，Create Plan 会收到协议错误结果，避免上游一直等待未应答的请求；可用客户端提供的工具完成相应操作。VM Setup 没有拒绝分支，直接返回不支持错误，不伪造成功。原有图片生成行为不变，未知协议字段保留有界诊断和超时保护。
- 检查点的上限为 15 分钟 TTL、64 条和 16 MiB；进程重启后不保留。原始检查点、凭据和管理密钥不会写入浏览器存储、宿主 metadata、日志或发布证据。
- 暂不直接实现 Responses API 或 `/v1/images/generations`；图片生成仍是 `chat-completions` 内的 Cursor Agent 工具流程，CLIProxyAPI 可按其 executor 翻译能力把其他协议转换到插件声明的输入输出格式。
- Cursor 没有公开、稳定的 OAuth 订阅剩余额度接口；管理页不会伪造百分比或余额。
- 成功/失败请求来自 CLIProxyAPI 的 `request.complete` 终态回调；同一用户请求即使发生宿主重试，最多记录一次最终结果。近期请求 ID 通过两个独立的 15 分钟 Bloom 时间窗去重，固定占用 16 MiB；在单窗不超过 100 万个完成事件（约 1,111 次/秒）的设计负载内，双窗查询的理论误判概率低于 `6e-8`。误判会跳过一条本地终态样本，使计数少计并保留此前的最近结果。宿主自身的成功/失败值按“调度尝试”单独展示。
- token usage 为每次 Cursor 插件执行的本地估算值（包括宿主重试触发的再次执行），不代表 Cursor 账单或订阅额度，进程重启后重新计数。
- 当前发布包提供 7 平台构建（darwin/linux/freebsd/windows × amd64/arm64），由 CI 在对应平台原生构建后随 Release 发布。

CLIProxyAPI 动态库插件是进程内受信代码。请只安装来自可信来源且校验过 `SHA256SUMS` 的构建。使用时应遵守 Cursor 的服务条款和可接受使用政策，不应共享账号、转售访问或规避配额与安全控制。

## 来源

Cursor 协议实现参考 [opencodex](https://github.com/lidge-jun/opencodex) 提交 `5840591322117f3ee9568b35b135a6d4339f7711`；插件 ABI 参考 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) v7.2.139 提交 `0a14eb70ce19fac1d114bcdb4a476d61adc819e2`。两者均采用 MIT 许可证，详见 `THIRD_PARTY_NOTICES.md`。
