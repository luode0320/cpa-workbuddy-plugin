---
name: project-cpa-workbuddy-plugin-host-config-safety-rules
description: 当生产 CPA 宿主（cpa.luode.vip / 45.207.222.65 的 cli-proxy-api 容器）出现「管理面板打不开」「登录报 404 未找到 v8 Management API」「外网 502 Bad Gateway」「容器监听端口漂移为随机端口」「/v0 或 /v8 management 路由全 404」等故障，或需要对生产 config_store / spool 配置做任何读写时触发。负责生产 CPA 宿主配置存储契约（config_store.content 必须是多行 YAML 原文）、配置损坏类故障的根因定位与带回滚恢复、配置写入红线与防呆。发布链路见 project-cpa-workbuddy-plugin-release-rules，本地编译验证见 cgo-plugin-isolated-test；本 skill 只管生产宿主配置这一层。
---

# cpa-workbuddy-plugin 生产宿主配置安全

## Skill 作用与适用场景

- 生产 CPA（CLIProxyAPI）宿主的**配置数据层**是本项目所有插件的共同底座：配置一旦写坏，9 个插件会集体「看起来全挂了」，极易被误判为「插件更新把 cpa 搞坏了」。
- 本 skill 负责三件事：
  1. **讲清配置存储契约**（`config_store.content` 必须是什么形态），让「写配置」这一步不再踩坑；
  2. **故障特征速判 + 根因定位**（404 / 端口漂移 / 502 的真实链路）；
  3. **带回滚的恢复流程**（备份 → 修复 → 重启 → 验证，任一步失败可退）。
- 边界：发布链路归 `project-cpa-workbuddy-plugin-release-rules`；本地编译验证归 `cgo-plugin-isolated-test`；插件代码缺陷归各插件自身域；**账号数据（`auth_store` / spool auths）的误删恢复归 `project-cpa-workbuddy-plugin-account-data-recovery-rules`**——本 skill 只管 `config_store` 配置层，不要拿配置层的写回手法去动账号表。本 skill 只处理「生产宿主配置」。
- 通用可迁移部分（配置二次序列化损坏模式、故障时段写入源定位手法）见用户级 skill `config-persistence-safety-rules`，本 skill 只写本项目专属事实。

## 核心事实：配置存储契约（铁律，2026-10-11 实证）

生产拓扑：

| 项 | 值 |
|---|---|
| 入口 | `https://cpa.luode.vip`（服务器 `45.207.222.65:18998`） |
| 容器 | `cli-proxy-api`（镜像 `luode0320/cli-proxy-api:latest`，**host 网络**，版本 `03fbfbee`） |
| 配置库 | MySQL 容器 `mysql`，库 `cli_proxy_api`，表 `config_store`（单条 `id='config'`） |
| spool 文件 | 容器 `/CLIProxyAPI/mysqlstore/mysqlstore/config/config.yaml`＝宿主 `/usr/local/src/cli-proxy-api/mysqlstore/mysqlstore/config/config.yaml` |
| 反代 | nginx `/etc/nginx/conf.d/cpa.luode.vip.conf` → `proxy_pass http://127.0.0.1:8317` |
| 业务端口 | `8317`（必须显式配置，见下方「端口显式固定」） |

**契约**：`config_store.content` **必须是「多行 YAML 原文」**。

- CPA 启动时 `syncConfigFromDatabase` 把 `content` **逐字节原样**写入 spool 文件，随后直接对该文件做 YAML 解析，**不做任何 JSON / `\n` 解码**。
- 实证：`SELECT md5(content)` 与 spool 文件 `md5sum` **完全相同**；spool 的 mtime 恰为容器启动时刻。
- 推论：`content` 一旦被写成**字面 `\n`（0x5c 0x6e）转义的单行格式**，spool 即塌缩成单行；整篇以 `#` 开头会被 YAML 判为整行注释 → **解析出空配置**。

> 历史错记警示：本机知识库旧笔记曾记「content 是单行 JSON 风格转义」，**该描述已修正，勿再据此操作**——它正是 2026-10-11 事故的诱因。

## 故障特征速判（空配置的两个致命后果）

`content` 塌缩为单行转义 → 空配置后，会出现且仅出现以下组合症状：

