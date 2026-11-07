# Changelog

本文件记录 `safeguard-killer` 插件的版本变更。格式按版本号倒序，每条包含背景、改动、验证与涉及文件。

## 0.1.2

### Feat - 管理面板全量汉化（简体中文）

- 背景: 生产面板 `https://cpa.luode.vip/management.html#/plugin-pages/safeguard-killer/0` 全英文，用户反馈「英文的看不懂」，需要简体中文界面。
- 改动: `safeguard-killer/ui.go` 的 `uiHTML` 常量内所有面向用户的可见文案改为简体中文——页面标题与描述、状态胶囊（版本/凭据/分组/会话/最近更新）、工具栏（刷新/放弃/保存/未保存改动）、5 个标签页、两张设置卡片（Router & Quota Settings 4 个字段的标签与说明、Safeguards & Client Adaptations 8 个开关标题与说明、客户端提示前缀）、智能分组编辑器（代号/可见名/路由策略/上移下移移除/添加模型/删除分组/三条策略提示）、校验错误、保存横幅、相对时间与倒计时、配额窗口、凭据表、会话表、决策表与排序表表头、错误横幅、页面异常提示。
- 新增展示层翻译函数（只翻译渲染，不改后端取值与判断）: `REASON_TEXT` 映射表 + `zhReason` / `zhAttempt` / `zhExcluded` / `zhKind` / `zhMgmtError`，把后端返回的英文 reason、attempt result、排除原因、决策类型、配额采集错误在渲染时翻成中文。
- 不动的部分（刻意保留原样）: 后端日志与判据字段（`served` / `skipped: ` / `failed: ` / "%s has %.0f%% left"）以及测试断言依赖的字符串；注册菜单名 `Menu: "Safeguard Killer"` 与路由路径；`STATE_API_URL` / `CONFIG_API_URL` 两个接口常量；技术标识与技术名词（Claude Code / OpenAI/Codex / Gemini / Fable/Mythos / x-cc-* 头名 / safeguards / not_flagged 等）；默认提示前缀 `⚡ [Safeguard Killer] `；`managementRegistrationPayload` 的 Route/Resource Description 一并改为中文说明，但 `Menu` 保持英文（`ui_test.go:15` 断言）。
- 顺带修复: 汉化过程中 `showError('网络错误，请求 ' + STATE_API_URL + ' 失败', String(err))` 曾漏掉一个闭合引号导致内嵌 JS 语法错误，已在 `ui.go` 修正。
- 验证: 提取 `uiHTML` 内嵌 `<script>`（35923 字节）跑 `node --check` 退出码 0；确认 `uiHTML` 内反引号仅首尾各 1 个，Go raw string 未被破坏；`python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿（`ok github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`）；HTML 段残留英文仅技术名词。
- 涉及文件: `safeguard-killer/ui.go`、`safeguard-killer/VERSION`、`safeguard-killer/main.go`、`safeguard-killer/CHANGELOG.md`。

## 0.1.1

### Fix - 把插件声明的 schema 版本从 9 降到宿主实际支持的 6，修复生产注册失败

- 背景: 0.1.0 vendor 进来的 `schemaVersion` 沿用了上游的 9（上游对着 latest 主线开发）。生产宿主 `cli-proxy-api` 的 commit 是 `7417cfb1bbfb794417764f8cd974a637a0779ddd`，其 `sdk/pluginabi.SchemaVersion = 6`，`internal/pluginhost/rpc_client.go:56-68` 的 `registerRPCPlugin` 在拿到注册包后执行 `if resp.SchemaVersion > pluginabi.SchemaVersion { return ... fmt.Errorf("plugin schema version %d is not supported", resp.SchemaVersion) }` —— 声明 9 会被直接拒绝，插件整体注册不上。
- 改动: `safeguard-killer/main.go` 的 `schemaVersion` 常量由 `9` 改为 `6`，并在常量上方补注释说明「必须 <= 宿主 sdk/pluginabi.SchemaVersion」的原因，避免后续再被上游版本号带偏。
- 相容性取证: 全仓库 grep `Registrations` / `omit_body` / `patch_body` / `body_patcher` / `request_body` / `HistoryChunks` 命中数均为 0 —— 插件没有用到 v7（拦截器省略 body）、v8（原始 body + 字节区间 patch）、v9（ModelRegistrar 多 provider Registrations）、v10（多 OAuth 标识）任何新特性。注册包只用 `Metadata` + `Capabilities`，拦截器只用 `Headers` / `Body` / `ClearHeaders` / `Terminate` / `StatusCode` / `ResponseHeaders` / `ResponseBody`，流式拦截只用 `Body` / `DropChunk`，模型注册只产出单 provider `ModelRegistrationResponse{Provider, Models}` —— 全部落在 v6 契约内。且 `configurePlugin` 不按 `schema_version` 分支，宿主传 6 或 9 行为完全一致，降版不改变任何业务逻辑。
- 附带收益: v6 起宿主对 management 响应不再做 HTML 实体转义（`ManagementResponse` 的注释明确 `On schema_version >= 6, JSON bodies are returned without HTML entity escaping`），面板与 `/v0/management/plugins/safeguard-killer/*` 的 URL 类字段可以直接使用，无需前端反转义。
- 验证: 新增 `safeguard-killer/config_test.go` 的 `TestPluginRegistrationSchemaVersionWithinHostContract` —— 断言注册包 schema 与常量一致且不超过宿主上限 6；用临时哨兵 Test 确认该文件真实进入编译链路（shim 输出 FAIL）后删除哨兵。`python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿（`ok github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`）。
- 涉及文件: `safeguard-killer/main.go`、`safeguard-killer/config_test.go`、`safeguard-killer/VERSION`、`safeguard-killer/CHANGELOG.md`。

## 0.1.0

### Feat - 首次从上游 vendor 并纳入本仓库发布链路

- 背景: 需要一套能在本仓库自行构建、自行发布、可长期维护的「拒答救援 + Smart Groups 路由」能力。上游 `esauvisky/CLIProxyAPI-Safeguard-Killer-Plugin` v0.5.10 已具备完整能力（拒答检测与透明换模型、Claude Code 拒答污染头剥离、auto-mode 分类器旁路、配额加权凭证平衡、5 分钟 prompt-cache 粘性），因此原样 vendor 进本仓库，只改归属与版本号，不改业务逻辑。
- 归属改造: `pluginName` 保持 `Safeguard Killer`；`pluginAuthor` 改为 `luode0320`；`pluginRepo` / `pluginLogo` 指向本仓库；`pluginVersion` 由常量改为 `var pluginVersion = version`，版本号交由 CI 以 `-ldflags -X main.version` 注入，与本仓库其余插件一致。plugin id 仍为 `safeguard-killer`，因此管理路由 `/v0/management/plugins/safeguard-killer/state` 与 `CONFIG_API_URL` 无需改名。
- 模块改造: `go.mod` module 改为 `github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`，`go 1.26` → `go 1.26.0` 对齐仓库其余插件；依赖不变（`github.com/tiktoken-go/tokenizer v0.8.1`、`gopkg.in/yaml.v3 v3.0.1`、间接 `github.com/dlclark/regexp2/v2 v2.5.1`）。插件不依赖 CLIProxyAPI SDK，纯 ABI v1，因此与本仓库任何插件并存且不受宿主 SDK 版本约束。
- 发布链路: `build.yml` 增加 tag 模式 `safeguard-killer-v*`、dispatch choice、test matrix、`PLUGINS` 字典、release job 的 tag 判定与 `tag → plugin` 分支、`DISPLAY` 显示名；`registry.json` 增加 6 平台（darwin amd64/arm64、linux amd64/arm64、windows amd64/arm64）direct 安装条目。
- 本地验证工具修复: `scripts/cgo-shim-build.py` 原先只会替换标准 ABI 符号，遇到本插件的三个 C 静态封装（`store_host_api` / `call_host_api` / `free_host_buffer`）会编译失败。已补三个 shim 桩并把 `C.free_host_buffer` 的替换规则排到 `C.free` 之前（前缀规则会先把 `C.free_host_buffer` 吃成 `cgoFree_host_buffer`）。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿（`ok github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`）；`python scripts/validate-registry.py registry.json` 返回 `OK registry.json: 12 plugin(s), schema_version=2`。
- 涉及文件: `safeguard-killer/`（16 个 `.go` 含 7 个 `_test.go`、`go.mod`、`go.sum`、`LICENSE`、`Makefile`、`README.md`、`VERSION`、`tests/abi_test.c`）、`assets/icons/SafeguardKiller.png`、`.github/workflows/build.yml`、`registry.json`、`scripts/cgo-shim-build.py`。
