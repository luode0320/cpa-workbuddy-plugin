// instance_guard_test.go 锁定「热重载后旧实例自行退出」的机制。
package main

import (
	"os"
	"testing"
)

// TestPluginInstanceSupersededDetectsNewOwner 验证新实例登记后旧实例会被判定为过期。
//
// 生产背景（2026-11-08）：宿主热重载不调用 plugin_shutdown，旧实例常驻循环
// 继续跑，探活请求随热重载次数线性放大（实测 10 分钟周期下 90 分钟被探活 96 次；
// 修复守卫位置后又实测 45 分钟内每账号被探活 59 次）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 20:35:00；改动原因：新增，锁住实例守卫语义。
func TestPluginInstanceSupersededDetectsNewOwner(t *testing.T) {
	path := pluginInstancePath()
	backup, backupErr := os.ReadFile(path)
	t.Cleanup(func() {
		if backupErr == nil {
			_ = os.WriteFile(path, backup, 0o644)
		} else {
			_ = os.Remove(path)
		}
	})

	// 1. 自己登记后不算过期。
	claimPluginInstance()
	if instanceGuardID == "" {
		t.Fatal("claimPluginInstance 未生成实例标识")
	}
	if pluginInstanceSuperseded() {
		t.Fatal("刚登记的实例不应被判为过期")
	}

	// 2. 模拟新实例覆盖所有权标记。
	if err := os.WriteFile(path, []byte("newer-instance"), 0o644); err != nil {
		t.Fatalf("写入新实例标记失败: %v", err)
	}
	if !pluginInstanceSuperseded() {
		t.Fatal("所有权被覆盖后必须判为过期，否则旧循环不会退出")
	}
}

// TestPluginInstanceSupersededToleratesMissingMarker 验证标记缺失时不阻断循环。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-08 20:35:00；改动原因：新增。
func TestPluginInstanceSupersededToleratesMissingMarker(t *testing.T) {
	path := pluginInstancePath()
	backup, backupErr := os.ReadFile(path)
	t.Cleanup(func() {
		if backupErr == nil {
			_ = os.WriteFile(path, backup, 0o644)
		} else {
			_ = os.Remove(path)
		}
	})

	_ = os.Remove(path)
	if pluginInstanceSuperseded() {
		t.Fatal("标记缺失时应放行，不得静默停摆")
	}
}
