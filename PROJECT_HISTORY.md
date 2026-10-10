# 项目历史事件

> 本文件追加关键历史事件并只保留最近 20 条（按日期倒序、新事件置顶、追加后自动裁剪）；普通启动默认不读取，只有历史追问、当前状态不足或真实卡点时才窄检索。

## 事件

- 2026-10-11：qoderwork-provider **0.9.26 修复宿主 HTTP 桥状态码解码错误（签到假成功根因，与 qoder-ai 0.1.3 同源）并发布部署**——用户报错：生产 QoderWork 账号 `u09a5b6ab` 可签到但「签到好像有 bug，无法签到」，面板显示签到成功却拿不到积分（假成功）。根因：`qoderwork/host_bridge.go` 用 `json:"status_code"` 解码宿主 `host.http.do` 响应，而宿主 v7.2.x 序列化 `pluginapi.HTTPResponse` 无 json tag，线协议键名是 PascalCase `{"StatusCode":404,...}`；下划线标签既不精确匹配也不构成大小写不敏感匹配 → `StatusCode` 恒为 0 → `if resp.StatusCode >= 400` 永不触发 → 404/4xx/5xx 被当 200 成功；`performCheckinCall` 再对缺 `success` 字段的响应盲归一化 `m["success"]=true` → 对外「签到成功」但积分不变。修复：移植 `parseHostHTTPDoResult`（PascalCase 优先 + 下划线防御性回退）；`performCheckinCall` 缺 `success` 布尔字段即返回 `success:false`。本地 `cgo-shim-build.py qoderwork` 全绿 + 反证（改回旧标签必失败，`StatusCode = 0, want 200`）。发布链：`72ce2d0` → CI run `38064786540` 65/65 success → `fb07a16`（8 资产）→ `712cfc6`（registry 0.9.26）→ `c37fea5`（prune 0.9.25）→ `20e6b01`（文档）；远端 7 平台 zip sha256 全等、旧版资产 404。生产部署：`plugin-store install` 0.9.26 落盘 `.so` sha256 `53a909b0e84bc70483db55ee94bfb928345eb3bdc0eacce4f60f396aa0fd29c1` 与本地 zip 一致，日志 `plugin loaded/registered version=0.9.26`；行为验收生产账号 `u09a5b6ab`（`6a155a864014ffd8`）调用 `/checkin` 返回 `success:false` + 真实上游 `http 409 AlreadyExists`（summary fail:1），由「假成功」变为如实失败。

- 2026-10-10：traework-provider **0.2.5 发布部署与签到失败双根因修复**（`x-device-id` 16 位数字风控 + 自动签到调度器相位对齐）——用户诉求：「修复 TraeWork 签到失败，用生产账号『用户04878311608』测试」。生产对照实验锁定两项根因：① `deviceIDFor` 拼接 `<baseDeviceID>-<userID>`（33 字符含 `-`）及旧版 `randomDeviceID()` 尾零填充/首位越界（如目标账号 `1114256688551036` 的 `deviceId=9670064000000000`）命中 `/trae/api/v2/ug/checkin_credits/claim` 设备指纹风控，100% 返回 9074「当前参与用户太多，请稍后再试」，且重试复用同一被拦截 ID；② `autoCheckinLoop` 原用 `time.NewTicker(1min)` + `now.Minute()==0` 相位错配导致自动签到漏触发。修复：`deviceIDFor` 改为按 `(baseDeviceID, userID)` SHA-256 确定性派生首位 1~3、末位 1~9 的 16 位纯数字设备号；`randomDeviceID` 改用 `crypto/rand` 直接映射 16 位数字；`checkinAccount` 遇 9074/9095 自动切换新 16 位设备号重试；`autoCheckinLoop` 改为 `nextAutoCheckinTime` + `time.NewTimer` 绝对时间对齐。本地 `cgo-shim-build.py traework` 全绿（含必失败哨兵）+ 6-review `STYLE: PASS`。发布链：`54f490a` → CI run `38059677609` 64 jobs success → `6291b2a`（8 资产 ALL CHECKSUMS OK）→ `2ffcdc9`（registry 0.2.5）→ `74dfe92`（prune 0.2.4）；远端 7 平台 raw URL 全 200。生产部署：`plugin-store install` 热重载 `active_version=0.2.5 retired_version=0.2.4`，落盘 `.so` SHA-256 `f24fe5f42cf6203cb8f69f68b6f1edfb1b1d9b56e7cc62221a24ddb5fbdbf4eb` 与本地一致；目标账号「用户04878311608」（`76bc7754f3fd72b2`）签到成功（积分包 2→3，剩余积分 130→230），单账号与全量 7 账号调用 `/checkin` 均返回 `ok: true`（`checked_in: 7, fail: 0`）。

