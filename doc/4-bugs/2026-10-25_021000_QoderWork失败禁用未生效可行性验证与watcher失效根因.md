# QoderWork 失败禁用探活未生效：宿主 watcher 不监听 mysqlstore spool（可行性验证结论）

- 编号：BUG-QODERWORK-FAILOVER-DISABLE-NOT-PERSISTED-20261025
- 侦察时间：2026-10-25 01:32 — 02:10
- 验证时间：2026-10-25 02:10（本轮）
- 状态：**根因已确证；上一轮的「host.auth.save 会重新启用账号」判断被推翻；方案 A 可行**
- 关联：本文件取代 `2026-10-25_014500_QoderWork失败禁用探活未生效禁用写盘被宿主回写覆盖根因诊断.md` 的根因段落（该文结论「Metadata 搬运通道未触发」不成立，见 §4）

## 1. 本轮验证目标

上一轮裁定走「选项 A」（改用 `host.auth.save` 写入禁用），并声明可行性验证的通过标准是：**本地 shim 构造禁用文档走 `host.auth.save`，观察 `disabled` 是否被保留**。本轮执行该验证，并顺带定位真正的持久化断点。

## 2. 可行性验证结果：方案 A 可行（且是唯一可行通道）

### 2.1 SDK 源码逐行核对（v7.2.129，与生产同版本）

宿主的 `host.auth.save` 全链路已完整读完，共四段：

| 环节 | 文件:行 | 行为 |
| --- | --- | --- |
| RPC 入口 | `internal/pluginhost/auth_callbacks.go:116` | `callHostAuthSave` 解析 `HostAuthSaveRequest{Name, JSON}` |
| 落盘 | `internal/pluginhost/auth_callbacks.go:281` | `saveAuthFile` → `os.WriteFile(dst, data, 0600)` |
| 构造记录 | `internal/pluginhost/auth_callbacks.go:305` | `buildAuthFromFileData`：**不读 `disabled` 字段**，`Status` 硬编码 `StatusActive` |
| 写库 | `internal/pluginhost/auth_callbacks.go:370` | `upsertAuthRecord` → `Manager.Update` → `persist` → `PostgresStore.Save` |
| 序列化 | `internal/store/postgresstore.go:251-253` | `auth.Metadata` 整体 `json.Marshal` 后写文件 |
| 入库 | `internal/store/postgresstore.go:537-545` | `upsertAuthRecord` **重新读物理文件原样 upsert 进 `auth_store`** |

**关键否定项**：`buildAuthFromFileData` 不设置 `auth.Disabled`（该文件全文仅第 394/414/452 行引用 `auth.Disabled`，均为读或透传，无赋值）。而路由判据在多处依赖 `candidate.Disabled`（`conductor_selection.go:634/665/1024/1257/1334/1442`）。因此**「`host.auth.save` 会重新启用账号」的警告成立**——若用它写 `disabled:true`，内存态仍是启用。

**但这不是阻塞**：同一函数把完整 JSON 放进 `Metadata`（`buildAuthFromFileData:341` `Metadata: metadata`），`PostgresStore.Save` 的 case 2 分支会把 `Metadata` 整体序列化落盘并入库。**`failover_disable` 三键只要在 JSON 里，就能进 `auth_store`**。

### 2.2 真正决定持久化的是 `auth.parse` 通道，而它本来就是对的

`pluginAuthDataToCoreAuth`（`internal/pluginhost/auth_provider.go:537-589`）逐字段赋值为：

```go
status := coreauth.StatusActive
if data.Disabled { status = coreauth.StatusDisabled }   // :567-570
auth := &coreauth.Auth{
    Disabled: data.Disabled,                             // :579   ✅ 正确传递
    Status:   status,
    Metadata: metadata,                                  // :582
    Storage:  &pluginTokenStorage{...},                  // :581
}
```

`mergedStorageJSON`（同文件 478-503）规则：`StorageJSON` 打底 → `Metadata` **逐键覆盖** → 强制 `type`。

即：**插件 `handleParseAuth` 回传的 `AuthData.Disabled` 与 `Metadata` 会被宿主正确采纳并落库**。插件侧代码也正确（`main.go:663` 读盘上 `disabled`、`main.go:670` 搬运三键）。

仓库里已有两个测试锁死这段契约，且本轮变异验证确认它们有效：

- `TestParseAuthCarriesFailoverMarkersIntoMetadata`（`qoderwork/failover_disable_test.go:762`）
- `TestHostRebuildFromParseAuthDataKeepsMarkers`（同文件 `:905`）
- 本轮新增 `test/qoderwork/failover_persist_verify_test.go` 三个用例，与上两条互补（覆盖「无标记时不得凭空造」的反向守卫）

**变异验证**：临时移除 `main.go:670` 的 `carryFailoverKeysIntoMetadata` 调用 → 上列三个测试全部 FAIL，报 `metadata[failover_disable] = <nil>, want true`。说明测试真实有效、插件侧逻辑已正确。

### 2.3 因此问题不在任一端的代码，而在「通道是否被触发」

## 3. 真根因：宿主 watcher 不监听 mysqlstore spool

生产配置与目录事实：

- 容器 `config.yaml`：`auth-dir: "~/.cli-proxy-api"`
- 容器环境：`MYSQL_LOCAL_PATH=/CLIProxyAPI/mysqlstore`
- qoderwork 的 18 个 auth 文件**只存在于** `/CLIProxyAPI/mysqlstore/mysqlstore/auths/`
- `/root/.cli-proxy-api/`（`auth-dir` 指向处）里 **qoderwork 文件数 = 0**

