---
name: project-cpa-workbuddy-plugin-account-data-recovery-rules
description: 当生产 CPA（cpa.luode.vip / cli-proxy-api 容器）某个插件（traework / workbuddy / qoderwork / coze / zcode / copilot 等）的账号被误删、账号列表突然变空、面板提示「无账号」、spool 与 auth_store 中该 provider 记录归零，或需要还原某个时间点账号状态时触发。负责生产账号数据的三层存储定位（MySQL auth_store 真相源 / mysqlstore spool 投影 / 插件本地目录）、可恢复性判定（binlog 保留窗口）、还原点建立、从 binlog 精确提取被删行、凭据有效性实测与写回顺序。binlog 二进制解析的通用偏移细节见知识库《MySQL-binlog恢复被误删行数据-二进制JSON与偏移基准》；发布链路见 project-cpa-workbuddy-plugin-release-rules，宿主配置写入安全见 project-cpa-workbuddy-plugin-host-config-safety-rules。
---

# cpa-workbuddy-plugin 生产账号数据恢复

## Skill 作用与适用场景

- 用户说「生产 XX 插件的账号被删了 / 全没了 / 找不回来吗」
- 插件 `/accounts` 返回空数组，但历史上确实有账号
- spool 目录 `ls | grep <prefix>` 归零，或 `auth_store` 中该 provider 记录数为 0
- 误操作（面板批量删除、脚本误跑、人工 `rm`）后需要评估能否回滚

**边界**：本 skill 管「账号数据层的丢失诊断与恢复」。插件自身的禁用/冷却/路由逻辑归对应功能 skill；宿主 `config.yaml` / `config_store` 写坏归 `project-cpa-workbuddy-plugin-host-config-safety-rules`；插件发版归 `project-cpa-workbuddy-plugin-release-rules`。

## 自动触发信号

- 用户说「账号被误删了」「账号全没了」「能找回吗」「账户不见了」
- 插件 `/accounts` 返回 0 条，但用户确认历史上有账号
- `auth_store` 中 `id LIKE '<prefix>-%'` 计数为 0，或 spool 目录该前缀归零
- 需要还原某个时间点的账号集合（删号后重建、误操作回滚）

## 进入后先做什么

1. **先定位三层存储的实际状态**（见下节），拿到三个数字再下结论，不要凭「面板空了」推断已被删。
2. **立刻备份 binlog 本体**（`docker cp`），这一步必须先于任何解析尝试。
3. 判定可恢复性（`log_bin` / `binlog_row_image` / 保留窗口 / 删除时间），三项齐备才继续。
4. 建还原点三层（表 dump + spool 快照 + 数量基线），用户明确要求「可还原」时这一步不可跳过。
5. 提取 → 自证 → 实测凭据 → 按顺序写回 → 四步验收。

## 默认执行流程

按本文顺序执行：三层存储定位 → 可恢复性判定 → 还原点 → binlog 提取 → 凭据实测 → 写回 → 验收。每步的判定标准与命令见对应章节，**不得跳步**（尤其「先备份 binlog」「先建还原点」两步）。

## 三层存储：先搞清账号到底存在哪

这是本 skill 最重要的前提。**不同插件不是同一套存储**，按错层去找会得出「全部丢失、无法恢复」的错误结论。

| 层 | 位置 | 性质 | 说明 |
|---|---|---|---|
| ① MySQL `auth_store` | 容器 `mysql` → 库 `cli_proxy_api` → 表 `auth_store`（`id` / `content` JSON） | **真相源** | 宿主以它为权威；`content` 是 MySQL 二进制 JSON 列，不是文本 |
| ② spool 文件 | `/usr/local/src/cli-proxy-api/mysqlstore/mysqlstore/auths/` | 投影层 | 宿主的 file watcher 读这里；与 `auth_store` 应保持一致 |
| ③ 插件本地目录 | `~/.antigravity_cockpit/<plugin>_accounts/` | 仅部分插件有 | coze / zcode / copilot 有；**traework / workbuddy 系没有** |

### 必须先排除的三个假信号

1. **`auth-dir: "~/.cli-proxy-api"` 是空的**：配置里写着这个路径，但实测该目录 0 个文件。**宿主真实读取的是 ② mysqlstore spool**，按配置路径去找会一无所获。
2. **`docker inspect` 的 Mounts 也不指向真实目录**：挂载源同样为空。
3. **插件停用会掩盖恢复结果**：`enabled=false` 时账号不进 `auth-files` 列表、插件 `/accounts` 也可能为空。**恢复后账号仍不出现，先查插件启用态，别急着判定恢复失败**。

