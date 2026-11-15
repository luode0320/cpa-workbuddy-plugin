STYLE: PASS

# 6-review：doubao 新增网页链路别名 doubao/web（0.1.22）

审查对象：`F:\cpa-plugin\doubao\` 下 5 个代码文件 + `main.go` / `VERSION` / `CHANGELOG.md`。
最近修改时间：2026-11-16 00:20:00。

## 静态路由命中

在 `D:\谷歌云盘\luode-skills` 下运行：

```text
python code-style-consistency-rules/scripts/static_owner_router.py --changed "doubao/models.go,doubao/models_test.go,doubao/protocol.go,doubao/completion_test.go,doubao/active_ping.go,doubao/main.go,doubao/VERSION,doubao/CHANGELOG.md"
```

命中 7 步：STYLE-01 / STYLE-02 / STYLE-03 / STYLE-04 / STYLE-05 / STYLE-08 / STYLE-09。
未命中 STYLE-06（公共复用）与 STYLE-07（接口契约），理由见文末「未命中说明」。

## STYLE-01 静态格式与编码一致性

| 文件 | 字节 | BOM | 控制字符 | 换行 |
| --- | --- | --- | --- | --- |
| `models.go` | 25048 | 否 | 0 | CRLF 645，孤立 LF 0 |
| `models_test.go` | 11077 | 否 | 0 | CRLF 297，孤立 LF 0 |
| `protocol.go` | 25212 | 否 | 0 | CRLF 534，孤立 LF 0 |
| `completion_test.go` | 18299 | 否 | 0 | CRLF 465，孤立 LF 0 |
| `active_ping.go` | 11311 | 否 | 0 | CRLF 295，孤立 LF 0 |
| `main.go` | 16516 | 否 | 0 | CRLF 457，孤立 LF 0 |
| `CHANGELOG.md` | 61115 | 否 | 0 | 纯 LF 502 行 |
| `VERSION` | 6 | 否 | 0 | 无换行（`0.1.22`） |

`git ls-files --eol` 复核：五个 `.go` 文件均为 `i/lf w/crlf`，与改动前一致；`CHANGELOG.md` 仍为纯 LF，没有引入行尾抖动。

`gofmt -l`：`$env:TEMP\fmtchk0122b` 下对全部 51 个 `.go`（转 LF 副本）执行 → `FMT_OK`，零输出。

## STYLE-02 命名与符号引用一致

**新增标识符（2 个）**：

| 符号 | 落点 | 命名依据 |
| --- | --- | --- |
| `modelAlias.WebPipeline` | `models.go` 的 `type modelAlias struct` | 与同结构里已有的 `Skill` / `ModelItemKey` / `TotalWindow` 一致，采用「名词短语 + bool」的字段命名；`WebPipeline` 直接对应代码注释里长期的「网页管线 / agent_mode=2」口径，不复用 `Skill`（语义是 skill 激活，不是链路选择） |
| `"web"` 别名 ID | `models.go` 的 `capabilityAliases` | 与 `default` / `pro` / `image` / `video` 同级、同为小写单词；对外暴露为 `doubao/web`，与 `modelIDPrefix` 拼接规则一致 |

**未新增函数**：`modeForModel` 与 `workModeForAlias` 只增加分支，不新增导出或非导出函数，避免为一次性判断抽出 `isWebPipelineAlias` 这类单实现 helper。

**引用一致性**：`WebPipeline` 只被 `modeForModel`（1 处）与 `workModeForAlias`（1 处）读取，`grep` 确认无第三处；`"web"` 字面量只在 `capabilityAliases` 出现一次，测试里通过 `doubao/web` 走 `normalizeModelName` 命中，不硬编码内部结构。

## STYLE-03 注释分层与定义位置

- 字段注释写在字段正上方、空行分隔，与 `modelAlias` 里既有字段（`Skill` / `ModelItemKey` / `MemoryProfile`）保持同一形态：一句话定义 + 空行 + 用途/理由段落。
- `modeForModel` 新增分支写成 `// 2. 网页链路别名：…`，与既有 `// 1.` / 原 `// 2.`（现 `// 3.`）/ 原 `// 3.`（现 `// 4.`）保持步骤编号连续；因为插入新步骤，后续两步编号同步顺延，未出现重号或跳号。
- `workModeForAlias` 新增分支写成 `// 3. 网页链路别名显式不进工作模式：…`，与既有 `// 1. 技能别名…` / `// 2.` 连续。
- 两个函数头都更新了 `最近修改时间：2026-11-15 23:55:00；改动原因：…`，`workModeForAlias` 的原「2026-11-14 02:05:00」被替换（函数体确实改了，必须刷新时间）。
- `active_ping.go` 的改动位点补了一行内联注释说明「网页链路别名没有 `model_item_key`，此时不能拼出空的上游模型名」，符合补丁位点注释要求。
- `statusCodeHint` 的 `case -1` 保留原有六行块注释（论证式块注释，记录五模型 × 双管线矩阵实测），文案里删掉「改用技能模式」的误导，改为指向 `doubao/web`。

## STYLE-04 函数签名与参数结构

