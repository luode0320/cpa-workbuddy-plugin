# cpa-workbuddy-plugin — CLIProxyAPI 插件集合

[CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) 的 Go 插件仓库，将多个 AI 服务封装为 **OpenAI 兼容 provider** 供 CPA 网关统一调度：多账号管理、账号 failover 与 40x 换号重试、会话粘性路由、保号池 watchdog、动态模型、流式推理、每日签到、积分生命周期、token 用量统计。目前包含 **4 个并列插件**，每个插件独立版本、独立发布。

| 插件 | 服务 | 已发布 | 一句话 |
|---|---|---|---|
| [`workbuddy/`](workbuddy/) | 腾讯 CodeBuddy（CN + Global） | v0.14.19 | 原生 OAuth provider，积分生命周期 + 每日签到 + 用量 feed |
| [`qoderwork/`](qoderwork/) | QoderWork CN（qoder.com.cn） | v0.9.6 | 双登录 + COSY 签名推理，逆向产物封装（已全量对齐 workbuddy 架构） |
| [`traework/`](traework/) | TRAE SOLO CN | v0.1.40 | TRAE SOLO 逆向 provider，面板浏览器授权登录 + 长推理流式稳定性 |
| [`token-usage-tracker/`](token-usage-tracker/) | workbuddy 账户用量 | v0.2.2 | 真实 token 消耗 dashboard，经共享 feed 采集数据 |

> 「已发布」以 [registry.json](registry.json)（插件商店实际可安装版本）为准；main 分支 HEAD 可能存在已 bump 待发布的更高版本。

---

## 插件总览

### workbuddy — 腾讯 CodeBuddy provider

覆盖国内版 `copilot.tencent.com` 与国际版 `workbuddy.ai`，CN/Global 共用一个插件：

- **OAuth 登录** — 多账号 `workbuddy-<uid>.json` 写入宿主 auth store；面板登录轮询按文件路径去重
- **动态模型** — 上游 models API 实时拉取 + 5 分钟缓存 + 静态兜底；支持宿主侧 `oauth-model-alias` / `oauth-excluded-models` 配置；兼容宿主 YAML block sequence / 多行 JSON 两种 models 落库形态，配置优先 + 自动获取补充
- **执行器** — OpenAI 兼容 chat completions，流式（真 SSE 走 `host.stream.emit`）与非流式；内置 `tool_choice` 归一、Claude Code 模板清洗
- **账号 failover** — 429/402/5xx/传输错误按 1/3/10 分钟阶梯退避；401/403/404/405 计入账号级故障走 `retry_on_4xx` 同请求换号（预算默认 3，缺省键保持当前值，400 业务错直通不重试）
- **调度与会话粘性** — `session` 模式按会话粘住账号（跨换号驱逐绑定）；`credits` 选中面板账号
- **保号池 watchdog** — 0.12.0 起移除三池路由只留保号池，按积分阈值自动归池（默认 10m 刷新、阈值 50）
- **积分生命周期** — CN 账号耗尽自动禁用、签到回血自动恢复；Global 账号耗尽删除 auth（一次性 trial）；executor 遇硬积分错误立即触发 reconcile
- **每日签到** — 09:00 / 21:00 定时自动签到，面板可手动批量签；per-account 互斥锁防并发
- **成功/失败计数持久化** — 内存为主 + 跟随保号节奏落盘 auth 文件顶层 `success_count`/`failed_count`，容器重启不归零
- **面板** — `/v0/resource/plugins/workbuddy-provider/panel`：积分进度条、CN/Global 筛选、账号删除（二次确认 + 严格归属校验）、连败/冷却倒计时、异步节流刷新
- **用量 feed** — 每条请求的 token 消耗以 NDJSON 追加写入共享 feed，供 token-usage-tracker 消费

### qoderwork — QoderWork CN provider

基于对 QoderWork 桌面客户端的逆向（见 [KNOWLEDGE.md](KNOWLEDGE.md)），纯软件实现其私有鉴权链路：

