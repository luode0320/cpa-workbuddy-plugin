STYLE: PASS

# 6-review：doubao 设备指纹 fp 采集与撤销 session 组过滤（0.1.21）

审查对象：`F:\cpa-plugin\doubao\` 下 13 个文件。
最近修改时间：2026-11-15 22:50:00。

## 静态路由命中

在 `D:\谷歌云盘\luode-skills` 下运行：

```text
python code-style-consistency-rules/scripts/static_owner_router.py --changed "doubao/protocol.go,doubao/cookiejar.go,doubao/cookiejar_test.go,doubao/workmode_test.go,doubao/completion_test.go,doubao/active_ping_test.go,doubao/credentials_test.go,doubao/tokenbeat_test.go,doubao/authfile.go,doubao/login.go,doubao/main.go,doubao/VERSION,doubao/CHANGELOG.md"
```

命中 7 步：STYLE-01 / STYLE-02 / STYLE-03 / STYLE-04 / STYLE-05 / STYLE-08 / STYLE-09。
未命中 STYLE-06（公共复用）与 STYLE-07（接口契约），理由见文末「未命中说明」。

## STYLE-01 静态格式与编码一致性

| 文件 | 字节 | BOM | 控制字符 | 换行 |
| --- | --- | --- | --- | --- |
| `protocol.go` | 24303 | 否 | 0 | CRLF 527，孤立 LF 0 |
| `cookiejar.go` | 16533 | 否 | 0 | CRLF 417，孤立 LF 0 |
| `authfile.go` | 21363 | 否 | 0 | CRLF 581，孤立 LF 0 |
| `login.go` | 54640 | 否 | 0 | CRLF 1086，孤立 LF 0 |
| `cookiejar_test.go` | 20222 | 否 | 0 | CRLF 562，孤立 LF 0 |
| `workmode_test.go` | 20081 | 否 | 0 | CRLF 453，孤立 LF 0 |
| `completion_test.go` | 18003 | 否 | 0 | CRLF 461，孤立 LF 0 |
| `active_ping_test.go` | 15640 | 否 | 0 | CRLF 381，孤立 LF 0 |
| `credentials_test.go` | 22644 | 否 | 0 | CRLF 566，孤立 LF 0 |
| `tokenbeat_test.go` | 19901 | 否 | 0 | 纯 LF 509 行（`i/lf w/lf`） |
| `main.go` | 16516 | 否 | 0 | CRLF 457，孤立 LF 0 |
| `CHANGELOG.md` | 56659 | 否 | 0 | 纯 LF 430 行 |
| `VERSION` | 6 | 否 | 0 | 无换行（`0.1.21`） |

`git ls-files --eol` 复核：`.go` 文件为 `i/lf w/crlf`、`tokenbeat_test.go` 为 `i/lf w/lf`、`CHANGELOG.md` 为 `i/lf w/lf`，与改动前一致，没有引入行尾抖动。

`gofmt -l` 首轮报 `FMT_DIRTY: authfile.go login.go workmode_test.go`，三处均为本轮新增内容带来的对齐问题（`StoredAuth` 字段块、`const` 块、`buildAuthScript` 注释末尾缺空行、`TestBuildClientQueryIsClientShaped` 的 `const` 组对齐）。用 `gofmt -w` 在 `$env:TEMP\fmtchk0121` 修好后写回并恢复 CRLF，复跑 `FMT_OK`（10 个 `.go` 文件零输出）。

## STYLE-02 命名与符号引用一致

**删除的标识符（3 个，全部只在 `cookiejar.go` + `cookiejar_test.go` 内引用）**：

| 符号 | 删除理由 |
| --- | --- |
| `blockedHostCookieNames` | 被 0.1.21 的四象限实测推翻，保留会让后人误以为黑名单仍有效 |
| `isBlockedHostCookieName` | 只服务 `filterHostCookies`，无第二调用者 |
| `filterHostCookies` | 只被 `durableJar.Cookies` 调用，取消过滤后成孤儿 |

删除后 `grep -n "blockedHostCookieNames\|isBlockedHostCookieName\|filterHostCookies" doubao/` 零命中，无悬空引用。

**新增的标识符（2 个）**：

| 符号 | 命名依据 |
| --- | --- |
| `fingerprintOf(sa *StoredAuth) string` | 与同文件已有的 `deviceIDFor(sa)` 对齐为「名词 + Of + 入参」的取值形态，一眼能看出是从账号取某项 |
| `callbackSVWebIDHeader` | 与相邻的 `callbackErrorHeader` / `callbackMsTokenHeader` 同构，`SVWebID` 沿用上游 Cookie 名 `s_v_web_id` 的缩写形式 |

**改签名的函数**：`buildBusinessQuery` / `buildClientQuery` 的末位新增 `fp string`。参数名与 `StoredAuth` 的 JSON 键 `s_v_web_id` 在语义上同指，但函数内统一用短名 `fp`，因为它在 query 里的键就叫 `fp`；`StoredAuth` 字段仍叫 `SVWebID`，避免与「字段名 = query 键名」的习惯混淆。

**保留不动的命名**：`deviceIDFor` 未改名也未改实现（实测它派生的假 `device_id` 不是判据），注释里显式写了「`device_id` / `web_id` / `tea_uuid` 用插件自己派生的稳定值即可」，避免后人误以为它已废弃。

## STYLE-03 注释分层与定义位置

- `fingerprintOf` 是新增函数，按仓库既有形态补齐完整元信息块：作用、`为什么不能自己拼`（含四轮实测数据）、`旧实现错在哪`、`老账号没有该字段时的行为`、`[参数]`、`[返回]`、`最近修改时间 + 改动原因`。
- `durableJar.Cookies` 的注释把「0.1.19 为什么错」写成一段完整因果：当时依据「带上 session 组就回 `710022004`」→ 四象限交替实测推翻了该对照 → 真正判据是假 `fp` → 当时把两个变量混在一起改 → 所以把假 `fp` 的锅记到了 session 组头上。写清「为什么推翻」而不只写「已删除」，避免下一个人重犯。
- `sessionCookieNames` 的注释补一句边界：「只用于『登录态是否还在』判定，**不再参与出站过滤**」。同一组 Cookie 名此前承担两个口径（判活 + 出站黑名单），注释显式把两个口径分开。
- `protocol.go:223` 的 `buildClientQuery` 文件级说明原文写「`device_id` / `fp` / `tea_uuid` 三者同源（fp 为 `verify_` 前缀）」，本轮改为「`device_id` / `tea_uuid` 同源……`fp` 必须是账号在浏览器里真实用过的那一串」。原注释描述的是**已被推翻的旧实现**，留在原地会误导。
- `login.go` 的 `buildAuthScript` / `buildBookmarkletScript` 注释各补一条「同一次执行还要顺手带走两个页面侧才有的值」，把 `xmst` 与 `s_v_web_id` 的采集动机写成同一个理由（都拿不到就整条链路降级），而不是分别写两句重复话。
- 所有改动位点的 `最近修改时间` 均为真实操作时刻：`protocol.go` / `clientEndpointURL` / `buildBusinessQuery` / `buildClientQuery` / `fingerprintOf` 为 `2026-11-15 22:05:00`；`cookiejar.go` 的 `Cookies` 为 `2026-11-15 22:10:00`；`login.go` 的两个脚本函数为 `2026-11-15 22:20:00`；`tokenbeat_test.go` 为 `2026-11-15 22:40:00`。

## STYLE-04 函数签名与参数结构

| 函数 | 改动前 | 改动后 | 取舍 |
| --- | --- | --- | --- |
| `buildBusinessQuery` | `(deviceID, webID, webTabID string, extra map[string]string)` | `(deviceID, webID, webTabID, fp string, extra map[string]string)` | `fp` 与其余三个设备标识同层，放在 `extra` 之前；`extra` 必须留在最后，否则调用点要改两处顺序 |
| `buildClientQuery` | `(deviceID, webID string)` | `(deviceID, webID, fp string)` | 与 `buildBusinessQuery` 的参数前置序列对齐（都是「标识…，fp」） |
| `fingerprintOf` | 无 | `(sa *StoredAuth) string` | 单入参单出参，没有为未来扩展预留结构体 |
| `durableJar.Cookies` | 已有 | 未改签名 | 只改函数体，去掉过滤分支 |
| `saveCredentials` | 已有 | 未改签名 | 只在 `StoredAuth` 字面量里补一个字段 |
| `handleLoginCallback` | 已有 | 未改签名 | 从既有请求头读新值，接口形态不变 |
| `buildAuthScript` / `buildBookmarkletScript` | 已有 | 未改签名 | 生成的 JS 里多读一个 Cookie，Go 层签名不变 |

没有为「是否带 fp」引入 `Options` 结构体或布尔开关：`fp == ""` 本身就是「未采集」的表达，再加一层 `hasFP bool` 会产生两个真相源。

## STYLE-05 结构与落点

- 设备指纹的读取集中在 `protocol.go` 的 `fingerprintOf` 一处，两条 query 管线（`endpointURL` / `clientEndpointURL`）都经它取，没有让调用点各自判断。
- `s_v_web_id` 的采集进 `authfile.go` 的 `saveCredentials`（导入链路）与 `login.go` 的授权回调（书签链路），与既有的 `ms_token` 采集落在同一批文件同一批函数，没有新建文件或目录。
- Cookie 过滤的删除只动 `cookiejar.go` 本体与它的测试；`completion.go` 的 `newUpstreamClient` 一行未改（它只调 `jar.Cookies`/`jar.seedFromHeader`，不感知过滤细节）。
- `main.go` 只改 `version` 常量一处，`VERSION` 只改内容，均为发版必需的最小改动。
- 依赖方向未变：`protocol.go` 不 import 任何新包（`strings` 原本就在用），`cookiejar.go` 删符号后 import 集合不变（`crypto/sha256` / `encoding/hex` 仍被 `fileDigest` 使用）。

## STYLE-08 语言与框架特异写法

- `fingerprintOf` 先判 `sa == nil` 再取字段，与文件内 `endpointURL` / `clientEndpointURL` 不判 nil 的既有写法**不同**——这是刻意的：这两条 URL 构造函数的调用方保证 `sa` 非 nil，而 `fingerprintOf` 是纯取值函数，未来可能被别的入口复用，判 nil 只花一行。
- `if fp != "" { q.Set("fp", fp) }` 用条件写入而不是先 `q.Set("fp", fp)` 再删空值：`url.Values.Encode()` 会把空值编码为 `fp=`，与「不带这个参数」在上游看来是两种请求。
- JS 侧取 Cookie 用 `document.cookie.match(/(?:^|;\\s*)s_v_web_id=([^;]*)/)`：`(?:^|;\s*)` 前缀避免匹配到 `xxx_s_v_web_id`，`decodeURIComponent` 还原 URL 编码值（实测真实值含 `%7C` 等转义）。
- 两个脚本里的 `try/catch` 都返回空串而不是抛错：取不到指纹时授权仍应完成（写入 `ms_token`），只在 `login extras missing` 日志里体现。
- 未新增 import；`tokenbeat_test.go` 新增用例复用已有的 `strings` / `testing`。

## STYLE-09 测试资产与编码前契约

| 用例 | 覆盖的不变量 | 本轮变化 |
| --- | --- | --- |
| `TestCookiesKeepsSessionCookiesForSiteHost` | 发往站点域名时必须保留 `sessionid` / `sessionid_ss` / `sid_tt` / `sid_guard` | 整块替换原 `TestCookiesFiltersSessionCookiesForSiteHost`（后者断言「必须剔除」，方向已错） |
| `TestEndpointURLCarriesRealFingerprint` | 两条 query 管线都必须带账号真实 `fp`，且不得出现 `verify_<deviceID>`；未采集时不得出现 `fp=` | 新增 |
| `TestFingerprintOfReadsAccountField` | `nil` → 空串；只有 `DeviceID` → 空串；带首尾空白 → 去空白 | 新增 |
| `TestBuildClientQueryOmitsFPWhenAbsent` | 未采集 `fp` 时 query 里不出现该键 | 新增 |
| `TestBuildClientQueryIsClientShaped` | 客户端 query 的 23 个键齐全且 `fp == 真实值` | 改三参调用，断言由 `verify_<deviceID>` 改为真实值 |
| `TestBuildClientQueryRotatesTabID` | `web_tab_id` 每轮换新 | 改三参调用 |
| `TestBuildBusinessQueryComplete` | 网页 query 的 19 键齐全且带 `fp` | 改五参调用，补 `fp` 与「空 fp 不出现」两条断言 |
| `TestSaveCredentialsWritesFile` | 导入链路必须把 `s_v_web_id` 写进账号文件并回读一致 | 补内存对象与落盘回读两条断言 |
| `TestBuildBookmarkletScriptKeepOriginPlaceholder` | 书签只允许为取 `s_v_web_id` 读 `document.cookie`，不得出现会话 Cookie 名 | Cookie 断言口径改写 |
| `TestBuildAuthScriptCarriesFingerprint` | 一次性脚本与固定书签都必须读 `s_v_web_id` 并回传 `svw=` | 新增 |
| `TestStatusCodeHint` | 9 条错误码文案各自含可操作关键词 | `710022004: "验证码"` → `"设备指纹"`；`710022002: "请求过快"` → `"s_v_web_id"` |
| `TestRunActiveTestRejectsStreamError` | HTTP 200 + STREAM_ERROR 帧必须判失败且提示可操作 | `strings.Contains(msg, "验证码")` → `"设备指纹"` |

**哨兵 A（fp 退回假值）**：把 `fingerprintOf` 临时改成 `return "verify_" + sa.DeviceID` 后重跑 `python scripts/cgo-shim-build.py doubao`：

```text
--- FAIL: TestEndpointURLCarriesRealFingerprint (0.00s)
    workmode_test.go:359: 网页管线必须带账号真实 fp，实际 "verify_7300000000000000001"
    workmode_test.go:362: fp 不得再由 device_id 拼假
    ...