| 丢失项 | 症状 |
|---|---|
| `remote-management.secret-key` | 管理路由被禁用 → `/v0/management/*` 与 `/v8/management/*` **全部 404**；v8 前端把 404 解释成「服务端不支持 v8」，报 `未找到 v8 Management API，请升级至 v8` |
| `port` | 端口回退 `0` → 容器启动绑定**随机端口**（曾见 44363）→ nginx 仍代理 8317 → 外网 **502 Bad Gateway** |

**两种故障形态**（取决于容器是否重启过）：

- **形态 A（容器重启后）**：`port` 丢失 → 绑定随机端口 → nginx 仍代理 8317 → 外网 **502**，面板彻底打不开。
- **形态 B（容器未重启、运行中热重载）**：端口**不漂移**（仍在 `:8317`）、`/management.html` 仍 **200**，**只有管理面 404** —— 极易被误判为「面板 bug」，必须查 `content` 换行数才能定性。（2026-10-11 18:06 二次事故即此形态）

**快速判据**（三条同时成立即可定性）：

```bash
# 1) 端口是否漂移（应看到 :8317；漂移时是随机端口或没有 CLIProxyAPI 监听）
ss -lntp | grep -E ':8317|CLIProxyAPI'

# 2) 管理接口是否 404（401=路由在需鉴权=正常；404=空配置）
for p in /management.html /v0/management/config /v8/management/config; do
  printf '%s -> ' "$p"; curl -s -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:8317$p"
done

# 3) content 是否塌缩成单行（正常真实换行数 ~1200；故障时 =1）
docker exec mysql sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -N -e \
  "SELECT char_length(content), (char_length(content)-char_length(replace(content,char(10),\"\"))) AS lf FROM cli_proxy_api.config_store WHERE id=0x636f6e666967;" 2>/dev/null'
```

## 根因定位：写入源怎么找

生产故障时**先定位「谁写的」，再修**，否则修完会被再次写坏。

1. **看容器/系统事件**，锁定故障时段谁在操作：
   ```bash
   docker events --since 2026-10-11T16:50:00 --until 2026-10-11T17:05:00 | head -40
   ```
   典型痕迹：外部会话在容器内 `exec grep ... /CLIProxyAPI/config.yaml`、直连 `mysql ... cli_proxy_api.config_store`。
2. **确认是否有其他 SSH 会话在线**（写入源常是并行会话，本项目两次事故均如此）：
   ```bash
   who; w; last -n 12
   ```
   - 交互式会话会出现在 `w` / `last` 中（可见来源 IP、登录时间、正在跑的命令）；
   - **`ssh host "cmd"` 这类非交互式命令不留 `last` 记录**，只能靠 `docker events` 抓；
   - 本项目事故特征：外部会话在装完插件后，`exec: mysql -sN -e 'select content from cli_proxy_api.config_store where id="config"'` 读配置 → 132ms 后 spool 被改写 → `port: 8317 -> 0`。
2. **按时间窗找被改文件**：
   ```bash
   find /root /tmp /usr/local/src /home -newermt '2026-10-11 16:50' ! -newermt '2026-10-11 17:05' -type f 2>/dev/null | grep -v '\.git/'
   ```
3. **指纹比对定案**：候选文件与 spool `md5sum` 相同即为写入源证据（本项目事故中命中宿主 `/tmp/new_config.yaml`，md5 与 spool 完全一致）。
4. **时间线交叉验证**：`config_reload.go` 日志的 `port: 8317 -> 0`、MySQL `config_store.updated_at`、文件 mtime 三者应指向同一时刻。

**常见误判排除**：
- ❌ 不是插件更新导致——`plugin-store install` 走正常管理 API，安装后管理接口仍应为 200；
- ❌ 不是 `management.html` 自动升级导致——CPA 本体已是 v8，`/v8/management` 下确有 `config`、`plugins` 等路由，前端升级只是让报错文案指向「v8」；但 **v8 只是路由子集别名**，不能据此推断所有接口都能按 v8 拼（实测 `/v8/management/oauth-excluded-models` 带有效密钥仍 404，该接口只在 `/v0/management` 注册）。判定办法：带密钥逐个打，401=路由存在缺鉴权，404=该前缀下没注册这条。
- ❌ 不是 nginx 配置被改——nginx 一直指向 8317，是 CPA 端口漂移后才 502。

## 恢复流程（必须带回滚，任一步失败可退）