- **双登录共存** — ① OAuth 设备授权（PKCE，`dt-` 30 天 + `drt-` 1 年自动旋转）② PAT 导入（`pt-`，长期有效兜底），两家族可共存于同一 auth 文件；OAuth 登录成功后自动领取一次性 Pro 升级包
- **COSY 签名推理** — RSA 包裹 AES 会话密钥 + MD5 请求签名（纯 Go 标准库实现），对接 `gateway.qoder.com.cn` SSE 流式，透传 OpenAI chunk
- **QoderEncoding** — 自定义 base64 字母表 + 三段重排的 body 编码
- **动态模型** — COSY 拉取 `/algo/api/v2/model/list`（chat scene：Qwen / DeepSeek / GLM / Kimi / MiniMax 等），10 静态模型兜底
- **每日签到** — 09:00 / 21:00 定时 + 面板手动（单账号/批量），签到后返回积分快照
- **token 保活** — 22:00 定时刷新，按 token 前缀路由（`drt-` → deviceToken、`jrt-` → jobToken），PAT 永不劫持 OAuth 刷新
- **架构对齐 workbuddy（0.9.5+）** — 面板 5 卡片、账号删除、成功/失败计数持久化、会话粘性、usage feed（12 参数）、保号池 watchdog 全量对齐（逐函数适配，保留 COSY/SSE 嵌套解包等架构差异）
- **auth 隔离** — 文件名前缀 `qoderwork-` 过滤，与其他插件互不干扰

### traework — TRAE SOLO CN provider

基于对 TRAE SOLO CN 桌面客户端的逆向封装（0.1.16 起对齐 workbuddy 能力五件套）：

- **凭据导入** — IDE `storage.json` 凭据解析导入（路径面板固定展示）；**面板浏览器授权登录**（0.1.38+）：PKCE S256 + 设备指纹复刻 SOLO 客户端，免 IDE 完成 OAuth 导入；授权回调拼回环 `/authorize` 形状适配 TRAE 白名单，配合面板引导式粘贴提交闭环
- **每日签到** — 定时 + 面板手动（单账号/批量）+ 签到重试队列（幂等 upsert，独立重试端点）
- **账号 failover 与会话粘性** — 阶梯退避 + 同请求换号 + `session` 粘性路由，与 workbuddy 同构
- **保号池 watchdog / lifecycle / keepalive** — 积分阈值自动归池、自动停用、ExchangeToken 保号；面板含保号状态展示
- **流式长推理稳定性** — 宿主流桥 open/read 阶段超时竞速降级直连（30s/90s）；伪完成检测（content+reasoning 双轴健康度，reasoning 流式放行避免零字节 504）；上游断流兜底收尾（`finish_reason=length`）；流式请求缺省补 `max_tokens=20000`
- **真实用量** — 解析上游 `event:token_usage`，dashboard Token 列为真实值非估算；usage feed 含会话 key（跨换号冻结）与首字延迟（ttft）列
- **积分面板** — 对齐 workbuddy 面板：账号卡、签到、用量汇总（进度条）、浏览器授权登录入口

### token-usage-tracker — 用量统计 dashboard

记录并可视化 **workbuddy 账户的真实 token 消耗**（实盘数据）：

```
workbuddy 插件                         token-usage-tracker 插件
┌────────────────────────┐   NDJSON   ┌─────────────────────────┐
│ 每次请求完成            │ append ▶  │ 轮询读取（默认 5s）      │
│ publishUsage 汇聚点 ────┼──────────▶│ 导入自身 bbolt 库        │
│ 追加一行到共享 feed      │  O_APPEND │ dashboard（"Token 用量"）│
└────────────────────────┘           └─────────────────────────┘
```