- `modeForModel(requested string) generationMode`：签名未变。
- `workModeForAlias(alias modelAlias, allowWorkMode bool) generationMode`：签名未变；网页链路分支复用既有 `allowWorkMode=false` 的返回值路径，没有为它新增第三个参数。
- `runActiveTest(sa *StoredAuth, model string) map[string]any`：签名未变；只在拼 `message` 时引入局部变量 `upstream`，不改返回结构。
- `buildCompletionRequest` / `completionURL` / `streamCompletion` 全部未改：网页链路别名最终仍落到既有 `mode.WorkMode == false` 分支，不需要新参数。

## STYLE-05 结构与落点（结构体角色 / 目录位置 / 依赖方向）

- `WebPipeline` 放在 `modelAlias` 结构体内（别名语义的一部分），没有新建类型、没有新建文件、没有把链路选择逻辑挪出 `models.go`。
- 分支判断留在 `modeForModel`（模型名 → 生成模式）与 `workModeForAlias`（别名 → 生成模式）这两个既有职责位，没有下沉到 `completion.go`（请求构造层不该知道别名语义）。
- `active_ping.go` 的改动属于「测试成功消息的展示」，与它既有的 `message` 拼装同处一个函数，不跨越职责边界。
- 依赖方向未变：`models.go` 仍只依赖 `pluginapi` 与标准库（`strings` / `strconv`），没有引入新 import。

## STYLE-08 语言与框架特异写法

- Go 惯例：bool 字段零值即「非网页链路」，所以 `capabilityAliases` 里既有四条不需要补 `WebPipeline: false`，避免噪音。
- `strings.EqualFold(name, alias.ID)` 与同函数内其它别名匹配写法一致（大小写不敏感），没有改用 `==` 或 `ToLower` 比较。
- 清空字段用逐字段赋值（`mode.ModelItemKey = ""` …）而非重建结构体，保留上游 `mode := generationMode{...}` 的单一构造点，避免出现两份字面量。
- `strings.TrimSpace` 已在 `active_ping.go` 顶部导入并在同函数其它位置使用，没有新增 import。

## STYLE-09 测试资产与编码前契约

- 新增 `TestModeForModelWebPipeline`：覆盖四种写法（`doubao/web` / `DOUBAO/WEB` / `web` / `doubao/web:latest`）+ 反向守卫（`doubao/deepseek-v4-flash` 必须仍是 `WorkMode == true`），防止新分支把其它模型带走。
- 新增 `TestWebPipelineAliasReachesTestableList`：覆盖「活跃测试可选列表包含它」+「`testModeForModel` 返回 `WorkMode == false`」，对应本轮修复里 `-1` 文案承诺的出路必须真实存在。
- `TestCatalogueAliases` 的 `want` 同步加入 `doubao/web`，与 `catalogueModels` 的顺序（能力别名在前）一致。
- 测试全部留在 `doubao/` 包内的 `*_test.go`，未新增测试目录、未新增 fixture 文件、未引入外部依赖。

## 哨兵验证（原文）

把 `models.go` 的 `if alias.WebPipeline {` 改成 `if false && alias.WebPipeline {`：

```text
--- FAIL: TestWebPipelineAliasReachesTestableList (0.00s)
    models_test.go:153: 活跃测试里 doubao/web 也必须走网页链路，实际 {Skill:0 Kind:text ModelItemKey:person-deepseek-v4-1-flash-261017 MemoryProfile:300k_230 TotalWindow:300000 ReasoningEffort:4 WorkMode:true}
[cgo-shim] FAILED: go test ./... (exit 1)
```

恢复后 `python scripts/cgo-shim-build.py doubao` → `OK: go build ./...` / `OK: go vet ./...` / `ok ... 6.975s` / `all green (doubao)`。

## 无关 diff 与清理

- 本轮 diff 共 5 个代码文件 / **120 insertions(+), 10 deletions(-)**，逐文件核对：`models.go 41`、`models_test.go 65`、`protocol.go 9`、`completion_test.go 8`、`active_ping.go 7`，全部可追溯到「新增网页链路别名」与「改写 `-1` 文案」两个目标。
- 未做任何格式化批量改动、未重排无关代码、未删除历史死代码。
- `F:\cpa-plugin\cpa-shim-manual\` 为本轮临时探针工作副本，**已删除**；`$env:TEMP\fmtchk0122` / `fmtchk0122b` 为 gofmt 临时目录，**已删除**。

## 未命中说明

- **STYLE-06 公共复用**：本轮没有新增可复用的工具函数或公共组件；`WebPipeline` 是结构字段，`upstream` 是函数内局部变量，都不构成公共能力，不触发「新增导出工具函数须同步公共工具索引」的要求。
- **STYLE-07 接口契约**：本轮未改动任何对外 HTTP 接口路径、请求参数、响应结构或 Swagger/OpenAPI 文档。新增的 `doubao/web` 是**模型别名**（`/v0/models` 与面板测试下拉的来源），走的是既有的 `pluginapi.ModelInfo` 列表，字段结构未变；`/test-active` 的请求体（`auth_index` / `model`）与响应体（`ok` / `model` / `elapsed` / `message`）也未变，只是 `message` 文案与 `model` 取值集合扩大。

## 边界声明

本报告只判断代码风格、格式、位置、注释、日志、可读性与目录归位，不判断业务正确性、需求覆盖或发布放行。业务侧的对照实验证据记录在 `F:\cpa-plugin\doc\5-tests\2026-11-14_173500_doubao签到与活动赠送额度验证.md` 第 23 节。
