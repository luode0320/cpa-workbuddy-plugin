package main

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖插件内定时续期循环：目标筛选（禁用 / 缺路径）、单账号失败
// 隔离、列表失败不 panic，以及宿主就绪等待的边界行为。
//
// 不覆盖真实上游：续期内核已由 login_test.go 的 token beat 桩验证。

// renewRecorder 记录一轮续期里每个账号被调用的次数与结果。
type renewRecorder struct {
	mu      sync.Mutex
	calls   map[string]int
	renewed map[string]bool
	logs    []string
}

// newRenewRecorder 构造空记录器。
// [参数] 无。
// [返回] 记录器。
// 最近修改时间：2026-10-18；改动原因：新增，测试记录器。
func newRenewRecorder() *renewRecorder {
	return &renewRecorder{calls: map[string]int{}, renewed: map[string]bool{}}
}

// deps 生成一整套注入依赖：list 返回给定账号，renew 按 authID 决定成败。
// [参数] accounts: 账号列表；failIDs: 需要返回失败的账号标识。
// [返回] 依赖集合。
// 最近修改时间：2026-10-18；改动原因：新增，测试依赖装配。
func (r *renewRecorder) deps(accounts []*StoredAuth, failIDs ...string) renewDeps {
	fail := make(map[string]bool, len(failIDs))
	for _, id := range failIDs {
		fail[id] = true
	}
	return renewDeps{
		list: func() ([]*StoredAuth, error) { return accounts, nil },
		renew: func(sa *StoredAuth) (*renewResult, error) {
			r.mu.Lock()
			r.calls[sa.AuthID]++
			r.mu.Unlock()
			if fail[sa.AuthID] {
				return nil, errors.New("上游拒绝")
			}
			r.mu.Lock()
			r.renewed[sa.AuthID] = true
			r.mu.Unlock()
			return &renewResult{Renewed: true, Note: "测试换发"}, nil
		},
		logf: func(level, message string, fields map[string]any) {
			r.mu.Lock()
			r.logs = append(r.logs, level+" "+message)
			r.mu.Unlock()
		},
	}
}

// callCount 返回某账号被调用的次数。
// [参数] authID: 账号标识。
// [返回] 次数。
// 最近修改时间：2026-10-18；改动原因：新增，断言辅助。
func (r *renewRecorder) callCount(authID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[authID]
}

// TestRenewTargets_IncludesDisabledAndSkipsPathless 验证禁用号参与续期、缺路径被排除。
//
// 语义在 2026-10-25 反转：禁用号必须继续续期，否则 Cookie 会在等待
// 「次日签到」的窗口内失效，签到失败后账号永久卡死无法自动恢复。
// 缺路径的账号仍要排除——续期结果无处落盘，跑了也是白耗上游请求。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：禁用号改为参与续期（解禁依赖凭据可用）。
func TestRenewTargets_IncludesDisabledAndSkipsPathless(t *testing.T) {
	accounts := []*StoredAuth{
		{AuthID: "coze-ok", PhysicalPath: "/tmp/coze-ok.json"},
		{AuthID: "coze-disabled", PhysicalPath: "/tmp/coze-disabled.json", Disabled: true},
		{AuthID: "coze-nopath"},
		nil,
	}
	targets, pathless := renewTargets(accounts)
	if len(targets) != 2 || targets[0].AuthID != "coze-ok" || targets[1].AuthID != "coze-disabled" {
		t.Fatalf("待续期账号 = %v，期望 [coze-ok coze-disabled]", authIDsOf(targets))
	}
	if len(pathless) != 1 || pathless[0] != "coze-nopath" {
		t.Fatalf("缺路径账号 = %v，期望 [coze-nopath]", pathless)
	}
}

// authIDsOf 提取账号标识列表，供断言消息使用。
// [参数] accounts: 账号列表。
// [返回] 标识切片。
// 最近修改时间：2026-10-18；改动原因：新增，断言辅助。
func authIDsOf(accounts []*StoredAuth) []string {
	out := make([]string, 0, len(accounts))
	for _, sa := range accounts {
		out = append(out, sa.AuthID)
	}
	return out
}

// TestRunRenewTick_RenewsAllAccounts 验证一轮会续期所有具路径账号（含禁用）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：禁用号改为参与续期。
func TestRunRenewTick_RenewsAllAccounts(t *testing.T) {
	rec := newRenewRecorder()
	accounts := []*StoredAuth{
		{AuthID: "coze-a", PhysicalPath: "/tmp/a.json"},
		{AuthID: "coze-b", PhysicalPath: "/tmp/b.json"},
		{AuthID: "coze-c", PhysicalPath: "/tmp/c.json", Disabled: true},
	}
	renewed := runRenewTick(rec.deps(accounts))
	if renewed != 3 {
		t.Fatalf("成功续期数 = %d，期望 3", renewed)
	}
	if rec.callCount("coze-c") != 1 {
		t.Fatalf("禁用账号应继续续期（解禁依赖凭据可用），实际调用 %d 次", rec.callCount("coze-c"))
	}
	if rec.callCount("coze-a") != 1 || rec.callCount("coze-b") != 1 {
		t.Fatalf("启用账号调用次数 = a:%d b:%d，期望各 1 次", rec.callCount("coze-a"), rec.callCount("coze-b"))
	}
}

