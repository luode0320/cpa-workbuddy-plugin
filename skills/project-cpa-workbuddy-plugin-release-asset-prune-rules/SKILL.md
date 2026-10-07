---
name: project-cpa-workbuddy-plugin-release-asset-prune-rules
description: 当用户要求"清理发布缓存 / 清理 release-assets / 发布后清理旧版本 / 清一下发布产物 / 发布相关磁盘清理"，或一次发布闭环完成后需要收口清理时触发。负责把 release-assets/ 下除 registry.json 当前版本外的历史版本目录安全删除（git rm + 提交推送），并清理本地未跟踪缓存（.workbuddy/release-assets）与发布临时目录。发布主链路在 project-cpa-workbuddy-plugin-release-rules，本 skill 只补"发布后清理"这一段，不重复发布步骤。
---

# cpa-workbuddy-plugin 发布后资产清理

## Skill 作用与适用场景

- 每次发布闭环（Step 1-13）后，`release-assets/` 会不断累积历史版本目录；一个版本目录约 20MB×7 平台 zip，几十个版本即数 GB。
- 本 skill 负责把"不再被 registry 引用"的历史版本目录安全删除并提交，保持仓库与本地磁盘干净。
- **默认定位**：发布完成后自动执行一轮清理；用户单独说"清理发布缓存 / 清理 release-assets"时同样触发。
- 边界：发布执行归 `project-cpa-workbuddy-plugin-release-rules`，本地编译验证归 `cgo-plugin-isolated-test`；本 skill 只管"发布后的资产/缓存清理"。

## 自动触发信号

- 用户说"清理发布缓存""清理 release-assets""把旧的发布产物删掉""发布后清理""清一下磁盘（发布相关）"。
- 一次发布闭环（含 Step 12 远端验证）完成后收口。
- 发现 `release-assets/` 目录体积异常增大（历史版本堆积）。

## 保留集判定铁律

- **保留集 = `registry.json` 中每个 plugin 的 `<id>-<version>`**（即"当前版本"），对应目录 `release-assets/<id>-<version>/` 必须保留，因为远端 `raw.githubusercontent.com/.../main/release-assets/<id>-<version>/...` 与生产 `plugin-store install` 都直接指向它。
- **删除集 = `release-assets/` 下不在保留集里的所有版本目录**（历史版本）。
- 判定必须逐插件读 `registry.json` 的 `id` + `version`，**不得用"最新 N 个""最近日期"等启发式**。
- 删除前二次断言：保留集目录不得出现在删除列表。
- 保留集目录若不存在（如某插件尚未发布资产）→ 只跳过，不报错。

## 默认执行流程

1. 读 `registry.json`，取每个 plugin 的 `id` + `version` 组成保留集。
2. `python scripts/prune-release-assets.py --dry-run`：列出保留集 / 待删集 / 待删体积，人工核对。
3. `python scripts/prune-release-assets.py --apply`：脚本对受跟踪文件用 `git rm -r`（随提交入库）、对空/未跟踪目录用目录删除。
4. **只暂存 `release-assets/` 的删除**：`git diff --cached --name-only` 必须全部以 `release-assets/` 开头，确保不混入其它会话/并行改动。
5. 提交 + push（本仓库默认发布授权，见 `AGENTS.md` 的「提交 / 发布授权」段）：
   - `git commit -m "chore(release): [清理旧发布缓存] 删除 release-assets 历史版本目录（仅保留 registry 当前版本）"`
   - `git push origin main`
6. 远端抽查：当前版本资产的 raw URL HEAD 应为 200；任一被删旧版本应为 404。
7. 记录释放体积（前/后）到收口说明。

## 本地未跟踪缓存与临时目录清理

- `release-assets/`（仓库根，**受 git 跟踪**）→ 走上面的 `git rm` 流程。
- `.workbuddy/release-assets/`（**被 `.gitignore` 忽略**，本地工具缓存）→ 直接删目录即可；它不在 registry 引用路径上，但仍属发布缓存，可一并清理。
- 发布临时目录（`/tmp/cpa-0xxx`、`cpa-shim-*`、`.tmp_verify/`）按发布 skill Step 13 收口清理，本 skill 复核其无残留。

## 脚本

`scripts/prune-release-assets.py`（本 skill 随附）：

```bash
python scripts/prune-release-assets.py --dry-run   # 只报告
python scripts/prune-release-assets.py --apply     # git rm 旧目录 + 删空目录
```

- 只作用于 `release-assets/` 一层子目录，绝不触碰保留集与仓库其它文件。
- `--dry-run` 与 `--apply` 互斥且必填其一，避免误删。
- 受跟踪目录用 `git rm -r -q`（未暂存则命令整体失败、不留半成品）；空/未跟踪目录用 `rmdir`（非空自动跳过）。

## 收口校验

- `release-assets/` 只剩保留集目录。
- `git diff --cached --name-only` 全部在 `release-assets/` 下。
- 远端当前版本资产 200、被删旧版本 404。
- 记录释放体积（如 3.75 GB → 0.13 GB）。

## 权责边界与不负责事项

- 只负责"发布后的资产/缓存清理"，不负责发布执行、代码验证、需求/Bug/编码。
- 不删除 registry 当前版本目录（删除会直接导致生产下载 404）。
- 不用启发式（日期/数量）判定保留集，只认 `registry.json`。
- 不把清理动作与发布动作混在同一提交；清理单独成提交，便于回滚。

## 执行通过 / 驳回标准

- 通过：保留集完整（registry 每插件当前版本目录都在）、删除集 = 全部历史版本目录、暂存区只含 `release-assets/` 删除、远端当前版本 200 + 旧版本 404。
- 驳回：误删当前版本目录、暂存区混入非 `release-assets/` 改动、用启发式替代 registry 判定、未提交推送（在默认授权下）。