实测（三项独立实验，全部只读或可回滚）：

| 实验 | 操作 | watcher 事件 |
| --- | --- | --- |
| 1 | 直接改 spool 文件的 `note` 字段 | **0 条** |
| 2 | 用**插件同款 `os.Rename` 原子替换**写入 `disabled:true` + `failover_disable:true` | **0 条** |
| 3 | 删除后重建同名文件 | **0 条** |

写入后文件内容确认已变（`failover_disable` 计数=1、`disabled` 字段存在），但 DB `auth_store` 始终 `disabled=false`、无 `failover_disable` 键。

**事件历史佐证**（48 小时窗口）：

- qoderwork 有 18 次 `auth file changed`，但**全部是 `REMOVE` 类型**，且时间集中在 23:52:37-23:52:40 一次窗口 + 此前每 4 小时一次（04:02、08:03、12:02、16:02、20:02、00:00 —— 正好是签到/watchdog 周期）。
- 23:52 之后 48 小时内 **qoderwork 零事件**，而同期 spool 文件的 mtime 持续在变（01:13、01:24、01:31、01:33、01:36、02:06）。
- 对照 workbuddy：同一窗口 397 + 626 条事件，`CREATE/WRITE/REMOVE` 齐全。

⇒ **watcher 对 qoderwork 所在目录的有效监听已失效**，而 workbuddy 的账号目录仍在被监听。这是「插件写盘成功、宿主无感知、DB 不更新」的完整解释。

### 3.1 为什么 workbuddy 能生效

workbuddy 的 `failover_disable` 确实进了 DB（2 个账号，顶层键含 `failover_disable` / `_at` / `_model`）。它的写入路径与 qoderwork 同构，差别只在**它所在目录的 watcher 仍在工作** → 写盘触发 `addOrUpdateClientLocked` → 重新走 `auth.parse` → Metadata 里的三键被采纳 → 落库。

## 4. 推翻上一轮结论

上一轮文档 §3 判断「宿主从 DB 回写 spool 时不走 `handleParseAuth`，标记因此从未入库」。本轮通过 SDK 源码与三项实验证明：

- 宿主**不会**主动用 DB 内容覆盖 spool 中已存在的本地修改（否则手工写入的标记不会稳定存活 12 秒）。
- 真正的断点是 **watcher 未感知变更 → 根本没有任何后续处理**，而非「处理了但丢弃标记」。
- 因此上一轮给出的方案 B（「写盘后主动触发一次 parse 回传」）方向正确但落点不准——问题不是没触发一次 parse，而是**整个目录的监听失效**。

## 5. 结论与建议

### 5.1 可行性验证结论

| 项 | 结论 |
| --- | --- |
| 方案 A（`host.auth.save` 写禁用） | **不可行**——`buildAuthFromFileData` 硬置 `StatusActive` 且不读 `disabled`，用它写禁用会强制启用账号。原警告成立。 |
| `auth.parse` 通道本身 | **完全可行且已正确实现**，测试覆盖充分（变异验证 3/3 捕获）。 |
| 真正阻塞 | 宿主 watcher 对 mysqlstore spool 目录的监听失效，导致 `auth.parse` 永不触发。 |
| 插件侧是否需要改代码 | **不需要**。当前代码在通道可达时行为正确。 |

### 5.2 修复方向（按优先级，均需你裁定）

| 方案 | 做法 | 层级 | 风险 |
| --- | --- | --- | --- |
| **D（推荐）** | 定位并修复 watcher 失效（容器重启或检查 store 的 `AuthDir()` 返回是否在 23:52 后变化）；恢复后现有代码自动生效，零代码改动 | 运维/宿主 | 需窗口重启，须你确认 |
| E（备选） | 插件侧增加「禁用后主动经 `host.auth.save` 写一次」的补偿，但**只写 `failover_disable` 三键、`disabled` 保持 false**，靠探活循环的内存集合排除（放弃写盘禁用语义） | 插件 | 语义降级：宿主仍会把请求路由过来，只能同请求内换号 |
| F | 插件侧把 spool 路径改为 `auth-dir` 指向的目录（`~/.cli-proxy-api`），让 watcher 可见 | 插件 | 需确认该目录在 mysqlstore 模式下是否被使用，可能与 store 冲突 |

**默认兜底（若本轮不裁定）**：维持现状不改代码——现有代码在通道可用时正确，重启窗口由你择机安排；在修复前，失败禁用应被视为「不可用」，排障时以内存冷却层（`isAccountCoolingDown`）为准，不要以 `disabled` 字段判断账号健康。

## 6. 证据留痕

- 生产版本 `qoderwork-provider 0.10.1`，容器 `StartedAt=2026-10-22T17:39:55Z`、`RestartCount=6`。
- 所有探针操作均先备份后恢复，未改 DB、未改配置、未重启容器。
- 本地：`python scripts/cgo-shim-build.py qoderwork` 全绿（含新增 3 用例）；变异验证 3 个测试全部捕获。
- 新增测试资产：[test/qoderwork/failover_persist_verify_test.go](file:///F:/cpa-plugin/test/qoderwork/failover_persist_verify_test.go)（当前未提交）。
