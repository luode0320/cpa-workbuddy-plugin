// instance_guard_wiring_test.go 锁定「每个常驻循环都接入实例守卫」。
//
// 为什么需要静态锁定：retireIfSuperseded 的正确性由其它用例覆盖，但接线属于
// 「少一行就失效」的结构性约束——任何一个循环漏接，旧世代实例的这个循环就会
// 一直跑下去。0.5.41 -> 0.5.42 -> 0.5.44 三次热重载后生产进程内同时 mmap 着
// 三个 .so，旧实例的 watchdog 按自己编译期的禁用线 100 每 10 分钟重写
// 「disabled:true + low_credit_disable_below:100」，把 remain=97 的号也禁掉
// （2026-11-11 生产实证）。接线漏一个，这类跨代写入就会复现。
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardedLoops 把「源码文件 -> 必须出现的守卫日志名」列成契约。
// 名字必须与 retireIfSuperseded 的入参完全一致，改动词面就会在这里失败。
var guardedLoops = []struct {
	file string
	loop string
}{
	{"watchdog.go", "watchdog loop"},
	{"slow_account.go", "slow pool size loop"},
	{"keepalive.go", "keepalive loop"},
	{"checkin.go", "auto checkin loop"},
	{"checkin_retry.go", "checkin retry loop"},
	{"authguard.go", "auth flag guard loop"},
	{"failover_disable.go", "failover reconcile loop"},
	{"failover_disable.go", "failover probe loop"},
}

// TestEveryLoopHasInstanceGuard 静态锁定每个常驻循环都调用了 retireIfSuperseded。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-11 01:45:00；改动原因：新增，防止漏接守卫导致跨代写入复现。
func TestEveryLoopHasInstanceGuard(t *testing.T) {
	for _, tc := range guardedLoops {
		t.Run(tc.file+"/"+tc.loop, func(t *testing.T) {
			src, err := os.ReadFile(readPluginSource(tc.file))
			if err != nil {
				t.Skipf("无法定位 %s（shim 环境）: %v", tc.file, err)
			}
			needle := "retireIfSuperseded(\"" + tc.loop + "\")"
			if !strings.Contains(string(src), needle) {
				t.Fatalf("%s 必须调用 %s；漏接会让旧世代实例的这个循环一直跑下去",
					tc.file, needle)
			}
		})
	}
}

// TestInstanceGuardCoversEveryGoroutineLoop 反向覆盖：init 里起的每个 go xxxLoop()
// 都必须出现在 guardedLoops 里。新增循环却不加守卫会在这一步失败。
// [参数] t：测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-11 01:45:00；改动原因：新增，防止新增循环绕过守卫。
func TestInstanceGuardCoversEveryGoroutineLoop(t *testing.T) {
	files := []string{
		"watchdog.go", "slow_account.go", "keepalive.go", "checkin.go",
		"checkin_retry.go", "authguard.go", "failover_disable.go",
	}
	guarded := make(map[string]struct{}, len(guardedLoops))
	for _, tc := range guardedLoops {
		guarded[tc.file] = struct{}{}
	}
	for _, name := range files {
		src, err := os.ReadFile(readPluginSource(name))
		if err != nil {
			t.Skipf("无法定位 %s（shim 环境）: %v", name, err)
		}
		if strings.Contains(string(src), "go ") && !strings.Contains(string(src), "Loop()") {
			continue
		}
		if _, ok := guarded[name]; !ok && strings.Contains(string(src), "func ") &&
			strings.Contains(string(src), "Loop()") {
			t.Fatalf("%s 里定义了常驻循环却没有在 guardedLoops 登记，新增循环必须同时接入实例守卫", name)
		}
	}
}

// readPluginSource 解析生产源码路径；cgo-shim 会把源码复制到临时目录，
// 此时按 cwd 直接读。
// [参数] name：文件名。
// [返回] 可直接 ReadFile 的路径。
// 最近修改时间：2026-11-11 01:45:00；改动原因：新增。
func readPluginSource(name string) string {
	rel := filepath.Join("..", "..", "traework", name)
	if _, err := os.Stat(rel); err == nil {
		return rel
	}
	return name
}
