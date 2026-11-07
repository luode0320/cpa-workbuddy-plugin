# workbuddy 上游分阶段超时与 HostCallbackID 透传 — 真实测试

- 版本：workbuddy-provider 0.21.14（前置 0.21.13）
- 提交：`8208d85`
- 时间：2026-11-07 22:00 起
- 环境：Windows 本机无 CGO，统一走 `python scripts/cgo-shim-build.py workbuddy`

## 一、测试通道与依据

包为 `c-shared`（`import "C"`），Windows 无 gcc，`go test` 直接跑会报 `undefined` 符号。按仓库既定口径走 cgo-shim（build + vet + test 三段全绿才算过）。

## 二、代码级验证

### 2.1 构建与静态检查

```
[cgo-shim] OK: go build ./...
[cgo-shim] OK: go vet ./...
ok  github.com/luode0320/cpa-workbuddy-plugin/workbuddy  34.6s
[cgo-shim] OK: go test ./...
[cgo-shim] all green (workbuddy)
```

- 全量既有用例无回归（`go test ./...` 一次跑完整个包，含 100+ 既有 `_test.go`）。
- `gofmt -l` 对 5 个 Go 文件全部返回空。

### 2.2 新增用例清单（host_bridge_timeout_test.go，6 组）

| 用例 | 断言目标 |
|---|---|
| `TestSharedHTTPClient_HasLayeredTimeouts` | `ResponseHeaderTimeout` 等于常量且非 0；`TLSHandshakeTimeout` 等于常量；`DialContext` 已注入；`TLSNextProto` 非 nil 且长度为 0（禁 h2）；`client.Timeout` > `ResponseHeaderTimeout`（否则首字节闸门形同虚设） |
| `TestUpstreamResponseHeaderTimeout_InHealthyRange` | 60s 落在 (健康观测上界 20s, 异常观测 150s) 之间，防被改成极小值（误杀）或极大值（等于没设） |
| `TestRPCHostHTTPRequestWire_CarriesHostCallbackID` | 信封含 `host_callback_id`，且 `request` 仍是嵌套子对象；空 callback id 时字段完全省略 |
| `TestBuildHostHTTPStreamWire_CarriesCallbackID` | 真实 wire 构造函数的输出含 callback id、嵌套 request、base64 请求体；空 id 时不出现该字段 |
| `TestExecutorStreamRequest_ParsesHostCallbackID` | `executorStreamRequest` 能同时解析 `stream_id` / `host_callback_id` / `model` |
| `TestExecutors_PassHostCallbackIDToUpstream` | 源码级接线：三条调用链都传 `req.HostCallbackID`；`handleExecExecute` 不再用裸 `pluginapi.ExecutorRequest` |

### 2.3 变异验证（三轮，全部 KILLED）

这一节是本轮最关键的质量证据——前两轮的结论直接改变了实现方案。

**变异 1：把 `ResponseHeaderTimeout` 改成 0**

```
--- FAIL: TestSharedHTTPClient_HasLayeredTimeouts (0.00s)
    host_bridge_timeout_test.go:32: ResponseHeaderTimeout = 0s，want 1m0s
```

**变异 2（首轮）：删掉 wire 结构体里的 `HostCallbackID` 赋值**

结果：**all green —— 未被捕获**。

暴露真实缺口：当时只有 `TestRPCHostHTTPRequestWire_CarriesHostCallbackID`，它测的是「手动构造的结构体序列化后长什么样」，而不是「`hostHTTPDoStream` 这个调用点有没有真的赋值」。删掉调用点赋值，结构体单测依然全绿。

**修正**：抽出 `buildHostHTTPStreamWire` 纯函数承载 wire 构造，并让 `hostHTTPDoStream` 调用它。

**变异 2（重做）：删掉纯函数里的 callbackID 赋值**

```
--- FAIL: TestBuildHostHTTPStreamWire_CarriesCallbackID (0.00s)
    host_bridge_timeout_test.go:156: 信封缺少 host_callback_id，实际 = {"request":{...}}
```

**变异 3（首轮）：把 `handleExecStream` 的入口传参换成 `""`**

结果：**all green —— 未被捕获**。

再次暴露缺口：纯函数测试覆盖了「wire 怎么造」，但覆盖不了「入口有没有把 `req.HostCallbackID` 一路传进来」。

**修正**：新增源码级接线断言 `TestExecutors_PassHostCallbackIDToUpstream`（仓库已有同类先例：`test/traework/credits_sweep_test.go:122` 读源码核对）。

**变异 3（重做）：同样把入口传参换成 `""`**

```
--- FAIL: TestExecutors_PassHostCallbackIDToUpstream (0.00s)
    host_bridge_timeout_test.go:190: handleExecStream 未把 req.HostCallbackID 传给 pumpUpstreamStream
```

三轮变异最终全部 KILLED，且每轮暴露的缺口都以「补测试」而非「降低标准」收口。变异标记已全部移除，`Select-String -Pattern "MUTATION"` 零残留。