- 2026-10-10：qoder-ai-provider **0.1.3** 修复宿主 HTTP 桥状态码解码错误（签到假成功根因）——用户报错：生产账号 u8e6a5348（393 积分）与 ua554edc3（0 积分）签到显示「成功 +100」但积分不变。根因：`host_bridge.go` 用 `json:"status_code"` 解码宿主响应，宿主 v7.2.x 未加 tag 输出 PascalCase `{"StatusCode":404,...}`，`StatusCode` 恒为 0 → 404 被当成功；`performCheckinCall` 再盲归一化 `m["success"]=true`。修复：移植 workbuddy `parseHostHTTPDoResult`，收紧归一化。本地 `cgo-shim-build.py qoder-ai` 全绿 + 反证（改回旧标签必失败）。产品事实：国际版 openapi.qoder.sh 无签到端点（404），app.asar 无签到代码，奖励体系为 campaigns（均不可领），**已发布部署并生产验收（2026-10-10）**：提交 `3fbe653`→assets `6bb57b5`→registry `746b1ef`→prune `d405ebd`；CI run 38062071659 success；远端 7/7 资产 sha256+size 全等、被删 0.1.2 资产 404；生产 plugin-store install 0.1.3 落盘 `.so` sha256 `e7f7537d…a46e0` 与本地 zip 一致、日志 `plugin loaded/registered version=0.1.3`；行为验收三账号 `/checkin` 由「假成功 success:true」变为如实 `fail:1 + http 404 NotFound`，缺陷 A 已修（不再假成功）。真实签到入口仍待产品确认（GAP-001）。

- 2026-10-10：qoder-ai-provider **0.1.2** / qoderwork-provider **0.9.25** content 多形态解析根因修复（真实推理 503）——用户报错：生产调用 `qwen-3.8-flash` 返回 `503 auth_unavailable: ... last upstream error: payload parse: json: cannot unmarshal array into Go struct field ***.***.content of type string`，而面板「测试」按钮通过。根因：两插件 `body.go` 的 `openAIMessage.Content` 声明为强类型 `string`，客户端按 OpenAI 多模态规范发送部件数组 content（`[{"type":"text","text":"..."}]`）时 `json.Unmarshal` 直接失败 → `handleExecExecute`/`handleExecStream` 进入 `payload parse` 失败分支 → 对外 503；测试按钮走 `sendActivePingQoder` **直接构造结构体**（`Content: "hi"`）绕过 JSON 解析，故测试通过而真实请求必失败（两条路径不同）。修复：为 `openAIMessage` 增加自定义 `UnmarshalJSON`（`decodeOpenAIContent`），纯字符串原样接收、部件数组提取 `text`/`input_text` 拼接、null/缺省归一空串；`Content` 字段类型与下游签名零改动。对照：workbuddy/workbuddy-ai 走 `map[string]any` 泛型解析 + `rewriteContentField` 已显式处理两种形态，不受影响；traework/cursor 亦已支持数组。本地：两插件各新增 `body_test.go`（4 项），cgo-shim build/vet/test 全绿；**修复前必失败已分别回退修复块复跑反证**，报错文本与生产 `last upstream error` 一致。**本轮关键冲突**：首次提交 `65f554c` 时 qoderwork 版本定为 0.9.24，但并行会话已于 21:57:35（`cafc4cf`）发布 `qoderwork-provider-v0.9.24`（tag→`bc365e6`，**不含**本次修复）并回填 registry，故改发 **0.9.25**（`27d6c99`）避免复用已存在 tag；已取消撞 tag 的两个 queued run（38057923742/38057921816）。文档：Bug 主文档 `doc/4-bugs/2026-10-10_215813_Qoder插件多模态content数组致推理503.md`、测试主文档 `doc/5-tests/2026-10-10_215813_Qoder插件content多形态解析回归.md`、6-review `STYLE: PASS`。

