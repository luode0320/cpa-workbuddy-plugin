// instance_guard.go 让热重载后的旧插件实例自行退出常驻循环。
//
// 宿主热重载只把旧 .so 记入 retired 列表、不调用 plugin_shutdown，旧实例的
// goroutine 会一直跑下去；常驻循环因此随热重载次数累积。2026-11-08 生产实测：
// 探活周期 10 分钟，但每账号 90 分钟被探活 96 次（约 10 个实例并行），
// 在账号配额已耗尽时仍在持续打上游。
package main

import (
	"log"
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
//
// 跨实例去重的唯一手段：宿主热重载不调用 plugin_shutdown，旧实例的 goroutine
// 不会自行结束；进程内 mutex 对旧实例无效（旧实例有自己的副本），因此必须
// 借助进程外可见的标记文件。
// [返回] true 表示应停止常驻循环。
// 最近修改时间：2026-11-08 20:30:00；改动原因：新增。
// 最近修改时间：2026-11-08 22:10:00；改动原因：补充跨实例语义说明。
func pluginInstanceSuperseded() bool {
	claimPluginInstance()
	raw, err := os.ReadFile(pluginInstancePath())
	if err != nil {
		// 读不到标记时不阻断循环，宁可多跑一轮也不静默停摆。
		return false
	}
	return strings.TrimSpace(string(raw)) != instanceGuardID
}

// retireIfSuperseded 是常驻循环的标准实例守卫闸门：已被更新实例取代时打一条
// 带 loop 名的停止日志并返回 true，调用方据此直接 return 退出循环。
//
// 为什么必须由每个常驻循环自己调用：宿主热重载只把旧 .so 记入 retired 列表、
// 从不调用 plugin_shutdown，旧实例的 goroutine 会一直跑到进程结束。0.5.41 →
// 0.5.42 → 0.5.44 三次热重载后，生产进程内同时 mmap 着三个 .so；旧实例的
// watchdog 仍按自己编译期常量低积分禁用线（0.5.41 = 100）每 10 分钟重写
// 「disabled:true + low_credit_disable_below:100」标记，把 remain=97 的账号
// 也禁掉，账户池被反复削到个位数，并发堆到少数账号上，首字延迟尾部同步抬升
// （2026-11-11 生产实证）。
//
// 幂等且廉价：一次 os.ReadFile（标记文件在 tmpfs，几十字节），每轮循环只调用
// 一次，相对各自的 1~10 分钟周期可忽略。
// [参数] loop：循环名，仅用于停止日志定位。
// [返回] true 表示本实例已被取代，调用方必须立即 return。
// 最近修改时间：2026-11-11 01:30:00；改动原因：新增，把实例守卫收敛成统一闸门。
func retireIfSuperseded(loop string) bool {
	if !pluginInstanceSuperseded() {
		return false
	}
	log.Printf("[traework] %s stopped: superseded by newer instance", loop)
	return true
}
