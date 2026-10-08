# 项目历史事件

> 本文件追加关键历史事件并只保留最近 20 条（按日期倒序、新事件置顶、追加后自动裁剪）；普通启动默认不读取，只有历史追问、当前状态不足或真实卡点时才窄检索。

## 事件

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

- 2026-09-05：traework-provider **0.1.43 发布部署**（浏览器授权登录 ExchangeToken 打错域修复）：0.1.42 实测 AuthCode 解析链已通但 exchange 报 invalid character '<'——www.trae.cn 是 SPA 域对 API 路径返回 HTML 首页，非 API 域；api.trae.cn / api.trae.com.cn 双域实测均为真 JSON API。修复：新增 browserLoginAuthHost=api.trae.cn（与生产凭据 host、签到 defaultAPIHost 同域），exchange/GetUserInfo 切域 + GetUserInfo 解析容错（ResponseMetadata.Error + camelCase result 回落）。发布链 faca5e9→737589a→a1c58eb；CI run 33900103965 success（14m30s）；远端 ALL PASS；生产 install 落盘 sha256 3d564208 一致 + 0.1.43 热重载；生产冒烟实锤：假 code submit 返回上游 JSON 错误（10101 无效参数），exchange 已打真 API 域，整链只差用户真实 AuthCode。

- 2026-09-05：traework-provider **0.1.42 发布部署**（浏览器授权登录适配 TRAE 授权页真实回调形状）：用户真机实测 0.1.40 实锤 TRAE 授权页跳转非标准 OAuth——不回传 code/state，授权码在 `authCodeInfo` JSON 参数里。修复：extractAuthCode（code 优先、authCodeInfo 回落）+ 会话定位链（start 返回 state → 面板带回 body.state → URL state → 最新 pending 兜底）+ callback/submit 双通道同构。发布链 98b81e0(fix)→a60038d(assets)→e0b1850(registry)；CI run 33897217089 success（12m10s）；远端 ALL PASS 零残留；生产 install 落盘 sha256 `c5150057` 一致 + loaded/registered 0.1.42 热重载 + 冒烟两条新语义生效。同窗口并行会话发布 0.1.41/0.14.20/0.9.7（删除按钮 busy 修复，0.1.41 顺带卷入本会话被带走的前端 browserLoginState 改动但无后端解析）。

- 2026-09-04：traework-provider **0.1.40 发布部署**（浏览器授权回调白名单适配 + 面板引导式粘贴 submit）：五组对照定案 TRAE 授权页白名单判据（回环 host AND 路径恰好 /authorize，协议端口无关，resource 长路径全场景拒绝）→ start 改拼回环 `/authorize` + 新增 `POST /browser-login/submit`（body {url} 整段 parse，共享 settleBrowserLogin）+ 面板粘贴引导卡片；用户否决 py relay，定案与宿主手动粘贴通道同构的零依赖方案。发布链 c168bd1(fix 7 文件)→9e94b4a(assets)→fad7b56(registry)；CI run 33891007981 success（12m23s）；远端 raw ALL PASS 零残留；生产 install 落盘 sha256 `97b54b1f` 一致 + loaded/registered 0.1.40 热重载 + accounts/panel/submit 验证通过。剩用户人工登录实测。

- 2026-09-04：traework-provider **0.1.39 发布部署**（浏览器授权登录三缺陷修复）：生产端到端验证 0.1.38 暴露三缺陷——宿主 management JSON 响应强制 htmlsanitize（`&`→`&amp;` 授权页参数解析必挂→面板 replaceAll 兜底）；callback 注册在 management 前缀被宿主 management key 中间件拦截（含 GET）→ 移入 Resources 免鉴权 resource 前缀；授权 URL 缺 OAuth state（回传无法匹配会话）→ `q.Set("state", state)`。发布链 bf6ba87(fix 7 文件)，cgo-shim 全绿 + 新增 2 契约测试。