- 2026-10-10：qoderwork-provider **0.9.23** 发布部署（面板「测试」按钮改为弹出模型选择窗口按指定模型测试）——用户诉求：qoderwork 与三插件对齐，卡片「测试」不再随机挑模型，改为点击后弹出该账号支持的模型小窗口、点选指定模型再测。后端：qoderwork/models.go 新增 authModelsForIndex + handleModelsQuery（cachedDynamicModels → callModelsAPI，AccessToken 非空前置判断），management.go 新增只读 GET /models?auth_index= 路由（未进 mutatingManagementPath），active_ping.go 的 handleTestActiveWithAuth(sa, authIndex, model) 支持指定模型（model 空保持随机兼容旧面板），兜底模型 auto；前端 panel.html 新增测试弹窗（openTestModal / closeTestModal / onTestMaskClick / loadTestModels / runTestModel）并补 .model-list/.model-item 等 7 条 CSS。本地：cgo-shim build/vet/test 全绿 + 必失败哨兵确证真实进编译；Node vm 回归 17 项 PASS + 旧版反证 FAIL（TypeError openTestModal is not a function）；6-review STYLE: PASS。发布链 7df7f9a（13 文件）→ CI run 37964833825 success（head=7df7f9a）→ 7f18e2a（8 资产，7/7 sha256 OK）→ registry 0.9.23 回填（随 30dfe9b 一并推送，7 artifacts sha256+size 与 checksums 全等）；远端 raw 7/7 200 且 size 一致。生产：plugin-store install 0.9.23 installed，落盘 .so sha256 bb55d2df…3977 与本地 zip 内 .so 完全一致；热重载 active_version=0.9.23 retired_version=0.9.22；行为验收 GET /models?auth_index= 返回 {"models":["auto"]}、POST /test-active 指定 model=auto 成功（421ms，回显该 model）、无 model 走兼容随机路径同样成功（622ms）、panel/accounts 均 200、面板资源含 testModal/openTestModal/loadTestModels/runTestModel/data-action="test" 全部命中。**重要踩坑**：期间遇到生产自定义源被别的会话半成品条目（qoder-ai-provider `direct` + 空 artifacts）整体校验失败，导致该源全部插件 install 报 plugin_not_found；根因是宿主 ParseRegistry 对单源 registry 原子校验、任一条目非法即整源不入列，待对端补齐 artifacts 后源自动恢复，无需重启宿主。发布后清理：prune dry-run 确认 8 插件保留集完整、待删 0。

- 2026-10-09：workbuddy-provider **0.15.4** / workbuddy-ai-provider **0.1.9** / traework-provider **0.2.3** 发布部署（三插件测试按钮改为弹出模型选择窗口按指定模型测试）——用户诉求：面板卡片「测试」按钮原为随机挑模型直接测，改为点击后弹出该账号支持的模型小窗口、点选指定模型再测。后端：三插件 models.go 新增 authModelsForIndex + handleModelsQuery，management.go 新增只读 GET /models?auth_index= 路由，active_ping.go 的 handleTestActiveWithAuth(sa, authIndex, model) 支持指定模型（model 空则保持随机，兼容旧面板）；前端：三插件 panel.html 新增测试弹窗（openTestModal / loadTestModels / runTestModel），无模型时提示「暂无可用模型」并允许关闭。本地：cgo-shim 三插件 build/vet/test 全绿 + 必失败哨兵确证真实进编译；三面板 Node vm 回归各 17 项 PASS + 旧版反证 FAIL；6-review STYLE: PASS。发布链 ce6b4e2（32 文件）→ CI 三 run success（37940758486 / 37940778484 / 37940786472，head=ce6b4e2）→ fe5742d（24 资产，三目录 7/7 sha256 OK）→ 207e22c（registry 回填）→ ada831f（prune 旧版 0.15.3 / 0.1.8 / 0.2.2）；远端 49/49 当前资产 200、被删旧版 404。生产：三插件 plugin-store install 全部 installed，落盘 .so sha256 与本地 zip 完全一致（workbuddy 0.15.4 9c3b85b6… / workbuddy-ai 0.1.9 dd77aab4… / traework 0.2.3 2d671e16…），热重载 active_version 命中目标版本，panel / accounts 三插件全 200；行为验收 /models 三插件返回真实模型（17 / 27 / 大量）、缺 auth_index 返回 auth_index is required、/test-active 指定 deepseek-v4.1-flash 在 traework 成功（1.625s，回显该 model），workbuddy 因额度 429 但错误文案确认使用指定模型。

