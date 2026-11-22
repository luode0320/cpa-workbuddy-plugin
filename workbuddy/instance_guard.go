// instance_guard.go 让热重载后的旧插件实例自行退出常驻循环。
//
// 宿主热重载只把旧 .so 记入 retired 列表、不调用 plugin_shutdown，旧实例的
// goroutine 会一直跑下去；常驻循环因此随热重载次数累积。2026-11-08 生产实测：
// 探活周期 10 分钟，但单账号 60 分钟内被探活 54 次（多个实例并行）。
// 2026-11-22 生产实测：每小时固定跑 3 轮积分扫描（单实例应 1 轮），快照被多代
// 实例轮流整文件覆盖，落盘 rename 因共用固定临时名互相删除而失败。
//
// 与 traework 的同源实现一致，标记文件名按插件区分，避免两个插件互相顶掉。
//
// 最近修改时间：2026-11-22 17:40:00；改动原因：补孤儿标记接管与统一退出闸门，
// 消除「死进程标记压死常驻循环」与「旧实例永不停手」两条缺陷。
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
// 最近修改时间：2026-11-08 22:35:00；改动原因：新增，收敛热重载遗留的循环实例。
func pluginInstancePath() string {
	return filepath.Join(os.TempDir(), "cpa-workbuddy-provider-instance")
}

// claimPluginInstance 把本实例登记为当前活跃实例。
// [返回] 无。
// 最近修改时间：2026-11-22 17:40:00；改动原因：登记前先判孤儿标记，不让死进程的标记压死长驻实例。
func claimPluginInstance() {
	instanceGuardOnce.Do(func() {
		instanceGuardID = strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(os.Getpid())
		if owner, err := os.ReadFile(pluginInstancePath()); err == nil {
			// 标记属于一个已经退出的进程时说明上一任实例已死，直接接管：
			// 否则本实例会把自己登记进标记，却永远被判为「被取代」。
			if orphanOwnerMarker(string(owner)) {
				log.Printf("[workbuddy] taking over orphan instance marker (previous %s no longer alive)", strings.TrimSpace(string(owner)))
			}
		}
		_ = os.WriteFile(pluginInstancePath(), []byte(instanceGuardID), 0o644)
	})
}

// orphanOwnerMarker 判断标记内容是否属于「已退出进程留下的孤儿标记」。
//
// 标记格式为「<纳秒36进制>-<pid>」，见 claimPluginInstance。判定刻意保守：
// 只有能明确解出 pid、且该进程确实已不存在时才认定为孤儿；内容存在但解析
// 不出 pid 的标记一律按「可能是更新实例」处理，宁可多停一个循环，也不让
// 未知来源的标记失去历史守卫语义。
// [参数] owner：标记文件的原始内容。
// [返回] true 表示标记所有者进程已不存在，本实例应当接管。
// 最近修改时间：2026-11-22 17:40:00；改动原因：新增，识别容器内短命进程留下的标记。
func orphanOwnerMarker(owner string) bool {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		// 空/损坏标记不可能代表活跃实例，直接接管。
		return true
	}
	idx := strings.LastIndex(owner, "-")
	if idx < 0 || idx == len(owner)-1 {
		return false
	}
	pid, err := strconv.Atoi(owner[idx+1:])
	if err != nil || pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		// 同一进程内的第二次登记不是孤儿。
		return false
	}
	return !processAlive(pid)
}

// pluginInstanceSuperseded 报告本实例是否已被更新实例取代。
//
// 跨实例去重的唯一手段：宿主热重载不调用 plugin_shutdown，旧实例的 goroutine
// 不会自行结束；进程内 mutex 对旧实例无效（旧实例有自己的副本），因此必须
// 借助进程外可见的标记文件。
// [返回] true 表示应停止常驻循环。
// 最近修改时间：2026-11-22 17:40:00；改动原因：标记所有者进程已退出时不判过期，避免孤儿标记永久压死常驻循环。
func pluginInstanceSuperseded() bool {
	claimPluginInstance()
	raw, err := os.ReadFile(pluginInstancePath())
	if err != nil {
		// 读不到标记时不阻断循环，宁可多跑一轮也不静默停摆。
		return false
	}
	owner := strings.TrimSpace(string(raw))
	if owner == instanceGuardID {
		return false
	}
	if orphanOwnerMarker(owner) {
		// 标记来自死进程：既不是「更新的实例」，也不该压死本实例。
		// 直接接管标记，让后续判断回到正常轨道。
		_ = os.WriteFile(pluginInstancePath(), []byte(instanceGuardID), 0o644)
		return false
	}
	return true
}

// retireIfSuperseded 是常驻循环的统一实例守卫闸门：已被更新实例取代时打一条
// 带 loop 名的停止日志并返回 true，调用方据此直接 return 退出循环。
//
// 为什么必须由每个常驻循环自己调用：宿主热重载只把旧 .so 记入 retired 列表、
// 从不调用 plugin_shutdown，旧实例的 goroutine 会一直跑到进程结束。2026-11-22
// 生产实测：进程内同时 mmap 着 0.21.27 / 0.21.28 / 0.21.29 三代 .so，每小时
// 固定跑 3 轮积分扫描，快照被三代实例轮流覆盖，42 个账号的 fetched_at 落后
// 文件 updated_at 15 分钟。
//
// 幂等且廉价：一次 os.ReadFile（标记文件在 tmpfs，几十字节），每轮只调用一次，
// 相对各自的 1 小时 / 10 分钟周期可忽略。
// [参数] loop：循环名，仅用于停止日志定位。
// [返回] true 表示本实例已被取代，调用方必须立即 return。
// 最近修改时间：2026-11-22 17:40:00；改动原因：新增，把实例守卫收敛成统一闸门。
func retireIfSuperseded(loop string) bool {
	if !pluginInstanceSuperseded() {
		return false
	}
	log.Printf("[workbuddy] %s stopped: superseded by newer instance", loop)
	return true
}
