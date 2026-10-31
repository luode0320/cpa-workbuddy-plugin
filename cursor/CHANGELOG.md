# Cursor Provider Plugin Changelog

## 0.1.7

### Fix - 兼容宿主主面板 enc::v2:: 密钥格式（修复面板 401）

- 背景: 宿主主面板 `management.html` 于 2026-10-31 自动更新后，把 `cli-proxy-auth` 的密钥混淆格式从 `enc::v1::` 升级为 `enc::v2::`，XOR key 由 `salt|host|userAgent` 改为 `salt|host`。
- 问题: 插件面板 `deobfuscate()` 只识别 `enc::v1::`，遇到 v2 前缀时原样返回密文，`JSON.parse` 失败导致取不到管理密钥。若 sessionStorage 里还残留旧流程写入的密文，面板会持续用错误 Bearer 请求管理接口，停在「management key 无效或缺失（将自动重试）」并无法自愈。
- 修复: 新增 `ENC_PREFIX_V2` 与带版本参数的 `_keyBytes(v2)`；`deobfuscate()` 先按 v2 解、再回退 v1，兼容升级前写入的旧值；新增 `readSessionKey()`，读取 sessionStorage 时若命中 `enc::` 密文前缀即判定为脏值并清除，让 `getKey()` 回落到 localStorage / URL / 手动输入。
- 影响范围: 仅前端 `panel.html`，后端零改动；账号数据、路由与鉴权语义不变。
- 验证: 复刻宿主真实算法构造 v1 / v2 双格式密文，10 个插件面板在「干净 v2」「sessionStorage 残留 v2」「旧版 v1」三种场景下均解出正确密钥；`node new Function` 语法校验通过；`cgo-shim-build.py` build/vet/test 全绿。

## 0.1.6

### Fix - 支持向 CPA 宿主返回全局模型列表（executor_model_scope 升级为 both，补齐 model.static 动态与保底发现）

- 变更要点:
  1. 根因修复：`registration` 中声明的 `executor_model_scope` 原为 `"oauth"`，导致 CPA 宿主启动与重配时判定 `executorScopeAllowsStaticModels` 为假并直接跳过该插件的静态模型加载，造成管理台请求 `/v8/management/routing/model-definitions/cursor-provider` 报 400 提示“无法获取模型列表”。现将作用域升级为 `"both"`，打通宿主全局模型目录挂载链路。
  2. 补齐 `model.static` 统一处理：新增 `Handler.staticModels`，优先通过首个可用凭据探测上游最新可用模型，无凭据或网络异常时优雅回退到主流常用模型列表，保证静态管理台下拉秒开与模型选取。
  3. 扩充常用主流模型列表：在 `defaultCursorModels` 中补充 `claude-3-7-sonnet`、`claude-3-7-sonnet-thought`、`claude-3-5-haiku`、`claude-3-opus`、`o3-mini`、`o1`、`deepseek-r1`、`gemini-2.5-pro`、`gemini-2.5-flash` 等官方主流模型。
  4. 单元测试与验证覆盖：在 `handler_registration_test.go` 中断言 `executor_model_scope: both`，在 `models_test.go` 中补充 `Test_Handler_static_models_discovers_models_from_available_credential` 覆盖，并通过必失败哨兵反证测试。
- 验证: `python scripts/cgo-shim-build.py cursor` build/vet/test 全绿；必失败哨兵反证拦截通过。
- 涉及文件: cursor/VERSION、cursor/main.go、cursor/CHANGELOG.md、cursor/internal/plugin/handler.go、cursor/internal/plugin/models.go、cursor/internal/plugin/handler_registration_test.go、cursor/internal/plugin/models_test.go

## 0.1.5

### Perf - 测试探活（/test-active）改为流式首包即停，消除思考模型整段思维链等待

