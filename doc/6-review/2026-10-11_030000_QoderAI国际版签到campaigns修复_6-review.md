# 6-review 风格回归：Qoder AI 国际版签到改走 campaigns API（0.1.4 / 0.1.5）

- 日期：2026-10-11 03:00
- 范围：qoder-ai/main.go、qoder-ai/billing.go、qoder-ai/checkin.go、qoder-ai/management.go、qoder-ai/panel.html、qoder-ai/qoder_ai_endpoints_test.go、qoder-ai/CHANGELOG.md、qoder-ai/VERSION
- 结论：STYLE: PASS

## 检查步骤（static_owner_router.py 推导）

- STYLE-01 静态格式与编码一致性：变更文件均为 UTF-8；Go 文件经 `cgo-shim-build.py` 的 build/vet 校验通过，格式与仓库既有 Go 写法一致。
- STYLE-02 命名与符号引用一致：新增 `campaignHeaders` / `campaignStatusResponse` / `campaign` / `benefit` / `pickCheckinCampaign` 命名与既有 `*Response` / `fetch*` 风格一致；端点常量沿用 `endpoint*` 前缀。
- STYLE-03 注释分层与定义位置：新增/修改函数注释含 `[参数]`/`[返回]`/`最近修改时间` 元信息，其余位置为单行目的说明。
- STYLE-04 函数签名与参数结构：`pickCheckinCampaign` 返回多值而非输出参数；`performCheckinCall(sa, campaignID)` 参数清晰，与调用方一致。
- STYLE-08 语言与框架特异写法：Go 惯用法，`json.Unmarshal` + map 断言显式判空；时间窗用 `time.Time.Unix()` 比较。
- STYLE-09 测试资产与编码前契约：回归测试落在 `qoder-ai/qoder_ai_endpoints_test.go`，命名 `TestPickCheckinCampaign_*` 符合仓库惯例。

## 证据

- `python scripts/cgo-shim-build.py qoder-ai`：build / vet / test 全绿。
- 反证：临时把 `CLAIM_BENEFIT` 判定改成错误值，`TestPickCheckinCampaign_ClaimBenefit` 必失败（`active=false; want true`）。
- panel.html 两段 script `node --check` 通过。
- 变更 diff：端点常量替换、billing 新增 campaigns 解析、checkin 传参 + 统计单列，无无关格式化或整文件重排。

## 负向边界

本记录只判断写法、位置、格式与既有习惯，不判断业务正确性、需求覆盖或发布放行。