**原则**：先备份 → 再修复 → 每步回读校验 → 失败即回滚。**不要**用 SQL `REPLACE(content,'\n','\n')` 这类裸转义写法（多层 shell/SQL 转义极易再次写坏），一律用 **hex 编码**写入。

```bash
# ── Step 1 备份（两个回滚点，缺一不可）
cp -a /usr/local/src/cli-proxy-api/mysqlstore/mysqlstore/config/config.yaml /root/config.yaml.broken.$(date +%Y%m%d_%H%M)
docker exec mysql sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e \
  "DROP TABLE IF EXISTS cli_proxy_api.config_store_bak_$(date +%Y%m%d_%H%M); \
   CREATE TABLE cli_proxy_api.config_store_bak_$(date +%Y%m%d_%H%M) AS SELECT * FROM cli_proxy_api.config_store;" 2>/dev/null'

# ── Step 2 生成修复 SQL（hex 编码，彻底规避转义）
#   先得到「反转义后的正确多行 YAML」到 /tmp/preview_config.yaml（用 chr() 构造字符避开转义层）
#   再生成：UPDATE cli_proxy_api.config_store SET content = CONVERT(UNHEX('<hex>') USING utf8mb4) WHERE id='config';

# ── Step 3 执行 + 立即回读校验（不一致就回滚，不要往下走）
docker exec -i mysql sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" cli_proxy_api' < /tmp/fix_config.sql
#   期望：char_length 与预期一致、真实换行数 ~1200、md5 == 预期文件 md5sum
#   注意：修复采用「双写」——DB content 与 spool 文件必须是同一份正确内容（md5 一致），
#         因为 CPA 是原样搬运 content 到 spool，任一端不一致都会再次触发解析失败。

# ── Step 4 让配置生效（两条路径，按端口是否漂移选择）
#   路径 A（推荐；端口未漂移、容器仍在跑）：把正确内容直接写回 spool，靠 file watcher 热重载，业务零中断
cp /tmp/preview_config.yaml /usr/local/src/cli-proxy-api/mysqlstore/mysqlstore/config/config.yaml
#     等待数秒，日志应依次出现：
#       config successfully reloaded
#       remote-management.secret-key: created
#       management routes enabled after secret key update
#   路径 B（端口已漂移 / 容器状态异常）：重启容器
docker restart cli-proxy-api
#   注意：加载 9 个插件 + 模型刷新，从启动到 started successfully 约需 60–80 秒，勿误判失败
#   这是「配置损坏恢复」场景下的必要重启（空配置必须重启才能重新加载 spool），非常规动作；见下方写入红线第 7 条

# ── Step 5 验收
docker logs --tail 80 cli-proxy-api 2>&1 | grep -E 'management routes registered|started successfully'
#   应见：management routes registered after secret key configuration
#         API server started successfully on: :8317
curl -s -o /dev/null -w '%{http_code}\n' https://cpa.luode.vip/management.html   # 200
curl -s -o /dev/null -w '%{http_code}\n' https://cpa.luode.vip/v8/management/config  # 401
```

**回滚（如需）**：

```sql
UPDATE cli_proxy_api.config_store
SET content = (SELECT content FROM cli_proxy_api.config_store_bak_<ts> WHERE id='config')
WHERE id='config';
```
随后 `docker restart cli-proxy-api`。

## 配置写入红线（防呆，最高优先级）

1. **禁止**对生产 `config_store.content` 或 spool 文件做「**读出 → 二次序列化（JSON）→ 写回**」——这是本次事故的成因，任何会话/脚本都不得这样做。
2. 一切配置写操作走**管理 API** `PUT /v8/management/config`，不要直接改库或改文件。
3. 必须直接写库时：**hex 编码**写入（`CONVERT(UNHEX('<hex>') USING utf8mb4)`），写后必须 `md5` 回读校验。
4. **端口显式固定**：`server.port` 保持显式 `8317`，避免配置解析异常时端口静默漂移到随机值，让 nginx 502 掩盖真实故障。
5. **禁止多会话并行改同一台生产配置**——本次诱因即另一会话在操作生产配置时引入转义。改前先确认没有其他会话/自动化在动同一份配置。
6. **凭据纪律**：`docker events` 等输出会带出 mysql 密码等明文，**不得**把凭据原值回显到日志、报告、回复或提交信息中。两个已发生的自伤形态（2026-10-11）：① 探测脚本里写 `echo "probe [$AUTH] -> ..."` 或先组装 `Authorization: Bearer $KEY` 再 echo 该变量，会把管理密钥打进工具输出——**只允许打印 `${#KEY}`（长度）或前缀标签**，请求头变量一旦组装成功就只进 `curl`，不进任何 echo；② 用 `sed 's/(KEY|TOKEN|SECRET|PASSWORD)=.*/.../'` 按**变量名**脱敏去打印 `env`，会漏掉把口令**内嵌在值里**的连接串（如 `SQL_DSN=root:pw@tcp(host:port)/db`、`REDIS_ADDR=redis://:pw@host`）——必须按 `://<user>:<pw>@` 结构脱敏，或干脆只打印变量名清单（`env | cut -d= -f1`）与长度，绝不整行打印值。
   补充判据：连接串、DSN、URL、header 这四类值默认视为含密，打印前先做结构脱敏；同一弱口令在多处复用（管理密钥 / MySQL / Redis / SESSION_SECRET）本身就是需要轮换的信号，发现时在报告中提示而不复述其值。
