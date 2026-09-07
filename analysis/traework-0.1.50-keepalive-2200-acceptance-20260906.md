# traework 0.1.50/0.1.51 keepalive 22:00 首次生产 run 验收报告（只读取证）

- 时间：2026-09-06 22:40–22:50（GMT+8）
- 服务器：45.207.222.65:18998（容器 cli-proxy-api，宿主机 +08 时区）
- 凭据：management key 全程 shell 变量流转，零回显；auth 文件仅提取 disabled/note/expiredAt 等元数据

## 结论：验收不通过 —— 修复未完全生效（回归信号确凿）

22:00 共产生 **3 次** daily run（02/27/50 秒），行为不一致：

| run | 时刻 | 结果 | 行为 |
|---|---|---|---|
| 1 | 22:00:02 | refreshed=0 failed=2 **session_dead=0** total=4 | ✅ 修复后行为：HTTP 400 判 failed 不判死 |
| 2 | 22:00:27 | refreshed=0 failed=2 **session_dead=0** total=4 | ✅ 同上 |
| 3 | 22:00:50 | refreshed=0 failed=0 **session_dead=2** total=4 | ❌ 旧版行为：刷新被拒即判死 |

## 决定性证据

1. **物理文件被 22:00:50 的 run 改写**（容器内 `/CLIProxyAPI/mysqlstore/mysqlstore/auths/`）：
   - `traework-3313275866721299.json`（用户36360053870）：mtime **2026-09-06 22:00:50**，`disabled=True`，note=`Session expired (refresh token dead): re-login required`；该账号另 exhausted=True / preserved=True（remain=0）
   - `traework-392978863762272.json`（用户69557875132）：mtime **2026-09-06 22:00:50**，`disabled=True`，note=`Session expired (refresh token dead): re-login required`；该账号 remain=4169、exhausted=False、fail_count=0 —— **明显误杀**
   - 其余 4 个文件 disabled=False，mtime 22:38（credits 拉取），无 session-dead note
2. **22:00:50 伴随插件热重载痕迹**：auth 文件 CREATE 事件（含上述两个 traework 文件 + 两个 workbuddy 文件）、token-usage-tracker 双次重新初始化。容器本体 **未重启**（StartedAt=2026-09-06T05:17:50Z 即 13:17:50 +08，RestartCount=0）。
3. 管理面账号状态：6 账号中 2 个 disabled=True（即上列两个），anomaly 全 False；`keepalive.last_run` 仍保留 22:00:02 run 的结果（2 skipped "token not near expiry" + 2 failed）。
4. retry 环路（10 分钟间隔、预算 50）对两个文件持续 retry 至 22:40+，均 HTTP 400 `refresh token is not matched to the client` —— 与 run 1/2 的 failed 重试并存，提示 **可能存在新旧两套 keepalive 实例并存**（run 3 疑似旧 .so 在重载时被加载）。
5. auth-guard：3h 内无 re-applied 行（当前无可自动刷新干预的停用标记），与"账号大多 enabled"一致，属正常。

## 与任务预期的偏差

- 任务简报中的 4 个无 expiredAt uid（4104930657578889 / 4351221664059467 / 438080225149472 等）**均不在服务器账号集内**；实际 6 账号 uid：1993858382824235 / 2033439621254311 / 2433670276462265 / 2627180001239321 / 3313275866721299 / 392978863762272。
- daily run total=4（非 6）：2433670276462265、2033439621254311 skipped（token 未临近过期）；1993858382824235、2627180001239321 完全不在 run 结果里。
- HTTP 400 报错为 `refresh token is not matched to the client`（code 10101），非"参数无效"文案。

## 紧急止血建议

1. **立即**：在服务器 config.yaml 的 traework plugin 配置加 `token_keepalive: false` 后重载，阻断后续判死（今晚 23:00 若有重试/其他触发仍可能再判死）。
2. **恢复误杀账号**：`traework-392978863762272.json` 改 `disabled=false` 并清除 note（该号积分充足、TokenExpireAt 约 2026-09-28，完全可救）；`traework-3313275866721299.json` 积分已耗尽（remain=0），可顺手恢复但价值有限。
3. **根因排查**（不止血无法安心）：定位 22:00:50 是什么触发了插件重载、重载加载的是哪个版本的 traework .so、为何同一晚 3 次 daily run —— 怀疑旧版本插件实例未被 0.1.50 覆盖或双实例并存。

## 交叉信息与版本口径修正（验收后补充）

- **生产实际版本**：今日 16:27–17:50 已发布三件套（traework **0.1.51** / workbuddy 0.14.22 / qoderwork 0.9.10），0.1.51 包含 0.1.50 的 keepalive 修复 + SSE 200 业务错误换号。本验收针对的"修复后首个 22:00 run"实际运行的是 0.1.51；回归信号在 0.1.51 上依然成立。
- **并行会话线索**：21:59–22:30 另一会话在同一服务器排查保号池震荡，已发现"keepalive 22:00 全量重写 auth 文件剥离插件顶层字段、磁盘 mtime 全量退化"的缺陷（详见 `.workbuddy/memory/2026-09-06.md` 保号池一节）。本次 22:00:50 的第三次 daily run / 重载痕迹可能与该时段的取证/配置操作同源，也可能与 host.auth.get 不透传插件字段的缺陷相关——建议与保号池修复（方案 A/B）合并排查。
- auth-guard（0.1.50 新增）按设计会把 markSessionDead 注册进 guard（"session-dead"），因此即使 core auto-refresh 抹掉磁盘 disabled，guard 也会把停用标记 reapply 回来——误杀一旦发生**不会自愈**，需手动恢复文件或面板启用（启用会 guardUnregister）。
