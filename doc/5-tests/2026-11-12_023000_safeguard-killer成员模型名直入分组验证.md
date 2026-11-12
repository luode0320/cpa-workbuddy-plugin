# safeguard-killer 0.1.5 验证记录 — 成员模型名直入智能分组

- 日期：2026-11-12 02:04 ~ 20:30
- 对应提交：`7c6da39`（feat）/ `e73eeea`（registry）
- 生产版本：0.1.5（`hot reloaded active_version=0.1.5 retired_version=0.1.4`）

## 1. 本地验证

| 项 | 结果 |
|---|---|
| `python scripts/cgo-shim-build.py safeguard-killer` | build / vet / test 全绿 |
| 新增测试 | `TestRouteGroupClaimsRealModelNames`、`TestResolveTargetGroupRotatesToRequestedModel`、`TestRotateModels`、`TestProductionStyleGroupEntersByMemberModel` |
| 哨兵验证 | 插入 `TestSentinelGroupsZ` → 输出 `--- FAIL`，证明新测试真实进编译；删除后全绿 |
| 变异验证 1 | 禁用 `groupServing` 命中 → 3 条用例 FAIL（捕获） |
| 变异验证 2 | `rotateModels` 恒返回原序 → 3 条用例 FAIL（捕获） |
| gofmt | `groups.go` / `groups_test.go` LF 归一后无输出 |

## 2. 递归安全性（宿主源码级证据）

克隆 `router-for-me/CLIProxyAPI` 逐行确认：

- `internal/pluginhost/host_callbacks.go:445-446` — `SkipRouterPluginID` 与 `SkipInterceptorPluginID` 同时写入发起插件 id
- `sdk/api/handlers/handlers_routing.go:365,380` — `applyModelRouter` 据此跳过该插件自身
- `sdk/api/handlers/handlers_model_router_test.go:542` — 宿主断言守护 `"was re-entered despite SkipRouterPluginID"`
- `internal/pluginhost/executor_route.go:85-92` — `ExecutePluginExecutor` 注释 "without changing the requested model"

结论：分组内部下发真实模型名不会被自己二次接管。

## 3. 发布链路

| 步骤 | 结果 |
|---|---|
| 代码提交 | `7c6da39`，6 文件 +156/-6，路径限定提交 |
| push | 本地 push 被 reject（远端已被并行会话推进到 `bb77f12`）；`git merge-base --is-ancestor` 验证远端已含本提交（并行会话 push 携带），无需重推 |
| CI dispatch | `plugin=safeguard-killer` + `version=0.1.5` + `allow_partial=true`，run `43072871871` |
| CI 结果 | `Release` job = **success**，release 实体 5 资产；`darwin/arm64` cancelled、`windows/arm64` 失败（已知平台缺件） |
| 资产校验 | `sha256sum -c checksums.txt` 4/4 OK，另用 `Get-FileHash` 独立复核一致 |
| registry | `publish-assets.py safeguard-killer 0.1.5 --allow-missing` → commit `e73eeea` |
| 远端验证 | registry 0.1.5，4/4 size+sha256 ALL PASS，无 0.1.4 残留 |
| 生产部署 | `POST /v0/management/plugin-store/safeguard-killer/install?version=0.1.5&source=source-636fa01121af`（多源必须带 source，源 id 现取不硬编码） |
| 落盘校验 | `.so` sha256 `ffc83bb42c4e8e602eb9eb1c219fbaeb068cfbbb9ffaffb3382bce4b89b2d2ee` 与本地 zip 内 `.so` 逐字节一致 |
| 热重载 | `hot reloaded active_version=0.1.5 retired_version=0.1.4` |

## 4. 生产行为验收（真实入口，非冒烟）

请求 `POST /v1/chat/completions`，`model=DeepSeek-V4-Pro`（分组成员，非虚拟 id），非流式，提示词「只回复：收到」：

| 序号 | 模型 | 结果 |
|---|---|---|
| 1 | DeepSeek-V4-Pro | 失败（上游 11102 service info not found） |
| 2 | Doubao-Seed-2.1-Pro | 失败（model_not_found） |
| 3 | GLM-5.3 | 失败（10605 上游排队） |
| 4 | GLM-5.3-Flash | 失败（11102） |
| 5 | **deepseek-v4.1-flash** | **成功，返回「收到」，finish_reason=stop** |

- 响应含 `⚡ [Safeguard Killer] ` 通知前缀，完整列出失败链与服务模型
- 面板「最近决策」由 **0 → 13 → 200**（环形缓冲上限），最新 group 决策 `served by deepseek-v4.1-flash`
- 插件状态 `configured/registered/enabled/effective_enabled` 全 true
- 容器在 04:39:23 有一次外部重启（非本轮发布动作；本轮全程走 install 热重载，未执行任何重启），重启后插件重新 `loaded 0.1.5`、状态四项全 true、落盘哈希未变、group 决策持续产生

## 5. 遗留（配置侧，非插件缺陷）

生产分组 `id: "1"` 的 11 个成员中，3 个模型名在宿主 `/v1/models`（共 50 个）中不存在：

- `Doubao-Seed-2.1-Pro` — doubao-provider 已于 2026-10-22 下线
- `hy4`
- `qwen3.8-flash` — 真实名为 `qwen3.8-max`

这些名在链路中只会白耗一次失败尝试（约数秒），建议在面板删除或改名。

`DeepSeek-V4-Pro` 虽在模型列表内，但上游报 11102（账号侧无该服务），与插件无关。

## 6. 结论

**PASS** — 功能在生产真实流量上生效，链式切换、通知提示、会话粘性均按设计工作；发布链路完整、证据可回溯。
