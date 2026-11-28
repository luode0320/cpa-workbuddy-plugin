# 2026-11-29_022500 glm-provider 面板按 workbuddy 规格重构

## 一、问题

用户反馈：「智谱清言 账号面板的功能和 workbuddy 的面板功能还是差距很大，继续调整参考 workbuddy 的面板功能。」

## 二、差距量化

| 维度 | workbuddy | glm（重构前） | 差距 |
|---|---|---|---|
| 面板体积 | 87,404 B | 30,752 B | 2.8 倍 |
| 管理接口 | 17 个 | 10 个 | 缺 7 个 |
| 积分展示 | 进度条（剩余/已用/额度池/快照时刻） | 一行纯文本 | 无视觉层次 |
| 排序 | 创建时间 ↔ 积分 双向 | 无 | 无法按积分挑号 |
| 批量操作 | 全部签到 / 全部解冻 | 无 | 只能逐个点 |
| 筛选维度 | 全部/可用/耗尽/失败/测试 | 全部/可用/停用/失败 | 缺低积分维度 |
| 概览 | 含积分合计 | 仅账号计数 | 看不到总余量 |
| 增量刷新 | `fetchAndPatchCredits` 单卡 patch | 整表重绘 | 滚动被打断 |

workbuddy 独有而 GLM 没有对应能力的接口：`/checkin`、`/checkin/config`、`/keepalive`、`/keepalive/status`、`/trial`、`/growth`、`/select`、`/export/zip`。其中签到、专家试用、成长值是 WorkBuddy 平台特有业务，GLM 上游没有对应端点（0.2.12 已穷举 40+ 路径），**不移植**；`/keepalive` 对 GLM 也无意义（refresh_token 长期有效，不像 workbuddy 的短期 token 需要保活）。

因此本次聚焦**面板呈现与运维能力**，不动业务语义。

## 三、实现

### 3.1 积分进度条 `progressHTML`

按 workbuddy 同构：

```
[可用 199.01 分] [低积分(<20)]        已用 1%
▓░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
剩余 199.01 分 · 余量 99% · 额度池 201 · 快照 2026-11-29 02:18:36
```

- 上游只给 `left_score` 与 `token_left_percent` / `token_usage_percent`，不给总额度，故额度池按百分比反推：`pool = remain / (1 - usedPct/100)`。
- 着色阈值沿用 workbuddy：`>60%` 黄、`>85%` 红。
- 未拉到数据时渲染 spinner 加载态，不显示 `0` 误导。
- `score_rule` 挂在 meta 的 `title` 上，不占版面。

### 3.2 四种排序 `toggleSort`

`创建 ↓ → 创建 ↑ → 积分 ↑ → 积分 ↓` 轮转，按钮文案与 tooltip 同步更新。

排序依赖 `created_at`，但后端 `accountSummary` 原先没暴露该字段 —— 补上 `created_at` / `updated_at`，否则排序恒等（`""` 与 `""` 比较）。

积分排序对未拉取账号用 `±Infinity` 兜底，避免未知值被当成 0 排到最前。

### 3.3 批量操作

- **全部测试** `testAll`：串行探测所有未停用账号，顶部显示 `测试 n/m`，结束汇总成功/失败数。
- **刷新积分** `refreshAllCredits`：串行重拉全部账号积分，顶部显示 `刷新积分 n/m`。

均用顶部 `#refreshProgress` 显示进度，不阻塞 UI。

### 3.4 低积分筛选

`LOW_CREDIT_THRESHOLD = 20` 单一来源，同时供筛选计数、卡片警示段使用，避免两处阈值漂移。新增「低积分」标签与 `cntLow` 计数。

### 3.5 增量刷新

`patchCardProgress(id)` 只替换该卡片的 `[data-pb]` 区域，不整表重绘 —— 避免刷新积分时打断滚动与输入焦点。

`creditCache` 按 auth_id 存快照；`load()` 只对缺失项回源，已有缓存沿用。

### 3.6 单卡「刷积分」

`manualRefreshCredits` 清缓存后重拉，成功/失败都给 toast，失败原因来自 `creditCache[id].error`。

## 四、验证

```
python scripts/cgo-shim-build.py glm → build / vet / test 全绿（34 测试）
gofmt 23 个 Go 文件 PASS
面板 2 个 <script> 块 node --check 全 OK
面板体积 30,752 → 41,166 B
```

关键函数接线核对（出现次数）：

| 函数 | 次数 | 含义 |
|---|---|---|
| `creditOf` | 9 | 缓存读取，被排序/筛选/概览/卡片共用 |
| `fetchAndPatchCredits` | 6 | 定义 + load/刷新/单卡/批量调用 |
| `isLowCredit` | 5 | 定义 + 筛选 + 计数 + 卡片警示 |
| `progressHTML` | 4 | 定义 + 卡片初始 + patch |
| `sortedAccounts` | 3 | 定义 + renderGrid |
| `testAll` / `refreshAllCredits` / `toggleSort` / `manualRefreshCredits` | 各 3 | 定义 + HTML onclick + 调用 |

## 五、未移植项与理由

| workbuddy 功能 | 不移植原因 |
|---|---|
| `/checkin` 每日签到 | WorkBuddy 平台特有，GLM 无签到端点 |
| `/trial` 专家试用 | WorkBuddy 业务概念，GLM 无对应 |
| `/growth` 成长值 | 同上 |
| `/keepalive` token 保活 | GLM 的 refresh_token 长期有效，无短期过期问题 |
| `/select` 选中账号 | GLM 面板无需「当前账号」概念，调度器轮询即可 |
| `/export/zip` 分账号导出 | 当前账号量少，单 JSON 汇总足够；账号量上来再加 |

## 六、结论

面板从「能用」补到「可运维」：积分有视觉层次、可按积分挑号、可批量测试与刷新、能筛出低积分账号、刷新不打断操作。

四层版本一致：代码 = `VERSION` = registry = 生产 `0.2.14`。

## 七、后续可加（按需）

1. **积分历史曲线**：`/credits` 目前只返回当前快照，若要画曲线需在插件侧落时序数据。
2. **`/export/zip` 分账号导出**：账号量到两位数后有必要。
3. **积分自动刷新间隔**：目前手动触发，可加 `setInterval` 与开关（对应 workbuddy 的自动签到开关位）。
4. **卡片上显示 `score_rule` 完整文案**：目前只在 title 里，若用户常看可提为一行小字。