--- FAIL: TestFingerprintOfReadsAccountField (0.00s)
FAIL	github.com/luode0320/cpa-workbuddy-plugin/doubao
[cgo-shim] FAILED: go test ./... (exit 1)
```

**哨兵 B（session 组仍被剔除）**：在 `Cookies` 里加临时循环跳过 `isSessionCookieName` 后重跑：

```text
--- FAIL: TestCookiesKeepsSessionCookiesForSiteHost (0.00s)
    cookiejar_test.go:421: 发往站点域名时必须保留 "sessionid"，实际 map[flow_cur_user_sec_id:true uid_tt:true]
FAIL	github.com/luode0320/cpa-workbuddy-plugin/doubao
[cgo-shim] FAILED: go test ./... (exit 1)
```

两处哨兵都真实进编译并能捕获方向性回归；恢复后 `all green (doubao)`（6.786s，多次复跑 6.78~6.90s）。

测试资产归位：所有用例仍在源码目录 `doubao/*_test.go`（白盒同包），没有新增 `test/` 目录或 `mock/`，符合本仓库既有形态。

## 无关 diff 与清理

- 未改动 `completion.go` / `credentials.go` / `sign.go` / `management.go` / `panel.html` / `scheduler.go` / `failover.go`。
- `git diff --stat doubao/` 为 **13 files changed, 421 insertions(+), 168 deletions(-)**，其中 `cookiejar.go` 的 104 行改动里有 80 行是删掉的黑名单实现与其测试支撑，属本轮目标本身。
- 未改动 `doc/4-bugs/` 下既有文档；本轮在 `doc/5-tests/` 追加第 22 节、在 `doc/3-实施/` 推进阶段表。
- 本轮临时资产（`F:\cpa-plugin\cpa-shim-manual\` 及其 `zz_ablate_test.go` / `zz_fpone_test.go` / `zz_sess_test.go` / `zz_alt_test.go`、`$env:TEMP\fmtchk0121` / `fmtchk0121b` / `fmtchk0121c`、`$env:TEMP\acct_v2.json`）在收口时清理。

## 未命中说明

- **STYLE-06（公共复用）未命中**：本轮新增的 `fingerprintOf` 只被 `endpointURL` / `clientEndpointURL` 两处调用，抽到公共层没有第二个消费者；删除的三个符号本来就是本文件私有实现。
- **STYLE-07（接口契约）未命中**：`/test-active`、`/accounts` 等对外 HTTP 接口的请求/响应结构一字未改；账号文件的 JSON **新增了一个可选键** `s_v_web_id`，但既有键名、类型、语义全部保持，`omitempty` 保证老文件继续可读，属向后兼容的加法。

## 边界声明

本 review 只判断代码风格、格式、位置、注释、日志、可读性与目录归位，
不判断业务正确性、上游行为结论或发布放行。业务结论见
`doc/5-tests/2026-11-14_173500_doubao签到与活动赠送额度验证.md` 第 22 节。
