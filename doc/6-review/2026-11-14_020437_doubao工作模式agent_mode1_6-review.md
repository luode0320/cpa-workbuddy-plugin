# 6-review 风格回归记录 — doubao-provider 0.1.7（工作模式 agent_mode=1 接入）

- 日期：2026-11-14 02:04
- 审查对象：`doubao/` 工作模式改造（13 文件 +1060/−122，另新增 `doubao/workmode_test.go` 362 行）
- 范围：`doubao/protocol.go`、`doubao/models.go`、`doubao/completion.go`、`doubao/main.go`、`doubao/authfile.go`、`doubao/model_discovery.go`、`doubao/models_test.go`、`doubao/workmode_test.go`、`doubao/README.md`、`doubao/CHANGELOG.md`、`doubao/VERSION`、`registry.json`、`.github/workflows/build.yml`

STYLE: PASS

## 检查范围

- 测试证据：`doc/5-tests/2026-11-14_020437_doubao工作模式agent_mode1验证.md`（`TEST-*`）
- 改动文件：见上「范围」
- 规则来源：`PROJECT_STYLE.md`、`code-style-consistency-rules/references/style-review-pipeline.md`、`comment-rules`
- 路由命令：`python code-style-consistency-rules/scripts/static_owner_router.py --root 'C:\Users\luode\.workbuddy\skills' --changed <13 个路径>` → 命中 7 步（STYLE-01/02/03/04/05/08/09）

## 风格证据

- `STYLE-01` **静态格式与编码一致性**：46 个 `doubao/` 文件逐字节扫描 → 全部 UTF-8 无 BOM；换行全部 CRLF 且 `crlf == lf` 一致（`.gitattributes` 为 `* text=auto`、`core.autocrlf=true`，索引侧 `i/lf`、工作区 `w/crlf`，与仓库既有口径一致，无 CRLF 漂移）；`doubao/VERSION` 7 字节 `0.1.7\r\n` 无 BOM；无尾随空白；40 个 Go 文件 CRLF→LF 归一后 `gofmt -l` 无输出。未引入项目未使用的格式化风格。**PASS**
- `STYLE-02` **命名与符号引用一致**：新增 19 个符号全部沿用同域既有前缀——`build*`（`buildClientQuery` / `buildClientOption` / `buildTaskInputJSON` / `buildSkillOption` / `buildWorkOption`，与既有 `buildCompletionRequest` / `buildBusinessQuery` / `buildAccountInfoQuery` 同构）、`*For*`（`localPermissionsFor` / `deviceIDFor` / `aliasForWorkModel` / `clientEndpointURL`）、`work*` 前缀集中表达工作模式语义（`workModeForAlias` / `workDeviceName` / `workHomeDir`）。无同义不同名（工作模式统一用 `WorkMode` / `work_*`，未出现 `agentMode` / `clientMode` 混用）。**PASS**
- `STYLE-03` **注释分层与定义位置**：17 个本轮新增/改动的函数逐个校验函数头三要素（`[参数]` / `[返回]` / `最近修改时间 yyyy-MM-dd HH:mm:ss + 一句话改动原因`）→ 17/17 齐全；函数体按 `comment-step-numbering-gate` 在 ≥6 非空行块内补顶层编号步骤（`buildClientQuery` 3 段、`collectCompletion` 6 段、`streamCompletion` 3 段、`postBusinessJSON` 2 段、`configurePlugin` 3 段等）；编号紧贴步骤首行，无集中写在函数开头；字段与结构体字面量只写一句话作用，未平摊完整格式；注释全部简体中文。**未改动的 tag 遗留函数不补编号**，避免无关 diff（`randomUUID` / `kindForAlias` / `scanSSE` / `newUpstreamClient` / `buildBusinessQuery` / `statusCodeHint` / `buildAccountInfoQuery` 的补充曾落地后回退）。**PASS**
- `STYLE-04` **函数签名与参数结构**：`buildWorkOption(cfg Config, sa *StoredAuth, mode generationMode, localMessageID string, needCreate bool)` 参数顺序为上下文（cfg/sa）→ 领域值（mode/localMessageID）→ 开关（needCreate），与既有 `buildCompletionRequest(sa, text, mode, conversationID, lastSectionID, lastMessageIndex, needCreate)` 同序；同源可选字段已收敛进 `Config`（七项 `work_*`）与 `generationMode`（`ModelItemKey` / `MemoryProfile` / `TotalWindow` / `ReasoningEffort` / `WorkMode`），未把可选字段平铺成参数、未用多行签名代替收敛。**PASS**
- `STYLE-05` **结构与落点**：新增结构体全部为内部传输/临时结构（`conversationInitOption` / `conversationInitExt` / `agentTaskParam` / `agentTaskParamChange` / `agentWorkspace` / `clientOption` / `generalTaskParam` / `agentsMD` / `taskInput`），与既有 `clientMeta` / `completionOption` / `aggregateParams` 同处 `doubao/completion.go` 请求体段，未误进公共层；无新增目录；依赖方向仍为 `main → executor → completion → protocol`，`models.go` 不反向依赖 `completion.go`（`generationMode` 定义在 `protocol.go`）。**PASS**
- `STYLE-08` **语言与框架特异写法**：常量集中声明于 `protocol.go` 单一 const 块（新增 15 个客户端常量紧随 `skillVideoGen` 之后，未散落）；未引入自定义枚举类型；字符串归一化只在必要处（`aliasForWorkModel` 的 `EqualFold`、`atoiSafe` 的逐字符前缀解析），未做无意义 `strings.TrimSpace` 泛化；无包别名新增；`generationMode` 由 2 字段扩到 7 字段仍用值语义传递（小结构体，避免指针逃逸）。**PASS**
- `STYLE-09` **测试资产与编码前契约**：`doubao/workmode_test.go`（362 行 / 11 个用例）与被测包同目录，与仓库既有 Go 惯例一致（`workbuddy` 76 个、`traework` 72 个、`qoderwork` 58 个测试文件均为源码目录内 `*_test.go`，`copilot` 30 个生产文件 0 个同目录测试亦非本插件范式）；`test/doubao/` 现有 8 个探针脚本仍保留原职责未搬迁。测试资产未落 `doc/`。编码前风格契约已在写码前形成（读取 `PROJECT_STYLE.md` + `completion.go` / `models.go` / `protocol.go` 同域写法）。**PASS**

