# workbuddy 0.21.26 验证记录 — 导入原生兼容第三方 auth_raw/profile_raw 记录

- 日期：2026-11-12 22:05 ~ 22:52
- 对应提交：`fabbed3`（feat，6 文件 +267/−6）/ `adf788c`（registry）
- 生产版本：0.21.26（`hot reloaded active_version=0.21.26 retired_version=0.21.25`）
- 用户诉求：先报「work10.json 导入到 wokrbuddy 失败了」，拿到一轮人工转换产物后提出「把 wokrbuddy 的导入兼容一下这种格式的 json, 下次我们就不用手动转换手动导入了」

## 1. 根因

第三方导出文件 `C:\Users\luode\Downloads\trae\work10.json` 是**顶层数组 + 嵌套原始字段**形态：

- 令牌在 `auth_raw`（对象）：`accessToken` / `refreshToken` / `expiresAt`（**毫秒**，如 `1796910717981`）/ `domain` / `expiresIn` / `refreshExpiresIn` / `scope` / `sessionState` / `tokenType`
- 账号在 `profile_raw`（对象）：`uid` / `nickname` / `uin` / `phoneNumber` 等 20 个字段
- 顶层另有蛇形副本 `access_token` / `refresh_token` / `expires_at`（**秒**）

`workbuddy/main.go:510 parseStored` 改前只有两条分支：顶层含 `auth` 键 → 嵌套结构体；否则扁平 `accessToken`。两种都不匹配上述形态 ⇒ 逐条返回 `parse_error: missing accessToken`。

`workbuddy/panel.html:1639 expandCredentials` 会把顶层数组展开成 10 条独立记录（展开本身没问题），但每条仍无 `accessToken` ⇒ 面板逐条报同一条错误。

结论：**文件格式不被插件识别，不是插件既有功能的回归**。

## 2. 实现

| 文件 | 改动 |
|---|---|
| `workbuddy/auth_thirdparty.go` | 新增。`thirdPartyRecord` 结构体；`isThirdPartyStored(probe map[string]json.RawMessage) bool`（认 `auth_raw` / `profile_raw` / `access_token` 任一键）；`parseThirdPartyStored(raw []byte) storedAuth`（令牌优先 `auth_raw.accessToken`、回落顶层 `access_token`；账号优先 `profile_raw`、回落顶层；过期时间经 `epochsToSeconds` 归一）；`epochsToSeconds(v int64) int64`（`v > 1e11` 判毫秒则 `/1000`，非正值返回 0） |
| `workbuddy/main.go:510` | `parseStored` 改为三分支分发：嵌套 `auth` / 第三方 `isThirdPartyStored` / 扁平 `accessToken`；末尾统一 `accessToken` 校验 |
| `workbuddy/panel.html:266` | 导入提示补「第三方导出 `{"auth_raw":{...},"profile_raw":{...}}`」一种形态 |
| `workbuddy/VERSION`、`workbuddy/main.go:359` | 0.21.25 → **0.21.26** |
| `workbuddy/CHANGELOG.md` | 顶部新增 0.21.26 节（背景 / 修复 1-3 / 为什么不改上游或调用方 / 测试） |
| `test/workbuddy/auth_thirdparty_test.go` | 新增 5 个用例 |

### 为什么不改 panel 的展开逻辑

`expandCredentials` 已有三条分支（顶层数组 / `{accounts:[{credential}]}` / 单对象）足以把第三方文件拆成 10 条；真正缺的是「拆出来的单条能被解析」。转换收敛在 `parseStored` 一处，导入、`handleParseAuth` 认领、`handleExecExecute`、导出回读等 **10 个调用点全部自动受益**，避免两处各写一份映射。

## 3. 本地验证

| 项 | 结果 |
|---|---|
| `python scripts/cgo-shim-build.py workbuddy` | build / vet / test 全绿，`ok ... 34.078s`，`all green (workbuddy)`；版本 bump 后复跑一次仍全绿 |
| 新增测试 | `TestParseStored_ThirdPartyRecord`、`TestParseStored_ThirdPartyFallback`、`TestParseStored_NestedAndFlatUnaffected`、`TestParseStored_MissingAccessToken`、`TestEpochsToSeconds` |
| 真实数据探针 | 把 `work10.json` 的 10 条记录逐条喂 `parseStored` → `verified 10/10 credentials`；样本 `uid=399529bf-4251-4f08-b9a8-c0ca053c4b3d`、`nick=157****3836`、`exp=1796910717`（秒）、`domain=www.workbuddy.cn`、`atLen=1539`、`rtLen=1540`。探针文件用后即删 |
| gofmt | `auth_thirdparty.go` / `main.go` / `auth_thirdparty_test.go` CRLF→LF 归一后 `gofmt -l` 无输出 |
| 面板语法 | `panel.html` 两个 `<script>` 块 `node --check` 均 exit=0 |
| 编码 | 四个改动文件 `bom=False`，中文串正常 |

## 4. 发布链路

