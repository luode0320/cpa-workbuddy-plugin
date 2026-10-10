# Cursor Provider Plugin Changelog

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
