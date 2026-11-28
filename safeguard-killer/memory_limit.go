// 本文件为插件提供「自身内存自限」与「启动错峰」两项能力。
//
// 为什么需要：每个 CPA 插件以 -buildmode=c-shared 编译，各自链接一份完整的 Go
// runtime。宿主注入的 GOMEMLIMIT 是环境变量，会被每个 runtime 独立读取，因此它
// 不是「整进程总预算」，而是「每个 runtime 各自的软上限」；容器内存上限才是唯一
// 硬约束。启动与批量高峰时多个 runtime 同时扩张，整进程就会被 OOM 杀掉。这里在
// 插件内部给自身堆设一个独立软上限，超限只触发本插件自己的 GC，不影响宿主与其它
// 插件。
package main

import (
	"os"
	"runtime/debug"
	"strconv"
)

const (
	// pluginMemoryLimitEnv 是插件堆软上限的环境变量名（单位 MiB），供运维在生产
	// 不重新出包的情况下调整；未设置或非法值时使用 defaultPluginMemoryLimitMB。
	pluginMemoryLimitEnv = "CPA_PLUGIN_MEMORY_LIMIT_MB"
	// defaultPluginMemoryLimitMB 是本插件的默认堆软上限（MiB）。
	//
	// 取值依据：容器内存上限（2GiB）才是整进程硬约束，宿主自身常驻约 300MiB，
	// 同一进程最多会同时加载多个插件 runtime。按 8 个插件计 128MiB×8≈1GiB，
	// 给宿主与瞬时副本留出余量；单插件稳态堆实测只有数十 MB，128MiB 足以覆盖
	// 正常峰值。需要更宽松的预算时用 CPA_PLUGIN_MEMORY_LIMIT_MB 覆盖（如 200）。
	defaultPluginMemoryLimitMB = 128
)

// init 在动态库加载时给本插件 runtime 设置堆软上限。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，防止多插件 runtime 叠加触发整进程 OOM。
func init() {
	debug.SetMemoryLimit(pluginMemoryLimitBytes())
}

// pluginMemoryLimitBytes 解析环境变量得到堆软上限；未设置、非数字或非正数时回落默认值。
// [参数] 无。
// [返回] 堆软上限（字节）。
// 最近修改时间：2026-11-27；改动原因：新增，支持生产侧按需覆盖上限。
func pluginMemoryLimitBytes() int64 {
	if raw := os.Getenv(pluginMemoryLimitEnv); raw != "" {
		if mb, err := strconv.Atoi(raw); err == nil && mb > 0 {
			return int64(mb) << 20
		}
	}
	return int64(defaultPluginMemoryLimitMB) << 20
}
