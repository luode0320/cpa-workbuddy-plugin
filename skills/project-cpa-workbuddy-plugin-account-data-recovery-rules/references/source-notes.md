# 来源记录

## 2026-10-25 · 生产 traework-provider 17 个账号误删后 binlog 全量恢复

- **通道**：项目本地 skill 主动沉淀（用户点名「吸收本次恢复的经验到项目skill中」）
- **新增 skill**：`project-cpa-workbuddy-plugin-account-data-recovery-rules`
- **来源事实**：生产 CPA 的 traework-provider 账号被误删（binlog 定位到 17 条 DELETE 事件，三批：10-20 15:34 / 10-21 15:11 / 10-22 12:58~12:59），最终从 `binlog.000017` 完整恢复 17/17，其中 16 个经上游刷新实测可用并写回生产，插件启用后通过 `/accounts`、宿主 `auth-files`、`test-active`、`/v1/chat/completions` 四层验收。
- **分层判定**：
  - **项目级（本 skill）**：三层存储的具体路径（`cli_proxy_api.auth_store` / `/usr/local/src/cli-proxy-api/mysqlstore/mysqlstore/auths/` / `~/.antigravity_cockpit/`）、`auth-dir` 空目录陷阱、traework 无独立本地目录、traework 刷新端点与真实 ClientID、插件停用态掩盖恢复结果、写回 SQL 与启用命令。
  - **通用层（知识库）**：binlog 可恢复性四步判定、recovery point 三层、纯 Python 直解 binlog 的头部事实、MySQL 二进制 JSON 列三个偏移陷阱、自证方法。落在 `D:\谷歌云盘\知识库\20-Knowledge\工程实践\MySQL-binlog恢复被误删行数据-二进制JSON与偏移基准.md`，已挂载进「数据库与存储工程」MOC。
- **查重结论**：现有 5 个项目 skill（host-config-safety / model-specs / release / sse-and-ping / trae-local-verify）均不覆盖账号数据恢复；本 skill 与 `host-config-safety` 的边界是「账号数据层 vs 配置数据层」，已在双方适用范围内写明互引。
- **整理与去重**：通用解析细节不在本 skill 重复正文，改为一句摘要 + 指向知识库笔记；刷新区块只保留本项目真实端点与误判点，不复述 Trae 上游完整协议（那归 `trae-local-verify-rules`）。
- **未登记的环境依赖**：本 skill 依赖生产 SSH 别名 `cpa-server`、容器名 `mysql` / `cli-proxy-api`，均已在正文命令中显式写出（项目内固定事实，不构成跨机器漂移）。