7. **非必要不重启生产 cpa 容器（2026-10-11）**：`docker restart cli-proxy-api` 会中断在线流量并触发 60–80 秒冷启动（9 插件 + 模型刷新），属高成本、高影响操作。**插件的新增 / 更新 / 安装走 `plugin-store install` 热重载即可，一般不需要重启容器**（详见 `project-cpa-workbuddy-plugin-release-rules` Step 13）。**配置损坏恢复**：端口未漂移（形态 B）时**不需要重启**——改 DB + 写回 spool 靠 file watcher 热重载即可（2026-10-11 18:12 实证，业务零中断）；**仅当端口已漂移（形态 A）或容器状态异常时才必须重启**。任何重启执行前需用户确认；除此之外不得以任何理由（含"发布后顺手重启一下"）重启生产容器。

## 更新插件的正确姿势（关键：不需要手工改数据库表）

**结论：安装 / 更新生产插件只需要一条管理 API 命令，`config_store` 由 CPA 自动维护，任何人都不需要直接读写它。**

- **正确流程（唯一）**：`POST /v0/management/plugin-store/<plugin-id>/install?version=<x.y.z>`；CPA 会自动把插件版本号、artifacts（7 平台 url/sha256/size）等写入 `config_store.content` 的 `plugins.configs.<id>`，并触发热重载。
- **实证（2026-10-11 18:04）**：`plugin-store/zcode-provider/install?version=0.1.3` → 200，日志 `config file changed, reloading`；把两次配置快照反转义后 diff，**唯一差异**就是 `zcode-provider` 的 `version: 0.1.2 → 0.1.3` 与 7 个 artifacts 的更新 —— 全部由 CPA 自动写入，**零人工介入**。
- **不要做**：为了「确认 / 修改插件配置」而 `select content from config_store` 再把内容写回。这正是 2026-10-11 两次事故的成因。
- 需要读配置确认时：**只读**，读完绝不写回；需要改就用管理 API `PUT /v8/management/config`。
- **判断是否被「二次序列化」的铁证**：不仅换行变字面 `\n`，**原有的反斜杠也会被再次转义**（`\` → `\\`）。可用两次快照 diff 观察注释区转义层级是否多了一层。

## 权责边界与不负责事项

- 只负责「生产 CPA 宿主配置」的契约、故障定位与恢复，不负责发布链路、插件代码、需求/Bug 主流程。
- 不重复 `project-cpa-workbuddy-plugin-release-rules` 的发布步骤，也不重复 `cgo-plugin-isolated-test` 的本地验证。
- 不把可迁移的通用手法锁死在本 skill（那部分引用用户级 `config-persistence-safety-rules`）。
- 不在未备份、未取得用户对生产写操作确认的情况下执行 DB 写入或容器重启。

## 执行通过 / 驳回标准

- 通过：故障定性有「端口 + 管理接口 + content 换行数」三重证据；写入源有「事件日志 + 时间窗文件 + md5 指纹」证据；恢复有回滚点且每步回读校验；验收含本地与外网双端 200/401。
- 通过：涉及生产写库/重启前已获得用户明确确认（生产操作属需确认级）。
- 驳回：未定位写入源就修复；未备份即改库；用裸 `REPLACE` 转义写法；只看外网 502 就断言「nginx 坏了」或「插件把 cpa 搞坏了」；把凭据明文写入任何输出。
