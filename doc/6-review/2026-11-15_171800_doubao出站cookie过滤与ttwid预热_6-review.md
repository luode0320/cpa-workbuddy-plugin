STYLE: PASS

# 6-review：doubao 出站 Cookie 分流过滤与 ttwid 预热（0.1.19）

审查对象：`F:\cpa-plugin\doubao\` 下 10 个文件。
最近修改时间：2026-11-15 17:18:00。

## 静态路由命中

在 `D:\谷歌云盘\luode-skills` 下运行 `code-style-consistency-rules/scripts/static_owner_router.py`，
命中 7 步：STYLE-01 / STYLE-02 / STYLE-03 / STYLE-04 / STYLE-05 / STYLE-08 / STYLE-09。
未命中 STYLE-06（公共复用）与 STYLE-07（接口契约），理由见文末「未命中说明」。

## STYLE-01 静态格式与编码一致性

| 文件 | 字节 | BOM | 控制字符 | CRLF |
| --- | --- | --- | --- | --- |
| `cookiejar.go` | 18738 | 否 | 0 | 465 |
| `cookiejar_test.go` | 19934 | 否 | 0 | 562 |
| `credentials.go` | 11340 | 否 | 0 | 276 |
| `completion.go` | 44840 | 否 | 0 | 1145 |
| `completion_test.go` | 17488 | 否 | 0 | 452 |
| `active_ping_test.go` | 15603 | 否 | 0 | 381 |
| `protocol.go` | 21228 | 否 | 0 | 485 |
| `main.go` | 16516 | 否 | 0 | 457 |
| `CHANGELOG.md` | 51080 | 否 | 0 | 0（纯 LF，343 行） |
| `VERSION` | 6 | 否 | 0 | 无换行（`0.1.19`） |

`gofmt -l` 对 8 个 `.go` 文件零输出（转 LF 副本后校验）。

## STYLE-02 命名与符号引用一致

- `blockedHostCookieNames` 与既有 `sessionCookieNames` 成对出现，一个管「判登录态」、一个管「出站黑名单」，命名前缀一致、语义可区分。
- `fallbackSessionCookieName` 用 `fallback` 前缀表达「替代品」，与 `withFallbackCookie` / `hasFallbackSessionCookie` / `hasFallbackCookie` 三名同源。
- `isSiteHost` 对齐既有 `isSessionCookieName` / `isBlockedHostCookieName` 的判定函数命名风格。
- `warmUpFallbackCookie` / `warmUpTimeout` 与既有 `testPingText` / `activeTestTimeout` 一样，用「动作 + 对象」命名。
- `siteHostname` 与 `doubaoSiteHost` 同常量块并列，注释写明「必须保持一致」。
- `siteWarmUpOverride` 与既有 `accountInfoEndpointOverride` / `completionEndpointOverride` 同一命名范式（`xxxOverride`）。

## STYLE-03 注释分层与定义位置

- 新增常量 `warmUpTimeout` 用块注释说明取值依据（生产实测 0.3~0.9s），不只写「超时」二字。
- `Cookies` 方法内用 `// 1.` `// 2.` 步骤编号，并解释「只删不补会拿到空响应」这一非显然事实——这正是本轮最贵的知识。
- `blockedHostCookieNames` 上方注释点明「此组名同时是禁止发往 www.doubao.com 的黑名单，两个口径必须分开」，避免后来者误把两处合并。
- `statusCodeHint` 的文件级说明改写成「按出站 Cookie 触发」的新结论，并保留旧结论为什么作废。
- 所有改动函数的 `最近修改时间` 与 `改动原因` 均已更新，时间戳取真实操作时刻。

## STYLE-04 函数签名与参数结构

