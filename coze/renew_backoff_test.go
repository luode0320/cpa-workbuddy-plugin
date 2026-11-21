package main

import (
	"sync"
	"testing"
	"time"
)

// 本文件覆盖续期失败退避（TASK-03）与未换发日志区分（TASK-02）。
//
// 为什么要退避：失效账号打 token beat 必定返回会话过期，而定时器每 4
// 小时就跑一轮。实测 12 个账号里 5 个已失效，8 天累计约 240 次必然失败
// 的上游请求——白耗配额，还淹掉真正的告警。用例全部用假时钟，不真实等待。

// TestRenewBackoff_DelayLadder 验证阶梯取值与封顶。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，阶梯口径锁定。
func TestRenewBackoff_DelayLadder(t *testing.T) {
	state := newRenewBackoffState()
	cases := []struct {
		count int
		want  time.Duration
	}{
		{1, 4 * time.Hour},
		{2, 8 * time.Hour},
		{3, 24 * time.Hour},
		{4, 48 * time.Hour},
		{5, 48 * time.Hour},
		{99, 48 * time.Hour},
		{0, 4 * time.Hour},
	}
	for _, c := range cases {
		if got := state.delayFor(c.count); got != c.want {
			t.Fatalf("连续失败 %d 次退避 = %v，期望 %v", c.count, got, c.want)
		}
	}
}

// TestRenewBackoff_SkipsWithinWindow 验证退避窗口内跳过、过期后恢复。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，过滤判据。
func TestRenewBackoff_SkipsWithinWindow(t *testing.T) {
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	state := newRenewBackoffState()
	state.now = func() time.Time { return base }

	if state.shouldSkip("coze-a") {
		t.Fatal("未知账号不应被跳过")
	}
	if count := state.recordFail("coze-a"); count != 1 {
		t.Fatalf("首次失败计数 = %d，期望 1", count)
	}
	if !state.shouldSkip("coze-a") {
		t.Fatal("刚失败后应处于退避窗口内")
	}
	state.now = func() time.Time { return base.Add(3 * time.Hour) }
	if !state.shouldSkip("coze-a") {
		t.Fatal("4 小时窗口内应继续跳过")
	}
	state.now = func() time.Time { return base.Add(4*time.Hour + time.Second) }
	if state.shouldSkip("coze-a") {
		t.Fatal("窗口结束后应允许重试")
	}
}

// TestRenewBackoff_SuccessClears 验证成功一次即清零。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，成功回退口径。
func TestRenewBackoff_SuccessClears(t *testing.T) {
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	state := newRenewBackoffState()
	state.now = func() time.Time { return base }
	state.recordFail("coze-a")
	state.recordFail("coze-a")

	state.recordSuccess("coze-a")
	if state.shouldSkip("coze-a") {
		t.Fatal("成功后必须立即解除退避")
	}
	if count := state.recordFail("coze-a"); count != 1 {
		t.Fatalf("清零后重新计数应为 1，实际 %d", count)
	}
}

// TestSplitByBackoff_SeparatesTargets 验证目标拆分。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，拆分口径。
func TestSplitByBackoff_SeparatesTargets(t *testing.T) {
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	state := newRenewBackoffState()
	state.now = func() time.Time { return base }
	state.recordFail("coze-skip")

	targets := []*StoredAuth{
		{AuthID: "coze-skip", PhysicalPath: "/tmp/a.json"},
		{AuthID: "coze-run", PhysicalPath: "/tmp/b.json"},
	}
	run, skipped := splitByBackoff(targets, state)
	if len(run) != 1 || run[0].AuthID != "coze-run" {
		t.Fatalf("本轮应只跑 coze-run，实际 %v", authIDsOf(run))
	}
	if len(skipped) != 1 || skipped[0] != "coze-skip" {
		t.Fatalf("应跳过 coze-skip，实际 %v", skipped)
	}
}

// TestSplitByBackoff_NilStatePassesThrough 验证状态缺失时原样放行。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，防御性分支。
func TestSplitByBackoff_NilStatePassesThrough(t *testing.T) {
	targets := []*StoredAuth{{AuthID: "coze-a", PhysicalPath: "/tmp/a.json"}}
	run, skipped := splitByBackoff(targets, nil)
	if len(run) != 1 || len(skipped) != 0 {
		t.Fatalf("状态缺失时应原样放行，实际 run=%v skipped=%v", authIDsOf(run), skipped)
	}
}

