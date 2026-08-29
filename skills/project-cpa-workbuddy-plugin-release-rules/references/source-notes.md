# Source Notes

## 2026-08-23 创建：project-cpa-workbuddy-plugin-release-rules

- **创建原因**：用户点名「把提交、push、构建、registry 等后续一整套发布的流程吸收到项目的 skill」
- **来源**：cpa-workbuddy-plugin 0.12.0 / 0.12.1 / 0.12.2 三次完整发布实操（对话 + .workbuddy/memory/2026-08-22.md + 2026-08-23.md）
- **覆盖**：版本 bump 三处、混合文件分离发布、HTTPS+askpass push、dispatch CI、下载 assets、publish registry、远端验证、12 条实测踩坑
- **关联 skill**：cgo-plugin-isolated-test（本地验证职责，本 skill 引用不重复）；luode-skills 仓库 `project-interface-release-execution-rules` 是接口测试门禁（不同职责域，无重叠）
- **边界**：只管发布执行链路；代码正确性验证、需求/编码规则在其他 skill

## 2026-08-29 更新：补齐 0.14.16 / 0.9.6 / 0.1.9 三插件发布实战新铁律

- **更新原因**：2026-08-29 晚间会话完成 workbuddy-provider 0.14.16（失败筛选功能）/ qoderwork-provider 0.9.6 / traework-provider 0.1.9 三插件并行发布闭环，暴露多条 SKILL.md 未覆盖的坑
- **来源**：.workbuddy/memory/2026-08-29.md + MEMORY.md 铁律 + 会话实操
- **主要变更**：
  - push 由 HTTPS+askpass 改为 SSH 直推（origin 是 SSH remote，Step 7 重写；askpass 降级为遗留回退）
  - dispatch 铁律：inputs 必须同时含 plugin（provider id）+ version，缺 version → Release job skipped（不建 tag/Release，assets 下载 404）；plugin 传源码目录名 → 422
  - CI 轮询上限 11 分钟 → 15 分钟（deadline 900s，实测出现过 12 分钟 run）
  - 新增 Step 13 生产服务器部署：POST /v0/management/plugin-store/<provider-id>/install?version=<x.y.z>（docker cp 与 PUT config 均不触发热重载）
  - 下载脚本已参数化 <provider-id> <version>，item 14 标注已修复
  - qoderwork 状态更新为已全量对齐 workbuddy（2026-08-28）
  - 踩坑清单追加 21-27：TLS 备用通道、authFilePrefix 铁律（traework 面板「暂无账号」第三层根因）、多会话共享工作树 fetch 对齐
  - 插件清单更新为四插件 + 当前版本带（workbuddy 0.14.x / qoderwork 0.9.x / traework 0.1.x / token-usage 0.2.x）
- **验证**：全文回读无过时残留；版本示例统一为 0.14.16
