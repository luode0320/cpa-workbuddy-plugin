// 本文件为插件提供「自身内存自限」与「启动错峰」两项能力。
//
// 为什么需要：每个 CPA 插件以 -buildmode=c-shared 编译，各自链接一份完整的 Go
// runtime。宿主注入的 GOMEMLIMIT 是环境变量，会被每个 runtime 独立读取，因此它
// 不是「整进程总预算」，而是「每个 runtime 各自的软上限」；容器内存上限才是唯一
// 硬约束。启动与批量高峰时多个 runtime 同时扩张，整进程就会被 OOM 杀掉。这里在
// 插件内部给自身堆设一个独立软上限，超限只触发本插件自己的 GC，不影响宿主与其它
// 插件；同时让后台循环错峰启动，避免所有插件同刻点火。
package main

import (
	"context"
	"math/rand"
	"os"
	"runtime/debug"
	"strconv"
	"time"
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

// startupJitterMax 是后台循环首次启动的最大随机延迟。声明为变量而非常量，
// 是为了让测试可以注入更短的延迟，避免单测被真实 0~3s 抖动拖慢。
// 最近修改时间：2026-11-27；改动原因：新增，打散多插件启动期的并发峰值。
var startupJitterMax = 3 * time.Second

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

// goAfterStartupJitter 在随机 0~startupJitterMax 延迟后启动后台循环。
// [参数] fn: 循环入口函数。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，打散多插件启动期的并发峰值。
func goAfterStartupJitter(fn func()) {
	go func() {
		if d := time.Duration(rand.Int63n(int64(startupJitterMax))); d > 0 {
			time.Sleep(d)
		}
		fn()
	}()
}

// waitStartupJitter 在当前协程内等待错峰窗口，ctx 取消时立即返回 false。
//
// 需要「停止时能立刻返回」的循环（例如配置热更新会同步等待协程退出）必须用本函数，
// 不能用 goAfterStartupJitter，否则停止方会被错峰睡眠拖住。
// [参数] ctx: 取消上下文。
// [返回] true 表示错峰等待完成可以继续；false 表示已被取消。
// 最近修改时间：2026-11-27；改动原因：新增，给可取消循环提供可中断的错峰等待。
func waitStartupJitter(ctx context.Context) bool {
	d := time.Duration(rand.Int63n(int64(startupJitterMax)))
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