// TestRunRenewTick_SkipsBackoffAccount 验证退避中的账号不再打上游。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，节流主行为。
func TestRunRenewTick_SkipsBackoffAccount(t *testing.T) {
	rec := newRenewRecorder()
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	rec.backoff.now = func() time.Time { return base }
	rec.backoff.recordFail("coze-dead")

	accounts := []*StoredAuth{
		{AuthID: "coze-dead", PhysicalPath: "/tmp/dead.json"},
		{AuthID: "coze-live", PhysicalPath: "/tmp/live.json"},
	}
	renewed := runRenewTick(rec.deps(accounts))
	if renewed != 1 {
		t.Fatalf("成功续期数 = %d，期望 1（退避账号应被跳过）", renewed)
	}
	if rec.callCount("coze-dead") != 0 {
		t.Fatalf("退避中的账号不得调用上游，实际 %d 次", rec.callCount("coze-dead"))
	}
	if rec.callCount("coze-live") != 1 {
		t.Fatalf("正常账号应调用 1 次，实际 %d 次", rec.callCount("coze-live"))
	}
}

// TestRunRenewTick_FailureEntersBackoff 验证失败后下一轮被跳过、窗口后恢复。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，失败回写闭环。
func TestRunRenewTick_FailureEntersBackoff(t *testing.T) {
	rec := newRenewRecorder()
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	rec.backoff.now = func() time.Time { return base }

	accounts := []*StoredAuth{{AuthID: "coze-bad", PhysicalPath: "/tmp/bad.json"}}
	runRenewTick(rec.deps(accounts, "coze-bad"))
	if rec.callCount("coze-bad") != 1 {
		t.Fatalf("首轮应尝试 1 次，实际 %d 次", rec.callCount("coze-bad"))
	}
	runRenewTick(rec.deps(accounts, "coze-bad"))
	if rec.callCount("coze-bad") != 1 {
		t.Fatalf("退避窗口内不得重试，实际累计 %d 次", rec.callCount("coze-bad"))
	}
	rec.backoff.now = func() time.Time { return base.Add(4*time.Hour + time.Second) }
	runRenewTick(rec.deps(accounts, "coze-bad"))
	if rec.callCount("coze-bad") != 2 {
		t.Fatalf("窗口结束后应重试，实际累计 %d 次", rec.callCount("coze-bad"))
	}
}

// TestRunRenewTick_NotRenewedLogsWarn 验证未换发走 warn 而非 info。
//
// 这是 TASK-02 的核心：旧实现无论 Renewed 真假都打「定时续期完成」，
// 生产日志里两种走向不可区分，会话死了很久也看不出来。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，日志诚实性回归。
func TestRunRenewTick_NotRenewedLogsWarn(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	deps := renewDeps{
		list: func() ([]*StoredAuth, error) {
			return []*StoredAuth{{AuthID: "coze-stale", PhysicalPath: "/tmp/stale.json"}}, nil
		},
		renew: func(*StoredAuth) (*renewResult, error) {
			return &renewResult{Note: "第二步未换到会话主 Cookie（本轮下发 9 条 Cookie，无会话主项）"}, nil
		},
		logf: func(level, message string, fields map[string]any) {
			mu.Lock()
			logs = append(logs, level+" "+message)
			mu.Unlock()
		},
		backoff: newRenewBackoffState(),
	}
	if renewed := runRenewTick(deps); renewed != 0 {
		t.Fatalf("未换发不应计入成功数，实际 %d", renewed)
	}
	var sawWarn, sawCompleted bool
	for _, line := range logs {
		switch line {
		case "warn 定时续期未换发":
			sawWarn = true
		case "info 定时续期完成":
			sawCompleted = true
		}
	}
	if !sawWarn {
		t.Fatalf("未换发必须打 warn，实际日志 %v", logs)
	}
	if sawCompleted {
		t.Fatalf("未换发不得打「定时续期完成」，实际日志 %v", logs)
	}
}

// TestRunRenewTick_NotRenewedEntersBackoff 验证未换发同样计入退避。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，未换发也要节流。
func TestRunRenewTick_NotRenewedEntersBackoff(t *testing.T) {
	state := newRenewBackoffState()
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	state.now = func() time.Time { return base }
	deps := renewDeps{
		list: func() ([]*StoredAuth, error) {
			return []*StoredAuth{{AuthID: "coze-stale", PhysicalPath: "/tmp/stale.json"}}, nil
		},
		renew: func(*StoredAuth) (*renewResult, error) {
			return &renewResult{Note: "上游未换到会话主 Cookie"}, nil
		},
		logf:    func(string, string, map[string]any) {},
		backoff: state,
	}
	runRenewTick(deps)
	if !state.shouldSkip("coze-stale") {
		t.Fatal("未换发必须进入退避，否则每轮都会白打一次上游")
	}
}
