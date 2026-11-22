// credits_sweep_guard_test.go 锁定积分扫描循环的实例守卫闸门。
//
// 生产背景（2026-11-22）：creditSweepLoop 原先没有任何实例守卫，热重载后旧实例
// 继续每小时扫一遍全部 43 个账号（实测每小时 3 轮，相位 HH:06 / HH:21 / HH:43），
// 三代实例互相覆盖同一份快照。本文件锁定「被取代即停手、仍是持有者则照常扫描」。
package main

import (
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// errGuardTestListStop 是本文件用来「让一轮扫描立刻结束且不落盘」的哨兵错误。
var errGuardTestListStop = errors.New("guard test: stop sweep here")

// TestCreditSweepTickOnceRetiresWhenSuperseded 锁定闸门：标记被另一个存活实例
// 占据时，tick 必须返回 true（调用方据此退出循环），且一轮扫描也不许启动。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 18:05:00；改动原因：新增，锁住 D1 的扫描闸门。
func TestCreditSweepTickOnceRetiresWhenSuperseded(t *testing.T) {
	backupInstanceMarker(t)
	claimPluginInstance()
	if instanceGuardID == "" {
		t.Fatal("claimPluginInstance 未生成实例标识")
	}
	// 1. 用存活父进程充当「另一个实例」：写死 pid 1 在 Windows 开发机上不存在。
	liveOther := liveOtherPID(t)
	liveMarker := strconv.FormatInt(1, 36) + "-" + strconv.Itoa(liveOther)
	if err := os.WriteFile(pluginInstancePath(), []byte(liveMarker), 0o644); err != nil {
		t.Fatalf("写入存活实例标记失败: %v", err)
	}
	// 2. 闸门必须拦下本轮，且不得触发任何扫描（list 一旦被调用即判失败）。
	origList := creditsSweepListFn
	t.Cleanup(func() { creditsSweepListFn = origList })
	creditsSweepListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		t.Error("被取代的实例不得启动扫描")
		return nil, errGuardTestListStop
	}
	if !creditSweepTickOnce() {
		t.Fatal("被存活实例取代时必须返回 true，否则旧实例会继续扫描")
	}
}

// TestCreditSweepTickOnceRunsWhenOwner 反证：本实例仍是标记持有者时闸门必须放行，
// 修复不能把正常扫描一并停掉。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 18:05:00；改动原因：新增。
func TestCreditSweepTickOnceRunsWhenOwner(t *testing.T) {
	backupInstanceMarker(t)
	claimPluginInstance()
	// 1. 刚登记的实例必须是标记持有者，闸门不得拦。
	// 标记文件可能被同包其他用例改写过，先显式写回本实例标识再断言。
	if err := os.WriteFile(pluginInstancePath(), []byte(instanceGuardID), 0o644); err != nil {
		t.Fatalf("写回本实例标记失败: %v", err)
	}
	if pluginInstanceSuperseded() {
		t.Fatal("刚登记的实例不应被判为过期")
	}
	// 2. 让本轮扫描立刻结束：list 返回哨兵错误，runCreditSweep 直接返回不落盘。
	origList := creditsSweepListFn
	t.Cleanup(func() { creditsSweepListFn = origList })
	var calls int
	creditsSweepListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		calls++
		return nil, errGuardTestListStop
	}
	if creditSweepTickOnce() {
		t.Fatal("当前实例持有标记时不应退出循环")
	}
	// 3. 扫描是异步入场的，等它释放扫描锁后再断言，避免读到未执行的中间态。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if creditsSweepMu.TryLock() {
			creditsSweepMu.Unlock()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls != 1 {
		t.Fatalf("持有标记时本轮扫描应启动一次，got %d 次", calls)
	}
}
