# 6-review — traework 第三方导出账号缺 uid 落盘 legacy 单文件（0.5.21）

## 结论

STYLE: PASS

## 检查范围

本轮改动 7 个文件（4 代码 + 2 新增测试 + 1 版本/日志组）：

| 文件 | 改动性质 |
|---|---|
| `traework/credential.go` | `traeAuth` 新增 `Name` 字段；新增 `uidFromTraeJWT`；`parseTraeAuth` 补 name→nickname、JWT data.id→uid 兜底 |
| `traework/host_auth.go` | 抽出 `hostAuthListAll`（全量列表）；`hostAuthList` 改为复用后过滤，行为不变 |
| `traework/legacy_auth_migrate.go` | 新增：legacy `traework.json` → per-uid 迁移决策与启动自愈 |
| `traework/watchdog.go` | 启动序列挂载 `migrateLegacyAuthFiles()`（与 `purgeLegacy*` 并列） |
| `traework/credential_test.go` | 新增：JWT 提取、真实 1.json 形状、字段优先级 |
| `traework/legacy_auth_migrate_test.go` | 新增：迁移决策/编排/孤儿清理/宿主未就绪 |
| `traework/main.go`、`VERSION`、`CHANGELOG.md` | 0.5.20 → 0.5.21 + 变更说明 |

未纳入本轮：`browserlogin.go`、`qoderwork/policy_test.go` 等工作区既有改动属其它会话，未触碰、未提交。

## 逐项检查

### 格式与排版

- `gofmt -l`：新增文件直接返回空；3 个存量 tracked 文件按仓库既有口径做 CRLF→LF 临时转换后校验，返回空。
- 未对存量文件做批量格式化，diff 仅限改动位点；缩进为 tab，结构体字段按 gofmt 对齐。
- 新增中文注释、CHANGELOG、Bug 文档均为 UTF-8，无乱码。

### 命名

- `uidFromTraeJWT`、`hostAuthListAll`、`planLegacyAuthMigration`、`migrateLegacyAuthFiles` 与既有 `stripAnomalyKey` / `purgeLegacy*` 命名节奏一致。
- 未改任何既有函数签名；`hostAuthList` 语义与返回不变。

### 注释

- 新增/修改函数均带中文 `[参数]` / `[返回]` / `最近修改时间` 与改动原因，符合 `PROJECT_STYLE.md`。
- 注释解释「为什么」：空 uid 为何导致 legacy 文件名、面板前缀为何过滤、为何不验签、为何 cookies 不落盘。
- 迁移函数显式写明安全边界（只认 legacy 名、只读该文件、同 uid 只清孤儿）。

### 日志

- 新增日志只输出文件名与 uid 级标识，不含 token/refreshToken/credential。
- 单文件失败 `log.Printf` 后继续，不阻断其余账号迁移。

### 错误处理

- JWT 提取任何异常（非三段、坏 base64、坏 JSON、无 data.id）返回空串，不 panic。
- 迁移：列表失败静默返回（宿主未就绪，下次启动再试）；解析失败/无 uid 一律跳过，绝不盲写。
- 删除只走 `deleteAuthFileInDir` / `persistAuthDirect` 的既有安全校验（绝对路径 + legacy 名 + 目录内）。

### 复用

- 复用 `parseTraeAuth`、`authFileNameFor`、`isLegacyAuthName`、`persistAuthDirect`、`deleteAuthFileInDir`，未新造并行实现。
- `hostAuthListAll` 抽自原 `hostAuthList` 的 RPC 解析段，消除重复的 envelope 解析。
- 仅标准库，无新依赖。

### 测试

- `credential_test.go`：真实 1.json 形状断言 uid/nickname/deviceId/machineId 与 `traework-181896157339808.json` 文件名；显式字段优先；credential nickname 优先于 name。
- `legacy_auth_migrate_test.go`：迁移决策表、persist 参数、同 uid 只清孤儿、列表失败零写入。
- 必失败哨兵确认新测试进入编译链路后删除；`python scripts/cgo-shim-build.py traework` build+vet+test 全绿。

## 残留风险

- 存量 legacy 文件若未保存 `name`（旧版 traeAuth 无该字段），迁移后面板以 UID 展示；重新导入同 uid 会被判重，不会补昵称。
- 迁移在 watchdog 首次 tick 前执行，依赖宿主 auth 列表已就绪；未就绪时静默跳过，下次启动重试。