```bash
# 三层一次看清
docker exec mysql sh -c "MYSQL_PWD=\$MYSQL_ROOT_PASSWORD mysql -uroot -N -e \
  \"SELECT COUNT(*) FROM cli_proxy_api.auth_store WHERE id LIKE '<prefix>-%';\""
ls /usr/local/src/cli-proxy-api/mysqlstore/mysqlstore/auths/ | grep -c '<prefix>'
docker exec cli-proxy-api sh -c "ls /root/.antigravity_cockpit/"
```

账号文件名前缀不等于插件 id（`traework-provider` 的文件是 `traework-<uid>.json`），排查前先从源码取真实前缀：

```bash
grep -rn 'authFilePrefix\s*=' <plugin>/*.go
```

## 可恢复性判定（五分钟定性）

```sql
SHOW VARIABLES LIKE 'log_bin%';                    -- 必须 ON
SHOW VARIABLES LIKE 'binlog_row_image';            -- 必须 FULL
SHOW VARIABLES LIKE 'binlog_expire_logs_seconds';  -- 保留窗口，生产为 86400（24 小时）
SHOW BINARY LOGS;                                  -- 现有文件与大小
```

**三家都必须满足**：`log_bin=ON` + `binlog_row_image=FULL` + **删除事件落在保留窗口内**。生产实测 24 小时窗口，超窗的 binlog 已被 purge，任何工具都救不回来。

### 第一动作：备份 binlog 本体，不是找解析工具

```bash
mkdir -p /root/traework-recovery && cd /root/traework-recovery
docker cp mysql:/var/lib/mysql/binlog.000017 ./binlog.000017
md5sum binlog.000017
```

binlog 会持续轮转，装解析工具、查资料期间数据就可能被清掉。**顺序不能颠倒。**

## 还原点三层（用户要求「出错可还原」时强制）

```bash
cd /root/traework-recovery
# ① 目标表全表 dump
docker exec mysql sh -c "MYSQL_PWD=\$MYSQL_ROOT_PASSWORD mysqldump -uroot --single-transaction \
  cli_proxy_api auth_store" > PRE_RESTORE_auth_store_$(date +%Y%m%d_%H%M%S).sql
# ② spool 目录完整快照
tar czf PRE_RESTORE_spool_$(date +%Y%m%d_%H%M%S).tar.gz \
  -C /usr/local/src/cli-proxy-api/mysqlstore/mysqlstore auths
# ③ 数量基线（不可省）
echo "spool=$(ls .../auths/ | grep -c '<prefix>')" > PRE_RESTORE_baseline.txt
```

**基线不可省的理由**：恢复后「有 17 条」不等于「原本就该 17 条」。只有恢复前的 `0` 基线配合恢复后的 `17`，才能证明净增量正确、没有重复写入。

回滚方式：`mysql cli_proxy_api < PRE_RESTORE_*.sql` + `tar xzf PRE_RESTORE_spool_*.tar.gz -C <原父目录>`。

## 从 binlog 提取被删行

### 删除溯源：先拿到时间线

按表名筛 `DELETE_ROWS`（etype=32），即可得到「何时删了哪些文件」，实测能直接对上插件目录的 mtime：

```
del 2026-10-20 15:34:02  traework-3313287900183084.json
del 2026-10-21 15:11:02  traework-2046641643717561.json
del 2026-10-22 12:59:34  traework-780003972744745.json
```

### 解析通道降级顺序

1. `mysqlbinlog --base64-output=DECODE-ROWS -v`（最优，但生产镜像可能**不带**该二进制）
2. Python `mysql-replication` 库（宿主可能无 `pip` / `ensurepip`）
3. **纯 Python 直解**（前两条都不可用时的唯一出路）

生产实测：容器内无 `mysqlbinlog`，宿主无 `pip` 且 `python3 -m ensurepip` 也不可用 → 走第 3 条。

**三个偏移陷阱（详见知识库笔记，务必先读再写解析器）**：
- 事件头是 `<IBIIIH>`（ts4 + type1 + server_id4 + size4 + log_pos4 + flags2）
- `TABLE_MAP` 的 `table_id` 是 **6 字节**，按 8 字节解会让所有表名错位
- `auth_store.content` 是**二进制 JSON 列**：偏移基准是 `payload`（类型字节之后）、value entry 是 `[type:1][offset:2]`（type 在前）、utf8mb4 偏移要 `-1`