| 步骤 | 结果 |
|---|---|
| 代码提交 | `fabbed3`，6 文件 +267/−6 |
| CI dispatch | `plugin=workbuddy-provider`、`version=0.21.26`、`allow_partial=true`，run **43238620141** |
| CI 结果 | `Release` job = **success**，13 个 Test job 全绿；失败/跳过仅 `windows-arm64 (go-cross)` 与 `darwin/arm64` cancelled（踩坑 44 已知形态，判据只看 Release job） |
| 冗余 run | push 触发的 run 43238579831 已 cancel（踩坑 55） |
| Release | `workbuddy-provider-v0.21.26`（id 443076874），4 assets：darwin_amd64 / linux_amd64 / linux_arm64 / windows_amd64 + checksums.txt |
| 资产校验 | `download-release-assets.py 0.21.26 workbuddy-provider` → `ALL CHECKSUMS OK`（4/4） |
| 内置版本串 | 四个包内二进制全部 `contains 0.21.26`（dylib 7636712 / .so 8466976 / .so 7803480 / dll 7907328 字节） |
| registry | `publish-assets.py workbuddy-provider 0.21.26` 首跑 exit=1（`missing zips: ['darwin_arm64','windows_arm64']`）→ 加 `--allow-missing` 成功，commit `adf788c`（4 artifacts） |
| 远端验证 | raw CDN **滞后 4.5 分钟**返回 0.21.25（14 次重试全旧版）→ 改用 GitHub Contents API 读取：version 0.21.26、4 artifacts size+sha256 全 OK、无旧版残留 → `FINAL: ALL PASS` |

## 5. 生产部署（热重载，容器未重启）

| 项 | 结果 |
|---|---|
| install | `POST /v0/management/plugin-store/workbuddy-provider/install?version=0.21.26` → 200 `{"status":"installed","source_id":"source-636fa01121af","path":"plugins/linux/amd64/workbuddy-provider-v0.21.26.so","restart_required":false}` |
| 热重载 | `[2026-11-12 22:50:19] pluginhost: plugin hot reloaded plugin_id=workbuddy-provider active_version=0.21.26 retired_version=0.21.25` |
| 落盘一致性 | `/CLIProxyAPI/plugins/linux/amd64/workbuddy-provider-v0.21.26.so` 8,466,976 B，sha256 `fca9f1550424b2136f7e58f0a01ef8e0e99f526c98048669620637ee54a70cca`，与本地 release zip 内 `.so` **逐字节一致** |
| 功能入二进制 | `grep -a -c 'auth_raw'` = **3**；`grep -a -oE '0\.21\.2[0-9]'` 只出 `0.21.26` |
| 面板资源 | `GET /v0/resource/plugins/workbuddy-provider/panel` → 200 / 86,919 字节，含 `auth_raw` |
| 账号池 | `GET /v0/management/plugins/workbuddy-provider/accounts` → 200，**30 个账号**；`work10.json` 的 10 个 uid **10/10 全部在池中** |

## 6. 生产行为验收（决定性证据）

取 `work10.json` 中 uid `399529bf-4251-4f08-b9a8-c0ca053c4b3d` 的**原始未转换记录**（`auth_raw` + `profile_raw` 形态），直接 `POST /v0/management/plugins/workbuddy-provider/import`，body `{"json": <原始记录>}`：

```
200 {"success":true,"uid":"399529bf-4251-4f08-b9a8-c0ca053c4b3d","nickname":"157****3836",
     "name":"workbuddy-399529bf-4251-4f08-b9a8-c0ca053c4b3d.json",
     "path":"/root/.cli-proxy-api/workbuddy-399529bf-4251-4f08-b9a8-c0ca053c4b3d.json"}
```

落盘文件（3,469 B）规范化校验：

| 项 | 实测值 | 判据 |
|---|---|---|
| 顶层键 | `['account','auth','disabled','logo','note','provider','type']` | 与 `buildAuthFileJSON` 产物一致 |
| `auth` 键 | `['accessToken','domain','expiresAt','refreshToken']` | 第三方原始字段已归一 |
| `expiresAt` | **int `1796910717`**（10 位秒级） | 源值 `1796910717981` 毫秒已正确 `/1000` |
| `domain` | `www.workbuddy.cn` | 保留原域名，`isGlobalDomain` 判 CN 走 `copilot.tencent.com` |
| `atLen` / `rtLen` | `1539` / `1540` | 与源值一致，令牌未被截断 |
| `account` | `{'enterpriseId':'','nickname':'157****3836','uid':'399529bf-…'}` | `profile_raw` 正确映射 |
| `note` | `CN · 积分未知` | `displayNote` 走 CN 分支 |
| `disabled` | `False` | 导入默认可路由 |
| `auth_raw` 残留 | **False** | 输出文档干净，无原始字段泄漏 |

结论：**用户「下次不用手动转换」的目标在生产真实入口上达成**。

## 7. 本轮新踩坑

1. `registry.json` 的 raw CDN 本次滞后 **4.5 分钟**（14 次重试全为旧版本），远超历史「1-2 分钟」经验值；改用 GitHub Contents API（`/contents/registry.json?ref=main` + base64 解码）可即时读到真源。
2. auth 文件落在**容器内** `/root/.cli-proxy-api/`，宿主同名路径不存在；容器内**无 `python3`**，须 `docker exec cli-proxy-api cat <file> > /tmp/x.json` 拉到宿主再解析（宿主 `python3` 可用）。
3. 生产管理密钥：`remote-management.secret-key` 取不到（`rm_key_len=0`，用它必 401）；明文 9 字节密钥在 `config.yaml` 第 **479** 行 `workbuddy-provider:` 段的 `cpa_management_key`。
4. 探针日志不要靠管道 `Select-String` —— 会被 failover 噪音日志淹没、抓不到目标行；先 `> log 2>&1` 再用 `Select-String -Path` 检索。

## 8. 遗留（非本轮缺陷）

- `darwin_arm64` / `windows_arm64` 平台缺件（与 0.21.25 同状况，按 `--allow-missing` 剔除槽位），Apple Silicon 与 Windows ARM 用户需后续补发。
- 面板没有专门的「第三方格式」提示示例块，只有第 266 行一行文字说明；如需更友好的引导可另立需求。

## 9. 结论

**PASS** — 第三方 `auth_raw`/`profile_raw` 记录可直接导入并在生产落盘为规范凭证文档，毫秒过期时间、账号映射、域名判定、CN 备注全部正确；发布链路完整、证据可回溯，容器全程未重启。
