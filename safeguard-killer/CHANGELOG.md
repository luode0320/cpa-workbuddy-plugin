# Changelog

本文件记录 `safeguard-killer` 插件的版本变更。格式按版本号倒序，每条包含背景、改动、验证与涉及文件。

## 0.1.5

### Feat - 用成员模型的真实名称直接进入智能分组，无需客户端改配置

- 背景: 用户配置了一个分组 `id: "1"`、成员是 11 个真实模型名（`DeepSeek-V4-Pro` / `GLM-5.3` / `Doubao-Seed-2.1-Pro` 等），但面板「最近决策」恒为 0 —— 客户端请求的是真实模型名，而旧实现只把分组的 `id`（`"1"`）当作入口，成员名一律不认领。结果是该分组从未被执行器接管，`fallback_on_refusal`（拒答自动换模型）这条最关键的破甲能力一次都没跑过，只有全请求级的四个开关在工作。
- 改动: `groups.go` 新增 `pluginConfig.groupServing`（按成员模型名反查所属分组）与 `rotateModels`（把链路旋转到被请求的模型开头、其余按配置顺序环绕）；`resolveTargetGroup` 在 `id` 未命中后增加成员名命中分支，命中即返回旋转后的成员链。
- 语义: 请求某个成员模型时，先按客户端要求的那个模型执行（保持客户端预期与 prompt 缓存），只有它拒答/失败/配额耗尽才滚到分组里的下一个。分组 `id` 入口与 Claude Code `claude-opus-4-8` 兜底入口的行为保持不变。
- 不引入递归: 分组内部经 `host.model.execute` 下发成员真实模型名，宿主 `modelExecutionRequestFromPlugin` 会把发起插件 id 同时写入 `SkipRouterPluginID`，`applyModelRouter` 据此跳过该插件自身（宿主侧有 `was re-entered despite SkipRouterPluginID` 断言守护），因此不会二次接管。
- 面板: 「智能分组」说明文案补充「也可以用成员模型的真实名称直接进入」。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿；新增 `TestRouteGroupClaimsRealModelNames`、`TestResolveTargetGroupRotatesToRequestedModel`、`TestRotateModels`、`TestProductionStyleGroupEntersByMemberModel`（按生产配置同形数据构造）；哨兵测试 `TestSentinelGroupsZ` 确认新测试真实进入编译链路后删除；两组变异（禁用 `groupServing` 命中、`rotateModels` 恒返回原序）各被 3 条用例捕获，还原后复跑全绿。
- 涉及文件: `safeguard-killer/groups.go`、`safeguard-killer/groups_test.go`、`safeguard-killer/ui.go`、`safeguard-killer/VERSION`、`safeguard-killer/main.go`、`safeguard-killer/CHANGELOG.md`。

## 0.1.4

### Fix - 宿主缺少 host.auth.models 回调时降级运行，不再整体失效并输出红色致命横幅

