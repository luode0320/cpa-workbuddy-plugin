# Source Notes

## 2026-10-07 创建：project-cpa-workbuddy-plugin-release-asset-prune-rules

- **创建原因**：用户要求"这个规则要吸收到项目 skill 和规则中，每次发布后都需要清理不需要的垃圾"。
- **来源**：2026-10-07 实操——`release-assets` 累积 3.75 GB，删除 185 个历史版本目录 / 1469 个文件，保留 registry 当前 6 个版本目录，降到 0.13 GB；提交 `095f4a4` 并推送 `origin/main`。
- **覆盖**：保留集判定（registry 每插件 `<id>-<version>`）、`git rm` 删除流程、本地未跟踪缓存 `.workbuddy/release-assets`、远端 200/404 抽查、释放体积记录。
- **关联 skill**：`project-cpa-workbuddy-plugin-release-rules`（发布主链路，本 skill 只补"发布后清理"）；`cgo-plugin-isolated-test`（本地验证，不涉及）。
- **边界**：只管发布后的资产/缓存清理；发布执行、代码验证、需求/编码规则在其他 skill。
- **同域冗余扫描**：范围=本 skill + release-rules + trae-local-verify-rules。发现 0 处逐字重复、0 处门控层叠、0 处散落产物；release-rules 的 Step 13 收尾清理是"发布链内临时文件（/tmp、shim）"，本 skill 是"仓库 release-assets 资产 + 本地缓存"，职责互补不重叠。PASS。
- **环境依赖登记**：无环境变量/宿主配置依赖（仅依赖仓库内 `registry.json` 与 `scripts/prune-release-assets.py`）。
- **凭据纪律**：无凭据写入。
- **字典刷新**：本项目 `skills/` 无 data.js / 字典.md（skill 字典机制属 luode-skills 仓库，跨项目只读），不适用。
