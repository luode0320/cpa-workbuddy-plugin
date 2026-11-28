# Changelog

## 0.2.9 (2026-11-28)

- **面板「测试」支持选择模型**：账号卡片新增模型下拉（目录取自 `/models` 的五个别名），测试时把选中值随 `/test-active` 一起提交；后端 `handleMgmtTest` 早已支持 `model` 字段，此前面板未传，恒用默认模型 `glm/glm-5.1`。
- **面板显示账号积分与额度**：新增 `GET /credits?auth_id=<id>`，用 `access_token` 请求上游 `GET /chatglm/user-api/user/info`，取 `result.member_info` 的 `left_score`（剩余积分）、`token_left_percent` / `token_usage_percent`（额度百分比）、`member_status`、`score_rule`，并附 `fetched_at` 快照时刻。
- 积分按卡片懒加载（先渲染列表再逐张拉取），账号多时不阻塞首屏；读取失败就地显示原因并挂 `title`，不弹 toast。
- `Config` 新增 `user_info_endpoint` 可覆盖端点，便于本地 mock；该常量此前已定义但从未被引用，本次接入。
- 新增 `TestFetchCreditsParsesMemberInfo`、`TestFetchCreditsRejectsTokenWithoutDeviceID`、`TestFetchCreditsUpstreamError`。

## 0.2.8 (2026-11-28)

- **修复对话请求同样被上游判 `40004`**：`prepareStreamRequest` 的 `X-Device-Id` 原为随机串，与 0.2.7 修的刷新链路同源问题。上游 `/chatglm/backend-api/assistant/stream` 同样要求该头等于令牌载荷内的 `device_id`。现从 `access_token` 载荷解析后发送；解析失败在发请求前即报「access_token 缺少 device_id」。
- `deviceIDFromRefreshToken` 泛化为 `deviceIDFromToken`，刷新与对话两条链路共用同一解析；测试夹具 `fakeRefreshToken` 相应泛化为 `fakeToken(deviceID, kind)`。
- 新增 `TestPrepareStreamRequestUsesTokenDeviceID`（断言 `X-Device-Id` 来自载荷、`Authorization` 与会话头齐备）与 `TestPrepareStreamRequestRejectsTokenWithoutDeviceID`。

## 0.2.7 (2026-11-28)

- **修复 `refresh_token` 换取 `access_token` 必失败（上游 `40004 bad request`）**：`X-Device-Id` 原先发送随机串，上游要求它必须等于 `refresh_token` 的 JWT 载荷里编码的 `device_id`。新增 `deviceIDFromRefreshToken` 从载荷解析 `device_id` 并用作该请求头；载荷缺失时在请求上游前直接报「refresh_token 缺少 device_id」。
- **修复授权页提交后白屏**：`/login` 表单原以 `POST` 提交到 `/v0/resource/plugins/glm-provider/login/callback`，而 resource 路由只接受 `GET`，`POST` 返回 404 空响应体，前端 `document.write` 写入空串导致整页空白。改为 `GET` + `X-GLM-RefreshToken` 请求头（同源不触发预检，令牌不进 URL 与历史），并增加状态码判空兜底，失败时改为就地报错而非清空页面。
- 测试夹具改用 JWT 形 `fakeRefreshToken(deviceID)`，新增 `TestDeviceIDFromRefreshToken`（四种边界）与 `TestRequestTokenRejectsMissingDeviceID`，并在 `TestRequestTokenMock` 中断言 `X-Device-Id` 来自载荷。

## 0.2.6 (2026-11-28)

- 版本三轨统一：`main.go` 的 `version`、`VERSION`、`registry.json` 三处同值 0.2.6，消除 0.2.2/0.2.5 并存导致的发布歧义。
- 移除已改名的 `glm-web/` 旧目录，仓库只保留 `glm/`，避免双目录并存。
- 授权入口定型为 token 录入页（移除书签方案后的最终形态），面板内附 chatglm.cn 取 token 三步指引。

## 0.2.5 (2026-11-28)

- 恢复书签一键授权：`/login` 引导页 + 长期可复用书签，在 chatglm.cn 页面点一下即自动回调落盘，无需手工复制令牌。
- `/login/callback` 支持自定义请求头与 query 参数双通道回传 `chatglm_refresh_token`。

## 0.2.4 (2026-11-28)

- 面板自动解密宿主管理密钥（`cli-proxy-auth` 的 `enc::v1`/`enc::v2` 格式），不再要求用户手填。

## 0.2.3 (2026-11-28)

- 面板按 workbuddy 规格重构：卡片列表、筛选栏、汇总区、模态框、toast 提示，由 100 行扩至 503 行。

## 0.2.2 (2026-11-28)

- 授权入口由上游官网改为本插件落地页绝对地址：新增 `rememberPublicOrigin`/`currentPublicOrigin`，从 `X-Forwarded-Host`/`Host` 与 `X-Forwarded-Proto` 观测外部 origin，回环地址不覆盖。
- 移除书签方案：`chatglm_refresh_token` 存于 HttpOnly Cookie，网页脚本无法读取，`/login` 改为纯 token 录入页。

## 0.2.1 (2026-11-28)

- 管理接口改用标准 `pluginapi.ManagementResponse`（StatusCode/Headers/Body），修复面板白屏与 `/accounts`、`/models` 返回空 body。

## 0.2.0 (2026-11-28)

- 改名：插件 id `glm-web-provider` → `glm-provider`，源码目录 `glm-web/` → `glm/`，模型前缀 `glm-web/` → `glm/`。
- 修复面板白屏：面板响应改为标准 `pluginapi.ManagementResponse`（StatusCode/Headers/Body）。
- 新增一键授权：授权引导页 + 长期可复用书签，在 chatglm.cn 页面点一下书签即自动回调落盘，无需手工复制令牌；保留手工粘贴兜底。
- 新增 resource 路由：`/login`（引导页）、`/login/callback`（令牌接收，免管理密钥）。
- 资源路由前缀兼容 `/v0/resource/plugins/<id>`。

## 0.1.0 (2026-11-28)

首个版本：智谱清言网页版 refresh_token 反代插件。

- 鉴权：仅接受 `chatglm_refresh_token`，插件内以网页签名换 `access_token`（有效期 3600s），全程 token 鉴权，不支持扫码/Cookie 直登。
- 对话：OpenAI 兼容 `/v1/chat/completions`，同步与非流式；GLM SSE `parts[].content[]` 按全量快照增量去重，`text`/`think` 分离为 `content`/`reasoning_content`，`[DONE]`/`event: done` 立即收尾。
- 账号：token 型 `StoredAuth` 落盘，归并写入保留未知顶层键；面板粘贴 token 导入（附取 token 指引）与账号启停/删除/导出。
- 多账号：轮询调度 + 连续故障记账；流式轻量探活；模型前缀 `glm/` 与 zcode-provider 隔离。
- 用量：NDJSON feed 上报（provider=glm-provider，auth_type=refresh_token）。
- 上游 base/refresh/stream 端点可配置覆盖，便于协议漂移修正与本地 mock。
- 真实账号端到端验证（H-01/02/04/05）留待上线后 OAuth 登录账号时进行。