- 变更要点:
  1. 根因：`handleTestActiveWithAuth` 在调用 `handler.cursor.Run` 时，回调一直返回 `nil` 等待整条 Connect-RPC 响应流完全结束；测试 `claude-3-7-sonnet-thinking`、`deepseek-r1`、`gemini-2.5-pro` 等思考模型时需等待完整思维链与正文生成完毕。
  2. 优化：引入 `errCursorTestActiveFirstChunkDone` 首包即停机制，一旦收到首个非空 `EventText`、`EventThinking`、`EventToolCall` 或 `EventDone` 立即终止流读取并返回成功，实现 TTFT 级快速探活响应。
- 涉及文件: cursor/internal/plugin/management_test_active.go、cursor/VERSION、cursor/main.go、cursor/CHANGELOG.md

## 0.1.4

### Fix - 修复测试弹窗模型 Query 参数解析、DiscoverModels 真实模型展现与点号模型别名归一化（彻底修复上游 not_found）

- 变更要点:
  1. 修复管理路由 Query 参数解析与模型发现穿透：修正 `management.go` 中 `managementRequest` 结构体，将原本标记 `json:"-"` 的 `Query` 字段正确声明为 `Query map[string][]string json:"Query"`，并对齐宿主 RPC `pluginapi.ManagementRequest` 字段契约；重构 `parseAuthIndexFromRequest` 优先解析 Query 参数并向下兼容 Path query string；当未传 auth_index 时通过 `getFirstAvailableCredential` 自动命中首个可用账号发现模型，彻底解决管理接口 `/models` 永远回退到硬编码 `defaultCursorModels` 导致账号发现的 256 个真实模型被完全屏蔽的问题。
  2. 建立模型别名归一化机制（根治上游 `not_found`）：新增 `openai.CanonicalModel`，将客户端或测试端传入的带点号版本名（如 `claude-3.5-sonnet`、`claude-3.5-haiku`、`claude-3.7-sonnet`、`gpt-4.5`）自动规范化映射为 Cursor 上游原生权威连字符模型 ID（如 `claude-3-5-sonnet`、`claude-3-5-haiku`、`claude-3-7-sonnet`、`gpt-4.5-preview`）；在 `handleTestActiveWithAuth` 与 `openai.ParseChatRequest` 中统一接入，彻底解决测试探活及实际转发请求时上游 Connect-RPC 报错 `Cursor stream failed: not_found` 的缺陷。
  3. 优化兜底模型列表与默认推荐：更新 `defaultCursorModels` 剔除失效与重复的点号模型，补齐官方权威基准模型 `default`、`claude-3-5-sonnet` 与 `gpt-4.5-preview`；优化 `pickDefaultCursorModel` 优先推荐 `default`/`auto`。
  4. 面板测试弹窗搜索过滤与秒开交互：在 `assets/management.html` 的测试弹窗中增加实时搜索过滤输入框，支持在 256 个海量模型中毫秒级模糊搜索（如输入 `claude`、`gpt`、`default`）；打开弹窗时优先使用当前账号已有缓存立即秒开渲染，并在后台异步校验刷新最新模型列表。
  5. 自动化测试与必失败反证全绿：在 `management_test.go` 中新增宿主线协议标准 Query 参数传递与专属发现模型断言、点号模型别名测试探活断言；在 `model_identity_test.go` 中新增别名归一化单元测试；并通过必失败哨兵反证测试。
- 验证: `python scripts/cgo-shim-build.py cursor` build/vet/test 全绿；必失败哨兵反证测试通过；`node test/cursor/panel_auto_entry_repro.mjs` 全部 100% PASS。
- 涉及文件: cursor/VERSION、cursor/main.go、cursor/CHANGELOG.md、cursor/internal/plugin/management.go、cursor/internal/plugin/management_test_active.go、cursor/internal/plugin/models.go、cursor/internal/openai/request.go、cursor/internal/openai/model_identity_test.go、cursor/internal/plugin/management_test.go、cursor/internal/plugin/assets/management.html

## 0.1.3

### Fix - 消除侧边栏双菜单重复注册，全量对齐 WorkBuddy / ZCode 现代面板设计风格

