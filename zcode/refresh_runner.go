package main

import (
	"strings"
	"sync"
	"time"
)

// 后台额度刷新器：参照 workbuddy 的 refreshRunner 语义，为 zcode 面板提供
// 「进入即刷 + 手动刷新」的批量额度拉取，并以只读快照暴露进度供前端轮询。
//
// 与 workbuddy 的差异：zcode 无签到/领取等额度变更动作，刷新只读 billing/current，
// 因此不引入令牌桶限流与重试退避，只保留幂等（同一时刻至多一轮）与逐账号进度。
const (
	zcodeRefreshPerAccountDelay = 300 * time.Millisecond
)

// refreshTarget 是单次刷新任务的目标账号。
type refreshTarget struct {
	AuthID string
	Auth   *StoredAuth
}

// refreshJobState 是单账号刷新状态。
type refreshJobState struct {
	Status string `json:"status"` // pending | running | done | failed
	Error  string `json:"error,omitempty"`
}

// refreshSnapshot 是 GET /refresh/status 的只读视图。
type refreshSnapshot struct {
	Running    bool                     `json:"running"`
	Total      int                      `json:"total"`
	Done       int                      `json:"done"`
	Failed     int                      `json:"failed"`
	StartedAt  string                   `json:"started_at,omitempty"`
	PerAccount []refreshAccountProgress `json:"per_account"`
}

// refreshAccountProgress 是快照中的单账号进度。
type refreshAccountProgress struct {
	AuthID string `json:"auth_index"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// zcodeRefreshRunner 串行执行批量刷新，保证同一时刻至多一轮。
type zcodeRefreshRunner struct {
	mu       sync.Mutex
	inFlight bool
	gen      int
	states   map[string]refreshJobState
	order    []string
	started  time.Time
	wake     chan struct{}
}

var globalQuotaRefresh = newZcodeRefreshRunner()

// newZcodeRefreshRunner 构造刷新器并启动工作协程。
//
// [参数] 无。
// [返回] 刷新器实例。
func newZcodeRefreshRunner() *zcodeRefreshRunner {
	r := &zcodeRefreshRunner{
		states: make(map[string]refreshJobState),
		wake:   make(chan struct{}, 1),
	}
	go r.run()
	return r
}

// EnqueueAll 启动一轮全量刷新；已有刷新在跑时幂等忽略本次请求。
//
// [参数] targets: 待刷新账号列表；source: 触发来源（仅用于日志语义）。
// [返回] 实际入队账号数；已有刷新在跑时返回 -1。
func (r *zcodeRefreshRunner) EnqueueAll(targets []refreshTarget) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight {
		return -1
	}
	r.gen++
	r.states = make(map[string]refreshJobState, len(targets))
	r.order = r.order[:0]
	for _, t := range targets {
		id := strings.TrimSpace(t.AuthID)
		if id == "" {
			continue
		}
		r.states[id] = refreshJobState{Status: "pending"}
		r.order = append(r.order, id)
	}
	r.started = time.Now()
	r.inFlight = len(r.order) > 0
	r.signal()
	return len(r.order)
}

// EnqueueOne 刷新单个账号；已有全量刷新在跑时仅追加/覆盖该账号状态。
//
// [参数] authID: 账号标识。
// [返回] 是否成功入队。
func (r *zcodeRefreshRunner) EnqueueOne(authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.states[authID]; !ok {
		r.order = append(r.order, authID)
	}
	r.states[authID] = refreshJobState{Status: "pending"}
	if !r.inFlight {
		r.gen++
		r.started = time.Now()
		r.inFlight = true
	}
	r.signal()
	return true
}

// signal 唤醒工作协程（非阻塞）。
//
// [参数] 无。
// [返回] 无。
func (r *zcodeRefreshRunner) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// run 是工作协程主循环：被唤醒后串行拉取所有 pending 账号额度。
//
// [参数] 无。
// [返回] 无（常驻协程）。
func (r *zcodeRefreshRunner) run() {
	for range r.wake {
		for {
			r.mu.Lock()
			if !r.inFlight {
				r.mu.Unlock()
				break
			}
			// 取出下一个 pending 账号。
			next := ""
			for _, id := range r.order {
				if st, ok := r.states[id]; ok && st.Status == "pending" {
					next = id
					break
				}
			}
			if next == "" {
				// 本轮结束：把残留 running 视作异常并收尾。
				for id, st := range r.states {
					if st.Status == "running" {
						st.Status = "failed"
						st.Error = "刷新中断"
						r.states[id] = st
					}
				}
				r.inFlight = false
				r.mu.Unlock()
				break
			}
			r.states[next] = refreshJobState{Status: "running"}
			r.mu.Unlock()

			r.refreshOne(next)

			time.Sleep(zcodeRefreshPerAccountDelay)
		}
	}
}

// refreshOne 刷新单个账号并把结果写回状态表。
//
// [参数] authID: 账号标识。
// [返回] 无。
func (r *zcodeRefreshRunner) refreshOne(authID string) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		r.markFailed(authID, "加载账号失败: "+errList.Error())
		return
	}
	var target *StoredAuth
	for _, acc := range accounts {
		if acc.AuthID == authID {
			target = acc
			break
		}
	}
	if target == nil {
		r.markFailed(authID, "账号不存在")
		return
	}
	if snap, errFetch := quotaForAccount(target, true); errFetch != nil {
		r.markFailed(authID, errFetch.Error())
		return
	} else {
		// 拿到新鲜额度后立即分级：耗尽即写盘禁用，已越过重置时刻则按探活结果恢复。
		authIndex, _ := failoverResolveIdxFn(authID)
		gradeQuotaForAuth(authIndex, authID, target, snap)
	}
	// 额度刷新成功后触发定时活跃探活，失败仅记录不做刷新失败处理
	if pingErr := doActivePing(target); pingErr != nil {
		r.markPingWarning(authID, pingErr.Error())
	}
	r.mu.Lock()
	r.states[authID] = refreshJobState{Status: "done"}
	r.mu.Unlock()
}

// markPingWarning 在刷新成功但探活失败时记录告警文案，供面板提示。
//
// [参数] authID: 账号标识；msg: 探活失败信息。
// [返回] 无。
func (r *zcodeRefreshRunner) markPingWarning(authID, msg string) {
	r.mu.Lock()
	r.states[authID] = refreshJobState{Status: "done", Error: "探活告警: " + msg}
	r.mu.Unlock()
}

// markFailed 记录单账号刷新失败。
//
// [参数] authID: 账号标识；msg: 错误信息。
// [返回] 无。
func (r *zcodeRefreshRunner) markFailed(authID, msg string) {
	r.mu.Lock()
	r.states[authID] = refreshJobState{Status: "failed", Error: msg}
	r.mu.Unlock()
}

// Snapshot 返回一致的只读进度视图。
//
// [参数] 无。
// [返回] 进度快照。
func (r *zcodeRefreshRunner) Snapshot() refreshSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := refreshSnapshot{
		Running: r.inFlight,
		Total:   len(r.order),
	}
	if r.inFlight && !r.started.IsZero() {
		snap.StartedAt = r.started.UTC().Format(time.RFC3339)
	}
	snap.PerAccount = make([]refreshAccountProgress, 0, len(r.order))
	for _, id := range r.order {
		st, ok := r.states[id]
		if !ok {
			continue
		}
		switch st.Status {
		case "done":
			snap.Done++
		case "failed":
			snap.Failed++
		}
		snap.PerAccount = append(snap.PerAccount, refreshAccountProgress{
			AuthID: id,
			Status: st.Status,
			Error:  st.Error,
		})
	}
	return snap
}