- **共享 feed**：`<CLIProxyAPI root>/data/token-usage-feed.ndjson`，与 workbuddy 的 `usage_feed_path` 一致即可互相发现
- **实时刷新**（0.2.2）— feed 新增 usage 经 `/usage/events` SSE 短连接通知 dashboard（seq 前进触发刷新，15s 轮询兜底）
- **会话与首字延迟列** — feed 记录含 `session_key` 与 `ttft_ns`，dashboard 独立成列（配合 traework 0.1.33+）
- **为什么是文件 feed**：宿主 `UsagePlugin` 广播对插件 executor 恒为空；bbolt 排它锁不允许两个长驻进程共享同一数据库。追加写 NDJSON 是唯一干净的跨插件数据通道（无锁、可回放、可轮转，超 128MB 自动截断）
- **统计核心** 移植自社区插件 [AITNR/cap-token-usage-tracker](https://github.com/AITNR/cap-token-usage-tracker)

---

## 技术亮点

- **QoderWork 逆向**：废弃 WASM 签名与 qodercli 子进程两条弯路后，确认签名是 JS 内重写的 RSA+AES+MD5，PAT → jobToken（`jt-` 24h / `jrt-` 48h）链路端到端实测 200 OK。完整知识库见 [KNOWLEDGE.md](KNOWLEDGE.md)
- **模型路由陷阱**：QoderWork 响应 `"model":"auto"` 是服务端硬编码，不代表路由失败；真实路由靠 `x-model-key` header + `model_config.key` body 字段双管齐下
- **traework 流式三级防护**：宿主流桥 open/read 超时竞速降级直连 + 伪完成双轴健康度判定 + 上游断流兜底 length 收尾——长推理不再 499/504/中途静默截断
- **TRAE 浏览器授权白名单适配**：授权页只放行回环 host + 路径恰好 `/authorize`（对照实验实锤），callback 拼回环形状 + 面板粘贴 submit 闭环实现免 IDE OAuth
- **CPA 原生 type 契约对齐**：存量 auth 文件补齐 `type` 字段 + 双插件 `ParseAuth` 对称防御（v0.1.19 / v0.8.4）
- **跨插件数据通道**：文件 feed 而非共享 bbolt，规避文件锁冲突（v0.8.9 拆分决策）

---

## 仓库结构

```text
cpa-workbuddy-plugin/
├── workbuddy/              # 插件 1：腾讯 CodeBuddy provider（含测试与 CHANGELOG）
├── qoderwork/              # 插件 2：QoderWork CN provider（含 baseprompt.json 模板）
├── traework/               # 插件 3：TRAE SOLO CN provider（浏览器授权登录 / 面板 / 流式）
├── token-usage-tracker/    # 插件 4：token 用量 dashboard（usage_stats/ 统计核心）
├── registry.json           # 插件商店源（schema v2，direct install，含 sha256/size）
├── release-assets/         # 各版本多架构 zip + checksums.txt 历史产物
├── .github/workflows/build.yml  # 多架构构建 + tag 触发独立版本发布
├── scripts/
│   ├── publish-assets.py          # 扫描 release-assets → 更新 registry.json → 校验
│   ├── download-release-assets.py # 按 <version> [plugin] 从 Release 下载资产入库
│   ├── validate-registry.py       # registry.json 格式/完整性校验（CI 中执行）
│   ├── cgo-shim-build.py          # 本地 cgo 编译验证 shim（产物 cpa-shim-*/，gitignore）
│   └── qoder_cn_pat_login.py      # QoderWork CN PAT 获取（阿里云 SSO 短信）
├── docs/                   # 面板截图（cpamp-workbuddy-panel.png）+ models-config 文档
├── analysis/               # 关键决策分析记录（登录方案、拆分合并、升级排障等）
└── *.md                    # KNOWLEDGE / LOOP / plan / STATUS / CLAUDIUM_SPEC / DEAD_CODE_REPORT
```

---

## 多架构 Release

每个插件独立版本发 Release（tag `<id>-v*`，`<id>` 为 provider id，如 `traework-provider-v0.1.40`），产物为 CPA 插件商店标准格式：

```text
<id>_<version>_linux_amd64.zip      # zip 根目录: <id>.so
<id>_<version>_linux_arm64.zip
<id>_<version>_darwin_amd64.zip     # <id>.dylib
<id>_<version>_darwin_arm64.zip
<id>_<version>_windows_amd64.zip    # <id>.dll
<id>_<version>_windows_arm64.zip
<id>_<version>_freebsd_amd64.zip
checksums.txt
```

命名规则与官方一致：`ArchiveName(id, version, goos, goarch) = {id}_{version}_{goos}_{goarch}.zip`（见 CLIProxyAPI `internal/pluginstore`）。

**CI（GitHub Actions）：**

| 触发 | 行为 |
|---|---|
| push / PR | 全量构建 + `go test` + `go vet` + registry 校验（只出 artifacts，不发 Release） |
| tag `<id>-v*`（如 `traework-provider-v0.1.40`） | 该插件独立版本 Release |
| workflow_dispatch | 手动选插件（provider id）+ 版本，CI 构建并发布 Release |

发布流程：bump `VERSION`/`main.go` → commit & push → workflow_dispatch 触发 CI（**必须同时传插件 id 与版本**）→ `scripts/download-release-assets.py <version> [plugin]` 下载资产入 `release-assets/` → `scripts/publish-assets.py <plugin> <version>` 更新 registry → 提交推送 → 远端 raw 校验。

---

## 安装

### 方式一：插件商店（推荐）

CPA 插件商店添加自定义源：

```text
https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/registry.json
```

然后在商店 UI 安装/更新 **workbuddy-provider**、**qoderwork-provider**、**traework-provider**、**workbuddy-token-usage**。

> 服务器侧生产部署走管理 API：`POST /v0/management/plugin-store/<id>/install?version=`（docker cp .so / PUT config 均不触发热重载）。registry push 后 CDN 边缘偶有滞后，install 误报 `version not found` 时等几分钟重试。

### 方式二：手动部署（linux/amd64 示例）

```bash
unzip traework-provider_0.1.40_linux_amd64.zip
# 扁平 plugins 目录（常见 docker 挂载）
cp traework-provider.so /path/to/cliproxyapi/plugins/traework-provider.so
# 或平台子目录布局
# mkdir -p plugins/linux/amd64 && cp traework-provider.so plugins/linux/amd64/
```

### config.yaml

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    workbuddy:
      enabled: true
    qoderwork:
      enabled: true
    traework:
      enabled: true
    token-usage-tracker:
      enabled: true
```

---

## 本地开发

```bash
# 构建 c-shared 插件（以 qoderwork 为例）
cd qoderwork
CGO_ENABLED=1 go build -buildmode=c-shared -ldflags "-X main.version=0.9.6" -o qoderwork-provider.so .

# 测试 + 静态检查（与 CI 一致）
go test ./...
go vet ./...

# registry 校验
python3 scripts/validate-registry.py registry.json
```

Windows 无 C 工具链时用 `python scripts/cgo-shim-build.py <plugin>` 生成 shim 验证 C ABI 与打包逻辑（build+vet+test 全绿，产物在 `cpa-shim-*/`，已 gitignore）。

---

## 文档导航

| 文档 | 内容 |
|---|---|
| [KNOWLEDGE.md](KNOWLEDGE.md) | QoderWork CN 完整逆向知识库（凭证体系 / COSY / QoderEncoding / 模型路由真相） |
| [LOOP.md](LOOP.md) | QoderWork 持续优化循环（Backlog、已解决问题、决策原则） |
| [plan.md](plan.md) | QoderWork 实现计划 v4（认证链路 / Loop 拆分 / 模型清单） |
| [STATUS.md](STATUS.md) | 逆向与对接现状、实测记录、技术决策记录 |
| [CLAUDIUM_SPEC.md](CLAUDIUM_SPEC.md) | QoderWork 插件实施规格（已验证事实，禁止重新逆向） |
| [DEAD_CODE_REPORT.md](DEAD_CODE_REPORT.md) | qoderwork 死代码与 workbuddy 残留分析 |
| [analysis/](analysis/) | 关键决策分析：OAuth 方案、token-tracker 拆分/合并、升级排障等 |
| [doc/](doc/) | 工程过程文档（6-review 风格回归记录、发布链路踩坑等） |

各插件目录内另有独立 CHANGELOG.md 记录版本明细；仓库级变更历史以 git log 为准。