// TestRunRenewTick_IsolatesFailure 验证单账号失败不影响同轮其它账号。
//
// 这是定时续期的关键语义：一个账号凭据坏掉，不能让整批账号停更。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，失败隔离。
func TestRunRenewTick_IsolatesFailure(t *testing.T) {
	rec := newRenewRecorder()
	accounts := []*StoredAuth{
		{AuthID: "coze-bad", PhysicalPath: "/tmp/bad.json"},
		{AuthID: "coze-good", PhysicalPath: "/tmp/good.json"},
	}
	renewed := runRenewTick(rec.deps(accounts, "coze-bad"))
	if renewed != 1 {
		t.Fatalf("成功续期数 = %d，期望 1", renewed)
	}
	if !rec.renewed["coze-good"] {
		t.Fatal("失败账号不应阻断同轮其它账号续期")
	}
	if rec.callCount("coze-bad") != 1 {
		t.Fatalf("失败账号应尝试 1 次，实际 %d 次", rec.callCount("coze-bad"))
	}
}

// TestRunRenewTick_ListErrorReturnsZero 验证列表失败时不 panic 且返回 0。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，错误路径。
func TestRunRenewTick_ListErrorReturnsZero(t *testing.T) {
	var logged bool
	deps := renewDeps{
		list: func() ([]*StoredAuth, error) { return nil, errors.New("宿主不可用") },
		renew: func(*StoredAuth) (*renewResult, error) {
			t.Fatal("列表失败时不应调用续期")
			return nil, nil
		},
		logf: func(string, string, map[string]any) { logged = true },
	}
	if renewed := runRenewTick(deps); renewed != 0 {
		t.Fatalf("成功续期数 = %d，期望 0", renewed)
	}
	if !logged {
		t.Fatal("列表失败应记录日志")
	}
}

// TestRunRenewTick_EmptyDepsNoPanic 验证依赖缺失时安全返回。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，防御性分支。
func TestRunRenewTick_EmptyDepsNoPanic(t *testing.T) {
	if renewed := runRenewTick(renewDeps{}); renewed != 0 {
		t.Fatalf("成功续期数 = %d，期望 0", renewed)
	}
}

// TestWaitHostReadyForRenew_ZeroWaitProbesOnce 验证 maxWait<=0 时只探测一次。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，等待边界。
func TestWaitHostReadyForRenew_ZeroWaitProbesOnce(t *testing.T) {
	var calls int
	ready := func() bool {
		calls++
		return true
	}
	if !waitHostReadyForRenew(0, ready) {
		t.Fatal("就绪时应返回 true")
	}
	if calls != 1 {
		t.Fatalf("探测次数 = %d，期望 1", calls)
	}
}

// TestWaitHostReadyForRenew_TimesOut 验证超时后返回 false 且不无限等待。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，超时路径。
func TestWaitHostReadyForRenew_TimesOut(t *testing.T) {
	start := time.Now()
	if waitHostReadyForRenew(60*time.Millisecond, func() bool { return false }) {
		t.Fatal("始终未就绪时应返回 false")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("等待耗时 %v，超时后应立即返回", elapsed)
	}
}

// TestRenewIntervalDefaultIsFourHours 验证默认续期间隔为 4 小时。
//
// 用户明确要求「定时 4 小时补 refresh_interval_seconds，和其他插件一样」；
// 这个值同时驱动宿主 metadata 与插件内定时器，改动会被两条链路同时感知。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，需求口径锁定。
func TestRenewIntervalDefaultIsFourHours(t *testing.T) {
	if got := defaultConfig().RenewIntervalSeconds; got != 4*3600 {
		t.Fatalf("默认续期间隔 = %d，期望 %d", got, 4*3600)
	}
}

// TestRenewLoopFiresOnInterval 验证循环按配置间隔重复触发。
//
// 这里驱动真实的 renewLoopWith（不是重写的近似逻辑）：把启动等待设为 0、
// 间隔压到 1 秒，用假依赖计数，确认「首轮 + 定时轮」都真实发生。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，定时器真实行为验证。
func TestRenewLoopFiresOnInterval(t *testing.T) {
	prevCfg := currentConfig()
	t.Cleanup(func() { activeConfig.Store(prevCfg) })

	cfg := defaultConfig()
	cfg.RenewIntervalSeconds = 1
	activeConfig.Store(cfg)

	var mu sync.Mutex
	tickCount := 0
	deps := renewDeps{
		list: func() ([]*StoredAuth, error) {
			mu.Lock()
			tickCount++
			mu.Unlock()
			return nil, nil
		},
		renew: func(*StoredAuth) (*renewResult, error) {
			return &renewResult{Renewed: true}, nil
		},
		logf: func(string, string, map[string]any) {},
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		renewLoopWith(0, func() bool { return true }, deps, stop)
		close(done)
	}()
	t.Cleanup(func() { close(stop) })

	// 首轮 + 一个定时周期：3s 足以覆盖一次 1s 定时触发。
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		got := tickCount
		mu.Unlock()
		if got >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("3s 内循环轮次 = %d，期望至少 2 轮", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRenewLoopStopsOnSignal 验证停止信号能立即唤醒循环并退出。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，热重载退出路径。
func TestRenewLoopStopsOnSignal(t *testing.T) {
	prevCfg := currentConfig()
	t.Cleanup(func() { activeConfig.Store(prevCfg) })
	cfg := defaultConfig()
	cfg.RenewIntervalSeconds = 3600
	activeConfig.Store(cfg)

	deps := renewDeps{
		list:  func() ([]*StoredAuth, error) { return nil, nil },
		renew: func(*StoredAuth) (*renewResult, error) { return &renewResult{}, nil },
		logf:  func(string, string, map[string]any) {},
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		renewLoopWith(0, func() bool { return true }, deps, stop)
		close(done)
	}()

	// 等首轮跑完再发停止信号，避免在首轮中途退出而掩盖定时等待分支。
	time.Sleep(100 * time.Millisecond)
	close(stop)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("停止信号后 2s 内循环未退出")
	}
}
