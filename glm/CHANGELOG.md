# Changelog

## 0.3.2 (2026-11-29)

**修复非流式回答重复三遍。**

- **现象**：非流式请求问「只回答两个字：收到」，返回 `收到收到收到`。
- **根因**：上游每一帧 `parts` 携带的是**全量快照**而非增量（实测应答只有 3 帧：`init` / `finish` / `finish`，每帧都含完整 `收到`）。流式路径用 `parseGLMFrame` 按快照长度求增量、有去重；`convertNonStream` 却逐帧直接 `WriteString(c.Text)`，于是把 3 帧的全量快照拼成 3 份。
- **修复**：`convertNonStream` 改为复用 `parseGLMFrame` 的快照增量算法，只在快照变长时追加新增部分。
- 新增回归测试：`TestConvertNonStreamDedupsSnapshots`（三帧同一快照只出一份）、`TestConvertNonStreamAccumulatesGrowingSnapshots`（快照真增长时正确累积为最长版本）。

## 0.3.1 (2026-11-29)

**修复 0.3.0 的目录污染：模型目录被历史会话里的垃圾 chat_mode 撑成 27 项。**

- **根因**：0.3.0 的 `discoverChatModes` 无过滤采信历史会话里的 `chat_mode`，再用它给每个真实模型拼后缀生成条目。而该字段是客户端可任意写入的——协议探测期间留下的 `zzz-bogus`、`___definitely_not_a_real_value___`、`glm-5.3`、`turbo` 等全部被当成「模式」写进目录，线上实际产出 27 个模型（含 `glm/glm-5.3-zzz-bogus`、`glm/glm-5.3-flash-___definitely_not_a_real_value___` 等 19 个垃圾条目）。
- **白名单过滤**：新增 `isKnownChatMode`，历史发现只放行已知模式（`""` / `zero` / `chat_agent` / `deep_research`），其余一律丢弃，不再污染目录。
- **发现结果不再扩目录**：`discoverChatModes` 的产出只用于告警提示（`unknownChatModes`），不再参与模型条目生成。目录只由 `modelCatalog` 决定。
- **模式别名改为可选开关**：新增配置 `expose_mode_aliases`（默认 `false`）。默认状态下目录严格等于网页模型菜单的两项；需要 `-think` / `-agent` / `-research` 后缀时显式开启。`resolveUpstreamTarget` 即使开关关闭也能解析这些后缀，老调用方不受影响。
- 新增回归测试：`TestDiscoverChatModesFiltersUnknown`（垃圾取值被过滤）、`TestDynamicModelInfosIgnoresPollutedHistory`（污染历史不得扩出假模型）、`TestAllModelEntriesWithoutModeAliases`（默认恰好 2 项）、`TestAllModelEntriesWithModeAliases`（开启后 ID 仍派生自真实模型）。

## 0.3.0 (2026-11-29)

**重大修正：删除全部虚构模型名，只暴露网页真实存在的两个模型。**

此前 0.2.x 暴露的 `glm-4-plus`、`glm-5.1`、`glm-4.7`、`glm/zero`、`glm/chat_agent`、`glm/deep_research` 全部是编造的名字，网页模型菜单里一个都没有 —— 用户反馈「网页只有 2 个模型，插件一个都没对上」，属实质缺陷。

- **确认网页只有两个模型**：`GLM-5.3` 与 `GLM-5.3 Flash`。
- **确认唯一可用的模型开关是 `if_plus_model`**：注入实验证明 `model`、`model_id`、`chat_model`、`glm_model`、`subscribe_id`、`is_old_model` 等字段被上游完全忽略（传非法值不报错、不记录）；只有 `if_plus_model` 会被回显并写入会话记录，`chat_mode` 会被记录。故 `if_plus_model=true` → GLM-5.3，`false` → GLM-5.3 Flash。
- **模型与模式分离**：`modelCatalog` 只放两个真实模型；`chatModeCatalog`（标准 / 思考 / 智能体 / 深度研究）是正交的「模式」维度，按后缀拼到模型名上（`glm/glm-5.3-think`），不再把内部取值当模型名暴露。
- **未知模型名不再透传**：`resolveUpstreamTarget` 对未收录的名字回落到 GLM-5.3，绝不把任意字符串当 `chat_mode` 发出去（旧实现会把 `totally-bogus-model` 原样发送）。
- **顺带修复强制联网**：`completion.go` 的 `is_networking` 由硬编码 `true` 改为 `false`，与网页默认一致。
- 新增契约测试：`TestModelCatalogOnlyRealModels`（模型目录只能包含网页那两项）、`TestAllModelEntriesOnlyUseRealModelNames`（展开后的 ID 必须由真实模型派生）、`TestResolveUpstreamTargetUnknownDefaultsToGLM53`（未知名字不得变成 chat_mode）、`TestResolveUpstreamTargetMapsRealModels`（两个模型与模式后缀的映射）。