**自证要求**：解析器必须用「已知值反查」验证（从原始字节里 `find` 一个已知 uid，比对解析结果）。本次前两版解析器都是**不报错但值全空/串位**，只有反查才暴露。

## 凭据有效性实测（恢复出字段 ≠ 能用）

恢复后必须逐个实测刷新，且注意三个高频误判：

1. **客户端身份可能不在代码常量里**。traework 代码常量是 `ono9krqynydwx5`，但真实 ClientID 是 `en1oxy7wnw8j9n`（存在各账号 `credential` 字段内的明文 JSON）。用错常量会返回**误导性的业务错误** `400 / 10101 refresh token is not matched to the client`，看起来像「全部凭据已死」。
2. **响应字段在 `Result` 下且首字母大写**（`Token` / `RefreshToken` / `TokenExpireAt` / `RefreshExpireAt`）。按小写取值会误判「无 token」。
3. **`credential` 本身可能是加密二进制**，不是 base64 JSON，不可直接解。

### traework 刷新端点（实测可用）

```
POST https://api.trae.cn/cloudide/api/v3/trae/oauth/ExchangeToken
{"ClientID":"en1oxy7wnw8j9n","RefreshToken":"<rt>","ClientSecret":"-","UserID":"<uid>"}
```

判定：`Result.Token` 非空 = 账号可救；`401 / 20101 refresh token is invalid` = 真死。

**区分「本次误删造成」与「删除前就已失效」**：查账号的 `note` 字段。本次 17 个里有 1 个删除前就是 `Session expired (refresh token dead): re-login required`，它刷新失败属既有状态，**不是恢复失败，不要计入失败率**。

## 写回顺序与验收

**先真相源、后投影层**，顺序颠倒会被宿主覆盖：

```sql
-- ① auth_store：content 是 JSON 列，用 hex 规避多层转义
INSERT INTO auth_store (id, content) VALUES ('traework-<uid>.json', CAST(X'<utf8-hex>' AS JSON))
ON DUPLICATE KEY UPDATE content=VALUES(content), updated_at=CURRENT_TIMESTAMP(6);
```

```bash
# ② spool：原子写（临时文件 + os.replace），权限 600
```

```bash
# ③ 确认插件启用态（停用时账号不出现，是最常见的「恢复失败」误判）
curl -s -H "Authorization: Bearer $KEY" http://127.0.0.1:8317/v0/management/plugins | grep traework
# 停用则启用：
curl -s -X PUT -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"enabled":true}' http://127.0.0.1:8317/v0/management/plugins/traework-provider/config
```

### 验收四步（缺一不可）

1. **数量对齐**：`auth_store` 条数 == spool 文件数 == 插件 `/accounts` 条数
2. **宿主可见**：`/v0/management/auth-files` 总数应等于「恢复前基线 + 恢复数」（本次 56 → 73）
3. **单号探活**：抽样 `POST /v0/management/plugins/<id>/test-active`，至少覆盖多个账号
4. **真实入口**：`POST /v1/chat/completions` 拿 `finish_reason=stop` + 非空正文

**探活失败要分级**：`upstream 200: all models failed` 是**上游模型级不可用**，不是账号故障，不能据此判定该账号恢复失败。只有账号级鉴权错误（401/20101）才算账号本身不可用。

## 权责边界与不负责事项

- 只负责账号数据的丢失诊断、可恢复性判定、还原点、提取、写回与验收
- **不重启生产容器**（重启红线见项目规则）；本流程全程走数据库 + 文件 + 管理 API，不需要重启
- 不在未建立还原点前写入任何生产数据
- 不把「恢复出的 `disabled` 全为 false」当成还原了当时的禁用态——**删除前的禁用标记不在 DELETE 前镜像的语义里**，如需还原要另行核对
- 不承担「凭据已彻底失效需重新扫码登录」的补登流程

## 执行通过 / 驳回标准

- 通过：三层存储各有实测数字；可恢复性四问有 SQL 输出；还原点三层齐备（含基线）；提取结果经反查自证；凭据逐个实测且区分既有失效；写回后数量三方对齐 + 探活 + 真实入口验证
- 驳回：未备份 binlog 就开始装工具；未建还原点就写入；只报「恢复了 N 个」而不给基线对比；把 `all models failed` 当成账号失败；把「插件停用」误判成「恢复失败」