### 2.4 哨兵验证（确认新测试真实进编译）

shim 的输出会隐藏用例名，无法确认新文件是否真的参与编译。按项目 skill 的既有做法加临时必失败哨兵：

```
--- FAIL: TestSentinelMustFail (0.00s)
    host_bridge_timeout_test.go:207: sentinel: 新测试文件未进编译
```

FAIL 证明新文件确实进了编译与执行链。哨兵随后移除，复查 `SENTINEL` / `TestSentinelMustFail` 零残留，再跑一次确认 all green。

## 三、真实运行验证

### 3.1 生产前置取证（改动前基线）

生产 45.207.222.65，usage feed 16,944 条 `deepseek-v4.1-flash` 样本：

| 指标 | p50 | p90 | p99 | max |
|---|---|---|---|---|
| TTFT | 3,479ms | 5,336ms | 7,993ms | **150,478ms** |
| 总耗时 | 5,533ms | 10,200ms | 36,906ms | **694,166ms** |

长尾请求 `status_code` 全部为 0（传输级），确认缺的是传输层前置闸门。

### 3.2 对照实验（证明插件转发链路本身无问题）

同 prompt、同时段，直连 `copilot.tencent.com` vs 经插件 `127.0.0.1:8317`：

| 场景 | 直连 | 经插件 |
|---|---|---|
| 长输出（1..300, effort=max） | TTFT 1.70s / 267 tok/s | TTFT 2.60~3.35s / 162~228 tok/s |
| 大 input（67,637 prompt tokens） | TTFT 2.75~3.79s | TTFT 2.64~3.08s |

结论：同量级，转发链路未引入可观延迟。

### 3.3 CI 与发布

- run `42380096523`：12 个 Test job 全 success；`darwin/arm64` 如预期 cancelled。
- **run conclusion 显示 `cancelled`，但 Release job 为 `success`** —— 按项目记忆的既定判据，只看 Release job 与 release 实体，不看 run conclusion。
- release 实体 `workbuddy-provider-v0.21.14`：6 assets（5 平台 zip + checksums.txt），checksums 全部 OK。
- registry 更新后 `validate-registry.py` OK（11 plugins / schema v2），远端 raw 校验 `version=0.21.14 / artifacts=5`。

### 3.4 生产部署（热重载，未重启容器）

```
POST /v0/management/plugin-store/workbuddy-provider/install
→ 200 {"version":"0.21.14","restart_required":false}
```

- 日志：`plugin hot reloaded plugin_id=workbuddy-provider active_version=0.21.14 retired_version=0.21.13`。
- 落盘 `.so` sha256 `8433a54c…` 与本地 release zip 内二进制**逐字节一致**。
- 插件状态：`configured/registered/enabled/effective_enabled` 全 true。

### 3.5 真实推理验证

真实入口 `POST /v1/chat/completions`（`deepseek-v4.1-flash`，`reasoning_effort=max`，流式）：
status 200，耗时 **2.04s**，返回 69,810 字节正常 SSE。

### 3.6 P1 断连传播验证（本次修复的直接证据）

构造长输出流式请求（要求数 1..800），收满 2249 字节后**硬断开 socket**（不发正常 FIN）：

- 断连后日志出现 **`failed=true`** 的用量记录，证明客户端断连已被插件正确识别并记账（修复前会一直读到上游自然结束）。
- 观察期 45s 后系统 ESTAB 连接数 38，无堆积。

### 3.7 上线后真实指标（28 条请求）

| 指标 | 基线（0.21.13 及以前） | 上线后（0.21.14） |
|---|---|---|
| TTFT p50 | 3,479ms | 3,413ms |
| TTFT p90 | 5,336ms | 5,080ms |
| TTFT **max** | **150,478ms** | **10,601ms** |
| lat p50 | 5,533ms | 6,181ms |
| lat **max** | **694,166ms** | **52,784ms** |

TTFT 最大值从 150.5s 收敛到 10.6s，总耗时最大值从 694s 收敛到 52.8s。样本量仅 28 条、观察窗约 20 分钟，**属趋势性证据，不是足量统计结论**——需在后续自然流量中继续观察长尾是否复发。

## 五、边界与未验证项

- `ResponseHeaderTimeout=60s` 在本次 28 条样本中**尚未被实际触发**（最长 TTFT 10.6s），因此「闸门开启后能正确失败并换号」这条路径只有单测覆盖，**没有生产实证**。
- P0 只加固了插件兜底 client；生产实际走宿主 `host.http.do_stream`，其 `NewProxyAwareHTTPClient(..., 0)` 仍无分阶段超时。宿主层缺陷**未修复**（跨项目写入红线内），本次改动不改变宿主侧行为。
- 断连传播的定量收益（线程数、内存）未测量，只验证了「不再读到上游自然结束」这一行为特征。

## 四、测试资产

- 新增长期资产：`workbuddy/host_bridge_timeout_test.go`（6 组用例）。
- 临时资产：变异标记、哨兵用例、远端统计脚本均已清理并复查。
