# WorkBuddy + QoderWork 账号面板「异步节流刷新」设计方案

> 2026-08-23 设计。确认方案：GET /refresh/status 轮询 + 面板只读缓存 + 1s/账号硬编码节流。

## 0. 第二轮修订（2026-08-23 晚，最终口径）

在 v1 基线（下文第 1~9 节）基础上做三处收敛，**本节优先于下文**：

1. **去掉「刷新数据」按钮 → 进入页面自动触发**
   - 前端删除顶部 `刷新数据` 按钮；新增 `enterPanel()`（`await load()` 渲染缓存 → `startBackgroundRefresh()`）与 `startBackgroundRefresh()`（`POST /refresh` 静默触发 → `pollRefreshStatus()`）。
   - 页面进入（init + saveKey）走 `enterPanel()`；checkin / unfreeze / delete / select 等软刷新只 `load()`（渲染缓存，不再触发整轮刷新）。
   - 前端无感知：无「已开始 / 已完成」toast，仅保留顶部 `后台刷新中 N/M` 进度 + 卡片三态（done 短暂高亮 / failed 红框保留 8s）。

2. **刷新轮幂等（关键）**
   - `EnqueueAll` / `EnqueueOne` 改为**运行中则忽略**：`idx < len(batch)` 时直接返回 0 / false，不再替换批次（`generation++`）、不再追加。
   - 效果：面板进入、watchdog 10m tick、单卡 track=1 等多路触发在刷新过程中并发到达时，只跑一轮，绝不重复一轮。
   - v1 第 3 节「合并入队 不重启」、第 5.2 节 `started:true` 返回结构按此更新：`started` 由 `EnqueueAll` 返回值 `n>0` 推导，运行中返回 `started:false, queued:0`。

3. **范围**
   - 后端幂等修复双插件同步（两份 `refresh_runner.go` 完全一致）。
   - 前端「去按钮 + 进入自动刷新」本次仅改 WorkBuddy；qoderwork 面板前端暂不动（结构差异较大，待确认是否对称同步）。

## 1. 现状（已盘点）

| 路径 | 当前行为 | 问题 |
|---|---|---|
| `GET /accounts`（打开面板） | `buildDashboardEx(false,false)` 不 fetchCredits；**前端 `lazyLoadCredits` 仍并发跑 N 个 `GET /credits?auth_index=…`** | 上游 billing API 在账号量大时被并发打挂 |
| `POST /refresh`（面板按钮） | `buildDashboardEx(true,true)` **同步全量 goroutine**，等全部完成才返回 | UI 转圈 N×latency，账号量大时永远不返回 |
| `runPreserveWatchdogTick`（10m tick） | 串行遍历所有账号，每账号独立 `cachedAccountDetails(...,true)` | 没有 1s 节流；N=100 时一次 tick 跑很久 |
| `GET /credits?auth_index=…`（卡片点） | 单卡请求，立即 `fetchUserResource`，无节流 | 卡片按钮可用，但 lazyLoad 全量并发调它就是 N 并发 |

`qoderwork/management.go panel.go panel.html` 与 `workbuddy` 同构；本次必须双插件对称同步。

## 2. 目标

1. **打开面板** → 仅读 `accountCache`，无缓存就显示 `加载中…` 占位，**不发任何上游请求**
2. **2 个刷新入口共用同一刷新器**，1s/账号节流：
   - 面板 `刷新数据` 按钮 → 异步触发，立刻返回
   - watchdog 10m tick → 异步入队，立刻返回
3. 刷新进度对前端可见，每账号状态：`pending / running / done / failed + fetched_at`
4. 不破坏现有 singleflight（`accountDetailFlight`）的 dedup 语义
5. 双插件（workbuddy 0.14.9 + qoderwork 0.9.4）对称发版

## 3. 架构：RefreshRunner 单例

```
+---------------------- RefreshRunner (singleton) ----------------------+
|  queue []refreshJob      (FIFO: per-account, indexed by authID)        |
|  running bool            (one queue worker at a time)                 |
|  perAccount map[string]state                                             |
|      state ∈ { pending | running | done | failed, fetched_at, err }    |
|  ctx cancel            (current batch)                                 |
|  ticker 1s             (hard-coded default; const refreshTickInterval) |
+------------------------------------------------------------------------+
              ^             ^              ^
              |             |              |
   POST /refresh    watchdog 10m   GET /credits?auth_index=…&track=true
   (panel button)   preserve tick   (single card "刷新", 节流版)
```