- 变更要点:
  1. 消除侧边栏双菜单重复：收敛 `management.go` 中的 `managementRegistration().Resources` 为仅声明 `/panel` 单一菜单项，去除原本作为兼容别名重复暴露的 `/status` 资源，彻底消除宿主侧边栏出现两个并列 "Cursor" 菜单的现象；保持后端路由对 `/status`、`/enable-all`、`/disable-all` 的完整分发支持。
  2. 面板全量对齐 WorkBuddy 风格体系：完全重构 `assets/management.html`，接入 CPA 官方 `:root` / `white` / `dark` CSS 主题变量体系（--bg:#faf9f5、--card:#ffffff、--acc:#4c8dff 等）；提供全屏 `.wrap` 规范、操作工具栏（导入、导出、全部启用、全部禁用、实时搜索、排序）、状态过滤标签（全部/可用/禁用/异常）与 `.summary` 统计概览卡片。
  3. 卡片网格与交互组件升级：响应式 `.grid` 与 `.card` 设计，包含状态 Badge、UID/AuthID、累计调用成功/失败统计、核心模型标签列表、选模型实时推理测试弹窗、账号启用/禁用切换与右上角红叉删除二次确认；接入右上角平滑滑入的 Toast 状态通知。
  4. 自动化测试与免密直入全绿：更新 `handler_registration_test.go` 与 `management_test.go` 单测；执行 `node test/cursor/panel_auto_entry_repro.mjs` 免密直入回归场景全断言通过；内嵌两段脚本 `node --check` 语法校验通过。
- 验证: `python scripts/cgo-shim-build.py cursor` build/vet/test 全绿；必失败哨兵反证测试通过；`node test/cursor/panel_auto_entry_repro.mjs` 全部 100% PASS。
- 涉及文件: cursor/VERSION、cursor/main.go、cursor/CHANGELOG.md、cursor/internal/plugin/management.go、cursor/internal/plugin/assets/management.html、cursor/internal/plugin/handler_registration_test.go、cursor/internal/plugin/management_test.go

## 0.1.2

### Feat - 对齐 WorkBuddy 核心能力：统一用量上报、JSON 备份凭据恢复与面板体验增强

- 变更要点:
  1. 接入统一用量数据通道：新增 `usage_feed.go`，在流式与非流式请求完成时异步追加 NDJSON 至 `<root>/data/token-usage-feed.ndjson`；在 `token-usage-tracker` 补充 Cursor 供应商映射，打通与 TokenTracker 统一看板的用量监控与图表展示。
  2. 支持 JSON 备份批量恢复：增强 `import.go`，支持从导出的完整备份 JSON 文件（或单个/数组凭据 JSON）一键恢复账号凭据，与导出功能形成闭环。
  3. 面板体验全面增强：面板顶部新增账号概览统计栏（账号总数、活跃数、已禁用数、覆盖模型数），操作栏新增账号实时模糊搜索过滤输入框，导入弹窗新增「从备份恢复」独立区域与操作按钮。
  4. 自动化测试闭环：新增 `usage_feed_test.go` 验证用量 NDJSON 生成与写入，在 `import_operations_test.go` 新增从备份 JSON 恢复账号凭据的完整测试断言，并通过必失败哨兵反证。
- 验证: `python scripts/cgo-shim-build.py cursor` 与 `token-usage-tracker` 全部 build/vet/test 全绿；`node test/cursor/panel_auto_entry_repro.mjs` 全断言通过；内联两个 script 块 `node --check` 语法检查通过。
- 涉及文件: cursor/internal/plugin/usage_feed.go、cursor/internal/plugin/usage_feed_test.go、cursor/internal/plugin/executor.go、cursor/internal/plugin/import.go、cursor/internal/plugin/import_operations_test.go、cursor/internal/plugin/assets/management.html、token-usage-tracker/usage_stats/auth_identity.go、cursor/CHANGELOG.md、cursor/VERSION、cursor/main.go