| 函数 | 签名 | 说明 |
| --- | --- | --- |
| `isBlockedHostCookieName` | `(name string) bool` | 纯判定 |
| `filterHostCookies` | `(cookies []*http.Cookie) []*http.Cookie` | 纯转换，原切片无剔除项时原样返回（零分配） |
| `hasFallbackSessionCookie` | `(cookies []*http.Cookie) bool` | 纯判定 |
| `withFallbackCookie` | `(j *durableJar) (cookies []*http.Cookie) []*http.Cookie` | 接收者方法，返回新切片不原地改 |
| `isSiteHost` | `(host string) bool` | 纯判定 |
| `warmUpFallbackCookie` | `(jar *durableJar, timeout time.Duration) error` | 副作用集中在这里 |

全部符合既有「判定 / 转换 / 副作用」三分法，没有把副作用塞进判定函数。

## STYLE-05 结构与落点

- Cookie 分流逻辑放 `cookiejar.go`（Jar 的职责范围），没有外溢到 `completion.go`。
- `siteHostname` 放 `credentials.go` 常量块（与 `doubaoSiteHost` 同处），避免在两处各写一份 `"www.doubao.com"`。
- 预热调用点放 `newUpstreamClient`（客户端构造处），符合「构造时补齐前置条件」的既有形态。
- 未新增文件、未新增目录、未改动依赖方向。

## STYLE-08 语言与框架特异写法

- `[]*http.Cookie` 切片操作用 `make([]*http.Cookie, 0, len(cookies))` 预分配。
- 预热请求复用 `sharedUpstreamTransport`（0.1.18 引入），没有新开 `&http.Client{}` 字面量。
- 用 `io.CopyN(io.Discard, resp.Body, 1<<12)` 丢弃响应体，保证连接可复用。
- `defer resp.Body.Close()` 紧跟错误判断之后。
- `strings.TrimPrefix(host, ".")` 处理域名前导点，符合 Cookie Domain 语义。

## STYLE-09 测试资产与编码前契约

新增 7 个用例覆盖四条不变量：

| 用例 | 覆盖的不变量 |
| --- | --- |
| `TestCookiesFiltersSessionCookiesForSiteHost` | 站点域名出站不得带 session 组 |
| `TestCookiesKeepsSessionCookiesForAccountHost` | 判据端点必须保留 session 组 |
| `TestCookiesInjectsFallbackCookieWhenMissing` | 过滤后必须补 ttwid |
| `TestCookiesSkipsFallbackWhenAlreadyPresent` | 已有 ttwid 不得重复补 |
| `TestWarmUpFallbackCookieFetchesTtwid` | 预热能把 Set-Cookie 落到 Jar |
| `TestWarmUpFallbackCookieSkipsWhenPresent` | 已有 ttwid 时预热不得发请求 |
| `TestStatusCodeHint`（扩充） | 9 条错误码文案断言 |

哨兵验证：把 `filtered := filterHostCookies(cookies)` 改为 `filtered := cookies` 后
`TestCookiesFiltersSessionCookiesForSiteHost` 立即失败（`cookiejar_test.go:416`），
证明用例真实进编译且能捕获该回归。

## 无关 diff 与清理

- 未改动 `sign.go` / `login.go` / `management.go` / `panel.html`。
- `doc/` 下新增 1 份 6-review 与测试文档第 19 节，无其他文档变更。
- 本轮所有临时文件（`zz_real_test.go`、`$env:TEMP\zzshim3\`、`utls3\` 等）
  已在收口时清理，`git status --porcelain doubao/` 只剩本轮 10 个目标文件。

## 未命中说明

- **STYLE-06（公共复用）未命中**：新增的 6 个小函数各自只服务一条调用链
  （`Cookies` 过滤链与 `warmUpFallbackCookie`），没有第二实现，抽公共层属过度设计。
- **STYLE-07（接口契约）未命中**：本轮没有新增或修改任何对外 HTTP 接口、
  宿主 RPC 形态或配置文件 schema；判据端点与令牌端点的请求/响应结构一字未改。

## 边界声明

本 review 只判断代码风格、格式、位置、注释、日志、可读性与目录归位，
不判断业务正确性、上游行为结论或发布放行。业务结论见
`doc/4-bugs/` 与 `doc/5-tests/` 对应章节。