**并发约束**（关键）：
- RefreshRunner 内部只有一个 worker goroutine 在跑（守护循环）
- 新的 batch（trigger 来源）→ 合并入队 **不重启** 已有的 tick
- 用 `channel struct{}{1}` coalesce（已有 `requestPreserveTick` 同款）
- 已有 `accountDetailFlight` singleflight 自动 dedup 同 authID，并发调用直接 reuse，不需新加

**可观测**：
- 内存维护 `lastRunStartedAt / lastRunFinishedAt / lastSource`（debug 用）
- `GetRefreshStatus()` 返回 `{running, total, done, failed, pending, running_auth, sources, started_at}`

## 4. 路由变化（workbuddy + qoderwork 对称）

| 路由 | 旧 | 新 |
|---|---|---|
| `GET /accounts` | `buildDashboardEx(false,false)` + 前端 lazyLoad 全量并发 | `buildDashboardEx(false,false)`；前端删 lazyLoad。返回体加 `refresh_status` 字段供首屏读 |
| `POST /refresh` | 同步阻塞等全量 | **立即**返回 `{started: true, refresh_id, queued, source: "panel"}`；调 `RefreshRunner.EnqueueAll()` |
| `GET /refresh/status` | — | 新增。返回 RefreshRunner 当前状态快照（用于前端轮询） |
| `GET /credits?auth_index=…&track=1` | 立即 fetch 上游 | `track=1` 时入 RefreshRunner；`track=0`（默认）保持现有单卡立即拉（卡片内小 spin 不阻塞） |

**前端只读 status 字段**（不需要再加 SSE）：
- 2s 一次 `GET /refresh/status`
- 对比 done 集合 → 调现有 `updateOneCard()` 局部刷新
- watchdog 触发的刷新同样可见（如需，可隐藏 source）

## 5. 关键实现点（行号级）

### 5.1 后端：新建 `refresh_runner.go`（workbuddy + qoderwork 各一份，内容同构）

```
package main

const refreshTickInterval = 1 * time.Second   // 硬编码 1s/账号

type refreshStatus string
const (
    rsPending  refreshStatus = "pending"
    rsRunning  refreshStatus = "running"
    rsDone     refreshStatus = "done"
    rsFailed   refreshStatus = "failed"
)

type refreshJobState struct {
    AuthIndex  string
    AuthID     string
    Status     refreshStatus
    Error      string
    FetchedAt  time.Time     // 最近成功拉取的 UTC
    Source     string        // "panel" / "watchdog" / "credits"
}

type refreshRunner struct {
    mu        sync.Mutex
    batch     []refreshJobState       // 当前 batch 的全量
    idx       int                     // 下一个要跑的 idx
    running   bool
    sources   []string                // 本 batch 的触发源
    startedAt time.Time
    finishedAt time.Time
    enqueueCh chan struct{}          // 容量 1，coalesce
    stopCh    chan struct{}           // 用于测试 / 关闭
}

var globalRefresh = newRefreshRunner()  // 启动守护 goroutine

func newRefreshRunner() *refreshRunner { ... }
func (r *refreshRunner) EnqueueAll(auths []refreshTarget, source string) int  // 入队，返回新增数量
func (r *refreshRunner) EnqueueOne(authIndex, authID, source string) bool
func (r *refreshRunner) Snapshot() refreshSnapshot  // 给 /refresh/status
func (r *refreshRunner) run()                        // 守护 goroutine，1s tick
```

守护循环伪代码：

```
for {
    select {
    case <-r.enqueueCh:
        // batch 已经合并好；继续往下
    case <-r.stopCh:
        return
    }
    r.mu.Lock()
    if r.idx >= len(r.batch) {
        r.running = false
        r.finishedAt = now
        r.mu.Unlock()
        continue  // 等待下一次 enqueue
    }
    job := r.batch[r.idx]
    job.Status = rsRunning
    r.batch[r.idx] = job
    authIdx := job.AuthIndex
    authID := job.AuthID
    r.mu.Unlock()

    // 真正拉一次，错误存到 job 上
    err := doFetchOne(authIdx, authID)   // 走 cachedAccountDetails+singleflight
    r.mu.Lock()
    job = r.batch[r.idx]
    job.FetchedAt = now
    if err != nil { job.Status = rsFailed; job.Error = err.Error() }
    else          { job.Status = rsDone }
    r.batch[r.idx] = job
    r.idx++
    r.mu.Unlock()

    // 1s 节流（即使 doFetchOne 很快也要等）
    select {
    case <-time.After(refreshTickInterval):
    case <-r.stopCh:
        return
    }
}
```

