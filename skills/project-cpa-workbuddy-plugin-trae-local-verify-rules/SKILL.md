---
name: project-cpa-workbuddy-plugin-trae-local-verify-rules
description: 当需要本地直连验证 Trae 账号能否跑通推理（如"测试这个账号用 qwen3.8-max 能否完成 X"）、复现 traework 上游请求、或排查 Trae 长任务"生成中途停止"时触发。负责用 traework 插件源码（cgo-shim 无 CGO 构建）复用解密/header/payload/SSE 判定逻辑直连上游 llm_utils_chat，判定流式请求是否完整完成（done 收尾 vs 提前 done vs 断流）。本地逻辑验证用 cgo-plugin-isolated-test（cgo-shim-build.py），本 skill 只补充"真实上游直连验证"这一段，不重复实现。
---

# cpa-workbuddy-plugin Trae 本地直连验证

## Skill 作用与适用场景

- 用户给出 `globalStorage\storage.json` 或 Trae 账号，要求"测试这个账号用某模型能否完成 XX 推理/分析"
- 需要判断一次 Trae 流式请求的终结形态：`done`（完整完成）/ `output_eof`（中途断流，插件补 length 收尾）/ `invalid`（空响应）
- 复现生产 traework 请求（同一账号、同一模型、同一上游契约），对照判断「短输出伪完成」是账号级还是插件级问题
- 排查 0.1.23 日志插桩后仍无法归因的中断问题（需要本地复现流形态）

**边界**：本 skill 管"如何直连上游并判定流形态"；本地编译/测试环境（cgo-shim 构建）归 `cgo-plugin-isolated-test`；发布链路归 `project-cpa-workbuddy-plugin-release-rules`。

## 自动触发信号

- 用户说"测试这个账号""用 storage.json 的账号跑一下推理""分析这个项目能完成吗"
- 需要判定某个 Trae 账号 + 模型能否长输出（对比生产账号的短输出伪完成）
- 需要抓取某次真实上游请求的 SSE 流形态（done / output_eof / invalid）

## 进入后先做什么

1. **确认账号来源**：storage.json 的 `iCubeAuthInfo://icube.cloudide` 是 tc-header 加密 blob（`dGMFEAAAC...` 开头），非明文 JSON；先解密拿到 token/uid。
2. **确认模型名**：上游用 `solo_work_lite` 池 + 精确 config_name 透传；`qwen3.8-max` 直接透传。未知短 ID 会走 `traeConfigNameAliases` 映射。
3. **确认请求形态**：判定是"测试能不能完成"（→ 本 skill 完整直连）还是"复现生产某次请求"（→ 按 CPA 实际传入的 payload/headers 复刻）。

## 默认执行流程（本地直连验证）

### Step 1 · 准备临时验证目录

```bash
# 复制 traework 源文件（排除 _test.go），临时目录，不入库
rm -rf /tmp/traework_verify && mkdir -p /tmp/traework_verify
cp traework/go.mod traework/go.sum /tmp/traework_verify/
for f in traework/*.go; do case "$f" in *_test.go) ;; *) cp "$f" /tmp/traework_verify/ ;; esac; done
cp traework/panel.html /tmp/traework_verify/   # go:embed 依赖
cp scripts/cgo-shim-build.py /tmp/traework_verify/
```

### Step 2 · 对 main.go 做 cgo shim（Windows 无 gcc）

```bash
cd /tmp/traework_verify && python -c "
import sys; sys.path.insert(0, '.')
ns = {}; exec(open('cgo-shim-build.py', encoding='utf-8').read(), ns)
from pathlib import Path
ns['shim_main'](Path('main.go'))
"
```

shim 后 `hostAPI` 为 nil → `hostBridgeAvailable()` 返回 false → `hostHTTPDo`/`hostHTTPDoStream` 自动走 Direct 直连（且 Windows 上 `hostHTTPDo` 恒走 Direct）。**shim 后 main.go 里的 `func main() {}` 要改名为 stub**（如 `pluginEntryStub`），否则与自定义 verify_main.go 的 main 冲突。

### Step 3 · 写 verify_main.go（复用插件同包函数）

核心逻辑（完整可运行模板见 `references/verify-main-template.md`）：

1. 读 storage.json → 取 `iCubeAuthInfo://icube.cloudide` → `decryptCredentialString(blob)` 解密
2. 构造 `traeAuth{Token, UserID, Host}`（deviceId/machineId 留空——storage.json 直导生产路径一致，上游容忍缺省）
3. 构造 messages → `toTraeMessages()` → `buildTraePayload(messages, model, true, 0, nil, nil)`
   - **max_tokens 传 0** → 走 0.1.24 逻辑补默认 `streamDefaultMaxTokens = 20000`（这就是生产修复点）