- 2026-10-09：workbuddy-ai-provider **0.1.8** 发布部署（国际版每日签到替换失效加油包）——面板把已失效的「领取专家加油包」入口替换为国际版每日签到（状态查询 + 领取积分），新增自动签到开关（00/04/08/12/16/20 六时段）与批量签到；发布链 583bb05→b4e3a3e（8 资产 7/7 sha256 OK）→45f6ce5（registry）；CI run 37836464993 全 57 jobs success；生产热重载 active_version=0.1.8 retired_version=0.1.7、落盘 .so sha256 baf416d3…c49a 与本地 zip 一致；生产 /checkin 按契约透传上游结果（code=10001 签到活动未开启，未伪造成功），四路请求头对照 T0-T3 零差异 + 全 11 账号 active=false + CN 域反证（CN active=true）+ banner 12302 判定为上游活动离线，登记 GAP-001 环境性阻断。

- 2026-10-09：workbuddy-token-usage **0.2.4** 发布部署——移除面板 SSE 短连接轮询（每 2s EventSource 重连）与 15s 定时轮询，改打开时单次加载 + 手动刷新，默认时间范围改「最近 1 小时」；补图标 assets/icons/TokenTracker.png。发布链 aab549c→37de5e4（8 资产 7/7 OK）→cd8b642（registry）→63a9879（prune 0.2.3）；CI run 37812435637 全 57 jobs success；远端 0.2.4 资产 200 + sha256 一致、0.2.3 已 404；生产热重载 active_version=0.2.4 retired=0.2.3、落盘 .so sha256 8ce19240… 与本地 zip 一致；生产面板 /usage 200（EventSource=0/setInterval=0/last_1_hour×7）。

- 2026-10-07：cursor-provider **0.1.0** 发布部署与端到端验收——发布闭环：commit 44d2bb4（162 文件）推送后派发 CI run 37636155616，全 57 jobs success；下载 8 资产（7 平台 zip + checksums）SHA256 校验 ALL OK（commit a17c2aa）；publish-assets 回填 registry（commit 92f569c）；远端 raw 7 资产 size+sha256 ALL PASS；生产 plugin-store install 0.1.0，落盘 .so sha256 bae19b38 与本地 zip 100% 一致，容器日志 plugin loaded + plugin registered 热重载成功，plugin-store 状态 installed/registered/enabled/effective_enabled 全 true。生产端到端验收：真实 token 导入成功（auth_index 9c0e4081ebf23fa7 → cursor-6c2463e563a13c02.json，面板可见 246 模型），重复导入正确去重；推理验收 cursor/default 非流式 3 次 + 流式（1..5 完整 + finish_reason:stop + [DONE]）+ 多轮对话全部成功；export 接口 200。高级模型（gpt-5.3-codex / composer-2.5 / claude-4-sonnet / gemini-3.x 等）统一返回上游 resource_exhausted（429，约 280ms 快速拒绝），判定为 Cursor 服务端对该账号订阅的配额限制，插件按真实 429 语义透传，非移植缺陷。发布后清理：release-assets prune dry-run 确认保留集完整、无待删；本地缓存 .workbuddy/release-assets 已删除。

- 2026-10-07：发布后清理规则固化与 release-assets 瘦身——把「每次发布后清理不需要的垃圾」吸收为项目规则与 skill：新增项目 skill project-cpa-workbuddy-plugin-release-asset-prune-rules（保留集=registry 每插件当前版本目录）+ 通用脚本 scripts/prune-release-assets.py（--dry-run/--apply，受跟踪目录 git rm、空目录 rmdir）；AGENTS.md/CLAUDE.md「仓库与发布」小节新增「发布后清理」条；发布 skill 增 Step 13.5。实操：release-assets 从 3.75 GB（191 版本目录）清理到 0.13 GB（保留 registry 当前 6 个版本目录：workbuddy-provider-0.15.3 / qoderwork-provider-0.9.22 / traework-provider-0.2.2 / workbuddy-ai-provider-0.1.7 / workbuddy-token-usage-0.2.3 / gemini-provider-0.1.1），删 185 个历史版本目录 / 1469 文件，提交 095f4a4 推送 origin/main；远端抽查当前版本 raw 200、旧版本 404。