## 0.1.1

### Feat - 官方高清图标补齐、面板路由对齐、指定模型测试探活与可用性完善

- 变更要点:
  1. 补充官方高清图标：入库 `assets/icons/Cursor.png`（640×640 32-bit RGBA），同步更新 `handler.go` 的 `pluginLogoURL`、`registry.json` 的 `logo` 字段以及面板顶部品牌图标，消除破图与空白。
  2. 面板路由规范对齐：资源路由补齐标准 `/panel`（同时兼容别名 `/status`），彻底解决 CPA 宿主管理后台点击进入面板报 404 Not Found 的问题。
  3. 增加标准账号接口：管理接口支持 `GET /plugins/cursor-provider/accounts` 别名，与全仓库其它插件接口规范对齐。
  4. 支持模型查询与测试探活：新增 `GET /models?auth_index=` 与 `POST /test-active` 路由，面板卡片新增「测试」按钮与 `testModal` 弹窗交互，支持按指定模型实时测试 Cursor 账号连通性、验证 Token 并在 401 时自动刷新凭据重试。
  5. 模型发现优雅降级：新增 `defaultCursorModels`（auto、cursor-fast、composer-2.5、claude-3.5-sonnet、gpt-4o 等），当上游 GetUsableModels 动态拉取失败时自动优雅降级，防止整个账号状态被置为 unavailable 及模型列表为空。
  6. 面板免密直入与主题同步：管理密钥按“主面板同源存储 → URL 参数 → 会话存储”三级自动获取，已有密钥时直接加载状态，密钥输入区默认隐藏；移除冗余额度说明区块；嵌入宿主时自动镜像 `data-theme`。
- 验证: `python scripts/cgo-shim-build.py cursor` build/vet/test 全绿；必失败哨兵反证证实单测真实生效；`node test/cursor/panel_auto_entry_repro.mjs` 全断言通过；`python scripts/validate-registry.py` 通过。
- 涉及文件: assets/icons/Cursor.png、cursor/internal/plugin/handler.go、cursor/internal/plugin/management.go、cursor/internal/plugin/management_test_active.go、cursor/internal/plugin/models.go、cursor/internal/plugin/assets/management.html、cursor/internal/plugin/management_test.go、cursor/internal/plugin/handler_registration_test.go、cursor/internal/plugin/models_test.go、cursor/CHANGELOG.md、cursor/VERSION、cursor/main.go、registry.json

## 0.1.0

### Feat - 上游 Cursor 插件移植与 Token 导入

- 变更要点:
  1. 从 yobo2u/omsub cursor-plugin（上游 0.6.6）全量移植为独立插件 cursor-provider：Connect-RPC agent 服务客户端、上下文准入、会话排队与 Blob 预算、输出截断、模型发现可用性保护、检查点与会话连续性、图片输入与工具循环。
  2. 新增会话 Token 导入：支持 `user_<id>::<jwt>`、URL 编码 `%3A%3A`、`WorkosCursorSessionToken=` Cookie 形态与裸 JWT；统一取 `::` 后 JWT 片段，经标准 OAuth 令牌端点兑换 access_token，落盘 `cursor-<hash8>.json`，按 account_id / email 去重。
  3. 新增管理路由：导入 / 导出 / 删除 / 启用 / 禁用 / 模型禁用（`/v0/management/plugins/cursor-provider/*`），管理面板与 workbuddy 风格统一。
  4. 修正令牌兑换链路：旧交换端点已失效（401），refresh 改走标准 OAuth 令牌端点 `https://api2.cursor.sh/oauth/token`。
  5. CI 接入 7 平台构建（darwin/linux/freebsd/windows × amd64/arm64），registry.json 新增 cursor-provider 条目。
- 验证: `python scripts/cgo-shim-build.py cursor` build/vet/test 全绿；面板 vm + DOM 桩验证通过；`validate-registry.py` 通过。
- 涉及文件: cursor/、.github/workflows/build.yml、registry.json
