---
reader_level: business_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
source: TraeWork 签到 x-device-id 拼接与尾零风控 9074 拦截修复（v0.2.5）
date: 2026-10-10
status: passed
---

# TraeWork 签到 x-device-id 风控修复回归

结论：验证通过。`traework-provider` v0.2.5 针对签到接口 `/trae/api/v2/ug/checkin_credits/claim` 的 9074 风控拦截修复已通过本地 `cgo-shim-build.py traework` 编译、`go vet` 与单元测试全绿验证（含必失败哨兵反证），并在生产服务器针对目标账号「用户04878311608」（`1114256688551036`）及全量 TraeWork 账号完成签到与状态验证。

影响：消除因 `<baseDeviceID>-<userID>` 字符串拼接及旧版 `randomDeviceID` 末尾补零/首位越界导致的 `9074 当前参与用户太多，请稍后再试` 持续失败，同时在遇到 9074 或 9095 设备冲突时自动切换新 16 位数字设备号重试。

范围：`traework/checkin.go`、`traework/browserlogin.go`、`traework/checkin_headers_test.go`、`traework/browserlogin_test.go`、`test/traework/checkin_device_rotation_test.go`。

非范围：不改动对话流转发与宿主桥接逻辑。

完成标准：`python scripts/cgo-shim-build.py traework` 全绿；必失败哨兵确证新测试进入编译；生产账号「用户04878311608」签到返回 `ok: true` 且积分包到账。

验证状态：已完成。

## 1. 本地编译与单元测试证据

1. 必失败哨兵反证（`test/traework/checkin_device_rotation_test.go` 插入 `t.Fatal("SENTINEL_CHECKIN_DEVICE_ID_TEST")`）：
   - 命令：`python scripts/cgo-shim-build.py traework`
   - 结果：`--- FAIL: TestCheckinAccount_RotatesDeviceIDOn9074AndDeviceBlocked (0.51s) checkin_headers_test.go:78: SENTINEL_CHECKIN_DEVICE_ID_TEST`，证实测试真实参与编译执行。
2. 移除哨兵后回归：
   - 命令：`python scripts/cgo-shim-build.py traework`
   - 结果：`go build ./...` OK、`go vet ./...` OK、`go test ./...` OK（`2.516s`），`[cgo-shim] all green (traework)`。
3. 覆盖用例：
   - `TestDeviceIDFor`：验证空输入、单一合规 16 位设备号/用户号复用、生产「用户04878311608」同款尾零 `9670064000000000` + `1114256688551036` 确定性派生为合规 16 位数字且同设备多账号互异。
   - `TestRandomDeviceIDShape`：连续采样 50 次验证 `randomDeviceID()` 恒为 16 位纯数字、首位 `'1'~'3'`、末位 `'1'~'9'`、无连续尾零。
   - `TestCheckinAccount_RotatesDeviceIDOn9074AndDeviceBlocked`：通过 `httptest.NewServer` 模拟第 1 次返回 `9074`、第 2 次返回 `9095`、第 3 次返回 `code:0`，验证自动切换不同 16 位合规设备号重试并成功返回 100 积分。

## 2. 生产真实账号验证证据

- 目标账号：「用户04878311608」（`uid=1114256688551036`，`auth_index=76bc7754f3fd72b2`）
- 修复前状态：`checkin_today=false`，`pack_count=2`，`total_remain=130`，`total_size=600`，调用 `/checkin` 返回 `{"ok":false,"message":"当前参与用户太多，请稍后再试"}`。
- 切换合规 16 位数字 `x-device-id` 后上游响应：`{"code":0,"message":"success"}`；`/trae/api/v2/ug/checkin_credits/status` 变为 `checked_in: true`；积分包新增 `签到奖励 limit=100 start=1791639996 (2026-10-10 21:46:36)`，`pack_count` 从 2 增至 3，`total_remain` 从 130 增至 230，`total_size` 从 600 增至 700。