- 2026-10-07：cursor-provider **0.1.0** 移植与 Token 导入——新增第六个插件 cursor-provider（来源 yobo2u/omsub cursor-plugin，按 workbuddy 面板口径全量移植，落 cursor/，不依赖 CLIProxyAPI SDK），并新增会话 Token 导入账号能力：粘贴 user_<id>::<jwt>（或 URL 编码 / Cookie 前缀 / 裸 JWT）→ 取 :: 后段 JWT → POST https://api2.cursor.sh/oauth/token（grant_type=refresh_token + client_id + refresh_token）兑换 access_token → host.auth.save 落盘 cursor-<hash8>.json（顶层 type=cursor-provider），按 account_id/email 去重；管理面板增 import/export/delete/enable/disable 五路由与卡片删除/启停、全部启停、导入弹窗、导出、双语 i18n、key 三回退；保留 OAuth 轮询登录、executor tool-loop、checkpoint/会话粘性、图片输入、上下文准入。本地 cgo-shim build/vet/test 全绿（含必失败哨兵），面板 node --check 通过，cursor 全量 LF 镜像 gofmt 清零，6-review STYLE: PASS；registry.json 增 cursor-provider 0.1.0（7 平台 artifacts 占位待 CI 回填），待发布。

- 2026-10-06：workbuddy-ai-provider **0.1.0** 独立国际版插件落地、双通道扫码授权与生产部署——新设独立插件 ID: workbuddy-ai-provider（版本 0.1.0），唯一锁定国际站基址 https://www.workbuddy.ai，凭证文件名前缀固定为 workbuddyai-（type: workbuddy-ai-provider），彻底杜绝与国内版凭证碰撞。实现双通道扫码登录：支持宿主原生 OAuth 与专属管理面板 panel.html「📱 扫码登录 / 添加账号」弹窗（方案 B）；保留低积分优先与会话粘性调度、10分钟活跃心跳探测（watchdog）、4小时 token 保活刷新（keepalive）、4xx 换号重试与用量统计归一化；彻底剔除国内签到与 CN 保号池。本地 cgo-shim 全绿（含哨兵）；GitHub Actions CI Run 37469999135 成功，8 资产推送入库，registry.json 原子发布且 CDN 校验 ALL PASS；生产环境通过 plugin-store install 热重载部署上线，二进制 SHA256 100% 吻合，accounts 接口 200，panel 资源 200。

- 2026-10-05：gemini-provider **0.1.1** 补充 Google 官方彩色图标、正式发布并成功热重载部署生产。提取 Google 官方标准正方形彩色矢量 PNG（192×192 RGBA 透明底，6,382 字节），分别落盘 `assets/icons/Gemini.png` 与 `assets/icons/Google.png`；更新 `registry.json` 与 `gemini/main.go` 中的 Logo URL 指向 raw.githubusercontent 仓库源；atomic bump 版本为 `0.1.1`，补齐 CHANGELOG.md；本地 `cgo-shim-build.py gemini` 验证 build/vet/test 全绿；代码提交推送至 main（commit `e1e0b7f`）；派发 GitHub Actions CI（Run ID `37286392309`）40 个 jobs 全部 success；下载 8 个 release assets，7 平台 zip + checksums.txt 双重 SHA256 校验全绿（commit `809100c`）；执行 `publish-assets.py` 回填 registry.json 并推送（commit `8cf6e90`），远端 raw CDN 与哈希验证 ALL PASS；生产环境调用 `plugin-store install` 安装，落盘 .so SHA256 `779c73be...` 与本地 release zip 100% 一致，CPA 容器日志证实热重载成功：`pluginhost: plugin hot reloaded plugin_id=gemini-provider active_version=0.1.1 retired_version=0.1.0`，管理端 plugin-store 图标正常显示。

