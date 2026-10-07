---
schema_version: 1
template_version: v1
doc_id: STYLE-WB-RELEASE-ASSET-PRUNE-20261007
doc_type: style_regression
source_ids: [GOAL-WB-RELEASE-ASSET-PRUNE-20261007, IMPL-WB-RELEASE-ASSET-PRUNE-20261007, TEST-WB-RELEASE-ASSET-PRUNE-20261007]
status: accepted
version: v1.0
current_slice: 发布后清理规则（项目 skill + AGENTS/CLAUDE + 发布链 Step 13.5 + prune 脚本）与 release-assets 瘦身
updated_at: 2026-10-07 21:35:00
reader_level: business_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
---

# 6-review — 发布后清理规则固化与 release-assets 瘦身

结论：本轮规则、skill、脚本与记忆改动通过风格回归（STYLE: PASS）。影响：仓库首次把「每次发布后清理不需要的垃圾」固化为可执行规则与脚本，`release-assets` 由 3.75 GB 降到 0.13 GB。范围：`AGENTS.md`、`CLAUDE.md`、项目 skill `project-cpa-workbuddy-plugin-release-asset-prune-rules`、发布 skill `project-cpa-workbuddy-plugin-release-rules`、`scripts/prune-release-assets.py`、项目记忆三文件。非范围：本记录不判断清理策略的业务取向是否正确、不批准发布、不把本地校验写成生产验收。变化：新增一个项目 skill 与一个通用脚本，规则文件新增「发布后清理」条，发布 skill 新增 Step 13.5。完成标准：`quick_validate.py` 取 `Skill is valid!`；`prune-release-assets.py --dry-run` 幂等（0 待删）；记忆锚点脚本 `ok=true`（20/20）；`git diff --check` 干净。验证状态：静态与脚本级验证通过；规则与 skill 已提交推送（commit `870e45c`），清理动作已提交推送（commit `095f4a4`）。图片资产决策：N/A，原因：只核对 Markdown 规则、脚本与记忆文本，不引入位图。

## 文档信息

- 来源对象：用户请求「这个规则要吸收到项目 skill 和规则中，每次发布后都需要清理不需要的垃圾。」
- 实施记录：`AGENTS.md` / `CLAUDE.md`「仓库与发布」小节新增「发布后清理」条；发布 skill Step 13.5；项目 skill `project-cpa-workbuddy-plugin-release-asset-prune-rules`；`scripts/prune-release-assets.py`。
- 真实测试：`python scripts/prune-release-assets.py --dry-run`（保留集 7、待删 0，幂等）；`quick_validate.py` → `Skill is valid!`；`check_memory_anchors.py --json` → `ok=true`。
- 规则来源：`AGENTS.md` 既有「仓库与发布」写法、`code-style-consistency-rules`、`PROJECT_STYLE.md`。
- 发布放行：N/A。原因：不属于 `6-review` 职责。证据：本轮未改插件二进制，未走插件发布链。

## 检查范围

- 规则文件：`AGENTS.md`、`CLAUDE.md`（各 +1 行，纯 CRLF）。
- 项目 skill：`skills/project-cpa-workbuddy-plugin-release-asset-prune-rules/`（新增 SKILL.md + agents/openai.yaml + references/source-notes.md）。
- 发布 skill：`skills/project-cpa-workbuddy-plugin-release-rules/SKILL.md`（description + Step 13.5 + 速查表行）与 `references/source-notes.md`。
- 脚本：`scripts/prune-release-assets.py`（新增）。
- 记忆文件：`PROJECT_MEMORY.md`、`PROJECT_HISTORY.md`、`PROJECT_CURRENT.md`。
- 文档资产：本 `6-review` 记录。

## 6-review 结论

STYLE: PASS

## 检查清单