- 背景: 生产宿主 commit 7417cfb（BuiltAt 2026-10-15）早于上游 2026-10-21 才引入的 `host.auth.models` 回调，插件查询各凭据模型列表时得到 `unsupported host callback host.auth.models`。旧实现把该失败当成致命错误直接上报，导致整份 host snapshot 作废：面板红横幅「路由组件读取宿主凭据列表失败」、凭据与模型列表全空。又因 `skipReason` 用 `containsString(snapshot.models[auth.ID], model)` 过滤，模型列表为空时会把所有凭据全部跳过，模型维度的分组路由实际不可用。
- 改动: `balance.go` 的 `hostSnapshot` 新增 `modelsErr` 承载降级原因，`hostState` 只在 `host.auth.list` 失败时返回错误，模型列表失败改为返回空表继续；新增 `hostModelsUnsupported` 闩（首次发现宿主不支持即记住，避免每个 15s 缓存窗口重复打回调）、`hostCredentialModels()`、`mergedModels()`（宿主优先、管理 API 兜底补空缺）、`servesKnownModel()`（模型列表为空的凭据视为可服务任意模型）。
- 改动: `quota.go` 新增 `ensureFallbackModels` / `fallbackModels` / `credentialModelsCall`，用既有的 `GET /v0/management/auth-files/models?name=` 异步（10 分钟 TTL、4 并发、单飞）补齐模型列表；空结果不覆盖旧表，避免把「该 provider 由代码静态定义模型」误判成「不服务任何模型」。
- 改动: `groups.go` 的 `skipReason` 与 `refusal.go` 的 `orderByQuotaWeightedRandom` 改用 `mergedModels` + `servesKnownModel`；`types.go` 的 `uiState` 新增 `models_error`；`ui.go` 新增黄色 warn 横幅「模型列表来源降级」（`zhModelsError`），删掉 `zhHostError` 里把该情形说成致命的旧分支，凭据表在模型列表未知时标注「按可服务任意模型处理」。
- 不动的部分: `host.auth.list` 失败仍是致命错误并显示红色横幅；后端 `skipped: ` / `failed: ` / `%s has %.0f%% left` 等被测试断言的字符串、`Menu: "Safeguard Killer"`、两个接口常量、默认提示前缀全部保持原样。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿；新增三个用例并单独 `-v` 复跑通过——`TestHostStateDegradesWithoutHostAuthModels`（models 回调报 unsupported 时 hostState 不报错、记录 modelsErr、闩置位）、`TestSkipReasonTreatsUnknownModelListAsUsable`（模型未知时不被跳过）、`TestBuildUIStateUsesFallbackModelLists`（管理 API 兜底列表进入 credentials 与 models，`state.error` 为空、`models_error` 有值）；重新提取 `uiHTML` 内嵌 `<script>`（36905 字节）跑 `node --check` 退出码 0。
- 涉及文件: `safeguard-killer/balance.go`、`safeguard-killer/quota.go`、`safeguard-killer/groups.go`、`safeguard-killer/refusal.go`、`safeguard-killer/types.go`、`safeguard-killer/ui.go`、`safeguard-killer/balance_test.go`、`safeguard-killer/VERSION`、`safeguard-killer/main.go`、`safeguard-killer/CHANGELOG.md`。

## 0.1.3

### Fix - 补齐 0.1.2 遗漏的三处英文残留（凭据状态、宿主回调错误、配额窗口重置前缀）

- 背景: 0.1.2 已把面板主体汉化，但用真实 DTO 形态驱动渲染函数做残留扫描时仍发现三处英文：配额窗口行的 `resets` 前缀、凭据状态徽标直接输出宿主返回的英文 status（`unavailable` 等）、宿主回调错误横幅直接输出 `host callback host.auth.models: unsupported ...` 原文。
- 改动: `safeguard-killer/ui.go` 的 `renderWindow` 把 `resets ` 前缀改为 `重置 `；新增 `STATUS_TEXT` 映射表与 `zhStatus(text)`，把宿主凭据状态值（active/ready/disabled/unavailable/expired/invalid/error/pending/refreshing/cooling）翻成中文，未命中的状态原样透出以便排查；新增 `zhHostError(text)`，对 `host.auth.models` / `host.auth.list` 未实现的回调错误给出中文成因说明并附原文，其余错误原样透出。
- 不动的部分: 后端 `hostState` / `buildUIState` 的取值与判据、宿主原始错误串、`ui_test.go` 断言依赖的 `Menu`、路由路径与两个接口常量；`resets` 只在展示层替换，不涉及任何后端字段名。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿；重新提取 `uiHTML` 内嵌 `<script>`（36905 字节）跑 `node --check` 退出码 0；用 vm + DOM stub 以真实 `uiState` DTO 形态（`credentials[].auth` / `has_quota` / `quota.windows[].remaining`、`decisions[].ranking` / `attempts`）驱动 `render` / `renderGroups` / `renderCredentials` / `renderSessions` / `renderDecisions`，输出的 7614 字节 HTML 中纯英文文本节点仅剩 15 项，全部是凭据名、供应商名、模型 ID、配额来源标识与 `uarr`/`darr`/`times` 三个 HTML 实体符号，无遗漏的可译文案。
- 涉及文件: `safeguard-killer/ui.go`、`safeguard-killer/VERSION`、`safeguard-killer/main.go`、`safeguard-killer/CHANGELOG.md`。

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