- 2026-10-05：gemini-provider **0.1.0** 正式发布部署与多插件 Token 用量统一统计——引入开源插件 `cpa-plugin-gemini-cli` 作为仓库第 4 个插件（3 服务商 + 1 用量统计），发布名称定为 "Gemini Provider"（ID: `gemini-provider`，版本: `0.1.0`）。CI 发布工作流 Run 37279598274 全部 success，生成 8 个 release assets 并 push 到 main；registry.json 0.1.0 远端验证 ALL PASS；生产服务器通过 plugin-store install 成功热重载加载 gemini-provider v0.1.0（.so sha256 ca9a64f4... 吻合，registered=True, enabled=True），成为仓库正式第 4 个插件。

- 2026-10-05：gemini-provider **0.1.0** 集成与多插件 Token 用量统一统计——引入开源插件 `cpa-plugin-gemini-cli` 作为仓库第 4 个插件（3 服务商 + 1 用量统计），发布名称定为 "Gemini Provider"（ID: `gemini-provider`，版本: `0.1.0`）。建立 `gemini/` 独立模块（Go 1.26，对齐 `CLIProxyAPI/v7 v7.2.129`），导出标准 C ABI（`cliproxy_plugin_init`、`cliproxyPluginCall`、`cliproxyPluginFree`、`cliproxyPluginShutdown` 等），提供 panic recover 保护；实现 `usage.go` 与 `usage_feed.go`，将流式/非流式请求的 `usageMetadata` 与 TTFT 首包耗时写入共享 `<root>/data/token-usage-feed.ndjson`；更新 `token-usage-tracker` 身份归一化，将 `gemini` / `gemini-cli` / `gemini-provider` 统一展示为 `"Gemini"`；更新 CI 构建矩阵与 `registry.json`（新增 7 平台 artifacts 配置）。验证：`cgo-shim-build.py` 5 插件全绿通过（gemini, token-usage-tracker, traework, workbuddy, qoderwork），测试用例经哨兵拦截确认真实进编译，`validate-registry.py` 校验 5 插件全绿。沉淀知识库《集成GeminiProvider插件与多服务商Token用量统一聚合》。

- 2026-10-01：workbuddy-provider **0.15.1** / traework-provider **0.2.1** / qoderwork-provider **0.9.21** —— 修复账号面板筛选标签计数不随积分回填重算（三插件已发布部署：CI 三 run 同 commit `df2c92c` success、远端 raw ALL PASS、生产 hot reloaded active=0.15.1/0.2.1/0.9.21、生产面板标签实测与筛选自洽）。用户反馈 WorkBuddy 面板筛选标签「可用 51 / 耗尽 0」与同屏汇总卡「21 个账号 · 可用 20 · 耗尽 0」自相矛盾。根因：`updateFilterCounts()` 是标签计数唯一写入口却只在 `load()` 调用一次，积分回填链路的其余重绘入口（`filterRegion` / `renderGrid` / `updateOneCard`）只走 `renderSummary()`；冷启动缓存为空使所有账号 `credits=null`，首屏把「未知」当「可用」写死 `可用=51 耗尽=0`，后台回填真实 credits 后只有汇总卡重算。修复：标签计数挂到统一渲染入口 `renderSummary()`（三插件同构）+ `load()` 去重；workbuddy 新增 `isAccountExhausted()` 收敛标签计数 / `accountsForFilter` / 卡片徽标 / 汇总卡四处判定（与后端 `isCreditsExhausted` 同口径）。验证：新增长期回归资产 `test/workbuddy/panel_filter_counts_repro.mjs`（Node vm + DOM 桩真实执行内联 JS，三插件参数），修复后 PASS（21/30 且标签与筛选自洽）、对 HEAD 反证 FAIL 3 项（51/0）；`cgo-shim-build.py` 三插件 build/vet/test 全绿。沉淀知识库《派生计数只在首屏算一次会长期停在旧值》。