## 踩坑记录

### 坑 1：`auth-dir` 配置指向的目录是空的

配置 `auth-dir: "~/.cli-proxy-api"`，容器内该目录 0 个文件、inode 与 mysqlstore auths 不同。**宿主真正读的是 `/CLIProxyAPI/mysqlstore/mysqlstore/auths/`**。按配置路径恢复 = 全部无效。

### 坑 2：插件本地账号目录并非所有插件都有

`~/.antigravity_cockpit/` 下只有 `copilot_accounts` / `coze_accounts` / `zcode_accounts`。**traework / workbuddy 系没有独立目录，账号只在宿主 spool**。按「其他插件的目录结构」去找 traework 会误判「连本地目录都被删了」。

### 坑 3：解析器「不报错」不等于「解析正确」

三处偏移任一错位都会产出空文档或字段串位，且 `json.loads` 照样成功。必须用已知值反查。

### 坑 4：刷新失败的业务错误码可能指向「你的请求参数错了」而非「凭据死了」

`10101 refresh token is not matched to the client` 的真实含义是 ClientID 与 token 不配套。换用账号 `credential` 里的真实 ClientID 后，同一批账号从 0/17 成功变为 16/17。

### 坑 5：`binlog_expire_logs_seconds=86400`

只有 24 小时。误删发生在 3 天前仍能恢复，是因为 binlog 文件恰未轮转；这是运气，不是必然。**发现误删立即备份 binlog 本体。**

### 坑 6：binlog 超窗 ≠ 不可恢复——第二顺位是历史全表 dump

24 小时窗口一过，binlog 通道即死（2026-11-07 实证：qoderwork 16 个账号删除早于 11-06 23:21，254 仅剩 11-06 03:43 起的 26 个 binlog，全部 0 命中）。**但「全表 dump」是跨 provider 的兜底源**：为恢复某个 provider 误删而建的 `auth_store` 全表还原点，会顺带保存当时**所有** provider 的完整行。

本次 qoderwork 16 个账号的全部凭据，就来自 13 天前为 traework 建的那份 `PRE_RESTORE_auth_store_20261025_134113.sql`。

**检索顺序固定为**：① 当前 `auth_store` → ② spool 目录 → ③ binlog（窗口内）→ ④ **历次 `PRE_RESTORE_*` / 迁移 / 归档的 `auth_store` 全表 dump** → ⑤ `auth-backup-*` 目录。

判据只需一条命令：`zcat <dump.gz> | grep -c '<prefix>-'`。两份全库备份都为 0 时，不要急着下「已彻底删除」的结论——**先回头找 `PRE_RESTORE_*` 这类一次性还原点**，它们不在任何定时备份策略里。

### 坑 7：凭据家族决定可恢复窗口，先判前缀再谈「能不能救」

同一插件可能并存两族凭据，**有效期差两个数量级**，恢复前必须先判族（只看前缀，不输出值）：

| 家族 | accessToken | refreshToken | 有效期 | 删除后能否恢复 |
|---|---|---|---|---|
| OAuth device（qoderwork） | `dt-` | `drt-` | at ~30d / rt **~1y（rotating）** | **可**，一年内都能刷活 |
| PAT（qoderwork 旧） | `jt-` | `jrt-` | at 24h / rt **48h** | **不可**，超 48h 全死 |
| traework | `token` | `refreshToken` | at 15d / **rt ~6 个月** | **可**，见下方轮换语义 |

qoderwork 2026-11-07 实证：18 个账号全为 `dt-/drt-`，删除 13 天后 `dt-` 仍在有效期（到 11-23），上游 `GET /sash/api/v1/me/campaigns` **18/18 返回 200 且 `uid` 与文件名逐一匹配**。

**只读探活优先于刷新**：`dt-` 未过期时直接打只读端点即可判定存活，**不要先调 `deviceToken/refresh`**——`drt-` 是 rotating，刷新一次就消费掉旧值；一旦刷新结果没落盘，等于把可恢复凭据换成了一次性垃圾。

### 坑 8：插件直写物理文件的 provider，`auth_store` 与 spool 会不一致