- 2026-09-04：traework-provider **0.1.38 发布部署**（浏览器授权登录：免 IDE 完整 OAuth 导入）：逆向 TRAE SOLO CN 0.1.62 main.js 确认浏览器授权码+PKCE(S256) 流程；`browserlogin.go` 三路由（start: PKCE 对+EC P-256 设备密钥+随机指纹+origin 校验 / callback: 换 token→取用户→去重入库→回跳页 / result: 读后即焚）+ 面板按钮与回跳轮询。发布链 b5f105c→babd598(assets 7 平台)→ea57fee(registry)；CI run 33788701204 success；生产 install 落盘哈希一致 + hot reloaded active=0.1.38 + 流式回归健康。生产复验暴露三缺陷由 0.1.39 承接。

- 2026-09-02：traework-provider **0.1.30 三缺陷修复代码级完成 local 全绿（未发布）**：用户「不是号有问题, 就是我们的插件有问题」否决账号归因（纠偏见 [[traework-prod-account-225774-dead-203343-refresh-mismatch]]）。生产取证（21:26-21:30 stream 2331-2350，0.1.29）失败链=死号 225774 async 401 open error 不核算不驱逐绑定（session 亲和每请求重绑）→ 健康号 203343 被窗口性节流判伪后无同号重试 → pool exhausted；2351 反证 203343 同号 30s 后恢复 18696 tokens。修复：**FIX-A** 伪完成仅当 `PickNextAuth` 无其它候选才对当前账号同号退避重试 1 次（sync+async 收敛一致，`pseudoRetryBudget=1`，不耗跨账号 Budget，有候选仍 A→B 保留既有回归）；**FIX-B** async 401 open error 补 `reconcileAfterExecutorError`+`evictSessionBindingsForAuth`（对照 sync 路径既有核算）；**FIX-C** `isPseudoCompletion` content+reasoning 双计健康度（任一达 600 健康 / content0+reasoning>0 reasoning-only 永不判伪 / 双短才需长输入门槛）。验证：新增 test/traework/executor_same_auth_retry_test.go（async+sync 单号池伪→同号重试→成功 + resetAccountFailover 清零），负向哨兵+定向 Fatal 探针双重证明真实编译执行；既有 5 伪完成回归 + `TestIsPseudoCompletion` reasoning-only 豁免全绿；cgo-shim build/vet/test 全绿、gofmt 干净、改动限 stream.go+executor.go+新测试文件（pump gate reasoning 流式放行属独立优化已回退避免越界）。**未提交未发布**（生产仍 0.1.29；下一步 CYCLE-03 发布 0.1.30 → CYCLE-04 生产真实短请求验收：死号 401 不再拖垮池、伪完成同号退避可恢复、1664/1942/1945 类挂死不复现）。


## 计数锚点区

> 本区由 `memory-usage-tracking-rules` 收口闸门维护：HISTORY 仅窄读计入，会话启动不读不计；被裁剪事件的锚点随事件一起删除（不保留 retired）；本区计数仅作主题热度弱信号。锚点 key 用事件 `- YYYY-MM-DD：` 后的核心主题短语（约前 12 字符，可前缀匹配）。

```yaml
version: 1
anchors:
- title: 'workbuddy-ai-provider **0.1.8** 发布部署'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'workbuddy-token-usage **0.2.4** 发布部署'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'cursor-provider **0.1.0** 发布部署与端到端验收'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: '发布后清理规则固化与 release-assets'
  usage_count: 0
  usage_days: 0
  last_used_at: null
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
- title: workbuddy 0.14.42 面板「创建」时间改用 J
  usage_count: 1
  usage_days: 1
  last_used_at: 2026-09-27
  absorbed_to: null
- title: 'traework-provider **0.1.44 发布部'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'traework-provider **0.1.43 发布部'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'traework-provider **0.1.42 发布部'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'traework-provider **0.1.40 发布部'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'traework-provider **0.1.39 发布部'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'traework-provider **0.1.38 发布部'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null
- title: 'traework-provider **0.1.30 三缺陷'
  usage_count: 0
  usage_days: 0
  last_used_at: null
  absorbed_to: null

```