4. `http.NewRequest` + `buildTraeAuthHeaders(a)` → 自定义 client 直连 `apiHostFor(a) + llmUtilsChatPath`
5. `scanSSE` 扫描，复用 `traeSSETerminal`/`classify` 判定终结形态

### Step 4 · 运行并判定

```bash
cd /tmp/traework_verify && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -o verify.exe . && ./verify.exe
```

判定标准：
- `termination=done` → 完整完成（业务成功）
- `termination=output_eof` → 上游中途断流，插件补 `finish_reason=length` 兜底收尾
- `termination=invalid` → 空响应，无效
- 同时记录 HTTP 状态、output 分片数、正文/思考字符数、总耗时作证据

## 权责边界与不负责事项

- 只负责"直连上游 + 判定流形态 + 输出证据"，不做账号 failover/用量核算（那是插件运行时职责）
- 不连生产 CPA（红线：本地连接只允许 local 配置；真实上游直连是对 Trae 官方 API 的本地测试，需用户明确授权）
- 不自动提交 git（临时验证资产在 /tmp，仓库只留 skill 本身）
- 验证会消耗该账号一次真实 Trae 配额——用户要求测试该账号才执行

## 执行通过 / 驳回标准

- 通过：能输出 HTTP 状态 + 终结形态（done/output_eof/invalid）+ output 分片数/字符数/耗时，并对"能否完成"给出明确结论
- 通过：结论与日志证据可回指（临时目录保留 verify_main.go + 运行日志）
- 驳回：只报"超时"不区分流形态（必须先区分"完全无数据挂起"与"有数据但慢"）
- 驳回：把 120s 客户端超时误判为上游问题（见踩坑）

## 踩坑记录

### 坑 1：插件 sharedHTTPClient 的 120s Timeout 会掐断长流式（最关键）

`traework/host_bridge.go` 的 `sharedHTTPClient()` 设了 `Timeout: 120 * time.Second`，这是**整个请求**（含读 body）的超时。qwen3.8-max 长任务先思考 20s+ 再输出正文，全程可能 2-3 分钟——**本地验证必须自定义长超时 client**（如 `&http.Client{Timeout: 0}` + `context.WithTimeout(10*time.Minute)`），否则 120s 报 `context deadline exceeded` 且此时只收了 reasoning 没正文，容易误判"上游挂起/断流"。**这也是生产插件的一个潜在限制**：生产走宿主桥（不走 sharedHTTPClient），但若某天 fallback 到 Direct 会踩此坑。

### 坑 2：先思考后正文的流形态

qwen3.8-max 前 ~20s 只有 reasoning（`reasoning_content`），out_chars 为 0 是正常的，不是没响应。判定"能否完成"必须等全程，不能在前 60s 就下结论。

### 坑 3：storage.json 账号 ≠ 生产账号

本机 `globalStorage\storage.json` 是 Trae SOLO 桌面客户端当前登录账号（如 uid `2257747741770235`）；生产 CPA（远程 Docker）用的是另一账号（如 `2033439621254311`）。测试前先确认测的是哪个账号。**账号级结论不能跨账号外推**：实测新账号 qwen3.8-max 完整 done（2m37s / 595 chunk / 2.4 万字符），而生产账号历史平均 77 tokens 就 done——两者行为不同，说明短输出是生产账号的账号级问题而非模型/插件通用限制。

### 坑 4：hostBridge 在 Windows 自动直连

`hostHTTPDo` 里有 `runtime.GOOS == "windows"` 直接走 `hostHTTPDoDirect`，所以 Windows 本地验证天然走真实 HTTP，无需 mock。`hostHTTPDoStream` 在 `hostBridgeAvailable()==false`（shim 下 hostAPI 为 nil）也走 Direct。

### 坑 5：SSE 里 output 事件可能是 {type:text,content} 或 {response} 两种格式

`normalizeOutput` 同时处理两种；`event:error`（4011 限流 / 14018 额度用尽）藏在 HTTP 200 + SSE 层，scanSSE 回调里必须处理 `case "error"`。

## 相关 skill 与文档

- 本地编译/测试环境：`cgo-plugin-isolated-test`（cgo-shim-build.py）
- 发布链路：`project-cpa-workbuddy-plugin-release-rules`
- 流终止日志插桩背景：项目记忆 `traework-stream-log-instrumentation` / `traework-upstream-truncation-vs-host`