`qoderwork` 走插件直写 spool（`writeAuthFileDirect`），不经宿主 `auth.save`。实证：恢复前 `auth_store` 中 qoderwork = **0**，而 spool 有 **2** 个在役账号——**这 2 个从未进过 MySQL**。

后果：若宿主从 `auth_store` 重建 spool（重启/迁移），这 2 个账号会消失。**恢复时必须补写**，且要用**当前 spool 的最新内容**，绝不能用 dump 里的旧 token（那会把已轮换的凭据回滚到 13 天前）。判定命令：`auth_store` 条数 vs `ls spool | grep -c '<prefix>'`，不等即需 backfill。

### 坑 9：「轮换」不等于「一次性作废」，别因怕消费而放弃实测

traework 的 `ExchangeToken` **每次都返回新的 `refreshToken`**（2026-10-25 那份刷新结果里 16/16 全部与备份值不同），很容易误判成「旧 RT 已被消费、13 天后必然全死」。**实测证伪**：

- 同一份 10-25 的 RT，13 天后逐个重放：**12/16 仍返回 200**
- 对同一个 RT **连续调用两次**，两次都 200 并各自返回新 RT —— 说明旧 RT 在有效期内**可重复使用**，轮换是「发新值」而非「作废旧值」

**这条改写了操作准则**：不能因为「刷新会消费凭据」就不敢试。正确做法是**每成功一个立即落盘**（`Token`/`RefreshToken`/`TokenExpireAt`/`RefreshExpireAt`），最坏情况也只是拿到新值而没保存，重放一次即可恢复。

**判定真死的唯一信号**：`401 / 20101`（2026-11-07 实测 4 个）。`note` 里的 "Session expired (refresh token dead)" **不可作为判据**——同一批里 6 个带这个 note，其中 3 个照样刷新成功，note 只是 10-25 当时的旧状态快照。

### 坑 10：`excluded-models` 会让恢复后的账号「看起来完全不可用」

账号恢复到 12 个、探活 9 个通过，但 `POST /v1/chat/completions` 报 `unknown provider for model X`，`/v1/models` 里也找不到该插件任何模型。**这不是恢复失败，是 `config.yaml` 的 `excluded-models` 在挡**。

2026-11-07 实测：`excluded-models.traework-provider` 列了 **52 个**模型名，而插件动态拉到 **44 个**，其中 **43 个被排除**，只剩 `kimi-k3.1-pro` 一个对外暴露。

**恢复后的验收顺序必须补这一步**：账号层通过 → **再查 `excluded-models` 与 `/v1/models` 的交集** → 最后才做真实入口。否则会把配置层的模型过滤误判成「账号恢复无效」，白折腾一轮。

判据命令：`grep -A60 '"traework-provider"' config.yaml | grep -c '^\s*-'` 对比插件 `/models` 的实际条数；剩余为 0 时该插件对外等于不可用。

**先查审计日志，再怀疑热重载**（2026-11-07 二次实证，代价是一次多余的重启）：排除项可能不是静态写在文件里的，而是运行时通过管理 API 改的。容器日志里有明确留痕：

```
[2026-11-07 14:53:32] [info] [config_reload.go:130] oauth-excluded-models[qoderwork-provider]: updated (13 -> 20 entries)
```

看到 `/v1/models` 突然少了一批模型、而插件自身 `/models` 仍有全部条目时，**正确顺序是**：① `grep` 日志里的 `excluded-models.*updated` → ② 读 `excluded-models` 对应 provider 段 → ③ 最后才怀疑插件注册链路。宿主 `host.go` 里那段「热重载跳过 `callRegister`」的分支极具迷惑性，容易让人得出错误的宿主缺陷结论并重启容器——但重启救不回被配置排除的模型。

**注意配置有两套**：`excluded-models`（静态文件）与 `oauth-excluded-models`（管理 API 可写，日志前缀带 `oauth-`）。只查前者会漏掉后者的改动。

## 相关 skill 与文档

- binlog 二进制解析的通用细节（含可复用的 Python 解析要点）：知识库《MySQL-binlog恢复被误删行数据-二进制JSON与偏移基准》
- 生产账号文件位置与只读取证入口：项目记忆 `project-cpa-prod-auth-file-location`
- 宿主配置写入安全：`project-cpa-workbuddy-plugin-host-config-safety-rules`
- 发布链路：`project-cpa-workbuddy-plugin-release-rules`
- 重启红线 / 日志查询窗口：`AGENTS.md`「项目特有规则 → 仓库与发布」