| 类别 | 结果 | 证据 |
| --- | --- | --- |
| 格式与编码 | PASS | 新增/修改文件均为合法 UTF-8 无 BOM；`quick_validate.py` 解析 frontmatter 通过 |
| 尾随空白 | PASS | `git diff --cached --check` 退出码 0，命中 0 处 |
| 标题层级 | PASS | 新 skill 用 `##` 二级标题，与既有项目 skill 一致；Step 13.5 与 Step 14 均为 `###`，编号连续 |
| 数字口径 | PASS | 「3.75 GB → 0.13 GB」「185 目录 / 1469 文件」「保留 6 版本目录」与实操一致；锚点 20/20 |
| 术语一致 | PASS | 统一使用「发布后清理」「保留集」「registry 当前版本」，未混用「缓存」歧义表述 |
| 注释与说明 | PASS | 脚本 docstring 说明用法与保留集定义；SKILL.md 说明「为什么保留当前版本」 |
| 临时产物 | PASS | `__pycache__/` 被 `.gitignore` 忽略、未跟踪；无遗留临时脚本 |
| 改动范围 | PASS | 仅规则、skill、脚本、记忆与 6-review 文档；未触碰插件源码、VERSION、registry |
| 业务正确性 | N/A | 原因：由真实测试负责；证据：本轮无插件二进制改动 |
| 需求覆盖与发布 | N/A | 原因：不属于风格回归职责；证据：本轮为规则与清理改动 |

## 6 维交付残留自查

- 维度 1 需求覆盖：用户诉求「吸收到项目 skill 和规则」「每次发布后清理」→ 落点：项目 skill `project-cpa-workbuddy-plugin-release-asset-prune-rules`、`AGENTS.md`/`CLAUDE.md`「发布后清理」条、发布 skill Step 13.5、`scripts/prune-release-assets.py`。全覆盖。
- 维度 2 影响面：新增脚本与 skill 不改任何既有契约面（无改名/改签名/改配置键）；`registry.json` 与 `release-assets` 路径约定未变。`N/A + 不涉及契约面`。
- 维度 3 残留物：`git status --porcelain` 逐项归类；`scripts/__pycache__/` 被忽略且未跟踪；无新增后台进程（本轮未启动服务）。`prune-release-assets.py` 产生的临时无。
- 维度 4 文档与引用一致性：新增 skill 被 `AGENTS.md`/`CLAUDE.md` 与发布 skill Step 13.5 双向引用；`scripts/prune-release-assets.py` 被 skill 与规则引用；无零引用孤立文件。
- 维度 5 口径一致性：体积、目录数、版本目录数与实际一致；`release-assets` 现存 6 个目录与 registry 当前 6 版本对齐（cursor-provider 目录待其发布补齐，非本轮范围）。
- 维度 6 验证有效性反审：`quick_validate.py` 为独立外部判据；`prune-release-assets.py --dry-run` 用真实 `registry.json` 计算保留集（非硬编码）；远端 200/404 抽查为独立外部证据。无自我指涉。

## 问题与修复

- 首次 `git rm` 因空目录 `workbuddy-0.14.27`（无跟踪文件）整体失败，改为「有跟踪文件用 `git rm -r`、空目录用 rmdir」两步处理，已在 skill 与脚本中固化。
- Step 13.5 首次插入位置错误（落在 Step 14 标题与正文之间），已修正为 Step 13.5 在前、Step 14 标题紧接其正文。
- 未发现需要继续修改的格式、命名、注释、日志、可读性或目录问题。

## 执行附录

```text
quick_validate: Skill is valid!
prune --dry-run: keep=7, to_delete=0 (idempotent)
MEMORY_ANCHORS: PASS (ok=true, history 20/20)
git diff --cached --check: clean
```

## 追踪附录

| 追踪 ID | 对应内容 | 证据 |
| --- | --- | --- |
| IMPL-001 | 新增项目 skill 与 prune 脚本 | `skills/project-cpa-workbuddy-plugin-release-asset-prune-rules/`、`scripts/prune-release-assets.py` |
| IMPL-002 | 规则写入 AGENTS/CLAUDE | 两文件「仓库与发布」小节「发布后清理」条 |
| IMPL-003 | 发布链新增 Step 13.5 | `project-cpa-workbuddy-plugin-release-rules/SKILL.md` |
| IMPL-004 | release-assets 瘦身 | commit `095f4a4`（1469 删除） |
| TEST-001 | skill 结构校验 | `quick_validate.py` → `Skill is valid!` |
| TEST-002 | prune 脚本幂等 | `prune-release-assets.py --dry-run` → 0 待删 |
| TEST-003 | 记忆锚点健康 | `check_memory_anchors.py --json` → `ok=true` |
| EVIDENCE-001 | 远端当前版本资产可达 | raw URL HEAD 200（0.15.3 zip/checksums） |
| EVIDENCE-002 | 远端旧版本已下架 | raw URL HEAD 404（0.15.2 zip） |
