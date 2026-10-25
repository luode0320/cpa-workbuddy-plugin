package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 故障 sidecar 的三条硬约束（2026-10-25）：
//  1. 跨重启存活——进程重启后禁用判定必须仍然生效，否则热重载一次整池坏号回流；
//  2. 损坏容错——文件坏了按空状态继续，绝不能 panic 或拒绝服务；
//  3. 只动目标——清除一个账号不得连带清掉别的账号。
//
// 隔离纪律：sidecar 是进程级共享状态，每个用例必须先用 tmpdir 注入路径，
// 否则用例之间会经由真实 data 目录互相污染（qoderwork 0.11.0 踩过这个坑）。

// setupFailoverStateDir 把 sidecar 指向 t.TempDir() 下的临时文件并清理内存镜像。
func setupFailoverStateDir(t *testing.T) string {
	t.Helper()
	resetFailoverStateForTest()
	path := filepath.Join(t.TempDir(), failoverStateFileName)
	restore := isolateFailoverStatePathForTest(path)
	t.Cleanup(func() {
		restore()
		resetFailoverStateForTest()
	})
	return path
}

// TestFailoverState_SurvivesRestart 模拟「写禁用 → 进程重启 → 判定仍生效」。
//
// 重启的关键语义是内存镜像清空、只留磁盘文件。这里用 resetFailoverStateForTest
// 清空内存 + 保留已落盘的临时文件来复刻该状态。
func TestFailoverState_SurvivesRestart(t *testing.T) {
	setupFailoverStateDir(t)

	failoverStateMarkDisabled("traework-acc-restart", "qwen3.8-max")
	if !failoverStateIsDisabled("acc-restart") {
		t.Fatalf("写入后应立即判定为禁用（归一化键必须命中）")
	}

	// 复刻重启：只清内存，磁盘文件保持不动。
	resetFailoverStateForTest()
	if !failoverStateIsDisabled("traework-acc-restart") {
		t.Fatalf("进程重启后 sidecar 禁用状态必须从磁盘恢复")
	}
	entry, ok := failoverStateGet("acc-restart")
	if !ok {
		t.Fatalf("重启后必须能读到状态条目")
	}
	if entry.Model != "qwen3.8-max" {
		t.Fatalf("重启后失败模型必须存活，want qwen3.8-max got %q", entry.Model)
	}
	if entry.At == "" {
		t.Fatalf("重启后禁用时刻必须存活")
	}
}

// TestFailoverState_ToleratesCorruptFile 锁定损坏容错：坏文件按空状态继续。
func TestFailoverState_ToleratesCorruptFile(t *testing.T) {
	path := setupFailoverStateDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"accounts":{`), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	if failoverStateIsDisabled("acc-corrupt") {
		t.Fatalf("损坏文件必须按空状态处理，不得凭空判定禁用")
	}
	// 写入必须仍然可用：容错不等于禁用整个机制。
	failoverStateMarkDisabled("acc-corrupt", "m1")
	if !failoverStateIsDisabled("acc-corrupt") {
		t.Fatalf("损坏文件之后写操作必须能覆盖为正常 sidecar")
	}
}

// TestFailoverState_VersionMismatchIsEmpty 锁定版本不认识的降级口径。
func TestFailoverState_VersionMismatchIsEmpty(t *testing.T) {
	path := setupFailoverStateDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"version":99,"updated_at":"","accounts":{"acc":{"disabled":true}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if failoverStateIsDisabled("acc") {
		t.Fatalf("版本不认识时必须按空状态处理，不能按错误 schema 解释禁用状态")
	}
}

// TestFailoverState_FailurePathWritesSidecar 锁定整合链路：真实失败记账必须写盘。
//
// 前面几个用例只测 sidecar 自身的读写；若 disableAccountForFailover 里的
// failoverStateMarkDisabled 调用被删掉，它们全部仍会通过——这正是变异测试
// 存活的形态。本用例走生产入口 recordAccountFailure，断言失败确实落到 sidecar。
func TestFailoverState_FailurePathWritesSidecar(t *testing.T) {
	setupFailoverStateDir(t)
	clearFailoverStates()
	t.Cleanup(clearFailoverStates)
	// 必须给一个可解析的宿主账号：解析不到 auth_index 时禁用链路会整体跳过
	// （无主禁用探不回来），这条跳过规则本身由 failover_disable_test.go 覆盖。
	h := newFailoverFakeHost(t)
	h.addAccount("acc-integration", "idx-integration", nil)
	setFailoverEnabled(true)
	t.Cleanup(func() { setFailoverEnabled(true) })

	if !recordAccountFailure("acc-integration", 500, "boom") {
		t.Fatalf("500 必须记账")
	}
	if !failoverStateIsDisabled("acc-integration") {
		t.Fatalf("失败记账必须同时写入 sidecar（否则重启后禁用状态全丢）")
	}
}

// TestFailoverState_ClearOnlyTouchesTarget 锁定清除边界：只动目标账号。
func TestFailoverState_ClearOnlyTouchesTarget(t *testing.T) {
	setupFailoverStateDir(t)

	failoverStateMarkDisabled("acc-a", "m1")
	failoverStateMarkDisabled("acc-b", "m2")

	failoverStateClearDisabled("acc-a")

	if failoverStateIsDisabled("acc-a") {
		t.Fatalf("清除后目标账号不应再被禁用")
	}
	if !failoverStateIsDisabled("acc-b") {
		t.Fatalf("清除 acc-a 不得连带清掉 acc-b")
	}
	// 清空后的条目全字段为空，落盘时会被过滤掉（不占盘），因此内存里可能有
	// 但磁盘上不会有 —— 这里只断言「不是禁用」这个生效语义。
	if entry, ok := failoverStateGet("acc-a"); ok && entry.Disabled {
		t.Fatalf("清除后条目的 Disabled 必须为 false，got %+v", entry)
	}
}

// TestFailoverState_RecordFailurePersistsCooldown 锁定冷却跨重启存活。
func TestFailoverState_RecordFailurePersistsCooldown(t *testing.T) {
	setupFailoverStateDir(t)

	failoverStateRecordFailure("acc-cooldown", time.Now().Add(90*time.Second), 3)
	resetFailoverStateForTest()

	entry, ok := failoverStateGet("acc-cooldown")
	if !ok {
		t.Fatalf("冷却状态必须落盘")
	}
	if entry.FailCount != 3 {
		t.Fatalf("FailCount 必须存活，want 3 got %d", entry.FailCount)
	}
	if entry.CooldownUntil == 0 {
		t.Fatalf("CooldownUntil 必须存活")
	}
}

// TestFailoverState_MarkDisabledIdempotent 锁定重复禁用不重复写盘。
func TestFailoverState_MarkDisabledIdempotent(t *testing.T) {
	path := setupFailoverStateDir(t)

	failoverStateMarkDisabled("acc-idem", "m1")
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after first write: %v", err)
	}
	// 同账号同模型的重复禁用不得改写文件（高频失败不该放大成高频磁盘 IO）。
	failoverStateMarkDisabled("acc-idem", "m1")
	second, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after second write: %v", err)
	}
	if !first.ModTime().Equal(second.ModTime()) || first.Size() != second.Size() {
		t.Fatalf("同账号同模型的重复禁用不应重写文件")
	}
}
