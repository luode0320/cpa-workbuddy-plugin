// instance_guard_orphan_test.go 锁定「孤儿实例标记接管」语义。
//
// 生产背景（2026-11-22）：容器内执行过一次短命进程，留下标记
// `dn0mlpa0gx1h-1930`，而 pid 1930 早已不存在。旧实现只比较标记内容与本实例
// 标识，于是长驻实例每次判守卫都得到「已被取代」，积分扫描与探活被永久压死；
// 修复方向是识别「标记所有者进程已退出」并接管。
package main

import (
	"os"
	"strconv"
	"testing"
)

// findDeadPID 找一个当前确实不存在的进程号，供孤儿标记用例构造死进程标记。
// [参数] 无。
// [返回] 可安全视作「已退出进程」的 pid。
// 最近修改时间：2026-11-22 17:55:00；改动原因：新增，避免在用例里写死平台相关的 pid。
func findDeadPID(t *testing.T) int {
	t.Helper()
	for pid := 60000; pid > 1000; pid-- {
		if !processAlive(pid) {
			return pid
		}
	}
	t.Fatal("未能找到空闲 pid，无法构造孤儿标记")
	return 0
}

// backupInstanceMarker 备份当前标记文件并注册恢复逻辑，避免用例污染进程标记。
// [参数] t：测试上下文。
// [返回] 标记文件路径。
// 最近修改时间：2026-11-22 17:55:00；改动原因：新增，统一标记文件的保存与还原。
func backupInstanceMarker(t *testing.T) string {
	t.Helper()
	path := pluginInstancePath()
	backup, backupErr := os.ReadFile(path)
	t.Cleanup(func() {
		if backupErr == nil {
			_ = os.WriteFile(path, backup, 0o644)
			return
		}
		_ = os.Remove(path)
	})
	return path
}

// TestOrphanOwnerMarkerKeepsUnknownMarkersAuthoritative 锁定孤儿判定的保守边界：
// 只有能解出 pid 且进程确实不存在才算孤儿；纯文本、无 pid 后缀、自身 pid 一律
// 按「可能是更新实例」处理，否则历史守卫语义会被未知来源的标记悄悄废掉。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 17:55:00；改动原因：新增。
func TestOrphanOwnerMarkerKeepsUnknownMarkersAuthoritative(t *testing.T) {
	// 1. 解析不出 pid 的标记必须继续当作权威标记，不能被判成孤儿。
	for _, owner := range []string{"newer-instance", "abc-", "abc-notapid"} {
		if orphanOwnerMarker(owner) {
			t.Fatalf("标记 %q 不应判为孤儿", owner)
		}
	}
	// 2. 自身 pid 属于「同进程第二次登记」，不是孤儿。
	self := strconv.FormatInt(1, 36) + "-" + strconv.Itoa(os.Getpid())
	if orphanOwnerMarker(self) {
		t.Fatalf("自身 pid 标记 %q 不应判为孤儿", self)
	}
	// 3. 空标记与死进程标记才是真正的孤儿。
	if !orphanOwnerMarker("   ") {
		t.Fatal("空标记必须判为孤儿，否则损坏标记会压死常驻循环")
	}
	dead := strconv.FormatInt(1, 36) + "-" + strconv.Itoa(findDeadPID(t))
	if !orphanOwnerMarker(dead) {
		t.Fatalf("死进程标记 %q 必须判为孤儿", dead)
	}
}

// TestPluginInstanceSupersededTakesOverOrphanMarker 锁定接管行为：标记属于已退出
// 进程时，守卫必须返回 false 并把标记改写成当前实例，让后续判断回到正常轨道。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 17:55:00；改动原因：新增。
func TestPluginInstanceSupersededTakesOverOrphanMarker(t *testing.T) {
	path := backupInstanceMarker(t)
	claimPluginInstance()
	if instanceGuardID == "" {
		t.Fatal("claimPluginInstance 未生成实例标识")
	}
	deadMarker := strconv.FormatInt(1, 36) + "-" + strconv.Itoa(findDeadPID(t))
	if err := os.WriteFile(path, []byte(deadMarker), 0o644); err != nil {
		t.Fatalf("写入孤儿标记失败: %v", err)
	}

	// 1. 死进程标记不能让本实例误判为「已被取代」。
	if pluginInstanceSuperseded() {
		t.Fatal("孤儿标记必须被接管，否则常驻循环会被永久压死")
	}
	// 2. 接管后标记内容应已是本实例标识，且再次判断仍然放行。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回标记失败: %v", err)
	}
	if got := string(raw); got != instanceGuardID {
		t.Fatalf("标记未被接管: got %q, want %q", got, instanceGuardID)
	}
	if pluginInstanceSuperseded() {
		t.Fatal("接管后应稳定判为当前实例")
	}
}

// liveOtherPID 返回一个「确定存活、且不等于本进程」的 pid，供存活实例语义用例使用。
//
// 用父进程而非写死的 pid 1：pid 1 在容器里常驻，但在 Windows 开发机上不存在，
// 写死会让用例在本地被跳过，等于没有守住「活标记仍然权威」这条语义。
// [参数] t：测试上下文。
// [返回] 存活且不同于本进程的 pid。
// 最近修改时间：2026-11-22 18:12:00；改动原因：新增，替换不可移植的 pid 1 假设。
func liveOtherPID(t *testing.T) int {
	t.Helper()
	pid := os.Getppid()
	if pid <= 0 || pid == os.Getpid() || !processAlive(pid) {
		t.Skip("父进程不可探测，跳过存活实例语义用例")
	}
	return pid
}

// TestOrphanOwnerMarkerNotClaimedByLiveOwner 反证：活跃实例（存活的不同 pid）写下
// 的标记仍必须判为「已被取代」，修复不能把守卫本身废掉。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 18:12:00；改动原因：改用存活父进程 pid，避免平台差异导致用例被跳过。
func TestOrphanOwnerMarkerNotClaimedByLiveOwner(t *testing.T) {
	pid := liveOtherPID(t)
	if orphanOwnerMarker("abc-" + strconv.Itoa(pid)) {
		t.Fatalf("pid %d 存活时该标记不得判为孤儿", pid)
	}
}
