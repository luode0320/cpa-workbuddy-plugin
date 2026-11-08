// instance_guard.go 让热重载后的旧插件实例自行退出常驻循环。
//
// 宿主热重载只把旧 .so 记入 retired 列表、不调用 plugin_shutdown，旧实例的
// goroutine 会一直跑下去；常驻循环因此随热重载次数累积。2026-11-08 生产实测：
// 探活周期 10 分钟，但每账号 90 分钟被探活 96 次（约 10 个实例并行），
// 在账号配额已耗尽时仍在持续打上游。
package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// instanceGuardOnce 保证本进程只登记一次。
	instanceGuardOnce sync.Once
	// instanceGuardID 是本实例的唯一标识。
	instanceGuardID string
)

// pluginInstancePath 返回实例所有权标记文件路径。
// [返回] 标记文件的绝对路径。
// 最近修改时间：2026-11-08 20:30:00；改动原因：新增，收敛热重载遗留的循环实例。
func pluginInstancePath() string {
	return filepath.Join(os.TempDir(), "cpa-traework-provider-instance")
}

// claimPluginInstance 把本实例登记为当前活跃实例。
// [返回] 无。
// 最近修改时间：2026-11-08 20:30:00；改动原因：新增。
func claimPluginInstance() {
	instanceGuardOnce.Do(func() {
		instanceGuardID = strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(os.Getpid())
		_ = os.WriteFile(pluginInstancePath(), []byte(instanceGuardID), 0o644)
	})
}

// pluginInstanceSuperseded 报告本实例是否已被更新实例取代。
// [返回] true 表示应停止常驻循环。
// 最近修改时间：2026-11-08 20:30:00；改动原因：新增。
func pluginInstanceSuperseded() bool {
	claimPluginInstance()
	raw, err := os.ReadFile(pluginInstancePath())
	if err != nil {
		// 读不到标记时不阻断循环，宁可多跑一轮也不静默停摆。
		return false
	}
	return strings.TrimSpace(string(raw)) != instanceGuardID
}