`doFetchOne` 内容：
- `hostAuthGet(authIndex)` → SA
- `cachedAccountDetails(authID, sa, /*force=*/true)` → 复用现有 singleflight，force=true 强制重拉
- 错误返回 `err`（不强 log，避免海量化）

### 5.2 路由层（management.go）

`POST /refresh`：

```go
case req.Method == http.MethodPost && path == base+"/refresh":
    files, _ := hostAuthList()
    targets := make([]refreshTarget, 0, len(files))
    for _, f := range files {
        targets = append(targets, refreshTarget{AuthIndex: f.AuthIndex, AuthID: f.ID})
    }
    added := globalRefresh.EnqueueAll(targets, "panel")
    return okEnvelope(mgmtJSONResponse(http.StatusOK, map[string]any{
        "started":       true,
        "source":        "panel",
        "queued_or_running": added + globalRefresh.AlreadyRunningCount(),
        "refresh_id":    globalRefresh.CurrentBatchID(),
    }))
```

`GET /refresh/status`：

```go
case req.Method == http.MethodGet && path == base+"/refresh/status":
    return okEnvelope(mgmtJSONResponse(http.StatusOK, globalRefresh.Snapshot()))
```

`mutatingManagementPath(path)` 中加入 `base + "/refresh/status"` **不需要**（读路径）。

`GET /credits?auth_index=…&track=1`：

```go
track := req.Query["track"] // 形如 ["1"]
if authIndex != "" && (len(track) > 0 && (track[0] == "1" || track[0] == "true")) {
    sa, _ := hostAuthGet(authIndex)
    if sa != nil {
        globalRefresh.EnqueueOne(authIndex, /*authID*/ lookupAuthID(authIndex), "credits")
        return okEnvelope(mgmtJSONResponse(http.StatusAccepted, map[string]any{
            "queued": true, "auth_index": authIndex, "refresh_status": globalRefresh.Snapshot(),
        }))
    }
}
// 否则保持现状：立即 fetch 上游（向后兼容）
```

### 5.3 watchdog 适配（watchdog.go / keepalive.go）

`runPreserveWatchdogTick`：
- 旧：串行 `for _, f := range files { ... cachedAccountDetails(f.ID, sa, true) }`
- 新：
  1. `files, _ := hostAuthList()`
  2. `targets := make([]refreshTarget, 0, len(files))`
  3. 全部入队 `globalRefresh.EnqueueAll(targets, "watchdog")`
  4. 立即 return（不阻塞 watchdog tick）

副作用：
- `preserveFlipDecision` 无法在本 tick 内完成（数据异步拉回来后才完成）
- 解决：增加**"消费快照"**机制：每次 watchdog tick 启动时，先 `globalRefresh.Snapshot()` 拿上一 batch 的 done 列表，对其中 credits 已变/已拉的账号执行 preserve flip
- 即 watchdog tick 现在是 2 步：
  1. 入队 + 立即 return
  2. 上一 batch 的 done 列表里做 preserve flip（用 `cachedAccountDetails` 缓存读，不是再调上游）
- 复杂度过高？退一步：**watchdog tick 启动时，调用 `cachedAccountDetails(...force=false)` 同步读（仍走缓存），不够就接受延迟一拍**

权衡：本轮选 **退一步方案**——watchdog tick 内：
- 走 `cachedAccountDetails(f.ID, sa, false)`，**优先复用缓存**（没有就显示无数据，本 tick 跳过这个账号）
- 然后 `globalRefresh.EnqueueAll(..., "watchdog")` 入队让下一 tick 看见
- 这样既维持现有 10m 节奏，也不阻塞 tick，更不会因上游 503 拉崩 watchdog

### 5.4 前端 panel.html

**改 `load(force, btn)`：**
- `force=false`（打开面板）：不调用 `lazyLoadCredits`
- `force=true`（点刷新）：调 `POST /refresh`，立即 toast `已开始后台刷新（X 个账号）`，**不阻塞 UI**；然后启动 `pollRefreshStatus()` 轮询
- 删除 `lazyLoadCredits` 自动触发；保留函数体（供 `updateOneCard` 单卡刷新用，可能）

**新增 `pollRefreshStatus()`**：

