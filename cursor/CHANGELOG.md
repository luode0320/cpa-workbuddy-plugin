# Cursor Provider Plugin Changelog

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
