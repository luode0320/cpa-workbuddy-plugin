# Changelog

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
