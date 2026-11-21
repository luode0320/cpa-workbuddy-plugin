// instance_guard_test.go 锁定「热重载后旧实例自行退出」的机制。
package main

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// backupInstanceMarker 备份并还原实例标记文件，供各用例复用。
// [参数] t：测试上下文
// [返回] 标记文件路径
// 最近修改时间：2026-11-21 13:48:00；改动原因：新增，收敛重复的备份还原逻辑。
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

// deadPID 返回一个当前必然不存在的进程号，用于伪造孤儿标记。
// [返回] 不存在的 pid
// 最近修改时间：2026-11-21 13:48:00；改动原因：新增。
func deadPID() int {
	// 取一个远高于常规 pid 上限的值，再叠加纳秒低位避免与任何刚退出的进程撞号。
	pid := 1 << 22
	for processAlive(pid) {
		pid++
	}
	return pid
}

// TestPluginInstanceSupersededDetectsNewOwner 验证新实例登记后旧实例会被判定为过期。
//
// 生产背景（2026-11-08）：宿主热重载不调用 plugin_shutdown，旧实例常驻循环
// 继续跑，探活请求随热重载次数线性放大（实测 10 分钟周期下 90 分钟被探活 96 次；
// 修复守卫位置后又实测 45 分钟内每账号被探活 59 次）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 20:35:00；改动原因：新增，锁住实例守卫语义。
func TestPluginInstanceSupersededDetectsNewOwner(t *testing.T) {
	path := backupInstanceMarker(t)

	// 1. 自己登记后不算过期。
	claimPluginInstance()
	if instanceGuardID == "" {
		t.Fatal("claimPluginInstance 未生成实例标识")
	}
	if pluginInstanceSuperseded() {
		t.Fatal("刚登记的实例不应被判为过期")
	}

	// 2. 模拟新实例覆盖所有权标记。写入存活 pid（当前进程），标记才代表
	// 一个真实存在的更新实例；死 pid 属于孤儿标记，由下面的接管用例覆盖。
	newer := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(path, []byte(newer), 0o644); err != nil {
		t.Fatalf("写入新实例标记失败: %v", err)
	}
	if !pluginInstanceSuperseded() {
		t.Fatal("所有权被覆盖后必须判为过期，否则旧循环不会退出")
	}
}

// TestPluginInstanceSupersededTakesOverOrphanMarker 锁定孤儿标记必须被接管。
//
// 生产背景（2026-11-21）：容器内执行过一次 ./CLIProxyAPI --version，该短命
// 进程加载全部插件、写入实例标记后立即退出；长驻实例从此把标记判定为「更新
// 实例」，watchdog 等十个常驻循环在各自下一拍全部 return，积分快照停摆超过
// 12 小时，且任何宿主 API（重装、DELETE+install、改配置）都无法复活——因为
// 重新 dlopen 同一个 .so 不会重跑 init()。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-21 13:48:00；改动原因：新增，锁住孤儿标记自愈语义。
func TestPluginInstanceSupersededTakesOverOrphanMarker(t *testing.T) {
	path := backupInstanceMarker(t)

	claimPluginInstance()
	// 伪造「标记属于已退出进程」的状态。
	orphan := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(deadPID())
	if !orphanOwnerMarker(orphan) {
		t.Fatalf("伪造的孤儿标记未被识别为死进程: %s", orphan)
	}
	if err := os.WriteFile(path, []byte(orphan), 0o644); err != nil {
		t.Fatalf("写入孤儿标记失败: %v", err)
	}

	if pluginInstanceSuperseded() {
		t.Fatal("标记所有者进程已退出时必须放行，否则常驻循环被永久压死")
	}

	// 接管后标记应回到本实例，后续判断不受影响。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取接管后的标记失败: %v", err)
	}
	if got := string(raw); got != instanceGuardID {
		t.Fatalf("孤儿标记未被接管，当前标记 = %q，期望 %q", got, instanceGuardID)
	}
	if pluginInstanceSuperseded() {
		t.Fatal("接管后必须稳定判定为本实例活跃")
	}
}

// TestOrphanOwnerMarkerKeepsUnknownMarkersAuthoritative 验证无法解析 pid 的标记
// 不被误判为孤儿。
//
// 守卫的历史语义是「任何不是自己的标记都代表存在更新实例」；孤儿接管只针对
// 可证明已死 pid 的那一类。解析不出 pid 的标记来源未知，必须继续保留历史语义，
// 否则任意格式的标记都会让旧实例重新跑起来，热重载去重失效。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-21 13:50:00；改动原因：新增，锁住孤儿接管的保守边界。
func TestOrphanOwnerMarkerKeepsUnknownMarkersAuthoritative(t *testing.T) {
	for _, owner := range []string{"no-pid-suffix-", "newer-instance", "trailing-", "stamp-abc"} {
		if orphanOwnerMarker(owner) {
			t.Fatalf("标记 %q 解析不出 pid，不得判为孤儿", owner)
		}
	}
	// 空标记无法代表活跃实例，必须允许接管。
	if !orphanOwnerMarker("") || !orphanOwnerMarker("   ") {
		t.Fatal("空标记必须按孤儿处理，否则实例永远无法登记")
	}
	// 存活 pid（当前进程）必须被判为非孤儿。
	if orphanOwnerMarker("stamp-" + strconv.Itoa(os.Getpid())) {
		t.Fatal("当前进程的 pid 不应被判为孤儿")
	}
}

// TestPluginInstanceSupersededToleratesMissingMarker 验证标记缺失时不阻断循环。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 20:35:00；改动原因：新增。
func TestPluginInstanceSupersededToleratesMissingMarker(t *testing.T) {
	path := backupInstanceMarker(t)

	_ = os.Remove(path)
	if pluginInstanceSuperseded() {
		t.Fatal("标记缺失时应放行，不得静默停摆")
	}
}
