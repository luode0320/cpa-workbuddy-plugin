# traework 第三方导出账号缺 uid 落盘 legacy 单文件致面板不可见根因诊断

- 报告时间：2026-11-08 19:25:00（GMT+8）
- 报告对象：traework-provider 0.5.20（修复前）→ 0.5.21（修复后）
- 触发场景：面板「导入凭证」导入第三方导出的 Trae 账号 JSON（`{"accounts":[…]}`）
- 用户现象：宿主「认证文件管理」里只看到一个 `traework` 条目；TraeWork 面板账号列表里没有这个账号。

## 1. 现象与输入

用户导入文件 `1.json`，结构如下（单个账号）：

```json
{
  "accounts": [{
    "name": "用户06076610181",
    "token": "<JWT，payload.data.id = 181896157339808>",
    "refreshToken": "…",
    "deviceId": "3216283905996619",
    "machineId": "206e1cff…",
    "enabled": true,
    "cookies": ["…19 条…"]
  }]
}
```

关键事实：该账号对象**没有 `uid` / `userId` 字段**，身份只在 JWT payload 的
`data.id`；展示名在顶层 `name`。

## 2. 取证链

### 2.1 前端已正确展开，问题在后端身份解析

- [panel.html:1278-1283](file:///F:/cpa-plugin/traework/panel.html#L1278-L1283)
  `expandCredentials` 对 `accounts[]` 遍历：条目无 `credential` 字段时 `push(a)`
  整个账号对象，逐条 POST `/import`。
- [import.go](file:///F:/cpa-plugin/traework/import.go) `handleImportCredential` →
  `parseCredentialImport`：账号对象走 `parseTraeAuth` 的 flat 路径。

### 2.2 flat 解析拿得到 token，却拿不到 uid

旧版 [credential.go](file:///F:/cpa-plugin/traework/credential.go) 的 `traeAuth`
只映射 `uid`/`nickname`，不识别顶层 `name`，也不从 JWT 取身份：

- `token` → `Token` 有值，`hasToken()` 通过（所以导入**不报错**）；
- `uid` 缺失 → `UserID = ""`；
- `name` 无映射 → `Nickname = ""`。

### 2.3 空 uid 让文件名回退 legacy 名

- [authfile.go:46-53](file:///F:/cpa-plugin/traework/authfile.go#L46-L53)
  `authFileNameFor`：uid 为空 → 返回 `authFileName` = `traework.json`。
- 落盘 [import.go](file:///F:/cpa-plugin/traework/import.go) `hostAuthSaveJSON(name, raw)`。

### 2.4 面板前缀过滤排除了 legacy 名

- [host_auth.go:54+](file:///F:/cpa-plugin/traework/host_auth.go#L54)
  `hostAuthList` 用 `authFilePrefix = "traework-"` 过滤；
- `strings.HasPrefix("traework.json", "traework-")` = **false** →
  面板 `/accounts` 永远枚举不到它。

### 2.5 所有缺 uid 的导入互相覆盖同一个文件

由于文件名恒定 `traework.json`，这类导入只会保留最后一个账号；宿主侧却按
`type/provider=traework-provider` 把它显示成「一个 traework」，与用户截图一致。

## 3. 根因

**导入身份字段缺失导致落盘文件名降级**：第三方导出格式不含 `uid`，旧解析
不从 JWT 的 `data.id` 兜底，`UserID` 为空 → 文件名 `traework.json` → 面板
`traework-` 前缀过滤排除。表现为「导入成功但面板空白」，并遗留一个宿主可见、
插件不可见的 legacy 单文件。

## 4. 修复（0.5.21）

1. [credential.go:246](file:///F:/cpa-plugin/traework/credential.go#L246) 新增
   `uidFromTraeJWT`：base64url 解 JWT payload，取 `data.id`；任何异常返回空串，
   不验签（仅用于命名/展示，鉴权仍由上游负责）。
2. [credential.go:276](file:///F:/cpa-plugin/traework/credential.go#L276)
   `parseTraeAuth` 在 credential 解密后统一兜底：`Nickname` 空取顶层 `name`，
   `UserID` 空取 JWT `data.id`；显式字段优先，nested/flat 同时生效。
3. 新增 [legacy_auth_migrate.go](file:///F:/cpa-plugin/traework/legacy_auth_migrate.go)：
   watchdog 启动时（[watchdog.go:171](file:///F:/cpa-plugin/traework/watchdog.go#L171)）
   用全量列表 [host_auth.go:37](file:///F:/cpa-plugin/traework/host_auth.go#L37)
   `hostAuthListAll` 找到 legacy `traework.json`，可解析出 uid 即原样字节迁移为
   `traework-<uid>.json` 并删除旧名；同 uid 新文件已存在时只清孤儿；只对 legacy
   名读盘，不触碰其它插件认证。
4. `hostAuthList` 行为不变（仍前缀过滤），`hostAuthListAll` 仅供迁移；避免把
   其它插件账号纳入本插件面板/调度。

## 5. 验证

- 新增 `traework/credential_test.go`：
  - 真实 1.json 形状（无 uid）→ `UserID=181896157339808`、`Nickname=用户06076610181`、
    文件名 `traework-181896157339808.json`；
  - JWT 提取正常/补 padding/非 JWT/坏 base64/无 data.id；
  - 显式 uid/nickname 优先；credential 解出的 nickname 优先于顶层 name。
- 新增 `traework/legacy_auth_migrate_test.go`：迁移/拒绝分支、迁移编排、
  同 uid 只清孤儿、宿主未就绪不写盘。
- `python scripts/cgo-shim-build.py traework`：build + vet + test 全绿（2026-11-08）；
  另以必失败哨兵确认新测试真实进入编译链路后删除。

## 6. 影响与后续

- 新导入：按 `traework-<uid>.json` 落盘，面板可见，昵称取 `name`。
- 存量：插件加载后 watchdog 启动 sweep 自动把 legacy `traework.json` 迁移为
  per-uid 文件，账号随即出现在面板；若旧文件未保存 `name`，面板以 UID 展示。
- cookies 不参与业务认证（请求走 `Cloud-IDE-JWT` Bearer 头），导出格式中的
  cookies 不落盘，无功能影响。
