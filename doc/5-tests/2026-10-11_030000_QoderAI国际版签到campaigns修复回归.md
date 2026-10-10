---
schema_version: 1
doc_id: TEST-QODERAI-CHECKIN-CAMPAIGN-20261011
doc_type: test
source_ids: [BUG-QODERAI-CHECKIN-FALSE-SUCCESS-20261010]
status: passed
version: v1.0
updated_at: 2026-10-11 03:00:00
reader_level: business_general
writing_style: plain_chinese
---

# Qoder AI 国际版签到改走 campaigns API 回归验证

## 验证范围

- 国际版签到端点由 CN 专有 `/sash/api/v1/me/daily-check-in/*` 改为 campaigns API。
- 新增 `campaignHeaders`（`Cosy-ClientType:10` + `User-Agent:Qoder`）与 `pickCheckinCampaign` 活动挑选。
- `performCheckinCall(sa, campaignID)` 改 POST `.../campaigns/{campaignId}/claim`。
- 批量签到统计新增 `no_activity`，不再把无活动账号计入 fail。
- 不改推理链路、模型映射、账号调度与生命周期、宿主 ABI。

## 验证入口

- 真实测试入口：`python scripts/cgo-shim-build.py qoder-ai`（build + vet + test）。
- 测试文件：`qoder-ai/qoder_ai_endpoints_test.go`（与源码同目录）。

## 用例与结果

| 用例 | 断言 | 结果 |
| --- | --- | --- |
| TestQoderAI_GlobalEndpointsContract | 端点常量指向 campaigns（含 claim 前缀） | PASS |
| TestPickCheckinCampaign_ClaimBenefit | 挑出窗口内 CLAIM_BENEFIT 的 CREDITS 活动，识别 CLAIMED | PASS |
| TestPickCheckinCampaign_OutOfWindow | 窗口外活动不算 active | PASS |
| TestPickCheckinCampaign_NoCampaign | 无活动时全零 | PASS |

## 必失败反证

把 `pickCheckinCampaign` 的 `ActionType != "CLAIM_BENEFIT"` 临时改成错误值后执行测试：

```
--- FAIL: TestPickCheckinCampaign_ClaimBenefit (0.00s)
    qoder_ai_endpoints_test.go:64: active=false; want true (CLAIM_BENEFIT CREDITS campaign in window)
FAIL
```

恢复正确实现后重跑，build / vet / test 全绿。panel.html 两段 script `node --check` 通过。

## 生产真实端到端验证（2026-10-11）

- 三个 qoder-ai 账号带 `Cosy-ClientType:10`+`User-Agent:Qoder` 头 `GET /sash/api/v1/me/campaigns` 均返回真实活动（`campaignKey=act-20261009-118`, `benefit=CREDITS/100/30天`）。
- `ua554edc3` 识别 `active=true, checked=true, credit=100`；`POST .../{campaignId}/claim` 返回 200 `status=CLAIMED`。
- 生产 0.1.5 面板 `/accounts` 如实显示：2 账号 `active:true + campaign_id`，5 账号 `active:false`（无窗口内活动），不再出现 404。
- 生产批量 `/checkin`：`summary = {already:2, no_activity:5, fail:0}`（0.1.4 曾误报 `fail:5`）。

## 结论

本地 build / vet / test 全绿，反证成立；生产 0.1.4 / 0.1.5 部署与行为验收通过。
