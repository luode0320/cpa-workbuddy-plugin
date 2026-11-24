# 6-review：ChinaCPA 视频生成逻辑吸收 CYCLE02（失败快速换号）

STYLE: PASS

## 检查范围
- 测试证据：`python scripts/cgo-shim-build.py doubao` 全绿（go build/vet/test OK，go test 约 65s，EXITCODE=0）；哨兵反证把早退改成 `if false && errors.As(...)` 后 `TestWaitVideoAssetEarlyExitOnRateLimit` 真实 FAIL，恢复后全绿。
- 改动文件：`doubao/protocol.go`、`doubao/video_chain.go`、`doubao/completion_test.go`、`doubao/video_chain_test.go`（CYCLE01 的 creation.go/creation_test.go 已在 CYCLE01 6-review 覆盖，本轮 diff 仍含其一但代码未再改）。
- 规则来源：`PROJECT_STYLE.md`、本 Skill references、本轮代码风格契约
- 路由步骤：`static_owner_router.py --root ... --changed <6 文件> --json` 命中 STYLE-01/02/03/04/08/09（退出码 0）

## 风格证据
- `STYLE-01`：6 个改动文件 UTF-8 无 BOM、无尾随空白；`video_chain.go` 的 `errors` 按标准库字典序插入 import；格式无跳变 → PASS。
- `STYLE-02`：新增符号 `videoFastPollWindow`/`videoFastPollInterval`、`nextPollInterval`、`TestNextPollInterval`/`TestWaitVideoAssetEarlyExitOnRateLimit` 命名与同层既有符号领域一致，无同义不同名 → PASS。
- `STYLE-03`：`nextPollInterval` 与两个新测试的函数头注释均为完整层（整体说明 + [参数] + [返回] + 最近修改时间含改动原因）；常量行内只写一句话作用，未平摊完整格式；中文注释 → PASS。
- `STYLE-04`：`nextPollInterval(elapsed time.Duration) time.Duration` 单参数单返回值，无需收敛结构体；`waitVideoAsset`/`isRateLimit` 签名未变 → PASS。
- `STYLE-08`：常量 `videoFastPollWindow`/`videoFastPollInterval` 显式赋值无 iota；`errors.As` 具名承接 `var up *upstreamError` 而非 `_` 丢弃；无把多行块注释塞进同构语句组；`if false && errors.As(...)` 仅作一次性哨兵，已恢复 → PASS。
- `STYLE-09`：两个新测试追加在 `doubao/video_chain_test.go` 与被测源码同目录镜像，无独立 fixture 散落；编码前风格契约已产出并按其检查；哨兵临时改动全部恢复、无临时文件残留 → PASS。

## 边界声明
- 业务正确性：N/A + 原因：由真实测试与哨兵反证负责 + 证据：`TEST-*`（门禁全绿 + 反证 FAIL）。
- 需求覆盖与发布放行：N/A + 原因：不属于 6-review 职责 + 证据：实施计划 `AC-02`（错误分类与失败快速换号）。