- 2026-09-30：workbuddy-provider **0.15.0** / traework-provider **0.2.0** —— 移除保号池机制、路由排除改由「测试」标签（test_failed）承担，**已提交并发布部署**；同轮把「默认提交/发布授权」写入仓库级规则 AGENTS.md / CLAUDE.md。用户口径：保号已基本无意义 → 去掉保号池用「测试」标签代替；token keepalive（登录态续期）不在删除范围。关键设计：保留「定时活跃探测 + 积分刷新」循环（refresh_runner.doFetchOne → triggerActivePing 失败写 test_failed），它是测试标签唯一自动来源；删保号翻转语义并整体改名（preserveWatchdogLoop→watchdogLoop、runPreserveWatchdogTick→runWatchdogTick、requestPreserveTick→requestWatchdogTick、preserveTickCh→watchdogTickCh、preserveWatchdogStartupWait→watchdogStartupWait、preserveWatchdogReadyPoll→watchdogReadyPoll，删 preserveWatchdogDisabledPoll），固定 watchdogIntervalDefault=10m。改动：两插件删 preserve.go；scheduler/active_auth/failover_retry/session_auth 硬排除只留 isTestFailed + isAccountCoolingDown；workbuddy 删 preserve_* 解析 / panel Preserve 字段 / panel.html 保号 UI / lifecycle preserveSetClear / credits_handler 标签映射改 test_failed；traework 删 isAccountPreserved / refreshPreserveSetFromDisk / Preserved map / config case / main ConfigFields / panel.html 保号 UI。验证：cgo-shim 双插件 build/vet/test 全绿（11.229s/2.022s）+ 哨兵法证明新测试进编译 + 双 panel.html 4 script 块 node 校验全绿 + 本轮零新增 gofmt 抱怨 + grep 确认保号符号 0 命中 + 6-review STYLE PASS（doc/6-review/2026-09-30_000524）。发布链 1e4a571→5e08deb→8200656→55506b6；CI run 36603938568/36603955142 同 commit `5e08deb` 双 success；远端 raw ALL PASS（14 artifacts sha256 全对、旧版零残留）；生产 install 落盘 .so sha256 与本地 zip 一致（4727e7bb…/2d61f96f…）+ hot reloaded active=0.15.0 retired=0.14.43 与 active=0.2.0 retired=0.1.68 + accounts/panel 全 200；行为验收 `/v1/responses` 流式 qwen3.8-max 16s / 56655B / 144 帧 + nonce 完整 + `exec stream async done attempt=1 chunks=174`。同轮规则变更：AGENTS.md + CLAUDE.md 以「提交 / 发布授权（默认授权，强制）」段替换原「严禁自动提交 Git」段，确立本仓库默认提交/发布授权（当轮显式边界仍绝对优先）。

- 2026-09-29：workbuddy-provider **0.14.43** / traework-provider **0.1.68** 路由口径改造「优先可用账号 + 硬排除测试/保号/冷却 + 低积分优先」发布部署。改造前：workbuddy 走「健康层优先高积分」（0.14.40），`test_failed` 在调度链路完全没有排除，保号「全部保号时回退全量列表」等于把不可用账号放回路由；traework 另有真实缺陷——`refreshPreserveSetFromDisk()` 全仓无调用点，重启后保号账号仍被命中。落地：scheduler.pick 三段硬排除 + 全排除即 `Handled:false` 交还宿主跨 provider failover（删除"回退全量"兜底）；`sort.SliceStable` 按缓存积分升序（未测 -1 排最后）；新增 `accountRoutable` / `accountLowerCredits` 由 pickActiveAuth / ensureDefaultActiveAuth / pickSessionAuth / scheduler.pick 共用，面板选中项改为「可用账号中积分最低者」；`pickNextAuth` 跳过测试/保号但保持宿主顺序；`testFailedSet` 内存镜像 + `refreshTestFailedSetFromDisk` 三处同步（面板构建 / 保号 watchdog tick / 标签直写），重启后仍正确排除；traework 补 `refreshPreserveSetFromDisk` 调用点并修 `cachedCreditsScore` 空指针。验证：cgo-shim 双插件全绿（11.45s / 2.23s）+ 哨兵法证明新测试进编译 + 双 panel.html 4 script 块 node --check 全绿。发布链 cbb18b6→a9a893a→1c792cb→b33613c；CI run 36466163784/36466175909 同 commit `a9a893a` 双 success；远端 raw ALL PASS（14 artifacts size+sha256 全对、零残留）；生产 install 落盘 .so sha256 与本地一致（21ba3df2…/872a32e3…）+ hot reloaded active=0.14.43 retired=0.14.42 与 active=0.1.68 retired=0.1.67 + accounts/credits/panel 全 200。行为强证据：可用账号积分最低者（remain=31 的 `16226361146 [CN]`）与面板 `active_auth` 完全一致；真实 `/v1/responses` 3s 200 + nonce 完整。

