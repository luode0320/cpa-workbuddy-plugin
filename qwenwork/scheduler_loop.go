// scheduler_loop.go 驱动 token 保活的定时唤醒。
//
// QwenWork 没有签到概念（实测 /sash/api/v1/me/daily-check-in/status 返回 404），
// 因此原 qoderwork 的 checkin 调度被移除，这里只保留保活与生命周期两个定时动作。
package main

import (
	"math/rand/v2"
	"sync"
	"time"
)

var (
	schedulerStop chan struct{}
	schedulerMu   sync.Mutex
)

// ensureScheduler 幂等启动后台定时循环。
//
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：启动时登记实例所有权，供热重载旧实例退场。
func ensureScheduler() {
	schedulerMu.Lock()
	defer schedulerMu.Unlock()
	if schedulerStop != nil {
		return // already running
	}
	claimPluginInstance()
	schedulerStop = make(chan struct{})
	go schedulerLoop(schedulerStop)
}

// Note: there is deliberately no stopScheduler. The plugin shutdown export is
// a no-op (see cliproxyPluginShutdown) because the host invokes it during its
// own runtime teardown, where touching Go sync primitives from the plugin's
// c-shared runtime caused SIGSEGV on every restart.

// nextKeepaliveTime 返回下一个保活时刻（取 keepaliveHours 中最近的一个）。
//
// [参数] now 当前时间。
// [返回] 下一个触发时间点。
// 最近修改时间：2026-10-18 00:28:00；改动原因：签到调度删除后重建保活循环。
func nextKeepaliveTime(now time.Time) time.Time {
	var earliest time.Time
	for _, h := range keepaliveHours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour) // slot already passed today → tomorrow
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// schedulerJitterMax 是槽位触发时刻的随机抖动上限。
//
// 各插件的槽位都对齐本地整点（00/04/08/12/16/20），所有插件会在同一秒
// 一起醒来，账号规模上来后形成整点尖峰。给每个插件、每一轮叠加一段
// 不大于该值的随机偏移，把尖峰摊平到数分钟窗口内。
//
// 上界取 5 分钟：远小于 4 小时槽位间隔（不会跨槽串位），也远小于
// shouldRunKeepaliveNow 的 1 小时窗口（抖动后仍落在同一窗口内）。
const schedulerJitterMax = 5 * time.Minute

// schedulerJitterFn 是可替换的抖动源，单测注入确定值；生产用 [0, max) 均匀分布。
var schedulerJitterFn = func() time.Duration {
	return time.Duration(rand.N(int64(schedulerJitterMax)))
}

// nextKeepaliveFireTime 在纯函数 nextKeepaliveTime 的结果上叠加抖动。
//
// nextKeepaliveTime 保持无副作用，供既有调度断言继续验证绝对槽位；抖动只在
// 真正创建定时器时施加，避免把随机性带进可测的排期计算。
//
// [参数] now：当前时间。
// [返回] 实际唤醒时刻，严格晚于 now。
func nextKeepaliveFireTime(now time.Time) time.Time {
	next := nextKeepaliveTime(now)
	jitter := schedulerJitterFn()
	if jitter > 0 {
		next = next.Add(jitter)
	}
	// 抖动不得把唤醒时刻推到 now 之前（极小概率出现在槽位前一瞬间抖动越界）。
	if !next.After(now) {
		return now.Add(time.Second)
	}
	return next
}

// schedulerLoop 到达保活时刻后执行 token 刷新，并顺带跑一次生命周期对账。
//
// 每轮开头先过实例守卫：宿主热重载不卸载旧 .so，旧实例的循环必须自行退场，
// 否则多个实例会并发刷新同一 refresh token，把健康账号刷成 session-dead。
//
// [参数] stop 关闭信号通道，收到后停止定时器并退出循环。
// [返回] 无（阻塞循环，随 stop 关闭返回）。
// 最近修改时间：2026-11-27；改动原因：新增实例守卫闸门。
func schedulerLoop(stop chan struct{}) {
	for {
		if retireIfSuperseded("token-keepalive") {
			return
		}
		// 叠加随机抖动：各插件槽位同刻对齐时会形成整点尖峰，抖动把全机
		// 唤醒摊平到数分钟窗口内。
		next := nextKeepaliveFireTime(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			if shouldRunKeepaliveNow(time.Now()) {
				runTokenKeepalive()
			}
			if lifecycleEnabled() {
				reconcileAllAccounts(true)
			}
		}
	}
}