## 0.2.15 (2026-11-29)

修复导出文件只有 `refresh_token`、没有 `access_token` 的缺陷：

- **根因**：`acquireToken` 刷新成功后只写内存缓存 `globalTokenCache`，从不回写 `StoredAuth.AccessToken`；`StoredAuth` 虽有 `access_token` / `expires_at` 字段（带 `omitempty`），但磁盘上永远为空，导出自然是空的。
- **回写**：新增 `persistAccessToken`，刷新成功后按 `refresh_token` 反查账号并落盘 `access_token` / `expires_at` / 轮换后的 `refresh_token` / `updated_at`。回写失败只记日志，不阻断调用（令牌已在内存可用）。
- **反查**：新增 `findAuthByRefreshToken`，因为刷新发生在推理路径上，此时手上只有 `refresh_token`。
- **导出补齐**：`handleMgmtExport` 导出前对缺 `access_token` 或已过期的账号补刷一次（未过期则沿用），保证导出文件是完整令牌对。
- 新增 `persist_test.go`：`TestPersistAccessTokenWritesFile`（回写前后对比磁盘）、`TestPersistAccessTokenSkipsEmpty`（空令牌不覆盖）、`TestFindAuthByRefreshTokenMatches`（空/不存在令牌不误匹配）。

## 0.2.14 (2026-11-29)

面板按 workbuddy 规格重构，从「能用」补到「可运维」：

- **积分进度条**：`progressHTML` 按 workbuddy 同构渲染「可用 N 分 · 已用 X% · 额度池 · 快照时刻」，按已用比例着色（>60% 黄、>85% 红），未拉到数据时显示 spinner 加载态；`score_rule` 挂在 meta 的 title 上做说明。
- **筛选标签增加「低积分」**：阈值 `LOW_CREDIT_THRESHOLD=20` 单一来源，筛选计数与卡片警示共用，避免两边漂移。
- **概览增加「剩余积分合计」**：`renderSummary` 汇总已知积分，未知时显示读取中。
- **四种排序**：`创建 ↓ / 创建 ↑ / 积分 ↑ / 积分 ↓` 轮转，按钮文案与 tooltip 同步；后端 `accountSummary` 补 `created_at` / `updated_at` 两个字段，否则排序无据。
- **批量操作**：「全部测试」并发探测所有未停用账号并把结果写回列表；「刷新积分」串行重拉全部账号积分，顶部显示 `刷新积分 n/m` 进度。
- **单卡「刷积分」**：`manualRefreshCredits` 清缓存后重拉，即时 toast 反馈。
- **增量刷新**：`patchCardProgress` 只替换单卡进度条区域，不再整表重绘，避免打断滚动与闪烁。
- 积分缓存 `creditCache` 按 auth_id 存储，`load()` 只补缺失项，不每次全量回源。

## 0.2.13 (2026-11-29)

- **修复默认档重复**：上游会话历史把默认档记成 `chat_mode=normal`，而请求侧传 `""` 与 `normal` 行为一致，导致目录同时出现 `glm/glm-4-plus`（空档）与 `glm/normal` 两个同档项。新增 `normalizeChatMode`，把 `normal`（含大小写与空白变体）归一到 `""`；`resolveAssistantID` 同步处理，`glm/normal` 请求归一到默认档。
- 新增 `TestNormalizeChatModeCollapsesDefault`、`TestEffectiveChatModesDedupesNormal` 锁定该归一化。

## 0.2.12 (2026-11-29)