## 附加检查（超出 7 步流水线但本轮必需）

| 项 | 结论 | 证据 |
|---|---|---|
| 测试污染扫描 | PASS | `scan_test_pollution.py --root F:\cpa-plugin\doubao` 首跑 13 条 P4 FAIL，全部命中 `*_test.go` 内的 `os.Getenv("DOUBAO_LIVE_TEST")`；扫描器按顶层 `test/` 分桶、不识别 Go 后缀约定，故按同一 skill 的豁免通道登记 `doubao/.test-pollution-allowlist`（5 条键，逐条注明「live 测试开关，仅存在于测试文件内」）→ 复跑 `POLLUTION: PASS` |
| 注册表面量 | PASS | `validate-registry.py registry.json` → `OK registry.json: 13 plugin(s), schema_version=2` |
| 版本三处一致 | PASS | `doubao/VERSION`=`0.1.7`、`doubao/main.go:67 var version = "0.1.7"`、`doubao/CHANGELOG.md` 顶部 `## 0.1.7`、`registry.json` `doubao-provider.version`=`0.1.7` 四处对齐 |
| 最小改动边界 | PASS | 全部改动可追溯到「接入工作模式」单一目标；tag 遗留函数零改动（`cookiejar.go` / `failover.go` / `scheduler.go` / `login.go` / `management.go` / `stream.go` / `convert.go` / `creation*.go` 未进 diff）；`model_discovery.go` 仅 1 行签名适配（`applyUpstreamHeaders(req, false)`） |
| 配置项注册完整 | PASS | `main.go Config` 十项 ↔ `defaultConfig` ↔ `configurePlugin` ↔ `doubaoRegistration().Metadata.ConfigFields` 四处键名一一对应（`work_bot_id` / `work_mode_id` / `work_reasoning_effort` / `work_memory_profile` / `work_total_window` / `work_enable_sandbox` / `work_workspace`） |

## 边界声明

- 业务正确性：N/A + 原因：由真实测试负责 + 证据：`doc/5-tests/2026-11-14_020437_doubao工作模式agent_mode1验证.md` 的 PASS=147 / SKIP=13 / FAIL=0
- 需求覆盖与发布放行：N/A + 原因：不属于 6-review 职责 + 证据：本轮为插件内能力扩展，发布放行由提交与 CI 环节判定
- 未回归项（真实 Cookie 打通、图/视频生成链路、生产热重载）已在测试文档第 4 节明确声明，不在本记录内默认通过