```js
let pollTimer = null;
async function pollRefreshStatus() {
  stopPolling();
  pollTimer = setInterval(async () => {
    try {
      const d = await api("/refresh/status");
      updateRefreshUI(d);
      // 只更新 done/failed 状态变化的卡片
      (d.per_account || []).forEach(st => {
        if (!st.auth_index) return;
        const a = (lastAccounts || []).find(x => x.auth_index === st.auth_index);
        if (!a) return;
        const newCredits = (st.status === "done" || st.status === "failed");
        if (newCredits && !a._lastRefreshState) {
          // 拉这个账号的 credits 局部刷新（走 /credits?track=0，立即拉）
          api(`/credits?auth_index=${encodeURIComponent(st.auth_index)}`).then(d2 => {
            const acct = (d2.accounts||[])[0]||d2;
            if (acct && acct.credits) { a.credits = acct.credits; a.exhausted = acct.exhausted; }
            if (acct && acct.plan) a.plan = acct.plan;
            updateOneCard(a);
          });
          a._lastRefreshState = st.status;
        }
      });
      if (!d.running) stopPolling();
    } catch(e) {}
  }, 2000);
}
function stopPolling(){ if(pollTimer){clearInterval(pollTimer);pollTimer=null;} }
```

**改卡片渲染 `card(a)`**：增加 `data-refresh-state` 属性 + 三态样式：
- `pending` → 显示 "排队中…"
- `running` → 显示小 spin + "刷新中…"
- `done` → 1.5s 高亮后消失
- `failed` → 显示 ⚠️

**CSS**（追加）：

```css
.card .refresh-state{font-size:11px;color:var(--mut);display:inline-flex;align-items:center;gap:4px;margin-left:6px}
.card[data-refresh-state="running"]{outline:1px dashed var(--acc)}
.card[data-refresh-state="done"]{outline:1px solid var(--ok);transition:outline 1.5s ease-out}
.card[data-refresh-state="failed"]{outline:1px solid var(--err)}
```

**汇总小条 / 按钮态**：
- 面板顶部 `刷新数据` 按钮：触发后变 `刷新中…(N/M)` 文本，2s 轮询更新
- 用汇总卡的 `summary-meta` 显示 `后台刷新中 N/M 已完成 K`

## 6. 对称同步：qoderwork

qoderwork 的 `panel.go buildDashboardEx` / `panel.html` 高度同构；直接新建 `qoderwork/refresh_runner.go`（与 workbuddy 内容相同），按相同入口接入。

**行号差异点核对**：
- qoderwork `panel.go:45` 的 `buildDashboardEx`
- qoderwork `management.go:172-175` 的 `/refresh` `/accounts` route
- qoderwork `panel.html:651` 处的 `api("/refresh"/api("/accounts")` 二选

无需改动 watchog 名单逻辑（qoderwork 没 watchdog 10m，仅 workbuddy 有）。

## 7. 验证清单

1. **cgo-shim-build**：workbuddy + qoderwork 全绿（build+vet+test）
2. **新增单测 `refresh_runner_test.go`**：
   - `TestRefreshRunner_EnqueueCoalesce`：连发 3 次 EnqueueAll 合并为 1 batch
   - `TestRefreshRunner_DoesNotBlock`：EnqueueAll 后立刻返回，runner 在后台跑
   - `TestRefreshRunner_OnePerSecond`：5 个账号耗时 ≥4s（5 个 tick）
   - `TestRefreshRunner_StatusSnapshot`：running / done / failed 状态正确
3. **手工验证清单**（用户实际场景）：
   - 10 个账号场景：开面板应瞬开，没 credits 显示 "加载中…"；点刷新 → toast `已开始后台刷新 (10)`，10s 后陆续出现 done 高亮
   - 故意触发上游 5xx：1 账号失败时卡片显示 ⚠️ + 错误文字，不影响其他
   - 删除某账号后台刷新中的 → snapshot 仍带它但不阻塞 polling
4. **Publish 链路**：bump workbuddy → publish → bumps qoderwork → publish → registry 同步

## 8. 风险与回退

| 风险 | 概率 | 缓解 |
|---|---|---|
| watchdog 改成异步后，preserve flip 不再同一 tick 内生效 | 中 | 接受 ≤ 1 个 tick（10m）的延迟；保号最坏后果是 1 个 tick 内仍被路由 |
| 刷新状态字段加进 `/accounts` 后，前端某些场景刷新不及时 | 低 | status 直接读 /refresh/status，不耦合 |
| 单测覆盖不全（如 singleflight reuse） | 中 | 优先测核心路径；监控 publish 后首日面板日志 |
| qoderwork 多余补丁漏改 | 低 | 提交前双插件 cgo-shim-build 必须都过 |

## 9. 后续可优化（非本轮）

- 进度推送改 SSE（如果轮询被限流）
- `refreshTickInterval` 改成可配置（config_yaml）
- 卡片显示真实 `failed_count / queued_count` 计数