- **模型目录真正动态化，上游新增档位无需改插件**：新增 `discoverChatModes`，从账号历史会话 `POST/GET /chatglm/backend-api/assistant/conversation/list` 的 `meta_data.chat_mode` 中发现上游实际用过的档位。历史里出现过的任何取值都会被纳入目录，未知档位原样透传，因此上游上线新模型时插件下次刷新即自动获得。
- **补上遗漏的 `chat_agent` 档位**：实测该账号 46 条历史会话里 `chat_mode` 真实取值为 `chat_agent` / `normal` / `zero`，其中 `chat_agent` 是旧代码完全没有的档位（旧代码只认 `zero` 与 `deep_research`），这就是「模型对不上」的直接原因之一。
- **模型名改为档位直映射**：`glm/<chat_mode>` 成为对外模型名（`glm/chat_agent`、`glm/zero`、`glm/deep_research`、`glm/glm-4-plus` 表示默认档）；`resolveAssistantID` 对未知名字不再丢弃，而是当作 `chat_mode` 透传，避免新档位被静默降级成默认档。
- 档位发现带 10 分钟缓存（`modeDiscoveryCache`），避免每次打开面板都扫历史；扫描覆盖默认智能体与账号自建智能体。
- `glm-5.1` / `glm-4.7` 降级为纯兼容别名（`legacyAliasModels`），不再混在真实档位里误导。
- `Config` 新增 `conversation_list_endpoint`；`baselineModelInfos` 作为无账号或回源失败时的兜底目录。
- 测试重写为动态语义：`TestDiscoverChatModesFromHistory`（含未知档位）、`TestEffectiveChatModesUnionsBaselineAndDiscovered`、`TestDynamicModelInfosIncludesDiscoveredModes`（新档位自动进目录）、`TestChatModeModelIDRoundTrip`、`TestResolveAssistantIDMapsModes`。

## 0.2.11 (2026-11-29)

- **模型目录改为接口动态获取**：此前 `/models` 只回放 `defaultModels` 硬编码数组，从不回源。现在 `handleMgmtModels` 取指定（或首个可用）账号的 `access_token`，调 `POST /chatglm/backend-api/assistant/list` 拉取自建智能体并按 `glm/<assistant_id>` 追加；请求需带 `page` / `page_size`（缺省上游返回 400 校验错误）。
- **如实标注等价别名**：`glm-5.1`、`glm-4.7` 在上游与 `glm-4-plus` 完全等价（同一 `assistant_id` + 同一 `chat_mode`），此前五个名字看起来是五个模型。现改为 `modelCatalog` 结构，DisplayName 直接写「GLM 标准（glm-4-plus 同档）」，面板下拉据此显示；`glm-4-plus` / `glm-think` / `glm-deepresearch` 保留为真实三档。
- **`if_plus_model` 不单列**：实测 true/false 对响应形态无影响（均返回 `text` 段），真正区分档位的只有 `chat_mode`（`normal` / `zero` / `deep_research`），故不把该布尔当模型维度。
- `resolveAssistantID` 支持 `glm/<assistant_id>` 形式，先剥离前缀再匹配 ID 正则，使动态智能体可直接作为模型名使用。
- `Config` 新增 `assistant_list_endpoint` 可覆盖端点；回源失败降级为纯预置目录并返回 `warnings`，不影响面板可用性。
- 新增 `TestListUpstreamAssistantsParsesList`、`TestDynamicModelInfosAppendsAssistants`、`TestDynamicModelInfosFallsBackOnError`、`TestResolveAssistantIDSplitsGLMPrefix`、`TestModelCatalogMarksEquivalentAliases`。

## 0.2.10 (2026-11-29)

- **修复 `/credits` 取不到 `auth_id`**：宿主下发管理请求时可能只给纯路径（查询串单独放在 `req.Query`），而 `normalizeMgmtSubPath` 只从 `url.Parse(req.Path)` 取查询，导致 `/credits?auth_id=...` 恒返回 `auth_id required`。新增 `mergeRequestQuery` 合并两个来源，宿主下发的值优先。
- 移除 `handleManagement` 中原本显式忽略查询参数的 `_ = urlQuery` / `_ = req.Query` 两行占位。
- 新增 `TestMergeRequestQueryPrefersHostQuery`（三种合并组合）与 `TestNormalizeMgmtSubPathCredits`（management / 短前缀 / resource 三种前缀都归一到 `/credits`）。

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