- 2026-09-27：workbuddy 0.14.42 面板「创建」时间改用 JWT `auth_time` 真实创建时间（旧值取宿主 `HostAuthFileEntry.CreatedAt`，被 watcher 的 `time.Now()` 每次扫描刷成「最近写入时刻」）。新增 `created_at.go` + 8 用例；cgo-shim 全绿含哨兵；发布链 cb0a2be→f44eb08→6736c90；CI 36327182344 success；远端 raw ALL PASS；生产 hot reloaded 0.14.42 + 落盘 sha256 一致；行为验证 `242e1dde` 由错误 09-27 22:12 纠正为真实 08-20 23:11。（同日收口：按 project-memory-rules 主动裁剪 PROJECT_CURRENT.md「已完成」区最旧的 24 条（2026-08-22~08-30）以满足 51,200 字节硬限，63,445 → 47,493 字节；并同步对齐 PROJECT_HISTORY.md 既有漂移的计数锚点区（HEAD 为 20 事件/19 锚点，现为 20/20））

- 2026-09-05：traework-provider **0.1.44 发布部署**（GetUserInfo 401 回落回调 userInfo）：0.1.43 实测 exchange 已成功换到 token，但 GetUserInfo 报 401 "The user is not logged in"（cookie 会话鉴权路由，新 bearer token 不被认）。SOLO main.js 取证：客户端优先用回调 URL 的 userInfo JSON（r ?? await getUserInfo(...)），GetUserInfo 只是兜底。修复：parseBounceUserInfo 提取回调 userInfo 的 UserID/ScreenName，GetUserInfo 失败时回落。发布链 f38147d→4c924aa→a162f44；CI run 33902405197 success（16m+，两轮轮询窗口）；远端 ALL PASS；生产 install 首两次 CDN 滞后 version not found → 等 7 分钟第三次成功，落盘 sha256 60cb72ae 一致 + hot reloaded active=0.1.44。

## 计数锚点区

> 本区由 `memory-usage-tracking-rules` 收口闸门维护：HISTORY 仅窄读计入，会话启动不读不计；被裁剪事件的锚点随事件一起删除（不保留 retired）；本区计数仅作主题热度弱信号。锚点 key 用事件 `- YYYY-MM-DD：` 后的核心主题短语（约前 12 字符，可前缀匹配）。

```yaml
version: 1
anchors:
- title: 'qoderwork-provider **0.9.26 修复宿主'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'traework-provider **0.2.5 发布部署与签到失败双根因修复**'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'qoder-ai-provider **0.1.3** 修复宿主'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'qoder-ai-provider **0.1.2** / qoderwork-provi'
  usage_count: 1
  usage_days: 1
  last_used_at: 2026-10-10
  absorbed_to: null
- title: 'qoderwork-provider **0.9.23** 发布部署'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'workbuddy-provider **0.15.4** / workbuddy-ai-provider'
  usage_count: 1
  usage_days: 1
  last_used_at: 2026-10-10
  absorbed_to: null
- title: 'workbuddy-ai-provider **0.1.8** 发布部署'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'workbuddy-token-usage **0.2.4** 发布部署'
  usage_count: 1
  usage_days: 1
  last_used_at: 2026-10-09
  absorbed_to: null
- title: 'cursor-provider **0.1.0** 发布部署与端到端验收'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: '发布后清理规则固化与 release-assets'
  usage_count: 1
  usage_days: 1
  last_used_at: 2026-10-09
  absorbed_to: null
- title: 'cursor-provider **0.1.0** 移植与 Token 导入'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'workbuddy-ai-provider **0.1.0** 独立国际版'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'gemini-provider **0.1.1** 补充 Google'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'gemini-provider **0.1.0** 正式发布部'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'gemini-provider **0.1.0** 集成与多'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'workbuddy-provider **0.15.1** / tr'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'workbuddy-provider **0.15.0** / tr'
  usage_count: 1
  usage_days: 1
  last_used_at: 2026-09-30
  absorbed_to: null
- title: 'workbuddy-provider **0.14.43** / tr'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'workbuddy 0.14.42 面板「创建」时间改用 J'
  usage_count: 1
  usage_days: 1
  last_used_at: 2026-09-27
  absorbed_to: null
- title: 'traework-provider **0.1.44 发布部'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
```
