# 2026-11-28_233000 glm-provider 设备标识修复_6-review

## 1. 审查范围

本轮为 Bug 修复，改动 8 个文件（含 1 个新增文档），核心是两处 `X-Device-Id` 取值修正与一处授权页提交方式修正。

| 文件 | 改动性质 |
|---|---|
| `glm/token.go` | 新增 `deviceIDFromToken`，`requestToken` 改用解析值 |
| `glm/completion.go` | `prepareStreamRequest` 改用解析值 |
| `glm/login.go` | 表单提交 POST → GET，增状态码兜底 |
| `glm/token_test.go` | 夹具 `fakeToken`、新增 2 个测试、断言 `X-Device-Id` |
| `glm/completion_test.go` | 新增 2 个测试 |
| `glm/main.go` | `version` 0.2.6 → 0.2.8 |
| `glm/VERSION` | 同上 |
| `glm/CHANGELOG.md` | 新增 0.2.7 / 0.2.8 两节 |

## 2. 注释规范

- `deviceIDFromToken` 具备 `[参数]` / `[返回]` / `最近修改时间` 三项元信息，且说明了「为什么」——refresh 与 access 载荷都带该字段、上游要求一致。
- `prepareStreamRequest` 的函数头注明「X-Device-Id 必须取自 access_token 载荷，随机值会被上游判 40004」，把踩过的坑写进注释而不是只写行为。
- 修改既有函数时同步更新了 `最近修改时间` 与改动原因，未留旧描述。
- 新增测试函数均有一行中文说明其防回归意图（`TestPrepareStreamRequestUsesTokenDeviceID` 明确写出「随机设备标识会被上游判 40004」）。

判定：符合注释规则。

## 3. 命名

- `deviceIDFromRefreshToken` → `deviceIDFromToken`：改名是因为语义真实扩大（不再只服务 refresh），不是为改而改；调用点全部同步，无遗留旧名（`token_test.go:17` 注释中的旧名是「由 X 泛化」的历史说明，属正当引用）。
- 测试夹具 `fakeRefreshToken` → `fakeToken(deviceID, kind)`：新增 `kind` 参数区分 refresh/access，避免再造第二个近似夹具。
- 无 `myX` / `tmpX` / `data2` 一类占位命名。

判定：符合命名规则。

## 4. 错误处理

- 两处解析失败均在**请求上游之前**返回带行动指引的中文错误（`refresh_token 缺少 device_id，请从 chatglm.cn 重新复制` / `access_token 缺少 device_id，请重新授权`），不再把上游 `40004` 原样抛给用户。
- `deviceIDFromToken` 的三处失败分支（非三段式 / base64 解码失败 / JSON 解析失败）统一返回空串，由调用方决定语义；未吞异常、未 panic。
- 前端新增 `if(!res.ok||!res.body)` 兜底，失败时就地提示 `回调失败 HTTP <code>`，不再 `document.write("")` 清空页面。

判定：符合错误处理规则。

## 5. 最小改动与架构

- 未引入新依赖、新包、新抽象层；`refreshHeaders` 签名未变，只换调用方传值。
- `prepareStreamRequest` 三个调用点（`executor.go:87` / `executor.go:209` / `management.go:329`）签名不变，改动零外溢。
- 授权页由 POST 改 GET 是**服从宿主 resource 路由 GET-only 约束**的正解，不是绕过；未为此新开 management 通道或加白名单。
- 未打补丁式双轨：没有保留「随机 device_id 失败后重试正确值」这类兜底，直接消灭错误来源。

判定：符合最小改动与可读性要求。

## 6. 测试

| 测试 | 作用 |
|---|---|
| `TestDeviceIDFromToken` | 合法载荷 / 非三段式 / 无 `device_id` / 非法 JSON 四边界 |
| `TestRequestTokenRejectsMissingDeviceID` | 刷新链路在发请求前拒绝无 `device_id` 令牌 |
| `TestRequestTokenMock`（增强） | 断言 `X-Device-Id` 等于载荷值，防回退到随机串 |
| `TestPrepareStreamRequestUsesTokenDeviceID` | 对话链路 `X-Device-Id` 来源 + `Authorization` + 签名头齐备 |
| `TestPrepareStreamRequestRejectsTokenWithoutDeviceID` | 对话链路拒绝无 `device_id` 令牌 |

单测由 15 增至 19，全部 PASS。夹具改 JWT 形使新旧路径都能被测到，未出现「为过测试而放宽断言」。

判定：测试覆盖到位，无生产代码为测试让步。

## 7. 格式与风格一致性

- `gofmt -l`：18 个 Go 文件 PASS。
- 局部风格延续既有写法：函数头元信息块、`h.Set(...)` 链式构造头、`fmt.Errorf("...: %w", err)` 包装、测试用 `httptest.NewServer` + `defaultConfig()` 覆盖端点。
- 无 `iota`、无函数内 `var (...)`、无无名闭包替代具名函数的新增。
- 中文注释，UTF-8 无 BOM，行尾与仓库既有 CRLF/LF 约定一致。

## 8. 结论

`STYLE: PASS`

本轮改动集中在两处取值来源与一处 HTTP 方法，逻辑面积极小、证据链完整（探针矩阵 → 代码修正 → 单测锁定 → 生产端到端），未触碰调度、用量统计、模型目录等无关模块。生产已热重载 0.2.8，真实账号对话验证通过。